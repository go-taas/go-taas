package fvt

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePasswordIDP is an in-process OIDC IdP implementing the
// resource-owner password grant token endpoint. It returns an ID token
// with the given roles when the credentials match.
func fakePasswordIDP(t *testing.T, roles []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "password" ||
			r.FormValue("username") != "alice" || r.FormValue("password") != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		claims := map[string]any{
			"sub":                "sub-1",
			"preferred_username": "alice",
			"email":              "alice@x.com",
			"realm_access":       map[string]any{"roles": roles},
		}
		payload, _ := json.Marshal(claims)
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
		body := base64.RawURLEncoding.EncodeToString(payload)
		token := header + "." + body + ".sig"
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": token})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// seedPasswordProvider creates an enabled OIDC provider pointing at the
// fake IdP with a role attribute mapping.
func seedPasswordProvider(t *testing.T, env *ssoEnv, idpURL string) {
	t.Helper()
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers", map[string]any{
		"provider": map[string]any{
			"providerId": "keycloak", "type": "oidc", "displayName": "Keycloak",
			"issuer": idpURL, "clientId": "go-taas-console", "clientSecret": "secret",
			"redirectUri":        "http://localhost:9091/api/v1/auth/sso/*",
			"defaultOrg":         "org-fvt",
			"allowAutoProvision": true,
			"passwordLogin":      true,
			"attributeMapping":   `{"username":"preferred_username","email":"email","role":"realm_access.roles"}`,
		},
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers/keycloak:enable", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
}

// TestFVTSSOPasswordLoginAdmin walks AC3/AC10: an admin user's password
// login derives the admin realm and lands on the admin surface.
func TestFVTSSOPasswordLoginAdmin(t *testing.T) {
	env := newSSOEnv(t)
	idp := fakePasswordIDP(t, []string{"admin"})
	seedPasswordProvider(t, env, idp.URL)

	// User binding: admin role → admin realm.
	code, body := env.call(t, http.MethodPost, "/api/v1/auth/sso/keycloak/login", map[string]any{
		"username": "alice", "password": "pw",
	}, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "admin", body["realm"], "admin role derives the admin realm")
	sessionToken, _ := body["sessionToken"].(string)
	require.NotEmpty(t, sessionToken)

	// The session is resolvable with the admin realm.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/session", nil, map[string]string{"Authorization": "Bearer " + sessionToken})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "admin", body["realm"])
	assert.Equal(t, "alice", body["username"])
}

// TestFVTSSOPasswordLoginUser walks AC4/AC11: a non-admin user's
// password login derives the user realm.
func TestFVTSSOPasswordLoginUser(t *testing.T) {
	env := newSSOEnv(t)
	idp := fakePasswordIDP(t, []string{"developer"})
	seedPasswordProvider(t, env, idp.URL)

	code, body := env.call(t, http.MethodPost, "/api/v1/auth/sso/keycloak/login", map[string]any{
		"username": "alice", "password": "pw",
	}, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "user", body["realm"], "non-admin role derives the user realm")
}

// TestFVTSSOPasswordLoginAdminBinding walks AC10: an admin who signs in
// at the admin binding still derives the admin realm (role-derived, not
// binding-derived).
func TestFVTSSOPasswordLoginAdminBinding(t *testing.T) {
	env := newSSOEnv(t)
	idp := fakePasswordIDP(t, []string{"admin"})
	seedPasswordProvider(t, env, idp.URL)

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/keycloak/login", map[string]any{
		"username": "alice", "password": "pw",
	}, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "admin", body["realm"], "admin binding still derives the admin realm from the role")
}

// TestFVTSSOPasswordLoginWrongCredentials walks AC5: invalid credentials
// → 10024.
func TestFVTSSOPasswordLoginWrongCredentials(t *testing.T) {
	env := newSSOEnv(t)
	idp := fakePasswordIDP(t, []string{"admin"})
	seedPasswordProvider(t, env, idp.URL)

	code, body := env.call(t, http.MethodPost, "/api/v1/auth/sso/keycloak/login", map[string]any{
		"username": "alice", "password": "wrong",
	}, nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10024), body["code"])
}

// TestFVTSSOPasswordLoginUnknownProvider walks the validation order: an
// unknown provider → 10021.
func TestFVTSSOPasswordLoginUnknownProvider(t *testing.T) {
	env := newSSOEnv(t)
	code, body := env.call(t, http.MethodPost, "/api/v1/auth/sso/missing/login", map[string]any{
		"username": "alice", "password": "pw",
	}, nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10021), body["code"])
}

// TestFVTSSOPasswordLoginSAML walks the validation order: a SAML
// provider cannot use the password grant → 10028.
func TestFVTSSOPasswordLoginSAML(t *testing.T) {
	env := newSSOEnv(t)
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers", map[string]any{
		"provider": map[string]any{
			"providerId": "saml", "type": "saml", "displayName": "SAML",
			"entityId": "e", "acsUrl": "https://console.example.com/acs",
		},
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers/saml:enable", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	code, body = env.call(t, http.MethodPost, "/api/v1/auth/sso/saml/login", map[string]any{
		"username": "alice", "password": "pw",
	}, nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10028), body["code"])
}
