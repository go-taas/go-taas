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

package main

// The data-plane gateway plugin.
//
// It implements steps 01 (Auth) and 04 (Meter) of the ordered pipeline in
// architecture Section 3.2.1, plus the two limits whose contracts say the
// gateway enforces them locally (rate_limit_rpm and
// max_output_tokens_per_request). Routing stays with AIBrix: this filter sits
// on the outer gateway and never picks a backend.
//
// Two rules shape the whole file:
//
//   - **Auth fails closed.** A verification that times out or cannot be
//     parsed rejects the request. Guessing "allow" from a timeout is how an
//     outage becomes a breach.
//   - **Metering never blocks inference.** A usage report that cannot be
//     sent is logged and dropped; the request has already been served. The
//     platform's reconciliation attributes the gap by request id.

import (
	"strconv"
	"strings"

	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm"
	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm/types"
)

const (
	// header names
	headerAuthorization = "authorization"
	headerContentType   = "content-type"
	headerRequestID     = "x-request-id"
	headerGatewayToken  = "x-gateway-token"

	// shared-data key prefixes. Entries are per-VM (per worker thread).
	sharedPrefixVerdict   = "go-taas:verdict:"
	sharedPrefixRateLimit = "go-taas:rpm:"

	// maxResponseTail bounds how much of a response body is retained while
	// looking for the usage object. A stream's final chunk is small; keeping
	// the tail only is what makes metering cheap for a long generation.
	maxResponseTail = 32 << 10
)

// pluginContext owns the filter configuration and creates one httpContext
// per request.
type pluginContext struct {
	types.DefaultPluginContext
	cfg *config
	// vmID makes generated request ids unique per worker.
	vmID uint32
	// clock is the coarse second counter this worker ticks forward.
	clock int64
}

// httpContext is one request's state.
type httpContext struct {
	types.DefaultHttpContext
	plugin *pluginContext

	method string
	path   string
	// requestID is the idempotency key for metering.
	requestID string
	// model is what the caller asked for; it is both the authorization
	// subject and what the usage report names.
	model string
	// protected reports whether this path needs a verified key.
	protected bool
	// lastDigest is the SHA-256 of the presented key. The plaintext is
	// dropped immediately: nothing downstream needs it.
	lastDigest string
	// verdict is the platform's answer once it arrives.
	verdict *verdict
	// verifyCallID identifies the verification callout.
	verifyCallID uint32
	// verifyPending reports whether a verification call is in flight.
	verifyPending bool
	// requestBody accumulates the request body while it is being inspected.
	requestBody []byte
	// bodyTooLarge reports that the body exceeded the inspection budget; the
	// request is still authenticated, just without the model name.
	bodyTooLarge bool
	// responseTail keeps the end of the response, where usage lives.
	responseTail []byte
	// usageEmitted guards against reporting the same request twice.
	usageEmitted bool
	// startedAtSeconds is the coarse clock when the request headers
	// arrived, for an approximate latency.
	startedAtSeconds int64
	// rejected reports that the plugin already answered locally.
	rejected bool
}

// OnPluginStart parses the filter configuration.
func (p *pluginContext) OnPluginStart(pluginConfigurationSize int) types.OnPluginStartStatus {
	raw, err := proxywasm.GetPluginConfiguration()
	if err != nil && pluginConfigurationSize > 0 {
		proxywasm.LogErrorf("go-taas gateway plugin: read config: %v", err)
		return types.OnPluginStartStatusFailed
	}
	cfg, err := parseConfig(raw)
	if err != nil {
		proxywasm.LogErrorf("go-taas gateway plugin: %v", err)
		return types.OnPluginStartStatusFailed
	}
	if !cfg.valid() {
		// No endpoint or no token means the plugin cannot verify anything.
		// Failing the start makes Envoy refuse traffic (failOpen:false),
		// which is the honest outcome; the alternative is an unverified
		// inference surface that looks healthy.
		proxywasm.LogError("go-taas gateway plugin: cluster, host and token are required")
		return types.OnPluginStartStatusFailed
	}
	p.cfg = cfg
	// The tick drives the coarse clock (see time.go). It must be armed here,
	// before the first request, or every TTL and token bucket would read a
	// frozen clock.
	if err := proxywasm.SetTickPeriodMilliSeconds(tickPeriodMs); err != nil {
		proxywasm.LogWarnf("go-taas gateway plugin: arm clock tick: %v", err)
	}
	proxywasm.LogInfof("go-taas gateway plugin: started (cache %dms, timeout %dms)",
		cfg.CacheTTLMs, cfg.VerifyTimeoutMs)
	return types.OnPluginStartStatusOK
}

