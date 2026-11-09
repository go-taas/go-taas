package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/go-taas/go-taas/pkg/k8s"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
)

// changeEvent mirrors the desired-state change published by the infer
// module (architecture Section 4.5.1).
type changeEvent struct {
	EventType      string `json:"event_type"`
	ServiceID      string `json:"service_id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	Model          struct {
		ModelID string `json:"model_id"`
		// Name is the catalog model name clients pass as `model`. Empty
		// falls back to the service name.
		Name       string `json:"name,omitempty"`
		Version    string `json:"version"`
		WeightPath string `json:"weight_path"`
	} `json:"model"`
	Image struct {
		ImageID   string `json:"image_id"`
		Reference string `json:"reference"`
		Engine    string `json:"engine"`
	} `json:"image"`
	Replicas        int    `json:"replicas"`
	Accelerator     string `json:"accelerator"`
	AcceleratorType string `json:"accelerator_type"`
	// Autoscaling is the effective autoscaling policy (feature #16,
	// §6.1). Nil means autoscaling is disabled.
	Autoscaling *autoscalingPolicy `json:"autoscaling,omitempty"`
	// PoolID is the vGPU slicing pool the service opted into (feature
	// #52, AD13). Empty = whole card.
	PoolID string `json:"pool_id,omitempty"`
	// SliceTemplateID is the slice template the service requested
	// (feature #52, AD13). Empty = whole card.
	SliceTemplateID string `json:"slice_template_id,omitempty"`
	// SliceMemoryMiB is the resolved slice memory in MiB (feature #52,
	// AD13). Set only for shared-GPU services.
	SliceMemoryMiB int64 `json:"slice_memory_mib,omitempty"`
	// SliceComputePct is the resolved slice compute percentage (feature
	// #52, AD13). Set only for shared-GPU services.
	SliceComputePct int64 `json:"slice_compute_pct,omitempty"`
	// EngineArgs are the serving-engine parameters, keyed by flag name
	// without the leading dashes. A value of "flag:true" is a valueless
	// switch.
	EngineArgs map[string]string `json:"engine_args,omitempty"`
	// ExtraArgs are free-form engine arguments appended after EngineArgs.
	ExtraArgs []string `json:"extra_args,omitempty"`
	// Env are the environment variables set on every engine container of the
	// service. A per-role entry in RoleEngineArgs overrides one variable for
	// that role only.
	Env map[string]string `json:"env,omitempty"`
	// Rdma is the DRA device request for every pod of the service. Nil means
	// the pods get no RDMA device.
	Rdma *rdmaRequest `json:"rdma,omitempty"`
	// RoleEngineArgs overrides EngineArgs per role, so the prefill and decode
	// roles of a PD service (or the worker role of a multi-machine service)
	// can each be tuned.
	RoleEngineArgs []roleEngineArgs `json:"role_engine_args,omitempty"`
	// Topology is the serving layout: single, multinode or pd. It decides
	// the roles of the StormService the controller writes.
	Topology string `json:"topology,omitempty"`
	// TensorParallelSize / PipelineParallelSize shape one replica's pod
	// group (1 for a single-node deploy).
	TensorParallelSize   int `json:"tensor_parallel_size,omitempty"`
	PipelineParallelSize int `json:"pipeline_parallel_size,omitempty"`
	// PrefillReplicas / DecodeReplicas are the PD role counts (0 for the
	// other topologies).
	PrefillReplicas int `json:"prefill_replicas,omitempty"`
	DecodeReplicas  int `json:"decode_replicas,omitempty"`
	// GPUPerPod / CPUMilli / MemoryMiB are the per-pod resource requests of
	// every role. GPUPerPod 0 reads as 1 (the historical shape, one device
	// per pod); the CPU and memory requests are left off the pod when zero.
	GPUPerPod int   `json:"gpu_per_pod,omitempty"`
	CPUMilli  int64 `json:"cpu_milli,omitempty"`
	MemoryMiB int64 `json:"memory_mib,omitempty"`
	// Priority is the scheduling priority ("high", "normal", "low"). It
	// selects the pod's PriorityClass and the PodGroup's priority, which is
	// what Volcano preempts on.
	Priority string `json:"priority,omitempty"`
}

// autoscalingPolicy is the effective autoscaling policy carried in a
// change event (feature #16, §6.1).
type autoscalingPolicy struct {
	Enabled           bool `json:"enabled"`
	MinReplicas       int  `json:"min_replicas"`
	MaxReplicas       int  `json:"max_replicas"`
	TargetConcurrency int  `json:"target_concurrency"`
	ScaleToZero       bool `json:"scale_to_zero"`
	CooldownSeconds   int  `json:"cooldown_seconds"`
}

// statusReport is the observed-state report published on the status
// subject (architecture Section 4.5.2).
type statusReport struct {
	ServiceID     string   `json:"service_id"`
	State         string   `json:"state"`
	Endpoints     []string `json:"endpoints"`
	FailureReason *string  `json:"failure_reason"`
	// Autoscaling is the autoscaling status block (feature #16, §6.2).
	Autoscaling *autoscalingStatus `json:"autoscaling,omitempty"`
	ReportedAt  string             `json:"reported_at"`
}

// autoscalingStatus is the autoscaling status block inside a status
// report (feature #16, §6.2).
type autoscalingStatus struct {
	State              string `json:"state"`
	CurrentReplicas    int    `json:"current_replicas"`
	DesiredReplicas    int    `json:"desired_replicas"`
	CurrentConcurrency int    `json:"current_concurrency"`
	TargetConcurrency  int    `json:"target_concurrency"`
	LastScalingEventAt string `json:"last_scaling_event_at"`
	ErrorReason        string `json:"error_reason"`
}

// k8sReconciler is the default Reconciler backed by the Kubernetes
// clients. It decodes change events, drives Deployment/Service
// resources and reports observed state on the status subject.
type k8sReconciler struct {
	clientset       kubernetes.Interface
	statusPublisher mq.Client
	endpointBaseURL string
	// namespace is the namespace the controller manages resources in.
	// Defaults to "taas-infer" when empty (backward compatible).
	namespace string
	// weights configures the shared model-weights filesystem mounted
	// into inference pods (feature: JuiceFS-backed weights).
	weights WeightsConfig
	// dynamic is the dynamic client used to write AIBrix ModelClaims and
	// PodAutoscalers (feature #47, AD5). Nil disables the AIBrix path.
	dynamic dynamic.Interface
	// scheme is the shared serving-scheme cache (feature #47, AD6). Nil
	// defaults to plain.
	scheme *SchemeCache
	// Feature #52 (AD13): the pinned HAMI contract names and the pool
	// placement policy. policyAnnotation is the HAMI placement-policy
	// annotation name; placementPolicy is the pool's scheduling hint;
	// poolSelector is the pool's node label selector.
	policyAnnotation string
	placementPolicy  string
	poolSelector     map[string]string
	// schedulerName is the pod scheduler inference pods ask for. Empty uses
	// the cluster default; "volcano" turns on gang scheduling and preemption
	// (see podgroup.go).
	schedulerName string
	// priorityClasses maps a service priority (high/normal/low) onto the
	// PriorityClass name its pods carry. A missing entry leaves the pod with
	// the cluster default class.
	priorityClasses map[string]string
	// queueName is the Volcano queue the service's PodGroup joins. Empty
	// uses Volcano's default queue.
	queueName string
	// acceleratorLabelKey is the configured node label key carrying the
	// accelerator vendor. Empty uses labelAcceleratorVendor.
	acceleratorLabelKey string
	// resolvedAccelLabelKey caches the key actually present on the
	// cluster's nodes, so the deploy path does not list nodes per event.
	accelKeyMu            sync.Mutex
	resolvedAccelLabelKey string
	// cardTypeLabelKeys caches card model -> node label key ("" = no node
	// reports that card).
	cardTypeLabelKeys map[string]string
	// gatewayNamespace is the namespace holding the AIBrix Gateway and the
	// per-model HTTPRoutes. Empty uses defaultAIBrixNamespace.
	gatewayNamespace string
	// gatewayName is the AIBrix Gateway object model routes attach to.
	// Empty uses defaultAIBrixGateway.
	gatewayName string
	// engineProber answers "does the engine at this address serve
	// traffic?". Nil uses the HTTP health probe. Tests inject a stub so
	// they never reach the network.
	engineProber func(ctx context.Context, host string, port int) bool
	// rdmaClass is the DRA DeviceClass a service gets when it requests RDMA
	// without naming one. Empty uses defaultRdmaDeviceClass.
	rdmaClass string
	// logTail reads one pod's engine log tail for the timeout hint. Nil reads
	// it from the API server; tests inject a stub.
	logTail func(ctx context.Context, podName string) (string, error)
	// serviceState reads the platform database's desired state for a
	// service. It lets a long readiness wait notice a stop/delete that is
	// queued behind this event and abort promptly, so the teardown is not
	// delayed by the readiness timeout. Nil disables the check.
	serviceState ServiceStateProvider
}

// ServiceStateProvider reports the current desired state of an inference
// service from the platform database. updatedAt is the row's last write, which
// is how a reconcile tells "the state I published" from "a newer change the
// operator just made". found is false when the row no longer exists.
type ServiceStateProvider func(ctx context.Context, serviceID string) (state string, updatedAt time.Time, found bool, err error)

// errReconcileSuperseded is returned when the platform state says the service
// was stopped or deleted while this reconcile was still waiting. The event is
// acknowledged without a status report or an error: the stop/delete event
// already queued behind it owns the teardown.
var errReconcileSuperseded = errors.New("controller: reconcile superseded by a newer change")

// serviceStateCheckInterval bounds how often a reconcile re-reads the
// platform state while waiting for an engine. Five seconds is well under the
// time it takes an operator to notice a stuck deploy, and one query per
// service per interval is negligible.
const serviceStateCheckInterval = 5 * time.Second

// WeightsConfig holds the model-weights storage settings used by the
// controller when it mounts model weights into inference pods.
type WeightsConfig struct {
	// StorageClass is the JuiceFS-backed StorageClass used to provision
	// the weights PVC. Empty falls back to the cluster default.
	StorageClass string
	// PVCName is the name of the PersistentVolumeClaim that backs the
	// shared model-weights filesystem. Defaults to "model-weights".
	PVCName string
	// MountPath is the path the weights volume is mounted at inside
	// inference pods. Defaults to "/data/weights".
	MountPath string
}

// NewK8sReconciler builds the default Kubernetes-backed reconciler.
// statusPublisher is the MQ client used to publish observed-state
// reports; endpointBaseURL composes endpoint URLs ("" = in-cluster DNS).
// scheme is the shared serving-scheme cache (feature #47); nil defaults to
// plain. The dynamic client is taken from client.Dynamic().
func NewK8sReconciler(client *k8s.Client, statusPublisher mq.Client, endpointBaseURL, namespace string, weights WeightsConfig, scheme *SchemeCache, acceleratorLabelKey ...string) Reconciler {
	key := ""
	if len(acceleratorLabelKey) > 0 {
		key = acceleratorLabelKey[0]
	}
	r := newReconcilerWithClientset(client.Clientset(), client.Dynamic(), statusPublisher, endpointBaseURL, namespace, weights, scheme)
	if concrete, ok := r.(*k8sReconciler); ok {
		concrete.acceleratorLabelKey = key
	}
	return r
}

// newReconcilerWithClientset builds a reconciler over arbitrary clientsets
// (the fake clientset in tests).
func newReconcilerWithClientset(clientset kubernetes.Interface, dyn dynamic.Interface, statusPublisher mq.Client, endpointBaseURL, namespace string, weights WeightsConfig, scheme *SchemeCache) Reconciler {
	if namespace == "" {
		namespace = "taas-infer"
	}
	if weights.PVCName == "" {
		weights.PVCName = "model-weights"
	}
	if weights.MountPath == "" {
		weights.MountPath = "/data/weights"
	}
	if scheme == nil {
		scheme = NewSchemeCache()
	}
	return &k8sReconciler{
		clientset:       clientset,
		statusPublisher: statusPublisher,
		endpointBaseURL: endpointBaseURL,
		namespace:       namespace,
		weights:         weights,
		dynamic:         dyn,
		scheme:          scheme,
	}
}

// SetVGPUConfig installs the vGPU slicing-pool HAMI contract (feature
// #52, AD13): the placement-policy annotation name, the pool's
// placement policy, and the pool's node label selector. It must be
// called before serving.
func (r *k8sReconciler) SetVGPUConfig(policyAnnotation, placementPolicy string, poolSelector map[string]string) {
	r.policyAnnotation = policyAnnotation
	r.placementPolicy = placementPolicy
	r.poolSelector = poolSelector
}

// SetRDMAConfig installs the DRA DeviceClass used when a service requests RDMA
// devices without naming one. An empty value keeps defaultRdmaDeviceClass.
// It must be called before serving.
func (r *k8sReconciler) SetRDMAConfig(deviceClass string) {
	r.rdmaClass = strings.TrimSpace(deviceClass)
}

// SetScheduling installs the pod scheduling configuration: the scheduler
// inference pods ask for, the PriorityClass names a service priority maps
// onto, and the Volcano queue its PodGroup joins. An empty scheduler keeps
// the cluster default and turns gang scheduling off.
func (r *k8sReconciler) SetScheduling(schedulerName, highClass, normalClass, lowClass, queue string) {
	r.schedulerName = strings.TrimSpace(schedulerName)
	classes := map[string]string{}
	for priority, class := range map[string]string{
		"high":   highClass,
		"normal": normalClass,
		"low":    lowClass,
	} {
		if trimmed := strings.TrimSpace(class); trimmed != "" {
			classes[priority] = trimmed
		}
	}
	if len(classes) > 0 {
		r.priorityClasses = classes
	}
	r.queueName = strings.TrimSpace(queue)
}

// SetGatewayRouting names the AIBrix Gateway that model HTTPRoutes attach
// to. The namespace is also where the routes are written, because Gateway
// API only routes to a backend in another namespace through a route in the
// Gateway's own namespace plus a ReferenceGrant. Empty values keep the
// shipped defaults.
func (r *k8sReconciler) SetGatewayRouting(namespace, gateway string) {
	r.gatewayNamespace = namespace
	r.gatewayName = gateway
}

// SetServiceStateProvider installs the desired-state reader used to abort a
// reconcile promptly when the service is stopped or deleted while it waits for
// its engine. It must be called before serving.
func (r *k8sReconciler) SetServiceStateProvider(provider ServiceStateProvider) {
	r.serviceState = provider
}

// serviceSuperseded reports whether the platform state says this change is
// obsolete: the service was stopped/deleted (or its row is gone) while the
// event was in flight. An unavailable provider fails open so a database blip
// never leaves a deploy half-applied.
func (r *k8sReconciler) serviceSuperseded(ctx context.Context, serviceID string) bool {
	_, superseded := r.serviceStateSnapshot(ctx, serviceID, time.Time{})
	return superseded
}

// serviceStateSnapshot reads the desired state once and decides whether this
// reconcile has been superseded.
//
// Three states end it. "stopped" and "terminated" mean the teardown owns the
// service now. "pending" is the infer module's state for a change that has
// been accepted but not applied, and a pending row written *after* this
// reconcile started means a newer change is queued behind it — typically the
// operator fixing the very configuration this reconcile is waiting on. Without
// that check an update is stuck behind the twenty-minute engine wait, which is
// exactly the situation an operator is trying to escape when a deploy hangs.
func (r *k8sReconciler) serviceStateSnapshot(ctx context.Context, serviceID string, started time.Time) (string, bool) {
	if r.serviceState == nil || serviceID == "" {
		return "", false
	}
	state, updatedAt, found, err := r.serviceState(ctx, serviceID)
	if err != nil {
		logger.S().Warnw("controller: read desired service state failed, continuing",
			"service_id", serviceID, "err", err)
		return "", false
	}
	if !found || state == "stopped" || state == "terminated" {
		return state, true
	}
	if state == "pending" && !started.IsZero() && updatedAt.After(started) {
		return state, true
	}
	return state, false
}

// gatewayRouteTarget returns the namespace and Gateway name model routes
// are written against, applying the shipped defaults.
func (r *k8sReconciler) gatewayRouteTarget() (string, string) {
	namespace := r.gatewayNamespace
	if namespace == "" {
		namespace = defaultAIBrixNamespace
	}
	gateway := r.gatewayName
	if gateway == "" {
		gateway = defaultAIBrixGateway
	}
	return namespace, gateway
}

// ApplyInferServiceChange implements Reconciler.
func (r *k8sReconciler) ApplyInferServiceChange(ctx context.Context, msg mq.Message) error {
	var evt changeEvent
	if err := json.Unmarshal(msg.Body, &evt); err != nil {
		logger.S().Warnw("controller: malformed change event, rejecting",
			"subject", msg.Subject, "err", err)
		return mq.Permanent(fmt.Errorf("controller: malformed change event: %w", err))
	}
	if evt.ServiceID == "" || evt.Name == "" {
		return mq.Permanent(fmt.Errorf("controller: change event missing service_id or name"))
	}

	switch evt.EventType {
	case "delete":
		return r.applyDelete(ctx, evt)
	case "stop":
		return r.applyStop(ctx, evt)
	case "start":
		return r.applyUpsert(ctx, evt)
	case "upsert":
		return r.applyUpsert(ctx, evt)
	case "update_version":
		// In-place version change (feature #32, AD6): recreate the pods
		// with the new weights while keeping the service identity and
		// endpoints. applyUpsert already reconciles the Deployment/Service
		// from the resolved spec, so the version-change event reuses it.
		return r.applyUpsert(ctx, evt)
	case "rollback":
		// Deployment rollback (feature #34, AD5): apply the previous
		// desired state while keeping the service identity and endpoints.
		// applyUpsert reconciles the Deployment/Service from the resolved
		// spec, so the rollback event reuses it.
		return r.applyUpsert(ctx, evt)
	case "update_engine_args":
		// Serving-engine parameter change: recreate the pods with the new
		// startup arguments while keeping the service identity and
		// endpoints. applyUpsert reconciles the storm from the resolved
		// spec, so the engine-args event reuses it.
		return r.applyUpsert(ctx, evt)
	default:
		return mq.Permanent(fmt.Errorf("controller: unknown event_type %q", evt.EventType))
	}
}

// warmupTask mirrors the warmup task dispatch published by the image
// module (architecture Section 4.5.1).
type warmupTask struct {
	TaskID       string            `json:"task_id"`
	ImageID      string            `json:"image_id"`
	Reference    string            `json:"reference"`
	NodeSelector map[string]string `json:"node_selector"`
	PublishedAt  time.Time         `json:"published_at"`
}

// warmupNodeResult is one node's pull outcome.
type warmupNodeResult struct {
	Node    string `json:"node"`
	State   string `json:"state"`
	Message string `json:"message"`
}

// warmupStatus is the observed-state report published on the warmup
// status subject (architecture Section 4.5.2).
type warmupStatus struct {
	TaskID        string             `json:"task_id"`
	State         string             `json:"state"`
	NodeResults   []warmupNodeResult `json:"node_results"`
	FailureReason *string            `json:"failure_reason"`
	ReportedAt    string             `json:"reported_at"`
}

// ApplyImageWarmup implements Reconciler: it decodes the task, reports
// running, lists the nodes matching the selector, runs a one-shot
// helper pod per node (imagePullPolicy=Always, spec.nodeName pinning)
// and reports the aggregated outcome (architecture Section 10.4).
func (r *k8sReconciler) ApplyImageWarmup(ctx context.Context, msg mq.Message) error {
	var task warmupTask
	if err := json.Unmarshal(msg.Body, &task); err != nil {
		logger.S().Warnw("controller: malformed warmup task, rejecting",
			"subject", msg.Subject, "err", err)
		return mq.Permanent(fmt.Errorf("controller: malformed warmup task: %w", err))
	}
	if task.TaskID == "" || task.Reference == "" {
		return mq.Permanent(fmt.Errorf("controller: warmup task missing task_id or reference"))
	}

	if err := r.publishWarmupStatus(ctx, task.TaskID, "running", nil, nil); err != nil {
		logger.S().Warnw("controller: publish warmup running status failed",
			"task_id", task.TaskID, "err", err)
	}

	nodes, err := r.listNodes(ctx, task.NodeSelector)
	if err != nil {
		return r.reportWarmupFailure(ctx, task, err)
	}
	if len(nodes) == 0 {
		return r.reportWarmupFailure(ctx, task,
			fmt.Errorf("controller: no node matches the selector %v", task.NodeSelector))
	}

	results := make([]warmupNodeResult, 0, len(nodes))
	failed := false
	for _, node := range nodes {
		result := r.warmupNode(ctx, task, node)
		results = append(results, result)
		if result.State != "succeeded" {
			failed = true
		}
	}

	if failed {
		reason := "one or more nodes failed to pull the image"
		return r.reportWarmupFailureWithResults(ctx, task, reason, results)
	}
	if err := r.publishWarmupStatus(ctx, task.TaskID, "succeeded", results, nil); err != nil {
		logger.S().Warnw("controller: publish warmup succeeded status failed",
			"task_id", task.TaskID, "err", err)
	}
	logger.S().Infow("controller: image warmup succeeded",
		"task_id", task.TaskID, "reference", task.Reference, "nodes", len(nodes))
	return nil
}

// warmupNode runs the one-shot helper pod on one node and returns its
// result. The pod name is deterministic per (task, node) so a retry is
// idempotent; a leftover pod from a crashed attempt is deleted first.
func (r *k8sReconciler) warmupNode(ctx context.Context, task warmupTask, node string) warmupNodeResult {
	podName := warmupPodName(task.TaskID, node)
	// Best-effort cleanup of a leftover pod from a crashed attempt.
	if err := r.clientset.CoreV1().Pods(r.namespace).
		Delete(ctx, podName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return warmupNodeResult{Node: node, State: "failed",
			Message: fmt.Sprintf("delete leftover pod: %v", err)}
	}

	pod := r.buildWarmupPod(task, podName, node)
	if _, err := r.clientset.CoreV1().Pods(r.namespace).
		Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return warmupNodeResult{Node: node, State: "failed",
			Message: fmt.Sprintf("create pod: %v", err)}
	}

	phase, message, err := r.awaitPodTerminal(ctx, podName)
	if err != nil {
		return warmupNodeResult{Node: node, State: "failed", Message: err.Error()}
	}
	// Pod phase Running/Succeeded means the image pull succeeded: the
	// kubelet only starts the container once the pull completed.
	if phase == corev1.PodRunning || phase == corev1.PodSucceeded {
		return warmupNodeResult{Node: node, State: "succeeded", Message: message}
	}
	return warmupNodeResult{Node: node, State: "failed", Message: message}
}

// warmupTimeout bounds how long one node's helper pod may take. An
// unreachable registry keeps the pod in ImagePullBackOff forever, so the
// task must fail with the pull reason instead of staying "running".
const warmupTimeout = 10 * time.Minute

// awaitPodTerminal polls the pod until it reaches a terminal phase
// (Succeeded/Failed) or a Running phase (pull done, container started).
// It gives up after warmupTimeout with the current container wait reason.
func (r *k8sReconciler) awaitPodTerminal(ctx context.Context, podName string) (corev1.PodPhase, string, error) {
	ctx, cancel := context.WithTimeout(ctx, warmupTimeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		pod, err := r.clientset.CoreV1().Pods(r.namespace).
			Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return "", "", fmt.Errorf("controller: get pod %s: %w", podName, err)
		}
		switch pod.Status.Phase {
		case corev1.PodSucceeded:
			return pod.Status.Phase, "pull completed", nil
		case corev1.PodFailed:
			return pod.Status.Phase, podFailureMessage(pod), nil
		case corev1.PodRunning:
			return pod.Status.Phase, "pull completed, container started", nil
		}
		select {
		case <-ctx.Done():
			// A timeout means the pull never completed; report the wait
			// reason (e.g. ErrImagePull) so the console explains why.
			latest, getErr := r.clientset.CoreV1().Pods(r.namespace).
				Get(context.WithoutCancel(ctx), podName, metav1.GetOptions{})
			if getErr == nil {
				msg := podFailureMessage(latest)
				return "", msg, fmt.Errorf("controller: image pull did not complete within %s: %s",
					warmupTimeout, msg)
			}
			return "", "", fmt.Errorf("controller: pod %s not terminal within %s: %w",
				podName, warmupTimeout, ctx.Err())
		case <-ticker.C:
			// keep polling
		}
	}
}

// podFailureMessage extracts a human-readable failure reason from the
// pod status (container wait reasons carry pull errors).
func podFailureMessage(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason + ": " + cs.State.Waiting.Message
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason + ": " + cs.State.Terminated.Message
		}
	}
	return "pod failed"
}

// listNodes lists cluster nodes filtered by the task's node selector.
func (r *k8sReconciler) listNodes(ctx context.Context, selector map[string]string) ([]string, error) {
	listOpts := metav1.ListOptions{}
	if len(selector) > 0 {
		// Selector ANDs all key=value pairs.
		pairs := make([]string, 0, len(selector))
		for k, v := range selector {
			pairs = append(pairs, k+"="+v)
		}
		listOpts.LabelSelector = strings.Join(pairs, ",")
	}
	nodeList, err := r.clientset.CoreV1().Nodes().List(ctx, listOpts)
	if err != nil {
		return nil, fmt.Errorf("controller: list nodes: %w", err)
	}
	names := make([]string, 0, len(nodeList.Items))
	for _, node := range nodeList.Items {
		names = append(names, node.Name)
	}
	return names, nil
}

// buildWarmupPod composes the one-shot helper pod pinned to a node:
// imagePullPolicy=Always forces a real pull even when the image is
// already present on the node (a cached image would otherwise mask a
// registry outage).
func (r *k8sReconciler) buildWarmupPod(task warmupTask, podName, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: r.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "taas-controller",
				"go-taas.com/task-type":        "image-warmup",
				"go-taas.com/task-id":          task.TaskID,
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			NodeName:      node,
			Containers: []corev1.Container{{
				Name:            "warmup",
				Image:           task.Reference,
				ImagePullPolicy: corev1.PullAlways,
				// The engine image's real entrypoint may need GPUs or
				// weights; override it with a trivially succeeding
				// command so the pull is the only thing being tested.
				Command: []string{"/bin/sh", "-c", "exit 0"},
			}},
		},
	}
}

// warmupPodName is the deterministic helper pod name for a (task, node)
// pair. Kubernetes resource names are lowercase; the UUID task id is
// already lowercase.
func warmupPodName(taskID, node string) string {
	sanitized := strings.ToLower(strings.ReplaceAll(taskID, "-", ""))
	if len(sanitized) > 40 {
		sanitized = sanitized[:40]
	}
	return "warmup-" + sanitized + "-" + node
}

// reportWarmupFailure publishes the failed warmup status with the
// reason and returns the wrapped error.
func (r *k8sReconciler) reportWarmupFailure(ctx context.Context, task warmupTask, cause error) error {
	reason := cause.Error()
	if len(reason) > 512 {
		reason = reason[:512]
	}
	return r.reportWarmupFailureWithResults(ctx, task, reason, nil)
}

// reportWarmupFailureWithResults publishes the failed warmup status with
// per-node results.
//
// It returns a Permanent error so the broker stops redelivering the task:
// the failure has been reported and the task is terminal, so a retry would
// only repeat the same failing pull. Only a publish failure is retryable
// (the status never reached the platform).
func (r *k8sReconciler) reportWarmupFailureWithResults(ctx context.Context, task warmupTask, reason string, results []warmupNodeResult) error {
	if len(reason) > 512 {
		reason = reason[:512]
	}
	if err := r.publishWarmupStatus(ctx, task.TaskID, "failed", results, &reason); err != nil {
		logger.S().Errorw("controller: publish warmup failed status failed",
			"task_id", task.TaskID, "err", err)
		return fmt.Errorf("controller: publish warmup %s failed status: %w", task.TaskID, err)
	}
	logger.S().Warnw("controller: image warmup failed",
		"task_id", task.TaskID, "reference", task.Reference, "reason", reason)
	return mq.Permanent(fmt.Errorf("controller: warmup %s failed: %s", task.TaskID, reason))
}

// publishWarmupStatus publishes a warmup observed-state report on the
// warmup status subject.
func (r *k8sReconciler) publishWarmupStatus(ctx context.Context, taskID, state string, results []warmupNodeResult, failureReason *string) error {
	if r.statusPublisher == nil {
		return nil
	}
	report := warmupStatus{
		TaskID:        taskID,
		State:         state,
		NodeResults:   results,
		FailureReason: failureReason,
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return r.statusPublisher.Publish(ctx, mq.DefaultSubjects().ImageWarmupStatus, body, nil)
}

// applyDelete tears down the StormService, Service and autoscaler, plus the
// Deployment an older controller created for the same service. The RPC
// already set state=terminated; no status report is needed.
func (r *k8sReconciler) applyDelete(ctx context.Context, evt changeEvent) error {
	if err := r.teardown(ctx, evt); err != nil {
		return err
	}
	logger.S().Infow("controller: tore down inference service",
		"service_id", evt.ServiceID, "name", evt.Name)
	return nil
}

// applyStop stops a running service: it deletes the same Kubernetes
// resources as applyDelete (StormService, Service, autoscaler, model
// route) but keeps the catalog row so the service can be started again.
// The RPC already set state=stopped; no status report is needed.
func (r *k8sReconciler) applyStop(ctx context.Context, evt changeEvent) error {
	if err := r.teardown(ctx, evt); err != nil {
		return err
	}
	logger.S().Infow("controller: stopped inference service",
		"service_id", evt.ServiceID, "name", evt.Name)
	return nil
}

const (
	// reclaimWaitTimeout bounds how long an upsert waits for a stale object
	// with a reused name to finish terminating.
	reclaimWaitTimeout = 30 * time.Second
	// reclaimPollInterval is how often the native reclaim wait re-checks a
	// terminating object.
	reclaimPollInterval = 200 * time.Millisecond
)

// teardown removes every Kubernetes resource a service owns. It deletes by
// name (the fast path) and then sweeps by the go-taas.com/service-id label
// (the backstop): a resource left behind by an earlier partial teardown, or
// one whose name does not match the current naming rule, is still removed so
// no workload keeps serving after the service was stopped or deleted.
//
// Every delete is independent: a failure on one resource (e.g. a Deployment
// stuck terminating) must not leave the others behind. Errors are collected
// and returned together so the broker retries, but each resource is still
// attempted.
func (r *k8sReconciler) teardown(ctx context.Context, evt changeEvent) error {
	var errs []error

	// Fast path: delete the well-known names. The ownership check keeps a
	// late delete event for an old service from tearing down the objects
	// of a new service that reused its name.
	if err := r.deleteDeploymentOwnedBy(ctx, deploymentName(evt.Name), evt.ServiceID); err != nil {
		errs = append(errs, err)
	}
	if err := r.deleteServiceOwnedBy(ctx, serviceName(evt.Name), evt.ServiceID); err != nil {
		errs = append(errs, err)
	}
	// A stateful StormService creates a headless Service named after the
	// storm. It carries the service-id label, but deleting it explicitly
	// makes teardown work even when that label is missing.
	if err := r.deleteServiceOwnedBy(ctx, evt.Name, evt.ServiceID); err != nil {
		errs = append(errs, err)
	}
	if err := r.deleteLegacyHPA(ctx, evt.Name); err != nil {
		errs = append(errs, err)
	}
	if err := deleteUnstructuredOwnedBy(ctx, r.dynamic, stormServiceGVR, r.namespace,
		stormServiceName(evt), evt.ServiceID); err != nil {
		errs = append(errs, fmt.Errorf("controller: delete stormservice %s: %w", evt.Name, err))
	}
	if err := deleteUnstructuredOwnedBy(ctx, r.dynamic, modelClaimGVR, r.namespace,
		modelClaimName(evt.Name), evt.ServiceID); err != nil {
		errs = append(errs, fmt.Errorf("controller: delete modelclaim %s: %w", evt.Name, err))
	}
	if err := r.deleteModelRoute(ctx, evt); err != nil {
		errs = append(errs, fmt.Errorf("controller: delete model route %s: %w", evt.Name, err))
	}
	if err := deletePodAutoscalerOwnedBy(ctx, r.dynamic, r.namespace, evt.Name, evt.ServiceID); err != nil {
		errs = append(errs, fmt.Errorf("controller: delete podautoscaler %s: %w", evt.Name, err))
	}
	if err := deleteUnstructuredOwnedBy(ctx, r.dynamic, podGroupGVR, r.namespace,
		podGroupName(evt), evt.ServiceID); err != nil {
		errs = append(errs, fmt.Errorf("controller: delete podgroup %s: %w", podGroupName(evt), err))
	}
	// The RDMA claim template is workload output: without this, a service
	// that is stopped and later restarted for a different model would keep a
	// stale device request.
	if err := r.deleteRdmaClaimTemplate(ctx, evt); err != nil {
		errs = append(errs, fmt.Errorf("controller: delete rdma claim template %s: %w", rdmaClaimTemplateName(evt), err))
	}

	// Backstop: sweep every resource still labeled with the service id.
	if err := r.sweepByServiceID(ctx, evt.ServiceID); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// deleteDeploymentOwnedBy deletes a Deployment by name unless another
// inference service owns it.
func (r *k8sReconciler) deleteDeploymentOwnedBy(ctx context.Context, name, serviceID string) error {
	dep, err := r.clientset.AppsV1().Deployments(r.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("controller: get deployment %s: %w", name, err)
	}
	if ownedByOtherService(dep.Labels, serviceID) {
		return nil
	}
	return r.deleteDeployment(ctx, name)
}

// deleteServiceOwnedBy deletes a Service by name unless another inference
// service owns it.
func (r *k8sReconciler) deleteServiceOwnedBy(ctx context.Context, name, serviceID string) error {
	svc, err := r.clientset.CoreV1().Services(r.namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("controller: get service %s: %w", name, err)
	}
	if ownedByOtherService(svc.Labels, serviceID) {
		return nil
	}
	return r.deleteService(ctx, name)
}

// deleteDeployment deletes one Deployment, ignoring not-found.
func (r *k8sReconciler) deleteDeployment(ctx context.Context, name string) error {
	err := r.clientset.AppsV1().Deployments(r.namespace).
		Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("controller: delete deployment %s: %w", name, err)
	}
	return nil
}

// deleteService deletes one Service, ignoring not-found.
func (r *k8sReconciler) deleteService(ctx context.Context, name string) error {
	err := r.clientset.CoreV1().Services(r.namespace).
		Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("controller: delete service %s: %w", name, err)
	}
	return nil
}

// sweepByServiceID deletes every object that still carries the service's
// go-taas.com/service-id label. It is the backstop that keeps a partially
// torn-down service from leaking resources: the fast path deletes by the
// names the current controller would use, so anything an older controller
// named differently (or left behind after a failed delete) is removed here.
//
// The sweep covers every resource a service can own: Services, Deployments,
// StatefulSets, Pods, StormServices, PodGroups, ModelClaims and the model
// HTTPRoute in the gateway namespace. A blank service id is a no-op (never
// sweep the whole namespace).
func (r *k8sReconciler) sweepByServiceID(ctx context.Context, serviceID string) error {
	if strings.TrimSpace(serviceID) == "" {
		return nil
	}
	selector := labelServiceID + "=" + serviceID
	var errs []error

	if svcs, err := r.clientset.CoreV1().Services(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector}); err != nil {
		errs = append(errs, fmt.Errorf("controller: list services for %s: %w", serviceID, err))
	} else {
		for i := range svcs.Items {
			if err := r.deleteService(ctx, svcs.Items[i].Name); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if deps, err := r.clientset.AppsV1().Deployments(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector}); err != nil {
		errs = append(errs, fmt.Errorf("controller: list deployments for %s: %w", serviceID, err))
	} else {
		for i := range deps.Items {
			if err := r.deleteDeployment(ctx, deps.Items[i].Name); err != nil {
				errs = append(errs, err)
			}
		}
	}
	// A stale StatefulSet (created by the storm controller) would keep its
	// pods alive after the StormService is gone.
	if sets, err := r.clientset.AppsV1().StatefulSets(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector}); err != nil {
		errs = append(errs, fmt.Errorf("controller: list statefulsets for %s: %w", serviceID, err))
	} else {
		for i := range sets.Items {
			if err := r.clientset.AppsV1().StatefulSets(r.namespace).Delete(ctx, sets.Items[i].Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("controller: delete statefulset %s: %w", sets.Items[i].Name, err))
			}
		}
	}
	// Pods are deleted last: deleting them before their controller can make
	// the controller recreate them.
	if pods, err := r.clientset.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector}); err != nil {
		errs = append(errs, fmt.Errorf("controller: list pods for %s: %w", serviceID, err))
	} else {
		for i := range pods.Items {
			if err := r.clientset.CoreV1().Pods(r.namespace).Delete(ctx, pods.Items[i].Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("controller: delete pod %s: %w", pods.Items[i].Name, err))
			}
		}
	}
	if r.dynamic != nil {
		if err := r.sweepDynamicByServiceID(ctx, serviceID, selector); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// sweepDynamicByServiceID deletes the dynamic-client resources labeled for
// the service. The HTTPRoute lives in the gateway namespace, not the
// workload namespace, so it is swept there.
func (r *k8sReconciler) sweepDynamicByServiceID(ctx context.Context, serviceID, selector string) error {
	var errs []error
	targets := []struct {
		kind      string
		namespace string
		gvr       schema.GroupVersionResource
	}{
		{"stormservice", r.namespace, stormServiceGVR},
		{"modelclaim", r.namespace, modelClaimGVR},
		{"podgroup", r.namespace, podGroupGVR},
		{"podautoscaler", r.namespace, podAutoscalerGVR},
	}
	gatewayNamespace, _ := r.gatewayRouteTarget()
	targets = append(targets, struct {
		kind      string
		namespace string
		gvr       schema.GroupVersionResource
	}{"httproute", gatewayNamespace, httpRouteGVR})
	// RDMA claims: the template and the claims DRA generated from it. A
	// generated claim outlives its pod only if the pod was force-deleted,
	// which is exactly the leak this sweep exists for.
	targets = append(targets,
		struct {
			kind      string
			namespace string
			gvr       schema.GroupVersionResource
		}{"resourceclaimtemplate", r.namespace, resourceClaimTemplateGVR},
		struct {
			kind      string
			namespace string
			gvr       schema.GroupVersionResource
		}{"resourceclaim", r.namespace, resourceClaimGVR},
	)

	for _, target := range targets {
		items, err := r.dynamic.Resource(target.gvr).Namespace(target.namespace).
			List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			// A missing CRD is not a leak: nothing of that kind exists.
			if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("controller: list %ss for %s: %w", target.kind, serviceID, err))
			continue
		}
		for i := range items.Items {
			name := items.Items[i].GetName()
			if err := deleteUnstructuredObject(ctx, r.dynamic, target.gvr, target.namespace, name); err != nil {
				errs = append(errs, fmt.Errorf("controller: delete %s %s: %w", target.kind, name, err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// deleteLegacyDeployment removes the Deployment an older controller created
// for the same service. Serving moved to the StormService, so a leftover
// Deployment would keep its ReplicaSet running next to the storm's pods;
// both match the Service selector, so the gateway would route to stale pods
// that serve the previous spec.
func (r *k8sReconciler) deleteLegacyDeployment(ctx context.Context, name string) error {
	err := r.clientset.AppsV1().Deployments(r.namespace).
		Delete(ctx, deploymentName(name), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("controller: delete legacy deployment %s: %w", name, err)
	}
	return nil
}

// applyUpsert reports deploying, creates or updates the Deployment,
// Service and (when autoscaling is enabled) the HPA, then reports
// running with endpoints. Any reconcile failure reports failed with the
// reason (AC7).
func (r *k8sReconciler) applyUpsert(ctx context.Context, evt changeEvent) error {
	// A stop/delete may have been applied before this (stale) change was
	// delivered, e.g. an update event published just before the operator
	// deleted the service. Recreating its resources would resurrect a
	// service the platform already considers gone.
	if r.serviceSuperseded(ctx, evt.ServiceID) {
		logger.S().Infow("controller: skipping obsolete change, service is stopped, deleted or has a newer change queued",
			"service_id", evt.ServiceID, "name", evt.Name, "event", evt.EventType)
		return nil
	}

	// A service name can be reused after its previous owner was deleted.
	// The previous teardown may still be in flight (or may have been lost),
	// and its objects carry the old service id. Reclaim them before
	// creating so the new service starts from clean objects instead of
	// colliding with a terminating Deployment/StormService or inheriting a
	// stale route. This is retryable: if an object takes too long to
	// terminate the broker redelivers the event, rather than marking the
	// service failed permanently.
	if err := r.reclaimServiceName(ctx, evt); err != nil {
		return fmt.Errorf("controller: reclaim name %s: %w", evt.Name, err)
	}

	// The service is queued the moment the controller picks the change up:
	// nothing has been scheduled yet, so the console shows "queued" until a
	// pod actually lands on a node.
	if err := r.publishStatus(ctx, evt.ServiceID, "queued", nil, nil); err != nil {
		logger.S().Warnw("controller: publish queued status failed",
			"service_id", evt.ServiceID, "err", err)
	}

	// Ensure the shared model-weights PVC exists before the Deployment
	// references it. The PVC is provisioned from the configured
	// JuiceFS-backed StorageClass so every inference pod reads the same
	// model weights the control plane downloads.
	if err := r.ensureWeightsPVC(ctx); err != nil {
		return r.reportFailure(ctx, evt, err)
	}

	// The RDMA claim template must exist before the storm is written: a pod
	// referencing a missing template is rejected by admission, and the
	// failure would only surface later as a stalled deploy.
	if err := r.applyRdmaClaims(ctx, evt); err != nil {
		return r.reportFailure(ctx, evt, err)
	}

	// Serving is an AIBrix StormService: one object per service whose roles
	// express the topology (single-node, distributed, PD). The plain
	// Deployment path is only used by a controller started without a dynamic
	// client (unit tests, a CRD-less install).
	if r.dynamic != nil {
		storm, err := r.buildStormService(ctx, evt)
		if err != nil {
			return r.reportFailure(ctx, evt, err)
		}
		if err := applyStormService(ctx, r.dynamic, storm); err != nil {
			return r.reportFailure(ctx, evt, err)
		}
		// The gang the pods join. It is written before the pods can be
		// admitted (the storm's own controller creates them), so Volcano
		// never sees a gang member without a group.
		if r.gangScheduling() {
			if err := applyPodGroup(ctx, r.dynamic, r.buildPodGroup(evt)); err != nil {
				return r.reportFailure(ctx, evt, err)
			}
		}
		// A service that used to run as a Deployment must not keep its old
		// ReplicaSet alive next to the storm's pods: both match the Service
		// selector, so the gateway would route to the stale pods too.
		if err := r.deleteLegacyDeployment(ctx, evt.Name); err != nil {
			return r.reportFailure(ctx, evt, err)
		}
	} else {
		deployment := r.buildDeployment(ctx, evt)
		if err := r.createOrUpdateDeployment(ctx, deployment); err != nil {
			return r.reportFailure(ctx, evt, err)
		}
	}
	svc := r.buildService(evt)
	if err := r.createOrUpdateService(ctx, svc); err != nil {
		return r.reportFailure(ctx, evt, err)
	}

	// The serving gateway refuses every request for a model it cannot
	// resolve to a route, so a service is unroutable until its model route
	// exists. The write is best-effort: a failure (Gateway API CRDs absent
	// or dynamic client unavailable) is logged and the plain serving path
	// continues unchanged.
	if err := r.applyModelRoute(ctx, evt); err != nil {
		logger.S().Warnw("controller: aibrix model route write failed, continuing on the plain path",
			"service_id", evt.ServiceID, "name", evt.Name, "err", err)
	}

	// Feature #47 (AD5, AD13): when the serving scheme is aibrix, register
	// the model with AIBrix by creating or updating a ModelClaim through
	// the dynamic client. The write is best-effort: a failure (CRD absent
	// or dynamic client unavailable) is logged and the plain serving path
	// continues unchanged.
	if r.aibrixScheme() {
		claim := buildModelClaimFromEvent(evt, r.namespace, r.weights.MountPath)
		if err := applyModelClaim(ctx, r.dynamic, claim); err != nil {
			logger.S().Warnw("controller: aibrix modelclaim write failed, continuing on the plain path",
				"service_id", evt.ServiceID, "name", evt.Name, "err", err)
		}
	}

	// Autoscaling is the AIBrix PodAutoscaler, never a Kubernetes HPA: the
	// HPA cannot target a StormService, and two autoscalers on one workload
	// fight over the replica count. A failure to write it is reported as a
	// failure instead of silently pinning the replica count, so the console
	// never shows "autoscaled" for a service nothing is scaling.
	//
	// Stale HPAs from an older controller are deleted on every reconcile:
	// leaving one behind would compete with the PodAutoscaler.
	if err := r.deleteLegacyHPA(ctx, evt.Name); err != nil {
		return r.reportFailure(ctx, evt, err)
	}
	if evt.Autoscaling != nil && evt.Autoscaling.Enabled {
		if err := r.applyAIBrixAutoscaler(ctx, evt); err != nil {
			// Drop the stale object before reporting: leaving it behind
			// keeps the AIBrix controller scaling the storm with a policy
			// nobody asked for.
			deletePodAutoscaler(ctx, r.dynamic, r.namespace, evt.Name)
			return r.reportFailure(ctx, evt, err)
		}
	} else {
		deletePodAutoscaler(ctx, r.dynamic, r.namespace, evt.Name)
		// Pin the workload to the fixed replica count (FR2.5).
		if r.dynamic != nil {
			if err := r.pinStormServiceReplicas(ctx, evt); err != nil {
				return r.reportFailure(ctx, evt, err)
			}
		} else if err := r.pinDeploymentReplicas(ctx, evt); err != nil {
			return r.reportFailure(ctx, evt, err)
		}
	}

	// Follow the pods: "starting" once the scheduler placed them on a node,
	// "running" only when the engine itself answers. A pod that is merely
	// Running has not loaded its weights yet, so its readiness says nothing
	// about whether the service can serve traffic.
	if err := r.awaitEngineReady(ctx, evt); err != nil {
		if errors.Is(err, errReconcileSuperseded) {
			logger.S().Infow("controller: engine wait abandoned, service was stopped, deleted or has a newer change queued",
				"service_id", evt.ServiceID, "name", evt.Name)
			return nil
		}
		return r.reportFailure(ctx, evt, err)
	}

	endpoints := []string{r.endpointFor(evt.Name)}
	if err := r.publishStatus(ctx, evt.ServiceID, "running", endpoints, nil); err != nil {
		logger.S().Warnw("controller: publish running status failed",
			"service_id", evt.ServiceID, "err", err)
	}
	logger.S().Infow("controller: inference service running",
		"service_id", evt.ServiceID, "name", evt.Name, "endpoints", endpoints)
	return nil
}

// deleteLegacyHPA deletes a Kubernetes HPA an older controller created for
// the same service, ignoring not-found. Autoscaling is the AIBrix
// PodAutoscaler now; a leftover HPA would compete with it for the replica
// count, so it is swept on every reconcile.
func (r *k8sReconciler) deleteLegacyHPA(ctx context.Context, name string) error {
	err := r.clientset.AutoscalingV2().HorizontalPodAutoscalers(r.namespace).
		Delete(ctx, autoscalerName(name), metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("controller: delete legacy hpa %s: %w", name, err)
	}
	return nil
}

// aibrixScheme reports whether the cached serving scheme is aibrix.
func (r *k8sReconciler) aibrixScheme() bool {
	return r.scheme != nil && r.scheme.Scheme() == schemeAIBrix
}

// applyAIBrixAutoscaler creates or updates the AIBrix PodAutoscaler for a
// change event with autoscaling enabled (feature #47, §13.5). The
// min/max replicas come from the effective autoscaling policy.
func (r *k8sReconciler) applyAIBrixAutoscaler(ctx context.Context, evt changeEvent) error {
	policy := evt.Autoscaling
	if policy == nil {
		policy = &autoscalingPolicy{Enabled: true, MinReplicas: 1, MaxReplicas: 10, TargetConcurrency: 32}
	}
	minReplicas := clampReplicas(policy.MinReplicas)
	if minReplicas < 0 {
		minReplicas = 0
	}
	maxReplicas := clampReplicas(policy.MaxReplicas)
	if maxReplicas < 1 {
		maxReplicas = 1
	}
	target := clampReplicas(policy.TargetConcurrency)
	if target < 1 {
		target = 32
	}
	return applyPodAutoscalerForService(ctx, r.dynamic, r.namespace, stormServiceName(evt), evt.ServiceID, minReplicas, maxReplicas, target)
}

// pinDeploymentReplicas sets the Deployment's replicas to the fixed
// count when autoscaling is disabled (FR2.5).
func (r *k8sReconciler) pinDeploymentReplicas(ctx context.Context, evt changeEvent) error {
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{"replicas": clampReplicas(evt.Replicas)},
	})
	if err != nil {
		return err
	}
	_, err = r.clientset.AppsV1().Deployments(r.namespace).
		Patch(ctx, deploymentName(evt.Name), types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}

// reportFailure publishes the failed status with the reason and returns
// the wrapped error (AC7). A status-publish failure is logged; the
// service stays in its last known state (never silently running).
// reportFailure publishes the failed status for a reconcile and returns
// the error the broker sees.
//
// The failure is permanent once reported: the task is terminal, and a
// redelivery would re-run the same failing reconcile while blocking the
// single-prefetch consumer (every later deploy would queue behind it).
// Only a failed status publish stays retryable, because the platform never
// learned the outcome.
func (r *k8sReconciler) reportFailure(ctx context.Context, evt changeEvent, cause error) error {
	reason := cause.Error()
	if len(reason) > 512 {
		reason = reason[:512]
	}
	if err := r.publishStatus(ctx, evt.ServiceID, "failed", nil, &reason); err != nil {
		logger.S().Errorw("controller: publish failed status failed",
			"service_id", evt.ServiceID, "err", err)
		return fmt.Errorf("controller: publish %s failed status: %w", evt.Name, err)
	}
	logger.S().Warnw("controller: inference service failed",
		"service_id", evt.ServiceID, "name", evt.Name, "reason", reason)
	return mq.Permanent(fmt.Errorf("controller: reconcile %s failed: %w", evt.Name, cause))
}

// deployBlockedReason explains why a Deployment's pods are not ready: the
// first pod's scheduling or container waiting reason (unschedulable,
// ImagePullBackOff, CrashLoopBackOff, ...). Empty when nothing is
// available to report.
func (r *k8sReconciler) deployBlockedReason(ctx context.Context, name string) string {
	pods, err := r.clientset.CoreV1().Pods(r.namespace).
		List(ctx, metav1.ListOptions{LabelSelector: "app=" + name})
	if err != nil || len(pods.Items) == 0 {
		return ""
	}
	pod := pods.Items[0]
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			msg := cs.State.Waiting.Reason
			if cs.State.Waiting.Message != "" {
				msg += ": " + cs.State.Waiting.Message
			}
			return msg
		}
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse && cond.Message != "" {
			return cond.Message
		}
	}
	if pod.Status.Phase != "" {
		return "pod phase " + string(pod.Status.Phase)
	}
	return ""
}

// createOrUpdateDeployment creates the Deployment or patches the
// desired spec (idempotent reconcile).
func (r *k8sReconciler) createOrUpdateDeployment(ctx context.Context, dep *appsv1.Deployment) error {
	_, err := r.clientset.AppsV1().Deployments(r.namespace).
		Create(ctx, dep, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Strategic merge patch: the spec must be wrapped in a "spec" key.
	patch, err := json.Marshal(map[string]any{"spec": dep.Spec})
	if err != nil {
		return err
	}
	_, err = r.clientset.AppsV1().Deployments(r.namespace).
		Patch(ctx, dep.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}

// createOrUpdateService creates the Service or patches the desired
// spec (idempotent reconcile).
func (r *k8sReconciler) createOrUpdateService(ctx context.Context, svc *corev1.Service) error {
	_, err := r.clientset.CoreV1().Services(r.namespace).
		Create(ctx, svc, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Strategic merge patch: the spec must be wrapped in a "spec" key.
	patch, err := json.Marshal(map[string]any{"spec": svc.Spec})
	if err != nil {
		return err
	}
	_, err = r.clientset.CoreV1().Services(r.namespace).
		Patch(ctx, svc.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}

// ensureWeightsPVC creates the shared model-weights PVC if it does not
// exist, provisioning it from the configured JuiceFS-backed
// StorageClass. A pre-existing PVC is left untouched (idempotent).
func (r *k8sReconciler) ensureWeightsPVC(ctx context.Context) error {
	_, err := r.clientset.CoreV1().PersistentVolumeClaims(r.namespace).
		Get(ctx, r.weights.PVCName, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("controller: get weights pvc %s: %w", r.weights.PVCName, err)
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.weights.PVCName,
			Namespace: r.namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "taas-controller",
				"go-taas.com/role":             "model-weights",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("100Gi"),
				},
			},
		},
	}
	if r.weights.StorageClass != "" {
		pvc.Spec.StorageClassName = &r.weights.StorageClass
	}
	if _, err := r.clientset.CoreV1().PersistentVolumeClaims(r.namespace).
		Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("controller: create weights pvc %s: %w", r.weights.PVCName, err)
	}
	logger.S().Infow("controller: ensured model-weights PVC",
		"pvc", r.weights.PVCName, "storage_class", r.weights.StorageClass)
	return nil
}

// buildDeployment composes the Deployment for a change event: engine
// image, replicas, weight-volume mount and accelerator node selector.
func (r *k8sReconciler) buildDeployment(ctx context.Context, evt changeEvent) *appsv1.Deployment {
	labels := resourceLabels(evt)
	spec, annotations := r.buildPodTemplate(ctx, evt, stormRole{Name: stormRoleWorker, PodGroupSize: 1})
	// The serving gateway discovers which model a workload serves from the
	// workload's own metadata, so both the Deployment (which the gateway
	// derives the model route from) and its pods (which receive the
	// routed traffic) carry the model name, port and backing Service.
	modelName := servedModelName(evt)
	routeLabels := routingLabels(modelName)
	routeAnnotations := routingAnnotations(modelName, serviceName(evt.Name))
	deploymentLabels := mergeLabels(labels, routeLabels)
	deploymentAnnotations := mergeLabels(annotations, routeAnnotations)
	podLabels := deploymentLabels
	podAnnotations := deploymentAnnotations
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        deploymentName(evt.Name),
			Namespace:   r.namespace,
			Labels:      deploymentLabels,
			Annotations: deploymentAnnotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(clampReplicas(evt.Replicas)),
			// The selector stays the shared resource labels: it is
			// immutable, and a service whose served model name changes must
			// keep updating in place.
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels, Annotations: podAnnotations},
				Spec:       spec,
			},
		},
	}
}

// buildPodTemplate composes the engine pod spec for one role of a service.
// engineRole is "" for a plain worker pod and "prefill"/"decode" for a
// PD-disaggregated role; it decides the role-specific engine flags. The
// returned annotations are the pod-level ones the container needs (the HAMI
// placement policy for a shared-GPU service).
func (r *k8sReconciler) buildPodTemplate(ctx context.Context, evt changeEvent, role stormRole) (corev1.PodSpec, map[string]string) {
	// Feature #52 (AD13): a shared-GPU service requests a HAMI slice
	// instead of a whole card. The engine container requests the HAMI
	// extended resources, the pod carries the placement-policy
	// annotation, and the nodeSelector is constrained to the pool's
	// label selector so HAMI schedules only onto pool nodes.
	slice := evt.PoolID != "" && evt.SliceTemplateID != ""
	annotations := map[string]string{}
	nodeSelector := acceleratorNodeSelector(r.acceleratorLabelKeyFor(ctx), evt.Accelerator)
	if evt.AcceleratorType != "" {
		if key := r.cardTypeLabelKeyFor(ctx, evt.AcceleratorType); key != "" {
			nodeSelector[key] = evt.AcceleratorType
		}
	}
	// A pod owns gpu_per_pod devices (default one). Multi-machine layouts
	// split their parallelism across pods of this size, so the devices a
	// container sees and the world size the engine is told always agree.
	resources := r.podResources(evt)
	if slice {
		resources = sliceResources(evt.Accelerator, evt.SliceMemoryMiB, evt.SliceComputePct)
		annotations[r.policyAnnotation] = r.placementPolicy
		nodeSelector = mergeSelectors(nodeSelector, r.poolSelector)
	}
	// Volcano schedules the pods of one replica as a gang and preempts a
	// lower priority gang for a higher one (see podgroup.go). Without a
	// configured scheduler the cluster default places them one by one, which
	// is the historical behaviour.
	schedulerName := r.schedulerName
	priorityClass := r.priorityClassFor(evt.Priority)
	spec := corev1.PodSpec{
		SchedulerName:     schedulerName,
		PriorityClassName: priorityClass,
		Containers: []corev1.Container{{
			Name:  "engine",
			Image: evt.Image.Reference,
			Ports: []corev1.ContainerPort{{ContainerPort: 8000, Name: "http"}},
			// The engine container requests the accelerator's
			// extended resource so the scheduler places it on a
			// node with free capacity and the device plugin
			// exposes the device to the container.
			Resources: resources,
			// The engine command points at the model's weights
			// directory (the whole JuiceFS filesystem is mounted
			// at the weights mount path; the model lives in a
			// subdirectory named by its weight path).
			Command: engineCommand(evt, r.weights.MountPath, evt.Model.WeightPath, servedModelName(evt), role),
			// Operator-supplied environment variables. They apply to every
			// topology; a PD or multi-machine role may override one for
			// itself (see envForRole).
			Env: evt.envForRole(role.Name),
			VolumeMounts: []corev1.VolumeMount{{
				Name:      "weights",
				MountPath: r.weights.MountPath,
				ReadOnly:  true,
			}},
		}},
		Volumes: []corev1.Volume{{
			Name: "weights",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: r.weights.PVCName,
				},
			},
		}},
		NodeSelector: nodeSelector,
	}
	// RDMA devices come from the service's ResourceClaimTemplate: each pod
	// created from this template gets its own allocation, so replicas never
	// share one device set. The container has to name the claim as well as
	// the pod, which is how DRA binds devices into the container.
	if !rdmaDisabled(evt) {
		template := rdmaClaimTemplateName(evt)
		spec.ResourceClaims = append(spec.ResourceClaims, corev1.PodResourceClaim{
			Name:                      rdmaClaimName,
			ResourceClaimTemplateName: ptr(template),
		})
		spec.Containers[0].Resources.Claims = append(spec.Containers[0].Resources.Claims,
			corev1.ResourceClaim{Name: rdmaClaimName})
	}

	// A distributed engine (multi-machine or PD) runs collectives inside the
	// pod and exchanges tensors through shared memory. So does a single-node
	// engine whose tensor/pipeline parallelism shards the model across more
	// than one device: vLLM's multiproc executor allocates a shared-memory
	// broadcast buffer even within one pod. Kubernetes defaults /dev/shm to
	// 64 MiB, which makes the engine die with "Insufficient space in
	// /dev/shm", so such a pod gets a memory-backed volume of its own.
	if evt.distributedServing() || evt.engineParallel() {
		size := resource.MustParse("2Gi")
		spec.Volumes = append(spec.Volumes, corev1.Volume{
			Name: "shm",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: &size,
				},
			},
		})
		spec.Containers[0].VolumeMounts = append(spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "shm",
			MountPath: "/dev/shm",
		})
	}
	return spec, annotations
}

// gpuResourceName maps an accelerator to the Kubernetes extended
// resource name its device plugin publishes. Unknown accelerators map
// to "" (no resource request).
func gpuResourceName(accelerator string) string {
	switch accelerator {
	case "nvidia":
		return "nvidia.com/gpu"
	case "iluvatar":
		return "iluvatar.ai/vgpu"
	case "metax":
		return "metax-tech.com/gpu"
	default:
		return ""
	}
}

// engineResources returns the container resource requests for an
// accelerator: one unit of the accelerator's extended resource so the
// scheduler places the pod on a node with free capacity. Unknown
// accelerators get no resource request.
func engineResources(accelerator string) corev1.ResourceRequirements {
	return engineResourcesFor(accelerator, 1)
}

// engineResourcesFor returns the container resource requests for cards units
// of an accelerator's extended resource. A tensor/pipeline-parallel replica
// needs one device per shard in the single pod that runs it, so the pod asks
// for TP × PP devices: the engine's world size must not exceed the devices the
// pod can see (vLLM refuses to start otherwise).
func engineResourcesFor(accelerator string, cards int) corev1.ResourceRequirements {
	name := gpuResourceName(accelerator)
	if name == "" {
		return corev1.ResourceRequirements{}
	}
	if cards < 1 {
		cards = 1
	}
	quantity := resource.MustParse(strconv.Itoa(cards))
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceName(name): quantity,
		},
		Requests: corev1.ResourceList{
			corev1.ResourceName(name): quantity,
		},
	}
}

// podResources returns the container resource requests of one pod: the
// accelerator devices the operator asked for, plus the CPU and memory
// requests. A zero request is left out of the list entirely, which keeps the
// pod's shape identical to what it was before these fields existed.
func (r *k8sReconciler) podResources(evt changeEvent) corev1.ResourceRequirements {
	cards := evt.GPUPerPod
	if cards < 1 {
		cards = 1
	}
	res := engineResourcesFor(evt.Accelerator, cards)
	if evt.CPUMilli > 0 {
		quantity := *resource.NewMilliQuantity(evt.CPUMilli, resource.DecimalSI)
		if res.Requests == nil {
			res.Requests = corev1.ResourceList{}
		}
		res.Requests[corev1.ResourceCPU] = quantity
		// A CPU request without a limit is the right default for a serving
		// engine: it reserves capacity for the scheduler without capping a
		// process that legitimately bursts while loading weights.
	}
	if evt.MemoryMiB > 0 {
		quantity := *resource.NewQuantity(evt.MemoryMiB<<20, resource.BinarySI)
		if res.Requests == nil {
			res.Requests = corev1.ResourceList{}
		}
		res.Requests[corev1.ResourceMemory] = quantity
	}
	return res
}

// sliceResources returns the container resource requests for a
// shared-GPU service (feature #52, AD13): the HAMI extended resources
// (memory MiB + compute percentage) instead of a whole card. Unknown
// accelerators get no resource request.
func sliceResources(accelerator string, memMiB, computePct int64) corev1.ResourceRequirements {
	memName := gpuMemResourceName(accelerator)
	coreName := gpuCoreResourceName(accelerator)
	if memName == "" || coreName == "" {
		return corev1.ResourceRequirements{}
	}
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceName(memName):  resource.MustParse(fmt.Sprintf("%dMi", memMiB)),
			corev1.ResourceName(coreName): resource.MustParse(fmt.Sprintf("%d", computePct)),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceName(memName):  resource.MustParse(fmt.Sprintf("%dMi", memMiB)),
			corev1.ResourceName(coreName): resource.MustParse(fmt.Sprintf("%d", computePct)),
		},
	}
}

// gpuMemResourceName maps an accelerator to the HAMI memory resource
// name (feature #52, Section 5.3). Unknown accelerators map to "".
func gpuMemResourceName(accelerator string) string {
	switch accelerator {
	case "nvidia":
		return "nvidia.com/gpumem"
	case "iluvatar":
		return "iluvatar.ai/vgpu-mem"
	case "metax":
		return "metax-tech.com/gpu-mem"
	default:
		return ""
	}
}

// gpuCoreResourceName maps an accelerator to the HAMI compute resource
// name (feature #52, Section 5.3). Unknown accelerators map to "".
func gpuCoreResourceName(accelerator string) string {
	switch accelerator {
	case "nvidia":
		return "nvidia.com/gpucores"
	case "iluvatar":
		return "iluvatar.ai/vgpu-cores"
	case "metax":
		return "metax-tech.com/gpu-cores"
	default:
		return ""
	}
}

// mergeSelectors merges two node-selector maps (the pool selector wins
// on key conflicts).
func mergeSelectors(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// rendezvousPort is the port the workers of a multi-pod role use to meet.
// It is vLLM's default master port and is passed to SGLang as the
// distributed-init port.
const rendezvousPort = 29501

// engineCommand builds the engine container command that serves the model at
// the weights mount path. The whole JuiceFS filesystem is mounted at
// mountPath; the model lives in the subdirectory named by weightPath (e.g.
// "Qwen__Qwen2.5-0.5B-Instruct/"). The command is engine-specific; unknown
// engines get no command (the image's default entrypoint runs).
//
// role decides three things: which engine arguments apply (the shared set
// overridden by the role's own), the PD role marker, and whether the pod has
// to rendezvous with sibling pods (a multi-machine or PD role spans several
// pods, one per machine).
func engineCommand(evt changeEvent, mountPath, weightPath, servedName string, role stormRole) []string {
	engineArgs, extraArgs := evt.argsForRole(role.Name)
	multiPod := role.PodGroupSize > 1

	var base []string
	switch evt.Image.Engine {
	case "vllm":
		base = []string{
			"python3", "-m", "vllm.entrypoints.openai.api_server",
			"--model", modelDir(mountPath, weightPath),
			"--port", strconv.Itoa(enginePort),
			"--host", "0.0.0.0",
		}
	case "sglang":
		base = []string{
			"python3", "-m", "sglang.launch_server",
			"--model-path", modelDir(mountPath, weightPath),
			"--port", strconv.Itoa(enginePort),
			"--host", "0.0.0.0",
		}
	default:
		return nil
	}
	// The engine must accept the name clients send as `model`, which is the
	// catalog name rather than the weights directory.
	if servedName != "" {
		base = append(base, "--served-model-name", servedName)
	}
	base = append(base, topologyEngineArgs(evt.Image.Engine, role, engineArgs, evt)...)
	base = appendEngineArgs(base, engineArgs, extraArgs)
	if !multiPod {
		return base
	}
	return rendezvousCommand(evt, base, role)
}

// rendezvousCommand wraps the engine arguments so each pod of a multi-pod role
// knows its own rank and the address of rank 0.
//
// A stateful storm gives every pod a stable hostname and a headless Service
// named after the storm, so the rank is the pod's ordinal (the last dash
// segment of its hostname) and the master is the pod whose ordinal is 0 in the
// same role set. Both are resolved at container start, which is why the
// command runs through `sh -c`: the flags cannot be baked into the template,
// because every pod of the role shares it.
//
// The engine arguments are passed as positional parameters (sh -c script name
// arg...) so their quoting - JSON values such as a kv-transfer-config - is
// never re-parsed.
func rendezvousCommand(evt changeEvent, base []string, role stormRole) []string {
	nnodes := strconv.Itoa(int(role.PodGroupSize))
	master := fmt.Sprintf(`"${HOSTNAME%%-*}-0.%s"`, evt.Name)
	script := `rank="${HOSTNAME##*-}"; master=` + master + `; exec "$0" "$@" ` +
		`--nnodes ` + nnodes + ` --node-rank "$rank"`
	switch evt.Image.Engine {
	case "vllm":
		script += ` --master-addr "$master" --master-port ` + strconv.Itoa(rendezvousPort)
	case "sglang":
		script += ` --dist-init-addr "$master:` + strconv.Itoa(rendezvousPort) + `"`
	}
	return append([]string{"sh", "-c", script}, base...)
}

// topologyEngineArgs returns the flags a serving topology needs, in the order
// they are appended to the engine command. Only the parameters the operator
// cannot express in the generic form are derived here:
//
//	multinode    the parallelism of the whole role (tensor/pipeline size)
//	pd           the same, plus the role marker: vLLM's kv-transfer-config
//	             (kv_role producer/consumer) or SGLang's
//	             --disaggregation-mode prefill/decode
//
// Every one of them is skipped when the role's engine arguments already set
// the flag, so the explicit arguments stay authoritative.
func topologyEngineArgs(engine string, role stormRole, engineArgs map[string]string, evt changeEvent) []string {
	if !isMultiNode(evt.Topology) && evt.Topology != topologyPD {
		return nil
	}
	args := make([]string, 0, 6)
	tp := evt.TensorParallelSize
	if tp < 1 {
		tp = 1
	}
	pp := evt.PipelineParallelSize
	if pp < 1 {
		pp = 1
	}
	// vLLM shards with --tensor-parallel-size / --pipeline-parallel-size;
	// SGLang spells the tensor axis --tp-size.
	tpFlag, ppFlag := "tensor-parallel-size", "pipeline-parallel-size"
	if engine == "sglang" {
		tpFlag, ppFlag = "tp-size", "pp-size"
	}
	if _, set := engineArgs[tpFlag]; !set && tp > 1 {
		args = append(args, "--"+tpFlag, strconv.Itoa(tp))
	}
	if _, set := engineArgs[ppFlag]; !set && pp > 1 {
		args = append(args, "--"+ppFlag, strconv.Itoa(pp))
	}
	if role.EngineRole == "" {
		return args
	}
	switch engine {
	case "vllm":
		if _, set := engineArgs["kv-transfer-config"]; set {
			return args
		}
		kvRole := "kv_producer"
		if role.EngineRole == stormRoleDecode {
			kvRole = "kv_consumer"
		}
		config, err := json.Marshal(map[string]any{
			"kv_connector": pdKVConnector,
			"kv_role":      kvRole,
			// The two PD roles form one KV transfer group.
			"kv_rank":          0,
			"kv_parallel_size": 2,
		})
		if err != nil {
			return args
		}
		args = append(args, "--kv-transfer-config", string(config))
	case "sglang":
		if _, set := engineArgs["disaggregation-mode"]; set {
			return args
		}
		args = append(args, "--disaggregation-mode", role.EngineRole)
	}
	return args
}

// enginePortExplicit reports whether the operator pinned the port through
// engine arguments, in which case the readiness probe must use that port
// instead of the default the command sets.
func enginePortFrom(engineArgs map[string]string) int {
	for _, name := range []string{"port", "api-port"} {
		if raw, ok := engineArgs[name]; ok {
			if port, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && port > 0 && port < 65536 {
				return port
			}
		}
	}
	return 0
}

// appendEngineArgs appends the operator's engine parameters, sorted so the
// command line is stable, then the free-form arguments verbatim. A value
// prefixed with "flag:" is a valueless switch ("flag:true" → "--flag").
func appendEngineArgs(args []string, engineArgs map[string]string, extraArgs []string) []string {
	names := make([]string, 0, len(engineArgs))
	for name := range engineArgs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := strings.TrimSpace(engineArgs[name])
		if value == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(value, "flag:"); ok {
			// A switch: "flag:true" emits the bare flag, "flag:false"
			// emits nothing so the default applies.
			if rest == "true" || rest == "1" || rest == "" {
				args = append(args, "--"+name)
			}
			continue
		}
		args = append(args, "--"+name, value)
	}
	for _, raw := range extraArgs {
		arg := strings.TrimSpace(raw)
		if arg != "" {
			args = append(args, arg)
		}
	}
	return args
}

// modelDir joins the weights mount path and a model's weight path into
// the directory the engine serves. The weight path is a relative
// subdirectory (e.g. "Qwen__Qwen2.5-0.5B-Instruct/"); the result is the
// absolute path inside the container.
func modelDir(mountPath, weightPath string) string {
	wp := strings.Trim(weightPath, "/")
	if wp == "" {
		return mountPath
	}
	return strings.TrimSuffix(mountPath, "/") + "/" + wp
}

// buildService composes the ClusterIP Service fronting the pods.
func (r *k8sReconciler) buildService(evt changeEvent) *corev1.Service {
	labels := resourceLabels(evt)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName(evt.Name),
			Namespace: r.namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			// The engine port is exposed next to the plain HTTP port because
			// the serving gateway's model route addresses the backend by the
			// engine's own port (model.aibrix.ai/port).
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt32(enginePort),
				},
				{
					Name:       "engine",
					Port:       enginePort,
					TargetPort: intstr.FromInt32(enginePort),
				},
			},
		},
	}
}

// autoscalerName is the Kubernetes resource name of a service's
// PodAutoscaler. The historical "hpa-" prefix is kept so an object created
// before the move to the AIBrix autoscaler is updated (not duplicated).
func autoscalerName(name string) string { return "hpa-" + name }

// parallelCards is how many devices one replica needs: the product of the
// tensor and pipeline parallelism, and 1 for a single-node replica or a
// shared-GPU slice.
func (e changeEvent) distributedServing() bool {
	return isMultiNode(e.Topology) || e.Topology == topologyPD
}

// engineParallel reports whether the engine shards the model across more
// than one device, so its multiprocess executor needs a large /dev/shm
// even inside a single pod. It covers the single-node tensor/pipeline
// parallel case (topology "single" with tensor_parallel_size > 1) as well
// as a pod that itself owns more than one device.
func (e changeEvent) engineParallel() bool {
	tp := e.TensorParallelSize
	if tp < 1 {
		tp = 1
	}
	pp := e.PipelineParallelSize
	if pp < 1 {
		pp = 1
	}
	if tp*pp > 1 {
		return true
	}
	return e.GPUPerPod > 1
}

// roleEngineArgs is one role's engine-argument override, mirroring the infer
// module's RoleEngineArgs.
// rdmaRequest is the RDMA device request carried by a change event.
type rdmaRequest struct {
	Enabled     bool   `json:"enabled"`
	Devices     int    `json:"devices"`
	DeviceClass string `json:"device_class,omitempty"`
}

type roleEngineArgs struct {
	Role       string            `json:"role"`
	EngineArgs map[string]string `json:"engine_args,omitempty"`
	ExtraArgs  []string          `json:"extra_args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

// argsForRole merges one role's override over the shared engine arguments:
// a flag the role sets wins, the rest is inherited, and the role's free-form
// arguments are appended after the shared ones.
func (e changeEvent) argsForRole(role string) (map[string]string, []string) {
	engineArgs := make(map[string]string, len(e.EngineArgs))
	for name, value := range e.EngineArgs {
		engineArgs[name] = value
	}
	extra := append([]string{}, e.ExtraArgs...)
	for _, override := range e.RoleEngineArgs {
		if override.Role != role {
			continue
		}
		for name, value := range override.EngineArgs {
			engineArgs[name] = value
		}
		extra = append(extra, override.ExtraArgs...)
	}
	if len(engineArgs) == 0 {
		engineArgs = nil
	}
	return engineArgs, extra
}

// envForRole merges one role's environment overrides over the shared set: a
// variable the role sets wins, the rest is inherited. The result is sorted by
// name so the pod template is deterministic — an unordered map would rewrite
// the storm (and roll the pods) on every reconcile.
func (e changeEvent) envForRole(role string) []corev1.EnvVar {
	merged := make(map[string]string, len(e.Env))
	for name, value := range e.Env {
		merged[name] = value
	}
	for _, override := range e.RoleEngineArgs {
		if override.Role != role {
			continue
		}
		for name, value := range override.Env {
			merged[name] = value
		}
	}
	if len(merged) == 0 {
		return nil
	}
	names := make([]string, 0, len(merged))
	for name := range merged {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]corev1.EnvVar, 0, len(names))
	for _, name := range names {
		out = append(out, corev1.EnvVar{Name: name, Value: merged[name]})
	}
	return out
}

