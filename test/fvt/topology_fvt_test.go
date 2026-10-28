package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFVTCreateServiceWithTopologies verifies the create API accepts every
// serving layout and echoes it back on the detail read, so the console can
// render what will be deployed.
func TestFVTCreateServiceWithTopologies(t *testing.T) {
	env := newModelInferEnv(t)
	modelID := registerFVTCreateModel(t, env, "topology-model")

	cases := []struct {
		name     string
		payload  map[string]any
		topology string
		tp       float64
		pp       float64
		prefill  float64
		decode   float64
	}{
		{
			name:     "topology-single-svc",
			payload:  map[string]any{},
			topology: "single",
			tp:       1, pp: 1,
		},
		{
			name: "topology-distributed-svc",
			payload: map[string]any{
				"topology":             "distributed",
				"tensorParallelSize":   "4",
				"pipelineParallelSize": "2",
			},
			topology: "distributed",
			tp:       4, pp: 2,
		},
		{
			name: "topology-pd-svc",
			payload: map[string]any{
				"topology":             "pd",
				"tensorParallelSize":   "2",
				"pipelineParallelSize": "1",
				"prefillReplicas":      "2",
				"decodeReplicas":       "3",
			},
			topology: "pd",
			tp:       2, pp: 1,
			prefill: 2, decode: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"name":         tc.name,
				"modelId":      modelID,
				"modelVersion": "v1",
				"imageId":      "img-vllm-nvidia",
				"replicas":     "1",
				"accelerator":  "nvidia",
			}
			for k, v := range tc.payload {
				payload[k] = v
			}
			code, body := env.call(t, http.MethodPost, "/api/v1/admin/inference-services", payload, "org-fvt")
			require.Equal(t, http.StatusOK, code, "body: %v", body)
			serviceID, _ := body["serviceId"].(string)
			require.NotEmpty(t, serviceID)

			code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
			require.Equal(t, http.StatusOK, code, "body: %v", body)
			service, _ := body["service"].(map[string]any)
			require.NotNil(t, service)
			assert.Equal(t, tc.topology, service["topology"])
			assert.EqualValues(t, tc.tp, service["tensorParallelSize"])
			assert.EqualValues(t, tc.pp, service["pipelineParallelSize"])
			assert.EqualValues(t, tc.prefill, service["prefillReplicas"])
			assert.EqualValues(t, tc.decode, service["decodeReplicas"])
		})
	}
}

// TestFVTTopologyRejected verifies an impossible layout is refused with the
// topology code before any desired state is written.
func TestFVTTopologyRejected(t *testing.T) {
	env := newModelInferEnv(t)
	modelID := registerFVTCreateModel(t, env, "bad-topology-model")

	// Distributed with one pod is a single-node deploy.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name":               "bad-topology-svc",
		"modelId":            modelID,
		"modelVersion":       "v1",
		"imageId":            "img-vllm-nvidia",
		"replicas":           "1",
		"accelerator":        "nvidia",
		"topology":           "distributed",
		"tensorParallelSize": "1",
	}, "org-fvt")
	require.Equal(t, http.StatusInternalServerError, code, "body: %v", body)
	assert.EqualValues(t, 10319, body["code"], "body: %v", body)
}

// TestFVTUpdateServiceTopology verifies the in-place layout change: the
// service keeps its id, returns to pending and reports the new roles.
func TestFVTUpdateServiceTopology(t *testing.T) {
	env := newModelInferEnv(t)
	modelID := registerFVTCreateModel(t, env, "reshape-model")

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name":         "reshape-svc",
		"modelId":      modelID,
		"modelVersion": "v1",
		"imageId":      "img-vllm-nvidia",
		"replicas":     "1",
		"accelerator":  "nvidia",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	serviceID, _ := body["serviceId"].(string)
	require.NotEmpty(t, serviceID)

	code, body = env.call(t, http.MethodPost,
		"/api/v1/admin/inference-services/"+serviceID+":update-topology", map[string]any{
			"serviceId":            serviceID,
			"topology":             "pd",
			"tensorParallelSize":   "2",
			"pipelineParallelSize": "1",
			"prefillReplicas":      "2",
			"decodeReplicas":       "2",
			"replicas":             "2",
		}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "pending", body["state"], "a spec change waits for the controller")

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	service, _ := body["service"].(map[string]any)
	require.NotNil(t, service)
	assert.Equal(t, "pd", service["topology"])
	assert.EqualValues(t, 2, service["tensorParallelSize"])
	assert.EqualValues(t, 2, service["prefillReplicas"])
	assert.EqualValues(t, 2, service["decodeReplicas"])
	assert.EqualValues(t, 2, service["replicas"])
}
