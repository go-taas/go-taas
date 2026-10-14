package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/go-taas/go-taas/pkg/mq"
)

// newFakeDynamic builds a fake dynamic client with the AIBrix CRD GVRs
// registered as list kinds.
func newFakeDynamic(objs ...k8sruntime.Object) *dynamicfake.FakeDynamicClient {
	scheme := k8sruntime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		modelClaimGVR:    "ModelClaimList",
		podAutoscalerGVR: "PodAutoscalerList",
	}, objs...)
}

func TestModelClaimNameSanitizes(t *testing.T) {
	assert.Equal(t, "mc-demo", modelClaimName("demo"))
	assert.Equal(t, "mc-qwen2.5-0.5b", modelClaimName("Qwen2.5-0.5B"))
	assert.Equal(t, "mc-a-b", modelClaimName("a/b"))
	assert.Equal(t, "mc-model", modelClaimName("///"))
}

func TestBuildModelClaimFromEvent(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Autoscaling = &autoscalingPolicy{Enabled: true, MinReplicas: 1, MaxReplicas: 5}
	claim := buildModelClaimFromEvent(evt, reconcileNamespace, "/data/weights")

	assert.Equal(t, "ModelClaim", claim.GetKind())
	assert.Equal(t, "model.aibrix.ai/v1alpha1", claim.GetAPIVersion())
	assert.Equal(t, "mc-demo", claim.GetName())
	assert.Equal(t, reconcileNamespace, claim.GetNamespace())

	engine, _, _ := unstructured.NestedString(claim.Object, "spec", "engine")
	assert.Equal(t, "vllm", engine)
	artifact, _, _ := unstructured.NestedString(claim.Object, "spec", "artifactURL")
	assert.Equal(t, "/data/weights/qwen/v1", artifact)
	replicas, _, _ := unstructured.NestedInt64(claim.Object, "spec", "replicas")
	assert.Equal(t, int64(2), replicas)

	matchLabels, _, _ := unstructured.NestedStringMap(claim.Object, "spec", "podSelector", "matchLabels")
	assert.Equal(t, "demo", matchLabels["app"])
	assert.Equal(t, "svc-1", matchLabels[mcLabelServiceID])

	labels := claim.GetLabels()
	assert.Equal(t, managedByValue, labels[mcLabelManagedBy])
	assert.Equal(t, originService, labels[mcLabelOrigin])
}

func TestBuildModelClaimFromChange(t *testing.T) {
	change := &runtimeModelChange{
		EventType:   "upsert",
		ModelName:   "qwen2",
		Engine:      "sglang",
		Replicas:    3,
		ArtifactURL: "/data/w",
		EngineArgs:  map[string]string{"max-model-len": "8192"},
	}
	claim := buildModelClaimFromChange(change, reconcileNamespace)

	assert.Equal(t, "mc-qwen2", claim.GetName())
	engine, _, _ := unstructured.NestedString(claim.Object, "spec", "engine")
	assert.Equal(t, "sglang", engine)
	replicas, _, _ := unstructured.NestedInt64(claim.Object, "spec", "replicas")
	assert.Equal(t, int64(3), replicas)

	// No pod selector supplied: the default label is applied.
	matchLabels, _, _ := unstructured.NestedStringMap(claim.Object, "spec", "podSelector", "matchLabels")
	assert.Equal(t, "qwen2", matchLabels[mcLabelModel])

	args, _, _ := unstructured.NestedStringMap(claim.Object, "spec", "engineConfig", "args")
	assert.Equal(t, "8192", args["max-model-len"])
	assert.Equal(t, originRegistration, claim.GetLabels()[mcLabelOrigin])
}

