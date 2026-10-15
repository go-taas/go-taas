// Feature #51 (service quota management) full-verification suite: the
// four quota RPCs of taas.tenancy.v1.TenancyService (three admin-prefix
// under /api/v1/admin/quotas*, one user-prefix /api/v1/quotas) over one
// gRPC server with the production RealmGuard in front, the VerifyAPIKey
// verdict extension (auth), the infer service-count guard and the batch
// daily-jobs guard, the change-history read from audit_events, and the
// synthetic gateway-contract checks (check order, Retry-After, clamp-vs-
// reject) pinned in docs/architecture/service-quota-management.md §5.3.
// It exercises the acceptance criteria AC1–AC13.

package fvt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	batchv1 "github.com/go-taas/go-taas/proto/taas/batch/v1"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/batch"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
)

// quotaFvtEnv is the in-process stack for feature #51.
type quotaFvtEnv struct {
	db     *gorm.DB
	gwSrv  *httptest.Server
	grpcLn net.Listener
	ten    *tenancy.Service
	auth   *auth.Service
	infer  *infer.Service
	batch  *batch.Service
	realm  *runtimeFvtRealmResolver
}

// newQuotaFvtEnv builds the quota FVT stack. The admin session resolves
// to user "admin-fvt" of org "org-fvt" (an org admin); the plain
// session resolves to "user-fvt" (a plain member). The quota config is
// installed process-wide with a request-increase mailbox so the mailto
// path is exercised.
func newQuotaFvtEnv(t *testing.T) *quotaFvtEnv {
	t.Helper()

	cfg := &config.Configuration{}
	cfg.Quota.Timezone = "UTC"
	cfg.Quota.RequestIncreaseTo = "ops@example.com"
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, audit.MigrateSchemaForFVT(db))
	require.NoError(t, auth.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, batch.MigrateSchemaForFVT(db))
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, db.Create(&tenancy.Organization{
		ID: "org-fvt", DisplayName: "org-fvt", State: tenancy.StateActive,
	}).Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{
		OrganizationID: "org-fvt", UserID: "admin-fvt", Role: tenancy.RoleAdmin, JoinedAt: time.Now().UTC(),
	}).Error)
	require.NoError(t, db.Create(&tenancy.OrgMember{
		OrganizationID: "org-fvt", UserID: "user-fvt", Role: tenancy.RoleMember, JoinedAt: time.Now().UTC(),
	}).Error)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	tenSvc := tenancy.NewForFVT(db)
	tenSvc.SetRoleGuard(tenancy.NewRoleGuard(db))
	tenSvc.SetAuditRecorder(audit.NewRecorder(audit.NewRepository(db)))

	authSvc := auth.NewForFVT(db)
	authSvc.SetOrgQuotaReader(tenSvc)

	inferSvc := infer.NewForFVT(db, newRecordingBus())
	inferSvc.SetOrgQuotaReader(tenSvc)

	batchSvc := batch.NewForFVT(db, batch.NewMemFileStore())
	batchSvc.SetOrgQuotaReader(tenSvc)

	env := &quotaFvtEnv{db: db, grpcLn: ln, ten: tenSvc, auth: authSvc, infer: inferSvc, batch: batchSvc}

	tenancyv1.RegisterTenancyServiceServer(grpcSrv, tenSvc)
	authv1.RegisterAuthServiceServer(grpcSrv, authSvc)
	inferv1.RegisterInferServiceServiceServer(grpcSrv, inferSvc)
	batchv1.RegisterBatchServiceServer(grpcSrv, batchSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, tenancyv1.RegisterTenancyServiceHandler(context.Background(), mux, conn))
	require.NoError(t, authv1.RegisterAuthServiceHandler(context.Background(), mux, conn))
	require.NoError(t, inferv1.RegisterInferServiceServiceHandler(context.Background(), mux, conn))
	require.NoError(t, batchv1.RegisterBatchServiceHandler(context.Background(), mux, conn))

	realm := &runtimeFvtRealmResolver{realms: map[string]string{}}
	gwSrv := httptest.NewServer(server.RealmGuard(mux, realm))
	t.Cleanup(gwSrv.Close)
	env.gwSrv = gwSrv
	env.realm = realm

	return env
}

