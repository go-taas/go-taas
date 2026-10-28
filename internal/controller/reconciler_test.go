package controller

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/go-taas/go-taas/pkg/k8s"
	"github.com/go-taas/go-taas/pkg/mq"
)

// reconcileNamespace is the default namespace the reconciler uses when
// none is configured (mirrors the production default).
const reconcileNamespace = "taas-infer"

// testReconciler returns a reconciler over a fake clientset for the
// build-function tests.
// testReconciler builds a reconciler over a fleet with one A100 node, so
// the card type in testChangeEvent resolves to a real node label.
func testReconciler() *k8sReconciler {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "gpu-node-1",
		Labels: map[string]string{
			labelAcceleratorVendor:   "nvidia",
			"nvidia.com/gpu.product": "A100",
		},
	}}
	return &k8sReconciler{
		clientset: fake.NewSimpleClientset(node),
		namespace: reconcileNamespace,
		weights:   WeightsConfig{PVCName: "model-weights", MountPath: "/data/weights"},
	}
}

func testChangeEvent(eventType, name string) changeEvent {
	evt := changeEvent{
		EventType:       eventType,
		ServiceID:       "svc-1",
		OrganizationID:  "org-1",
		Name:            name,
		Replicas:        2,
		Accelerator:     "nvidia",
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
	dep := testReconciler().buildDeployment(context.Background(), evt)

	assert.Equal(t, "demo", dep.Name)
	assert.Equal(t, reconcileNamespace, dep.Namespace)
	require.NotNil(t, dep.Spec.Replicas)
	assert.Equal(t, int32(2), *dep.Spec.Replicas)

	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	c := dep.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "ghcr.io/go-taas/vllm:v0.6.3", c.Image)
	require.Len(t, c.VolumeMounts, 1)
	// The whole JuiceFS filesystem is mounted (no SubPath); the model
	// lives in a subdirectory named by its weight path.
	assert.Equal(t, "", c.VolumeMounts[0].SubPath)
	assert.Equal(t, "/data/weights", c.VolumeMounts[0].MountPath)

	// The engine requests the accelerator's extended resource so the
	// scheduler places it on a node with free capacity.
	require.NotNil(t, c.Resources.Limits)
	assert.Equal(t, resource.MustParse("1"), c.Resources.Limits[corev1.ResourceName("nvidia.com/gpu")])
	require.NotNil(t, c.Resources.Requests)
	assert.Equal(t, resource.MustParse("1"), c.Resources.Requests[corev1.ResourceName("nvidia.com/gpu")])

	// The engine command points at the model's weights directory and
	// serves under the name clients send as `model`.
	assert.Equal(t, []string{
		"python3", "-m", "vllm.entrypoints.openai.api_server",
		"--model", "/data/weights/qwen/v1", "--port", "8000", "--host", "0.0.0.0",
		"--served-model-name", "demo",
	}, c.Command)

	// Accelerator node selector.
	assert.Equal(t, "nvidia", dep.Spec.Template.Spec.NodeSelector["go-taas.com/accelerator"])
	assert.Equal(t, "A100", dep.Spec.Template.Spec.NodeSelector["nvidia.com/gpu.product"],
		"the card model is pinned through the label that carries it")

	// The selector keeps the shared resource labels (it is immutable); the
	// template adds the serving-gateway routing labels on top.
	assert.Equal(t, evt2Labels(evt), dep.Spec.Selector.MatchLabels)
	for key, value := range evt2Labels(evt) {
		assert.Equal(t, value, dep.Spec.Template.Labels[key])
	}
	assert.Equal(t, "svc-1", dep.Spec.Template.Labels["go-taas.com/service-id"])
	assert.Equal(t, "demo", dep.Spec.Template.Labels[modelNameLabel])
	assert.Equal(t, "8000", dep.Spec.Template.Labels[modelPortLabel])
	assert.Equal(t, "demo", dep.Spec.Template.Annotations[modelNameLabel])
	assert.Equal(t, "demo-svc", dep.Spec.Template.Annotations[modelServiceAnno])
	assert.Equal(t, "demo", dep.Labels[modelNameLabel], "the gateway derives the route from the Deployment")
}

