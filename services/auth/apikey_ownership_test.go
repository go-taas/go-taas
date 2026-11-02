package auth

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/tenancy"
)

// fakeProjectLookup is the ProjectLookup seam for the ownership tests.
type fakeProjectLookup struct {
	projects map[string]string // project id -> owning org
	names    map[string]string
	members  map[string]bool // "project|user" -> member
	orgUsers map[string]bool // "org|user" -> member
}

func (f fakeProjectLookup) ProjectInOrg(_ context.Context, projectID, orgID string) (string, bool, error) {
	org, ok := f.projects[projectID]
	if !ok || org != orgID {
		return "", false, nil
	}
	return f.names[projectID], true, nil
}

func (f fakeProjectLookup) ProjectName(_ context.Context, projectID string) (string, error) {
	return f.names[projectID], nil
}

func (f fakeProjectLookup) UserInProject(_ context.Context, projectID, userID string) (bool, error) {
	return f.members[projectID+"|"+userID], nil
}

func (f fakeProjectLookup) UserInOrg(_ context.Context, orgID, userID string) (bool, error) {
	return f.orgUsers[orgID+"|"+userID], nil
}

// fakeQuotaResolver is the three-level resolver seam for the verdict test.
type fakeQuotaResolver struct {
	quota tenancy.EffectiveQuota
	err   error
	calls int
}

func (f *fakeQuotaResolver) EffectiveQuota(context.Context, string, string, string) (tenancy.EffectiveQuota, error) {
	f.calls++
	return f.quota, f.err
}

func TestKeyAccessManages(t *testing.T) {
	owner := "u1"
	other := "u2"
	owned := &APIKey{OwnerUserID: &owner}
	shared := &APIKey{}

	// A session-less caller keeps the transitional full access.
	assert.True(t, keyAccess{}.manages(owned))
	assert.True(t, keyAccess{}.manages(shared))

	member := keyAccess{userID: "u1", hasSession: true}
	assert.True(t, member.manages(owned))
	assert.False(t, member.manages(&APIKey{OwnerUserID: &other}))
	assert.False(t, member.manages(shared), "a shared key is administrators only")

	admin := keyAccess{userID: "admin", hasSession: true, admin: true}
	assert.True(t, admin.manages(owned))
	assert.True(t, admin.manages(&APIKey{OwnerUserID: &other}))
	assert.True(t, admin.manages(shared))
}

func TestResolveRequestedOwner(t *testing.T) {
	lookup := fakeProjectLookup{orgUsers: map[string]bool{"org-a|u1": true, "org-a|u2": true}}
	svc := &Service{projectLookup: lookup}
	ctx := context.Background()

	// A member's key is their own.
	owner, err := svc.resolveRequestedOwner(ctx, "org-a", keyAccess{userID: "u1", hasSession: true}, "", false)
	require.NoError(t, err)
	require.NotNil(t, owner)
	assert.Equal(t, "u1", *owner)

	// A member cannot create a shared key.
	_, err = svc.resolveRequestedOwner(ctx, "org-a", keyAccess{userID: "u1", hasSession: true}, "", true)
	assert.Error(t, err)

	// A member cannot assign another owner.
	_, err = svc.resolveRequestedOwner(ctx, "org-a", keyAccess{userID: "u1", hasSession: true}, "u2", false)
	assert.Error(t, err)

	// An administrator may assign a member of the organization.
	admin := keyAccess{userID: "admin", hasSession: true, admin: true}
	owner, err = svc.resolveRequestedOwner(ctx, "org-a", admin, "u2", false)
	require.NoError(t, err)
	require.NotNil(t, owner)
	assert.Equal(t, "u2", *owner)

	// An administrator may create a shared key.
	owner, err = svc.resolveRequestedOwner(ctx, "org-a", admin, "", true)
	require.NoError(t, err)
	assert.Nil(t, owner)

	// Unknown organization users are rejected.
	_, err = svc.resolveRequestedOwner(ctx, "org-a", admin, "stranger", false)
	assert.Error(t, err)

	// Without a lookup the transitional path keeps the old behavior.
	bare := &Service{}
	owner, err = bare.resolveRequestedOwner(ctx, "org-a", keyAccess{}, "", false)
	require.NoError(t, err)
	assert.Nil(t, owner)
}

