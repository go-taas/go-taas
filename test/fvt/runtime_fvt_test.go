// Feature #47 (distributed inference runtime) full-verification suite: the
// seven admin RPCs of taas.runtime.v1.RuntimeService over one gRPC server
// with the gateway mux in front, against a fake runtime.health snapshot
// (the projection cache) and a recording MQ bus. It exercises the
// acceptance criteria of docs/architecture/distributed-inference-runtime.md
// (AC1–AC6, AC8, AC9, AC10, AC11).

package fvt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	gatewayruntime "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	runtimev1 "github.com/go-taas/go-taas/proto/taas/runtime/v1"
	"github.com/go-taas/go-taas/services/audit"
	runtimesvc "github.com/go-taas/go-taas/services/runtime"
)

// runtimeFvtEnv is the in-process stack for the runtime feature: the
// runtime service over one gRPC server, the gateway mux wrapped in the
// production RealmGuard, a shared SQLite for the served scheme, the real
// runtime.health consumer over one projection cache, and a recording MQ
// bus that doubles as the controller's snapshot publisher.
type runtimeFvtEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server
	mqBus *recordingBus
	cache *runtimesvc.ProjectionCache
	svc   *runtimesvc.Service
	audit *runtimeFvtAudit
	realm *runtimeFvtRealmResolver
}

// runtimeFvtAudit records the runtime module's audit events.
type runtimeFvtAudit struct {
	mu     sync.Mutex
	events []*audit.AuditEvent
}

func (a *runtimeFvtAudit) Record(_ context.Context, ev *audit.AuditEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, ev)
}

func (a *runtimeFvtAudit) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.events))
	for _, ev := range a.events {
		out = append(out, ev.Action)
	}
	return out
}

// runtimeFvtRealmResolver maps a bearer token to its session realm.
type runtimeFvtRealmResolver struct {
	realms map[string]string
}

func (r *runtimeFvtRealmResolver) SessionRealm(_ context.Context, token string) (string, error) {
	realm, ok := r.realms[token]
	if !ok {
		return "", errors.New("unknown session")
	}
	return realm, nil
}

// runtimeFvtRoleGuard denies (or allows) every role check.
type runtimeFvtRoleGuard struct{ allowed bool }

func (g runtimeFvtRoleGuard) RequireRole(context.Context, string, string, string) error {
	if !g.allowed {
		return apierrors.New(apierrors.CodeForbidden)
	}
	return nil
}

func newRuntimeFvtEnv(t *testing.T) *runtimeFvtEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, runtimesvc.MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	bus := newRecordingBus()
	// The service and the health consumer share one projection cache,
	// exactly as the production wiring does (the consumer replaces it,
	// the service reads it).
	cache := runtimesvc.NewProjectionCache()
	svc := runtimesvc.NewForFVT(db, cache)
	svc.SetPublisher(bus)
	rec := &runtimeFvtAudit{}
	svc.SetAuditRecorder(rec)

	// Run the real snapshot consumer against the bus so published
	// snapshots flow through the production path into the cache. The
	// recording bus's Subscribe returns immediately, so Run returns after
	// registering the handler.
	consumer := runtimesvc.NewHealthConsumer(bus, cache)
	require.NoError(t, consumer.Run(context.Background()))

	runtimev1.RegisterRuntimeServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := gatewayruntime.NewServeMux(
		gatewayruntime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, runtimev1.RegisterRuntimeServiceHandler(context.Background(), mux, conn))

	realm := &runtimeFvtRealmResolver{realms: map[string]string{}}
	gwSrv := httptest.NewServer(server.RealmGuard(mux, realm))
	t.Cleanup(gwSrv.Close)

	return &runtimeFvtEnv{db: db, gwSrv: gwSrv, mqBus: bus, cache: cache, svc: svc, audit: rec, realm: realm}
}

// publish publishes a full runtime.health snapshot exactly as the
// controller's collector does (AD4).
func (e *runtimeFvtEnv) publish(t *testing.T, snap runtimesvc.HealthSnapshot) {
	t.Helper()
	if snap.ReportedAt.IsZero() {
		snap.ReportedAt = time.Now().UTC()
	}
	body, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, e.mqBus.Publish(context.Background(), mq.DefaultSubjects().RuntimeHealth, body, nil))
}

// seedScheme persists the serving scheme directly (the admin RPC is
// covered by its own test).
func (e *runtimeFvtEnv) seedScheme(t *testing.T, scheme string) {
	t.Helper()
	_, err := runtimesvc.NewRuntimeRepository(e.db).UpdateScheme(context.Background(), scheme, "fvt")
	require.NoError(t, err)
}

