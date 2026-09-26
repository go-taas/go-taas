package infer

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/database"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// LoadTestFilter narrows a load-test history query (feature #20, AC3).
type LoadTestFilter struct {
	// ServiceID filters by target service (optional).
	ServiceID string
	// ModelID filters by deployed model (optional).
	ModelID string
	// State filters by run state (optional, closed set).
	State string
	// Search matches a service-name substring (optional, case-insensitive).
	Search string
}

// LoadTestRepository persists load-test runs on top of the generic
// repository base (feature #20, §14.3).
type LoadTestRepository struct {
	*database.BaseRepository[LoadTest]
	db *database.Manager
}

// NewLoadTestRepository constructs a LoadTestRepository bound to a
// database Manager.
func NewLoadTestRepository(db *gorm.DB) *LoadTestRepository {
	mgr := database.NewManager(db)
	return &LoadTestRepository{
		BaseRepository: database.NewBaseRepository[LoadTest](mgr),
		db:             mgr,
	}
}

// Create inserts a new pending run.
func (r *LoadTestRepository) Create(ctx context.Context, run *LoadTest) error {
	return r.BaseRepository.Create(ctx, run)
}

// FindByID returns one run by id; a miss maps to 10308.
func (r *LoadTestRepository) FindByID(ctx context.Context, id string) (*LoadTest, error) {
	var row LoadTest
	err := r.DB(ctx).Where("id = ?", id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apierrors.New(apierrors.CodeLoadTestNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// List returns one page of runs (newest first) and the total count for
// the given filter.
func (r *LoadTestRepository) List(ctx context.Context, filter LoadTestFilter, offset, limit int) ([]*LoadTest, int64, error) {
	if limit <= 0 {
		return nil, 0, apierrors.Newf(apierrors.CodeInternal, "infer: invalid load-test page limit %d", limit)
	}
	if offset < 0 {
		return nil, 0, apierrors.Newf(apierrors.CodeInternal, "infer: negative load-test page offset %d", offset)
	}

	// build returns a fresh query so the count and the page read never
	// share statement state (GORM finishers mutate the statement).
	build := func() *gorm.DB {
		query := r.DB(ctx).Model(&LoadTest{})
		if filter.ServiceID != "" {
			query = query.Where("service_id = ?", filter.ServiceID)
		}
		if filter.ModelID != "" {
			query = query.Where("model_id = ?", filter.ModelID)
		}
		if filter.State != "" {
			query = query.Where("state = ?", filter.State)
		}
		if filter.Search != "" {
			query = query.Where("LOWER(service_name) LIKE ?", "%"+strings.ToLower(filter.Search)+"%")
		}
		return query
	}

	var total int64
	if err := build().Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}

	var rows []*LoadTest
	err := build().
		Order("created_at DESC").
		Order("id DESC").
		Offset(offset).
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListActive returns the pending/running runs (runner recovery and the
// active-runs section).
func (r *LoadTestRepository) ListActive(ctx context.Context) ([]*LoadTest, error) {
	var rows []*LoadTest
	err := r.DB(ctx).
		Where("state IN ?", []string{LoadTestStatePending, LoadTestStateRunning}).
		Order("created_at ASC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// UpdateProgress persists a live-progress snapshot (AD12). Progress is
// not stored in dedicated columns: the in-memory aggregator owns the
// live numbers and this write refreshes updated_at plus the running
// counts so a restarted server can still see the run was active.
func (r *LoadTestRepository) UpdateProgress(ctx context.Context, id string, progress LoadTestProgress) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		res := r.DB(ctx).Model(&LoadTest{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"total_requests": progress.RequestsSent,
				"success_count":  progress.SuccessCount,
				"failure_count":  progress.FailureCount,
				"updated_at":     time.Now().UTC(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return apierrors.New(apierrors.CodeLoadTestNotFound)
		}
		return nil
	})
}

// completeFields builds the terminal update map from a result set and
// state (AD6, AD9).
func completeFields(result LoadTestResult, state string, failureReason *string, completedAt time.Time) map[string]any {
	fields := map[string]any{
		"state":                 state,
		"total_requests":        result.TotalRequests,
		"success_count":         result.SuccessCount,
		"failure_count":         result.FailureCount,
		"error_rate":            result.ErrorRate,
		"throughput_rps":        result.ThroughputRPS,
		"output_tokens_per_sec": result.OutputTokensPerSec,
		"input_tokens":          result.InputTokens,
		"output_tokens":         result.OutputTokens,
		"latency_p50_ms":        result.LatencyP50Ms,
		"latency_p90_ms":        result.LatencyP90Ms,
		"latency_p95_ms":        result.LatencyP95Ms,
		"latency_p99_ms":        result.LatencyP99Ms,
		"completed_at":          completedAt,
		"updated_at":            completedAt,
	}
	if failureReason != nil {
		fields["failure_reason"] = *failureReason
	}
	return fields
}

// Complete writes the final results and terminal state of a run.
func (r *LoadTestRepository) Complete(ctx context.Context, id string, result LoadTestResult, state string, failureReason *string) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		res := r.DB(ctx).Model(&LoadTest{}).
			Where("id = ?", id).
			Updates(completeFields(result, state, failureReason, time.Now().UTC()))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return apierrors.New(apierrors.CodeLoadTestNotFound)
		}
		return nil
	})
}

// MarkRunning transitions a run to running and records started_at.
func (r *LoadTestRepository) MarkRunning(ctx context.Context, id string, startedAt time.Time) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		res := r.DB(ctx).Model(&LoadTest{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"state":      LoadTestStateRunning,
				"started_at": startedAt,
				"updated_at": startedAt,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return apierrors.New(apierrors.CodeLoadTestNotFound)
		}
		return nil
	})
}

// Delete removes a run by id; a miss maps to 10308.
func (r *LoadTestRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithinTx(ctx, func(ctx context.Context) error {
		res := r.DB(ctx).Delete(&LoadTest{}, "id = ?", id)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return apierrors.New(apierrors.CodeLoadTestNotFound)
		}
		return nil
	})
}

// DeleteBefore removes terminal runs created before the given time
// (retention cleanup, AD9). Active runs are never deleted.
func (r *LoadTestRepository) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	res := r.DB(ctx).
		Where("created_at < ? AND state IN ?", before, []string{
			LoadTestStateCompleted, LoadTestStateFailed, LoadTestStateStopped,
		}).
		Delete(&LoadTest{})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// ListCompletedForModel returns the most recent completed runs for a
// model (the masked user projection, AD2).
func (r *LoadTestRepository) ListCompletedForModel(ctx context.Context, modelID string, limit int) ([]*LoadTest, error) {
	if limit <= 0 {
		return nil, nil
	}
	var rows []*LoadTest
	err := r.DB(ctx).
		Where("model_id = ? AND state = ?", modelID, LoadTestStateCompleted).
		Order("completed_at DESC").
		Order("id DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
