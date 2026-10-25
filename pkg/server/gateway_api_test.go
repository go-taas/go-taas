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

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

const testGatewayToken = "test-gateway-token"

// fakeGatewayService implements both gateway seams so one stub covers the
// verifier and the sink.
type fakeGatewayService struct {
	mu       sync.Mutex
	verifyFn func(context.Context, *GatewayVerifyRequest) (*GatewayVerifyResponse, error)
	usageFn  func(context.Context, *GatewayUsageRequest) (string, error)
	verified []*GatewayVerifyRequest
	recorded []*GatewayUsageRequest
}

func (f *fakeGatewayService) AttachToServer(_ *grpc.Server) {}
func (f *fakeGatewayService) ServiceName() string           { return "fake-gateway" }

func (f *fakeGatewayService) VerifyGatewayKey(ctx context.Context, req *GatewayVerifyRequest) (*GatewayVerifyResponse, error) {
	f.mu.Lock()
	f.verified = append(f.verified, req)
	f.mu.Unlock()
	if f.verifyFn != nil {
		return f.verifyFn(ctx, req)
	}
	return &GatewayVerifyResponse{Allowed: true, OrganizationID: "org-1", KeyID: "key-1"}, nil
}

func (f *fakeGatewayService) RecordGatewayUsage(ctx context.Context, req *GatewayUsageRequest) (string, error) {
	f.mu.Lock()
	f.recorded = append(f.recorded, req)
	f.mu.Unlock()
	if f.usageFn != nil {
		return f.usageFn(ctx, req)
	}
	return "voucher-1", nil
}

// newGatewayTestServer builds a server with the plugin enabled and returns
// its monitor mux, which is where the internal API lives.
func newGatewayTestServer(t *testing.T, svc *fakeGatewayService, opts GatewayPluginOptions) *http.ServeMux {
	t.Helper()
	if opts.Token == "" {
		opts.Token = testGatewayToken
	}
	srv, err := NewServer(&Options{
		Name:          "test",
		ServicePort:   1,
		MonitorPort:   2,
		GatewayPlugin: opts,
	})
	require.NoError(t, err)
	if svc != nil {
		srv.RegisterService(svc)
	}
	cs := srv.(*commonServer)
	cs.initMonitor()
	cs.initGatewayAPI()
	return cs.monitorMux
}

