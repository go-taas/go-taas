package model

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/transfer"
	"github.com/go-taas/go-taas/pkg/warmup"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
)

var errPublish = errors.New("publish failed")

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
	svc.SetWarmupSettings(true)
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

	// The copied version now lives in the target backend.
	version, err := svc.repo.FindVersion(adminCtx(), reg.GetModelId(), "v1")
	require.NoError(t, err)
	assert.Equal(t, target.Name, version.StorageBackend)
}

func TestWarmModelAndStatus(t *testing.T) {
	svc, _, _ := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	svc.SetWarmupSettings(true)
	createTestWarmupGroup(t, svc, "gpu", "10Gi")
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

func TestWarmModelRecordsWeightSize(t *testing.T) {
	svc, source, _ := storageTestService(t)
	pub := &recordingPublisher{}
	svc.SetTransferPublisher(pub)
	svc.SetWarmupSettings(true)
	createTestWarmupGroup(t, svc, "gpu", "10Gi")
	// A warmup row must carry the real weight size so the console and the
	// LocalModelCache storage accounting do not fall back to a placeholder.
	dir := filepath.Join(source.Filesystem.MountPath, "qwen/v1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.bin"), make([]byte, 4096), 0o644))
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen-size", HubModelId: "qwen/size", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)

	resp, err := svc.WarmModel(adminCtx(), &modelv1.WarmModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	assert.EqualValues(t, 4096, resp.GetCache().GetSizeBytes())

	var request warmup.Request
	require.NoError(t, json.Unmarshal(pub.body, &request))
	assert.EqualValues(t, 4096, request.SizeBytes)
}

func TestWarmModelFallsBackToRecordedDownloadSize(t *testing.T) {
	svc, _, _ := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	svc.SetWarmupSettings(true)
	createTestWarmupGroup(t, svc, "gpu", "10Gi")
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen-remote", HubModelId: "qwen/remote", Version: "v1", WeightPath: "qwen/remote",
	})
	require.NoError(t, err)
	db, err := svc.gormDB()
	require.NoError(t, err)
	require.NoError(t, db.Create(&DownloadJob{
		ID: uuid.NewString(), Source: "modelscope", HubModelID: "qwen/remote",
		Name: "qwen-remote", State: DownloadStateSucceeded,
		TotalBytes: 8192, ModelID: reg.GetModelId(), CreatedAt: time.Now().UTC(),
	}).Error)

	resp, err := svc.WarmModel(adminCtx(), &modelv1.WarmModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	// The backend filesystem is not mounted in this test, so the recorded
	// byte count is the only size the server can trust.
	assert.EqualValues(t, 8192, resp.GetCache().GetSizeBytes())
}

func TestListWarmupsBackfillsLegacyOneByteSize(t *testing.T) {
	svc, source, _ := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	svc.SetWarmupSettings(true)
	createTestWarmupGroup(t, svc, "gpu", "10Gi")
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen-legacy", HubModelId: "qwen/legacy", Version: "v1", WeightPath: "qwen/legacy",
	})
	require.NoError(t, err)
	resp, err := svc.WarmModel(adminCtx(), &modelv1.WarmModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	db, err := svc.gormDB()
	require.NoError(t, err)
	// Rows created before size resolution carry the 1-byte placeholder the
	// controller wrote into LocalModelCache.modelSize.
	require.NoError(t, db.Model(&ModelCacheEntry{}).Where("id = ?", resp.GetCache().GetCacheId()).
		Update("size_bytes", 1).Error)
	dir := filepath.Join(source.Filesystem.MountPath, "qwen/legacy")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.bin"), make([]byte, 4096), 0o644))

	list, err := svc.ListModelCacheEntries(adminCtx(), &modelv1.ListModelCacheEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetCaches(), 1)
	assert.EqualValues(t, 4096, list.GetCaches()[0].GetSizeBytes())
	var row ModelCacheEntry
	require.NoError(t, db.Where("id = ?", resp.GetCache().GetCacheId()).First(&row).Error)
	assert.EqualValues(t, 4096, row.SizeBytes)
}

