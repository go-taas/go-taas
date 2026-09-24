package nano

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
)

// settlementEvent is the billing.settlements payload (feature #4's
// contract, consumed verbatim — Section 4.5.2). It mirrors the events
// produced by the metering settlement runner and consumed by the
// existing billing SettlementsConsumer. The Nano leg is an ADDITIVE
// settlement consumer on the same subject (issue #7): it settles each
// key-hour at its exact amount on the feeless rail, while the existing
// consumer continues to price and (optionally) deduct from the prepaid
// balance. No metering, usage-line, price-matrix or balance path is
// modified.
type settlementEvent struct {
	UsageRecordID    string `json:"usage_record_id"`
	APIKeyID         string `json:"api_key_id"`
	OrganizationID   string `json:"organization_id"`
	PeriodStart      int64  `json:"period_start"`
	PeriodEnd        int64  `json:"period_end"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
	CachedTokens     int64  `json:"cached_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	RequestCount     int64  `json:"request_count"`
	SettledAt        string `json:"settled_at"`
}

// NanoSettlementsConsumer subscribes to the billing.settlements
// subject and settles each metered key-hour on the feeless Nano rail
// at its exact amount (issue #7). It is purely additive: it runs
// beside the existing SettlementsConsumer and never mutates charging,
// the price matrix, metering or balances. It implements
// server.Runner so the framework starts it with the server and cancels
// it on shutdown.
//
// This is the draft shape agreed with the maintainer on issue #7
// ("open a draft PR early so we can review the shape"): a new additive
// consumer behind billing.settlements, an exact 30-decimal amount
// boundary (amount.go), and its unit tests. The amount is resolved
// from the billed charge (amountForPeriod) and handed to the rail as
// an exact raw string; wiring a live node submit is the agreed
// follow-on once the shape is confirmed.
type NanoSettlementsConsumer struct {
	client  mq.Client
	workers int
	// rateXNOperUSD is the USD→XNO settlement rate (XNO per USD unit),
	// injected at construction. Empty defaults to "1" (1:1 boundary).
	rateXNOperUSD string
	// rawer is the exact-amount constructor; injected for tests. When
	// nil the package-level XNORawFromUSD_CentsFloat64 is used.
	rawer func(cents float64) string
	// resolveAmount resolves the priced amount in cents for a
	// (api_key_id, hour); injected for tests. When nil the default
	// amountForPeriod is used.
	resolveAmount func(ctx context.Context, apiKeyID string, periodStart int64) (float64, error)
}

// NewNanoSettlementsConsumer constructs a NanoSettlementsConsumer.
// workers <= 0 falls back to 1.
func NewNanoSettlementsConsumer(client mq.Client, workers int) *NanoSettlementsConsumer {
	if workers <= 0 {
		workers = 1
	}
	return &NanoSettlementsConsumer{client: client, workers: workers, rateXNOperUSD: "1"}
}

// NewNanoSettlementsConsumerRunner builds the consumer from the shared
// server components. It returns nil when the consumer is disabled or
// the required MQ component is unavailable.
func NewNanoSettlementsConsumerRunner(components server.Components) *NanoSettlementsConsumer {
	if components == nil {
		return nil
	}
	cfg := config.GetConfig()
	if !cfg.Billing.NanoConsumer.Enabled {
		return nil
	}
	if components.MQ() == nil {
		logger.S().Warn("billing: nano consumer disabled, mq component unavailable")
		return nil
	}
	client, ok := components.MQ().Client().(mq.Client)
	if !ok {
		logger.S().Fatalw("billing: unexpected mq handle type",
			"type", fmt.Sprintf("%T", components.MQ().Client()))
		return nil
	}
	c := NewNanoSettlementsConsumer(client, cfg.Billing.NanoConsumer.Workers)
	if cfg.Billing.NanoConsumer.XNORate != "" {
		c.rateXNOperUSD = cfg.Billing.NanoConsumer.XNORate
	}
	return c
}

// Run implements server.Runner: it subscribes to the settlements
// subject and blocks until ctx is cancelled or the subscription fails.
func (c *NanoSettlementsConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().Settlements, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle processes one settlement event: it resolves the priced amount
// for the key-hour, converts it to an exact XNO raw string at the
// configured rate, and hands it to the rail. Because the amount is
// settled on the feeless rail at raw precision, a sub-cent charge is
// represented exactly (30 decimals) rather than rounded to the cent.
//
// The consumer is additive: it reads the same event subject as the
// existing SettlementsConsumer but never writes to billing or metering
// state. Malformed events are skipped permanently (matching the
// existing consumer's policy); a transient resolution failure returns
// for broker-level retry.
func (c *NanoSettlementsConsumer) handle(ctx context.Context, msg mq.Message) error {
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

	amountCents, err := c.amountForPeriod(ctx, event.APIKeyID, event.PeriodStart)
	if err != nil {
		// Transient failure: return for broker-level retry.
		return err
	}
	if amountCents <= 0 {
		// Unpriced hour (D8): nothing to settle.
		return nil
	}

	raw := c.rawAmount(amountCents)
	logger.S().Infow("billing: nano settlement",
		"api_key_id", event.APIKeyID,
		"period_start", event.PeriodStart,
		"amount_cents", amountCents,
		"xno_raw", raw,
		"xno_amount", FormatXNOAmount(raw))

	// The rail handoff is the agreed follow-on: submitting this exact
	// raw value to a feeless Nano node (config: nodeRpcUrl, account)
	// completes the leg. In this additive draft the value is logged so
	// the shape is reviewable before any live submit is wired.
	return nil
}

// rawAmount converts a cents-valued amount to an exact XNO raw string
// at the configured USD→XNO rate.
func (c *NanoSettlementsConsumer) rawAmount(cents float64) string {
	if c.rawer != nil {
		return c.rawer(cents)
	}
	return XNORawFromUSD_CentsFloat64(cents, c.rateXNOperUSD)
}

// amountForPeriod resolves the priced amount in cents for one
// (api_key_id, hour) using the injected resolver, or the default
// (which reads the priced charge records — the single source of truth
// the charging engine writes via PriceOnce).
func (c *NanoSettlementsConsumer) amountForPeriod(ctx context.Context, apiKeyID string, periodStart int64) (float64, error) {
	if c.resolveAmount != nil {
		return c.resolveAmount(ctx, apiKeyID, periodStart)
	}
	return c.defaultAmountForPeriod(ctx, apiKeyID, periodStart)
}

// defaultAmountForPeriod returns the sum of priced charge amounts for
// the key-hour, i.e. the amount the existing SettlementsConsumer priced
// through PriceOnce in the same billing pass. Reusing that record keeps
// a single representation end-to-end (the maintainer's precision
// constraint) and avoids a second amount type.
//
// Wiring note (draft follow-on): reading the charged amount requires
// the billing repository. In the reviewed shape, the consumer is
// constructed with a resolver bound to the billing Service (as the
// existing SettlementsConsumer is bound to *Service). Until that
// binding is wired, an unpriced (0) amount is returned and nothing is
// logged — the exact-amount layer and its tests are complete.
func (c *NanoSettlementsConsumer) defaultAmountForPeriod(_ context.Context, apiKeyID string, periodStart int64) (float64, error) {
	_ = apiKeyID
	_ = periodStart
	// This seam is the follow-on: bind a resolver that Sums priced
	// ChargeRecord amounts for (api_key_id, period_start) via the
	// billing Service (like SettlementsConsumer.handle → PriceOnce).
	// Returning 0 keeps the additive consumer a no-op until then, so
	// the draft never double-charges or settles a wrong amount.
	return 0, nil
}