func TestApplyAndDeleteModelClaim(t *testing.T) {
	dyn := newFakeDynamic()
	ctx := context.Background()
	change := &runtimeModelChange{ModelName: "m1", Engine: "vllm", Replicas: 1, ArtifactURL: "/w"}
	claim := buildModelClaimFromChange(change, reconcileNamespace)

	require.NoError(t, applyModelClaim(ctx, dyn, claim))
	got, err := dyn.Resource(modelClaimGVR).Namespace(reconcileNamespace).Get(ctx, "mc-m1", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "mc-m1", got.GetName())

	// Idempotent update.
	require.NoError(t, applyModelClaim(ctx, dyn, buildModelClaimFromChange(change, reconcileNamespace)))

	require.NoError(t, deleteModelClaim(ctx, dyn, reconcileNamespace, "m1"))
	_, err = dyn.Resource(modelClaimGVR).Namespace(reconcileNamespace).Get(ctx, "mc-m1", metav1.GetOptions{})
	require.Error(t, err)
}

func TestApplyPodAutoscaler(t *testing.T) {
	dyn := newFakeDynamic()
	ctx := context.Background()
	require.NoError(t, applyPodAutoscaler(ctx, dyn, reconcileNamespace, "demo", "demo", 1, 5, 32))
	got, err := dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).Get(ctx, hpaName("demo"), metav1.GetOptions{})
	require.NoError(t, err)
	max, _, _ := unstructured.NestedInt64(got.Object, "spec", "maxReplicas")
	assert.Equal(t, int64(5), max)
	deletePodAutoscaler(ctx, dyn, reconcileNamespace, "demo")
}

// TestBuildPodAutoscalerSatisfiesCRD asserts the built PodAutoscaler object
// carries every field the AIBrix PodAutoscaler CRD marks required
// (spec.maxReplicas, spec.scaleTargetRef, spec.scalingStrategy) and that
// scalingStrategy is a supported enum value. This is a golden-object
// assertion on the built spec, so a fake dynamic client (which performs no
// schema validation) can no longer hide a missing required field — the
// regression behind the row-47 bug report.
func TestBuildPodAutoscalerSatisfiesCRD(t *testing.T) {
	pa := buildPodAutoscaler(reconcileNamespace, "demo-deploy", "demo", 1, 5, 48)

	assert.Equal(t, "PodAutoscaler", pa.GetKind())
	assert.Equal(t, "autoscaling.aibrix.ai/v1alpha1", pa.GetAPIVersion())
	assert.Equal(t, hpaName("demo"), pa.GetName())
	assert.Equal(t, reconcileNamespace, pa.GetNamespace())
	assert.Equal(t, managedByValue, pa.GetLabels()[mcLabelManagedBy])

	// CRD spec.required = [maxReplicas, scaleTargetRef, scalingStrategy].
	max, found, err := unstructured.NestedInt64(pa.Object, "spec", "maxReplicas")
	require.NoError(t, err)
	require.True(t, found, "spec.maxReplicas is CRD-required and must be set")
	assert.Equal(t, int64(5), max)

	min, found, err := unstructured.NestedInt64(pa.Object, "spec", "minReplicas")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), min)

	ref, found, err := unstructured.NestedMap(pa.Object, "spec", "scaleTargetRef")
	require.NoError(t, err)
	require.True(t, found, "spec.scaleTargetRef is CRD-required and must be set")
	assert.Equal(t, map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"name":       "demo-deploy",
	}, ref)

	// spec.scalingStrategy is a CRD-required string enum: HPA | KPA | APA.
	strategy, found, err := unstructured.NestedString(pa.Object, "spec", "scalingStrategy")
	require.NoError(t, err)
	require.True(t, found, "spec.scalingStrategy is CRD-required and must be set")
	assert.Contains(t, []string{"HPA", "KPA", "APA"}, strategy,
		"spec.scalingStrategy must be a supported CRD enum value")
	assert.NotEmpty(t, strategy, "an empty scalingStrategy is rejected by the CRD with a 422")

	// spec.metricsSources is enforced by the AIBrix admission webhook
	// (minItems: 1 on the CRD property, not listed in spec.required — the
	// regression behind the row-47 metricsSources bug report). Each entry
	// must carry its required per-item fields: metricSourceType (enum
	// pod|resource|custom|external|domain), targetMetric, targetValue, and
	// for metricSourceType=pod the per-type fields protocolType, port and
	// path (AIBrix pkg/webhook validatePodMetricSource rejects a pod entry
	// missing any of the three — the second half of the row-47 bug report).
	sources, found, err := unstructured.NestedSlice(pa.Object, "spec", "metricsSources")
	require.NoError(t, err)
	require.True(t, found, "spec.metricsSources must be set (admission webhook requires minItems: 1)")
	require.NotEmpty(t, sources, "spec.metricsSources must carry at least one entry")
	entry, ok := sources[0].(map[string]any)
	require.True(t, ok, "metricsSources entries must be objects")
	assert.Contains(t, []string{"pod", "resource", "custom", "external", "domain"},
		entry["metricSourceType"], "metricSourceType must be a supported CRD enum value")
	assert.Contains(t, []string{"http", "https"}, entry["protocolType"],
		"protocolType is required for metricSourceType=pod and must be a CRD enum value")
	assert.Equal(t, podAutoscalerProtocolType, entry["protocolType"])
	assert.Equal(t, podAutoscalerMetricsPort, entry["port"],
		"port is required for metricSourceType=pod and must match the engine container's metrics port")
	assert.Equal(t, podAutoscalerMetricsPath, entry["path"],
		"path is required for metricSourceType=pod and must match the engine's metrics path")
	assert.Equal(t, podAutoscalerTargetMetric, entry["targetMetric"],
		"targetMetric must be an AIBrix registry engine-agnostic name so the runtime fetcher can resolve it")
	assert.Equal(t, "48", entry["targetValue"],
		"targetValue must be the target concurrency rendered as a string")

	// A non-positive target concurrency falls back to the platform default
	// (mirrors buildHPA's clamp), never an empty targetValue.
	defaulted := buildPodAutoscaler(reconcileNamespace, "demo-deploy", "demo", 1, 5, 0)
	ds, found, err := unstructured.NestedSlice(defaulted.Object, "spec", "metricsSources")
	require.NoError(t, err)
	require.True(t, found)
	de, ok := ds[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "32", de["targetValue"], "a zero target concurrency must default to 32")
}