// OnTick advances the coarse clock. It runs once per tickPeriodMs on the
// plugin context, independently of any request.
func (p *pluginContext) OnTick() {
	p.bumpClock()
}

// NewHttpContext creates one filter instance per request.
func (p *pluginContext) NewHttpContext(contextID uint32) types.HttpContext {
	return &httpContext{plugin: p, requestID: ""}
}

// OnHttpRequestHeaders is the entry point: it decides whether the request
// needs a credential, and pauses the stream so the body can be inspected for
// the model name before verification.
func (h *httpContext) OnHttpRequestHeaders(_ int, endOfStream bool) types.Action {
	h.startedAtSeconds = h.plugin.nowSeconds()
	h.method, _ = proxywasm.GetHttpRequestHeader(":method")
	h.path, _ = proxywasm.GetHttpRequestHeader(":path")
	if h.path == "" {
		h.path, _ = proxywasm.GetHttpRequestHeader("x-envoy-original-path")
	}
	if idx := strings.IndexByte(h.path, '?'); idx >= 0 {
		h.path = h.path[:idx]
	}
	h.requestID, _ = proxywasm.GetHttpRequestHeader(headerRequestID)
	if h.requestID == "" {
		h.requestID = h.generateRequestID()
	}
	// Propagate the id so the platform's request logs and the engine's
	// logs can be joined to this voucher.
	_ = proxywasm.ReplaceHttpRequestHeader(headerRequestID, h.requestID)

	h.protected = h.plugin.protects(h.path)
	if !h.protected {
		return types.ActionContinue
	}
	if h.plugin.cfg == nil {
		// The filter configuration never arrived, so there is nothing to
		// verify against. Admitting the request would be a silent fail-open
		// on a protected path; refusing it is loud and safe.
		h.rejectLocal(503, "authorization unavailable", "server_error", "service_unavailable")
		return types.ActionContinue
	}

	header, _ := proxywasm.GetHttpRequestHeader(headerAuthorization)
	token := bearerToken(header)
	if token == "" {
		h.rejectLocal(401, "missing api key", "invalid_request_error", "invalid_api_key")
		return types.ActionContinue
	}
	if !looksLikeAPIKey(token) {
		// Not even shaped like a platform key: answer locally rather than
		// spending a control-plane round trip on it.
		h.rejectLocal(401, "invalid api key", "invalid_request_error", "invalid_api_key")
		return types.ActionContinue
	}
	h.lastDigest = keyDigest(token)

	if endOfStream {
		// No body to inspect: verify with an empty model. This is the only
		// pause in this request, which is what makes the resume work.
		h.dispatchVerify()
		return types.ActionPause
	}
	// A body follows. Do not pause here: the pause happens in
	// OnHttpRequestBody, where the model name is available.
	//
	// Pausing twice (headers, then body) looks safer but is not — Envoy
	// resumes only the phase it last stopped in, so the request never leaves
	// the filter and the client times out. Header-only forwarding costs
	// nothing here: an engine answers nothing until the body arrives, and
	// the body is where the pause is.
	h.requestBody = nil
	return types.ActionContinue
}

// OnHttpRequestBody accumulates the body, then verifies.
func (h *httpContext) OnHttpRequestBody(bodySize int, endOfStream bool) types.Action {
	if h.rejected {
		return types.ActionContinue
	}
	if !h.protected {
		return types.ActionContinue
	}
	if !h.bodyTooLarge {
		frame, err := proxywasm.GetHttpRequestBody(0, bodySize)
		if err != nil {
			proxywasm.LogWarnf("go-taas gateway plugin: read request body: %v", err)
			h.bodyTooLarge = true
		} else if len(h.requestBody)+len(frame) > h.plugin.cfg.MaxBodyBytes {
			// Past the budget the model name is unavailable. Authenticate
			// anyway: skipping auth because a body was large would turn an
			// upload into a way around the key check.
			h.bodyTooLarge = true
			h.requestBody = nil
		} else {
			h.requestBody = append(h.requestBody, frame...)
		}
	}
	if !endOfStream {
		return types.ActionPause
	}
	if !h.bodyTooLarge {
		if model, _, ok := requestIdentity(string(h.requestBody)); ok {
			h.model = model
		}
	}
	h.dispatchVerify()
	return types.ActionPause
}

