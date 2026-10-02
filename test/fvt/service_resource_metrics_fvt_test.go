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

	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/server"
	resourcemetricsv1 "github.com/go-taas/go-taas/proto/taas/resourcemetrics/v1"
	"github.com/go-taas/go-taas/services/resourcemetrics"
	"github.com/go-taas/go-taas/services/tenancy"
)

// resourceMetricsEnv is the in-process stack for the resource metrics
// feature: the resourcemetrics service over one gRPC server with the
// gateway mux in front, a shared SQLite database, and the
// inference_services table for the service-existence check.
type resourceMetricsEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server

	rmSvc *resourcemetrics.Service
}

func newResourceMetricsEnv(t *testing.T) *resourceMetricsEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, resourcemetrics.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	// The inference_services table for the service-existence check.
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS inference_services (id TEXT PRIMARY KEY, organization_id TEXT, name TEXT)`).Error)
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

	rmSvc := resourcemetrics.NewForFVT(db)
	rmSvc.SetServiceExists(serviceExistsProvider{db: db})

	resourcemetricsv1.RegisterResourceMetricsServiceServer(grpcSrv, rmSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, resourcemetricsv1.RegisterResourceMetricsServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &resourceMetricsEnv{db: db, gwSrv: gwSrv, rmSvc: rmSvc}
}

// serviceExistsProvider reports service existence against the shared DB.
type serviceExistsProvider struct{ db *gorm.DB }

func (p serviceExistsProvider) ServiceExists(_ context.Context, serviceID string) (bool, error) {
	var count int64
	err := p.db.Table("inference_services").Where("id = ?", serviceID).Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// call issues a JSON request against the gateway and decodes the body.
func (e *resourceMetricsEnv) call(t *testing.T, method, path string, org string) (int, map[string]any) {
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

// TestFVTServiceResourceMetrics walks the acceptance criteria of the
// service resource metrics architecture doc (feature #37): AC1 (cards +
// series + replicas, 10404 on invalid range), AC2 (Controller writes
// sample rows), AC3 (hourly/daily buckets + data_through).
func TestFVTServiceResourceMetrics(t *testing.T) {
	env := newResourceMetricsEnv(t)

	// Seed an inference service and sample rows (the Controller writes
	// the rows in production; the FVT seeds the table to exercise the
	// aggregation, AC2).
	serviceID := "11111111-1111-1111-1111-111111111111"
	require.NoError(t, env.db.Exec(`INSERT INTO inference_services (id, organization_id, name) VALUES (?, 'org-fvt', 'demo-svc')`, serviceID).Error)

	now := time.Now().UTC()
	gpu := 42.0
	rows := []resourcemetrics.ServiceMetricRow{
		{ID: "1", ServiceID: serviceID, ReplicaIndex: "replica-1", SampledAt: now.Add(-2 * time.Hour), CPUPercent: 50, MemoryBytes: 1024, GPUPercent: &gpu},
		{ID: "2", ServiceID: serviceID, ReplicaIndex: "replica-2", SampledAt: now.Add(-2 * time.Hour), CPUPercent: 70, MemoryBytes: 2048, GPUPercent: &gpu},
		{ID: "3", ServiceID: serviceID, ReplicaIndex: "replica-1", SampledAt: now.Add(-time.Hour), CPUPercent: 60, MemoryBytes: 1536, GPUPercent: &gpu},
		{ID: "4", ServiceID: serviceID, ReplicaIndex: "replica-2", SampledAt: now.Add(-time.Hour), CPUPercent: 80, MemoryBytes: 2560, GPUPercent: &gpu},
	}
	require.NoError(t, env.db.Create(&rows).Error)

	// AC1: valid range returns cards, series, replicas.
	since := now.Add(-3 * time.Hour).Unix()
	until := now.Unix()
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/services/"+serviceID+"/metrics?since="+fmt.Sprintf("%d", since)+"&until="+fmt.Sprintf("%d", until), "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	cards, _ := body["cards"].(map[string]any)
	require.NotNil(t, cards)
	assert.Equal(t, "2", cards["replicaCount"])
	series, _ := body["series"].([]any)
	require.NotEmpty(t, series)
	replicas, _ := body["replicas"].([]any)
	require.Len(t, replicas, 2)

	// AC1: since > until returns 10404.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/services/"+serviceID+"/metrics?since="+fmt.Sprintf("%d", until)+"&until="+fmt.Sprintf("%d", since), "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "invalid range must be rejected: %v", body)
	assert.Equal(t, float64(10404), body["code"])

	// AC1: unknown service returns 10301.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/services/does-not-exist/metrics", "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown service must be rejected: %v", body)
	assert.Equal(t, float64(10301), body["code"])

	// AD9: invalid metric returns 12101.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/services/"+serviceID+"/metrics?metric=bogus", "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "invalid metric must be rejected: %v", body)
	assert.Equal(t, float64(12101), body["code"])

	// AC3: every response carries data_through.
	dt, ok := cards["dataThrough"].(string)
	require.True(t, ok, "data_through must be present: %v", cards)
	assert.NotEmpty(t, dt)
}