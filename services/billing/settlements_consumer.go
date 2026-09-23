package billing

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// settlementEvent is the billing.settlements payload (feature #4's
// contract, consumed verbatim — Section 4.5.2). The event names the
// (api_key_id, period) to price; the usage lines are the token source.
type settlementEvent struct {
	UsageRecordID string `json:"usage_record_id"`
	APIKeyID      string `json:"api_key_id"`
	OrganizationID string `json:"organization_id"`
	PeriodStart   int64  `json:"period_start"`
	PeriodEnd     int64  `json:"period_end"`
}

// SettlementsConsumer subscribes to the billing.settlements subject and
// charges each settled key-hour through PriceOnce (D7). It implements
// server.Runner so the framework starts it with the server and cancels
// it on shutdown.
type SettlementsConsumer struct {
	client  mq.Client
	service *Service
	workers int
}

// NewSettlementsConsumer constructs a SettlementsConsumer. workers <= 0
// falls back to 1.
func NewSettlementsConsumer(client mq.Client, service *Service, workers int) *SettlementsConsumer {
	if workers <= 0 {
		workers = 1
	}
	return &SettlementsConsumer{client: client, service: service, workers: workers}
}

// NewSettlementsConsumerRunner builds the SettlementsConsumer from the
// shared server components. It returns nil when the consumer is
// disabled or the required components are unavailable.
func NewSettlementsConsumerRunner(components server.Components) *SettlementsConsumer {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Billing.SettlementsConsumer.Enabled {
		return nil
	}
	if components.MQ() == nil || components.DB() == nil {
		logger.S().Warn("billing: settlements consumer disabled, mq or db component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("billing: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	raw := components.DB().GormDB()
	db, ok := raw.(*gorm.DB)
	if !ok {
		logger.S().Fatalw("billing: unexpected db handle type",
			"type", fmt.Sprintf("%T", raw))
		return nil
	}
	return NewSettlementsConsumer(client, NewForFVT(db, client), cfg.Billing.SettlementsConsumer.Workers)
}

// Run implements server.Runner: it subscribes to the settlements
// subject and blocks until ctx is cancelled or the subscription fails.
func (c *SettlementsConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().Settlements, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle applies one settlement event: PriceOnce on the event's period
// (not the current time — D7). A redelivered event is absorbed by the
// charge unique index (AC10); malformed events are permanently skipped.
func (c *SettlementsConsumer) handle(ctx context.Context, msg mq.Message) error {
	var event settlementEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		logger.S().Warnw("billing: malformed settlement event, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}
	if event.APIKeyID == "" || event.PeriodStart <= 0 {
		logger.S().Warnw("billing: invalid settlement event, skipping",
			"usage_record_id", event.UsageRecordID)
		return nil
	}
	if err := c.service.PriceOnce(ctx, event.APIKeyID, event.PeriodStart); err != nil {
		// Transient failure: return for broker-level retry.
		return err
	}
	return nil
}