// TestApplyPodAutoscalerWritesScalingStrategy asserts the object persisted
// through the dynamic client keeps the required scalingStrategy (the fake
// client round-trips the spec verbatim, so this catches a builder that
// drops the field before the write).
func TestApplyPodAutoscalerWritesScalingStrategy(t *testing.T) {
	dyn := newFakeDynamic()
	ctx := context.Background()
	require.NoError(t, applyPodAutoscaler(ctx, dyn, reconcileNamespace, "demo", "demo", 2, 8, 32))
	got, err := dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).Get(ctx, hpaName("demo"), metav1.GetOptions{})
	require.NoError(t, err)
	strategy, found, err := unstructured.NestedString(got.Object, "spec", "scalingStrategy")
	require.NoError(t, err)
	require.True(t, found, "persisted PodAutoscaler must carry spec.scalingStrategy")
	assert.Equal(t, podAutoscalerScalingStrategy, strategy)

	// Idempotent re-apply keeps the strategy on the update path too.
	require.NoError(t, applyPodAutoscaler(ctx, dyn, reconcileNamespace, "demo", "demo", 2, 8, 32))
	got, err = dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).Get(ctx, hpaName("demo"), metav1.GetOptions{})
	require.NoError(t, err)
	strategy, _, err = unstructured.NestedString(got.Object, "spec", "scalingStrategy")
	require.NoError(t, err)
	assert.Equal(t, podAutoscalerScalingStrategy, strategy)
	deletePodAutoscaler(ctx, dyn, reconcileNamespace, "demo")
}

