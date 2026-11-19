// Feature-13 (per-tenant model authorization) full-verification suite:
// the real model + infer + auth stack behind the gateway, over one
// shared database, exercising the acceptance criteria of
// docs/architecture/model-authorization.md (AC1-AC11).

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
	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
)

// nvidiaImageID is the seeded catalog image compatible with the
// accelerator the deploy cases use.
const nvidiaImageID = "img-vllm-nvidia"

// modelAuthEnv is the in-process stack for the model authorization
// feature: the model, infer and auth services over one gRPC server, the
// gateway mux in front, a shared SQLite database and a recording MQ
// client that doubles as the fake controller.
type modelAuthEnv struct {
	db       *gorm.DB
	gwSrv    *httptest.Server
	mqBus    *recordingBus
	modelSvc *model.Service
	authSvc  *auth.Service
}

func newModelAuthEnv(t *testing.T) *modelAuthEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, auth.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	// The org context is validated against the organizations table.
	for _, orgID := range []string{"org-a", "org-b", "org-c"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// Seed one catalog image so the deploy cases can pass image
	// validation (the registry is DB-backed since feature #3).
	require.NoError(t, image.MigrateSchemaForFVT(db))
	require.NoError(t, image.NewRepository(db).CreateImage(context.Background(), &image.Image{
		ID: nvidiaImageID, Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3",
		Accelerator: "nvidia", Engine: "vllm",
	}))
	image.WireRegistryForTest(db)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// The production interceptor chain normalizes business errors into
	// gRPC status errors so the gateway renders the unified envelope.
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	bus := newRecordingBus()
	authSvc := auth.NewForFVT(db)
	authSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	// AD5: the model repository is the single authorization check
	// shared by the control and data planes.
	authSvc.SetModelAuthorizer(model.NewRepository(db))
	modelSvc := model.NewForFVT(db)
	modelSvc.SetDeleteGuard(infer.NewDeleteModelGuard(db))
	modelSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	// AD8: granted_by is resolved from the session; the auth service is
	// the resolver in production too. No session exists in this env, so
	// the transitional caller organization is recorded.
	modelSvc.SetSessionResolver(authSvc)
	inferSvc := infer.NewForFVT(db, bus)
	inferSvc.SetOrgGuard(tenancy.NewOrgGuard(db))

	modelv1.RegisterModelServiceServer(grpcSrv, modelSvc)
	inferv1.RegisterInferServiceServiceServer(grpcSrv, inferSvc)
	authv1.RegisterAuthServiceServer(grpcSrv, authSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, modelv1.RegisterModelServiceHandler(context.Background(), mux, conn))
	require.NoError(t, inferv1.RegisterInferServiceServiceHandler(context.Background(), mux, conn))
	require.NoError(t, authv1.RegisterAuthServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &modelAuthEnv{db: db, gwSrv: gwSrv, mqBus: bus, modelSvc: modelSvc, authSvc: authSvc}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *modelAuthEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

// registerModel registers a model with one version and returns its id.
func (e *modelAuthEnv) registerModel(t *testing.T, name, org string) string {
	t.Helper()
	code, body := e.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": name, "hubModelId": "fvt/" + name, "version": "v1", "weightPath": fmt.Sprintf("models/%s/v1", name),
	}, org)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	id, _ := body["modelId"].(string)
	require.NotEmpty(t, id)
	return id
}

// createKey creates an API key for the org and returns its plaintext.
func (e *modelAuthEnv) createKey(t *testing.T, org string) string {
	t.Helper()
	code, body := e.call(t, http.MethodPost, "/api/v1/admin/auth/api-keys", map[string]any{
		"name": "key-" + org,
	}, org)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	plaintext, _ := body["apiKey"].(string)
	require.NotEmpty(t, plaintext)
	return plaintext
}

// deploy attempts to deploy the model as org and returns the HTTP
// status and the decoded body.
func (e *modelAuthEnv) deploy(t *testing.T, modelID, org string) (int, map[string]any) {
	t.Helper()
	return e.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name":    fmt.Sprintf("svc-%s-%d", org, time.Now().UnixNano()%100000),
		"modelId": modelID, "modelVersion": "v1",
		"imageId": nvidiaImageID, "replicas": "1", "accelerator": "nvidia",
	}, org)
}

// verify calls the data-plane key verification for the model as the
// caller holding plaintext, returning the verdict error (nil when
// allowed).
func (e *modelAuthEnv) verify(t *testing.T, plaintext, modelID string) error {
	t.Helper()
	_, err := e.authSvc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: auth.KeyDigest(plaintext),
		Model:     modelID,
	})
	return err
}

// businessCode extracts the business code of an API error, or 0.
func businessCode(err error) apierrors.Code {
	if err == nil {
		return apierrors.CodeOK
	}
	if ae, ok := apierrors.As(err); ok {
		return ae.Code
	}
	return apierrors.CodeInternal
}

