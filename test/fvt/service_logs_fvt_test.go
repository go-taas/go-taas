package fvt

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/infer"
)

// fakeLogFetcher is a deterministic LogFetcher for the service-logs FVT.
type fakeLogFetcher struct {
	pods []infer.LogPod
	// windows maps "pod|container" to a fixed window.
	windows map[string]infer.LogWindow
}

func (f *fakeLogFetcher) ListServiceLogPods(_ context.Context, _ string) ([]infer.LogPod, error) {
	return f.pods, nil
}

func (f *fakeLogFetcher) GetServiceLogs(_ context.Context, _, pod, container string, _ int, _, _ string) (infer.LogWindow, error) {
	if w, ok := f.windows[pod+"|"+container]; ok {
		return w, nil
	}
	return infer.LogWindow{}, nil
}

// TestFVTServiceLogs walks the acceptance criteria of the inference
// service logs viewer architecture doc (feature #33): AC1
// (ListServiceLogPods), AC2 (GetServiceLogs bounded window), AC3
// (next_offset pagination).
func TestFVTServiceLogs(t *testing.T) {
	env := newModelInferEnv(t)

	// Register a model and create a service.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-3b", "hubModelId": "qwen/qwen-3b", "version": "v1", "weightPath": "qwen/3b/v1",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	modelID, _ := body["modelId"].(string)

	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "demo-svc", "modelId": modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": "2", "accelerator": "nvidia",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	serviceID, _ := body["serviceId"].(string)
	require.NotEmpty(t, serviceID)

	// Wire a fake LogFetcher.
	env.inferSvc.SetLogFetcher(&fakeLogFetcher{
		pods: []infer.LogPod{
			{ReplicaIndex: "replica-1", Container: "engine", State: "Running"},
			{ReplicaIndex: "replica-2", Container: "engine", State: "Running"},
		},
		windows: map[string]infer.LogWindow{
			"replica-1|engine": {
				Lines: []infer.LogLine{
					{Timestamp: 200, Level: "info", Message: "started"},
					{Timestamp: 100, Level: "error", Message: "boom"},
				},
				NextOffset: "replica-1|engine|100",
				HasMore:    true,
			},
		},
	})

	// AC1: ListServiceLogPods returns masked replica indices.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/services/"+serviceID+"/logs/pods", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	pods, _ := body["pods"].([]any)
	require.Len(t, pods, 2)
	p0 := pods[0].(map[string]any)
	assert.Equal(t, "replica-1", p0["replicaIndex"])
	assert.Equal(t, "engine", p0["container"])
	assert.Equal(t, "Running", p0["state"])

	// AC1: unknown service → 10301.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/services/does-not-exist/logs/pods", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown service must be rejected: %v", body)
	assert.Equal(t, float64(10301), body["code"])

	// AC2: GetServiceLogs returns a bounded window.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/services/"+serviceID+"/logs?pod=replica-1&container=engine&tail=500", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	lines, _ := body["lines"].([]any)
	require.Len(t, lines, 2)
	l0 := lines[0].(map[string]any)
	assert.Equal(t, "200", l0["timestamp"])
	assert.Equal(t, "info", l0["level"])
	assert.Equal(t, "started", l0["message"])
	assert.Equal(t, "replica-1|engine|100", body["nextOffset"])
	assert.Equal(t, true, body["hasMore"])

	// AC2: invalid tail → validation error.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/services/"+serviceID+"/logs?pod=replica-1&tail=99999", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "invalid tail must be rejected: %v", body)
	assert.Equal(t, float64(10404), body["code"])

	// AC2: malformed since → validation error.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/services/"+serviceID+"/logs?pod=replica-1&since=not-a-time", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "malformed since must be rejected: %v", body)
	assert.Equal(t, float64(10404), body["code"])

	// AC2: unknown service → 10301.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/services/does-not-exist/logs?pod=replica-1", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown service must be rejected: %v", body)
	assert.Equal(t, float64(10301), body["code"])
}
