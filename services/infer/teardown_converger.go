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

// teardownConvergerWindow bounds how far back the converger looks for
// terminated/stopped services to re-teardown. Older rows are left alone:
// re-publishing them forever would be churn with no benefit, and a leak
// older than the window is an operational cleanup.
const teardownConvergerWindow = 24 * time.Hour

// TeardownConverger periodically re-publishes the teardown event for
// services in a terminal/stopped state. It guarantees convergence: if the
// original delete/stop event was lost (a broker or controller restart), or
// the controller failed partway, the resources are still removed on the
// next pass. The event handling is idempotent, so re-publishing is safe.
//
// It implements server.Runner.
type TeardownConverger struct {
	repo     *InferenceServiceRepository
	models   *model.Repository
	pub      mq.Client
	interval time.Duration
	window   time.Duration
}

// NewTeardownConverger builds the converger over a repository, the model
// repository (the served model name decides which HTTPRoute the teardown
// targets) and the MQ client.
func NewTeardownConverger(repo *InferenceServiceRepository, models *model.Repository, pub mq.Client, interval time.Duration) *TeardownConverger {
	if interval <= 0 {
		interval = time.Minute
	}
	return &TeardownConverger{repo: repo, models: models, pub: pub, interval: interval, window: teardownConvergerWindow}
}

// NewTeardownConvergerFromConfig builds the converger from the shared
// components when the feature is enabled. It returns nil when disabled or
// when the required components are unavailable, so callers can pass the
// result straight to AddRunner.
func NewTeardownConvergerFromConfig(components server.Components) *TeardownConverger {
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
	interval := time.Minute
	if cfg.Infer.TeardownConverger.Interval > 0 {
		interval = cfg.Infer.TeardownConverger.Interval
	}
	return NewTeardownConverger(NewInferenceServiceRepository(db), model.NewRepository(db), pub, interval)
}

// RunnerName implements server.Runner.
func (c *TeardownConverger) RunnerName() string { return "infer-teardown-converger" }

// Run ticks until ctx is cancelled.
func (c *TeardownConverger) Run(ctx context.Context) error {
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

// runOnce re-publishes a teardown event for every candidate. Published
// errors are logged; the next tick retries.
func (c *TeardownConverger) runOnce(ctx context.Context) {
	if c.repo == nil || c.pub == nil {
		return
	}
	since := time.Now().UTC().Add(-c.window)
	rows, err := c.repo.ListTeardownCandidates(ctx, []string{StateTerminated, StateStopped}, since, 200)
	if err != nil {
		logger.S().Warnw("infer: teardown converger query failed", "err", err)
		return
	}
	for _, row := range rows {
		eventType := EventTypeDelete
		if row.State == StateStopped {
			eventType = EventTypeStop
		}
		modelName := servedModelName(ctx, c.models, row)
		// Re-point the model route at a running sibling first: the route is
		// named after the model, so the replay below would otherwise remove
		// it from under a service that is still serving that model.
		publishRouteReconciliations(ctx, c.pub, c.repo, c.models, modelName, row.ID)
		evt := buildChangeEvent(eventType, row, modelName, "", "", "")
		if err := publishChange(ctx, c.pub, evt); err != nil {
			logger.S().Warnw("infer: teardown converger publish failed",
				"service_id", row.ID, "event", eventType, "err", err)
		}
	}
}
