package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/model"
)

// playgroundTimeout bounds a playground test call. It is generous enough
// for a cold model's first tokens and short enough that a stuck gateway
// never holds the console request open indefinitely.
const playgroundTimeout = 60 * time.Second

// playgroundMaxBodyBytes caps the upstream response the playground reads,
// so a misbehaving gateway cannot exhaust the server's memory.
const playgroundMaxBodyBytes = 4 << 20

// servingGatewayBaseURL resolves the serving gateway's base URL (no
// trailing slash, no "/v1"). It follows the same order as
// GetInferenceEndpoint: the configured value first, then the discovered
// gateway. Empty means nothing is reachable.
func (s *Service) servingGatewayBaseURL(ctx context.Context) string {
	if cfg := config.GetConfig(); cfg != nil {
		if v := strings.TrimSpace(cfg.Infer.EndpointBaseURL); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	if s.gatewayResolver != nil {
		if v := strings.TrimSpace(s.gatewayResolver.InferenceGatewayURL(ctx)); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	return ""
}

// playgroundChatBody is the OpenAI-compatible request the playground sends
// to the serving gateway.
type playgroundChatBody struct {
	Model       string                  `json:"model"`
	Messages    []playgroundChatMessage `json:"messages"`
	Temperature float64                 `json:"temperature,omitempty"`
	MaxTokens   int32                   `json:"max_tokens,omitempty"`
}

// playgroundChatMessage is one OpenAI-compatible chat message.
type playgroundChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// playSelfHostedModel serves a self-hosted model through the serving
// gateway, authenticated with the caller's own API key.
//
// The key travels in the request the browser sends and is used exactly
// once, here: it is placed in the Authorization header of this call and
// then goes out of scope. It is never written to the database, never
// logged, never echoed in the response and never included in an error
// message. Nothing can recover it afterwards, which is why the caller has
// to supply it on every test — the server stores API keys only as salted
// hashes.
//
// Going through the gateway rather than calling the engine pod directly is
// deliberate: the call takes the same path as the user's real traffic, so
// it exercises whatever the gateway enforces (model resolution, routing,
// and — once the gateway verifies keys — authentication, per-key rate
// limits and the model allow-list). The shipped AIBrix gateway does not
// verify bearer tokens today, so a green result currently proves routing
// and generation, not that the key is valid.
func (s *Service) playSelfHostedModel(ctx context.Context, req *inferv1.PlaygroundModelRequest, m *model.Model) (*inferv1.PlaygroundModelResponse, error) {
	apiKey := strings.TrimSpace(req.GetApiKey())
	if apiKey == "" {
		return nil, apierrors.Newf(apierrors.CodePlaygroundKeyMissing,
			"playground: send the API key with the request; keys are stored hashed and cannot be read back")
	}
	baseURL := s.servingGatewayBaseURL(ctx)
	if baseURL == "" {
		return nil, apierrors.New(apierrors.CodeInferEndpointNotFound)
	}

	body, err := json.Marshal(playgroundChatBody{
		// The gateway routes on the served model name, which is the
		// catalog name rather than the catalog id.
		Model:       m.Name,
		Messages:    []playgroundChatMessage{{Role: "user", Content: req.GetPrompt()}},
		Temperature: req.GetTemperature(),
		MaxTokens:   req.GetMaxTokens(),
	})
	if err != nil {
		return nil, apierrors.Wrap(apierrors.CodeInternal, err, "playground: encode request")
	}

	callCtx, cancel := context.WithTimeout(ctx, playgroundTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, apierrors.Wrap(apierrors.CodePlaygroundRequestFailed, err, "playground: build request")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	start := time.Now()
	resp, err := playgroundHTTPClient.Do(httpReq)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		// The transport error never carries the header, so relaying its
		// text cannot leak the key.
		return nil, apierrors.Newf(apierrors.CodePlaygroundRequestFailed,
			"playground: call the serving gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, playgroundMaxBodyBytes))
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodePlaygroundRequestFailed,
			"playground: read the gateway response: %v", err)
	}

	completion, promptTokens, completionTokens := parseChatCompletion(respBody)
	if resp.StatusCode != http.StatusOK {
		// The gateway's own message is the actionable part ("model x does
		// not exist", "rate limit exceeded"), so it is relayed — it is a
		// server response body and never contains the caller's key.
		return nil, apierrors.Newf(apierrors.CodePlaygroundRequestFailed,
			"playground: the serving gateway returned %s: %s", resp.Status, gatewayErrorText(respBody))
	}
	return &inferv1.PlaygroundModelResponse{
		Response:         okResponse(),
		RequestId:        uuid.NewString(),
		Completion:       completion,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		LatencyMs:        latency,
	}, nil
}

// gatewayErrorText extracts the human-readable part of an OpenAI-compatible
// error body, falling back to the raw (bounded) body.
func gatewayErrorText(body []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if msg := strings.TrimSpace(envelope.Error.Message); msg != "" {
			return msg
		}
		if msg := strings.TrimSpace(envelope.Message); msg != "" {
			return msg
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 512 {
		text = text[:512] + "…"
	}
	if text == "" {
		return "no response body"
	}
	return text
}

// playgroundHTTPClient is the client every playground call uses. The
// transport keeps connections alive across calls and refuses redirects: a
// playground call carries an Authorization header, and following a
// redirect would replay it to a host the operator never vetted.
var playgroundHTTPClient = &http.Client{
	Timeout: playgroundTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}
