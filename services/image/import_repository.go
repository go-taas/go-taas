package image

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// ImportTaskRepository persists image import tasks on top of the
// generic repository base.
type ImportTaskRepository struct {
	*database.BaseRepository[ImportTask]
	db *database.Manager
}

// NewImportTaskRepository constructs an ImportTaskRepository bound to a
// database Manager.
func NewImportTaskRepository(db *gorm.DB) *ImportTaskRepository {
	mgr := database.NewManager(db)
	return &ImportTaskRepository{
		BaseRepository: database.NewBaseRepository[ImportTask](mgr),
		db:             mgr,
	}
}

// Create inserts a new import task row, generating the id when unset.
func (r *ImportTaskRepository) Create(ctx context.Context, task *ImportTask) error {
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	return r.BaseRepository.Create(ctx, task)
}

// FindByID returns the import task with the given id. A miss maps to
// CodeImageNotFound (task errors are image-scoped for a consistent
// console experience).
func (r *ImportTaskRepository) FindByID(ctx context.Context, id string) (*ImportTask, error) {
	row, err := r.GetByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeImageNotFound)
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// List returns one page of import tasks, newest first, and the total
// count.
func (r *ImportTaskRepository) List(ctx context.Context, offset, limit int) ([]*ImportTask, int64, error) {
	var total int64
	query := r.DB(ctx).Model(&ImportTask{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*ImportTask
	if err := query.Order("created_at DESC").Order("id DESC").
		Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// NextPending returns the oldest pending task, claiming it as running
// atomically so concurrent workers never pick the same task. It returns
// nil when no pending task exists.
func (r *ImportTaskRepository) NextPending(ctx context.Context) (*ImportTask, error) {
	var task ImportTask
	err := r.DB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.
			Where("state = ?", ImportStatePending).
			Order("created_at ASC").Order("id ASC").
			First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		return tx.Model(&ImportTask{}).
			Where("id = ? AND state = ?", task.ID, ImportStatePending).
			Update("state", ImportStateRunning).Error
	})
	if err != nil {
		return nil, err
	}
	if task.ID == "" {
		return nil, nil
	}
	return &task, nil
}

// HasActiveByDestination reports whether a pending or running task
// targets the same destination reference. It is the friendly pre-check
// that keeps an operator from enqueueing the same import twice while
// one is in flight.
func (r *ImportTaskRepository) HasActiveByDestination(ctx context.Context, destRef string) (bool, error) {
	var count int64
	err := r.DB(ctx).Model(&ImportTask{}).
		Where("destination_reference = ? AND state IN ?", destRef, []string{ImportStatePending, ImportStateRunning}).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ApplySuccess records a successful import: the destination reference
// and the registered image id, with a terminal succeeded state.
func (r *ImportTaskRepository) ApplySuccess(ctx context.Context, taskID, destRef, imageID string) error {
	return r.DB(ctx).Model(&ImportTask{}).
		Where("id = ?", taskID).
		Updates(map[string]any{
			"state":                ImportStateSucceeded,
			"destination_reference": destRef,
			"image_id":             imageID,
			"source_password":      "",
			"failure_reason":       "",
			"updated_at":           time.Now().UTC(),
		}).Error
}

// ApplyFailure records a failed import with a reason, clearing the
// stored source password.
func (r *ImportTaskRepository) ApplyFailure(ctx context.Context, taskID, reason string) error {
	return r.DB(ctx).Model(&ImportTask{}).
		Where("id = ?", taskID).
		Updates(map[string]any{
			"state":           ImportStateFailed,
			"failure_reason":  reason,
			"source_password": "",
			"updated_at":      time.Now().UTC(),
		}).Error
}

// RecoverInterruptedTasks fails tasks a previous process left pending or
// running: their worker is gone, so they would otherwise stay active
// forever.
func (r *ImportTaskRepository) RecoverInterruptedTasks(ctx context.Context, reason string) (int64, error) {
	res := r.DB(ctx).Model(&ImportTask{}).
		Where("state IN ?", []string{ImportStatePending, ImportStateRunning}).
		Updates(map[string]any{
			"state":           ImportStateFailed,
			"failure_reason":  reason,
			"source_password": "",
			"updated_at":      time.Now().UTC(),
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}