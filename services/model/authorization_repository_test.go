package model

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedModel inserts a model with a given name and returns it.
func seedModel(t *testing.T, repo *Repository, name string) *Model {
	t.Helper()
	m := &Model{Name: name, CreatedAt: time.Now().UTC()}
	require.NoError(t, repo.CreateModel(context.Background(), m))
	return m
}

// seedGrant inserts a grant row with an explicit creation time (AC10
// needs a strict ordering that granting in a loop cannot guarantee).
func seedGrant(t *testing.T, repo *Repository, modelID, orgID string, at time.Time) {
	t.Helper()
	require.NoError(t, repo.authorizations.Create(context.Background(), &Authorization{
		ID:             uuid.NewString(),
		ModelID:        modelID,
		OrganizationID: orgID,
		GrantedBy:      "u-admin",
		CreatedAt:      at,
	}))
}

// TestAuthorizationGrantAccessIdempotent covers AC1 and AC2: the first
// grant stores the row with granted_by and created_at, and a repeated
// grant writes no second row.
func TestAuthorizationGrantAccessIdempotent(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	m := seedModel(t, repo, "qwen-3b")

	require.NoError(t, repo.GrantAccess(ctx, m.ID, "org-a", "u-admin"))

	rows, total, err := repo.ListAuthorizations(ctx, m.ID, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "org-a", rows[0].OrganizationID)
	assert.Equal(t, "u-admin", rows[0].GrantedBy)
	assert.False(t, rows[0].CreatedAt.IsZero(), "AC1: created_at is stored")

	// AC2: the duplicate grant is a no-op, not a second row and not an
	// error.
	require.NoError(t, repo.GrantAccess(ctx, m.ID, "org-a", "u-other"))
	count, err := repo.CountAuthorizations(ctx, m.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count, "AC2: duplicate grant writes no second row")

	// The grant covers the model, not the organization globally.
	require.NoError(t, repo.GrantAccess(ctx, m.ID, "org-b", "u-admin"))
	other := seedModel(t, repo, "qwen-7b")
	count, err = repo.CountAuthorizations(ctx, other.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, count, "grants are per model")
}

// TestAuthorizationRevokeAccessIdempotent covers AC3 and AC4: revoking a
// grant that does not exist is a no-op success, and revoking the last
// grant returns the model to default-allow.
func TestAuthorizationRevokeAccessIdempotent(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	m := seedModel(t, repo, "qwen-3b")
	require.NoError(t, repo.GrantAccess(ctx, m.ID, "org-a", "u-admin"))

	// AC4: an organization without a grant revokes cleanly.
	require.NoError(t, repo.RevokeAccess(ctx, m.ID, "org-b"))
	count, err := repo.CountAuthorizations(ctx, m.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count, "AC4: nothing else is removed")
	require.NoError(t, repo.RevokeAccess(ctx, m.ID, "org-b"), "AC4: repeated revoke stays a no-op")

	// AC3: removing the last grant empties the list.
	require.NoError(t, repo.RevokeAccess(ctx, m.ID, "org-a"))
	count, err = repo.CountAuthorizations(ctx, m.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, count, "AC3: the last grant is gone")
}

// TestIsModelAuthorizedDefaultAllow covers the AD2/AC9 default-allow rule
// and AC3's return to it.
func TestIsModelAuthorizedDefaultAllow(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	m := seedModel(t, repo, "qwen-3b")

	// Zero grants: every organization is allowed.
	for _, org := range []string{"org-a", "org-b", "org-never-seen"} {
		allowed, err := repo.IsModelAuthorized(ctx, m.ID, org)
		require.NoError(t, err)
		assert.True(t, allowed, "AC9: zero grants means open to %s", org)
	}

	// One grant restricts the model to that organization.
	require.NoError(t, repo.GrantAccess(ctx, m.ID, "org-a", "u-admin"))
	allowed, err := repo.IsModelAuthorized(ctx, m.ID, "org-a")
	require.NoError(t, err)
	assert.True(t, allowed, "AC5: the granted organization is allowed")
	allowed, err = repo.IsModelAuthorized(ctx, m.ID, "org-b")
	require.NoError(t, err)
	assert.False(t, allowed, "AC5: a restricted model denies a non-granted organization")

	has, err := repo.HasAuthorization(ctx, m.ID, "org-a")
	require.NoError(t, err)
	assert.True(t, has, "HasAuthorization ignores the default-allow rule")
	has, err = repo.HasAuthorization(ctx, m.ID, "org-b")
	require.NoError(t, err)
	assert.False(t, has)

	// Revoking the last grant returns the model to default-allow.
	require.NoError(t, repo.RevokeAccess(ctx, m.ID, "org-a"))
	allowed, err = repo.IsModelAuthorized(ctx, m.ID, "org-b")
	require.NoError(t, err)
	assert.True(t, allowed, "AC3: default-allow is back")
}

