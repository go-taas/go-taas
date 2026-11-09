package infer

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/model"
)

// servingConvergerLimit caps how many running services one pass reconciles.
// The list is newest-first, so a larger fleet still converges over several
// passes instead of holding the runner for a long time.
const servingConvergerLimit = 200

// ServingConverger periodically asks the controller to re-apply the serving
// route of every running service.
//
// A route is named after the served model, and the controller can only write
// it when it handles a change event. Nothing re-applies it afterwards, so a
// route deleted out of band — by a manual cleanup, by another service of the
// same model being torn down, or by a failed write — leaves a running service
// that looks healthy in the console and answers 503 at the gateway
// (`httproutes.gateway.networking.k8s.io "<model>-router" not found`) until an
// operator happens to scale or update it.
//
// This runner is the missing heartbeat: it publishes a route-only
// reconciliation event per running service, which the controller answers by
// re-writing the route (and nothing else). It is cheap — one event per service
// per interval, no pod churn — and idempotent, because the route write is an
// apply.
//
// It implements server.Runner.
type ServingConverger struct {
	repo     *InferenceServiceRepository
	models   *model.Repository
	pub      mq.Client
	interval time.Duration
}

// NewServingConverger builds the converger over the service repository, the
// model repository (for the served model name) and the MQ client.
func NewServingConverger(repo *InferenceServiceRepository, models *model.Repository, pub mq.Client, interval time.Duration) *ServingConverger {
	if interval <= 0 {
		interval = time.Minute
	}
	return &ServingConverger{repo: repo, models: models, pub: pub, interval: interval}
}

// NewServingConvergerFromConfig builds the converger from the shared
// components when the feature is enabled. It returns nil when disabled or when
// the required components are unavailable, so callers can pass the result
// straight to AddRunner.
func NewServingConvergerFromConfig(components server.Components) *ServingConverger {
	if components == nil || components.DB() == nil || components.MQ() == nil {
		return nil
	}
	cfg := config.GetConfig()
	if cfg == nil || !cfg.Infer.TeardownConverger.Enabled {
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		return nil
	}
	pub, ok := components.MQ().Client().(mq.Client)
	if !ok {
		return nil
	}
	// The two convergers share one cadence: both answer "is the cluster still
	// what the catalog says it is", and an operator tuning one interval means
	// the other too.
	interval := time.Minute
	if cfg.Infer.TeardownConverger.Interval > 0 {
		interval = cfg.Infer.TeardownConverger.Interval
	}
	return NewServingConverger(NewInferenceServiceRepository(db), model.NewRepository(db), pub, interval)
}

// RunnerName implements server.Runner.
func (c *ServingConverger) RunnerName() string { return "infer-serving-converger" }

// Run ticks until ctx is cancelled.
func (c *ServingConverger) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	c.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.runOnce(ctx)
		}
	}
}

// runOnce publishes one route reconciliation per running service. Publish
// errors are logged; the next tick retries.
func (c *ServingConverger) runOnce(ctx context.Context) {
	if c.repo == nil || c.pub == nil {
		return
	}
	rows, err := c.repo.ListRunning(ctx, servingConvergerLimit)
	if err != nil {
		logger.S().Warnw("infer: serving converger query failed", "err", err)
		return
	}
	for _, row := range rows {
		name := servedModelName(ctx, c.models, row)
		evt := buildChangeEvent(EventTypeReconcileRoute, row, name, "", "", "")
		if err := publishChange(ctx, c.pub, evt); err != nil {
			logger.S().Warnw("infer: serving converger publish failed",
				"service_id", row.ID, "err", err)
		}
	}
}
