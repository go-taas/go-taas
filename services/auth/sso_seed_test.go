package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/services/tenancy"
)

// TestSeedComposeProviderAndAdmin verifies the compose seeding
// (feature-22 AD6/AD10): it creates the Keycloak provider, the admin
// user, and the admin binding idempotently.
func TestSeedComposeProviderAndAdmin(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, svc.EnsureComposeSeed(ctx))

	// The provider exists with the compose params.
	prov, err := svc.ssoRepo.FindProvider(ctx, seedKeycloakProviderID)
	require.NoError(t, err)
	assert.Equal(t, ProviderTypeOIDC, prov.Type)
	assert.Equal(t, seedKeycloakIssuer, prov.Issuer)
	assert.Equal(t, seedKeycloakClientID, prov.ClientID)
	assert.True(t, prov.Enabled)
	assert.True(t, prov.AllowAutoProvision)

	// The admin user exists.
	adminUser, err := svc.ssoRepo.FindUserByUsername(ctx, seedAdminUsername)
	require.NoError(t, err)
	assert.Equal(t, "admin@example.com", adminUser.Email)

	// The admin user has an org_members row with the admin role so the
	// session realm derives as admin when the membership resolver is
	// wired (feature-22 AD8).
	var memberCount int64
	require.NoError(t, db.Model(&tenancy.OrgMember{}).
		Where("user_id = ? AND role = ?", adminUser.ID, tenancy.RoleAdmin).
		Count(&memberCount).Error)
	assert.EqualValues(t, 1, memberCount, "admin user has an admin org membership")

	// The seed is idempotent: re-running does not duplicate rows.
	require.NoError(t, svc.EnsureComposeSeed(ctx))
	var providerCount, userCount, memberCount2 int64
	require.NoError(t, db.Model(&SSOProvider{}).Count(&providerCount).Error)
	require.NoError(t, db.Model(&User{}).Count(&userCount).Error)
	require.NoError(t, db.Model(&tenancy.OrgMember{}).Count(&memberCount2).Error)
	assert.EqualValues(t, 1, providerCount, "no duplicate provider on re-seed")
	assert.EqualValues(t, 1, userCount, "no duplicate admin user on re-seed")
	assert.EqualValues(t, 2, memberCount2, "no duplicate membership on re-seed (admin org + platform)")
}

// fakeAdminOIDCServer is an in-process OIDC IdP that answers the
// resource-owner password grant for admin/admin with a fixed subject.
func fakeAdminOIDCServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("grant_type") != "password" ||
			r.FormValue("username") != "admin" || r.FormValue("password") != "admin" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		claims := map[string]any{
			"sub":                "admin-subject",
			"preferred_username": "admin",
			"email":              "admin@x.com",
			"realm_access":       map[string]any{"roles": []string{"admin"}},
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

// TestSeedComposeProviderAndAdminBinding verifies the admin binding is
// created when the Keycloak admin subject can be resolved (feature-22
// AD10). The fake IdP returns the admin subject via the password grant.
func TestSeedComposeProviderAndAdminBinding(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	// A fake IdP that answers the password grant for admin/admin with a
	// subject we can resolve.
	idp := fakeAdminOIDCServer(t)
	// Point the seeded provider at the fake IdP by pre-creating it.
	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: seedKeycloakProviderID, Type: ProviderTypeOIDC, DisplayName: "Keycloak",
		Issuer: idp.URL, ClientID: seedKeycloakClientID, ClientSecret: seedKeycloakClientSecret,
		RedirectURI: seedKeycloakRedirectURI, Enabled: true, AllowAutoProvision: true,
		AttributeMapping: seedKeycloakAttributeMapping,
	}))

	require.NoError(t, svc.EnsureComposeSeed(ctx))

	// The admin user exists.
	adminUser, err := svc.ssoRepo.FindUserByUsername(ctx, seedAdminUsername)
	require.NoError(t, err)

	// The binding exists for the resolved subject.
	binding, err := svc.ssoRepo.FindBindingBySubject(ctx, seedKeycloakProviderID, idp.URL+":admin-subject")
	require.NoError(t, err)
	assert.Equal(t, adminUser.ID, binding.UserID)

	// Idempotent re-seed does not duplicate the binding.
	require.NoError(t, svc.EnsureComposeSeed(ctx))
	var bindingCount int64
	require.NoError(t, svc.ssoRepo.db.DB(ctx).Model(&IdentityBinding{}).Count(&bindingCount).Error)
	assert.EqualValues(t, 1, bindingCount, "no duplicate binding on re-seed")
}

