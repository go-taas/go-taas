package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// enginePort is the port every supported engine serves on.
const enginePort = 8000

// Ownership labels every object this controller writes carries, so a sweep can
// tell a platform object from a user object that happens to share its name.
const (
	labelManagedBy = "app.kubernetes.io/managed-by"
	managedByValue = "taas-controller"
)

// servedModelName returns the name a client addresses a service's model
// by: the catalog model name, or the service name when the event carries
// none (older publishers do not set it).
func servedModelName(evt changeEvent) string {
	if name := strings.TrimSpace(evt.Model.Name); name != "" {
		return name
	}
	return evt.Name
}

// applyUnstructuredObject creates the object, or updates it in place while
// keeping the resource version the API server handed out.
func applyUnstructuredObject(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
	if dyn == nil {
		return fmt.Errorf("controller: dynamic client unavailable")
	}
	namespace, name := obj.GetNamespace(), obj.GetName()
	existing, err := dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = dyn.Resource(gvr).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	_, err = dyn.Resource(gvr).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

// deleteUnstructuredObject deletes an object by name, ignoring not-found.
func deleteUnstructuredObject(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) error {
	if dyn == nil {
		return nil
	}
	err := dyn.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// managedByController reports whether an object was written by this
// controller. Objects it owns always carry the label; a pre-existing
// user object with our deterministic name must not be reclaimed.
func managedByController(labels map[string]string) bool {
	return labels[labelManagedBy] == managedByValue
}

// ownedByOtherService reports whether an object carries a service-id label
// that belongs to a different inference service. A late delete event for a
// service whose name was reused must never tear down the replacement's
// objects, so every delete-by-name checks this first. An object without the
// label (written by an older controller) is treated as owned by the caller.
func ownedByOtherService(labels map[string]string, serviceID string) bool {
	if serviceID == "" {
		return false
	}
	owner := labels[labelServiceID]
	return owner != "" && owner != serviceID
}

// deleteUnstructuredOwnedBy deletes an object by name unless another
// inference service owns it. It ignores not-found.
func deleteUnstructuredOwnedBy(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace, name, serviceID string) error {
	if dyn == nil || name == "" {
		return nil
	}
	obj, err := dyn.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		// Not found, or the CRD is not installed: nothing to delete.
		return nil
	}
	if err != nil {
		return err
	}
	if ownedByOtherService(obj.GetLabels(), serviceID) {
		return nil
	}
	return deleteUnstructuredObject(ctx, dyn, gvr, namespace, name)
}

// reclaimTarget is one object a reused service name must not inherit.
type reclaimTarget struct {
	kind string
	name string
	// get returns the object's labels, whether it is being deleted, and
	// whether it exists at all.
	get func(context.Context) (map[string]string, bool, bool, error)
	del func(context.Context) error
}

// reclaimServiceName removes objects left behind by a previous service with
// the same name and waits (bounded) for them to finish terminating before the
// caller creates its own. Service names are platform-unique, so an object with
// this name that belongs to another service can only be stale: the old
// service's teardown is still in flight, or its event was lost. Without this
// step a new service that reuses the name would collide with a terminating
// object (or inherit one with the wrong identity) and fail to start.
//
// Only objects this controller manages are reclaimed. A user-created object
// that happens to share the name is left alone: the apply path will report
// the conflict instead of deleting data it does not own.
func (r *k8sReconciler) reclaimServiceName(ctx context.Context, evt changeEvent) error {
	targets := r.reclaimTargets(evt)
	reclaimed := make([]reclaimTarget, 0, len(targets))
	for _, target := range targets {
		labels, deleting, found, err := target.get(ctx)
		if err != nil {
			return fmt.Errorf("controller: inspect %s %s: %w", target.kind, target.name, err)
		}
		if !found {
			continue
		}
		if !managedByController(labels) && labels[labelServiceID] == "" {
			// Not ours: leave it for the apply path to report.
			continue
		}
		if !deleting && !ownedByOtherService(labels, evt.ServiceID) {
			// Ours and healthy: the apply path updates it in place.
			continue
		}
		if !deleting && r.otherOwnerActive(ctx, labels[labelServiceID]) {
			// Another service still owns this object (e.g. two services
			// serving the same model route). Never steal a live object.
			continue
		}
		if err := target.del(ctx); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("controller: reclaim %s %s: %w", target.kind, target.name, err)
		}
		reclaimed = append(reclaimed, target)
	}
	if len(reclaimed) == 0 {
		return nil
	}

	deadline := time.Now().Add(reclaimWaitTimeout)
	for _, target := range reclaimed {
		for {
			_, _, found, err := target.get(ctx)
			if err != nil {
				return fmt.Errorf("controller: wait for %s %s: %w", target.kind, target.name, err)
			}
			if !found {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("controller: %s %s is still terminating after %s; retrying",
					target.kind, target.name, reclaimWaitTimeout)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(reclaimPollInterval):
			}
		}
	}
	return nil
}