func TestBuildService(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	svc := testReconciler().buildService(evt)

	assert.Equal(t, "demo-svc", svc.Name)
	assert.Equal(t, reconcileNamespace, svc.Namespace)
	require.Len(t, svc.Spec.Ports, 2)
	assert.Equal(t, int32(80), svc.Spec.Ports[0].Port)
	// The gateway's model route addresses the backend by the engine port.
	assert.Equal(t, int32(enginePort), svc.Spec.Ports[1].Port)
	assert.Equal(t, intstr.FromInt32(enginePort), svc.Spec.Ports[1].TargetPort)
	assert.Equal(t, svc.Spec.Selector, evt2Labels(evt))
}

// evt2Labels mirrors resourceLabels for the test event.
func evt2Labels(evt changeEvent) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "taas-controller",
		"go-taas.com/service-id":       evt.ServiceID,
		"app":                          evt.Name,
	}
}

func TestEndpointFor(t *testing.T) {
	r := &k8sReconciler{endpointBaseURL: "https://infer.example.com"}
	assert.Equal(t, "https://infer.example.com/demo/v1", r.endpointFor("demo"))

	r = &k8sReconciler{endpointBaseURL: "https://infer.example.com/"}
	assert.Equal(t, "https://infer.example.com/demo/v1", r.endpointFor("demo"))

	// Empty base URL: in-cluster DNS.
	r = &k8sReconciler{namespace: reconcileNamespace}
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
	reports       []statusReport
	warmupReports []warmupStatus
}

func (p *recordingPublisher) Publish(_ context.Context, subject string, body []byte, _ map[string]string) error {
	switch subject {
	case mq.DefaultSubjects().InferServiceStatus:
		var report statusReport
		if err := json.Unmarshal(body, &report); err != nil {
			return err
		}
		p.reports = append(p.reports, report)
	case mq.DefaultSubjects().ImageWarmupStatus:
		var report warmupStatus
		if err := json.Unmarshal(body, &report); err != nil {
			return err
		}
		p.warmupReports = append(p.warmupReports, report)
	}
	return nil
}

// newFakeReconciler builds a reconciler over the fake clientset. A
// get-reactor reports every Deployment's desired replicas as ready, so
// awaitEngineReady returns without waiting on the poll ticker — for
// both the create and the patch (idempotent re-apply) paths. A second
// get-reactor reports every warmup helper pod as Running, so
// awaitPodTerminal returns immediately.
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
	clientset.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		getAction, ok := action.(k8stesting.GetAction)
		if !ok {
			return false, nil, nil
		}
		obj, err := clientset.Tracker().Get(action.GetResource(), action.GetNamespace(), getAction.GetName())
		if err != nil {
			return true, nil, err
		}
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return false, nil, nil
		}
		pod.Status.Phase = corev1.PodRunning
		return true, pod, nil
	})
	rec := newReconcilerWithClientset(clientset, nil, publisher, "https://infer.example.com/", "", WeightsConfig{}, nil)
	stubScheduledPod(clientset)
	stubEngineReady(rec)
	return rec, clientset
}

// stubScheduledPod makes the fake cluster look like a deploy that reached
// the engine start: a pod exists, the scheduler placed it on a node and it
// has an address. Pods the test itself created (a warmup helper, for
// example) win, so a test that tracks its own pod still sees it.
func stubScheduledPod(clientset *fake.Clientset) {
	clientset.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listAction, ok := action.(k8stesting.ListAction)
		if !ok {
			return false, nil, nil
		}
		if tracked, err := clientset.Tracker().List(
			listAction.GetResource(), corev1.SchemeGroupVersion.WithKind("Pod"),
			listAction.GetNamespace(),
		); err == nil && len(tracked.(*corev1.PodList).Items) > 0 {
			return false, nil, nil
		}
		return true, &corev1.PodList{
			ListMeta: metav1.ListMeta{ResourceVersion: "1"},
			Items: []corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "scheduled-pod",
					Namespace: listAction.GetNamespace(),
					// The reconciler selects on the service id, so the
					// stub carries the id the tests use.
					Labels: map[string]string{serviceLabelKey: "svc-1"},
				},
				Spec:   corev1.PodSpec{NodeName: "node-1"},
				Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.7"},
			}},
		}, nil
	})
}

