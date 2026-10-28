package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFVTEngineParamTemplateLifecycle exercises the parameter-template API over
// the gateway: create, list with a match context, export, delete, and import.
func TestFVTEngineParamTemplateLifecycle(t *testing.T) {
	env := newModelInferEnv(t)

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/engine-param-templates", map[string]any{
		"name":            "fvt-template",
		"engine":          "vllm",
		"topology":        "single",
		"accelerator":     "nvidia",
		"engineArgs":      map[string]any{"max-model-len": "8192"},
		"extraArgs":       []any{"--enable-chunked-prefill"},
		"roleEngineArgs":  []any{map[string]any{"role": "worker", "engineArgs": map[string]any{"max-num-seqs": "32"}}},
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	tpl, _ := body["template"].(map[string]any)
	require.NotNil(t, tpl)
	templateID, _ := tpl["templateId"].(string)
	require.NotEmpty(t, templateID)

	// The list matches on the deployment context.
	code, body = env.call(t, http.MethodGet,
		"/api/v1/admin/engine-param-templates?engine=vllm&topology=single&accelerator=nvidia", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	templates, _ := body["templates"].([]any)
	require.NotEmpty(t, templates)

	// A different engine is not a match.
	code, body = env.call(t, http.MethodGet,
		"/api/v1/admin/engine-param-templates?engine=sglang", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Empty(t, body["templates"])

	// Export renders a document, import reads it back.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/engine-param-templates:export", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	payload, _ := body["payload"].(string)
	require.Contains(t, payload, "fvt-template")

	// Delete, then import the exported document to bring it back.
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/engine-param-templates/"+templateID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	code, body = env.call(t, http.MethodPost, "/api/v1/admin/engine-param-templates:import", map[string]any{
		"payload": payload,
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, 1, body["created"])

	// A bad document is refused with the template code.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/engine-param-templates:import", map[string]any{
		"payload": `{"version":99,"templates":[]}`,
	}, "org-fvt")
	require.Equal(t, http.StatusInternalServerError, code, "body: %v", body)
	assert.EqualValues(t, 10322, body["code"], "body: %v", body)
}
