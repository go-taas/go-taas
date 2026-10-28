package infer

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	routingv1 "github.com/go-taas/go-taas/proto/taas/routing/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/routing"
)

// PlaygroundModel sends a test inference for a model on the user surface
// (feature-17 AD8). It resolves the caller's organization, validates the
// model exists and is authorized for that organization (feature-13's
// default-allow rule), finds a ready inference service for the model, and
// forwards the prompt over the normal metered path. The tenant contract
// mirrors the OpenAI-compatible endpoint and never names an inference
// service.
func (s *Service) PlaygroundModel(ctx context.Context, req *inferv1.PlaygroundModelRequest) (*inferv1.PlaygroundModelResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetModelId()) == "" {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	if strings.TrimSpace(req.GetPrompt()) == "" {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}

	modelRepo, err := s.modelRepository()
	if err != nil {
		return nil, err
	}
	m, err := modelRepo.GetModel(ctx, req.GetModelId())
	if err != nil {
		// Feature #49 (AD14): a catalog miss with a wired resolver is a
		// virtual model: the id is a routing group name. Resolve the
		// group, serve the first attempt and record the decision.
		if apierrors.CodeOf(err) != apierrors.CodeModelNotFound || s.routingResolver == nil {
			return nil, err
		}
		return s.playVirtualModel(ctx, req, orgID)
	}
	// Feature-13 data-plane predicate: a restricted model may only be
	// played by a granted organization (AC20).
	authorized, err := modelRepo.IsModelAuthorized(ctx, req.GetModelId(), orgID)
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, apierrors.New(apierrors.CodeModelUnauthorized)
	}

	// Feature #48 (AD14): a published external model has no inference
	// service; it is proxied to its provider on the metered path through
	// the injected ExternalModelProxy. The resolver fails closed with
	// 13011/13015/13007 and maps upstream failures to 13012/13013.
	if m.External {
		return s.playExternalModel(ctx, req, orgID, m.ID)
	}

	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	svc, err := repo.FindBlockingServiceByModel(ctx, req.GetModelId())
	if err != nil {
		return nil, err
	}
	if svc == nil {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	if svc.State != StateRunning {
		return nil, apierrors.New(apierrors.CodeInferServiceStateInvalid)
	}
	// A service's access policy decides who may use it. The console enforces
	// it here as well as at the data plane: the gateway plugin authenticates
	// API keys and meters usage, but the access policy is a console-side rule
	// about which people may use a service, and it is checked on this path too
	// so a session without a grant is refused before any key is used.
	userID, _, _ := s.resolveSessionActor(ctx)
	if err := s.authorizeServiceUse(ctx, repo, svc.ID, orgID, userID); err != nil {
		return nil, err
	}
	return s.playSelfHostedModel(ctx, req, m, orgID)
}

