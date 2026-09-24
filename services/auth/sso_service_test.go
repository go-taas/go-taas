package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/tenancy"
)

// newSSOService wires a Service with the SSO repository, a fake Redis
// session store, and the tenancy guard.
func newSSOService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&APIKey{}, &SSOProvider{}, &IdentityBinding{}, &User{}))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	srv := newFakeRedisServer(t)
	client := redis.NewClient(&redis.Options{Addr: srv.addr})
	t.Cleanup(func() { _ = client.Close() })

	svc := NewForFVT(db)
	svc.SetSessionStore(NewSessionStore(client, time.Hour))
	svc.SetOrgGuard(tenancy.NewOrgGuard(db))
	return svc, db
}

func TestServiceCreateSSOProvider(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	// Valid OIDC provider.
	resp, err := svc.CreateSSOProvider(ctx, &authv1.CreateSSOProviderRequest{
		Provider: &authv1.SSOProvider{
			ProviderId: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta",
			Issuer: "https://idp.example.com", ClientId: "c1", ClientSecret: "secret",
			RedirectUri: "https://console.example.com/callback",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "okta", resp.GetProvider().GetProviderId())
	assert.Equal(t, "••••", resp.GetProvider().GetClientSecret(), "secret masked")

	// Duplicate → 10020.
	_, err = svc.CreateSSOProvider(ctx, &authv1.CreateSSOProviderRequest{
		Provider: &authv1.SSOProvider{
			ProviderId: "okta", Type: ProviderTypeOIDC, DisplayName: "Dup",
			Issuer: "https://idp.example.com", ClientId: "c1", RedirectUri: "https://console.example.com/callback",
		},
	})
	assert.EqualValues(t, apierrors.CodeSSOProviderExists, apierrors.CodeOf(err))

	// Invalid id → 10028.
	_, err = svc.CreateSSOProvider(ctx, &authv1.CreateSSOProviderRequest{
		Provider: &authv1.SSOProvider{ProviderId: "BAD ID", Type: ProviderTypeOIDC, DisplayName: "X"},
	})
	assert.EqualValues(t, apierrors.CodeSSOProviderInvalid, apierrors.CodeOf(err))

	// Invalid type → 10028.
	_, err = svc.CreateSSOProvider(ctx, &authv1.CreateSSOProviderRequest{
		Provider: &authv1.SSOProvider{ProviderId: "wechat", Type: "wechat", DisplayName: "X"},
	})
	assert.EqualValues(t, apierrors.CodeSSOProviderInvalid, apierrors.CodeOf(err))

	// Missing OIDC params → 10028.
	_, err = svc.CreateSSOProvider(ctx, &authv1.CreateSSOProviderRequest{
		Provider: &authv1.SSOProvider{ProviderId: "bad-oidc", Type: ProviderTypeOIDC, DisplayName: "X"},
	})
	assert.EqualValues(t, apierrors.CodeSSOProviderInvalid, apierrors.CodeOf(err))
}

func TestServiceListGetUpdateProvider(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta", ClientSecret: "secret",
		Issuer: "https://idp.example.com", ClientID: "c1", RedirectURI: "https://console.example.com/callback",
	}))
	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: "github", Type: ProviderTypeOIDC, DisplayName: "GitHub",
		Issuer: "https://idp.example.com", ClientID: "c2", RedirectURI: "https://console.example.com/callback",
	}))

	// List.
	resp, err := svc.ListSSOProviders(ctx, &authv1.ListSSOProvidersRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 10},
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetProviders(), 2)
	assert.EqualValues(t, 2, resp.GetPageMeta().GetTotal())
	// Secret masked.
	for _, p := range resp.GetProviders() {
		if p.GetProviderId() == "okta" {
			assert.Equal(t, "••••", p.GetClientSecret())
		}
	}

	// Get.
	got, err := svc.GetSSOProvider(ctx, &authv1.GetSSOProviderRequest{ProviderId: "okta"})
	require.NoError(t, err)
	assert.Equal(t, "Okta", got.GetProvider().GetDisplayName())

	// Get unknown → 10021.
	_, err = svc.GetSSOProvider(ctx, &authv1.GetSSOProviderRequest{ProviderId: "missing"})
	assert.EqualValues(t, apierrors.CodeSSOProviderNotFound, apierrors.CodeOf(err))

	// Update.
	upd, err := svc.UpdateSSOProvider(ctx, &authv1.UpdateSSOProviderRequest{
		ProviderId: "okta",
		Provider: &authv1.SSOProvider{
			DisplayName: "Okta2", AllowAutoProvision: false,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "Okta2", upd.GetProvider().GetDisplayName())
	assert.False(t, upd.GetProvider().GetAllowAutoProvision())

	// Update unknown → 10021.
	_, err = svc.UpdateSSOProvider(ctx, &authv1.UpdateSSOProviderRequest{
		ProviderId: "missing",
		Provider:   &authv1.SSOProvider{DisplayName: "X"},
	})
	assert.EqualValues(t, apierrors.CodeSSOProviderNotFound, apierrors.CodeOf(err))
}

func TestServiceEnableDisableDeleteProvider(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta"}))

	// Enable.
	en, err := svc.EnableSSOProvider(ctx, &authv1.EnableSSOProviderRequest{ProviderId: "okta"})
	require.NoError(t, err)
	assert.True(t, en.GetProvider().GetEnabled())

	// Disable.
	dis, err := svc.DisableSSOProvider(ctx, &authv1.DisableSSOProviderRequest{ProviderId: "okta"})
	require.NoError(t, err)
	assert.False(t, dis.GetProvider().GetEnabled())

	// Enable unknown → 10021.
	_, err = svc.EnableSSOProvider(ctx, &authv1.EnableSSOProviderRequest{ProviderId: "missing"})
	assert.EqualValues(t, apierrors.CodeSSOProviderNotFound, apierrors.CodeOf(err))

	// Delete with no bindings succeeds.
	_, err = svc.DeleteSSOProvider(ctx, &authv1.DeleteSSOProviderRequest{ProviderId: "okta"})
	require.NoError(t, err)

	// Delete unknown → 10021.
	_, err = svc.DeleteSSOProvider(ctx, &authv1.DeleteSSOProviderRequest{ProviderId: "missing"})
	assert.EqualValues(t, apierrors.CodeSSOProviderNotFound, apierrors.CodeOf(err))

	// Delete blocked with bindings → 10026.
	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{ID: "okta2", Type: ProviderTypeOIDC, DisplayName: "Okta2"}))
	require.NoError(t, svc.ssoRepo.CreateUser(ctx, &User{ID: "user-1", Username: "alice"}))
	require.NoError(t, svc.ssoRepo.CreateBinding(ctx, &IdentityBinding{ID: "b-1", ProviderID: "okta2", ExternalSubject: "s1", UserID: "user-1"}))
	_, err = svc.DeleteSSOProvider(ctx, &authv1.DeleteSSOProviderRequest{ProviderId: "okta2"})
	assert.EqualValues(t, apierrors.CodeIdentityBindingExists, apierrors.CodeOf(err))
}