// call issues a JSON request against the gateway and decodes the body.
func (e *runtimeFvtEnv) call(t *testing.T, method, path string, body any, headers map[string]string) (int, map[string]any) {
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

// aibrixSnapshot builds an available aibrix snapshot with the five
// control-plane components, the Envoy gateway, and the given models.
func aibrixSnapshot(models ...runtimesvc.RegisteredModel) runtimesvc.HealthSnapshot {
	components := make([]runtimesvc.Component, 0, 6)
	for _, name := range []string{
		"aibrix-controller-manager", "aibrix-gateway-plugins", "aibrix-gpu-optimizer",
		"aibrix-metadata-service", "aibrix-redis-master",
	} {
		components = append(components, runtimesvc.Component{
			ComponentID: name, Name: name, Kind: runtimesvc.KindControlPlane,
			Namespace: "aibrix-system", Status: "ready", ReadyReplicas: 1, DesiredReplicas: 1,
		})
	}
	components = append(components, runtimesvc.Component{
		ComponentID: "envoy-aibrix-system-aibrix-eg", Name: "envoy-aibrix-system-aibrix-eg",
		Kind: runtimesvc.KindGateway, Namespace: "aibrix-system", Status: "ready",
		ReadyReplicas: 1, DesiredReplicas: 1,
	})
	return runtimesvc.HealthSnapshot{
		Scheme:       runtimesvc.SchemeAIBrix,
		Availability: runtimesvc.AvailabilityAvailable,
		RuntimeName:  runtimesvc.SchemeAIBrix,
		ClusterName:  "sd4090",
		Components:   components,
		Models:       models,
		ReportedAt:   time.Now().UTC(),
	}
}

// plainFallbackSnapshot builds an available plain-scheme snapshot with the
// fallback components (AD4, D10).
func plainFallbackSnapshot() runtimesvc.HealthSnapshot {
	return runtimesvc.HealthSnapshot{
		Scheme:       runtimesvc.SchemePlain,
		Availability: runtimesvc.AvailabilityAvailable,
		RuntimeName:  runtimesvc.SchemePlain,
		Components: []runtimesvc.Component{
			{ComponentID: "go-taas-controller", Name: "go-taas-controller", Kind: runtimesvc.KindFallback, Status: "ready", ReadyReplicas: 1, DesiredReplicas: 1},
			{ComponentID: "kubernetes-api", Name: "kubernetes-api", Kind: runtimesvc.KindFallback, Status: "ready"},
		},
		Models:     []runtimesvc.RegisteredModel{},
		ReportedAt: time.Now().UTC(),
	}
}

// TestRuntimeFVTStatusAndComponents covers AC1 and AC2: the overview joins
// the persisted scheme with the cached snapshot, and the component table
// lists the five AIBrix control-plane components plus the Envoy gateway.
func TestRuntimeFVTStatusAndComponents(t *testing.T) {
	env := newRuntimeFvtEnv(t)
	env.seedScheme(t, runtimesvc.SchemeAIBrix)
	env.publish(t, aibrixSnapshot())

	code, body := env.call(t, http.MethodGet, "/api/v1/admin/runtime", nil, nil)
	require.Equal(t, http.StatusOK, code)
	overview, ok := body["runtime"].(map[string]any)
	require.True(t, ok, "response has no runtime object: %v", body)
	assert.Equal(t, runtimesvc.SchemeAIBrix, overview["scheme"])
	assert.Equal(t, runtimesvc.AvailabilityAvailable, overview["availability"])
	assert.Equal(t, "sd4090", overview["clusterName"])
	assert.Len(t, overview["components"], 6)

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/runtime/components", nil, nil)
	require.Equal(t, http.StatusOK, code)
	components, ok := body["components"].([]any)
	require.True(t, ok)
	assert.Len(t, components, 6)
}

// TestRuntimeFVTPlainFallbackComponents covers AC11: under the plain
// scheme the fallback components are served and the model list is empty.
func TestRuntimeFVTPlainFallbackComponents(t *testing.T) {
	env := newRuntimeFvtEnv(t)
	env.publish(t, plainFallbackSnapshot())

	code, body := env.call(t, http.MethodGet, "/api/v1/admin/runtime/components", nil, nil)
	require.Equal(t, http.StatusOK, code)
	components, ok := body["components"].([]any)
	require.True(t, ok)
	require.Len(t, components, 2)
	for _, c := range components {
		comp, ok := c.(map[string]any)
		require.True(t, ok)
		assert.Equal(t, runtimesvc.KindFallback, comp["kind"])
	}

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/runtime/models", nil, nil)
	require.Equal(t, http.StatusOK, code)
	models, ok := body["models"].([]any)
	require.True(t, ok)
	assert.Empty(t, models)
}

// TestRuntimeFVTSchemeSwitcher covers AC3, AC10 and AC11: switching is
// audited, confirmed and idempotent; switching to aibrix while the
// runtime is unavailable returns 12901; an unknown scheme returns 12902.
func TestRuntimeFVTSchemeSwitcher(t *testing.T) {
	env := newRuntimeFvtEnv(t)
	env.publish(t, aibrixSnapshot())

	code, body := env.call(t, http.MethodGet, "/api/v1/admin/runtime/scheme", nil, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, runtimesvc.SchemePlain, body["scheme"].(map[string]any)["scheme"])

	code, body = env.call(t, http.MethodPut, "/api/v1/admin/runtime/scheme", map[string]any{"scheme": runtimesvc.SchemeAIBrix}, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, runtimesvc.SchemeAIBrix, body["scheme"].(map[string]any)["scheme"])
	assert.Equal(t, []string{"runtime.scheme_changed"}, env.audit.actions())

	// Idempotent: reapplying the current scheme is a no-op with no audit.
	code, _ = env.call(t, http.MethodPut, "/api/v1/admin/runtime/scheme", map[string]any{"scheme": runtimesvc.SchemeAIBrix}, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []string{"runtime.scheme_changed"}, env.audit.actions())

	// An unknown scheme is rejected with 12902.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/runtime/scheme", map[string]any{"scheme": "bogus"}, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeRuntimeSchemeInvalid, body["code"])
}

// TestRuntimeFVTSwitchToAibrixUnavailable covers AC11: switching to aibrix
// while the runtime is not observed returns 12901.
func TestRuntimeFVTSwitchToAibrixUnavailable(t *testing.T) {
	env := newRuntimeFvtEnv(t)

	code, body := env.call(t, http.MethodPut, "/api/v1/admin/runtime/scheme", map[string]any{"scheme": runtimesvc.SchemeAIBrix}, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeRuntimeUnavailable, body["code"])
}

// TestRuntimeFVTModels covers AC4: the model table supports name search,
// engine/status filters and pagination.
func TestRuntimeFVTModels(t *testing.T) {
	env := newRuntimeFvtEnv(t)
	env.seedScheme(t, runtimesvc.SchemeAIBrix)
	env.publish(t, aibrixSnapshot(
		runtimesvc.RegisteredModel{ModelName: "alpha", Engine: "vllm", Replicas: 1, Status: "ready"},
		runtimesvc.RegisteredModel{ModelName: "beta", Engine: "sglang", Replicas: 2, Status: "progressing"},
		runtimesvc.RegisteredModel{ModelName: "gamma", Engine: "vllm", Replicas: 1, Status: "ready"},
	))

	code, body := env.call(t, http.MethodGet, "/api/v1/admin/runtime/models?engine=vllm&page.limit=1", nil, nil)
	require.Equal(t, http.StatusOK, code)
	models, ok := body["models"].([]any)
	require.True(t, ok)
	assert.Len(t, models, 1)
	assert.Equal(t, "2", body["pageMeta"].(map[string]any)["total"])

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/runtime/models?name=amm", nil, nil)
	require.Equal(t, http.StatusOK, code)
	models, _ = body["models"].([]any)
	require.Len(t, models, 1)
	assert.Equal(t, "gamma", models[0].(map[string]any)["modelName"])
}

// TestRuntimeFVTRegisterAndDeregister covers AC5, AC6 and AC10: the
// register validation codes, the desired-state publish, the deregister
// confirmation and the audit events.
func TestRuntimeFVTRegisterAndDeregister(t *testing.T) {
	env := newRuntimeFvtEnv(t)
	env.seedScheme(t, runtimesvc.SchemeAIBrix)
	env.publish(t, aibrixSnapshot(runtimesvc.RegisteredModel{ModelName: "alpha", Engine: "vllm", Status: "ready"}))

	// Validation codes.
	cases := []struct {
		req      map[string]any
		wantCode apierrors.Code
	}{
		{map[string]any{"modelName": "Bad Name", "engine": "vllm", "replicas": 1, "artifactUrl": "/w"}, apierrors.CodeRuntimeModelNameInvalid},
		{map[string]any{"modelName": "m1", "engine": "tgi", "replicas": 1, "artifactUrl": "/w"}, apierrors.CodeRuntimeEngineUnsupported},
		{map[string]any{"modelName": "m1", "engine": "vllm", "replicas": 99, "artifactUrl": "/w"}, apierrors.CodeRuntimeReplicasInvalid},
		{map[string]any{"modelName": "m1", "engine": "vllm", "replicas": 1}, apierrors.CodeRuntimeModelNameInvalid},
		{map[string]any{"modelName": "alpha", "engine": "vllm", "replicas": 1, "artifactUrl": "/w"}, apierrors.CodeRuntimeModelExists},
	}
	for _, tc := range cases {
		code, body := env.call(t, http.MethodPost, "/api/v1/admin/runtime/models", tc.req, nil)
		assert.Equal(t, http.StatusInternalServerError, code, "payload %v", tc.req)
		assert.EqualValues(t, tc.wantCode, body["code"], "payload %v", tc.req)
	}

	// A valid registration publishes the upsert and audits.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/runtime/models", map[string]any{
		"modelName": "qwen2", "engine": "sglang", "replicas": 2, "artifactUrl": "/data/w",
	}, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "qwen2", body["model"].(map[string]any)["modelName"])
	assert.Contains(t, env.audit.actions(), "runtime.model_registered")

	// Deregister: unknown → 12903, known → 200 + audit.
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/runtime/models/missing", nil, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeRuntimeModelNotFound, body["code"])

	code, _ = env.call(t, http.MethodDelete, "/api/v1/admin/runtime/models/alpha", nil, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, env.audit.actions(), "runtime.model_deregistered")
}

