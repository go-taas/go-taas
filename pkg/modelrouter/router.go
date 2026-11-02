// Copyright 2025 The go-taas Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package modelrouter is the data plane's model selector.
//
// The serving gateway routes a request to a workload by the model it names,
// which works for a model an inference service serves and fails for one the
// platform sells from a provider: there is no workload to route to, so the
// request is answered with "model does not exist".
//
// This router sits in front of the serving gateway for the chat endpoint and
// makes that distinction: a provider-backed model is forwarded to the control
// plane's provider proxy, everything else is forwarded to the serving gateway
// exactly as before. Authentication, rate limits and metering stay where they
// were — on the gateway, before this hop — so the router only decides which
// upstream answers.
package modelrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
)

// Paths and defaults. The chat path is the only one with an external
// counterpart: provider models speak the OpenAI chat contract, and a path the
// router does not know is left to the gateway.
const (
	// ChatCompletionsPath is the endpoint the router selects an upstream for.
	ChatCompletionsPath = "/v1/chat/completions"
	// ExternalModelsPath is the control plane's provider-backed model list.
	ExternalModelsPath = "/internal/gateway/external/models"
	// ExternalChatPath is the control plane's provider chat proxy.
	ExternalChatPath = "/internal/gateway/external/chat/completions"
	// GatewayTokenHeader is the header name carrying the shared secret the
	// control plane's internal gateway API requires. It is a header name, not
	// a credential: the value comes from the release Secret.
	GatewayTokenHeader = "X-Taas-Gateway-Token" //nolint:gosec // header name, not a secret

	// defaultRefreshInterval is how often the provider-backed model set is
	// reloaded. A newly published provider model becomes callable within one
	// interval without a restart.
	defaultRefreshInterval = 30 * time.Second
	// defaultRequestTimeout bounds one forwarded request end to end.
	defaultRequestTimeout = 600 * time.Second
	// maxBodyBytes bounds a buffered request body. It matches the gateway
	// plugin's limit, so a request the plugin accepted is never rejected here.
	maxBodyBytes = 1 << 20
)

// Config configures a Router.
type Config struct {
	// ServerURL is the control plane's base URL (no trailing slash, no path).
	ServerURL string
	// ServerToken is the shared secret the internal gateway API requires.
	ServerToken string
	// GatewayURL is the serving gateway's base URL (no trailing slash).
	GatewayURL string
	// RefreshInterval is the provider-model list reload period. Zero means
	// the default.
	RefreshInterval time.Duration
	// RequestTimeout bounds one forwarded request. Zero means the default.
	RequestTimeout time.Duration
}

// Router decides between the provider proxy and the serving gateway.
type Router struct {
	cfg    Config
	client *http.Client
	proxy  *httputil.ReverseProxy

	mu       sync.RWMutex
	external map[string]struct{}
	// loaded records whether the provider-model set was ever fetched. Before
	// it is, nothing is treated as provider-backed: an unanswered control
	// plane must not change how self-hosted traffic flows.
	loaded bool
}

// New builds a Router. Both upstreams are required: without the control plane
// there is nothing to forward a provider model to, and without the gateway a
// self-hosted model has nowhere to go.
func New(cfg Config) (*Router, error) {
	if strings.TrimSpace(cfg.ServerURL) == "" {
		return nil, errors.New("model router: control-plane URL is required")
	}
	if strings.TrimSpace(cfg.GatewayURL) == "" {
		return nil, errors.New("model router: serving gateway URL is required")
	}
	gatewayURL, err := url.Parse(strings.TrimRight(cfg.GatewayURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("model router: invalid serving gateway URL: %w", err)
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = defaultRefreshInterval
	}
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")

	r := &Router{
		cfg:      cfg,
		client:   &http.Client{Timeout: timeout},
		external: map[string]struct{}{},
	}
	proxy := httputil.NewSingleHostReverseProxy(gatewayURL)
	// Inference answers stream. Flushing per read keeps a generation arriving
	// as it is produced instead of in one block at the end.
	proxy.FlushInterval = -1
	r.proxy = proxy
	return r, nil
}

// Handler returns the router's HTTP surface.
func (r *Router) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", okHandler)
	mux.HandleFunc("/readyz", okHandler)
	mux.HandleFunc(ChatCompletionsPath, r.chatHandler)
	return mux
}

