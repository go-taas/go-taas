package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	providerv1 "github.com/go-taas/go-taas/proto/taas/provider/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
)

// resolvedExternal is the in-memory resolution of a catalog external model
// to its provider and decrypted credential (AD14).
type resolvedExternal struct {
	ProviderID       string
	ProviderName     string
	BaseURL          string
	UpstreamModelID  string
	TimeoutSeconds   int
	APIKey           string
	ExtraHeaders     map[string]string
	CatalogModelName string
}

// ResolveExternalModel resolves a catalog model to its provider, the
// decrypted key and the upstream model id, or a fail-closed code. It is an
// internal RPC (no HTTP annotation); the plaintext key exists in memory for
// the duration of the call only.
func (s *Service) ResolveExternalModel(ctx context.Context, req *providerv1.ResolveExternalModelRequest) (*providerv1.ResolveExternalModelResponse, error) {
	resolved, err := s.resolve(ctx, req.GetModelId(), req.GetModelName())
	if err != nil {
		return nil, err
	}
	return &providerv1.ResolveExternalModelResponse{
		Response: okResponse(),
		Model: &providerv1.ResolvedExternalModel{
			ProviderId:            resolved.ProviderID,
			ProviderName:          resolved.ProviderName,
			BaseUrl:               resolved.BaseURL,
			UpstreamModelId:       resolved.UpstreamModelID,
			RequestTimeoutSeconds: int64(resolved.TimeoutSeconds),
			ApiKey:                resolved.APIKey,
			ExtraHeaders:          resolved.ExtraHeaders,
		},
	}, nil
}

// resolve looks up and validates an external catalog model. It fails closed
// with 13015 (not external / unpublished), 13001 (provider unknown), 13011
// (provider disabled) or 13007 (key undecryptable).
func (s *Service) resolve(ctx context.Context, modelID, modelName string) (*resolvedExternal, error) {
	if s.catalog == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "provider: model catalog unavailable")
	}
	cm, err := s.catalog.LookupExternalModel(ctx, modelID, modelName)
	if err != nil {
		return nil, err
	}
	if cm == nil || !cm.External || !cm.Published {
		return nil, apierrors.New(apierrors.CodeProviderNotPublished)
	}
	providerRepo, err := s.providers()
	if err != nil {
		return nil, err
	}
	p, err := providerRepo.GetByID(ctx, cm.ProviderID)
	if err != nil {
		if apierrors.CodeOf(err) == apierrors.CodeProviderNotFound {
			return nil, apierrors.New(apierrors.CodeProviderNotPublished)
		}
		return nil, err
	}
	if !p.Enabled {
		return nil, apierrors.New(apierrors.CodeProviderDisabled)
	}
	key, err := s.decryptSecret(p.APIKeyCiphertext)
	if err != nil {
		return nil, err
	}
	headers, err := s.decryptHeaders(p.ExtraHeaders)
	if err != nil {
		return nil, err
	}
	return &resolvedExternal{
		ProviderID:       p.ID,
		ProviderName:     p.Name,
		BaseURL:          p.BaseURL,
		UpstreamModelID:  cm.UpstreamModelID,
		TimeoutSeconds:   p.RequestTimeoutSeconds,
		APIKey:           key,
		ExtraHeaders:     headers,
		CatalogModelName: cm.Name,
	}, nil
}

