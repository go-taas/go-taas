package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeOIDCServer is an in-process OIDC IdP implementing the token
// endpoint. It returns an ID token with the given claims.
func fakeOIDCServer(t *testing.T, sub, username, email string, groups []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		claims := map[string]any{
			"sub":              sub,
			"preferred_username": username,
			"email":            email,
			"groups":           groups,
		}
		payload, _ := json.Marshal(claims)
		// A JWT-like token: header.payload.signature (signature ignored).
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
		body := base64.RawURLEncoding.EncodeToString(payload)
		token := header + "." + body + ".sig"
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": token})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestOIDCPluginAuthorize(t *testing.T) {
	plugin := &OIDCPlugin{}
	prov := &SSOProvider{
		Issuer:      "https://idp.example.com",
		ClientID:    "client-1",
		RedirectURI: "https://console.example.com/callback",
		Scopes:      "openid profile email",
	}
	result, err := plugin.Authorize(context.Background(), prov)
	require.NoError(t, err)
	assert.Contains(t, result.RedirectURL, "https://idp.example.com/authorize")
	assert.Contains(t, result.RedirectURL, "client_id=client-1")
	assert.Contains(t, result.RedirectURL, "state=")

	// Missing issuer → 10028.
	_, err = plugin.Authorize(context.Background(), &SSOProvider{ClientID: "c"})
	assert.EqualValues(t, apierrors.CodeSSOProviderInvalid, apierrors.CodeOf(err))
}

func TestOIDCPluginCallback(t *testing.T) {
	srv := fakeOIDCServer(t, "sub-1", "alice", "alice@x.com", []string{"admins"})
	plugin := &OIDCPlugin{}
	prov := &SSOProvider{
		Issuer:      srv.URL,
		ClientID:    "client-1",
		ClientSecret: "secret",
		RedirectURI: "https://console.example.com/callback",
		AttributeMapping: `{"username":"preferred_username","email":"email","org":"groups","role":"groups"}`,
	}

	// Build a valid state.
	state := newState()
	signedState := state + "." + signState(state)

	identity, err := plugin.Callback(context.Background(), prov, &authv1.SSOCallbackRequest{
		Code:  "code-1",
		State: signedState,
	})
	require.NoError(t, err)
	assert.Equal(t, srv.URL+":sub-1", identity.ExternalSubject)
	assert.Equal(t, "alice", identity.Username)
	assert.Equal(t, "alice@x.com", identity.Email)
	assert.Equal(t, []string{"admins"}, identity.Claims["groups"])

	// Bad state → 10023.
	_, err = plugin.Callback(context.Background(), prov, &authv1.SSOCallbackRequest{
		Code:  "code-1",
		State: "bad-state",
	})
	assert.EqualValues(t, apierrors.CodeSSOInvalidState, apierrors.CodeOf(err))

	// IdP rejection → 10024 (token endpoint returns non-200).
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(badSrv.Close)
	badProv := &SSOProvider{
		Issuer:       badSrv.URL,
		ClientID:     "c",
		ClientSecret: "s",
		RedirectURI:  "https://console.example.com/callback",
	}
	_, err = plugin.Callback(context.Background(), badProv, &authv1.SSOCallbackRequest{
		Code:  "code-1",
		State: signedState,
	})
	assert.EqualValues(t, apierrors.CodeSSOAuthFailed, apierrors.CodeOf(err))
}