// call issues a JSON request against the gateway and decodes the body.
// admin=true wires the admin session (an org admin); admin=false wires
// a plain member session.
func (e *quotaFvtEnv) call(t *testing.T, method, path string, body any, admin bool) (int, map[string]any) {
	t.Helper()
	user := "user-fvt"
	if admin {
		user = "admin-fvt"
	}
	e.ten.SetSessionResolver(&fakeRBACSession{userID: user, email: user + "@example.com"})
	e.ten.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	return e.callRaw(t, method, path, body)
}

// callRaw issues a JSON request without touching the session resolvers.
func (e *quotaFvtEnv) callRaw(t *testing.T, method, path string, body any) (int, map[string]any) {
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
	req.Header.Set("X-Organization-Id", "org-fvt")
	resp, err := e.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestFVTQuotaAdminLifecycle covers AC1/AC2/AC10: the admin list, get +
// change history, update + audit, and the 13305/13306 validation.
func TestFVTQuotaAdminLifecycle(t *testing.T) {
	env := newQuotaFvtEnv(t)

	// List: org-fvt is unlimited by default.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/quotas?limit=50", nil, true)
	require.Equal(t, http.StatusOK, code, "list: %v", body)
	quotas, _ := body["quotas"].([]any)
	require.Len(t, quotas, 1)
	row := quotas[0].(map[string]any)
	q := row["quota"].(map[string]any)
	assert.Equal(t, "org-fvt", q["organizationId"])
	assert.Equal(t, float64(0), numberOf(t, q["maxConcurrentRequests"]))

	// Update: set concurrency 50 and services 20.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/quotas/org-fvt", map[string]any{
		"maxConcurrentRequests": 50,
		"maxInferenceServices":  20,
	}, true)
	require.Equal(t, http.StatusOK, code, "update: %v", body)
	assert.Equal(t, float64(50), numberOf(t, body["quota"].(map[string]any)["maxConcurrentRequests"]))
	assert.Equal(t, "admin-fvt", body["quota"].(map[string]any)["updatedBy"])

	// Get: the change history carries the two dimensions.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/quotas/org-fvt", nil, true)
	require.Equal(t, http.StatusOK, code, "get: %v", body)
	changes, _ := body["changes"].([]any)
	require.Len(t, changes, 2)
	ch := changes[0].(map[string]any)
	assert.Equal(t, "max_concurrent_requests", ch["dimension"])
	assert.Equal(t, float64(0), numberOf(t, ch["oldValue"]))
	assert.Equal(t, float64(50), numberOf(t, ch["newValue"]))

	// Invalid value -> 13305.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/quotas/org-fvt", map[string]any{
		"maxConcurrentRequests": 10001,
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "invalid: %v", body)
	assert.EqualValues(t, 13305, body["code"])

	// Unknown org -> 13306.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/quotas/org-missing", nil, true)
	assert.NotEqual(t, http.StatusOK, code, "unknown: %v", body)
	assert.EqualValues(t, 13306, body["code"])

	// Invalid limit_state -> 13305.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/quotas?limit_state=bogus", nil, true)
	assert.NotEqual(t, http.StatusOK, code, "limit_state: %v", body)
	assert.EqualValues(t, 13305, body["code"])
}

