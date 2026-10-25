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

package server

// The data-plane gateway's internal API.
//
// The Wasm plugin that fronts the inference gateway makes no business
// decisions (architecture Section 3.3): it parses the bearer token, hashes
// it, and asks this API. That keeps one implementation of key verification
// and one of voucher ingestion, so the gateway cannot drift from the
// control plane.
//
// Three endpoints live on the monitor mux (cluster-internal, never on the
// public gateway port):
//
//	POST /internal/gateway/verify   key digest -> verdict + limits
//	POST /internal/gateway/usage    token usage -> metering event
//	GET  /internal/gateway/plugin.wasm   the plugin artifact itself
//
// They are not user-facing: the plugin authenticates with a shared secret,
// and a wrong or missing one is a deployment fault (12910), never an
// inference caller's problem. That is also why they answer with HTTP
// statuses an OpenAI SDK understands instead of this platform's usual
// "business code in the body, 500 on the wire".

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

// gatewayUsageDropped counts usage events the platform could not record.
//
// It exists because a metering failure must not block inference
// (architecture Section 3.2.1) — but an invisible failure is worse than the
// failure itself, so every drop is counted here and logged at warn level.
var gatewayUsageDropped = prometheus.NewCounter(prometheus.CounterOpts{
	Namespace: "taas",
	Subsystem: "gateway_plugin",
	Name:      "usage_dropped_total",
	Help:      "Token-usage events the internal gateway API could not record.",
})

// gatewayVerifyTotal counts verifications by outcome, so an operator can
// tell "keys are being rejected" from "the verifier itself is failing".
var gatewayVerifyTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "taas",
	Subsystem: "gateway_plugin",
	Name:      "verify_total",
	Help:      "API-key verifications requested by the data-plane gateway, by outcome.",
}, []string{"result"})

// Internal gateway API paths. They are on the monitor mux, which is not
// published outside the cluster.
const (
	gatewayVerifyPath     = "/internal/gateway/verify"
	gatewayUsagePath      = "/internal/gateway/usage"
	gatewayPluginPath     = "/internal/gateway/plugin.wasm"
	gatewayTokenHeader    = "X-Gateway-Token"
	gatewayMaxVerifyBody  = 64 << 10
	gatewayMaxUsageBody   = 1 << 20
	gatewayVerifyDeadline = 5 * time.Second
	gatewayUsageDeadline  = 10 * time.Second
)

// GatewayVerifyRequest is one API-key verification on behalf of the
// gateway. The digest is the contract: the plaintext key never leaves the
// gateway (services/auth.KeyDigest).
type GatewayVerifyRequest struct {
	KeyDigest string `json:"key_digest"`
	Model     string `json:"model"`
	SourceIP  string `json:"source_ip"`
}

