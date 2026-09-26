package fvt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/tenancy"
)

// ssoEnv is the in-process stack for the SSO feature: the auth service
// with the SSO repository, a fake Redis session store, a fake IdP, and
// the tenancy guard, over one gRPC server with the gateway mux.
type ssoEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server
	svc   *auth.Service
}

func newSSOEnv(t *testing.T) *ssoEnv {
	t.Helper()

	// A file-backed temp database avoids sqlite shared-cache locking.
	dbPath := filepath.Join(t.TempDir(), "sso.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, auth.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	// Seed orgs for the session's accessible orgs.
	for _, orgID := range []string{"org-fvt", "org-a", "org-b"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Fake Redis for the session store.
	redisSrv := newFakeRedisServer(t)
	redisClient := redis.NewClient(&redis.Options{Addr: redisSrv.addr})
	t.Cleanup(func() { _ = redisClient.Close() })

	// Real gRPC server with the production interceptor chain.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	svc := auth.NewForFVT(db)
	svc.SetOrgGuard(tenancy.NewOrgGuard(db))
	svc.SetSessionStore(auth.NewSessionStore(redisClient, time.Hour))
	authv1.RegisterAuthServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	// Gateway mux in front of the gRPC server.
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, authv1.RegisterAuthServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &ssoEnv{db: db, gwSrv: gwSrv, svc: svc}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *ssoEnv) call(t *testing.T, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, e.gwSrv.URL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestFVTSSOProviderLifecycle walks AC1-AC4: provider CRUD, enable/
// disable, delete gated by bindings.
func TestFVTSSOProviderLifecycle(t *testing.T) {
	env := newSSOEnv(t)

	// AC1: create an OIDC provider.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers", map[string]any{
		"provider": map[string]any{
			"providerId": "okta", "type": "oidc", "displayName": "Okta",
			"issuer": "https://idp.example.com", "clientId": "c1", "clientSecret": "secret",
			"redirectUri": "https://console.example.com/callback",
		},
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	prov, _ := body["provider"].(map[string]any)
	assert.Equal(t, "okta", prov["providerId"])
	assert.Equal(t, "••••", prov["clientSecret"], "secret masked")

	// Duplicate → 10020.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers", map[string]any{
		"provider": map[string]any{
			"providerId": "okta", "type": "oidc", "displayName": "Dup",
			"issuer": "https://idp.example.com", "clientId": "c1", "redirectUri": "https://console.example.com/callback",
		},
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10020), body["code"])

	// AC2: enable/disable idempotent.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers/okta:enable", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, true, body["provider"].(map[string]any)["enabled"])

	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers/okta:disable", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, false, body["provider"].(map[string]any)["enabled"])

	// AC3: update.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/auth/sso/providers/okta", map[string]any{
		"provider": map[string]any{"displayName": "Okta2"},
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "Okta2", body["provider"].(map[string]any)["displayName"])

	// AC4: delete with no bindings succeeds.
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/auth/sso/providers/okta", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
}

// TestFVTSSOAuthorizeAndCallback walks AC5-AC7: the SSO login flow with
// a fake IdP, JIT provisioning, and binding reuse.
func TestFVTSSOAuthorizeAndCallback(t *testing.T) {
	env := newSSOEnv(t)

	// A fake OIDC IdP.
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		claims := map[string]any{
			"sub": "sub-1", "preferred_username": "alice", "email": "alice@x.com",
			"groups": []string{"org-fvt"},
		}
		payload, _ := json.Marshal(claims)
		header := base64RawURL([]byte(`{"alg":"none"}`))
		body := base64RawURL(payload)
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": header + "." + body + ".sig"})
	}))
	t.Cleanup(idp.Close)

	// Create an enabled OIDC provider pointing at the fake IdP.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers", map[string]any{
		"provider": map[string]any{
			"providerId": "okta", "type": "oidc", "displayName": "Okta",
			"issuer": idp.URL, "clientId": "c1", "clientSecret": "secret",
			"redirectUri":      "https://console.example.com/callback",
			"defaultOrg":       "org-fvt",
			"attributeMapping": `{"username":"preferred_username","email":"email","org":"groups","role":"groups"}`,
		},
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers/okta:enable", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// AC5: authorize returns a redirect URL.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/sso/okta/authorize", nil, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	redirectURL, _ := body["redirectUrl"].(string)
	assert.Contains(t, redirectURL, idp.URL+"/authorize")

	// Extract the state from the redirect URL.
	state := extractState(t, redirectURL)

	// AC5: callback with a valid code and state issues a session.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/sso/okta/callback?code=code-1&state="+state, nil, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	sessionToken, _ := body["sessionToken"].(string)
	require.NotEmpty(t, sessionToken)

	// AC6: the session is resolvable and carries the mapped org/role.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/session", nil, map[string]string{"Authorization": "Bearer " + sessionToken})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "alice", body["username"])
	assert.Equal(t, "org-fvt", body["activeOrg"])
	roles, _ := body["roles"].([]any)
	assert.Contains(t, roles, "org-fvt")

	// AC6: a second login reuses the binding (no duplicate user).
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/sso/okta/callback?code=code-2&state="+state, nil, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	var userCount int64
	require.NoError(t, env.db.Model(&auth.User{}).Count(&userCount).Error)
	assert.EqualValues(t, 1, userCount, "no duplicate user on re-login")

	// AC5: bad state → 10023.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/sso/okta/callback?code=code-3&state=bad-state", nil, nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10023), body["code"])
}

