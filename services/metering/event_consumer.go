package metering

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

// meteringEvent is the token-usage event published by the data-plane
// gateway's Wasm plugin on metering.events (architecture Section 4.5.1).
type meteringEvent struct {
	RequestID      string     `json:"request_id"`
	OrganizationID string     `json:"organization_id"`
	APIKeyID       string     `json:"api_key_id"`
	ModelID        string     `json:"model_id"`
	ServiceID      string     `json:"service_id"`
	Usage          tokenUsage `json:"usage"`
	CompletedAt    int64      `json:"completed_at"`
}

// tokenUsage is the per-part token breakdown carried on the event.
type tokenUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

// EventConsumer subscribes to the metering events subject and writes
// one voucher per event through the shared ingestion handler. It
// implements server.Runner so the framework starts it with the server
// and cancels it on shutdown.
type EventConsumer struct {
	client  mq.Client
	service *Service
	workers int
}

// NewEventConsumer constructs an EventConsumer. workers <= 0 falls back
// to 1 (sequential handling).
func NewEventConsumer(client mq.Client, service *Service, workers int) *EventConsumer {
	if workers <= 0 {
		workers = 1
	}
	return &EventConsumer{client: client, service: service, workers: workers}
}

// NewEventConsumerRunner builds the EventConsumer from the shared
// server components. It returns nil when the consumer is disabled or
// the required components are unavailable, so callers can pass the
// result straight to AddRunner.
func NewEventConsumerRunner(components server.Components) *EventConsumer {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Metering.EventConsumer.Enabled {
		return nil
	}
	if components.MQ() == nil || components.DB() == nil {
		logger.S().Warn("metering: event consumer disabled, mq or db component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("metering: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("metering: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	return NewEventConsumer(client, NewForFVT(db, client), cfg.Metering.EventConsumer.Workers)
}

// Run implements server.Runner: it subscribes to the metering events
// subject and blocks until ctx is cancelled or the subscription fails.
func (c *EventConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().MeteringEvents, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle applies one metering event. JSON decode errors and validation
// failures are logged and permanently skipped (a malformed event never
// becomes well-formed — no retry, FR1.2). Transient database failures
// return the error for broker-level retry (FR1.5).
func (c *EventConsumer) handle(ctx context.Context, msg mq.Message) error {
	var event meteringEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		logger.S().Warnw("metering: malformed event, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}
	voucherID, err := c.service.handleEvent(ctx, &event)
	if err != nil {
		if ae, ok := apierrors.As(err); ok && ae.Code == apierrors.CodeMeteringEventInvalid {
			// Permanently invalid: log and skip, never retry.
			logger.S().Warnw("metering: invalid event, skipping",
				"request_id", event.RequestID, "err", err)
			return nil
		}
		// Transient failure: return the error so the broker retries.
		return err
	}
	logger.S().Infow("metering: event ingested",
		"request_id", event.RequestID, "voucher_id", voucherID)
	return nil
}
