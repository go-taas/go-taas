package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/evaluation"
	"github.com/go-taas/go-taas/services/model"
)

// evaluationProvider is the infer-backed implementation of the
// evaluation module's CompletionProvider seam (feature #44, AD5). It
// resolves a running service with endpoints for the model, presents
// the synthetic platform credential (the same one the load-test
// runner uses, feature #20 AD11) and drives one real HTTP
// chat-completion request (mirroring LoadTestRunner.fire). The
// tenant's api_key_id is carried for attribution; the key secret is
// never read.
type evaluationProvider struct {
	repo  *InferenceServiceRepository
	creds SystemCredentialProvider
	model *model.Repository
	// client is injectable for tests; nil means the default HTTP client.
	client *http.Client
}

// NewEvaluationCompletionProvider constructs the infer-backed
// completion provider (feature #44, AD5).
func NewEvaluationCompletionProvider(repo *InferenceServiceRepository, creds SystemCredentialProvider, modelRepo *model.Repository) evaluation.CompletionProvider {
	return &evaluationProvider{repo: repo, creds: creds, model: modelRepo}
}

// SetEvaluationHTTPClient installs a custom HTTP client (tests).
func (p *evaluationProvider) SetEvaluationHTTPClient(c *http.Client) { p.client = c }

// CompleteModelCall implements evaluation.CompletionProvider (feature
// #44, AD5): resolve a ready service, resolve the model name, obtain
// the platform credential and fire one real chat-completion request.
// Every failure fails closed with a business error; no simulated
// completion is ever returned.
func (p *evaluationProvider) CompleteModelCall(ctx context.Context, in *evaluation.CompletionInput) (*evaluation.CompletionResult, error) {
	if in == nil || strings.TrimSpace(in.ModelID) == "" || strings.TrimSpace(in.OrganizationID) == "" {
		return nil, errors.New(errors.CodeModelNotFound)
	}
	if p.repo == nil || p.creds == nil || p.model == nil {
		return nil, errors.New(errors.CodeInternal)
	}
	// Resolve a running service with at least one endpoint for the
	// model. The evaluation data path mirrors PlaygroundModel's
	// resolution (feature-17 AD8) but additionally requires a usable
	// endpoint, because the runner fires the real request itself.
	svc, err := p.repo.FindReadyByModelAndOrganization(ctx, in.OrganizationID, in.ModelID)
	if err != nil {
		return nil, err
	}
	if svc == nil || svc.State != StateRunning || len(svc.Endpoints) == 0 {
		return nil, errors.New(errors.CodeInferServiceNotFound)
	}
	// Resolve the model name for the request body (the endpoint's
	// OpenAI-compatible API keys off the model name).
	m, err := p.model.GetModel(ctx, in.ModelID)
	if err != nil {
		return nil, err
	}
	credential, err := p.creds.GetSystemCredential(ctx)
	if err != nil {
		return nil, errors.New(errors.CodeInternal)
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	return p.fire(ctx, client, svc.Endpoints[0], credential, m.Name, in)
}

// fire sends one chat-completion request and returns the completion
// text, token usage and latency (mirrors LoadTestRunner.fire, feature
// #44 AD5).
func (p *evaluationProvider) fire(ctx context.Context, client *http.Client, endpoint, credential, modelName string, in *evaluation.CompletionInput) (*evaluation.CompletionResult, error) {
	body, err := json.Marshal(evaluationChatRequest{
		Model: modelName,
		Messages: []evaluationChatMessage{{
			Role: "user", Content: in.Prompt,
		}},
		MaxTokens: in.MaxTokens,
	})
	if err != nil {
		return nil, errors.New(errors.CodeInternal)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatCompletionsURL(endpoint), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New(errors.CodeInternal)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)
	// The tenant's key identity rides along as attribution headers so
	// the data-plane gateway meters the call to the right org/key when
	// it fronts the endpoint (AD5: normal authorization and metering).
	req.Header.Set("X-Organization-Id", in.OrganizationID)
	req.Header.Set("X-Api-Key-Id", in.APIKeyID)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Newf(errors.CodeInferServiceStateInvalid, "evaluation: inference request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	latency := time.Since(start).Milliseconds()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.Newf(errors.CodeInferServiceStateInvalid,
			"evaluation: inference endpoint returned status %d", resp.StatusCode)
	}
	var parsed evaluationChatResponse
	if err = json.Unmarshal(raw, &parsed); err != nil {
		return nil, errors.Newf(errors.CodeInferServiceStateInvalid,
			"evaluation: unparseable inference response: %v", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.Newf(errors.CodeInferServiceStateInvalid,
			"evaluation: inference response contained no choices")
	}
	out := &evaluation.CompletionResult{
		Completion: parsed.Choices[0].Message.Content,
		LatencyMS:  latency,
	}
	if parsed.Usage != nil {
		out.PromptTokens = parsed.Usage.PromptTokens
		out.CompletionTokens = parsed.Usage.CompletionTokens
	}
	return out, nil
}

// evaluationChatRequest is the minimal OpenAI-compatible request body.
type evaluationChatRequest struct {
	Model     string                  `json:"model"`
	Messages  []evaluationChatMessage `json:"messages"`
	MaxTokens int32                   `json:"max_tokens,omitempty"`
}

// evaluationChatMessage is one chat message.
type evaluationChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// evaluationChatResponse is the subset of the chat-completion response
// the evaluation provider reads.
type evaluationChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// ensure the provider satisfies the seam at compile time.
var _ evaluation.CompletionProvider = (*evaluationProvider)(nil)
