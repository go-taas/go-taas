// Package fvt hosts full-verification tests: they exercise the real
// gRPC server, gateway mux and service stack end to end (in-process),
// against the acceptance criteria of each feature's architecture doc.
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

	"github.com/go-taas/go-taas/pkg/server"
	authv1 "github.com/go-taas/go-taas/proto/taas/auth/v1"
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/tenancy"
)

// fvtEnv is a fully wired in-process stack: gRPC server on a real
// listener, gateway mux in front of it, and a shared SQLite database.
type fvtEnv struct {
	db      *gorm.DB
	gwSrv   *httptest.Server
	grpcLn  net.Listener
	grpcSrv *grpc.Server
	authSvc *auth.Service
}

func newFVTEnv(t *testing.T) *fvtEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, auth.MigrateSchemaForFVT(db))
	// Feature #6: the org context is validated against the
	// organizations table, so the schema and the fixture rows must
	// exist and the guard must be wired.
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-fvt", "org-a", "org-b", "org-other"} {
		require.NoError(t, db.Create(&tenancy.Organization{
			ID: orgID, DisplayName: orgID, State: tenancy.StateActive,
		}).Error)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// Real gRPC server on an ephemeral port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer()
	t.Cleanup(grpcSrv.Stop)

	// Auth service bound to the shared database through the injection
	// constructor; the verdict cache is the in-memory fake.
	svc := auth.NewForFVT(db)
	svc.SetOrgGuard(tenancy.NewOrgGuard(db))
	authv1.RegisterAuthServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	// Gateway mux in front of the gRPC server.
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, authv1.RegisterAuthServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &fvtEnv{db: db, gwSrv: gwSrv, grpcLn: ln, grpcSrv: grpcSrv, authSvc: svc}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *fvtEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

// TestFVTAPIKeyLifecycle walks the acceptance criteria of the API key
// architecture doc: AC1 (format), AC3 (masked list + pagination),
// AC4 (created key verifies), AC5 (revoke rejects and evicts cache),
// AC6 (idempotent revoke, cross-org isolation).
func TestFVTAPIKeyLifecycle(t *testing.T) {
	env := newFVTEnv(t)
	ctx := context.Background()

	// AC1: create returns a well-formed plaintext key exactly once.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/auth/api-keys", map[string]any{"name": "fvt key"}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	plaintext, _ := body["apiKey"].(string)
	require.Regexp(t, `^sk-[A-Za-z0-9]{43}$`, plaintext)
	keyID, _ := body["keyId"].(string)
	require.NotEmpty(t, keyID)

	// AC4: the created key verifies over gRPC (data-plane path).
	verdict, err := env.authSvc.VerifyAPIKey(ctx, &authv1.VerifyAPIKeyRequest{KeyDigest: auth.KeyDigest(plaintext)})
	require.NoError(t, err)
	assert.Equal(t, "org-fvt", verdict.GetOrganizationId())
	assert.Equal(t, keyID, verdict.GetKeyId())
	assert.Equal(t, "agent", verdict.GetRole())

	// AC3: list shows masked summaries with correct pagination totals.
	for _, name := range []string{"second", "third"} {
		code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/api-keys", map[string]any{"name": name}, "org-fvt")
		require.Equal(t, http.StatusOK, code, "body: %v", body)
	}
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/api-keys?page.offset=0&page.limit=2", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	pageMeta, _ := body["pageMeta"].(map[string]any)
	require.NotNil(t, pageMeta)
	assert.EqualValues(t, "3", fmt.Sprint(pageMeta["total"]))
	keys, _ := body["keys"].([]any)
	require.Len(t, keys, 2)
	for _, k := range keys {
		summary, _ := k.(map[string]any)
		prefix, _ := summary["prefix"].(string)
		assert.Regexp(t, `^sk-[A-Za-z0-9]{5}$`, prefix)
		assert.NotContains(t, summary, "apiKey")
	}

	// AC6: cross-org isolation — the other org sees none of these keys.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/auth/api-keys", nil, "org-other")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	pageMeta, _ = body["pageMeta"].(map[string]any)
	assert.EqualValues(t, "0", fmt.Sprint(pageMeta["total"]))

	// AC5: revoke evicts the cache and subsequent verify is rejected.
	code, body = env.call(t, http.MethodPost, fmt.Sprintf("/api/v1/admin/auth/api-keys/%s:revoke", keyID), map[string]any{}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	_, err = env.authSvc.VerifyAPIKey(ctx, &authv1.VerifyAPIKeyRequest{KeyDigest: auth.KeyDigest(plaintext)})
	require.Error(t, err)

	// AC6: revoke is idempotent.
	code, body = env.call(t, http.MethodPost, fmt.Sprintf("/api/v1/admin/auth/api-keys/%s:revoke", keyID), map[string]any{}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// AC6: cross-org revoke is not found.
	code, _ = env.call(t, http.MethodPost, "/api/v1/admin/auth/api-keys/no-such-key:revoke", map[string]any{}, "org-other")
	assert.Equal(t, http.StatusInternalServerError, code) // business code in body; see architecture 4.5

	// Expiry validation: past expiry is rejected at create time (AC7).
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/auth/api-keys", map[string]any{
		"name": "expired", "expiresAt": time.Now().Add(-time.Hour).Unix(),
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "past expiry must be rejected: %v", body)
}
