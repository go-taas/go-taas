package model

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/modelhub"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/transfer"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	"github.com/go-taas/go-taas/services/audit"
)

// TransferSettings is the subset of the transfer configuration the model
// service needs. It is copied at wiring time so the service stays free of a
// config-package dependency.
type TransferSettings struct {
	Mode      string
	Namespace string
	Image     string
}

// SetStorageConfig installs the normalized storage backend configuration.
// It must be called before serving.
func (s *Service) SetStorageConfig(cfg storage.Config) { s.storageConfig = cfg }

// SetTransferSettings installs the transfer execution settings.
func (s *Service) SetTransferSettings(settings TransferSettings) { s.transferSettings = settings }

// SetTransferPublisher overrides the message-queue publisher used to hand a
// transfer to the controller. When nil, the shared MQ component is used.
func (s *Service) SetTransferPublisher(p TransferPublisher) { s.transferPublisher = p }

// TransferPublisher publishes a transfer request. It is implemented by the
// shared MQ component and by test fakes.
type TransferPublisher interface {
	Publish(ctx context.Context, subject string, body []byte, headers map[string]string) error
}

// publishTransfer sends a payload to subject. It returns an error when no
// publisher is configured, letting the caller fall back to inline execution.
func (s *Service) publishTransfer(ctx context.Context, subject string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if s.transferPublisher != nil {
		return s.transferPublisher.Publish(ctx, subject, data, map[string]string{"content-type": "application/json"})
	}
	if s.components != nil && s.components.MQ() != nil {
		return s.components.MQ().Publish(ctx, subject, data, map[string]string{"content-type": "application/json"})
	}
	return fmt.Errorf("model: message queue is not configured")
}

