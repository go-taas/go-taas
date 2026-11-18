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
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
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
	// ServingModelsPath is the control plane's served-model routing table: the
	// model a service serves, mapped to the service that serves it.
	ServingModelsPath = "/internal/gateway/serving/models"
	// ExternalChatPath is the control plane's provider chat proxy.
	ExternalChatPath = "/internal/gateway/external/chat/completions"
	// GatewayTokenHeader is the header name carrying the shared secret the
	// control plane's internal gateway API requires. It is the same header the
	// Wasm plugin presents, so one secret and one header guard every internal
	// caller. A header name, not a credential: the value comes from the
	// release Secret.
	GatewayTokenHeader = "X-Gateway-Token" //nolint:gosec // header name, not a secret

	// defaultRefreshInterval is how often the provider-backed model set is
	// reloaded. A newly published provider model becomes callable within one
	// interval without a restart.
	defaultRefreshInterval = 30 * time.Second
	// defaultWorkloadPort is the port a KServe workload Service serves the
	// OpenAI API on when no other port is configured.
	defaultWorkloadPort = 8000
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
	// GatewayURL is the base URL of the data plane's internal listener. It is
	// the fallback for a model the control plane does not report: the model has
	// no known workload Service, so the request goes to the listener, which
	// answers it only if the deployment's Gateway serves a route for it.
	GatewayURL string
	// InferNamespace is the namespace inference services live in. KServe names
	// a model's workload Service after the service, in this namespace.
	InferNamespace string
	// InternalHost is the host name the Gateway's internal listener matches.
	// It is presented with a request that has no resolvable workload Service,
	// because the public host would not select the internal listener.
	InternalHost string
	// WorkloadSuffix and WorkloadPort address a model's own workload Service.
	// KServe names that Service "<service><suffix>" and serves the OpenAI API
	// on WorkloadPort, so it is where a self-hosted model is reached.
	WorkloadSuffix string
	// WorkloadPort is the port a model's workload Service serves on.
	WorkloadPort int
	// RefreshInterval is the provider-model list reload period. Zero means
	// the default.
	RefreshInterval time.Duration
	// RequestTimeout bounds one forwarded request. Zero means the default.
	RequestTimeout time.Duration
}

// Router decides between the provider proxy and a model's own workload.
type Router struct {
	cfg    Config
	client *http.Client
	proxy  *httputil.ReverseProxy
	// gateway is where a model the platform does not report is sent: the data
	// plane's internal listener, which answers only if it has a route for it.
	gateway *url.URL
	// direct reports whether a self-hosted model is reached at its own
	// workload Service.
	direct bool

	mu       sync.RWMutex
	external map[string]struct{}
	served   map[string]string
	// loaded records whether the provider-model set was ever fetched. Before
	// it is, nothing is treated as provider-backed: an unanswered control
	// plane must not change how self-hosted traffic flows.
	loaded bool
}

// New builds a Router. The control plane is required: without it there is
// nothing to forward a provider model to. The data plane's internal listener is
// required too, because it answers a model the control plane does not report.
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
	if cfg.WorkloadSuffix != "" && cfg.WorkloadPort <= 0 {
		cfg.WorkloadPort = defaultWorkloadPort
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
		served:   map[string]string{},
		gateway:  gatewayURL,
		direct:   strings.TrimSpace(cfg.WorkloadSuffix) != "",
	}
	proxy := httputil.NewSingleHostReverseProxy(gatewayURL)
	// A proxy that sets Rewrite may not also carry the Director
	// NewSingleHostReverseProxy installs.
	proxy.Director = nil
	proxy.Rewrite = r.rewrite
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
	mux.HandleFunc("/", r.chatHandler)
	return mux
}