func TestResolveRequestedProject(t *testing.T) {
	lookup := fakeProjectLookup{
		projects: map[string]string{"proj-a": "org-a"},
		names:    map[string]string{"proj-a": "Project A"},
		members:  map[string]bool{"proj-a|u1": true},
	}
	svc := &Service{projectLookup: lookup}
	ctx := context.Background()

	project, err := svc.resolveRequestedProject(ctx, "org-a", keyAccess{userID: "u1", hasSession: true}, "proj-a")
	require.NoError(t, err)
	require.NotNil(t, project)
	assert.Equal(t, "proj-a", *project)

	// Empty clears the project.
	project, err = svc.resolveRequestedProject(ctx, "org-a", keyAccess{userID: "u1", hasSession: true}, "")
	require.NoError(t, err)
	assert.Nil(t, project)

	// Another organization's project is rejected.
	_, err = svc.resolveRequestedProject(ctx, "org-b", keyAccess{userID: "u1", hasSession: true}, "proj-a")
	assert.Error(t, err)

	// A member who does not belong to the project is rejected.
	_, err = svc.resolveRequestedProject(ctx, "org-a", keyAccess{userID: "u2", hasSession: true}, "proj-a")
	assert.Error(t, err)

	// An administrator may use any project of the organization.
	project, err = svc.resolveRequestedProject(ctx, "org-a", keyAccess{userID: "admin", hasSession: true, admin: true}, "proj-a")
	require.NoError(t, err)
	require.NotNil(t, project)
}

func TestVerifyAPIKeyUsesEffectiveQuota(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)

	resolver := &fakeQuotaResolver{quota: tenancy.EffectiveQuota{
		MaxConcurrentRequests:     7,
		MaxOutputTokensPerRequest: 2048,
	}}
	env.svc.SetQuotaResolver(resolver)

	owner := "u1"
	project := "p1"
	row, err := env.repo.FindByLookupHash(ctx, KeyDigest(created.GetApiKey()))
	require.NoError(t, err)
	row.OwnerUserID = &owner
	row.ProjectID = &project
	require.NoError(t, env.db.Save(row).Error)

	// Cache miss: the resolver runs once and the verdict carries its caps.
	resp, err := env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 7, resp.GetMaxConcurrentRequests())
	assert.EqualValues(t, 2048, resp.GetMaxOutputTokensPerRequest())
	assert.Equal(t, "u1", resp.GetOwnerUserId())
	assert.Equal(t, "p1", resp.GetProjectId())
	assert.Equal(t, 1, resolver.calls)

	// Cache hit: the caps deserialize from the cached verdict.
	resp, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 7, resp.GetMaxConcurrentRequests())
	assert.EqualValues(t, 1, resolver.calls)
}

func TestVerifyAPIKeyQuotaFailureDegrades(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")
	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)

	env.svc.SetQuotaResolver(&fakeQuotaResolver{err: assert.AnError})
	resp, err := env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: KeyDigest(created.GetApiKey()),
	})
	require.NoError(t, err, "a quota read failure must not block inference")
	assert.EqualValues(t, 0, resp.GetMaxConcurrentRequests(), "degrades to unlimited")
}

func TestUpdateAPIKeyOwnershipTransfersWithinOrganization(t *testing.T) {
	env := newTestService(t)
	svc := env.svc
	svc.SetProjectLookup(fakeProjectLookup{
		projects: map[string]string{"proj-a": "org-a"},
		names:    map[string]string{"proj-a": "Project A"},
		orgUsers: map[string]bool{"org-a|bob": true},
	})
	ctx := orgCtx("org-a")

	created, err := svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "k"})
	require.NoError(t, err)

	// The transitional caller is an administrator: the transfer succeeds.
	resp, err := svc.UpdateAPIKeyOwnership(ctx, &authv1.UpdateAPIKeyOwnershipRequest{
		KeyId:       created.GetKeyId(),
		OwnerUserId: "bob",
		ProjectId:   "proj-a",
	})
	require.NoError(t, err)
	assert.Equal(t, "bob", resp.GetKey().GetOwnerUserId())
	assert.Equal(t, "proj-a", resp.GetKey().GetProjectId())
	assert.Equal(t, "Project A", resp.GetKey().GetProjectDisplayName())

	// An unknown key answers not-found.
	_, err = svc.UpdateAPIKeyOwnership(ctx, &authv1.UpdateAPIKeyOwnershipRequest{KeyId: "missing"})
	assert.Error(t, err)

	// Clearing the owner turns the key into an organization-shared key.
	resp, err = svc.UpdateAPIKeyOwnership(ctx, &authv1.UpdateAPIKeyOwnershipRequest{KeyId: created.GetKeyId()})
	require.NoError(t, err)
	assert.True(t, resp.GetKey().GetShared())
	assert.Empty(t, resp.GetKey().GetOwnerUserId())
}

