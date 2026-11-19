package fvt

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/services/auth"
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
	assert.Equal(t, "user", body["realm"], "a local account with no org role is a user-realm session")
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

// The full policy is a length floor plus three of four character classes.
// Every combination is checked here, so a future relaxation of one class is
// a visible test change.
func TestFVTLocalAccountPasswordPolicyClasses(t *testing.T) {
	env := newSSOEnv(t)

	cases := []struct {
		name     string
		password string
		ok       bool
	}{
		{"too short", "Ab1!", false},
		{"two classes", "abcdefgh", false},
		{"lower digit only", "abcdefg1", false},
		{"lower upper digit", "Abcdefg1", true},
		{"lower upper special", "Abcdefg!", true},
		{"upper digit special", "ABCDEFG1!", true},
		{"all four", "Abcdef1!", true},
		{"too long", "Aa1!" + strings.Repeat("a", 300), false},
	}
	for i, tc := range cases {
		code, body := env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
			"username": fmt.Sprintf("policy%d", i), "password": tc.password, "email": fmt.Sprintf("policy%d@example.com", i),
		}, nil)
		if tc.ok {
			require.Equal(t, http.StatusOK, code, "%s: %v", tc.name, body)
			continue
		}
		assert.NotEqual(t, http.StatusOK, code, "%s", tc.name)
		assert.EqualValues(t, float64(10014), body["code"], "%s: %v", tc.name, body)
	}
}

// A disabled account is refused with USER_DISABLED (10040), not invalid
// credentials: the console must be able to say why the sign-in failed.
func TestFVTLocalAccountDisabledLoginReason(t *testing.T) {
	env := newSSOEnv(t)
	hash, err := auth.HashPassword("Password123", config.Argon2Params{Algorithm: "argon2id", Time: 1, MemoryMiB: 16, Parallelism: 1})
	require.NoError(t, err)
	require.NoError(t, env.db.Create(&auth.User{
		ID: "user-disabled", Username: "disabled", Email: "disabled@example.com",
		PasswordHash: hash, State: auth.UserStateDisabled,
	}).Error)

	code, body := env.call(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "disabled", "password": "Password123",
	}, nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10040), body["code"], "%v", body)
}