// TestApplyPodAutoscalerRetriesOnConflict asserts the update path survives
// a resourceVersion conflict: the AIBrix controller mutates the object
// concurrently (status, labels), so a stale read can fail the update with
// "the object has been modified". The retry loop must re-read the live
// object and reapply the desired spec until the write lands.
func TestApplyPodAutoscalerRetriesOnConflict(t *testing.T) {
	dyn := newFakeDynamic()
	ctx := context.Background()
	require.NoError(t, applyPodAutoscaler(ctx, dyn, reconcileNamespace, "demo", "demo", 1, 5, 32))

	// Fail the first update with a conflict, then let the retry succeed.
	var conflicts int
	dyn.PrependReactor("update", "podautoscalers", func(_ k8stesting.Action) (bool, k8sruntime.Object, error) {
		if conflicts == 0 {
			conflicts++
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "autoscaling.aibrix.ai", Resource: "podautoscalers"},
				hpaName("demo"), errors.New("the object has been modified"))
		}
		return false, nil, nil
	})

	require.NoError(t, applyPodAutoscaler(ctx, dyn, reconcileNamespace, "demo", "demo", 2, 8, 48))
	assert.Equal(t, 1, conflicts, "exactly one injected conflict must have been absorbed")
	got, err := dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).Get(ctx, hpaName("demo"), metav1.GetOptions{})
	require.NoError(t, err)
	max, found, err := unstructured.NestedInt64(got.Object, "spec", "maxReplicas")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(8), max, "the retried write must land the desired spec")
	sources, _, err := unstructured.NestedSlice(got.Object, "spec", "metricsSources")
	require.NoError(t, err)
	entry, ok := sources[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "48", entry["targetValue"], "the retried write must land the new target")
	deletePodAutoscaler(ctx, dyn, reconcileNamespace, "demo")
}

// TestApplyPodAutoscalerGivesUpAfterRepeatedConflicts asserts the retry
// loop is bounded: persistent conflicts surface as an error instead of
// spinning forever.
func TestApplyPodAutoscalerGivesUpAfterRepeatedConflicts(t *testing.T) {
	dyn := newFakeDynamic()
	ctx := context.Background()
	require.NoError(t, applyPodAutoscaler(ctx, dyn, reconcileNamespace, "demo", "demo", 1, 5, 32))

	dyn.PrependReactor("update", "podautoscalers", func(_ k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Group: "autoscaling.aibrix.ai", Resource: "podautoscalers"},
			hpaName("demo"), errors.New("the object has been modified"))
	})

	err := applyPodAutoscaler(ctx, dyn, reconcileNamespace, "demo", "demo", 2, 8, 48)
	require.Error(t, err, "persistent conflicts must surface as an error")
	assert.True(t, apierrors.IsConflict(err), "the surfaced error must be the conflict")
	deletePodAutoscaler(ctx, dyn, reconcileNamespace, "demo")
}

// newAIBrixFakeReconciler builds a reconciler over the fake clientset and
// a fake dynamic client with the aibrix scheme active.
func newAIBrixFakeReconciler(dyn *dynamicfake.FakeDynamicClient) (Reconciler, *fake.Clientset, *SchemeCache) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("get", "deployments", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
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
	cache := NewSchemeCache()
	cache.SetScheme(schemeAIBrix)
	return newReconcilerWithClientset(clientset, dyn, mq.NewFake(), "https://infer.example.com/", "", WeightsConfig{}, cache), clientset, cache
}

func TestApplyUpsertWritesModelClaimUnderAIBrix(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)

	require.NoError(t, reconciler.ApplyInferServiceChange(context.Background(), changeMessage(testChangeEvent("upsert", "demo"))))

	got, err := dyn.Resource(modelClaimGVR).Namespace(reconcileNamespace).Get(
		context.Background(), "mc-demo", metav1.GetOptions{})
	require.NoError(t, err)
	engine, _, _ := unstructured.NestedString(got.Object, "spec", "engine")
	assert.Equal(t, "vllm", engine)
}

func TestApplyUpsertFallsBackWhenModelClaimWriteFails(t *testing.T) {
	// A dynamic client over a scheme without the GVR returns an error on
	// write; the plain serving path must still succeed (AD13).
	dyn := dynamicfake.NewSimpleDynamicClient(k8sruntime.NewScheme())
	reconciler, clientset, _ := newAIBrixFakeReconciler(dyn)

	err := reconciler.ApplyInferServiceChange(context.Background(), changeMessage(testChangeEvent("upsert", "demo")))
	require.NoError(t, err)

	// The Deployment was still reconciled despite the ModelClaim failure.
	_, err = clientset.AppsV1().Deployments(reconcileNamespace).Get(context.Background(), "demo", metav1.GetOptions{})
	require.NoError(t, err)
}

