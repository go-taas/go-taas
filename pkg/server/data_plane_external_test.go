package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

const testExternalToken = "external-token"

// fakeExternalModels stands in for the provider module: it reports a fixed
// model set and records what it was asked to proxy.
type fakeExternalModels struct {
	names []string
	list  error
	resp  string
	err   error

	calls int
	model string
	body  string
}

func (f *fakeExternalModels) AttachToServer(_ *grpc.Server) {}

func (f *fakeExternalModels) ServiceName() string { return "external-models" }

func (f *fakeExternalModels) ListExternalModelNames(_ context.Context) ([]string, error) {
	if f.list != nil {
		return nil, f.list
	}
	return f.names, nil
}

func (f *fakeExternalModels) ProxyExternalChatForGateway(_ context.Context, modelName string, body []byte) ([]byte, error) {
	f.calls++
	f.model = modelName
	f.body = string(body)
	return []byte(f.resp), f.err
}

// newExternalTestServer returns the monitor mux with the provider-backed
// model endpoints registered behind the token.
func newExternalTestServer(t *testing.T, svc *fakeExternalModels) *http.ServeMux {
	t.Helper()
	srv, err := NewServer(&Options{
		Name:          "test",
		ServicePort:   1,
		MonitorPort:   2,
		GatewayPlugin: GatewayPluginOptions{Enabled: true, Token: testExternalToken},
	})
	require.NoError(t, err)
	srv.RegisterService(svc)
	cs := srv.(*commonServer)
	cs.initMonitor()
	cs.initGatewayAPI()
	return cs.monitorMux
}

// externalRequest issues one request to the monitor mux with the token.
func externalRequest(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(gatewayTokenHeader, testExternalToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestExternalModelsHandlerListsProviderBackedModels covers the catalog the
// router refreshes from.
func TestExternalModelsHandlerListsProviderBackedModels(t *testing.T) {
	mux := newExternalTestServer(t, &fakeExternalModels{names: []string{"deepseek-flash", "gpt-4o-mini"}})

	rec := externalRequest(t, mux, http.MethodGet, externalModelsPath, "")
	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		Models []string `json:"models"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, []string{"deepseek-flash", "gpt-4o-mini"}, payload.Models)
}

// TestExternalModelsHandlerFailsClosedOnCatalogError covers an unreachable
// catalog: the router must not mistake "unknown" for "nothing is external".
func TestExternalModelsHandlerFailsClosedOnCatalogError(t *testing.T) {
	mux := newExternalTestServer(t, &fakeExternalModels{
		list: apierrors.Newf(apierrors.CodeInternal, "db down"),
	})

	rec := externalRequest(t, mux, http.MethodGet, externalModelsPath, "")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// TestExternalEndpointsRequireTheGatewayToken covers the security boundary:
// without the shared secret neither endpoint answers, because both spend the
// platform's provider credentials.
func TestExternalEndpointsRequireTheGatewayToken(t *testing.T) {
	mux := newExternalTestServer(t, &fakeExternalModels{names: []string{"deepseek-flash"}, resp: `{"id":"x"}`})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, externalModelsPath},
		{http.MethodPost, externalChatPath},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"model":"deepseek-flash"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", tc.method, tc.path)
	}
}

// TestExternalChatHandlerForwardsToProvider covers the happy path: the body
// reaches the provider unchanged and the provider's body is returned as-is.
func TestExternalChatHandlerForwardsToProvider(t *testing.T) {
	proxy := &fakeExternalModels{resp: `{"id":"chatcmpl-1","usage":{"prompt_tokens":4}}`}
	mux := newExternalTestServer(t, proxy)

	rec := externalRequest(t, mux, http.MethodPost, externalChatPath,
		`{"model":"deepseek-flash","messages":[{"role":"user","content":"hi"}]}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, proxy.calls)
	assert.Equal(t, "deepseek-flash", proxy.model)
	assert.Contains(t, proxy.body, `"messages"`)
	assert.JSONEq(t, `{"id":"chatcmpl-1","usage":{"prompt_tokens":4}}`, rec.Body.String())
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

// TestExternalChatHandlerRejectsBadRequests covers the request-shape errors,
// which must never reach the provider.
func TestExternalChatHandlerRejectsBadRequests(t *testing.T) {
	cases := []struct {
		name string
		body string
		code string
	}{
		{"missing model", `{"messages":[]}`, "model_required"},
		{"not json", `not-json`, "invalid_body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy := &fakeExternalModels{resp: `{}`}
			mux := newExternalTestServer(t, proxy)
			rec := externalRequest(t, mux, http.MethodPost, externalChatPath, tc.body)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Zero(t, proxy.calls)
			var payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
			assert.Equal(t, tc.code, payload.Error.Code)
		})
	}
}

// TestExternalChatHandlerMapsPlatformErrors covers the error translation an
// OpenAI SDK relies on: a model the platform does not sell is a 404, a
// provider failure is a 5xx.
func TestExternalChatHandlerMapsPlatformErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not published", apierrors.New(apierrors.CodeProviderNotPublished), http.StatusNotFound, "model_not_found"},
		{"model not found", apierrors.New(apierrors.CodeModelNotFound), http.StatusNotFound, "model_not_found"},
		{"provider disabled", apierrors.New(apierrors.CodeProviderDisabled), http.StatusServiceUnavailable, "model_unavailable"},
		{"upstream rejected", apierrors.New(apierrors.CodeProviderUpstreamUnauthorized), http.StatusBadGateway, "upstream_unauthorized"},
		{"upstream down", apierrors.New(apierrors.CodeProviderUpstreamUnavailable), http.StatusServiceUnavailable, "model_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := newExternalTestServer(t, &fakeExternalModels{err: tc.err})
			rec := externalRequest(t, mux, http.MethodPost, externalChatPath,
				`{"model":"deepseek-flash","messages":[]}`)

			assert.Equal(t, tc.status, rec.Code)
			var payload struct {
				Error struct {
					Code    string `json:"code"`
					Type    string `json:"type"`
					Message string `json:"message"`
					Param   string `json:"param"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
			assert.Equal(t, tc.code, payload.Error.Code)
			assert.NotEmpty(t, payload.Error.Type)
			assert.NotEmpty(t, payload.Error.Message)
			assert.Equal(t, "model", payload.Error.Param)
		})
	}
}

// TestExternalChatHandlerRejectsWrongMethod covers the method guard.
func TestExternalChatHandlerRejectsWrongMethod(t *testing.T) {
	mux := newExternalTestServer(t, &fakeExternalModels{resp: `{}`})
	rec := externalRequest(t, mux, http.MethodGet, externalChatPath, "")
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
