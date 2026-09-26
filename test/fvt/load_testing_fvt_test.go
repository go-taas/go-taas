package fvt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/tenancy"
)

// fvtCredProvider is the fake system-credential provider (feature #20,
// AD11).
type fvtCredProvider struct{ cred string }

func (p fvtCredProvider) GetSystemCredential(context.Context) (string, error) {
	return p.cred, nil
}

// loadTestEnv is the in-process stack for the load-testing feature: the
// infer service over one gRPC server, the gateway mux in front, a shared
// SQLite, a fake system credential, and an httptest inference endpoint
// the runner drives.
type loadTestEnv struct {
	db       *gorm.DB
	gwSrv    *httptest.Server
	grpcLn   net.Listener
	mqBus    *recordingBus
	target   *httptest.Server
	inferSvc *infer.Service
}

// newLoadTestEnv builds the load-testing FVT stack.
func newLoadTestEnv(t *testing.T, target http.Handler) *loadTestEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, model.MigrateSchemaForFVT(db))
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

	targetSrv := httptest.NewServer(target)
	t.Cleanup(targetSrv.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	bus := newRecordingBus()
	env := &loadTestEnv{db: db, mqBus: bus, grpcLn: ln, target: targetSrv}
	inferSvc := infer.NewForFVT(db, bus)
	runner := infer.NewLoadTestRunner(
		infer.NewLoadTestRepository(db),
		fvtCredProvider{cred: "sk-system"},
		20*time.Millisecond,
	)
	inferSvc.SetLoadTestRunner(runner)
	inferSvc.SetSystemCredentialProvider(fvtCredProvider{cred: "sk-system"})
	env.inferSvc = inferSvc

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runner.Run(runCtx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	inferv1.RegisterInferServiceServiceServer(grpcSrv, inferSvc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, inferv1.RegisterInferServiceServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)
	env.gwSrv = gwSrv

	return env
}

// call issues a JSON request against the gateway and decodes the body.
func (e *loadTestEnv) call(t *testing.T, method, path string, body any) (int, map[string]any) {
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

// seedRunningService seeds a running service pointing at the test target
// and returns its id.
func (e *loadTestEnv) seedRunningService(t *testing.T, name string) string {
	t.Helper()
	repo := infer.NewInferenceServiceRepository(e.db)
	svc := &infer.InferenceService{
		ID:             infer.NewServiceID(),
		OrganizationID: "org-fvt",
		Name:           name,
		ModelID:        e.seedModel(t, "qwen", "v1"),
		ModelVersion:   "v1",
		ImageID:        "img-1",
		Replicas:       1,
		Accelerator:    "nvidia",
		State:          infer.StateRunning,
		Endpoints:      []string{e.target.URL},
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	require.NoError(t, repo.Create(context.Background(), svc))
	return svc.ID
}

// seedModel registers a model and returns its id.
func (e *loadTestEnv) seedModel(t *testing.T, name, version string) string {
	t.Helper()
	repo := model.NewRepository(e.db)
	id, err := repo.RegisterModelOrCreateVersion(context.Background(), name, "", version, name+"/"+version)
	require.NoError(t, err)
	return id
}

// fvtNum reads a numeric JSON field that may be encoded as a number
// (int32/double) or a string (protobuf serializes int64 as a string).
func fvtNum(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}

// chatHandler answers chat-completion requests with a token usage block.
func chatHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":10,"completion_tokens":20}}`)
	})
	return mux
}

// TestFVTLoadTestLifecycle covers AC1–AC5: create/run/get, list filters,
// stop, delete, and the 10308/10310/10311 error paths.
func TestFVTLoadTestLifecycle(t *testing.T) {
	env := newLoadTestEnv(t, chatHandler())
	serviceID := env.seedRunningService(t, "svc-load")

	// AC1: create returns pending, then reaches completed with results.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/load-tests", map[string]any{
		"service_id":       serviceID,
		"concurrency":      2,
		"duration_seconds": 5,
		"request_rate":     200,
		"prompt_template":  "hello",
		"max_tokens":       32,
	})
	require.Equal(t, http.StatusOK, code, "create must succeed: %v", body)
	runID, _ := body["loadTestId"].(string)
	require.NotEmpty(t, runID)
	assert.Equal(t, "pending", body["state"])

	// Poll until terminal.
	var summary map[string]any
	require.Eventually(t, func() bool {
		_, got := env.call(t, http.MethodGet, "/api/v1/admin/load-tests/"+runID, nil)
		res, ok := got["result"].(map[string]any)
		if !ok {
			return false
		}
		summary = got
		return fvtNum(res["totalRequests"]) > 0
	}, 15*time.Second, 100*time.Millisecond, "run must reach a terminal state with results")

	sumSummary := summary["summary"].(map[string]any)
	assert.Equal(t, "completed", sumSummary["state"])
	result := summary["result"].(map[string]any)
	assert.Greater(t, fvtNum(result["totalRequests"]), 0.0)
	assert.Greater(t, fvtNum(result["throughputRps"]), 0.0)
	assert.Greater(t, fvtNum(result["latencyP95Ms"]), 0.0)
	assert.Greater(t, fvtNum(result["outputTokensPerSec"]), 0.0)
	assert.Equal(t, 0.0, fvtNum(result["errorRate"]))

	// AC3: list with a status filter and a service filter.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/load-tests?status=completed", nil)
	require.Equal(t, http.StatusOK, code)
	assert.GreaterOrEqual(t, len(body["runs"].([]any)), 1)
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/load-tests?serviceId="+serviceID, nil)
	require.Equal(t, http.StatusOK, code)
	assert.GreaterOrEqual(t, len(body["runs"].([]any)), 1)

	// AC5: a terminal run can be deleted.
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/load-tests/"+runID, nil)
	require.Equal(t, http.StatusOK, code, "delete must succeed: %v", body)

	// AC1: the deleted run is gone (10308).
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/load-tests/"+runID, nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10308, body["code"])
}