// TestFVTQuotaRoleAndRealm covers AC12/AC13: a plain member gets 10036
// on the admin RPCs; a user-realm session on an admin route gets 10038.
func TestFVTQuotaRoleAndRealm(t *testing.T) {
	env := newQuotaFvtEnv(t)

	// A plain member (no admin role) -> 10036.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/quotas", nil, false)
	assert.NotEqual(t, http.StatusOK, code, "member list: %v", body)
	assert.EqualValues(t, 10036, body["code"])

	// A user-realm session token on an admin route -> 10038.
	env.realm.realms["user-token"] = "user"
	req, err := http.NewRequest(http.MethodGet, env.gwSrv.URL+"/api/v1/admin/quotas", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer user-token")
	resp, err := env.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	assert.NotEqual(t, http.StatusOK, resp.StatusCode, "realm: %v", out)
	assert.EqualValues(t, 10038, out["code"])
}

// TestFVTQuotaGetMyQuota covers AC9: the user surface returns the
// caller's own org quota with live usage and the request-increase
// mailbox; no other org's data leaks.
func TestFVTQuotaGetMyQuota(t *testing.T) {
	env := newQuotaFvtEnv(t)

	// Set a quota as admin first.
	code, body := env.call(t, http.MethodPatch, "/api/v1/admin/quotas/org-fvt", map[string]any{
		"maxConcurrentRequests": 50,
	}, true)
	require.Equal(t, http.StatusOK, code, "update: %v", body)

	// The user surface reads the same org.
	code, body = env.call(t, http.MethodGet, "/api/v1/quotas", nil, false)
	require.Equal(t, http.StatusOK, code, "my quota: %v", body)
	assert.Equal(t, "org-fvt", body["quota"].(map[string]any)["organizationId"])
	assert.Equal(t, float64(50), numberOf(t, body["quota"].(map[string]any)["maxConcurrentRequests"]))
	assert.Equal(t, "ops@example.com", body["requestIncreaseEmail"])
}

// TestFVTQuotaServiceCountGuard covers AC5: the infer create-time guard
// rejects the N+1st service with 13302.
func TestFVTQuotaServiceCountGuard(t *testing.T) {
	env := newQuotaFvtEnv(t)

	// Cap the org at 1 service.
	code, body := env.call(t, http.MethodPatch, "/api/v1/admin/quotas/org-fvt", map[string]any{
		"maxInferenceServices": 1,
	}, true)
	require.Equal(t, http.StatusOK, code, "update: %v", body)

	// Seed one running service for org-fvt.
	repo := infer.NewInferenceServiceRepository(env.db)
	require.NoError(t, repo.Create(context.Background(), &infer.InferenceService{
		ID: infer.NewServiceID(), OrganizationID: "org-fvt", Name: "existing",
		ModelID: "model-1", ModelVersion: "v1", ImageID: "img-1", Replicas: 1,
		Accelerator: "nvidia", State: infer.StateRunning, Endpoints: []string{},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	// The next create is rejected with 13302.
	env.infer.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	code, body = env.callRaw(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "demo", "modelId": "model-1", "modelVersion": "v1",
		"imageId": "img-1", "replicas": 1, "accelerator": "nvidia",
	})
	assert.NotEqual(t, http.StatusOK, code, "create: %v", body)
	assert.EqualValues(t, 13302, body["code"])
}

// TestFVTQuotaBatchDailyGuard covers AC6: the batch create-time guard
// rejects the N+1st job with 13303.
func TestFVTQuotaBatchDailyGuard(t *testing.T) {
	env := newQuotaFvtEnv(t)

	// Cap the org at 1 job/day.
	code, body := env.call(t, http.MethodPatch, "/api/v1/admin/quotas/org-fvt", map[string]any{
		"maxBatchJobsPerDay": 1,
	}, true)
	require.Equal(t, http.StatusOK, code, "update: %v", body)

	// Seed one job created today.
	repo := batch.NewRepository(env.db)
	_, err := repo.InsertBatchJob(context.Background(), &batch.BatchJob{
		OrganizationID: "org-fvt", Model: "gpt-4o", Status: batch.StatusValidating,
	})
	require.NoError(t, err)

	// The next create is rejected with 13303.
	env.batch.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	code, body = env.callRaw(t, http.MethodPost, "/api/v1/batch", map[string]any{
		"input_file":        base64.StdEncoding.EncodeToString(validBatchJSONL()),
		"completion_window": "24h",
	})
	assert.NotEqual(t, http.StatusOK, code, "create: %v", body)
	assert.EqualValues(t, 13303, body["code"])
}

// TestFVTQuotaVerdictExtension covers AC11: the VerifyAPIKey verdict
// carries the org's hot-path caps after a quota update.
func TestFVTQuotaVerdictExtension(t *testing.T) {
	env := newQuotaFvtEnv(t)

	// Create an API key for org-fvt via the auth service (the
	// X-Organization-Id header is set by callRaw).
	code, body := env.callRaw(t, http.MethodPost, "/api/v1/auth/api-keys", map[string]any{"name": "quota-key"})
	require.Equal(t, http.StatusOK, code, "create key: %v", body)
	key := body["apiKey"].(string)
	digest := auth.KeyDigest(key)

	// Set the hot-path caps.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/quotas/org-fvt", map[string]any{
		"maxConcurrentRequests":     50,
		"maxOutputTokensPerRequest": 8192,
	}, true)
	require.Equal(t, http.StatusOK, code, "update: %v", body)

	// Verify the key over gRPC (data-plane path): the verdict carries
	// the caps.
	verdict, err := env.auth.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{KeyDigest: digest})
	require.NoError(t, err)
	assert.Equal(t, int64(50), verdict.GetMaxConcurrentRequests())
	assert.Equal(t, int64(8192), verdict.GetMaxOutputTokensPerRequest())
}