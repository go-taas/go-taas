package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestServingWeightsMountUsesEventClaim(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Model.Delivery = "mount"
	evt.Model.ClaimName = "other-weights"
	evt.Model.MountPath = "/mnt/weights"

	volume, mount, dir, init := testReconciler().servingWeights(context.Background(), evt)

	assert.Equal(t, "weights", volume.Name)
	require.NotNil(t, volume.PersistentVolumeClaim)
	assert.Equal(t, "other-weights", volume.PersistentVolumeClaim.ClaimName)
	assert.Equal(t, "/mnt/weights", mount.MountPath)
	assert.Equal(t, "/mnt/weights/qwen/v1", dir)
	assert.Empty(t, init)
}

func TestServingWeightsDownloadAddsInitializer(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Model.Delivery = "download"
	evt.Model.ModelURI = "s3://models/qwen/v1"
	evt.Model.DownloadImage = "kserve/storage-initializer:v0.21.0"
	evt.Model.CredentialsSecret = "minio-keys"
	evt.Model.ObjectEndpoint = "http://minio:9000"
	evt.Model.ObjectRegion = "us-east-1"
	evt.Model.ObjectForcePathStyle = true

	volume, mount, dir, init := testReconciler().servingWeights(context.Background(), evt)

	assert.NotNil(t, volume.EmptyDir)
	assert.Equal(t, "/models", mount.MountPath)
	assert.Equal(t, "/models", dir)
	require.Len(t, init, 1)
	assert.Equal(t, "storage-initializer", init[0].Name)
	assert.Equal(t, []string{"s3://models/qwen/v1", "/models"}, init[0].Args)
	assert.NotEmpty(t, init[0].Env)
}

func TestServingWeightsCacheUsesHostPath(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Model.Delivery = "cache"
	evt.Model.ModelURI = "s3://models/qwen/v1"

	r := testReconciler()
	r.warmupHostPath = "/models"
	volume, mount, dir, init := r.servingWeights(context.Background(), evt)

	require.NotNil(t, volume.HostPath)
	assert.Equal(t, "/models", volume.HostPath.Path)
	require.NotNil(t, volume.HostPath.Type)
	assert.Equal(t, corev1.HostPathDirectory, *volume.HostPath.Type)
	assert.Equal(t, "/models", mount.MountPath)
	assert.Equal(t, "/models/models/"+storageKeyOf("s3://models/qwen/v1"), dir)
	assert.Empty(t, init)
}

func TestServingWeightsFallsBackToMountWithoutURI(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Model.Delivery = "download"

	volume, _, dir, _ := testReconciler().servingWeights(context.Background(), evt)

	require.NotNil(t, volume.PersistentVolumeClaim)
	assert.Equal(t, "model-weights", volume.PersistentVolumeClaim.ClaimName)
	assert.Equal(t, "/data/weights/qwen/v1", dir)
}

func TestServingWeightsCacheResolvesNodeGroupHostPath(t *testing.T) {
	cache := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelCache",
		"metadata":   map[string]any{"name": "taas-model-cache-1"},
		"spec": map[string]any{
			"sourceModelUri": "s3://models/qwen/v1",
			"nodeGroups":     []any{"gpu"},
		},
	}}
	group := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.kserve.io/v1alpha1",
		"kind":       "LocalModelNodeGroup",
		"metadata":   map[string]any{"name": "gpu"},
		"spec": map[string]any{
			"persistentVolumeSpec": map[string]any{
				"hostPath": map[string]any{"path": "/var/lib/taas/cache"},
			},
		},
	}}
	fake := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		localModelCacheGVR:     "LocalModelCacheList",
		localModelNodeGroupGVR: "LocalModelNodeGroupList",
	}, cache, group)
	r := testReconciler()
	r.dynamic = fake
	r.warmupHostPath = "/models"
	evt := testChangeEvent("upsert", "demo")
	evt.Model.Delivery = "cache"
	evt.Model.ModelURI = "s3://models/qwen/v1"

	volume, mount, dir, _ := r.servingWeights(context.Background(), evt)

	require.NotNil(t, volume.HostPath)
	assert.Equal(t, "/var/lib/taas/cache", volume.HostPath.Path)
	assert.Equal(t, "/var/lib/taas/cache", mount.MountPath)
	assert.Equal(t, "/var/lib/taas/cache/models/"+storageKeyOf("s3://models/qwen/v1"), dir)
}

func TestServingWeightsCacheFallsBackWithoutCache(t *testing.T) {
	fake := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		localModelCacheGVR: "LocalModelCacheList",
	})
	r := testReconciler()
	r.dynamic = fake
	r.warmupHostPath = "/models"
	evt := testChangeEvent("upsert", "demo")
	evt.Model.Delivery = "cache"
	evt.Model.ModelURI = "s3://models/missing/v1"

	volume, _, dir, _ := r.servingWeights(context.Background(), evt)

	require.NotNil(t, volume.HostPath)
	assert.Equal(t, "/models", volume.HostPath.Path)
	assert.Equal(t, "/models/models/"+storageKeyOf("s3://models/missing/v1"), dir)
}