func TestWarmupStatusDoesNotOverwriteKnownSizeWithPlaceholder(t *testing.T) {
	svc, _, _ := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	svc.SetWarmupSettings(true)
	createTestWarmupGroup(t, svc, "gpu", "10Gi")
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen-placeholder", HubModelId: "qwen/placeholder", Version: "v1", WeightPath: "qwen/placeholder",
	})
	require.NoError(t, err)
	resp, err := svc.WarmModel(adminCtx(), &modelv1.WarmModelRequest{ModelId: reg.GetModelId()})
	require.NoError(t, err)
	db, err := svc.gormDB()
	require.NoError(t, err)
	require.NoError(t, db.Model(&ModelCacheEntry{}).Where("id = ?", resp.GetCache().GetCacheId()).
		Update("size_bytes", 4096).Error)

	body, _ := json.Marshal(warmup.Status{
		ID: resp.GetCache().GetCacheId(), State: warmup.StateWarming, SizeBytes: 1,
	})
	require.NoError(t, svc.HandleModelWarmupStatus(adminCtx(), body))
	var row ModelCacheEntry
	require.NoError(t, db.Where("id = ?", resp.GetCache().GetCacheId()).First(&row).Error)
	assert.EqualValues(t, 4096, row.SizeBytes)
}

