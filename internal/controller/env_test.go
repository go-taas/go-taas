package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// roleEnv reads one role template's engine-container environment into a map.
func roleEnv(t *testing.T, role map[string]any) map[string]string {
	t.Helper()
	spec := role["template"].(map[string]any)["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	require.Len(t, containers, 1)
	out := map[string]string{}
	env, _ := containers[0].(map[string]any)["env"].([]any)
	for _, raw := range env {
		entry, ok := raw.(map[string]any)
		require.True(t, ok)
		out[entry["name"].(string)] = entry["value"].(string)
	}
	return out
}

// roleEnvOrder reads one role template's environment names in declared order.
func roleEnvOrder(t *testing.T, role map[string]any) []string {
	t.Helper()
	spec := role["template"].(map[string]any)["spec"].(map[string]any)
	containers := spec["containers"].([]any)
	env, _ := containers[0].(map[string]any)["env"].([]any)
	names := make([]string, 0, len(env))
	for _, raw := range env {
		names = append(names, raw.(map[string]any)["name"].(string))
	}
	return names
}

// TestBuildStormServiceAppliesEnvToEveryTopology verifies an operator-supplied
// environment variable reaches the engine container of every role of every
// topology: single, multinode and pd.
func TestBuildStormServiceAppliesEnvToEveryTopology(t *testing.T) {
	for _, topology := range []string{"", topologyDistributed, topologyPD} {
		name := topology
		if name == "" {
			name = "single"
		}
		t.Run(name, func(t *testing.T) {
			evt := testChangeEvent("upsert", "demo")
			evt.Topology = topology
			evt.TensorParallelSize = 2
			evt.PrefillReplicas = 1
			evt.DecodeReplicas = 1
			evt.Env = map[string]string{"VLLM_USE_V1": "1", "HF_HOME": "/models/hf"}

			storm, err := testReconciler().buildStormService(context.Background(), evt)
			require.NoError(t, err)

			roles := stormRolesOf(t, storm)
			require.NotEmpty(t, roles)
			for _, role := range roles {
				assert.Equal(t, map[string]string{
					"VLLM_USE_V1": "1",
					"HF_HOME":     "/models/hf",
				}, roleEnv(t, role), "role %v must carry the service env", role["name"])
			}
		})
	}
}

// TestBuildStormServiceRoleEnvOverrides verifies a per-role variable wins over
// the shared value and that the other variables are still inherited.
func TestBuildStormServiceRoleEnvOverrides(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Topology = topologyPD
	evt.TensorParallelSize = 2
	evt.PrefillReplicas = 1
	evt.DecodeReplicas = 1
	evt.Env = map[string]string{"VLLM_USE_V1": "1", "HF_HOME": "/models/hf"}
	evt.RoleEngineArgs = []roleEngineArgs{{
		Role: stormRoleDecode,
		Env:  map[string]string{"VLLM_USE_V1": "0", "DECODE_ONLY": "1"},
	}}

	storm, err := testReconciler().buildStormService(context.Background(), evt)
	require.NoError(t, err)

	roles := stormRolesOf(t, storm)
	require.Len(t, roles, 2)
	prefill, decode := roles[0], roles[1]
	require.Equal(t, stormRolePrefill, prefill["name"])
	require.Equal(t, stormRoleDecode, decode["name"])

	assert.Equal(t, map[string]string{"VLLM_USE_V1": "1", "HF_HOME": "/models/hf"},
		roleEnv(t, prefill), "the prefill role inherits the shared env unchanged")
	assert.Equal(t, map[string]string{
		"VLLM_USE_V1": "0",
		"HF_HOME":     "/models/hf",
		"DECODE_ONLY": "1",
	}, roleEnv(t, decode), "the decode role overrides only the variable it names")
}

// TestBuildStormServiceEnvIsSorted verifies the environment list is declared in
// name order. An unordered map would rewrite the storm template on every
// reconcile and roll the pods for no reason.
func TestBuildStormServiceEnvIsSorted(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Env = map[string]string{"ZED": "1", "ALPHA": "2", "MIDDLE": "3"}

	storm, err := testReconciler().buildStormService(context.Background(), evt)
	require.NoError(t, err)
	roles := stormRolesOf(t, storm)
	require.Len(t, roles, 1)
	assert.Equal(t, []string{"ALPHA", "MIDDLE", "ZED"}, roleEnvOrder(t, roles[0]))
}

// TestBuildDeploymentAppliesEnv verifies the plain Deployment path (a
// controller started without a dynamic client) also carries the variables.
func TestBuildDeploymentAppliesEnv(t *testing.T) {
	evt := testChangeEvent("upsert", "demo")
	evt.Env = map[string]string{"VLLM_USE_V1": "1"}

	dep := testReconciler().buildDeployment(context.Background(), evt)
	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	require.Len(t, dep.Spec.Template.Spec.Containers[0].Env, 1)
	assert.Equal(t, "VLLM_USE_V1", dep.Spec.Template.Spec.Containers[0].Env[0].Name)
	assert.Equal(t, "1", dep.Spec.Template.Spec.Containers[0].Env[0].Value)
}

// TestChangeEventDecodeEnv verifies the controller's changeEvent decodes the
// infer module's published env (shared and per role).
func TestChangeEventDecodeEnv(t *testing.T) {
	body := []byte(`{
		"event_type": "upsert",
		"service_id": "svc-1",
		"name": "demo",
		"env": {"VLLM_USE_V1": "1", "EMPTY": ""},
		"role_engine_args": [
			{"role": "decode", "env": {"DECODE_ONLY": "1"}}
		]
	}`)
	var evt changeEvent
	require.NoError(t, json.Unmarshal(body, &evt))
	assert.Equal(t, map[string]string{"VLLM_USE_V1": "1", "EMPTY": ""}, evt.Env)
	require.Len(t, evt.RoleEngineArgs, 1)
	assert.Equal(t, map[string]string{"DECODE_ONLY": "1"}, evt.RoleEngineArgs[0].Env)

	// The merged view the pod uses: shared plus the role's override.
	merged := map[string]string{}
	for _, v := range evt.envForRole(stormRoleDecode) {
		merged[v.Name] = v.Value
	}
	assert.Equal(t, map[string]string{"VLLM_USE_V1": "1", "EMPTY": "", "DECODE_ONLY": "1"}, merged)
}

// TestApplyUpsertWritesEnvToStorm verifies the whole upsert path writes the
// variables into the live StormService.
func TestApplyUpsertWritesEnvToStorm(t *testing.T) {
	dyn := newFakeDynamic()
	reconciler, _, _ := newAIBrixFakeReconciler(dyn)
	ctx := context.Background()

	evt := testChangeEvent("upsert", "demo")
	evt.Env = map[string]string{"VLLM_USE_V1": "1"}
	require.NoError(t, reconciler.ApplyInferServiceChange(ctx, changeMessage(evt)))

	storm, err := dyn.Resource(stormServiceGVR).Namespace(reconcileNamespace).
		Get(ctx, "demo", metav1.GetOptions{})
	require.NoError(t, err)
	roles := stormRolesOf(t, storm)
	require.NotEmpty(t, roles)
	assert.Equal(t, map[string]string{"VLLM_USE_V1": "1"}, roleEnv(t, roles[0]))
}
