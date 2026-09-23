package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// testEnv wires a Service against an in-memory SQLite database and the
// in-memory fake verdict cache.
type testEnv struct {
	svc   *Service
	repo  *APIKeyRepository
	cache *fakeVerdictCache
	db    *gorm.DB
}

func newTestService(t *testing.T) *testEnv {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&APIKey{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	repo := NewAPIKeyRepository(db)
	cache := newFakeVerdictCache()
	svc := NewWithRepositoryAndCache(repo, cache, config.Argon2Params{
		Algorithm: "argon2id", Time: 1, MemoryMiB: 16, Parallelism: 1,
	})
	return &testEnv{svc: svc, repo: repo, cache: cache, db: db}
}

// cachedVerdict returns the cached verdict for testing cache assertions.
func (f *fakeVerdictCache) cachedVerdict(t *testing.T, digest string) *keyVerdict {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.entries[digest]
	if !ok {
		return nil
	}
	return v.verdict
}

func orgCtx(org string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-organization-id", org))
}

func TestCreateAPIKey(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	resp, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "my key"})
	require.NoError(t, err)
	assert.Regexp(t, `^sk-[A-Za-z0-9]{43}$`, resp.GetApiKey())
	assert.NotEmpty(t, resp.GetKeyId())

	// The stored row carries salt + salted hash, no plaintext (AC2).
	row, err := env.repo.FindByLookupHash(ctx, KeyDigest(resp.GetApiKey()))
	require.NoError(t, err)
	assert.Equal(t, resp.GetKeyId(), row.ID)
	assert.NotEmpty(t, row.Salt)
	assert.NotEmpty(t, row.SaltedHash)
	assert.Equal(t, "org-a", row.OrganizationID)
	assert.Equal(t, resp.GetApiKey()[:8], row.Prefix)
	assert.False(t, row.Revoked)
	assert.Nil(t, row.ExpiresAt)
}

func TestCreateAPIKeyValidation(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	cases := []struct {
		name string
		req  *authv1.CreateAPIKeyRequest
	}{
		{"empty name", &authv1.CreateAPIKeyRequest{Name: ""}},
		{"whitespace name", &authv1.CreateAPIKeyRequest{Name: "   "}},
		{"name too long", &authv1.CreateAPIKeyRequest{Name: string(make([]byte, 65))}},
		{"past expiry", &authv1.CreateAPIKeyRequest{Name: "k", ExpiresAt: time.Now().Add(-time.Hour).Unix()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.svc.CreateAPIKey(ctx, tc.req)
			require.Error(t, err)
			ae, ok := apierrors.As(err)
			require.True(t, ok, "must be a business error")
			assert.Equal(t, apierrors.CodeAPIKeyInvalid, ae.Code)
		})
	}

	// Missing organization header.
	_, err := env.svc.CreateAPIKey(context.Background(), &authv1.CreateAPIKeyRequest{Name: "k"})
	require.Error(t, err)
	ae, _ := apierrors.As(err)
	assert.Equal(t, apierrors.CodeUnauthorized, ae.Code)
}

func TestCreateAPIKeyWithExpiry(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	expiry := time.Now().Add(24 * time.Hour).Unix()
	resp, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k", ExpiresAt: expiry})
	require.NoError(t, err)

	row, err := env.repo.FindByLookupHash(ctx, KeyDigest(resp.GetApiKey()))
	require.NoError(t, err)
	require.NotNil(t, row.ExpiresAt)
	assert.Equal(t, expiry, row.ExpiresAt.Unix())
}

func TestListAPIKeys(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	// Create three keys for org-a and one for org-b.
	for _, name := range []string{"one", "two", "three"} {
		_, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: name})
		require.NoError(t, err)
	}
	_, err := env.svc.CreateAPIKey(orgCtx("org-b"), &authv1.CreateAPIKeyRequest{Name: "other"})
	require.NoError(t, err)

	resp, err := env.svc.ListAPIKeys(ctx, &authv1.ListAPIKeysRequest{})
	require.NoError(t, err)
	assert.Equal(t, int64(3), resp.GetPageMeta().GetTotal())
	require.Len(t, resp.GetKeys(), 3)
	// Masked summaries only: prefix present, no secret or hash (AC3).
	for _, k := range resp.GetKeys() {
		assert.Len(t, k.GetPrefix(), 8)
		assert.Regexp(t, `^sk-[A-Za-z0-9]{5}$`, k.GetPrefix())
	}
}

func TestListAPIKeysPaginationAndActiveOnly(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	var lastID string
	for i := 0; i < 3; i++ {
		resp, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: fmt.Sprintf("k%d", i)})
		require.NoError(t, err)
		lastID = resp.GetKeyId()
	}
	// Revoke the newest one.
	_, err := env.svc.RevokeAPIKey(ctx, &authv1.RevokeAPIKeyRequest{KeyId: lastID})
	require.NoError(t, err)

	// Page 2 with limit 2 (dotted pagination is a gateway concern; the
	// RPC takes page.offset/page.limit directly).
	resp, err := env.svc.ListAPIKeys(ctx, &authv1.ListAPIKeysRequest{
		Page: &commonv1.PageRequest{Offset: 2, Limit: 1},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3), resp.GetPageMeta().GetTotal())
	require.Len(t, resp.GetKeys(), 1)

	// active_only excludes the revoked key server-side.
	resp, err = env.svc.ListAPIKeys(ctx, &authv1.ListAPIKeysRequest{ActiveOnly: true})
	require.NoError(t, err)
	assert.Equal(t, int64(2), resp.GetPageMeta().GetTotal())
	require.Len(t, resp.GetKeys(), 2)
	for _, k := range resp.GetKeys() {
		assert.False(t, k.GetRevoked())
	}
}