// StartModelTransfer records a transfer and either hands it to the
// controller's transfer Job or, when Job execution is unavailable, runs it
// inline with a guarded fallback. The row is created before the hand-off so
// the console can always poll it.
func (s *Service) StartModelTransfer(ctx context.Context, req *modelv1.StartModelTransferRequest) (*modelv1.StartModelTransferResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	operation := strings.TrimSpace(req.GetOperation())
	if operation == "" {
		operation = transfer.OperationCopy
	}
	switch operation {
	case transfer.OperationCopy, transfer.OperationDownload, transfer.OperationDelete:
	default:
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: transfer operation must be copy, download or delete")
	}
	modelID := strings.TrimSpace(req.GetModelId())
	if modelID == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: model_id is required")
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	row, err := repo.GetModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	version, err := s.resolveTransferVersion(ctx, repo, row.ID, strings.TrimSpace(req.GetVersion()))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(version.WeightPath) == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: model version has no weight path")
	}
	cfg := s.storageConfig
	if !cfg.Enabled() {
		return nil, apierrors.Newf(apierrors.CodeInternal, "model: storage backends are not configured")
	}
	sourceBackendName := firstNonEmpty(strings.TrimSpace(req.GetSourceBackend()), version.StorageBackend, cfg.DefaultBackend)
	targetBackendName := firstNonEmpty(strings.TrimSpace(req.GetTargetBackend()), version.StorageBackend, cfg.DefaultBackend)
	if operation == transfer.OperationCopy && targetBackendName == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: target_backend is required for a copy")
	}
	sourceBackend, err := cfg.Resolve(sourceBackendName)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	targetBackend, err := cfg.Resolve(targetBackendName)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	sourceURI, err := sourceBackend.URI(version.WeightPath)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	targetURI := ""
	if operation != transfer.OperationDelete {
		targetURI, err = targetBackend.URI(version.WeightPath)
		if err != nil {
			return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
		}
	}
	now := time.Now().UTC()
	rowTransfer := &ModelTransfer{
		ID:            uuid.NewString(),
		Operation:     operation,
		ModelID:       row.ID,
		ModelName:     row.Name,
		Version:       version.Version,
		SourceBackend: sourceBackend.Name,
		TargetBackend: targetBackend.Name,
		SourceURI:     sourceURI,
		TargetURI:     targetURI,
		WeightPath:    version.WeightPath,
		State:         transfer.StateQueued,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).Create(rowTransfer).Error; err != nil {
		return nil, err
	}
	spec := &transfer.Spec{
		ID:            rowTransfer.ID,
		Operation:     operation,
		ModelID:       row.ID,
		ModelName:     row.Name,
		Version:       version.Version,
		SourceBackend: sourceBackend.Name,
		TargetBackend: targetBackend.Name,
		SourceURI:     sourceURI,
		TargetURI:     targetURI,
		WeightPath:    version.WeightPath,
	}
	if operation == transfer.OperationDelete {
		spec.TargetURI = ""
	}
	if sourceBackend.Type == storage.TypeFilesystem {
		spec.SourcePath, _ = storage.CleanJoin(sourceBackend.Filesystem.MountPath, version.WeightPath)
	} else {
		ref, refErr := s.remoteRef(sourceBackend)
		if refErr != nil {
			return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", refErr)
		}
		ref.AccessKeyEnv = "TRANSFER_SOURCE_ACCESS_KEY"
		ref.SecretKeyEnv = "TRANSFER_SOURCE_SECRET_KEY"
		spec.SourceObjectStore = &ref
	}
	if targetBackend.Type == storage.TypeFilesystem {
		spec.TargetPath, _ = storage.CleanJoin(targetBackend.Filesystem.MountPath, version.WeightPath)
	} else if operation != transfer.OperationDelete {
		ref, refErr := s.remoteRef(targetBackend)
		if refErr != nil {
			return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", refErr)
		}
		ref.AccessKeyEnv = "TRANSFER_TARGET_ACCESS_KEY"
		ref.SecretKeyEnv = "TRANSFER_TARGET_SECRET_KEY"
		spec.TargetObjectStore = &ref
	}
	if s.transferSettings.Mode == "job" && strings.TrimSpace(s.transferSettings.Image) != "" {
		err := s.publishTransfer(ctx, mq.DefaultSubjects().ModelTransfers, spec)
		if err == nil {
			return &modelv1.StartModelTransferResponse{Response: okResponse(), Transfer: transferProjection(rowTransfer)}, nil
		}
		logger.S().Warnw("model: transfer job dispatch failed, falling back to inline transfer", "transfer", rowTransfer.ID, "err", err)
	}
	// The caller asked for the transfer, so it must outlive the request.
	//
	//nolint:gosec // G118: a detached job intentionally ignores the request context
	go s.runInlineTransfer(rowTransfer, sourceBackend, targetBackend)
	return &modelv1.StartModelTransferResponse{Response: okResponse(), Transfer: transferProjection(rowTransfer)}, nil
}

// startDownloadTransfer hands a hub download to the controller as a
// transfer Job. It returns false when the Job cannot be queued so the caller
// can fall back to the in-process downloader. The model row is registered
// when the Job's terminal status arrives.
func (s *Service) startDownloadTransfer(ctx context.Context, job *DownloadJob) bool {
	cfg := s.storageConfig
	backend, err := cfg.Resolve(job.StorageBackend)
	if err != nil {
		return false
	}
	weightPath := modelhub.CleanModelID(job.HubModelID) + "/"
	source := "hub:" + job.Source
	targetURI, err := backend.URI(weightPath)
	if err != nil {
		return false
	}
	spec := &transfer.Spec{
		ID:            uuid.NewString(),
		Operation:     transfer.OperationDownload,
		ModelName:     job.Name,
		Version:       job.Version,
		SourceBackend: source,
		TargetBackend: backend.Name,
		SourceURI:     source,
		TargetURI:     targetURI,
		HubSource:     job.Source,
		HubModelID:    job.HubModelID,
		WeightPath:    weightPath,
	}
	if backend.Type == storage.TypeFilesystem {
		spec.TargetPath, err = storage.CleanJoin(backend.Filesystem.MountPath, weightPath)
		if err != nil {
			return false
		}
	} else {
		ref, refErr := s.remoteRef(backend)
		if refErr != nil {
			return false
		}
		ref.AccessKeyEnv = "TRANSFER_TARGET_ACCESS_KEY"
		ref.SecretKeyEnv = "TRANSFER_TARGET_SECRET_KEY"
		spec.TargetObjectStore = &ref
	}
	now := time.Now().UTC()
	row := &ModelTransfer{
		ID:            spec.ID,
		Operation:     transfer.OperationDownload,
		ModelName:     job.Name,
		Version:       job.Version,
		SourceBackend: source,
		TargetBackend: backend.Name,
		SourceURI:     source,
		TargetURI:     targetURI,
		WeightPath:    weightPath,
		State:         transfer.StateQueued,
		DownloadID:    &job.ID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	db, err := s.gormDB()
	if err != nil {
		return false
	}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return false
	}
	if err := s.publishTransfer(ctx, mq.DefaultSubjects().ModelTransfers, spec); err != nil {
		logger.S().Warnw("model: download transfer dispatch failed, falling back inline", "download", job.ID, "err", err)
		_ = db.WithContext(ctx).Delete(row).Error
		return false
	}
	return true
}