// stubEngineReady makes the readiness wait return immediately.
func stubEngineReady(r Reconciler) {
	r.(*k8sReconciler).engineProber = func(context.Context, string, int) bool { return true }
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

	// Status reports: queued when the change is accepted, starting when the
	// pod is scheduled, then running with the endpoint.
	require.Len(t, publisher.reports, 3)
	assert.Equal(t, "queued", publisher.reports[0].State)
	assert.Equal(t, "starting", publisher.reports[1].State)
	assert.Equal(t, "running", publisher.reports[2].State)
	assert.Equal(t, []string{"https://infer.example.com/demo/v1"}, publisher.reports[2].Endpoints)
}

func TestApplyUpdateVersionReusesUpsert(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	reconciler, clientset := newFakeReconciler(publisher)

	// A version-change event (feature #32, AD6) reconciles the
	// Deployment with the new weights while keeping the service identity.
	evt := testChangeEvent("update_version", "demo")
	evt.Model.Version = "v2"
	evt.Model.WeightPath = "qwen/v2"
	err := reconciler.ApplyInferServiceChange(context.Background(), changeMessage(evt))
	require.NoError(t, err)

	// The Deployment is created with the new weight path.
	dep, err := clientset.AppsV1().Deployments(reconcileNamespace).Get(
		context.Background(), "demo", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"python3", "-m", "vllm.entrypoints.openai.api_server",
		"--model", "/data/weights/qwen/v2", "--port", "8000", "--host", "0.0.0.0",
		"--served-model-name", "demo",
	}, dep.Spec.Template.Spec.Containers[0].Command)

	// The Service keeps the same identity.
	svc, err := clientset.CoreV1().Services(reconcileNamespace).Get(
		context.Background(), "demo-svc", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "demo", svc.Spec.Selector["app"])

	// Status reports: queued, starting, then running.
	require.Len(t, publisher.reports, 3)
	assert.Equal(t, "queued", publisher.reports[0].State)
	assert.Equal(t, "starting", publisher.reports[1].State)
	assert.Equal(t, "running", publisher.reports[2].State)
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
	reconciler := newReconcilerWithClientset(fake.NewSimpleClientset(), nil, publisher, "", "", WeightsConfig{}, nil)

	// Cancel the context so the readiness wait fails after the first poll.
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
	reconciler, clientset := newFakeReconciler(publisher)

	// One node labeled nvidia; the task selector matches it.
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", Labels: map[string]string{"go-taas.com/accelerator": "nvidia"}}}
	_, err := clientset.CoreV1().Nodes().Create(context.Background(), node, metav1.CreateOptions{})
	require.NoError(t, err)

	task := `{"task_id":"11111111-2222-3333-4444-555555555555","image_id":"img-1","reference":"ghcr.io/go-taas/vllm:v0.6.3","node_selector":{"go-taas.com/accelerator":"nvidia"}}`
	err = reconciler.ApplyImageWarmup(context.Background(), mq.Message{
		Subject: mq.DefaultSubjects().ImageWarmups,
		Body:    []byte(task),
	})
	require.NoError(t, err)

	// The helper pod was created on the node with imagePullPolicy=Always.
	pods, err := clientset.CoreV1().Pods(reconcileNamespace).List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, pods.Items, 1)
	pod := pods.Items[0]
	assert.Equal(t, "node-a", pod.Spec.NodeName)
	assert.Equal(t, corev1.PullAlways, pod.Spec.Containers[0].ImagePullPolicy)
	assert.Equal(t, "ghcr.io/go-taas/vllm:v0.6.3", pod.Spec.Containers[0].Image)

	// The running status was published.
	require.NotEmpty(t, publisher.warmupReports)
	assert.Equal(t, "running", publisher.warmupReports[0].State)
}

func TestApplyImageWarmupNoMatchingNode(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	reconciler, _ := newFakeReconciler(publisher)

	task := `{"task_id":"11111111-2222-3333-4444-555555555555","image_id":"img-1","reference":"ghcr.io/go-taas/vllm:v0.6.3","node_selector":{"go-taas.com/accelerator":"metax"}}`
	err := reconciler.ApplyImageWarmup(context.Background(), mq.Message{
		Subject: mq.DefaultSubjects().ImageWarmups,
		Body:    []byte(task),
	})
	require.Error(t, err)
	// The failure is permanent: the task is terminal and already
	// reported, so the broker must not redeliver it.
	assert.True(t, mq.IsPermanent(err), "a reported failure is not retried")

	// The failure was reported with a reason.
	require.NotEmpty(t, publisher.warmupReports)
	last := publisher.warmupReports[len(publisher.warmupReports)-1]
	assert.Equal(t, "failed", last.State)
	require.NotNil(t, last.FailureReason)
	assert.Contains(t, *last.FailureReason, "no node matches")
}