// TestSubjectFromExternal verifies the subject extraction helper.
func TestSubjectFromExternal(t *testing.T) {
	assert.Equal(t, "sub-1", subjectFromExternal("http://issuer:sub-1", "http://issuer"))
	assert.Equal(t, "", subjectFromExternal("http://other:sub-1", "http://issuer"))
	assert.Equal(t, "", subjectFromExternal("http://issuer", "http://issuer"))
}

// TestEnsureComposeSeedNoRepo verifies the injection point returns an
// error when the SSO repository is not wired (feature-22 AD6).
func TestEnsureComposeSeedNoRepo(t *testing.T) {
	svc := &Service{}
	err := svc.EnsureComposeSeed(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sso repository not wired")
}

// TestSeedComposeProviderAndAdminJITFallback verifies the seed falls
// back to JIT provisioning when the Keycloak admin subject cannot be
// resolved (feature-22 AD10): the provider and user are still seeded,
// and the missing binding is not an error.
func TestSeedComposeProviderAndAdminJITFallback(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	// Pre-create the provider pointing at an unreachable IdP so the
	// admin-subject resolution fails and the seed falls back to JIT.
	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: seedKeycloakProviderID, Type: ProviderTypeOIDC, DisplayName: "Keycloak",
		Issuer: "http://127.0.0.1:1/realms/go-taas", ClientID: seedKeycloakClientID,
		ClientSecret: seedKeycloakClientSecret, RedirectURI: seedKeycloakRedirectURI,
		Enabled: true, AllowAutoProvision: true, AttributeMapping: seedKeycloakAttributeMapping,
	}))

	require.NoError(t, svc.EnsureComposeSeed(ctx))

	// The provider and admin user are still seeded.
	prov, err := svc.ssoRepo.FindProvider(ctx, seedKeycloakProviderID)
	require.NoError(t, err)
	assert.True(t, prov.Enabled)
	_, err = svc.ssoRepo.FindUserByUsername(ctx, seedAdminUsername)
	require.NoError(t, err)

	// No binding is created (the subject could not be resolved).
	var bindingCount int64
	require.NoError(t, db.Model(&IdentityBinding{}).Count(&bindingCount).Error)
	assert.EqualValues(t, 0, bindingCount, "no binding when the subject is unresolvable")
}

// TestSeedComposeProviderAndAdminDisabled verifies the seed is a no-op
// when auth.seedSso.enabled is false (feature-22 AD6): no provider, user,
// or binding is created.
func TestSeedComposeProviderAndAdminDisabled(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	config.SetConfigForTest(&config.Configuration{Auth: config.AuthConfig{
		SeedSso: config.SeedSsoConfig{Enabled: false},
	}})
	t.Cleanup(func() { config.SetConfigForTest(nil) })

	require.NoError(t, svc.EnsureComposeSeed(ctx))

	var providerCount, userCount, bindingCount int64
	require.NoError(t, db.Model(&SSOProvider{}).Count(&providerCount).Error)
	require.NoError(t, db.Model(&User{}).Count(&userCount).Error)
	require.NoError(t, db.Model(&IdentityBinding{}).Count(&bindingCount).Error)
	assert.EqualValues(t, 0, providerCount, "no provider when seed disabled")
	assert.EqualValues(t, 0, userCount, "no user when seed disabled")
	assert.EqualValues(t, 0, bindingCount, "no binding when seed disabled")
}