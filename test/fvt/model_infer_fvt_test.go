package fvt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	imagev1 "github.com/go-taas/go-taas/proto/taas/image/v1"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	modelv1 "github.com/go-taas/go-taas/proto/taas/model/v1"
	"github.com/go-taas/go-taas/services/image"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/model"
)

// modelInferEnv is the in-process stack for the model catalog and
// one-click deployment feature: model + image + infer services over one
// gRPC server, gateway mux in front, shared SQLite, and a recording MQ
// client that doubles as the fake controller.
type modelInferEnv struct {
	db      *gorm.DB
	gwSrv   *httptest.Server
	grpcLn  net.Listener
	grpcSrv *grpc.Server
	mqBus   *recordingBus

	modelSvc *model.Service
	inferSvc *infer.Service
}

// recordingBus is an mq.Client that records published messages and
// delivers them to registered subscribers: the fake controller on the
// changes subject, the real status consumer on the status subject.
type recordingBus struct {
	mu      sync.Mutex
	changes []mq.Message
	status  []mq.Message
	subs    map[string][]mq.Handler
}

func newRecordingBus() *recordingBus {
	return &recordingBus{subs: map[string][]mq.Handler{}}
}

func (b *recordingBus) Publish(_ context.Context, subject string, body []byte, headers map[string]string) error {
	msg := mq.Message{Subject: subject, Body: body, Headers: headers, Timestamp: time.Now()}
	b.mu.Lock()
	switch subject {
	case mq.DefaultSubjects().InferServiceChanges:
		b.changes = append(b.changes, msg)
	case mq.DefaultSubjects().InferServiceStatus:
		b.status = append(b.status, msg)
	}
	handlers := append([]mq.Handler(nil), b.subs[subject]...)
	b.mu.Unlock()
	for _, h := range handlers {
		if err := h(msg); err != nil {
			return err
		}
	}
	return nil
}

func (b *recordingBus) Subscribe(_ context.Context, subject string, handler mq.Handler) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[subject] = append(b.subs[subject], handler)
	return nil
}

func (b *recordingBus) Close() error { return nil }

func newModelInferEnv(t *testing.T) *modelInferEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	// Seed the image registry with the three catalog images.
	restore := image.ResetForTest([]*image.Summary{
		{ImageID: "img-vllm-nvidia", Name: "ghcr.io/go-taas/vllm", Tag: "v0.6.3", Accelerator: "nvidia", Engine: "vllm"},
		{ImageID: "img-vllm-iluvatar", Name: "ghcr.io/go-taas/vllm-iluvatar", Tag: "v0.6.3", Accelerator: "iluvatar", Engine: "vllm"},
		{ImageID: "img-sglang-metax", Name: "ghcr.io/go-taas/sglang", Tag: "v0.1.4", Accelerator: "metax", Engine: "sglang"},
	})
	t.Cleanup(restore)

	// Real gRPC server on an ephemeral port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer()
	t.Cleanup(grpcSrv.Stop)

	bus := newRecordingBus()
	modelSvc := model.NewForFVT(db)
	modelSvc.SetDeleteGuard(infer.NewDeleteModelGuard(db))
	inferSvc := infer.NewForFVT(db, bus)
	imageSvc := image.New(nil)

	// The real status consumer runs against the bus, so controller
	// reports flow through the production path.
	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	t.Cleanup(cancelConsumer)
	statusConsumer := infer.NewStatusConsumer(bus, infer.NewInferenceServiceRepository(db), 1)
	go func() { _ = statusConsumer.Run(consumerCtx) }()

	modelv1.RegisterModelServiceServer(grpcSrv, modelSvc)
	inferv1.RegisterInferServiceServiceServer(grpcSrv, inferSvc)
	imagev1.RegisterImageServiceServer(grpcSrv, imageSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	// Gateway mux in front of the gRPC server.
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

	return &modelInferEnv{
		db: db, gwSrv: gwSrv, grpcLn: ln, grpcSrv: grpcSrv,
		mqBus: bus, modelSvc: modelSvc, inferSvc: inferSvc,
	}
}

