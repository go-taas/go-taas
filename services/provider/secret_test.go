package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// TestMaskHint covers design FR2.1: the masked hint is the last four
// characters for keys of at least eight runes, otherwise fully masked.
func TestMaskHint(t *testing.T) {
	assert.Equal(t, "••••3f9a", maskHint("sk-abcdef3f9a"))
	assert.Equal(t, "••••••••", maskHint("short"))
	assert.Equal(t, "••••••••", maskHint("1234567"))
	assert.Equal(t, "••••5678", maskHint("12345678"))
}

// TestSecretRoundTrip asserts the webhook cipher round-trips and a wrong
// master key maps to 13007.
func TestSecretRoundTrip(t *testing.T) {
	svc := &Service{secretKey: "dev-provider-key-test"}
	ct, err := svc.encryptSecret("sk-secret-value")
	require.NoError(t, err)
	got, err := svc.decryptSecret(ct)
	require.NoError(t, err)
	assert.Equal(t, "sk-secret-value", got)

	wrong := &Service{secretKey: "another-key"}
	_, err = wrong.decryptSecret(ct)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderSecretUnavailable, apierrors.CodeOf(err))
}

// TestDecryptEmptyCiphertextIsSecretUnavailable asserts an existing provider
// with an empty ciphertext fails closed with 13007.
func TestDecryptEmptyCiphertextIsSecretUnavailable(t *testing.T) {
	svc := &Service{secretKey: "k"}
	_, err := svc.decryptSecret(nil)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderSecretUnavailable, apierrors.CodeOf(err))
}

// TestHeaderRoundTrip asserts extra-header values are encrypted and
// decrypted symmetrically and the stored form is not the plaintext.
func TestHeaderRoundTrip(t *testing.T) {
	svc := &Service{secretKey: "dev-provider-key-test"}
	enc, err := svc.encryptHeaders(map[string]string{"X-Api-Tenant": "tenant-secret"})
	require.NoError(t, err)
	require.NotContains(t, enc["X-Api-Tenant"], "tenant-secret")
	dec, err := svc.decryptHeaders(enc)
	require.NoError(t, err)
	assert.Equal(t, "tenant-secret", dec["X-Api-Tenant"])
}

// TestHeaderNames asserts reads expose names only.
func TestHeaderNames(t *testing.T) {
	enc, err := (&Service{secretKey: "k"}).encryptHeaders(map[string]string{"X-B": "2", "X-A": "1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"X-A", "X-B"}, headerNames(enc))
}
