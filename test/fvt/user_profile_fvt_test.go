package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// User profile get/update round-trips through the gateway.
func TestFVTUserProfile(t *testing.T) {
	env := newSSOEnv(t)

	// Register a local account.
	code, body := env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
		"username": "alice", "password": "password123", "email": "alice@x.com",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// Login to get a session token.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "alice", "password": "password123",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	token, _ := body["sessionToken"].(string)
	require.NotEmpty(t, token)

	// Get the profile.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/profile", nil, map[string]string{"Authorization": "Bearer " + token})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	profile := body["profile"].(map[string]any)
	assert.Equal(t, "alice", profile["username"])
	assert.Equal(t, "alice@x.com", profile["email"])

	// Update the profile.
	code, body = env.call(t, http.MethodPatch, "/api/v1/auth/profile", map[string]any{
		"displayName": "Alice Updated", "avatar": "data:image/png;base64,iVBORw0KGgo=",
	}, map[string]string{"Authorization": "Bearer " + token})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	profile = body["profile"].(map[string]any)
	assert.Equal(t, "Alice Updated", profile["displayName"])
	assert.Equal(t, "data:image/png;base64,iVBORw0KGgo=", profile["avatar"])
}