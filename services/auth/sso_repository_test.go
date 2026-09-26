package auth

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// newSSOTestDB opens a fresh in-memory sqlite database with the SSO
// schema applied.
func newSSOTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&SSOProvider{}, &IdentityBinding{}, &User{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func TestSSORepositoryProviderCRUD(t *testing.T) {
	db := newSSOTestDB(t)
	repo := NewSSORepository(db)
	ctx := context.Background()

	// Create.
	prov := &SSOProvider{ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta", Enabled: false}
	require.NoError(t, repo.CreateProvider(ctx, prov))

	// Duplicate id maps to 10020.
	err := repo.CreateProvider(ctx, &SSOProvider{ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Dup"})
	assert.EqualValues(t, apierrors.CodeSSOProviderExists, apierrors.CodeOf(err))

	// Find.
	found, err := repo.FindProvider(ctx, "okta")
	require.NoError(t, err)
	assert.Equal(t, "Okta", found.DisplayName)

	// Unknown id passes ErrRecordNotFound through.
	_, err = repo.FindProvider(ctx, "missing")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// Update.
	updated, err := repo.UpdateProvider(ctx, "okta", &SSOProvider{DisplayName: "Okta2", AllowAutoProvision: false})
	require.NoError(t, err)
	assert.Equal(t, "Okta2", updated.DisplayName)
	assert.False(t, updated.AllowAutoProvision)

	// State transitions are idempotent.
	enabled, err := repo.SetProviderEnabled(ctx, "okta", true)
	require.NoError(t, err)
	assert.True(t, enabled.Enabled)
	enabledAgain, err := repo.SetProviderEnabled(ctx, "okta", true)
	require.NoError(t, err)
	assert.True(t, enabledAgain.Enabled)

	// Unknown id on update/state passes ErrRecordNotFound through.
	_, err = repo.UpdateProvider(ctx, "missing", &SSOProvider{DisplayName: "X"})
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, err = repo.SetProviderEnabled(ctx, "missing", true)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestSSORepositoryListProviders(t *testing.T) {
	db := newSSOTestDB(t)
	repo := NewSSORepository(db)
	ctx := context.Background()

	for _, id := range []string{"okta", "github", "ldap1"} {
		require.NoError(t, repo.CreateProvider(ctx, &SSOProvider{ID: id, Type: ProviderTypeOIDC, DisplayName: id}))
	}
	_, err := repo.SetProviderEnabled(ctx, "github", true)
	require.NoError(t, err)

	// No filter: all three.
	rows, total, err := repo.ListProviders(ctx, ProviderFilter{}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, rows, 3)

	// Enabled filter.
	enabled := true
	rows, total, err = repo.ListProviders(ctx, ProviderFilter{Enabled: &enabled}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	assert.Equal(t, "github", rows[0].ID)

	// Pagination.
	rows, total, err = repo.ListProviders(ctx, ProviderFilter{}, 1, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, rows, 1)
}

func TestSSORepositoryBindingCRUD(t *testing.T) {
	db := newSSOTestDB(t)
	repo := NewSSORepository(db)
	ctx := context.Background()

	require.NoError(t, repo.CreateProvider(ctx, &SSOProvider{ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta"}))
	require.NoError(t, repo.CreateUser(ctx, &User{ID: "user-1", Username: "alice"}))

	// Create binding.
	b := &IdentityBinding{ID: "b-1", ProviderID: "okta", ExternalSubject: "iss:sub-1", UserID: "user-1"}
	require.NoError(t, repo.CreateBinding(ctx, b))

	// Duplicate (provider, subject) maps to 10026.
	err := repo.CreateBinding(ctx, &IdentityBinding{ID: "b-2", ProviderID: "okta", ExternalSubject: "iss:sub-1", UserID: "user-1"})
	assert.EqualValues(t, apierrors.CodeIdentityBindingExists, apierrors.CodeOf(err))

	// Find by subject.
	found, err := repo.FindBindingBySubject(ctx, "okta", "iss:sub-1")
	require.NoError(t, err)
	assert.Equal(t, "user-1", found.UserID)

	// Unknown subject passes ErrRecordNotFound through.
	_, err = repo.FindBindingBySubject(ctx, "okta", "iss:missing")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// List with filters.
	require.NoError(t, repo.CreateBinding(ctx, &IdentityBinding{ID: "b-3", ProviderID: "okta", ExternalSubject: "iss:sub-2", UserID: "user-1"}))
	rows, total, err := repo.ListBindings(ctx, BindingFilter{ProviderID: "okta"}, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Len(t, rows, 2)

	// Delete (idempotent).
	require.NoError(t, repo.DeleteBinding(ctx, "b-1"))
	require.NoError(t, repo.DeleteBinding(ctx, "b-1"))
	_, err = repo.FindBindingBySubject(ctx, "okta", "iss:sub-1")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestSSORepositoryUserCRUD(t *testing.T) {
	db := newSSOTestDB(t)
	repo := NewSSORepository(db)
	ctx := context.Background()

	require.NoError(t, repo.CreateUser(ctx, &User{ID: "user-1", Username: "alice", Email: "a@x.com"}))

	// Duplicate username maps to 10004.
	err := repo.CreateUser(ctx, &User{ID: "user-2", Username: "alice"})
	assert.EqualValues(t, apierrors.CodeUserExists, apierrors.CodeOf(err))

	// Find.
	found, err := repo.FindUser(ctx, "user-1")
	require.NoError(t, err)
	assert.Equal(t, "alice", found.Username)

	// Unknown id passes ErrRecordNotFound through.
	_, err = repo.FindUser(ctx, "user-missing")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestSSORepositoryCountBindingsByProvider(t *testing.T) {
	db := newSSOTestDB(t)
	repo := NewSSORepository(db)
	ctx := context.Background()

	require.NoError(t, repo.CreateProvider(ctx, &SSOProvider{ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta"}))
	require.NoError(t, repo.CreateUser(ctx, &User{ID: "user-1", Username: "alice"}))

	count, err := repo.CountBindingsByProvider(ctx, "okta")
	require.NoError(t, err)
	assert.EqualValues(t, 0, count)

	require.NoError(t, repo.CreateBinding(ctx, &IdentityBinding{ID: "b-1", ProviderID: "okta", ExternalSubject: "s1", UserID: "user-1"}))
	count, err = repo.CountBindingsByProvider(ctx, "okta")
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
}
