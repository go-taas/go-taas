package image

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Repository persists images on top of the generic repository base.
// All reads and writes join an open transaction via the context.
type Repository struct {
	*database.BaseRepository[Image]
	db *database.Manager
}

// NewRepository constructs a Repository bound to a database Manager.
func NewRepository(db *gorm.DB) *Repository {
	mgr := database.NewManager(db)
	return &Repository{
		BaseRepository: database.NewBaseRepository[Image](mgr),
		db:             mgr,
	}
}

// CreateImage inserts a new image row, generating the id when unset. A
// unique violation on (name, tag, accelerator) maps to CodeImageExists.
func (r *Repository) CreateImage(ctx context.Context, img *Image) error {
	if img.ID == "" {
		img.ID = uuid.NewString()
	}
	if err := r.Create(ctx, img); err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeImageExists)
		}
		return err
	}
	return nil
}

// FindByID returns the image with the given id. A miss maps to
// CodeImageNotFound.
func (r *Repository) FindByID(ctx context.Context, id string) (*Image, error) {
	row, err := r.GetByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeImageNotFound)
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// FindByTriple returns the image with the given (name, tag, accelerator)
// triple. A miss maps to CodeImageNotFound.
func (r *Repository) FindByTriple(ctx context.Context, name, tag, accelerator string) (*Image, error) {
	var row Image
	err := r.DB(ctx).
		Where("name = ? AND tag = ? AND accelerator = ?", name, tag, accelerator).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeImageNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListImages returns one page of images ordered by creation time
// (newest first) and the total count. Empty filter values match
// everything.
func (r *Repository) ListImages(ctx context.Context, accelerator, engine string, offset, limit int) ([]*Image, int64, error) {
	query := r.DB(ctx).Model(&Image{})
	if accelerator != "" {
		query = query.Where("accelerator = ?", accelerator)
	}
	if engine != "" {
		query = query.Where("engine = ?", engine)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*Image
	if err := query.Order("created_at DESC").Order("id DESC").
		Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// UpdateDescription edits the description only (D6): identity and
// compatibility fields are immutable by contract.
func (r *Repository) UpdateDescription(ctx context.Context, id, description string) error {
	return r.DB(ctx).Model(&Image{}).
		Where("id = ?", id).
		Update("description", description).Error
}

// Delete removes the image row (hard delete).
func (r *Repository) Delete(ctx context.Context, id string) error {
	return r.DB(ctx).Delete(&Image{}, "id = ?", id).Error
}

// CountAll returns the total number of image rows. It gates the
// first-boot seed: a non-empty table is never re-seeded (D2).
func (r *Repository) CountAll(ctx context.Context) (int64, error) {
	var count int64
	if err := r.DB(ctx).Model(&Image{}).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// SeedFromConfig inserts the config image.registry entries as rows,
// preserving their config imageId values (D2, D4). It is insert-only:
// existing rows are never overwritten. Malformed entries are logged
// and skipped, never fatal.
func (r *Repository) SeedFromConfig(ctx context.Context, entries []config.ImageRegistryEntry) error {
	for _, e := range entries {
		// Insert-only: an existing row (by id or by triple) is left
		// untouched; only genuinely new entries are added.
		if _, err := r.FindByID(ctx, e.ImageID); err == nil {
			continue
		}
		row := &Image{
			ID:          e.ImageID,
			Name:        e.Name,
			Tag:         e.Tag,
			Accelerator: strings.ToLower(e.Accelerator),
			Engine:      e.Engine,
		}
		if err := r.CreateImage(ctx, row); err != nil {
			// A triple collision with a differently-id'd row is a
			// config drift: log and skip, never fatal.
			if ae, ok := apierrors.As(err); ok && ae.Code == apierrors.CodeImageExists {
				continue
			}
			return err
		}
	}
	return nil
}

// WarmupTaskRepository persists warmup tasks.
type WarmupTaskRepository struct {
	*database.BaseRepository[WarmupTask]
	db *database.Manager
}

// NewWarmupTaskRepository constructs a WarmupTaskRepository bound to a
// database Manager.
func NewWarmupTaskRepository(db *gorm.DB) *WarmupTaskRepository {
	mgr := database.NewManager(db)
	return &WarmupTaskRepository{
		BaseRepository: database.NewBaseRepository[WarmupTask](mgr),
		db:             mgr,
	}
}

// Create inserts a new warmup task row, generating the id when unset.
// A unique violation on the one-active-per-image partial index maps to
// CodeImageWarmupFailed (D8).
func (r *WarmupTaskRepository) Create(ctx context.Context, task *WarmupTask) error {
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	if task.NodeSelector == nil {
		task.NodeSelector = datatypes.JSON([]byte("{}"))
	}
	if task.NodeResults == nil {
		task.NodeResults = datatypes.JSON([]byte("[]"))
	}
	if err := r.BaseRepository.Create(ctx, task); err != nil {
		if isUniqueViolation(err) {
			return apierrors.New(apierrors.CodeImageWarmupFailed)
		}
		return err
	}
	return nil
}

// FindByID returns the warmup task with the given id. A miss maps to
// CodeImageNotFound (task errors are image-scoped for a consistent
// console experience).
func (r *WarmupTaskRepository) FindByID(ctx context.Context, id string) (*WarmupTask, error) {
	row, err := r.GetByID(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeImageNotFound)
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// ListByImageID returns one page of warmup tasks for the image, newest
// first, and the total count.
func (r *WarmupTaskRepository) ListByImageID(ctx context.Context, imageID string, offset, limit int) ([]*WarmupTask, int64, error) {
	var total int64
	query := r.DB(ctx).Model(&WarmupTask{}).Where("image_id = ?", imageID)
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*WarmupTask
	if err := query.Order("created_at DESC").Order("id DESC").
		Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// HasActiveByImageID reports whether a pending or running task exists
// for the image. It is the friendly pre-check before the partial unique
// index backstop (D8).
func (r *WarmupTaskRepository) HasActiveByImageID(ctx context.Context, imageID string) (bool, error) {
	var count int64
	err := r.DB(ctx).Model(&WarmupTask{}).
		Where("image_id = ? AND state IN ?", imageID, []string{TaskStatePending, TaskStateRunning}).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// LatestByImageIDs returns the newest warmup task per image for the
// given ids. Images without tasks are absent from the result.
func (r *WarmupTaskRepository) LatestByImageIDs(ctx context.Context, imageIDs []string) (map[string]*WarmupTask, error) {
	out := map[string]*WarmupTask{}
	if len(imageIDs) == 0 {
		return out, nil
	}
	var rows []*WarmupTask
	// Distinct-on is PostgreSQL-specific; the portable form joins a
	// grouped subquery on (image_id, max created_at). Ids are UUIDs, so
	// MAX(id) is not chronological.
	err := r.DB(ctx).
		Joins("JOIN (?) AS latest ON latest.image_id = warmup_tasks.image_id AND latest.max_created = warmup_tasks.created_at",
			r.DB(ctx).Model(&WarmupTask{}).
				Select("image_id, MAX(created_at) AS max_created").
				Where("image_id IN ?", imageIDs).
				Group("image_id"),
		).
		Where("warmup_tasks.image_id IN ?", imageIDs).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if cur, ok := out[row.ImageID]; ok && cur.CreatedAt.After(row.CreatedAt) {
			continue
		}
		out[row.ImageID] = row
	}
	return out, nil
}

// ApplyStatus applies one status report from the controller. Terminal
// states are never overwritten: a late running report after succeeded
// is skipped (RowsAffected == 0).
func (r *WarmupTaskRepository) ApplyStatus(ctx context.Context, taskID, state string, nodeResults []NodeResult, failureReason *string) error {
	resultsJSON, err := json.Marshal(nodeResults)
	if err != nil {
		return err
	}
	updates := map[string]any{
		"state":        state,
		"node_results": datatypes.JSON(resultsJSON),
		"updated_at":   time.Now().UTC(),
	}
	if failureReason != nil {
		updates["failure_reason"] = *failureReason
	}
	res := r.DB(ctx).Model(&WarmupTask{}).
		Where("id = ? AND state NOT IN ?", taskID, []string{TaskStateSucceeded, TaskStateFailed}).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		// Unknown task id or terminal-state overwrite: the caller
		// decides whether to log-and-skip; the repository surfaces it
		// as not-found.
		return apierrors.New(apierrors.CodeImageNotFound)
	}
	return nil
}

// MarkFailed sets state=failed with a reason. It is the compensation
// path when publishing the task event fails after the row was written.
func (r *WarmupTaskRepository) MarkFailed(ctx context.Context, taskID, reason string) error {
	res := r.DB(ctx).Model(&WarmupTask{}).
		Where("id = ?", taskID).
		Updates(map[string]any{
			"state":          TaskStateFailed,
			"failure_reason": reason,
			"updated_at":     time.Now().UTC(),
		})
	return res.Error
}

// oneActivePartialIndex is the D8 race-free backstop: at most one
// pending-or-running task per image. GORM does not model partial
// indexes, so it is created after AutoMigrate.
const oneActivePartialIndex = `CREATE UNIQUE INDEX IF NOT EXISTS idx_warmup_tasks_one_active ON warmup_tasks(image_id) WHERE state IN ('pending','running')`

// isUniqueViolation reports whether err is a unique-constraint violation
// across the supported dialects (PostgreSQL 23505, SQLite generic
// message).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "23505") ||
		strings.Contains(msg, "UNIQUE constraint failed")
}
