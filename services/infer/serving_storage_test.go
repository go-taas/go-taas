package infer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/services/model"
)

func TestServingStorageForFilesystemMount(t *testing.T) {
	cfg := storage.Config{
		DefaultBackend: "shared",
		Backends: map[string]storage.Backend{
			"shared": {
				Type: storage.TypeFilesystem,
				Filesystem: storage.Filesystem{
					ExistingClaim: "taas-model-weights",
					MountPath:     "/data/weights",
				},
				Delivery: storage.Delivery{Default: storage.ModeMount, Allowed: []string{storage.ModeMount}},
			},
		},
	}
	svc := &Service{storageConfig: cfg.Normalize("", "", "")}
	plan := svc.servingStorageFor(&model.Version{Version: "v1", WeightPath: "qwen/v1"})
	assert.Equal(t, "shared", plan.Backend)
	assert.Equal(t, storage.ModeMount, plan.Delivery)
	assert.Equal(t, "pvc://taas-model-weights/qwen/v1", plan.URI)
	assert.Equal(t, "/data/weights", plan.MountPath)
	assert.Equal(t, "taas-model-weights", plan.Claim)
}

func TestServingStorageForObjectStoreDownload(t *testing.T) {
	cfg := storage.Config{
		DefaultBackend: "archive",
		Backends: map[string]storage.Backend{
			"archive": {
				Type: storage.TypeObjectStore,
				ObjectStore: storage.ObjectStore{
					Endpoint:       "http://minio:9000",
					Region:         "us-east-1",
					Bucket:         "models",
					Prefix:         "weights",
					ForcePathStyle: true,
					Credentials: storage.Credentials{
						ExistingSecret: "minio-keys",
						AccessKeyKey:   "accessKey",
						SecretKeyKey:   "secretKey",
					},
				},
				Delivery: storage.Delivery{Default: storage.ModeDownload, Allowed: []string{storage.ModeDownload}},
			},
		},
	}
	svc := &Service{storageConfig: cfg.Normalize("", "", "")}
	plan := svc.servingStorageFor(&model.Version{
		Version: "v2", WeightPath: "qwen/v2", StorageBackend: "archive",
	})
	assert.Equal(t, storage.ModeDownload, plan.Delivery)
	assert.Equal(t, "s3://models/weights/qwen/v2", plan.URI)
	assert.Equal(t, "minio-keys", plan.CredentialsSecret)
	assert.Equal(t, "http://minio:9000", plan.Endpoint)
	assert.True(t, plan.ForcePathStyle)
	assert.NotEmpty(t, plan.DownloadImage)
}

func TestServingStorageForCacheUsesHostPathDelivery(t *testing.T) {
	cfg := storage.Config{
		DefaultBackend: "archive",
		Backends: map[string]storage.Backend{
			"archive": {
				Type: storage.TypeObjectStore,
				ObjectStore: storage.ObjectStore{
					Endpoint: "http://minio:9000",
					Bucket:   "models",
					Prefix:   "weights",
					Credentials: storage.Credentials{
						ExistingSecret: "minio-keys",
					},
				},
				Delivery: storage.Delivery{
					Default: storage.ModeCache,
					Allowed: []string{storage.ModeCache},
				},
			},
		},
	}
	svc := &Service{storageConfig: cfg.Normalize("", "", "")}
	plan := svc.servingStorageFor(&model.Version{
		Version: "v2", WeightPath: "qwen/v2", StorageBackend: "archive",
	})
	// Cache delivery points the controller at the node-local hostPath; the
	// object-store fields still describe the source the node agent copies.
	assert.Equal(t, storage.ModeCache, plan.Delivery)
	assert.Equal(t, "s3://models/weights/qwen/v2", plan.URI)
	assert.Empty(t, plan.MountPath)
}

func TestApplyServingStorageLeavesLegacyEventUntouched(t *testing.T) {
	svc := &Service{}
	evt := changeEvent{}
	evt.Model.WeightPath = "qwen/v1"
	got := svc.applyServingStorage(evt, &model.Version{Version: "v1", WeightPath: "qwen/v1"})
	assert.Equal(t, "", got.Model.StorageBackend)
	assert.Equal(t, "", got.Model.Delivery)
}