// TestFVTSSOSessionAndBindings walks AC8, AC11-AC13: binding CRUD,
// session get/logout, org switch, and attribute-to-role mapping.
func TestFVTSSOSessionAndBindings(t *testing.T) {
	env := newSSOEnv(t)

	// Create a provider and a user for binding tests.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/auth/sso/providers", map[string]any{
		"provider": map[string]any{
			"providerId": "okta", "type": "oidc", "displayName": "Okta",
			"issuer": "https://idp.example.com", "clientId": "c1", "clientSecret": "secret",
			"redirectUri": "https://console.example.com/callback",
		},
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// Create a user directly.
	user := &auth.User{ID: "user-1", Username: "alice"}
	require.NoError(t, env.db.Create(user).Error)

	// AC8: create a binding.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/identity-bindings", map[string]any{
		"providerId": "okta", "externalSubject": "iss:sub-1", "userId": "user-1",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	bindingID, _ := body["binding"].(map[string]any)["bindingId"].(string)
	require.NotEmpty(t, bindingID)

	// Duplicate → 10026.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/identity-bindings", map[string]any{
		"providerId": "okta", "externalSubject": "iss:sub-1", "userId": "user-1",
	}, map[string]string{"X-Organization-Id": "org-fvt"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10026), body["code"])

	// AC8: list bindings.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/identity-bindings", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	bindings, _ := body["bindings"].([]any)
	assert.Len(t, bindings, 1)

	// AC8: delete the binding (keeps the user).
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/auth/identity-bindings/"+bindingID, nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	var userCount int64
	require.NoError(t, env.db.Model(&auth.User{}).Count(&userCount).Error)
	assert.EqualValues(t, 1, userCount, "user kept after binding delete")

	// AC11-AC13: session flow via a directly-created session.
	// Create a session in the store.
	sess := &auth.Session{
		SessionID:      "sess-1",
		UserID:         "user-1",
		Username:       "alice",
		Roles:          []string{"admin"},
		AccessibleOrgs: []string{"org-fvt", "org-a"},
		ActiveOrg:      "org-fvt",
		ExpiresAt:      time.Now().Add(time.Hour).Unix(),
		CreatedAt:      time.Now().Unix(),
		Realm:          auth.RealmUser,
	}
	require.NoError(t, env.svc.CreateSessionForTest(context.Background(), sess, "token"))

	// AC11: GetSession.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/session", nil, map[string]string{"Authorization": "Bearer sess-1"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "alice", body["username"])
	assert.Equal(t, "org-fvt", body["activeOrg"])

	// AC12: UpdateSessionOrg to an accessible org.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/session/org", map[string]any{
		"organizationId": "org-a",
	}, map[string]string{"Authorization": "Bearer sess-1"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "org-a", body["activeOrg"])

	// AC12: inaccessible org → 10005.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/session/org", map[string]any{
		"organizationId": "org-x",
	}, map[string]string{"Authorization": "Bearer sess-1"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10005), body["code"])

	// AC11: Logout revokes the session.
	code, body = env.call(t, http.MethodPost, "/api/v1/auth/logout", nil, map[string]string{"Authorization": "Bearer sess-1"})
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// Subsequent GetSession → 10027.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/session", nil, map[string]string{"Authorization": "Bearer sess-1"})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10027), body["code"])
}

// extractState parses the state parameter from an authorize redirect URL.
func extractState(t *testing.T, redirectURL string) string {
	t.Helper()
	u, err := url.Parse(redirectURL)
	require.NoError(t, err)
	return u.Query().Get("state")
}

func base64RawURL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
