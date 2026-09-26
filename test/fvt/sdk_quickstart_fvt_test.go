// Feature #21 (SDK / Quickstart) full-verification suite: the new
// user-realm GET /api/v1/inference-endpoint read over a real infer
// service and gateway mux, exercising the acceptance criteria of
// docs/architecture/sdk-quickstart.md (AC1, AC11).

package fvt

import (
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

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/tenancy"
)

// quickstartEnv is the in-process stack for the SDK/Quickstart feature:
// the infer service over one gRPC server with the gateway mux in front.
type quickstartEnv struct {
	gwSrv *httptest.Server
}

func newQuickstartEnv(t *testing.T) *quickstartEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-fvt", "org-a"} {
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

	inferSvc := infer.NewForFVT(db, newRecordingBus())
	inferSvc.SetOrgGuard(tenancy.NewOrgGuard(db))
	inferv1.RegisterInferServiceServiceServer(grpcSrv, inferSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, inferv1.RegisterInferServiceServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &quickstartEnv{gwSrv: gwSrv}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *quickstartEnv) call(t *testing.T, method, path string, org string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.gwSrv.URL+path, nil)
	require.NoError(t, err)
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

// TestFVTSDKQuickstart walks the acceptance criteria of the SDK /
// Quickstart architecture doc: AC1 (GET /api/v1/inference-endpoint
// returns the configured base_url; empty config → 500) and AC11 (the
// read is user-realm and requires no organization context).
func TestFVTSDKQuickstart(t *testing.T) {
	env := newQuickstartEnv(t)

	// AC1: empty infer.endpointBaseURL → standard internal error (500).
	config.SetConfigForTest(&config.Configuration{})
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
	code, body := env.call(t, http.MethodGet, "/api/v1/inference-endpoint", "org-fvt")
	assert.Equal(t, http.StatusInternalServerError, code, "body: %v", body)
	assert.EqualValues(t, 500, body["code"], "empty config → CodeInternal")

	// AC1: configured base URL → base_url = TrimSuffix(base, "/") + "/v1".
	cfg := &config.Configuration{}
	cfg.Infer.EndpointBaseURL = "https://infer.example.com"
	config.SetConfigForTest(cfg)
	code, body = env.call(t, http.MethodGet, "/api/v1/inference-endpoint", "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "https://infer.example.com/v1", body["baseUrl"])

	// AC11: the read requires no organization context — it succeeds even
	// without an X-Organization-Id header (user-realm read).
	code, body = env.call(t, http.MethodGet, "/api/v1/inference-endpoint", "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "https://infer.example.com/v1", body["baseUrl"])
}
