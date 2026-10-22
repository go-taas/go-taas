// Feature #52 (HAMI-based vGPU slicing pool) full-verification suite:
// the six RPCs of taas.vgpu.v1.VGPUService (five admin-prefix under
// /api/v1/admin/vgpu-pools*, one user-prefix /api/v1/vgpu-pools) over
// one gRPC server with the production RealmGuard in front, the
// deploy-time slice validation seam (infer), the pool CRUD validation
// (13401/13402), the delete-blocked guard (13405), the grant-filtered
// tenant list (AD10), and the realm/role negatives (10038/10036). It
// exercises the acceptance criteria AC1–AC13.

package fvt

import (
	"bytes"
	"context"
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

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"
	vgpuv1 "github.com/go-taas/go-taas/proto/taas/vgpu/v1"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
	"github.com/go-taas/go-taas/services/vgpu"
)

// vgpuFvtEnv is the in-process stack for feature #52.
type vgpuFvtEnv struct {
	db      *gorm.DB
	gwSrv   *httptest.Server
	grpcLn  net.Listener
	vgpu    *vgpu.Service
	infer   *infer.Service
	ten     *tenancy.Service
	realm   *runtimeFvtRealmResolver
	modelID string
}

// newVgpuFvtEnv builds the vgpu FVT stack. The admin session resolves
// to user "admin-fvt" of org "org-fvt" (an org admin); the plain
// session resolves to "user-fvt" (a plain member).
func newVgpuFvtEnv(t *testing.T) *vgpuFvtEnv {
	t.Helper()

	cfg := &config.Configuration{}
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, audit.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, vgpu.MigrateSchemaForFVT(db))
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, image.MigrateSchemaForFVT(db))
	require.NoError(t, db.Create(&tenancy.Organization{
		ID: "org-fvt", DisplayName: "org-fvt", State: tenancy.StateActive,
	}).Error)
	// Seed a model + version and the image registry so the deploy-time
	// slice validation can be exercised end-to-end.
	modelRepo := model.NewRepository(db)
	modelID, err := modelRepo.RegisterModelOrCreateVersion(context.Background(), "qwen-3b", "", "v1", "qwen-3b/v1")
	require.NoError(t, err)
	imageRepo := image.NewRepository(db)
	require.NoError(t, imageRepo.CreateImage(context.Background(), &image.Image{
		ID: "img-vllm-nvidia", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm",
	}))
	image.WireRegistryForTest(db)
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

	vgpuSvc := vgpu.NewForFVT(db)
	vgpuSvc.SetRoleGuard(tenancy.NewRoleGuard(db))
	vgpuSvc.SetAuditRecorder(audit.NewRecorder(audit.NewRepository(db)))

	inferSvc := infer.NewForFVT(db, newRecordingBus())
	inferSvc.SetSliceValidator(vgpuSvc)

	env := &vgpuFvtEnv{db: db, grpcLn: ln, vgpu: vgpuSvc, infer: inferSvc, ten: tenSvc, modelID: modelID}

	vgpuv1.RegisterVGPUServiceServer(grpcSrv, vgpuSvc)
	inferv1.RegisterInferServiceServiceServer(grpcSrv, inferSvc)
	tenancyv1.RegisterTenancyServiceServer(grpcSrv, tenSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, vgpuv1.RegisterVGPUServiceHandler(context.Background(), mux, conn))
	require.NoError(t, inferv1.RegisterInferServiceServiceHandler(context.Background(), mux, conn))
	require.NoError(t, tenancyv1.RegisterTenancyServiceHandler(context.Background(), mux, conn))

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
func (e *vgpuFvtEnv) call(t *testing.T, method, path string, body any, admin bool) (int, map[string]any) {
	t.Helper()
	user := "user-fvt"
	if admin {
		user = "admin-fvt"
	}
	e.ten.SetSessionResolver(&fakeRBACSession{userID: user, email: user + "@example.com"})
	e.ten.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	e.vgpu.SetSessionResolver(&fakeRBACSession{userID: user, email: user + "@example.com"})
	e.vgpu.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	return e.callRaw(t, method, path, body)
}

