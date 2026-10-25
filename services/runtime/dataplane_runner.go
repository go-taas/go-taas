package runtime

import (
	"context"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
)

// defaultDataPlaneInterval is how often the runner re-publishes the route.
// It is a resync rather than a reaction: the route is written on every
// settings save, so the loop only repairs drift (a route deleted by hand, or
// a fresh install whose first reconcile lost a race with the API server).
const defaultDataPlaneInterval = 5 * time.Minute

// DataPlaneRunner keeps the data-plane route in step with the saved
// hostname. It implements server.Runner.
type DataPlaneRunner struct {
	svc      *Service
	interval time.Duration
}

// NewDataPlaneRunner builds the runner. A zero interval uses the default.
func NewDataPlaneRunner(svc *Service, interval time.Duration) *DataPlaneRunner {
	if interval <= 0 {
		interval = defaultDataPlaneInterval
	}
	return &DataPlaneRunner{svc: svc, interval: interval}
}

// Run reconciles once immediately, then on the interval until ctx ends.
// Failures are logged and retried: a route the platform cannot publish must
// not take the server down, because the control plane still works without it.
func (r *DataPlaneRunner) Run(ctx context.Context) error {
	if r == nil || r.svc == nil {
		return nil
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if err := r.svc.EnsureDataPlane(ctx); err != nil && ctx.Err() == nil {
			logger.S().Warnw("runtime: data-plane route reconcile failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
