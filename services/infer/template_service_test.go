package infer

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
)

func TestCreateEngineParamTemplate(t *testing.T) {
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	resp, err := svc.CreateEngineParamTemplate(orgContext("org-1"), &inferv1.CreateEngineParamTemplateRequest{
		Name:            "Qwen 单机 4090",
		Description:     "tuned for the 4090 node",
		Engine:          "vllm",
		ModelId:         modelID,
		Topology:        "single",
		Accelerator:     "nvidia",
		AcceleratorType: "NVIDIA-GeForce-RTX-4090",
		EngineArgs:      map[string]string{"max-model-len": "8192", "gpu-memory-utilization": "0.9"},
		ExtraArgs:       []string{"--enable-chunked-prefill"},
		RoleEngineArgs: []*inferv1.RoleEngineArgs{
			{Role: "worker", EngineArgs: map[string]string{"max-num-seqs": "32"}},
		},
	})
	require.NoError(t, err)
	tpl := resp.GetTemplate()
	assert.NotEmpty(t, tpl.GetTemplateId())
	assert.Equal(t, "Qwen 单机 4090", tpl.GetName())
	assert.Equal(t, "vllm", tpl.GetEngine())
	assert.Equal(t, "qwen-3b", tpl.GetModelName())
	assert.Equal(t, "8192", tpl.GetEngineArgs()["max-model-len"])
	assert.Contains(t, tpl.GetExtraArgs(), "--enable-chunked-prefill")
	require.Len(t, tpl.GetRoleEngineArgs(), 1)
	assert.Equal(t, "32", tpl.GetRoleEngineArgs()[0].GetEngineArgs()["max-num-seqs"])

	// A duplicate name is refused.
	_, err = svc.CreateEngineParamTemplate(orgContext("org-1"), &inferv1.CreateEngineParamTemplateRequest{
		Name: "Qwen 单机 4090", Engine: "vllm",
		EngineArgs: map[string]string{"max-model-len": "8192"},
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeEngineTemplateExists, apierrors.CodeOf(err))
}

func TestCreateEngineParamTemplateValidation(t *testing.T) {
	svc, _, _ := newInferTestService(t)
	ctx := orgContext("org-1")

	cases := map[string]*inferv1.CreateEngineParamTemplateRequest{
		"missing name":     {Engine: "vllm"},
		"missing engine":   {Name: "x"},
		"unknown topology": {Name: "x", Engine: "vllm", Topology: "cluster-mesh", EngineArgs: map[string]string{"max-model-len": "8"}},
		"bad accelerator":  {Name: "x", Engine: "vllm", Accelerator: "TPU!!", EngineArgs: map[string]string{"max-model-len": "8"}},
		"bad engine arg":   {Name: "x", Engine: "vllm", EngineArgs: map[string]string{"--max": "1"}},
		"unknown role":     {Name: "x", Engine: "vllm", RoleEngineArgs: []*inferv1.RoleEngineArgs{{Role: "coordinator", EngineArgs: map[string]string{"max-num-seqs": "4"}}}},
		"nothing to load":  {Name: "x", Engine: "vllm"},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateEngineParamTemplate(ctx, req)
			require.Error(t, err)
			code := apierrors.CodeOf(err)
			assert.Contains(t, []apierrors.Code{
				apierrors.CodeEngineTemplateInvalid,
				apierrors.CodeInferEngineArgsInvalid,
			}, code, "unexpected code %d", code)
		})
	}
}

