// The data-plane path for provider-backed (external) models: a caller posts a
// chat completion to the platform's OpenAI-compatible endpoint naming a model
// the platform sells from a provider, and gets an answer instead of "model does
// not exist".
//
// The suite wires the real catalog, the real provider module and the real model
// router. The control plane's two internal endpoints are stood up over the
// provider service, because that is all they do: list the provider-backed
// names, and proxy one chat completion. The serving gateway is replaced by a
// recording double, which is where a self-hosted request must land instead.

package fvt

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/modelrouter"
)

// dataPlaneToken is the shared secret the internal endpoints require.
const dataPlaneToken = "fvt-data-plane-token"

// dataPlaneEnv is the router with its two upstreams.
type dataPlaneEnv struct {
	router       *modelrouter.Router
	controlPlane *httptest.Server
	gateway      *httptest.Server

	gatewayCalls atomic.Int64
	gatewayPath  atomic.Value
	gatewayBody  atomic.Value
}

// newDataPlaneEnv starts the control plane's internal endpoints over the real
// provider service and a recording serving gateway.
func newDataPlaneEnv(t *testing.T, env *providerFvtEnv) *dataPlaneEnv {
	t.Helper()
	dp := &dataPlaneEnv{}
	dp.gatewayPath.Store("")
	dp.gatewayBody.Store("")

	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(modelrouter.GatewayTokenHeader) != dataPlaneToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case modelrouter.ExternalModelsPath:
			names, err := env.svc.ListExternalModelNames(r.Context())
			require.NoError(t, err)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"models": names})
		case modelrouter.ExternalChatPath:
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var payload struct {
				Model string `json:"model"`
			}
			require.NoError(t, json.Unmarshal(body, &payload))
			out, err := env.svc.ProxyExternalChatForGateway(r.Context(), payload.Model, body)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "model_not_found"}})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(out)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(controlPlane.Close)

	dp.gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dp.gatewayCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		dp.gatewayPath.Store(r.URL.Path)
		dp.gatewayBody.Store(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-selfhosted"}`))
	}))
	t.Cleanup(dp.gateway.Close)

	dp.controlPlane = controlPlane
	router, err := modelrouter.New(modelrouter.Config{
		ServerURL:   controlPlane.URL,
		ServerToken: dataPlaneToken,
		GatewayURL:  dp.gateway.URL,
	})
	require.NoError(t, err)
	require.NoError(t, router.Refresh(context.Background()))
	dp.router = router
	return dp
}

// chat posts a chat completion through the router and returns the body.
func (e *dataPlaneEnv) chat(t *testing.T, model string) (int, string) {
	t.Helper()
	srv := httptest.NewServer(e.router.Handler())
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(http.MethodPost, srv.URL+modelrouter.ChatCompletionsPath,
		strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

// TestExternalModelDataPlaneServesProviderModels covers the fix: a published
// provider model is answered on the OpenAI-compatible endpoint, and a model an
// inference service serves still reaches the serving gateway.
func TestExternalModelDataPlaneServesProviderModels(t *testing.T) {
	env := newProviderFvtEnv(t)
	providerID := env.createProvider(t, "acme", env.okServer.URL, true)
	_, catalogID := env.addAndPublish(t, providerID, "gpt-4o", "deepseek-flash")
	require.NotEmpty(t, catalogID)
	dp := newDataPlaneEnv(t, env)

	// The provider-backed model is answered by the provider, not the gateway.
	code, body := dp.chat(t, "deepseek-flash")
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, "The answer is 42.")
	assert.Zero(t, dp.gatewayCalls.Load(), "a provider model never reaches the serving gateway")

	// A model no provider backs keeps the old path.
	code, body = dp.chat(t, "qwen")
	require.Equal(t, http.StatusOK, code, body)
	assert.Contains(t, body, "chatcmpl-selfhosted")
	assert.Equal(t, int64(1), dp.gatewayCalls.Load())
	assert.Equal(t, modelrouter.ChatCompletionsPath, dp.gatewayPath.Load())
	assert.Contains(t, dp.gatewayBody.Load().(string), `"qwen"`)
}

// TestExternalModelDataPlaneFollowsPublication covers the catalog contract: a
// model stops being provider-backed once it is unpublished, without the router
// being restarted.
func TestExternalModelDataPlaneFollowsPublication(t *testing.T) {
	env := newProviderFvtEnv(t)
	providerID := env.createProvider(t, "acme", env.okServer.URL, true)
	providerModelID, _ := env.addAndPublish(t, providerID, "gpt-4o", "deepseek-flash")
	dp := newDataPlaneEnv(t, env)

	code, _ := dp.chat(t, "deepseek-flash")
	require.Equal(t, http.StatusOK, code)

	// Unpublish: the next refresh drops the name and the request falls back to
	// the serving gateway, which is where an unknown model goes.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/providers/"+providerID+"/models/"+providerModelID+":unpublish", nil, nil)
	require.Equal(t, http.StatusOK, code, "%v", body)
	require.NoError(t, dp.router.Refresh(context.Background()))

	code, _ = dp.chat(t, "deepseek-flash")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, int64(1), dp.gatewayCalls.Load(), "an unpublished model is no longer provider-backed")
}

// TestExternalModelDataPlaneRequiresTheSharedSecret covers the boundary: the
// provider endpoints answer only the router.
func TestExternalModelDataPlaneRequiresTheSharedSecret(t *testing.T) {
	env := newProviderFvtEnv(t)
	providerID := env.createProvider(t, "acme", env.okServer.URL, true)
	env.addAndPublish(t, providerID, "gpt-4o", "deepseek-flash")

	// A router with the wrong secret cannot load the model list, so nothing is
	// treated as provider-backed and traffic flows to the gateway untouched.
	dp := newDataPlaneEnv(t, env)
	require.NoError(t, dp.router.Refresh(context.Background()))

	badRouter, err := modelrouter.New(modelrouter.Config{
		ServerURL:   dp.controlPlane.URL,
		ServerToken: "wrong",
		GatewayURL:  dp.gateway.URL,
	})
	require.NoError(t, err)
	assert.Error(t, badRouter.Refresh(context.Background()), "a wrong secret must not be accepted")
}