func TestSAMLPluginAuthorizeAndCallback(t *testing.T) {
	plugin := &SAMLPlugin{}
	prov := &SSOProvider{
		MetadataURL: "https://idp.example.com/saml",
		EntityID:    "sp-entity",
		ACSUrl:      "https://console.example.com/acs",
		AttributeMapping: `{"username":"uid","email":"mail","org":"groups","role":"groups"}`,
	}

	result, err := plugin.Authorize(context.Background(), prov)
	require.NoError(t, err)
	assert.Contains(t, result.RedirectURL, "https://idp.example.com/saml")

	// Build a signed assertion.
	attrs := normalizeSAMLAttrs(map[string]string{"uid": "bob", "mail": "bob@x.com", "groups": "devs"})
	assertion, err := encodeSAMLAssertion("https://idp.example.com", "name-1", attrs)
	require.NoError(t, err)

	state := newState()
	signedState := state + "." + signState(state)
	identity, err := plugin.Callback(context.Background(), prov, &authv1.SSOCallbackRequest{
		Code:  assertion,
		State: signedState,
	})
	require.NoError(t, err)
	assert.Equal(t, "https://idp.example.com:name-1", identity.ExternalSubject)
	assert.Equal(t, "bob", identity.Username)

	// Bad state → 10023.
	_, err = plugin.Callback(context.Background(), prov, &authv1.SSOCallbackRequest{
		Code:  assertion,
		State: "bad",
	})
	assert.EqualValues(t, apierrors.CodeSSOInvalidState, apierrors.CodeOf(err))

	// Invalid signature → 10024.
	badAssertion := base64.RawURLEncoding.EncodeToString([]byte(`{"issuer":"x","name_id":"y","attrs":{},"signature":"AAAA"}`))
	_, err = plugin.Callback(context.Background(), prov, &authv1.SSOCallbackRequest{
		Code:  badAssertion,
		State: signedState,
	})
	assert.EqualValues(t, apierrors.CodeSSOAuthFailed, apierrors.CodeOf(err))
}

func TestLDAPPluginAuthorizeAndCallback(t *testing.T) {
	// Fake LDAP responder.
	mux := http.NewServeMux()
	mux.HandleFunc("/bind", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("username") == "alice" && r.FormValue("password") == "pw" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"dn": "cn=alice,dc=example,dc=com",
				"attrs": map[string][]string{
					"uid":  {"alice"},
					"mail": {"alice@x.com"},
				},
			})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	plugin := &LDAPPlugin{}
	prov := &SSOProvider{
		Host:             srv.URL,
		BaseDN:           "dc=example,dc=com",
		UserFilter:       "(uid={{username}})",
		AttributeMapping: `{"username":"uid","email":"mail"}`,
	}

	// Authorize returns a bind form.
	result, err := plugin.Authorize(context.Background(), prov)
	require.NoError(t, err)
	assert.Equal(t, "ldap", result.BindForm)

	// Valid bind.
	identity, err := plugin.Callback(context.Background(), prov, &authv1.SSOCallbackRequest{
		Username: "alice",
		Password: "pw",
	})
	require.NoError(t, err)
	assert.Equal(t, "cn=alice,dc=example,dc=com", identity.ExternalSubject)
	assert.Equal(t, "alice", identity.Username)

	// Invalid credentials → 10024.
	_, err = plugin.Callback(context.Background(), prov, &authv1.SSOCallbackRequest{
		Username: "alice",
		Password: "wrong",
	})
	assert.EqualValues(t, apierrors.CodeSSOAuthFailed, apierrors.CodeOf(err))
}

func TestNewPlugin(t *testing.T) {
	_, err := NewPlugin(ProviderTypeOIDC)
	require.NoError(t, err)
	_, err = NewPlugin(ProviderTypeSAML)
	require.NoError(t, err)
	_, err = NewPlugin(ProviderTypeLDAP)
	require.NoError(t, err)
	_, err = NewPlugin("wechat")
	assert.EqualValues(t, apierrors.CodeSSOProviderInvalid, apierrors.CodeOf(err))
}

func TestStateSigning(t *testing.T) {
	state := newState()
	sig := signState(state)
	assert.True(t, verifyState(state, sig))
	assert.False(t, verifyState(state, "wrong"))
	assert.False(t, verifyState("other", sig))
}

func TestParseAttributeMapping(t *testing.T) {
	m, err := parseAttributeMapping(`{"username":"preferred_username","email":"email","org":"groups","role":"groups"}`)
	require.NoError(t, err)
	assert.Equal(t, "preferred_username", m.Username)
	assert.Equal(t, "groups", m.Org)

	// Malformed → 10028.
	_, err = parseAttributeMapping(`{invalid`)
	assert.EqualValues(t, apierrors.CodeSSOProviderInvalid, apierrors.CodeOf(err))

	// Empty → nil error.
	_, err = parseAttributeMapping("")
	require.NoError(t, err)
}

var _ = strings.TrimSpace