// postJSON issues one POST with the shared token unless token is empty.
func postJSON(mux *http.ServeMux, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(gatewayTokenHeader, token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestGatewayAPIDisabledRegistersNothing proves the endpoints do not exist
// when the plugin is off — a disabled feature must not be reachable.
func TestGatewayAPIDisabledRegistersNothing(t *testing.T) {
	mux := newGatewayTestServer(t, &fakeGatewayService{}, GatewayPluginOptions{Enabled: false})
	rec := postJSON(mux, gatewayVerifyPath, testGatewayToken,
		`{"key_digest":"`+strings.Repeat("a", 64)+`"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestGatewayAPIRefusesToStartWithoutToken covers the dangerous
// misconfiguration: an unauthenticated verification endpoint is an open
// door to key probing, so it is not registered at all.
func TestGatewayAPIRefusesToStartWithoutToken(t *testing.T) {
	srv, err := NewServer(&Options{
		Name:          "test",
		ServicePort:   1,
		MonitorPort:   2,
		GatewayPlugin: GatewayPluginOptions{Enabled: true},
	})
	require.NoError(t, err)
	srv.RegisterService(&fakeGatewayService{})
	cs := srv.(*commonServer)
	cs.initMonitor()
	cs.initGatewayAPI()

	rec := postJSON(cs.monitorMux, gatewayVerifyPath, "", `{}`)
	assert.Equal(t, http.StatusNotFound, rec.Code,
		"an enabled plugin without a token must not expose the endpoints")
}

// TestGatewayAPITokenGuard covers 12910: a missing or wrong token is
// refused, and the verifier is never consulted (a token failure must not
// consume a key-verification budget or leak a verdict).
func TestGatewayAPITokenGuard(t *testing.T) {
	svc := &fakeGatewayService{}
	mux := newGatewayTestServer(t, svc, GatewayPluginOptions{Enabled: true})
	digest := strings.Repeat("a", 64)

	for _, token := range []string{"", "wrong", testGatewayToken + "x"} {
		rec := postJSON(mux, gatewayVerifyPath, token, `{"key_digest":"`+digest+`"}`)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "token %q", token)

		var payload map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
		assert.EqualValues(t, apierrors.CodeGatewayTokenInvalid, payload["code"])
		assert.NotContains(t, rec.Body.String(), digest, "the response must not echo the digest")
	}
	assert.Empty(t, svc.verified, "a token failure must not reach the verifier")
}

// TestGatewayVerifyReturnsVerdictAndLimits covers the happy path and that
// the limits the plugin enforces locally travel on the response.
func TestGatewayVerifyReturnsVerdictAndLimits(t *testing.T) {
	svc := &fakeGatewayService{verifyFn: func(_ context.Context, req *GatewayVerifyRequest) (*GatewayVerifyResponse, error) {
		assert.Equal(t, "qwen2.5-0.5b", req.Model, "the served model must reach the verifier")
		assert.Equal(t, "10.0.0.9", req.SourceIP)
		return &GatewayVerifyResponse{
			Allowed:                   true,
			OrganizationID:            "org-1",
			KeyID:                     "key-1",
			Role:                      "member",
			RateLimitRPM:              60,
			MaxOutputTokensPerRequest: 512,
		}, nil
	}}
	mux := newGatewayTestServer(t, svc, GatewayPluginOptions{Enabled: true})

	rec := postJSON(mux, gatewayVerifyPath, testGatewayToken,
		`{"key_digest":"`+strings.Repeat("b", 64)+`","model":"qwen2.5-0.5b","source_ip":"10.0.0.9"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp GatewayVerifyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Allowed)
	assert.Equal(t, "org-1", resp.OrganizationID)
	assert.EqualValues(t, 60, resp.RateLimitRPM)
	assert.EqualValues(t, 512, resp.MaxOutputTokensPerRequest)
}

// TestGatewayVerifyRejectionIsA200WithAVerdict pins the distinction the
// plugin relies on: "the key is bad" is a successful transport carrying
// allowed=false, while "the platform could not answer" is a 503. Only the
// second one makes the plugin fail closed on infrastructure rather than
// reject the caller.
func TestGatewayVerifyRejectionIsA200WithAVerdict(t *testing.T) {
	svc := &fakeGatewayService{verifyFn: func(context.Context, *GatewayVerifyRequest) (*GatewayVerifyResponse, error) {
		return &GatewayVerifyResponse{
			Allowed: false,
			Code:    int(apierrors.CodeAPIKeyInvalid),
			Message: "invalid api key",
		}, nil
	}}
	mux := newGatewayTestServer(t, svc, GatewayPluginOptions{Enabled: true})

	rec := postJSON(mux, gatewayVerifyPath, testGatewayToken, `{"key_digest":"`+strings.Repeat("c", 64)+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, "a rejected key is a verdict, not a transport failure")

	var resp GatewayVerifyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.False(t, resp.Allowed)
	assert.Equal(t, int(apierrors.CodeAPIKeyInvalid), resp.Code)
}

// TestGatewayVerifyInfrastructureFailureIs503 covers the fail-closed path:
// the plugin cannot tell a good key from a bad one, so it must not guess.
func TestGatewayVerifyInfrastructureFailureIs503(t *testing.T) {
	svc := &fakeGatewayService{verifyFn: func(context.Context, *GatewayVerifyRequest) (*GatewayVerifyResponse, error) {
		return nil, errors.New("redis down")
	}}
	mux := newGatewayTestServer(t, svc, GatewayPluginOptions{Enabled: true})

	rec := postJSON(mux, gatewayVerifyPath, testGatewayToken, `{"key_digest":"`+strings.Repeat("d", 64)+`"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "unavailable")
	assert.NotContains(t, rec.Body.String(), "redis", "an internal cause must not leak to the caller")
}

// TestGatewayVerifyRejectsMalformedInput covers the 400 paths.
func TestGatewayVerifyRejectsMalformedInput(t *testing.T) {
	mux := newGatewayTestServer(t, &fakeGatewayService{}, GatewayPluginOptions{Enabled: true})

	rec := postJSON(mux, gatewayVerifyPath, testGatewayToken, `{not json`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = postJSON(mux, gatewayVerifyPath, testGatewayToken, ``)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	req := httptest.NewRequest(http.MethodGet, gatewayVerifyPath, nil)
	req.Header.Set(gatewayTokenHeader, testGatewayToken)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

// TestGatewayUsageRecordsOnce proves the sink is handed the full event,
// including the four token classes the ledger prices separately.
func TestGatewayUsageRecordsOnce(t *testing.T) {
	svc := &fakeGatewayService{}
	mux := newGatewayTestServer(t, svc, GatewayPluginOptions{Enabled: true})

	body := `{"request_id":"req-1","organization_id":"org-1","api_key_id":"key-1",` +
		`"model":"qwen2.5-0.5b","usage":{"prompt_tokens":11,"completion_tokens":22,` +
		`"cached_tokens":3,"reasoning_tokens":4},"completed_at":1700000000,` +
		`"latency_ms":900,"ttft_ms":120,"generation_ms":780,"status":"success"}`
	rec := postJSON(mux, gatewayUsagePath, testGatewayToken, body)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "voucher-1")

	require.Len(t, svc.recorded, 1)
	assert.Equal(t, "req-1", svc.recorded[0].RequestID)
	assert.EqualValues(t, 11, svc.recorded[0].Usage.PromptTokens)
	assert.EqualValues(t, 22, svc.recorded[0].Usage.CompletionTokens)
	assert.EqualValues(t, 3, svc.recorded[0].Usage.CachedTokens)
	assert.EqualValues(t, 4, svc.recorded[0].Usage.ReasoningTokens)
}

// TestGatewayUsageWithoutRequestIDIsRejected keeps the idempotency key
// mandatory: an event without it cannot be deduplicated, so ingesting it
// would risk double billing.
func TestGatewayUsageWithoutRequestIDIsRejected(t *testing.T) {
	svc := &fakeGatewayService{}
	mux := newGatewayTestServer(t, svc, GatewayPluginOptions{Enabled: true})

	rec := postJSON(mux, gatewayUsagePath, testGatewayToken,
		`{"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, svc.recorded)
}

// TestGatewayUsageFailureIs503AndCounted pins the metering posture: the
// platform does not hide a lost usage event, and the response tells the
// plugin so it can count it too.
func TestGatewayUsageFailureIs503AndCounted(t *testing.T) {
	before := testutil.ToFloat64(gatewayUsageDropped)
	svc := &fakeGatewayService{usageFn: func(context.Context, *GatewayUsageRequest) (string, error) {
		return "", errors.New("broker down")
	}}
	mux := newGatewayTestServer(t, svc, GatewayPluginOptions{Enabled: true})

	rec := postJSON(mux, gatewayUsagePath, testGatewayToken,
		`{"request_id":"req-2","usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Greater(t, testutil.ToFloat64(gatewayUsageDropped), before,
		"a dropped usage event must be visible in metrics")
}

// TestGatewayPluginArtifactIsServed covers the /plugin.wasm path: the
// artifact is served with the right content type, and a missing file is a
// clear 404 rather than a panic or an empty 200.
func TestGatewayPluginArtifactIsServed(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "gateway-plugin.wasm")
	require.NoError(t, os.WriteFile(artifact, []byte("\x00asm-fake"), 0o600))
	mux := newGatewayTestServer(t, &fakeGatewayService{},
		GatewayPluginOptions{Enabled: true, PluginPath: artifact})

	// Envoy's Wasm code source cannot send a header, so the artifact is
	// deliberately reachable without the token.
	req := httptest.NewRequest(http.MethodGet, gatewayPluginPath, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/wasm", rec.Header().Get("Content-Type"))
	assert.Equal(t, "\x00asm-fake", rec.Body.String())
}

// TestGatewayPluginArtifactMissingIs404 covers a build without the
// artifact: the platform keeps working and the failure is explicit.
func TestGatewayPluginArtifactMissingIs404(t *testing.T) {
	mux := newGatewayTestServer(t, &fakeGatewayService{},
		GatewayPluginOptions{Enabled: true, PluginPath: filepath.Join(t.TempDir(), "absent.wasm")})

	req := httptest.NewRequest(http.MethodGet, gatewayPluginPath, nil)
	req.Header.Set(gatewayTokenHeader, testGatewayToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "not built")
}

// TestTokenMatches is the constant-time comparison, including the case
// where nothing is configured (an empty expectation must never match).
func TestTokenMatches(t *testing.T) {
	assert.False(t, tokenMatches("", ""))
	assert.False(t, tokenMatches("abc", ""))
	assert.False(t, tokenMatches("", "abc"))
	assert.True(t, tokenMatches("abc", "abc"))
	assert.False(t, tokenMatches("abd", "abc"))
}

// TestGatewayTokenUsageEmpty separates "no tokens measured" from
// "measured zero", so the sink can skip an event that would only add
// noise to the ledger.
func TestGatewayTokenUsageEmpty(t *testing.T) {
	assert.True(t, GatewayTokenUsage{}.Empty())
	assert.False(t, GatewayTokenUsage{PromptTokens: 1}.Empty())
	assert.False(t, GatewayTokenUsage{ReasoningTokens: 1}.Empty())
}
