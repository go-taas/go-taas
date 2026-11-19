// Package controller reconciles the platform's Kubernetes resources.
//
// Every inference service is realized by exactly one KServe
// LLMInferenceService (serving.kserve.io/v1alpha2). KServe owns the workload,
// the Service, the inference pool with its endpoint picker and the route that
// publishes the model on the platform's inference Gateway; the controller only
// overlays the parts the platform owns.
//
// The role set is what makes the three supported topologies differ:
//
//	single       one pod per replica, one engine port
//	multinode    one leader pod plus its workers per replica, one pod per
//	             tensor/pipeline shard, gang scheduled together
//	pd           a prefill and a decode LLMInferenceService, each with its
//	             own replica count and its own role in the KV exchange
//
// The model server container is always the preset's "main" container: KServe
// templates the pod (image, ports, probes, the PD routing sidecar) and this
// file only overlays the parts the platform owns - the engine command, the
// weights mount, the placement constraints and the environment.
package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/go-taas/go-taas/pkg/logger"
)

// llmSVCGVR is the KServe LLMInferenceService resource. There is no vendored
// KServe client, so the controller writes it through the dynamic client.
var llmSVCGVR = schema.GroupVersionResource{
	Group: "serving.kserve.io", Version: "v1alpha2", Resource: "llminferenceservices",
}

// Role names a service's pod set. "worker" covers single-node and multi-node
// serving; prefill/decode disaggregation splits the work into two roles.
const (
	serveRoleWorker  = "worker"
	serveRolePrefill = "prefill"
	serveRoleDecode  = "decode"
)

// mainContainerName is the name a KServe preset gives the model server
// container. The platform's engine container is renamed to it so the preset
// and the overlay describe one container instead of two.
const mainContainerName = "main"

// kserveWorkloadSuffix is the suffix KServe appends to the workload Service it
// creates for an LLMInferenceService. The platform's own service name is the
// LLMInferenceService's name, so a caller inside the cluster reaches the model
// through this Service, not through a Service named after the service.
const kserveWorkloadSuffix = "-kserve-workload-svc"

// decodeEnginePort is the port a decode server listens on when the replica is
// disaggregated: KServe's PD decode preset puts a routing sidecar on enginePort
// in front of the model server, so the engine must listen here instead.
const decodeEnginePort = 8001

// pdKVConnector is the vLLM KV-transfer connector used when the operator did
// not supply a `kv-transfer-config` of their own. It has to be the connector
// both sides of a disaggregated pair speak: the PD routing sidecar KServe runs
// in front of the model server speaks NIXL v2, so another connector would leave
// prefill and decode never exchanging KV.
const pdKVConnector = "NixlConnector"

// weightURIScheme is the storage scheme that points the model at the platform's
// shared weights claim. The claim is mounted by this controller, so the URI is
// only what KServe records for the model; it never fetches it.
const weightURIScheme = "pvc://"

// Serving topology names as carried in a change event. They mirror
// services/infer (the control plane decides the value; the controller only
// interprets it).
const (
	// topologyMultiNode spreads one replica across several machines, one pod
	// per machine. topologyDistributed is its pre-rename name and keeps
	// working for an older control plane.
	topologyMultiNode   = "multinode"
	topologyDistributed = "distributed"
	topologyPD          = "pd"
)

// isMultiNode reports whether the event asks for the multi-machine layout.
func isMultiNode(topology string) bool {
	return topology == topologyMultiNode || topology == topologyDistributed
}

// serveRole is one pod set of the service: the worker of a single-node or
// multi-node replica, or one side of a disaggregated pair.
type serveRole struct {
	// Name is the role name (worker, prefill or decode).
	Name string
	// Replicas is how many replicas of this role one service runs.
	Replicas int32
	// PodGroupSize is how many pods one replica of this role spans: one pod
	// per machine.
	PodGroupSize int32
	// EngineRole is the prefill/decode role passed to the engine; empty for
	// the worker role.
	EngineRole string
	// Port is the model server port this role's engine listens on.
	Port int32
}

// rolePort is the port a role's engine listens on.
func rolePort(role serveRole) int32 {
	if role.Port > 0 {
		return role.Port
	}
	return enginePort
}

// workerPods is the number of pods (and therefore machines) one replica spans.
// A single-node replica is one pod; a multi-node replica is one pod per
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
	devices := tp * pp
	// Each pod owns gpu_per_pod devices, so a replica of devices devices spans
	// devices/gpu_per_pod pods. The control plane rejects an uneven split, so
	// the division is exact; the guard covers a malformed event.
	perPod := evt.GPUPerPod
	if perPod < 1 {
		perPod = 1
	}
	pods := devices / perPod
	if pods < 1 {
		pods = 1
	}
	return clampReplicas(int(pods))
}

