// Feature #48 (external model providers) full-verification suite: the
// twelve admin RPCs of taas.provider.v1.ProviderService over one gRPC
// server with the production RealmGuard in front, the model module's
// publish/projection seam, the five-state connectivity probe against real
// local HTTP servers, and the infer.PlaygroundModel external proxy seam
// with metering. It exercises the acceptance criteria of
// docs/architecture/external-model-providers.md (AC1–AC14).

package fvt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gatewayruntime "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	providerv1 "github.com/go-taas/go-taas/proto/taas/provider/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/model"
	providersvc "github.com/go-taas/go-taas/services/provider"
	"github.com/go-taas/go-taas/services/tenancy"
)

// providerFvtEnv is the in-process stack for feature #48.
type providerFvtEnv struct {
	db       *gorm.DB
	gwSrv    *httptest.Server
	bus      *recordingBus
	svc      *providersvc.Service
	modelSvc *model.Service
	inferSvc *infer.Service
	audit    *runtimeFvtAudit
	realm    *runtimeFvtRealmResolver

	// okServer serves GET /models -> 200 and POST /chat/completions -> 200.
	okServer *httptest.Server
	// unauthServer serves GET /models -> 401 and POST /chat/completions ->
	// 401 (the upstream-unauthorized path).
	unauthServer *httptest.Server
	// unavailServer serves GET /models -> 503 and POST /chat/completions ->
	// 503 (the upstream-unavailable path).
	unavailServer *httptest.Server
}

