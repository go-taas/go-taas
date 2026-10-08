package fvt

import (
	"bytes"
	"context"
	"encoding/base64"
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
	batchv1 "github.com/go-taas/go-taas/proto/taas/batch/v1"
	"github.com/go-taas/go-taas/services/batch"
	"github.com/go-taas/go-taas/services/tenancy"
)

// batchEnv is the in-process stack for the batch inference feature.
type batchEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server
	store *batch.MemFileStore
	svc   *batch.Service
}

func newBatchEnv(t *testing.T) *batchEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, batch.MigrateSchemaForFVT(db))
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

	store := batch.NewMemFileStore()
	svc := batch.NewForFVT(db, store)

	batchv1.RegisterBatchServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, batchv1.RegisterBatchServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &batchEnv{db: db, gwSrv: gwSrv, store: store, svc: svc}
}

func (e *batchEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

func validBatchJSONL() []byte {
	return []byte(
		`{"custom_id":"a","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}}` + "\n" +
			`{"custom_id":"b","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4o","messages":[{"role":"user","content":"yo"}]}}` + "\n",
	)
}

// TestFVTBatchInference walks the acceptance criteria of the batch
// inference architecture doc (feature #42): AC1 (create returns
// validating, 12603/12604/12605), AC2 (list + get + 12601), AC3 (cancel
// + 12602), AC4 (download + 12606), AC6 (tenant-scoped).
func TestFVTBatchInference(t *testing.T) {
	env := newBatchEnv(t)

	// AC1: create a batch job returns status=validating.
	code, body := env.call(t, http.MethodPost, "/api/v1/batch", map[string]any{
		"input_file":        base64.StdEncoding.EncodeToString(validBatchJSONL()),
		"completion_window": "24h",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	job, _ := body["batchJob"].(map[string]any)
	require.NotNil(t, job)
	batchID, _ := job["batchId"].(string)
	require.NotEmpty(t, batchID)
	assert.Equal(t, "BATCH_JOB_STATUS_VALIDATING", job["status"])
	assert.Equal(t, "gpt-4o", job["model"])

	// AC1: invalid JSONL -> 12603.
	code, body = env.call(t, http.MethodPost, "/api/v1/batch", map[string]any{
		"input_file": base64.StdEncoding.EncodeToString([]byte(`{"bad`)),
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "invalid JSONL must be rejected: %v", body)
	assert.Equal(t, float64(12603), body["code"])

	// AC1: multi-model -> 12605.
	multiModel := `{"custom_id":"a","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-4o"}}` + "\n" +
		`{"custom_id":"b","method":"POST","url":"/v1/chat/completions","body":{"model":"gpt-3.5"}}` + "\n"
	code, body = env.call(t, http.MethodPost, "/api/v1/batch", map[string]any{
		"input_file": base64.StdEncoding.EncodeToString([]byte(multiModel)),
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "multi-model must be rejected: %v", body)
	assert.Equal(t, float64(12605), body["code"])

	// AC2: list batch jobs.
	code, body = env.call(t, http.MethodGet, "/api/v1/batch", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	jobs, _ := body["batchJobs"].([]any)
	require.Len(t, jobs, 1)

	// AC2: get the batch job.
	code, body = env.call(t, http.MethodGet, "/api/v1/batch/"+batchID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	got, _ := body["batchJob"].(map[string]any)
	assert.Equal(t, batchID, got["batchId"])

	// AC2: unknown batch id -> 12601.
	code, body = env.call(t, http.MethodGet, "/api/v1/batch/00000000-0000-0000-0000-000000000000", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "unknown batch must be rejected: %v", body)
	assert.Equal(t, float64(12601), body["code"])

	// AC3: cancel the batch job.
	code, body = env.call(t, http.MethodPost, "/api/v1/batch/"+batchID+"/cancel", map[string]any{}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	cancelled, _ := body["batchJob"].(map[string]any)
	assert.Equal(t, "BATCH_JOB_STATUS_CANCELLED", cancelled["status"])

	// AC3: cancel in terminal state -> 12602.
	code, body = env.call(t, http.MethodPost, "/api/v1/batch/"+batchID+"/cancel", map[string]any{}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "terminal cancel must be rejected: %v", body)
	assert.Equal(t, float64(12602), body["code"])

	// AC4: download before completed -> 12606.
	code, body = env.call(t, http.MethodGet, "/api/v1/batch/"+batchID+"/result", nil, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "download before completed must be rejected: %v", body)
	assert.Equal(t, float64(12606), body["code"])

	// AC6: tenant-scoped — org-b cannot see org-fvt's job.
	code, body = env.call(t, http.MethodGet, "/api/v1/batch/"+batchID, nil, "org-b")
	assert.NotEqual(t, http.StatusOK, code, "cross-tenant get must be rejected: %v", body)
	assert.Equal(t, float64(12601), body["code"])

	// Admin surface: list all jobs across tenants.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/batch", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	adminJobs, _ := body["batchJobs"].([]any)
	require.Len(t, adminJobs, 1)
}
