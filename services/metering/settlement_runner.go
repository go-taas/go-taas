package metering

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// settlementEvent is the event published to billing.settlements after a
// usage record commits (architecture Section 4.5.2). No amounts —
// pricing is feature #5's contract (D6).
type settlementEvent struct {
	UsageRecordID    string `json:"usage_record_id"`
	APIKeyID         string `json:"api_key_id"`
	OrganizationID   string `json:"organization_id"`
	PeriodStart      int64  `json:"period_start"`
	PeriodEnd        int64  `json:"period_end"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	CachedTokens     int64  `json:"cached_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	RequestCount     int64  `json:"request_count"`
	SettledAt        string `json:"settled_at"`
}

// SettlementRunner aggregates unsettled vouchers of complete past hours
// into usage records and publishes settlement events. It implements
// server.Runner so the framework starts it with the server and cancels
// it on shutdown.
type SettlementRunner struct {
	repo        *Repository
	publisher   mq.Client
	interval    time.Duration
	gracePeriod time.Duration
	workers     int
}

// NewSettlementRunner constructs a SettlementRunner. workers <= 0 falls
// back to 1.
func NewSettlementRunner(repo *Repository, publisher mq.Client, interval, gracePeriod time.Duration, workers int) *SettlementRunner {
	if workers <= 0 {
		workers = 1
	}
	if interval <= 0 {
		interval = time.Minute
	}
	return &SettlementRunner{
		repo:        repo,
		publisher:   publisher,
		interval:    interval,
		gracePeriod: gracePeriod,
		workers:     workers,
	}
}

// NewSettlementRunnerRunner builds the SettlementRunner from the shared
// server components. It returns nil when the runner is disabled or the
// required components are unavailable, so callers can pass the result
// straight to AddRunner.
func NewSettlementRunnerRunner(components server.Components) *SettlementRunner {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Metering.Settlement.Enabled {
		return nil
	}
	if components.MQ() == nil || components.DB() == nil {
		logger.S().Warn("metering: settlement runner disabled, mq or db component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("metering: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("metering: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	return NewSettlementRunner(
		NewRepository(db), client,
		cfg.Metering.Settlement.Interval,
		cfg.Metering.Settlement.GracePeriod,
		cfg.Metering.Settlement.Workers,
	)
}

// Run implements server.Runner: it loops on the settlement ticker until
// ctx is cancelled.
func (r *SettlementRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.SettleOnce(ctx); err != nil {
				logger.S().Warnw("metering: settlement pass failed", "err", err)
			}
		}
	}
}

// SettleOnce runs one settlement pass: fetch unsettled vouchers once,
// group them into (api_key_id, hour) buckets in Go, settle each
// eligible bucket (bounded concurrency), then publish settlement events
// for unpublished records. A bucket error is logged and retried on the
// next pass; a publish failure leaves the record unpublished for the
// next pass (FR3.4, AC7).
func (r *SettlementRunner) SettleOnce(ctx context.Context) error {
	now := time.Now().UTC()

	// Fetch unsettled vouchers once; only those completed before the
	// grace cutoff can be in eligible buckets.
	unsettled, err := r.repo.UnsettledVouchers(ctx, now)
	if err != nil {
		return err
	}

	// Group into hour buckets; keep only eligible buckets (the hour
	// closed at least gracePeriod ago, FR3.1/AC6).
	buckets := map[HourBucket]struct{}{}
	for _, v := range unsettled {
		periodStart := v.CompletedAt.Unix() / 3600 * 3600
		if now.Unix() < periodStart+3600+int64(r.gracePeriod.Seconds()) {
			continue // not yet eligible
		}
		buckets[HourBucket{
			APIKeyID:       v.APIKeyID,
			OrganizationID: v.OrganizationID,
			PeriodStart:    periodStart,
		}] = struct{}{}
	}

	// Settle buckets with bounded concurrency.
	if len(buckets) > 0 {
		queue := make(chan HourBucket, len(buckets))
		for b := range buckets {
			queue <- b
		}
		close(queue)
		var wg sync.WaitGroup
		for i := 0; i < r.workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for b := range queue {
					record, err := r.repo.SettleBucket(ctx, b, now)
					if err != nil {
						logger.S().Warnw("metering: bucket settlement failed, will retry next pass",
							"api_key_id", b.APIKeyID, "period_start", b.PeriodStart, "err", err)
						continue
					}
					if record == nil {
						continue
					}
				}
			}()
		}
		wg.Wait()
	}

	// Publish settlement events for unpublished records.
	return r.publishUnpublished(ctx)
}

// publishUnpublished publishes one settlement event per unpublished
// usage record and marks it published on success. A publish failure
// leaves the record unpublished; the next pass re-publishes (FR3.4).
func (r *SettlementRunner) publishUnpublished(ctx context.Context) error {
	records, err := r.repo.UnpublishedRecords(ctx)
	if err != nil {
		return err
	}
	for _, rec := range records {
		body, err := json.Marshal(settlementEvent{
			UsageRecordID:    rec.ID,
			APIKeyID:         rec.APIKeyID,
			OrganizationID:   rec.OrganizationID,
			PeriodStart:      rec.PeriodStart,
			PeriodEnd:        rec.PeriodEnd,
			PromptTokens:     rec.PromptTokens,
			CompletionTokens: rec.CompletionTokens,
			CachedTokens:     rec.CachedTokens,
			ReasoningTokens:  rec.ReasoningTokens,
			RequestCount:     rec.RequestCount,
			SettledAt:        rec.SettledAt.UTC().Format(time.RFC3339),
		})
		if err != nil {
			logger.S().Warnw("metering: settlement event marshal failed", "usage_record_id", rec.ID, "err", err)
			continue
		}
		if err := r.publisher.Publish(ctx, mq.DefaultSubjects().Settlements, body,
			map[string]string{"usage_record_id": rec.ID}); err != nil {
			// Leave unpublished: the next pass re-publishes (AC7).
			logger.S().Warnw("metering: settlement event publish failed, will retry next pass",
				"usage_record_id", rec.ID, "err", err)
			continue
		}
		if err := r.repo.MarkPublished(ctx, rec.ID); err != nil {
			logger.S().Warnw("metering: mark published failed", "usage_record_id", rec.ID, "err", err)
		}
	}
	return nil
}