// TestApplyUpsertFallsBackAndCleansStalePodAutoscaler asserts the HPA
// fallback path deletes a leftover AIBrix PodAutoscaler: a stale object
// keeps the AIBrix controller driving its own derived HPA against the
// same Deployment, so two autoscalers would fight over the replica count
// (observed live as a status write storm on both sides).
func TestApplyUpsertFallsBackAndCleansStalePodAutoscaler(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, clientset, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	// Seed a stale PodAutoscaler from an earlier successful write.
	evt := testChangeEvent("upsert", "demo")
	evt.Autoscaling = &autoscalingPolicy{Enabled: true, MinReplicas: 1, MaxReplicas: 5, TargetConcurrency: 32}
	require.NoError(t, applyPodAutoscaler(ctx, dyn, reconcileNamespace, "demo", "demo", 1, 5, 32))
	_, err := dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).Get(ctx, hpaName("demo"), metav1.GetOptions{})
	require.NoError(t, err, "seed PodAutoscaler must exist")

	// Make every subsequent PodAutoscaler write fail with a conflict so
	// the reconciler takes the HPA fallback path.
	dyn.PrependReactor("update", "podautoscalers", func(_ k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Group: "autoscaling.aibrix.ai", Resource: "podautoscalers"},
			hpaName("demo"), errors.New("the object has been modified"))
	})

	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))

	// The stale PodAutoscaler must be gone: the fallback owns autoscaling
	// via the plain HPA alone.
	_, err = dyn.Resource(podAutoscalerGVR).Namespace(reconcileNamespace).Get(ctx, hpaName("demo"), metav1.GetOptions{})
	require.Error(t, err, "stale PodAutoscaler must be deleted on HPA fallback")
	assert.True(t, apierrors.IsNotFound(err))

	// The plain HPA must exist and own the autoscaling.
	_, err = clientset.AutoscalingV2().HorizontalPodAutoscalers(reconcileNamespace).Get(ctx, hpaName("demo"), metav1.GetOptions{})
	require.NoError(t, err, "fallback HPA must be created")
}

func TestRuntimeSchemeConsumerSetsScheme(t *testing.T) {
	cache := NewSchemeCache()
	consumer := NewRuntimeSchemeConsumer(mq.NewFake(), cache)
	body, _ := json.Marshal(runtimeSchemeMessage{Scheme: schemeAIBrix, Revision: 4})
	require.NoError(t, consumer.handle(mq.Message{Body: body}))
	assert.Equal(t, schemeAIBrix, cache.Scheme())

	// An unknown scheme is ignored.
	bad, _ := json.Marshal(runtimeSchemeMessage{Scheme: "bogus"})
	require.NoError(t, consumer.handle(mq.Message{Body: bad}))
	assert.Equal(t, schemeAIBrix, cache.Scheme())
}

func TestRuntimeModelChangeConsumerAppliesAndDeletes(t *testing.T) {
	dyn := newFakeDynamic()
	consumer := NewRuntimeModelChangeConsumer(mq.NewFake(), dyn, reconcileNamespace)
	ctx := context.Background()

	upsert, _ := json.Marshal(runtimeModelChange{
		EventType: "upsert", ModelName: "qwen2", Engine: "vllm", Replicas: 1, ArtifactURL: "/w",
	})
	require.NoError(t, consumer.handle(mq.Message{Body: upsert}))
	_, err := dyn.Resource(modelClaimGVR).Namespace(reconcileNamespace).Get(ctx, "mc-qwen2", metav1.GetOptions{})
	require.NoError(t, err)

	del, _ := json.Marshal(runtimeModelChange{EventType: "delete", ModelName: "qwen2"})
	require.NoError(t, consumer.handle(mq.Message{Body: del}))
	_, err = dyn.Resource(modelClaimGVR).Namespace(reconcileNamespace).Get(ctx, "mc-qwen2", metav1.GetOptions{})
	require.Error(t, err)
}

