package controller

import (
	"context"
	"encoding/json"
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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	// Topology is the serving layout: single, distributed or pd. It decides
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
}

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

// SetGatewayRouting names the AIBrix Gateway that model HTTPRoutes attach
// to. The namespace is also where the routes are written, because Gateway
// API only routes to a backend in another namespace through a route in the
// Gateway's own namespace plus a ReferenceGrant. Empty values keep the
// shipped defaults.
func (r *k8sReconciler) SetGatewayRouting(namespace, gateway string) {
	r.gatewayNamespace = namespace
	r.gatewayName = gateway
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
// Deployment an older controller created for the same service, ignoring
// not-found. The RPC already set state=terminated; no status report is
// needed.
func (r *k8sReconciler) applyDelete(ctx context.Context, evt changeEvent) error {
	depErr := r.clientset.AppsV1().Deployments(r.namespace).
		Delete(ctx, deploymentName(evt.Name), metav1.DeleteOptions{})
	if depErr != nil && !apierrors.IsNotFound(depErr) {
		return fmt.Errorf("controller: delete deployment %s: %w", evt.Name, depErr)
	}
	svcErr := r.clientset.CoreV1().Services(r.namespace).
		Delete(ctx, serviceName(evt.Name), metav1.DeleteOptions{})
	if svcErr != nil && !apierrors.IsNotFound(svcErr) {
		return fmt.Errorf("controller: delete service %s: %w", evt.Name, svcErr)
	}
	// Autoscaling is the AIBrix PodAutoscaler; the HPA delete sweeps an
	// object an older controller left behind.
	if err := r.deleteLegacyHPA(ctx, evt.Name); err != nil {
		return err
	}
	// The storm is the serving object: a failed delete is a real error
	// (something would keep serving after the service was removed).
	if err := deleteStormService(ctx, r.dynamic, r.namespace, stormServiceName(evt)); err != nil {
		return fmt.Errorf("controller: delete stormservice %s: %w", evt.Name, err)
	}
	// Feature #47: tear down the AIBrix ModelClaim and PodAutoscaler
	// best-effort (ignoring absence and write failures).
	if err := deleteModelClaim(ctx, r.dynamic, r.namespace, evt.Name); err != nil {
		logger.S().Warnw("controller: delete aibrix modelclaim failed", "name", evt.Name, "err", err)
	}
	if err := r.deleteModelRoute(ctx, evt); err != nil {
		logger.S().Warnw("controller: delete aibrix model route failed", "name", evt.Name, "err", err)
	}
	deletePodAutoscaler(ctx, r.dynamic, r.namespace, evt.Name)
	logger.S().Infow("controller: tore down inference service",
		"service_id", evt.ServiceID, "name", evt.Name)
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
	return applyPodAutoscaler(ctx, r.dynamic, r.namespace, stormServiceName(evt), minReplicas, maxReplicas, target)
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
	spec, annotations := r.buildPodTemplate(ctx, evt, "")
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
func (r *k8sReconciler) buildPodTemplate(ctx context.Context, evt changeEvent, engineRole string) (corev1.PodSpec, map[string]string) {
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
	resources := engineResources(evt.Accelerator)
	if slice {
		resources = sliceResources(evt.Accelerator, evt.SliceMemoryMiB, evt.SliceComputePct)
		annotations[r.policyAnnotation] = r.placementPolicy
		nodeSelector = mergeSelectors(nodeSelector, r.poolSelector)
	}
	spec := corev1.PodSpec{
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
			Command: engineCommand(evt.Image.Engine, r.weights.MountPath, evt.Model.WeightPath, servedModelName(evt), evt.EngineArgs, evt.ExtraArgs, engineRole, evt),
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
	name := gpuResourceName(accelerator)
	if name == "" {
		return corev1.ResourceRequirements{}
	}
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceName(name): resource.MustParse("1"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceName(name): resource.MustParse("1"),
		},
	}
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

// engineCommand builds the engine container command that serves the
// model at the weights mount path. The whole JuiceFS filesystem is
// mounted at mountPath; the model lives in the subdirectory named by
// weightPath (e.g. "Qwen__Qwen2.5-0.5B-Instruct/"). The command is
// engine-specific; unknown engines get no command (the image's default
// entrypoint runs).
//
// engineRole is "" for a plain worker pod and "prefill"/"decode" for a
// PD-disaggregated role. The topology flags (tensor/pipeline parallelism and
// the PD role) are derived from evt and injected only when the operator did
// not already set them, so an explicit engine argument always wins.
func engineCommand(engine, mountPath, weightPath, servedName string, engineArgs map[string]string, extraArgs []string, engineRole string, evt changeEvent) []string {
	args := topologyEngineArgs(engine, engineRole, evt)
	switch engine {
	case "vllm":
		base := []string{
			"python3", "-m", "vllm.entrypoints.openai.api_server",
			"--model", modelDir(mountPath, weightPath),
			"--port", strconv.Itoa(enginePort),
			"--host", "0.0.0.0",
		}
		// The engine must accept the name clients send as `model`, which
		// is the catalog name rather than the weights directory.
		if servedName != "" {
			base = append(base, "--served-model-name", servedName)
		}
		base = append(base, args...)
		return appendEngineArgs(base, engineArgs, extraArgs)
	case "sglang":
		base := []string{
			"python3", "-m", "sglang.launch_server",
			"--model-path", modelDir(mountPath, weightPath),
			"--port", strconv.Itoa(enginePort),
			"--host", "0.0.0.0",
		}
		if servedName != "" {
			base = append(base, "--served-model-name", servedName)
		}
		base = append(base, args...)
		return appendEngineArgs(base, engineArgs, extraArgs)
	default:
		return nil
	}
}

// topologyEngineArgs returns the flags a serving topology needs, in the
// order they are appended to the engine command. Only the parameters the
// operator cannot express in the generic form are derived here:
//
//	distributed  --tensor-parallel-size / --pipeline-parallel-size
//	pd           the same, plus the role marker: vLLM's kv-transfer-config
//	             (kv_role producer/consumer) or SGLang's
//	             --disaggregation-mode prefill/decode
//
// Every one of them is skipped when the operator already supplied the flag,
// so the explicit engine arguments stay authoritative.
func topologyEngineArgs(engine, engineRole string, evt changeEvent) []string {
	if evt.Topology != topologyDistributed && evt.Topology != topologyPD {
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
	if _, set := evt.EngineArgs["tensor-parallel-size"]; !set && tp > 1 {
		args = append(args, "--tensor-parallel-size", strconv.Itoa(tp))
	}
	if _, set := evt.EngineArgs["pipeline-parallel-size"]; !set && pp > 1 {
		args = append(args, "--pipeline-parallel-size", strconv.Itoa(pp))
	}
	if engineRole == "" {
		return args
	}
	switch engine {
	case "vllm":
		if _, set := evt.EngineArgs["kv-transfer-config"]; set {
			return args
		}
		kvRole := "kv_producer"
		if engineRole == stormRoleDecode {
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
		if _, set := evt.EngineArgs["disaggregation-mode"]; set {
			return args
		}
		args = append(args, "--disaggregation-mode", engineRole)
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
