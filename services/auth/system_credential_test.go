package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
)

// newSystemCredService builds an auth service bound to a test database.
func newSystemCredService(t *testing.T) (*Service, *APIKeyRepository) {
	t.Helper()
	db := newAPIKeyTestDB(t)
	repo := NewAPIKeyRepository(db)
	svc := NewWithRepositoryAndCache(repo, newFakeVerdictCache(), config.Argon2Params{
		Algorithm: "argon2id", Time: 1, MemoryMiB: 16, Parallelism: 1,
	})
	return svc, repo
}

func TestSystemCredentialSeedAndAccessor(t *testing.T) {
	svc, repo := newSystemCredService(t)
	ctx := context.Background()

	// Before seeding the accessor fails closed (500) so a load test can
	// never drive traffic without a credential.
	_, err := svc.GetSystemCredential(ctx)
	require.Error(t, err)

	require.NoError(t, svc.EnsureSystemCredential(ctx))

	cred, err := svc.GetSystemCredential(ctx)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(cred, "sk-"), "credential must use the sk- scheme")

	// The seeded row is flagged system and verifies against the
	// credential the runner received (AD11: the gateway recognizes it).
	row, err := repo.FindSystemCredential(ctx)
	require.NoError(t, err)
	assert.True(t, row.IsSystem)
	assert.Equal(t, systemCredentialOrgID, row.OrganizationID)
	assert.Equal(t, KeyDigest(cred), row.LookupHash)
	assert.True(t, VerifyKeyHash(row.LookupHash, row.Salt, row.SaltedHash, svc.hashParamsFor()))

	// Re-seeding is idempotent: exactly one system row is kept.
	require.NoError(t, svc.EnsureSystemCredential(ctx))
	var count int64
	require.NoError(t, newAPIKeyTestDBQuery(t, repo).Model(&APIKey{}).Where("is_system = ?", true).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestSystemCredentialAbsentWhenNotSeeded(t *testing.T) {
	_, repo := newSystemCredService(t)
	_, err := repo.FindSystemCredential(context.Background())
	require.Error(t, err, "a missing system credential must not resolve")
}

// newAPIKeyTestDBQuery exposes the repository's DB handle for count
// assertions without exporting it from the repository.
func newAPIKeyTestDBQuery(t *testing.T, repo *APIKeyRepository) *gorm.DB {
	t.Helper()
	return repo.db.DB(context.Background())
}
