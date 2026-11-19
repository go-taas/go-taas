package model

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/transfer"
	"github.com/go-taas/go-taas/pkg/warmup"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
)

type recordingPublisher struct {
	subject string
	body    []byte
	err     error
}

func (p *recordingPublisher) Publish(_ context.Context, subject string, body []byte, _ map[string]string) error {
	p.subject = subject
	p.body = append([]byte(nil), body...)
	return p.err
}

func storageTestService(t *testing.T) (*Service, storage.Backend, storage.Backend) {
	t.Helper()
	svc := newModelTestService(t)
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	cfg := storage.Config{
		DefaultBackend: "source",
		Backends: map[string]storage.Backend{
			"source": {
				Name:       "source",
				Type:       storage.TypeFilesystem,
				Filesystem: storage.Filesystem{ExistingClaim: "source-claim", MountPath: sourceRoot},
				Delivery:   storage.Delivery{Default: storage.ModeMount, Allowed: []string{storage.ModeMount}},
			},
			"target": {
				Name:       "target",
				Type:       storage.TypeFilesystem,
				Filesystem: storage.Filesystem{ExistingClaim: "target-claim", MountPath: targetRoot},
				Delivery:   storage.Delivery{Default: storage.ModeMount, Allowed: []string{storage.ModeMount}},
			},
		},
	}
	svc.SetStorageConfig(cfg)
	return svc, cfg.Backends["source"], cfg.Backends["target"]
}

func TestListStorageBackendsReportsDeliveryAndWarmup(t *testing.T) {
	svc, source, _ := storageTestService(t)
	svc.SetWarmupSettings(true, nil)
	resp, err := svc.ListStorageBackends(adminCtx(), &modelv1.ListStorageBackendsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetBackends(), 2)
	assert.Equal(t, "source", resp.GetDefaultBackend())
	for _, backend := range resp.GetBackends() {
		if backend.GetName() == source.Name {
			assert.True(t, backend.GetIsDefault())
			assert.Equal(t, source.Filesystem.MountPath, backend.GetMountPath())
			// A filesystem backend has no cache delivery unless declared.
			assert.False(t, backend.GetWarmupEnabled())
		}
	}
}

func TestStartModelTransferQueuesCopy(t *testing.T) {
	svc, source, target := storageTestService(t)
	pub := &recordingPublisher{}
	svc.SetTransferPublisher(pub)
	svc.SetTransferSettings(TransferSettings{Mode: "job", Namespace: "taas-jobs", Image: "taas-transfer:test"})
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen", HubModelId: "qwen/hub", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)

	resp, err := svc.StartModelTransfer(adminCtx(), &modelv1.StartModelTransferRequest{
		ModelId: reg.GetModelId(), Operation: transfer.OperationCopy,
		SourceBackend: source.Name, TargetBackend: target.Name,
	})
	require.NoError(t, err)
	assert.Equal(t, transfer.StateQueued, resp.GetTransfer().GetState())
	assert.Equal(t, "model.transfers", pub.subject)
	var spec transfer.Spec
	require.NoError(t, json.Unmarshal(pub.body, &spec))
	assert.Equal(t, source.Name, spec.SourceBackend)
	assert.Equal(t, target.Name, spec.TargetBackend)
	assert.Equal(t, filepath.Join(source.Filesystem.MountPath, "qwen/v1"), spec.SourcePath)
	assert.Equal(t, filepath.Join(target.Filesystem.MountPath, "qwen/v1"), spec.TargetPath)

	// The transfer row is queryable and cancellable.
	listed, err := svc.ListModelTransfers(adminCtx(), &modelv1.ListModelTransfersRequest{})
	require.NoError(t, err)
	require.Len(t, listed.GetTransfers(), 1)
	cancelled, err := svc.CancelModelTransfer(adminCtx(), &modelv1.CancelModelTransferRequest{TransferId: resp.GetTransfer().GetTransferId()})
	require.NoError(t, err)
	assert.Equal(t, transfer.StateCancelled, cancelled.GetTransfer().GetState())
}