// OnHttpCallResponse handles the platform's answers, for both verification
// and the fire-and-forget usage report.
func (h *httpContext) OnHttpCallResponse(calloutID uint32, _ int, bodySize int, _ int) {
	if calloutID != h.verifyCallID {
		// The usage report's answer is only interesting on failure.
		if body, err := proxywasm.GetHttpCallResponseBody(0, bodySize); err == nil {
			if status, _ := proxywasm.GetHttpCallResponseHeaders(); !httpStatusOK(status) {
				proxywasm.LogWarnf("go-taas gateway plugin: usage report rejected: %s", string(body))
			}
		}
		return
	}
	h.verifyPending = false
	headers, _ := proxywasm.GetHttpCallResponseHeaders()
	if !httpStatusOK(headers) {
		// The platform could not answer — a timeout, an unreachable cluster
		// or a 5xx. Fail closed: a failed call must never be read as
		// permission. The status is logged because "the plugin returned 503"
		// is otherwise unattributable.
		proxywasm.LogWarnf("go-taas gateway plugin: verification failed: status=%s flags=%s",
			headerValue(headers, ":status"), headerValue(headers, "x-envoy-upstream-service-time"))
		h.rejectLocal(503, "authorization unavailable", "server_error", "service_unavailable")
		return
	}
	body, err := proxywasm.GetHttpCallResponseBody(0, bodySize)
	if err != nil {
		proxywasm.LogWarnf("go-taas gateway plugin: read verification response: %v", err)
		h.rejectLocal(503, "authorization unavailable", "server_error", "service_unavailable")
		return
	}
	v, ok := parseVerdict(body)
	if !ok {
		proxywasm.LogWarnf("go-taas gateway plugin: unparsable verification response: %d bytes", len(body))
		h.rejectLocal(503, "authorization unavailable", "server_error", "service_unavailable")
		return
	}
	v.fetchedAtSeconds = h.plugin.nowSeconds()
	h.applyVerdict(v)
}

// applyVerdict enforces a verdict: reject, or admit and continue the pipeline.
//
// It is shared by the network path and the cache path on purpose — a cached
// verdict must not skip a check the fresh one performs, or the cache would be
// a way around the rate limit.
func (h *httpContext) applyVerdict(v *verdict) {
	if !v.allowed {
		status, errType, errCode := rejection(v.code, v.message)
		message := v.message
		if message == "" {
			message = "request not authorized"
		}
		h.rejectLocal(status, message, errType, errCode)
		return
	}
	h.verdict = v

	// Rate limit (pipeline step 02) after auth, because an unauthenticated
	// request must be cheaper to reject than a rate-limited one.
	if !h.allowRate() {
		h.rejectLocal(429, "rate limit exceeded", "rate_limit_error", "rate_limit_exceeded")
		return
	}
	// Rewrite the request where it is safe and useful, then release it.
	//
	// Both rewrites work on one buffer: the token cap the platform asked for,
	// and the usage flag that makes a streamed response report its tokens at
	// all. Anything unreadable is left alone — a request that cannot be
	// rewritten is still authenticated and routed.
	if !h.bodyTooLarge && len(h.requestBody) > 0 {
		body := string(h.requestBody)
		changed := false
		if v.maxOutputTokensPerRequest > 0 {
			if rewritten, clamped, ok := clampMaxTokens(body, v.maxOutputTokensPerRequest); ok && clamped {
				body, changed = rewritten, true
				proxywasm.LogDebugf("go-taas gateway plugin: clamped max_tokens to %d",
					v.maxOutputTokensPerRequest)
			}
		}
		if rewritten, injected := ensureStreamUsage(body); injected {
			body, changed = rewritten, true
			proxywasm.LogDebugf("go-taas gateway plugin: requested usage on a streamed response")
		}
		if changed {
			if err := proxywasm.ReplaceHttpRequestBody([]byte(body)); err != nil {
				proxywasm.LogWarnf("go-taas gateway plugin: rewrite request body: %v", err)
			}
		}
	}
	_ = proxywasm.ResumeHttpRequest()
}

// OnHttpResponseHeaders remembers the content type so the body can be read
// without pausing the stream.
func (h *httpContext) OnHttpResponseHeaders(_ int, _ bool) types.Action {
	return types.ActionContinue
}

// OnHttpResponseBody keeps a bounded tail of the response and emits the usage
// report when the body ends.
//
// The response is never paused: inference answers stream, and buffering them
// to meter them would add exactly the latency this design exists to avoid.
func (h *httpContext) OnHttpResponseBody(bodySize int, endOfStream bool) types.Action {
	if bodySize > 0 {
		if frame, err := proxywasm.GetHttpResponseBody(0, bodySize); err == nil {
			h.responseTail = appendTail(h.responseTail, frame, maxResponseTail)
		}
	}
	if endOfStream {
		h.emitUsage()
	}
	return types.ActionContinue
}

