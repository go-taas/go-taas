package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// enableCaptcha turns on the login CAPTCHA for a test.
func enableCaptcha(t *testing.T) {
	t.Helper()
	cfg := &config.Configuration{}
	cfg.Auth.Captcha.Enabled = true
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
}

// registerUser creates a local account and marks it a platform
// administrator when platformAdmin is true.
func registerUser(t *testing.T, svc *Service, db *gorm.DB, username string, platformAdmin bool) {
	t.Helper()
	resp, err := svc.CreateUser(context.Background(), &authv1.CreateUserRequest{
		Username: username, Password: "Password123", Email: username + "@x.com",
	})
	require.NoError(t, err)
	if platformAdmin {
		require.NoError(t, db.Model(&User{}).Where("id = ?", resp.GetUserId()).
			Update("platform_admin", true).Error)
	}
}

func TestGetCaptchaDisabled(t *testing.T) {
	svc, db := newSSOService(t)
	config.SetConfigForTest(&config.Configuration{})
	registerUser(t, svc, db, "admin", true)

	resp, err := svc.GetCaptcha(context.Background(), &authv1.GetCaptchaRequest{Username: "admin"})
	require.NoError(t, err)
	assert.Empty(t, resp.GetCaptchaId(), "a disabled CAPTCHA issues no id")
	assert.Empty(t, resp.GetImage(), "a disabled CAPTCHA issues no image")
}

// Ordinary accounts are not challenged: they prove themselves with the
// email verification code.
func TestGetCaptchaSkipsOrdinaryUser(t *testing.T) {
	svc, db := newSSOService(t)
	enableCaptcha(t)
	registerUser(t, svc, db, "alice", false)

	resp, err := svc.GetCaptcha(context.Background(), &authv1.GetCaptchaRequest{Username: "alice"})
	require.NoError(t, err)
	assert.Empty(t, resp.GetCaptchaId())
	assert.Empty(t, resp.GetImage())
}

func TestGetCaptchaRendersPNG(t *testing.T) {
	svc, db := newSSOService(t)
	enableCaptcha(t)
	registerUser(t, svc, db, "admin", true)
	ctx := context.Background()

	resp, err := svc.GetCaptcha(ctx, &authv1.GetCaptchaRequest{Username: "admin"})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetCaptchaId())
	assert.Greater(t, resp.GetExpiresAt(), int64(0))
	require.True(t, strings.HasPrefix(resp.GetImage(), "data:image/png;base64,"), "image must be a PNG data URL")

	// The stored answer matches the alphabet and length.
	client, err := svc.redisClientFor()
	require.NoError(t, err)
	stored, err := client.Get(ctx, captchaKeyPrefix+resp.GetCaptchaId()).Result()
	require.NoError(t, err)
	require.Len(t, stored, captchaLen)
	for _, c := range stored {
		assert.Contains(t, captchaAlphabet, string(c))
	}

	// The data URL decodes to a PNG of the expected size.
	raw := strings.TrimPrefix(resp.GetImage(), "data:image/png;base64,")
	decoded, err := base64.StdEncoding.DecodeString(raw)
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(decoded))
	require.NoError(t, err)
	assert.Equal(t, captchaWidth, img.Bounds().Dx())
	assert.Equal(t, captchaHeight, img.Bounds().Dy())
}

// The seeded admin username is challenged even before the account exists,
// so the first sign-in on a fresh install is protected.
func TestGetCaptchaChallengesSeedAdminUsername(t *testing.T) {
	svc, _ := newSSOService(t)
	enableCaptcha(t)

	resp, err := svc.GetCaptcha(context.Background(), &authv1.GetCaptchaRequest{Username: "ADMIN"})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetCaptchaId())
	assert.NotEmpty(t, resp.GetImage())
}

func TestLoginRequiresCaptchaForPlatformAdmin(t *testing.T) {
	svc, db := newSSOService(t)
	enableCaptcha(t)
	ctx := context.Background()
	registerUser(t, svc, db, "admin", true)

	// No CAPTCHA at all → 10043, checked before the password.
	_, err := svc.Login(ctx, &authv1.LoginRequest{Username: "admin", Password: "Password123"})
	assert.EqualValues(t, apierrors.CodeCaptchaInvalid, apierrors.CodeOf(err))

	// A wrong answer burns the puzzle.
	captcha, err := svc.GetCaptcha(ctx, &authv1.GetCaptchaRequest{Username: "admin"})
	require.NoError(t, err)
	_, err = svc.Login(ctx, &authv1.LoginRequest{
		Username: "admin", Password: "Password123",
		CaptchaId: captcha.GetCaptchaId(), CaptchaCode: "XXXX",
	})
	assert.EqualValues(t, apierrors.CodeCaptchaInvalid, apierrors.CodeOf(err))

	client, err := svc.redisClientFor()
	require.NoError(t, err)
	_, err = client.Get(ctx, captchaKeyPrefix+captcha.GetCaptchaId()).Result()
	assert.Error(t, err, "a failed attempt consumes the stored answer")

	// A fresh puzzle with the right answer signs in.
	captcha, err = svc.GetCaptcha(ctx, &authv1.GetCaptchaRequest{Username: "admin"})
	require.NoError(t, err)
	answer, err := client.Get(ctx, captchaKeyPrefix+captcha.GetCaptchaId()).Result()
	require.NoError(t, err)
	resp, err := svc.Login(ctx, &authv1.LoginRequest{
		Username: "admin", Password: "Password123",
		CaptchaId: captcha.GetCaptchaId(), CaptchaCode: strings.ToLower(answer),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.SessionToken)

	// The answer is single-use: replaying it fails.
	_, err = svc.Login(ctx, &authv1.LoginRequest{
		Username: "admin", Password: "Password123",
		CaptchaId: captcha.GetCaptchaId(), CaptchaCode: answer,
	})
	assert.EqualValues(t, apierrors.CodeCaptchaInvalid, apierrors.CodeOf(err))
}

// An ordinary account signs in with no CAPTCHA at all.
func TestLoginSkipsCaptchaForOrdinaryUser(t *testing.T) {
	svc, db := newSSOService(t)
	enableCaptcha(t)
	ctx := context.Background()
	registerUser(t, svc, db, "alice", false)

	resp, err := svc.Login(ctx, &authv1.LoginRequest{Username: "alice", Password: "Password123"})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.SessionToken)

	// A wrong password is still just invalid credentials, not a CAPTCHA error.
	_, err = svc.Login(ctx, &authv1.LoginRequest{Username: "alice", Password: "wrong"})
	assert.EqualValues(t, apierrors.CodeInvalidCredentials, apierrors.CodeOf(err))
}

func TestVerifyCaptchaUnknownID(t *testing.T) {
	svc, _ := newSSOService(t)
	enableCaptcha(t)

	err := svc.verifyCaptcha(context.Background(), "does-not-exist", "ABCD")
	assert.EqualValues(t, apierrors.CodeCaptchaInvalid, apierrors.CodeOf(err))
}

func TestRandomCaptchaCode(t *testing.T) {
	code, err := randomCaptchaCode()
	require.NoError(t, err)
	assert.Len(t, code, captchaLen)
	for _, c := range code {
		assert.Contains(t, captchaAlphabet, string(c))
	}
}