func TestListAPIKeysScopeFilters(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	// Two keys, one owned and one shared: both visible to the
	// transitional caller, distinguishable by scope.
	_, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "one", OwnerUserId: "alice"})
	require.NoError(t, err)
	_, err = env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "shared", Shared: true})
	require.NoError(t, err)

	shared, err := env.svc.ListAPIKeys(ctx, &authv1.ListAPIKeysRequest{Scope: "shared"})
	require.NoError(t, err)
	require.Len(t, shared.GetKeys(), 1)
	assert.True(t, shared.GetKeys()[0].GetShared())

	all, err := env.svc.ListAPIKeys(ctx, &authv1.ListAPIKeysRequest{Scope: "all"})
	require.NoError(t, err)
	assert.Len(t, all.GetKeys(), 2)

	_, err = env.svc.ListAPIKeys(ctx, &authv1.ListAPIKeysRequest{Scope: "bogus"})
	assert.Error(t, err)
}

// sessionCtx builds an incoming context carrying a bearer session.
func sessionCtx(token string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+token))
}

// newSessionEnv wires the ownership checks against real sessions and a
// real role guard over one database.
type sessionEnv struct {
	*testEnv
	sessions *SessionStore
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()
	base := newTestService(t)
	// The role guard reads org_members; the project lookup reads
	// projects/project_members.
	require.NoError(t, base.db.AutoMigrate(&tenancy.OrgMember{}, &tenancy.Project{}, &tenancy.ProjectMember{}))
	for _, m := range []struct {
		user string
		role string
	}{
		{"admin-fvt", "admin"},
		{"alice", "member"},
		{"bob", "member"},
	} {
		require.NoError(t, base.db.Create(&tenancy.OrgMember{
			OrganizationID: "org-a", UserID: m.user, Role: m.role, JoinedAt: time.Now().UTC(),
		}).Error)
	}
	require.NoError(t, base.db.Create(&tenancy.Project{
		ID: "proj-a", OrganizationID: "org-a", DisplayName: "Project A", State: "active",
	}).Error)
	require.NoError(t, base.db.Create(&tenancy.ProjectMember{
		ProjectID: "proj-a", UserID: "alice", Role: "member", JoinedAt: time.Now().UTC(),
	}).Error)

	srv := newFakeRedisServer(t)
	client := redis.NewClient(&redis.Options{Addr: srv.addr})
	t.Cleanup(func() { _ = client.Close() })
	store := NewSessionStore(client, time.Hour)
	base.svc.sessionStore = store
	base.svc.SetRoleGuard(tenancy.NewRoleGuard(base.db))

	for _, user := range []string{"admin-fvt", "alice", "bob"} {
		require.NoError(t, store.Create(context.Background(), &Session{
			SessionID: user, UserID: user, ExpiresAt: time.Now().Add(time.Hour).Unix(),
			ActiveOrg: "org-a",
		}, user))
	}
	return &sessionEnv{testEnv: base, sessions: store}
}

func TestResolveKeyAccessWithSessions(t *testing.T) {
	env := newSessionEnv(t)
	ctx := sessionCtx("alice")

	access, err := env.svc.resolveKeyAccess(ctx, "org-a")
	require.NoError(t, err)
	assert.True(t, access.hasSession)
	assert.Equal(t, "alice", access.userID)
	assert.False(t, access.admin)

	admin, err := env.svc.resolveKeyAccess(sessionCtx("admin-fvt"), "org-a")
	require.NoError(t, err)
	assert.True(t, admin.admin)

	// Without a session the transitional path stays permissive.
	bare, err := env.svc.resolveKeyAccess(orgCtx("org-a"), "org-a")
	require.NoError(t, err)
	assert.False(t, bare.hasSession)
	assert.True(t, bare.manages(&APIKey{}))
}

func TestRequireKeyManageEnforcesOwnership(t *testing.T) {
	env := newSessionEnv(t)

	// Alice creates her own key through the real session path.
	created, err := env.svc.CreateAPIKey(sessionCtx("alice"), &authv1.CreateAPIKeyRequest{Name: "alice"})
	require.NoError(t, err)
	row, err := env.repo.FindByIDAndOrganization(context.Background(), "org-a", created.GetKeyId())
	require.NoError(t, err)
	require.NotNil(t, row.OwnerUserID)
	assert.Equal(t, "alice", *row.OwnerUserID)

	// Alice manages it; Bob is denied without leaking existence.
	_, _, err = env.svc.requireKeyManage(sessionCtx("alice"), "org-a", created.GetKeyId())
	require.NoError(t, err)
	_, _, err = env.svc.requireKeyManage(sessionCtx("bob"), "org-a", created.GetKeyId())
	assert.Error(t, err)

	// An administrator manages any key.
	_, access, err := env.svc.requireKeyManage(sessionCtx("admin-fvt"), "org-a", created.GetKeyId())
	require.NoError(t, err)
	assert.True(t, access.admin)

	// An unknown key answers not-found.
	_, _, err = env.svc.requireKeyManage(sessionCtx("alice"), "org-a", "missing")
	assert.Error(t, err)
}