func TestListEngineParamTemplatesMatchOrdering(t *testing.T) {
	svc, _, db := newInferTestService(t)
	ctx := orgContext("org-1")
	modelID := seedModel(t, db, "qwen-3b", "v1")
	otherModel := seedModel(t, db, "llama", "v1")

	mk := func(req *inferv1.CreateEngineParamTemplateRequest) {
		_, err := svc.CreateEngineParamTemplate(ctx, req)
		require.NoError(t, err)
	}
	// Generic vLLM defaults: matches every vLLM deployment. A template still
	// has to carry something worth loading.
	mk(&inferv1.CreateEngineParamTemplateRequest{Name: "vllm default", Engine: "vllm",
		EngineArgs: map[string]string{"max-num-seqs": "128"}})
	// Model-specific: the strongest signal.
	args := map[string]string{"max-model-len": "8192"}
	mk(&inferv1.CreateEngineParamTemplateRequest{Name: "qwen tuned", Engine: "vllm", ModelId: modelID, EngineArgs: args})
	// Wrong model: filtered out entirely.
	mk(&inferv1.CreateEngineParamTemplateRequest{Name: "llama tuned", Engine: "vllm", ModelId: otherModel, EngineArgs: args})
	// Wrong engine: vLLM flags mean nothing to SGLang.
	mk(&inferv1.CreateEngineParamTemplateRequest{Name: "sglang", Engine: "sglang",
		EngineArgs: map[string]string{"tp-size": "2"}})

	resp, err := svc.ListEngineParamTemplates(ctx, &inferv1.ListEngineParamTemplatesRequest{
		Engine: "vllm", ModelId: modelID, Topology: "single", Accelerator: "nvidia",
	})
	require.NoError(t, err)
	names := make([]string, 0, len(resp.GetTemplates()))
	for _, tpl := range resp.GetTemplates() {
		names = append(names, tpl.GetName())
	}
	assert.Equal(t, []string{"qwen tuned", "vllm default"}, names)
	assert.Greater(t, resp.GetTemplates()[0].GetMatchScore(), resp.GetTemplates()[1].GetMatchScore())
}

func TestUpdateAndDeleteEngineParamTemplate(t *testing.T) {
	svc, _, _ := newInferTestService(t)
	ctx := orgContext("org-1")

	created, err := svc.CreateEngineParamTemplate(ctx, &inferv1.CreateEngineParamTemplateRequest{
		Name: "tpl", Engine: "vllm", EngineArgs: map[string]string{"max-model-len": "4096"},
	})
	require.NoError(t, err)
	id := created.GetTemplate().GetTemplateId()

	updated, err := svc.UpdateEngineParamTemplate(ctx, &inferv1.UpdateEngineParamTemplateRequest{
		TemplateId: id, Name: "tpl renamed", Engine: "vllm",
		EngineArgs: map[string]string{"max-model-len": "16384"},
		Topology:   "pd",
	})
	require.NoError(t, err)
	assert.Equal(t, "tpl renamed", updated.GetTemplate().GetName())
	assert.Equal(t, "16384", updated.GetTemplate().GetEngineArgs()["max-model-len"])
	assert.Equal(t, TopologyPD, updated.GetTemplate().GetTopology())

	// A second template must not be able to take the name.
	other, err := svc.CreateEngineParamTemplate(ctx, &inferv1.CreateEngineParamTemplateRequest{Name: "other", Engine: "vllm",
		EngineArgs: map[string]string{"max-num-seqs": "8"}})
	require.NoError(t, err)
	_, err = svc.UpdateEngineParamTemplate(ctx, &inferv1.UpdateEngineParamTemplateRequest{
		TemplateId: other.GetTemplate().GetTemplateId(), Name: "tpl renamed", Engine: "vllm",
		EngineArgs: map[string]string{"max-model-len": "4096"},
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeEngineTemplateExists, apierrors.CodeOf(err))

	_, err = svc.DeleteEngineParamTemplate(ctx, &inferv1.DeleteEngineParamTemplateRequest{TemplateId: id})
	require.NoError(t, err)
	_, err = svc.GetEngineParamTemplate(ctx, &inferv1.GetEngineParamTemplateRequest{TemplateId: id})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeEngineTemplateNotFound, apierrors.CodeOf(err))

	// Deleting twice is a miss, not a no-op.
	_, err = svc.DeleteEngineParamTemplate(ctx, &inferv1.DeleteEngineParamTemplateRequest{TemplateId: id})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeEngineTemplateNotFound, apierrors.CodeOf(err))
}

