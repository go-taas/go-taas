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
	finetuningv1 "github.com/go-taas/go-taas/proto/taas/finetuning/v1"
	"github.com/go-taas/go-taas/services/finetuning"
	"github.com/go-taas/go-taas/services/tenancy"
)

// finetuningEnv is the in-process stack for the model fine-tuning
// feature: the finetuning service over one gRPC server with the gateway
// mux in front, a shared SQLite database, and fake model/deployer
// providers.
type finetuningEnv struct {
	db    *gorm.DB
	gwSrv *httptest.Server

	svc *finetuning.Service
}

func newFinetuningEnv(t *testing.T) *finetuningEnv {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, finetuning.MigrateSchemaForFVT(db))
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

	svc := finetuning.NewForFVT(db)
	svc.SetModelResolver(fvtModelResolver{exists: true})
	svc.SetServiceDeployer(fvtServiceDeployer{serviceID: "svc-ft"})

	finetuningv1.RegisterFineTuningServiceServer(grpcSrv, svc)
	go func() { _ = grpcSrv.Serve(ln) }()

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, finetuningv1.RegisterFineTuningServiceHandler(context.Background(), mux, conn))

	gwSrv := httptest.NewServer(mux)
	t.Cleanup(gwSrv.Close)

	return &finetuningEnv{db: db, gwSrv: gwSrv, svc: svc}
}

type fvtModelResolver struct{ exists bool }

func (f fvtModelResolver) ModelVersionExists(context.Context, string, string) (bool, error) {
	return f.exists, nil
}

func (f fvtModelResolver) RegisterModelVersion(_ context.Context, _, _, _ string) (string, error) {
	return "ft-model", nil
}

type fvtServiceDeployer struct{ serviceID string }

func (f fvtServiceDeployer) DeployModel(_ context.Context, _, _, _, _, _ string, _ int32) (string, error) {
	return f.serviceID, nil
}

func (e *finetuningEnv) call(t *testing.T, method, path string, body any, org string) (int, map[string]any) {
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

// TestFVTModelFineTuning walks the acceptance criteria of the model
// fine-tuning architecture doc (feature #39): AC1 (register + list
// datasets), AC2 (create job returns pending), AC3 (list + get job),
// AC4 (deploy succeeded job).
func TestFVTModelFineTuning(t *testing.T) {
	env := newFinetuningEnv(t)

	// AC1: register a dataset.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/finetuning/datasets", map[string]any{
		"name": "train", "format": "jsonl", "objectPath": "data/train.jsonl",
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	datasetID, _ := body["datasetId"].(string)
	require.NotEmpty(t, datasetID)

	// AC1: list datasets.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/finetuning/datasets", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	datasets, _ := body["datasets"].([]any)
	require.Len(t, datasets, 1)

	// AC2: create a job returns state=pending.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/finetuning/jobs", map[string]any{
		"name": "job-1", "baseModelId": "m1", "baseModelVersion": "v1", "datasetId": datasetID,
		"hyperparameters": map[string]any{"epochs": 3, "batchSize": 4, "learningRate": 0.001},
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	jobID, _ := body["jobId"].(string)
	require.NotEmpty(t, jobID)
	assert.Equal(t, "pending", body["state"])

	// AC3: list jobs.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/finetuning/jobs", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	jobs, _ := body["jobs"].([]any)
	require.Len(t, jobs, 1)

	// AC3: get the job.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/finetuning/jobs/"+jobID, nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	job, _ := body["job"].(map[string]any)
	require.NotNil(t, job)
	summary, _ := job["summary"].(map[string]any)
	assert.Equal(t, "job-1", summary["name"])

	// AC4: deploy on a pending job -> 12302.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/finetuning/jobs/"+jobID+":deploy", map[string]any{
		"imageId": "img", "accelerator": "nvidia", "replicas": 1,
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "deploy on pending must be rejected: %v", body)
	assert.Equal(t, float64(12302), body["code"])

	// Mark the job succeeded and deploy.
	require.NoError(t, env.db.Model(&finetuning.FineTuningJob{}).Where("id = ?", jobID).Update("state", "succeeded").Error)
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/finetuning/jobs/"+jobID+":deploy", map[string]any{
		"imageId": "img", "accelerator": "nvidia", "replicas": 1,
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "svc-ft", body["serviceId"])
}