// GatewayVerifyResponse is the verdict plus the limits the gateway
// enforces locally. Allowed=false carries Code/Message for the caller's
// error body.
type GatewayVerifyResponse struct {
	Allowed                   bool   `json:"allowed"`
	Code                      int    `json:"code,omitempty"`
	Message                   string `json:"message,omitempty"`
	OrganizationID            string `json:"organization_id,omitempty"`
	KeyID                     string `json:"key_id,omitempty"`
	Role                      string `json:"role,omitempty"`
	RateLimitRPM              int64  `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM              int64  `json:"rate_limit_tpm,omitempty"`
	MaxConcurrentRequests     int64  `json:"max_concurrent_requests,omitempty"`
	MaxOutputTokensPerRequest int64  `json:"max_output_tokens_per_request,omitempty"`
}

// GatewayUsageRequest is one inference request's token usage, emitted by
// the gateway after the response completed. It is idempotent on
// RequestID, so a retried emission cannot double-bill.
type GatewayUsageRequest struct {
	RequestID      string            `json:"request_id"`
	OrganizationID string            `json:"organization_id"`
	APIKeyID       string            `json:"api_key_id"`
	Model          string            `json:"model"`
	ServiceID      string            `json:"service_id"`
	Usage          GatewayTokenUsage `json:"usage"`
	CompletedAt    int64             `json:"completed_at"`
	LatencyMs      int64             `json:"latency_ms"`
	TTFTMs         int64             `json:"ttft_ms"`
	GenerationMs   int64             `json:"generation_ms"`
	Status         string            `json:"status"`
	Error          string            `json:"error"`
}

// GatewayTokenUsage is the four-class token breakdown the metering ledger
// prices separately (architecture Section 3.4).
type GatewayTokenUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
}

// Empty reports whether the event carries no usage at all, in which case
// there is nothing worth metering.
func (u GatewayTokenUsage) Empty() bool {
	return u.PromptTokens == 0 && u.CompletionTokens == 0 &&
		u.CachedTokens == 0 && u.ReasoningTokens == 0
}

// GatewayVerifier verifies an API key on the gateway's behalf. It is
// implemented by the auth service by delegating to VerifyAPIKey, so the
// authoritative rules — hashing, ownership, revocation, expiry, model
// scope — stay in one place.
type GatewayVerifier interface {
	VerifyGatewayKey(ctx context.Context, req *GatewayVerifyRequest) (*GatewayVerifyResponse, error)
}

// GatewayUsageSink records one request's usage. It is implemented by the
// metering service and publishes the same event its consumer already
// decodes, so producer and consumer cannot disagree about the shape.
type GatewayUsageSink interface {
	RecordGatewayUsage(ctx context.Context, req *GatewayUsageRequest) (string, error)
}

// GatewayPluginOptions configures the internal gateway API.
type GatewayPluginOptions struct {
	// Enabled turns the endpoints on. When false they are not registered
	// at all, so a disabled plugin cannot be reached even in-cluster.
	Enabled bool
	// Token is the shared secret the plugin presents. Empty disables the
	// endpoints: an unauthenticated verification endpoint would be an
	// open door to key probing.
	Token string
	// PluginPath is the Wasm artifact to serve. Empty resolves to
	// plugin.wasm next to the executable.
	PluginPath string
}

// initGatewayAPI registers the internal endpoints when the plugin is
// configured. It is a no-op otherwise.
func (s *commonServer) initGatewayAPI() {
	opts := s.opts.GatewayPlugin
	if !opts.Enabled {
		return
	}
	if opts.Token == "" {
		logger.S().Warn("gateway plugin enabled without a shared token; internal gateway API disabled")
		return
	}
	var verifier GatewayVerifier
	var sink GatewayUsageSink
	for _, svc := range s.services {
		if v, ok := svc.(GatewayVerifier); ok && verifier == nil {
			verifier = v
		}
		if u, ok := svc.(GatewayUsageSink); ok && sink == nil {
			sink = u
		}
	}
	if verifier == nil {
		logger.S().Warn("gateway plugin enabled but no service implements GatewayVerifier; verification disabled")
	}
	if sink == nil {
		logger.S().Warn("gateway plugin enabled but no service implements GatewayUsageSink; metering disabled")
	}

	guard := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !tokenMatches(r.Header.Get(gatewayTokenHeader), opts.Token) {
				writeGatewayError(w, http.StatusUnauthorized, int(apierrors.CodeGatewayTokenInvalid),
					"invalid gateway token")
				return
			}
			next(w, r)
		}
	}

	if verifier != nil {
		s.monitorMux.HandleFunc(gatewayVerifyPath, guard(s.verifyHandler(verifier)))
	}
	if sink != nil {
		s.monitorMux.HandleFunc(gatewayUsagePath, guard(s.usageHandler(sink)))
	}
	// The artifact is served without the token on purpose: Envoy's Wasm code
	// source has no way to send a header, and the artifact is not a secret —
	// it is the filter binary, which is public source. The API that verifies
	// keys and records usage stays guarded.
	s.monitorMux.HandleFunc(gatewayPluginPath, s.pluginHandler(opts.PluginPath))
	for _, c := range gatewayCollectors() {
		if err := s.monitorRegistry.Register(c); err != nil {
			logger.S().Debugw("gateway plugin: collector already registered", "err", err)
		}
	}
	logger.S().Infow("gateway plugin API registered",
		"verify", verifier != nil, "usage", sink != nil, "plugin", true)
}

// verifyHandler answers one key verification.
//
// A rejected key is a 200 with allowed=false: the plugin needs the
// business code to build the caller's error body, and the transport
// succeeded. Only a malformed request or an infrastructure failure is a
// non-200, so the plugin can tell "the key is bad" from "the platform
// could not answer" — the two demand different reactions (reject vs
// fail closed).
func (s *commonServer) verifyHandler(verifier GatewayVerifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requirePOST(w, r) {
			return
		}
		body, err := readJSONBody(r, gatewayMaxVerifyBody)
		if err != nil {
			writeGatewayError(w, http.StatusBadRequest, 0, err.Error())
			return
		}
		var req GatewayVerifyRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeGatewayError(w, http.StatusBadRequest, 0, "malformed verify request")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), gatewayVerifyDeadline)
		defer cancel()
		resp, err := verifier.VerifyGatewayKey(ctx, &req)
		if err != nil {
			gatewayVerifyTotal.WithLabelValues("error").Inc()
			writeGatewayError(w, http.StatusServiceUnavailable, 0,
				"key verification unavailable")
			return
		}
		outcome := "allowed"
		if !resp.Allowed {
			outcome = "rejected"
		}
		gatewayVerifyTotal.WithLabelValues(outcome).Inc()
		writeJSON(w, http.StatusOK, resp)
	}
}

// usageHandler records one request's token usage.
//
// A publish failure is a 503 rather than a silent 200: the plugin counts
// it and the operator can see the reconciliation gap. It is still never a
// reason to fail the inference request (architecture Section 3.2.1), which
// is why this endpoint's status is advisory to the plugin.
func (s *commonServer) usageHandler(sink GatewayUsageSink) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requirePOST(w, r) {
			return
		}
		body, err := readJSONBody(r, gatewayMaxUsageBody)
		if err != nil {
			writeGatewayError(w, http.StatusBadRequest, 0, err.Error())
			return
		}
		var req GatewayUsageRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeGatewayError(w, http.StatusBadRequest, 0, "malformed usage request")
			return
		}
		if req.RequestID == "" {
			writeGatewayError(w, http.StatusBadRequest, 0, "request_id is required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), gatewayUsageDeadline)
		defer cancel()
		voucherID, err := sink.RecordGatewayUsage(ctx, &req)
		if err != nil {
			gatewayUsageDropped.Inc()
			logger.S().Warnw("gateway plugin: usage event not recorded",
				"request_id", req.RequestID, "err", err)
			writeGatewayError(w, http.StatusServiceUnavailable, 0, "usage not recorded")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"voucher_id": voucherID})
	}
}

// pluginHandler serves the Wasm artifact the policy loads.
//
// Serving it from the server makes one release produce exactly one plugin
// version, with no second image and no registry credentials for Envoy.
func (s *commonServer) pluginHandler(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		resolved, err := resolvePluginPath(path)
		if err != nil {
			// Not fatal: a binary built without the wasm artifact still
			// serves the rest of the platform.
			logger.S().Warnw("gateway plugin: artifact unavailable", "err", err)
			writeGatewayError(w, http.StatusNotFound, 0, "gateway plugin artifact not built")
			return
		}
		w.Header().Set("Content-Type", "application/wasm")
		http.ServeFile(w, r, resolved)
	}
}

// resolvePluginPath finds the artifact: the configured path, else
// plugin.wasm next to the executable.
func resolvePluginPath(configured string) (string, error) {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", err
		}
		return configured, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(filepath.Dir(exe), "gateway-plugin.wasm")
	if _, err := os.Stat(candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

// tokenMatches compares the presented token with the configured one in
// constant time, so a wrong token cannot be guessed by timing.
func tokenMatches(presented, expected string) bool {
	if expected == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// requirePOST rejects a non-POST verb, reporting whether the caller
// should continue.
func requirePOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost {
		return true
	}
	w.Header().Set("Allow", "POST")
	writeGatewayError(w, http.StatusMethodNotAllowed, 0, "use POST")
	return false
}

// readJSONBody reads a bounded body.
func readJSONBody(r *http.Request, limit int64) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("empty body")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return nil, errors.New("unreadable body")
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, errors.New("empty body")
	}
	return body, nil
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.S().Warnw("gateway plugin: write response failed", "err", err)
	}
}

// writeGatewayError writes the internal API's error envelope. It carries
// the business code (when one applies) so the plugin can map it to the
// caller's error body without a second translation table.
func writeGatewayError(w http.ResponseWriter, status, code int, message string) {
	payload := map[string]any{"message": message, "http_status": status}
	if code != 0 {
		payload["code"] = code
	}
	writeJSON(w, status, payload)
}

// gatewayCollectors returns the plugin's metrics for the monitor registry.
func gatewayCollectors() []prometheus.Collector {
	return []prometheus.Collector{gatewayUsageDropped, gatewayVerifyTotal}
}
