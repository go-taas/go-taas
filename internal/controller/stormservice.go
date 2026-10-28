// AIBrix StormService desired state.
//
// Every inference service is realized by exactly one StormService: the
// object is the platform's unit of serving, and its role set is what makes
// the three supported topologies differ:
//
//	single       one role ("worker"), one pod per replica
//	distributed  one role ("worker") whose pod group is the replicas'
//	             tensor/pipeline shards, gang scheduled together
//	pd           a "prefill" and a "decode" role, each with its own pod
//	             group and replica count
//
// StormService semantics this file relies on (verified against the CRD and
// the AIBrix controller):
//
//   - spec.replicas is the number of role sets ("storm replicas")
//   - each role runs role.replicas pod groups of role.podGroupSize pods
//   - pods inherit the role template's labels, which is how the Service,
//     the ModelClaim pod selector and the status follower find them

package controller

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// stormServiceGVR is the AIBrix StormService resource. There is no vendored
// AIBrix client, so the controller writes it through the dynamic client.
var stormServiceGVR = schema.GroupVersionResource{
	Group: "orchestration.aibrix.ai", Version: "v1alpha1", Resource: "stormservices",
}

// Role names the storm carries. "worker" covers single-node and distributed
// serving; PD disaggregation splits the work into prefill and decode roles.
const (
	stormRoleWorker  = "worker"
	stormRolePrefill = "prefill"
	stormRoleDecode  = "decode"
)

// roleLabel is the label that names a pod's role inside the storm, so an
// operator (and the log follower) can tell prefill from decode.
const roleLabel = "go-taas.com/role"

// Serving topology names as carried in a change event. They mirror
// services/infer (the control plane decides the value; the controller only
// interprets it).
const (
	// topologyMultiNode spreads one replica across several pods, one per
	// machine. topologyDistributed is its pre-rename name and keeps working
	// for an older control plane.
	topologyMultiNode   = "multinode"
	topologyDistributed = "distributed"
	topologyPD          = "pd"
)

// isMultiNode reports whether the event asks for the multi-machine layout.
func isMultiNode(topology string) bool {
	return topology == topologyMultiNode || topology == topologyDistributed
}

// pdKVConnector is the vLLM KV-transfer connector used when the operator did
// not supply a `kv-transfer-config` of their own. AIBrix's PD examples run
// vLLM's PyNccl connector, so it is the default instead of leaving the
// engine unconfigured (which makes prefill and decode never exchange KV).
const pdKVConnector = "PyNcclConnector"

// stormRole is one role of the storm's role set.
type stormRole struct {
	// Name is the role name (worker, prefill, decode).
	Name string
	// Replicas is how many pod groups of this role one storm replica runs.
	Replicas int32
	// PodGroupSize is how many pods one pod group has: one pod per worker.
	// A single-node replica is one pod; a multi-machine replica is one pod
	// per tensor/pipeline shard, each on its own machine.
	PodGroupSize int32
	// EngineRole is the prefill/decode role passed to the engine; empty for
	// the worker role.
	EngineRole string
}

// workerPods is the number of pods (and therefore machines) one replica spans.
// A single-node replica is one pod; a multi-machine replica is one pod per
// tensor/pipeline shard.
func workerPods(evt changeEvent, topology string) int32 {
	if !isMultiNode(topology) && topology != topologyPD {
		return 1
	}
	tp := evt.TensorParallelSize
	if tp < 1 {
		tp = 1
	}
	pp := evt.PipelineParallelSize
	if pp < 1 {
		pp = 1
	}
	return clampReplicas(tp * pp)
}

// stormRoles turns the requested topology into the storm's role set.
//
// Every worker is its own pod, so a multi-machine or PD replica spans
// podGroupSize pods and the pods rendezvous through the headless Service a
// stateful storm gets (see buildStormService/engineCommand).
func stormRoles(evt changeEvent) []stormRole {
	switch evt.Topology {
	case topologyMultiNode, topologyDistributed:
		return []stormRole{{Name: stormRoleWorker, Replicas: 1, PodGroupSize: workerPods(evt, evt.Topology)}}
	case topologyPD:
		prefill := clampReplicas(evt.PrefillReplicas)
		if prefill < 1 {
			prefill = 1
		}
		decode := clampReplicas(evt.DecodeReplicas)
		if decode < 1 {
			decode = 1
		}
		podGroup := workerPods(evt, topologyPD)
		return []stormRole{
			{Name: stormRolePrefill, Replicas: prefill, PodGroupSize: podGroup, EngineRole: stormRolePrefill},
			{Name: stormRoleDecode, Replicas: decode, PodGroupSize: podGroup, EngineRole: stormRoleDecode},
		}
	default:
		return []stormRole{{Name: stormRoleWorker, Replicas: 1, PodGroupSize: 1}}
	}
}

// stormServiceName is the storm's name: the service name, which is already
// platform-unique and DNS-safe.
func stormServiceName(evt changeEvent) string { return evt.Name }

