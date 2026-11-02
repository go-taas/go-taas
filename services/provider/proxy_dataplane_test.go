package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	providerv1 "github.com/go-taas/go-taas/proto/taas/provider/v1"
)

// recordingPublisher is an mq.Client double that keeps the subjects it was
// asked to publish on.
type recordingPublisher struct {
	subjects []string
}

func (p *recordingPublisher) Publish(_ context.Context, subject string, _ []byte, _ map[string]string) error {
	p.subjects = append(p.subjects, subject)
	return nil
}

func (p *recordingPublisher) Subscribe(_ context.Context, _ string, _ mq.Handler) error { return nil }

func (p *recordingPublisher) Close() error { return nil }

// upstreamRecorder is a stand-in provider endpoint that records what the
// proxy sent and answers with a canned OpenAI-compatible body.
type upstreamRecorder struct {
	srv      *httptest.Server
	status   int
	body     string
	lastPath string
	lastAuth string
	lastBody map[string]any
}

func newUpstreamRecorder(t *testing.T, status int, body string) *upstreamRecorder {
	t.Helper()
	rec := &upstreamRecorder{status: status, body: body}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.lastPath = r.URL.Path
		rec.lastAuth = r.Header.Get("Authorization")
		rec.lastBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&rec.lastBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.status)
		_, _ = w.Write([]byte(rec.body))
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

// createProviderAt creates an enabled provider pointing at baseURL and
// registers a published external model served under name.
func createProviderAt(t *testing.T, svc *Service, cat *fakeCatalog, baseURL, name string) string {
	t.Helper()
	ctx := context.Background()
	resp, err := svc.CreateProvider(ctx, &providerv1.CreateProviderRequest{
		Name:    "acme",
		Type:    TypeOpenAI,
		BaseUrl: baseURL,
		ApiKey:  "sk-abcdef123456",
	})
	require.NoError(t, err)
	id := resp.GetProvider().GetProviderId()
	_, err = svc.UpdateProvider(ctx, &providerv1.UpdateProviderRequest{
		ProviderId: id, Name: "acme", Type: TypeOpenAI, BaseUrl: baseURL, Enabled: true,
	})
	require.NoError(t, err)
	_, err = cat.CreateExternalModel(ctx, name, id, "upstream-"+name)
	require.NoError(t, err)
	return id
}

// TestProxyExternalChatForGatewayForwardsByServedName covers the data-plane
// fallback: the caller names the catalog model, the proxy rewrites the
// request to the provider's upstream model id and returns the provider's
// body unchanged.
func TestProxyExternalChatForGatewayForwardsByServedName(t *testing.T) {
	upstream := newUpstreamRecorder(t, http.StatusOK,
		`{"id":"chatcmpl-1","model":"upstream-deepseek-flash","usage":{"prompt_tokens":7,"completion_tokens":5}}`)
	svc, cat := newProviderSvc(t)
	createProviderAt(t, svc, cat, upstream.srv.URL, "deepseek-flash")

	body, err := svc.ProxyExternalChatForGateway(context.Background(), "deepseek-flash",
		[]byte(`{"model":"deepseek-flash","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	assert.Contains(t, string(body), `"id":"chatcmpl-1"`)

	assert.Equal(t, "/chat/completions", upstream.lastPath)
	assert.Equal(t, "Bearer sk-abcdef123456", upstream.lastAuth)
	assert.Equal(t, "upstream-deepseek-flash", upstream.lastBody["model"],
		"the catalog name is never sent upstream")
}

// TestProxyExternalChatForGatewayUnknownModel covers a name that is not an
// external catalog model: the caller gets the not-published code, never an
// upstream call.
func TestProxyExternalChatForGatewayUnknownModel(t *testing.T) {
	upstream := newUpstreamRecorder(t, http.StatusOK, `{"id":"unused"}`)
	svc, cat := newProviderSvc(t)
	createProviderAt(t, svc, cat, upstream.srv.URL, "deepseek-flash")

	_, err := svc.ProxyExternalChatForGateway(context.Background(), "qwen",
		[]byte(`{"model":"qwen"}`))
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderNotPublished, apierrors.CodeOf(err))
	assert.Empty(t, upstream.lastPath, "no upstream call for an unknown name")
}

// TestProxyExternalChatForGatewayMapsUpstreamFailures covers the status
// translation: credentials rejected and everything else.
func TestProxyExternalChatForGatewayMapsUpstreamFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   apierrors.Code
	}{
		{"unauthorized", http.StatusUnauthorized, apierrors.CodeProviderUpstreamUnauthorized},
		{"forbidden", http.StatusForbidden, apierrors.CodeProviderUpstreamUnauthorized},
		{"server error", http.StatusInternalServerError, apierrors.CodeProviderUpstreamUnavailable},
		{"rate limited", http.StatusTooManyRequests, apierrors.CodeProviderUpstreamUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newUpstreamRecorder(t, tc.status, `{"error":"nope"}`)
			svc, cat := newProviderSvc(t)
			createProviderAt(t, svc, cat, upstream.srv.URL, "deepseek-flash")

			_, err := svc.ProxyExternalChatForGateway(context.Background(), "deepseek-flash",
				[]byte(`{"model":"deepseek-flash"}`))
			require.Error(t, err)
			assert.Equal(t, tc.code, apierrors.CodeOf(err))
		})
	}
}

// TestProxyExternalChatForGatewayEmitsNoMeteringEvent covers the no-double-
// count rule: the gateway plugin owns metering on the data plane.
func TestProxyExternalChatForGatewayEmitsNoMeteringEvent(t *testing.T) {
	upstream := newUpstreamRecorder(t, http.StatusOK,
		`{"id":"chatcmpl-1","usage":{"prompt_tokens":7,"completion_tokens":5}}`)
	svc, cat := newProviderSvc(t)
	createProviderAt(t, svc, cat, upstream.srv.URL, "deepseek-flash")
	pub := &recordingPublisher{}
	svc.SetPublisher(pub)

	_, err := svc.ProxyExternalChatForGateway(context.Background(), "deepseek-flash",
		[]byte(`{"model":"deepseek-flash"}`))
	require.NoError(t, err)
	assert.Empty(t, pub.subjects, "the data plane is metered by the gateway plugin")
}
