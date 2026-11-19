// Platform administrator accounts are system artifacts: they cannot be
// deleted from the directory, while ordinary accounts still can.

package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/services/auth"
)

func TestFVTAdminUserCannotBeDeleted(t *testing.T) {
	env := newSSOEnv(t)

	require.NoError(t, env.db.Create(&auth.User{
		ID: "user-admin", Username: "admin", Email: "admin@example.com",
		State: auth.UserStateActive, PlatformAdmin: true,
	}).Error)
	require.NoError(t, env.db.Create(&auth.User{
		ID: "user-root", Username: "root", Email: "root@example.com",
		State: auth.UserStateActive, PlatformAdmin: true,
	}).Error)
	require.NoError(t, env.db.Create(&auth.User{
		ID: "user-alice", Username: "alice", Email: "alice@example.com",
		State: auth.UserStateActive,
	}).Error)

	for _, id := range []string{"user-admin", "user-root"} {
		code, body := env.call(t, http.MethodDelete, "/api/v1/admin/auth/users/"+id, nil,
			map[string]string{"X-Organization-Id": "org-fvt"})
		assert.NotEqual(t, http.StatusOK, code)
		assert.EqualValues(t, float64(10046), body["code"], "protect %s: %v", id, body)

		var count int64
		require.NoError(t, env.db.Model(&auth.User{}).Where("id = ?", id).Count(&count).Error)
		assert.EqualValues(t, 1, count, "%s must still exist", id)
	}

	// The guard is narrow: an ordinary account is still deletable.
	code, body := env.call(t, http.MethodDelete, "/api/v1/admin/auth/users/user-alice", nil,
		map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "ordinary delete: %v", body)
}

// A platform administrator cannot be disabled either: the bootstrap account
// is the installation's recovery path, and a disabled one locks the platform
// out just like a deleted one.
func TestFVTAdminUserCannotBeDisabled(t *testing.T) {
	env := newSSOEnv(t)

	require.NoError(t, env.db.Create(&auth.User{
		ID: "user-admin", Username: "admin", Email: "admin@example.com",
		State: auth.UserStateActive, PlatformAdmin: true,
	}).Error)
	require.NoError(t, env.db.Create(&auth.User{
		ID: "user-root", Username: "root", Email: "root@example.com",
		State: auth.UserStateActive, PlatformAdmin: true,
	}).Error)
	require.NoError(t, env.db.Create(&auth.User{
		ID: "user-alice", Username: "alice", Email: "alice@example.com",
		State: auth.UserStateActive,
	}).Error)

	for _, id := range []string{"user-admin", "user-root"} {
		code, body := env.call(t, http.MethodPost, "/api/v1/admin/auth/users/"+id+":disable", nil,
			map[string]string{"X-Organization-Id": "org-fvt"})
		assert.NotEqual(t, http.StatusOK, code)
		assert.EqualValues(t, float64(10047), body["code"], "protect %s: %v", id, body)

		var row auth.User
		require.NoError(t, env.db.First(&row, "id = ?", id).Error)
		assert.Equal(t, auth.UserStateActive, row.State, "%s must stay active", id)
	}

	// An ordinary account is still disableable and re-enableable.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/auth/users/user-alice:disable", nil,
		map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "ordinary disable: %v", body)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/users/user-alice:enable", nil,
		map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "ordinary enable: %v", body)
}
