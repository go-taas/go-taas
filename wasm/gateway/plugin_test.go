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

// The plugin's tests run on the SDK's host emulator, which executes the real
// filter against a simulated Envoy: same callbacks, same hostcalls, same
// buffering rules. That means these tests exercise the behaviour that ships,
// not a reimplementation of it.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm/proxytest"
	"github.com/tetratelabs/proxy-wasm-go-sdk/proxywasm/types"
)

const (
	testToken = "test-gateway-token"
	testKey   = "sk-0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefg"
)

// pluginConfigJSON is a complete policy config for the emulator.
func pluginConfigJSON(extra string) []byte {
	base := `{"cluster":"httproute/taas/taas-internal-api/rule/0",` +
		`"host":"taas-internal.invalid",` +
		`"token":"` + testToken + `",` +
		`"cache_ttl_ms":30000,"verify_timeout_ms":500`
	if extra != "" {
		base += "," + extra
	}
	return []byte(base + "}")
}

// newHost builds an emulator with the plugin loaded.
func newHost(t *testing.T, cfg []byte) (proxytest.HostEmulator, func()) {
	t.Helper()
	host, reset := proxytest.NewHostEmulator(
		proxytest.NewEmulatorOption().WithVMContext(&vmContext{}).WithPluginConfiguration(cfg),
	)
	// Envoy configures the filter before any request arrives; the emulator
	// only does so on StartPlugin, and a config that never arrives is the
	// fail-closed path the plugin tests separately.
	require.Equal(t, types.OnPluginStartStatusOK, host.StartPlugin())
	return host, reset
}

// requestHeaders is a minimal OpenAI-style request.
func requestHeaders(path, authorization string) [][2]string {
	headers := [][2]string{
		{":method", "POST"},
		{":path", path},
		{":authority", "llm.example.com"},
		{"content-type", "application/json"},
	}
	if authorization != "" {
		headers = append(headers, [2]string{headerAuthorization, authorization})
	}
	return headers
}

// verifyOK builds a successful verify response body.
func verifyOK(extra string) []byte {
	body := `{"allowed":true,"organization_id":"org-1","key_id":"key-1","role":"member"`
	if extra != "" {
		body += "," + extra
	}
	return []byte(body + "}")
}

// verifyRejected builds a rejection verdict.
func verifyRejected(code int, message string) []byte {
	return []byte(`{"allowed":false,"code":` + formatInt(int64(code)) +
		`,"message":"` + message + `"}`)
}

// localResponse returns the status and body the plugin answered with, if any.
func localResponse(t *testing.T, host proxytest.HostEmulator, id uint32) (int, string) {
	t.Helper()
	resp := host.GetSentLocalResponse(id)
	if resp == nil {
		return 0, ""
	}
	return int(resp.StatusCode), string(resp.Data)
}

// TestRejectsAMissingCredential is the gap this plugin closes: before it,
// the gateway forwarded any request, with or without a key.
func TestRejectsAMissingCredential(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	action := host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", ""), false)
	require.Equal(t, types.ActionContinue, action)

	status, body := localResponse(t, host, id)
	assert.Equal(t, 401, status)
	assert.Contains(t, body, `"code":"invalid_api_key"`, "the body must use the OpenAI error shape")
	assert.Contains(t, body, `"type":"invalid_request_error"`)
	assert.Empty(t, host.GetCalloutAttributesFromContext(id),
		"a missing key must be rejected without a control-plane round trip")
}

// TestRejectsANonKeyCredential covers the local short-circuit: something that
// is not shaped like a platform key costs no control-plane round trip.
func TestRejectsANonKeyCredential(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer not-a-key"), false)
	status, body := localResponse(t, host, id)
	assert.Equal(t, 401, status)
	assert.Contains(t, body, "invalid api key")
}

// TestIgnoresUnprotectedPaths proves health and discovery stay open: a probe
// that cannot present a key must still be able to reach the gateway, or
// Kubernetes would restart a healthy pod forever.
func TestIgnoresUnprotectedPaths(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()

	for _, path := range []string{"/healthz", "/readyz", "/", "/metrics"} {
		id := host.InitializeHttpContext()
		action := host.CallOnRequestHeaders(id, requestHeaders(path, ""), true)
		assert.Equal(t, types.ActionContinue, action, "path %q must not require a key", path)
		status, _ := localResponse(t, host, id)
		assert.Zero(t, status, "path %q must not be rejected locally", path)
	}

	// A traversal attempt that begins with a protected prefix stays
	// protected: prefix matching is what Envoy routes on, so accepting the
	// normalized path here would open a bypass.
	id := host.InitializeHttpContext()
	host.CallOnRequestHeaders(id, requestHeaders("/v1/../healthz", ""), true)
	status, _ := localResponse(t, host, id)
	assert.Equal(t, 401, status, "a protected prefix must not be escaped by a relative path")
}

