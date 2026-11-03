package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"time"

	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// platformAdminEnv is one auth service with three accounts and a live session
// per account, so the RPCs resolve the caller the way production does.
type platformAdminEnv struct {
	svc    *Service
	db     *gorm.DB
	ids    map[string]string
	tokens map[string]string
	reaper *fakeSessionReaper
}

// fakeSessionReaper records the accounts whose sessions were revoked.
type fakeSessionReaper struct {
	reaped []string
}

func (f *fakeSessionReaper) RevokeUserSessions(_ context.Context, userID string) (int, error) {
	f.reaped = append(f.reaped, userID)
	return 1, nil
}

// newPlatformAdminEnv builds the service over a fresh database with one
// platform administrator, one ordinary active user and one disabled user.
func newPlatformAdminEnv(t *testing.T) *platformAdminEnv {
	t.Helper()
	svc, db := newSSOService(t)
	ctx := context.Background()
	repo, err := svc.ssoRepository()
	require.NoError(t, err)

	env := &platformAdminEnv{svc: svc, db: db, ids: map[string]string{}, tokens: map[string]string{},
		reaper: &fakeSessionReaper{}}
	for _, row := range []*User{
		{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Username: "root", Email: "root@example.com", State: UserStateActive, PlatformAdmin: true},
		{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Username: "alice", Email: "alice@example.com", State: UserStateActive},
		{ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Username: "carol", Email: "carol@example.com", State: UserStateDisabled},
	} {
		require.NoError(t, repo.CreateUser(ctx, row))
		env.ids[row.Username] = row.ID
	}
	svc.SetSessionReaper(env.reaper)
	return env
}

// ctx returns a context authenticated as the given account, by minting a real
// session in the store the service reads.
func (e *platformAdminEnv) ctx(t *testing.T, username string) context.Context {
	t.Helper()
	token, ok := e.tokens[username]
	if !ok {
		token = "session-" + username
		require.NoError(t, e.svc.sessionStore.Create(context.Background(), &Session{
			SessionID: token,
			UserID:    e.ids[username],
			Username:  username,
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
			CreatedAt: time.Now().Unix(),
			Realm:     "admin",
		}, "access-"+token))
		e.tokens[username] = token
	}
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer "+token))
}

// TestGrantPlatformAdminCoversTheGrantRules covers the assignment contract:
// the role is granted to an active user, is idempotent, is refused for a
// disabled account, and is visible on the directory projection.
func TestGrantPlatformAdminCoversTheGrantRules(t *testing.T) {
	env := newPlatformAdminEnv(t)
	ctx := env.ctx(t, "root")

	resp, err := env.svc.GrantPlatformAdmin(ctx, &authv1.GrantPlatformAdminRequest{UserId: env.ids["alice"]})
	require.NoError(t, err)
	assert.True(t, resp.GetUser().GetPlatformAdmin())

	// Idempotent: granting twice succeeds and stays granted.
	_, err = env.svc.GrantPlatformAdmin(ctx, &authv1.GrantPlatformAdminRequest{UserId: env.ids["alice"]})
	require.NoError(t, err)

	// A disabled account cannot become an administrator: the role would be
	// dormant but would come back the moment the account is re-enabled.
	_, err = env.svc.GrantPlatformAdmin(ctx, &authv1.GrantPlatformAdminRequest{UserId: env.ids["carol"]})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// An unknown user is a not-found, not a forbidden.
	_, err = env.svc.GrantPlatformAdmin(ctx, &authv1.GrantPlatformAdminRequest{
		UserId: "99999999-9999-4999-8999-999999999999",
	})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}