// TestRuntimeFVTPlainSchemeWritesRejected covers AD12/AC11: under the
// plain scheme register and deregister return 12901.
func TestRuntimeFVTPlainSchemeWritesRejected(t *testing.T) {
	env := newRuntimeFvtEnv(t)
	env.publish(t, plainFallbackSnapshot())

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/runtime/models", map[string]any{
		"modelName": "qwen2", "engine": "vllm", "replicas": 1, "artifactUrl": "/data/w",
	}, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeRuntimeUnavailable, body["code"])

	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/runtime/models/alpha", nil, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeRuntimeUnavailable, body["code"])
}

// TestRuntimeFVTRealmSeparation covers AC8: a valid user-realm session on
// the admin prefix is rejected with 10038.
func TestRuntimeFVTRealmSeparation(t *testing.T) {
	env := newRuntimeFvtEnv(t)
	env.realm.realms["user-token"] = "user"
	env.realm.realms["admin-token"] = "admin"

	code, body := env.call(t, http.MethodGet, "/api/v1/admin/runtime", nil,
		map[string]string{"Authorization": "Bearer user-token"})
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeRealmMismatch, body["code"])

	// The admin-realm session passes the guard and reaches the service.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/runtime", nil,
		map[string]string{"Authorization": "Bearer admin-token"})
	require.Equal(t, http.StatusOK, code)
	_, ok := body["runtime"].(map[string]any)
	assert.True(t, ok)
}

