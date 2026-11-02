package modelrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upstreams holds the two stand-in upstreams and what they observed.
type upstreams struct {
	server  *httptest.Server
	gateway *httptest.Server

	models     atomic.Value // []string
	modelCalls atomic.Int64
	modelToken atomic.Value // string
	chatBody   atomic.Value // string
	chatCalls  atomic.Int64

	gatewayPath atomic.Value // string
	gatewayBody atomic.Value // string
	gatewayAuth atomic.Value // string
	gatewayCall atomic.Int64
}

// newUpstreams starts a control plane and a serving gateway double.
func newUpstreams(t *testing.T) *upstreams {
	t.Helper()
	u := &upstreams{}
	u.models.Store([]string{})
	u.modelToken.Store("")
	u.gatewayPath.Store("")
	u.gatewayBody.Store("")
	u.gatewayAuth.Store("")

	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case ExternalModelsPath:
			u.modelCalls.Add(1)
			u.modelToken.Store(r.Header.Get(GatewayTokenHeader))
			names, _ := u.models.Load().([]string)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"models": names})
		case ExternalChatPath:
			u.chatCalls.Add(1)
			body, _ := io.ReadAll(r.Body)
			u.chatBody.Store(string(body))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chatcmpl-provider","usage":{"prompt_tokens":9}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(u.server.Close)

	u.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.gatewayCall.Add(1)
		body, _ := io.ReadAll(r.Body)
		u.gatewayPath.Store(r.URL.Path)
		u.gatewayBody.Store(string(body))
		u.gatewayAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-selfhosted"}`))
	}))
	t.Cleanup(u.gateway.Close)
	return u
}

// newRouter builds a router wired to the doubles.
func newRouter(t *testing.T, u *upstreams) *Router {
	t.Helper()
	r, err := New(Config{
		ServerURL:      u.server.URL,
		ServerToken:    "shared-secret",
		GatewayURL:     u.gateway.URL,
		RequestTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	return r
}

// chatRequest builds a chat request naming model.
func chatRequest(model string) *http.Request {
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, ChatCompletionsPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-test")
	return req
}

// TestNewRequiresBothUpstreams covers configuration: a router without either
// upstream would silently break one class of model.
func TestNewRequiresBothUpstreams(t *testing.T) {
	_, err := New(Config{GatewayURL: "http://gateway"})
	require.Error(t, err)

	_, err = New(Config{ServerURL: "http://server"})
	require.Error(t, err)

	_, err = New(Config{ServerURL: "http://server", GatewayURL: "://bad"})
	require.Error(t, err)
}

// TestProviderModelGoesToTheControlPlane covers the fix: a model the platform
// sells from a provider is answered instead of failing at the router.
func TestProviderModelGoesToTheControlPlane(t *testing.T) {
	u := newUpstreams(t)
	u.models.Store([]string{"deepseek-flash"})
	r := newRouter(t, u)
	require.NoError(t, r.Refresh(context.Background()))

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, chatRequest("deepseek-flash"))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "chatcmpl-provider")
	assert.Equal(t, int64(1), u.chatCalls.Load())
	assert.Zero(t, u.gatewayCall.Load(), "a provider model never reaches the gateway")
	assert.Contains(t, mustString(u.chatBody.Load()), `"deepseek-flash"`)
}

// TestSelfHostedModelGoesToTheGateway covers the unchanged path: everything
// else is routed by the gateway exactly as before, headers included.
func TestSelfHostedModelGoesToTheGateway(t *testing.T) {
	u := newUpstreams(t)
	u.models.Store([]string{"deepseek-flash"})
	r := newRouter(t, u)
	require.NoError(t, r.Refresh(context.Background()))

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, chatRequest("qwen"))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "chatcmpl-selfhosted")
	assert.Equal(t, int64(1), u.gatewayCall.Load())
	assert.Zero(t, u.chatCalls.Load())
	assert.Equal(t, ChatCompletionsPath, mustString(u.gatewayPath.Load()))
	assert.Equal(t, "Bearer sk-test", mustString(u.gatewayAuth.Load()), "the caller's credentials are forwarded")
	assert.Contains(t, mustString(u.gatewayBody.Load()), `"qwen"`)
}