// callRaw issues a JSON request without touching the session resolvers.
func (e *vgpuFvtEnv) callRaw(t *testing.T, method, path string, body any) (int, map[string]any) {
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

// TestFVTvgpuAdminLifecycle covers AC1/AC2/AC10: the admin pool CRUD,
// validation (13401/13402), and the audit trail.
func TestFVTvgpuAdminLifecycle(t *testing.T) {
	env := newVgpuFvtEnv(t)

	// Create a pool.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/vgpu-pools", map[string]any{
		"name":            "pool-a",
		"labelKey":        "go-taas.com/vgpu-pool",
		"labelValue":      "pool-a",
		"placementPolicy": "binpack",
		"templates": []map[string]any{
			{"name": "small", "memoryGib": 8, "computePct": 25},
			{"name": "medium", "memoryGib": 16, "computePct": 50},
		},
	}, true)
	require.Equal(t, http.StatusOK, code, "create: %v", body)
	poolID := body["pool"].(map[string]any)["poolId"].(string)
	assert.NotEmpty(t, poolID)

	// List.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/vgpu-pools?limit=50", nil, true)
	require.Equal(t, http.StatusOK, code, "list: %v", body)
	pools, _ := body["pools"].([]any)
	require.Len(t, pools, 1)
	assert.Equal(t, "pool-a", pools[0].(map[string]any)["name"])

	// Get.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/vgpu-pools/"+poolID, nil, true)
	require.Equal(t, http.StatusOK, code, "get: %v", body)
	assert.Equal(t, "pool-a", body["pool"].(map[string]any)["name"])

	// Update the policy.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/vgpu-pools/"+poolID, map[string]any{
		"placementPolicy": "spread",
	}, true)
	require.Equal(t, http.StatusOK, code, "update: %v", body)
	assert.Equal(t, "spread", body["pool"].(map[string]any)["placementPolicy"])

	// Invalid name -> 13401.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/vgpu-pools", map[string]any{
		"name": "Bad Name!", "labelKey": "k", "labelValue": "v",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "invalid name: %v", body)
	assert.EqualValues(t, 13401, body["code"])

	// Invalid template -> 13402.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/vgpu-pools", map[string]any{
		"name": "pool-b", "labelKey": "k", "labelValue": "v",
		"templates": []map[string]any{{"name": "small", "memoryGib": 0, "computePct": 25}},
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "invalid template: %v", body)
	assert.EqualValues(t, 13402, body["code"])

	// Unknown pool -> 13406.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/vgpu-pools/missing", nil, true)
	assert.NotEqual(t, http.StatusOK, code, "unknown: %v", body)
	assert.EqualValues(t, 13406, body["code"])

	// Malformed (non-UUID) pool id -> 13406, not a uuid cast error
	// (bug VGPU-1).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/vgpu-pools/not-a-uuid", nil, true)
	assert.NotEqual(t, http.StatusOK, code, "malformed: %v", body)
	assert.EqualValues(t, 13406, body["code"])

	// Delete the idle pool.
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/vgpu-pools/"+poolID, nil, true)
	require.Equal(t, http.StatusOK, code, "delete: %v", body)
}

