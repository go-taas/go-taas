package batch

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// Repository persists batch jobs on top of the generic repository base
// (feature #42).
type Repository struct {
	*database.BaseRepository[BatchJob]
	db *database.Manager
}

// NewRepository constructs a Repository bound to a database Manager.
func NewRepository(db *gorm.DB) *Repository {
	mgr := database.NewManager(db)
	return &Repository{
		BaseRepository: database.NewBaseRepository[BatchJob](mgr),
		db:             mgr,
	}
}

// InsertBatchJob inserts a batch job and returns the generated id (AC1).
func (r *Repository) InsertBatchJob(ctx context.Context, job *BatchJob) (string, error) {
	if job.BatchID == "" {
		job.BatchID = uuid.NewString()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	if err := r.DB(ctx).Create(job).Error; err != nil {
		return "", apierrors.Wrap(apierrors.CodeInternal, err, "batch: insert failed")
	}
	return job.BatchID, nil
}

// FindBatchJobByID returns the batch job with the given id scoped to the
// org. A miss maps to 12601 (AC2).
func (r *Repository) FindBatchJobByID(ctx context.Context, orgID, batchID string) (*BatchJob, error) {
	if _, err := uuid.Parse(batchID); err != nil {
		return nil, apierrors.New(apierrors.CodeBatchJobNotFound)
	}
	var row BatchJob
	err := r.DB(ctx).First(&row, "batch_id = ? AND organization_id = ?", batchID, orgID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeBatchJobNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// FindBatchJobAnyOrg returns a batch job by id regardless of org (for the
// admin fleet view). A miss maps to 12601.
func (r *Repository) FindBatchJobAnyOrg(ctx context.Context, batchID string) (*BatchJob, error) {
	if _, err := uuid.Parse(batchID); err != nil {
		return nil, apierrors.New(apierrors.CodeBatchJobNotFound)
	}
	var row BatchJob
	err := r.DB(ctx).First(&row, "batch_id = ?", batchID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeBatchJobNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListBatchJobs returns one page of batch jobs for the org, newest first,
// with the total count (AC2).
func (r *Repository) ListBatchJobs(ctx context.Context, filter BatchFilter) ([]*BatchJob, int64, error) {
	query := r.DB(ctx).Model(&BatchJob{}).Where("organization_id = ?", filter.OrganizationID)
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.Model != "" {
		query = query.Where("model = ?", filter.Model)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*BatchJob
	if err := query.
		Order("created_at DESC").Order("batch_id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListBatchJobsAnyOrg returns one page of batch jobs across all orgs,
// newest first (admin fleet view, masked).
func (r *Repository) ListBatchJobsAnyOrg(ctx context.Context, filter BatchFilter) ([]*BatchJob, int64, error) {
	query := r.DB(ctx).Model(&BatchJob{})
	if filter.OrganizationID != "" {
		query = query.Where("organization_id = ?", filter.OrganizationID)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.Model != "" {
		query = query.Where("model = ?", filter.Model)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []*BatchJob
	if err := query.
		Order("created_at DESC").Order("batch_id DESC").
		Offset(filter.Offset).Limit(filter.Limit).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// UpdateBatchJob persists the mutable fields of a batch job.
func (r *Repository) UpdateBatchJob(ctx context.Context, job *BatchJob) error {
	return r.DB(ctx).Model(&BatchJob{}).
		Where("batch_id = ?", job.BatchID).
		Updates(map[string]any{
			"status":             job.Status,
			"input_file_key":     job.InputFileKey,
			"result_file_key":    job.ResultFileKey,
			"error_file_key":     job.ErrorFileKey,
			"processed_requests": job.ProcessedRequests,
			"succeeded_requests": job.SucceededRequests,
			"failed_requests":    job.FailedRequests,
			"input_tokens":       job.InputTokens,
			"output_tokens":      job.OutputTokens,
			"cost":               job.Cost,
			"files_expired":      job.FilesExpired,
			"error":              job.Error,
			"completed_at":       job.CompletedAt,
			"files_expire_at":    job.FilesExpireAt,
		}).Error
}

// NextInProgressJob returns the oldest in_progress job (for the worker).
func (r *Repository) NextInProgressJob(ctx context.Context) (*BatchJob, error) {
	var row BatchJob
	err := r.DB(ctx).Where("status = ?", StatusInProgress).
		Order("created_at ASC").Order("batch_id ASC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// PromoteValidatingJob atomically claims the oldest validating job by
// advancing it to in_progress (AD3: validating -> in_progress). The
// conditional update makes the claim single-writer safe: only the
// caller whose update matched the row owns the job.
func (r *Repository) PromoteValidatingJob(ctx context.Context) (*BatchJob, error) {
	var row BatchJob
	err := r.DB(ctx).Where("status = ?", StatusValidating).
		Order("created_at ASC").Order("batch_id ASC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := r.DB(ctx).Model(&BatchJob{}).
		Where("batch_id = ? AND status = ?", row.BatchID, StatusValidating).
		Update("status", StatusInProgress)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		// Another worker claimed it between the read and the update.
		return nil, nil
	}
	row.Status = StatusInProgress
	return &row, nil
}

// IncrementCounters atomically advances a job's progress counters.
func (r *Repository) IncrementCounters(ctx context.Context, batchID string, processed, succeeded, failed, inputTokens, outputTokens, cost int64) error {
	return r.DB(ctx).Model(&BatchJob{}).Where("batch_id = ?", batchID).
		Updates(map[string]any{
			"processed_requests": gorm.Expr("processed_requests + ?", processed),
			"succeeded_requests": gorm.Expr("succeeded_requests + ?", succeeded),
			"failed_requests":    gorm.Expr("failed_requests + ?", failed),
			"input_tokens":       gorm.Expr("input_tokens + ?", inputTokens),
			"output_tokens":      gorm.Expr("output_tokens + ?", outputTokens),
			"cost":               gorm.Expr("cost + ?", cost),
		}).Error
}

// SetStatus transitions a job to a new status with optional completion
// timestamps.
func (r *Repository) SetStatus(ctx context.Context, batchID, status string, completedAt, filesExpireAt *time.Time) error {
	updates := map[string]any{"status": status}
	if completedAt != nil {
		updates["completed_at"] = completedAt
	}
	if filesExpireAt != nil {
		updates["files_expire_at"] = filesExpireAt
	}
	return r.DB(ctx).Model(&BatchJob{}).Where("batch_id = ?", batchID).Updates(updates).Error
}

// SetError sets the failure reason on a job.
func (r *Repository) SetError(ctx context.Context, batchID, errMsg string) error {
	return r.DB(ctx).Model(&BatchJob{}).Where("batch_id = ?", batchID).
		Updates(map[string]any{"error": errMsg}).Error
}

// SetFileKeys sets the result/error file keys on a job.
func (r *Repository) SetFileKeys(ctx context.Context, batchID, resultKey, errorKey string) error {
	return r.DB(ctx).Model(&BatchJob{}).Where("batch_id = ?", batchID).
		Updates(map[string]any{"result_file_key": resultKey, "error_file_key": errorKey}).Error
}

// FindExpiredFiles returns completed jobs whose files have expired (for
// the retention runner, AD8).
func (r *Repository) FindExpiredFiles(ctx context.Context, cutoff time.Time, limit int) ([]*BatchJob, error) {
	var rows []*BatchJob
	err := r.DB(ctx).
		Where("status = ? AND files_expired = ? AND files_expire_at IS NOT NULL AND files_expire_at < ?",
			StatusCompleted, false, cutoff).
		Order("files_expire_at ASC").
		Limit(limit).
		Find(&rows).Error
	return rows, err
}

// MarkFilesExpired sets the files_expired flag on a job (AD8).
func (r *Repository) MarkFilesExpired(ctx context.Context, batchID string) error {
	return r.DB(ctx).Model(&BatchJob{}).Where("batch_id = ?", batchID).
		Updates(map[string]any{"files_expired": true}).Error
}
