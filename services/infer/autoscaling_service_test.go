package infer

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func TestValidateAutoscalingPolicy(t *testing.T) {
	valid := &inferv1.AutoscalingPolicy{
		Enabled: true, MinReplicas: 1, MaxReplicas: 10,
		TargetConcurrency: 32, ScaleToZero: false, CooldownSeconds: 300,
	}
	assert.NoError(t, validateAutoscalingPolicy(valid))

	// nil is valid (inherit).
	assert.NoError(t, validateAutoscalingPolicy(nil))

	cases := []struct {
		name string
		p    *inferv1.AutoscalingPolicy
	}{
		{"min > max", &inferv1.AutoscalingPolicy{MinReplicas: 10, MaxReplicas: 5, TargetConcurrency: 32, CooldownSeconds: 300}},
		{"min 0 without scale-to-zero", &inferv1.AutoscalingPolicy{MinReplicas: 0, MaxReplicas: 5, TargetConcurrency: 32, CooldownSeconds: 300}},
		{"scale-to-zero with min > 0", &inferv1.AutoscalingPolicy{MinReplicas: 1, MaxReplicas: 5, TargetConcurrency: 32, ScaleToZero: true, CooldownSeconds: 300}},
		{"target too low", &inferv1.AutoscalingPolicy{MinReplicas: 1, MaxReplicas: 5, TargetConcurrency: 0, CooldownSeconds: 300}},
		{"target too high", &inferv1.AutoscalingPolicy{MinReplicas: 1, MaxReplicas: 5, TargetConcurrency: 1001, CooldownSeconds: 300}},
		{"cooldown negative", &inferv1.AutoscalingPolicy{MinReplicas: 1, MaxReplicas: 5, TargetConcurrency: 32, CooldownSeconds: -1}},
		{"cooldown too high", &inferv1.AutoscalingPolicy{MinReplicas: 1, MaxReplicas: 5, TargetConcurrency: 32, CooldownSeconds: 3601}},
		{"max too low", &inferv1.AutoscalingPolicy{MinReplicas: 1, MaxReplicas: 0, TargetConcurrency: 32, CooldownSeconds: 300}},
		{"max too high", &inferv1.AutoscalingPolicy{MinReplicas: 1, MaxReplicas: 101, TargetConcurrency: 32, CooldownSeconds: 300}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAutoscalingPolicy(tc.p)
			require.Error(t, err)
			assert.EqualValues(t, apierrors.CodeAutoscalingPolicyInvalid, apierrors.CodeOf(err))
		})
	}
}

func TestGetAutoscalingPolicy(t *testing.T) {
	svc, _, _ := newInferTestService(t)
	resp, err := svc.GetAutoscalingPolicy(context.Background(), &inferv1.GetAutoscalingPolicyRequest{})
	require.NoError(t, err)
	// The singleton is seeded with defaults.
	assert.True(t, resp.GetPolicy().GetEnabled())
	assert.Equal(t, int32(1), resp.GetPolicy().GetMinReplicas())
	assert.Equal(t, int32(10), resp.GetPolicy().GetMaxReplicas())
	assert.Equal(t, int32(32), resp.GetPolicy().GetTargetConcurrency())
	assert.Equal(t, int32(300), resp.GetPolicy().GetCooldownSeconds())
}

