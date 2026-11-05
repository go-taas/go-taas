package billing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// ReconciliationRunner is the charging safety net: it periodically
// scans uncharged hour buckets and prices them through PriceOnce, so
// charges converge even when settlement events are lost (D7, FR4.5,
// AC11). It implements server.Runner.
type ReconciliationRunner struct {
	repo        *Repository
	service     *Service
	interval    time.Duration
	gracePeriod time.Duration
	workers     int
}

// NewReconciliationRunner constructs a ReconciliationRunner. workers <=
// 0 falls back to 1; interval <= 0 falls back to a minute.
func NewReconciliationRunner(repo *Repository, service *Service, interval, gracePeriod time.Duration, workers int) *ReconciliationRunner {
	if workers <= 0 {
		workers = 1
	}
	if interval <= 0 {
		interval = time.Minute
	}
	return &ReconciliationRunner{
		repo:        repo,
		service:     service,
		interval:    interval,
		gracePeriod: gracePeriod,
		workers:     workers,
	}
}

// NewReconciliationRunnerRunner builds the ReconciliationRunner from
// the shared server components. It returns nil when the runner is
// disabled or the required components are unavailable.
func NewReconciliationRunnerRunner(components server.Components) *ReconciliationRunner {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Billing.Reconciliation.Enabled {
		return nil
	}
	if components.DB() == nil {
		logger.S().Warn("billing: reconciliation runner disabled, db component unavailable")
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("billing: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	repo := NewRepository(db)
	return NewReconciliationRunner(
		repo, NewForFVT(db, nil),
		cfg.Billing.Reconciliation.Interval,
		cfg.Billing.Reconciliation.GracePeriod,
		cfg.Billing.Reconciliation.Workers,
	)
}

// Run implements server.Runner: it loops on the reconciliation ticker
// until ctx is cancelled.
func (r *ReconciliationRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.ReconcileOnce(ctx); err != nil {
				logger.S().Warnw("billing: reconciliation pass failed", "err", err)
			}
		}
	}
}

// staleAccrualAfter is how long past an hour's close an accrual may linger
// before it is released. Charging retires it within one grace period, so this
// is a wide margin: reaching it means the charge path is broken, and the gate
// must not keep subtracting a figure that nothing will retire.
const staleAccrualAfter = 2 * time.Hour

// ReconcileOnce runs one reconciliation pass: fetch the uncharged hour
// buckets whose hours closed at least gracePeriod ago, and price each
// bucket with bounded concurrency. A bucket error is logged and
// retried on the next pass (FR4.5, AC11).
func (r *ReconciliationRunner) ReconcileOnce(ctx context.Context) error {
	now := time.Now().UTC()
	// Release accruals that outlived their hour by a wide margin first: this
	// is the safety valve for a charge path that cannot converge, and it keeps
	// the gate from counting the same usage forever.
	r.releaseStaleAccrual(ctx, now)
	// Only lines completed before the eligibility cutoff can be in
	// eligible buckets: hour closed + grace.
	eligibleBefore := now.Add(-r.gracePeriod)
	buckets, err := r.repo.UnchargedHourBuckets(ctx, eligibleBefore)
	if err != nil {
		return err
	}

	// Keep only eligible buckets (the hour closed at least gracePeriod
	// ago — the same rule as metering settlement, FR4.1/AC11).
	eligible := make([]HourBucket, 0, len(buckets))
	for _, b := range buckets {
		if now.Unix() < b.PeriodStart+3600+int64(r.gracePeriod.Seconds()) {
			continue
		}
		eligible = append(eligible, b)
	}
	if len(eligible) == 0 {
		return nil
	}

	queue := make(chan HourBucket, len(eligible))
	for _, b := range eligible {
		queue <- b
	}
	close(queue)
	var wg sync.WaitGroup
	for i := 0; i < r.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range queue {
				if err := r.service.PriceOnce(ctx, b.APIKeyID, b.PeriodStart); err != nil {
					logger.S().Warnw("billing: bucket pricing failed, will retry next pass",
						"api_key_id", b.APIKeyID, "period_start", b.PeriodStart, "err", err)
				}
			}
		}()
	}
	wg.Wait()
	return nil
}

// releaseStaleAccrual applies the stale-accrual safety valve. It never fails
// the pass: the money question is bounded (a released accrual is still billed
// when charging catches up), so a read error is logged rather than retried.
func (r *ReconciliationRunner) releaseStaleAccrual(ctx context.Context, now time.Time) {
	cutoff := now.Add(-staleAccrualAfter)
	rows, micros, err := r.repo.ReleaseStaleAccrual(ctx, cutoff)
	if err != nil {
		logger.S().Warnw("billing: stale accrual release failed", "err", err)
		return
	}
	if rows > 0 {
		logger.S().Warnw("billing: released stale in-flight accrual; charging is falling behind",
			"rows", rows, "micros", micros, "completed_before", cutoff)
	}
}
