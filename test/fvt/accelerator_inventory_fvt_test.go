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
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	acceleratorv1 "github.com/go-taas/go-taas/proto/taas/accelerator/v1"
	"github.com/go-taas/go-taas/services/accelerator"
	"github.com/go-taas/go-taas/services/image"
)

// acceleratorEnv is the in-process stack for the accelerator inventory
// feature: the accelerator service over one gRPC server, the gateway mux
// in front, a shared SQLite (for the warmup-task context read), and a
// recording MQ bus that doubles as the controller's snapshot publisher.
type acceleratorEnv struct {
	db      *gorm.DB
	gwSrv   *httptest.Server
	grpcSrv *grpc.Server
	mqBus   *recordingBus
	accSvc  *accelerator.Service
}

// publishSnapshot publishes a full accelerator inventory snapshot on the
// accelerator.inventory subject, exactly as the controller's collection
// loop does (feature #18, Section 4.1).
func (e *acceleratorEnv) publishSnapshot(t *testing.T, nodes []map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"nodes":       nodes,
		"reported_at": time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.NoError(t, e.mqBus.Publish(context.Background(), mq.DefaultSubjects().AcceleratorInventory, body, nil))
}

func newAcceleratorEnv(t *testing.T) *acceleratorEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, image.MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	bus := newRecordingBus()
	// The service and the snapshot consumer share one projection cache,
	// exactly as the production wiring does (the consumer replaces it,
	// the service reads it).
	cache := accelerator.NewProjectionCache()
	accSvc := accelerator.NewWithCache(cache)
	// Wire the warmup-task context provider from the image module.
	accSvc.SetWarmupProvider(image.NewWarmupTasksForNodeProvider(db))

	// Run the real snapshot consumer against the bus so published
	// snapshots flow through the production path into the cache. The
	// recording bus's Subscribe returns immediately, so Run returns
	// immediately after registering the handler; calling it
	// synchronously guarantees the subscription exists before any test
	// publishes a snapshot.
	consumer := accelerator.NewSnapshotConsumer(bus, cache, 1)
	require.NoError(t, consumer.Run(context.Background()))

	acceleratorv1.RegisterAcceleratorServiceServer(grpcSrv, accSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, acceleratorv1.RegisterAcceleratorServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &acceleratorEnv{db: db, gwSrv: gwSrv, grpcSrv: grpcSrv, mqBus: bus, accSvc: accSvc}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *acceleratorEnv) call(t *testing.T, method, path string, body any) (int, map[string]any) {
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
	resp, err := e.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestFVTAccleratorInventory walks the acceptance criteria of the
// accelerator inventory & health architecture doc: AC1 (list returns
// every labeled node with vendor/card types/allocated-free/driver/
// device-plugin/readiness/health; unlabeled = unspecified), AC2 (card
// type summary sorted, omits zero-total), AC3 (node detail with per-GPU,
// labels, taints, resources, warmup context; missing node -> 10208),
// AC4 (health composite semantics).
func TestFVTAccleratorInventory(t *testing.T) {
	env := newAcceleratorEnv(t)

	// Seed a warmup task that targeted node-a so the detail view's
	// warmup context is populated (AC3).
	require.NoError(t, env.db.Create(&image.WarmupTask{
		ID:          "warmup-1",
		ImageID:     "img-vllm-nvidia",
		State:       "succeeded",
		NodeResults: []byte(`[{"node":"node-a","state":"succeeded"}]`),
	}).Error)

	// Publish a full snapshot with three nodes: a healthy nvidia node,
	// a degraded nvidia node, and an unlabeled (unspecified) node.
	env.publishSnapshot(t, []map[string]any{
		{
			"node_id": "n1", "name": "node-a", "vendor": "nvidia",
			"card_types": []string{"gpu"}, "gpus_allocated": 2, "gpus_free": 6,
			"driver_version": "535.104.05", "device_plugin_state": "healthy",
			"readiness": "ready",
			"gpus": []map[string]any{
				{"index": 0, "model": "A800", "allocated": true, "free": false, "note": ""},
				{"index": 1, "model": "A800", "allocated": false, "free": true, "note": ""},
			},
			"labels": map[string]string{"go-taas.io/accelerator": "nvidia"},
			"taints": []string{"dedicated=gpu:NoSchedule"},
			"resources": []map[string]any{
				{"card_type": "A800", "allocatable": 8, "allocated": 2},
			},
		},
		{
			"node_id": "n2", "name": "node-b", "vendor": "nvidia",
			"card_types": []string{"gpu"}, "gpus_allocated": 8, "gpus_free": 0,
			"driver_version": "535.104.05", "device_plugin_state": "degraded",
			"readiness": "ready",
			"resources": []map[string]any{
				{"card_type": "A800", "allocatable": 8, "allocated": 8},
			},
		},
		{
			"node_id": "n3", "name": "node-c", "vendor": "unspecified",
			"card_types": []string{}, "gpus_allocated": 0, "gpus_free": 0,
			"driver_version": "", "device_plugin_state": "unknown",
			"readiness": "ready",
		},
	})

	// AC1: list returns every node with its signals; unlabeled node is
	// unspecified.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/accelerators/nodes", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	nodes, _ := body["nodes"].([]any)
	require.Len(t, nodes, 3)

	byName := map[string]map[string]any{}
	for _, n := range nodes {
		nm := n.(map[string]any)
		byName[nm["name"].(string)] = nm
	}
	// node-a is healthy (all three signals hold).
	assert.Equal(t, "healthy", byName["node-a"]["health"])
	assert.Equal(t, "nvidia", byName["node-a"]["vendor"])
	// protobuf int64 fields marshal to JSON strings.
	assert.Equal(t, "2", byName["node-a"]["gpusAllocated"])
	assert.Equal(t, "6", byName["node-a"]["gpusFree"])
	assert.Equal(t, "535.104.05", byName["node-a"]["driverVersion"])
	assert.Equal(t, "healthy", byName["node-a"]["devicePluginState"])
	assert.Equal(t, "ready", byName["node-a"]["readiness"])
	// node-b is degraded (device plugin degraded).
	assert.Equal(t, "degraded", byName["node-b"]["health"])
	// node-c is unspecified and ready -> healthy (AC1, FR4.2).
	assert.Equal(t, "unspecified", byName["node-c"]["vendor"])
	assert.Equal(t, "healthy", byName["node-c"]["health"])

	// AC1: vendor filter.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/accelerators/nodes?vendor=nvidia", nil)
	require.Equal(t, http.StatusOK, code)
	nodes, _ = body["nodes"].([]any)
	require.Len(t, nodes, 2)

	// AC1: health filter.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/accelerators/nodes?health=degraded", nil)
	require.Equal(t, http.StatusOK, code)
	nodes, _ = body["nodes"].([]any)
	require.Len(t, nodes, 1)
	assert.Equal(t, "node-b", nodes[0].(map[string]any)["name"])

	// AC1: search + pagination.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/accelerators/nodes?search=node-a&page.offset=0&page.limit=20", nil)
	require.Equal(t, http.StatusOK, code)
	nodes, _ = body["nodes"].([]any)
	require.Len(t, nodes, 1)
	assert.Equal(t, "node-a", nodes[0].(map[string]any)["name"])

	// AC2: card type summary is sorted by vendor then free ascending and
	// omits zero-total card types.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/accelerators/card-types", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	cardTypes, _ := body["cardTypes"].([]any)
	require.Len(t, cardTypes, 1)
	row := cardTypes[0].(map[string]any)
	assert.Equal(t, "nvidia", row["vendor"])
	assert.Equal(t, "A800", row["cardType"])
	// protobuf int64 fields marshal to JSON strings.
	assert.Equal(t, "16", row["total"])
	assert.Equal(t, "10", row["allocated"])
	assert.Equal(t, "6", row["free"])
	assert.Equal(t, "2", row["nodeCount"])

	// AC3: node detail returns per-GPU, labels, taints, resources and
	// warmup context.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/accelerators/nodes/n1", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	node, _ := body["node"].(map[string]any)
	require.NotNil(t, node)
	gpus, _ := node["gpus"].([]any)
	require.Len(t, gpus, 2)
	// protobuf int64 fields marshal to JSON strings.
	assert.Equal(t, "0", gpus[0].(map[string]any)["index"])
	assert.Equal(t, true, gpus[0].(map[string]any)["allocated"])
	labels, _ := node["labels"].(map[string]any)
	assert.Equal(t, "nvidia", labels["go-taas.io/accelerator"])
	taints, _ := node["taints"].([]any)
	require.Len(t, taints, 1)
	resources, _ := node["resources"].([]any)
	require.Len(t, resources, 1)
	assert.Equal(t, "8", resources[0].(map[string]any)["allocatable"])
	// Warmup context (AC3, FR3.2).
	warmup, _ := node["warmupTasks"].([]any)
	require.Len(t, warmup, 1)
	assert.Equal(t, "warmup-1", warmup[0].(map[string]any)["taskId"])
	assert.Equal(t, "succeeded", warmup[0].(map[string]any)["nodeOutcome"])

	// AC3: a missing node id returns 10208.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/accelerators/nodes/missing", nil)
	assert.Equal(t, http.StatusInternalServerError, code)
	assert.EqualValues(t, 10208, body["code"])
}

// TestFVTAccleratorHealthComposite walks AC4: health is healthy only when
// readiness is ready, device-plugin is healthy and a driver version is
// present; any single failure degrades the composite; any unknown signal
// yields unknown.
func TestFVTAccleratorHealthComposite(t *testing.T) {
	env := newAcceleratorEnv(t)

	env.publishSnapshot(t, []map[string]any{
		{
			"node_id": "h1", "name": "healthy-node", "vendor": "nvidia",
			"driver_version": "535", "device_plugin_state": "healthy", "readiness": "ready",
		},
		{
			"node_id": "h2", "name": "no-driver", "vendor": "nvidia",
			"driver_version": "", "device_plugin_state": "healthy", "readiness": "ready",
		},
		{
			"node_id": "h3", "name": "not-ready", "vendor": "nvidia",
			"driver_version": "535", "device_plugin_state": "healthy", "readiness": "not_ready",
		},
		{
			"node_id": "h4", "name": "unknown-dp", "vendor": "nvidia",
			"driver_version": "535", "device_plugin_state": "unknown", "readiness": "ready",
		},
	})

	code, body := env.call(t, http.MethodGet, "/api/v1/admin/accelerators/nodes", nil)
	require.Equal(t, http.StatusOK, code)
	nodes, _ := body["nodes"].([]any)
	byName := map[string]map[string]any{}
	for _, n := range nodes {
		nm := n.(map[string]any)
		byName[nm["name"].(string)] = nm
	}
	assert.Equal(t, "healthy", byName["healthy-node"]["health"])
	assert.Equal(t, "degraded", byName["no-driver"]["health"])
	assert.Equal(t, "degraded", byName["not-ready"]["health"])
	assert.Equal(t, "unknown", byName["unknown-dp"]["health"])
}

// TestFVTAccleratorEmptyInventory walks AC8: an empty inventory renders
// the empty state (the list returns no nodes).
func TestFVTAccleratorEmptyInventory(t *testing.T) {
	env := newAcceleratorEnv(t)

	// No snapshot published yet -> empty cache.
	code, body := env.call(t, http.MethodGet, "/api/v1/admin/accelerators/nodes", nil)
	require.Equal(t, http.StatusOK, code)
	nodes, _ := body["nodes"].([]any)
	assert.Empty(t, nodes)

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/accelerators/card-types", nil)
	require.Equal(t, http.StatusOK, code)
	cardTypes, _ := body["cardTypes"].([]any)
	assert.Empty(t, cardTypes)
}
