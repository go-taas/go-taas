package runtime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// SchemeBroadcaster publishes the persisted serving scheme to the
// runtime.scheme subject on startup, on every change (via Notify), and on
// a fixed interval so a restarted DB-less controller reconverges (AD6).
type SchemeBroadcaster struct {
	repo     *RuntimeRepository
	client   mq.Client
	interval time.Duration
	revision atomic.Int64
	// notify triggers an immediate publish on a scheme change.
	notify chan struct{}
}

// NewSchemeBroadcaster builds a SchemeBroadcaster.
func NewSchemeBroadcaster(repo *RuntimeRepository, client mq.Client, interval time.Duration) *SchemeBroadcaster {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return &SchemeBroadcaster{
		repo:     repo,
		client:   client,
		interval: interval,
		notify:   make(chan struct{}, 1),
	}
}

// Notify requests an immediate scheme broadcast. It never blocks.
func (b *SchemeBroadcaster) Notify() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

// Run implements server.Runner: publish once immediately, then on every
// Notify and the configured interval until ctx is cancelled.
func (b *SchemeBroadcaster) Run(ctx context.Context) error {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	b.publish(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			b.publish(ctx)
		case <-b.notify:
			b.publish(ctx)
		}
	}
}

// publish reads the persisted scheme and publishes it. Errors are logged;
// the loop retries on the next tick.
func (b *SchemeBroadcaster) publish(ctx context.Context) {
	setting, err := b.repo.GetScheme(ctx)
	if err != nil {
		logger.S().Warnw("runtime: read scheme for broadcast failed", "err", err)
		return
	}
	msg := SchemeMessage{Scheme: setting.Scheme, Revision: b.revision.Add(1)}
	if !setting.UpdatedAt.IsZero() {
		msg.UpdatedAt = setting.UpdatedAt.UTC().Format(time.RFC3339)
	}
	msg.UpdatedBy = setting.UpdatedBy
	body, err := json.Marshal(msg)
	if err != nil {
		logger.S().Warnw("runtime: marshal scheme broadcast failed", "err", err)
		return
	}
	if err := b.client.Publish(ctx, mq.DefaultSubjects().RuntimeScheme, body, nil); err != nil {
		logger.S().Warnw("runtime: publish scheme broadcast failed", "err", err)
	}
}

// NewSchemeBroadcasterRunner builds the SchemeBroadcaster from the shared
// server components. It returns nil when the required components are
// unavailable.
func NewSchemeBroadcasterRunner(components server.Components, repo *RuntimeRepository, interval time.Duration) *SchemeBroadcaster {
	if components == nil || repo == nil {
		return nil
	}
	if components.MQ() == nil {
		logger.S().Warn("runtime: scheme broadcaster disabled, mq component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		return nil
	}
	return NewSchemeBroadcaster(repo, client, interval)
}