func TestServiceSSOAuthorize(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta", Enabled: true,
		Issuer: "https://idp.example.com", ClientID: "c1", RedirectURI: "https://console.example.com/callback",
	}))
	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: "off", Type: ProviderTypeOIDC, DisplayName: "Off", Enabled: false,
	}))

	// Enabled provider returns a redirect URL.
	resp, err := svc.SSOAuthorize(ctx, &authv1.SSOAuthorizeRequest{ProviderId: "okta"})
	require.NoError(t, err)
	assert.Contains(t, resp.GetRedirectUrl(), "https://idp.example.com/authorize")

	// Disabled provider → 10022.
	_, err = svc.SSOAuthorize(ctx, &authv1.SSOAuthorizeRequest{ProviderId: "off"})
	assert.EqualValues(t, apierrors.CodeSSOProviderDisabled, apierrors.CodeOf(err))

	// Unknown → 10021.
	_, err = svc.SSOAuthorize(ctx, &authv1.SSOAuthorizeRequest{ProviderId: "missing"})
	assert.EqualValues(t, apierrors.CodeSSOProviderNotFound, apierrors.CodeOf(err))
}

func TestServiceSSOCallbackJIT(t *testing.T) {
	svc, db := newSSOService(t)
	ctx := context.Background()

	// Seed an org for the session's accessible orgs.
	require.NoError(t, db.Create(&tenancy.Organization{ID: "org-a", DisplayName: "Org A", State: tenancy.StateActive}).Error)

	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta", Enabled: true,
		Issuer: "https://idp.example.com", ClientID: "c1", ClientSecret: "secret",
		RedirectURI: "https://console.example.com/callback",
		DefaultOrg:  "org-a",
		AttributeMapping: `{"username":"preferred_username","email":"email","org":"groups","role":"groups"}`,
	}))

	// Build a valid state and a fake OIDC token.
	state := newState()
	signedState := state + "." + signState(state)
	// The callback exchanges the code at the token endpoint; use a fake
	// IdP server.
	srv := fakeOIDCServer(t, "sub-1", "alice", "alice@x.com", []string{"org-a"})
	_, err := svc.ssoRepo.UpdateProvider(ctx, "okta", &SSOProvider{
		Issuer: srv.URL, ClientID: "c1", ClientSecret: "secret",
		RedirectURI: "https://console.example.com/callback",
		DefaultOrg:  "org-a",
		AllowAutoProvision: true,
		AttributeMapping: `{"username":"preferred_username","email":"email","org":"groups","role":"groups"}`,
	})
	require.NoError(t, err)

	// First login: JIT provisions a user + binding.
	resp, err := svc.SSOCallback(ctx, &authv1.SSOCallbackRequest{
		ProviderId: "okta", Code: "code-1", State: signedState,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetSessionToken())
	assert.NotEmpty(t, resp.GetAccessToken())

	// The session is resolvable.
	sess, err := svc.sessionStore.Get(ctx, resp.GetSessionToken())
	require.NoError(t, err)
	assert.Equal(t, "alice", sess.Username)
	assert.Equal(t, "org-a", sess.ActiveOrg)
	assert.Equal(t, []string{"org-a"}, sess.AccessibleOrgs)
	assert.Equal(t, []string{"org-a"}, sess.Roles)

	// Second login reuses the binding (no duplicate user).
	resp2, err := svc.SSOCallback(ctx, &authv1.SSOCallbackRequest{
		ProviderId: "okta", Code: "code-2", State: signedState,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp2.GetSessionToken())
	var userCount int64
	require.NoError(t, db.Model(&User{}).Count(&userCount).Error)
	assert.EqualValues(t, 1, userCount, "no duplicate user on re-login")
}

func TestServiceSSOCallbackNoAccount(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta", Enabled: true,
		Issuer: "https://idp.example.com", ClientID: "c1", ClientSecret: "secret",
		RedirectURI: "https://console.example.com/callback",
		AllowAutoProvision: false, // JIT disabled
	}))

	state := newState()
	signedState := state + "." + signState(state)
	srv := fakeOIDCServer(t, "sub-1", "alice", "alice@x.com", nil)
	_, err := svc.ssoRepo.UpdateProvider(ctx, "okta", &SSOProvider{
		Issuer: srv.URL, ClientID: "c1", ClientSecret: "secret",
		RedirectURI: "https://console.example.com/callback",
		AllowAutoProvision: false,
	})
	require.NoError(t, err)

	// JIT disabled + no binding → 10025.
	_, err = svc.SSOCallback(ctx, &authv1.SSOCallbackRequest{
		ProviderId: "okta", Code: "code-1", State: signedState,
	})
	assert.EqualValues(t, apierrors.CodeSSONoAccount, apierrors.CodeOf(err))
}

