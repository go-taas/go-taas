package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakePasswordOIDCServer is an in-process OIDC IdP implementing the
// resource-owner password grant token endpoint. It returns an ID token
// with the given claims when the credentials match.
func fakePasswordOIDCServer(t *testing.T, sub, username, email string, roles []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "password" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.FormValue("username") != "alice" || r.FormValue("password") != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		claims := map[string]any{
			"sub":                sub,
			"preferred_username": username,
			"email":              email,
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
func seedPasswordProvider(t *testing.T, svc *Service, idpURL string, roleClaim string) {
	t.Helper()
	prov := &SSOProvider{
		ID: "keycloak", Type: ProviderTypeOIDC, DisplayName: "Keycloak",
		Issuer: idpURL, ClientID: "go-taas-console", ClientSecret: "secret",
		RedirectURI:        "http://localhost:9091/api/v1/auth/sso/*",
		Enabled:            true,
		AllowAutoProvision: true,
		AttributeMapping:   `{"username":"preferred_username","email":"email","role":"` + roleClaim + `"}`,
	}
	require.NoError(t, svc.ssoRepo.CreateProvider(context.Background(), prov))
}

func TestSSOPasswordLoginAdminRealm(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()
	idp := fakePasswordOIDCServer(t, "sub-admin", "admin", "admin@x.com", []string{"admin"})
	seedPasswordProvider(t, svc, idp.URL, "realm_access.roles")

	resp, err := svc.SSOPasswordLogin(ctx, &authv1.SSOPasswordLoginRequest{
		ProviderId: "keycloak", Username: "alice", Password: "pw",
	})
	require.NoError(t, err)
	assert.Equal(t, RealmAdmin, resp.GetRealm(), "admin role derives the admin realm")
	assert.NotEmpty(t, resp.GetSessionToken())
	assert.True(t, resp.GetExpiresAt() > time.Now().Unix())

	// The session is resolvable and carries the admin realm.
	sess, err := svc.sessionStore.Get(ctx, resp.GetSessionToken())
	require.NoError(t, err)
	assert.Equal(t, RealmAdmin, sess.Realm)
	assert.Contains(t, sess.Roles, "admin")
}

func TestSSOPasswordLoginUserRealm(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()
	idp := fakePasswordOIDCServer(t, "sub-user", "alice", "alice@x.com", []string{"developer"})
	seedPasswordProvider(t, svc, idp.URL, "realm_access.roles")

	resp, err := svc.SSOPasswordLogin(ctx, &authv1.SSOPasswordLoginRequest{
		ProviderId: "keycloak", Username: "alice", Password: "pw",
	})
	require.NoError(t, err)
	assert.Equal(t, RealmUser, resp.GetRealm(), "non-admin role derives the user realm")
}

func TestSSOPasswordLoginValidation(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	// Unknown provider → 10021.
	_, err := svc.SSOPasswordLogin(ctx, &authv1.SSOPasswordLoginRequest{
		ProviderId: "missing", Username: "a", Password: "b",
	})
	assert.EqualValues(t, apierrors.CodeSSOProviderNotFound, apierrors.CodeOf(err))

	// Disabled provider → 10022.
	require.NoError(t, svc.ssoRepo.CreateProvider(context.Background(), &SSOProvider{
		ID: "disabled", Type: ProviderTypeOIDC, DisplayName: "D",
		Issuer: "https://idp.example.com", ClientID: "c", ClientSecret: "s",
		RedirectURI: "https://console.example.com/callback", Enabled: false,
	}))
	_, err = svc.SSOPasswordLogin(ctx, &authv1.SSOPasswordLoginRequest{
		ProviderId: "disabled", Username: "a", Password: "b",
	})
	assert.EqualValues(t, apierrors.CodeSSOProviderDisabled, apierrors.CodeOf(err))

	// SAML provider → 10028.
	require.NoError(t, svc.ssoRepo.CreateProvider(context.Background(), &SSOProvider{
		ID: "saml", Type: ProviderTypeSAML, DisplayName: "S",
		EntityID: "e", ACSUrl: "https://console.example.com/acs", Enabled: true,
	}))
	_, err = svc.SSOPasswordLogin(ctx, &authv1.SSOPasswordLoginRequest{
		ProviderId: "saml", Username: "a", Password: "b",
	})
	assert.EqualValues(t, apierrors.CodeSSOProviderInvalid, apierrors.CodeOf(err))

	// Empty username/password → 10024.
	require.NoError(t, svc.ssoRepo.CreateProvider(context.Background(), &SSOProvider{
		ID: "oidc", Type: ProviderTypeOIDC, DisplayName: "O",
		Issuer: "https://idp.example.com", ClientID: "c", ClientSecret: "s",
		RedirectURI: "https://console.example.com/callback", Enabled: true,
	}))
	_, err = svc.SSOPasswordLogin(ctx, &authv1.SSOPasswordLoginRequest{
		ProviderId: "oidc", Username: "", Password: "b",
	})
	assert.EqualValues(t, apierrors.CodeSSOAuthFailed, apierrors.CodeOf(err))
}

func TestSSOPasswordLoginWrongCredentials(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()
	idp := fakePasswordOIDCServer(t, "sub-1", "alice", "alice@x.com", []string{"admin"})
	seedPasswordProvider(t, svc, idp.URL, "realm_access.roles")

	// The fake IdP rejects a wrong password → 10024.
	_, err := svc.SSOPasswordLogin(ctx, &authv1.SSOPasswordLoginRequest{
		ProviderId: "keycloak", Username: "alice", Password: "wrong",
	})
	assert.EqualValues(t, apierrors.CodeSSOAuthFailed, apierrors.CodeOf(err))
}

func TestSSOPasswordLoginJITDisabledNoBinding(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()
	idp := fakePasswordOIDCServer(t, "sub-1", "alice", "alice@x.com", []string{"admin"})
	prov := &SSOProvider{
		ID: "keycloak", Type: ProviderTypeOIDC, DisplayName: "Keycloak",
		Issuer: idp.URL, ClientID: "go-taas-console", ClientSecret: "secret",
		RedirectURI:        "http://localhost:9091/api/v1/auth/sso/*",
		Enabled:            true,
		AllowAutoProvision: false,
		AttributeMapping:   `{"username":"preferred_username","email":"email","role":"realm_access.roles"}`,
	}
	require.NoError(t, svc.ssoRepo.CreateProvider(context.Background(), prov))

	// JIT disabled and no binding → 10025.
	_, err := svc.SSOPasswordLogin(ctx, &authv1.SSOPasswordLoginRequest{
		ProviderId: "keycloak", Username: "alice", Password: "pw",
	})
	assert.EqualValues(t, apierrors.CodeSSONoAccount, apierrors.CodeOf(err))
}

func TestAdminRolesHelper(t *testing.T) {
	svc, _ := newSSOService(t)
	// Default set.
	config.SetConfigForTest(&config.Configuration{Auth: config.AuthConfig{
		AdminRoles: []string{"platform-admin", "org-admin", "admin", "owner"},
	}})
	t.Cleanup(func() { config.SetConfigForTest(nil) })
	assert.True(t, svc.isAdminRole([]string{"admin"}))
	assert.True(t, svc.isAdminRole([]string{"developer", "owner"}))
	assert.False(t, svc.isAdminRole([]string{"developer", "user"}))
	assert.False(t, svc.isAdminRole(nil))

	// Custom set.
	config.SetConfigForTest(&config.Configuration{Auth: config.AuthConfig{
		AdminRoles: []string{"superadmin"},
	}})
	assert.True(t, svc.isAdminRole([]string{"superadmin"}))
	assert.False(t, svc.isAdminRole([]string{"admin"}))
}

func TestSwitchSurface(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()
	// Pin the default admin-role set so a prior test's custom config
	// does not leak into this one.
	config.SetConfigForTest(&config.Configuration{Auth: config.AuthConfig{
		AdminRoles: []string{"platform-admin", "org-admin", "admin", "owner"},
	}})
	t.Cleanup(func() { config.SetConfigForTest(nil) })

	// Seed a user-realm session with an admin role.
	sess := &Session{
		SessionID: "sess-user", UserID: "user-1", Username: "admin",
		Roles: []string{"admin"}, AccessibleOrgs: []string{"org-fvt"},
		ActiveOrg: "org-fvt", ExpiresAt: time.Now().Add(time.Hour).Unix(),
		CreatedAt: time.Now().Unix(), Realm: RealmUser,
	}
	require.NoError(t, svc.CreateSessionForTest(ctx, sess, "token"))
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer sess-user"))

	// switch-to-admin mints an admin-realm session.
	resp, err := svc.SwitchSurface(ctx, &authv1.SwitchSurfaceRequest{})
	require.NoError(t, err)
	assert.Equal(t, RealmAdmin, resp.GetRealm())
	assert.NotEmpty(t, resp.GetSessionToken())
	adminSess, err := svc.sessionStore.Get(ctx, resp.GetSessionToken())
	require.NoError(t, err)
	assert.Equal(t, RealmAdmin, adminSess.Realm)
	assert.Equal(t, "user-1", adminSess.UserID)

	// The source session is left intact.
	_, err = svc.sessionStore.Get(ctx, "sess-user")
	require.NoError(t, err)
}

func TestSwitchSurfaceForbidden(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	// Seed a user-realm session WITHOUT an admin role.
	sess := &Session{
		SessionID: "sess-user", UserID: "user-1", Username: "alice",
		Roles: []string{"developer"}, AccessibleOrgs: []string{"org-fvt"},
		ActiveOrg: "org-fvt", ExpiresAt: time.Now().Add(time.Hour).Unix(),
		CreatedAt: time.Now().Unix(), Realm: RealmUser,
	}
	require.NoError(t, svc.CreateSessionForTest(ctx, sess, "token"))
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer sess-user"))

	// switch-to-admin without an admin role → 10036.
	_, err := svc.SwitchSurface(ctx, &authv1.SwitchSurfaceRequest{})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestSwitchSurfaceNoSession(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()
	// No session → 10027.
	_, err := svc.SwitchSurface(ctx, &authv1.SwitchSurfaceRequest{})
	assert.EqualValues(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))
}

func TestSwitchSurfaceAdmin(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()
	config.SetConfigForTest(&config.Configuration{Auth: config.AuthConfig{
		AdminRoles: []string{"platform-admin", "org-admin", "admin", "owner"},
	}})
	t.Cleanup(func() { config.SetConfigForTest(nil) })

	// Seed an admin-realm session.
	sess := &Session{
		SessionID: "sess-admin", UserID: "user-1", Username: "admin",
		Roles: []string{"admin"}, AccessibleOrgs: []string{"org-fvt"},
		ActiveOrg: "org-fvt", ExpiresAt: time.Now().Add(time.Hour).Unix(),
		CreatedAt: time.Now().Unix(), Realm: RealmAdmin,
	}
	require.NoError(t, svc.CreateSessionForTest(ctx, sess, "token"))
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer sess-admin"))

	// switch-to-user mints a user-realm session (no role check).
	resp, err := svc.SwitchSurfaceAdmin(ctx, &authv1.SwitchSurfaceRequest{})
	require.NoError(t, err)
	assert.Equal(t, RealmUser, resp.GetRealm())
	assert.NotEmpty(t, resp.GetSessionToken())
	userSess, err := svc.sessionStore.Get(ctx, resp.GetSessionToken())
	require.NoError(t, err)
	assert.Equal(t, RealmUser, userSess.Realm)
}