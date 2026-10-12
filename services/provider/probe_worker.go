package provider

import (
	"context"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
)

// ProbeWorker is the scheduled connectivity probe runner (design FR3.6). It
// probes every enabled provider on the configured interval and persists the
// result. It never disables a provider automatically (the operator
// decides).
type ProbeWorker struct {
	svc      *Service
	interval time.Duration
	workers  int
}

// NewProbeWorker constructs a ProbeWorker over a provider service.
func NewProbeWorker(svc *Service, interval time.Duration, workers int) *ProbeWorker {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if workers <= 0 {
		workers = 1
	}
	return &ProbeWorker{svc: svc, interval: interval, workers: workers}
}

// Run implements server.Runner: it probes once immediately, then on every
// tick until ctx is cancelled.
func (w *ProbeWorker) Run(ctx context.Context) error {
	if w == nil || w.svc == nil {
		return nil
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.probeAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.probeAll(ctx)
		}
	}
}

// probeAll probes every enabled provider with bounded concurrency.
func (w *ProbeWorker) probeAll(ctx context.Context) {
	repo, err := w.svc.providers()
	if err != nil {
		logger.S().Warnw("provider: probe worker cannot resolve repository", "err", err)
		return
	}
	rows, err := repo.ListEnabled(ctx)
	if err != nil {
		logger.S().Warnw("provider: probe worker cannot list providers", "err", err)
		return
	}
	sem := make(chan struct{}, w.workers)
	done := make(chan struct{}, len(rows))
	for _, row := range rows {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		go func(p *Provider) {
			defer func() { <-sem; done <- struct{}{} }()
			w.probeOne(ctx, repo, p)
		}(row)
	}
	for range rows {
		<-done
	}
}

// probeOne probes a single provider and persists the result.
func (w *ProbeWorker) probeOne(ctx context.Context, repo *ProviderRepository, p *Provider) {
	key, err := w.svc.decryptSecret(p.APIKeyCiphertext)
	if err != nil {
		logger.S().Warnw("provider: probe skipped, secret unavailable", "provider_id", p.ID)
		return
	}
	headers, err := w.svc.decryptHeaders(p.ExtraHeaders)
	if err != nil {
		logger.S().Warnw("provider: probe skipped, headers unavailable", "provider_id", p.ID)
		return
	}
	res := w.svc.probeClient.Probe(ctx, p.BaseURL, key, headers, p.RequestTimeoutSeconds)
	if err := repo.UpdateProbe(ctx, p.ID, res); err != nil {
		logger.S().Warnw("provider: probe persist failed", "provider_id", p.ID, "err", err)
		return
	}
	logger.S().Infow("provider: probe completed", "provider_id", p.ID, "status", res.Status)
}
