package routing

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// DecisionRetentionRunner deletes decision-log entries older than the
// configured retention days in batches (feature #49, AD11). Decision
// entries are diagnostic metadata; the runner uses the
// routing.retention enabled/batchSize/interval settings and
// routing.decisionRetentionDays. It implements server.Runner.
type DecisionRetentionRunner struct {
	repo          *DecisionRepository
	retentionDays int
	batchSize     int
	interval      time.Duration
}

// NewDecisionRetentionRunner constructs a DecisionRetentionRunner.
func NewDecisionRetentionRunner(repo *DecisionRepository, retentionDays int, batchSize int, interval time.Duration) *DecisionRetentionRunner {
	if retentionDays <= 0 {
		retentionDays = 7
	}
	if batchSize <= 0 {
		batchSize = 1000
	}
	if interval <= 0 {
		interval = time.Hour
	}
	return &DecisionRetentionRunner{
		repo:          repo,
		retentionDays: retentionDays,
		batchSize:     batchSize,
		interval:      interval,
	}
}

// NewDecisionRetentionRunnerFromComponents builds the runner from the
// shared server components. It returns nil when disabled or the DB
// component is unavailable, so callers can pass the result straight to
// AddRunner.
func NewDecisionRetentionRunnerFromComponents(components server.Components) *DecisionRetentionRunner {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Routing.Retention.Enabled {
		return nil
	}
	if components.DB() == nil {
		logger.S().Warn("routing: decision retention runner disabled, db component unavailable")
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("routing: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	return NewDecisionRetentionRunner(
		NewDecisionRepository(db),
		cfg.Routing.DecisionRetentionDays,
		cfg.Routing.Retention.BatchSize,
		cfg.Routing.Retention.Interval,
	)
}

// Run implements server.Runner: it loops on the retention ticker until
// ctx is cancelled.
func (r *DecisionRetentionRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.RetainOnce(ctx)
		}
	}
}

// RetainOnce runs one retention pass: drain decisions older than the
// cutoff in batches until a pass deletes fewer than batchSize rows.
func (r *DecisionRetentionRunner) RetainOnce(ctx context.Context) {
	cutoff := time.Now().UTC().AddDate(0, 0, -r.retentionDays)
	var total int64
	for {
		deleted, err := r.repo.DeleteBefore(ctx, cutoff, r.batchSize)
		if err != nil {
			logger.S().Warnw("routing: decision retention delete failed", "err", err)
			break
		}
		total += deleted
		if deleted < int64(r.batchSize) {
			break
		}
	}
	if total > 0 {
		logger.S().Infow("routing: decision retention pass complete", "deleted", total)
	}
}
