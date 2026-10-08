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

	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
)

// routingEnv is the in-process stack for the routing-policy feature
// (feature #45): the infer service over one gRPC server, the gateway
// mux in front, a shared SQLite, and the tenancy role guard wired for
// the admin session realm.
type routingEnv struct {
	db       *gorm.DB
	gwSrv    *httptest.Server
	grpcLn   net.Listener
	inferSvc *infer.Service
	mqBus    *recordingBus
}

// fvtSessionUserResolver resolves the session user from the
// x-session-user metadata the FVT header matcher forwards.
type fvtSessionUserResolver struct{ user string }

func (r fvtSessionUserResolver) SessionUserID(context.Context) (string, error) {
	return r.user, nil
}

// fvtSessionOrgResolver resolves the active org from the
// x-organization-id metadata.
type fvtSessionOrgResolver struct{ org string }

func (r fvtSessionOrgResolver) SessionActiveOrg(context.Context) (string, error) {
	return r.org, nil
}

// newRoutingEnv builds the routing-policy FVT stack. The admin session
// resolves to user "admin-fvt" of org "org-fvt" (an admin member); the
// plain session resolves to "user-fvt" (a plain member).
func newRoutingEnv(t *testing.T) *routingEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, db.Create(&tenancy.Organization{
		ID: "org-fvt", DisplayName: "org-fvt", State: tenancy.StateActive,
	}).Error)
	// The admin session user is an org admin; the plain user is a
	// member (feature #45, AD6).
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

	bus := newRecordingBus()
	inferSvc := infer.NewForFVT(db, bus)
	inferSvc.SetRoleGuard(tenancy.NewRoleGuard(db))
	env := &routingEnv{db: db, grpcLn: ln, inferSvc: inferSvc, mqBus: bus}

	inferv1.RegisterInferServiceServiceServer(grpcSrv, inferSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, inferv1.RegisterInferServiceServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)
	env.gwSrv = gwSrv

	return env
}

// call issues a JSON request against the gateway and decodes the body.
// admin=true wires the admin session (an org admin); admin=false wires
// a plain member session.
func (e *routingEnv) call(t *testing.T, method, path string, body any, admin bool) (int, map[string]any) {
	t.Helper()
	if admin {
		e.inferSvc.SetSessionUserResolver(fvtSessionUserResolver{user: "admin-fvt"})
		e.inferSvc.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	} else {
		e.inferSvc.SetSessionUserResolver(fvtSessionUserResolver{user: "user-fvt"})
		e.inferSvc.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	}
	return e.callRaw(t, method, path, body)
}

