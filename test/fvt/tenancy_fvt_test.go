package fvt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/tenancy"
)

// tenancyEnv is the in-process stack for the multi-tenancy feature:
// the tenancy service plus the auth service (as the guard consumer
// stand-in) over one gRPC server with the gateway mux in front and a
// shared file-backed SQLite database.
type tenancyEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server

	tenancySvc *tenancy.Service
	authSvc    *auth.Service
}

func newTenancyEnv(t *testing.T) *tenancyEnv {
	t.Helper()

	// A file-backed temp database avoids sqlite shared-cache locking
	// between pooled connections.
	dbPath := filepath.Join(t.TempDir(), "tenancy.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, auth.MigrateSchemaForFVT(db))
	// The inference_services table backs the per-organization service
	// count in the organization summary.
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Real gRPC server on an ephemeral port with the production
	// interceptor chain.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	tenancySvc := tenancy.NewForFVT(db)
	authSvc := auth.NewForFVT(db)
	// The guard is wired exactly like main.go does in production.
	authSvc.SetOrgGuard(tenancy.NewOrgGuard(db))

	tenancyv1.RegisterTenancyServiceServer(grpcSrv, tenancySvc)
	authv1.RegisterAuthServiceServer(grpcSrv, authSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	// Gateway mux in front of the gRPC server.
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, tenancyv1.RegisterTenancyServiceHandler(context.Background(), mux, conn))
	require.NoError(t, authv1.RegisterAuthServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &tenancyEnv{db: db, gwSrv: gwSrv, tenancySvc: tenancySvc, authSvc: authSvc}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *tenancyEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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
	if org != "" {
		req.Header.Set("X-Organization-Id", org)
	}
	resp, err := e.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestFVTTenancyEndToEnd walks the acceptance criteria of the
// multi-tenancy architecture doc: AC1-AC5 (organization lifecycle),
// AC6-AC7 (project lifecycle), AC8-AC9 (org gating of consumers),
// AC10 (cross-org isolation), AC11 (first-boot seed).
func TestFVTTenancyEndToEnd(t *testing.T) {
	env := newTenancyEnv(t)

	// ---- AC1: create organizations ----
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/tenancy/organizations", map[string]any{
		"organizationId": "org-acme",
		"displayName":    "ACME Inc.",
		"description":    "the acme org",
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	org, _ := body["organization"].(map[string]any)
	require.NotNil(t, org)
	assert.Equal(t, "org-acme", org["organizationId"])
	assert.Equal(t, "ACME Inc.", org["displayName"])
	assert.Equal(t, "active", org["state"])
	assert.EqualValues(t, "0", fmt.Sprint(org["apiKeyCount"]))

	// Invalid ids are rejected with 10019.
	for _, badID := range []string{"", "ab", "-abc", "UPPER", "a", "x-"} {
		code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/organizations", map[string]any{
			"organizationId": badID,
			"displayName":    "Bad",
		}, "")
		assert.NotEqual(t, http.StatusOK, code, "id %q should be rejected", badID)
		assert.EqualValues(t, float64(10019), body["code"], "id %q", badID)
	}

	// Duplicate id is rejected with 10015.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/organizations", map[string]any{
		"organizationId": "org-acme",
		"displayName":    "Dup",
	}, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10015), body["code"])

	// A second organization for isolation checks.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/organizations", map[string]any{
		"organizationId": "org-globex",
		"displayName":    "Globex",
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// ---- AC2: list organizations with counts ----
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	orgs, _ := body["organizations"].([]any)
	assert.Len(t, orgs, 2)
	pageMeta, _ := body["pageMeta"].(map[string]any)
	assert.EqualValues(t, "2", fmt.Sprint(pageMeta["total"]))

	// State filter.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations?state=disabled", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	orgs, _ = body["organizations"].([]any)
	assert.Empty(t, orgs)

	// Invalid state filter is rejected with 10019.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations?state=bogus", nil, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10019), body["code"])

	// ---- AC3: get + update ----
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations/org-acme", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	org, _ = body["organization"].(map[string]any)
	assert.Equal(t, "ACME Inc.", org["displayName"])

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations/org-missing", nil, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10005), body["code"])

	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/tenancy/organizations/org-acme", map[string]any{
		"displayName": "ACME Corp.",
		"description": "renamed",
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	org, _ = body["organization"].(map[string]any)
	assert.Equal(t, "ACME Corp.", org["displayName"])
	assert.Equal(t, "renamed", org["description"])

	// ---- AC6: projects ----
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects", map[string]any{
		"organizationId": "org-acme",
		"projectId":      "proj-core",
		"displayName":    "Core Platform",
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	project, _ := body["project"].(map[string]any)
	require.NotNil(t, project)
	assert.Equal(t, "proj-core", project["projectId"])
	assert.Equal(t, "org-acme", project["organizationId"])

	// Duplicate project id is globally unique: 10016.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects", map[string]any{
		"organizationId": "org-globex",
		"projectId":      "proj-core",
		"displayName":    "Clash",
	}, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10016), body["code"])

	// Unknown organization: 10005.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects", map[string]any{
		"organizationId": "org-missing",
		"projectId":      "proj-x",
		"displayName":    "X",
	}, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10005), body["code"])

	// List projects with org filter.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/projects?organizationId=org-acme", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	projects, _ := body["projects"].([]any)
	assert.Len(t, projects, 1)

	// ---- AC4: disable/enable organization ----
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/organizations/org-acme:disable", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	org, _ = body["organization"].(map[string]any)
	assert.Equal(t, "disabled", org["state"])

	// Idempotent.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/organizations/org-acme:disable", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// ---- AC8: a disabled organization cannot accrue new API keys ----
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/api-keys", map[string]any{
		"name": "blocked key",
	}, "org-acme")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10017), body["code"])

	// Reads still work under a disabled organization.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/api-keys", nil, "org-acme")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// ---- AC7: enabling a project under a disabled org is blocked ----
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects/proj-core:disable", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects/proj-core:enable", nil, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10017), body["code"])

	// Creating a project under a disabled org is blocked too.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects", map[string]any{
		"organizationId": "org-acme",
		"projectId":      "proj-blocked",
		"displayName":    "Blocked",
	}, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10017), body["code"])

	// Re-enable the organization; the project enable now succeeds.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/organizations/org-acme:enable", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/projects/proj-core:enable", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	project, _ = body["project"].(map[string]any)
	assert.Equal(t, "active", project["state"])

	// ---- AC9/AC10: consumer gating + isolation ----
	// An API key under org-acme now succeeds (org re-enabled).
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/api-keys", map[string]any{
		"name": "acme key",
	}, "org-acme")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// An unknown organization id is rejected on reads: 10005.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/api-keys", nil, "org-missing")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10005), body["code"])

	// org-globex sees no acme keys (isolation).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/api-keys", nil, "org-globex")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	keys, _ := body["apiKeys"].([]any)
	assert.Empty(t, keys)

	// The organization list reflects the api-key count.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations/org-acme", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	org, _ = body["organization"].(map[string]any)
	assert.EqualValues(t, "1", fmt.Sprint(org["apiKeyCount"]))
	assert.EqualValues(t, "1", fmt.Sprint(org["projectCount"]))

	// ---- AC5/AC11: update project + unknown project ----
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/tenancy/projects/proj-core", map[string]any{
		"displayName": "Core Platform v2",
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	project, _ = body["project"].(map[string]any)
	assert.Equal(t, "Core Platform v2", project["displayName"])

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/projects/proj-missing", nil, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10006), body["code"])
}

