package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/warmup"
)

func testWarmupReconciler(objects ...runtime.Object) (*ModelWarmupReconciler, *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, objects...)
	return NewModelWarmupReconciler(dyn, mq.NewFake(), config.WarmupConfig{
		Enabled: true,
		NodeGroups: []config.WarmupNodeGroup{{
			Name:         "worker",
			StorageLimit: "200Gi",
			HostPath:     "/var/lib/taas/models",
			StorageClass: "local-path",
			NodeSelector: map[string]string{"kserve/localmodel": "worker"},
		}},
	}, "taas-infer", WarmupSeedConfig{}), dyn
}

func TestApplyModelWarmupCreatesGroupAndCache(t *testing.T) {
	reconciler, dyn := testWarmupReconciler()
	body, err := json.Marshal(&warmup.Request{
		ID:         "cache-1",
		ModelID:    "model-1",
		ModelName:  "qwen",
		Version:    "v1",
		SourceURI:  "pvc://model-weights/qwen/v1",
		SizeBytes:  1024,
		NodeGroups: []warmup.NodeGroup{{Name: "worker", StorageLimit: "200Gi", HostPath: "/var/lib/taas/models", NodeSelector: map[string]string{"kserve/localmodel": "worker"}}},
	})
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyModelWarmup(context.Background(), mq.Message{Body: body}))

	group, err := dyn.Resource(localModelNodeGroupGVR).Get(context.Background(), "worker", metav1.GetOptions{})
	require.NoError(t, err)
	spec := group.Object["spec"].(map[string]any)
	assert.Equal(t, "200Gi", spec["storageLimit"])
	volumeSpec := spec["persistentVolumeSpec"].(map[string]any)
	affinity := volumeSpec["nodeAffinity"].(map[string]any)
	required := affinity["required"].(map[string]any)
	terms := required["nodeSelectorTerms"].([]any)
	require.Len(t, terms, 1)
	expressions := terms[0].(map[string]any)["matchExpressions"].([]any)
	require.Len(t, expressions, 1)
	assert.Equal(t, "kserve/localmodel", expressions[0].(map[string]any)["key"])
	assert.Equal(t, "worker", expressions[0].(map[string]any)["values"].([]any)[0])

	cache, err := dyn.Resource(localModelCacheGVR).Get(context.Background(), "taas-model-cache-1", metav1.GetOptions{})
	require.NoError(t, err)
	cacheSpec := cache.Object["spec"].(map[string]any)
	assert.Equal(t, "pvc://model-weights/qwen/v1", cacheSpec["sourceModelUri"])
	assert.Equal(t, []any{"worker"}, cacheSpec["nodeGroups"])
	assert.Equal(t, "qwen", cache.GetAnnotations()["taas.io/model-name"])
	assert.Equal(t, "v1", cache.GetAnnotations()["taas.io/model-version"])

	// Applying again updates instead of failing on AlreadyExists.
	require.NoError(t, reconciler.ApplyModelWarmup(context.Background(), mq.Message{Body: body}))
}

func TestApplyModelWarmupCancelDeletesCache(t *testing.T) {
	cache := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata": map[string]any{
			"name":   "taas-model-cache-2",
			"labels": map[string]any{"taas.io/model-cache-id": "cache-2"},
		},
	}}
	reconciler, dyn := testWarmupReconciler(cache)
	body, _ := json.Marshal(&warmup.Request{ID: "cache-2"})
	require.NoError(t, reconciler.ApplyModelWarmup(context.Background(), mq.Message{Body: body}))
	_, err := dyn.Resource(localModelCacheGVR).Get(context.Background(), "taas-model-cache-2", metav1.GetOptions{})
	assert.Error(t, err)
}

func TestReportCachesPublishesReadyState(t *testing.T) {
	cache := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata": map[string]any{
			"name":   "taas-model-cache-3",
			"labels": map[string]any{"taas.io/model-cache-id": "cache-3"},
		},
		"spec": map[string]any{
			"nodeGroups": []any{"worker"},
			"modelSize":  "2Gi",
		},
		"status": map[string]any{
			"copies": map[string]any{"available": int64(2)},
			"nodeStatus": map[string]any{
				"node-a": "NodeDownloaded",
				"node-b": "NodeDownloaded",
			},
		},
	}}
	reconciler, _ := testWarmupReconciler(cache)
	clientMQ := mq.NewFake()
	reconciler.client = clientMQ
	var got []warmup.Status
	require.NoError(t, clientMQ.Subscribe(context.Background(), mq.DefaultSubjects().ModelWarmupStatus, func(msg mq.Message) error {
		var status warmup.Status
		if err := json.Unmarshal(msg.Body, &status); err != nil {
			return err
		}
		got = append(got, status)
		return nil
	}))
	require.NoError(t, reconciler.reportCaches(context.Background()))
	require.Len(t, got, 1)
	assert.Equal(t, warmup.StateReady, got[0].State)
	assert.ElementsMatch(t, []string{"node-a", "node-b"}, got[0].WarmedNodes)
}