func TestServiceIdentityBindingCRUD(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta"}))
	require.NoError(t, svc.ssoRepo.CreateUser(ctx, &User{ID: "user-1", Username: "alice"}))

	// Create.
	resp, err := svc.CreateIdentityBinding(ctx, &authv1.CreateIdentityBindingRequest{
		ProviderId: "okta", ExternalSubject: "iss:sub-1", UserId: "user-1",
	})
	require.NoError(t, err)
	assert.Equal(t, "okta", resp.GetBinding().GetProviderId())

	// Duplicate → 10026.
	_, err = svc.CreateIdentityBinding(ctx, &authv1.CreateIdentityBindingRequest{
		ProviderId: "okta", ExternalSubject: "iss:sub-1", UserId: "user-1",
	})
	assert.EqualValues(t, apierrors.CodeIdentityBindingExists, apierrors.CodeOf(err))

	// Unknown provider → 10021.
	_, err = svc.CreateIdentityBinding(ctx, &authv1.CreateIdentityBindingRequest{
		ProviderId: "missing", ExternalSubject: "s", UserId: "user-1",
	})
	assert.EqualValues(t, apierrors.CodeSSOProviderNotFound, apierrors.CodeOf(err))

	// Unknown user → 10005.
	_, err = svc.CreateIdentityBinding(ctx, &authv1.CreateIdentityBindingRequest{
		ProviderId: "okta", ExternalSubject: "s2", UserId: "user-missing",
	})
	assert.EqualValues(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))

	// List.
	list, err := svc.ListIdentityBindings(ctx, &authv1.ListIdentityBindingsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 10},
	})
	require.NoError(t, err)
	assert.Len(t, list.GetBindings(), 1)

	// Delete (idempotent).
	_, err = svc.DeleteIdentityBinding(ctx, &authv1.DeleteIdentityBindingRequest{BindingId: resp.GetBinding().GetBindingId()})
	require.NoError(t, err)
	_, err = svc.DeleteIdentityBinding(ctx, &authv1.DeleteIdentityBindingRequest{BindingId: resp.GetBinding().GetBindingId()})
	require.NoError(t, err)
}