// TestRuntimeFVTRoleDenied covers AC9: a session without the admin role
// receives 10036.
func TestRuntimeFVTRoleDenied(t *testing.T) {
	env := newRuntimeFvtEnv(t)
	env.svc.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-1"})
	env.svc.SetSessionUserResolver(fvtSessionUserResolver{user: "user-1"})
	env.svc.SetRoleGuard(runtimeFvtRoleGuard{allowed: false})

	code, body := env.call(t, http.MethodGet, "/api/v1/admin/runtime", nil, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeForbidden, body["code"])
}

// TestRuntimeFVTPlatformSettings covers the operator-editable data-plane
// endpoint: the round trip through the admin API, the normalisation, the
// rejection of a malformed hostname (12908), and the audit trail — the
// value decides where every tenant's inference traffic goes.
func TestRuntimeFVTPlatformSettings(t *testing.T) {
	env := newRuntimeFvtEnv(t)

	code, body := env.call(t, http.MethodGet, "/api/v1/admin/platform-settings", nil, nil)
	require.Equal(t, http.StatusOK, code)
	settings, ok := body["settings"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "", settings["inferenceGatewayHostname"])
	assert.Equal(t, "https", settings["inferenceGatewayScheme"])
	assert.Equal(t, "", settings["inferenceBaseUrl"])

	// Saving composes the URL users are handed.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/platform-settings", map[string]any{
		"inferenceGatewayHostname": "API.Sudoinfotech.com",
		"inferenceGatewayScheme":   "https",
	}, nil)
	require.Equal(t, http.StatusOK, code, "save must succeed: %v", body)
	settings = body["settings"].(map[string]any)
	assert.Equal(t, "api.sudoinfotech.com", settings["inferenceGatewayHostname"])
	assert.Equal(t, "https://api.sudoinfotech.com/v1", settings["inferenceBaseUrl"])
	assert.Equal(t, []string{"runtime.platform_settings_changed"}, env.audit.actions())

	// The value is durable.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/platform-settings", nil, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "https://api.sudoinfotech.com/v1", body["settings"].(map[string]any)["inferenceBaseUrl"])

	// A hostname carrying a scheme is rejected and changes nothing.
	code, body = env.call(t, http.MethodPut, "/api/v1/admin/platform-settings", map[string]any{
		"inferenceGatewayHostname": "https://api.example.com/v1",
	}, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeRuntimeSettingInvalid, body["code"])

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/platform-settings", nil, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "https://api.sudoinfotech.com/v1", body["settings"].(map[string]any)["inferenceBaseUrl"],
		"a rejected value must not be stored")
}