// newCollectorClient builds a fake clientset with the AIBrix control-plane
// Deployments and the gateway Deployment.
func newCollectorClient() *fake.Clientset {
	desired := int32(1)
	objs := []k8sruntime.Object{}
	for _, name := range append(append([]string{}, aibrixControlPlaneDeployments...), "envoy-aibrix-system-aibrix-eg") {
		objs = append(objs, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "aibrix-system"},
			Spec:       appsv1.DeploymentSpec{Replicas: &desired},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
		})
	}
	return fake.NewSimpleClientset(objs...)
}

func TestRuntimeCollectorPublishesAIBrixSnapshot(t *testing.T) {
	client := mq.NewFake()
	var published []runtimeSnapshot
	require.NoError(t, client.Subscribe(context.Background(), mq.DefaultSubjects().RuntimeHealth, func(msg mq.Message) error {
		var snap runtimeSnapshot
		_ = json.Unmarshal(msg.Body, &snap)
		published = append(published, snap)
		return nil
	}))

	cache := NewSchemeCache()
	cache.SetScheme(schemeAIBrix)
	collector := NewRuntimeCollector(newCollectorClient(), newFakeDynamic(), client, RuntimeCollectorConfig{
		Interval: time.Minute, Namespace: reconcileNamespace,
		AIBrixNamespace: "aibrix-system", Gateway: "envoy-aibrix-system-aibrix-eg",
	}, cache)

	collector.collectAndPublish(context.Background())

	require.Len(t, published, 1)
	assert.Equal(t, schemeAIBrix, published[0].Scheme)
	assert.Equal(t, "available", published[0].Availability)
	assert.Len(t, published[0].Components, len(aibrixControlPlaneDeployments)+1)
}

// TestRuntimeCollectorDiscoversSuffixedGateway covers the real-cluster
// shape: Envoy AI Gateway suffixes its generated Deployment name with a
// hash and may place it outside the AIBrix namespace. The collector must
// still observe it (and keep the configured component identity).
func TestRuntimeCollectorDiscoversSuffixedGateway(t *testing.T) {
	desired := int32(1)
	objs := []k8sruntime.Object{}
	for _, name := range aibrixControlPlaneDeployments {
		objs = append(objs, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "aibrix-system"},
			Spec:       appsv1.DeploymentSpec{Replicas: &desired},
			Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
		})
	}
	objs = append(objs, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "envoy-aibrix-system-aibrix-eg-903790dc", Namespace: "yunzhi-system"},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	})

	client := mq.NewFake()
	var published []runtimeSnapshot
	require.NoError(t, client.Subscribe(context.Background(), mq.DefaultSubjects().RuntimeHealth, func(msg mq.Message) error {
		var snap runtimeSnapshot
		_ = json.Unmarshal(msg.Body, &snap)
		published = append(published, snap)
		return nil
	}))

	cache := NewSchemeCache()
	cache.SetScheme(schemeAIBrix)
	collector := NewRuntimeCollector(fake.NewSimpleClientset(objs...), newFakeDynamic(), client, RuntimeCollectorConfig{
		Interval: time.Minute, Namespace: reconcileNamespace,
		AIBrixNamespace: "aibrix-system", Gateway: "envoy-aibrix-system-aibrix-eg",
	}, cache)

	collector.collectAndPublish(context.Background())

	require.Len(t, published, 1)
	assert.Equal(t, "available", published[0].Availability)
	require.Len(t, published[0].Components, len(aibrixControlPlaneDeployments)+1)
	gateway := published[0].Components[len(published[0].Components)-1]
	assert.Equal(t, "envoy-aibrix-system-aibrix-eg", gateway.ComponentID)
	assert.Equal(t, "yunzhi-system", gateway.Namespace)
	assert.Equal(t, "ready", gateway.Status)
}

