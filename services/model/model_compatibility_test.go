package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
)

// fakeCompatibilityProvider is an injected CompatibilityProvider
// (feature #19, AD12).
type fakeCompatibilityProvider struct {
	summary string
	err     error
}

func (f fakeCompatibilityProvider) ModelCompatibilitySummary(context.Context, string) (string, error) {
	return f.summary, f.err
}

// TestServiceListAvailableModelsCompatibility covers the masked
// compatibility summary on the user-realm catalog (feature #19, AD12):
// each model carries the summary when the provider is wired.
func TestServiceListAvailableModelsCompatibility(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	registerOne(t, svc, "open-model")

	svc.SetCompatibilityProvider(fakeCompatibilityProvider{summary: "vLLM · A800, H800"})

	resp, err := svc.ListAvailableModels(context.Background(), &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 1)
	assert.Equal(t, "vLLM · A800, H800", resp.GetModels()[0].GetCompatibility())
}

// TestServiceListAvailableModelsNoCompatibilityProvider covers the
// nil-provider case: the summary is omitted (feature #19, AD12).
func TestServiceListAvailableModelsNoCompatibilityProvider(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	registerOne(t, svc, "open-model")

	resp, err := svc.ListAvailableModels(context.Background(), &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 1)
	assert.Empty(t, resp.GetModels()[0].GetCompatibility())
}

// TestServiceGetAvailableModelCompatibility covers the compatibility
// summary on the single-model read (feature #19, AD12).
func TestServiceGetAvailableModelCompatibility(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	modelID := registerOne(t, svc, "open-model")

	svc.SetCompatibilityProvider(fakeCompatibilityProvider{summary: "SGLang · M100"})

	resp, err := svc.GetAvailableModel(context.Background(), &modelv1.GetAvailableModelRequest{ModelId: modelID})
	require.NoError(t, err)
	require.NotNil(t, resp.GetModel())
	assert.Equal(t, "SGLang · M100", resp.GetModel().GetCompatibility())
}
