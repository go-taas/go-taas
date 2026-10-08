package evaluation

//revive:disable:exported

import (
	"context"
	"time"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

type RetentionRunner struct {
	repo     *Repository
	ttl      time.Duration
	interval time.Duration
	clock    func() time.Time
}

func NewRetentionRunner(repo *Repository, ttl, interval time.Duration) *RetentionRunner {
	if ttl <= 0 {
		ttl = 90 * 24 * time.Hour
	}
	if interval <= 0 {
		interval = time.Hour
	}
	return &RetentionRunner{repo: repo, ttl: ttl, interval: interval, clock: func() time.Time { return time.Now().UTC() }}
}

func NewRetentionRunnerFromConfig(_ server.Components, repo *Repository) *RetentionRunner {
	cfg := config.GetConfig()
	return NewRetentionRunner(repo, cfg.Evaluation.Retention.TTL, cfg.Evaluation.Retention.Interval)
}

func (r *RetentionRunner) RunnerName() string { return "evaluation-retention" }

func (r *RetentionRunner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	r.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.RunOnce(ctx)
		}
	}
}

func (r *RetentionRunner) RunOnce(ctx context.Context) {
	if _, err := r.repo.purgeExpired(ctx, r.clock(), 500); err != nil {
		logger.S().Warnw("evaluation: retention purge failed", "err", err)
	}
}

var _ server.Runner = (*RetentionRunner)(nil)
