package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestBuildLLMInferenceServiceSingleNode covers the default topology: one
// service is one LLMInferenceService with one replica holding one pod, and
// neither a leader-worker set nor a prefill half.
func TestBuildLLMInferenceServiceSingleNode(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	obj, err := testReconciler().buildLLMInferenceService(context.Background(), evt)
	require.NoError(t, err)
	require.NotNil(t, obj)

	assert.Equal(t, "LLMInferenceService", obj.GetKind())
	assert.Equal(t, "serving.kserve.io/v1alpha2", obj.GetAPIVersion())
	assert.Equal(t, "demo", obj.GetName())
	assert.Equal(t, reconcileNamespace, obj.GetNamespace())

	// The model names the platform's shared weights claim and the model's own
	// directory inside it. KServe itself must not fetch anything: the platform
	// mounts the claim.
	uri, found, err := unstructured.NestedString(obj.Object, "spec", "model", "uri")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "pvc://model-weights/qwen/v1", uri)

	modelName, found, err := unstructured.NestedString(obj.Object, "spec", "model", "name")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "demo", modelName)

	// A storage initializer would copy the whole model a second time and make
	// the pod wait for it, so it is switched off.
	enabled, found, err := unstructured.NestedBool(obj.Object, "spec", "storageInitializer", "enabled")
	require.NoError(t, err)
	require.True(t, found)
	assert.False(t, enabled)

	// One replica is one pod, so there is neither a leader-worker set nor a
	// size to declare.
	replicas, found, err := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), replicas)

	_, hasWorker, err := unstructured.NestedMap(obj.Object, "spec", "worker")
	require.NoError(t, err)
	assert.False(t, hasWorker)

	_, hasPrefill, err := unstructured.NestedMap(obj.Object, "spec", "prefill")
	require.NoError(t, err)
	assert.False(t, hasPrefill)

	// No router block: the platform's own route is what serves the model, and
	// KServe's route would declare an InferencePool backend the gateway in
	// this cluster does not serve, leaving the object permanently not ready
	// and adding an unused router pod beside the engine.
	_, hasRouter, err := unstructured.NestedMap(obj.Object, "spec", "router")
	require.NoError(t, err)
	assert.False(t, hasRouter)

	// A model holds its accelerator for the whole life, so a rollout that
	// starts the replacement first cannot fit on the device the old pod still
	// holds. Replacing in place is what keeps an update possible.
	surge, found, err := unstructured.NestedInt64(obj.Object, "spec", "rolloutStrategy", "maxSurge")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(0), surge)

	unavailable, found, err := unstructured.NestedInt64(obj.Object, "spec", "rolloutStrategy", "maxUnavailable")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), unavailable)
}

// TestBuildLLMInferenceServiceContainerOverlay proves the engine pod is an
// overlay on the preset's model-server container: it renames the container to
// the preset's name so KServe merges into that container instead of adding a
// second one, and it does not redeclare the port the preset owns.
func TestBuildLLMInferenceServiceContainerOverlay(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Env = map[string]string{"TAAS_TEST_ENV": "on"}
	obj, err := testReconciler().buildLLMInferenceService(context.Background(), evt)
	require.NoError(t, err)

	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "containers")
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, containers, 1)

	container, ok := containers[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "main", container["name"])
	assert.Equal(t, "ghcr.io/go-taas/vllm:v0.6.3", container["image"])

	_, hasPorts := container["ports"]
	assert.False(t, hasPorts)

	// The presets declare the engine non-root, but the platform's engine
	// images run as root: the overlay has to opt out or the kubelet refuses
	// the container before the engine starts.
	securityContext, ok := container["securityContext"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, false, securityContext["runAsNonRoot"])
	assert.Equal(t, int64(0), securityContext["runAsUser"])

	// Environment the operator set reaches the engine container, so a customer
	// override is not dropped by the overlay.
	env, ok := container["env"].([]interface{})
	require.True(t, ok)
	hasEnv := false
	for _, raw := range env {
		entry, isMap := raw.(map[string]interface{})
		if isMap && entry["name"] == "TAAS_TEST_ENV" {
			hasEnv = true
		}
	}
	assert.True(t, hasEnv)
}

// TestScalingPolicyKeepsAFixedReplicaCount proves a service never carries a
// KServe scaling block: KServe rejects one that names no autoscaler (KEDA or
// WVA) and rejects replicas beside one, and this platform ships neither. A
// service with an autoscaling policy therefore still runs at its replica
// count, which is what the serving object states.
func TestScalingPolicyKeepsAFixedReplicaCount(t *testing.T) {
	policies := map[string]*autoscalingPolicy{
		"no policy":     nil,
		"disabled":      {Enabled: false, MinReplicas: 1, MaxReplicas: 4},
		"bounded":       {Enabled: true, MinReplicas: 2, MaxReplicas: 6},
		"scale to zero": {Enabled: true, MinReplicas: 1, MaxReplicas: 4, ScaleToZero: true},
		"max below min": {Enabled: true, MinReplicas: 3, MaxReplicas: 1},
	}
	for name, policy := range policies {
		t.Run(name, func(t *testing.T) {
			evt := testChangeEvent("upsert", "demo")
			evt.Replicas = 2
			evt.Autoscaling = policy
			obj, err := testReconciler().buildLLMInferenceService(context.Background(), evt)
			require.NoError(t, err)
			_, hasScaling, err := unstructured.NestedMap(obj.Object, "spec", "scaling")
			require.NoError(t, err)
			assert.False(t, hasScaling, "KServe rejects a scaling block the platform cannot back")
			replicas, ok, err := unstructured.NestedInt64(obj.Object, "spec", "replicas")
			require.NoError(t, err)
			require.True(t, ok)
			assert.GreaterOrEqual(t, replicas, int64(1), "a service keeps a fixed replica count")
		})
	}
}

// TestWeightModelURI pins the URI KServe records for a model: the claim plus
// the model's directory, and just the claim when the model sits at its root.
func TestWeightModelURI(t *testing.T) {
	assert.Equal(t, "pvc://model-weights/qwen/v1", weightModelURI("model-weights", "qwen/v1"))
	assert.Equal(t, "pvc://model-weights/qwen/v1", weightModelURI("model-weights", "/qwen/v1/"))
	assert.Equal(t, "pvc://model-weights", weightModelURI("model-weights", ""))
	assert.Equal(t, "pvc://model-weights", weightModelURI("model-weights", "/"))
}

// TestApplyLLMInferenceServiceCreatesThenUpdates proves the second apply
// updates the object the first one created instead of failing on the taken
// name.
func TestApplyLLMInferenceServiceCreatesThenUpdates(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	r := testReconciler()
	r.dynamic = newFakeDynamic()
	obj, err := r.buildLLMInferenceService(context.Background(), evt)
	require.NoError(t, err)
	require.NoError(t, applyLLMInferenceService(context.Background(), r.dynamic, obj))
	// A duplicate create would come back as AlreadyExists here.
	require.NoError(t, applyLLMInferenceService(context.Background(), r.dynamic, obj))
}
