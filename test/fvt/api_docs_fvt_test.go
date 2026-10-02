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

	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	docsv1 "github.com/go-taas/go-taas/proto/taas/docs/v1"
	"github.com/go-taas/go-taas/services/docs"
)

// docsEnv is the in-process stack for the API docs feature: the docs
// service over one gRPC server with the gateway mux in front.
type docsEnv struct {
	gwSrv *httptest.Server
}

func newDocsEnv(t *testing.T) *docsEnv {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	docsSvc := docs.NewForFVT()
	docsv1.RegisterDocsServiceServer(grpcSrv, docsSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, docsv1.RegisterDocsServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &docsEnv{gwSrv: gwSrv}
}

func (e *docsEnv) call(t *testing.T, method, path string, org string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.gwSrv.URL+path, bytes.NewReader(nil))
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

// TestFVTApiDocs walks the acceptance criteria of the API docs explorer
// architecture doc (feature #38): AC1 (curated catalog with categories,
// endpoints, parameters, examples, error codes), AC2 (covers inference
// and control-plane APIs).
func TestFVTApiDocs(t *testing.T) {
	env := newDocsEnv(t)

	// AC1: GetApiDocs returns the curated catalog.
	code, body := env.call(t, http.MethodGet, "/api/v1/docs", "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	categories, _ := body["categories"].([]any)
	require.NotEmpty(t, categories)

	// AC2: the catalog covers the inference and control-plane APIs.
	paths := map[string]bool{}
	for _, c := range categories {
		cat := c.(map[string]any)
		endpoints, _ := cat["endpoints"].([]any)
		for _, e := range endpoints {
			ep := e.(map[string]any)
			paths[ep["path"].(string)] = true
		}
	}
	assert.True(t, paths["/v1/chat/completions"], "chat completions missing")
	assert.True(t, paths["/v1/embeddings"], "embeddings missing")
	assert.True(t, paths["/api/v1/models"], "models missing")
	assert.True(t, paths["/api/v1/auth/api-keys"], "api-keys missing")
	assert.True(t, paths["/api/v1/usage"], "usage missing")
	assert.True(t, paths["/api/v1/inference-endpoint"], "inference-endpoint missing")

	// AC1: no admin endpoint is exposed.
	for p := range paths {
		assert.NotContains(t, p, "/api/v1/admin/", "catalog must not expose admin endpoint %s", p)
	}
}