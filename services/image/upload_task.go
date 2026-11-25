package image

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

// Upload task states. An upload moves uploading -> processing -> terminal, so
// the console can show a progress bar while the archive streams in and an
// indeterminate bar while skopeo pushes it. The terminal states are the same
// names the import history uses, so both history tables read alike.
const (
	// UploadStateUploading is a task whose archive is still arriving.
	UploadStateUploading = "uploading"
	// UploadStateProcessing is a task whose archive is staged and being pushed.
	UploadStateProcessing = "processing"
	// UploadStateSucceeded is a task whose image was pushed and registered.
	UploadStateSucceeded = "succeeded"
	// UploadStateFailed is a task that failed; Message carries why.
	UploadStateFailed = "failed"
	// UploadStateCancelled is a task the operator stopped from the console.
	UploadStateCancelled = "cancelled"
)

// UploadTask is the GORM model of the image_upload_tasks table: one archive
// upload from the operator's disk, kept so the console can list past uploads
// the way it lists past model uploads and downloads.
type UploadTask struct {
	// ID is the server-generated UUID v4 exposed as taskId.
	ID string `gorm:"primaryKey;type:uuid"`
	// ArchiveName is the uploaded file's name.
	ArchiveName string `gorm:"size:255;not null;default:''"`
	// Name, Tag, SourceReference and the classification fields are the
	// form values the push uses when the archive arrives.
	Name            string `gorm:"size:255;not null;default:''"`
	Tag             string `gorm:"size:128;not null;default:''"`
	SourceReference string `gorm:"size:512;not null;default:''"`
	Accelerator     string `gorm:"size:32;not null;default:''"`
	Engine          string `gorm:"size:64;not null;default:''"`
	Description     string `gorm:"size:1024;not null;default:''"`
	// State is one of the UploadState values.
	State string `gorm:"size:16;not null;index"`
	// BytesReceived and TotalBytes drive the console progress bar.
	BytesReceived int64 `gorm:"not null;default:0"`
	TotalBytes    int64 `gorm:"not null;default:0"`
	// ImageID and Reference are set when the upload succeeded.
	ImageID   string `gorm:"size:64;not null;default:''"`
	Reference string `gorm:"size:512;not null;default:''"`
	// Message carries the failure detail; empty on success.
	Message string `gorm:"size:1024;not null;default:''"`
	// Actor is the account that uploaded the archive, for the audit trail.
	Actor     string    `gorm:"size:255;not null;default:''"`
	CreatedAt time.Time `gorm:"index:idx_image_upload_tasks_created,priority:1,sort:DESC"`
	UpdatedAt time.Time
}

// TableName returns the table name of UploadTask.
func (UploadTask) TableName() string { return "image_upload_tasks" }

// UploadTaskBegin is the metadata a task row is created with. It mirrors the
// upload form and the archive's size, which the console reports before the
// body starts so the progress bar has a denominator.
type UploadTaskBegin struct {
	ArchiveName     string
	Name            string
	Tag             string
	SourceReference string
	Accelerator     string
	Engine          string
	Description     string
	TotalBytes      int64
	Actor           string
}

// UploadTaskView is one row of the upload history as the console reads it.
type UploadTaskView struct {
	TaskID          string `json:"taskId"`
	ArchiveName     string `json:"archiveName"`
	Name            string `json:"name"`
	Tag             string `json:"tag"`
	Accelerator     string `json:"accelerator"`
	Engine          string `json:"engine"`
	State           string `json:"state"`
	BytesReceived   int64  `json:"bytesReceived"`
	TotalBytes      int64  `json:"totalBytes"`
	ImageID         string `json:"imageId"`
	Reference       string `json:"reference"`
	Message         string `json:"message"`
	Actor           string `json:"actor"`
	CreatedAt       int64  `json:"createdAt"`
	UpdatedAt       int64  `json:"updatedAt"`
}

// view converts a row into the console-facing shape.
func (t *UploadTask) view() *UploadTaskView {
	return &UploadTaskView{
		TaskID:        t.ID,
		ArchiveName:   t.ArchiveName,
		Name:          t.Name,
		Tag:           t.Tag,
		Accelerator:   t.Accelerator,
		Engine:        t.Engine,
		State:         t.State,
		BytesReceived: t.BytesReceived,
		TotalBytes:    t.TotalBytes,
		ImageID:       t.ImageID,
		Reference:     t.Reference,
		Message:       t.Message,
		Actor:         t.Actor,
		CreatedAt:     t.CreatedAt.Unix(),
		UpdatedAt:     t.UpdatedAt.Unix(),
	}
}

