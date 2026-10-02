package finetuning

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// JobRepository persists fine-tuning jobs.
type JobRepository struct {
	db *database.Manager
}

// NewJobRepository constructs a JobRepository bound to a GORM database.
func NewJobRepository(db *gorm.DB) *JobRepository {
	return &JobRepository{db: database.NewManager(db)}
}

// CreateJob inserts a new job row.
func (r *JobRepository) CreateJob(ctx context.Context, j *FineTuningJob) error {
	return r.db.DB(ctx).Create(j).Error
}

// FindJobByID returns one job by id. A miss maps to
// CodeFineTuningJobNotFound.
func (r *JobRepository) FindJobByID(ctx context.Context, jobID string) (*FineTuningJob, error) {
	if _, err := uuid.Parse(jobID); err != nil {
		return nil, apierrors.New(apierrors.CodeFineTuningJobNotFound)
	}
	var row FineTuningJob
	err := r.db.DB(ctx).Where("id = ?", jobID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeFineTuningJobNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// ListJobs returns the jobs, newest first.
func (r *JobRepository) ListJobs(ctx context.Context) ([]FineTuningJob, error) {
	var rows []FineTuningJob
	err := r.db.DB(ctx).Order("created_at DESC").Find(&rows).Error
	return rows, err
}

// UpdateJobState transitions a job's state in one transaction, setting
// the failure reason and fine-tuned model id (AD6).
func (r *JobRepository) UpdateJobState(ctx context.Context, jobID, state, failureReason, fineTunedModelID string) error {
	updates := map[string]any{"state": state}
	if failureReason != "" {
		updates["failure_reason"] = failureReason
	}
	if fineTunedModelID != "" {
		updates["fine_tuned_model_id"] = fineTunedModelID
	}
	return r.db.DB(ctx).Model(&FineTuningJob{}).Where("id = ?", jobID).Updates(updates).Error
}