func TestApplyWarmupAffinity(t *testing.T) {
	cache := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata": map[string]any{
			"name":        "taas-model-cache-affinity",
			"annotations": map[string]any{"taas.io/model-name": "qwen", "taas.io/model-version": "v1"},
		},
	}}
	node := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelNode",
		"metadata":   map[string]any{"name": "node-a"},
		"spec": map[string]any{
			"localModels": []any{map[string]any{"modelName": "taas-model-cache-affinity"}},
		},
		"status": map[string]any{
			"modelStatus": map[string]any{"taas-model-cache-affinity": "ModelDownloaded"},
		},
	}}
	_, dyn := testWarmupReconciler(cache, node)
	reconciler := &k8sReconciler{dynamic: dyn, warmupEnabled: true, warmupMode: "preferred", warmupWeight: 80}
	spec := map[string]any{}
	reconciler.applyWarmupAffinity(context.Background(), spec, "qwen", "v1")
	affinity, ok := spec["affinity"].(map[string]any)
	require.True(t, ok)
	nodeAffinity := affinity["nodeAffinity"].(map[string]any)
	preferred := nodeAffinity["preferredDuringSchedulingIgnoredDuringExecution"].([]any)
	assert.Equal(t, int64(80), preferred[0].(map[string]any)["weight"])

	// A different version is not warmed by this cache.
	otherVersion := map[string]any{}
	reconciler.applyWarmupAffinity(context.Background(), otherVersion, "qwen", "v2")
	assert.Empty(t, otherVersion)

	// An unwarmed model leaves the pod spec untouched.
	untouched := map[string]any{}
	reconciler.applyWarmupAffinity(context.Background(), untouched, "missing", "")
	assert.Empty(t, untouched)
}

func TestSeedCachesCreatesPVCSeedJobAndMarksNodeDownloaded(t *testing.T) {
	sourceURI := "pvc://taas-model-weights/qwen/v1"
	cache := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata": map[string]any{
			"name":   "taas-model-cache-seed",
			"labels": map[string]any{"taas.io/model-cache-id": "cache-seed"},
		},
		"spec": map[string]any{
			"sourceModelUri": sourceURI,
			"nodeGroups":     []any{"worker"},
		},
	}}
	node := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelNode",
		"metadata":   map[string]any{"name": "sd4090"},
		"spec": map[string]any{
			"localModels": []any{map[string]any{
				"modelName":      "taas-model-cache-seed",
				"nodeGroup":      "worker",
				"sourceModelUri": sourceURI,
			}},
		},
	}}
	reconciler, dyn := testWarmupReconciler(cache, node)
	reconciler.seed = WarmupSeedConfig{
		Image:          "registry/taas-transfer:test",
		Namespace:      "taas",
		ServiceAccount: "taas-transfer",
		NodeSelector:   map[string]string{"kubernetes.io/os": "linux"},
	}
	require.NoError(t, reconciler.seedCaches(context.Background()))

	job, err := dyn.Resource(jobGVR).Namespace("taas").Get(context.Background(), seedJobName("taas-model-cache-seed", "sd4090"), metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, seedJobName("taas-model-cache-seed", "sd4090"), job.GetName())
	jobSpec := job.Object["spec"].(map[string]any)
	template := jobSpec["template"].(map[string]any)["spec"].(map[string]any)
	assert.Equal(t, "sd4090", template["nodeName"])
	assert.Equal(t, "taas-transfer", template["serviceAccountName"])
	container := template["containers"].([]any)[0].(map[string]any)
	env := container["env"].([]any)[0].(map[string]any)
	assert.Equal(t, "TRANSFER_SPEC_JSON", env["name"])
	var transferSpec map[string]any
	require.NoError(t, json.Unmarshal([]byte(env["value"].(string)), &transferSpec))
	assert.Equal(t, "/source/qwen/v1", transferSpec["sourcePath"])
	assert.Equal(t, "/target/models/"+storageKeyOf(sourceURI), transferSpec["targetPath"])

	// A successful Job flips the node entry to ModelDownloaded.
	job.Object["status"] = map[string]any{"succeeded": int64(1)}
	_, err = dyn.Resource(jobGVR).Namespace("taas").Update(context.Background(), job, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.NoError(t, reconciler.seedCaches(context.Background()))
	updated, err := dyn.Resource(localModelNodeGVR).Get(context.Background(), "sd4090", metav1.GetOptions{})
	require.NoError(t, err)
	status := updated.Object["status"].(map[string]any)
	modelStatus := status["modelStatus"].(map[string]any)
	assert.Equal(t, "ModelDownloaded", modelStatus["taas-model-cache-seed"])
}

func TestParsePVCURIAndStorageKey(t *testing.T) {
	claim, path, ok := parsePVCURI("pvc://weights/qwen/v1/")
	assert.True(t, ok)
	assert.Equal(t, "weights", claim)
	assert.Equal(t, "qwen/v1", path)

	_, _, ok = parsePVCURI("s3://bucket/key")
	assert.False(t, ok)
	assert.Equal(t, "471591f9c6a5faf7", storageKeyOf("pvc://taas-model-weights/storage-warmup-test/upload-20261119-170954"))
	assert.LessOrEqual(t, len(seedJobName("taas-model-1e8ff38d-50af-4603-807a-3f08d37ac215", "sd4090")), 63)
}