// TestListAuthorizationsOrderingAndPagination covers AC10: newest first,
// paginated, with the total over the whole grant list.
func TestListAuthorizationsOrderingAndPagination(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	m := seedModel(t, repo, "qwen-3b")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedGrant(t, repo, m.ID, "org-old", base)
	seedGrant(t, repo, m.ID, "org-mid", base.Add(time.Hour))
	seedGrant(t, repo, m.ID, "org-new", base.Add(2*time.Hour))
	// Another model's grants must never leak into the page.
	other := seedModel(t, repo, "qwen-7b")
	seedGrant(t, repo, other.ID, "org-other", base.Add(3*time.Hour))

	rows, total, err := repo.ListAuthorizations(ctx, m.ID, 0, 2)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Len(t, rows, 2)
	assert.Equal(t, "org-new", rows[0].OrganizationID, "AC10: newest first")
	assert.Equal(t, "org-mid", rows[1].OrganizationID)

	rows, total, err = repo.ListAuthorizations(ctx, m.ID, 2, 2)
	require.NoError(t, err)
	require.EqualValues(t, 3, total, "AC10: the total spans every page")
	require.Len(t, rows, 1)
	assert.Equal(t, "org-old", rows[0].OrganizationID)
}

// TestRestrictedModelIDs covers AC13: only models with at least one grant
// row are flagged.
func TestRestrictedModelIDs(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	restricted := seedModel(t, repo, "qwen-3b")
	open := seedModel(t, repo, "llama-8b")
	require.NoError(t, repo.GrantAccess(ctx, restricted.ID, "org-a", "u-admin"))

	flags, err := repo.RestrictedModelIDs(ctx, []string{restricted.ID, open.ID})
	require.NoError(t, err)
	assert.True(t, flags[restricted.ID])
	assert.False(t, flags[open.ID])

	flags, err = repo.RestrictedModelIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, flags)
}

// TestListModelsForOrganization covers AC11: every unrestricted model is
// always returned, plus the restricted models the organization holds.
func TestListModelsForOrganization(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()

	grantedToA := seedModel(t, repo, "for-a")
	grantedToB := seedModel(t, repo, "for-b")
	seedModel(t, repo, "open")
	require.NoError(t, repo.GrantAccess(ctx, grantedToA.ID, "org-a", "u-admin"))
	require.NoError(t, repo.GrantAccess(ctx, grantedToB.ID, "org-b", "u-admin"))

	// org-c holds no grant: it sees the unrestricted model only.
	rows, total, err := repo.ListModelsForOrganization(ctx, "org-c", 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "open", rows[0].Name)

	// org-a sees its own restricted model plus the unrestricted one.
	rows, total, err = repo.ListModelsForOrganization(ctx, "org-a", 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.Len(t, rows, 2)
	assert.ElementsMatch(t, []string{"for-a", "open"}, []string{rows[0].Name, rows[1].Name})

	// The unfiltered catalog still returns everything.
	rows, total, err = repo.ListModels(ctx, 0, 20)
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	require.Len(t, rows, 3)
}

// TestDeleteModelCascadesAuthorizations pins the AD1 note that the model
// module owns and cascades its grant rows, so a re-registered model never
// inherits a stale grant list.
func TestDeleteModelCascadesAuthorizations(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	m := seedModel(t, repo, "qwen-3b")
	require.NoError(t, repo.GrantAccess(ctx, m.ID, "org-a", "u-admin"))
	other := seedModel(t, repo, "qwen-7b")
	require.NoError(t, repo.GrantAccess(ctx, other.ID, "org-a", "u-admin"))

	require.NoError(t, repo.DeleteModel(ctx, m.ID))

	count, err := repo.CountAuthorizations(ctx, m.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, count, "the deleted model's grants are cascaded")
	count, err = repo.CountAuthorizations(ctx, other.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count, "another model's grants are untouched")
}

// TestIsModelAuthorizedAcceptsTheServedName covers the data-plane caller.
//
// The gateway only knows what the caller typed in the request body, so it
// sends the served model name rather than the catalog id. Comparing a name to
// the uuid-typed model_id column is a SQL type error, not a "no grants"
// answer — and that error surfaced as a 503 on every gateway-verified
// inference request.
func TestIsModelAuthorizedAcceptsTheServedName(t *testing.T) {
	repo := newModelTestRepo(t)
	ctx := context.Background()
	m := seedModel(t, repo, "qwen2.5-0.5b")

	allowed, err := repo.IsModelAuthorized(ctx, "qwen2.5-0.5b", "org-a")
	require.NoError(t, err, "a served name must not be compared to a uuid column")
	assert.True(t, allowed, "an unrestricted model stays open when named")

	// The name must reach the same grant list as the id.
	require.NoError(t, repo.GrantAccess(ctx, m.ID, "org-a", "u-admin"))
	allowed, err = repo.IsModelAuthorized(ctx, "qwen2.5-0.5b", "org-a")
	require.NoError(t, err)
	assert.True(t, allowed, "the granted organization is allowed by name")

	allowed, err = repo.IsModelAuthorized(ctx, "qwen2.5-0.5b", "org-b")
	require.NoError(t, err)
	assert.False(t, allowed, "a restricted model denies a non-granted organization by name")
}

// TestIsModelAuthorizedUnknownNameIsOpen covers an alias the catalog has never
// seen. It cannot carry a grant, so default-allow applies and routing is left
// to answer for it.
func TestIsModelAuthorizedUnknownNameIsOpen(t *testing.T) {
	repo := newModelTestRepo(t)
	allowed, err := repo.IsModelAuthorized(context.Background(), "some-fine-tune", "org-a")
	require.NoError(t, err)
	assert.True(t, allowed)
}

// TestIsModelAuthorizedEmptyReferenceIsOpen covers a model-less call.
func TestIsModelAuthorizedEmptyReferenceIsOpen(t *testing.T) {
	repo := newModelTestRepo(t)
	allowed, err := repo.IsModelAuthorized(context.Background(), "", "org-a")
	require.NoError(t, err)
	assert.True(t, allowed)
}