// resourceLabels returns the label set shared by the Deployment and
// Service for one inference service.
func resourceLabels(evt changeEvent) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "taas-controller",
		"go-taas.com/service-id":       evt.ServiceID,
		"app":                          evt.Name,
	}
}

// acceleratorNodeSelector maps the accelerator vendor request to a node
// label selector. A card type is resolved separately (see
// cardTypeLabelKeyFor) because the label that carries the card model is
// vendor-specific, not derived from the vendor label key.
func acceleratorNodeSelector(labelKey, accelerator string) map[string]string {
	return map[string]string{labelKey: accelerator}
}

// cardModelLabelCandidates are the node label keys that may carry a card
// model. The controller resolves a requested card type to the key a node
// actually uses, so the pod pins a real label instead of a synthesized
// "<vendor-label>-type" key that matches nothing.
var cardModelLabelCandidates = []string{
	"nvidia.com/gpu.product",
	"nvidia.com/gpu.model",
	"iluvatar.ai/gpu.product",
	"iluvatar.ai/product",
	"metax-tech.com/gpu.product",
	"metax-tech.com/product",
	"go-taas.com/accelerator-type",
	"gpu-product",
}

// cardTypeLabelKeyFor returns the node label key whose value is the given
// card type, or "" when no node reports that card. The answer is cached:
// card models rarely change and the deploy path must not list nodes per
// event. An unresolvable card type degrades the selector to the vendor
// family instead of emitting an unsatisfiable label (which would leave the
// pod Pending forever).
func (r *k8sReconciler) cardTypeLabelKeyFor(ctx context.Context, cardType string) string {
	if cardType == "" {
		return ""
	}
	r.accelKeyMu.Lock()
	defer r.accelKeyMu.Unlock()
	if r.cardTypeLabelKeys == nil {
		r.cardTypeLabelKeys = map[string]string{}
	}
	if key, ok := r.cardTypeLabelKeys[cardType]; ok {
		return key
	}
	nodes, err := r.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		// Do not cache a transient API failure.
		return ""
	}
	resolved := ""
	for _, key := range cardModelLabelCandidates {
		for _, node := range nodes.Items {
			if node.Labels[key] == cardType {
				resolved = key
				break
			}
		}
		if resolved != "" {
			break
		}
	}
	r.cardTypeLabelKeys[cardType] = resolved
	if resolved == "" {
		logger.S().Warnw("controller: no node reports the requested card type, pinning the accelerator family only",
			"card_type", cardType)
	}
	return resolved
}

