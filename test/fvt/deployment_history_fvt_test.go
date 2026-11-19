package fvt

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFVTDeploymentHistory walks the acceptance criteria of the
// deployment history & audit architecture doc (feature #34): AC1
// (ListDeploymentEvents), AC2 (field-level diff), AC3
// (RollbackDeployment).
func TestFVTDeploymentHistory(t *testing.T) {
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

	// Create a service at v1 with 2 replicas → records a create event.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "demo-svc", "modelId": modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": "2", "accelerator": "nvidia",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	serviceID, _ := body["serviceId"].(string)
	require.NotEmpty(t, serviceID)

	// Drive to running.
	env.reportStatus(t, serviceID, "running", []string{"https://infer.example.com/demo/v1"}, nil)

	// AC1: ListDeploymentEvents returns the trail newest first with
	// service_id/service_name/event_type/actor/before-after/created_at.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/deployments/events?service_id="+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	events, _ := body["events"].([]any)
	require.Len(t, events, 1, "one create event")
	ev := events[0].(map[string]any)
	assert.Equal(t, serviceID, ev["serviceId"])
	assert.Equal(t, "demo-svc", ev["serviceName"])
	assert.Equal(t, "create", ev["eventType"])
	assert.Equal(t, "org-fvt", ev["actor"])
	assert.Equal(t, "{}", ev["before"])
	assert.Contains(t, ev["after"], "replicas")
	assert.NotEmpty(t, ev["createdAt"])

	// AC1: unknown service_id → 10301.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/deployments/events?service_id=does-not-exist", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown service must be rejected: %v", body)
	assert.Equal(t, float64(10301), body["code"])

	// AC2: scale records a field-level diff (replicas 2 → 4).
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services/"+serviceID+":scale", map[string]any{
		"replicas": "4",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/deployments/events?service_id="+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	events, _ = body["events"].([]any)
	require.Len(t, events, 2, "create + scale")
	scaleEv := events[0].(map[string]any)
	assert.Equal(t, "scale", scaleEv["eventType"])
	assert.Contains(t, scaleEv["before"], "2")
	assert.Contains(t, scaleEv["after"], "4")

	// AC2: event-type filter shows only matching events.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/deployments/events?service_id="+serviceID+"&event_type=scale", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	events, _ = body["events"].([]any)
	require.Len(t, events, 1)
	assert.Equal(t, "scale", events[0].(map[string]any)["eventType"])

	// AC3: RollbackDeployment reverts to the scale event's before
	// (replicas 2), keeps service_id, transitions to deploying, and
	// records a new rollback event.
	scaleEventID, _ := scaleEv["eventId"].(string)
	env.mqBus.changes = nil
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/deployments/"+serviceID+":rollback", map[string]any{
		"eventId": scaleEventID,
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, serviceID, body["serviceId"])
	assert.Equal(t, "deploying", body["state"])

	// A rollback change event was published.
	require.Len(t, env.mqBus.changes, 1)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(env.mqBus.changes[0].Body, &evt))
	assert.Equal(t, "rollback", evt["event_type"])
	assert.Equal(t, serviceID, evt["service_id"])
	assert.Equal(t, float64(2), evt["replicas"], "rolled back to before replicas")

	// A new rollback event appears in the trail.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/deployments/events?service_id="+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	events, _ = body["events"].([]any)
	require.Len(t, events, 3, "create + scale + rollback")
	assert.Equal(t, "rollback", events[0].(map[string]any)["eventType"])

	// AC3: unknown event_id → 10304.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/deployments/"+serviceID+":rollback", map[string]any{
		"eventId": "does-not-exist",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown event must be rejected: %v", body)
	assert.Equal(t, float64(10304), body["code"])

	// AC3: unknown service_id → 10301.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/deployments/does-not-exist:rollback", map[string]any{
		"eventId": scaleEventID,
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown service must be rejected: %v", body)
	assert.Equal(t, float64(10301), body["code"])
}
