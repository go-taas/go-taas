package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// HealthConsumer subscribes to the runtime.health subject and applies
// each full snapshot to the projection cache (AD4). It implements
// server.Runner so the framework starts it with the server and cancels it
// on shutdown.
type HealthConsumer struct {
	client mq.Client
	cache  *ProjectionCache
}

// NewHealthConsumer builds a HealthConsumer.
func NewHealthConsumer(client mq.Client, cache *ProjectionCache) *HealthConsumer {
	return &HealthConsumer{client: client, cache: cache}
}

// NewHealthConsumerRunner builds the HealthConsumer from the shared
// server components over the caller-provided projection cache. It returns
// nil when the required components are unavailable or the consumer is
// disabled.
func NewHealthConsumerRunner(components server.Components, cache *ProjectionCache, enabled bool) *HealthConsumer {
	if !enabled {
		logger.S().Warn("runtime: health snapshot consumer disabled")
		return nil
	}
	if components == nil {
		return nil
	}
	if components.MQ() == nil {
		logger.S().Warn("runtime: health snapshot consumer disabled, mq component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("runtime: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	return NewHealthConsumer(client, cache)
}

// Run implements server.Runner: it subscribes to the runtime.health
// subject and blocks until ctx is cancelled or the subscription fails.
func (c *HealthConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().RuntimeHealth, func(msg mq.Message) error {
		return c.handle(msg)
	})
}

// handle applies one full snapshot. A malformed snapshot is logged and
// skipped. The cache is replaced wholesale.
func (c *HealthConsumer) handle(msg mq.Message) error {
	var snap HealthSnapshot
	if err := json.Unmarshal(msg.Body, &snap); err != nil {
		logger.S().Warnw("runtime: malformed health snapshot, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}
	c.cache.Replace(&snap)
	logger.S().Infow("runtime: health snapshot applied",
		"scheme", snap.Scheme, "availability", snap.Availability,
		"components", len(snap.Components), "models", len(snap.Models))
	return nil
}
