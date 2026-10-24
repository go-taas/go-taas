package fvt

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/services/auth"
)

// enableEmailVerificationFVT turns on email verification for the test.
func enableEmailVerificationFVT(t *testing.T) {
	t.Helper()
	cfg := &config.Configuration{}
	cfg.Auth.EmailVerification = true
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
}

// Email verification code send + register + login round-trip.
func TestFVTEmailCodeRegisterAndLogin(t *testing.T) {
	env := newSSOEnv(t)
	enableEmailVerificationFVT(t)

	// Send a registration code.
	code, body := env.call(t, http.MethodPost, "/api/v1/auth/email-code", map[string]any{
		"email": "alice@x.com", "purpose": "register",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "60", body["resendAfterSeconds"])

	// Read the stored code from Redis.
	client := env.svc.SessionStoreClient()
	require.NotNil(t, client)
	stored, err := client.Get(t.Context(), "auth:email-code:alice@x.com:register").Result()
	require.NoError(t, err)

	// Register with the code.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
		"username": "alice", "password": "password123", "email": "alice@x.com", "emailCode": stored,
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, float64(0), body["response"].(map[string]any)["code"])

	// Register without a code → 10002.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
		"username": "bob", "password": "password123", "email": "bob@x.com",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10002), body["code"])

	// Send a login code and login with it.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/email-code", map[string]any{
		"email": "alice@x.com", "purpose": "login",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	stored, err = client.Get(t.Context(), "auth:email-code:alice@x.com:login").Result()
	require.NoError(t, err)
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "alice", "password": "password123", "emailCode": stored,
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.NotEmpty(t, body["sessionToken"])
}

var _ = auth.UserStateActive