// playExternalModel proxies a published external model to its provider on
// the metered path (feature #48, AD14). The proxy resolves the provider,
// injects the upstream model id, performs the OpenAI-compatible call,
// emits a metering event and returns the parsed completion.
func (s *Service) playExternalModel(ctx context.Context, req *inferv1.PlaygroundModelRequest, orgID, modelID string) (*inferv1.PlaygroundModelResponse, error) {
	if s.externalProxy == nil {
		// No proxy wired: fail closed (no self-hosted service exists for an
		// external model).
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	body, err := buildExternalChatBody(req.GetPrompt(), req.GetTemperature(), req.GetMaxTokens())
	if err != nil {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	start := time.Now()
	respBody, err := s.externalProxy.ProxyExternalChat(ctx, modelID, orgID, req.GetApiKeyId(), body)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return nil, err
	}
	completion, promptTokens, completionTokens := parseChatCompletion(respBody)
	return &inferv1.PlaygroundModelResponse{
		Response:         okResponse(),
		RequestId:        uuid.NewString(),
		Completion:       completion,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		LatencyMs:        latency,
	}, nil
}

// playVirtualModel serves a routing group through the playground
// (feature #49, AD14): resolve the group name, serve the first attempt
// (external via the provider proxy, self-hosted via a ready inference
// service) and record the decision trail. The response names the served
// model so the user sees which candidate answered.
func (s *Service) playVirtualModel(ctx context.Context, req *inferv1.PlaygroundModelRequest, orgID string) (*inferv1.PlaygroundModelResponse, error) {
	res, err := s.routingResolver.ResolveRoutingGroup(ctx, &routingv1.ResolveRoutingGroupRequest{
		GroupName: req.GetModelId(),
		Prompt:    req.GetPrompt(),
		OrgId:     orgID,
		ApiKeyId:  req.GetApiKeyId(),
	})
	if err != nil {
		return nil, err
	}
	if len(res.GetAttemptModelIds()) == 0 {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}

	requestID := uuid.NewString()
	modelRepo, err := s.modelRepository()
	if err != nil {
		return nil, err
	}
	outcomes := map[string]string{}
	var resp *inferv1.PlaygroundModelResponse
	var servedID, servedName string
	for i, id := range res.GetAttemptModelIds() {
		name := ""
		if i < len(res.GetAttemptModelNames()) {
			name = res.GetAttemptModelNames()[i]
		}
		attempt, attemptErr := s.playCandidate(ctx, req, orgID, id, modelRepo)
		if attemptErr != nil {
			outcomes[id] = routing.OutcomeFailedRetryable
			continue
		}
		outcomes[id] = routing.OutcomeServed
		resp = attempt
		servedID, servedName = id, name
		break
	}
	if resp == nil {
		// Every attempt failed: record the trail and fail closed.
		_ = s.routingResolver.RecordDecision(ctx, res, requestID, req.GetModelId(), "", "", outcomes)
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	resp.ServedModel = servedName
	// Record the decision trail best-effort (AD12): a recorder failure
	// never fails the served response.
	_ = s.routingResolver.RecordDecision(ctx, res, requestID, req.GetModelId(), servedID, servedName, outcomes)
	return resp, nil
}

// playCandidate serves one routing attempt for a virtual playground
// request (feature #49, AD14): an external candidate through the
// provider proxy, a self-hosted one through a ready inference service.
func (s *Service) playCandidate(ctx context.Context, req *inferv1.PlaygroundModelRequest, orgID, modelID string, modelRepo *model.Repository) (*inferv1.PlaygroundModelResponse, error) {
	m, err := modelRepo.GetModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if m.External {
		return s.playExternalModel(ctx, req, orgID, m.ID)
	}
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	svc, err := repo.FindBlockingServiceByModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if svc == nil || svc.State != StateRunning {
		return nil, apierrors.New(apierrors.CodeInferServiceNotFound)
	}
	userID, _, _ := s.resolveSessionActor(ctx)
	if err := s.authorizeServiceUse(ctx, repo, svc.ID, orgID, userID); err != nil {
		return nil, err
	}
	return s.playSelfHostedModel(ctx, req, m, orgID)
}

// externalChatRequest is the OpenAI-compatible chat request the proxy sends
// upstream. The proxy overwrites model with the upstream model id.
type externalChatRequest struct {
	Messages    []externalChatMessage `json:"messages"`
	Temperature float64               `json:"temperature,omitempty"`
	MaxTokens   int32                 `json:"max_tokens,omitempty"`
}

// externalChatMessage is one OpenAI-compatible chat message.
type externalChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// buildExternalChatBody renders the OpenAI-compatible chat request body for
// an external playground call.
func buildExternalChatBody(prompt string, temperature float64, maxTokens int32) ([]byte, error) {
	return json.Marshal(externalChatRequest{
		Messages:    []externalChatMessage{{Role: "user", Content: prompt}},
		Temperature: temperature,
		MaxTokens:   maxTokens,
	})
}

// parseChatCompletion extracts the completion text and token usage from an
// OpenAI-compatible response best-effort.
func parseChatCompletion(body []byte) (completion string, promptTokens, completionTokens int64) {
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", 0, 0
	}
	if len(envelope.Choices) > 0 {
		completion = envelope.Choices[0].Message.Content
	}
	// A byte-level tokenizer can end a generation on a partial multi-byte
	// sequence, and Go's JSON decoder turns those bytes into U+FFFD. Drop them
	// so the console shows the text the model produced instead of a row of
	// replacement characters.
	completion = strings.ToValidUTF8(completion, "")
	completion = strings.ReplaceAll(completion, "\uFFFD", "")
	return completion, envelope.Usage.PromptTokens, envelope.Usage.CompletionTokens
}