// completeDownloadTransfer finishes the model_downloads row linked to a
// terminal transfer. A successful Job has already written the weights, so
// only the catalog registration remains.
func (s *Service) completeDownloadTransfer(ctx context.Context, row *ModelTransfer, status transfer.Status) {
	db, err := s.gormDB()
	if err != nil {
		return
	}
	var job DownloadJob
	if row.DownloadID == nil {
		return
	}
	if err := db.WithContext(ctx).Where("id = ?", *row.DownloadID).First(&job).Error; err != nil {
		return
	}
	if status.State != transfer.StateSucceeded {
		message := status.Message
		if message == "" {
			message = "download " + status.State
		}
		s.updateDownload(ctx, job.ID, map[string]any{
			"state":      DownloadStateFailed,
			"message":    truncateMessage(message),
			"bytes":      status.Bytes,
			"updated_at": time.Now().UTC(),
		})
		return
	}
	repo, err := s.repository()
	if err != nil {
		s.updateDownload(ctx, job.ID, map[string]any{
			"state": DownloadStateFailed, "message": truncateMessage(err.Error()),
			"bytes": status.Bytes, "updated_at": time.Now().UTC(),
		})
		return
	}
	modelID, err := repo.RegisterModelOrCreateVersion(ctx, job.Name, job.Description, job.HubModelID, job.Version, row.WeightPath)
	if err != nil {
		s.updateDownload(ctx, job.ID, map[string]any{
			"state": DownloadStateFailed, "message": truncateMessage(err.Error()),
			"bytes": status.Bytes, "updated_at": time.Now().UTC(),
		})
		return
	}
	if err := repo.SetVersionStorageBackend(ctx, modelID, job.Version, row.TargetBackend); err != nil {
		logger.S().Warnw("model: record download storage backend failed", "model", modelID, "version", job.Version, "err", err)
	}
	s.updateDownload(ctx, job.ID, map[string]any{
		"state":       DownloadStateSucceeded,
		"message":     "",
		"bytes":       status.Bytes,
		"weight_path": row.WeightPath,
		"model_id":    modelID,
		"updated_at":  time.Now().UTC(),
	})
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  s.grantedBy(ctx),
		ActorType:    "user",
		Action:       "model.download",
		ResourceType: "model",
		ResourceID:   modelID,
		Result:       "success",
	})
}

// resolveTransferVersion resolves the requested version, the active version,
// or the latest version when none is named.
func (s *Service) resolveTransferVersion(ctx context.Context, repo *Repository, modelID, version string) (*Version, error) {
	if version != "" {
		return repo.FindVersion(ctx, modelID, version)
	}
	if active, err := repo.ActiveVersion(ctx, modelID); err == nil && active != nil {
		return active, nil
	}
	return repo.LatestVersion(ctx, modelID)
}

// ListModelTransfers returns one page of transfer jobs, newest first.
func (s *Service) ListModelTransfers(ctx context.Context, req *modelv1.ListModelTransfersRequest) (*modelv1.ListModelTransfersResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	query := db.WithContext(ctx).Model(&ModelTransfer{})
	if state := strings.TrimSpace(req.GetState()); state != "" {
		query = query.Where("state = ?", state)
	}
	if modelID := strings.TrimSpace(req.GetModelId()); modelID != "" {
		query = query.Where("model_id = ?", modelID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var rows []*ModelTransfer
	if err := query.Order("created_at DESC").Order("id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	transfers := make([]*modelv1.ModelTransfer, 0, len(rows))
	for _, row := range rows {
		transfers = append(transfers, transferProjection(row))
	}
	return &modelv1.ListModelTransfersResponse{
		Response:  okResponse(),
		Transfers: transfers,
		PageMeta:  &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// GetModelTransfer returns one transfer row.
func (s *Service) GetModelTransfer(ctx context.Context, req *modelv1.GetModelTransferRequest) (*modelv1.GetModelTransferResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	row, err := s.getTransfer(ctx, req.GetTransferId())
	if err != nil {
		return nil, err
	}
	return &modelv1.GetModelTransferResponse{Response: okResponse(), Transfer: transferProjection(row)}, nil
}

// CancelModelTransfer marks a queued/running transfer cancelled and tells
// the controller to delete its Job. The bytes already moved stay in place.
func (s *Service) CancelModelTransfer(ctx context.Context, req *modelv1.CancelModelTransferRequest) (*modelv1.CancelModelTransferResponse, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	row, err := s.getTransfer(ctx, req.GetTransferId())
	if err != nil {
		return nil, err
	}
	if transfer.Terminal(row.State) {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: transfer is already %s", row.State)
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := db.WithContext(ctx).Model(&ModelTransfer{}).Where("id = ?", row.ID).Updates(map[string]any{
		"state":      transfer.StateCancelled,
		"message":    "cancelled by operator",
		"updated_at": now,
	}).Error; err != nil {
		return nil, err
	}
	_ = s.publishTransfer(ctx, mq.DefaultSubjects().ModelTransfers, &transfer.Spec{ID: row.ID, Operation: transfer.OperationCancel})
	row.State = transfer.StateCancelled
	row.Message = "cancelled by operator"
	row.UpdatedAt = now
	return &modelv1.CancelModelTransferResponse{Response: okResponse(), Transfer: transferProjection(row)}, nil
}

// HandleModelTransferStatus applies a controller status report. It is called
// by the model status consumer runner.
func (s *Service) HandleModelTransferStatus(ctx context.Context, body []byte) error {
	var status transfer.Status
	if err := json.Unmarshal(body, &status); err != nil {
		return err
	}
	if strings.TrimSpace(status.ID) == "" {
		return fmt.Errorf("model: transfer status has no id")
	}
	db, err := s.gormDB()
	if err != nil {
		return err
	}
	row, err := s.getTransfer(ctx, status.ID)
	if err != nil {
		return err
	}
	updates := map[string]any{
		"bytes":       status.Bytes,
		"total_bytes": status.TotalBytes,
		"updated_at":  time.Now().UTC(),
	}
	if status.State != "" {
		updates["state"] = status.State
	}
	if status.Message != "" || transfer.Terminal(status.State) {
		updates["message"] = status.Message
	}
	if status.JobName != "" {
		updates["job_name"] = status.JobName
	}
	result := db.WithContext(ctx).Model(&ModelTransfer{}).
		Where("id = ? AND state NOT IN ?", status.ID, []string{transfer.StateSucceeded, transfer.StateFailed, transfer.StateCancelled}).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		// A late status for a cancelled/terminal row is expected; the row
		// was already loaded above, so there is nothing left to report.
		return nil
	}
	if row.Operation == transfer.OperationDownload && row.DownloadID != nil && transfer.Terminal(status.State) {
		s.completeDownloadTransfer(ctx, row, status)
	}
	// A successful copy makes the target the version's home: serving must
	// read the copy from the backend that now holds it instead of the
	// source the version was uploaded to.
	if row.Operation == transfer.OperationCopy && status.State == transfer.StateSucceeded &&
		row.TargetBackend != "" && s.repo != nil {
		if err := s.repo.SetVersionStorageBackend(ctx, row.ModelID, row.Version, row.TargetBackend); err != nil {
			logger.S().Warnw("model: record transfer target backend failed",
				"transfer", row.ID, "model", row.ModelID, "version", row.Version, "err", err)
		}
	}
	return nil
}

// getTransfer loads one transfer row.
func (s *Service) getTransfer(ctx context.Context, id string) (*ModelTransfer, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: transfer_id is required")
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var row ModelTransfer
	if err := db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelNotFound, "model: transfer %s not found", id)
	}
	return &row, nil
}

// runInlineTransfer executes a transfer in this process. It exists for
// installations without a controller and for upgrade safety; the production
// path is a transfer Job.
func (s *Service) runInlineTransfer(row *ModelTransfer, source, target storage.Backend) {
	ctx := context.Background()
	s.updateTransfer(ctx, row.ID, transfer.StateRunning, "", 0, 0)
	written, err := s.executeTransfer(ctx, row, source, target, func(n int64) {
		s.updateTransfer(ctx, row.ID, transfer.StateRunning, "", n, 0)
	})
	if err != nil {
		s.updateTransfer(ctx, row.ID, transfer.StateFailed, err.Error(), written, 0)
		return
	}
	s.updateTransfer(ctx, row.ID, transfer.StateSucceeded, "", written, written)
}

// executeTransfer moves the bytes for the row and returns the byte count.
func (s *Service) executeTransfer(ctx context.Context, row *ModelTransfer, source, target storage.Backend, progress func(int64)) (int64, error) {
	sourcePath, err := s.localPath(source, row.WeightPath)
	if err != nil {
		return 0, err
	}
	targetPath, err := s.localPath(target, row.WeightPath)
	if err != nil {
		return 0, err
	}
	if row.Operation == transfer.OperationDelete {
		if source.Type == storage.TypeObjectStore {
			ref, err := s.remoteRef(source)
			if err != nil {
				return 0, err
			}
			client, err := storage.NewRemoteClient(ref)
			if err != nil {
				return 0, err
			}
			return client.DeletePrefix(ctx, row.WeightPath)
		}
		if err := os.RemoveAll(sourcePath); err != nil {
			return 0, err
		}
		return 0, nil
	}
	switch {
	case source.Type == storage.TypeFilesystem && target.Type == storage.TypeFilesystem:
		return storage.CopyDir(ctx, sourcePath, targetPath)
	case source.Type == storage.TypeFilesystem && target.Type == storage.TypeObjectStore:
		ref, err := s.remoteRef(target)
		if err != nil {
			return 0, err
		}
		client, err := storage.NewRemoteClient(ref)
		if err != nil {
			return 0, err
		}
		written, err := client.UploadDir(ctx, sourcePath, row.WeightPath)
		if err == nil && row.Operation == transfer.OperationCopy && row.SourceURI != row.TargetURI {
			// A copy to another backend intentionally leaves the source in
			// place; deleteSource is honored only when the caller asked for
			// a move, which the console maps to delete after copy.
			_ = progress
		}
		return written, err
	case source.Type == storage.TypeObjectStore && target.Type == storage.TypeFilesystem:
		ref, err := s.remoteRef(source)
		if err != nil {
			return 0, err
		}
		client, err := storage.NewRemoteClient(ref)
		if err != nil {
			return 0, err
		}
		return client.DownloadPrefix(ctx, row.WeightPath, targetPath, progress)
	case source.Type == storage.TypeObjectStore && target.Type == storage.TypeObjectStore:
		sourceRef, err := s.remoteRef(source)
		if err != nil {
			return 0, err
		}
		targetRef, err := s.remoteRef(target)
		if err != nil {
			return 0, err
		}
		sourceClient, err := storage.NewRemoteClient(sourceRef)
		if err != nil {
			return 0, err
		}
		targetClient, err := storage.NewRemoteClient(targetRef)
		if err != nil {
			return 0, err
		}
		if sourceRef.Endpoint == targetRef.Endpoint && sourceRef.Bucket == targetRef.Bucket && sourceRef.Prefix == targetRef.Prefix {
			return sourceClient.CopyPrefix(ctx, row.WeightPath, row.WeightPath)
		}
		// No server-side copy exists across buckets or endpoints. Stage the
		// objects in a temporary directory so the bytes stream through this
		// process at most once per object.
		temp, err := os.MkdirTemp("", "taas-transfer-")
		if err != nil {
			return 0, err
		}
		defer func() { _ = os.RemoveAll(temp) }()
		if _, err := sourceClient.DownloadPrefix(ctx, row.WeightPath, temp, progress); err != nil {
			return 0, err
		}
		return targetClient.UploadDir(ctx, temp, row.WeightPath)
	default:
		return 0, fmt.Errorf("model: unsupported transfer from %s to %s", source.Type, target.Type)
	}
}

// localPath maps a backend key to a path this process can read. A
// filesystem backend uses the configured weights directory as its root, so
// the legacy single-mount deployment keeps working; other filesystem
// backends use their declared mount path.
func (s *Service) localPath(backend storage.Backend, key string) (string, error) {
	if backend.Type != storage.TypeFilesystem {
		// Return the key so the caller can ignore the path for an object
		// store; executeTransfer branches on the type first.
		return key, nil
	}
	root := strings.TrimSpace(backend.Filesystem.MountPath)
	if root == "" {
		root = storage.DefaultMountPath
	}
	if s.weightsDir != "" && (backend.Name == "" || backend.Name == s.storageConfig.DefaultBackend) {
		root = s.weightsDir
	}
	return storage.CleanJoin(root, key)
}

// remoteRef resolves an object-store backend and its credential env names.
// The transfer Job receives the same values through its environment.
func (s *Service) remoteRef(backend storage.Backend) (storage.RemoteRef, error) {
	store := backend.ObjectStore
	if strings.TrimSpace(store.Endpoint) == "" || strings.TrimSpace(store.Bucket) == "" {
		return storage.RemoteRef{}, fmt.Errorf("model: object-store backend %q is incomplete", backend.Name)
	}
	accessEnv, secretEnv := storage.CredentialEnvNames(backend.Name)
	// The chart provisions one backend-scoped pair for every object store, so
	// two backends cannot shadow each other. A process configured the old way
	// keeps working through the AWS names.
	if _, ok := os.LookupEnv(accessEnv); !ok {
		if _, legacy := os.LookupEnv("AWS_ACCESS_KEY_ID"); legacy {
			accessEnv, secretEnv = "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY" //nolint:gosec // G101: env names, not credentials
		}
	}
	return storage.RemoteRef{ //nolint:gosec // G101: these are env var names, not credentials
		Provider:           store.Provider,
		Endpoint:           store.Endpoint,
		Region:             store.Region,
		Bucket:             store.Bucket,
		Prefix:             store.Prefix,
		ForcePathStyle:     store.ForcePathStyle,
		InsecureSkipVerify: store.TLS.InsecureSkipVerify,
		AccessKeyEnv:       accessEnv,
		SecretKeyEnv:       secretEnv,
	}, nil
}

// updateTransfer writes one progress/state update.
func (s *Service) updateTransfer(ctx context.Context, id, state, message string, bytes, total int64) {
	db, err := s.gormDB()
	if err != nil {
		logger.S().Warnw("model: transfer progress update skipped", "transfer", id, "err", err)
		return
	}
	updates := map[string]any{
		"state":      state,
		"message":    message,
		"bytes":      bytes,
		"updated_at": time.Now().UTC(),
	}
	if total > 0 {
		updates["total_bytes"] = total
	}
	if err := db.WithContext(ctx).Model(&ModelTransfer{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		logger.S().Warnw("model: transfer progress update failed", "transfer", id, "err", err)
	}
}

// transferProjection maps the row to its API message.
func transferProjection(row *ModelTransfer) *modelv1.ModelTransfer {
	return &modelv1.ModelTransfer{
		TransferId:    row.ID,
		Operation:     row.Operation,
		ModelId:       row.ModelID,
		ModelName:     row.ModelName,
		Version:       row.Version,
		SourceBackend: row.SourceBackend,
		TargetBackend: row.TargetBackend,
		SourceUri:     row.SourceURI,
		TargetUri:     row.TargetURI,
		WeightPath:    row.WeightPath,
		State:         row.State,
		Message:       row.Message,
		Bytes:         row.Bytes,
		TotalBytes:    row.TotalBytes,
		JobName:       row.JobName,
		CreatedAt:     row.CreatedAt.Unix(),
		UpdatedAt:     row.UpdatedAt.Unix(),
	}
}

// firstNonEmpty returns the first non-empty trimmed value.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
