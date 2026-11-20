package infer

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/pkg/storage"
	"github.com/go-taas/go-taas/services/model"
)

// servingConvergerLimit caps how many services one pass reconciles.
// The list is newest-first, so a larger fleet still converges over several
// passes instead of holding the runner for a long time.
const servingConvergerLimit = 200

// servingRetryAfter is how long a service that is not running waits before the
// converger re-states it. A failure the controller reports is a snapshot, not a
// verdict: the engine may come up a moment later (a slow weights load, a crash
// loop that has since recovered), and nothing else re-states the object. The
// wait keeps a service that genuinely cannot start from being re-applied on
// every tick.
const servingRetryAfter = 5 * time.Minute

// ServingConverger periodically re-states the cluster object of every service
// that should be serving: the running ones, and the ones that are not running
// yet and have not been re-stated since before the retry window (a settled
// failure, or a change whose event never reached the controller).
//
// A service's cluster state — its LLMInferenceService and the workload, route
// and inference pool KServe derives from it — is written when the controller
// handles a change event, and nothing re-applies it afterwards. An object
// removed out of band (a manual cleanup, a namespace wipe, a failed write)
// otherwise leaves a service that looks healthy in the console but serves
// nothing until an operator happens to scale or update it, and a failure the
// controller reported stays on the row even after the engine recovers.
//
// This runner is that heartbeat: it publishes the service's complete desired
// state once per interval, and the controller answers it with an apply. The
// event carries everything a change event carries — the engine image, the
// engine's arguments and the model's weights — because the controller
// rebuilds the whole object from it. A partial event would make the apply
// write an incomplete object, so it must never be sent: the object is not a
// merge of what happens to be in the message.
//
// It is idempotent: an apply of the state the object already holds changes
// nothing, so no pod is recreated and no model is reloaded.
//
// It implements server.Runner.
type ServingConverger struct {
	repo     *InferenceServiceRepository
	models   *model.Repository
	pub      mq.Client
	storage  storage.Config
	interval time.Duration
}

// NewServingConverger builds the converger over the service repository, the
// model repository (for the served model name) and the MQ client.
func NewServingConverger(repo *InferenceServiceRepository, models *model.Repository, pub mq.Client, cfg storage.Config, interval time.Duration) *ServingConverger {
	if interval <= 0 {
		interval = time.Minute
	}
	return &ServingConverger{repo: repo, models: models, pub: pub, storage: cfg, interval: interval}
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
	return NewServingConverger(NewInferenceServiceRepository(db), model.NewRepository(db), pub, cfg.Storage, interval)
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

// runOnce re-states every running service once. Publish errors are logged;
// the next tick retries.
func (c *ServingConverger) runOnce(ctx context.Context) {
	if c.repo == nil || c.pub == nil {
		return
	}
	rows, err := c.repo.ListNeedingConvergence(ctx, servingConvergerLimit, time.Now().UTC().Add(-servingRetryAfter))
	if err != nil {
		logger.S().Warnw("infer: serving converger query failed", "err", err)
		return
	}
	for _, row := range rows {
		evt, err := c.desiredState(ctx, row)
		if err != nil {
			logger.S().Warnw("infer: serving converger resolve failed",
				"service_id", row.ID, "err", err)
			continue
		}
		if err := publishChange(ctx, c.pub, evt); err != nil {
			logger.S().Warnw("infer: serving converger publish failed",
				"service_id", row.ID, "err", err)
		}
	}
}

// desiredState is the service's complete desired state, rebuilt from the
// catalog. The controller composes the cluster object from the event alone, so
// it carries every resolved part: the model's weights, the engine image and
// the engine's arguments.
func (c *ServingConverger) desiredState(ctx context.Context, row *InferenceService) (changeEvent, error) {
	return buildServiceEvent(ctx, c.models, c.storage, row, EventTypeReconcileRoute)
}
