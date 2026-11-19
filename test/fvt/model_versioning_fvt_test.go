package fvt

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFVTModelVersioning walks the acceptance criteria of the model
// versioning & rollback architecture doc (feature #32): AC1
// (ListModelVersions), AC2 (ActivateModelVersion), AC3
// (UpdateInferenceServiceVersion).
func TestFVTModelVersioning(t *testing.T) {
	env := newModelInferEnv(t)

	// Register a model with two versions.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-3b", "hubModelId": "qwen/qwen-3b", "version": "v1", "weightPath": "qwen/3b/v1",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	modelID, _ := body["modelId"].(string)
	require.NotEmpty(t, modelID)

	code, body = env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-3b", "hubModelId": "qwen/qwen-3b", "version": "v2", "weightPath": "qwen/3b/v2",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// AC1: ListModelVersions returns name/model_id/active_version and
	// versions newest first with weight_path/created_at/is_active/
	// deployment_count.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models/"+modelID+"/versions", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, modelID, body["modelId"])
	assert.Equal(t, "qwen-3b", body["name"])
	assert.Equal(t, "", body["activeVersion"], "no active version yet")
	versions, _ := body["versions"].([]any)
	require.Len(t, versions, 2)
	v0 := versions[0].(map[string]any)
	v1 := versions[1].(map[string]any)
	assert.Equal(t, "v2", v0["version"], "newest first")
	assert.Equal(t, "qwen/3b/v2", v0["weightPath"])
	assert.Equal(t, false, v0["isActive"])
	assert.Equal(t, "0", v0["deploymentCount"])
	assert.Equal(t, "v1", v1["version"])

	// AC1: unknown model_id → 10101.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models/does-not-exist/versions", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown model must be rejected: %v", body)
	assert.Equal(t, float64(10101), body["code"])

	// AC2: ActivateModelVersion sets v1 active, clears previous, one
	// active per model.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/models/"+modelID+"/versions/v1:activate", map[string]any{}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "v1", body["activeVersion"])

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models/"+modelID+"/versions", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "v1", body["activeVersion"])
	versions, _ = body["versions"].([]any)
	v0 = versions[0].(map[string]any)
	v1 = versions[1].(map[string]any)
	assert.Equal(t, false, v0["isActive"], "v2 not active")
	assert.Equal(t, true, v1["isActive"], "v1 active")

	// AC2: activating the already-active version is a no-op success.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/models/"+modelID+"/versions/v1:activate", map[string]any{}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "v1", body["activeVersion"])

	// AC2: activating v2 clears v1 (one active per model).
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/models/"+modelID+"/versions/v2:activate", map[string]any{}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "v2", body["activeVersion"])
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models/"+modelID+"/versions", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	versions, _ = body["versions"].([]any)
	v0 = versions[0].(map[string]any)
	v1 = versions[1].(map[string]any)
	assert.Equal(t, true, v0["isActive"], "v2 active")
	assert.Equal(t, false, v1["isActive"], "v1 cleared")

	// AC2: unknown version → 10103.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/models/"+modelID+"/versions/nope:activate", map[string]any{}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown version must be rejected: %v", body)
	assert.Equal(t, float64(10103), body["code"])

	// AC3: UpdateInferenceServiceVersion changes only model_version,
	// keeps service_id/endpoints, transitions running→deploying→running.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "demo-svc", "modelId": modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": "2", "accelerator": "nvidia",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	serviceID, _ := body["serviceId"].(string)
	require.NotEmpty(t, serviceID)

	// Drive the service to running.
	env.reportStatus(t, serviceID, "running", []string{"https://infer.example.com/demo/v1"}, nil)

	// Roll back to v2 in place.
	env.mqBus.changes = nil
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services/"+serviceID+":update-version", map[string]any{
		"modelVersion": "v2",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, serviceID, body["serviceId"])
	assert.Equal(t, "deploying", body["state"])

	// Exactly one version-change event was published.
	require.Len(t, env.mqBus.changes, 1)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(env.mqBus.changes[0].Body, &evt))
	assert.Equal(t, "update_version", evt["event_type"])
	assert.Equal(t, serviceID, evt["service_id"])
	assert.Equal(t, "v2", evt["model"].(map[string]any)["version"])

	// The service row reflects the new version and deploying state.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	svc, _ := body["service"].(map[string]any)
	assert.Equal(t, "v2", svc["modelVersion"])
	assert.Equal(t, "deploying", svc["state"])

	// The controller reports running on the target version.
	env.reportStatus(t, serviceID, "running", []string{"https://infer.example.com/demo/v1"}, nil)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	svc, _ = body["service"].(map[string]any)
	assert.Equal(t, "v2", svc["modelVersion"])
	assert.Equal(t, "running", svc["state"])

	// AC3: unknown service → 10301.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services/does-not-exist:update-version", map[string]any{
		"modelVersion": "v2",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown service must be rejected: %v", body)
	assert.Equal(t, float64(10301), body["code"])

	// AC3: unknown version → 10103.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services/"+serviceID+":update-version", map[string]any{
		"modelVersion": "nope",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown version must be rejected: %v", body)
	assert.Equal(t, float64(10103), body["code"])

	// AC3: terminated service → 10303.
	code, _ = env.call(t, http.MethodDelete, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services/"+serviceID+":update-version", map[string]any{
		"modelVersion": "v2",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "terminated service must be rejected: %v", body)
	assert.Equal(t, float64(10303), body["code"])
}
