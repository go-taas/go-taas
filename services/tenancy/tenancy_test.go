package tenancy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// newTestDB opens a fresh in-memory sqlite database with the tenancy
// schema applied.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func TestRepositoryOrganizationCRUD(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	// Create.
	org := &Organization{ID: "org-a", DisplayName: "Org A", State: StateActive}
	require.NoError(t, repo.CreateOrganization(ctx, org))

	// Duplicate id maps to 10015.
	err := repo.CreateOrganization(ctx, &Organization{ID: "org-a", DisplayName: "Dup", State: StateActive})
	assert.EqualValues(t, apierrors.CodeOrganizationExists, apierrors.CodeOf(err))

	// Find.
	found, err := repo.FindOrganization(ctx, "org-a")
	require.NoError(t, err)
	assert.Equal(t, "Org A", found.DisplayName)
	assert.Equal(t, StateActive, found.State)

	// Unknown id passes gorm.ErrRecordNotFound through.
	_, err = repo.FindOrganization(ctx, "org-missing")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Update.
	updated, err := repo.UpdateOrganization(ctx, "org-a", "Org A2", "desc")
	require.NoError(t, err)
	assert.Equal(t, "Org A2", updated.DisplayName)
	assert.Equal(t, "desc", updated.Description)

	// State transitions are idempotent.
	disabled, err := repo.SetOrganizationState(ctx, "org-a", StateDisabled)
	require.NoError(t, err)
	assert.Equal(t, StateDisabled, disabled.State)
	disabledAgain, err := repo.SetOrganizationState(ctx, "org-a", StateDisabled)
	require.NoError(t, err)
	assert.Equal(t, StateDisabled, disabledAgain.State)

	// Unknown id on update/state passes ErrRecordNotFound through.
	_, err = repo.UpdateOrganization(ctx, "org-missing", "x", "")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = repo.SetOrganizationState(ctx, "org-missing", StateActive)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestRepositoryListOrganizations(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	for _, id := range []string{"org-a", "org-b", "org-c"} {
		require.NoError(t, repo.CreateOrganization(ctx, &Organization{ID: id, DisplayName: id, State: StateActive}))
	}
	_, err := repo.SetOrganizationState(ctx, "org-b", StateDisabled)
	require.NoError(t, err)

	// No filter: all three.
	rows, total, err := repo.ListOrganizations(ctx, OrgFilter{}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, rows, 3)

	// State filter.
	rows, total, err = repo.ListOrganizations(ctx, OrgFilter{State: StateDisabled}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "org-b", rows[0].ID)

	// Pagination.
	rows, total, err = repo.ListOrganizations(ctx, OrgFilter{}, 1, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, rows, 1)
}

func TestRepositoryProjectCRUD(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.CreateOrganization(ctx, &Organization{ID: "org-a", DisplayName: "Org A", State: StateActive}))

	// Create.
	project := &Project{ID: "proj-1", OrganizationID: "org-a", DisplayName: "P1", State: StateActive}
	require.NoError(t, repo.CreateProject(ctx, project))

	// Duplicate id maps to 10016.
	err := repo.CreateProject(ctx, &Project{ID: "proj-1", OrganizationID: "org-a", DisplayName: "Dup", State: StateActive})
	assert.EqualValues(t, apierrors.CodeProjectExists, apierrors.CodeOf(err))

	// Find.
	found, err := repo.FindProject(ctx, "proj-1")
	require.NoError(t, err)
	assert.Equal(t, "org-a", found.OrganizationID)

	// Unknown id.
	_, err = repo.FindProject(ctx, "proj-missing")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Update.
	updated, err := repo.UpdateProject(ctx, "proj-1", "P1b", "d")
	require.NoError(t, err)
	assert.Equal(t, "P1b", updated.DisplayName)

	// State.
	_, err = repo.SetProjectState(ctx, "proj-1", StateDisabled)
	require.NoError(t, err)

	// List with filters.
	require.NoError(t, repo.CreateProject(ctx, &Project{ID: "proj-2", OrganizationID: "org-a", DisplayName: "P2", State: StateActive}))
	rows, total, err := repo.ListProjects(ctx, ProjectFilter{OrganizationID: "org-a"}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Len(t, rows, 2)

	rows, total, err = repo.ListProjects(ctx, ProjectFilter{State: StateActive}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "proj-2", rows[0].ID)
}

func TestRepositoryCountsAndSeed(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.CreateOrganization(ctx, &Organization{ID: "org-a", DisplayName: "Org A", State: StateActive}))
	require.NoError(t, repo.CreateProject(ctx, &Project{ID: "proj-1", OrganizationID: "org-a", DisplayName: "P1", State: StateActive}))

	count, err := repo.CountOrganizations(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)

	projects, err := repo.ProjectCountByOrganization(ctx, "org-a")
	require.NoError(t, err)
	assert.EqualValues(t, 1, projects)

	// The api_keys / inference_services tables do not exist in this
	// schema; the count queries must degrade to zero rows without
	// failing the caller (sqlite reports missing tables as errors, so
	// these helpers are exercised in FVT instead). Here we only assert
	// the project count path.

	// Seed: insert-only, idempotent.
	require.NoError(t, repo.SeedDefaultOrganization(ctx, "org-default", "Default Organization"))
	require.NoError(t, repo.SeedDefaultOrganization(ctx, "org-default", "Default Organization"))
	seeded, err := repo.FindOrganization(ctx, "org-default")
	require.NoError(t, err)
	assert.Equal(t, "Default Organization", seeded.DisplayName)
}

func TestOrgGuard(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	guard := NewOrgGuard(db)
	ctx := context.Background()

	require.NoError(t, repo.CreateOrganization(ctx, &Organization{ID: "org-active", DisplayName: "A", State: StateActive}))
	require.NoError(t, repo.CreateOrganization(ctx, &Organization{ID: "org-off", DisplayName: "B", State: StateDisabled}))

	// RequireExists: unknown → 10005, known → nil.
	err := guard.RequireExists(ctx, "org-missing")
	assert.EqualValues(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))
	assert.NoError(t, guard.RequireExists(ctx, "org-active"))
	assert.NoError(t, guard.RequireExists(ctx, "org-off"))

	// RequireActive: unknown → 10005, disabled → 10017, active → nil.
	err = guard.RequireActive(ctx, "org-missing")
	assert.EqualValues(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))
	err = guard.RequireActive(ctx, "org-off")
	assert.EqualValues(t, apierrors.CodeOrganizationDisabled, apierrors.CodeOf(err))
	assert.NoError(t, guard.RequireActive(ctx, "org-active"))
}
