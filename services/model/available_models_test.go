package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/tenancy"
)

// fakeSessionOrgResolver is an injected SessionOrgResolver (feature-17
// AD6).
type fakeSessionOrgResolver struct {
	org string
	err error
}

func (f fakeSessionOrgResolver) SessionActiveOrg(context.Context) (string, error) {
	return f.org, f.err
}

// TestServiceListAvailableModels covers the masked user-realm catalog
// (feature-17 AD8/AD11): the org is resolved from the session (or the
// transitional header), the default-allow rule applies, and the
// projection carries no weight_path or grant rows.
func TestServiceListAvailableModels(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a", "org-b")
	ctx := context.Background()

	// Two models: one open (zero grants), one restricted to org-a.
	openID := registerOne(t, svc, "open-model")
	restrictedID := registerOne(t, svc, "restricted-model")
	_, err := svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: restrictedID, OrganizationId: "org-a",
	})
	require.NoError(t, err)

	// Session resolves org-a: sees both models.
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	resp, err := svc.ListAvailableModels(ctx, &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	ids := map[string]bool{}
	for _, m := range resp.GetModels() {
		ids[m.GetModelId()] = true
	}
	assert.True(t, ids[openID])
	assert.True(t, ids[restrictedID])
	// The projection is masked: the wire type has no weight_path field
	// (feature-17 AD8), so nothing to assert beyond the model id/name.
	assert.NotEmpty(t, resp.GetModels()[0].GetModelId())

	// Session resolves org-b: sees only the open model.
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-b"})
	resp, err = svc.ListAvailableModels(ctx, &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	ids = map[string]bool{}
	for _, m := range resp.GetModels() {
		ids[m.GetModelId()] = true
	}
	assert.True(t, ids[openID])
	assert.False(t, ids[restrictedID])

	// No session resolver and no header → 10001.
	svc.SetSessionOrgResolver(nil)
	_, err = svc.ListAvailableModels(ctx, &modelv1.ListAvailableModelsRequest{})
	assert.EqualValues(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))

	// No session resolver, header org-b → sees only the open model.
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(organizationMetadataKey, "org-b"))
	resp, err = svc.ListAvailableModels(ctx, &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	ids = map[string]bool{}
	for _, m := range resp.GetModels() {
		ids[m.GetModelId()] = true
	}
	assert.True(t, ids[openID])
	assert.False(t, ids[restrictedID])
}

// TestServiceListAvailableModelsSessionError covers a session resolver
// that fails (e.g. an invalid session) propagating the error.
func TestServiceListAvailableModelsSessionError(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{err: apierrors.New(apierrors.CodeSessionInvalid)})
	_, err := svc.ListAvailableModels(context.Background(), &modelv1.ListAvailableModelsRequest{})
	assert.EqualValues(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))
}

// TestServiceListAvailableModelsTenancyGuard ensures the tenancy schema
// is present for the org filter query.
func TestServiceListAvailableModelsTenancyGuard(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	registerOne(t, svc, "m")
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	resp, err := svc.ListAvailableModels(context.Background(), &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	assert.NotNil(t, resp)
	_ = tenancy.StateActive
}
// fakePriceLookup is an injected PriceLookup double: it answers with the
// rates it was given, keyed by model id.
type fakePriceLookup struct {
	rates map[string]Rate
	err   error
	asked []string
}

func (f *fakePriceLookup) CurrentModelRates(_ context.Context, modelIDs []string) (map[string]Rate, error) {
	f.asked = append(f.asked, modelIDs...)
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]Rate{}
	for _, id := range modelIDs {
		if rate, ok := f.rates[id]; ok {
			out[id] = rate
		}
	}
	return out, nil
}

// TestServiceListAvailableModelsCarriesPricing covers the rate card the
// console shows next to each model: the projection carries the current
// input/output/cache rates and nothing else of the internal price row, and a
// lookup failure degrades to "no price" rather than failing the catalog.
func TestServiceListAvailableModelsCarriesPricing(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	ctx := context.Background()
	id := registerOne(t, svc, "priced-model")

	lookup := &fakePriceLookup{rates: map[string]Rate{
		id: {
			InputPerMillion:  2,
			OutputPerMillion: 8,
			CachedPerMillion: 0.4,
			Currency:         "CNY",
			AcceleratorType:  "default",
		},
	}}
	svc.SetPriceLookup(lookup)
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})

	resp, err := svc.ListAvailableModels(ctx, &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 1)
	pricing := resp.GetModels()[0].GetPricing()
	require.NotNil(t, pricing)
	assert.Equal(t, 2.0, pricing.GetInputPricePerMillion())
	assert.Equal(t, 8.0, pricing.GetOutputPricePerMillion())
	assert.Equal(t, 0.4, pricing.GetCachedPricePerMillion())
	assert.Equal(t, "CNY", pricing.GetCurrency())
	assert.Contains(t, lookup.asked, id, "the catalog asks for every listed model in one call")

	// A lookup failure leaves the catalog usable: the models still list,
	// they just carry no price.
	svc.SetPriceLookup(&fakePriceLookup{err: apierrors.Newf(apierrors.CodeInternal, "db down")})
	resp, err = svc.ListAvailableModels(ctx, &modelv1.ListAvailableModelsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 1)
	assert.Nil(t, resp.GetModels()[0].GetPricing())
}