func TestApplyImageWarmupMalformed(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	reconciler, _ := newFakeReconciler(publisher)

	err := reconciler.ApplyImageWarmup(context.Background(), mq.Message{
		Subject: mq.DefaultSubjects().ImageWarmups,
		Body:    []byte(`{not json`),
	})
	require.Error(t, err)
	assert.True(t, mq.IsPermanent(err))
}

// TestApplyUpsertReportsQueuedThenStartingThenRunning verifies the state
// follows the cluster: queued when the controller accepts the change,
// starting once the scheduler placed a pod, and running only when the
// engine itself answers.
func TestApplyUpsertReportsQueuedThenStartingThenRunning(t *testing.T) {
	publisher := &recordingPublisher{Client: mq.NewFake()}
	clientset := fake.NewSimpleClientset()
	rec := newReconcilerWithClientset(clientset, nil, publisher, "", "", WeightsConfig{}, nil)
	r := rec.(*k8sReconciler)
	probes := 0
	r.engineProber = func(context.Context, string, int) bool {
		probes++
		// The engine needs a second probe to answer, so the loop reports
		// starting before it reports running.
		return probes > 1
	}
	stubScheduledPod(clientset)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, r.applyUpsert(ctx, testChangeEvent("upsert", "demo")))

	states := make([]string, 0, len(publisher.reports))
	for _, report := range publisher.reports {
		states = append(states, report.State)
	}
	require.NotEmpty(t, states)
	assert.Equal(t, "queued", states[0], "the first report is queued")
	assert.Contains(t, states, "starting", "a scheduled pod reports starting")
	assert.Equal(t, "running", states[len(states)-1], "running comes last")
}

func TestApplyUpsertWithNilPublisher(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	reconciler := newReconcilerWithClientset(clientset, nil, nil, "", "", WeightsConfig{}, nil)

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
	assert.Equal(t, "svc-1", labels["go-taas.com/service-id"])
	assert.Equal(t, "demo", labels["app"])
}

func TestAcceleratorNodeSelector(t *testing.T) {
	// The vendor family is pinned by the vendor label; a card type is
	// resolved to the label that actually carries it (see
	// TestBuildDeploymentPinsCardModelLabel).
	selector := acceleratorNodeSelector(labelAcceleratorVendor, "nvidia")
	assert.Equal(t, map[string]string{"go-taas.com/accelerator": "nvidia"}, selector)

	selector = acceleratorNodeSelector("gpu-vendor", "metax")
	assert.Equal(t, map[string]string{"gpu-vendor": "metax"}, selector)
}

func TestGPUResourceName(t *testing.T) {
	assert.Equal(t, "nvidia.com/gpu", gpuResourceName("nvidia"))
	assert.Equal(t, "iluvatar.ai/vgpu", gpuResourceName("iluvatar"))
	assert.Equal(t, "metax-tech.com/gpu", gpuResourceName("metax"))
	assert.Equal(t, "", gpuResourceName("tpu"))
	assert.Equal(t, "", gpuResourceName(""))
}

func TestEngineResources(t *testing.T) {
	res := engineResources("nvidia")
	require.NotNil(t, res.Limits)
	assert.Equal(t, resource.MustParse("1"), res.Limits[corev1.ResourceName("nvidia.com/gpu")])
	require.NotNil(t, res.Requests)
	assert.Equal(t, resource.MustParse("1"), res.Requests[corev1.ResourceName("nvidia.com/gpu")])

	// Unknown accelerators get no resource request.
	assert.Empty(t, engineResources("tpu").Limits)
	assert.Empty(t, engineResources("").Limits)
}