// callRaw issues a JSON request without touching the session resolvers,
// so a test can first install a broken/missing session seam.
func (e *routingEnv) callRaw(t *testing.T, method, path string, body any) (int, map[string]any) {
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

// seedRoutingModel registers a model, activates its version and returns
// the model id.
func (e *routingEnv) seedRoutingModel(t *testing.T, name, version string) string {
	t.Helper()
	repo := model.NewRepository(e.db)
	id, err := repo.RegisterModelOrCreateVersion(context.Background(), name, "", version, name+"/"+version)
	require.NoError(t, err)
	_, err = repo.ActivateVersion(context.Background(), id, version)
	require.NoError(t, err)
	return id
}

// seedRoutingService inserts an inference service row directly.
func (e *routingEnv) seedRoutingService(t *testing.T, modelID, version, state string) string {
	t.Helper()
	repo := infer.NewInferenceServiceRepository(e.db)
	svc := &infer.InferenceService{
		ID:             infer.NewServiceID(),
		OrganizationID: "org-fvt",
		Name:           "svc-" + infer.NewServiceID()[:8],
		ModelID:        modelID,
		ModelVersion:   version,
		ImageID:        "img-1",
		Replicas:       1,
		Accelerator:    "nvidia",
		State:          state,
		Endpoints:      []string{},
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	require.NoError(t, repo.Create(context.Background(), svc))
	return svc.ID
}

// TestFVTRoutingPolicyLifecycle covers the admin routing-policy
// surface end-to-end (feature #45): list/get/update happy path, the
// 10312/10313 error paths, the enable-with-no-ready-target rejection,
// the masked health projection and the revision history.
func TestFVTRoutingPolicyLifecycle(t *testing.T) {
	env := newRoutingEnv(t)
	modelID := env.seedRoutingModel(t, "qwen-3b", "v1")
	readyID := env.seedRoutingService(t, modelID, "v1", infer.StateRunning)
	pendingID := env.seedRoutingService(t, modelID, "v1", infer.StatePending)

	// AC: the list shows the model with DEFAULT state before any
	// policy exists.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/routing-policies", nil, true)
	require.Equal(t, http.StatusOK, code, "list must succeed: %v", body)
	policies, _ := body["policies"].([]any)
	require.Len(t, policies, 1)
	first, _ := policies[0].(map[string]any)
	assert.Equal(t, modelID, first["modelId"])
	assert.Equal(t, "DEFAULT", first["policyState"])

	// AC: get projects the default policy (revision 0, disabled).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-policies/"+modelID, nil, true)
	require.Equal(t, http.StatusOK, code, "get must succeed: %v", body)
	policy, _ := body["policy"].(map[string]any)
	assert.Equal(t, "0", fmt.Sprint(policy["revision"]))
	assert.Equal(t, false, policy["enabled"])

	// AC: update creates revision 1 with the ordered targets.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-policies/"+modelID, map[string]any{
		"modelVersion":     "v1",
		"enabled":          true,
		"serviceIds":       []string{readyID, pendingID},
		"maxAttempts":      2,
		"retryOn":          []string{"RETRY_CATEGORY_HTTP_429"},
		"expectedRevision": "0",
	}, true)
	require.Equal(t, http.StatusOK, code, "update must succeed: %v", body)
	policy, _ = body["policy"].(map[string]any)
	assert.Equal(t, "1", fmt.Sprint(policy["revision"]))
	assert.Equal(t, true, policy["enabled"])

	// AC: the list now reports ENABLED with one ready target.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-policies", nil, true)
	require.Equal(t, http.StatusOK, code)
	policies, _ = body["policies"].([]any)
	first, _ = policies[0].(map[string]any)
	assert.Equal(t, "ENABLED", first["policyState"])
	assert.Equal(t, float64(1), first["readyCount"])

	// AC: a stale expected_revision yields 10313.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-policies/"+modelID, map[string]any{
		"modelVersion":     "v1",
		"enabled":          false,
		"serviceIds":       []string{readyID},
		"maxAttempts":      1,
		"expectedRevision": "0",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "stale revision must fail: %v", body)
	assert.EqualValues(t, 10313, body["code"])

	// AC: an invalid target (wrong version) yields 10312.
	wrongSvc := env.seedRoutingService(t, modelID, "v2", infer.StateRunning)
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-policies/"+modelID, map[string]any{
		"modelVersion":     "v1",
		"enabled":          true,
		"serviceIds":       []string{wrongSvc},
		"maxAttempts":      1,
		"expectedRevision": "1",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "wrong-version target must fail: %v", body)
	assert.EqualValues(t, 10312, body["code"])

	// AC: enabling a model with no ready target is rejected.
	otherModel := env.seedRoutingModel(t, "llama-8b", "v1")
	env.seedRoutingService(t, otherModel, "v1", infer.StatePending)
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-policies/"+otherModel, map[string]any{
		"modelVersion":     "v1",
		"enabled":          true,
		"serviceIds":       []string{},
		"maxAttempts":      1,
		"expectedRevision": "0",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "enable without ready target must fail: %v", body)
	assert.EqualValues(t, 10312, body["code"])

	// AC: the health projection is masked (no endpoint URLs).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-policies/"+modelID+"/health", nil, true)
	require.Equal(t, http.StatusOK, code, "health must succeed: %v", body)
	targets, _ := body["targets"].([]any)
	require.Len(t, targets, 2)
	for _, raw := range targets {
		target, _ := raw.(map[string]any)
		assert.NotEmpty(t, target["serviceId"])
		// The projection carries readiness flags, never endpoint URLs.
		_, hasEndpoint := target["endpoints"]
		assert.False(t, hasEndpoint, "health projection must not expose endpoints")
	}

	// AC: the revision history lists the immutable revision with
	// before/after values.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-policies/"+modelID+"/revisions", nil, true)
	require.Equal(t, http.StatusOK, code, "revisions must succeed: %v", body)
	revisions, _ := body["revisions"].([]any)
	require.Len(t, revisions, 1)
	rev, _ := revisions[0].(map[string]any)
	assert.Equal(t, "1", fmt.Sprint(rev["revision"]))
	assert.Equal(t, "admin-fvt", rev["actor"])
	after, _ := rev["after"].(map[string]any)
	assert.Equal(t, true, after["enabled"])

	// AC: a plain member session is denied (AD6).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-policies", nil, false)
	assert.NotEqual(t, http.StatusOK, code, "plain member must be denied: %v", body)
	assert.EqualValues(t, 10036, body["code"])

	// AC: an unauthenticated caller is denied.
	env.inferSvc.SetSessionUserResolver(nil)
	code, body = env.callRaw(t, http.MethodGet, "/api/v1/admin/routing-policies", nil)
	assert.NotEqual(t, http.StatusOK, code, "missing session must be denied: %v", body)
	assert.EqualValues(t, 10001, body["code"])
}

// TestFVTRoutingPolicyOutboxPublication verifies the committed revision
// reaches the dedicated infer.routing.policies subject through the
// outbox publisher (feature #45, AD5).
func TestFVTRoutingPolicyOutboxPublication(t *testing.T) {
	env := newRoutingEnv(t)
	modelID := env.seedRoutingModel(t, "qwen-3b", "v1")
	readyID := env.seedRoutingService(t, modelID, "v1", infer.StateRunning)

	code, body := env.call(t, http.MethodPut, "/api/v1/admin/routing-policies/"+modelID, map[string]any{
		"modelVersion":     "v1",
		"enabled":          true,
		"serviceIds":       []string{readyID},
		"maxAttempts":      1,
		"expectedRevision": "0",
	}, true)
	require.Equal(t, http.StatusOK, code, "update must succeed: %v", body)

	// The outbox publisher delivers the snapshot to the dedicated
	// subject.
	repo := infer.NewRoutingPolicyRepository(env.db)
	publisher := infer.NewRoutingPolicyPublisher(repo, env.mqBus, 0)
	publisher.RunOnce(context.Background())

	var outbox infer.RoutingPolicyOutbox
	require.NoError(t, env.db.Where("model_id = ?", modelID).First(&outbox).Error)
	require.NotNil(t, outbox.PublishedAt, "outbox row must be marked published")

	env.mqBus.mu.Lock()
	messages := append([]mq.Message(nil), env.mqBus.routing...)
	env.mqBus.mu.Unlock()
	require.Len(t, messages, 1, "the snapshot must reach the dedicated subject exactly once")
	assert.Contains(t, string(messages[0].Body), modelID)
	assert.Equal(t, "1", messages[0].Headers["revision"])
}
