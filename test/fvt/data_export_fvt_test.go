package fvt

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	accountv1 "github.com/go-taas/go-taas/proto/taas/account/v1"
	"github.com/go-taas/go-taas/services/account"
	"github.com/go-taas/go-taas/services/tenancy"
)

// accountEnv is the in-process stack for the data export feature: the
// account service over one gRPC server with the gateway mux in front, a
// shared SQLite database, and a fake export data provider.
type accountEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server

	svc *account.Service
}

func newAccountEnv(t *testing.T) *accountEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, account.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-fvt", "org-a", "org-b"} {
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
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	svc := account.NewForFVT(db)

	accountv1.RegisterAccountServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, accountv1.RegisterAccountServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &accountEnv{db: db, gwSrv: gwSrv, svc: svc}
}

func (e *accountEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

// TestFVTDataExport walks the acceptance criteria of the data export &
// privacy architecture doc (feature #41): AC1 (create returns pending),
// AC2 (list + get), AC3 (download when ready, 12504 before).
func TestFVTDataExport(t *testing.T) {
	env := newAccountEnv(t)

	// AC1: create an export returns status=pending.
	code, body := env.call(t, http.MethodPost, "/api/v1/account/export", map[string]any{
		"type": "usage", "format": "json",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	export, _ := body["export"].(map[string]any)
	require.NotNil(t, export)
	exportID, _ := export["exportId"].(string)
	require.NotEmpty(t, exportID)
	assert.Equal(t, "pending", export["status"])

	// AC1: invalid type -> 12502.
	code, body = env.call(t, http.MethodPost, "/api/v1/account/export", map[string]any{
		"type": "bogus", "format": "json",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "invalid type must be rejected: %v", body)
	assert.Equal(t, float64(12502), body["code"])

	// AC2: list exports.
	code, body = env.call(t, http.MethodGet, "/api/v1/account/export", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	exports, _ := body["exports"].([]any)
	require.Len(t, exports, 1)

	// AC2: get the export.
	code, body = env.call(t, http.MethodGet, "/api/v1/account/export/"+exportID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	got, _ := body["export"].(map[string]any)
	assert.Equal(t, "pending", got["status"])

	// AC3: download before ready -> 12504.
	code, body = env.call(t, http.MethodGet, "/api/v1/account/export/"+exportID+"/download", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "download before ready must be rejected: %v", body)
	assert.Equal(t, float64(12504), body["code"])

	// Mark ready and download.
	require.NoError(t, env.db.Model(&account.DataExport{}).Where("id = ?", exportID).Updates(map[string]any{
		"status": "ready", "row_count": 1, "file": `{"usage":[]}`,
	}).Error)
	code, body = env.call(t, http.MethodGet, "/api/v1/account/export/"+exportID+"/download", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, `{"usage":[]}`, body["file"])
	assert.Equal(t, "application/json", body["contentType"])

	// AC2: unknown export -> 12501.
	code, body = env.call(t, http.MethodGet, "/api/v1/account/export/missing", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown export must be rejected: %v", body)
	assert.Equal(t, float64(12501), body["code"])
}