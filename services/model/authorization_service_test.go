package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/tenancy"
)

// fakeSessionResolver is an injected SessionResolver (AD8).
type fakeSessionResolver struct {
	userID string
	err    error
}

func (f fakeSessionResolver) SessionUserID(context.Context) (string, error) {
	return f.userID, f.err
}

// newAuthorizationTestService returns a service whose org guard is wired
// against a database that carries the organizations table, so the
// unknown-organization path (10005) is exercised.
func newAuthorizationTestService(t *testing.T, orgIDs ...string) *Service {
	t.Helper()
	db := newModelTestDB(t)
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, id := range orgIDs {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: id, DisplayName: id, State: tenancy.StateActive,
		}).Error)
	}
	svc := NewForFVT(db)
	svc.SetOrgGuard(tenancy.NewOrgGuard(db))
	return svc
}

// registerOne registers a model with a single version and returns its id.
func registerOne(t *testing.T, svc *Service, name string) string {
	t.Helper()
	resp, err := svc.RegisterModel(context.Background(), &modelv1.RegisterModelRequest{
		Name: name, Version: "v1", WeightPath: name + "/v1",
		HubModelId: name + "/hub",
	})
	require.NoError(t, err)
	return resp.GetModelId()
}

// TestServiceGrantModelAccess covers AC1 (the grant is stored), AC2 (a
// repeated grant is an idempotent no-op), the unknown-model (10101) and
// unknown-organization (10005) errors, and AD8 (granted_by is the
// resolved caller, with the transitional fallbacks).
func TestServiceGrantModelAccess(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a", "org-b")
	ctx := context.Background()
	modelID := registerOne(t, svc, "qwen-3b")

	// AC1: the first grant stores the row.
	_, err := svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-a",
	})
	require.NoError(t, err)

	repo, err := svc.repository()
	require.NoError(t, err)
	rows, _, err := repo.ListAuthorizations(ctx, modelID, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "org-a", rows[0].OrganizationID)
	assert.Equal(t, grantedByPlaceholder, rows[0].GrantedBy,
		"AD8: no session and no org header falls back to the placeholder")
	assert.False(t, rows[0].CreatedAt.IsZero())

	// AD8: the session resolver wins when it is wired.
	svc.SetSessionResolver(fakeSessionResolver{userID: "u-admin"})
	_, err = svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-b",
	})
	require.NoError(t, err)
	rows, _, err = repo.ListAuthorizations(ctx, modelID, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "u-admin", rows[0].GrantedBy, "AD8: granted_by is the caller's user id")

	// AC2: granting org-a again is an idempotent no-op.
	_, err = svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-a",
	})
	require.NoError(t, err, "AC2: a duplicate grant succeeds")
	count, err := repo.CountAuthorizations(ctx, modelID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, count, "AC2: no duplicate row")

	// Unknown model → 10101.
	_, err = svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: "no-such-model", OrganizationId: "org-a",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)

	// Unknown organization → 10005.
	_, err = svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-missing",
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeOrganizationNotFound, ae.Code)

	// An empty organization is refused rather than silently recorded.
	_, err = svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{ModelId: modelID})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeOrganizationNotFound, ae.Code)
}

// TestServiceGrantModelAccessWithoutOrgGuard pins the injection pattern:
// with no guard wired the grant still succeeds (unit-test mode).
func TestServiceGrantModelAccessWithoutOrgGuard(t *testing.T) {
	svc := newModelTestService(t)
	modelID := registerOne(t, svc, "qwen-3b")

	_, err := svc.GrantModelAccess(context.Background(), &modelv1.GrantModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-any",
	})
	require.NoError(t, err)
}

