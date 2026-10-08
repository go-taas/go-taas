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
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/grpcmiddleware"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/pkg/server"
	evaluationv1 "github.com/go-taas/go-taas/proto/taas/evaluation/v1"
	promptv1 "github.com/go-taas/go-taas/proto/taas/prompt/v1"
	"github.com/go-taas/go-taas/services/auth"
	"github.com/go-taas/go-taas/services/evaluation"
	"github.com/go-taas/go-taas/services/infer"
	"github.com/go-taas/go-taas/services/metering"
	"github.com/go-taas/go-taas/services/model"
	"github.com/go-taas/go-taas/services/prompt"
	"github.com/go-taas/go-taas/services/tenancy"
)

func TestFVTPromptEvaluationTenantAndCaseValidation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, prompt.MigrateSchemaForFVT(db))
	require.NoError(t, auth.MigrateSchemaForFVT(db))
	require.NoError(t, evaluation.MigrateSchemaForFVT(db))
	require.NoError(t, tenancy.MigrateSchemaForFVT(db))
	require.NoError(t, model.MigrateSchemaForFVT(db))
	require.NoError(t, infer.MigrateSchemaForFVT(db))
	require.NoError(t, metering.MigrateSchemaForFVT(db))
	for _, orgID := range []string{"org-a", "org-b"} {
		require.NoError(t, db.Create(&tenancy.Organization{ID: orgID, DisplayName: orgID, State: tenancy.StateActive}).Error)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcmiddleware.UnaryServerInterceptor()))
	t.Cleanup(grpcSrv.Stop)

	// Feature #44 (AD5): the run must execute through a real
	// completion-returning metered inference path. The FVT stack wires
	// the infer-backed provider against an httptest chat-completion
	// target, the evaluation worker runner, and the standard metering
	// event consumer, then asserts the real completion, token usage,
	// check results and metering attribution (voucher + request log
	// with org/key/model).
	cfg := &config.Configuration{}
	cfg.Evaluation.Worker.Enabled = true
	cfg.Evaluation.Worker.PollInterval = 50 * time.Millisecond
	cfg.Evaluation.MaxInFlightRunsPerOrg = 10
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })

	chatTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"the answer is 42"}}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`)
	}))
	t.Cleanup(chatTarget.Close)

	// The metering bus captures the standard metering.events payloads.
	bus := mq.NewFake()
	meteringSvc := metering.NewForFVT(db, bus)
	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	t.Cleanup(cancelConsumer)
	go func() { _ = metering.NewEventConsumer(bus, meteringSvc, 1).Run(consumerCtx) }()

	promptSvc := prompt.NewForFVT(db)
	evalSvc := evaluation.NewForFVT(db, promptSvc)
	evalSvc.SetAPIKeyValidator(auth.NewForFVT(db))
	// The real completion seam: infer provider + system credential +
	// running service with the chat target endpoint.
	modelRepo := model.NewRepository(db)
	modelID, err := modelRepo.RegisterModelOrCreateVersion(context.Background(), "qwen-eval", "", "v1", "qwen-eval/v1")
	require.NoError(t, err)
	inferRepo := infer.NewInferenceServiceRepository(db)
	require.NoError(t, inferRepo.Create(context.Background(), &infer.InferenceService{
		ID: infer.NewServiceID(), OrganizationID: "org-a", Name: "svc-eval", ModelID: modelID, ModelVersion: "v1",
		ImageID: "img-1", Replicas: 1, Accelerator: "nvidia", State: infer.StateRunning,
		Endpoints: []string{chatTarget.URL}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}))
	evalSvc.SetCompletionProvider(infer.NewEvaluationCompletionProvider(inferRepo, fvtCredProvider{cred: "sk-system"}, modelRepo))
	evalSvc.SetMeteringPublisher(evaluation.NewMQMeteringPublisher(bus))

	promptv1.RegisterPromptServiceServer(grpcSrv, promptSvc)
	evaluationv1.RegisterEvaluationServiceServer(grpcSrv, evalSvc)
	go func() { _ = grpcSrv.Serve(ln) }()
	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// The evaluation worker executes pending runs.
	runner := evaluation.NewRunner(evalSvc)
	runner.SetPollInterval(50 * time.Millisecond)
	runCtx, cancelRunner := context.WithCancel(context.Background())
	t.Cleanup(cancelRunner)
	done := make(chan struct{})
	go func() { defer close(done); _ = runner.Run(runCtx) }()
	t.Cleanup(func() { cancelRunner(); <-done })

	mux := runtime.NewServeMux(runtime.WithIncomingHeaderMatcher(server.FVTHeaderMatcher))
	require.NoError(t, promptv1.RegisterPromptServiceHandler(context.Background(), mux, conn))
	require.NoError(t, evaluationv1.RegisterEvaluationServiceHandler(context.Background(), mux, conn))
	gateway := httptest.NewServer(mux)
	t.Cleanup(gateway.Close)
	call := func(method, path string, body any, org string) (int, map[string]any) {
		var raw []byte
		if body != nil {
			raw, err = json.Marshal(body)
			require.NoError(t, err)
		}
		req, err := http.NewRequest(method, gateway.URL+path, bytes.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Organization-Id", org)
		resp, err := gateway.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		result := map[string]any{}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		return resp.StatusCode, result
	}

	status, promptBody := call(http.MethodPost, "/api/v1/prompts", map[string]any{
		"name": "support", "content": "Answer ${question}", "variables": []string{"${question}"},
	}, "org-a")
	require.Equal(t, http.StatusOK, status, "%v", promptBody)
	promptObj := promptBody["prompt"].(map[string]any)
	promptID := promptObj["promptId"].(string)

	status, body := call(http.MethodPost, "/api/v1/evaluations", map[string]any{
		"name": "support-regression", "promptId": promptID, "promptVersion": 1,
	}, "org-a")
	require.Equal(t, http.StatusOK, status, "%v", body)
	suite := body["evaluation"].(map[string]any)
	suiteID := suite["evaluationId"].(string)

	status, body = call(http.MethodGet, "/api/v1/evaluations/"+suiteID, nil, "org-b")
	require.NotEqual(t, http.StatusOK, status)
	require.Equal(t, float64(12801), body["code"])

	status, body = call(http.MethodPost, "/api/v1/evaluations/"+suiteID+"/cases", map[string]any{
		"name": "missing variable", "variables": map[string]string{},
		"checks": []map[string]string{{"type": evaluation.CheckContains, "expected": "answer"}},
	}, "org-a")
	require.NotEqual(t, http.StatusOK, status)
	require.Equal(t, float64(12803), body["code"])

	status, body = call(http.MethodPost, "/api/v1/evaluations/"+suiteID+"/cases", map[string]any{
		"name": "valid case", "variables": map[string]string{"question": "hello"},
		"checks": []map[string]string{{"type": evaluation.CheckContains, "expected": "answer"}},
	}, "org-a")
	require.Equal(t, http.StatusOK, status, "%v", body)
	caseObj := body["case"].(map[string]any)
	caseID := caseObj["caseId"].(string)
	require.NotEmpty(t, caseID)

	unknownKeyStatus, unknownKeyBody := call(http.MethodPost, "/api/v1/evaluations/"+suiteID+"/runs", map[string]any{
		"modelId": "model-fvt", "apiKeyId": "00000000-0000-0000-0000-000000000001", "promptVersion": 1, "caseIds": []string{caseID},
	}, "org-a")
	require.NotEqual(t, http.StatusOK, unknownKeyStatus)
	require.Equal(t, float64(10007), unknownKeyBody["code"])
	validKey := &auth.APIKey{ID: "77777777-7777-4777-8777-777777777777", Name: "fvt-key", Prefix: "sk-fvt", LookupHash: "digest", Salt: "salt", SaltedHash: "hash", OrganizationID: "org-a", CreatedAt: time.Now().UTC()}
	require.NoError(t, db.Create(validKey).Error)
	status, body = call(http.MethodPost, "/api/v1/evaluations/"+suiteID+"/runs", map[string]any{
		"modelId": modelID, "apiKeyId": validKey.ID, "promptVersion": 1, "caseIds": []string{caseID},
	}, "org-a")
	require.Equal(t, http.StatusOK, status, "%v", body)
	run := body["run"].(map[string]any)
	runID := run["runId"].(string)
	// The run is created pending and executes through the real metered
	// completion path (AD5); the worker drives it to a terminal state.
	require.Equal(t, "pending", run["status"])
	require.NotContains(t, run, "apiKey")

	// Wait for the worker to finish the run and assert the real
	// completion, check results and usage (AD5: never a simulated
	// completion).
	require.Eventually(t, func() bool {
		code, getBody := call(http.MethodGet, "/api/v1/evaluations/"+suiteID+"/runs/"+runID, nil, "org-a")
		if code != http.StatusOK {
			return false
		}
		return getBody["run"].(map[string]any)["status"] == "completed"
	}, 10*time.Second, 100*time.Millisecond)
	status, body = call(http.MethodGet, "/api/v1/evaluations/"+suiteID+"/runs/"+runID, nil, "org-a")
	require.Equal(t, http.StatusOK, status, "%v", body)
	run = body["run"].(map[string]any)
	require.Equal(t, "completed", run["status"])
	require.Equal(t, float64(1), run["passedCount"])
	require.Equal(t, float64(0), run["errorCount"])
	require.Equal(t, "Answer ${question}", run["promptContent"])
	runCases := run["cases"].([]any)
	require.Len(t, runCases, 1)
	caseResult := runCases[0].(map[string]any)
	require.Equal(t, "passed", caseResult["status"])
	require.Equal(t, "the answer is 42", caseResult["completion"])
	require.Equal(t, "10", caseResult["inputTokens"])
	require.Equal(t, "20", caseResult["outputTokens"])
	checkResults := caseResult["checkResults"].([]any)
	require.Len(t, checkResults, 1)
	require.Equal(t, true, checkResults[0].(map[string]any)["passed"])

	// AD5: the call flowed through the standard metering path — a
	// voucher and request log carry the org/key/model attribution.
	require.Eventually(t, func() bool {
		var voucherCount int64
		require.NoError(t, db.Model(&metering.Voucher{}).Where("organization_id = ? AND api_key_id = ? AND model_id = ?", "org-a", validKey.ID, modelID).Count(&voucherCount).Error)
		return voucherCount >= 1
	}, 10*time.Second, 100*time.Millisecond)
	var voucher metering.Voucher
	require.NoError(t, db.Where("organization_id = ? AND api_key_id = ?", "org-a", validKey.ID).First(&voucher).Error)
	require.Equal(t, int64(10), voucher.PromptTokens)
	require.Equal(t, int64(20), voucher.CompletionTokens)

	status, body = call(http.MethodGet, "/api/v1/evaluations/"+suiteID+"/runs/"+runID, nil, "org-b")
	require.NotEqual(t, http.StatusOK, status)
	require.Equal(t, float64(12801), body["code"])

	status, body = call(http.MethodGet, "/api/v1/evaluations/"+suiteID, nil, "org-a")
	require.Equal(t, http.StatusOK, status, "%v", body)
	gotSuite := body["evaluation"].(map[string]any)
	require.Equal(t, float64(1), gotSuite["caseCount"])

	status, body = call(http.MethodPatch, "/api/v1/evaluations/"+suiteID, map[string]any{
		"name": "support-regression-v2", "description": "updated",
	}, "org-a")
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.Equal(t, "support-regression-v2", body["evaluation"].(map[string]any)["name"])

	status, body = call(http.MethodDelete, "/api/v1/evaluations/"+suiteID, nil, "org-a")
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = call(http.MethodGet, "/api/v1/evaluations/"+suiteID, nil, "org-a")
	require.NotEqual(t, http.StatusOK, status)
	require.Equal(t, float64(12801), body["code"])
}
