package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeModelOrgResolver is an injected SessionOrgResolver (feature-17 AD6).
type fakeModelOrgResolver struct{ org string }

func (f fakeModelOrgResolver) SessionActiveOrg(context.Context) (string, error) { return f.org, nil }

// fakeProviderNames resolves a provider name for the external marker.
type fakeProviderNames struct{ name string }

func (f fakeProviderNames) ProviderName(context.Context, string) (string, error) { return f.name, nil }

// TestCreateExternalModel covers the publish delegation (feature #48 AD4):
// create, live-conflict, and re-enable of an unpublished same-named model.
func TestCreateExternalModel(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	cm, err := svc.CreateExternalModel(ctx, "external-gpt", "prov-1", "gpt-4o")
	require.NoError(t, err)
	assert.True(t, cm.External)
	assert.True(t, cm.Published)
	assert.Equal(t, "external-gpt", cm.Name)

	// A live name conflict -> 13014.
	_, err = svc.CreateExternalModel(ctx, "external-gpt", "prov-1", "gpt-4o")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderPublishConflict, apierrors.CodeOf(err))

	// Unpublish then re-create: the same model is re-enabled, no new row.
	require.NoError(t, svc.SetExternalModelPublished(ctx, cm.ID, false))
	again, err := svc.CreateExternalModel(ctx, "external-gpt", "prov-1", "gpt-4o")
	require.NoError(t, err)
	assert.Equal(t, cm.ID, again.ID)
	assert.True(t, again.Published)
}

// TestSetAndLookupExternalModelFailClosed covers 13015.
func TestSetAndLookupExternalModelFailClosed(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()

	err := svc.SetExternalModelPublished(ctx, "missing", true)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderNotPublished, apierrors.CodeOf(err))

	_, err = svc.LookupExternalModel(ctx, "missing", "")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderNotPublished, apierrors.CodeOf(err))

	// A self-hosted model is not an external model.
	repo, err := svc.repository()
	require.NoError(t, err)
	require.NoError(t, repo.CreateModel(ctx, &Model{ID: "self-1", Name: "self-hosted"}))
	_, err = svc.LookupExternalModel(ctx, "self-1", "")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderNotPublished, apierrors.CodeOf(err))
}

// TestExternalProjections covers AC6/AC11: the admin projection carries the
// external marker and provider fields; the masked user projection carries
// only external + provider_name and hides unpublished models.
func TestExternalProjections(t *testing.T) {
	svc := newModelTestService(t)
	ctx := context.Background()
	svc.SetProviderNameResolver(fakeProviderNames{name: "acme"})
	svc.SetSessionOrgResolver(fakeModelOrgResolver{org: "org-a"})

	cm, err := svc.CreateExternalModel(ctx, "external-gpt", "prov-1", "gpt-4o")
	require.NoError(t, err)

	// Admin ListModels carries the external marker + provider fields.
	resp, err := svc.ListModels(ctx, &modelv1.ListModelsRequest{})
	require.NoError(t, err)
	var admin *modelv1.ModelSummary
	for _, m := range resp.GetModels() {
		if m.GetModelId() == cm.ID {
			admin = m
		}
	}
	require.NotNil(t, admin)
	assert.True(t, admin.GetExternal())
	assert.Equal(t, "acme", admin.GetProviderName())
	assert.Equal(t, "prov-1", admin.GetProviderId())
	assert.Equal(t, "gpt-4o", admin.GetUpstreamModelId())
	assert.Empty(t, admin.GetLatestVersion(), "an external model has no version row")

	// Admin GetModel carries the same marker.
	got, err := svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: cm.ID})
	require.NoError(t, err)
	assert.True(t, got.GetModel().GetExternal())
	assert.Equal(t, "prov-1", got.GetModel().GetProviderId())

	// The user projection carries only external + provider_name.
	avail, err := svc.ListAvailableModels(ctx, &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	var user *modelv1.AvailableModel
	for _, m := range avail.GetModels() {
		if m.GetModelId() == cm.ID {
			user = m
		}
	}
	require.NotNil(t, user)
	assert.True(t, user.GetExternal())
	assert.Equal(t, "acme", user.GetProviderName())

	// Unpublishing hides the model from the user projection.
	require.NoError(t, svc.SetExternalModelPublished(ctx, cm.ID, false))
	avail, err = svc.ListAvailableModels(ctx, &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	for _, m := range avail.GetModels() {
		assert.NotEqual(t, cm.ID, m.GetModelId(), "an unpublished model must be hidden from the user catalog")
	}
	_, err = svc.GetAvailableModel(ctx, &modelv1.GetAvailableModelRequest{ModelId: cm.ID})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeModelNotFound, apierrors.CodeOf(err))
}