func TestExportImportEngineParamTemplates(t *testing.T) {
	svc, _, _ := newInferTestService(t)
	ctx := orgContext("org-1")

	_, err := svc.CreateEngineParamTemplate(ctx, &inferv1.CreateEngineParamTemplateRequest{
		Name: "exported", Engine: "vllm", Topology: "multinode",
		EngineArgs:     map[string]string{"max-model-len": "8192"},
		RoleEngineArgs: []*inferv1.RoleEngineArgs{{Role: "worker", ExtraArgs: []string{"--seed=1"}}},
	})
	require.NoError(t, err)

	exported, err := svc.ExportEngineParamTemplates(ctx, &inferv1.ExportEngineParamTemplatesRequest{})
	require.NoError(t, err)
	assert.EqualValues(t, 1, exported.GetCount())

	var doc templateExportDocument
	require.NoError(t, json.Unmarshal([]byte(exported.GetPayload()), &doc))
	assert.Equal(t, templateExportVersion, doc.Version)
	require.Len(t, doc.Templates, 1)
	assert.Equal(t, "exported", doc.Templates[0].Name)

	// Importing the same document onto a second service creates the template.
	svc2, _, _ := newInferTestService(t)
	res, err := svc2.ImportEngineParamTemplates(orgContext("org-1"), &inferv1.ImportEngineParamTemplatesRequest{
		Payload: exported.GetPayload(),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.GetCreated())
	assert.EqualValues(t, 0, res.GetSkipped())

	// A second import skips by default, overwrites on request.
	res, err = svc2.ImportEngineParamTemplates(orgContext("org-1"), &inferv1.ImportEngineParamTemplatesRequest{
		Payload: exported.GetPayload(),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 0, res.GetCreated())
	assert.EqualValues(t, 1, res.GetSkipped())

	res, err = svc2.ImportEngineParamTemplates(orgContext("org-1"), &inferv1.ImportEngineParamTemplatesRequest{
		Payload: exported.GetPayload(), Strategy: "overwrite",
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.GetOverwritten())
}

func TestImportEngineParamTemplatesRejectsBadDocuments(t *testing.T) {
	svc, _, _ := newInferTestService(t)
	ctx := orgContext("org-1")

	cases := map[string]string{
		"empty":        "",
		"not json":     "not json",
		"bad version":  `{"version": 99, "templates": [{"name": "x", "engine": "vllm"}]}`,
		"no templates": `{"version": 1, "templates": []}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.ImportEngineParamTemplates(ctx, &inferv1.ImportEngineParamTemplatesRequest{Payload: payload})
			require.Error(t, err)
			assert.Equal(t, apierrors.CodeEngineTemplateInvalid, apierrors.CodeOf(err))
		})
	}
}

func TestImportEngineParamTemplatesSkipsMalformedEntry(t *testing.T) {
	svc, _, _ := newInferTestService(t)
	ctx := orgContext("org-1")

	payload := `{"version":1,"templates":[
		{"name":"good","engine":"vllm","engineArgs":{"max-model-len":"1024"}},
		{"name":"bad","engine":"vllm","engineArgs":{"--bad":"1"}}
	]}`
	res, err := svc.ImportEngineParamTemplates(ctx, &inferv1.ImportEngineParamTemplatesRequest{Payload: payload})
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.GetCreated())
	assert.EqualValues(t, 1, res.GetSkipped(), "a malformed entry is skipped, not fatal")
}

// TestEngineParamTemplateNeedsContent covers the empty-template rule: a
// template with no parameters, no extra arguments and no role override
// records nothing worth loading, so it is refused rather than stored as a row
// that matches everything and changes nothing.
func TestEngineParamTemplateNeedsContent(t *testing.T) {
	svc, _, _ := newInferTestService(t)
	ctx := orgContext("org-1")

	_, err := svc.CreateEngineParamTemplate(ctx, &inferv1.CreateEngineParamTemplateRequest{
		Name: "empty", Engine: "vllm",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeEngineTemplateInvalid, apierrors.CodeOf(err))

	// Any one of the three kinds of content is enough.
	created, err := svc.CreateEngineParamTemplate(ctx, &inferv1.CreateEngineParamTemplateRequest{
		Name: "only a role override", Engine: "vllm",
		RoleEngineArgs: []*inferv1.RoleEngineArgs{
			{Role: "prefill", EngineArgs: map[string]string{"max-num-seqs": "8"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, created.GetTemplate().GetRoleEngineArgs(), 1)

	// An update is held to the same rule: blanking a template out would leave
	// the same useless row behind.
	_, err = svc.UpdateEngineParamTemplate(ctx, &inferv1.UpdateEngineParamTemplateRequest{
		TemplateId: created.GetTemplate().GetTemplateId(),
		Name:       "blanked", Engine: "vllm",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeEngineTemplateInvalid, apierrors.CodeOf(err))
}