func TestEngineCommand(t *testing.T) {
	assert.Equal(t, []string{
		"python3", "-m", "vllm.entrypoints.openai.api_server",
		"--model", "/data/weights/qwen/v1", "--port", "8000", "--host", "0.0.0.0",
		"--served-model-name", "qwen2.5-0.5b",
	}, engineCommand("vllm", "/data/weights", "qwen/v1", "qwen2.5-0.5b", nil, nil, "", changeEvent{}))

	// An empty name leaves the weights path as the served name, which is
	// what the engine defaults to.
	assert.Equal(t, []string{
		"python3", "-m", "vllm.entrypoints.openai.api_server",
		"--model", "/data/weights/qwen/v1", "--port", "8000", "--host", "0.0.0.0",
	}, engineCommand("vllm", "/data/weights", "qwen/v1", "", nil, nil, "", changeEvent{}))

	// SGLang serves on its own module and takes --model-path.
	assert.Equal(t, []string{
		"python3", "-m", "sglang.launch_server",
		"--model-path", "/data/weights/qwen/v1", "--port", "8000", "--host", "0.0.0.0",
		"--served-model-name", "m",
	}, engineCommand("sglang", "/data/weights", "qwen/v1", "m", nil, nil, "", changeEvent{}))

	// Unknown engines get no command (the image's default entrypoint runs).
	assert.Nil(t, engineCommand("", "/data/weights", "qwen/v1", "m", nil, nil, "", changeEvent{}))
}

func TestModelDir(t *testing.T) {
	assert.Equal(t, "/data/weights/qwen/v1", modelDir("/data/weights", "qwen/v1"))
	assert.Equal(t, "/data/weights/qwen/v1", modelDir("/data/weights", "qwen/v1/"))
	assert.Equal(t, "/data/weights", modelDir("/data/weights", ""))
	assert.Equal(t, "/data/weights", modelDir("/data/weights", "/"))
}

func TestDeploymentNameAndServiceName(t *testing.T) {
	assert.Equal(t, "demo", deploymentName("demo"))
	assert.Equal(t, "demo-svc", serviceName("demo"))
}

func TestBuildDeploymentDefaults(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Replicas = 0
	dep := testReconciler().buildDeployment(context.Background(), evt)
	assert.Equal(t, int32(0), *dep.Spec.Replicas)
}

func TestBuildDeploymentCustomWeights(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	r := &k8sReconciler{
		clientset: fake.NewSimpleClientset(),
		namespace: reconcileNamespace,
		weights:   WeightsConfig{PVCName: "my-weights", MountPath: "/models"},
	}
	dep := r.buildDeployment(context.Background(), evt)
	c := dep.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "/models", c.VolumeMounts[0].MountPath)
	assert.Equal(t, "my-weights", dep.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName)
}

func TestEnsureWeightsPVCCreates(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	r := &k8sReconciler{
		clientset: clientset,
		namespace: reconcileNamespace,
		weights:   WeightsConfig{PVCName: "model-weights", StorageClass: "juicefs-taas-models", MountPath: "/data/weights"},
	}
	ctx := context.Background()

	require.NoError(t, r.ensureWeightsPVC(ctx))

	pvc, err := clientset.CoreV1().PersistentVolumeClaims(reconcileNamespace).
		Get(ctx, "model-weights", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, pvc.Spec.StorageClassName)
	assert.Equal(t, "juicefs-taas-models", *pvc.Spec.StorageClassName)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, pvc.Spec.AccessModes)

	// Idempotent: a second call does not error.
	require.NoError(t, r.ensureWeightsPVC(ctx))
}

func TestEnsureWeightsPVCNoStorageClass(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	r := &k8sReconciler{
		clientset: clientset,
		namespace: reconcileNamespace,
		weights:   WeightsConfig{PVCName: "model-weights", MountPath: "/data/weights"},
	}
	ctx := context.Background()

	require.NoError(t, r.ensureWeightsPVC(ctx))
	pvc, err := clientset.CoreV1().PersistentVolumeClaims(reconcileNamespace).
		Get(ctx, "model-weights", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Nil(t, pvc.Spec.StorageClassName)
}

func TestNewK8sReconciler(t *testing.T) {
	// The constructor wires the clientset through; reconcile paths are
	// covered by the fake-clientset tests above.
	reconciler := NewK8sReconciler(&k8s.Client{}, mq.NewFake(), "https://infer.example.com", "", WeightsConfig{}, nil)
	assert.NotNil(t, reconciler)
}

func TestPodFailureMessage(t *testing.T) {
	// Waiting container with a reason.
	pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "pull failed"}},
	}}}}
	assert.Equal(t, "ErrImagePull: pull failed", podFailureMessage(pod))

	// Terminated container.
	pod = &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", Message: "out of memory"}},
	}}}}
	assert.Equal(t, "OOMKilled: out of memory", podFailureMessage(pod))

	// No container statuses.
	assert.Equal(t, "pod failed", podFailureMessage(&corev1.Pod{}))
}