// ProxyExternalChat resolves an external model, proxies a
// POST {base_url}/chat/completions call with the decrypted key, maps
// upstream failures (401/403 -> 13012, 5xx/timeout -> 13013), emits a
// metering event on success and failure, and returns the upstream body. It
// implements the infer.ExternalModelProxy seam (AD14).
func (s *Service) ProxyExternalChat(ctx context.Context, modelID, orgID, apiKeyID string, body []byte) ([]byte, error) {
	resolved, err := s.resolve(ctx, modelID, "")
	if err != nil {
		return nil, err
	}
	timeout := resolved.TimeoutSeconds
	if timeout <= 0 {
		timeout = defaultRequestTimeoutSeconds
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	target := trimTrailingSlashes(resolved.BaseURL) + "/chat/completions"
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(injectUpstreamModel(body, resolved.UpstreamModelID)))
	if err != nil {
		return nil, apierrors.New(apierrors.CodeProviderUpstreamUnavailable)
	}
	req.Header.Set("Content-Type", "application/json")
	if resolved.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+resolved.APIKey)
	}
	for name, value := range resolved.ExtraHeaders {
		req.Header.Set(name, value)
	}

	requestID := uuid.NewString()
	start := time.Now()
	resp, err := s.probeClient.httpClient.Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		s.emitMetering(ctx, requestID, orgID, apiKeyID, modelID, meteringUsage{}, latency, "error")
		return nil, upstreamTransportError(err, timeout)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		s.emitMetering(ctx, requestID, orgID, apiKeyID, modelID, meteringUsage{}, latency, "error")
		return nil, apierrors.New(apierrors.CodeProviderUpstreamUnavailable)
	}

	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		usage := parseUsage(respBody)
		s.emitMetering(ctx, requestID, orgID, apiKeyID, modelID, usage, latency, "success")
		return respBody, nil
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		s.emitMetering(ctx, requestID, orgID, apiKeyID, modelID, meteringUsage{}, latency, "error")
		return nil, apierrors.New(apierrors.CodeProviderUpstreamUnauthorized)
	default:
		// Any other non-2xx (5xx, 429, …) is treated as upstream
		// unavailable (design FR6.3).
		s.emitMetering(ctx, requestID, orgID, apiKeyID, modelID, meteringUsage{}, latency, "error")
		return nil, apierrors.New(apierrors.CodeProviderUpstreamUnavailable)
	}
}

// upstreamTransportError maps a transport failure to 13013.
func upstreamTransportError(err error, timeoutSeconds int) error {
	// The transport category is kept out of the tenant-visible message; the
	// sanitized reason is only used for internal diagnostics.
	_ = transportMessage(err, timeoutSeconds)
	return apierrors.New(apierrors.CodeProviderUpstreamUnavailable)
}

// injectUpstreamModel sets the "model" field of an OpenAI-compatible chat
// request body to the upstream model id so the caller never has to know it
// (the tenant contract is the catalog model id). A non-object body is
// returned unchanged.
func injectUpstreamModel(body []byte, upstreamModelID string) []byte {
	if upstreamModelID == "" {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	payload["model"] = upstreamModelID
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// meteringUsage mirrors the metering module's token usage JSON.
type meteringUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

// meteringEvent mirrors the metering module's event JSON
// (services/metering/event_consumer.go) so the standard consumer ingests it
// unchanged (design FR6.4).
type meteringEvent struct {
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

// emitMetering publishes a metering event on the standard subject
// best-effort (a publish failure never fails the request).
func (s *Service) emitMetering(ctx context.Context, requestID, orgID, apiKeyID, modelID string, usage meteringUsage, latencyMS int64, status string) {
	client, err := s.mqClient()
	if err != nil || client == nil {
		return
	}
	payload := meteringEvent{
		RequestID:      requestID,
		OrganizationID: orgID,
		APIKeyID:       apiKeyID,
		ModelID:        modelID,
		Usage:          usage,
		CompletedAt:    time.Now().UTC().Unix(),
		LatencyMs:      latencyMS,
		Status:         status,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = client.Publish(ctx, mq.DefaultSubjects().MeteringEvents, body, nil)
}

// mqClient resolves the MQ client from the injected publisher or the shared
// components.
func (s *Service) mqClient() (mq.Client, error) {
	if s.publisher != nil {
		return s.publisher, nil
	}
	if s.components == nil || s.components.MQ() == nil {
		return nil, nil
	}
	client, ok := s.components.MQ().Client().(mq.Client)
	if !ok {
		return nil, nil
	}
	return client, nil
}

// parseUsage extracts the token usage from an OpenAI-compatible response
// best-effort; a missing or malformed usage yields zeros.
func parseUsage(body []byte) meteringUsage {
	var envelope struct {
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			CachedTokens     int64 `json:"cached_tokens"`
			ReasoningTokens  int64 `json:"reasoning_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return meteringUsage{}
	}
	return meteringUsage{
		PromptTokens:     envelope.Usage.PromptTokens,
		CompletionTokens: envelope.Usage.CompletionTokens,
		CachedTokens:     envelope.Usage.CachedTokens,
		ReasoningTokens:  envelope.Usage.ReasoningTokens,
	}
}