func TestHandleModelTransferStatusUpdatesRow(t *testing.T) {
	svc, source, target := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	svc.SetTransferSettings(TransferSettings{Mode: "job", Image: "taas-transfer:test"})
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen", HubModelId: "qwen/hub", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)
	resp, err := svc.StartModelTransfer(adminCtx(), &modelv1.StartModelTransferRequest{
		ModelId: reg.GetModelId(), SourceBackend: source.Name, TargetBackend: target.Name,
	})
	require.NoError(t, err)

	body, _ := json.Marshal(transfer.Status{
		ID: resp.GetTransfer().GetTransferId(), State: transfer.StateSucceeded,
		Bytes: 42, TotalBytes: 42, JobName: "taas-transfer-x",
	})
	require.NoError(t, svc.HandleModelTransferStatus(adminCtx(), body))
	got, err := svc.GetModelTransfer(adminCtx(), &modelv1.GetModelTransferRequest{TransferId: resp.GetTransfer().GetTransferId()})
	require.NoError(t, err)
	assert.Equal(t, transfer.StateSucceeded, got.GetTransfer().GetState())
	assert.Equal(t, int64(42), got.GetTransfer().GetBytes())
	assert.Equal(t, "taas-transfer-x", got.GetTransfer().GetJobName())
}

func TestWarmModelAndStatus(t *testing.T) {
	svc, _, _ := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	svc.SetWarmupSettings(true, []WarmupNodeGroupSettings{{Name: "gpu", StorageLimit: "10Gi"}})
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen", HubModelId: "qwen/hub", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)
	resp, err := svc.WarmModel(adminCtx(), &modelv1.WarmModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	require.NotNil(t, resp.GetCache())
	assert.Equal(t, warmup.StateQueued, resp.GetCache().GetState())

	body, _ := json.Marshal(warmup.Status{
		ID: resp.GetCache().GetCacheId(), State: warmup.StateReady,
		CacheName: "taas-model-x", WarmedNodes: []string{"node-1"}, TotalNodes: 1,
	})
	require.NoError(t, svc.HandleModelWarmupStatus(adminCtx(), body))
	list, err := svc.ListModelCacheEntries(adminCtx(), &modelv1.ListModelCacheEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetCaches(), 1)
	assert.Equal(t, warmup.StateReady, list.GetCaches()[0].GetState())
	assert.Equal(t, []string{"node-1"}, list.GetCaches()[0].GetWarmedNodes())
}

func TestWarmModelIsIdempotentWhileCacheLive(t *testing.T) {
	svc, _, _ := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	svc.SetWarmupSettings(true, []WarmupNodeGroupSettings{{Name: "gpu", StorageLimit: "10Gi"}})
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen-idem", HubModelId: "qwen/hub", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)
	first, err := svc.WarmModel(adminCtx(), &modelv1.WarmModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	second, err := svc.WarmModel(adminCtx(), &modelv1.WarmModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	assert.Equal(t, first.GetCache().GetCacheId(), second.GetCache().GetCacheId())

	list, err := svc.ListModelCacheEntries(adminCtx(), &modelv1.ListModelCacheEntriesRequest{})
	require.NoError(t, err)
	assert.Len(t, list.GetCaches(), 1)
}

func TestCompleteDownloadTransferRegistersModel(t *testing.T) {
	svc, source, _ := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	// Stage a fake weight tree so registration has a path to point at.
	require.NoError(t, os.MkdirAll(filepath.Join(source.Filesystem.MountPath, "qwen/v1"), 0o755))

	job := &DownloadJob{ID: "download-1", Source: "modelscope", HubModelID: "qwen/hub",
		Name: "qwen", Version: "v1", Description: "d", State: DownloadStateQueued}
	db, err := svc.gormDB()
	require.NoError(t, err)
	require.NoError(t, db.Create(job).Error)

	_, err = svc.StartModelTransfer(adminCtx(), &modelv1.StartModelTransferRequest{
		ModelId: "", Operation: transfer.OperationCopy,
	})
	require.Error(t, err)

	// The completion path is exercised directly: a successful download
	// transfer registers the catalog entry and settles the job row.
	row := &ModelTransfer{
		ID: "transfer-1", Operation: transfer.OperationDownload, ModelName: "qwen",
		Version: "v1", WeightPath: "qwen/v1/", DownloadID: &job.ID,
		State: transfer.StateRunning,
	}
	require.NoError(t, db.Create(row).Error)
	svc.completeDownloadTransfer(context.Background(), row, transfer.Status{
		ID: row.ID, State: transfer.StateSucceeded, Bytes: 7,
	})
	var got DownloadJob
	require.NoError(t, db.Where("id = ?", job.ID).First(&got).Error)
	assert.Equal(t, DownloadStateSucceeded, got.State)
	assert.NotEmpty(t, got.ModelID)
}