// TestVerifiesTheDigestAndNotTheKey is the security property that matters
// most: the plaintext key never leaves the gateway, not even in a log.
func TestVerifiesTheDigestAndNotTheKey(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(`{"model":"qwen-2.5-0.5b","max_tokens":10}`), true)
	host.CallOnHttpCallResponse(0, [][2]string{{":status", "200"}}, nil, verifyOK(""))

	callout := host.GetCalloutAttributesFromContext(id)
	require.Len(t, callout, 1)
	body := string(callout[0].Body)
	assert.Contains(t, body, keyDigest(testKey), "the digest is what the platform receives")
	assert.NotContains(t, body, testKey, "the plaintext key must never be forwarded")
	assert.Contains(t, body, "qwen-2.5-0.5b", "the model is what the platform authorizes")

	// The token guards the internal endpoint.
	found := false
	for _, kv := range callout[0].Headers {
		if kv[0] == headerGatewayToken {
			found = true
			assert.Equal(t, testToken, kv[1])
		}
	}
	assert.True(t, found, "the shared token must be presented")

	logs := strings.Join(append(append(host.GetInfoLogs(), host.GetDebugLogs()...), host.GetErrorLogs()...), "\n")
	assert.NotContains(t, logs, testKey, "the plaintext key must never be logged")
}

// TestFailsClosedOnAVerificationTimeout is the fail-closed rule: a platform
// that cannot answer must not be read as permission.
func TestFailsClosedOnAVerificationTimeout(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(`{"model":"m"}`), true)

	// A non-2xx answer stands in for a timeout or an unavailable platform.
	host.CallOnHttpCallResponse(0, [][2]string{{":status", "503"}}, nil, []byte(`{}`))

	status, body := localResponse(t, host, id)
	assert.Equal(t, 503, status)
	assert.Contains(t, body, `"code":"service_unavailable"`)
}

// TestFailsClosedOnAnUnparsableVerdict covers a compromised or broken
// response body: without a parseable verdict there is no "allow".
func TestFailsClosedOnAnUnparsableVerdict(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(`{"model":"m"}`), true)
	host.CallOnHttpCallResponse(0, [][2]string{{":status", "200"}}, nil, []byte(`not json`))

	status, _ := localResponse(t, host, id)
	assert.Equal(t, 503, status)
}

// TestMapsBusinessCodesToStatuses pins the table an SDK's retry logic reads:
// a bad key is a 401 (do not retry), a rate limit is a 429 (retry later).
func TestMapsBusinessCodesToStatuses(t *testing.T) {
	cases := []struct {
		code   int
		status int
		code_  string
	}{
		{codeAPIKeyNotFound, 401, "invalid_api_key"},
		{codeAPIKeyInvalid, 401, "invalid_api_key"},
		{codeAPIKeyRevoked, 401, "api_key_revoked"},
		{codeAPIKeyExpired, 401, "api_key_expired"},
		{codeForbidden, 403, "model_not_allowed"},
		{codeAPIKeyModelNotAllowed, 403, "model_not_allowed"},
		{codeModelUnauthorized, 403, "model_not_allowed"},
		{codeRateLimitExceeded, 429, "rate_limit_exceeded"},
		{codeQuotaOutputTokensLimit, 400, "max_tokens_exceeded"},
	}
	for _, tc := range cases {
		host, reset := newHost(t, pluginConfigJSON(""))
		id := host.InitializeHttpContext()
		host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
		host.CallOnRequestBody(id, []byte(`{"model":"m"}`), true)
		host.CallOnHttpCallResponse(0, [][2]string{{":status", "200"}}, nil, verifyRejected(tc.code, "denied"))

		status, body := localResponse(t, host, id)
		assert.Equal(t, tc.status, status, "code %d", tc.code)
		assert.Contains(t, body, `"code":"`+tc.code_+`"`, "code %d", tc.code)
		reset()
	}
}