// otherOwnerActive reports whether the platform still considers the object's
// owner an active service. Without a state provider there is no way to tell,
// so the caller treats the object as reclaimable (the ownership label already
// says it is not the current service's).
func (r *k8sReconciler) otherOwnerActive(ctx context.Context, serviceID string) bool {
	if r.serviceState == nil || serviceID == "" {
		return false
	}
	state, _, found, err := r.serviceState(ctx, serviceID)
	if err != nil {
		return false
	}
	return found && state != "stopped" && state != "terminated"
}

// reclaimTargets lists every object that could block or corrupt a service
// whose name was reused. The served model route is included because it is
// named after the model, not the service, and lives in the gateway namespace.
func (r *k8sReconciler) reclaimTargets(evt changeEvent) []reclaimTarget {
	targets := []reclaimTarget{
		r.reclaimTyped("Deployment", deploymentName(evt.Name),
			func(ctx context.Context) (*metav1.ObjectMeta, error) {
				obj, err := r.clientset.AppsV1().Deployments(r.namespace).Get(ctx, deploymentName(evt.Name), metav1.GetOptions{})
				if err != nil {
					return nil, err
				}
				return &obj.ObjectMeta, nil
			},
			func(ctx context.Context) error {
				return r.deleteDeployment(ctx, deploymentName(evt.Name))
			}),
	}
	for _, name := range []string{serviceName(evt.Name), evt.Name} {
		name := name
		targets = append(targets, r.reclaimTyped("Service", name,
			func(ctx context.Context) (*metav1.ObjectMeta, error) {
				obj, err := r.clientset.CoreV1().Services(r.namespace).Get(ctx, name, metav1.GetOptions{})
				if err != nil {
					return nil, err
				}
				return &obj.ObjectMeta, nil
			},
			func(ctx context.Context) error {
				return r.deleteService(ctx, name)
			}))
	}
	targets = append(targets,
		r.reclaimDynamic("LLMInferenceService", r.namespace, llmSVCGVR, llmSVCName(evt)),
		r.reclaimDynamic("PodGroup", r.namespace, podGroupGVR, podGroupName(evt)),
		r.reclaimDynamic("ResourceClaimTemplate", r.namespace, resourceClaimTemplateGVR, rdmaClaimTemplateName(evt)),
	)
	return targets
}

// reclaimTyped adapts a typed-object getter to a reclaimTarget.
func (r *k8sReconciler) reclaimTyped(kind, name string, get func(context.Context) (*metav1.ObjectMeta, error), del func(context.Context) error) reclaimTarget {
	return reclaimTarget{
		kind: kind,
		name: name,
		get: func(ctx context.Context) (map[string]string, bool, bool, error) {
			meta, err := get(ctx)
			if apierrors.IsNotFound(err) {
				return nil, false, false, nil
			}
			if err != nil {
				return nil, false, false, err
			}
			return meta.Labels, meta.DeletionTimestamp != nil, true, nil
		},
		del: del,
	}
}

// reclaimDynamic adapts an unstructured-object getter to a reclaimTarget.
func (r *k8sReconciler) reclaimDynamic(kind, namespace string, gvr schema.GroupVersionResource, name string) reclaimTarget {
	return reclaimTarget{
		kind: kind,
		name: name,
		get: func(ctx context.Context) (map[string]string, bool, bool, error) {
			if r.dynamic == nil || name == "" {
				return nil, false, false, nil
			}
			obj, err := r.dynamic.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
				return nil, false, false, nil
			}
			if err != nil {
				return nil, false, false, err
			}
			return obj.GetLabels(), obj.GetDeletionTimestamp() != nil, true, nil
		},
		del: func(ctx context.Context) error {
			return deleteUnstructuredObject(ctx, r.dynamic, gvr, namespace, name)
		},
	}
}