func TestRevokeAPIKey(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)

	// Verify first so the cache is populated.
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
	})
	require.NoError(t, err)
	require.NotNil(t, env.cache.cachedVerdict(t, KeyDigest(created.GetApiKey())))

	// Revoke.
	_, err = env.svc.RevokeAPIKey(ctx, &authv1.RevokeAPIKeyRequest{KeyId: created.GetKeyId()})
	require.NoError(t, err)

	// Cache entry deleted at revoke time (AC5).
	assert.Nil(t, env.cache.cachedVerdict(t, KeyDigest(created.GetApiKey())))

	// Verify now rejects with API_KEY_REVOKED.
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeAPIKeyRevoked, ae.Code)

	// Idempotent re-revoke succeeds (AC6).
	_, err = env.svc.RevokeAPIKey(ctx, &authv1.RevokeAPIKeyRequest{KeyId: created.GetKeyId()})
	require.NoError(t, err)

	// revoked_at is exposed in the list for audit display.
	listResp, err := env.svc.ListAPIKeys(ctx, &authv1.ListAPIKeysRequest{})
	require.NoError(t, err)
	require.Len(t, listResp.GetKeys(), 1)
	assert.True(t, listResp.GetKeys()[0].GetRevoked())
	assert.NotZero(t, listResp.GetKeys()[0].GetRevokedAt())
}

func TestRevokeAPIKeyErrors(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	// Empty key id.
	_, err := env.svc.RevokeAPIKey(ctx, &authv1.RevokeAPIKeyRequest{KeyId: ""})
	require.Error(t, err)
	ae, _ := apierrors.As(err)
	assert.Equal(t, apierrors.CodeAPIKeyInvalid, ae.Code)

	// Missing organization.
	_, err = env.svc.RevokeAPIKey(context.Background(), &authv1.RevokeAPIKeyRequest{KeyId: "x"})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeUnauthorized, ae.Code)

	// Cross-org revoke -> not found (AC6).
	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)
	_, err = env.svc.RevokeAPIKey(orgCtx("org-b"), &authv1.RevokeAPIKeyRequest{KeyId: created.GetKeyId()})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeAPIKeyNotFound, ae.Code)
}

func TestVerifyAPIKey(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())

	// AC4: created key verifies, returns org / KeyID / role.
	resp, err := env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	require.NoError(t, err)
	assert.Equal(t, "org-a", resp.GetOrganizationId())
	assert.Equal(t, created.GetKeyId(), resp.GetKeyId())
	assert.Equal(t, "agent", resp.GetRole())

	// Second call is served from the cache (still correct).
	resp, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	require.NoError(t, err)
	assert.Equal(t, "org-a", resp.GetOrganizationId())
}

func TestVerifyAPIKeyRejections(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	// Empty digest stays codes.InvalidArgument (existing behavior).
	_, err := env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	// Malformed digest.
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: "not-hex"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeAPIKeyInvalid, ae.Code)

	// Unknown digest.
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: KeyDigest("sk-unknown")})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeAPIKeyNotFound, ae.Code)

	// Expired key: lazy check, no background job (AC7).
	past := time.Now().Add(-time.Minute).Unix()
	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())
	require.NoError(t, env.db.Model(&APIKey{}).Where("id = ?", created.GetKeyId()).
		Update("expires_at", time.Unix(past, 0)).Error)
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeAPIKeyExpired, ae.Code)

	// Hash mismatch after lookup hit: integrity alert.
	created2, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k2"})
	require.NoError(t, err)
	digest2 := KeyDigest(created2.GetApiKey())
	require.NoError(t, env.db.Model(&APIKey{}).Where("id = ?", created2.GetKeyId()).
		Update("salted_hash", "tampered").Error)
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest2})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeAPIKeyInvalid, ae.Code)
}

func TestResolveOrganizationID(t *testing.T) {
	// Present.
	ctx := orgCtx("org-a")
	org, err := resolveOrganizationID(ctx)
	require.NoError(t, err)
	assert.Equal(t, "org-a", org)

	// Missing.
	_, err = resolveOrganizationID(context.Background())
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeUnauthorized, ae.Code)

	// Empty value.
	ctx = metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-organization-id", ""))
	_, err = resolveOrganizationID(ctx)
	require.Error(t, err)
}

func TestMigrate(t *testing.T) {
	env := newTestService(t)
	// Migrate on a fresh database creates the table.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()+"-migrate")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	svc := NewWithRepositoryAndCache(NewAPIKeyRepository(db), newFakeVerdictCache(),
		config.Argon2Params{Algorithm: "argon2id", Time: 1, MemoryMiB: 16, Parallelism: 1})
	require.NoError(t, svc.Migrate(context.Background()))
	assert.True(t, db.Migrator().HasTable(&APIKey{}))
	_ = env
}
