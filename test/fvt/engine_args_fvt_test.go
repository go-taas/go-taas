package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registerFVTCreateModel registers a model and returns its id.
func registerFVTCreateModel(t *testing.T, env *modelInferEnv, name string) string {
	t.Helper()
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": name, "version": "v1", "weightPath": name + "/v1/",
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	id, _ := body["modelId"].(string)
	require.NotEmpty(t, id)
	return id
}

// TestFVTEngineArgsAccepted verifies the create-service API accepts the
// engine parameters and the free-form arguments, so the controller can
// build the engine command line from them.
func TestFVTEngineArgsAccepted(t *testing.T) {
	env := newModelInferEnv(t)
	modelID := registerFVTCreateModel(t, env, "args-model")

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name":            "args-svc",
		"modelId":         modelID,
		"modelVersion":    "v1",
		"imageId":         "img-vllm-nvidia",
		"replicas":        "1",
		"accelerator":     "nvidia",
		"acceleratorType": "A100",
		"engineArgs": map[string]any{
			"max-model-len":          "8192",
			"gpu-memory-utilization": "0.9",
			"enable-prefix-caching":  "flag:true",
		},
		"extraArgs": []any{"--seed=7"},
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	serviceID, _ := body["serviceId"].(string)
	require.NotEmpty(t, serviceID)

	// The service exists and waits for the controller.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	service, _ := body["service"].(map[string]any)
	require.NotNil(t, service)
	assert.Equal(t, "pending", service["state"], "a new service waits for the controller")
}

// TestFVTEngineArgsRejected verifies a flag name that could break the
// command line is refused with the engine-args code before any write.
func TestFVTEngineArgsRejected(t *testing.T) {
	env := newModelInferEnv(t)
	modelID := registerFVTCreateModel(t, env, "bad-args-model")

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name":         "bad-args-svc",
		"modelId":      modelID,
		"modelVersion": "v1",
		"imageId":      "img-vllm-nvidia",
		"replicas":     "1",
		"accelerator":  "nvidia",
		"engineArgs":   map[string]any{"--max-model-len": "8192"},
	}, "org-fvt")
	// A business error comes back as an HTTP 500 with the code in the
	// envelope, like every other business rejection in this platform.
	require.Equal(t, http.StatusInternalServerError, code, "body: %v", body)
	assert.EqualValues(t, 10318, body["code"], "body: %v", body)
}