// call issues a JSON request against the gateway and decodes the body.
func (e *modelInferEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

// reportStatus publishes a synthetic controller status report; the
// real status consumer applies it.
func (e *modelInferEnv) reportStatus(t *testing.T, serviceID, state string, endpoints []string, reason *string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"service_id":     serviceID,
		"state":          state,
		"endpoints":      endpoints,
		"failure_reason": reason,
		"reported_at":    time.Now().UTC().Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.NoError(t, e.mqBus.Publish(context.Background(), mq.DefaultSubjects().InferServiceStatus, body, nil))
}

// TestFVTModelCatalogAndDeployment walks the acceptance criteria of the
// model catalog + one-click deployment architecture doc: AC1 (register
// + conflict), AC2 (ordered versions), AC3 (delete blocked), AC4 (fast
// create), AC5 (validation publishes nothing), AC6 (status flow),
// AC7 (failure reason), AC8 (scale), AC9 (idempotent delete).
func TestFVTModelCatalogAndDeployment(t *testing.T) {
	env := newModelInferEnv(t)

	// AC1: register a model, then a new version of it.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-3b", "version": "v1", "weightPath": "qwen/3b/v1", "description": "Qwen 3B",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	modelID, _ := body["modelId"].(string)
	require.NotEmpty(t, modelID)

	code, body = env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-3b", "version": "v2", "weightPath": "qwen/3b/v2",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, modelID, body["modelId"])

	// AC1: duplicate (model, version) is rejected.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-3b", "version": "v1", "weightPath": "qwen/3b/v1",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "duplicate must be rejected: %v", body)

	// AC2: detail lists versions newest first.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models/"+modelID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	versions, _ := body["versions"].([]any)
	require.Len(t, versions, 2)
	assert.Equal(t, "v2", versions[0])
	assert.Equal(t, "v1", versions[1])

	// List models.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/models", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	models, _ := body["models"].([]any)
	require.Len(t, models, 1)

	// Image catalog is served.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/images", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	images, _ := body["images"].([]any)
	require.Len(t, images, 3)

	// AC4: create returns service_id quickly (synchronous validation
	// only, no cluster wait).
	env.mqBus.changes = nil
	start := time.Now()
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "demo-svc", "modelId": modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": "2", "accelerator": "nvidia",
	}, "org-fvt")
	elapsed := time.Since(start)
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Less(t, elapsed, 2*time.Second, "create must return in < 2s (AC4)")
	serviceID, _ := body["serviceId"].(string)
	require.NotEmpty(t, serviceID)

	// Exactly one upsert change event was published.
	require.Len(t, env.mqBus.changes, 1)
	var evt map[string]any
	require.NoError(t, json.Unmarshal(env.mqBus.changes[0].Body, &evt))
	assert.Equal(t, "upsert", evt["event_type"])
	assert.Equal(t, serviceID, evt["service_id"])
	assert.Equal(t, "demo-svc", evt["name"])

	// AC5: a validation failure publishes nothing.
	before := len(env.mqBus.changes)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "bad-svc", "modelId": modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": "0", "accelerator": "nvidia",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "invalid replicas must be rejected: %v", body)
	assert.Len(t, env.mqBus.changes, before, "no event on validation failure (AC5)")

	// AC6: the fake controller drives pending → deploying → running.
	env.reportStatus(t, serviceID, "deploying", nil, nil)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	svc, _ := body["service"].(map[string]any)
	require.NotNil(t, svc)
	assert.Equal(t, "deploying", svc["state"])
	assert.Empty(t, body["endpoints"], "endpoints hidden until running")

	env.reportStatus(t, serviceID, "running", []string{"https://infer.example.com/demo/v1"}, nil)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	svc, _ = body["service"].(map[string]any)
	assert.Equal(t, "running", svc["state"])
	endpoints, _ := body["endpoints"].([]any)
	require.Len(t, endpoints, 1)
	assert.Equal(t, "https://infer.example.com/demo/v1", endpoints[0])

	// AC3: delete-model is blocked while the service references it.
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/models/"+modelID, nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "delete must be blocked: %v", body)
	errDetail := fmt.Sprint(body)
	assert.Contains(t, errDetail, "demo-svc", "error names the blocking service (AC3)")

	// AC8: scale changes replicas only.
	env.mqBus.changes = nil
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services/"+serviceID+":scale", map[string]any{
		"replicas": "3",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Len(t, env.mqBus.changes, 1)
	require.NoError(t, json.Unmarshal(env.mqBus.changes[0].Body, &evt))
	assert.EqualValues(t, float64(3), evt["replicas"])

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	svc, _ = body["service"].(map[string]any)
	assert.EqualValues(t, "3", fmt.Sprint(svc["replicas"]))
	assert.Equal(t, "running", svc["state"], "scale must not touch state (AC8)")

	// AC7: fault injection reports failed with a reason.
	reason := "image pull backoff"
	env.reportStatus(t, serviceID, "failed", nil, &reason)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	svc, _ = body["service"].(map[string]any)
	assert.Equal(t, "failed", svc["state"])

	// AC9: delete is idempotent; terminated services leave the list.
	env.mqBus.changes = nil
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.Len(t, env.mqBus.changes, 1, "one delete event")

	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/inference-services/"+serviceID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Len(t, env.mqBus.changes, 1, "second delete publishes nothing (AC9)")

	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	services, _ := body["services"].([]any)
	assert.Empty(t, services, "terminated services are hidden (AC9)")

	// AC3 (unblocked): with the service terminated, the model deletes.
	code, _ = env.call(t, http.MethodDelete, "/api/v1/admin/models/"+modelID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code)

	code, _ = env.call(t, http.MethodGet, "/api/v1/admin/models/"+modelID, nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "deleted model is gone")
}

// TestFVTCrossOrgIsolation verifies inference services are scoped to
// the caller organization.
func TestFVTCrossOrgIsolation(t *testing.T) {
	env := newModelInferEnv(t)

	code, body := env.call(t, http.MethodPost, "/api/v1/admin/models", map[string]any{
		"name": "qwen-3b", "version": "v1", "weightPath": "qwen/3b/v1",
	}, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	modelID, _ := body["modelId"].(string)

	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "org-a-svc", "modelId": modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": "1", "accelerator": "nvidia",
	}, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	serviceID, _ := body["serviceId"].(string)

	// org-b sees neither the service in its list...
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/inference-services", nil, "org-b")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	services, _ := body["services"].([]any)
	assert.Empty(t, services)

	// ...nor by id.
	code, _ = env.call(t, http.MethodGet, "/api/v1/admin/inference-services/"+serviceID, nil, "org-b")
	assert.NotEqual(t, http.StatusOK, code)

	// The same name is available in another organization.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/inference-services", map[string]any{
		"name": "org-a-svc", "modelId": modelID, "modelVersion": "v1",
		"imageId": "img-vllm-nvidia", "replicas": "1", "accelerator": "nvidia",
	}, "org-b")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
}
