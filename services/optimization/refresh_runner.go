package optimization

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// RefreshRunner recomputes the recommendation snapshot on the
// configured refresh interval (feature #50, AD6). It implements
// server.Runner; each tick calls Engine.Run with the ticker trigger. A
// failed pass logs and records the run row — the previous snapshot
// stays served until the next successful pass.
type RefreshRunner struct {
	engine   *Engine
	interval time.Duration
}

// NewRefreshRunner constructs a RefreshRunner over an engine.
func NewRefreshRunner(engine *Engine, interval time.Duration) *RefreshRunner {
	if interval <= 0 {
		interval = time.Hour
	}
	return &RefreshRunner{engine: engine, interval: interval}
}

// NewRefreshRunnerFromComponents builds the runner from the shared
// server components. It returns nil when the DB component is
// unavailable, so callers can pass the result straight to AddRunner.
func NewRefreshRunnerFromComponents(components server.Components) *RefreshRunner {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if cfg.Optimization.RefreshIntervalMinutes <= 0 {
		return nil
	}
	if components.DB() == nil {
		logger.S().Warn("optimization: refresh runner disabled, db component unavailable")
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("optimization: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	return NewRefreshRunner(
		NewEngine(db, cfg.Optimization, nil),
		time.Duration(cfg.Optimization.RefreshIntervalMinutes)*time.Minute,
	)
}

// Run implements server.Runner: it loops on the refresh ticker until
// ctx is cancelled.
func (r *RefreshRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.RefreshOnce(ctx)
		}
	}
}

// RefreshOnce runs one engine pass with the ticker trigger.
func (r *RefreshRunner) RefreshOnce(ctx context.Context) {
	started := time.Now().UTC()
	if _, err := r.engine.Run(ctx, TriggerTicker); err != nil {
		r.engine.RunFailure(ctx, TriggerTicker, started, err)
	}
}