func TestWarmModelIsIdempotentWhileCacheLive(t *testing.T) {
	svc, _, _ := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	svc.SetWarmupSettings(true)
	createTestWarmupGroup(t, svc, "gpu", "10Gi")
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

func TestWarmModelRequiresManagedNodeGroup(t *testing.T) {
	svc, _, _ := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	svc.SetWarmupSettings(true)
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen-nogroup", HubModelId: "qwen/hub", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)
	_, err = svc.WarmModel(adminCtx(), &modelv1.WarmModelRequest{ModelId: reg.GetModelId()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no warmup node group is configured")

	createTestWarmupGroup(t, svc, "gpu", "10Gi")
	_, err = svc.WarmModel(adminCtx(), &modelv1.WarmModelRequest{
		ModelId: reg.GetModelId(), NodeGroups: []string{"missing"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown warmup node group")
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

	var stored ModelTransfer
	require.NoError(t, db.Where("id = ?", row.ID).First(&stored).Error)
	assert.Equal(t, got.ModelID, stored.ModelID)
}

func createTestWarmupGroup(t *testing.T, svc *Service, name, limit string) {
	t.Helper()
	db, err := svc.gormDB()
	require.NoError(t, err)
	require.NoError(t, db.Create(&WarmupNodeGroup{
		Name:             name,
		NodeSelectorJSON: `{"kubernetes.io/hostname":"` + name + `"}`,
		StorageLimit:     limit,
		HostPath:         "/models",
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}).Error)
}

func TestWarmupNodeGroupCRUDAndNodeSnapshot(t *testing.T) {
	svc := newModelTestService(t)
	created, err := svc.CreateWarmupNodeGroup(adminCtx(), &modelv1.CreateWarmupNodeGroupRequest{
		Name:         "gpu",
		NodeSelector: map[string]string{"gpu": "true"},
		StorageLimit: "50Gi",
		HostPath:     "/models",
		StorageClass: "local-path",
	})
	require.NoError(t, err)
	assert.Equal(t, "gpu", created.GetNodeGroup().GetName())

	updated, err := svc.UpdateWarmupNodeGroup(adminCtx(), &modelv1.UpdateWarmupNodeGroupRequest{
		Name:         "gpu",
		NodeSelector: map[string]string{"gpu": "true", "pool": "fast"},
		StorageLimit: "80Gi",
		HostPath:     "/var/lib/taas/cache",
	})
	require.NoError(t, err)
	assert.Equal(t, "80Gi", updated.GetNodeGroup().GetStorageLimit())
	assert.Equal(t, "/var/lib/taas/cache", updated.GetNodeGroup().GetHostPath())

	require.NoError(t, svc.HandleWarmupNodeSnapshot(context.Background(), []byte(`{
		"nodes": [{
			"name": "node-a",
			"nodeGroups": ["gpu"],
			"models": [{"cacheName": "taas-model-cache-1", "nodeGroup": "gpu", "state": "ModelDownloaded"}],
			"modelCount": 1,
			"downloadedCount": 1,
			"observedAt": "2026-01-02T03:04:05Z"
		}]
	}`)))
	nodes, err := svc.ListWarmupNodes(adminCtx(), &modelv1.ListWarmupNodesRequest{})
	require.NoError(t, err)
	require.Len(t, nodes.GetNodes(), 1)
	assert.Equal(t, "node-a", nodes.GetNodes()[0].GetName())
	assert.Equal(t, []string{"gpu"}, nodes.GetNodes()[0].GetNodeGroups())
	assert.Equal(t, int32(1), nodes.GetNodes()[0].GetDownloadedCount())

	groups, err := svc.ListWarmupNodeGroups(adminCtx(), &modelv1.ListWarmupNodeGroupsRequest{})
	require.NoError(t, err)
	require.Len(t, groups.GetNodeGroups(), 1)
	assert.Equal(t, int32(1), groups.GetNodeGroups()[0].GetTotalNodes())
	assert.Equal(t, int32(1), groups.GetNodeGroups()[0].GetReadyNodes())

	// A full snapshot replaces the projection, so a deleted node disappears.
	require.NoError(t, svc.HandleWarmupNodeSnapshot(context.Background(), []byte(`{"nodes":[]}`)))
	nodes, err = svc.ListWarmupNodes(adminCtx(), &modelv1.ListWarmupNodesRequest{})
	require.NoError(t, err)
	assert.Empty(t, nodes.GetNodes())

	// The periodic sync publishes the full desired set, including groups a
	// controller that restarted may have missed.
	publisher := &recordingPublisher{}
	svc.SetTransferPublisher(publisher)
	require.NoError(t, svc.SyncWarmupNodeGroups(context.Background()))
	var snapshot warmup.NodeGroupEvent
	require.NoError(t, json.Unmarshal(publisher.body, &snapshot))
	assert.True(t, snapshot.Snapshot)
	require.Len(t, snapshot.NodeGroups, 1)
	assert.Equal(t, "gpu", snapshot.NodeGroups[0].Name)
	assert.Equal(t, "/var/lib/taas/cache", snapshot.NodeGroups[0].HostPath)

	_, err = svc.DeleteWarmupNodeGroup(adminCtx(), &modelv1.DeleteWarmupNodeGroupRequest{Name: "gpu"})
	require.NoError(t, err)
}

// TestStartDownloadTransferUsesSelectedBackend verifies a download queued for
// a named backend dispatches the transfer Job at that backend, not the
// platform default.
func TestStartDownloadTransferUsesSelectedBackend(t *testing.T) {
	svc, _, target := storageTestService(t)
	pub := &recordingPublisher{}
	svc.SetTransferPublisher(pub)
	svc.SetTransferSettings(TransferSettings{Mode: "job", Namespace: "taas-jobs", Image: "taas-transfer:test"})
	require.NoError(t, svc.Migrate(context.Background()))

	resp, err := svc.StartModelDownload(adminCtx(), &modelv1.StartModelDownloadRequest{
		Source: "modelscope", HubModelId: "qwen/hub", StorageBackend: target.Name,
	})
	require.NoError(t, err)
	assert.Equal(t, target.Name, resp.GetDownload().GetStorageBackend())

	var spec transfer.Spec
	require.NoError(t, json.Unmarshal(pub.body, &spec))
	assert.Equal(t, target.Name, spec.TargetBackend)
	assert.Equal(t, filepath.Join(target.Filesystem.MountPath, "qwen__hub/"), spec.TargetPath)

	// A download transfer exists before the model does. PostgreSQL rejects an
	// empty string in a uuid column, so the unassigned row must still carry a
	// valid uuid until the controller reports success and the catalogue row
	// exists.
	db, err := svc.gormDB()
	require.NoError(t, err)
	var row ModelTransfer
	require.NoError(t, db.Where("download_id = ?", resp.GetDownload().GetDownloadId()).First(&row).Error)
	_, err = uuid.Parse(row.ModelID)
	require.NoError(t, err)
}

// TestStartModelDownloadObjectStoreDispatchFailure avoids a silent fallback:
// when the Job cannot be queued for an object-store backend, the job fails
// instead of writing the weights into the local default directory.
func TestStartModelDownloadObjectStoreDispatchFailure(t *testing.T) {
	svc := newModelTestService(t)
	svc.SetStorageConfig(storage.Config{
		DefaultBackend: "shared",
		Backends: map[string]storage.Backend{
			"shared": {Name: "shared", Type: storage.TypeFilesystem},
			"minio": {
				Name: "minio", Type: storage.TypeObjectStore,
				ObjectStore: storage.ObjectStore{Endpoint: "https://minio.example.com", Bucket: "models"},
			},
		},
	})
	svc.SetTransferPublisher(&recordingPublisher{err: errPublish})
	svc.SetTransferSettings(TransferSettings{Mode: "job", Image: "taas-transfer:test"})
	require.NoError(t, svc.Migrate(context.Background()))

	_, err := svc.StartModelDownload(adminCtx(), &modelv1.StartModelDownloadRequest{
		Source: "modelscope", HubModelId: "qwen/hub", StorageBackend: "minio",
	})
	require.Error(t, err)
	list, err := svc.ListModelDownloads(adminCtx(), &modelv1.ListModelDownloadsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetDownloads(), 1)
	assert.Equal(t, DownloadStateFailed, list.GetDownloads()[0].GetState())
}

func TestRemoteRefPrefersBackendScopedCredentialEnv(t *testing.T) {
	svc, _, _ := storageTestService(t)
	backend := storage.Backend{
		Name: "archive",
		Type: storage.TypeObjectStore,
		ObjectStore: storage.ObjectStore{
			Endpoint: "https://s3.example.com",
			Bucket:   "models",
		},
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "legacy")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "legacy-secret")
	t.Setenv("TAAS_STORAGE_ARCHIVE_ACCESS_KEY", "backend")
	t.Setenv("TAAS_STORAGE_ARCHIVE_SECRET_KEY", "backend-secret")

	ref, err := svc.remoteRef(backend)
	require.NoError(t, err)
	assert.Equal(t, "TAAS_STORAGE_ARCHIVE_ACCESS_KEY", ref.AccessKeyEnv)
	assert.Equal(t, "TAAS_STORAGE_ARCHIVE_SECRET_KEY", ref.SecretKeyEnv)
}

func TestRemoteRefFallsBackToAWSCredentialEnv(t *testing.T) {
	svc, _, _ := storageTestService(t)
	backend := storage.Backend{
		Name: "archive",
		Type: storage.TypeObjectStore,
		ObjectStore: storage.ObjectStore{
			Endpoint: "https://s3.example.com",
			Bucket:   "models",
		},
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "legacy")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "legacy-secret")
	// t.Setenv cannot unset, so register the name and remove it after.
	t.Setenv("TAAS_STORAGE_ARCHIVE_ACCESS_KEY", "")
	t.Setenv("TAAS_STORAGE_ARCHIVE_SECRET_KEY", "")
	require.NoError(t, os.Unsetenv("TAAS_STORAGE_ARCHIVE_ACCESS_KEY"))
	require.NoError(t, os.Unsetenv("TAAS_STORAGE_ARCHIVE_SECRET_KEY"))

	ref, err := svc.remoteRef(backend)
	require.NoError(t, err)
	assert.Equal(t, "AWS_ACCESS_KEY_ID", ref.AccessKeyEnv)
	assert.Equal(t, "AWS_SECRET_ACCESS_KEY", ref.SecretKeyEnv)
}

func TestStorageBackendNameFallsBackToDefault(t *testing.T) {
	service := &Service{storageConfig: storage.Config{DefaultBackend: "shared"}}
	assert.Equal(t, "shared", service.storageBackendName(""))
	assert.Equal(t, "shared", service.storageBackendName("  "))
	assert.Equal(t, "minio", service.storageBackendName("minio"))
}

// TestHandleModelTransferStatusTracksDownloadProgress verifies a running
// transfer Job publishes its progress to the linked model_downloads row, so
// the console leaves the queued state and shows a percentage while the bytes
// are still moving.
func TestHandleModelTransferStatusTracksDownloadProgress(t *testing.T) {
	svc := newDownloadTestService(t)
	root := t.TempDir()
	svc.SetStorageConfig(storage.Config{
		DefaultBackend: "shared",
		Backends: map[string]storage.Backend{
			"shared": {
				Name:       "shared",
				Type:       storage.TypeFilesystem,
				Filesystem: storage.Filesystem{ExistingClaim: "shared-claim", MountPath: root},
			},
		},
	})
	svc.SetTransferSettings(TransferSettings{Mode: "job", Image: "taas-transfer:test"})
	svc.SetTransferPublisher(&recordingPublisher{})
	ctx := adminCtx()

	resp, err := svc.StartModelDownload(ctx, &modelv1.StartModelDownloadRequest{
		Source: "modelscope", HubModelId: "Qwen/Qwen2.5-0.5B-Instruct",
	})
	require.NoError(t, err)
	assert.Equal(t, DownloadStateQueued, resp.GetDownload().GetState())

	transfers, err := svc.ListModelTransfers(ctx, &modelv1.ListModelTransfersRequest{})
	require.NoError(t, err)
	require.Len(t, transfers.GetTransfers(), 1)
	transferID := transfers.GetTransfers()[0].GetTransferId()

	// The Job starts moving bytes: the download row follows before the
	// transfer reaches a terminal state.
	running, _ := json.Marshal(transfer.Status{
		ID: transferID, State: transfer.StateRunning,
		Bytes: 120, TotalBytes: 480, JobName: "taas-transfer-live",
	})
	require.NoError(t, svc.HandleModelTransferStatus(ctx, running))
	downloads, err := svc.ListModelDownloads(ctx, &modelv1.ListModelDownloadsRequest{})
	require.NoError(t, err)
	require.Len(t, downloads.GetDownloads(), 1)
	live := downloads.GetDownloads()[0]
	assert.Equal(t, DownloadStateDownloading, live.GetState())
	assert.EqualValues(t, 120, live.GetBytes())
	assert.EqualValues(t, 480, live.GetTotalBytes())

	// The terminal report still registers the model and flips the row.
	done, _ := json.Marshal(transfer.Status{
		ID: transferID, State: transfer.StateSucceeded,
		Bytes: 480, TotalBytes: 480, JobName: "taas-transfer-live",
	})
	require.NoError(t, svc.HandleModelTransferStatus(ctx, done))
	downloads, err = svc.ListModelDownloads(ctx, &modelv1.ListModelDownloadsRequest{})
	require.NoError(t, err)
	finished := downloads.GetDownloads()[0]
	assert.Equal(t, DownloadStateSucceeded, finished.GetState())
	assert.EqualValues(t, 480, finished.GetBytes())
	assert.EqualValues(t, 480, finished.GetTotalBytes())
	assert.NotEmpty(t, finished.GetModelId())
}

func TestStartModelTransferRejectsGenericDownload(t *testing.T) {
	svc, _, _ := storageTestService(t)
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen", HubModelId: "qwen/hub", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)

	_, err = svc.StartModelTransfer(adminCtx(), &modelv1.StartModelTransferRequest{
		ModelId: reg.GetModelId(), Operation: transfer.OperationDownload,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "model downloads")
}

func TestListModelWarmupsOrdersReadyFirst(t *testing.T) {
	svc := newModelTestService(t)
	db, err := svc.gormDB()
	require.NoError(t, err)
	now := time.Now().UTC()
	rows := []*ModelCacheEntry{
		{ID: uuid.NewString(), ModelID: uuid.NewString(), ModelName: "queued-newest", Version: "v1", State: warmup.StateQueued, CreatedAt: now, UpdatedAt: now},
		{ID: uuid.NewString(), ModelID: uuid.NewString(), ModelName: "ready-oldest", Version: "v1", State: warmup.StateReady, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now},
		{ID: uuid.NewString(), ModelID: uuid.NewString(), ModelName: "warming-middle", Version: "v1", State: warmup.StateWarming, CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now},
		{ID: uuid.NewString(), ModelID: uuid.NewString(), ModelName: "ready-newer", Version: "v1", State: warmup.StateReady, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now},
		{ID: uuid.NewString(), ModelID: uuid.NewString(), ModelName: "failed-old", Version: "v1", State: warmup.StateFailed, CreatedAt: now.Add(-4 * time.Hour), UpdatedAt: now},
	}
	for _, row := range rows {
		require.NoError(t, db.Create(row).Error)
	}

	list, err := svc.ListModelWarmups(adminCtx(), &modelv1.ListModelWarmupsRequest{})
	require.NoError(t, err)
	names := make([]string, 0, len(list.GetWarmups()))
	for _, entry := range list.GetWarmups() {
		names = append(names, entry.GetModelName())
	}
	assert.Equal(t, []string{
		"ready-newer", "ready-oldest", "queued-newest", "warming-middle", "failed-old",
	}, names)

	page, err := svc.ListModelWarmups(adminCtx(), &modelv1.ListModelWarmupsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 2},
	})
	require.NoError(t, err)
	require.Len(t, page.GetWarmups(), 2)
	assert.Equal(t, "ready-newer", page.GetWarmups()[0].GetModelName())
	assert.Equal(t, "ready-oldest", page.GetWarmups()[1].GetModelName())
}

// TestStartModelTransferQueuesMove locks the move semantics of a console
// transfer: the source is the backend holding the version, the target is the
// only caller-selectable side, and a successful move records the new backend.
func TestStartModelTransferQueuesMove(t *testing.T) {
	svc, source, target := storageTestService(t)
	pub := &recordingPublisher{}
	svc.SetTransferPublisher(pub)
	svc.SetTransferSettings(TransferSettings{Mode: "job", Namespace: "taas-jobs", Image: "taas-transfer:test"})
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen-move", HubModelId: "qwen/move", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)
	// The version records source as its home; a move must use it as the
	// source even when the client leaves source_backend empty.
	require.NoError(t, svc.repo.SetVersionStorageBackend(adminCtx(), reg.GetModelId(), "v1", source.Name))

	resp, err := svc.StartModelTransfer(adminCtx(), &modelv1.StartModelTransferRequest{
		ModelId: reg.GetModelId(), Operation: transfer.OperationMove, TargetBackend: target.Name,
	})
	require.NoError(t, err)
	assert.Equal(t, transfer.OperationMove, resp.GetTransfer().GetOperation())
	assert.Equal(t, source.Name, resp.GetTransfer().GetSourceBackend())
	assert.Equal(t, target.Name, resp.GetTransfer().GetTargetBackend())

	var spec transfer.Spec
	require.NoError(t, json.Unmarshal(pub.body, &spec))
	assert.Equal(t, transfer.OperationMove, spec.Operation)
	assert.Equal(t, source.Name, spec.SourceBackend)
	assert.Equal(t, target.Name, spec.TargetBackend)
	assert.True(t, spec.DeleteSource)
	assert.Equal(t, filepath.Join(source.Filesystem.MountPath, "qwen/v1"), spec.SourcePath)
	assert.Equal(t, filepath.Join(target.Filesystem.MountPath, "qwen/v1"), spec.TargetPath)

	body, _ := json.Marshal(transfer.Status{ID: resp.GetTransfer().GetTransferId(), State: transfer.StateSucceeded})
	require.NoError(t, svc.HandleModelTransferStatus(adminCtx(), body))
	version, err := svc.repo.FindVersion(adminCtx(), reg.GetModelId(), "v1")
	require.NoError(t, err)
	assert.Equal(t, target.Name, version.StorageBackend)
}

func TestStartModelTransferMovesOnlyToAnotherBackend(t *testing.T) {
	svc, source, target := storageTestService(t)
	svc.SetTransferPublisher(&recordingPublisher{})
	svc.SetTransferSettings(TransferSettings{Mode: "job", Image: "taas-transfer:test"})
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen-fixed", HubModelId: "qwen/fixed", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)
	require.NoError(t, svc.repo.SetVersionStorageBackend(adminCtx(), reg.GetModelId(), "v1", source.Name))

	// The client cannot move from a backend other than the model's.
	_, err = svc.StartModelTransfer(adminCtx(), &modelv1.StartModelTransferRequest{
		ModelId: reg.GetModelId(), Operation: transfer.OperationMove,
		SourceBackend: target.Name, TargetBackend: target.Name,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source backend is fixed")

	// Moving onto the same backend cannot delete the target it just wrote.
	_, err = svc.StartModelTransfer(adminCtx(), &modelv1.StartModelTransferRequest{
		ModelId: reg.GetModelId(), Operation: transfer.OperationMove, TargetBackend: source.Name,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target backend must differ")

	// A move always needs a destination.
	_, err = svc.StartModelTransfer(adminCtx(), &modelv1.StartModelTransferRequest{
		ModelId: reg.GetModelId(), Operation: transfer.OperationMove,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target_backend is required for a move")
}

func TestInlineMoveTransferDeletesSourceAndRetargetsVersion(t *testing.T) {
	svc, source, target := storageTestService(t)
	reg, err := svc.RegisterModel(adminCtx(), &modelv1.RegisterModelRequest{
		Name: "qwen-inline-move", HubModelId: "qwen/inline-move", Version: "v1", WeightPath: "qwen/v1",
	})
	require.NoError(t, err)
	require.NoError(t, svc.repo.SetVersionStorageBackend(adminCtx(), reg.GetModelId(), "v1", source.Name))

	sourceDir := filepath.Join(source.Filesystem.MountPath, "qwen/v1")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "model.bin"), []byte("weights"), 0o644))
	row := &ModelTransfer{
		ID: uuid.NewString(), Operation: transfer.OperationMove, ModelID: reg.GetModelId(),
		ModelName: "qwen-inline-move", Version: "v1", SourceBackend: source.Name,
		TargetBackend: target.Name, WeightPath: "qwen/v1", State: transfer.StateQueued,
	}
	db, err := svc.gormDB()
	require.NoError(t, err)
	require.NoError(t, db.Create(row).Error)

	svc.runInlineTransfer(row, source, target)

	var got ModelTransfer
	require.NoError(t, db.Where("id = ?", row.ID).First(&got).Error)
	assert.Equal(t, transfer.StateSucceeded, got.State)
	assert.NoFileExists(t, sourceDir)
	content, err := os.ReadFile(filepath.Join(target.Filesystem.MountPath, "qwen/v1", "model.bin"))
	require.NoError(t, err)
	assert.Equal(t, "weights", string(content))
	version, err := svc.repo.FindVersion(adminCtx(), reg.GetModelId(), "v1")
	require.NoError(t, err)
	assert.Equal(t, target.Name, version.StorageBackend)
}