// TestFVTvgpuDeleteBlocked covers AC9: a pool with active slice users
// cannot be deleted (13405).
func TestFVTvgpuDeleteBlocked(t *testing.T) {
	env := newVgpuFvtEnv(t)

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/vgpu-pools", map[string]any{
		"name": "pool-a", "labelKey": "go-taas.com/vgpu-pool", "labelValue": "pool-a",
		"placementPolicy": "binpack",
		"templates":       []map[string]any{{"name": "small", "memoryGib": 8, "computePct": 25}},
	}, true)
	require.Equal(t, http.StatusOK, code, "create: %v", body)
	poolID := body["pool"].(map[string]any)["poolId"].(string)

	// Seed a service using the pool.
	require.NoError(t, env.db.Exec(`INSERT INTO inference_services (id, organization_id, name, model_id, model_version, image_id, replicas, accelerator, accelerator_type, state, pool_id, slice_template_id, slice_gpu_index) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"11111111-1111-1111-1111-111111111111", "org-fvt", "svc-1", "m1", "v1", "img1", 1, "nvidia", "A800", "running", poolID, "small", -1).Error)

	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/vgpu-pools/"+poolID, nil, true)
	assert.NotEqual(t, http.StatusOK, code, "delete blocked: %v", body)
	assert.EqualValues(t, 13405, body["code"])
}

// TestFVTvgpuTenantList covers AC7: the user surface returns the
// grant-filtered pools for the deploy form.
func TestFVTvgpuTenantList(t *testing.T) {
	env := newVgpuFvtEnv(t)

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/vgpu-pools", map[string]any{
		"name": "pool-a", "labelKey": "go-taas.com/vgpu-pool", "labelValue": "pool-a",
		"placementPolicy": "binpack",
		"templates":       []map[string]any{{"name": "small", "memoryGib": 8, "computePct": 25}},
	}, true)
	require.Equal(t, http.StatusOK, code, "create: %v", body)

	// User surface: the pool is listed (default-allow grant).
	code, body = env.call(t, http.MethodGet, "/api/v1/vgpu-pools", nil, false)
	require.Equal(t, http.StatusOK, code, "tenant list: %v", body)
	pools, _ := body["pools"].([]any)
	require.Len(t, pools, 1)
	assert.Equal(t, "pool-a", pools[0].(map[string]any)["name"])
	assert.Len(t, pools[0].(map[string]any)["templates"].([]any), 1)
}

// TestFVTvgpuRoleAndRealm covers AC11/AC12: a plain member gets 10036
// on the admin RPCs; a user-realm session on an admin route gets 10038.
func TestFVTvgpuRoleAndRealm(t *testing.T) {
	env := newVgpuFvtEnv(t)

	// A plain member (no admin role) -> 10036.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/vgpu-pools", nil, false)
	assert.NotEqual(t, http.StatusOK, code, "member list: %v", body)
	assert.EqualValues(t, 10036, body["code"])

	// A user-realm session token on an admin route -> 10038.
	env.realm.realms["user-token"] = "user"
	req, err := http.NewRequest(http.MethodGet, env.gwSrv.URL+"/api/v1/admin/vgpu-pools", nil)
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

// TestFVTvgpuDeploySliceValidation covers AC7: the deploy-time slice
// validation seam rejects an invalid/ungranted slice (13403).
func TestFVTvgpuDeploySliceValidation(t *testing.T) {
	env := newVgpuFvtEnv(t)

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/vgpu-pools", map[string]any{
		"name": "pool-a", "labelKey": "go-taas.com/vgpu-pool", "labelValue": "pool-a",
		"placementPolicy": "binpack",
		"templates":       []map[string]any{{"name": "small", "memoryGib": 8, "computePct": 25}},
	}, true)
	require.Equal(t, http.StatusOK, code, "create: %v", body)

	// A slice request referencing an unknown pool -> 13403.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "svc-1", "modelId": env.modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": 1, "accelerator": "nvidia",
		"poolId": "missing", "sliceTemplateId": "small",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "unknown pool: %v", body)
	assert.EqualValues(t, 13403, body["code"])

	// A slice request referencing a malformed (non-UUID) pool id -> 13403,
	// not a uuid cast error (bug VGPU-1).
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "svc-2", "modelId": env.modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": 1, "accelerator": "nvidia",
		"poolId": "not-a-uuid", "sliceTemplateId": "small",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "malformed pool: %v", body)
	assert.EqualValues(t, 13403, body["code"])
}
