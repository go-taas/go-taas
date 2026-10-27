// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// Email verification codes for local registration and login: a 6-digit
// code is generated, stored in Redis with a short expiry, and sent via
// SMTP (or logged when SMTP is not configured). A 60-second resend
// cooldown prevents code spam.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/smtp"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
)

// emailCodeTTL is how long a verification code stays valid.
const emailCodeTTL = 5 * time.Minute

// emailCodeResendCooldown is the minimum wait before requesting another
// code.
const emailCodeResendCooldown = 60 * time.Second

// emailCodeKeyPrefix is the Redis key prefix for verification codes.
const emailCodeKeyPrefix = "auth:email-code:"

// emailCodeCooldownKeyPrefix is the Redis key prefix for the resend
// cooldown.
const emailCodeCooldownKeyPrefix = "auth:email-code-cooldown:"

// emailCodeLen is the number of digits in a verification code.
const emailCodeLen = 6

// emailCodePurposeRegister and emailCodePurposeLogin are the supported
// code purposes.
const (
	emailCodePurposeRegister = "register"
	emailCodePurposeLogin    = "login"
	// emailCodePurposeChangeEmail verifies ownership of a NEW address
	// before the profile's email is replaced.
	emailCodePurposeChangeEmail = "change_email"
)

// emailVerificationEnabled reports whether email verification is
// enabled by config.
func emailVerificationEnabled() bool {
	cfg := config.GetConfig()
	return cfg != nil && cfg.Auth.EmailVerification
}

// smtpConfig returns the configured SMTP transport.
func smtpConfig() config.SMTPConfig {
	cfg := config.GetConfig()
	if cfg == nil {
		return config.SMTPConfig{}
	}
	return cfg.Auth.SMTP
}

// redisClientFor lazily returns the Redis client for code storage.
func (s *Service) redisClientFor() (*goredis.Client, error) {
	if s.sessionStore != nil {
		return s.sessionStore.client, nil
	}
	if s.components == nil || s.components.Redis() == nil {
		return nil, apierrors.Newf(apierrors.CodeInternal, "auth: redis component unavailable")
	}
	raw := s.components.Redis().Client()
	client, ok := raw.(*goredis.Client)
	if !ok {
		return nil, apierrors.Newf(apierrors.CodeInternal, "auth: unexpected redis handle type %T", raw)
	}
	return client, nil
}

// SendEmailCode generates a verification code, stores it in Redis, and
// sends it to the given email. A 60-second cooldown prevents resend
// spam.
func (s *Service) SendEmailCode(ctx context.Context, req *authv1.SendEmailCodeRequest) (*authv1.SendEmailCodeResponse, error) {
	if !emailVerificationEnabled() {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	email := strings.TrimSpace(req.GetEmail())
	purpose := strings.TrimSpace(req.GetPurpose())
	if email == "" || (purpose != emailCodePurposeRegister &&
		purpose != emailCodePurposeLogin && purpose != emailCodePurposeChangeEmail) {
		return nil, apierrors.New(apierrors.CodeUserNotFound)
	}
	client, err := s.redisClientFor()
	if err != nil {
		return nil, err
	}
	// Enforce the resend cooldown.
	cooldownKey := emailCodeCooldownKeyPrefix + email + ":" + purpose
	ttl, err := client.TTL(ctx, cooldownKey).Result()
	if err != nil {
		return nil, err
	}
	if ttl > 0 {
		return nil, apierrors.New(apierrors.CodeRateLimitExceeded)
	}

	code, err := generateEmailCode()
	if err != nil {
		return nil, err
	}
	key := emailCodeKeyPrefix + email + ":" + purpose
	if err := client.Set(ctx, key, code, emailCodeTTL).Err(); err != nil {
		return nil, err
	}
	if err := client.Set(ctx, cooldownKey, "1", emailCodeResendCooldown).Err(); err != nil {
		return nil, err
	}
	if err := sendVerificationEmail(email, code); err != nil {
		// A send failure is not fatal for the code issuance; the code is
		// still stored and can be verified.
		logger.S().Warnw("email code send failed", "email", email, "err", err)
	}
	return &authv1.SendEmailCodeResponse{
		Response:           okResponse(),
		ResendAfterSeconds: int64(emailCodeResendCooldown / time.Second),
	}, nil
}

// verifyEmailCode checks a submitted code against the stored value and
// consumes it on success.
func (s *Service) verifyEmailCode(ctx context.Context, email, purpose, code string) error {
	if !emailVerificationEnabled() {
		return nil
	}
	if email == "" || code == "" {
		return apierrors.New(apierrors.CodeInvalidCredentials)
	}
	client, err := s.redisClientFor()
	if err != nil {
		return err
	}
	key := emailCodeKeyPrefix + email + ":" + purpose
	stored, err := client.Get(ctx, key).Result()
	if errors.Is(err, goredis.Nil) {
		return apierrors.New(apierrors.CodeInvalidCredentials)
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(code)) != 1 {
		return apierrors.New(apierrors.CodeInvalidCredentials)
	}
	// Consume the code (single-use).
	_ = client.Del(ctx, key).Err()
	return nil
}

// generateEmailCode returns a random 6-digit code.
func generateEmailCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// sendVerificationEmail sends the code via SMTP, or logs it when SMTP
// is not configured. Port 465 uses implicit TLS (SMTPS); other ports
// use STARTTLS via smtp.SendMail.
func sendVerificationEmail(email, code string) error {
	cfg := smtpConfig()
	if cfg.Host == "" {
		logger.S().Infow("email verification code", "email", email, "code", code)
		return nil
	}
	port := cfg.Port
	if port == 0 {
		port = 587
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, port)
	from := cfg.From
	if from == "" {
		from = "no-reply@go-taas.com"
	}
	msg := []byte(fmt.Sprintf("To: %s\r\nSubject: Go TaaS verification code\r\n\r\nYour verification code is %s. It expires in 5 minutes.\r\n", email, code))
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	if port == 465 {
		return sendMailImplicitTLS(addr, auth, from, []string{email}, msg)
	}
	return smtp.SendMail(addr, auth, from, []string{email}, msg)
}

// sendMailImplicitTLS sends mail over an implicit-TLS (SMTPS)
// connection, used by port 465.
func sendMailImplicitTLS(addr string, a smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: hostOf(addr), InsecureSkipVerify: false})
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	c, err := smtp.NewClient(conn, hostOf(addr))
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if a != nil {
		if err := c.Auth(a); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// hostOf returns the host portion of an addr (host:port).
func hostOf(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}