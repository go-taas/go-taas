package fvt

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/services/auth"
)

// enableCaptchaFVT turns on the login CAPTCHA for the test.
func enableCaptchaFVT(t *testing.T) {
	t.Helper()
	cfg := &config.Configuration{}
	cfg.Auth.Captcha.Enabled = true
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
}

// The local login asks a platform administrator for a solved, single-use
// image CAPTCHA; an ordinary account signs in with no CAPTCHA at all.
func TestFVTCaptchaLogin(t *testing.T) {
	enableCaptchaFVT(t)
	env := newSSOEnv(t)

	// Register an administrator and an ordinary account.
	code, body := env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
		"username": "admin", "password": "Password123", "email": "admin@x.com",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/users", map[string]any{
		"username": "alice", "password": "Password123", "email": "alice@x.com",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.NoError(t, env.db.Exec(
		`UPDATE users SET platform_admin = true WHERE username = 'admin'`).Error)

	// The ordinary account is not challenged.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/captcha?username=alice", nil, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Empty(t, body["captchaId"])
	assert.Empty(t, body["image"])
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "alice", "password": "Password123",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.NotEmpty(t, body["sessionToken"])

	// The administrator is: no CAPTCHA → 10043, before the password.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "Password123",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10043), body["code"])

	// The CAPTCHA endpoint returns an id and a PNG data URL for it.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/captcha?username=admin", nil, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	captchaID, _ := body["captchaId"].(string)
	image, _ := body["image"].(string)
	require.NotEmpty(t, captchaID)
	require.True(t, strings.HasPrefix(image, "data:image/png;base64,"), "image: %q", image)

	// Read the stored answer, then sign in with it.
	client := env.svc.SessionStoreClient()
	require.NotNil(t, client)
	answer, err := client.Get(t.Context(), "auth:captcha:"+captchaID).Result()
	require.NoError(t, err)
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "Password123",
		"captchaId": captchaID, "captchaCode": answer,
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.NotEmpty(t, body["sessionToken"])

	// The answer is single-use: replaying it fails.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "Password123",
		"captchaId": captchaID, "captchaCode": answer,
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10043), body["code"])
}

var _ = auth.UserStateActive
