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
		}},
	}, "taas-infer"), dyn
}

func TestApplyModelWarmupCreatesGroupAndCache(t *testing.T) {
	reconciler, dyn := testWarmupReconciler()
	body, err := json.Marshal(&warmup.Request{
		ID:         "cache-1",
		ModelID:    "model-1",
		ModelName:  "qwen",
		SourceURI:  "pvc://model-weights/qwen/v1",
		SizeBytes:  1024,
		NodeGroups: []warmup.NodeGroup{{Name: "worker", StorageLimit: "200Gi", HostPath: "/var/lib/taas/models"}},
	})
	require.NoError(t, err)
	require.NoError(t, reconciler.ApplyModelWarmup(context.Background(), mq.Message{Body: body}))

	group, err := dyn.Resource(localModelNodeGroupGVR).Get(context.Background(), "worker", metav1.GetOptions{})
	require.NoError(t, err)
	spec := group.Object["spec"].(map[string]any)
	assert.Equal(t, "200Gi", spec["storageLimit"])

	cache, err := dyn.Resource(localModelCacheGVR).Get(context.Background(), "taas-model-cache-1", metav1.GetOptions{})
	require.NoError(t, err)
	cacheSpec := cache.Object["spec"].(map[string]any)
	assert.Equal(t, "pvc://model-weights/qwen/v1", cacheSpec["sourceModelUri"])
	assert.Equal(t, []any{"worker"}, cacheSpec["nodeGroups"])

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
	node := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelNode",
		"metadata":   map[string]any{"name": "node-a"},
		"spec": map[string]any{
			"localModels": []any{map[string]any{"modelName": "qwen"}},
		},
		"status": map[string]any{
			"modelStatus": map[string]any{"qwen": "ModelDownloaded"},
		},
	}}
	_, dyn := testWarmupReconciler(node)
	reconciler := &k8sReconciler{dynamic: dyn, warmupEnabled: true, warmupMode: "preferred", warmupWeight: 80}
	spec := map[string]any{}
	reconciler.applyWarmupAffinity(context.Background(), spec, "qwen")
	affinity, ok := spec["affinity"].(map[string]any)
	require.True(t, ok)
	nodeAffinity := affinity["nodeAffinity"].(map[string]any)
	preferred := nodeAffinity["preferredDuringSchedulingIgnoredDuringExecution"].([]any)
	assert.Equal(t, int64(80), preferred[0].(map[string]any)["weight"])

	// An unwarmed model leaves the pod spec untouched.
	untouched := map[string]any{}
	reconciler.applyWarmupAffinity(context.Background(), untouched, "missing")
	assert.Empty(t, untouched)
}
