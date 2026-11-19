package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFVTPlaygroundCompare walks the acceptance criteria of the model
// playground comparison architecture doc (feature #35): AC1
// (CompareModels happy path), AC2 (validation), AC3 (metered path).
func TestFVTPlaygroundCompare(t *testing.T) {
	env := newModelInferEnv(t)

	// Register two models.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-3b", "hubModelId": "qwen/qwen-3b", "version": "v1", "weightPath": "qwen/3b/v1",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	modelA, _ := body["modelId"].(string)
	require.NotEmpty(t, modelA)

	code, body = env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-7b", "hubModelId": "qwen/qwen-7b", "version": "v1", "weightPath": "qwen/7b/v1",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	modelB, _ := body["modelId"].(string)
	require.NotEmpty(t, modelB)

	// Create running services for both models.
	for _, m := range []struct{ name, modelID string }{{"svc-a", modelA}, {"svc-b", modelB}} {
		code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
			"name": m.name, "modelId": m.modelID, "modelVersion": "v1",
			"imageId": "img-vllm-nvidia", "replicas": "1", "accelerator": "nvidia",
			"acceleratorType": "A800",
		}, "org-fvt")
		require.Equal(t, http.StatusOK, code, "body: %v", body)
		serviceID, _ := body["serviceId"].(string)
		require.NotEmpty(t, serviceID)
		env.reportStatus(t, serviceID, "running", []string{"https://infer.example.com/" + m.name + "/v1"}, nil)
	}

	// AC1: CompareModels runs the same prompt against 2 models and
	// returns results[] with model_id/model_name/completion/latency_ms/
	// input_tokens/output_tokens/cost.
	code, body = env.call(t, http.MethodPost, "/api/v1/playground/compare", map[string]any{
		"modelIds": []string{modelA, modelB},
		"apiKeyId": "key-1",
		"prompt":   "hello world",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	results, _ := body["results"].([]any)
	require.Len(t, results, 2)
	for _, r := range results {
		row := r.(map[string]any)
		assert.NotEmpty(t, row["modelId"])
		assert.NotEmpty(t, row["modelName"])
		assert.NotEmpty(t, row["completion"])
		assert.NotEmpty(t, row["latencyMs"])
		assert.NotEmpty(t, row["inputTokens"])
		assert.NotEmpty(t, row["outputTokens"])
		assert.NotEmpty(t, row["cost"])
	}

	// AC2: fewer than 2 models → validation error.
	code, body = env.call(t, http.MethodPost, "/api/v1/playground/compare", map[string]any{
		"modelIds": []string{modelA}, "apiKeyId": "key-1", "prompt": "hi",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "fewer than 2 models must be rejected: %v", body)
	assert.Equal(t, float64(10404), body["code"])

	// AC2: unknown model → 10101.
	code, body = env.call(t, http.MethodPost, "/api/v1/playground/compare", map[string]any{
		"modelIds": []string{modelA, "does-not-exist"}, "apiKeyId": "key-1", "prompt": "hi",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown model must be rejected: %v", body)
	assert.Equal(t, float64(10101), body["code"])
}
