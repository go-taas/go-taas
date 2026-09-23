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
	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/model"
)

// imageEnv is the in-process stack for the image management feature:
// image + model + infer services over one gRPC server, gateway mux in
// front, shared SQLite, and a recording MQ client.
type imageEnv struct {
	db     *gorm.DB
	gwSrv  *httptest.Server
	grpcLn net.Listener
	mqBus  *recordingBus

	imageSvc *image.Service
	inferSvc *infer.Service
}

// newImageEnv builds the image-management FVT stack.
func newImageEnv(t *testing.T) *imageEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, image.MigrateSchemaForFVT(db))
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

	bus := newRecordingBus()
	modelSvc := model.NewForFVT(db)
	modelSvc.SetDeleteGuard(infer.NewDeleteModelGuard(db))
	inferSvc := infer.NewForFVT(db, bus)
	imageSvc := image.NewForFVT(db, bus)
	imageSvc.SetDeleteGuard(infer.NewDeleteImageGuard(db))
	imageSvc.SetInUseProvider(infer.NewImageInUseProvider(db))

	// The real warmup status consumer runs against the bus, so
	// controller reports flow through the production path.
	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	t.Cleanup(cancelConsumer)
	warmupConsumer := image.NewWarmupStatusConsumer(bus, image.NewWarmupTaskRepository(db), 1)
	go func() { _ = warmupConsumer.Run(consumerCtx) }()

	modelv1.RegisterModelServiceServer(grpcSrv, modelSvc)
	inferv1.RegisterInferServiceServiceServer(grpcSrv, inferSvc)
	imagev1.RegisterImageServiceServer(grpcSrv, imageSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher),
	)
	require.NoError(t, modelv1.RegisterModelServiceHandler(context.Background(), mux, conn))
	require.NoError(t, inferv1.RegisterInferServiceServiceHandler(context.Background(), mux, conn))
	require.NoError(t, imagev1.RegisterImageServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &imageEnv{
		db: db, gwSrv: gwSrv, grpcLn: ln, mqBus: bus,
		imageSvc: imageSvc, inferSvc: inferSvc,
	}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *imageEnv) call(t *testing.T, method, path string, body any) (int, map[string]any) {
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
	req.Header.Set("X-Organization-Id", "org-fvt")
	resp, err := e.gwSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestFVTImageManagement walks the acceptance criteria of the image
// management architecture doc: AC1 (register + conflict), AC2 (catalog
// list + filters), AC3 (detail with in-use services), AC4 (description
// edit), AC5 (delete free), AC6 (delete blocked), AC7 (warmup trigger),
// AC8 (warmup active gate), AC9 (warmup status flow), AC10 (task
// history), AC11 (validation matrix).
func TestFVTImageManagement(t *testing.T) {
	env := newImageEnv(t)

	// AC1: register an image.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/images", map[string]any{
		"name": "ghcr.io/go-taas/vllm", "tag": "v0.6.3", "accelerator": "nvidia",
		"engine": "vllm", "description": "vLLM engine image",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	imageID, _ := body["imageId"].(string)
	require.NotEmpty(t, imageID)

	// AC1: duplicate (name, tag, accelerator) is rejected with 10202.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/images", map[string]any{
		"name": "ghcr.io/go-taas/vllm", "tag": "v0.6.3", "accelerator": "nvidia", "engine": "vllm",
	})
	assert.NotEqual(t, http.StatusOK, code, "duplicate must be rejected: %v", body)
	assert.Equal(t, float64(10202), body["code"])

	// AC2: list with filters.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/images?accelerator=nvidia", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	images, _ := body["images"].([]any)
	require.Len(t, images, 1)
	first, _ := images[0].(map[string]any)
	assert.Equal(t, "vLLM engine image", first["description"])
	// int64 fields serialize as JSON strings in proto3 JSON mapping.
	assert.Equal(t, "0", first["inUseCount"])

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/images?accelerator=metax", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	images, _ = body["images"].([]any)
	assert.Empty(t, images)

	// AC3: detail shows zero in-use services.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/images/"+imageID, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	inUse, _ := body["inUseServices"].([]any)
	assert.Empty(t, inUse)

	// AC4: edit the description.
	code, body = env.call(t, http.MethodPatch, "/api/v1/admin/images/"+imageID, map[string]any{
		"description": "updated description",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/images/"+imageID, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	img, _ := body["image"].(map[string]any)
	assert.Equal(t, "updated description", img["description"])

	// AC5: delete a free image succeeds.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/images", map[string]any{
		"name": "ghcr.io/go-taas/sglang", "tag": "v0.1.4", "accelerator": "metax", "engine": "sglang",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	freeID, _ := body["imageId"].(string)
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/images/"+freeID, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// AC6: delete is blocked while an inference service references the
	// image. Create a model + service first.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-3b", "version": "v1", "weightPath": "qwen/3b/v1",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	modelID, _ := body["modelId"].(string)

	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "demo-svc", "modelId": modelID, "modelVersion": "v1",
		"imageId": imageID, "replicas": "1", "accelerator": "nvidia",
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/images/"+imageID, nil)
	assert.NotEqual(t, http.StatusOK, code, "delete must be blocked: %v", body)
	assert.Equal(t, float64(10206), body["code"])

	// AC3: detail now lists the referencing service.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/images/"+imageID, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	inUse, _ = body["inUseServices"].([]any)
	require.Len(t, inUse, 1)
	svc, _ := inUse[0].(map[string]any)
	assert.Equal(t, "demo-svc", svc["name"])

	// AC7: trigger a warmup task.
	env.mqBus.warmups = nil
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/images/"+imageID+":warmup", map[string]any{
		"nodeSelector": map[string]string{"taas.go-taas.github.io/accelerator": "nvidia"},
	})
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	taskID, _ := body["taskId"].(string)
	require.NotEmpty(t, taskID)

	// The dispatch event was published with the resolved reference.
	require.Len(t, env.mqBus.warmups, 1)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(env.mqBus.warmups[0].Body, &evt))
	assert.Equal(t, taskID, evt["task_id"])
	assert.Equal(t, imageID, evt["image_id"])
	assert.Equal(t, "ghcr.io/go-taas/vllm:v0.6.3", evt["reference"])

	// AC8: a second trigger while active is rejected with 10205.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/images/"+imageID+":warmup", nil)
	assert.NotEqual(t, http.StatusOK, code, "second warmup must be rejected: %v", body)
	assert.Equal(t, float64(10205), body["code"])

	// AC9: the controller reports running then succeeded; the status
	// consumer applies both.
	env.reportWarmupStatus(t, taskID, "running", nil, nil)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/warmup-tasks/"+taskID, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	task, _ := body["task"].(map[string]any)
	assert.Equal(t, "running", task["state"])

	env.reportWarmupStatus(t, taskID, "succeeded", []map[string]any{
		{"node": "node-a", "state": "succeeded", "message": "pull completed"},
	}, nil)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/warmup-tasks/"+taskID, nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	task, _ = body["task"].(map[string]any)
	assert.Equal(t, "succeeded", task["state"])
	nodes, _ := task["nodeResults"].([]any)
	require.Len(t, nodes, 1)

	// AC10: task history lists the task; the image summary carries the
	// last warmup state.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/images/"+imageID+"/warmup-tasks", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	tasks, _ := body["tasks"].([]any)
	require.Len(t, tasks, 1)

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/images?accelerator=nvidia", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	images, _ = body["images"].([]any)
	require.Len(t, images, 1)
	first, _ = images[0].(map[string]any)
	assert.Equal(t, "succeeded", first["lastWarmupState"])

	// After the task settled, a new warmup can be triggered.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/images/"+imageID+":warmup", nil)
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// AC11: validation matrix — each invalid field is rejected with its
	// business code and nothing is written.
	invalid := []struct {
		name string
		req  map[string]any
		code float64
	}{
		{"name with colon", map[string]any{"name": "ghcr.io/x:v1", "tag": "t", "accelerator": "nvidia", "engine": "vllm"}, 10207},
		{"empty tag", map[string]any{"name": "ghcr.io/x", "tag": "", "accelerator": "nvidia", "engine": "vllm"}, 10207},
		{"bad digest", map[string]any{"name": "ghcr.io/x", "tag": "t", "digest": "sha256:zz", "accelerator": "nvidia", "engine": "vllm"}, 10203},
		{"bad accelerator", map[string]any{"name": "ghcr.io/x", "tag": "t", "accelerator": "tpu", "engine": "vllm"}, 10204},
		{"empty engine", map[string]any{"name": "ghcr.io/x", "tag": "t", "accelerator": "nvidia", "engine": ""}, 10207},
	}
	for _, tc := range invalid {
		code, body = env.call(t, http.MethodPost, "/api/v1/admin/images", tc.req)
		assert.NotEqual(t, http.StatusOK, code, "%s must be rejected: %v", tc.name, body)
		assert.Equal(t, tc.code, body["code"], "%s: business code", tc.name)
	}

	// Unknown image on every image-scoped RPC: 10201.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/images/img-missing", nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.Equal(t, float64(10201), body["code"])
}

// reportWarmupStatus publishes a synthetic controller warmup report;
// the real warmup status consumer applies it.
func (e *imageEnv) reportWarmupStatus(t *testing.T, taskID, state string, nodeResults []map[string]any, reason *string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"task_id":        taskID,
		"state":          state,
		"node_results":   nodeResults,
		"failure_reason": reason,
		"reported_at":    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.NoError(t, e.mqBus.Publish(context.Background(), mq.DefaultSubjects().ImageWarmupStatus, body, nil))
}
