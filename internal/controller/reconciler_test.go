package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/go-taas/go-taas/pkg/k8s"
	"github.com/go-taas/go-taas/pkg/mq"
)

func testChangeEvent(eventType, name string) changeEvent {
	evt := changeEvent{
		EventType:      eventType,
		ServiceID:      "svc-1",
		OrganizationID: "org-1",
		Name:           name,
		Replicas:       2,
		Accelerator:    "nvidia",
		AcceleratorType: "A100",
	}
	evt.Model.ModelID = "model-1"
	evt.Model.Version = "v1"
	evt.Model.WeightPath = "qwen/v1"
	evt.Image.ImageID = "img-1"
	evt.Image.Reference = "ghcr.io/go-taas/vllm:v0.6.3"
	evt.Image.Engine = "vllm"
	return evt
}

func TestBuildDeployment(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	dep := buildDeployment(evt)

	assert.Equal(t, "demo", dep.Name)
	assert.Equal(t, reconcileNamespace, dep.Namespace)
	require.NotNil(t, dep.Spec.Replicas)
	assert.Equal(t, int32(2), *dep.Spec.Replicas)

	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	c := dep.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "ghcr.io/go-taas/vllm:v0.6.3", c.Image)
	require.Len(t, c.VolumeMounts, 1)
	assert.Equal(t, "qwen/v1", c.VolumeMounts[0].SubPath)
	assert.Equal(t, "/data/weights", c.VolumeMounts[0].MountPath)

	// Accelerator node selector.
	assert.Equal(t, "nvidia", dep.Spec.Template.Spec.NodeSelector["taas.go-taas.github.io/accelerator"])
	assert.Equal(t, "A100", dep.Spec.Template.Spec.NodeSelector["taas.go-taas.github.io/accelerator-type"])

	// Labels are consistent between selector and template.
	assert.Equal(t, dep.Spec.Selector.MatchLabels, dep.Spec.Template.Labels)
	assert.Equal(t, "svc-1", dep.Spec.Template.Labels["taas.go-taas.github.io/service-id"])
}

func TestBuildService(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	svc := buildService(evt)

	assert.Equal(t, "demo-svc", svc.Name)
	assert.Equal(t, reconcileNamespace, svc.Namespace)
	require.Len(t, svc.Spec.Ports, 1)
	assert.Equal(t, int32(80), svc.Spec.Ports[0].Port)
	assert.Equal(t, svc.Spec.Selector, evt2Labels(evt))
}

// evt2Labels mirrors resourceLabels for the test event.
func evt2Labels(evt changeEvent) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by":          "taas-controller",
		"taas.go-taas.github.io/service-id":      evt.ServiceID,
		"app": evt.Name,
	}
}

func TestEndpointFor(t *testing.T) {
	r := &k8sReconciler{endpointBaseURL: "https://infer.example.com"}
	assert.Equal(t, "https://infer.example.com/demo/v1", r.endpointFor("demo"))

	r = &k8sReconciler{endpointBaseURL: "https://infer.example.com/"}
	assert.Equal(t, "https://infer.example.com/demo/v1", r.endpointFor("demo"))

	// Empty base URL: in-cluster DNS.
	r = &k8sReconciler{}
	assert.Equal(t, "http://demo-svc.taas-infer.svc.cluster.local/v1", r.endpointFor("demo"))
}

func TestApplyInferServiceChangeMalformed(t *testing.T) {
	r := &k8sReconciler{}

	err := r.ApplyInferServiceChange(context.Background(), mq.Message{Body: []byte("{not json")})
	require.Error(t, err)
	assert.True(t, mq.IsPermanent(err), "malformed events must be rejected, not retried")

	// Missing service id.
	err = r.ApplyInferServiceChange(context.Background(), mq.Message{Body: []byte(`{"event_type":"upsert","name":"x"}`)})
	require.Error(t, err)
	assert.True(t, mq.IsPermanent(err))

	// Unknown event type.
	body, _ := json.Marshal(testChangeEvent("explode", "demo"))
	err = r.ApplyInferServiceChange(context.Background(), mq.Message{Body: body})
	require.Error(t, err)
	assert.True(t, mq.IsPermanent(err))
}

func TestStatusReportJSONShape(t *testing.T) {
	reason := "image pull backoff"
	report := statusReport{
		ServiceID:     "svc-1",
		State:         "failed",
		Endpoints:     nil,
		FailureReason: &reason,
		ReportedAt:    "2026-01-01T00:00:00Z",
	}
	body, err := json.Marshal(report)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))
	assert.Equal(t, "svc-1", raw["service_id"])
	assert.Equal(t, "failed", raw["state"])
	assert.Equal(t, "image pull backoff", raw["failure_reason"])
	assert.Equal(t, "2026-01-01T00:00:00Z", raw["reported_at"])
}