// TestRevokePlatformAdminRefusesSelfAndLastAdmin covers the two guards that
// keep an installation from losing its administrators: nobody demotes
// themselves, and the last administrator cannot be demoted by anyone.
func TestRevokePlatformAdminRefusesSelfAndLastAdmin(t *testing.T) {
	env := newPlatformAdminEnv(t)
	ctx := env.ctx(t, "root")

	// Only one administrator exists: demoting it is refused even though the
	// caller is that administrator (self-revocation is checked first).
	_, err := env.svc.RevokePlatformAdmin(ctx, &authv1.RevokePlatformAdminRequest{UserId: env.ids["root"]})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// With a second administrator, the caller still cannot demote itself.
	_, err = env.svc.GrantPlatformAdmin(ctx, &authv1.GrantPlatformAdminRequest{UserId: env.ids["alice"]})
	require.NoError(t, err)
	_, err = env.svc.RevokePlatformAdmin(ctx, &authv1.RevokePlatformAdminRequest{UserId: env.ids["root"]})
	require.Error(t, err, "self-revocation is always refused")
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))

	// The other administrator can demote the first one.
	aliceCtx := env.ctx(t, "alice")
	resp, err := env.svc.RevokePlatformAdmin(aliceCtx, &authv1.RevokePlatformAdminRequest{UserId: env.ids["root"]})
	require.NoError(t, err)
	assert.False(t, resp.GetUser().GetPlatformAdmin())
}

// TestRevokePlatformAdminKeepsTheLastAdministrator covers the last-admin guard
// with two administrators: after one is demoted, the remaining one cannot be.
func TestRevokePlatformAdminKeepsTheLastAdministrator(t *testing.T) {
	env := newPlatformAdminEnv(t)
	ctx := env.ctx(t, "root")
	_, err := env.svc.GrantPlatformAdmin(ctx, &authv1.GrantPlatformAdminRequest{UserId: env.ids["alice"]})
	require.NoError(t, err)

	aliceCtx := env.ctx(t, "alice")
	_, err = env.svc.RevokePlatformAdmin(aliceCtx, &authv1.RevokePlatformAdminRequest{UserId: env.ids["root"]})
	require.NoError(t, err)

	// Alice is now the only administrator: neither she nor anyone else can
	// remove the role.
	_, err = env.svc.RevokePlatformAdmin(aliceCtx, &authv1.RevokePlatformAdminRequest{UserId: env.ids["alice"]})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestGrantPlatformAdminRequiresAPlatformAdministrator covers authorization:
// an ordinary account cannot hand out the role.
func TestGrantPlatformAdminRequiresAPlatformAdministrator(t *testing.T) {
	env := newPlatformAdminEnv(t)
	ctx := env.ctx(t, "alice")

	_, err := env.svc.GrantPlatformAdmin(ctx, &authv1.GrantPlatformAdminRequest{UserId: env.ids["carol"]})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestGrantPlatformAdminRevokesTheTargetsSessions covers the privilege-change
// side effect: a session caches the roles it was minted with, so the target's
// sessions are revoked — a demotion must not keep working until the token
// expires, and a promotion should start a fresh admin realm session.
func TestGrantPlatformAdminRevokesTheTargetsSessions(t *testing.T) {
	env := newPlatformAdminEnv(t)
	ctx := env.ctx(t, "root")

	_, err := env.svc.GrantPlatformAdmin(ctx, &authv1.GrantPlatformAdminRequest{UserId: env.ids["alice"]})
	require.NoError(t, err)
	assert.Contains(t, env.reaper.reaped, env.ids["alice"])

	_, err = env.svc.RevokePlatformAdmin(ctx, &authv1.RevokePlatformAdminRequest{UserId: env.ids["alice"]})
	require.NoError(t, err)
	assert.Equal(t, []string{env.ids["alice"], env.ids["alice"]}, env.reaper.reaped)
}

// TestDisableUserRevokesSessions covers the lifecycle side of the same rule: a
// disabled account must not keep working until its token expires.
func TestDisableUserRevokesSessions(t *testing.T) {
	env := newPlatformAdminEnv(t)
	ctx := env.ctx(t, "root")

	_, err := env.svc.DisableUser(ctx, &authv1.DisableUserRequest{UserId: env.ids["alice"]})
	require.NoError(t, err)
	assert.Contains(t, env.reaper.reaped, env.ids["alice"])

	// Enabling is not a privilege event: nothing to revoke.
	env.reaper.reaped = nil
	_, err = env.svc.EnableUser(ctx, &authv1.EnableUserRequest{UserId: env.ids["alice"]})
	require.NoError(t, err)
	assert.Empty(t, env.reaper.reaped)
}
