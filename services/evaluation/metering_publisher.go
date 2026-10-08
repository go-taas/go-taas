package evaluation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-taas/go-taas/pkg/mq"
)

// mqMeteringPublisher publishes one token-usage event to the standard
// metering.events subject (feature #44, AD5) — the same subject the
// data-plane gateway publishes to — so evaluation calls flow through
// the normal metering path: voucher write, request log, usage/cost and
// organization/key attribution.
type mqMeteringPublisher struct {
	client mq.Client
}

// NewMQMeteringPublisher constructs the publisher over an mq.Client.
func NewMQMeteringPublisher(client mq.Client) MeteringPublisher {
	return &mqMeteringPublisher{client: client}
}

// meteringUsage mirrors the metering module's token usage JSON.
type meteringUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

// meteringEventPayload mirrors the metering module's event JSON
// (services/metering/event_consumer.go) so the standard consumer can
// ingest it unchanged.
type meteringEventPayload struct {
	RequestID      string        `json:"request_id"`
	OrganizationID string        `json:"organization_id"`
	APIKeyID       string        `json:"api_key_id"`
	ModelID        string        `json:"model_id"`
	ServiceID      string        `json:"service_id"`
	Usage          meteringUsage `json:"usage"`
	CompletedAt    int64         `json:"completed_at"`
	LatencyMs      int64         `json:"latency_ms"`
	Status         string        `json:"status"`
	Error          string        `json:"error"`
}

// PublishMeteringEvent implements MeteringPublisher: it marshals the
// standard event payload and publishes it to metering.events.
func (p *mqMeteringPublisher) PublishMeteringEvent(ctx context.Context, event []byte) error {
	if p == nil || p.client == nil {
		return nil
	}
	return p.client.Publish(ctx, mq.DefaultSubjects().MeteringEvents, event, nil)
}

// buildMeteringEvent renders the standard metering event JSON for one
// completed case call (feature #44, AD5).
func buildMeteringEvent(requestID, orgID, apiKeyID, modelID, serviceID string, promptTokens, completionTokens int64, completedAt time.Time, latencyMS int64, status, errMsg string) []byte {
	payload := meteringEventPayload{
		RequestID:      requestID,
		OrganizationID: orgID,
		APIKeyID:       apiKeyID,
		ModelID:        modelID,
		ServiceID:      serviceID,
		Usage: meteringUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
		},
		CompletedAt: completedAt.Unix(),
		LatencyMs:   latencyMS,
		Status:      status,
		Error:       errMsg,
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// ensure the adapter satisfies the seam.
var _ MeteringPublisher = (*mqMeteringPublisher)(nil)
