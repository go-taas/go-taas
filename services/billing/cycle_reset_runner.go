package billing

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// CycleResetRunner is the postpaid monthly cycle reset (AD6): a ticker
// that periodically applies the condition-based guarded UPDATE —
// zeroing used_this_cycle_cents of postpaid accounts whose cycle
// started before the current UTC month start. It implements
// server.Runner.
type CycleResetRunner struct {
	repo     *AccountRepository
	interval time.Duration
}

// NewCycleResetRunner constructs a CycleResetRunner. interval <= 0
// falls back to a minute.
func NewCycleResetRunner(repo *AccountRepository, interval time.Duration) *CycleResetRunner {
	if interval <= 0 {
		interval = time.Minute
	}
	return &CycleResetRunner{repo: repo, interval: interval}
}

// NewCycleResetRunnerRunner builds the CycleResetRunner from the
// shared server components. It returns nil when the runner is disabled
// or the required components are unavailable.
func NewCycleResetRunnerRunner(components server.Components) *CycleResetRunner {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Billing.CycleReset.Enabled {
		return nil
	}
	if components.DB() == nil {
		logger.S().Warn("billing: cycle reset runner disabled, db component unavailable")
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Warnw("billing: cycle reset runner disabled, unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	return NewCycleResetRunner(NewAccountRepository(db), cfg.Billing.CycleReset.Interval)
}

// Run implements server.Runner: it loops on the reset ticker until ctx
// is cancelled.
func (r *CycleResetRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.ResetOnce(ctx); err != nil {
				logger.S().Warnw("billing: cycle reset pass failed", "err", err)
			}
		}
	}
}

// ResetOnce runs one reset pass: the guarded UPDATE for the current
// UTC month start. Zero rows affected is the idempotent no-op (AC8).
func (r *CycleResetRunner) ResetOnce(ctx context.Context) error {
	monthStart := monthStartOf(time.Now().UTC())
	count, err := r.repo.ResetCycle(ctx, monthStart)
	if err != nil {
		return err
	}
	if count > 0 {
		logger.S().Infow("billing: postpaid cycle reset", "accounts", count, "cycle_start", monthStart)
	}
	return nil
}
