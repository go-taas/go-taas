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

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	routingv1 "github.com/go-taas/go-taas/proto/taas/routing/v1"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/routing"
	"github.com/go-taas/go-taas/services/tenancy"
)

// routingGroupEnv is the in-process stack for the intelligent model
// routing feature (feature #49): the routing service (admin registry +
// internal resolver), the model service (catalog + virtual entries) and
// the infer service (playground virtual path) over one gRPC server and
// the gateway mux, sharing one SQLite database.
type routingGroupEnv struct {
	db         *gorm.DB
	gwSrv      *httptest.Server
	grpcLn     net.Listener
	routingSvc *routing.Service
	modelSvc   *model.Service
	inferSvc   *infer.Service
}

// newRoutingGroupEnv builds the routing-group FVT stack. The admin
// session resolves to user "admin-fvt" of org "org-fvt" (an org
// admin); the plain session resolves to "user-fvt" (a plain member).
// The model service is wired as the routing catalog and the routing
// service as the model module's virtual-model lister, mirroring the
// production wiring in apps/taas-server/main.go.
func newRoutingGroupEnv(t *testing.T) *routingGroupEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, routing.MigrateSchemaForFVT(db))
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

	routingSvc := routing.NewForFVT(db)
	routingSvc.SetRoleGuard(tenancy.NewRoleGuard(db))
	modelSvc := model.NewForFVT(db)
	inferSvc := infer.NewForFVT(db, newRecordingBus())
	inferSvc.SetRoleGuard(tenancy.NewRoleGuard(db))

	// Production wiring: the model module is the routing catalog; the
	// routing module lists the granted groups as virtual models; the
	// infer module resolves virtual playground requests.
	routingSvc.SetModelCatalog(modelSvc)
	modelSvc.SetVirtualModelLister(routingSvc)
	inferSvc.SetRoutingResolver(routingSvc)
	// The virtual playground calls the serving gateway with the caller's
	// key, so the test points the resolver at a fake gateway.
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`))
	}))
	t.Cleanup(gateway.Close)
	inferSvc.SetGatewayResolver(fvtGatewayResolver{url: gateway.URL})

	env := &routingGroupEnv{db: db, grpcLn: ln, routingSvc: routingSvc, modelSvc: modelSvc, inferSvc: inferSvc}

	routingv1.RegisterRoutingServiceServer(grpcSrv, routingSvc)
	modelv1.RegisterModelServiceServer(grpcSrv, modelSvc)
	inferv1.RegisterInferServiceServiceServer(grpcSrv, inferSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, routingv1.RegisterRoutingServiceHandler(context.Background(), mux, conn))
	require.NoError(t, modelv1.RegisterModelServiceHandler(context.Background(), mux, conn))
	require.NoError(t, inferv1.RegisterInferServiceServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)
	env.gwSrv = gwSrv

	return env
}

// call issues a JSON request against the gateway and decodes the body.
// admin=true wires the admin session (an org admin); admin=false wires
// a plain member session.
func (e *routingGroupEnv) call(t *testing.T, method, path string, body any, admin bool) (int, map[string]any) {
	t.Helper()
	user := "user-fvt"
	if admin {
		user = "admin-fvt"
	}
	e.routingSvc.SetSessionUserResolver(fvtSessionUserResolver{user: user})
	e.routingSvc.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	e.modelSvc.SetSessionUserResolver(fvtSessionUserResolver{user: user})
	e.modelSvc.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	e.inferSvc.SetSessionUserResolver(fvtSessionUserResolver{user: user})
	e.inferSvc.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	return e.callRaw(t, method, path, body)
}

// callRaw issues a JSON request without touching the session resolvers.
func (e *routingGroupEnv) callRaw(t *testing.T, method, path string, body any) (int, map[string]any) {
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

// seedCatalogModel registers and activates a catalog model and returns
// its id.
func (e *routingGroupEnv) seedCatalogModel(t *testing.T, name string) string {
	t.Helper()
	repo := model.NewRepository(e.db)
	id, err := repo.RegisterModelOrCreateVersion(context.Background(), name, "", "", "v1", name+"/v1")
	require.NoError(t, err)
	_, err = repo.ActivateVersion(context.Background(), id, "v1")
	require.NoError(t, err)
	return id
}

// seedRunningService inserts a running inference service row for the
// model so the playground's virtual path can serve it.
func (e *routingGroupEnv) seedRunningService(t *testing.T, modelID string) string {
	t.Helper()
	repo := infer.NewInferenceServiceRepository(e.db)
	svc := &infer.InferenceService{
		ID:             infer.NewServiceID(),
		OrganizationID: "org-fvt",
		Name:           "svc-" + infer.NewServiceID()[:8],
		ModelID:        modelID,
		ModelVersion:   "v1",
		ImageID:        "img-1",
		Replicas:       1,
		Accelerator:    "nvidia",
		State:          infer.StateRunning,
		Endpoints:      []string{},
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	require.NoError(t, repo.Create(context.Background(), svc))
	return svc.ID
}

// createGroup creates a group through the admin API and returns its id.
func (e *routingGroupEnv) createGroup(t *testing.T, name, strategy string) string {
	t.Helper()
	code, body := e.call(t, http.MethodPost, "/api/v1/admin/routing-groups", map[string]any{
		"name":     name,
		"strategy": strategy,
	}, true)
	require.Equal(t, http.StatusOK, code, "create group must succeed: %v", body)
	group, _ := body["group"].(map[string]any)
	require.NotNil(t, group)
	id, _ := group["groupId"].(string)
	require.NotEmpty(t, id)
	return id
}

// TestFVTRoutingGroupRegistryLifecycle covers the admin registry
// surface end-to-end (feature #49, AC1-AC4): create disabled, list with
// counts, get with ordered lists, update with the enable gate, the
// 13102/13103/13113/13107 error paths, delete with the 13111 grant
// block, and the role gate (AD6).
func TestFVTRoutingGroupRegistryLifecycle(t *testing.T) {
	env := newRoutingGroupEnv(t)
	modelA := env.seedCatalogModel(t, "qwen-3b")
	modelB := env.seedCatalogModel(t, "llama-8b")

	// AC1: create yields a disabled group with default counts.
	groupID := env.createGroup(t, "auto-chat", "weighted")
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/routing-groups/"+groupID, nil, true)
	require.Equal(t, http.StatusOK, code, "get must succeed: %v", body)
	group, _ := body["group"].(map[string]any)
	assert.Equal(t, "auto-chat", group["name"])
	assert.Equal(t, false, group["enabled"])
	assert.Equal(t, "0", fmt.Sprint(group["candidateCount"]))
	assert.Equal(t, "0", fmt.Sprint(group["ruleCount"]))

	// AC1: a duplicate name yields 13102.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/routing-groups", map[string]any{
		"name": "auto-chat", "strategy": "weighted",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "duplicate name must fail: %v", body)
	assert.EqualValues(t, 13102, body["code"])

	// AC1: an invalid name yields 13103.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/routing-groups", map[string]any{
		"name": "Bad_Name", "strategy": "weighted",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "invalid name must fail: %v", body)
	assert.EqualValues(t, 13103, body["code"])

	// AC1: a reserved real-model name yields 13113.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/routing-groups", map[string]any{
		"name": "qwen-3b", "strategy": "weighted",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "reserved name must fail: %v", body)
	assert.EqualValues(t, 13113, body["code"])

	// AC1: an invalid strategy yields 13107.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/routing-groups", map[string]any{
		"name": "auto-chat-2", "strategy": "round_robin",
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "invalid strategy must fail: %v", body)
	assert.EqualValues(t, 13107, body["code"])

	// AC5: replace candidates (ordered, weights, tiers).
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-groups/"+groupID+"/candidates", map[string]any{
		"groupId": groupID,
		"candidates": []map[string]any{
			{"modelId": modelA, "weight": 70, "costTier": 2, "latencyTier": 1},
			{"modelId": modelB, "weight": 30, "costTier": 1, "latencyTier": 3},
		},
	}, true)
	require.Equal(t, http.StatusOK, code, "replace candidates must succeed: %v", body)
	candidates, _ := body["candidates"].([]any)
	require.Len(t, candidates, 2)
	first, _ := candidates[0].(map[string]any)
	assert.Equal(t, modelA, first["modelId"])
	assert.Equal(t, float64(0), first["position"])

	// AC5: a duplicate candidate yields 13106.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-groups/"+groupID+"/candidates", map[string]any{
		"groupId": groupID,
		"candidates": []map[string]any{
			{"modelId": modelA, "weight": 10},
			{"modelId": modelA, "weight": 20},
		},
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "duplicate candidate must fail: %v", body)
	assert.EqualValues(t, 13106, body["code"])

	// AC5: an unknown model id yields 13104.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-groups/"+groupID+"/candidates", map[string]any{
		"groupId": groupID,
		"candidates": []map[string]any{
			{"modelId": "no-such-model", "weight": 10},
		},
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "unknown candidate must fail: %v", body)
	assert.EqualValues(t, 13104, body["code"])

	// AC6: replace rules (keyword + regex, ordered).
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-groups/"+groupID+"/rules", map[string]any{
		"groupId": groupID,
		"rules": []map[string]any{
			{"name": "code", "matchType": "keyword", "pattern": "code", "targetModelId": modelA, "enabled": true},
			{"name": "fast", "matchType": "regex", "pattern": "^quick", "targetModelId": modelB, "enabled": true},
		},
	}, true)
	require.Equal(t, http.StatusOK, code, "replace rules must succeed: %v", body)
	rules, _ := body["rules"].([]any)
	require.Len(t, rules, 2)

	// AC6: a duplicate rule name yields 13112.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-groups/"+groupID+"/rules", map[string]any{
		"groupId": groupID,
		"rules": []map[string]any{
			{"name": "code", "matchType": "keyword", "pattern": "code", "targetModelId": modelA, "enabled": true},
			{"name": "code", "matchType": "keyword", "pattern": "hack", "targetModelId": modelB, "enabled": true},
		},
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "duplicate rule name must fail: %v", body)
	assert.EqualValues(t, 13112, body["code"])

	// AC6: an invalid regex yields 13108.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-groups/"+groupID+"/rules", map[string]any{
		"groupId": groupID,
		"rules": []map[string]any{
			{"name": "bad", "matchType": "regex", "pattern": "([unclosed", "targetModelId": modelA, "enabled": true},
		},
	}, true)
	assert.NotEqual(t, http.StatusOK, code, "invalid regex must fail: %v", body)
	assert.EqualValues(t, 13108, body["code"])

	// AC3: enabling with candidates succeeds.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/routing-groups/"+groupID, map[string]any{
		"groupId": groupID, "name": "auto-chat", "strategy": "weighted", "enabled": true,
	}, true)
	require.Equal(t, http.StatusOK, code, "enable must succeed: %v", body)
	group, _ = body["group"].(map[string]any)
	assert.Equal(t, true, group["enabled"])

	// AC1: the list reports the counts and the 24h request count.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-groups?name=auto", nil, true)
	require.Equal(t, http.StatusOK, code, "list must succeed: %v", body)
	groups, _ := body["groups"].([]any)
	require.Len(t, groups, 1)
	listed, _ := groups[0].(map[string]any)
	assert.Equal(t, "2", fmt.Sprint(listed["candidateCount"]))
	assert.Equal(t, "2", fmt.Sprint(listed["ruleCount"]))
	assert.Equal(t, true, listed["enabled"])

	// AC4: delete is blocked while a grant references the group (13111).
	repo := routing.NewRepository(env.db)
	require.NoError(t, repo.GrantAccess(context.Background(), groupID, "org-fvt", "admin-fvt"))
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/routing-groups/"+groupID, nil, true)
	assert.NotEqual(t, http.StatusOK, code, "delete with grant must fail: %v", body)
	assert.EqualValues(t, 13111, body["code"])

	// AC4: after revoking the grant the delete succeeds.
	require.NoError(t, repo.RevokeAccess(context.Background(), groupID, "org-fvt"))
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/routing-groups/"+groupID, nil, true)
	require.Equal(t, http.StatusOK, code, "delete must succeed: %v", body)

	// AC4: the group is gone (13101).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-groups/"+groupID, nil, true)
	assert.NotEqual(t, http.StatusOK, code, "deleted group must 404: %v", body)
	assert.EqualValues(t, 13101, body["code"])

	// AD6: a plain member session is denied on the admin surface.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-groups", nil, false)
	assert.NotEqual(t, http.StatusOK, code, "plain member must be denied: %v", body)
	assert.EqualValues(t, 10036, body["code"])

	// AD6: an unauthenticated caller is denied.
	env.routingSvc.SetSessionUserResolver(nil)
	code, body = env.callRaw(t, http.MethodGet, "/api/v1/admin/routing-groups", nil)
	assert.NotEqual(t, http.StatusOK, code, "missing session must be denied: %v", body)
	assert.EqualValues(t, 10001, body["code"])
}

// TestFVTRoutingGroupResolutionAndDecisions covers the resolver and the
// decision log (feature #49, AC7-AC8): the dry-run panel, the internal
// resolve RPC's fail-closed codes, the playground virtual path with the
// served-model attribution, and the decision log filters.
func TestFVTRoutingGroupResolutionAndDecisions(t *testing.T) {
	env := newRoutingGroupEnv(t)
	modelA := env.seedCatalogModel(t, "qwen-3b")
	modelB := env.seedCatalogModel(t, "llama-8b")
	env.seedRunningService(t, modelA)
	env.seedRunningService(t, modelB)

	groupID := env.createGroup(t, "auto-chat", "weighted")
	code, body := env.call(t, http.MethodPut, "/api/v1/admin/routing-groups/"+groupID+"/candidates", map[string]any{
		"groupId": groupID,
		"candidates": []map[string]any{
			{"modelId": modelA, "weight": 50},
			{"modelId": modelB, "weight": 50},
		},
	}, true)
	require.Equal(t, http.StatusOK, code, "candidates must succeed: %v", body)
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/routing-groups/"+groupID+"/rules", map[string]any{
		"groupId": groupID,
		"rules": []map[string]any{
			{"name": "code", "matchType": "keyword", "pattern": "code", "targetModelId": modelB, "enabled": true},
		},
	}, true)
	require.Equal(t, http.StatusOK, code, "rules must succeed: %v", body)

	// AC7: the dry run reports the matched rule and the attempt order
	// without writing a decision.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/routing-groups/"+groupID+":resolve", map[string]any{
		"groupId": groupID, "prompt": "please write some code",
	}, true)
	require.Equal(t, http.StatusOK, code, "dry run must succeed: %v", body)
	assert.Equal(t, "code", body["matchedRuleName"])
	attempts, _ := body["attempts"].([]any)
	require.Len(t, attempts, 2)
	first, _ := attempts[0].(map[string]any)
	assert.Equal(t, modelB, first["modelId"], "the rule target must lead the attempt list")

	// AC7: the dry run wrote no decision.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-groups/decisions", nil, true)
	require.Equal(t, http.StatusOK, code)
	decisions, _ := body["decisions"].([]any)
	assert.Empty(t, decisions, "dry run must not write a decision")

	// AD14: the internal resolve RPC fails closed on a disabled group
	// (13110) before the playground path.
	_, err := env.routingSvc.ResolveRoutingGroup(context.Background(), &routingv1.ResolveRoutingGroupRequest{
		GroupName: "auto-chat", Prompt: "hello", OrgId: "org-fvt",
	})
	require.Error(t, err)
	assert.EqualValues(t, 13110, apierrors.CodeOf(err))

	// Enable the group, then resolve through the internal RPC.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/routing-groups/"+groupID, map[string]any{
		"groupId": groupID, "name": "auto-chat", "strategy": "weighted", "enabled": true,
	}, true)
	require.Equal(t, http.StatusOK, code, "enable must succeed: %v", body)

	res, err := env.routingSvc.ResolveRoutingGroup(context.Background(), &routingv1.ResolveRoutingGroupRequest{
		GroupName: "auto-chat", Prompt: "hello", OrgId: "org-fvt",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, res.GetAttemptModelIds())

	// AD14: an unknown group name fails closed with 13101.
	_, err = env.routingSvc.ResolveRoutingGroup(context.Background(), &routingv1.ResolveRoutingGroupRequest{
		GroupName: "no-such-group", Prompt: "hello", OrgId: "org-fvt",
	})
	require.Error(t, err)
	assert.EqualValues(t, 13101, apierrors.CodeOf(err))

	// AC8: the playground serves the virtual model and records the
	// decision with the served-model attribution.
	code, body = env.call(t, http.MethodPost, "/api/v1/models/auto-chat:playground", map[string]any{
		"modelId": "auto-chat", "prompt": "hello world", "apiKey": "sk-fvt",
	}, false)
	require.Equal(t, http.StatusOK, code, "virtual playground must succeed: %v", body)
	assert.NotEmpty(t, body["servedModel"], "the response must name the served model")

	// AC8: the decision log lists the entry with the candidates
	// considered.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-groups/decisions", nil, true)
	require.Equal(t, http.StatusOK, code, "decisions must succeed: %v", body)
	decisions, _ = body["decisions"].([]any)
	require.Len(t, decisions, 1)
	decision, _ := decisions[0].(map[string]any)
	assert.Equal(t, "auto-chat", decision["groupName"])
	assert.Equal(t, "weighted", decision["strategy"])
	assert.NotEmpty(t, decision["selectedModelName"])
	considered, _ := decision["candidatesConsidered"].([]any)
	assert.NotEmpty(t, considered)
	decisionID, _ := decision["decisionId"].(string)
	require.NotEmpty(t, decisionID)

	// AC8: get one decision.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-groups/decisions/"+decisionID, nil, true)
	require.Equal(t, http.StatusOK, code, "get decision must succeed: %v", body)
	decision, _ = body["decision"].(map[string]any)
	assert.Equal(t, decisionID, decision["decisionId"])

	// AC8: the outcome filter narrows the log.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-groups/decisions?outcome=served", nil, true)
	require.Equal(t, http.StatusOK, code)
	decisions, _ = body["decisions"].([]any)
	assert.Len(t, decisions, 1)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-groups/decisions?outcome=failed", nil, true)
	require.Equal(t, http.StatusOK, code)
	decisions, _ = body["decisions"].([]any)
	assert.Empty(t, decisions)

	// AC8: the group filter narrows the log.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/routing-groups/decisions?group_id="+groupID, nil, true)
	require.Equal(t, http.StatusOK, code)
	decisions, _ = body["decisions"].([]any)
	assert.Len(t, decisions, 1)
}

// TestFVTRoutingGroupVirtualModelListing covers the user-surface
// projection (feature #49, AD12): a granted organization sees the
// enabled group as a virtual entry in the available-models list and
// resolves it by id; an ungranted organization does not.
func TestFVTRoutingGroupVirtualModelListing(t *testing.T) {
	env := newRoutingGroupEnv(t)
	modelA := env.seedCatalogModel(t, "qwen-3b")
	env.seedRunningService(t, modelA)

	groupID := env.createGroup(t, "auto-chat", "manual")
	code, body := env.call(t, http.MethodPut, "/api/v1/admin/routing-groups/"+groupID+"/candidates", map[string]any{
		"groupId": groupID,
		"candidates": []map[string]any{
			{"modelId": modelA, "weight": 10, "pinned": true},
		},
	}, true)
	require.Equal(t, http.StatusOK, code, "candidates must succeed: %v", body)
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/routing-groups/"+groupID, map[string]any{
		"groupId": groupID, "name": "auto-chat", "strategy": "manual", "enabled": true,
	}, true)
	require.Equal(t, http.StatusOK, code, "enable must succeed: %v", body)

	// Default-allow (AD12): with zero grants the group is visible to
	// every organization, so the virtual entry already appears.
	code, body = env.call(t, http.MethodGet, "/api/v1/models", nil, false)
	require.Equal(t, http.StatusOK, code, "list models must succeed: %v", body)
	models, _ := body["models"].([]any)
	var virtual map[string]any
	for _, raw := range models {
		m, _ := raw.(map[string]any)
		if m["virtual"] == true {
			virtual = m
		}
	}
	require.NotNil(t, virtual, "a zero-grant group must appear as a virtual entry")
	assert.Equal(t, "auto-chat", virtual["name"])

	// Restricting the group to another org hides it from this org.
	repo := routing.NewRepository(env.db)
	require.NoError(t, repo.GrantAccess(context.Background(), groupID, "org-other", "admin-fvt"))
	code, body = env.call(t, http.MethodGet, "/api/v1/models", nil, false)
	require.Equal(t, http.StatusOK, code)
	models, _ = body["models"].([]any)
	for _, raw := range models {
		m, _ := raw.(map[string]any)
		assert.NotEqual(t, "auto-chat", m["name"], "a group granted to another org must not appear")
	}

	// Granting the group to this org restores the virtual entry.
	require.NoError(t, repo.GrantAccess(context.Background(), groupID, "org-fvt", "admin-fvt"))
	code, body = env.call(t, http.MethodGet, "/api/v1/models", nil, false)
	require.Equal(t, http.StatusOK, code)
	models, _ = body["models"].([]any)
	virtual = nil
	for _, raw := range models {
		m, _ := raw.(map[string]any)
		if m["virtual"] == true {
			virtual = m
		}
	}
	require.NotNil(t, virtual, "the granted group must appear as a virtual entry")
	assert.Equal(t, "auto-chat", virtual["name"])

	// The virtual entry resolves by id on the user surface.
	code, body = env.call(t, http.MethodGet, "/api/v1/models/"+groupID, nil, false)
	require.Equal(t, http.StatusOK, code, "get virtual model must succeed: %v", body)
	m, _ := body["model"].(map[string]any)
	assert.Equal(t, "auto-chat", m["name"])
	assert.Equal(t, true, m["virtual"])
}

// fvtGatewayResolver points the infer service at a fake serving gateway.
type fvtGatewayResolver struct{ url string }

func (r fvtGatewayResolver) InferenceGatewayURL(context.Context) string { return r.url }