// serveRoles turns the requested topology into the service's role set.
//
// Every worker is its own pod, so a multi-node or PD replica spans PodGroupSize
// pods and the pods rendezvous through the leader pod KServe elects.
func serveRoles(evt changeEvent) []serveRole {
	switch evt.Topology {
	case topologyMultiNode, topologyDistributed:
		return []serveRole{{Name: serveRoleWorker, Replicas: 1, PodGroupSize: workerPods(evt, evt.Topology), Port: enginePort}}
	case topologyPD:
		return pdServeRoles(evt)
	default:
		return []serveRole{{Name: serveRoleWorker, Replicas: 1, PodGroupSize: 1, Port: enginePort}}
	}
}

// pdServeRoles splits a disaggregated replica into its prefill and decode
// halves. Both halves span the same pods: the KV cache moves between the two
// halves of one replica, so both of them need the same machine layout.
func pdServeRoles(evt changeEvent) []serveRole {
	prefill := clampReplicas(evt.PrefillReplicas)
	if prefill < 1 {
		prefill = 1
	}
	decode := clampReplicas(evt.DecodeReplicas)
	if decode < 1 {
		decode = 1
	}
	pods := workerPods(evt, topologyPD)
	return []serveRole{
		{Name: serveRolePrefill, Replicas: prefill, PodGroupSize: pods, EngineRole: serveRolePrefill, Port: enginePort},
		{Name: serveRoleDecode, Replicas: decode, PodGroupSize: pods, EngineRole: serveRoleDecode, Port: decodeEnginePort},
	}
}

// llmSVCName is the object's name: the service name, which is already
// platform-unique and DNS-safe.
func llmSVCName(evt changeEvent) string { return evt.Name }

// weightModelURI is the URI KServe records for the model. The platform mounts
// the shared weights claim itself, so KServe must not fetch anything: the URI
// only has to name the claim and the model's directory inside it.
func weightModelURI(pvc, weightPath string) string {
	uri := weightURIScheme + pvc
	if path := strings.Trim(weightPath, "/"); path != "" {
		uri += "/" + path
	}
	return uri
}

// buildLLMInferenceService composes the LLMInferenceService for a change event.
//
// The pod template is the platform's engine pod: the same spec the plain
// deployment path uses, with the container renamed to the preset's "main" and
// its ports removed so the preset's own (and, for PD, the routing sidecar's)
// port declarations survive the merge.
func (r *k8sReconciler) buildLLMInferenceService(ctx context.Context, evt changeEvent) (*unstructured.Unstructured, error) {
	labels := resourceLabels(evt)
	modelName := servedModelName(evt)
	roles := serveRoles(evt)

	// The main role is the worker of a single-node or multi-node replica and
	// the decode half of a disaggregated pair; prefill is the other half.
	var main, prefill *serveRole
	for i := range roles {
		switch roles[i].Name {
		case serveRolePrefill:
			prefill = &roles[i]
		default:
			main = &roles[i]
		}
	}
	if main == nil {
		return nil, nil
	}

	spec := map[string]any{
		"replicas": int64(main.Replicas),
		// A model pod holds its accelerator for its whole life, so a rollout
		// that starts the replacement before stopping the old one cannot fit:
		// the new pod stays Pending on the device the old one still holds and
		// the rollout never finishes. Replacing in place is what makes an
		// update possible on the nodes a model actually runs on, and a
		// service that needs no interruption while it rolls out is served by
		// a second replica, not by a second pod of one.
		"rolloutStrategy": map[string]any{
			"maxSurge":       int64(0),
			"maxUnavailable": int64(1),
		},
		"model": map[string]any{
			"name": modelName,
			"uri":  weightModelURI(r.weights.PVCName, evt.Model.WeightPath),
		},
		// The platform mounts the weights itself. A storage initializer would
		// copy the whole model a second time and make the pod wait for it.
		"storageInitializer": map[string]any{"enabled": false},
		// The platform owns the data-plane route: one catch-all route on the
		// gateway hands a request to the model router, which resolves the model
		// to the workload's Service. The router block is therefore left out.
		// KServe's own route would declare an InferencePool backend, which the
		// Envoy Gateway in this cluster does not serve: the route stays
		// unaccepted, the object is reported not ready, and an unused
		// router-scheduler pod is started beside the engine.
	}
	if tmpl, err := r.servePodTemplate(ctx, evt, *main); err != nil {
		return nil, err
	} else if tmpl != nil {
		tmpl.apply(spec)
	}
	// A multi-machine replica is a leader plus its workers: KServe runs it as
	// a leader-worker set whose size is the replica's pod count.
	if main.PodGroupSize > 1 {
		if tmpl, err := r.servePodTemplate(ctx, evt, *main); err != nil {
			return nil, err
		} else if tmpl != nil {
			worker := map[string]any{}
			tmpl.apply(worker)
			spec["worker"] = worker
		}
		spec["parallelism"] = map[string]any{"data": int64(main.PodGroupSize)}
	}
	if prefill != nil {
		pTemplate, err := r.servePodTemplate(ctx, evt, *prefill)
		if err != nil {
			return nil, err
		}
		prefillSpec := map[string]any{"replicas": int64(prefill.Replicas)}
		if pTemplate != nil {
			pTemplate.apply(prefillSpec)
		}
		spec["prefill"] = prefillSpec
	}
	// Warm-aware placement: a pod is steered to a node whose local cache
	// already holds the weights, so the engine starts without copying the
	// model again. LLMInferenceService carries pod fields under
	// spec.template, not at the top level of spec.
	if template, ok := spec["template"].(map[string]any); ok {
		r.applyWarmupAffinity(ctx, template, modelName, evt.Model.Version)
	} else {
		r.applyWarmupAffinity(ctx, spec, modelName, evt.Model.Version)
	}

	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha2",
		"kind":       "LLMInferenceService",
		"metadata": map[string]any{
			"name":        llmSVCName(evt),
			"namespace":   r.namespace,
			"labels":      stringMapToAny(labels),
			"annotations": stringMapToAny(map[string]string{}),
		},
		"spec": spec,
	}}, nil
}