func TestOwnerScopedRepositoryOperations(t *testing.T) {
	env := newSessionEnv(t)
	ctx := orgCtx("org-a")
	owner := "alice"
	require.NoError(t, env.db.Create(&APIKey{
		ID: "key-1", Name: "k", Prefix: "sk-aaaaa", LookupHash: KeyDigest("sk-x"),
		Salt: "s", SaltedHash: "h", OrganizationID: "org-a", OwnerUserID: &owner,
		CreatedAt: time.Now().UTC(), Models: "[]",
	}).Error)

	// A non-owner identity sees not-found for every mutation.
	_, err := env.repo.RevokeByIDAndOrganizationOwned(ctx, "org-a", "key-1", "bob", time.Now().UTC())
	assert.Error(t, err)
	_, err = env.repo.DeleteByIDAndOrganizationOwned(ctx, "org-a", "key-1", "bob")
	assert.Error(t, err)
	_, err = env.repo.UpdateByIDAndOrganizationOwned(ctx, "org-a", "key-1", "bob", "n", nil, 0, 0)
	require.NoError(t, err)
	got, err := env.repo.FindByIDAndOrganization(ctx, "org-a", "key-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "k", got.Name, "a non-owner update changes nothing")

	// The owner may update and scope.
	updated, err := env.repo.UpdateByIDAndOrganizationOwned(ctx, "org-a", "key-1", "alice", "renamed", nil, 5, 6)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, "renamed", updated.Name)
	scoped, before, err := env.repo.UpdateScopeByIDAndOrganizationOwned(ctx, "org-a", "key-1", "alice", `["m1"]`)
	require.NoError(t, err)
	require.NotNil(t, scoped)
	assert.Empty(t, before)
	assert.Equal(t, []string{"m1"}, decodeModelIDs(scoped.Models))

	// The owner may revoke, then delete.
	_, err = env.repo.RevokeByIDAndOrganizationOwned(ctx, "org-a", "key-1", "alice", time.Now().UTC())
	require.NoError(t, err)
	_, err = env.repo.DeleteByIDAndOrganizationOwned(ctx, "org-a", "key-1", "alice")
	require.NoError(t, err)
	gone, err := env.repo.FindByIDAndOrganization(ctx, "org-a", "key-1")
	require.NoError(t, err)
	assert.Nil(t, gone)
}

// TestUpdateAPIKeyOwnershipClearsOwnerWithSession verifies that an
// administrator clears ownership through a real session: an empty
// owner means organization-shared, not "assign the caller".
func TestUpdateAPIKeyOwnershipClearsOwnerWithSession(t *testing.T) {
	env := newSessionEnv(t)
	ctx := sessionCtx("admin-fvt")
	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{
		Name: "admin-key", OwnerUserId: "alice",
	})
	require.NoError(t, err)
	row, err := env.repo.FindByIDAndOrganization(context.Background(), "org-a", created.GetKeyId())
	require.NoError(t, err)
	require.NotNil(t, row.OwnerUserID)
	assert.Equal(t, "alice", *row.OwnerUserID)

	resp, err := env.svc.UpdateAPIKeyOwnership(ctx, &authv1.UpdateAPIKeyOwnershipRequest{
		KeyId: created.GetKeyId(),
	})
	require.NoError(t, err)
	assert.True(t, resp.GetKey().GetShared())
	assert.Empty(t, resp.GetKey().GetOwnerUserId())
}

func TestDeleteAPIKeyByNonOwnerIsNotFound(t *testing.T) {
	env := newSessionEnv(t)
	created, err := env.svc.CreateAPIKey(sessionCtx("alice"), &authv1.CreateAPIKeyRequest{Name: "alice"})
	require.NoError(t, err)

	_, err = env.svc.DeleteAPIKey(sessionCtx("bob"), &authv1.DeleteAPIKeyRequest{KeyId: created.GetKeyId()})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeAPIKeyNotFound, apierrors.CodeOf(err))

	// Alice deletes her own key.
	_, err = env.svc.DeleteAPIKey(sessionCtx("alice"), &authv1.DeleteAPIKeyRequest{KeyId: created.GetKeyId()})
	require.NoError(t, err)
}