// OnHttpResponseTrailers is a no-op; usage arrives in the body.
func (h *httpContext) OnHttpResponseTrailers(_ int) types.Action { return types.ActionContinue }

// OnHttpRequestTrailers is a no-op; the stream was already resumed.
func (h *httpContext) OnHttpRequestTrailers(_ int) types.Action { return types.ActionContinue }

// OnHttpStreamDone is the last chance to report. It only fires when the
// response ended without a body frame carrying usage (for example an error
// response), so the emit path is shared and guarded.
func (h *httpContext) OnHttpStreamDone() {
	if !h.usageEmitted && h.verdict != nil {
		h.emitUsage()
	}
}

// dispatchVerify asks the platform to verify the key, unless a cached verdict
// is still fresh.
func (h *httpContext) dispatchVerify() {
	if v, ok := h.cachedVerdict(); ok {
		h.applyVerdict(v)
		return
	}
	payload := []byte(`{"key_digest":"` + jsonEscape(h.lastDigest) +
		`","model":"` + jsonEscape(h.model) + `"}`)
	headers := h.plugin.callHeaders(h.plugin.cfg.VerifyPath)
	// A local verdict cache (below) already served the common case; reaching
	// here means the platform must be asked.
	callID, err := proxywasm.DispatchHttpCall(
		h.plugin.cfg.Cluster, headers, payload, nil,
		uint32(h.plugin.cfg.VerifyTimeoutMs), h.onVerifyResponse)
	if err != nil {
		proxywasm.LogWarnf("go-taas gateway plugin: dispatch verify: %v", err)
		h.rejectLocal(503, "authorization unavailable", "server_error", "service_unavailable")
		return
	}
	h.verifyCallID = callID
	h.verifyPending = true
}

// onVerifyResponse bridges the SDK callback into the context.
func (h *httpContext) onVerifyResponse(numHeaders, bodySize, numTrailers int) {
	h.OnHttpCallResponse(h.verifyCallID, numHeaders, bodySize, numTrailers)
}

// rejectLocal answers the request itself, in the OpenAI error shape.
func (h *httpContext) rejectLocal(status int, message, errType, errCode string) {
	h.rejected = true
	if err := proxywasm.SendHttpResponse(uint32(status),
		[][2]string{{headerContentType, "application/json"}},
		errorBody(message, errType, errCode), -1); err != nil {
		proxywasm.LogWarnf("go-taas gateway plugin: send local response: %v", err)
	}
}

// emitUsage reports the request's token usage to the platform.
//
// It is fire-and-forget by design: the response has already been streamed to
// the caller, so there is nothing left to fail. A rejection is logged so the
// gap is visible to whoever reads the gateway's log.
func (h *httpContext) emitUsage() {
	if h.usageEmitted || h.verdict == nil || h.plugin.cfg.UsagePath == "" {
		return
	}
	h.usageEmitted = true
	usage, ok := extractUsage(string(h.responseTail))
	if !ok {
		return
	}
	rep := &usageReport{
		requestID:      h.requestID,
		organizationID: h.verdict.organizationID,
		keyID:          h.verdict.keyID,
		model:          h.model,
		usage:          usage,
		status:         "success",
		latencyMs:      (h.plugin.nowSeconds() - h.startedAtSeconds) * 1000,
	}
	if h.verdict.maxOutputTokensPerRequest > 0 && usage.completion >= h.verdict.maxOutputTokensPerRequest {
		// Hitting the cap is normal, but worth recording so a
		// cost investigation can tell truncation from a short answer.
		rep.status = "streaming"
	}
	headers := h.plugin.callHeaders(h.plugin.cfg.UsagePath)
	if _, err := proxywasm.DispatchHttpCall(h.plugin.cfg.Cluster, headers,
		usageBody(rep), nil, uint32(h.plugin.cfg.VerifyTimeoutMs), h.onUsageResponse); err != nil {
		proxywasm.LogWarnf("go-taas gateway plugin: dispatch usage: %v", err)
	}
}

// onUsageResponse logs a rejected usage report. It never retries: the
// platform's reconciliation works from the request log, and a retry loop in a
// filter is a way to convert one slow database into a queue of them.
func (h *httpContext) onUsageResponse(numHeaders, bodySize, numTrailers int) {
	h.OnHttpCallResponse(0, numHeaders, bodySize, numTrailers)
}