func TestServiceSessionFlow(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	// Create a session directly.
	sess := &Session{
		SessionID:      "sess-1",
		UserID:         "user-1",
		Username:       "alice",
		Roles:          []string{"admin"},
		AccessibleOrgs: []string{"org-a", "org-b"},
		ActiveOrg:      "org-a",
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
		CreatedAt:      time.Now().Unix(),
	}
	require.NoError(t, svc.sessionStore.Create(ctx, sess, "token"))

	sessCtx := metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer sess-1"))

	// GetSession.
	got, err := svc.GetSession(sessCtx, &authv1.GetSessionRequest{})
	require.NoError(t, err)
	assert.Equal(t, "user-1", got.GetUserId())
	assert.Equal(t, "org-a", got.GetActiveOrg())
	assert.Equal(t, []string{"org-a", "org-b"}, got.GetAccessibleOrgs())

	// UpdateSessionOrg.
	upd, err := svc.UpdateSessionOrg(sessCtx, &authv1.UpdateSessionOrgRequest{OrganizationId: "org-b"})
	require.NoError(t, err)
	assert.Equal(t, "org-b", upd.GetActiveOrg())

	// UpdateSessionOrg to an inaccessible org → 10005.
	_, err = svc.UpdateSessionOrg(sessCtx, &authv1.UpdateSessionOrgRequest{OrganizationId: "org-x"})
	assert.EqualValues(t, apierrors.CodeOrganizationNotFound, apierrors.CodeOf(err))

	// Logout.
	_, err = svc.Logout(sessCtx, &authv1.LogoutRequest{})
	require.NoError(t, err)

	// Subsequent GetSession → 10027.
	_, err = svc.GetSession(sessCtx, &authv1.GetSessionRequest{})
	assert.EqualValues(t, apierrors.CodeSessionInvalid, apierrors.CodeOf(err))
}

func TestServiceResolveOrgContext(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	// No session, header present → header org.
	headerCtx := metadata.NewIncomingContext(ctx, metadata.Pairs("x-organization-id", "org-a"))
	org, err := svc.resolveOrgContext(headerCtx)
	require.NoError(t, err)
	assert.Equal(t, "org-a", org)

	// Session present → session active org wins.
	sess := &Session{
		SessionID:      "sess-1",
		UserID:         "user-1",
		Username:       "alice",
		AccessibleOrgs: []string{"org-b"},
		ActiveOrg:      "org-b",
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
		CreatedAt:      time.Now().Unix(),
	}
	require.NoError(t, svc.sessionStore.Create(ctx, sess, "token"))
	sessCtx := metadata.NewIncomingContext(ctx, metadata.Pairs(
		"authorization", "Bearer sess-1",
		"x-organization-id", "org-a",
	))
	org, err = svc.resolveOrgContext(sessCtx)
	require.NoError(t, err)
	assert.Equal(t, "org-b", org, "session wins over header")

	// No session, no header → 10001.
	_, err = svc.resolveOrgContext(context.Background())
	assert.EqualValues(t, apierrors.CodeUnauthorized, apierrors.CodeOf(err))
}