// servePodTemplate lays the platform's engine pod over the preset's template.
func (r *k8sReconciler) servePodTemplate(ctx context.Context, evt changeEvent, role serveRole) (*workloadTemplate, error) {
	spec, annotations := r.buildPodTemplate(ctx, evt, role)
	specMap, err := podSpecToMap(spec)
	if err != nil {
		return nil, err
	}
	stripContainerPorts(specMap)
	renameMainContainer(specMap)
	// The presets run the model server as a non-root user; the engine images
	// the platform serves run as root and the kubelet rejects the container
	// before the engine starts. The overlay opts back out, matching the plain
	// workload path, which leaves the image's own user in charge.
	setEngineRootUser(specMap)
	// The labels reach the KServe workload's pods *and* its selector, which is
	// immutable. Only the identity labels go here: they never change for a
	// service, exactly as the plain path's Deployment selector, and a label
	// whose value can change (the role, for instance) would make every later
	// update of the workload fail with "field is immutable".
	labels := resourceLabels(evt)
	if annotations == nil {
		annotations = map[string]string{}
	}
	// Volcano binds none of a gang's pods until the whole gang fits, so the
	// annotation is what makes the PodGroup effective. It is written here and
	// not in buildPodTemplate because a shared-GPU service is placed by HAMi's
	// scheduler and joins no gang.
	if r.gangScheduling() && !usesSlice(evt) {
		annotations[podGroupAnnotation] = podGroupName(evt)
		specMap["schedulerName"] = SchedulerVolcano
	}
	return &workloadTemplate{pod: specMap, labels: labels, annotations: annotations}, nil
}

// workloadTemplate is one workload's pod spec together with the metadata the
// platform stamps on it. KServe keeps the pod spec and the pod's labels and
// annotations side by side in a workload, because `template` is a bare
// PodSpec and has no place for a metadata block of its own.
type workloadTemplate struct {
	pod         map[string]any
	labels      map[string]string
	annotations map[string]string
}

// apply writes the workload into the map that holds it: the pod spec under
// `template`, and the metadata beside it, where KServe reads it from.
func (t *workloadTemplate) apply(workload map[string]any) {
	if t == nil {
		return
	}
	workload["template"] = t.pod
	if len(t.labels) > 0 {
		workload["labels"] = stringMapToAny(t.labels)
	}
	if len(t.annotations) > 0 {
		workload["annotations"] = stringMapToAny(t.annotations)
	}
}

// renameMainContainer renames the platform's engine container to the name
// the KServe preset declares.
//
// The preset templates the model server as "main" and KServe merges the
// overlay into that container by name. An overlay named anything else would
// add a second container instead of replacing the engine, and the pod would
// run both the preset's server and this one.
func renameMainContainer(spec map[string]any) {
	containers, ok := spec["containers"].([]any)
	if !ok || len(containers) == 0 {
		return
	}
	if container, ok := containers[0].(map[string]any); ok {
		container["name"] = mainContainerName
	}
}

