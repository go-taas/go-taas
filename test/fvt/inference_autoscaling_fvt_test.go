package fvt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/mq"
)

// TestFVTInferenceAutoscaling walks the acceptance criteria of the
// inference autoscaling & scale-to-zero architecture doc: AC1 (global
// default save + inherit), AC2 (validation publishes nothing), AC3
// (create with explicit policy stores it; get returns policy + status),
// AC4 (per-service update async), AC5 (disabling returns to fixed count),
// AC8 (admin list/detail show autoscaling), AC9 (user model list/detail
// read-only summary).
func TestFVTInferenceAutoscaling(t *testing.T) {
	env := newModelInferEnv(t)

	// Register a model and deploy a service.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-3b", "version": "v1", "weightPath": "qwen/3b/v1",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	modelID, _ := body["modelId"].(string)

	// AC1: the global default policy exists and is readable.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/autoscaling/policy", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	policy, _ := body["policy"].(map[string]any)
	require.NotNil(t, policy)
	assert.Equal(t, true, policy["enabled"])
	assert.EqualValues(t, float64(1), policy["minReplicas"])
	assert.EqualValues(t, float64(10), policy["maxReplicas"])
	assert.EqualValues(t, float64(32), policy["targetConcurrency"])
	assert.EqualValues(t, float64(300), policy["cooldownSeconds"])

	// AC1: update the global default.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/autoscaling/policy", map[string]any{
		"policy": map[string]any{
			"enabled": true, "minReplicas": 2, "maxReplicas": 20,
			"targetConcurrency": 64, "scaleToZero": false, "cooldownSeconds": 600,
		},
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/autoscaling/policy", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	policy, _ = body["policy"].(map[string]any)
	assert.EqualValues(t, float64(2), policy["minReplicas"])
	assert.EqualValues(t, float64(20), policy["maxReplicas"])

	// AC2: an invalid policy is rejected and publishes nothing.
	before := len(env.mqBus.changes)
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/autoscaling/policy", map[string]any{
		"policy": map[string]any{
			"enabled": true, "minReplicas": 20, "maxReplicas": 2,
			"targetConcurrency": 32, "scaleToZero": false, "cooldownSeconds": 300,
		},
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "invalid policy must be rejected: %v", body)
	assert.Len(t, env.mqBus.changes, before, "no event on validation failure (AC2)")

	// AC3: create a service without an explicit policy inherits the
	// global default.
	env.mqBus.changes = nil
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "inherit-svc", "modelId": modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": "2", "accelerator": "nvidia",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	inheritID, _ := body["serviceId"].(string)
	require.NotEmpty(t, inheritID)

	// The change event carries the inherited (global default) policy.
	require.Len(t, env.mqBus.changes, 1)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(env.mqBus.changes[0].Body, &evt))
	as, ok := evt["autoscaling"].(map[string]any)
	require.True(t, ok, "change event must carry autoscaling")
	assert.EqualValues(t, float64(2), as["min_replicas"])
	assert.EqualValues(t, float64(20), as["max_replicas"])

	// AC3: get returns the effective (inherited) policy.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+inheritID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	autoscaling, _ := body["autoscaling"].(map[string]any)
	require.NotNil(t, autoscaling)
	assert.EqualValues(t, float64(20), autoscaling["maxReplicas"])

	// AC3: create a service with an explicit policy stores it.
	env.mqBus.changes = nil
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "explicit-svc", "modelId": modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": "2", "accelerator": "nvidia",
		"autoscaling": map[string]any{
			"enabled": true, "minReplicas": 1, "maxReplicas": 8,
			"targetConcurrency": 48, "scaleToZero": false, "cooldownSeconds": 200,
		},
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	explicitID, _ := body["serviceId"].(string)
	require.NotEmpty(t, explicitID)

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+explicitID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	autoscaling, _ = body["autoscaling"].(map[string]any)
	require.NotNil(t, autoscaling)
	assert.EqualValues(t, float64(8), autoscaling["maxReplicas"])
	assert.EqualValues(t, float64(48), autoscaling["targetConcurrency"])

	// AC4: per-service update is async and returns immediately.
	env.mqBus.changes = nil
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services/"+explicitID+":autoscaling", map[string]any{
		"policy": map[string]any{
			"enabled": true, "minReplicas": 1, "maxReplicas": 12,
			"targetConcurrency": 32, "scaleToZero": false, "cooldownSeconds": 300,
		},
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Len(t, env.mqBus.changes, 1, "one change event on per-service update")

	// The summary reflects the new max.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	services, _ := body["services"].([]any)
	require.Len(t, services, 2)
	for _, s := range services {
		svc := s.(map[string]any)
		if svc["serviceId"] == explicitID {
			assert.Equal(t, true, svc["autoscalingEnabled"])
			assert.EqualValues(t, float64(12), svc["maxReplicas"])
		}
	}

	// AC5: disabling returns to a fixed count.
	env.mqBus.changes = nil
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services/"+explicitID+":autoscaling", map[string]any{
		"policy": map[string]any{
			"enabled": false, "minReplicas": 1, "maxReplicas": 12,
			"targetConcurrency": 32, "scaleToZero": false, "cooldownSeconds": 300,
		},
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, float64(2), body["fixedReplicas"], "fixed count equals current desired (AC5)")

	// AC8: the admin list shows the autoscaling column.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	services, _ = body["services"].([]any)
	require.Len(t, services, 2)
	for _, s := range services {
		svc := s.(map[string]any)
		if svc["serviceId"] == explicitID {
			assert.Equal(t, false, svc["autoscalingEnabled"])
		}
		if svc["serviceId"] == inheritID {
			assert.Equal(t, true, svc["autoscalingEnabled"])
		}
	}

	// AC9: the user model list carries the autoscaling projection.
	// Drive the inherit service (autoscaling enabled) to running with
	// autoscaling status.
	env.reportStatusWithAutoscaling(t, inheritID, "running", []string{"https://infer.example.com/inherit/v1"}, nil, map[string]any{
		"state": "steady", "current_replicas": 2, "desired_replicas": 2,
		"current_concurrency": 5, "target_concurrency": 64,
		"last_scaling_event_at": time.Now().UTC().Format(time.RFC3339), "error_reason": "",
	})

	code, body = env.call(t, http.MethodGet, "/api/v1/models", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	models, _ := body["models"].([]any)
	require.Len(t, models, 1)
	m := models[0].(map[string]any)
	ma, ok := m["autoscaling"].(map[string]any)
	require.True(t, ok, "user model list must carry autoscaling projection")
	assert.Equal(t, true, ma["autoscaled"])
	assert.EqualValues(t, float64(2), ma["currentReplicas"])

	// AC9: the user model detail returns the read-only summary.
	code, body = env.call(t, http.MethodGet, "/api/v1/models/"+modelID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	detailModel, _ := body["model"].(map[string]any)
	require.NotNil(t, detailModel)
	assert.Equal(t, modelID, detailModel["modelId"])
	detailAS, _ := body["autoscaling"].(map[string]any)
	require.NotNil(t, detailAS)
	assert.Equal(t, "steady", detailAS["state"])
}

// reportStatusWithAutoscaling publishes a synthetic controller status
// report with an autoscaling status block.
func (e *modelInferEnv) reportStatusWithAutoscaling(t *testing.T, serviceID, state string, endpoints []string, reason *string, autoscaling map[string]any) {
	t.Helper()
	payload := map[string]any{
		"service_id":     serviceID,
		"state":          state,
		"endpoints":      endpoints,
		"failure_reason": reason,
		"reported_at":    time.Now().UTC().Format(time.RFC3339),
	}
	if autoscaling != nil {
		payload["autoscaling"] = autoscaling
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, e.mqBus.Publish(context.Background(), mq.DefaultSubjects().InferServiceStatus, body, nil))
}

// TestFVTInferenceAutoscalingSurfaceSeparation verifies the admin and
// user autoscaling endpoints are on their correct surfaces.
func TestFVTInferenceAutoscalingSurfaceSeparation(t *testing.T) {
	env := newModelInferEnv(t)

	// The admin policy endpoint is under /api/v1/admin.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/autoscaling/policy", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// The user model detail endpoint is under /api/v1.
	code, body = env.call(t, http.MethodGet, "/api/v1/models/does-not-exist", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown model must be rejected: %v", body)
	_ = fmt.Sprint(body)
}