// okHandler answers a liveness or readiness probe.
func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// chatHandler forwards one chat completion to the upstream that can answer it.
func (r *Router) chatHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "could not read request body")
		return
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "request body is not a JSON object")
		return
	}
	if payload.Model == "" {
		writeError(w, http.StatusBadRequest, "model_required", "model is required")
		return
	}

	if r.isExternal(payload.Model) {
		r.forwardToProvider(w, req, payload.Model, body)
		return
	}
	r.forwardToGateway(w, req, body)
}

// forwardToProvider relays the request to the control plane's provider proxy.
func (r *Router) forwardToProvider(w http.ResponseWriter, req *http.Request, model string, body []byte) {
	upstream, err := http.NewRequestWithContext(req.Context(), http.MethodPost,
		r.cfg.ServerURL+ExternalChatPath, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "model_unavailable", "model "+model+" is temporarily unavailable")
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if r.cfg.ServerToken != "" {
		upstream.Header.Set(GatewayTokenHeader, r.cfg.ServerToken)
	}
	resp, err := r.client.Do(upstream)
	if err != nil {
		logger.S().Warnw("model router: provider proxy unreachable", "model", model, "err", err)
		writeError(w, http.StatusServiceUnavailable, "model_unavailable", "model "+model+" is temporarily unavailable")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		logger.S().Debugw("model router: relay provider response failed", "model", model, "err", err)
	}
}

// forwardToGateway relays the request to the serving gateway, which routes it
// to the inference service that serves the model.
func (r *Router) forwardToGateway(w http.ResponseWriter, req *http.Request, body []byte) {
	outbound := req.Clone(req.Context())
	outbound.Body = io.NopCloser(bytes.NewReader(body))
	outbound.ContentLength = int64(len(body))
	outbound.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	r.proxy.ServeHTTP(w, outbound)
}

// isExternal reports whether the model is served by a provider.
func (r *Router) isExternal(model string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.loaded {
		return false
	}
	_, ok := r.external[model]
	return ok
}

// Refresh reloads the provider-backed model set. A failure keeps the previous
// set: a control-plane hiccup must not silently reroute models that were
// provider-backed a moment ago.
func (r *Router) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.ServerURL+ExternalModelsPath, nil)
	if err != nil {
		return err
	}
	if r.cfg.ServerToken != "" {
		req.Header.Set(GatewayTokenHeader, r.cfg.ServerToken)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model router: model list answered %d", resp.StatusCode)
	}
	var payload struct {
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}

	next := make(map[string]struct{}, len(payload.Models))
	for _, name := range payload.Models {
		if name = strings.TrimSpace(name); name != "" {
			next[name] = struct{}{}
		}
	}
	r.mu.Lock()
	r.external = next
	r.loaded = true
	r.mu.Unlock()
	return nil
}

// Run refreshes the model set until ctx is cancelled.
func (r *Router) Run(ctx context.Context) error {
	if err := r.Refresh(ctx); err != nil && ctx.Err() == nil {
		logger.S().Warnw("model router: initial provider-model load failed", "err", err)
	}
	ticker := time.NewTicker(r.cfg.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.Refresh(ctx); err != nil && ctx.Err() == nil {
				logger.S().Warnw("model router: provider-model refresh failed", "err", err)
			}
		}
	}
}

// ExternalModels reports the provider-backed models currently known. It is a
// diagnostic: the console and the tests use it to see what the data plane
// believes without reading the router's internals.
func (r *Router) ExternalModels() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.external))
	for name := range r.external {
		names = append(names, name)
	}
	return names
}

// writeError renders the OpenAI error envelope callers already parse.
func writeError(w http.ResponseWriter, status int, code, message string) {
	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    code,
			"param":   "model",
		},
	})
	if err != nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
