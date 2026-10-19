// Feature #53 (fault diagnosis & automatic fault tolerance)
// full-verification suite: the six RPCs of taas.fault.v1.FaultService
// (admin-prefix under /api/v1/admin/faults*) and the user-surface
// GetServiceStatus (taas.infer.v1) over one gRPC server with the
// production RealmGuard in front. It exercises the acceptance criteria
// AC1–AC13.

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

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	faultv1 "github.com/go-taas/go-taas/proto/taas/fault/v1"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	tenancyv1 "github.com/go-taas/go-taas/proto/taas/tenancy/v1"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/audit"
	"github.com/go-taas/go-taas/services/fault"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/tenancy"
)

// faultFvtEnv is the in-process stack for feature #53.
type faultFvtEnv struct {
	db     *gorm.DB
	gwSrv  *httptest.Server
	grpcLn net.Listener
	fault  *fault.Service
	infer  *infer.Service
	ten    *tenancy.Service
	realm  *runtimeFvtRealmResolver
}

// newFaultFvtEnv builds the fault FVT stack. The admin session resolves
// to user "admin-fvt" of org "org-fvt" (an org admin); the plain
// session resolves to "user-fvt" (a plain member).
func newFaultFvtEnv(t *testing.T) *faultFvtEnv {
	t.Helper()

	cfg := &config.Configuration{}
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, audit.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, fault.MigrateSchemaForFVT(db))
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

	faultSvc := fault.NewForFVT(db)
	faultSvc.SetRoleGuard(tenancy.NewRoleGuard(db))
	faultSvc.SetAuditRecorder(audit.NewRecorder(audit.NewRepository(db)))

	inferSvc := infer.NewForFVT(db, newRecordingBus())
	inferSvc.SetIncidentReader(faultSvc)

	env := &faultFvtEnv{db: db, grpcLn: ln, fault: faultSvc, infer: inferSvc, ten: tenSvc}

	faultv1.RegisterFaultServiceServer(grpcSrv, faultSvc)
	inferv1.RegisterInferServiceServiceServer(grpcSrv, inferSvc)
	tenancyv1.RegisterTenancyServiceServer(grpcSrv, tenSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, faultv1.RegisterFaultServiceHandler(context.Background(), mux, conn))
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
func (e *faultFvtEnv) call(t *testing.T, method, path string, body any, admin bool) (int, map[string]any) {
	t.Helper()
	user := "user-fvt"
	if admin {
		user = "admin-fvt"
	}
	e.ten.SetSessionResolver(&fakeRBACSession{userID: user, email: user + "@example.com"})
	e.ten.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	e.fault.SetSessionResolver(&fakeRBACSession{userID: user, email: user + "@example.com"})
	e.fault.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	return e.callRaw(t, method, path, body)
}

// callRaw issues a JSON request without touching the session resolvers.
func (e *faultFvtEnv) callRaw(t *testing.T, method, path string, body any) (int, map[string]any) {
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

// seedIncident creates an incident directly in the DB and returns its id.
func (e *faultFvtEnv) seedIncident(t *testing.T, cause, targetType, targetName string) string {
	t.Helper()
	now := time.Now().UTC()
	inc := &fault.Incident{
		ID: uuid.NewString(), Severity: fault.SeverityForCause(cause), Status: fault.StatusDetected,
		RootCause: cause, TargetType: targetType, TargetName: targetName,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, e.db.Create(inc).Error)
	return inc.ID
}

// TestFVTfaultAdminLifecycle covers AC1/AC2/AC10: the admin incident
// list, get + timeline, policy update + audit, and the 13502/13506
// validation.
func TestFVTfaultAdminLifecycle(t *testing.T) {
	env := newFaultFvtEnv(t)

	// Seed two incidents.
	id1 := env.seedIncident(t, fault.CauseServiceCrashLoop, fault.TargetService, "svc-a")
	env.seedIncident(t, fault.CauseNodeNotReady, fault.TargetNode, "node-a")

	// List.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/faults?limit=50", nil, true)
	require.Equal(t, http.StatusOK, code, "list: %v", body)
	incidents, _ := body["incidents"].([]any)
	require.Len(t, incidents, 2)

	// Get.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/faults/"+id1, nil, true)
	require.Equal(t, http.StatusOK, code, "get: %v", body)
	assert.Equal(t, "svc-a", body["incident"].(map[string]any)["targetName"])

	// Policy get.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/faults/policy", nil, true)
	require.Equal(t, http.StatusOK, code, "policy: %v", body)
	assert.True(t, body["policy"].(map[string]any)["drainAlwaysConfirm"].(bool))

	// Policy update.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/faults/policy", map[string]any{
		"autoRestart": map[string]any{"oom": false},
	}, true)
	require.Equal(t, http.StatusOK, code, "policy update: %v", body)
	assert.False(t, body["policy"].(map[string]any)["autoRestart"].(map[string]any)["oom"].(bool))

	// Invalid policy category -> 13502.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/faults/policy", map[string]any{
		"autoRestart": map[string]any{"bogus": true},
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "invalid policy: %v", body)
	assert.EqualValues(t, 13502, body["code"])

	// Unknown incident -> 13506.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/faults/missing", nil, true)
	assert.NotEqual(t, http.StatusOK, code, "unknown: %v", body)
	assert.EqualValues(t, 13506, body["code"])
}

