package infer

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// concurrencyReport is the gateway-reported in-flight concurrency for a
// service (feature #16, §6.3).
type concurrencyReport struct {
	ServiceID  string `json:"service_id"`
	InFlight   int    `json:"in_flight"`
	ReportedAt string `json:"reported_at"`
}

// ConcurrencyConsumer subscribes to the concurrency-metric subject and
// updates the service's current-concurrency projection (used by
// GetInferenceService). It implements server.Runner so the framework
// starts it with the server and cancels it on shutdown.
type ConcurrencyConsumer struct {
	client  mq.Client
	repo    *InferenceServiceRepository
	workers int
}

// NewConcurrencyConsumer constructs a ConcurrencyConsumer. workers <= 0
// falls back to 1 (sequential handling).
func NewConcurrencyConsumer(client mq.Client, repo *InferenceServiceRepository, workers int) *ConcurrencyConsumer {
	if workers <= 0 {
		workers = 1
	}
	return &ConcurrencyConsumer{client: client, repo: repo, workers: workers}
}

// NewConcurrencyConsumerRunner builds the ConcurrencyConsumer from the
// shared server components. It returns nil when the consumer is disabled
// or the required components are unavailable, so callers can pass the
// result straight to AddRunner.
func NewConcurrencyConsumerRunner(components server.Components) *ConcurrencyConsumer {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Infer.Autoscaling.ConcurrencyConsumer.Enabled {
		return nil
	}
	if components.MQ() == nil || components.DB() == nil {
		logger.S().Warn("infer: concurrency consumer disabled, mq or db component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("infer: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("infer: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	repo := NewInferenceServiceRepository(db)
	return NewConcurrencyConsumer(client, repo, cfg.Infer.Autoscaling.ConcurrencyConsumer.Workers)
}

// Run implements server.Runner: it subscribes to the concurrency subject
// and blocks until ctx is cancelled or the subscription fails.
func (c *ConcurrencyConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().InferServiceConcurrency, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle applies one concurrency report. Parse errors are logged and
// skipped; unknown service ids are logged and skipped (a deleted
// service's late report).
func (c *ConcurrencyConsumer) handle(ctx context.Context, msg mq.Message) error {
	var report concurrencyReport
	if err := json.Unmarshal(msg.Body, &report); err != nil {
		logger.S().Warnw("infer: malformed concurrency report, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}
	if report.ServiceID == "" {
		logger.S().Warnw("infer: concurrency report missing service_id, skipping",
			"subject", msg.Subject)
		return nil
	}
	if err := c.repo.ApplyConcurrency(ctx, report.ServiceID, report.InFlight); err != nil {
		if ae, ok := apierrors.As(err); ok && ae.Code == apierrors.CodeInferServiceNotFound {
			logger.S().Infow("infer: concurrency report for unknown service, skipping",
				"service_id", report.ServiceID)
			return nil
		}
		return err
	}
	return nil
}