// Feature #46 (API key model scoping) full-verification suite: the real
// model + auth stack behind the gateway, over one shared database,
// exercising the acceptance criteria of
// docs/architecture/api-key-model-scoping.md. VerifyAPIKey has no HTTP
// binding (it is the gateway's gRPC seam), so the enforcement criteria
// (AC2/AC3) are exercised here through the service seam — the only
// place they are testable end to end.

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
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
)

// keyScopeEnv is the in-process stack for the key model scoping
// feature: the model and auth services over one gRPC server, the
// gateway mux in front, and a shared SQLite database.
type keyScopeEnv struct {
	db      *gorm.DB
	gwSrv   *httptest.Server
	authSvc *auth.Service
}

func newKeyScopeEnv(t *testing.T) *keyScopeEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, auth.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-a", "org-b"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// The production interceptor chain normalizes business errors into
	// gRPC status errors so the gateway renders the unified envelope.
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	authSvc := auth.NewForFVT(db)
	authSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	// AD6: the model repository is the catalog existence check shared
	// with the data-plane authorizer wiring in production.
	authSvc.SetModelExistenceChecker(model.NewRepository(db))
	authv1.RegisterAuthServiceServer(grpcSrv, authSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, authv1.RegisterAuthServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &keyScopeEnv{db: db, gwSrv: gwSrv, authSvc: authSvc}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *keyScopeEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

// registerModel seeds one catalog model row and returns its id.
func (e *keyScopeEnv) registerModel(t *testing.T, name string) string {
	t.Helper()
	repo := model.NewRepository(e.db)
	require.NoError(t, repo.CreateModel(context.Background(), &model.Model{Name: name}))
	row, err := repo.FindByName(context.Background(), name)
	require.NoError(t, err)
	return row.ID
}

// createKey creates an API key (optionally scoped) for the org over the
// user-surface binding and returns its plaintext.
func (e *keyScopeEnv) createKey(t *testing.T, org string, models []string) string {
	t.Helper()
	code, body := e.call(t, http.MethodPost, "/api/v1/auth/api-keys", map[string]any{
		"name": "key-" + org, "models": models,
	}, org)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	plaintext, _ := body["apiKey"].(string)
	require.NotEmpty(t, plaintext)
	return plaintext
}

// verify calls the data-plane key verification for the model as the
// caller holding plaintext, returning the verdict error (nil when
// allowed).
func (e *keyScopeEnv) verify(plaintext, modelID string) error {
	_, err := e.authSvc.VerifyAPIKey(context.Background(), &authv1.VerifyAPIKeyRequest{
		KeyDigest: auth.KeyDigest(plaintext),
		Model:     modelID,
	})
	return err
}

// TestFVTAPIKeyModelScoping walks the acceptance criteria of the key
// model scoping architecture doc: AC1 (scoped create + list echo), AC2
// (in-scope allowed / out-of-scope 10039, no metering side effects by
// construction — the rejection happens before the gateway forwards),
// AC3 (unscoped key unchanged), AC4 (scope edit flips the verdict after
// cache eviction, no rotation), AC5 (unknown model rejected at write
// time), AC6 (admin binding read-only; scope writes are user-surface
// only), AC8 (cross-tenant masking).
func TestFVTAPIKeyModelScoping(t *testing.T) {
	env := newKeyScopeEnv(t)

	m1 := env.registerModel(t, "scope-m1")
	m2 := env.registerModel(t, "scope-m2")

	// AC1: create a scoped key; the list echoes the scope per row.
	scoped := env.createKey(t, "org-a", []string{m1})
	code, body := env.call(t, http.MethodGet, "/api/v1/auth/api-keys", nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	keys, _ := body["keys"].([]any)
	require.Len(t, keys, 1)
	summary, _ := keys[0].(map[string]any)
	models, _ := summary["models"].([]any)
	require.Len(t, models, 1)
	assert.Equal(t, m1, models[0])

	// AC2: the in-scope model verifies; the out-of-scope model is
	// rejected with 10039 on both cache paths.
	require.NoError(t, env.verify(scoped, m1))
	err := env.verify(scoped, m2)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeAPIKeyModelNotAllowed, businessCode(err))
	// Cache-hit path: same verdict.
	err = env.verify(scoped, m2)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeAPIKeyModelNotAllowed, businessCode(err))

	// AC3: an unscoped key is unchanged — any model verifies.
	unscoped := env.createKey(t, "org-a", nil)
	require.NoError(t, env.verify(unscoped, m1))
	require.NoError(t, env.verify(unscoped, m2))

	// AC4: editing the scope flips the verdict after the cache entry is
	// evicted, and the key material is not rotated (same plaintext
	// keeps verifying).
	keyID, _ := summary["keyId"].(string)
	code, body = env.call(t, http.MethodPut, fmt.Sprintf("/api/v1/auth/api-keys/%s/scope", keyID),
		map[string]any{"models": []string{m2}}, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	updated, _ := body["key"].(map[string]any)
	require.NotNil(t, updated)
	afterModels, _ := updated["models"].([]any)
	require.Len(t, afterModels, 1)
	assert.Equal(t, m2, afterModels[0])

	require.NoError(t, env.verify(scoped, m2), "newly in-scope model must pass after the edit")
	err = env.verify(scoped, m1)
	require.Error(t, err, "old in-scope model must now be rejected")
	assert.Equal(t, apierrors.CodeAPIKeyModelNotAllowed, businessCode(err))

	// Clearing the scope returns the key to unrestricted.
	code, body = env.call(t, http.MethodPut, fmt.Sprintf("/api/v1/auth/api-keys/%s/scope", keyID),
		map[string]any{"models": []string{}}, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.NoError(t, env.verify(scoped, m1))

	// AC5: an unknown model ID is rejected at write time (10101).
	code, body = env.call(t, http.MethodPut, fmt.Sprintf("/api/v1/auth/api-keys/%s/scope", keyID),
		map[string]any{"models": []string{"no-such-model"}}, "org-a")
	assert.Equal(t, http.StatusInternalServerError, code, "unknown model must be rejected: %v", body)
	assert.EqualValues(t, apierrors.CodeModelNotFound, body["code"])

	// AC6: the deprecated admin binding stays read-only — the scope
	// write is not routed under the admin prefix.
	code, body = env.call(t, http.MethodPut, fmt.Sprintf("/api/v1/admin/auth/api-keys/%s/scope", keyID),
		map[string]any{"models": []string{m1}}, "org-a")
	assert.Equal(t, http.StatusNotFound, code, "admin scope write must not be routed: %v", body)

	// AC6: the admin list binding returns the scope read-only.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/api-keys", nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	keys, _ = body["keys"].([]any)
	require.Len(t, keys, 2)

	// AC8: cross-tenant masking — org-b cannot see or edit org-a's key.
	code, body = env.call(t, http.MethodGet, "/api/v1/auth/api-keys", nil, "org-b")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	pageMeta, _ := body["pageMeta"].(map[string]any)
	assert.EqualValues(t, "0", fmt.Sprint(pageMeta["total"]))

	code, body = env.call(t, http.MethodPut, fmt.Sprintf("/api/v1/auth/api-keys/%s/scope", keyID),
		map[string]any{"models": []string{m1}}, "org-b")
	assert.Equal(t, http.StatusInternalServerError, code, "cross-org scope edit must be masked: %v", body)
	assert.EqualValues(t, apierrors.CodeAPIKeyNotFound, body["code"])

	// Scope validation over the gateway: duplicates -> 10008.
	code, body = env.call(t, http.MethodPut, fmt.Sprintf("/api/v1/auth/api-keys/%s/scope", keyID),
		map[string]any{"models": []string{m1, m1}}, "org-a")
	assert.Equal(t, http.StatusInternalServerError, code, "duplicates must be rejected: %v", body)
	assert.EqualValues(t, apierrors.CodeAPIKeyInvalid, body["code"])
}
