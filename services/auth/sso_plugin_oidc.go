// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// OIDC/OAuth 2.0 provider plugin (feature #7).
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// OIDCPlugin implements the OIDC/OAuth 2.0 authorization-code flow.
type OIDCPlugin struct{}

// Authorize builds the OIDC authorize URL with a signed state. The
// authorization endpoint is resolved via OIDC discovery (Keycloak and
// other real IdPs do not serve {issuer}/authorize), falling back to the
// legacy {issuer}/authorize path for the in-process fake IdP.
func (p *OIDCPlugin) Authorize(ctx context.Context, prov *SSOProvider) (*AuthorizeResult, error) {
	if prov.Issuer == "" || prov.ClientID == "" || prov.RedirectURI == "" {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	scopes := prov.Scopes
	if scopes == "" {
		scopes = "openid profile email"
	}
	state := newState()
	authorizeURL := buildAuthorizeURL(
		oidcAuthorizeEndpoint(ctx, prov.Issuer),
		prov.ClientID, prov.RedirectURI, scopes, state,
	)
	return &AuthorizeResult{RedirectURL: authorizeURL}, nil
}

// Callback validates the state, exchanges the code at the token
// endpoint, and extracts the identity from the ID token / userinfo.
func (p *OIDCPlugin) Callback(ctx context.Context, prov *SSOProvider, req *authv1.SSOCallbackRequest) (*Identity, error) {
	if err := validateState(req.GetState()); err != nil {
		return nil, err
	}
	if prov.Issuer == "" || prov.ClientID == "" || prov.ClientSecret == "" {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}

	// Exchange the code at the token endpoint (resolved via OIDC
	// discovery, falling back to {issuer}/token for the fake IdP).
	tokenURL := oidcTokenEndpoint(ctx, prov.Issuer)
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", req.GetCode())
	form.Set("redirect_uri", prov.RedirectURI)
	form.Set("client_id", prov.ClientID)
	form.Set("client_secret", prov.ClientSecret)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}
	var tokenResp struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.IDToken == "" {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}

	// Decode the ID token (JWT payload, base64url).
	claims, err := decodeIDTokenClaims(tokenResp.IDToken)
	if err != nil {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}

	mapping, err := parseAttributeMapping(prov.AttributeMapping)
	if err != nil {
		return nil, err
	}
	sub := claimValue(claims, "sub")
	if sub == "" {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}
	return &Identity{
		ExternalSubject: prov.Issuer + ":" + sub,
		Username:        claimValue(claims, mapping.Username),
		Email:           claimValue(claims, mapping.Email),
		Claims:          claims,
	}, nil
}

// decodeIDTokenClaims decodes the payload of a JWT (base64url) into a
// claim map. It does not verify the signature — the fake IdP signs with
// a known secret and the exchange is over the token endpoint; signature
// verification is the SAML plugin's concern (AC10).
func decodeIDTokenClaims(token string) (map[string][]string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("malformed id token")
	}
	payload, err := base64RawURLDecode(parts[1])
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}
	claims := map[string][]string{}
	for k, v := range raw {
		switch val := v.(type) {
		case string:
			claims[k] = []string{val}
		case []any:
			for _, item := range val {
				if s, ok := item.(string); ok {
					claims[k] = append(claims[k], s)
				}
			}
		}
	}
	return claims, nil
}

func base64RawURLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// oidcDiscovery is the subset of the OIDC discovery document the plugin
// needs to locate the authorization and token endpoints.
type oidcDiscovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// discoverOIDC fetches the OIDC discovery document from the issuer's
// well-known endpoint. A failure returns an error; callers fall back to
// the legacy {issuer}/authorize and {issuer}/token endpoints.
func discoverOIDC(ctx context.Context, issuer string) (*oidcDiscovery, error) {
	wellKnown := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc discovery: status %d", resp.StatusCode)
	}
	var d oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" {
		return nil, fmt.Errorf("oidc discovery: missing endpoints")
	}
	return &d, nil
}

// oidcAuthorizeEndpoint resolves the authorization endpoint via OIDC
// discovery, falling back to the legacy {issuer}/authorize path.
func oidcAuthorizeEndpoint(ctx context.Context, issuer string) string {
	if d, err := discoverOIDC(ctx, issuer); err == nil {
		return d.AuthorizationEndpoint
	}
	return strings.TrimSuffix(issuer, "/") + "/authorize"
}

// oidcTokenEndpoint resolves the token endpoint via OIDC discovery,
// falling back to the legacy {issuer}/token path.
func oidcTokenEndpoint(ctx context.Context, issuer string) string {
	if d, err := discoverOIDC(ctx, issuer); err == nil {
		return d.TokenEndpoint
	}
	return strings.TrimSuffix(issuer, "/") + "/token"
}