// listAuthorizations returns the model's grant rows over the gateway.
func (e *modelAuthEnv) listAuthorizations(t *testing.T, modelID, org string, query string) ([]map[string]any, string) {
	t.Helper()
	code, body := e.call(t, http.MethodGet, fmt.Sprintf("/api/v1/admin/models/%s/authorizations%s", modelID, query), nil, org)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	raw, _ := body["authorizations"].([]any)
	rows := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		row, _ := r.(map[string]any)
		rows = append(rows, row)
	}
	pageMeta, _ := body["pageMeta"].(map[string]any)
	total := ""
	if pageMeta != nil {
		total = fmt.Sprint(pageMeta["total"])
	}
	return rows, total
}

// TestFVTModelAuthorization walks the acceptance criteria of the
// per-tenant model authorization architecture doc: AC1 (grant stores the
// row and restricts the model), AC2 (idempotent grant), AC3 (revoke the
// last grant returns to default-allow), AC4 (idempotent revoke), AC5
// (blocked deploy publishes nothing), AC6 (granted deploy proceeds),
// AC7 (blocked call), AC8 (granted call), AC9 (default-allow
// regression), AC10 (newest-first paginated listing), AC11 (org-filtered
// catalog).
func TestFVTModelAuthorization(t *testing.T) {
	env := newModelAuthEnv(t)

	restricted := env.registerModel(t, "authz-restricted", "org-a")
	open := env.registerModel(t, "authz-open", "org-a")
	orgAKey := env.createKey(t, "org-a")
	orgBKey := env.createKey(t, "org-b")

	// AC9: a model with zero grants is open to every organization — both
	// the control plane and the data plane must leave it alone.
	before := len(env.mqBus.changes)
	code, body := env.deploy(t, open, "org-b")
	require.Equal(t, http.StatusOK, code, "default-allow deploy must proceed: %v", body)
	assert.Len(t, env.mqBus.changes, before+1, "default-allow deploy publishes the change")
	require.NoError(t, env.verify(t, orgBKey, open), "AC9: default-allow call is forwarded")

	// AC1: the first grant stores the row and flips the model to
	// restricted.
	code, body = env.call(t, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/models/%s:grant", restricted),
		map[string]any{"organizationId": "org-a"}, "org-a")
	require.Equal(t, http.StatusOK, code, "grant must succeed: %v", body)

	rows, total := env.listAuthorizations(t, restricted, "org-a", "")
	require.Len(t, rows, 1)
	assert.Equal(t, "org-a", rows[0]["organizationId"])
	assert.Equal(t, "1", total)
	grantedBy, _ := rows[0]["grantedBy"].(string)
	assert.NotEmpty(t, grantedBy, "AC1: granted_by is recorded")
	createdAt, _ := rows[0]["createdAt"].(string)
	assert.NotEmpty(t, createdAt, "AC1: created_at is recorded")

	// AC1/AC13: the model is now restricted in the catalog; the
	// zero-grant model is not.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models", nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	models, _ := body["models"].([]any)
	restrictedFlags := map[string]bool{}
	for _, m := range models {
		row, _ := m.(map[string]any)
		flag, _ := row["restricted"].(bool)
		restrictedFlags[fmt.Sprint(row["name"])] = flag
	}
	assert.True(t, restrictedFlags["authz-restricted"], "AC13: granted model is restricted")
	assert.False(t, restrictedFlags["authz-open"], "AC13: zero-grant model is open")

	// AC2: granting the same organization again is an idempotent no-op.
	code, body = env.call(t, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/models/%s:grant", restricted),
		map[string]any{"organizationId": "org-a"}, "org-a")
	require.Equal(t, http.StatusOK, code, "duplicate grant must succeed: %v", body)
	rows, total = env.listAuthorizations(t, restricted, "org-a", "")
	assert.Len(t, rows, 1, "AC2: no duplicate grant row")
	assert.Equal(t, "1", total)

	// Grant errors: unknown model and unknown organization.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/models/no-such-model:grant",
		map[string]any{"organizationId": "org-a"}, "org-a")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10101, body["code"], "unknown model: %v", body)
	code, body = env.call(t, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/models/%s:grant", restricted),
		map[string]any{"organizationId": "org-nope"}, "org-a")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10005, body["code"], "unknown org: %v", body)

	// AC5: a non-granted organization may neither deploy nor call.
	before = len(env.mqBus.changes)
	code, body = env.deploy(t, restricted, "org-b")
	assert.NotEqual(t, http.StatusOK, code, "blocked deploy must fail: %v", body)
	assert.EqualValues(t, 10105, body["code"], "AC5: 10105 MODEL_UNAUTHORIZED")
	assert.Contains(t, fmt.Sprint(body["message"]), "not authorized", "AC5: canonical message")
	assert.Len(t, env.mqBus.changes, before, "AC5: blocked deploy publishes nothing")

	// AC7: the data plane blocks the same organization's key.
	err := env.verify(t, orgBKey, restricted)
	require.Error(t, err, "AC7: blocked call must fail")
	assert.Equal(t, apierrors.CodeModelUnauthorized, businessCode(err), "AC7: 10105 MODEL_UNAUTHORIZED")

	// AC6/AC8: the granted organization deploys and calls normally.
	before = len(env.mqBus.changes)
	code, body = env.deploy(t, restricted, "org-a")
	require.Equal(t, http.StatusOK, code, "AC6: granted deploy must proceed: %v", body)
	assert.Len(t, env.mqBus.changes, before+1, "AC6: granted deploy publishes the change")
	serviceID, _ := body["serviceId"].(string)
	require.NotEmpty(t, serviceID)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	svc, _ := body["service"].(map[string]any)
	require.NotNil(t, svc)
	assert.Equal(t, "pending", svc["state"], "AC6: a granted deploy starts in pending")
	require.NoError(t, env.verify(t, orgAKey, restricted), "AC8: granted call is forwarded")

	// AC10: a second grant lists newest first with pagination.
	// Sleep past the second boundary so created_at ordering is strict.
	time.Sleep(1100 * time.Millisecond)
	code, body = env.call(t, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/models/%s:grant", restricted),
		map[string]any{"organizationId": "org-b"}, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	rows, total = env.listAuthorizations(t, restricted, "org-a", "?page.offset=0&page.limit=1")
	require.Len(t, rows, 1, "AC10: pagination caps the page")
	assert.Equal(t, "2", total, "AC10: total counts every grant row")
	assert.Equal(t, "org-b", rows[0]["organizationId"], "AC10: newest grant first")
	rows, _ = env.listAuthorizations(t, restricted, "org-a", "?page.offset=1&page.limit=1")
	require.Len(t, rows, 1)
	assert.Equal(t, "org-a", rows[0]["organizationId"], "AC10: older grant second")

	// AC11: the org-filtered catalog returns every unrestricted model
	// plus the restricted ones the org is granted.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models?organization_id=org-c", nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	models, _ = body["models"].([]any)
	names := map[string]bool{}
	for _, m := range models {
		row, _ := m.(map[string]any)
		names[fmt.Sprint(row["name"])] = true
	}
	assert.True(t, names["authz-open"], "AC11: unrestricted models are always listed")
	assert.False(t, names["authz-restricted"], "AC11: a restricted model is hidden from a non-granted org")

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models?organization_id=org-a", nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	models, _ = body["models"].([]any)
	names = map[string]bool{}
	for _, m := range models {
		row, _ := m.(map[string]any)
		names[fmt.Sprint(row["name"])] = true
	}
	assert.True(t, names["authz-restricted"], "AC11: a granted org sees the restricted model")
	assert.True(t, names["authz-open"], "AC11: and every unrestricted model")

	// AC4: revoking an organization with no grant is an idempotent
	// no-op success.
	code, body = env.call(t, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/models/%s:revoke", restricted),
		map[string]any{"organizationId": "org-c"}, "org-a")
	require.Equal(t, http.StatusOK, code, "AC4: revoke of a non-granted org succeeds: %v", body)
	_, total = env.listAuthorizations(t, restricted, "org-a", "")
	assert.Equal(t, "2", total, "AC4: the no-op revoke removed nothing")

	// AC3: revoking every grant returns the model to default-allow.
	for _, org := range []string{"org-a", "org-b"} {
		code, body = env.call(t, http.MethodPost,
			fmt.Sprintf("/api/v1/admin/models/%s:revoke", restricted),
			map[string]any{"organizationId": org}, "org-a")
		require.Equal(t, http.StatusOK, code, "revoke %s must succeed: %v", org, body)
	}
	_, total = env.listAuthorizations(t, restricted, "org-a", "")
	assert.Equal(t, "0", total, "AC3: every grant row is gone")

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models?organization_id=org-c", nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	models, _ = body["models"].([]any)
	names = map[string]bool{}
	for _, m := range models {
		row, _ := m.(map[string]any)
		names[fmt.Sprint(row["name"])] = true
	}
	assert.True(t, names["authz-restricted"], "AC3: the model is open again")

	before = len(env.mqBus.changes)
	code, body = env.deploy(t, restricted, "org-b")
	require.Equal(t, http.StatusOK, code, "AC3: default-allow is back: %v", body)
	assert.Len(t, env.mqBus.changes, before+1)
	require.NoError(t, env.verify(t, orgBKey, restricted), "AC3: the data plane follows")
}
