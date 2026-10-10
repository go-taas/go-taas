package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	"github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/audit"
)

// fakeModelExistenceChecker is the unit-test double of the
// ModelExistenceChecker seam (feature #46, AD6).
type fakeModelExistenceChecker struct {
	known map[string]bool
}

func (f *fakeModelExistenceChecker) ModelExists(_ context.Context, modelID string) (bool, error) {
	return f.known[modelID], nil
}

// recordingAuditRecorder captures audit events for assertions (the
// AuditRecorder seam's unit-test double).
type recordingAuditRecorder struct {
	events []*audit.AuditEvent
}

func (r *recordingAuditRecorder) Record(_ context.Context, ev *audit.AuditEvent) {
	r.events = append(r.events, ev)
}

// TestCreateAPIKeyWithScope verifies the create-time scope (feature #46,
// AC1): the list is stored, echoed by the summary, and carried into the
// cached verdict.
func TestCreateAPIKeyWithScope(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{
		Name:   "scoped",
		Models: []string{"m-1", "m-2"},
	})
	require.NoError(t, err)

	// The stored row carries the encoded allow-list.
	row, err := env.repo.FindByLookupHash(ctx, KeyDigest(created.GetApiKey()))
	require.NoError(t, err)
	assert.Equal(t, `["m-1","m-2"]`, row.Models)

	// ListAPIKeys echoes the scope per row (the admin read surface).
	listed, err := env.svc.ListAPIKeys(ctx, &authv1.ListAPIKeysRequest{})
	require.NoError(t, err)
	require.Len(t, listed.GetKeys(), 1)
	assert.Equal(t, []string{"m-1", "m-2"}, listed.GetKeys()[0].GetModels())

	// The cached verdict carries the scope (AD2).
	digest := KeyDigest(created.GetApiKey())
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest, Model: "m-1"})
	require.NoError(t, err)
	verdict := env.cache.cachedVerdict(t, digest)
	require.NotNil(t, verdict)
	assert.Equal(t, []string{"m-1", "m-2"}, verdict.Models)
}

// TestCreateAPIKeyScopeValidation covers the shared validator (feature
// #46, §5.2): duplicates, >50 entries, blank IDs, unknown catalog IDs.
func TestCreateAPIKeyScopeValidation(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	// Duplicate IDs -> 10008.
	_, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{
		Name: "k", Models: []string{"m-1", "m-1"},
	})
	require.Error(t, err)
	ae, ok := errors.As(err)
	require.True(t, ok)
	assert.Equal(t, errors.CodeAPIKeyInvalid, ae.Code)

	// Blank ID -> 10008.
	_, err = env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{
		Name: "k", Models: []string{"m-1", "  "},
	})
	require.Error(t, err)
	ae, _ = errors.As(err)
	assert.Equal(t, errors.CodeAPIKeyInvalid, ae.Code)

	// More than 50 entries -> 10008.
	tooMany := make([]string, 51)
	for i := range tooMany {
		tooMany[i] = "m"
	}
	_, err = env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k", Models: tooMany})
	require.Error(t, err)
	ae, _ = errors.As(err)
	assert.Equal(t, errors.CodeAPIKeyInvalid, ae.Code)

	// Unknown catalog ID -> 10101 when the checker is wired.
	env.svc.SetModelExistenceChecker(&fakeModelExistenceChecker{known: map[string]bool{"m-1": true}})
	_, err = env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{
		Name: "k", Models: []string{"m-1", "m-missing"},
	})
	require.Error(t, err)
	ae, _ = errors.As(err)
	assert.Equal(t, errors.CodeModelNotFound, ae.Code)

	// Nil checker (transitional fail-open): unknown IDs are accepted.
	env.svc.SetModelExistenceChecker(nil)
	_, err = env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{
		Name: "k", Models: []string{"m-missing"},
	})
	require.NoError(t, err)
}

// TestVerifyAPIKeyScopeEnforcement covers the data-plane gate (feature
// #46, AC2/AC3): in-scope models pass on both cache paths, out-of-scope
// models fail with 10039, and an unscoped key is byte-for-byte
// unchanged.
func TestVerifyAPIKeyScopeEnforcement(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{
		Name: "scoped", Models: []string{"m-1"},
	})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())

	// Cache miss path: in-scope model passes.
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest, Model: "m-1"})
	require.NoError(t, err)

	// Cache hit path: out-of-scope model -> 10039.
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest, Model: "m-2"})
	require.Error(t, err)
	ae, ok := errors.As(err)
	require.True(t, ok)
	assert.Equal(t, errors.CodeAPIKeyModelNotAllowed, ae.Code)

	// Cache hit path: in-scope model still passes.
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest, Model: "m-1"})
	require.NoError(t, err)

	// No model named: the scope check is skipped (gateway behavior for
	// model-less calls stays unchanged).
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	require.NoError(t, err)

	// Unscoped key: any model passes (AC3).
	unscoped, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "unscoped"})
	require.NoError(t, err)
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: KeyDigest(unscoped.GetApiKey()), Model: "m-anything",
	})
	require.NoError(t, err)
}

