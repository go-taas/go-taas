package infer

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
)

// TestNormalizeEnv verifies the environment map is validated and normalized:
// names are kept verbatim in their POSIX shape, an empty value is preserved
// (it clears an inherited variable), and a nil map stores {}.
func TestNormalizeEnv(t *testing.T) {
	out, err := normalizeEnv(map[string]string{
		"VLLM_USE_V1":                 "1",
		"HF_HOME":                     " /models/hf ",
		"VLLM_LOGGING_LEVEL":          "",
		"CUDA_DEVICE_MAX_CONNECTIONS": "8",
		"  ":                          "ignored",
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"VLLM_USE_V1":"1",
		"HF_HOME":" /models/hf ",
		"VLLM_LOGGING_LEVEL":"",
		"CUDA_DEVICE_MAX_CONNECTIONS":"8"
	}`, string(out))

	empty, err := normalizeEnv(nil)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(empty))
}

// TestNormalizeEnvRejectsBadNames verifies a name the shell would not read, or
// that could not be an environment variable at all, is refused.
func TestNormalizeEnvRejectsBadNames(t *testing.T) {
	for _, name := range []string{
		"1FOO", "FOO-BAR", "foo bar", "FOO=BAR", "FOO$(id)", "变量",
	} {
		_, err := normalizeEnv(map[string]string{name: "1"})
		require.Error(t, err, "name %q must be rejected", name)
		assert.Equal(t, apierrors.CodeInferEngineArgsInvalid, apierrors.CodeOf(err))
	}
}

// TestNormalizeEnvRejectsReservedNames verifies the variables Kubernetes and
// the storm controller own are refused: overriding them breaks the pod's
// identity or the distributed rendezvous.
func TestNormalizeEnvRejectsReservedNames(t *testing.T) {
	for name := range reservedEnvNames {
		_, err := normalizeEnv(map[string]string{name: "x"})
		require.Error(t, err, "reserved name %q must be rejected", name)
		assert.Equal(t, apierrors.CodeInferEngineArgsInvalid, apierrors.CodeOf(err))
	}
}

// TestNormalizeEnvBounds verifies the count and length bounds.
func TestNormalizeEnvBounds(t *testing.T) {
	many := map[string]string{}
	for i := 0; i <= maxEnvVars; i++ {
		many["VAR_"+strconv.Itoa(i)] = "1"
	}
	_, err := normalizeEnv(many)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferEngineArgsInvalid, apierrors.CodeOf(err))

	long := make([]byte, maxEnvValueLen+1)
	for i := range long {
		long[i] = 'x'
	}
	_, err = normalizeEnv(map[string]string{"HF_HOME": string(long)})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferEngineArgsInvalid, apierrors.CodeOf(err))

	_, err = normalizeEnv(map[string]string{"HF_HOME": "a\x00b"})
	require.Error(t, err, "a NUL byte must be rejected")
}

// TestCreateInferenceServiceStoresEnv verifies the create RPC persists the
// environment variables and the detail RPC returns them, on a single-node
// service.
func TestCreateInferenceServiceStoresEnv(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-env", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		Env: map[string]string{"VLLM_USE_V1": "1", "HF_HOME": "/models/hf"},
	})
	require.NoError(t, err)

	repo := NewInferenceServiceRepository(db)
	row, err := repo.FindByID(t.Context(), resp.GetServiceId())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"VLLM_USE_V1": "1", "HF_HOME": "/models/hf"}, EnvMap(row.Env))

	detail, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: resp.GetServiceId()})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"VLLM_USE_V1": "1", "HF_HOME": "/models/hf"}, detail.GetEnv())
}

// TestCreateInferenceServiceRejectsReservedEnv verifies a reserved variable
// name is refused before any row or event exists.
func TestCreateInferenceServiceRejectsReservedEnv(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	_, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-bad-env", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		Env: map[string]string{"HOSTNAME": "spoofed"},
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeInferEngineArgsInvalid, apierrors.CodeOf(err))
	assert.Empty(t, client.published, "nothing may be published when validation fails")
}

// TestCreateInferenceServiceStoresRoleEnv verifies a per-role environment
// override is persisted inside role_engine_args alongside the flags, and that
// a role carrying only variables is not dropped.
func TestCreateInferenceServiceStoresRoleEnv(t *testing.T) {
	seedImageRegistry(t)
	svc, _, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-pd-env", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		Topology:        "pd",
		PrefillReplicas: 1, DecodeReplicas: 1,
		Env: map[string]string{"VLLM_USE_V1": "1"},
		RoleEngineArgs: []*inferv1.RoleEngineArgs{{
			Role: "decode",
			Env:  map[string]string{"VLLM_USE_V1": "0", "DECODE_ONLY": "1"},
		}},
	})
	require.NoError(t, err)

	repo := NewInferenceServiceRepository(db)
	row, err := repo.FindByID(t.Context(), resp.GetServiceId())
	require.NoError(t, err)
	roles := RoleEngineArgsList(row.RoleEngineArgs)
	require.Len(t, roles, 1)
	assert.Equal(t, "decode", roles[0].Role)
	assert.Equal(t, map[string]string{"VLLM_USE_V1": "0", "DECODE_ONLY": "1"}, roles[0].Env)

	detail, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: resp.GetServiceId()})
	require.NoError(t, err)
	require.Len(t, detail.GetRoleEngineArgs(), 1)
	assert.Equal(t, map[string]string{"VLLM_USE_V1": "0", "DECODE_ONLY": "1"}, detail.GetRoleEngineArgs()[0].GetEnv())
}

// TestUpdateEngineArgsReplacesEnv verifies the hot update path replaces the
// environment variables (an empty map clears them) and publishes the new set.
func TestUpdateEngineArgsReplacesEnv(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	created, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-hot-env", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 1, Accelerator: "nvidia",
		Env: map[string]string{"VLLM_USE_V1": "1"},
	})
	require.NoError(t, err)
	client.published = nil

	_, err = svc.UpdateInferenceServiceEngineArgs(orgContext("org-1"), &inferv1.UpdateInferenceServiceEngineArgsRequest{
		ServiceId: created.GetServiceId(),
		Env:       map[string]string{"VLLM_USE_V1": "0", "NCCL_DEBUG": "INFO"},
	})
	require.NoError(t, err)

	repo := NewInferenceServiceRepository(db)
	row, err := repo.FindByID(t.Context(), created.GetServiceId())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"VLLM_USE_V1": "0", "NCCL_DEBUG": "INFO"}, EnvMap(row.Env))
	assert.Equal(t, StatePending, row.State, "a running service returns to pending for the hot update")

	// The published event carries the new variables so the controller can
	// roll the pods without a database read.
	require.NotEmpty(t, client.published)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	assert.Equal(t, map[string]string{"VLLM_USE_V1": "0", "NCCL_DEBUG": "INFO"}, evt.Env)

	// An empty map clears the variables.
	_, err = svc.UpdateInferenceServiceEngineArgs(orgContext("org-1"), &inferv1.UpdateInferenceServiceEngineArgsRequest{
		ServiceId: created.GetServiceId(),
	})
	require.NoError(t, err)
	row, err = repo.FindByID(t.Context(), created.GetServiceId())
	require.NoError(t, err)
	assert.Empty(t, EnvMap(row.Env))
}
