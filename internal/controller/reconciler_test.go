package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/go-taas/go-taas/pkg/mq"
)

// reconcileNamespace is the default namespace the reconciler uses when
// none is configured (mirrors the production default).
const reconcileNamespace = "taas-infer"

// testReconciler returns a reconciler over a fake clientset for the
// build-function tests.
// testReconciler builds a reconciler over a fleet with one A100 node, so
// the card type in testChangeEvent resolves to a real node label. The node
// carries only NVIDIA's own labels: the platform's go-taas.com/accelerator
// label is not required for scheduling.
func testReconciler() *k8sReconciler {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: "gpu-node-1",
		Labels: map[string]string{
			"nvidia.com/gpu.present": "true",
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

	// The vendor's own node labels pin the accelerator: no
	// go-taas.com/accelerator label is required.
	assert.Equal(t, "true", dep.Spec.Template.Spec.NodeSelector["nvidia.com/gpu.present"])
	assert.Equal(t, "A100", dep.Spec.Template.Spec.NodeSelector["nvidia.com/gpu.product"],
		"the card model is pinned through the label that carries it")
	_, hasPlatformLabel := dep.Spec.Template.Spec.NodeSelector["go-taas.com/accelerator"]
	assert.False(t, hasPlatformLabel, "scheduling must not depend on a platform-defined vendor label")

	// The selector keeps the shared resource labels (it is immutable); the
	// template adds the serving-gateway routing labels on top.
	assert.Equal(t, evt2Labels(evt), dep.Spec.Selector.MatchLabels)
	for key, value := range evt2Labels(evt) {
		assert.Equal(t, value, dep.Spec.Template.Labels[key])
	}
	assert.Equal(t, "svc-1", dep.Spec.Template.Labels["go-taas.com/service-id"])
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

// TestApplyUpsertRejectsEventWithoutEngineImage proves a change event that
// carries no engine image is refused instead of applied. The object is
// composed from the event alone, so applying one would write a pod template
// without an image: the engine would fall back to whatever the serving preset
// declares instead of the image the operator chose, and the service would run
// something else than the one it was created for.
func TestApplyUpsertRejectsEventWithoutEngineImage(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Image.Reference = ""

	err := testReconciler().applyUpsert(context.Background(), evt)
	require.Error(t, err)
	assert.True(t, mq.IsPermanent(err))
	assert.Contains(t, err.Error(), "carries no engine image")
}

// TestRunningStatusReportsTheServedReplicaCount covers the observed replica
// count: the platform pins the requested count, so "current" has to come from
// the pods that are actually ready, otherwise a serving service reports 0 and
// the console shows a service that is neither scaling nor serving.
func TestRunningStatusReportsTheServedReplicaCount(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	ready := func(name string, ready bool) *corev1.Pod {
		status := corev1.ConditionFalse
		if ready {
			status = corev1.ConditionTrue
		}
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: reconcileNamespace,
				Labels: map[string]string{serviceLabelKey: evt.ServiceID}},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: status},
			}},
		}
	}
	reconciler := testReconciler()
	reconciler.clientset = fake.NewSimpleClientset(
		ready("engine-ready", true),
		ready("engine-loading", false),
	)

	assert.Equal(t, 1, reconciler.readyReplicaCount(context.Background(), evt),
		"only a pod that reports Ready is serving")

	// A KServe service runs a router-scheduler pod beside the engine. It
	// carries the same name and part-of labels, so counting it would report
	// more replicas than the service has.
	serving := testReconciler()
	serving.dynamic = newFakeDynamic()
	serving.clientset = fake.NewSimpleClientset(
		kservePod("engine", "llminferenceservice-workload", true),
		kservePod("router", "llminferenceservice-router-scheduler", true),
	)
	assert.Equal(t, 1, serving.readyReplicaCount(context.Background(), evt),
		"the router pod is not a serving replica")
}

// kservePod builds a pod KServe stamps for a service, carrying the component label
// that distinguishes the engine from the router-scheduler.
func kservePod(name, component string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: reconcileNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "demo",
				"app.kubernetes.io/part-of":   "llminferenceservice",
				"app.kubernetes.io/component": component,
			}},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: status},
		}},
	}
}

// A cache-delivery pod mounts the warmup node directory. The constructor must
// carry the configured hostPath into the reconciler; without it every cache
// service silently falls back to the shared weights claim and waits forever
// for weights that are only on the node.
func TestNewReconcilerCarriesWarmupHostPath(t *testing.T) {
	r := newReconcilerWithClientset(fake.NewSimpleClientset(), nil, nil, "", reconcileNamespace,
		WeightsConfig{WarmupHostPath: "/models"}, nil)
	assert.Equal(t, "/models", r.(*k8sReconciler).warmupHostPath)

	plain := newReconcilerWithClientset(fake.NewSimpleClientset(), nil, nil, "", reconcileNamespace,
		WeightsConfig{}, nil)
	assert.Equal(t, "/models", plain.(*k8sReconciler).warmupHostPath,
		"the default warmup directory is also mounted")
}