func TestChangeEventDecodeRoundTrip(t *testing.T) {
	// The controller's changeEvent must decode the infer module's
	// published JSON (architecture Section 4.5.1).
	evt := testChangeEvent("upsert", "demo")
	body, err := json.Marshal(evt)
	require.NoError(t, err)

	var decoded changeEvent
	require.NoError(t, json.Unmarshal(body, &decoded))
	assert.Equal(t, "upsert", decoded.EventType)
	assert.Equal(t, "svc-1", decoded.ServiceID)
	assert.Equal(t, "qwen/v1", decoded.Model.WeightPath)
	assert.Equal(t, "ghcr.io/go-taas/vllm:v0.6.3", decoded.Image.Reference)
	assert.Equal(t, "vllm", decoded.Image.Engine)
	assert.Equal(t, 2, decoded.Replicas)
}

// recordingPublisher records status reports published by the reconciler.
type recordingPublisher struct {
	mq.Client
	reports []statusReport
}

func (p *recordingPublisher) Publish(_ context.Context, subject string, body []byte, _ map[string]string) error {
	if subject != mq.DefaultSubjects().InferServiceStatus {
		return nil
	}
	var report statusReport
	if err := json.Unmarshal(body, &report); err != nil {
		return err
	}
	p.reports = append(p.reports, report)
	return nil
}

// newFakeReconciler builds a reconciler over the fake clientset. A
// get-reactor reports every Deployment's desired replicas as ready, so
// awaitReadiness returns without waiting on the 2s poll ticker — for
// both the create and the patch (idempotent re-apply) paths.
func newFakeReconciler(publisher mq.Client) (Reconciler, *fake.Clientset) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok {
			return false, nil, nil
		}
		obj, err := clientset.Tracker().Get(action.GetResource(), action.GetNamespace(), getAction.GetName())
		if err != nil {
			return true, nil, err
		}
		dep, ok := obj.(*appsv1.Deployment)
		if !ok {
			return false, nil, nil
		}
		if dep.Spec.Replicas != nil {
			dep.Status.ReadyReplicas = *dep.Spec.Replicas
		}
		return true, dep, nil
	})
	return newReconcilerWithClientset(clientset, publisher, "https://infer.example.com/"), clientset
}

// markReady sets the Deployment's ready replicas so awaitReadiness
// returns.
func markReady(t *testing.T, clientset *fake.Clientset, name string, ready int32) {
	t.Helper()
	dep, err := clientset.AppsV1().Deployments(reconcileNamespace).Get(
		context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	dep.Status.ReadyReplicas = ready
	_, err = clientset.AppsV1().Deployments(reconcileNamespace).UpdateStatus(
		context.Background(), dep, metav1.UpdateOptions{})
	require.NoError(t, err)
}

// changeMessage marshals a change event into an mq.Message.
func changeMessage(evt changeEvent) mq.Message {
	body, _ := json.Marshal(evt)
	return mq.Message{Subject: mq.DefaultSubjects().InferServiceChanges, Body: body}
}

func TestApplyUpsertHappyPath(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	reconciler, clientset := newFakeReconciler(publisher)

	err := reconciler.ApplyInferServiceChange(context.Background(), changeMessage(testChangeEvent("upsert", "demo")))
	require.NoError(t, err)

	// Deployment and Service exist with the desired spec.
	dep, err := clientset.AppsV1().Deployments(reconcileNamespace).Get(
		context.Background(), "demo", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, int32(2), *dep.Spec.Replicas)
	assert.Equal(t, "ghcr.io/go-taas/vllm:v0.6.3", dep.Spec.Template.Spec.Containers[0].Image)

	svc, err := clientset.CoreV1().Services(reconcileNamespace).Get(
		context.Background(), "demo-svc", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "demo", svc.Spec.Selector["app"])

	// Status reports: deploying then running with the endpoint.
	require.Len(t, publisher.reports, 2)
	assert.Equal(t, "deploying", publisher.reports[0].State)
	assert.Equal(t, "running", publisher.reports[1].State)
	assert.Equal(t, []string{"https://infer.example.com/demo/v1"}, publisher.reports[1].Endpoints)
}

func TestApplyUpsertIdempotent(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	reconciler, clientset := newFakeReconciler(publisher)

	require.NoError(t, reconciler.ApplyInferServiceChange(context.Background(), changeMessage(testChangeEvent("upsert", "demo"))))

	// Second apply with more replicas patches instead of failing.
	evt := testChangeEvent("upsert", "demo")
	evt.Replicas = 3
	require.NoError(t, reconciler.ApplyInferServiceChange(context.Background(), changeMessage(evt)))

	dep, err := clientset.AppsV1().Deployments(reconcileNamespace).Get(
		context.Background(), "demo", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, int32(3), *dep.Spec.Replicas)
}

func TestApplyUpsertFailureReportsFailed(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	// Plain clientset: the Deployment never becomes ready.
	reconciler := newReconcilerWithClientset(fake.NewSimpleClientset(), publisher, "")

	// Cancel the context so awaitReadiness fails after the first poll.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := reconciler.ApplyInferServiceChange(ctx, changeMessage(testChangeEvent("upsert", "demo")))
	require.Error(t, err)

	// The failure was reported with a reason (AC7).
	require.NotEmpty(t, publisher.reports)
	last := publisher.reports[len(publisher.reports)-1]
	assert.Equal(t, "failed", last.State)
	require.NotNil(t, last.FailureReason)
}

func TestApplyDelete(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	reconciler, clientset := newFakeReconciler(publisher)

	// Create then delete.
	require.NoError(t, reconciler.ApplyInferServiceChange(context.Background(), changeMessage(testChangeEvent("upsert", "demo"))))

	delEvt := testChangeEvent("delete", "demo")
	require.NoError(t, reconciler.ApplyInferServiceChange(context.Background(), changeMessage(delEvt)))

	_, err := clientset.AppsV1().Deployments(reconcileNamespace).Get(
		context.Background(), "demo", metav1.GetOptions{})
	assert.Error(t, err, "deployment should be deleted")

	// Deleting again is idempotent (not-found ignored).
	require.NoError(t, reconciler.ApplyInferServiceChange(context.Background(), changeMessage(delEvt)))
}

func TestApplyImageWarmup(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	reconciler, _ := newFakeReconciler(publisher)

	err := reconciler.ApplyImageWarmup(context.Background(), mq.Message{
		Subject: mq.DefaultSubjects().ImageWarmups,
		Body:    []byte(`{"image_id":"img-1"}`),
	})
	assert.NoError(t, err)
}

func TestAwaitReadinessPolls(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	// Plain clientset: readiness only appears once markReady runs.
	clientset := fake.NewSimpleClientset()
	reconciler := newReconcilerWithClientset(clientset, publisher, "")

	evt := testChangeEvent("upsert", "demo")
	dep := buildDeployment(evt)
	_, err := clientset.AppsV1().Deployments(reconcileNamespace).Create(
		context.Background(), dep, metav1.CreateOptions{})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		markReady(t, clientset, "demo", 2)
	}()

	r := reconciler.(*k8sReconciler)
	require.NoError(t, r.awaitReadiness(ctx, evt))
}