func TestClampReplicas(t *testing.T) {
	assert.Equal(t, int32(0), clampReplicas(-5))
	assert.Equal(t, int32(1), clampReplicas(1))
	assert.Equal(t, int32(100), clampReplicas(100))
	assert.Equal(t, int32(math.MaxInt32), clampReplicas(math.MaxInt32+1))
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

// testChangeEventWithAutoscaling builds a change event with an
// autoscaling policy (feature #16).
func testChangeEventWithAutoscaling(eventType, name string, policy *autoscalingPolicy) changeEvent {
	evt := testChangeEvent(eventType, name)
	evt.Autoscaling = policy
	return evt
}

func TestApplyUpsertWithAutoscalingCreatesPodAutoscaler(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	evt := testChangeEventWithAutoscaling("upsert", "demo", &autoscalingPolicy{
		Enabled: true, MinReplicas: 1, MaxReplicas: 10,
		TargetConcurrency: 32, ScaleToZero: false, CooldownSeconds: 300,
	})
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))

	// The AIBrix PodAutoscaler exists and targets the service's storm.
	pa, err := dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).
		Get(ctx, autoscalerName("demo"), metav1.GetOptions{})
	require.NoError(t, err)
	max, _, _ := unstructured.NestedInt64(pa.Object, "spec", "maxReplicas")
	assert.Equal(t, int64(10), max)
	ref, _, _ := unstructured.NestedStringMap(pa.Object, "spec", "scaleTargetRef")
	assert.Equal(t, "StormService", ref["kind"])

	// Re-apply with a different max: the PodAutoscaler is updated
	// (idempotent).
	evt2 := testChangeEventWithAutoscaling("upsert", "demo", &autoscalingPolicy{
		Enabled: true, MinReplicas: 1, MaxReplicas: 20,
		TargetConcurrency: 32, ScaleToZero: false, CooldownSeconds: 300,
	})
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt2)))
	pa, err = dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).
		Get(ctx, autoscalerName("demo"), metav1.GetOptions{})
	require.NoError(t, err)
	max, _, _ = unstructured.NestedInt64(pa.Object, "spec", "maxReplicas")
	assert.Equal(t, int64(20), max)
}

func TestApplyUpsertAutoscalingDisabledDeletesPodAutoscaler(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	evt := testChangeEventWithAutoscaling("upsert", "demo", &autoscalingPolicy{
		Enabled: true, MinReplicas: 1, MaxReplicas: 10,
		TargetConcurrency: 32, ScaleToZero: false, CooldownSeconds: 300,
	})
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))
	_, err := dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).
		Get(ctx, autoscalerName("demo"), metav1.GetOptions{})
	require.NoError(t, err)

	// Then disable: the PodAutoscaler is deleted and the storm is pinned to
	// the fixed replica count.
	disabled := testChangeEventWithAutoscaling("upsert", "demo", &autoscalingPolicy{
		Enabled: false, MinReplicas: 1, MaxReplicas: 10,
		TargetConcurrency: 32, ScaleToZero: false, CooldownSeconds: 300,
	})
	disabled.Replicas = 3
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(disabled)))

	_, err = dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).
		Get(ctx, autoscalerName("demo"), metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "PodAutoscaler should be deleted when autoscaling is disabled")

	storm, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).
		Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)
	replicas, _, _ := unstructured.NestedInt64(storm.Object, "spec", "replicas")
	assert.Equal(t, int64(3), replicas, "the storm is pinned to the fixed count (FR2.5)")
}

func TestApplyDeleteRemovesStormAndPodAutoscaler(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	evt := testChangeEventWithAutoscaling("upsert", "demo", &autoscalingPolicy{
		Enabled: true, MinReplicas: 1, MaxReplicas: 10,
		TargetConcurrency: 32, ScaleToZero: false, CooldownSeconds: 300,
	})
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))
	_, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)

	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(testChangeEvent("delete", "demo"))))

	_, err = dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).Get(ctx, "demo", metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the storm is deleted with the service")
	_, err = dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).Get(ctx, autoscalerName("demo"), metav1.GetOptions{})
	assert.True(t, apierrors.IsNotFound(err), "the autoscaler is deleted with the service")
}

