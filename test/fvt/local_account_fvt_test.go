package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Local account registration and login round-trip through the gateway.
func TestFVTLocalAccountRegisterAndLogin(t *testing.T) {
	env := newSSOEnv(t)

	// Register a local account.
	code, body := env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
		"username": "alice", "password": "Password123", "email": "alice@x.com",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, float64(0), body["response"].(map[string]any)["code"])
	userID, _ := body["userId"].(string)
	assert.NotEmpty(t, userID)

	// Duplicate username → 10004.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
		"username": "alice", "password": "Password123",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10004), body["code"])

	// Login with correct credentials → session token.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "alice", "password": "Password123",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.NotEmpty(t, body["sessionToken"])
	assert.NotEmpty(t, body["expiresAt"])

	// Login with wrong password → 10002.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "alice", "password": "wrong",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10002), body["code"])

	// Login with unknown user → 10002.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "nobody", "password": "Password123",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10002), body["code"])
}

// A short password is rejected at registration.
func TestFVTLocalAccountPasswordPolicy(t *testing.T) {
	env := newSSOEnv(t)

	code, body := env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
		"username": "bob", "password": "short",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10014), body["code"])
}