func TestUpdateAutoscalingPolicy(t *testing.T) {
	svc, _, _ := newInferTestService(t)
	ctx := context.Background()

	// Valid update.
	resp, err := svc.UpdateAutoscalingPolicy(ctx, &inferv1.UpdateAutoscalingPolicyRequest{
		Policy: &inferv1.AutoscalingPolicy{
			Enabled: true, MinReplicas: 2, MaxReplicas: 20,
			TargetConcurrency: 64, ScaleToZero: false, CooldownSeconds: 600,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), resp.GetPolicy().GetMinReplicas())
	assert.Equal(t, int32(20), resp.GetPolicy().GetMaxReplicas())

	// Get returns the saved policy.
	get, err := svc.GetAutoscalingPolicy(ctx, &inferv1.GetAutoscalingPolicyRequest{})
	require.NoError(t, err)
	assert.Equal(t, int32(64), get.GetPolicy().GetTargetConcurrency())
	assert.Equal(t, int32(600), get.GetPolicy().GetCooldownSeconds())

	// Invalid update is rejected.
	_, err = svc.UpdateAutoscalingPolicy(ctx, &inferv1.UpdateAutoscalingPolicyRequest{
		Policy: &inferv1.AutoscalingPolicy{
			Enabled: true, MinReplicas: 20, MaxReplicas: 2,
			TargetConcurrency: 32, CooldownSeconds: 300,
		},
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeAutoscalingPolicyInvalid, apierrors.CodeOf(err))
}

func TestUpdateInferenceServiceAutoscaling(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	// Create a service first.
	createResp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 2, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	serviceID := createResp.GetServiceId()
	client.published = nil

	// Update the per-service policy.
	resp, err := svc.UpdateInferenceServiceAutoscaling(orgContext("org-1"), &inferv1.UpdateInferenceServiceAutoscalingRequest{
		ServiceId: serviceID,
		Policy: &inferv1.AutoscalingPolicy{
			Enabled: true, MinReplicas: 1, MaxReplicas: 5,
			TargetConcurrency: 16, ScaleToZero: false, CooldownSeconds: 120,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.GetFixedReplicas())

	// A change event was published with the effective policy.
	require.Len(t, client.published, 1)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	as, ok := evt["autoscaling"].(map[string]any)
	require.True(t, ok, "change event must carry autoscaling")
	assert.Equal(t, true, as["enabled"])
	assert.EqualValues(t, float64(1), as["min_replicas"])
	assert.EqualValues(t, float64(5), as["max_replicas"])

	// Get returns the effective policy.
	get, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: serviceID})
	require.NoError(t, err)
	require.NotNil(t, get.GetAutoscaling())
	assert.Equal(t, int32(16), get.GetAutoscaling().GetTargetConcurrency())
}

func TestUpdateInferenceServiceAutoscalingDisable(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	createResp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 3, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	serviceID := createResp.GetServiceId()
	client.published = nil

	// Disable autoscaling: returns the fixed count (FR2.5).
	resp, err := svc.UpdateInferenceServiceAutoscaling(orgContext("org-1"), &inferv1.UpdateInferenceServiceAutoscalingRequest{
		ServiceId: serviceID,
		Policy: &inferv1.AutoscalingPolicy{
			Enabled: false, MinReplicas: 1, MaxReplicas: 5,
			TargetConcurrency: 32, CooldownSeconds: 300,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), resp.GetFixedReplicas())

	// The change event carries autoscaling disabled.
	require.Len(t, client.published, 1)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	as, ok := evt["autoscaling"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, false, as["enabled"])
}

func TestUpdateInferenceServiceAutoscalingNotFound(t *testing.T) {
	svc, _, _ := newInferTestService(t)
	_, err := svc.UpdateInferenceServiceAutoscaling(orgContext("org-1"), &inferv1.UpdateInferenceServiceAutoscalingRequest{
		ServiceId: "missing",
		Policy: &inferv1.AutoscalingPolicy{
			Enabled: true, MinReplicas: 1, MaxReplicas: 5,
			TargetConcurrency: 32, CooldownSeconds: 300,
		},
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeInferServiceNotFound, apierrors.CodeOf(err))
}

func TestUpdateInferenceServiceAutoscalingInvalid(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	createResp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 2, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	client.published = nil

	// Invalid policy publishes nothing (AD10).
	_, err = svc.UpdateInferenceServiceAutoscaling(orgContext("org-1"), &inferv1.UpdateInferenceServiceAutoscalingRequest{
		ServiceId: createResp.GetServiceId(),
		Policy: &inferv1.AutoscalingPolicy{
			Enabled: true, MinReplicas: 10, MaxReplicas: 2,
			TargetConcurrency: 32, CooldownSeconds: 300,
		},
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeAutoscalingPolicyInvalid, apierrors.CodeOf(err))
	assert.Empty(t, client.published, "no event on validation failure (AD10)")
}

func TestCreateInferenceServiceWithAutoscaling(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	// Create with an explicit policy.
	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 2, Accelerator: "nvidia",
		Autoscaling: &inferv1.AutoscalingPolicy{
			Enabled: true, MinReplicas: 1, MaxReplicas: 8,
			TargetConcurrency: 48, ScaleToZero: false, CooldownSeconds: 200,
		},
	})
	require.NoError(t, err)
	serviceID := resp.GetServiceId()

	// The change event carries the effective policy.
	require.Len(t, client.published, 1)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	as, ok := evt["autoscaling"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, float64(8), as["max_replicas"])

	// Get returns the stored policy.
	get, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: serviceID})
	require.NoError(t, err)
	require.NotNil(t, get.GetAutoscaling())
	assert.Equal(t, int32(48), get.GetAutoscaling().GetTargetConcurrency())
}

func TestCreateInferenceServiceInheritsDefault(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	// Create without a policy: inherits the global default.
	resp, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 2, Accelerator: "nvidia",
	})
	require.NoError(t, err)
	serviceID := resp.GetServiceId()

	// The change event carries the global default (enabled, min 1, max 10).
	require.Len(t, client.published, 1)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(client.published[0].Body, &evt))
	as, ok := evt["autoscaling"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, as["enabled"])
	assert.EqualValues(t, float64(10), as["max_replicas"])

	// Get returns the effective (inherited) policy.
	get, err := svc.GetInferenceService(orgContext("org-1"), &inferv1.GetInferenceServiceRequest{ServiceId: serviceID})
	require.NoError(t, err)
	require.NotNil(t, get.GetAutoscaling())
	assert.Equal(t, int32(10), get.GetAutoscaling().GetMaxReplicas())
}