// UploadTaskRepository persists image upload tasks.
type UploadTaskRepository struct {
	*database.BaseRepository[UploadTask]
	db *database.Manager
}

// NewUploadTaskRepository constructs an UploadTaskRepository.
func NewUploadTaskRepository(db *gorm.DB) *UploadTaskRepository {
	mgr := database.NewManager(db)
	return &UploadTaskRepository{
		BaseRepository: database.NewBaseRepository[UploadTask](mgr),
		db:             mgr,
	}
}

// Create inserts a new upload task row, generating the id when unset.
func (r *UploadTaskRepository) Create(ctx context.Context, task *UploadTask) error {
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	return r.BaseRepository.Create(ctx, task)
}

// RecordProgress updates the received byte count while a task is still
// uploading. A task that already left that state (cancelled, failed) is left
// alone so a late progress write cannot revive it.
func (r *UploadTaskRepository) RecordProgress(ctx context.Context, id string, received int64) error {
	return r.DB(ctx).Model(&UploadTask{}).
		Where("id = ? AND state = ?", id, UploadStateUploading).
		Updates(map[string]any{
			"bytes_received": received,
			"updated_at":     time.Now().UTC(),
		}).Error
}

// MarkProcessing records that the archive is staged and the push started.
func (r *UploadTaskRepository) MarkProcessing(ctx context.Context, id string) error {
	return r.DB(ctx).Model(&UploadTask{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"state":      UploadStateProcessing,
			"updated_at": time.Now().UTC(),
		}).Error
}

// ApplySuccess records a successful upload: the registered image id and its
// reference, with a terminal state.
func (r *UploadTaskRepository) ApplySuccess(ctx context.Context, id, imageID, reference string) error {
	return r.DB(ctx).Model(&UploadTask{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"state":          UploadStateSucceeded,
			"image_id":       imageID,
			"reference":      reference,
			"bytes_received": gorm.Expr("total_bytes"),
			"message":        "",
			"updated_at":     time.Now().UTC(),
		}).Error
}

// ApplyFailure records a failed upload with a reason.
func (r *UploadTaskRepository) ApplyFailure(ctx context.Context, id, message string) error {
	return r.DB(ctx).Model(&UploadTask{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"state":      UploadStateFailed,
			"message":    message,
			"updated_at": time.Now().UTC(),
		}).Error
}