// TestUpdateAPIKeyScope covers the scope edit RPC (feature #46, AC4):
// replace, clear, cache invalidation, cross-org masking, revoked keys
// and the empty key_id guard.
func TestUpdateAPIKeyScope(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)
	digest := KeyDigest(created.GetApiKey())

	// Prime the cache with an unrestricted verdict.
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest, Model: "m-1"})
	require.NoError(t, err)
	require.NotNil(t, env.cache.cachedVerdict(t, digest))

	// Replace the scope.
	resp, err := env.svc.UpdateAPIKeyScope(ctx, &authv1.UpdateAPIKeyScopeRequest{
		KeyId: created.GetKeyId(), Models: []string{"m-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"m-1"}, resp.GetKey().GetModels())

	// The cache entry was deleted: the next verify repopulates it with
	// the scoped verdict and enforces it.
	require.Nil(t, env.cache.cachedVerdict(t, digest))
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest, Model: "m-2"})
	require.Error(t, err)
	ae, ok := errors.As(err)
	require.True(t, ok)
	assert.Equal(t, errors.CodeAPIKeyModelNotAllowed, ae.Code)

	// Clear the scope: every model passes again.
	_, err = env.svc.UpdateAPIKeyScope(ctx, &authv1.UpdateAPIKeyScopeRequest{KeyId: created.GetKeyId()})
	require.NoError(t, err)
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest, Model: "m-2"})
	require.NoError(t, err)

	// Another org's key -> 10007 (no cross-org existence leak, AC8).
	otherCtx := orgCtx("org-b")
	_, err = env.svc.UpdateAPIKeyScope(otherCtx, &authv1.UpdateAPIKeyScopeRequest{
		KeyId: created.GetKeyId(), Models: []string{"m-1"},
	})
	require.Error(t, err)
	ae, _ = errors.As(err)
	assert.Equal(t, errors.CodeAPIKeyNotFound, ae.Code)

	// Missing key_id -> 10008.
	_, err = env.svc.UpdateAPIKeyScope(ctx, &authv1.UpdateAPIKeyScopeRequest{})
	require.Error(t, err)
	ae, _ = errors.As(err)
	assert.Equal(t, errors.CodeAPIKeyInvalid, ae.Code)

	// Revoked key -> 10009.
	_, err = env.svc.RevokeAPIKey(ctx, &authv1.RevokeAPIKeyRequest{KeyId: created.GetKeyId()})
	require.NoError(t, err)
	_, err = env.svc.UpdateAPIKeyScope(ctx, &authv1.UpdateAPIKeyScopeRequest{
		KeyId: created.GetKeyId(), Models: []string{"m-1"},
	})
	require.Error(t, err)
	ae, _ = errors.As(err)
	assert.Equal(t, errors.CodeAPIKeyRevoked, ae.Code)
}

// TestUpdateAPIKeyScopeAudit verifies the audit event (feature #46,
// AC7): action, resource and before/after metadata.
func TestUpdateAPIKeyScopeAudit(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{
		Name: "k", Models: []string{"m-1"},
	})
	require.NoError(t, err)

	rec := &recordingAuditRecorder{}
	env.svc.SetAuditRecorder(rec)

	_, err = env.svc.UpdateAPIKeyScope(ctx, &authv1.UpdateAPIKeyScopeRequest{
		KeyId: created.GetKeyId(), Models: []string{"m-2", "m-3"},
	})
	require.NoError(t, err)

	require.Len(t, rec.events, 1)
	ev := rec.events[0]
	assert.Equal(t, "api_key.scope_updated", ev.Action)
	assert.Equal(t, "api_key", ev.ResourceType)
	assert.Equal(t, created.GetKeyId(), ev.ResourceID)
	assert.Equal(t, "org-a", ev.OrganizationID)
	assert.Contains(t, ev.Metadata, `"before":["m-1"]`)
	assert.Contains(t, ev.Metadata, `"after":["m-2","m-3"]`)
}