// acceleratorLabelKeyFor resolves the node label key that carries the
// accelerator vendor: the configured key when the cluster's nodes carry
// it, else "gpu-vendor" when they carry that instead, else the configured
// key (so a misconfiguration fails visibly rather than silently matching
// nothing). The answer is cached: node labels rarely change, and the
// deploy path must not list nodes on every event.
func (r *k8sReconciler) acceleratorLabelKeyFor(ctx context.Context) string {
	configured := r.acceleratorLabelKey
	if configured == "" {
		configured = labelAcceleratorVendor
	}
	r.accelKeyMu.Lock()
	defer r.accelKeyMu.Unlock()
	if r.resolvedAccelLabelKey != "" {
		return r.resolvedAccelLabelKey
	}
	nodes, err := r.clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		// Fall through with the configured key: a transient API error
		// must not change the desired spec.
		return configured
	}
	found := false
	for _, node := range nodes.Items {
		if _, ok := node.Labels[configured]; ok {
			found = true
			break
		}
	}
	switch {
	case found:
		r.resolvedAccelLabelKey = configured
	case hasNodeLabel(nodes.Items, fallbackAcceleratorLabel):
		r.resolvedAccelLabelKey = fallbackAcceleratorLabel
		logger.S().Infow("controller: using fallback accelerator node label",
			"configured", configured, "using", fallbackAcceleratorLabel)
	default:
		r.resolvedAccelLabelKey = configured
	}
	return r.resolvedAccelLabelKey
}