// TestServiceGrantModelAccessFallsBackWithoutIdentity covers the AD8
// fallback chain: a failing session resolver and an absent both fall back
// to the transitional caller organization and then to the placeholder,
// and a grant is never failed because the caller is unresolvable.
func TestServiceGrantModelAccessFallsBackWithoutIdentity(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	ctx := context.Background()
	repo, err := svc.repository()
	require.NoError(t, err)

	// No resolver: the transitional org header is recorded.
	withHeader := metadata.NewIncomingContext(ctx, metadata.Pairs(organizationMetadataKey, "org-a"))
	modelID := registerOne(t, svc, "qwen-3b")
	_, err = svc.GrantModelAccess(withHeader, &modelv1.GrantModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-a",
	})
	require.NoError(t, err)
	rows, _, err := repo.ListAuthorizations(ctx, modelID, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "org-a", rows[0].GrantedBy, "AD8: the transitional org is the fallback")

	// A failing resolver falls back the same way; the grant still lands.
	svc.SetSessionResolver(fakeSessionResolver{err: errors.New("no session")})
	second := registerOne(t, svc, "qwen-7b")
	_, err = svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: second, OrganizationId: "org-a",
	})
	require.NoError(t, err, "an unresolvable caller never fails the grant")
	rows, _, err = repo.ListAuthorizations(ctx, second, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, grantedByPlaceholder, rows[0].GrantedBy)
}

// TestServiceRevokeModelAccess covers AC3/AC4 and the unknown-model error.
func TestServiceRevokeModelAccess(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a", "org-b")
	ctx := context.Background()
	modelID := registerOne(t, svc, "qwen-3b")
	repo, err := svc.repository()
	require.NoError(t, err)

	_, err = svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-a",
	})
	require.NoError(t, err)

	// AC4: revoking an organization without a grant is a no-op success.
	_, err = svc.RevokeModelAccess(ctx, &modelv1.RevokeModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-b",
	})
	require.NoError(t, err)
	count, err := repo.CountAuthorizations(ctx, modelID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)

	// AC3: revoking the last grant empties the grant list.
	_, err = svc.RevokeModelAccess(ctx, &modelv1.RevokeModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-a",
	})
	require.NoError(t, err)
	count, err = repo.CountAuthorizations(ctx, modelID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, count)

	// Unknown model → 10101.
	_, err = svc.RevokeModelAccess(ctx, &modelv1.RevokeModelAccessRequest{
		ModelId: "no-such-model", OrganizationId: "org-a",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)

	// An empty organization is refused.
	_, err = svc.RevokeModelAccess(ctx, &modelv1.RevokeModelAccessRequest{ModelId: modelID})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeOrganizationNotFound, ae.Code)
}

