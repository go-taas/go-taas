package batch

import (
	"context"
	"time"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// BatchRetentionRunner deletes completed jobs' result/error files after
// the retention window and sets files_expired (AD8). It implements
// server.Runner.
type BatchRetentionRunner struct { //nolint:revive // batch.BatchRetentionRunner is the documented domain name
	repo     *Repository
	store    FileStore
	fileTTL  time.Duration
	interval time.Duration
	clock    func() time.Time
}

// NewBatchRetentionRunner constructs a BatchRetentionRunner.
func NewBatchRetentionRunner(repo *Repository, store FileStore, fileTTL, interval time.Duration) *BatchRetentionRunner {
	if fileTTL <= 0 {
		fileTTL = 720 * time.Hour
	}
	if interval <= 0 {
		interval = time.Hour
	}
	return &BatchRetentionRunner{
		repo:     repo,
		store:    store,
		fileTTL:  fileTTL,
		interval: interval,
		clock:    func() time.Time { return time.Now().UTC() },
	}
}

// NewBatchRetentionRunnerFromConfig builds the runner from the shared
// components and config.
func NewBatchRetentionRunnerFromConfig(_ server.Components, repo *Repository, store FileStore) *BatchRetentionRunner {
	fileTTL := 720 * time.Hour
	interval := time.Hour
	if cfg := config.GetConfig(); cfg != nil {
		if cfg.Batch.Retention.FileTTL > 0 {
			fileTTL = cfg.Batch.Retention.FileTTL
		}
		if cfg.Batch.Retention.Interval > 0 {
			interval = cfg.Batch.Retention.Interval
		}
	}
	return NewBatchRetentionRunner(repo, store, fileTTL, interval)
}

// RunnerName implements server.Runner.
func (r *BatchRetentionRunner) RunnerName() string { return "batch-retention-runner" }

// Run implements server.Runner.
func (r *BatchRetentionRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.RetainOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.RetainOnce(ctx)
		}
	}
}

// RetainOnce deletes expired result/error files. It is extracted for
// tests.
func (r *BatchRetentionRunner) RetainOnce(ctx context.Context) {
	cutoff := r.clock().Add(-r.fileTTL)
	jobs, err := r.repo.FindExpiredFiles(ctx, cutoff, 1000)
	if err != nil {
		logger.S().Warnw("batch: find expired files failed", "err", err)
		return
	}
	for _, job := range jobs {
		if err := r.store.DeleteFiles(ctx, job.OrganizationID, job.BatchID); err != nil {
			logger.S().Warnw("batch: delete expired files failed", "batch_id", job.BatchID, "err", err)
			continue
		}
		if err := r.repo.MarkFilesExpired(ctx, job.BatchID); err != nil {
			logger.S().Warnw("batch: mark files expired failed", "batch_id", job.BatchID, "err", err)
		}
	}
}