// TestUnknownCatalogLeavesTrafficAlone covers startup: before the model set is
// known, nothing is provider-backed, so self-hosted traffic flows untouched.
func TestUnknownCatalogLeavesTrafficAlone(t *testing.T) {
	u := newUpstreams(t)
	r := newRouter(t, u)

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, chatRequest("deepseek-flash"))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, int64(1), u.gatewayCall.Load())
}

// TestRefreshFailureKeepsThePreviousSet covers a control-plane hiccup: models
// that were provider-backed stay provider-backed.
func TestRefreshFailureKeepsThePreviousSet(t *testing.T) {
	u := newUpstreams(t)
	u.models.Store([]string{"deepseek-flash"})
	r := newRouter(t, u)
	require.NoError(t, r.Refresh(context.Background()))

	u.server.Close()
	assert.Error(t, r.Refresh(context.Background()))
	assert.True(t, r.isExternal("deepseek-flash"))
}

// TestRefreshReadsTheModelList covers the wire contract with the control
// plane, including the shared secret.
func TestRefreshReadsTheModelList(t *testing.T) {
	u := newUpstreams(t)
	u.models.Store([]string{"deepseek-flash", "gpt-4o-mini"})
	r := newRouter(t, u)
	require.NoError(t, r.Refresh(context.Background()))

	assert.ElementsMatch(t, []string{"deepseek-flash", "gpt-4o-mini"}, r.ExternalModels())
	assert.Equal(t, "shared-secret", mustString(u.modelToken.Load()))
}

// TestProviderUnreachableFailsClosed covers the provider hop: an unreachable
// control plane answers a 503 the SDK can act on instead of hanging.
func TestProviderUnreachableFailsClosed(t *testing.T) {
	u := newUpstreams(t)
	u.models.Store([]string{"deepseek-flash"})
	r := newRouter(t, u)
	require.NoError(t, r.Refresh(context.Background()))
	u.server.Close()

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, chatRequest("deepseek-flash"))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "model_unavailable")
}

// TestChatHandlerRejectsBadRequests covers the request shape errors.
func TestChatHandlerRejectsBadRequests(t *testing.T) {
	u := newUpstreams(t)
	r := newRouter(t, u)

	for _, tc := range []struct {
		name string
		req  *http.Request
		code string
	}{
		{"not post", httptest.NewRequest(http.MethodGet, ChatCompletionsPath, nil), "method_not_allowed"},
		{"not json", httptest.NewRequest(http.MethodPost, ChatCompletionsPath, strings.NewReader("x")), "invalid_body"},
		{"no model", httptest.NewRequest(http.MethodPost, ChatCompletionsPath, strings.NewReader(`{}`)), "model_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.Handler().ServeHTTP(rec, tc.req)
			assert.Contains(t, rec.Body.String(), tc.code)
			assert.Zero(t, u.gatewayCall.Load())
		})
	}
}

// TestHealthEndpoints covers the probes.
func TestHealthEndpoints(t *testing.T) {
	u := newUpstreams(t)
	r := newRouter(t, u)
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusOK, rec.Code, path)
	}
}

// TestRunStopsWithContext covers the refresh loop's shutdown.
func TestRunStopsWithContext(t *testing.T) {
	u := newUpstreams(t)
	u.models.Store([]string{"deepseek-flash"})
	r, err := New(Config{
		ServerURL:       u.server.URL,
		GatewayURL:      u.gateway.URL,
		RefreshInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	require.Eventually(t, func() bool { return u.modelCalls.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop")
	}
}

// mustString reads a value stored through atomic.Value.
func mustString(v any) string {
	s, _ := v.(string)
	return s
}