func TestServiceSessionTTL(t *testing.T) {
	svc, _ := newSSOService(t)
	// Default 24h when config is nil.
	assert.Equal(t, 24*time.Hour, svc.sessionTTL())

	cfg := &config.Configuration{}
	cfg.Auth.SessionTTL = 2 * time.Hour
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
	assert.Equal(t, 2*time.Hour, svc.sessionTTL())
}

func TestServiceListSSOProvidersFilters(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta",
		Issuer: "https://idp.example.com", ClientID: "c1", RedirectURI: "https://console.example.com/callback",
	}))
	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: "ldap1", Type: ProviderTypeLDAP, DisplayName: "LDAP",
		Host: "ldap.example.com", BaseDN: "dc=example,dc=com",
	}))

	// Type filter.
	resp, err := svc.ListSSOProviders(ctx, &authv1.ListSSOProvidersRequest{Type: ProviderTypeLDAP})
	require.NoError(t, err)
	assert.Len(t, resp.GetProviders(), 1)
	assert.Equal(t, "ldap1", resp.GetProviders()[0].GetProviderId())

	// Invalid type → 10028.
	_, err = svc.ListSSOProviders(ctx, &authv1.ListSSOProvidersRequest{Type: "bogus"})
	assert.EqualValues(t, apierrors.CodeSSOProviderInvalid, apierrors.CodeOf(err))
}

func TestServiceSSOAuthorizeLDAP(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: "ldap1", Type: ProviderTypeLDAP, DisplayName: "LDAP", Enabled: true,
		Host: "ldap.example.com", BaseDN: "dc=example,dc=com",
	}))

	resp, err := svc.SSOAuthorize(ctx, &authv1.SSOAuthorizeRequest{ProviderId: "ldap1"})
	require.NoError(t, err)
	assert.Equal(t, "ldap", resp.GetBindForm())
}

func TestServiceSSOCallbackBadState(t *testing.T) {
	svc, _ := newSSOService(t)
	ctx := context.Background()

	require.NoError(t, svc.ssoRepo.CreateProvider(ctx, &SSOProvider{
		ID: "okta", Type: ProviderTypeOIDC, DisplayName: "Okta", Enabled: true,
		Issuer: "https://idp.example.com", ClientID: "c1", ClientSecret: "secret",
		RedirectURI: "https://console.example.com/callback",
	}))

	// Bad state → 10023.
	_, err := svc.SSOCallback(ctx, &authv1.SSOCallbackRequest{
		ProviderId: "okta", Code: "code-1", State: "bad-state",
	})
	assert.EqualValues(t, apierrors.CodeSSOInvalidState, apierrors.CodeOf(err))
}

func TestServiceSSORepositoryLazyWiring(t *testing.T) {
	// A service with no wired repo resolves it lazily from components.
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&SSOProvider{}, &IdentityBinding{}, &User{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	svc := &Service{components: &fakeComponents{db: db}}
	repo, err := svc.ssoRepository()
	require.NoError(t, err)
	assert.NotNil(t, repo)
	assert.Same(t, repo, svc.ssoRepo)

	// A service with no components fails.
	empty := &Service{}
	_, err = empty.ssoRepository()
	require.Error(t, err)
	assert.EqualValues(t, apierrors.CodeInternal, apierrors.CodeOf(err))
}

// fakeComponents is a minimal server.Components exposing a GORM handle.
type fakeComponents struct {
	db *gorm.DB
}

func (f *fakeComponents) DB() server.DBComponent { return &fakeDBComponent{db: f.db} }
func (f *fakeComponents) Redis() server.RedisComponent { return nil }
func (f *fakeComponents) MQ() server.MQComponent        { return nil }

type fakeDBComponent struct{ db *gorm.DB }

func (f *fakeDBComponent) GormDB() any { return f.db }