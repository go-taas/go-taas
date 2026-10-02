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
	clusterv1 "github.com/go-taas/go-taas/proto/taas/cluster/v1"
	"github.com/go-taas/go-taas/services/cluster"
	"github.com/go-taas/go-taas/services/tenancy"
)

// clusterEnv is the in-process stack for the multi-cluster feature: the
// cluster service over one gRPC server with the gateway mux in front, a
// shared SQLite database, and a fake workload provider.
type clusterEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server

	svc *cluster.Service
}

func newClusterEnv(t *testing.T) *clusterEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, cluster.MigrateSchemaForFVT(db))
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

	svc := cluster.NewForFVT(db)
	svc.SetWorkloadProvider(fvtWorkloadProvider{})

	clusterv1.RegisterClusterServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, clusterv1.RegisterClusterServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &clusterEnv{db: db, gwSrv: gwSrv, svc: svc}
}

type fvtWorkloadProvider struct{}

func (f fvtWorkloadProvider) ListServicesByCluster(context.Context, string) ([]cluster.ClusterWorkload, error) {
	return []cluster.ClusterWorkload{
		{ServiceID: "svc-1", Name: "demo", ModelID: "m1", State: "running", CreatedAt: 100},
	}, nil
}

func (e *clusterEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

// TestFVTMultiCluster walks the acceptance criteria of the multi-cluster
// management architecture doc (feature #40): AC1 (register + list),
// AC2 (get + workloads), AC3 (disable idempotency).
func TestFVTMultiCluster(t *testing.T) {
	env := newClusterEnv(t)

	// AC1: register a cluster.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/clusters", map[string]any{
		"name": "cluster-a", "region": "cn-north", "kubeconfigRef": "kube/cluster-a",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	clusterID, _ := body["clusterId"].(string)
	require.NotEmpty(t, clusterID)
	assert.Equal(t, "active", body["state"])

	// AC1: list clusters.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/clusters", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	clusters, _ := body["clusters"].([]any)
	require.Len(t, clusters, 1)

	// AC2: get the cluster detail.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/clusters/"+clusterID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	clusterObj, _ := body["cluster"].(map[string]any)
	require.NotNil(t, clusterObj)
	summary, _ := clusterObj["summary"].(map[string]any)
	assert.Equal(t, "cluster-a", summary["name"])

	// AC2: get the workloads.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/clusters/"+clusterID+"/workloads", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	workloads, _ := body["workloads"].([]any)
	require.Len(t, workloads, 1)

	// AC3: disable the cluster (idempotent).
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/clusters/"+clusterID+":disable", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/clusters/"+clusterID+":disable", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// AC2: unknown cluster -> 12401.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/clusters/missing", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown cluster must be rejected: %v", body)
	assert.Equal(t, float64(12401), body["code"])
}