package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/go-taas/go-taas/pkg/k8s"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
)

// reconcileNamespace is the namespace the controller manages resources in.
const reconcileNamespace = "taas-infer"

// changeEvent mirrors the desired-state change published by the infer
// module (architecture Section 4.5.1).
type changeEvent struct {
	EventType      string `json:"event_type"`
	ServiceID      string `json:"service_id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	Model          struct {
		ModelID    string `json:"model_id"`
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
}

// statusReport is the observed-state report published on the status
// subject (architecture Section 4.5.2).
type statusReport struct {
	ServiceID     string   `json:"service_id"`
	State         string   `json:"state"`
	Endpoints     []string `json:"endpoints"`
	FailureReason *string  `json:"failure_reason"`
	ReportedAt    string   `json:"reported_at"`
}

// k8sReconciler is the default Reconciler backed by the Kubernetes
// clients. It decodes change events, drives Deployment/Service
// resources and reports observed state on the status subject.
type k8sReconciler struct {
	clientset       kubernetes.Interface
	statusPublisher mq.Client
	endpointBaseURL string
}

// NewK8sReconciler builds the default Kubernetes-backed reconciler.
// statusPublisher is the MQ client used to publish observed-state
// reports; endpointBaseURL composes endpoint URLs ("" = in-cluster DNS).
func NewK8sReconciler(client *k8s.Client, statusPublisher mq.Client, endpointBaseURL string) Reconciler {
	return newReconcilerWithClientset(client.Clientset(), statusPublisher, endpointBaseURL)
}

// newReconcilerWithClientset builds a reconciler over an arbitrary
// clientset (the fake clientset in tests).
func newReconcilerWithClientset(clientset kubernetes.Interface, statusPublisher mq.Client, endpointBaseURL string) Reconciler {
	return &k8sReconciler{
		clientset:       clientset,
		statusPublisher: statusPublisher,
		endpointBaseURL: endpointBaseURL,
	}
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
	if err := r.clientset.CoreV1().Pods(reconcileNamespace).
		Delete(ctx, podName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return warmupNodeResult{Node: node, State: "failed",
			Message: fmt.Sprintf("delete leftover pod: %v", err)}
	}

	pod := buildWarmupPod(task, podName, node)
	if _, err := r.clientset.CoreV1().Pods(reconcileNamespace).
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

// awaitPodTerminal polls the pod until it reaches a terminal phase
// (Succeeded/Failed) or a Running phase (pull done, container started).
func (r *k8sReconciler) awaitPodTerminal(ctx context.Context, podName string) (corev1.PodPhase, string, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		pod, err := r.clientset.CoreV1().Pods(reconcileNamespace).
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
			return "", "", fmt.Errorf("controller: pod %s not terminal before shutdown: %w", podName, ctx.Err())
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
func buildWarmupPod(task warmupTask, podName, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: reconcileNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":     "taas-controller",
				"taas.go-taas.github.io/task-type": "image-warmup",
				"taas.go-taas.github.io/task-id":   task.TaskID,
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

// reportWarmupFailureWithResults publishes the failed warmup status
// with per-node results and returns the wrapped error.
func (r *k8sReconciler) reportWarmupFailureWithResults(ctx context.Context, task warmupTask, reason string, results []warmupNodeResult) error {
	if len(reason) > 512 {
		reason = reason[:512]
	}
	if err := r.publishWarmupStatus(ctx, task.TaskID, "failed", results, &reason); err != nil {
		logger.S().Errorw("controller: publish warmup failed status failed",
			"task_id", task.TaskID, "err", err)
	}
	return fmt.Errorf("controller: warmup %s failed: %s", task.TaskID, reason)
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

// applyDelete tears down the Deployment and Service, ignoring
// not-found. The RPC already set state=terminated; no status report is
// needed.
func (r *k8sReconciler) applyDelete(ctx context.Context, evt changeEvent) error {
	depErr := r.clientset.AppsV1().Deployments(reconcileNamespace).
		Delete(ctx, deploymentName(evt.Name), metav1.DeleteOptions{})
	if depErr != nil && !apierrors.IsNotFound(depErr) {
		return fmt.Errorf("controller: delete deployment %s: %w", evt.Name, depErr)
	}
	svcErr := r.clientset.CoreV1().Services(reconcileNamespace).
		Delete(ctx, serviceName(evt.Name), metav1.DeleteOptions{})
	if svcErr != nil && !apierrors.IsNotFound(svcErr) {
		return fmt.Errorf("controller: delete service %s: %w", evt.Name, svcErr)
	}
	logger.S().Infow("controller: tore down inference service",
		"service_id", evt.ServiceID, "name", evt.Name)
	return nil
}

// applyUpsert reports deploying, creates or updates the Deployment and
// Service, then reports running with endpoints. Any reconcile failure
// reports failed with the reason (AC7).
func (r *k8sReconciler) applyUpsert(ctx context.Context, evt changeEvent) error {
	if err := r.publishStatus(ctx, evt.ServiceID, "deploying", nil, nil); err != nil {
		logger.S().Warnw("controller: publish deploying status failed",
			"service_id", evt.ServiceID, "err", err)
	}

	deployment := buildDeployment(evt)
	if err := r.createOrUpdateDeployment(ctx, deployment); err != nil {
		return r.reportFailure(ctx, evt, err)
	}
	svc := buildService(evt)
	if err := r.createOrUpdateService(ctx, svc); err != nil {
		return r.reportFailure(ctx, evt, err)
	}

	// Watch pod readiness: poll the Deployment's ready replicas with a
	// bounded interval. When ready, report running with endpoints.
	if err := r.awaitReadiness(ctx, evt); err != nil {
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

// reportFailure publishes the failed status with the reason and returns
// the wrapped error (AC7). A status-publish failure is logged; the
// service stays in its last known state (never silently running).
func (r *k8sReconciler) reportFailure(ctx context.Context, evt changeEvent, cause error) error {
	reason := cause.Error()
	if len(reason) > 512 {
		reason = reason[:512]
	}
	if err := r.publishStatus(ctx, evt.ServiceID, "failed", nil, &reason); err != nil {
		logger.S().Errorw("controller: publish failed status failed",
			"service_id", evt.ServiceID, "err", err)
	}
	return fmt.Errorf("controller: reconcile %s failed: %w", evt.Name, cause)
}

// awaitReadiness polls the Deployment until its ready replicas match
// the desired count or the context is cancelled.
func (r *k8sReconciler) awaitReadiness(ctx context.Context, evt changeEvent) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		dep, err := r.clientset.AppsV1().Deployments(reconcileNamespace).
			Get(ctx, deploymentName(evt.Name), metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("controller: get deployment %s: %w", evt.Name, err)
		}
		if dep.Status.ReadyReplicas >= clampReplicas(evt.Replicas) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("controller: deployment %s not ready before shutdown: %w", evt.Name, ctx.Err())
		case <-ticker.C:
			// keep polling
		}
	}
}

// createOrUpdateDeployment creates the Deployment or patches the
// desired spec (idempotent reconcile).
func (r *k8sReconciler) createOrUpdateDeployment(ctx context.Context, dep *appsv1.Deployment) error {
	_, err := r.clientset.AppsV1().Deployments(reconcileNamespace).
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
	_, err = r.clientset.AppsV1().Deployments(reconcileNamespace).
		Patch(ctx, dep.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}

// createOrUpdateService creates the Service or patches the desired
// spec (idempotent reconcile).
func (r *k8sReconciler) createOrUpdateService(ctx context.Context, svc *corev1.Service) error {
	_, err := r.clientset.CoreV1().Services(reconcileNamespace).
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
	_, err = r.clientset.CoreV1().Services(reconcileNamespace).
		Patch(ctx, svc.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}

// buildDeployment composes the Deployment for a change event: engine
// image, replicas, weight-volume mount and accelerator node selector.
func buildDeployment(evt changeEvent) *appsv1.Deployment {
	labels := resourceLabels(evt)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName(evt.Name),
			Namespace: reconcileNamespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(clampReplicas(evt.Replicas)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "engine",
						Image: evt.Image.Reference,
						Ports: []corev1.ContainerPort{{ContainerPort: 8000, Name: "http"}},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "weights",
							MountPath: "/data/weights",
							ReadOnly:  true,
							SubPath:   evt.Model.WeightPath,
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "weights",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: "model-weights",
							},
						},
					}},
					NodeSelector: acceleratorNodeSelector(evt.Accelerator, evt.AcceleratorType),
				},
			},
		},
	}
}

// buildService composes the ClusterIP Service fronting the pods.
func buildService(evt changeEvent) *corev1.Service {
	labels := resourceLabels(evt)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName(evt.Name),
			Namespace: reconcileNamespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt32(8000),
			}},
		},
	}
}

// resourceLabels returns the label set shared by the Deployment and
// Service for one inference service.
func resourceLabels(evt changeEvent) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by":      "taas-controller",
		"taas.go-taas.github.io/service-id": evt.ServiceID,
		"app":                               evt.Name,
	}
}

// acceleratorNodeSelector maps the accelerator request to node labels.
func acceleratorNodeSelector(accelerator, acceleratorType string) map[string]string {
	selector := map[string]string{
		"taas.go-taas.github.io/accelerator": accelerator,
	}
	if acceleratorType != "" {
		selector["taas.go-taas.github.io/accelerator-type"] = acceleratorType
	}
	return selector
}

// endpointFor composes the endpoint URL for a service: the configured
// base URL, or the in-cluster Service DNS name when unset.
func (r *k8sReconciler) endpointFor(name string) string {
	if r.endpointBaseURL != "" {
		return strings.TrimSuffix(r.endpointBaseURL, "/") + "/" + name + "/v1"
	}
	return fmt.Sprintf("http://%s.%s.svc.cluster.local/v1", serviceName(name), reconcileNamespace)
}

// publishStatus publishes an observed-state report on the status
// subject.
func (r *k8sReconciler) publishStatus(ctx context.Context, serviceID, state string, endpoints []string, failureReason *string) error {
	if r.statusPublisher == nil {
		return nil
	}
	report := statusReport{
		ServiceID:     serviceID,
		State:         state,
		Endpoints:     endpoints,
		FailureReason: failureReason,
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
