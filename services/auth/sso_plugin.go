// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// SSO plugin framework (feature #7): the pluggable identity-provider
// interface with three hooks — authorize, callback, and identity
// extraction — and the OIDC, SAML, and LDAP provider implementations.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// IDPPlugin is the pluggable identity-provider interface. Each provider
// type (oidc, saml, ldap) implements the three hooks. The framework
// dispatches by provider.Type at runtime.
type IDPPlugin interface {
	// Authorize builds the login initiation for an enabled provider:
	// a redirect URL (OIDC/SAML) or a bind form (LDAP).
	Authorize(ctx context.Context, p *SSOProvider) (*AuthorizeResult, error)

	// Callback completes the login: validates state/nonce, exchanges
	// the code/assertion or performs the LDAP bind, and returns the
	// extracted external identity.
	Callback(ctx context.Context, p *SSOProvider, req *authv1.SSOCallbackRequest) (*Identity, error)
}

// AuthorizeResult is the outcome of Authorize.
type AuthorizeResult struct {
	RedirectURL string // OIDC/SAML: the IdP authorize URL with signed state
	BindForm    string // LDAP: a marker the console uses to render the bind prompt
}

// Identity is the extracted external identity + claims (the
// identity-extraction hook's output).
type Identity struct {
	ExternalSubject string              // issuer + subject (OIDC/SAML) or DN (LDAP)
	Username        string              // from the mapped username claim
	Email           string              // from the mapped email claim
	Claims          map[string][]string // raw claims/attributes for org/role mapping
}

// NewPlugin returns the IDPPlugin for a provider type. An unknown type
// maps to CodeSSOProviderInvalid.
func NewPlugin(providerType string) (IDPPlugin, error) {
	switch providerType {
	case ProviderTypeOIDC:
		return &OIDCPlugin{}, nil
	case ProviderTypeSAML:
		return &SAMLPlugin{}, nil
	case ProviderTypeLDAP:
		return &LDAPPlugin{}, nil
	default:
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
}

// stateSecret is the server secret used to sign SSO state/nonce values.
// It is derived from a random value at process start; in production the
// state is additionally stored in a short-lived Redis key.
var stateSecret = func() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}()

// signState signs a state value with the server secret (HMAC-SHA256).
func signState(state string) string {
	mac := hmac.New(sha256.New, stateSecret)
	_, _ = mac.Write([]byte(state))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyState checks a state value against its signature.
func verifyState(state, sig string) bool {
	expected := signState(state)
	return hmac.Equal([]byte(expected), []byte(sig))
}

// newState generates a random state value.
func newState() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// httpClient is the shared HTTP client for IdP exchanges.
var httpClient = &http.Client{Timeout: 10 * time.Second}

// attributeMapping is the parsed attribute_mapping JSON.
type attributeMapping struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Org      string `json:"org"`
	Project  string `json:"project"`
	Role     string `json:"role"`
}

// parseAttributeMapping parses and validates the provider's
// attribute_mapping JSON. A malformed mapping maps to
// CodeSSOProviderInvalid.
func parseAttributeMapping(raw string) (*attributeMapping, error) {
	m := &attributeMapping{}
	if strings.TrimSpace(raw) == "" {
		return m, nil
	}
	if err := json.Unmarshal([]byte(raw), m); err != nil {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	return m, nil
}

// claimValue returns the first value of a claim path from the identity
// claims, or "" when absent.
func claimValue(claims map[string][]string, path string) string {
	if path == "" {
		return ""
	}
	if vals, ok := claims[path]; ok && len(vals) > 0 {
		return vals[0]
	}
	return ""
}

// buildAuthorizeURL builds an OIDC/SAML authorize URL with a signed
// state parameter.
func buildAuthorizeURL(base, clientID, redirectURI, scopes, state string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	if scopes != "" {
		q.Set("scope", scopes)
	}
	q.Set("state", state+"."+signState(state))
	u.RawQuery = q.Encode()
	return u.String()
}

// validateState checks the state parameter's signature. A mismatch maps
// to CodeSSOInvalidState.
func validateState(state string) error {
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 || !verifyState(parts[0], parts[1]) {
		return apierrors.New(apierrors.CodeSSOInvalidState)
	}
	return nil
}
