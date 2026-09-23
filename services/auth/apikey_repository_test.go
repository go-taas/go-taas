package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

func newAPIKeyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&APIKey{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func newTestRepo(t *testing.T) *APIKeyRepository {
	t.Helper()
	return NewAPIKeyRepository(newAPIKeyTestDB(t))
}

func seedKey(t *testing.T, repo *APIKeyRepository, org, name string, created time.Time) *APIKey {
	t.Helper()
	key := &APIKey{
		ID:             fmt.Sprintf("id-%s-%s", org, name),
		Name:           name,
		Prefix:         "sk-aaaaaa",
		LookupHash:     fmt.Sprintf("hash-%s-%s", org, name),
		Salt:           "c2FsdA==",
		SaltedHash:     "aGFzaA==",
		OrganizationID: org,
		CreatedAt:      created,
	}
	require.NoError(t, repo.Create(context.Background(), key))
	return key
}

func TestRepositoryListByOrganization(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	seedKey(t, repo, "org-a", "first", base)
	seedKey(t, repo, "org-a", "second", base.Add(time.Hour))
	seedKey(t, repo, "org-a", "third", base.Add(2*time.Hour))
	seedKey(t, repo, "org-b", "other", base.Add(3*time.Hour))

	// All keys of org-a, newest first.
	rows, total, err := repo.ListByOrganization(ctx, "org-a", 0, 20, false, base)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, rows, 3)
	assert.Equal(t, "third", rows[0].Name)
	assert.Equal(t, "second", rows[1].Name)
	assert.Equal(t, "first", rows[2].Name)

	// Pagination.
	rows, total, err = repo.ListByOrganization(ctx, "org-a", 1, 2, false, base)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, rows, 2)
	assert.Equal(t, "second", rows[0].Name)

	// Empty organization.
	_, total, err = repo.ListByOrganization(ctx, "org-none", 0, 20, false, base)
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
}

func TestRepositoryListActiveOnly(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	active := seedKey(t, repo, "org", "active", now.Add(-time.Hour))
	revoked := seedKey(t, repo, "org", "revoked", now.Add(-2*time.Hour))
	expired := seedKey(t, repo, "org", "expired", now.Add(-3*time.Hour))

	// Revoke one.
	_, err := repo.RevokeByIDAndOrganization(ctx, "org", revoked.ID, now)
	require.NoError(t, err)
	// Expire one.
	past := now.Add(-time.Minute)
	require.NoError(t, repo.DB(ctx).Model(&APIKey{}).
		Where("id = ?", expired.ID).Update("expires_at", past).Error)

	rows, total, err := repo.ListByOrganization(ctx, "org", 0, 20, true, now)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, active.ID, rows[0].ID)
}

func TestRepositoryFindByLookupHash(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	seeded := seedKey(t, repo, "org", "k", time.Now())

	got, err := repo.FindByLookupHash(ctx, seeded.LookupHash)
	require.NoError(t, err)
	assert.Equal(t, seeded.ID, got.ID)

	_, err = repo.FindByLookupHash(ctx, "missing")
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeAPIKeyNotFound, ae.Code)
}

func TestRepositoryRevokeIdempotentPreservesFirstRevokedAt(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	seeded := seedKey(t, repo, "org", "k", time.Now())

	first := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)

	hash, err := repo.RevokeByIDAndOrganization(ctx, "org", seeded.ID, first)
	require.NoError(t, err)
	assert.Equal(t, seeded.LookupHash, hash)

	// Re-revoke succeeds and preserves the first revoked_at.
	hash2, err := repo.RevokeByIDAndOrganization(ctx, "org", seeded.ID, second)
	require.NoError(t, err)
	assert.Equal(t, seeded.LookupHash, hash2)

	got, err := repo.FindByLookupHash(ctx, seeded.LookupHash)
	require.NoError(t, err)
	assert.True(t, got.Revoked)
	require.NotNil(t, got.RevokedAt)
	assert.Equal(t, first.Unix(), got.RevokedAt.Unix())
}

func TestRepositoryRevokeCrossOrgNotFound(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()
	seeded := seedKey(t, repo, "org-a", "k", time.Now())

	// AC6: revoking a key of another organization returns not found.
	_, err := repo.RevokeByIDAndOrganization(ctx, "org-b", seeded.ID, time.Now())
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeAPIKeyNotFound, ae.Code)

	// Unknown key id.
	_, err = repo.RevokeByIDAndOrganization(ctx, "org-a", "no-such-id", time.Now())
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeAPIKeyNotFound, ae.Code)
}

func TestAPIKeyTableName(t *testing.T) {
	assert.Equal(t, "api_keys", (&APIKey{}).TableName())
}
