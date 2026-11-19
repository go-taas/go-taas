package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/storage"
)

func TestApplyDefaultsSynthesizesStorageBackend(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()

	require.True(t, cfg.Storage.Enabled())
	assert.Equal(t, "default", cfg.Storage.DefaultBackend)
	backend, err := cfg.Storage.Resolve("")
	require.NoError(t, err)
	assert.Equal(t, storage.TypeFilesystem, backend.Type)
	assert.Equal(t, "model-weights", backend.Filesystem.ExistingClaim)
	assert.Equal(t, "/data/weights", backend.Filesystem.MountPath)
	assert.Equal(t, storage.ModeMount, backend.Delivery.Default)
}

func TestApplyDefaultsStorageTransferWarmup(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()

	assert.Equal(t, "job", cfg.Transfer.Mode)
	assert.Equal(t, "taas-jobs", cfg.Transfer.Namespace)
	assert.Equal(t, 4, cfg.Transfer.Concurrency)
	assert.Equal(t, "taas-transfer", cfg.Transfer.ServiceAccount)
	assert.NotZero(t, cfg.Transfer.ProgressInterval)
	assert.Equal(t, int32(2), cfg.Transfer.Job.BackoffLimit)
	assert.Equal(t, int64(21600), cfg.Transfer.Job.ActiveDeadlineSeconds)
	assert.Equal(t, int32(86400), cfg.Transfer.Job.TTLSecondsAfterFinish)

	assert.Equal(t, "preferred", cfg.Warmup.Scheduling.Mode)
	assert.Equal(t, int32(100), cfg.Warmup.Scheduling.Weight)
	assert.Equal(t, "lru", cfg.Warmup.Eviction.Policy)
	assert.Equal(t, 20, cfg.Warmup.Eviction.MaxModelsPerNode)
	assert.Positive(t, cfg.Warmup.Eviction.MinFreeBytes)
}

func TestValidateRejectsInvalidTransferMode(t *testing.T) {
	cfg := &Configuration{Transfer: TransferConfig{Mode: "async"}}
	assert.Error(t, cfg.Validate())
}

func TestValidateRejectsCacheDeliveryWithoutWarmup(t *testing.T) {
	cfg := &Configuration{}
	cfg.applyDefaults()
	cfg.Storage.Backends["object"] = storage.Backend{
		Name: "object",
		Type: storage.TypeObjectStore,
		ObjectStore: storage.ObjectStore{
			Endpoint:    "http://minio:9000",
			Bucket:      "models",
			Credentials: storage.Credentials{ExistingSecret: "s3-keys"},
		},
		Delivery: storage.Delivery{Default: storage.ModeCache, Allowed: []string{storage.ModeCache}},
	}
	cfg.Storage.DefaultBackend = "object"
	assert.Error(t, cfg.Validate())

	cfg.Warmup.Enabled = true
	cfg.Warmup.NodeGroups = []WarmupNodeGroup{{Name: "gpu"}}
	assert.NoError(t, cfg.Validate())
}

func TestValidateRejectsWarmupWithoutNodeGroups(t *testing.T) {
	cfg := &Configuration{Warmup: WarmupConfig{Enabled: true}}
	assert.Error(t, cfg.Validate())
}

func TestValidateRejectsInvalidWarmupWeight(t *testing.T) {
	cfg := &Configuration{Warmup: WarmupConfig{Scheduling: WarmupScheduling{Weight: 101}}}
	assert.Error(t, cfg.Validate())
}
