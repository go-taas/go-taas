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
	EventType       string `json:"event_type"`
	ServiceID       string `json:"service_id"`
	OrganizationID  string `json:"organization_id"`
	Name            string `json:"name"`
	Model           struct {
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

// ApplyImageWarmup implements Reconciler. Image pre-pull is feature #3;
// the event is acknowledged and logged until then.
func (r *k8sReconciler) ApplyImageWarmup(_ context.Context, msg mq.Message) error {
	logger.S().Infow("controller: image warmup not implemented (feature #3)",
		"subject", msg.Subject, "body_bytes", len(msg.Body))
	return nil
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
		"app.kubernetes.io/managed-by": "taas-controller",
		"taas.go-taas.github.io/service-id": evt.ServiceID,
		"app": evt.Name,
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
