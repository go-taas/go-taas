package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeAutoscalingProvider is an injected AutoscalingProvider (feature
// #16, AD13).
type fakeAutoscalingProvider struct {
	projection *modelv1.ModelAutoscaling
	err        error
}

func (f fakeAutoscalingProvider) ModelAutoscaling(context.Context, string, string) (*modelv1.ModelAutoscaling, error) {
	return f.projection, f.err
}

// TestServiceListAvailableModelsAutoscaling covers the autoscaling
// projection on the user-realm catalog (feature #16, AD13): each model
// carries a read-only autoscaling summary when the provider is wired.
func TestServiceListAvailableModelsAutoscaling(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	registerOne(t, svc, "open-model")

	svc.SetAutoscalingProvider(fakeAutoscalingProvider{
		projection: &modelv1.ModelAutoscaling{
			Autoscaled: true, CurrentReplicas: 3, MinReplicas: 1,
			MaxReplicas: 10, ScaleToZero: false, State: "steady",
		},
	})

	resp, err := svc.ListAvailableModels(context.Background(), &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 1)
	require.NotNil(t, resp.GetModels()[0].GetAutoscaling())
	assert.True(t, resp.GetModels()[0].GetAutoscaling().GetAutoscaled())
	assert.Equal(t, int32(3), resp.GetModels()[0].GetAutoscaling().GetCurrentReplicas())
	assert.Equal(t, "steady", resp.GetModels()[0].GetAutoscaling().GetState())
}

// TestServiceListAvailableModelsNoProvider covers the nil-provider case:
// the projection is omitted (feature #16, AD13).
func TestServiceListAvailableModelsNoProvider(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	registerOne(t, svc, "open-model")

	resp, err := svc.ListAvailableModels(context.Background(), &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 1)
	assert.Nil(t, resp.GetModels()[0].GetAutoscaling())
}

// TestServiceGetAvailableModel covers the new GetAvailableModel RPC
// (feature #16, AD13): it returns the masked projection plus the
// autoscaling summary, and 10101 for an unknown model.
func TestServiceGetAvailableModel(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	modelID := registerOne(t, svc, "open-model")

	svc.SetAutoscalingProvider(fakeAutoscalingProvider{
		projection: &modelv1.ModelAutoscaling{
			Autoscaled: true, CurrentReplicas: 2, MinReplicas: 1,
			MaxReplicas: 5, ScaleToZero: true, State: "scaled-to-zero",
		},
	})

	resp, err := svc.GetAvailableModel(context.Background(), &modelv1.GetAvailableModelRequest{ModelId: modelID})
	require.NoError(t, err)
	require.NotNil(t, resp.GetModel())
	assert.Equal(t, modelID, resp.GetModel().GetModelId())
	require.NotNil(t, resp.GetAutoscaling())
	assert.True(t, resp.GetAutoscaling().GetScaleToZero())
	assert.Equal(t, "scaled-to-zero", resp.GetAutoscaling().GetState())
}

// TestServiceGetAvailableModelNotFound covers the unknown-model path.
func TestServiceGetAvailableModelNotFound(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	registerOne(t, svc, "open-model")

	_, err := svc.GetAvailableModel(context.Background(), &modelv1.GetAvailableModelRequest{ModelId: "missing"})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))
}

// TestServiceGetAvailableModelNoSession covers the unauthorized path.
func TestServiceGetAvailableModelNoSession(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	_, err := svc.GetAvailableModel(context.Background(), &modelv1.GetAvailableModelRequest{ModelId: "x"})
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))
}