func TestCreateInferenceServiceInvalidAutoscaling(t *testing.T) {
	seedImageRegistry(t)
	svc, client, db := newInferTestService(t)
	modelID := seedModel(t, db, "qwen-3b", "v1")

	_, err := svc.CreateInferenceService(orgContext("org-1"), &inferv1.CreateInferenceServiceRequest{
		Name: "demo-svc", ModelId: modelID, ModelVersion: "v1",
		ImageId: "img-vllm-nvidia", Replicas: 2, Accelerator: "nvidia",
		Autoscaling: &inferv1.AutoscalingPolicy{
			Enabled: true, MinReplicas: 10, MaxReplicas: 2,
			TargetConcurrency: 32, CooldownSeconds: 300,
		},
	})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeAutoscalingPolicyInvalid, apierrors.CodeOf(err))
	assert.Empty(t, client.published, "no event on validation failure (AD10)")
}

func TestAutoscalingStateProto(t *testing.T) {
	assert.Equal(t, inferv1.AutoscalingState_AUTOSCALING_STATE_DISABLED, autoscalingStateProto("disabled"))
	assert.Equal(t, inferv1.AutoscalingState_AUTOSCALING_STATE_STEADY, autoscalingStateProto("steady"))
	assert.Equal(t, inferv1.AutoscalingState_AUTOSCALING_STATE_SCALING_UP, autoscalingStateProto("scaling-up"))
	assert.Equal(t, inferv1.AutoscalingState_AUTOSCALING_STATE_SCALING_DOWN, autoscalingStateProto("scaling-down"))
	assert.Equal(t, inferv1.AutoscalingState_AUTOSCALING_STATE_SCALED_TO_ZERO, autoscalingStateProto("scaled-to-zero"))
	assert.Equal(t, inferv1.AutoscalingState_AUTOSCALING_STATE_COLD_STARTING, autoscalingStateProto("cold-starting"))
	assert.Equal(t, inferv1.AutoscalingState_AUTOSCALING_STATE_ERROR, autoscalingStateProto("error"))
	assert.Equal(t, inferv1.AutoscalingState_AUTOSCALING_STATE_UNSPECIFIED, autoscalingStateProto("unknown"))
}
