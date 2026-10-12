package provider

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/webhook"
)

// devKeyPrefix is the prefix of the dev-only fallback master key derived
// from the MQ namespace, mirroring the webhook's rule (AD3/AD12).
const devKeyPrefix = "dev-provider-key-"

// maskHint returns the masked api-key hint: the last four characters
// prefixed with a fixed mask when the key is at least eight characters
// long, otherwise a fully-masked hint (design FR2.1).
func maskHint(key string) string {
	const fullMask = "••••••••"
	if utf8.RuneCountInString(key) < 8 {
		return fullMask
	}
	runes := []rune(key)
	return "••••" + string(runes[len(runes)-4:])
}

// encryptSecret encrypts a plaintext with the resolved master key using
// the webhook cipher (one crypto path, AD3).
func (s *Service) encryptSecret(plaintext string) ([]byte, error) {
	ct, err := webhook.EncryptSecret(plaintext, s.secretKey)
	if err != nil {
		return nil, apierrors.Newf(apierrors.CodeProviderSecretUnavailable, "provider: encrypt secret: %v", err)
	}
	return ct, nil
}

// decryptSecret decrypts a stored ciphertext with the resolved master key.
// A failure (changed master key, tampered/empty row) maps to 13007.
func (s *Service) decryptSecret(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", apierrors.New(apierrors.CodeProviderSecretUnavailable)
	}
	plain, err := webhook.DecryptSecret(ciphertext, s.secretKey)
	if err != nil {
		return "", apierrors.New(apierrors.CodeProviderSecretUnavailable)
	}
	return plain, nil
}

// encryptHeaders encrypts every extra-header value; a nil map stays nil.
func (s *Service) encryptHeaders(headers map[string]string) (StringMap, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(StringMap, len(headers))
	for name, value := range headers {
		ct, err := s.encryptSecret(value)
		if err != nil {
			return nil, err
		}
		out[name] = base64.StdEncoding.EncodeToString(ct)
	}
	return out, nil
}

// decryptHeaders decrypts the stored extra-header values.
func (s *Service) decryptHeaders(headers StringMap) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(headers))
	for name, encoded := range headers {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, apierrors.New(apierrors.CodeProviderSecretUnavailable)
		}
		plain, err := s.decryptSecret(raw)
		if err != nil {
			return nil, err
		}
		out[name] = plain
	}
	return out, nil
}

// trimTrailingSlashes strips trailing slashes from a base URL so the
// probe and proxy paths append cleanly (design FR3.2).
func trimTrailingSlashes(baseURL string) string {
	return strings.TrimRight(baseURL, "/")
}
