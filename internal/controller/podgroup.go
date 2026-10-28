// Volcano gang scheduling for inference pods.
//
// Every inference service is a StormService whose pods only make sense
// together: a PD pair needs both roles up to serve, and a multi-machine
// replica needs every shard before the engine can start. The default
// Kubernetes scheduler places pods one at a time, so on a full cluster it will
// happily bind half of a gang and leave the other half pending — the running
// half burns GPUs while serving nothing, and the region stays deadlocked until
// someone deletes it by hand. That is the classic reason platforms put a gang
// scheduler in front of distributed workloads.
//
// Volcano solves it with a PodGroup: pods annotated with the group's name
// belong to one gang, and Volcano binds none of them until minMember of them
// fit. The same object carries the priority, which is what Volcano preempts
// on — a high-priority group may evict a low-priority one instead of waiting
// for capacity.
//
// The grouping is one replica, not one service: minMember is the number of
// pods a single replica spans. A service with several replicas therefore never
// needs capacity for all of them at once (which would deadlock a scale-up),
// while each replica group still starts atomically.

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/go-taas/go-taas/pkg/logger"
)

// podGroupGVR is the Volcano PodGroup resource.
var podGroupGVR = schema.GroupVersionResource{
	Group: "scheduling.volcano.sh", Version: "v1beta1", Resource: "podgroups",
}

// SchedulerVolcano is the scheduler name Volcano registers. A pod whose
// schedulerName is anything else is placed by the cluster's default scheduler
// and ignores its PodGroup entirely.
const SchedulerVolcano = "volcano"

// podGroupAnnotation joins a pod to its PodGroup. Volcano's webhook reads this
// annotation on admission; a pod without it is scheduled on its own.
const podGroupAnnotation = "scheduling.k8s.io/group-name"

// podGroupName is the PodGroup of one service. It equals the storm's name, so
// an operator looking at either object finds the other.
func podGroupName(evt changeEvent) string { return evt.Name }

// podsPerReplica is how many pods Volcano must place together: the sum of the
// roles' pod group sizes (one role for single/multi-machine, two for PD).
func podsPerReplica(evt changeEvent) int32 {
	total := int32(0)
	for _, role := range stormRoles(evt) {
		if role.PodGroupSize < 1 {
			total++
			continue
		}
		total += role.PodGroupSize
	}
	if total < 1 {
		return 1
	}
	return total
}

// gangScheduling reports whether the controller should write PodGroups. It is
// on only when the configured scheduler is Volcano: a PodGroup nobody reads is
// noise, and annotating pods for a scheduler that is not there gains nothing.
func (r *k8sReconciler) gangScheduling() bool {
	return r.dynamic != nil && r.schedulerName == SchedulerVolcano
}

// priorityClassFor maps a service's priority onto a PriorityClass name. An
// unmapped priority returns "" so the pod inherits the cluster default rather
// than naming a class that may not exist.
func (r *k8sReconciler) priorityClassFor(priority string) string {
	if r.priorityClasses == nil {
		return ""
	}
	return r.priorityClasses[normalizePriority(priority)]
}

// normalizePriority reduces a stored priority to the closed set.
func normalizePriority(priority string) string {
	switch priority {
	case "high", "low":
		return priority
	default:
		return "normal"
	}
}

// buildPodGroup composes the PodGroup for a service.
//
// minResources is the sum of one replica's requests. Volcano uses it for queue
// accounting and for the preemption decision, so an understated value would
// make the gang look cheaper than it is and let it be scheduled onto a node
// set that cannot hold it.
func (r *k8sReconciler) buildPodGroup(evt changeEvent) *unstructured.Unstructured {
	minMember := podsPerReplica(evt)
	resources := r.podResources(evt)
	// Requests are what the gang reserves; limits are the accelerator
	// devices, which are also requests on every real cluster.
	requested := resources.Requests
	if len(requested) == 0 {
		requested = resources.Limits
	}
	minResources := scaleResourceList(requested, minMember)
	annotations := map[string]any{
		"go-taas.com/service-id": evt.ServiceID,
	}
	spec := map[string]any{
		"minMember":         int64(minMember),
		"priorityClassName": r.priorityClassFor(evt.Priority),
	}
	if len(minResources) > 0 {
		spec["minResources"] = minResources
	}
	// A queue is optional: without one Volcano uses the default queue, which
	// is what a single-tenant install wants.
	if r.queueName != "" {
		spec["queue"] = r.queueName
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "scheduling.volcano.sh/v1beta1",
		"kind":       "PodGroup",
		"metadata": map[string]any{
			"name":        podGroupName(evt),
			"namespace":   r.namespace,
			"annotations": annotations,
			"labels":      stringMapToAny(resourceLabels(evt)),
		},
		"spec": spec,
	}}
}

// applyPodGroup creates or updates the PodGroup idempotently.
func applyPodGroup(ctx context.Context, dyn dynamic.Interface, group *unstructured.Unstructured) error {
	if dyn == nil {
		return fmt.Errorf("controller: dynamic client unavailable")
	}
	namespace := group.GetNamespace()
	name := group.GetName()
	existing, err := dyn.Resource(podGroupGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = dyn.Resource(podGroupGVR).Namespace(namespace).Create(ctx, group, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	group.SetResourceVersion(existing.GetResourceVersion())
	_, err = dyn.Resource(podGroupGVR).Namespace(namespace).Update(ctx, group, metav1.UpdateOptions{})
	return err
}

// deletePodGroup removes a service's PodGroup, ignoring not-found. Volcano
// created no owner reference for it, so its lifecycle is the controller's.
func deletePodGroup(ctx context.Context, dyn dynamic.Interface, namespace, name string) {
	if dyn == nil || name == "" {
		return
	}
	err := dyn.Resource(podGroupGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		logger.S().Warnw("controller: delete podgroup failed", "name", name, "err", err)
	}
}

// scaleResourceList multiplies a resource list by count, for the PodGroup's
// minResources.
func scaleResourceList(resources corev1.ResourceList, count int32) map[string]any {
	if len(resources) == 0 || count < 1 {
		return nil
	}
	out := make(map[string]any, len(resources))
	for name, quantity := range resources {
		total := quantity.DeepCopy()
		for i := int32(1); i < count; i++ {
			total.Add(quantity)
		}
		out[string(name)] = total.String()
	}
	return out
}