// allowRate applies the key's per-minute budget.
func (h *httpContext) allowRate() bool {
	rpm := h.verdict.rateLimitRPM
	if rpm <= 0 {
		return true
	}
	key := sharedPrefixRateLimit + h.verdict.keyID
	state := h.loadRateState(key)
	allowed, next := allowRequest(state, rpm, h.plugin.nowSeconds())
	if next != nil {
		if err := setSharedData(key, encodeRateState(next)); err != nil {
			proxywasm.LogWarnf("go-taas gateway plugin: persist rate limit: %v", err)
		}
	}
	return allowed
}

// cachedVerdict returns a still-fresh cached verdict for this digest+model.
func (h *httpContext) cachedVerdict() (*verdict, bool) {
	if h.plugin.cfg.CacheTTLMs <= 0 || h.lastDigest == "" {
		return nil, false
	}
	key := sharedPrefixVerdict + cacheKey(h.lastDigest, h.model)
	raw, _, err := proxywasm.GetSharedData(key)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	v, ok := decodeVerdict(raw)
	if !ok {
		return nil, false
	}
	// The coarse clock is in seconds; the TTL is configured in milliseconds.
	if h.plugin.nowSeconds()-v.fetchedAtSeconds > int64(h.plugin.cfg.CacheTTLMs/1000) {
		return nil, false
	}
	return v, true
}

// loadRateState reads one key's bucket from shared data.
func (h *httpContext) loadRateState(key string) *rateLimitState {
	raw, _, err := proxywasm.GetSharedData(key)
	if err != nil {
		return nil
	}
	return decodeRateState(raw)
}

// storeVerdict caches the platform's answer locally.
//
// Shared data is per-VM, so this is a per-worker cache: with N workers the
// platform sees up to N verifications per TTL rather than one. That is the
// documented trade-off; the platform's own Redis cache sits underneath and
// makes each of those cheap.
func (h *httpContext) storeVerdict(v *verdict) {
	if h.plugin.cfg.CacheTTLMs <= 0 || v == nil {
		return
	}
	key := sharedPrefixVerdict + cacheKey(h.lastDigest, h.model)
	if err := setSharedData(key, encodeVerdict(v)); err != nil {
		proxywasm.LogWarnf("go-taas gateway plugin: cache verdict: %v", err)
	}
}

// generateRequestID builds a unique id when the caller did not supply one.
//
// It has to be stable for this request only; the counter plus the start time
// plus the worker id is unique within a VM and distinct across VMs, which is
// what the ledger's idempotency needs.
func (h *httpContext) generateRequestID() string {
	h.plugin.vmID++
	return "gw-" + strconv.FormatUint(uint64(h.startedAtSeconds), 36) + "-" +
		strconv.FormatUint(uint64(h.plugin.vmID), 36)
}

// appendTail keeps the last `limit` bytes of a growing buffer.
func appendTail(buf, frame []byte, limit int) []byte {
	if len(frame) >= limit {
		return append(buf[:0], frame[len(frame)-limit:]...)
	}
	if len(buf)+len(frame) > limit {
		drop := len(buf) + len(frame) - limit
		buf = append(buf[:0], buf[drop:]...)
	}
	return append(buf, frame...)
}

// headerValue returns a header's value, or "".
func headerValue(headers [][2]string, key string) string {
	for _, kv := range headers {
		if strings.EqualFold(kv[0], key) {
			return kv[1]
		}
	}
	return ""
}

// httpStatusOK reports whether a header list carries a 2xx status.
func httpStatusOK(headers [][2]string) bool {
	for _, kv := range headers {
		if strings.EqualFold(kv[0], ":status") {
			return len(kv[1]) == 3 && kv[1][0] == '2'
		}
	}
	return false
}

// protects reports whether path needs a verified key.
//
// A nil config protects the default prefixes rather than none: the filter
// must fail closed when its configuration never arrived, and a missing config
// is not evidence that the path is public.
func (p *pluginContext) protects(path string) bool {
	if p.cfg != nil {
		return p.cfg.protects(path)
	}
	for _, prefix := range defaultProtectedPrefixes {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// callHeaders builds the upstream headers for one internal API call.
func (p *pluginContext) callHeaders(path string) [][2]string {
	return [][2]string{
		{":method", "POST"},
		{":path", path},
		{":authority", p.cfg.Host},
		{headerContentType, "application/json"},
		{headerGatewayToken, p.cfg.Token},
	}
}
