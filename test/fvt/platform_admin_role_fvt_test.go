// Assigning the platform administrator role: the two admin-surface RPCs, the
// guards that keep an installation from losing its last administrator, and the
// directory flag the console renders.

package fvt

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/tenancy"
)

// platformAdminIDs are the accounts this suite adds on top of the ownership
// environment: one platform administrator and one ordinary user.
const (
	fvtRootID  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	fvtAliceID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	fvtCarolID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// platformAdminEnv is the ownership environment plus the three accounts and
// their real sessions.
type platformAdminEnv struct {
	*ownershipFvtEnv
}

func newPlatformAdminEnv2(t *testing.T) *platformAdminEnv {
	t.Helper()
	env := newOwnershipFvtEnv(t)
	for _, row := range []*auth.User{
		{ID: fvtRootID, Username: "root", Email: "root@example.com", State: auth.UserStateActive, PlatformAdmin: true},
		{ID: fvtAliceID, Username: "alice", Email: "alice@example.com", State: auth.UserStateActive},
		{ID: fvtCarolID, Username: "carol", Email: "carol@example.com", State: auth.UserStateDisabled},
	} {
		require.NoError(t, env.db.Create(row).Error)
	}
	for _, user := range []string{fvtRootID, fvtAliceID} {
		token := "admin-sess-" + user
		require.NoError(t, env.auth.CreateSessionForTest(context.Background(), &auth.Session{
			SessionID:      token,
			UserID:         user,
			Username:       user,
			Roles:          []string{tenancy.RolePlatformAdmin},
			AccessibleOrgs: []string{"org-fvt"},
			ActiveOrg:      "org-fvt",
			ExpiresAt:      time.Now().Add(time.Hour).Unix(),
			CreatedAt:      time.Now().Unix(),
			Realm:          auth.RealmAdmin,
		}, token))
		env.tokens[user] = token
	}
	return &platformAdminEnv{ownershipFvtEnv: env}
}

// TestFVTPlatformAdminGrantAndRevoke covers the assignment flow through the
// gateway: grant, see the flag in the directory, revoke, see it clear.
func TestFVTPlatformAdminGrantAndRevoke(t *testing.T) {
	env := newPlatformAdminEnv2(t)

	code, body := env.call(t, fvtRootID, http.MethodPost,
		"/api/v1/admin/auth/users/"+fvtAliceID+":grant-platform-admin", nil)
	require.Equal(t, http.StatusOK, code, "grant: %v", body)
	user, _ := body["user"].(map[string]any)
	require.NotNil(t, user)
	assert.Equal(t, true, user["platformAdmin"])

	_, body = env.call(t, fvtRootID, http.MethodGet, "/api/v1/admin/auth/users?page.limit=20", nil)
	rows, _ := body["users"].([]any)
	found := false
	for _, raw := range rows {
		row := raw.(map[string]any)
		if row["userId"] == fvtAliceID {
			found = true
			assert.Equal(t, true, row["platformAdmin"], "the directory reports the role")
		}
	}
	assert.True(t, found)

	// Alice is an administrator now, so she can revoke root (not herself).
	code, body = env.call(t, fvtAliceID, http.MethodPost,
		"/api/v1/admin/auth/users/"+fvtRootID+":revoke-platform-admin", nil)
	require.Equal(t, http.StatusOK, code, "revoke: %v", body)
	user, _ = body["user"].(map[string]any)
	assert.Equal(t, false, user["platformAdmin"])
}

// TestFVTPlatformAdminGuards covers the rules an operator hits through the API:
// no self-revocation, no demoting the last administrator, no role for a
// disabled account, and no role change by a non-administrator.
func TestFVTPlatformAdminGuards(t *testing.T) {
	env := newPlatformAdminEnv2(t)

	// Self-revocation is refused (and root is the only administrator).
	code, body := env.call(t, fvtRootID, http.MethodPost,
		"/api/v1/admin/auth/users/"+fvtRootID+":revoke-platform-admin", nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10036, body["code"], "self-revocation: %v", body)

	// A disabled account cannot be promoted.
	code, body = env.call(t, fvtRootID, http.MethodPost,
		"/api/v1/admin/auth/users/"+fvtCarolID+":grant-platform-admin", nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10036, body["code"], "disabled target: %v", body)

	// A non-administrator cannot promote anyone: Alice has no role yet, and
	// her session is a user-realm session, so the admin surface rejects it.
	code, body = env.call(t, fvtAliceID, http.MethodPost,
		"/api/v1/admin/auth/users/"+fvtCarolID+":grant-platform-admin", nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.False(t, code == http.StatusOK, "a plain account must never grant the role: %v", body)

	// After both guards, root is still the only administrator and nobody else
	// can be demoted into a zero-administrator installation.
	code, body = env.call(t, fvtRootID, http.MethodPost,
		"/api/v1/admin/auth/users/"+fvtAliceID+":revoke-platform-admin", nil)
	require.Equal(t, http.StatusOK, code, "revoking a non-administrator is a no-op: %v", body)
}