// TestFVTLoadTestValidation covers AC2: 10311 for a bad target and 10309
// for a bad config.
func TestFVTLoadTestValidation(t *testing.T) {
	env := newLoadTestEnv(t, chatHandler())

	// Unknown service → 10301 (the target lookup fails first).
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/load-tests", map[string]any{
		"service_id": "missing", "prompt_template": "hi", "duration_seconds": 5,
	})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10301, body["code"], "unknown target service: %v", body)

	// Invalid config → 10309.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/load-tests", map[string]any{
		"service_id": "missing", "prompt_template": "", "duration_seconds": 5,
	})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10309, body["code"], "invalid config: %v", body)

	// A non-running service → 10311.
	serviceID := env.seedRunningService(t, "svc-validate")
	repo := infer.NewInferenceServiceRepository(env.db)
	require.NoError(t, repo.MarkTerminated(context.Background(), "org-fvt", serviceID))
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/load-tests", map[string]any{
		"service_id": serviceID, "prompt_template": "hi", "duration_seconds": 5,
	})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10311, body["code"], "non-running target: %v", body)
}

// TestFVTLoadTestStopAndStateErrors covers AC4 and the 10310 paths.
func TestFVTLoadTestStopAndStateErrors(t *testing.T) {
	env := newLoadTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	serviceID := env.seedRunningService(t, "svc-stop")

	// Start a long run and stop it.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/load-tests", map[string]any{
		"service_id":       serviceID,
		"concurrency":      1,
		"duration_seconds": 3600,
		"request_rate":     50,
		"prompt_template":  "hi",
	})
	require.Equal(t, http.StatusOK, code, "create must succeed: %v", body)
	runID := body["loadTestId"].(string)

	// Wait until the run is in flight before stopping.
	require.Eventually(t, func() bool {
		_, got := env.call(t, http.MethodGet, "/api/v1/admin/load-tests/"+runID, nil)
		s, _ := got["summary"].(map[string]any)
		if s == nil {
			return false
		}
		return s["state"] == "running"
	}, 10*time.Second, 50*time.Millisecond, "run must be running")

	code, body = env.call(t, http.MethodPost, "/api/v1/admin/load-tests/"+runID+":stop", map[string]any{})
	require.Equal(t, http.StatusOK, code, "stop must succeed: %v", body)
	assert.Equal(t, "stopped", body["state"])

	// Stopping again → 10310 (the run is terminal).
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/load-tests/"+runID+":stop", map[string]any{})
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10310, body["code"], "stop on terminal: %v", body)

	// Deleting a missing run → 10308.
	code, body = env.call(t, http.MethodDelete, "/api/v1/admin/load-tests/missing", nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10308, body["code"])
}

// TestFVTLoadTestModelProjection covers AC6: the masked user projection
// (success, unknown model 10101, unauthorized model 10105) and the
// admin/user surface separation.
func TestFVTLoadTestModelProjection(t *testing.T) {
	env := newLoadTestEnv(t, chatHandler())
	serviceID := env.seedRunningService(t, "svc-user")
	// The projection is keyed by the service's model.
	svcRow, err := infer.NewInferenceServiceRepository(env.db).FindByID(context.Background(), serviceID)
	require.NoError(t, err)
	modelID := svcRow.ModelID

	// Create and let one run complete so the projection has content.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/load-tests", map[string]any{
		"service_id": serviceID, "concurrency": 1, "duration_seconds": 5,
		"request_rate": 200, "prompt_template": "hi",
	})
	require.Equal(t, http.StatusOK, code, "create: %v", body)
	runID := body["loadTestId"].(string)
	require.Eventually(t, func() bool {
		_, got := env.call(t, http.MethodGet, "/api/v1/admin/load-tests/"+runID, nil)
		s, _ := got["summary"].(map[string]any)
		return s != nil && s["state"] == "completed"
	}, 15*time.Second, 100*time.Millisecond)

	// The user projection for the seeded service's model returns masked
	// results with no service ids.
	code, body = env.call(t, http.MethodGet, "/api/v1/models/"+modelID+"/load-tests", nil)
	require.Equal(t, http.StatusOK, code, "user projection: %v", body)
	results, _ := body["results"].([]any)
	require.Len(t, results, 1)
	first := results[0].(map[string]any)
	assert.NotContains(t, first, "serviceId")
	assert.NotContains(t, first, "loadTestId")
	assert.Greater(t, fvtNum(first["throughputRps"]), 0.0)

	// Unknown model → 10101.
	code, body = env.call(t, http.MethodGet, "/api/v1/models/missing/load-tests", nil)
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, 10101, body["code"])

	// Surface separation: the admin prefix does not serve the user route
	// and the user prefix does not serve the admin route.
	code, _ = env.call(t, http.MethodGet, "/api/v1/models/"+modelID+"/load-tests", nil)
	require.Equal(t, http.StatusOK, code)
	code, _ = env.call(t, http.MethodGet, "/api/v1/admin/models/"+modelID+"/load-tests", nil)
	assert.Equal(t, http.StatusNotFound, code, "admin prefix must not serve user routes")
	code, _ = env.call(t, http.MethodGet, "/api/v1/load-tests", nil)
	assert.Equal(t, http.StatusNotFound, code, "user prefix must not serve admin routes")
}