// TestAllowsAValidRequest covers the happy path and that the request is
// resumed so it can reach a backend.
func TestAllowsAValidRequest(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(`{"model":"m"}`), true)
	host.CallOnHttpCallResponse(0, [][2]string{{":status", "200"}}, nil, verifyOK(""))

	status, _ := localResponse(t, host, id)
	assert.Zero(t, status, "an allowed request must not be answered locally")
}

// TestPropagatesARequestID gives metering its idempotency key, even when the
// caller supplied none.
func TestPropagatesARequestID(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	headers := host.GetCurrentRequestHeaders(id)
	assert.True(t, headerPresent(headers, headerRequestID),
		"a request id must be propagated for metering")
}

// TestKeepsTheCallersRequestID proves an inbound id is reused rather than
// replaced: a caller correlating its own logs must keep working.
func TestKeepsTheCallersRequestID(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	headers := append(requestHeaders("/v1/chat/completions", "Bearer "+testKey),
		[2]string{headerRequestID, "caller-supplied-id"})
	host.CallOnRequestHeaders(id, headers, false)

	for _, kv := range host.GetCurrentRequestHeaders(id) {
		if kv[0] == headerRequestID {
			assert.Equal(t, "caller-supplied-id", kv[1])
			return
		}
	}
	t.Fatal("the request id header was dropped")
}

// TestEmitsUsageFromANonStreamingResponse is the metering half: the engine's
// usage reaches the platform exactly once.
func TestEmitsUsageFromANonStreamingResponse(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(`{"model":"m","max_tokens":10}`), true)
	host.CallOnHttpCallResponse(0, [][2]string{{":status", "200"}}, nil, verifyOK(""))

	response := `{"choices":[{"message":{"content":"hi"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`
	host.CallOnResponseHeaders(id, [][2]string{{":status", "200"}, {"content-type", "application/json"}}, false)
	host.CallOnResponseBody(id, []byte(response), true)

	callouts := host.GetCalloutAttributesFromContext(id)
	require.GreaterOrEqual(t, len(callouts), 2, "verify plus one usage report")
	last := callouts[len(callouts)-1]
	assert.Contains(t, string(last.Body), `"prompt_tokens":7`)
	assert.Contains(t, string(last.Body), `"completion_tokens":3`)
	assert.Contains(t, string(last.Body), `"organization_id":"org-1"`)
	assert.Contains(t, string(last.Body), `"api_key_id":"key-1"`)
	assert.Contains(t, string(last.Body), `"request_id":"`)
}

// TestEmitsUsageFromAStreamingResponse covers SSE: the response is a sequence
// of data lines, and the usage arrives in the last chunk.
func TestEmitsUsageFromAStreamingResponse(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(`{"model":"m","stream":true}`), true)
	host.CallOnHttpCallResponse(0, [][2]string{{":status", "200"}}, nil, verifyOK(""))

	// Three frames, exactly as an SSE stream arrives.
	host.CallOnResponseHeaders(id, [][2]string{{":status", "200"}, {"content-type", "text/event-stream"}}, false)
	host.CallOnResponseBody(id, []byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"), false)
	host.CallOnResponseBody(id, []byte("data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}\n\n"), false)
	host.CallOnResponseBody(id,
		[]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":22}}\n\ndata: [DONE]\n\n"), true)

	callouts := host.GetCalloutAttributesFromContext(id)
	require.GreaterOrEqual(t, len(callouts), 2)
	last := callouts[len(callouts)-1]
	assert.Contains(t, string(last.Body), `"prompt_tokens":11`)
	assert.Contains(t, string(last.Body), `"completion_tokens":22`)
}

