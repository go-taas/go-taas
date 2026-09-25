package audit

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// AuditRetentionRunner deletes audit events older than the configured
// TTL in batches (feature #15, AD6). It is a separate runner from the
// request-log cleanup and never touches request logs, vouchers, usage
// records, or charge records (FR2.2). It implements server.Runner.
type AuditRetentionRunner struct { //nolint:revive // audit.AuditRetentionRunner is the domain name
	repo      *Repository
	eventTTL  time.Duration
	batchSize int
	interval  time.Duration
}

// NewAuditRetentionRunner constructs an AuditRetentionRunner.
func NewAuditRetentionRunner(repo *Repository, eventTTL time.Duration, batchSize int, interval time.Duration) *AuditRetentionRunner {
	if batchSize <= 0 {
		batchSize = 1000
	}
	if interval <= 0 {
		interval = time.Hour
	}
	return &AuditRetentionRunner{
		repo:      repo,
		eventTTL:  eventTTL,
		batchSize: batchSize,
		interval:  interval,
	}
}

// NewAuditRetentionRunnerRunner builds the runner from the shared
// server components. It returns nil when disabled or the DB component
// is unavailable, so callers can pass the result straight to AddRunner.
func NewAuditRetentionRunnerRunner(components server.Components) *AuditRetentionRunner {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Audit.Retention.Enabled {
		return nil
	}
	if components.DB() == nil {
		logger.S().Warn("audit: retention runner disabled, db component unavailable")
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("audit: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	return NewAuditRetentionRunner(
		NewRepository(db),
		cfg.Audit.Retention.EventTTL,
		cfg.Audit.Retention.BatchSize,
		cfg.Audit.Retention.Interval,
	)
}

// Run implements server.Runner: it loops on the retention ticker until
// ctx is cancelled.
func (r *AuditRetentionRunner) Run(ctx context.Context) error {
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

// RetainOnce runs one retention pass: drain audit events older than the
// TTL in batches until a pass deletes fewer than batchSize rows.
func (r *AuditRetentionRunner) RetainOnce(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-r.eventTTL)
	var total int64
	for {
		deleted, err := r.repo.DeleteAuditEventsBefore(ctx, cutoff, r.batchSize)
		if err != nil {
			logger.S().Warnw("audit: retention delete failed", "err", err)
			break
		}
		total += deleted
		if deleted < int64(r.batchSize) {
			break
		}
	}
	if total > 0 {
		logger.S().Infow("audit: retention pass complete", "deleted", total)
	}
}
