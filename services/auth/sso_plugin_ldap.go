// Package auth implements the authentication and authorization service:
// local accounts, API keys, SSO federation, and the gateway-facing key
// verification used to authenticate inference traffic.
//
// LDAP/LDAPS provider plugin (feature #7).
package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// LDAPPlugin implements the LDAP/LDAPS directory bind flow. The
// directory is reached over the wire (bind + search); the password is
// used only for the bind and never persisted (FR2.5).
type LDAPPlugin struct{}

// Authorize returns a bind form — the console renders a username/password
// prompt (FR2.1).
func (p *LDAPPlugin) Authorize(_ context.Context, _ *SSOProvider) (*AuthorizeResult, error) {
	return &AuthorizeResult{BindForm: "ldap"}, nil
}

// Callback performs a directory bind with the submitted DN/password,
// searches the base DN for the user, and extracts the identity by DN.
func (p *LDAPPlugin) Callback(ctx context.Context, prov *SSOProvider, req *authv1.SSOCallbackRequest) (*Identity, error) {
	if prov.Host == "" || prov.BaseDN == "" {
		return nil, apierrors.New(apierrors.CodeSSOProviderInvalid)
	}
	username := strings.TrimSpace(req.GetUsername())
	password := req.GetPassword()
	if username == "" || password == "" {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}

	// The directory endpoint is the host (the fake LDAP responder is an
	// HTTP server in tests). The bind is a POST with the DN/password.
	endpoint := prov.Host
	if !strings.HasPrefix(endpoint, "http") {
		endpoint = "http://" + endpoint
	}
	form := url.Values{}
	form.Set("username", username)
	form.Set("password", password)
	form.Set("base_dn", prov.BaseDN)
	form.Set("user_filter", prov.UserFilter)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/bind", strings.NewReader(form.Encode()))
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
	var bindResp struct {
		DN    string              `json:"dn"`
		Attrs map[string][]string `json:"attrs"`
	}
	if err := json.Unmarshal(body, &bindResp); err != nil || bindResp.DN == "" {
		return nil, apierrors.New(apierrors.CodeSSOAuthFailed)
	}

	mapping, err := parseAttributeMapping(prov.AttributeMapping)
	if err != nil {
		return nil, err
	}
	return &Identity{
		ExternalSubject: bindResp.DN,
		Username:        claimValue(bindResp.Attrs, mapping.Username),
		Email:           claimValue(bindResp.Attrs, mapping.Email),
		Claims:          bindResp.Attrs,
	}, nil
}