func TestChangeEventDecodeAutoscaling(t *testing.T) {
	// The controller's changeEvent must decode the infer module's
	// published autoscaling JSON (feature #16, §6.1).
	evt := testChangeEventWithAutoscaling("upsert", "demo", &autoscalingPolicy{
		Enabled: true, MinReplicas: 1, MaxReplicas: 8,
		TargetConcurrency: 48, ScaleToZero: false, CooldownSeconds: 200,
	})
	body, err := json.Marshal(evt)
	require.NoError(t, err)

	var decoded changeEvent
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.NotNil(t, decoded.Autoscaling)
	assert.Equal(t, true, decoded.Autoscaling.Enabled)
	assert.Equal(t, 8, decoded.Autoscaling.MaxReplicas)
	assert.Equal(t, 48, decoded.Autoscaling.TargetConcurrency)
	assert.Equal(t, 200, decoded.Autoscaling.CooldownSeconds)
}

// TestAwaitPodTerminalReportsPullFailure verifies a pod stuck in
// ImagePullBackOff fails with the pull reason instead of hanging the task
// forever: the wait is bounded by warmupTimeout.
func TestAwaitPodTerminalReportsPullFailure(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "warmup-x", Namespace: "taas-infer"},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			ContainerStatuses: []corev1.ContainerStatus{{
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "ErrImagePull",
					Message: `failed to resolve reference "hub.example.com/x": no such host`,
				}},
			}},
		},
	}
	reconciler := &k8sReconciler{
		clientset: fake.NewSimpleClientset(pod),
		namespace: "taas-infer",
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately after the first poll so the bounded wait ends;
	// the helper reads the latest pod and reports its wait reason.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, msg, err := reconciler.awaitPodTerminal(ctx, "warmup-x")
	require.Error(t, err)
	assert.Contains(t, msg, "ErrImagePull")
	assert.Contains(t, err.Error(), "image pull did not complete")
}

// TestEngineReadyTimesOutWithPodReason verifies a deploy whose pods never
// become ready does not block the reconcile forever: the wait is bounded,
// and the failure names the pod's own reason (an unbound weights PVC shows
// up as the scheduler's message).
func TestEngineReadyTimesOutWithPodReason(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "demo-0", Namespace: "taas-infer", Labels: map[string]string{"app": "demo"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Message: "0/1 nodes are available: pod has unbound immediate PersistentVolumeClaims",
			}},
		},
	}
	reconciler := &k8sReconciler{
		clientset: fake.NewSimpleClientset(pod),
		namespace: "taas-infer",
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := reconciler.awaitEngineReady(ctx, changeEvent{Name: "demo", Replicas: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready within")
	assert.Contains(t, err.Error(), "unbound immediate PersistentVolumeClaims",
		"the failure explains why the pods are stuck")
}

// TestEngineReadyFailsFastOnUnschedulablePod verifies an unschedulable pod
// is reported immediately with the scheduler's own reason instead of
// waiting for the readiness timeout.
func TestEngineReadyFailsFastOnUnschedulablePod(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.PodList{
			ListMeta: metav1.ListMeta{ResourceVersion: "1"},
			Items: []corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{
					Name: "demo-0", Namespace: reconcileNamespace,
					Labels: map[string]string{serviceLabelKey: "svc-1"},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					Conditions: []corev1.PodCondition{{
						Type:    corev1.PodScheduled,
						Status:  corev1.ConditionFalse,
						Reason:  corev1.PodReasonUnschedulable,
						Message: "0/1 nodes are available: no accelerator",
					}},
				},
			}},
		}, nil
	})
	reconciler := &k8sReconciler{clientset: clientset, namespace: reconcileNamespace}

	start := time.Now()
	err := reconciler.awaitEngineReady(context.Background(), testChangeEvent("upsert", "demo"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Unschedulable")
	assert.Contains(t, err.Error(), "no accelerator")
	assert.Less(t, time.Since(start), 5*time.Second, "an unschedulable pod fails fast")
}

// TestAcceleratorLabelKeyFallsBack verifies the node-selector label key
// adapts to what the cluster actually carries: the documented
// go-taas.com/accelerator label wins, and gpu-vendor is used when no node
// has it (otherwise every inference pod would be unschedulable).
func TestAcceleratorLabelKeyFallsBack(t *testing.T) {
	nvidiaNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"gpu-vendor": "nvidia"}}}
	reconciler := &k8sReconciler{clientset: fake.NewSimpleClientset(nvidiaNode), namespace: "taas-infer"}
	assert.Equal(t, "gpu-vendor", reconciler.acceleratorLabelKeyFor(context.Background()))

	// The documented label wins when present.
	labeled := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{
		"gpu-vendor": "nvidia", "go-taas.com/accelerator": "nvidia",
	}}}
	reconciler2 := &k8sReconciler{clientset: fake.NewSimpleClientset(labeled), namespace: "taas-infer"}
	assert.Equal(t, "go-taas.com/accelerator", reconciler2.acceleratorLabelKeyFor(context.Background()))

	// Neither label: keep the configured key so the mismatch is visible.
	bare := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{}}}
	reconciler3 := &k8sReconciler{clientset: fake.NewSimpleClientset(bare), namespace: "taas-infer"}
	assert.Equal(t, "go-taas.com/accelerator", reconciler3.acceleratorLabelKeyFor(context.Background()))
}