// okHandler answers a liveness or readiness probe.
func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// chatHandler forwards one chat completion to the upstream that can answer it.
//
// The public path is the OpenAI one, /v1/chat/completions, and the model is
// named in the body. The router resolves that model to the service that serves
// it and forwards the request to that service's own workload, which answers the
// same path: nothing has to be prefixed.
func (r *Router) chatHandler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost || !isChatPath(req.URL.Path) {
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
	r.forwardToGateway(w, req, payload.Model, body)
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

// forwardToGateway relays the request to the upstream that answers it.
//
// A model the control plane reports is answered by its own workload Service,
// which serves the OpenAI paths as they are. The platform publishes a
// per-service endpoint (`<host>/<service>/v1`), so a leading `/<service>`
// segment is dropped: it names the service, and the workload Service of that
// service is where the request goes. A model it does not report has no known
// workload: the request goes to the data plane's internal listener, which
// answers only if the deployment's Gateway happens to serve a route for it.
func (r *Router) forwardToGateway(w http.ResponseWriter, req *http.Request, model string, body []byte) {
	outbound := req.Clone(req.Context())
	outbound.Body = io.NopCloser(bytes.NewReader(body))
	outbound.ContentLength = int64(len(body))
	outbound.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	if target := r.directTarget(model); target != nil {
		outbound.URL.Path = stripServicePrefix(outbound.URL.Path, r.serviceForModel(model))
		outbound = outbound.WithContext(context.WithValue(outbound.Context(), upstreamTargetKey{}, target))
		r.proxy.ServeHTTP(w, outbound)
		return
	}
	outbound.URL.Path = r.upstreamPath(req.URL.Path, model)
	r.proxy.ServeHTTP(w, outbound)
}

// stripServicePrefix drops a leading "/<service>" from a caller's path.
//
// The platform publishes a per-service endpoint, "<host>/<service>/v1", so a
// client that copies it sends the service name as the first segment. The
// workload answers the OpenAI path itself, so that segment is dropped before
// the request is forwarded. A path without it is returned unchanged.
func stripServicePrefix(path, service string) string {
	if service == "" {
		return path
	}
	prefix := "/" + service
	if path == prefix || path == prefix+"/" {
		return "/"
	}
	if after, ok := strings.CutPrefix(path, prefix+"/"); ok {
		return "/" + after
	}
	return path
}

// directTarget is the workload that answers a model, or nil when the control
// plane does not report one.
//
// The serving stack creates the workload Service named after the model's
// service, in the inference namespace, and it serves the OpenAI API as it is:
// the router needs no path prefix for it. A model the control plane does not
// report has no such Service, so it has no upstream here either.
func (r *Router) directTarget(model string) *upstreamTarget {
	if !r.direct {
		return nil
	}
	service := r.serviceForModel(model)
	if service == "" {
		return nil
	}
	namespace := strings.Trim(r.cfg.InferNamespace, "/")
	host := net.JoinHostPort(
		service+r.cfg.WorkloadSuffix+"."+namespace+".svc",
		strconv.Itoa(r.cfg.WorkloadPort),
	)
	return &upstreamTarget{url: &url.URL{Scheme: "http", Host: host}, host: host}
}

// upstreamPath is the fallback's path: the data plane's internal listener routes
// by path, so the inference namespace and the resolved service are prefixed onto
// it. The delivery does not use this — a reported model is forwarded to its own
// workload Service with the path untouched — and the listener has no per-model
// route, so it answers this the way it answers any unknown path.
func (r *Router) upstreamPath(public, model string) string {
	namespace := strings.Trim(r.cfg.InferNamespace, "/")
	prefix := ""
	if namespace != "" {
		prefix = "/" + namespace
	}
	service := r.serviceForModel(model)
	if service != "" {
		if strings.HasPrefix(public, prefix+"/"+service+"/") {
			return public
		}
		return prefix + "/" + service + "/" + strings.TrimPrefix(public, "/")
	}
	if namespace == "" || strings.HasPrefix(public, prefix+"/") {
		return public
	}
	return "/" + namespace + "/" + strings.TrimPrefix(public, "/")
}

// upstreamTarget is where one request is forwarded: the absolute base URL of
// the upstream and the Host header to present to it.
type upstreamTarget struct {
	url  *url.URL
	host string
}

// upstreamTargetKey carries the resolved upstream from the handler, which is
// the only place that knows the model, to the proxy rewrite, which never reads
// a body.
type upstreamTargetKey struct{}

// rewrite applies a request's upstream to its outbound form.
//
// The handler resolves the upstream because only it has parsed the model; the
// rewrite only applies it. A request that carries no target keeps the gateway,
// so a model this router cannot resolve still gets the gateway's answer rather
// than an empty one.
func (r *Router) rewrite(pr *httputil.ProxyRequest) {
	target, _ := pr.In.Context().Value(upstreamTargetKey{}).(*upstreamTarget)
	if target == nil {
		target = &upstreamTarget{url: r.gateway, host: r.cfg.InternalHost}
	}
	pr.Out.URL.Scheme = target.url.Scheme
	pr.Out.URL.Host = target.url.Host
	pr.Out.Host = target.host
}

// isChatPath reports whether the path is the OpenAI chat endpoint. A segment in
// front of it is not read: the service is resolved from the model in the body.
func isChatPath(path string) bool {
	return strings.HasSuffix(strings.TrimSuffix(path, "/"), ChatCompletionsPath)
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
	return r.refreshServed(ctx)
}

// refreshServed reloads the served-model routing table: which service answers a
// model. The router reads the model from the body, so it cannot tell which
// workload should answer without this table. An older control plane answers 404;
// the previous table is kept, and a model that is not in it has no known
// workload and keeps the fallback.
func (r *Router) refreshServed(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.ServerURL+ServingModelsPath, nil)
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
	if resp.StatusCode == http.StatusNotFound {
		// A control plane that predates the table answers 404: keep the
		// previous one (usually empty) and fall back to the model name.
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model router: served-model table answered %d", resp.StatusCode)
	}
	var payload struct {
		Models map[string]string `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}
	next := make(map[string]string, len(payload.Models))
	for model, service := range payload.Models {
		model, service = strings.TrimSpace(model), strings.TrimSpace(service)
		if model != "" && service != "" {
			next[model] = service
		}
	}
	r.mu.Lock()
	r.served = next
	r.mu.Unlock()
	return nil
}

// serviceForModel returns the service whose route answers a served model, or ""
// when the control plane does not report one.
func (r *Router) serviceForModel(model string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.served[model]
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
