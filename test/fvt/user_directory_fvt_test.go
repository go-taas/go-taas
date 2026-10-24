package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/tenancy"
)

// TestFVTUserDirectory walks the user directory lifecycle: list, get,
// disable, enable, and the unknown-user error path.
func TestFVTUserDirectory(t *testing.T) {
	env := newSSOEnv(t)

	// Seed users directly (they are normally created by SSO JIT or
	// invitations).
	require.NoError(t, env.db.Create(&auth.User{ID: "user-1", Username: "alice", Email: "alice@x.com"}).Error)
	require.NoError(t, env.db.Create(&auth.User{ID: "user-2", Username: "bob", Email: "bob@x.com"}).Error)
	require.NoError(t, env.db.Create(&auth.User{ID: "user-3", Username: "carol", Email: "carol@x.com", State: auth.UserStateDisabled}).Error)
	// A membership for the org-count column (org-fvt is seeded by
	// newSSOEnv).
	require.NoError(t, env.db.Create(&tenancy.OrgMember{OrganizationID: "org-fvt", UserID: "user-1", Role: "member"}).Error)

	// List all users.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/auth/users?page.limit=10", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	users, _ := body["users"].([]any)
	assert.Len(t, users, 3)
	first := users[0].(map[string]any)
	assert.Equal(t, "user-3", first["userId"])
	assert.Equal(t, "disabled", first["state"])

	// Filter by active state.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/users?state=active&page.limit=10", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	users, _ = body["users"].([]any)
	assert.Len(t, users, 2)

	// Get one user with its org count.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/users/user-1", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	u := body["user"].(map[string]any)
	assert.Equal(t, "alice", u["username"])
	assert.Equal(t, "active", u["state"])

	// Disable a user.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/users/user-2:disable", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "disabled", body["user"].(map[string]any)["state"])

	// Enable it back.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/users/user-2:enable", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "active", body["user"].(map[string]any)["state"])

	// Unknown user → 10003.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/users/user-missing", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10003), body["code"])

	// Invalid state filter → 10019.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/users?state=bogus", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10019), body["code"])
}