// buildStormService composes the StormService for a change event. The pods it
// creates carry the same labels and annotations as the historical Deployment,
// so the serving gateway, the ModelClaim selector and the status follower do
// not care which object owns them.
func (r *k8sReconciler) buildStormService(ctx context.Context, evt changeEvent) (*unstructured.Unstructured, error) {
	labels := resourceLabels(evt)
	modelName := servedModelName(evt)
	podLabels := mergeLabels(labels, routingLabels(modelName))
	podAnnotations := routingAnnotations(modelName, serviceName(evt.Name))

	roles := make([]any, 0, len(stormRoles(evt)))
	// A storm whose role spans more than one pod must be stateful: the
	// controller then creates a headless Service named after the storm and
	// gives every pod a stable hostname, which is what the workers use to
	// rendezvous (see engineCommand).
	stateful := false
	for _, role := range stormRoles(evt) {
		if role.PodGroupSize > 1 {
			stateful = true
		}
	}
	for _, role := range stormRoles(evt) {
		spec, roleAnnotations := r.buildPodTemplate(ctx, evt, role)
		specMap, err := podSpecToMap(spec)
		if err != nil {
			return nil, err
		}
		roleLabels := mergeLabels(podLabels, map[string]string{roleLabel: role.Name})
		roleAnnotationSet := mergeLabels(podAnnotations, roleAnnotations)
		roles = append(roles, map[string]any{
			"name":         role.Name,
			"replicas":     int64(role.Replicas),
			"podGroupSize": int64(role.PodGroupSize),
			"stateful":     stateful,
			"template": map[string]any{
				"metadata": map[string]any{
					"labels":      stringMapToAny(roleLabels),
					"annotations": stringMapToAny(roleAnnotationSet),
				},
				"spec": specMap,
			},
		})
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orchestration.aibrix.ai/v1alpha1",
		"kind":       "StormService",
		"metadata": map[string]any{
			"name":        stormServiceName(evt),
			"namespace":   r.namespace,
			"labels":      stringMapToAny(mergeLabels(labels, routingLabels(modelName))),
			"annotations": stringMapToAny(routingAnnotations(modelName, serviceName(evt.Name))),
		},
		"spec": map[string]any{
			"replicas": int64(clampReplicas(evt.Replicas)),
			"stateful": stateful,
			"selector": map[string]any{"matchLabels": stringMapToAny(labels)},
			// One storm replica is replaced at a time: a PD pair needs both
			// roles up to serve, so a rolling update never takes more than
			// one replica group out of rotation.
			"updateStrategy": map[string]any{
				"type":           "RollingUpdate",
				"maxUnavailable": int64(1),
			},
			"template": map[string]any{
				"metadata": map[string]any{
					"labels":      stringMapToAny(podLabels),
					"annotations": stringMapToAny(podAnnotations),
				},
				"spec": map[string]any{"roles": roles},
			},
		},
	}}, nil
}

// podSpecToMap converts the typed pod spec into the unstructured shape the
// StormService role template expects. The conversion goes through JSON so the
// k8s types' own omitempty tags apply: the dynamic client round-trips maps,
// and an empty `resources: {}` or `securityContext: null` is exactly the kind
// of noise that confuses a CRD with defaults.
func podSpecToMap(spec corev1.PodSpec) (map[string]any, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("controller: marshal pod spec: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("controller: unmarshal pod spec: %w", err)
	}
	return out, nil
}

// applyStormService creates or updates the StormService idempotently. The
// update path re-reads the live object and reapplies the desired spec on
// conflict: the AIBrix controller mutates the object concurrently, so a stale
// resourceVersion would otherwise fail the write.
func applyStormService(ctx context.Context, dyn dynamic.Interface, storm *unstructured.Unstructured) error {
	if dyn == nil {
		return fmt.Errorf("controller: dynamic client unavailable")
	}
	namespace := storm.GetNamespace()
	name := storm.GetName()
	const maxAttempts = 5
	for attempt := 0; ; attempt++ {
		existing, err := dyn.Resource(stormServiceGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = dyn.Resource(stormServiceGVR).Namespace(namespace).Create(ctx, storm, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		storm.SetResourceVersion(existing.GetResourceVersion())
		_, err = dyn.Resource(stormServiceGVR).Namespace(namespace).Update(ctx, storm, metav1.UpdateOptions{})
		if err == nil {
			return nil
		}
		if apierrors.IsConflict(err) && attempt < maxAttempts-1 {
			continue
		}
		return err
	}
}

// deleteStormService removes the StormService, ignoring not-found.
func deleteStormService(ctx context.Context, dyn dynamic.Interface, namespace, name string) error {
	if dyn == nil {
		return nil
	}
	err := dyn.Resource(stormServiceGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// pinStormServiceReplicas sets the StormService's replica count when
// autoscaling is off, so a manual scale is not fought by a stale autoscaler.
func (r *k8sReconciler) pinStormServiceReplicas(ctx context.Context, evt changeEvent) error {
	if r.dynamic == nil {
		return nil
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"replicas": clampReplicas(evt.Replicas)}})
	if err != nil {
		return err
	}
	_, err = r.dynamic.Resource(stormServiceGVR).Namespace(r.namespace).
		Patch(ctx, stormServiceName(evt), "application/merge-patch+json", patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