// TestFVTTenancyFirstBootSeed verifies the first-boot default
// organization seed (D5): Migrate seeds org-default once, never
// re-runs, and deletions stick.
func TestFVTTenancyFirstBootSeed(t *testing.T) {
	env := newTenancyEnv(t)
	ctx := context.Background()

	// Install a config with the tenancy defaults so the seed has a
	// target id and display name.
	cfg := &config.Configuration{}
	cfg.Tenancy.DefaultOrgID = "org-default"
	cfg.Tenancy.DefaultOrgDisplayName = "Default Organization"
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	// The FVT constructor does not run Migrate; call it directly.
	require.NoError(t, env.tenancySvc.Migrate(ctx))

	code, body := env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	orgs, _ := body["organizations"].([]any)
	require.Len(t, orgs, 1)
	org, _ := orgs[0].(map[string]any)
	assert.Equal(t, "org-default", org["organizationId"])

	// Delete the seed row alongside another organization; Migrate must
	// NOT re-seed (the table is non-empty, deletions stick).
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/tenancy/organizations", map[string]any{
		"organizationId": "org-second",
		"displayName":    "Second",
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.NoError(t, env.db.Where("id = ?", "org-default").Delete(&tenancy.Organization{}).Error)
	require.NoError(t, env.tenancySvc.Migrate(ctx))

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/tenancy/organizations", nil, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	orgs, _ = body["organizations"].([]any)
	require.Len(t, orgs, 1)
	org, _ = orgs[0].(map[string]any)
	assert.Equal(t, "org-second", org["organizationId"])
}
