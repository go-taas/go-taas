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
		"username": "alice", "password": "Password123", "email": "alice@x.com", "emailCode": stored,
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, float64(0), body["response"].(map[string]any)["code"])

	// Register without a code → 10002.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
		"username": "bob", "password": "Password123", "email": "bob@x.com",
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
		"username": "alice", "password": "Password123", "emailCode": stored,
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.NotEmpty(t, body["sessionToken"])
}

var _ = auth.UserStateActive

// TestFVTEmailCodeChangeEmailOnAdminSurface verifies the admin console can
// request a change_email code (the profile page is shared by both
// surfaces, so the endpoint must answer on the admin prefix too).
func TestFVTEmailCodeChangeEmailOnAdminSurface(t *testing.T) {
	env := newSSOEnv(t)
	enableEmailVerificationFVT(t)

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/auth/email-code", map[string]any{
		"email": "new@x.com", "purpose": "change_email",
	}, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	client := env.svc.SessionStoreClient()
	require.NotNil(t, client)
	stored, err := client.Get(t.Context(), "auth:email-code:new@x.com:change_email").Result()
	require.NoError(t, err)
	assert.Len(t, stored, 6)
}

// A malformed address is rejected before any code is generated or mail is
// attempted, so the console can point at the field instead of claiming a
// send failure.
func TestFVTEmailCodeRejectsInvalidAddress(t *testing.T) {
	env := newSSOEnv(t)
	enableEmailVerificationFVT(t)

	for _, email := range []string{"not-an-email", "alice", "alice@", "@example.com", "alice@example", "a lice@example.com"} {
		code, body := env.call(t, http.MethodPost, "/api/v1/auth/email-code", map[string]any{
			"email": email, "purpose": "register",
		}, nil)
		assert.NotEqual(t, http.StatusOK, code, "email %q: %v", email, body)
		assert.EqualValues(t, float64(10044), body["code"], "email %q: %v", email, body)
	}

	// Registration refuses the same malformed address even when a code is
	// supplied: the address is validated independently of the code.
	code, body := env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
		"username": "badmail", "password": "Password123", "email": "not-an-email", "emailCode": "123456",
	}, nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10044), body["code"], "%v", body)
}

// A transport failure is surfaced to the caller instead of being logged and
// answered with success: the browser must tell the user the mail did not go
// out. The stored code and cooldown are released so a retry can work.
func TestFVTEmailCodeSendFailureIsReported(t *testing.T) {
	env := newSSOEnv(t)
	cfg := &config.Configuration{}
	cfg.Auth.EmailVerification = true
	cfg.Auth.SMTP = config.SMTPConfig{Host: "127.0.0.1", Port: 1, From: "no-reply@example.com"}
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	code, body := env.call(t, http.MethodPost, "/api/v1/auth/email-code", map[string]any{
		"email": "alice@example.com", "purpose": "register",
	}, nil)
	assert.NotEqual(t, http.StatusOK, code, "%v", body)
	assert.EqualValues(t, float64(10045), body["code"], "%v", body)

	client := env.svc.SessionStoreClient()
	require.NotNil(t, client)
	assert.EqualValues(t, 0, client.Exists(t.Context(), "auth:email-code:alice@example.com:register").Val(),
		"a code that could not be sent must not stay valid")
	assert.EqualValues(t, 0, client.Exists(t.Context(), "auth:email-code-cooldown:alice@example.com:register").Val(),
		"a failed send must not block the retry")
}
