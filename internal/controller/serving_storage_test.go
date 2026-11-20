package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
)

func TestServingWeightsMountUsesEventClaim(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Model.Delivery = "mount"
	evt.Model.ClaimName = "other-weights"
	evt.Model.MountPath = "/mnt/weights"

	volume, mount, dir, init := testReconciler().servingWeights(evt)

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

	volume, mount, dir, init := testReconciler().servingWeights(evt)

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
	volume, mount, dir, init := r.servingWeights(evt)

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

	volume, _, dir, _ := testReconciler().servingWeights(evt)

	require.NotNil(t, volume.PersistentVolumeClaim)
	assert.Equal(t, "model-weights", volume.PersistentVolumeClaim.ClaimName)
	assert.Equal(t, "/data/weights/qwen/v1", dir)
}