// stripContainerPorts drops the container ports of the platform's engine
// container.
//
// The preset already declares the model server's port, and a disaggregated
// decode puts its routing sidecar on the same container port as well. A
// port on this overlay would either duplicate the preset's entry or, next to
// the sidecar, make the pod spec invalid: the port the engine listens on is
// the preset's, and the platform must not declare it twice.
// setEngineRootUser lets the engine container start as the image's own user.
//
// The presets declare the model server non-root, and every engine image the
// platform ships runs as root: the kubelet then refuses the container with
// "container has runAsNonRoot and image will run as root" and the engine never
// starts. The overlay opts out explicitly so the container runs exactly as the
// plain Deployment path runs it, where the pod spec sets no user at all.
func setEngineRootUser(spec map[string]any) {
	containers, ok := spec["containers"].([]any)
	if !ok || len(containers) == 0 {
		return
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		return
	}
	container["securityContext"] = map[string]any{
		"runAsNonRoot": false,
		"runAsUser":    int64(0),
	}
}

func stripContainerPorts(spec map[string]any) {
	containers, ok := spec["containers"].([]any)
	if !ok {
		return
	}
	for _, raw := range containers {
		container, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		delete(container, "ports")
	}
}

// podSpecToMap converts the typed pod spec into the unstructured shape the
// LLMInferenceService template expects. The conversion goes through JSON so
// the k8s types' own omitempty tags apply: the dynamic client round-trips maps,
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

// applyLLMInferenceService creates or updates the LLMInferenceService
// idempotently. The update path re-reads the live object and reapplies the
// desired spec on conflict: KServe's controllers write the object's status and
// its own defaults concurrently, so a stale resourceVersion would otherwise
// make every apply after the first one fail.
func applyLLMInferenceService(ctx context.Context, dyn dynamic.Interface, obj *unstructured.Unstructured) error {
	if dyn == nil {
		return fmt.Errorf("controller: dynamic client unavailable")
	}
	namespace := obj.GetNamespace()
	name := obj.GetName()
	const maxAttempts = 5
	for attempt := 0; ; attempt++ {
		existing, err := dyn.Resource(llmSVCGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = dyn.Resource(llmSVCGVR).Namespace(namespace).Create(ctx, obj, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		obj.SetResourceVersion(existing.GetResourceVersion())
		_, err = dyn.Resource(llmSVCGVR).Namespace(namespace).Update(ctx, obj, metav1.UpdateOptions{})
		if err == nil {
			return nil
		}
		if apierrors.IsConflict(err) && attempt < maxAttempts-1 {
			continue
		}
		return err
	}
}

// deleteLLMInferenceService removes the service, ignoring not-found.
func deleteLLMInferenceService(ctx context.Context, dyn dynamic.Interface, namespace, name string) error {
	if dyn == nil {
		return nil
	}
	err := dyn.Resource(llmSVCGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// scalingAvailableNote is the operator-visible reason a service with an
// autoscaling policy still runs at a fixed replica count.
const scalingAvailableNote = "the cluster has no KServe autoscaler (KEDA or WVA); " +
	"the service keeps its replica count"

// notePinnedReplicas records, once per service, that a requested autoscaling
// policy cannot be honored here. KServe rejects a scaling block that names no
// autoscaler, and it rejects replicas next to one, so a policy only becomes a
// scaling block when the cluster actually runs the autoscaler it names.
func notePinnedReplicas(evt changeEvent) {
	if evt.Autoscaling == nil || !evt.Autoscaling.Enabled {
		return
	}
	if _, seen := autoscalingUnsupported.LoadOrStore(evt.ServiceID, struct{}{}); seen {
		return
	}
	logger.S().Infow("controller: autoscaling requested but unavailable; keeping the fixed replica count",
		"service_id", evt.ServiceID, "name", evt.Name, "note", scalingAvailableNote)
}

// autoscalingUnsupported remembers the services already reported as unable to
// scale, so a resync does not repeat the warning for every service.
var autoscalingUnsupported sync.Map

// pinLLMInferenceReplicas sets the service's replica count when autoscaling is
// off, so a manual scale is not fought by a stale autoscaler.
func (r *k8sReconciler) pinLLMInferenceReplicas(ctx context.Context, evt changeEvent) error {
	if r.dynamic == nil {
		return nil
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"replicas": clampReplicas(evt.Replicas)}})
	if err != nil {
		return err
	}
	_, err = r.dynamic.Resource(llmSVCGVR).Namespace(r.namespace).
		Patch(ctx, llmSVCName(evt), "application/merge-patch+json", patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// stringMapToAny widens a string map into the unstructured API's value map.
func stringMapToAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
