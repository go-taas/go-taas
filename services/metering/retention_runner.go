package metering

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// RetentionRunner deletes settled vouchers older than the configured
// TTL in batches. Usage records are never deleted (they are the billing
// evidence, D7/FR5.2); unsettled vouchers are kept and warned about
// (FR5.3). It implements server.Runner.
type RetentionRunner struct {
	repo       *Repository
	voucherTTL time.Duration
	batchSize  int
	interval   time.Duration
}

// NewRetentionRunner constructs a RetentionRunner. workers are not
// needed: the pass is a single drain loop.
func NewRetentionRunner(repo *Repository, voucherTTL time.Duration, batchSize int, interval time.Duration) *RetentionRunner {
	if batchSize <= 0 {
		batchSize = 1000
	}
	if interval <= 0 {
		interval = time.Hour
	}
	return &RetentionRunner{
		repo:       repo,
		voucherTTL: voucherTTL,
		batchSize:  batchSize,
		interval:   interval,
	}
}

// NewRetentionRunnerRunner builds the RetentionRunner from the shared
// server components. It returns nil when the runner is disabled or the
// DB component is unavailable, so callers can pass the result straight
// to AddRunner.
func NewRetentionRunnerRunner(components server.Components) *RetentionRunner {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Metering.Retention.Enabled {
		return nil
	}
	if components.DB() == nil {
		logger.S().Warn("metering: retention runner disabled, db component unavailable")
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("metering: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	return NewRetentionRunner(
		NewRepository(db),
		cfg.Metering.Retention.VoucherTTL,
		cfg.Metering.Retention.BatchSize,
		cfg.Metering.Retention.Interval,
	)
}

// Run implements server.Runner: it loops on the retention ticker until
// ctx is cancelled.
func (r *RetentionRunner) Run(ctx context.Context) error {
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

// RetainOnce runs one retention pass: drain settled vouchers older than
// the TTL in batches until a pass deletes fewer than batchSize rows,
// then warn about unsettled vouchers older than the TTL (kept, FR5.3).
func (r *RetentionRunner) RetainOnce(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-r.voucherTTL)
	var total int64
	for {
		deleted, err := r.repo.DeleteSettledBefore(ctx, cutoff, r.batchSize)
		if err != nil {
			logger.S().Warnw("metering: retention delete failed", "err", err)
			break
		}
		total += deleted
		if deleted < int64(r.batchSize) {
			break
		}
	}
	if total > 0 {
		logger.S().Infow("metering: retention pass complete", "deleted", total)
	}

	if unsettled, err := r.repo.CountUnsettledOlderThan(ctx, cutoff); err != nil {
		logger.S().Warnw("metering: unsettled count failed", "err", err)
	} else if unsettled > 0 {
		logger.S().Warnw("metering: unsettled vouchers older than retention TTL kept",
			"count", unsettled, "ttl", r.voucherTTL.String())
	}
}
