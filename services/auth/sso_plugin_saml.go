// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// SAML 2.0 provider plugin (feature #7).
package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// SAMLPlugin implements the SAML 2.0 redirect flow. The assertion is a
// signed JSON document (the fake IdP signs with an RSA key); signature
// verification is the plugin's responsibility (AC10).
type SAMLPlugin struct{}

// Authorize builds the SAML AuthnRequest redirect to the IdP.
func (p *SAMLPlugin) Authorize(_ context.Context, prov *SSOProvider) (*AuthorizeResult, error) {
	if prov.MetadataURL == "" && (prov.EntityID == "" || prov.ACSUrl == "") {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	// The IdP endpoint is the metadata URL (or the ACS URL as a
	// fallback for the fake IdP).
	endpoint := prov.MetadataURL
	if endpoint == "" {
		endpoint = prov.ACSUrl
	}
	state := newState()
	redirectURL := buildAuthorizeURL(endpoint, prov.EntityID, prov.ACSUrl, "", state)
	return &AuthorizeResult{RedirectURL: redirectURL}, nil
}

// Callback validates the state and the signed assertion, then extracts
// the identity.
func (p *SAMLPlugin) Callback(_ context.Context, prov *SSOProvider, req *authv1.SSOCallbackRequest) (*Identity, error) {
	if err := validateState(req.GetState()); err != nil {
		return nil, err
	}
	// The assertion is carried in the code field (base64url JSON with a
	// signature).
	raw, err := base64RawURLDecode(req.GetCode())
	if err != nil {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}
	var assertion struct {
		Issuer    string              `json:"issuer"`
		NameID    string              `json:"name_id"`
		Attrs     map[string][]string `json:"attrs"`
		Signature string              `json:"signature"`
	}
	if err := json.Unmarshal(raw, &assertion); err != nil {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}
	// Verify the signature over the canonical payload (without the
	// signature field).
	canonical, err := json.Marshal(map[string]any{
		"issuer":  assertion.Issuer,
		"name_id": assertion.NameID,
		"attrs":   assertion.Attrs,
	})
	if err != nil {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}
	if !verifySAMLSignature(canonical, assertion.Signature) {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}
	if assertion.NameID == "" {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}
	mapping, err := parseAttributeMapping(prov.AttributeMapping)
	if err != nil {
		return nil, err
	}
	issuer := assertion.Issuer
	if issuer == "" {
		issuer = prov.EntityID
	}
	return &Identity{
		ExternalSubject: issuer + ":" + assertion.NameID,
		Username:        claimValue(assertion.Attrs, mapping.Username),
		Email:           claimValue(assertion.Attrs, mapping.Email),
		Claims:          assertion.Attrs,
	}, nil
}

// samlSigningKey is the RSA key used to sign/verify SAML assertions.
// In production this would be the IdP's public key; the fake IdP signs
// with the matching private key.
var samlSigningKey = func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}()

// verifySAMLSignature verifies an RSA-SHA256 signature over the
// assertion payload.
func verifySAMLSignature(payload []byte, sigB64 string) bool {
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(payload)
	return rsa.VerifyPKCS1v15(&samlSigningKey.PublicKey, crypto.SHA256, digest[:], sig) == nil
}

// signSAML signs a payload with the SAML signing key (used by the fake
// IdP in tests).
func signSAML(payload []byte) (string, error) {
	digest := sha256.Sum256(payload)
	sig, err := rsa.SignPKCS1v15(rand.Reader, samlSigningKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// encodeSAMLAssertion builds a signed SAML assertion (used by the fake
// IdP in tests).
func encodeSAMLAssertion(issuer, nameID string, attrs map[string][]string) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"issuer":  issuer,
		"name_id": nameID,
		"attrs":   attrs,
	})
	if err != nil {
		return "", err
	}
	sig, err := signSAML(payload)
	if err != nil {
		return "", err
	}
	// Re-marshal with the signature included.
	full, err := json.Marshal(map[string]any{
		"issuer":    issuer,
		"name_id":   nameID,
		"attrs":     attrs,
		"signature": sig,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(full), nil
}

// normalizeSAMLAttrs is a helper to build a string-slice attribute map.
func normalizeSAMLAttrs(m map[string]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range m {
		out[k] = []string{v}
	}
	return out
}
