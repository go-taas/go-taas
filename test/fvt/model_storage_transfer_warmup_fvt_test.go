package fvt

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"sync"
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
	"github.com/go-taas/go-taas/pkg/storage"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
)

type storageFVTEnv struct {
	gwSrv *httptest.Server
}

type storageFVTPublisher struct {
	mu       sync.Mutex
	subjects []string
}

func (p *storageFVTPublisher) Publish(_ context.Context, subject string, _ []byte, _ map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subject)
	return nil
}

func newStorageFVTEnv(t *testing.T) *storageFVTEnv {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })

	svc := model.NewForFVT(db)
	svc.SetStorageConfig(storage.Config{
		DefaultBackend: "shared",
		Backends: map[string]storage.Backend{
			"shared": {Type: storage.TypeFilesystem, Filesystem: storage.Filesystem{ExistingClaim: "model-weights", MountPath: "/data/weights"}},
			"cold":   {Type: storage.TypeFilesystem, Filesystem: storage.Filesystem{ExistingClaim: "cold-weights", MountPath: "/cold"}},
		},
	})
	svc.SetTransferSettings(model.TransferSettings{Mode: "job", Namespace: "taas-jobs", Image: "taas-transfer:test"})
	svc.SetTransferPublisher(&storageFVTPublisher{})
	svc.SetWarmupSettings(true)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)
	modelv1.RegisterModelServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, modelv1.RegisterModelServiceHandler(context.Background(), mux, conn))
	gw := httptest.NewServer(mux)
	t.Cleanup(gw.Close)
	return &storageFVTEnv{gwSrv: gw}
}

func (e *storageFVTEnv) call(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	env := &modelDownloadEnv{gwSrv: e.gwSrv}
	return env.call(t, method, path, body)
}

func TestStorageTransferWarmupAPIs(t *testing.T) {
	env := newStorageFVTEnv(t)

	code, resp := env.call(t, "GET", "/api/v1/admin/storage-backends", nil)
	require.Equal(t, 200, code)
	backends, ok := resp["backends"].([]any)
	require.True(t, ok)
	assert.Len(t, backends, 2)

	code, created := env.call(t, "POST", "/api/v1/admin/models", map[string]any{
		"name": "qwen-fvt", "version": "v1", "hubModelId": "qwen/fvt", "weightPath": "qwen-fvt/v1",
	})
	require.Equal(t, 200, code)
	modelID, _ := created["modelId"].(string)
	require.NotEmpty(t, modelID)

	// Copy between the two filesystem backends.
	code, started := env.call(t, "POST", "/api/v1/admin/model-transfers", map[string]any{
		"modelId": modelID, "version": "v1", "operation": "copy",
		"sourceBackend": "shared", "targetBackend": "cold",
	})
	require.Equal(t, 200, code, "%v", started)
	transferObj, ok := started["transfer"].(map[string]any)
	require.True(t, ok)
	transferID, _ := transferObj["transferId"].(string)
	require.NotEmpty(t, transferID)
	assert.Equal(t, "queued", transferObj["state"])

	code, listed := env.call(t, "GET", "/api/v1/admin/model-transfers?page.limit=10", nil)
	require.Equal(t, 200, code)
	assert.Len(t, listed["transfers"].([]any), 1)

	code, fetched := env.call(t, "GET", "/api/v1/admin/model-transfers/"+transferID, nil)
	require.Equal(t, 200, code)
	assert.Equal(t, transferID, fetched["transfer"].(map[string]any)["transferId"])

	code, cancelled := env.call(t, "POST", "/api/v1/admin/model-transfers/"+transferID+":cancel", map[string]any{})
	require.Equal(t, 200, code)
	assert.Equal(t, "cancelled", cancelled["transfer"].(map[string]any)["state"])

	// Warmup node groups are platform-managed rows, not Helm values. Create
	// one through the admin API before queueing a warmup.
	code, group := env.call(t, "POST", "/api/v1/admin/model-warmup-node-groups", map[string]any{
		"name": "gpu", "nodeSelector": map[string]string{"gpu": "true"},
		"storageLimit": "10Gi", "hostPath": "/var/lib/taas/models",
	})
	require.Equal(t, 200, code, "%v", group)
	assert.Equal(t, "gpu", group["nodeGroup"].(map[string]any)["name"])

	code, groups := env.call(t, "GET", "/api/v1/admin/model-warmup-node-groups", nil)
	require.Equal(t, 200, code)
	assert.Len(t, groups["nodeGroups"].([]any), 1)

	// Warm the model on the selected node group.
	code, warmed := env.call(t, "POST", "/api/v1/admin/model-warmups", map[string]any{
		"modelId": modelID, "nodeGroups": []string{"gpu"},
	})
	require.Equal(t, 200, code, "%v", warmed)
	cache := warmed["cache"].(map[string]any)
	cacheID, _ := cache["cacheId"].(string)
	require.NotEmpty(t, cacheID)
	assert.Equal(t, "queued", cache["state"])

	code, caches := env.call(t, "GET", "/api/v1/admin/model-cache-entries?page.limit=10", nil)
	require.Equal(t, 200, code)
	assert.Len(t, caches["caches"].([]any), 1)

	// The renamed endpoint returns the same rows under its new field name.
	code, warmups := env.call(t, "GET", "/api/v1/admin/model-warmups?page.limit=10", nil)
	require.Equal(t, 200, code)
	assert.Len(t, warmups["warmups"].([]any), 1)

	code, cancelledCache := env.call(t, "POST", "/api/v1/admin/model-warmups/"+cacheID+":cancel", map[string]any{})
	require.Equal(t, 200, code)
	assert.Equal(t, "cancelled", cancelledCache["cache"].(map[string]any)["state"])

	code, deleted := env.call(t, "DELETE", "/api/v1/admin/model-warmup-node-groups/gpu", nil)
	require.Equal(t, 200, code, "%v", deleted)
}