// hasNodeLabel reports whether any node carries the label key.
func hasNodeLabel(nodes []corev1.Node, key string) bool {
	for _, node := range nodes {
		if _, ok := node.Labels[key]; ok {
			return true
		}
	}
	return false
}

// endpointFor composes the endpoint URL for a service: the configured
// base URL, or the in-cluster Service DNS name when unset.
func (r *k8sReconciler) endpointFor(name string) string {
	if r.endpointBaseURL != "" {
		return strings.TrimSuffix(r.endpointBaseURL, "/") + "/" + name + "/v1"
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local/v1", serviceName(name), r.namespace)
}

// publishStatus publishes an observed-state report on the status
// subject.
func (r *k8sReconciler) publishStatus(ctx context.Context, serviceID, state string, endpoints []string, failureReason *string) error {
	return r.publishStatusWithAutoscaling(ctx, serviceID, state, endpoints, failureReason, nil)
}

// publishStatusWithAutoscaling publishes an observed-state report with
// an optional autoscaling status block (feature #16, §6.2).
func (r *k8sReconciler) publishStatusWithAutoscaling(ctx context.Context, serviceID, state string, endpoints []string, failureReason *string, autoscaling *autoscalingStatus) error {
	if r.statusPublisher == nil {
		return nil
	}
	report := statusReport{
		ServiceID:     serviceID,
		State:         state,
		Endpoints:     endpoints,
		FailureReason: failureReason,
		Autoscaling:   autoscaling,
		ReportedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return r.statusPublisher.Publish(ctx, mq.DefaultSubjects().InferServiceStatus, body, nil)
}

// clampReplicas bounds a change event's replica count to the int32
// range the Kubernetes API accepts.
func clampReplicas(replicas int) int32 {
	if replicas < 0 {
		return 0
	}
	if replicas > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(replicas)
}

// deploymentName is the Kubernetes resource name of a service's
// Deployment.
func deploymentName(name string) string { return name }

// serviceName is the Kubernetes resource name of a service's Service.
func serviceName(name string) string { return name + "-svc" }

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }
