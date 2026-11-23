package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/modelarchive"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/pkg/transfer"
	"github.com/go-taas/go-taas/services/audit"
	"gorm.io/gorm"
)

// uploadCreateRequest opens a chunked upload.
type uploadCreateRequest struct {
	Name           string
	HubModelID     string
	Description    string
	StorageBackend string
	ArchiveName    string
	TotalBytes     int64
}

// uploadPartRequest uploads one chunk.
type uploadPartRequest struct {
	UploadID   string
	PartNumber int
	Body       io.Reader
	Size       int64
}

// stagingRemote builds the RemoteClient for the object-store backend that
// stages upload chunks. It validates that the backend is an object store.
func (s *Service) stagingRemote(ctx context.Context) (*storage.RemoteClient, error) {
	_ = ctx
	name := s.uploadStagingBackend()
	if name == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: no upload staging object store is configured (model.upload.stagingBackend)")
	}
	backend, err := s.storageConfig.Resolve(name)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	if backend.Type != storage.TypeObjectStore {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: upload staging backend %q is not an object store", name)
	}
	ref, err := s.remoteRef(backend)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	return storage.NewRemoteClient(ref)
}

// uploadStagingBackend returns the configured staging object-store backend.
func (s *Service) uploadStagingBackend() string {
	cfg := s.uploadConfig()
	return strings.TrimSpace(cfg.StagingBackend)
}

// uploadPartSize returns the configured chunk size, bounded to the object
// store's minimum.
func (s *Service) uploadPartSize() int64 {
	n := s.uploadConfig().PartSizeBytes
	if n < storage.MinPartSize {
		n = storage.MinPartSize
	}
	return n
}

// uploadMaxArchiveBytes returns the configured archive ceiling.
func (s *Service) uploadMaxArchiveBytes() int64 {
	n := s.uploadConfig().MaxArchiveBytes
	if n <= 0 {
		n = int64(200) << 30
	}
	return n
}

// CreateUploadSession validates the request, opens a multipart upload in the
// staging object store, and records the resumable session.
func (s *Service) createUploadSessionRow(ctx context.Context, req uploadCreateRequest) (*ModelUpload, error) {
	if err := s.requireAdminRole(ctx, callerOrgID(ctx)); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: name is required")
	}
	if len(name) > 128 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: name must be 1-128 characters")
	}
	hubModelID := strings.TrimSpace(req.HubModelID)
	if hubModelID == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: hub model id is required")
	}
	if err := validateHubModelID(hubModelID); err != nil {
		return nil, err
	}
	if req.TotalBytes <= 0 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: total_bytes must be positive")
	}
	if max := s.uploadMaxArchiveBytes(); req.TotalBytes > max {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: archive exceeds the %d byte limit", max)
	}
	format, err := modelarchive.Format("", req.ArchiveName)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}

	// Validate against the chosen target backend before moving any bytes.
	if _, err := s.resolveStorageBackend(req.StorageBackend); err != nil {
		return nil, err
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	if err := s.checkHubBinding(ctx, repo, name, hubModelID); err != nil {
		return nil, err
	}

	remote, err := s.stagingRemote(ctx)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	objectKey := uploadObjectKey(id)
	uploadID, err := remote.BeginMultipartUpload(ctx, objectKey)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: could not open the upload: %v", err)
	}
	version := s.defaultUploadVersion()
	now := time.Now().UTC()
	row := &ModelUpload{
		ID:             id,
		OrganizationID: callerOrgID(ctx),
		Name:           name,
		HubModelID:     hubModelID,
		Version:        version,
		Description:    strings.TrimSpace(req.Description),
		StorageBackend: strings.TrimSpace(req.StorageBackend),
		ArchiveName:    strings.TrimSpace(req.ArchiveName),
		TotalBytes:     req.TotalBytes,
		PartSizeBytes:  s.uploadPartSize(),
		UploadID:       uploadID,
		ObjectKey:      objectKey,
		Format:         format,
		State:          UploadStateCreating,
		Actor:          s.grantedBy(ctx),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	db, err := s.gormDB()
	if err != nil {
		_ = remote.AbortMultipartUpload(ctx, objectKey, uploadID)
		return nil, err
	}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		_ = remote.AbortMultipartUpload(ctx, objectKey, uploadID)
		return nil, err
	}
	row.State = UploadStateUploading
	s.updateUploadState(ctx, row.ID, UploadStateUploading, "")
	return row, nil
}

// UploadUploadPart stores one chunk in the staging object store and advances
// the received-byte counter.
func (s *Service) uploadPartRow(ctx context.Context, req uploadPartRequest) (string, error) {
	row, err := s.uploadSessionRow(ctx, req.UploadID)
	if err != nil {
		return "", err
	}
	if row.State != UploadStateCreating && row.State != UploadStateUploading {
		return "", apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: upload is not accepting parts (state %s)", row.State)
	}
	if req.PartNumber < 1 {
		return "", apierrors.Newf(apierrors.CodeModelPathInvalid, "model: part number must be >= 1")
	}
	if req.Size > row.PartSizeBytes {
		return "", apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: part exceeds the %d byte part size", row.PartSizeBytes)
	}
	remote, err := s.stagingRemote(ctx)
	if err != nil {
		return "", err
	}
	etag, err := remote.UploadPart(ctx, row.ObjectKey, row.UploadID, req.PartNumber, req.Body, req.Size)
	if err != nil {
		return "", apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: could not store part %d: %v", req.PartNumber, err)
	}
	s.addUploadBytes(ctx, row.ID, req.Size)
	return etag, nil
}

// ListUploadedPartNumbers returns the received part numbers for resume.
func (s *Service) listUploadedPartsRow(ctx context.Context, uploadID string) ([]int, error) {
	row, err := s.uploadSessionRow(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	remote, err := s.stagingRemote(ctx)
	if err != nil {
		return nil, err
	}
	parts, err := remote.ListUploadedParts(ctx, row.ObjectKey, row.UploadID)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	return parts, nil
}

// GetUploadSession returns one row.
func (s *Service) getUploadSessionRow(ctx context.Context, uploadID string) (*ModelUpload, error) {
	return s.uploadSessionRow(ctx, uploadID)
}

// ListUploadSessions returns the recent uploads, newest first.
func (s *Service) listUploadSessionsRow(ctx context.Context, limit int) ([]*ModelUpload, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var rows []*ModelUpload
	if err := db.WithContext(ctx).Order("created_at DESC").
		Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// CompleteUploadSession finalizes the multipart object, then hands the
// composed archive to a transfer Job (operation=upload) that unpacks it into
// the target backend and — on Job success — the server registers the model.
func (s *Service) completeUploadSessionRow(ctx context.Context, uploadID string) (*ModelUpload, error) {
	row, err := s.uploadSessionRow(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	if row.State != UploadStateUploading && row.State != UploadStateCreating {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: upload is not completeable in state %s", row.State)
	}
	remote, err := s.stagingRemote(ctx)
	if err != nil {
		return nil, err
	}
	parts, err := remote.CompleteUploadPartList(ctx, row.ObjectKey, row.UploadID)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: %v", err)
	}
	if len(parts.Parts) == 0 {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: no parts were uploaded")
	}
	if err := remote.CompleteMultipartUpload(ctx, row.ObjectKey, row.UploadID, parts); err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: could not finalize the upload: %v", err)
	}

	// Dispatch a transfer Job that unpacks the composed archive into the
	// target backend. The registration happens when the Job reports success.
	transferRow, err := s.startUploadTransfer(ctx, row)
	if err != nil {
		return nil, err
	}
	s.updateUploadState(ctx, row.ID, UploadStateProcessing, "")
	if transferRow != nil {
		s.updateUploadTransfer(ctx, row.ID, transferRow.ID)
	}
	return s.uploadSessionRow(ctx, row.ID)
}

// startUploadTransfer builds a transfer Job for an upload and returns the
// transfer row (nil when only inline execution is available).
func (s *Service) startUploadTransfer(ctx context.Context, row *ModelUpload) (*ModelTransfer, error) {
	targetBackend, err := s.resolveStorageBackend(row.StorageBackend)
	if err != nil {
		return nil, err
	}
	spec := &transfer.Spec{
		ID:            uuid.NewString(),
		Operation:     transfer.OperationUpload,
		ModelName:     row.Name,
		Version:       row.Version,
		SourceBackend: s.uploadStagingBackend(),
		TargetBackend: targetBackend.Name,
		WeightPath:    uploadWeightPath(row.Name, row.Version),
		Upload: &transfer.UploadSpec{
			ObjectKey:    row.ObjectKey,
			OriginalName: row.ArchiveName,
			Format:       row.Format,
		},
	}
	// Source: the staging object store.
	srcRef, err := s.remoteRefForBackend(s.uploadStagingBackend())
	if err != nil {
		return nil, err
	}
	srcRef.AccessKeyEnv = "TRANSFER_SOURCE_ACCESS_KEY"
	srcRef.SecretKeyEnv = "TRANSFER_SOURCE_SECRET_KEY"
	spec.SourceObjectStore = &srcRef
	// Target: filesystem mount or object store.
	if targetBackend.Type == storage.TypeFilesystem {
		spec.TargetPath, err = storage.CleanJoin(targetBackend.Filesystem.MountPath, spec.WeightPath)
		if err != nil {
			return nil, err
		}
	} else {
		tgtRef, refErr := s.remoteRef(targetBackend)
		if refErr != nil {
			return nil, refErr
		}
		tgtRef.AccessKeyEnv = "TRANSFER_TARGET_ACCESS_KEY"
		tgtRef.SecretKeyEnv = "TRANSFER_TARGET_SECRET_KEY"
		spec.TargetObjectStore = &tgtRef
	}

	now := time.Now().UTC()
	transferRow := &ModelTransfer{
		ID:            spec.ID,
		Operation:     transfer.OperationUpload,
		ModelID:       unassignedTransferModelID,
		ModelName:     row.Name,
		Version:       row.Version,
		SourceBackend: spec.SourceBackend,
		TargetBackend: targetBackend.Name,
		WeightPath:    spec.WeightPath,
		State:         transfer.StateQueued,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).Create(transferRow).Error; err != nil {
		return nil, err
	}
	if s.transferSettings.Mode == "job" && strings.TrimSpace(s.transferSettings.Image) != "" {
		if err := s.publishTransfer(ctx, mq.DefaultSubjects().ModelTransfers, spec); err != nil {
			// The row exists; the controller will hopefully pick it up, but a
			// publish failure means it never will — fail the upload.
			logger.S().Errorw("model: upload transfer dispatch failed", "upload", row.ID, "err", err)
			_ = db.WithContext(ctx).Delete(transferRow).Error
			return nil, apierrors.Newf(apierrors.CodeInternal, "model: could not start the upload job")
		}
		return transferRow, nil
	}
	// Inline fallback: unpack in-process (upgrade escape hatch).
	go func() {
		runner := &ModelTransfer{ID: transferRow.ID, Operation: transfer.OperationUpload, ModelID: unassignedTransferModelID}
		s.runInlineUpload(ctx, runner, row, targetBackend)
	}()
	return transferRow, nil
}

// runInlineUpload extracts the composed archive in-process when Job execution
// is unavailable. It is the legacy fallback for single-process installs.
func (s *Service) runInlineUpload(ctx context.Context, tm *ModelTransfer, row *ModelUpload, target storage.Backend) {
	s.updateTransfer(ctx, tm.ID, transfer.StateRunning, "", 0, 0)
	temp, err := os.MkdirTemp("", "taas-upload-inline-")
	if err != nil {
		s.updateTransfer(ctx, tm.ID, transfer.StateFailed, err.Error(), 0, 0)
		return
	}
	defer func() { _ = os.RemoveAll(temp) }()
	remote, rerr := s.stagingRemote(ctx)
	if rerr != nil {
		s.updateTransfer(ctx, tm.ID, transfer.StateFailed, rerr.Error(), 0, 0)
		return
	}
	// Download the composed object to a temp archive file.
	archivePath := temp + "/archive"
	obj, oerr := remote.OpenObject(ctx, row.ObjectKey)
	if oerr != nil {
		s.updateTransfer(ctx, tm.ID, transfer.StateFailed, oerr.Error(), 0, 0)
		return
	}
	out, cerr := os.Create(archivePath) //nolint:gosec // archivePath is a temp file we own
	if cerr != nil {
		_ = obj.Close()
		s.updateTransfer(ctx, tm.ID, transfer.StateFailed, cerr.Error(), 0, 0)
		return
	}
	_, cerr = io.Copy(out, obj)
	_ = obj.Close()
	_ = out.Close()
	if cerr != nil {
		s.updateTransfer(ctx, tm.ID, transfer.StateFailed, cerr.Error(), 0, 0)
		return
	}
	dest := ""
	if target.Type == storage.TypeFilesystem {
		dest, _ = storage.CleanJoin(target.Filesystem.MountPath, uploadWeightPath(row.Name, row.Version))
	} else {
		dest = temp + "/extract"
		_ = os.MkdirAll(dest, 0o750)
	}
	if _, _, uerr := modelarchive.Unpack(archivePath, row.Format, dest); uerr != nil {
		s.updateTransfer(ctx, tm.ID, transfer.StateFailed, uerr.Error(), 0, 0)
		return
	}
	if target.Type == storage.TypeObjectStore {
		tgtRef, terr := s.remoteRef(target)
		if terr != nil {
			s.updateTransfer(ctx, tm.ID, transfer.StateFailed, terr.Error(), 0, 0)
			return
		}
		tgt, terr := storage.NewRemoteClient(tgtRef)
		if terr != nil {
			s.updateTransfer(ctx, tm.ID, transfer.StateFailed, terr.Error(), 0, 0)
			return
		}
		if _, terr := tgt.UploadDir(ctx, dest, uploadWeightPath(row.Name, row.Version)); terr != nil {
			s.updateTransfer(ctx, tm.ID, transfer.StateFailed, terr.Error(), 0, 0)
			return
		}
	}
	// Register the model as the final step of the inline path.
	if err := s.registerUploadModel(ctx, row, uploadWeightPath(row.Name, row.Version)); err != nil {
		s.updateTransfer(ctx, tm.ID, transfer.StateFailed, err.Error(), 0, 0)
		return
	}
	s.updateTransfer(ctx, tm.ID, transfer.StateSucceeded, "", 0, 0)
	s.updateUploadState(ctx, row.ID, UploadStateSucceeded, "")
}

// registerUploadModel creates the catalog entry for an upload after the
// weights are in place.
func (s *Service) registerUploadModel(ctx context.Context, row *ModelUpload, weightPath string) error {
	repo, err := s.repository()
	if err != nil {
		return err
	}
	modelID, regErr := repo.RegisterModelOrCreateVersion(ctx, row.Name, row.Description, row.HubModelID, row.Version, weightPath)
	if regErr != nil {
		return regErr
	}
	if row.StorageBackend != "" {
		if err := repo.SetVersionStorageBackend(ctx, modelID, row.Version, row.StorageBackend); err != nil {
			logger.S().Warnw("model: record upload storage backend failed", "model", modelID, "version", row.Version, "err", err)
		}
	}
	s.updateUploadModel(ctx, row.ID, modelID)
	s.recordAudit(ctx, &audit.AuditEvent{
		ActorUserID:  s.grantedBy(ctx),
		ActorType:    "user",
		Action:       "model.upload",
		ResourceType: "model",
		ResourceID:   modelID,
		Result:       "success",
	})
	return nil
}

// CancelUploadSession aborts the multipart upload, removes the staged bytes
// and marks the session cancelled.
func (s *Service) CancelUploadSession(ctx context.Context, uploadID string) error {
	row, err := s.uploadSessionRow(ctx, uploadID)
	if err != nil {
		return err
	}
	if row.State == UploadStateSucceeded || row.State == UploadStateFailed || row.State == UploadStateCancelled {
		return apierrors.Newf(apierrors.CodeModelPathInvalid,
			"model: upload is already %s", row.State)
	}
	// Cancel any dispatch transfer too.
	if row.TransferID != "" {
		_ = s.publishTransfer(ctx, mq.DefaultSubjects().ModelTransfers,
			&transfer.Spec{ID: row.TransferID, Operation: transfer.OperationCancel})
	}
	remote, rerr := s.stagingRemote(ctx)
	if rerr == nil {
		_ = remote.AbortMultipartUpload(ctx, row.ObjectKey, row.UploadID)
		_, _ = remote.DeletePrefix(ctx, row.ObjectKey)
	}
	s.updateUploadState(ctx, row.ID, UploadStateCancelled, "cancelled by operator")
	return nil
}

// handleUploadTransferStatus applies a terminal transfer status for an upload
// Job: on success register the model; otherwise fail the linked session.
func (s *Service) handleUploadTransferStatus(ctx context.Context, status transfer.Status, src *ModelTransfer) {
	row, err := s.uploadSessionForTransfer(ctx, src.ID)
	if err != nil || row == nil {
		return
	}
	if status.State != transfer.StateSucceeded {
		msg := status.Message
		if msg == "" {
			msg = "upload job " + status.State
		}
		s.updateUploadState(ctx, row.ID, UploadStateFailed, msg)
		return
	}
	weightPath := uploadWeightPath(row.Name, row.Version)
	if err := s.registerUploadModel(ctx, row, weightPath); err != nil {
		s.updateUploadState(ctx, row.ID, UploadStateFailed, err.Error())
		return
	}
	s.updateUploadState(ctx, row.ID, UploadStateSucceeded, "")
}

// forwardRemoteRef resolves a named backend to a RemoteRef and sets its env
// names (kept separate because the staging backend is a storage backend but
// the SourceObjectStore naming differs).
func (s *Service) remoteRefForBackend(name string) (storage.RemoteRef, error) {
	backend, err := s.storageConfig.Resolve(name)
	if err != nil {
		return storage.RemoteRef{}, err
	}
	return s.remoteRef(backend)
}

// --- storage of session state ---

func uploadObjectKey(id string) string {
	return "taas-uploads/" + id + "/archive"
}

func uploadWeightPath(name, version string) string {
	return modelarchive.CleanModelDirName(name) + "/" + modelarchive.SanitizeVersion(version) + "/"
}

func (s *Service) defaultUploadVersion() string {
	return "upload-" + time.Now().UTC().Format("20060102-150405")
}

func (s *Service) uploadSessionRow(ctx context.Context, id string) (*ModelUpload, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, apierrors.Newf(apierrors.CodeModelPathInvalid, "model: upload_id is required")
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var row ModelUpload
	if err := db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, apierrors.Newf(apierrors.CodeModelNotFound, "model: upload %s not found", id)
	}
	return &row, nil
}

func (s *Service) uploadSessionForTransfer(ctx context.Context, transferID string) (*ModelUpload, error) {
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	var row ModelUpload
	if err := db.WithContext(ctx).Where("transfer_id = ?", transferID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func (s *Service) updateUploadState(ctx context.Context, id, state, message string) {
	s.updateUpload(ctx, id, map[string]any{"state": state, "message": message, "updated_at": time.Now().UTC()})
}

func (s *Service) updateUploadTransfer(ctx context.Context, id, transferID string) {
	s.updateUpload(ctx, id, map[string]any{"transfer_id": transferID, "updated_at": time.Now().UTC()})
}

func (s *Service) updateUploadModel(ctx context.Context, id, modelID string) {
	s.updateUpload(ctx, id, map[string]any{"model_id": modelID, "updated_at": time.Now().UTC()})
}

func (s *Service) addUploadBytes(ctx context.Context, id string, n int64) {
	s.updateUpload(ctx, id, map[string]any{
		"bytes_received": gorm.Expr("bytes_received + ?", n),
		"updated_at":     time.Now().UTC(),
	})
}

func (s *Service) updateUpload(ctx context.Context, id string, updates map[string]any) {
	db, err := s.gormDB()
	if err != nil {
		return
	}
	if err := db.WithContext(ctx).Model(&ModelUpload{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		logger.S().Warnw("model: upload update skipped", "upload", id, "err", err)
	}
}

func (s *Service) uploadConfig() config.ModelUploadConfig {
	if cfg := config.GetConfig(); cfg != nil {
		return cfg.Model.Upload
	}
	return config.ModelUploadConfig{}
}

var _ = fmt.Sprintf // keep fmt referenced if unused in a build without debug logging