func TestRuntimeCollectorPlainFallback(t *testing.T) {
	client := mq.NewFake()
	var published []runtimeSnapshot
	require.NoError(t, client.Subscribe(context.Background(), mq.DefaultSubjects().RuntimeHealth, func(msg mq.Message) error {
		var snap runtimeSnapshot
		_ = json.Unmarshal(msg.Body, &snap)
		published = append(published, snap)
		return nil
	}))

	collector := NewRuntimeCollector(fake.NewSimpleClientset(), newFakeDynamic(), client, RuntimeCollectorConfig{
		Interval: time.Minute, Namespace: reconcileNamespace,
	}, NewSchemeCache())

	collector.collectAndPublish(context.Background())

	require.Len(t, published, 1)
	assert.Equal(t, schemePlain, published[0].Scheme)
	require.Len(t, published[0].Components, 2)
	assert.Equal(t, "go-taas-controller", published[0].Components[0].ComponentID)
	assert.Equal(t, "available", published[0].Availability)
}

func TestRuntimeCollectorDegradesWhenModelClaimListFails(t *testing.T) {
	client := mq.NewFake()
	var published []runtimeSnapshot
	require.NoError(t, client.Subscribe(context.Background(), mq.DefaultSubjects().RuntimeHealth, func(msg mq.Message) error {
		var snap runtimeSnapshot
		_ = json.Unmarshal(msg.Body, &snap)
		published = append(published, snap)
		return nil
	}))

	cache := NewSchemeCache()
	cache.SetScheme(schemeAIBrix)
	// A list reactor makes the ModelClaim list fail, simulating an
	// unreachable cluster.
	dyn := newFakeDynamic()
	dyn.PrependReactor("list", "modelclaims", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, errors.New("api server unavailable")
	})
	collector := NewRuntimeCollector(newCollectorClient(), dyn, client, RuntimeCollectorConfig{
		Interval: time.Minute, Namespace: reconcileNamespace, AIBrixNamespace: "aibrix-system",
	}, cache)

	collector.collectAndPublish(context.Background())

	require.Len(t, published, 1)
	// No successful snapshot has been published yet, so the degradation
	// reports the unknown availability with a reason (AD4).
	assert.Equal(t, "unknown", published[0].Availability)
	assert.NotEmpty(t, published[0].Reason)
}

func TestProjectModelClaim(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "model.aibrix.ai/v1alpha1",
		"kind":       "ModelClaim",
		"metadata": map[string]any{
			"name":              "mc-qwen2",
			"namespace":         reconcileNamespace,
			"creationTimestamp": "2026-10-01T00:00:00Z",
			"labels":            map[string]any{mcLabelOrigin: originRegistration},
		},
		"spec": map[string]any{"modelName": "qwen2", "engine": "vllm", "replicas": int64(2)},
		"status": map[string]any{
			"candidates": int64(2),
			"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
		},
	}}
	model := projectModelClaim(obj)
	assert.Equal(t, "qwen2", model.ModelName)
	assert.Equal(t, "vllm", model.Engine)
	assert.Equal(t, int64(2), model.Replicas)
	assert.Equal(t, "ready", model.Status)
	assert.Equal(t, originRegistration, model.Origin)
}

func TestDeriveModelStatus(t *testing.T) {
	assert.Equal(t, "ready", deriveModelStatus([]runtimeModelCondition{{Type: "Ready", Status: "True"}}, 0))
	assert.Equal(t, "failed", deriveModelStatus([]runtimeModelCondition{{Type: "Ready", Status: "False", Reason: "Err"}}, 0))
	assert.Equal(t, "progressing", deriveModelStatus(nil, 1))
	assert.Equal(t, "unknown", deriveModelStatus(nil, 0))
}

func TestAggregateAvailability(t *testing.T) {
	assert.Equal(t, "unknown", aggregateAvailability(nil))
	assert.Equal(t, "available", aggregateAvailability([]runtimeComponent{{Status: "ready"}, {Status: "ready"}}))
	assert.Equal(t, "degraded", aggregateAvailability([]runtimeComponent{{Status: "ready"}, {Status: "degraded"}}))
	assert.Equal(t, "unavailable", aggregateAvailability([]runtimeComponent{{Status: "unavailable"}}))
}
