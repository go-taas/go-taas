package infer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
)

// TestNormalizeEngineArgs verifies the parameter map is validated and
// normalized: flag names without dashes, values trimmed, empty values
// dropped, and a nil map stored as {}.
func TestNormalizeEngineArgs(t *testing.T) {
	out, err := normalizeEngineArgs(map[string]string{
		"max-model-len":          " 8192 ",
		"enable-prefix-caching":  "flag:true",
		"gpu-memory-utilization": "0.9",
		"empty":                  "   ",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"max-model-len":"8192","enable-prefix-caching":"flag:true","gpu-memory-utilization":"0.9"}`, string(out))

	// No arguments stores an empty object, not null.
	empty, err := normalizeEngineArgs(nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(empty))
}

// TestNormalizeEngineArgsRejectsBadNames verifies a flag name that could
// break the command line is refused.
func TestNormalizeEngineArgsRejectsBadNames(t *testing.T) {
	for _, name := range []string{"--max-model-len", "Max-Len", "max len", "max;rm -rf /", ""} {
		_, err := normalizeEngineArgs(map[string]string{name: "1"})
		require.Error(t, err, "name %q must be rejected", name)
		assert.Equal(t, apierrors.CodeInferEngineArgsInvalid, apierrors.CodeOf(err))
	}
}

// TestNormalizeEngineArgsBounds verifies the count and length bounds.
func TestNormalizeEngineArgsBounds(t *testing.T) {
	many := map[string]string{}
	for i := 0; i < maxEngineArgs+1; i++ {
		many["flag-"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "1"
	}
	_, err := normalizeEngineArgs(many)
	require.Error(t, err)

	long := make([]byte, maxEngineArgLen+1)
	for i := range long {
		long[i] = 'x'
	}
	_, err = normalizeEngineArgs(map[string]string{"max-model-len": string(long)})
	require.Error(t, err)
}

// TestNormalizeExtraArgs verifies the free-form list is trimmed, emptied
// of blanks, and refuses newlines (which would split one argument into a
// command line injection).
func TestNormalizeExtraArgs(t *testing.T) {
	out, err := normalizeExtraArgs([]string{" --seed=7 ", "", "--quantization awq"})
	require.NoError(t, err)
	assert.JSONEq(t, `["--seed=7","--quantization awq"]`, string(out))

	empty, err := normalizeExtraArgs(nil)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(empty))

	_, err = normalizeExtraArgs([]string{"--seed=7\n--evil"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferEngineArgsInvalid, apierrors.CodeOf(err))
}

// TestCreateServiceStoresEngineArgs verifies the create RPC persists the
// parameters so the controller can turn them into the container command.
func TestCreateServiceStoresEngineArgs(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")
	ctx := orgContext("org-1")

	resp, err := svc.CreateInferenceService(ctx, &inferv1.CreateInferenceServiceRequest{
		Name:            "demo-args",
		ModelId:         modelID,
		ModelVersion:    "v1",
		ImageId:         "img-vllm-nvidia",
		Replicas:        1,
		Accelerator:     "nvidia",
		AcceleratorType: "A100",
		EngineArgs:      map[string]string{"max-model-len": "8192"},
		ExtraArgs:       []string{"--seed=7"},
	})
	require.NoError(t, err)

	repo, err := svc.repository()
	require.NoError(t, err)
	row, err := repo.FindByID(ctx, resp.GetServiceId())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"max-model-len": "8192"}, EngineArgsMap(row.EngineArgs))
	assert.Equal(t, []string{"--seed=7"}, ExtraArgsList(row.ExtraArgs))
}

// TestCreateServiceRejectsBadEngineArgs verifies a malformed parameter
// fails the create before any write, with the engine-args code.
func TestCreateServiceRejectsBadEngineArgs(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	_, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-bad-args", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		EngineArgs: map[string]string{"--bad": "1"},
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferEngineArgsInvalid, apierrors.CodeOf(err))
}