func newProviderFvtEnv(t *testing.T) *providerFvtEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, providersvc.MigrateSchemaForFVT(db))
	require.NoError(t, db.Create(&tenancy.Organization{ID: "org-fvt", DisplayName: "org-fvt", State: tenancy.StateActive}).Error)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// A real OpenAI-compatible external service (test infrastructure): the
	// probe GET /models and the proxied POST /chat/completions.
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o"}]}`))
		case "/chat/completions":
			assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"The answer is 42."}}],"usage":{"prompt_tokens":11,"completion_tokens":6}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(okServer.Close)

	unauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(unauthServer.Close)

	unavailServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(unavailServer.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	bus := newRecordingBus()
	modelSvc := model.NewForFVT(db)
	svc := providersvc.NewForFVT(db)
	svc.SetModelCatalog(modelSvc)
	svc.SetPublisher(bus)
	modelSvc.SetProviderNameResolver(svc)
	rec := &runtimeFvtAudit{}
	svc.SetAuditRecorder(rec)

	inferSvc := infer.NewForFVT(db, bus)
	inferSvc.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	inferSvc.SetSessionUserResolver(fvtSessionUserResolver{user: "user-fvt"})
	inferSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	inferSvc.SetExternalModelProxy(svc)

	providerv1.RegisterProviderServiceServer(grpcSrv, svc)
	modelv1.RegisterModelServiceServer(grpcSrv, modelSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := gatewayruntime.NewServeMux(
		gatewayruntime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, providerv1.RegisterProviderServiceHandler(context.Background(), mux, conn))
	require.NoError(t, modelv1.RegisterModelServiceHandler(context.Background(), mux, conn))

	realm := &runtimeFvtRealmResolver{realms: map[string]string{}}
	gwSrv := httptest.NewServer(server.RealmGuard(mux, realm))
	t.Cleanup(gwSrv.Close)

	return &providerFvtEnv{
		db: db, gwSrv: gwSrv, bus: bus, svc: svc, modelSvc: modelSvc, inferSvc: inferSvc,
		audit: rec, realm: realm,
		okServer: okServer, unauthServer: unauthServer, unavailServer: unavailServer,
	}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *providerFvtEnv) call(t *testing.T, method, path string, body any, headers map[string]string) (int, map[string]any) {
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

// createProvider registers a provider via the admin API and returns its id.
func (e *providerFvtEnv) createProvider(t *testing.T, name, baseURL string, enabled bool) string {
	t.Helper()
	code, body := e.call(t, http.MethodPost, "/api/v1/admin/providers", map[string]any{
		"name": name, "type": "openai_compatible", "baseUrl": baseURL,
		"apiKey": "sk-test-abcdef123456", "enabled": enabled,
	}, nil)
	require.Equal(t, http.StatusOK, code, "create provider: %v", body)
	provider, _ := body["provider"].(map[string]any)
	require.NotNil(t, provider)
	return provider["providerId"].(string)
}

// addAndPublish adds a curated row and publishes it, returning the provider
// model id and catalog model id.
func (e *providerFvtEnv) addAndPublish(t *testing.T, providerID, upstream, tenantName string) (string, string) {
	t.Helper()
	code, body := e.call(t, http.MethodPost, "/api/v1/admin/providers/"+providerID+"/models", map[string]any{
		"upstreamModelId": upstream, "enabled": true,
	}, nil)
	require.Equal(t, http.StatusOK, code, "add model: %v", body)
	pm, _ := body["model"].(map[string]any)
	modelID := pm["modelId"].(string)

	code, body = e.call(t, http.MethodPost, "/api/v1/admin/providers/"+providerID+"/models/"+modelID+":publish",
		map[string]any{"tenantFacingName": tenantName}, nil)
	require.Equal(t, http.StatusOK, code, "publish: %v", body)
	catalogID, _ := body["catalogModelId"].(string)
	return modelID, catalogID
}

// TestProviderFVTCRUDAndProbe covers AC1, AC2, AC3, AC4 and AC14.
func TestProviderFVTCRUDAndProbe(t *testing.T) {
	env := newProviderFvtEnv(t)

	id := env.createProvider(t, "acme", env.okServer.URL, false)

	// AC1: list.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/providers", nil, nil)
	require.Equal(t, http.StatusOK, code)
	providers, _ := body["providers"].([]any)
	require.Len(t, providers, 1)

	// AC3: the read carries only the masked hint.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/providers/"+id, nil, nil)
	require.Equal(t, http.StatusOK, code)
	provider, _ := body["provider"].(map[string]any)
	assert.Equal(t, "••••3456", provider["apiKeyHint"])
	raw, _ := json.Marshal(body)
	assert.NotContains(t, string(raw), "sk-test-abcdef123456")

	// AC4: a reachable endpoint is ok.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/providers/"+id+":test", nil, nil)
	require.Equal(t, http.StatusOK, code, "%v", body)
	probe, _ := body["probe"].(map[string]any)
	assert.Equal(t, "ok", probe["status"])
	assert.EqualValues(t, 200, probe["httpStatus"])

	// AC4: a real endpoint rejecting credentials is unauthorized, never
	// unreachable.
	unauthID := env.createProvider(t, "unauth", env.unauthServer.URL, false)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/providers/"+unauthID+":test", nil, nil)
	require.Equal(t, http.StatusOK, code)
	probe, _ = body["probe"].(map[string]any)
	assert.Equal(t, "unauthorized", probe["status"])
	assert.EqualValues(t, 401, probe["httpStatus"])

	// AC4: an unroutable host is unreachable with http_status 0.
	deadID := env.createProvider(t, "dead", "http://127.0.0.1:1", false)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/providers/"+deadID+":test", nil, nil)
	require.Equal(t, http.StatusOK, code)
	probe, _ = body["probe"].(map[string]any)
	assert.Equal(t, "unreachable", probe["status"])
	assert.EqualValues(t, 0, probe["httpStatus"])

	// AC8: enabling a provider whose last probe is unauthorized is blocked.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/providers/"+unauthID, map[string]any{
		"name": "unauth", "type": "openai_compatible", "baseUrl": env.unauthServer.URL, "enabled": true,
	}, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeProviderUpstreamUnauthorized, body["code"])

	// AC3: rotating the key changes the hint; an empty key keeps it.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/providers/"+id, map[string]any{
		"name": "acme", "type": "openai_compatible", "baseUrl": env.okServer.URL,
	}, nil)
	require.Equal(t, http.StatusOK, code)
	provider, _ = body["provider"].(map[string]any)
	assert.Equal(t, "••••3456", provider["apiKeyHint"])
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/providers/"+id, map[string]any{
		"name": "acme", "type": "openai_compatible", "baseUrl": env.okServer.URL, "apiKey": "sk-rotated987654",
	}, nil)
	require.Equal(t, http.StatusOK, code)
	provider, _ = body["provider"].(map[string]any)
	assert.Equal(t, "••••7654", provider["apiKeyHint"])

	// AC14: audit events were emitted with no key material.
	actions := env.audit.actions()
	assert.Contains(t, actions, "provider.created")
	assert.Contains(t, actions, "provider.key_rotated")
	for _, ev := range env.audit.events {
		assert.NotContains(t, ev.Metadata, "sk-")
	}
}