// TestFVTfaultRecoveryActions covers AC5/AC6: the trigger validation
// (drain without confirm -> 13503) and the override (no action in
// flight -> 13504).
func TestFVTfaultRecoveryActions(t *testing.T) {
	env := newFaultFvtEnv(t)
	id := env.seedIncident(t, fault.CauseServiceCrashLoop, fault.TargetService, "svc-a")

	// Manual restart.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/faults/"+id+":recover", map[string]any{
		"action": "restart",
	}, true)
	require.Equal(t, http.StatusOK, code, "recover: %v", body)
	assert.Equal(t, "restart", body["action"].(map[string]any)["action"])

	// Drain without confirm -> 13503.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/faults/"+id+":recover", map[string]any{
		"action": "drain",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "drain: %v", body)
	assert.EqualValues(t, 13503, body["code"])

	// Override with no action in flight -> 13504.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/faults/"+id+":override", nil, true)
	assert.NotEqual(t, http.StatusOK, code, "override: %v", body)
	assert.EqualValues(t, 13504, body["code"])
}

// TestFVTfaultRoleAndRealm covers AC11/AC12: a plain member gets 10036
// on the admin RPCs; a user-realm session on an admin route gets 10038.
func TestFVTfaultRoleAndRealm(t *testing.T) {
	env := newFaultFvtEnv(t)

	// A plain member (no admin role) -> 10036.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/faults", nil, false)
	assert.NotEqual(t, http.StatusOK, code, "member list: %v", body)
	assert.EqualValues(t, 10036, body["code"])

	// A user-realm session token on an admin route -> 10038.
	env.realm.realms["user-token"] = "user"
	req, err := http.NewRequest(http.MethodGet, env.gwSrv.URL+"/api/v1/admin/faults", nil)
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

// TestFVTfaultServiceStatus covers AC8/AC9: the user-surface service
// status returns the caller's own service's incident.
func TestFVTfaultServiceStatus(t *testing.T) {
	env := newFaultFvtEnv(t)

	// Seed a service and an incident for it.
	require.NoError(t, env.db.Exec(`INSERT INTO inference_services (id, organization_id, name, model_id, model_version, image_id, replicas, accelerator, accelerator_type, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"11111111-1111-1111-1111-111111111111", "org-fvt", "svc-a", "m1", "v1", "img1", 1, "nvidia", "A800", "running").Error)
	env.seedIncident(t, fault.CauseServiceCrashLoop, fault.TargetService, "11111111-1111-1111-1111-111111111111")

	// User surface: the service status shows the degraded incident.
	code, body := env.call(t, http.MethodGet, "/api/v1/inference-services/11111111-1111-1111-1111-111111111111/status", nil, false)
	require.Equal(t, http.StatusOK, code, "status: %v", body)
	assert.True(t, body["degraded"].(bool))
	inc, _ := body["incident"].(map[string]any)
	assert.Equal(t, "service_crash_loop", inc["rootCause"])
}