// TestBuildDeploymentUsesResolvedLabelKey verifies the Deployment's node
// selector uses the resolved key.
func TestBuildDeploymentUsesResolvedLabelKey(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"gpu-vendor": "nvidia"}}}
	reconciler := &k8sReconciler{
		clientset: fake.NewSimpleClientset(node), namespace: "taas-infer",
	}
	dep := reconciler.buildDeployment(context.Background(), testChangeEvent("upsert", "demo"))
	assert.Equal(t, "nvidia", dep.Spec.Template.Spec.NodeSelector["gpu-vendor"],
		"the pod pins the label the node actually carries")
}

// TestBuildDeploymentPinsCardModelLabel verifies that asking for a card
// type pins the node label that actually carries the card model
// (nvidia.com/gpu.product=NVIDIA-GeForce-RTX-4090). Synthesizing a
// "<vendor-label>-type" key matches no node, which made every
// card-type-specific deploy unschedulable.
func TestBuildDeploymentPinsCardModelLabel(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "sd4090",
		Labels: map[string]string{
			"gpu-vendor":             "nvidia",
			"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-4090",
		},
	}}
	reconciler := &k8sReconciler{clientset: fake.NewSimpleClientset(node), namespace: "taas-infer"}
	evt := testChangeEvent("upsert", "demo")
	evt.Accelerator = "nvidia"
	evt.AcceleratorType = "NVIDIA-GeForce-RTX-4090"

	dep := reconciler.buildDeployment(context.Background(), evt)
	selector := dep.Spec.Template.Spec.NodeSelector
	assert.Equal(t, "nvidia", selector["gpu-vendor"], "the vendor family is still pinned")
	assert.Equal(t, "NVIDIA-GeForce-RTX-4090", selector["nvidia.com/gpu.product"],
		"the card model is pinned through the label that carries it")
	for key := range selector {
		assert.NotContains(t, key, "-type", "no synthesized label key is used")
	}
}

// TestBuildDeploymentUnknownCardTypeKeepsVendorOnly verifies a card type no
// node reports degrades to the vendor selector instead of emitting an
// unsatisfiable label.
func TestBuildDeploymentUnknownCardTypeKeepsVendorOnly(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "sd4090",
		Labels: map[string]string{
			"gpu-vendor":             "nvidia",
			"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-4090",
		},
	}}
	reconciler := &k8sReconciler{clientset: fake.NewSimpleClientset(node), namespace: "taas-infer"}
	evt := testChangeEvent("upsert", "demo")
	evt.Accelerator = "nvidia"
	evt.AcceleratorType = "gpu" // the legacy resource family

	dep := reconciler.buildDeployment(context.Background(), evt)
	selector := dep.Spec.Template.Spec.NodeSelector
	assert.Equal(t, "nvidia", selector["gpu-vendor"])
	assert.Len(t, selector, 1)
}