// TestServiceListModelAuthorizations covers AC10 (newest first,
// paginated) and the unknown-model error.
func TestServiceListModelAuthorizations(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a", "org-b", "org-c")
	ctx := context.Background()
	modelID := registerOne(t, svc, "qwen-3b")
	repo, err := svc.repository()
	require.NoError(t, err)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedGrant(t, repo, modelID, "org-old", base)
	seedGrant(t, repo, modelID, "org-new", base.Add(time.Hour))

	resp, err := svc.ListModelAuthorizations(ctx, &modelv1.ListModelAuthorizationsRequest{
		ModelId: modelID,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetAuthorizations(), 2)
	assert.Equal(t, "org-new", resp.GetAuthorizations()[0].GetOrganizationId(), "AC10: newest first")
	assert.Equal(t, "u-admin", resp.GetAuthorizations()[0].GetGrantedBy())
	assert.NotZero(t, resp.GetAuthorizations()[0].GetCreatedAt(), "AC10: created_at is exposed")
	require.NotNil(t, resp.GetPageMeta())
	assert.EqualValues(t, 2, resp.GetPageMeta().GetTotal())

	// Pagination: the page is capped, the total spans every row.
	resp, err = svc.ListModelAuthorizations(ctx, &modelv1.ListModelAuthorizationsRequest{
		ModelId: modelID,
		Page:    &commonv1.PageRequest{Offset: 1, Limit: 1},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetAuthorizations(), 1)
	assert.Equal(t, "org-old", resp.GetAuthorizations()[0].GetOrganizationId())
	assert.EqualValues(t, 2, resp.GetPageMeta().GetTotal())

	// Unknown model → 10101.
	_, err = svc.ListModelAuthorizations(ctx, &modelv1.ListModelAuthorizationsRequest{
		ModelId: "no-such-model",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)

	// Empty model id → 10101.
	_, err = svc.ListModelAuthorizations(ctx, &modelv1.ListModelAuthorizationsRequest{})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)
}

// TestServiceListModelsOrganizationFilter covers AC11 and AC13 at the RPC
// level: the org filter hides a restricted model the org is not granted,
// and the restricted flag is populated either way.
func TestServiceListModelsOrganizationFilter(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a", "org-b")
	ctx := context.Background()
	restrictedID := registerOne(t, svc, "qwen-3b")
	openID := registerOne(t, svc, "llama-8b")
	_, err := svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: restrictedID, OrganizationId: "org-a",
	})
	require.NoError(t, err)

	// Unfiltered: every model, with the restricted flag populated.
	resp, err := svc.ListModels(ctx, &modelv1.ListModelsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 2)
	flags := map[string]bool{}
	for _, m := range resp.GetModels() {
		flags[m.GetModelId()] = m.GetRestricted()
	}
	assert.True(t, flags[restrictedID], "AC13: a granted model is restricted")
	assert.False(t, flags[openID], "AC13: a zero-grant model is not")

	// org-b holds no grant: the restricted model is hidden.
	resp, err = svc.ListModels(ctx, &modelv1.ListModelsRequest{OrganizationId: "org-b"})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 1)
	assert.Equal(t, openID, resp.GetModels()[0].GetModelId(), "AC11: only unrestricted models")
	assert.EqualValues(t, 1, resp.GetPageMeta().GetTotal(), "AC11: the total respects the filter")

	// org-a is granted: it sees both.
	resp, err = svc.ListModels(ctx, &modelv1.ListModelsRequest{OrganizationId: "org-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetModels(), 2)
	assert.EqualValues(t, 2, resp.GetPageMeta().GetTotal())
}

// TestAuthorizationRPCDatabaseFailures pins that a database failure is
// surfaced as an error by every authorization RPC instead of being
// mistaken for a successful no-op.
func TestAuthorizationRPCDatabaseFailures(t *testing.T) {
	db := newModelTestDB(t)
	svc := NewForFVT(db)
	ctx := context.Background()
	modelID := registerOne(t, svc, "qwen-3b")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	_, err = svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-a",
	})
	require.Error(t, err, "grant must not report success without a row")
	_, err = svc.RevokeModelAccess(ctx, &modelv1.RevokeModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-a",
	})
	require.Error(t, err, "revoke must not report success without a delete")
	_, err = svc.ListModelAuthorizations(ctx, &modelv1.ListModelAuthorizationsRequest{
		ModelId: modelID,
	})
	require.Error(t, err, "list must not report an empty grant list on failure")
	_, err = svc.ListModels(ctx, &modelv1.ListModelsRequest{})
	require.Error(t, err)
	_, err = svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: modelID})
	require.Error(t, err)
}

// TestServiceGetModelRestrictedFlag covers AC13 on the detail read.
func TestServiceGetModelRestrictedFlag(t *testing.T) {
	svc := newAuthorizationTestService(t, "org-a")
	ctx := context.Background()
	modelID := registerOne(t, svc, "qwen-3b")

	resp, err := svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: modelID})
	require.NoError(t, err)
	assert.False(t, resp.GetModel().GetRestricted(), "zero grants: not restricted")

	_, err = svc.GrantModelAccess(ctx, &modelv1.GrantModelAccessRequest{
		ModelId: modelID, OrganizationId: "org-a",
	})
	require.NoError(t, err)

	resp, err = svc.GetModel(ctx, &modelv1.GetModelRequest{ModelId: modelID})
	require.NoError(t, err)
	assert.True(t, resp.GetModel().GetRestricted(), "AC13: one grant restricts the model")
}
