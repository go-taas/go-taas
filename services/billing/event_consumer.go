package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"

	"github.com/go-taas/go-taas/services/infer"
)

// meteringEvent mirrors the metering.events payload (architecture
// Section 4.5.1) — the additive accelerator_type field included.
type meteringEvent struct {
	RequestID       string     `json:"request_id"`
	OrganizationID  string     `json:"organization_id"`
	APIKeyID        string     `json:"api_key_id"`
	ModelID         string     `json:"model_id"`
	ServiceID       string     `json:"service_id"`
	AcceleratorType string     `json:"accelerator_type"`
	Usage           tokenUsage `json:"usage"`
	CompletedAt     int64      `json:"completed_at"`
}

// tokenUsage is the per-part token breakdown carried on the event.
type tokenUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

// EventConsumer subscribes to the metering events subject and writes
// one usage line per event through the shared ingestion handler (D5).
// It implements server.Runner so the framework starts it with the
// server and cancels it on shutdown.
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
	if !cfg.Billing.EventConsumer.Enabled {
		return nil
	}
	if components.MQ() == nil || components.DB() == nil {
		logger.S().Warn("billing: event consumer disabled, mq or db component unavailable")
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
	return NewEventConsumer(client, NewForFVT(db, client), cfg.Billing.EventConsumer.Workers)
}

// Run implements server.Runner: it subscribes to the metering events
// subject and blocks until ctx is cancelled or the subscription fails.
func (c *EventConsumer) Run(ctx context.Context) error {
	return c.client.Subscribe(ctx, mq.DefaultSubjects().MeteringEvents, func(msg mq.Message) error {
		return c.handle(ctx, msg)
	})
}

// handle applies one metering event. JSON decode errors and validation
// failures are logged and permanently skipped (FR3.3). Transient
// database failures return the error for broker-level retry.
func (c *EventConsumer) handle(ctx context.Context, msg mq.Message) error {
	var event meteringEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		logger.S().Warnw("billing: malformed event, skipping",
			"subject", msg.Subject, "err", err)
		return nil
	}
	lineID, err := c.service.handleMeteringEvent(ctx, &event)
	if err != nil {
		if ae, ok := apierrors.As(err); ok && ae.Code == apierrors.CodeMeteringEventInvalid {
			// Permanently invalid: log and skip, never retry.
			logger.S().Warnw("billing: invalid event, skipping",
				"request_id", event.RequestID, "err", err)
			return nil
		}
		return err
	}
	logger.S().Infow("billing: usage line ingested",
		"request_id", event.RequestID, "line_id", lineID)
	return nil
}

// handleMeteringEvent is the shared ingestion core (D5/D6): metering
// validation → card resolution chain → idempotent usage-line write.
func (s *Service) handleMeteringEvent(ctx context.Context, ev *meteringEvent) (string, error) {
	if err := validateMeteringEvent(ev); err != nil {
		return "", err
	}
	repo, err := s.repository()
	if err != nil {
		return "", err
	}

	// D6 resolution chain: event field → service lookup → default.
	cardType := strings.TrimSpace(ev.AcceleratorType)
	if cardType == "" && ev.ServiceID != "" {
		resolved, err := s.resolveCardFromService(ctx, ev.ServiceID)
		if err != nil {
			return "", err
		}
		cardType = resolved
	}
	if cardType == "" {
		cardType = defaultCardType
	}

	line := &UsageLine{
		RequestID:        ev.RequestID,
		OrganizationID:   ev.OrganizationID,
		APIKeyID:         ev.APIKeyID,
		ModelID:          ev.ModelID,
		AcceleratorType:  cardType,
		PromptTokens:     ev.Usage.PromptTokens,
		CompletionTokens: ev.Usage.CompletionTokens,
		CachedTokens:     ev.Usage.CachedTokens,
		ReasoningTokens:  ev.Usage.ReasoningTokens,
		CompletedAt:      time.Unix(ev.CompletedAt, 0).UTC(),
	}
	if ev.ServiceID != "" {
		sid := ev.ServiceID
		line.ServiceID = &sid
	}
	stored, err := repo.IngestUsageLine(ctx, line)
	if err != nil {
		return "", err
	}
	return stored.ID, nil
}

// resolveCardFromService looks the service's accelerator type up in the
// infer module (read-only, ingestion time only — Section 3.4).
func (s *Service) resolveCardFromService(ctx context.Context, serviceID string) (string, error) {
	repo, err := s.inferRepository()
	if err != nil {
		return "", err
	}
	types, err := repo.AcceleratorTypesByServiceIDs(ctx, []string{serviceID})
	if err != nil {
		return "", err
	}
	return types[serviceID], nil
}

// validateMeteringEvent applies the metering validation matrix
// (10401-equivalent): the first failure returns and nothing is written
// (FR3.3).
func validateMeteringEvent(ev *meteringEvent) error {
	invalid := apierrors.New(apierrors.CodeMeteringEventInvalid)
	if strings.TrimSpace(ev.RequestID) == "" || len(ev.RequestID) > 128 {
		return invalid
	}
	if strings.TrimSpace(ev.OrganizationID) == "" || len(ev.OrganizationID) > 64 {
		return invalid
	}
	if strings.TrimSpace(ev.APIKeyID) == "" || len(ev.APIKeyID) > 64 {
		return invalid
	}
	if strings.TrimSpace(ev.ModelID) == "" || len(ev.ModelID) > 128 {
		return invalid
	}
	if len(ev.ServiceID) > 64 {
		return invalid
	}
	if len(ev.AcceleratorType) > 64 {
		return invalid
	}
	if ev.Usage.PromptTokens < 0 || ev.Usage.CompletionTokens < 0 ||
		ev.Usage.CachedTokens < 0 || ev.Usage.ReasoningTokens < 0 {
		return invalid
	}
	if ev.CompletedAt <= 0 {
		return invalid
	}
	return nil
}

// inferRepository lazily wires the read-only infer repository used for
// card resolution.
func (s *Service) inferRepository() (*infer.InferenceServiceRepository, error) {
	if s.inferRepo != nil {
		return s.inferRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		return nil, err
	}
	s.inferRepo = infer.NewInferenceServiceRepository(db)
	return s.inferRepo, nil
}