// TestRestoreAPIKeyLiftsRevocation covers the restore path: a revoked key
// verifies again after restore, and the secret is unchanged.
func TestRestoreAPIKeyLiftsRevocation(t *testing.T) {
	env := newSessionEnv(t)
	ctx := sessionCtx("alice")
	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "restorable"})
	require.NoError(t, err)

	digest := KeyDigest(created.GetApiKey())
	_, err = env.svc.RevokeAPIKey(ctx, &authv1.RevokeAPIKeyRequest{KeyId: created.GetKeyId()})
	require.NoError(t, err)
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	assert.Equal(t, apierrors.CodeAPIKeyRevoked, apierrors.CodeOf(err))

	resp, err := env.svc.RestoreAPIKey(ctx, &authv1.RestoreAPIKeyRequest{KeyId: created.GetKeyId()})
	require.NoError(t, err)
	assert.False(t, resp.GetKey().GetRevoked())
	assert.EqualValues(t, 0, resp.GetKey().GetRevokedAt())

	// The same secret verifies again.
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	require.NoError(t, err, "restored key must verify with the original secret")
}

// TestRestoreAPIKeyRejectsExpired covers the expiry rule: expiry is an
// independent check, so restoring an expired key would re-enable nothing.
func TestRestoreAPIKeyRejectsExpired(t *testing.T) {
	env := newSessionEnv(t)
	ctx := sessionCtx("alice")
	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{Name: "expiring"})
	require.NoError(t, err)
	require.NoError(t, env.db.Model(&APIKey{}).Where("id = ?", created.GetKeyId()).
		Updates(map[string]any{"revoked": true}).Error)
	past := time.Now().Add(-time.Hour)
	require.NoError(t, env.db.Model(&APIKey{}).Where("id = ?", created.GetKeyId()).
		Update("expires_at", past).Error)

	_, err = env.svc.RestoreAPIKey(ctx, &authv1.RestoreAPIKeyRequest{KeyId: created.GetKeyId()})
	assert.Equal(t, apierrors.CodeAPIKeyExpired, apierrors.CodeOf(err))
}

// TestRestoreAPIKeyOwnership covers the authorization rule: a member may
// not restore another member's key.
func TestRestoreAPIKeyOwnership(t *testing.T) {
	env := newSessionEnv(t)
	created, err := env.svc.CreateAPIKey(sessionCtx("alice"), &authv1.CreateAPIKeyRequest{Name: "mine"})
	require.NoError(t, err)
	require.NoError(t, env.db.Model(&APIKey{}).Where("id = ?", created.GetKeyId()).
		Update("revoked", true).Error)

	_, err = env.svc.RestoreAPIKey(sessionCtx("bob"), &authv1.RestoreAPIKeyRequest{KeyId: created.GetKeyId()})
	assert.Equal(t, apierrors.CodeAPIKeyNotFound, apierrors.CodeOf(err))

	_, err = env.svc.RestoreAPIKey(sessionCtx("admin-fvt"), &authv1.RestoreAPIKeyRequest{KeyId: created.GetKeyId()})
	require.NoError(t, err)
}

// scopeResolver maps a served name to its catalog id, the way the model
// repository does.
type scopeResolver struct {
	byName map[string]string
}

func (s scopeResolver) IsModelAuthorized(context.Context, string, string) (bool, error) {
	return true, nil
}

func (s scopeResolver) ResolveModelRef(_ context.Context, ref string) (string, error) {
	if id, ok := s.byName[ref]; ok {
		return id, nil
	}
	return ref, nil
}

// TestVerifyAPIKeyScopeMatchesServedName covers the name/id mismatch: a
// key scoped to a catalog id must accept the served model name, because
// that is all the data plane carries.
func TestVerifyAPIKeyScopeMatchesServedName(t *testing.T) {
	env := newTestService(t)
	ctx := orgCtx("org-a")

	created, err := env.svc.CreateAPIKey(ctx, &authv1.CreateAPIKeyRequest{
		Name:   "scoped",
		Models: []string{"model-uuid"},
	})
	require.NoError(t, err)
	env.svc.SetModelAuthorizer(scopeResolver{byName: map[string]string{"qwen": "model-uuid"}})

	digest := KeyDigest(created.GetApiKey())
	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: digest, Model: "qwen",
	})
	require.NoError(t, err, "the served name must match the stored catalog id")

	_, err = env.svc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: digest, Model: "other-model",
	})
	assert.Equal(t, apierrors.CodeAPIKeyModelNotAllowed, apierrors.CodeOf(err))
}