// TestProviderFVTCurationAndPublish covers AC5, AC6, AC7, AC8 and AC14.
func TestProviderFVTCurationAndPublish(t *testing.T) {
	env := newProviderFvtEnv(t)
	id := env.createProvider(t, "curate", env.okServer.URL, true)

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/providers/"+id+"/models",
		map[string]any{"upstreamModelId": "gpt-4o", "displayName": "GPT-4o", "contextWindow": 128000, "enabled": true}, nil)
	require.Equal(t, http.StatusOK, code, "%v", body)
	pm, _ := body["model"].(map[string]any)
	modelID := pm["modelId"].(string)

	// AC5: duplicate upstream id.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/providers/"+id+"/models",
		map[string]any{"upstreamModelId": "gpt-4o", "enabled": true}, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeProviderModelExists, body["code"])

	// AC6: publish.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/providers/"+id+"/models/"+modelID+":publish",
		map[string]any{"tenantFacingName": "gpt-4o"}, nil)
	require.Equal(t, http.StatusOK, code, "%v", body)
	catalogID, _ := body["catalogModelId"].(string)
	require.NotEmpty(t, catalogID)

	// AC6: a live name conflict.
	addCode, addBody := env.call(t, http.MethodPost, "/api/v1/admin/providers/"+id+"/models",
		map[string]any{"upstreamModelId": "gpt-4o-mini", "enabled": true}, nil)
	require.Equal(t, http.StatusOK, addCode)
	pm2, _ := addBody["model"].(map[string]any)
	secondID := pm2["modelId"].(string)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/providers/"+id+"/models/"+secondID+":publish",
		map[string]any{"tenantFacingName": "gpt-4o"}, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeProviderPublishConflict, body["code"])

	// AC8: a published row cannot be removed; the provider cannot be deleted.
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/providers/"+id+"/models/"+modelID, nil, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeProviderInUse, body["code"])
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/providers/"+id, nil, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeProviderInUse, body["code"])

	// AC6: the admin catalog shows the external marker + provider fields.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models", nil, nil)
	require.Equal(t, http.StatusOK, code)
	summary := findModel(t, body["models"], catalogID)
	require.NotNil(t, summary)
	assert.Equal(t, true, summary["external"])
	assert.Equal(t, "curate", summary["providerName"])
	assert.Equal(t, id, summary["providerId"])
	assert.Equal(t, "gpt-4o", summary["upstreamModelId"])

	// AC7: unpublish hides it from the tenant catalog and fails closed.
	code, _ = env.call(t, http.MethodPost, "/api/v1/admin/providers/"+id+"/models/"+modelID+":unpublish", nil, nil)
	require.Equal(t, http.StatusOK, code)
	code, body = env.call(t, http.MethodGet, "/api/v1/models", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code)
	assert.Nil(t, findModel(t, body["models"], catalogID), "an unpublished model must be hidden from the tenant catalog")

	// AC7: the resolved proxy fails closed with 13015.
	_, err := env.inferSvc.PlaygroundModel(context.Background(), &inferv1.PlaygroundModelRequest{ModelId: catalogID, Prompt: "hi"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderNotPublished, apierrors.CodeOf(err))

	// Re-publish restores visibility.
	_, _ = env.call(t, http.MethodPost, "/api/v1/admin/providers/"+id+"/models/"+modelID+":publish",
		map[string]any{"tenantFacingName": "gpt-4o"}, nil)

	// AC14: publish/unpublish audit events.
	actions := env.audit.actions()
	assert.Contains(t, actions, "provider.model_added")
	assert.Contains(t, actions, "provider.model_published")
	assert.Contains(t, actions, "provider.model_unpublished")
}

// TestProviderFVTTenantMarker covers AC11: the masked user projection
// carries external + provider_name and never the provider internals.
func TestProviderFVTTenantMarker(t *testing.T) {
	env := newProviderFvtEnv(t)
	id := env.createProvider(t, "tenantmark", env.okServer.URL, true)
	_, catalogID := env.addAndPublish(t, id, "gpt-4o", "external-gpt-4o")

	code, body := env.call(t, http.MethodGet, "/api/v1/models", nil, map[string]string{"X-Organization-Id": "org-fvt"})
	require.Equal(t, http.StatusOK, code)
	summary := findModel(t, body["models"], catalogID)
	require.NotNil(t, summary)
	assert.Equal(t, true, summary["external"])
	assert.Equal(t, "tenantmark", summary["providerName"])
	// The masked projection never carries the admin-only fields.
	raw, _ := json.Marshal(summary)
	assert.NotContains(t, string(raw), "providerId")
	assert.NotContains(t, string(raw), "upstreamModelId")
	assert.NotContains(t, string(raw), env.okServer.URL)
}

