package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// enableEmailVerification turns on email verification for a test.
func enableEmailVerification(t *testing.T) {
	t.Helper()
	cfg := &config.Configuration{}
	cfg.Auth.EmailVerification = true
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
}

func TestGenerateEmailCode(t *testing.T) {
	code, err := generateEmailCode()
	require.NoError(t, err)
	assert.Len(t, code, emailCodeLen)
	for _, c := range code {
		assert.True(t, c >= '0' && c <= '9', "code must be digits")
	}
}

func TestSendAndVerifyEmailCode(t *testing.T) {
	svc, _ := newSSOService(t)
	enableEmailVerification(t)
	ctx := context.Background()

	// Send a code.
	resp, err := svc.SendEmailCode(ctx, &authv1.SendEmailCodeRequest{
		Email: "alice@x.com", Purpose: emailCodePurposeRegister,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 60, resp.ResendAfterSeconds)

	// Read the stored code from Redis.
	client, err := svc.redisClientFor()
	require.NoError(t, err)
	key := emailCodeKeyPrefix + "alice@x.com:" + emailCodePurposeRegister
	stored, err := client.Get(ctx, key).Result()
	require.NoError(t, err)
	assert.Len(t, stored, emailCodeLen)

	// Verify with the correct code.
	require.NoError(t, svc.verifyEmailCode(ctx, "alice@x.com", emailCodePurposeRegister, stored))

	// The code is consumed (single-use).
	err = svc.verifyEmailCode(ctx, "alice@x.com", emailCodePurposeRegister, stored)
	assert.EqualValues(t, apierrors.CodeInvalidCredentials, apierrors.CodeOf(err))
}

func TestSendEmailCodeCooldown(t *testing.T) {
	svc, _ := newSSOService(t)
	enableEmailVerification(t)
	ctx := context.Background()

	_, err := svc.SendEmailCode(ctx, &authv1.SendEmailCodeRequest{
		Email: "alice@x.com", Purpose: emailCodePurposeRegister,
	})
	require.NoError(t, err)

	// A second send within the cooldown is rate-limited.
	_, err = svc.SendEmailCode(ctx, &authv1.SendEmailCodeRequest{
		Email: "alice@x.com", Purpose: emailCodePurposeRegister,
	})
	assert.EqualValues(t, apierrors.CodeRateLimitExceeded, apierrors.CodeOf(err))
}

func TestSendEmailCodeValidation(t *testing.T) {
	svc, _ := newSSOService(t)
	enableEmailVerification(t)
	ctx := context.Background()

	// Empty email → 10003.
	_, err := svc.SendEmailCode(ctx, &authv1.SendEmailCodeRequest{Email: "", Purpose: emailCodePurposeRegister})
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))

	// Invalid purpose → 10003.
	_, err = svc.SendEmailCode(ctx, &authv1.SendEmailCodeRequest{Email: "a@x.com", Purpose: "bogus"})
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))
}

func TestVerifyEmailCodeDisabled(t *testing.T) {
	svc, _ := newSSOService(t)
	// Email verification is disabled by default.
	ctx := context.Background()

	// Verification is a no-op when disabled.
	require.NoError(t, svc.verifyEmailCode(ctx, "a@x.com", emailCodePurposeRegister, ""))
}

func TestCreateUserRequiresEmailCode(t *testing.T) {
	svc, _ := newSSOService(t)
	enableEmailVerification(t)
	ctx := context.Background()

	// Without an email → 10003.
	_, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "alice", Password: "password123",
	})
	assert.EqualValues(t, apierrors.CodeUserNotFound, apierrors.CodeOf(err))

	// With an email but no code → 10002.
	_, err = svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "alice", Password: "password123", Email: "alice@x.com",
	})
	assert.EqualValues(t, apierrors.CodeInvalidCredentials, apierrors.CodeOf(err))

	// Send a code and register with it.
	_, err = svc.SendEmailCode(ctx, &authv1.SendEmailCodeRequest{
		Email: "alice@x.com", Purpose: emailCodePurposeRegister,
	})
	require.NoError(t, err)
	client, _ := svc.redisClientFor()
	stored, _ := client.Get(ctx, emailCodeKeyPrefix+"alice@x.com:"+emailCodePurposeRegister).Result()
	resp, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "alice", Password: "password123", Email: "alice@x.com", EmailCode: stored,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.UserId)
}

func TestLoginRequiresEmailCode(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	// Register a user with email verification disabled.
	_, err := svc.CreateUser(ctx, &authv1.CreateUserRequest{
		Username: "alice", Password: "password123", Email: "alice@x.com",
	})
	require.NoError(t, err)

	// Enable email verification.
	enableEmailVerification(t)

	// Login without a code → 10002.
	_, err = svc.Login(ctx, &authv1.LoginRequest{Username: "alice", Password: "password123"})
	assert.EqualValues(t, apierrors.CodeInvalidCredentials, apierrors.CodeOf(err))

	// Send a code and login with it.
	_, err = svc.SendEmailCode(ctx, &authv1.SendEmailCodeRequest{
		Email: "alice@x.com", Purpose: emailCodePurposeLogin,
	})
	require.NoError(t, err)
	client, _ := svc.redisClientFor()
	stored, _ := client.Get(ctx, emailCodeKeyPrefix+"alice@x.com:"+emailCodePurposeLogin).Result()
	resp, err := svc.Login(ctx, &authv1.LoginRequest{
		Username: "alice", Password: "password123", EmailCode: stored,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.SessionToken)
}

func TestSendVerificationEmailNoSMTP(t *testing.T) {
	// With no SMTP configured, sending logs the code and returns nil.
	require.NoError(t, sendVerificationEmail("alice@x.com", "123456"))
}

func TestSendVerificationEmailSMTPConfigured(t *testing.T) {
	// With SMTP configured but unreachable, sending returns an error
	// (exercises the SMTP path).
	cfg := &config.Configuration{}
	cfg.Auth.SMTP = config.SMTPConfig{Host: "127.0.0.1", Port: 1, Username: "u", Password: "p", From: "f@x.com"}
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	err := sendVerificationEmail("alice@x.com", "123456")
	assert.Error(t, err)
}

func TestSendVerificationEmailImplicitTLS(t *testing.T) {
	// Port 465 uses implicit TLS; an unreachable host returns an error
	// (exercises the TLS path).
	cfg := &config.Configuration{}
	cfg.Auth.SMTP = config.SMTPConfig{Host: "127.0.0.1", Port: 465, Username: "u", Password: "p", From: "f@x.com"}
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	err := sendVerificationEmail("alice@x.com", "123456")
	assert.Error(t, err)
}

func TestHostOf(t *testing.T) {
	assert.Equal(t, "smtp.example.com", hostOf("smtp.example.com:465"))
	assert.Equal(t, "smtp.example.com", hostOf("smtp.example.com"))
}