// TestReportsUsageOnceOnly protects the ledger from a double count: two
// end-of-stream signals must not produce two events.
func TestReportsUsageOnceOnly(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(`{"model":"m"}`), true)
	host.CallOnHttpCallResponse(0, [][2]string{{":status", "200"}}, nil, verifyOK(""))
	host.CallOnResponseHeaders(id, [][2]string{{":status", "200"}}, false)
	host.CallOnResponseBody(id, []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2}}`), true)
	host.CompleteHttpContext(id)

	callouts := host.GetCalloutAttributesFromContext(id)
	assert.Len(t, callouts, 2, "one verify, one usage report")
}

// TestSuppressesUsageWhenTheEngineReportedNone keeps the ledger honest: a
// response with no usage object produces no event, rather than a zero-token
// voucher that would look like a free request.
func TestSuppressesUsageWhenTheEngineReportedNone(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(`{"model":"m"}`), true)
	host.CallOnHttpCallResponse(0, [][2]string{{":status", "200"}}, nil, verifyOK(""))
	host.CallOnResponseHeaders(id, [][2]string{{":status", "200"}}, false)
	host.CallOnResponseBody(id, []byte(`{"choices":[]}`), true)

	assert.Len(t, host.GetCalloutAttributesFromContext(id), 1, "verify only")
}

// TestRejectsWhenTheRateLimitIsExhausted covers the per-key budget whose
// contract says the gateway enforces it.
func TestRejectsWhenTheRateLimitIsExhausted(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()

	// rpm=2: the first two requests fit the bucket, the third does not. Each
	// request names a different model so the verdict cache cannot answer it —
	// the point here is the bucket, not the cache.
	ask := func(model string) (int, string) {
		id := host.InitializeHttpContext()
		host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
		host.CallOnRequestBody(id, []byte(`{"model":"`+model+`"}`), true)
		// Answer whatever callout the plugin actually issued.
		callouts := host.GetCalloutAttributesFromContext(id)
		require.Len(t, callouts, 1, "model %q must be verified", model)
		host.CallOnHttpCallResponse(callouts[0].CalloutID, [][2]string{{":status", "200"}}, nil,
			verifyOK(`"rate_limit_rpm":2`))
		return localResponse(t, host, id)
	}

	for i := 0; i < 2; i++ {
		status, _ := ask("m" + formatInt(int64(i)))
		assert.Zero(t, status, "request %d must be allowed", i)
	}
	status, body := ask("m2")
	assert.Equal(t, 429, status)
	assert.Contains(t, body, "rate_limit_exceeded")
}

// TestClampsMaxTokensDownToTheCap implements the other half of the quota
// contract: clamp rather than reject, so a caller whose only mistake was
// asking for too much is still served.
func TestClampsMaxTokensDownToTheCap(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(`{"model":"m","max_tokens":4000,"stream":true}`), true)
	host.CallOnHttpCallResponse(0, [][2]string{{":status", "200"}}, nil,
		verifyOK(`"max_output_tokens_per_request":512`))

	body := string(host.GetCurrentRequestBody(id))
	assert.Contains(t, body, `"max_tokens":512`, "the cap must replace the requested value")
	assert.Contains(t, body, `"stream":true`, "the rest of the request must be untouched")
}

// TestLeavesMaxTokensAloneWhenUnderTheCap avoids rewriting a request that is
// already within its limits.
func TestLeavesMaxTokensAloneWhenUnderTheCap(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	original := `{"model":"m","max_tokens":100}`
	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(original), true)
	host.CallOnHttpCallResponse(0, [][2]string{{":status", "200"}}, nil,
		verifyOK(`"max_output_tokens_per_request":512`))

	assert.Equal(t, original, string(host.GetCurrentRequestBody(id)))
}

// TestRefusesToStartWithoutAToken covers the dangerous misconfiguration: with
// no shared token the filter must not start, so Envoy blocks rather than
// admitting unverified traffic.
func TestRefusesToStartWithoutAToken(t *testing.T) {
	host, reset := proxytest.NewHostEmulator(
		proxytest.NewEmulatorOption().WithVMContext(&vmContext{}).
			WithPluginConfiguration([]byte(`{"cluster":"c","host":"h"}`)),
	)
	defer reset()
	assert.Equal(t, types.OnPluginStartStatusFailed, host.StartPlugin(),
		"a filter that cannot verify must not start")
}

// TestFailsClosedWhenTheConfigNeverArrived is the bug this test was written
// from: an empty config made every path look unprotected, so the filter
// admitted traffic. A missing config must protect the default prefixes.
func TestFailsClosedWhenTheConfigNeverArrived(t *testing.T) {
	host, reset := proxytest.NewHostEmulator(
		proxytest.NewEmulatorOption().WithVMContext(&vmContext{}),
	)
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	status, body := localResponse(t, host, id)
	assert.Equal(t, 503, status, "an unconfigured filter must not admit inference traffic")
	assert.Contains(t, body, "service_unavailable")
}

// headerPresent reports whether a header list carries key.
func headerPresent(headers [][2]string, key string) bool {
	for _, kv := range headers {
		if strings.EqualFold(kv[0], key) {
			return true
		}
	}
	return false
}

// TestPausesExactlyOncePerRequest pins the pause discipline.
//
// Pausing at the headers and again at the body looks safer but is a hang:
// Envoy resumes only the phase it last stopped in, so the request never
// leaves the filter and the caller times out with no response at all. This
// was a live failure, not a hypothetical.
func TestPausesExactlyOncePerRequest(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	headers := host.CallOnRequestHeaders(id,
		requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	if headers == types.ActionPause {
		body := host.CallOnRequestBody(id, []byte(`{"model":"m"}`), true)
		t.Fatalf("paused at headers (%v) and at body (%v): one pause per request", headers, body)
	}

	body := host.CallOnRequestBody(id, []byte(`{"model":"m"}`), true)
	assert.Equal(t, types.ActionPause, body, "the body is where the model name becomes known")

	callouts := host.GetCalloutAttributesFromContext(id)
	require.Len(t, callouts, 1)
	host.CallOnHttpCallResponse(callouts[0].CalloutID, [][2]string{{":status", "200"}}, nil, verifyOK(""))
	// Resuming is what releases the buffered body; nothing may re-pause it.
	assert.Zero(t, len(localResponseBody(t, host, id)))
}

// localResponseBody returns the local response body, or "" when none was sent.
func localResponseBody(t *testing.T, host proxytest.HostEmulator, id uint32) string {
	t.Helper()
	if resp := host.GetSentLocalResponse(id); resp != nil {
		return string(resp.Data)
	}
	return ""
}

// TestRequestsUsageOnAStreamedResponse closes the gap that made streaming
// unmetered: an OpenAI-compatible engine omits `usage` from a stream unless
// the caller asks for it, so every `stream: true` request would reach the
// ledger as zero tokens.
func TestRequestsUsageOnAStreamedResponse(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(`{"model":"m","stream":true}`), true)
	callouts := host.GetCalloutAttributesFromContext(id)
	require.Len(t, callouts, 1)
	host.CallOnHttpCallResponse(callouts[0].CalloutID, [][2]string{{":status", "200"}}, nil, verifyOK(""))

	body := string(host.GetCurrentRequestBody(id))
	assert.Contains(t, body, `"include_usage":true`, "the engine must be asked for usage")
	assert.Contains(t, body, `"stream":true`, "and the stream flag must survive")
}

// TestKeepsTheCallersStreamOptions proves the plugin completes the request
// rather than overriding it: a caller who set the flag keeps their value.
func TestKeepsTheCallersStreamOptions(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	original := `{"model":"m","stream":true,"stream_options":{"include_usage":false}}`
	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(original), true)
	callouts := host.GetCalloutAttributesFromContext(id)
	require.Len(t, callouts, 1)
	host.CallOnHttpCallResponse(callouts[0].CalloutID, [][2]string{{":status", "200"}}, nil, verifyOK(""))

	assert.Equal(t, original, string(host.GetCurrentRequestBody(id)),
		"an explicit stream_options must be left alone")
}

// TestLeavesANonStreamingRequestAlone keeps a plain completion untouched.
func TestLeavesANonStreamingRequestAlone(t *testing.T) {
	host, reset := newHost(t, pluginConfigJSON(""))
	defer reset()
	id := host.InitializeHttpContext()

	original := `{"model":"m","messages":[]}`
	host.CallOnRequestHeaders(id, requestHeaders("/v1/chat/completions", "Bearer "+testKey), false)
	host.CallOnRequestBody(id, []byte(original), true)
	callouts := host.GetCalloutAttributesFromContext(id)
	require.Len(t, callouts, 1)
	host.CallOnHttpCallResponse(callouts[0].CalloutID, [][2]string{{":status", "200"}}, nil, verifyOK(""))

	assert.Equal(t, original, string(host.GetCurrentRequestBody(id)))
}

// TestEnsureStreamUsageFormatting covers the splice on differently formatted
// bodies, including the empty-object case that must not gain a leading comma.
func TestEnsureStreamUsageFormatting(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"stream":true}`, `{"stream":true,"stream_options":{"include_usage":true}}`},
		{`{"stream":true }`, `{"stream":true,"stream_options":{"include_usage":true}}`},
		{"{\n  \"stream\": true\n}", "{\n  \"stream\": true,\"stream_options\":{\"include_usage\":true}}"},
		{`{"stream":false}`, `{"stream":false}`},
		{`{"model":"m"}`, `{"model":"m"}`},
	} {
		got, changed := ensureStreamUsage(tc.in)
		assert.Equal(t, tc.want, got, "input %q", tc.in)
		assert.Equal(t, tc.in != tc.want, changed, "input %q", tc.in)
	}
}