func TestApplyUpsertWithNilPublisher(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	reconciler := newReconcilerWithClientset(clientset, nil, "")

	// The deployment never becomes ready; with a cancelled context the
	// reconcile fails but must not panic on the nil publisher.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := reconciler.ApplyInferServiceChange(ctx, changeMessage(testChangeEvent("upsert", "demo")))
	require.Error(t, err)
}

func TestApplyDeleteDeploymentMissing(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	reconciler, _ := newFakeReconciler(publisher)

	require.NoError(t, reconciler.ApplyInferServiceChange(context.Background(), changeMessage(testChangeEvent("delete", "ghost"))))
}

func TestResourceLabels(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	labels := resourceLabels(evt)
	assert.Equal(t, "taas-controller", labels["app.kubernetes.io/managed-by"])
	assert.Equal(t, "svc-1", labels["taas.go-taas.github.io/service-id"])
	assert.Equal(t, "demo", labels["app"])
}

func TestAcceleratorNodeSelector(t *testing.T) {
	selector := acceleratorNodeSelector("nvidia", "")
	assert.Equal(t, map[string]string{"taas.go-taas.github.io/accelerator": "nvidia"}, selector)

	selector = acceleratorNodeSelector("metax", "m100")
	assert.Equal(t, "metax", selector["taas.go-taas.github.io/accelerator"])
	assert.Equal(t, "m100", selector["taas.go-taas.github.io/accelerator-type"])
}

func TestDeploymentNameAndServiceName(t *testing.T) {
	assert.Equal(t, "demo", deploymentName("demo"))
	assert.Equal(t, "demo-svc", serviceName("demo"))
}

func TestBuildDeploymentDefaults(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Replicas = 0
	dep := buildDeployment(evt)
	assert.Equal(t, int32(0), *dep.Spec.Replicas)
}

func TestNewK8sReconciler(t *testing.T) {
	// The constructor wires the clientset through; reconcile paths are
	// covered by the fake-clientset tests above.
	reconciler := NewK8sReconciler(&k8s.Client{}, mq.NewFake(), "https://infer.example.com")
	assert.NotNil(t, reconciler)
}

func TestControllerRunSubscribes(t *testing.T) {
	bus := mq.NewFake()
	publisher := &recordingPublisher{Client: mq.NewFake()}
	reconciler, clientset := newFakeReconciler(publisher)

	ctrl := New(bus, reconciler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ctrl.Run(ctx) }()

	// Wait for the subscriptions to register before publishing.
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, bus.Publish(ctx, mq.DefaultSubjects().InferServiceChanges, changeMessage(testChangeEvent("upsert", "demo")).Body, nil))

	require.Eventually(t, func() bool {
		_, err := clientset.AppsV1().Deployments(reconcileNamespace).Get(
			ctx, "demo", metav1.GetOptions{})
		return err == nil
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