// TestProviderFVTExternalConsumptionAndMetering covers AC9 and AC10: a real
// external HTTP service is consumed through infer.PlaygroundModel on the
// metered path, and upstream failures map to distinct fail-closed codes.
func TestProviderFVTExternalConsumptionAndMetering(t *testing.T) {
	env := newProviderFvtEnv(t)

	// AC9: successful external consumption with metering.
	okID := env.createProvider(t, "okprov", env.okServer.URL, true)
	_, okCatalog := env.addAndPublish(t, okID, "gpt-4o", "ok-gpt-4o")
	resp, err := env.inferSvc.PlaygroundModel(context.Background(), &inferv1.PlaygroundModelRequest{ModelId: okCatalog, Prompt: "answer?"})
	require.NoError(t, err)
	assert.Contains(t, resp.GetCompletion(), "42")
	assert.EqualValues(t, 11, resp.GetPromptTokens())
	assert.EqualValues(t, 6, resp.GetCompletionTokens())

	// AC9: the metered event was emitted for the external call.
	require.NotEmpty(t, env.bus.meter)
	var event map[string]any
	require.NoError(t, json.Unmarshal(env.bus.meter[0].Body, &event))
	assert.Equal(t, okCatalog, event["model_id"])
	assert.Equal(t, "org-fvt", event["organization_id"])
	assert.Equal(t, "success", event["status"])

	// AC10: a disabled provider fails closed with 13011.
	disabledID := env.createProvider(t, "disabledprov", env.okServer.URL, false)
	_, disabledCatalog := env.addAndPublish(t, disabledID, "gpt-4o", "disabled-gpt-4o")
	_, err = env.inferSvc.PlaygroundModel(context.Background(), &inferv1.PlaygroundModelRequest{ModelId: disabledCatalog, Prompt: "hi"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderDisabled, apierrors.CodeOf(err))

	// AC10: an upstream 401 maps to 13012.
	unauthID := env.createProvider(t, "upstreamauth", env.unauthServer.URL, true)
	_, unauthCatalog := env.addAndPublish(t, unauthID, "gpt-4o", "unauth-gpt-4o")
	_, err = env.inferSvc.PlaygroundModel(context.Background(), &inferv1.PlaygroundModelRequest{ModelId: unauthCatalog, Prompt: "hi"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderUpstreamUnauthorized, apierrors.CodeOf(err))

	// AC10: an upstream 503 maps to 13013.
	unavailID := env.createProvider(t, "upstreamdown", env.unavailServer.URL, true)
	_, unavailCatalog := env.addAndPublish(t, unavailID, "gpt-4o", "unavail-gpt-4o")
	_, err = env.inferSvc.PlaygroundModel(context.Background(), &inferv1.PlaygroundModelRequest{ModelId: unavailCatalog, Prompt: "hi"})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeProviderUpstreamUnavailable, apierrors.CodeOf(err))

	// AC10: upstream calls are metered on failure too (the disabled case
	// fails closed before any upstream call, so it emits nothing).
	require.GreaterOrEqual(t, len(env.bus.meter), 3)
	var sawError bool
	for _, msg := range env.bus.meter {
		var ev map[string]any
		if json.Unmarshal(msg.Body, &ev) == nil && ev["status"] == "error" {
			sawError = true
		}
	}
	assert.True(t, sawError, "an upstream failure must emit an error metering event")
}

// TestProviderFVTRealmAndRole covers AC12 and AC13.
func TestProviderFVTRealmAndRole(t *testing.T) {
	env := newProviderFvtEnv(t)
	env.realm.realms["user-token"] = "user"
	env.realm.realms["admin-token"] = "admin"

	// AC12: a user-realm session on the admin prefix is rejected with 10038.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/providers", nil, map[string]string{"Authorization": "Bearer user-token"})
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeRealmMismatch, body["code"])

	// An admin-realm session passes the guard.
	code, _ = env.call(t, http.MethodGet, "/api/v1/admin/providers", nil, map[string]string{"Authorization": "Bearer admin-token"})
	require.Equal(t, http.StatusOK, code)

	// AC13: a session without the required role receives 10036.
	env.svc.SetSessionOrgResolver(fvtSessionOrgResolver{org: "org-fvt"})
	env.svc.SetSessionUserResolver(fvtSessionUserResolver{user: "user-fvt"})
	env.svc.SetRoleGuard(runtimeFvtRoleGuard{allowed: false})
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/providers", nil, nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, apierrors.CodeForbidden, body["code"])
}

// findModel returns the model summary with the given id, or nil.
func findModel(t *testing.T, models any, id string) map[string]any {
	t.Helper()
	list, _ := models.([]any)
	for _, item := range list {
		m, ok := item.(map[string]any)
		if ok && m["modelId"] == id {
			return m
		}
	}
	return nil
}