// Cancel marks a still-running task cancelled. It reports whether a row
// changed, so the caller can tell a no-op cancel from a real one.
func (r *UploadTaskRepository) Cancel(ctx context.Context, id string) (bool, error) {
	res := r.DB(ctx).Model(&UploadTask{}).
		Where("id = ? AND state IN ?", id, []string{UploadStateUploading, UploadStateProcessing}).
		Updates(map[string]any{
			"state":      UploadStateCancelled,
			"updated_at": time.Now().UTC(),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// FindByID returns the upload task with the given id; a miss maps to
// CodeImageNotFound, matching the import task history.
func (r *UploadTaskRepository) FindByID(ctx context.Context, id string) (*UploadTask, error) {
	row, err := r.GetByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeImageNotFound)
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// List returns one page of upload tasks, newest first, and the total count.
func (r *UploadTaskRepository) List(ctx context.Context, offset, limit int) ([]*UploadTask, int64, error) {
	var total int64
	query := r.DB(ctx).Model(&UploadTask{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*UploadTask
	if err := query.Order("created_at DESC").Order("id DESC").
		Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// uploadTaskRepository lazily wires and returns the upload task repository.
func (s *Service) uploadTaskRepository() (*UploadTaskRepository, error) {
	if s.uploadTasks != nil {
		return s.uploadTasks, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.uploadTasks = NewUploadTaskRepository(db)
	return s.uploadTasks, nil
}

// BeginUploadTask creates the history row for an upload that is about to
// stream in, so the console's task list can show its progress.
func (s *Service) BeginUploadTask(ctx context.Context, req *UploadTaskBegin) (string, error) {
	repo, err := s.uploadTaskRepository()
	if err != nil {
		return "", err
	}
	row := &UploadTask{
		ID:              uuid.NewString(),
		ArchiveName:     trimTo(req.ArchiveName, 255),
		Name:            trimTo(req.Name, 255),
		Tag:             trimTo(req.Tag, 128),
		SourceReference: trimTo(req.SourceReference, maxSourceReferenceLen),
		Accelerator:     trimTo(req.Accelerator, 32),
		Engine:          trimTo(req.Engine, maxEngineLen),
		Description:     trimTo(req.Description, maxDescriptionLen),
		State:           UploadStateUploading,
		TotalBytes:      req.TotalBytes,
		Actor:           trimTo(req.Actor, 255),
	}
	if err := repo.Create(ctx, row); err != nil {
		return "", err
	}
	return row.ID, nil
}

// RecordUploadProgress stores how many archive bytes landed so far. It is a
// best-effort write: a progress sample must never fail the upload.
func (s *Service) RecordUploadProgress(ctx context.Context, taskID string, received int64) {
	repo, err := s.uploadTaskRepository()
	if err != nil {
		return
	}
	if err := repo.RecordProgress(ctx, taskID, received); err != nil {
		logger.S().Warnw("image: record upload progress failed", "task", taskID, "err", err)
	}
}

// CompleteUploadTask pushes a staged archive whose row already exists: it
// refuses a cancelled task, marks the row processing, runs the same
// push-and-register path a synchronous upload uses, and records the outcome.
func (s *Service) CompleteUploadTask(ctx context.Context, taskID, archivePath string) (*UploadImageResult, error) {
	repo, err := s.uploadTaskRepository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if row.State == UploadStateCancelled {
		return nil, apierrors.Newf(apierrors.CodeImageReferenceInvalid, "image: upload cancelled")
	}
	if err := repo.MarkProcessing(ctx, taskID); err != nil {
		return nil, err
	}
	res, err := s.UploadImage(ctx, UploadImageRequest{
		ArchivePath:     archivePath,
		Name:            row.Name,
		Tag:             row.Tag,
		SourceReference: row.SourceReference,
		Accelerator:     row.Accelerator,
		Engine:          row.Engine,
		Description:     row.Description,
		Actor:           row.Actor,
	})
	if err != nil {
		message := "image: upload failed"
		if ae, ok := apierrors.As(err); ok {
			message = ae.Message
		} else if err.Error() != "" {
			message = err.Error()
		}
		_ = repo.ApplyFailure(ctx, taskID, trimTo(message, maxDescriptionLen))
		return nil, err
	}
	if err := repo.ApplySuccess(ctx, taskID, res.ImageID, res.Reference); err != nil {
		logger.S().Warnw("image: record upload success failed", "task", taskID, "err", err)
	}
	return res, nil
}

// FailUploadTask records that an upload aborted before the push started.
func (s *Service) FailUploadTask(ctx context.Context, taskID, message string) {
	repo, err := s.uploadTaskRepository()
	if err != nil {
		return
	}
	if err := repo.ApplyFailure(ctx, taskID, trimTo(message, maxDescriptionLen)); err != nil {
		logger.S().Warnw("image: record upload failure failed", "task", taskID, "err", err)
	}
}

// ListUploadTasks returns one page of the upload history, newest first.
func (s *Service) ListUploadTasks(ctx context.Context, offset, limit int) ([]*UploadTaskView, int64, error) {
	repo, err := s.uploadTaskRepository()
	if err != nil {
		return nil, 0, err
	}
	rows, total, err := repo.List(ctx, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	views := make([]*UploadTaskView, 0, len(rows))
	for _, row := range rows {
		views = append(views, row.view())
	}
	return views, total, nil
}

// CancelUploadTask stops a running upload: the row turns cancelled and the
// handler discards the staged archive instead of pushing it.
func (s *Service) CancelUploadTask(ctx context.Context, taskID string) error {
	repo, err := s.uploadTaskRepository()
	if err != nil {
		return err
	}
	if _, err := repo.Cancel(ctx, taskID); err != nil {
		return err
	}
	return nil
}

// trimTo bounds a stored field to its column, so a row insert cannot fail on
// a value the upload form let through.
func trimTo(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
