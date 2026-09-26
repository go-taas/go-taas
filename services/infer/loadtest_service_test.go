package infer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/services/model"
)

// fakeCredProvider is a SystemCredentialProvider fake.
type fakeCredProvider struct {
	cred string
	err  error
}

func (f fakeCredProvider) GetSystemCredential(context.Context) (string, error) {
	return f.cred, f.err
}

// newLoadTestEnv builds a service with the load-test runner wired and
// the runner started, plus the seeding helpers.
func newLoadTestEnv(t *testing.T) (*Service, *gorm.DB, *LoadTestRunner) {
	t.Helper()
	db := newInferTestDB(t)
	svc := NewForFVT(db, &recordingClient{})
	runner := NewLoadTestRunner(NewLoadTestRepository(db), fakeCredProvider{cred: "sk-system"}, 20*time.Millisecond)
	svc.SetLoadTestRunner(runner)
	svc.SetSystemCredentialProvider(fakeCredProvider{cred: "sk-system"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runner.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return svc, db, runner
}

// seedRunningServiceWithEndpoint seeds a running service pointing at
// endpoint for the given model.
func seedRunningServiceWithEndpoint(t *testing.T, db *gorm.DB, modelID, endpoint string) *InferenceService {
	t.Helper()
	repo := NewInferenceServiceRepository(db)
	svc := &InferenceService{
		ID:             NewServiceID(),
		OrganizationID: "org-a",
		Name:           "svc-loadtest",
		ModelID:        modelID,
		ModelVersion:   "v1",
		ImageID:        "img-1",
		Replicas:       1,
		Accelerator:    "nvidia",
		State:          StateRunning,
		Endpoints:      []string{endpoint},
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	require.NoError(t, repo.Create(context.Background(), svc))
	return svc
}

func TestLoadTestRunnerNameAndProgressWhileRunning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(15 * time.Millisecond)
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":3,"completion_tokens":4}}`)
	}))
	defer server.Close()

	svc, db, runner := newLoadTestEnv(t)
	assert.Equal(t, "infer-load-test-runner", runner.RunnerName())

	ctx := context.Background()
	modelID := seedModel(t, db, "qwen", "v1")
	target := seedRunningServiceWithEndpoint(t, db, modelID, server.URL)

	created, err := svc.CreateLoadTest(ctx, &inferv1.CreateLoadTestRequest{
		ServiceId:       target.ID,
		Concurrency:     2,
		DurationSeconds: 3600,
		RequestRate:     200,
		PromptTemplate:  "hi",
	})
	require.NoError(t, err)

	// While the run is live, GetLoadTest returns the progress block.
	require.Eventually(t, func() bool {
		got, err := svc.GetLoadTest(ctx, &inferv1.GetLoadTestRequest{LoadTestId: created.GetLoadTestId()})
		return err == nil && got.GetProgress() != nil && got.GetProgress().GetRequestsSent() > 0
	}, 10*time.Second, 20*time.Millisecond, "live progress must be reported")

	// The runner's in-flight map exposes the live counters.
	progress, ok := runner.Progress(created.GetLoadTestId())
	require.True(t, ok)
	assert.Greater(t, progress.RequestsSent, int64(0))

	require.True(t, runner.Stop(created.GetLoadTestId()))
}

func TestGetLoadTestProgressFromRowWhenNotInFlight(t *testing.T) {
	// A pending row that the runner has not picked up falls back to the
	// persisted counters.
	svc, db, _ := newLoadTestEnv(t)
	ctx := context.Background()
	repo := NewLoadTestRepository(db)
	start := time.Now().UTC().Add(-30 * time.Second)
	require.NoError(t, repo.Create(ctx, &LoadTest{
		ID:              "run-pending",
		ServiceName:     "svc",
		ModelID:         "model-1",
		ModelName:       "qwen-3b",
		Concurrency:     1,
		DurationSeconds: 60,
		PromptTemplate:  "hi",
		MaxTokens:       8,
		State:           LoadTestStatePending,
		StartedAt:       &start,
		TotalRequests:   42,
		SuccessCount:    40,
		FailureCount:    2,
		CreatedAt:       start,
		UpdatedAt:       start,
	}))

	got, err := svc.GetLoadTest(ctx, &inferv1.GetLoadTestRequest{LoadTestId: "run-pending"})
	require.NoError(t, err)
	require.NotNil(t, got.GetProgress())
	assert.Equal(t, int64(42), got.GetProgress().GetRequestsSent())
	assert.GreaterOrEqual(t, got.GetProgress().GetElapsedSeconds(), int64(29))
	assert.Nil(t, got.GetResult(), "a non-terminal run has no result block")
}

// TestResponseMigrateCreatesLoadTestSchema covers the production Migrate
// hook: the runner queries load_tests at startup (recovery), so the
// table must exist after Migrate. The FVT helper is a separate path and
// does not prove this (a bug that shipped once).
func TestResponseMigrateCreatesLoadTestSchema(t *testing.T) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})

	svc := New(&fakeComponents{db: db, mqType: "client"})
	require.NoError(t, svc.Migrate(context.Background()))

	assert.True(t, db.Migrator().HasTable(&LoadTest{}), "Migrate must create the load_tests table")
	assert.True(t, db.Migrator().HasTable(&InferenceService{}))
	assert.True(t, db.Migrator().HasTable(&AutoscalingPolicy{}))
}

// TestLoadTestSchemaIndexes pins the composite indexes the list filters
// and the masked user projection rely on. The column order is the point:
// an index with the same name but different columns is a different index.
func TestLoadTestSchemaIndexes(t *testing.T) {
	db := newInferTestDB(t)
	// Columns() excludes the implicit ordering suffix GORM adds per column.
	assertIndexColumns(t, db, "idx_load_tests_service_created", []string{"service_id", "created_at"})
	assertIndexColumns(t, db, "idx_load_tests_model_state_created", []string{"model_id", "state", "created_at"})
	assertIndexColumns(t, db, "idx_load_tests_state_created", []string{"state", "created_at"})
}

// assertIndexColumns asserts the ordered columns of a named index.
func assertIndexColumns(t *testing.T, db *gorm.DB, name string, want []string) {
	t.Helper()
	indexes, err := db.Migrator().GetIndexes(&LoadTest{})
	require.NoError(t, err)
	for _, idx := range indexes {
		if idx.Name() == name {
			assert.Equal(t, want, idx.Columns(), "index %s columns", name)
			return
		}
	}
	t.Fatalf("index %s not found", name)
}

func TestLoadTestRepositoryResolvesAndCaches(t *testing.T) {
	db := newInferTestDB(t)
	// A components-backed service resolves the repository from the DB and
	// caches it for subsequent calls.
	svc := New(&fakeComponents{db: db, mqType: "client"})
	first, err := svc.loadTestRepository()
	require.NoError(t, err)
	second, err := svc.loadTestRepository()
	require.NoError(t, err)
	assert.Same(t, first, second, "the repository must be resolved once")

	// A repository-less service with a wired infer repository falls back
	// to that repository's DB.
	fallback := NewForFVT(db, &recordingClient{})
	repo, err := fallback.loadTestRepository()
	require.NoError(t, err)
	require.NotNil(t, repo)
}

func TestCreateLoadTestDisabledByKillSwitch(t *testing.T) {
	db := newInferTestDB(t)
	svc := NewForFVT(db, &recordingClient{})
	// No runner wired → fail closed (10311).
	_, err := svc.CreateLoadTest(context.Background(), &inferv1.CreateLoadTestRequest{
		ServiceId: "svc", PromptTemplate: "hi",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeLoadTestTargetInvalid, ae.Code)
}

func TestNormalizeLoadTestConfigDefaultsAndBounds(t *testing.T) {
	// Defaults fill zero values (proto3 cannot distinguish unset).
	cfg, err := normalizeLoadTestConfig(&inferv1.CreateLoadTestRequest{PromptTemplate: "hi"})
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.concurrency)
	assert.Equal(t, 60, cfg.durationSeconds)
	assert.Equal(t, 256, cfg.maxTokens)
	assert.Equal(t, 0, cfg.requestRate)

	// Explicit in-range values survive.
	cfg, err = normalizeLoadTestConfig(&inferv1.CreateLoadTestRequest{
		Concurrency: 8, DurationSeconds: 30, RequestRate: 100, PromptTemplate: "hi", MaxTokens: 64,
	})
	require.NoError(t, err)
	assert.Equal(t, 8, cfg.concurrency)

	// Out-of-range values return 10309.
	for _, req := range []*inferv1.CreateLoadTestRequest{
		{Concurrency: 1001, PromptTemplate: "hi"},
		{Concurrency: -1, PromptTemplate: "hi"},
		{DurationSeconds: 4, PromptTemplate: "hi"},
		{DurationSeconds: 3601, PromptTemplate: "hi"},
		{RequestRate: 10001, PromptTemplate: "hi"},
		{PromptTemplate: ""},
		{MaxTokens: 8193, PromptTemplate: "hi"},
		{MaxTokens: -1, PromptTemplate: "hi"},
	} {
		_, err := normalizeLoadTestConfig(req)
		require.Error(t, err)
		ae, ok := apierrors.As(err)
		require.True(t, ok)
		assert.Equal(t, apierrors.CodeLoadTestConfigInvalid, ae.Code)
	}
}

func TestCreateLoadTestTargetInvalid(t *testing.T) {
	svc, db, _ := newLoadTestEnv(t)
	ctx := context.Background()
	modelID := seedModel(t, db, "qwen", "v1")

	// Unknown service id → 10301 (the target lookup fails first).
	_, err := svc.CreateLoadTest(ctx, &inferv1.CreateLoadTestRequest{
		ServiceId: "missing", PromptTemplate: "hi", DurationSeconds: 5,
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeInferServiceNotFound, ae.Code)

	// A non-running service → 10311.
	repo := NewInferenceServiceRepository(db)
	pending := seedService(t, repo, "org-a", "svc-pending", StatePending, time.Now().UTC())
	_, err = svc.CreateLoadTest(ctx, &inferv1.CreateLoadTestRequest{
		ServiceId: pending.ID, PromptTemplate: "hi", DurationSeconds: 5,
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeLoadTestTargetInvalid, ae.Code)

	// A running service with no endpoint → 10311.
	noEndpoint := seedService(t, repo, "org-a", "svc-no-endpoint", StateRunning, time.Now().UTC())
	_, err = svc.CreateLoadTest(ctx, &inferv1.CreateLoadTestRequest{
		ServiceId: noEndpoint.ID, PromptTemplate: "hi", DurationSeconds: 5,
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeLoadTestTargetInvalid, ae.Code)

	// An invalid config → 10309 (checked before the target).
	_, err = svc.CreateLoadTest(ctx, &inferv1.CreateLoadTestRequest{
		ServiceId: noEndpoint.ID, PromptTemplate: "", DurationSeconds: 5,
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeLoadTestConfigInvalid, ae.Code)

	_ = modelID
}

func TestCreateLoadTestRunsAndCompletes(t *testing.T) {
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		assert.Equal(t, "Bearer sk-system", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":10,"completion_tokens":20}}`)
	}))
	defer server.Close()

	svc, db, _ := newLoadTestEnv(t)
	ctx := context.Background()
	modelID := seedModel(t, db, "qwen", "v1")
	target := seedRunningServiceWithEndpoint(t, db, modelID, server.URL)

	resp, err := svc.CreateLoadTest(ctx, &inferv1.CreateLoadTestRequest{
		ServiceId:       target.ID,
		Concurrency:     2,
		DurationSeconds: 5,
		RequestRate:     200,
		PromptTemplate:  "hello",
		MaxTokens:       32,
	})
	require.NoError(t, err)
	assert.Equal(t, LoadTestStatePending, resp.GetState())
	require.NotEmpty(t, resp.GetLoadTestId())

	// Wait for the run to reach a terminal state.
	var row *LoadTest
	require.Eventually(t, func() bool {
		row, err = NewLoadTestRepository(db).FindByID(ctx, resp.GetLoadTestId())
		return err == nil && row.IsTerminal()
	}, 10*time.Second, 50*time.Millisecond, "run must reach a terminal state")

	assert.Equal(t, LoadTestStateCompleted, row.State)
	assert.Greater(t, row.TotalRequests, int64(0))
	assert.Equal(t, row.TotalRequests, row.SuccessCount)
	assert.Greater(t, row.OutputTokens, int64(0))
	assert.Greater(t, atomic.LoadInt64(&hits), int64(0))

	// GetLoadTest returns the full result set.
	got, err := svc.GetLoadTest(ctx, &inferv1.GetLoadTestRequest{LoadTestId: resp.GetLoadTestId()})
	require.NoError(t, err)
	assert.Equal(t, "hello", got.GetPromptTemplate())
	require.NotNil(t, got.GetResult())
	assert.Equal(t, row.TotalRequests, got.GetResult().GetTotalRequests())
	assert.Greater(t, got.GetResult().GetThroughputRps(), 0.0)
	assert.Greater(t, got.GetResult().GetLatencyP95Ms(), 0.0)
}

func TestGetLoadTestNotFound(t *testing.T) {
	svc, _, _ := newLoadTestEnv(t)
	_, err := svc.GetLoadTest(context.Background(), &inferv1.GetLoadTestRequest{LoadTestId: "missing"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeLoadTestNotFound, ae.Code)
}

func TestListLoadTestsFiltersSearchPagination(t *testing.T) {
	svc, db, _ := newLoadTestEnv(t)
	ctx := context.Background()
	repo := NewLoadTestRepository(db)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedLoadTestRow(t, repo, "run-alpha", "svc-alpha", "model-1", LoadTestStateCompleted, base)
	seedLoadTestRow(t, repo, "run-beta", "svc-beta", "model-2", LoadTestStateFailed, base.Add(time.Hour))

	// No filter: both rows, newest first.
	resp, err := svc.ListLoadTests(ctx, &inferv1.ListLoadTestsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetRuns(), 2)
	assert.Equal(t, "run-beta", resp.GetRuns()[0].GetLoadTestId())
	assert.Equal(t, int64(2), resp.GetPageMeta().GetTotal())

	// Status filter.
	resp, err = svc.ListLoadTests(ctx, &inferv1.ListLoadTestsRequest{Status: LoadTestStateFailed})
	require.NoError(t, err)
	require.Len(t, resp.GetRuns(), 1)
	assert.Equal(t, "run-beta", resp.GetRuns()[0].GetLoadTestId())

	// Model filter.
	resp, err = svc.ListLoadTests(ctx, &inferv1.ListLoadTestsRequest{ModelId: "model-1"})
	require.NoError(t, err)
	require.Len(t, resp.GetRuns(), 1)
	assert.Equal(t, "run-alpha", resp.GetRuns()[0].GetLoadTestId())

	// Case-insensitive service-name search.
	resp, err = svc.ListLoadTests(ctx, &inferv1.ListLoadTestsRequest{Search: "ALPHA"})
	require.NoError(t, err)
	require.Len(t, resp.GetRuns(), 1)
	assert.Equal(t, "run-alpha", resp.GetRuns()[0].GetLoadTestId())

	// Pagination.
	resp, err = svc.ListLoadTests(ctx, &inferv1.ListLoadTestsRequest{
		Page: &commonv1.PageRequest{Offset: 1, Limit: 1},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetRuns(), 1)
	assert.Equal(t, "run-alpha", resp.GetRuns()[0].GetLoadTestId())
	assert.Equal(t, int64(2), resp.GetPageMeta().GetTotal())
}

func TestStopLoadTestStateMachine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		_, _ = fmt.Fprint(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer server.Close()

	svc, db, runner := newLoadTestEnv(t)
	ctx := context.Background()
	repo := NewLoadTestRepository(db)

	// A terminal run cannot be stopped (10310).
	seedLoadTestRow(t, repo, "run-done", "svc", "model-1", LoadTestStateCompleted, time.Now().UTC())
	_, err := svc.StopLoadTest(ctx, &inferv1.StopLoadTestRequest{LoadTestId: "run-done"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeLoadTestStateInvalid, ae.Code)

	// A pending run that is not in flight cannot be stopped either.
	seedLoadTestRow(t, repo, "run-ghost", "svc", "model-1", LoadTestStatePending, time.Now().UTC())
	_, err = svc.StopLoadTest(ctx, &inferv1.StopLoadTestRequest{LoadTestId: "run-ghost"})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeLoadTestStateInvalid, ae.Code)

	// A live run stops and retains partial results.
	modelID := seedModel(t, db, "qwen", "v1")
	target := seedRunningServiceWithEndpoint(t, db, modelID, server.URL)
	created, err := svc.CreateLoadTest(ctx, &inferv1.CreateLoadTestRequest{
		ServiceId:       target.ID,
		Concurrency:     1,
		DurationSeconds: 3600,
		RequestRate:     50,
		PromptTemplate:  "hi",
	})
	require.NoError(t, err)

	// Wait until the runner has picked the run up (it registers the run
	// in flight before marking it running).
	require.Eventually(t, func() bool {
		_, ok := runner.Progress(created.GetLoadTestId())
		return ok
	}, 5*time.Second, 10*time.Millisecond, "runner must pick up the run")

	stopped, err := svc.StopLoadTest(ctx, &inferv1.StopLoadTestRequest{LoadTestId: created.GetLoadTestId()})
	require.NoError(t, err)
	assert.Equal(t, LoadTestStateStopped, stopped.GetState())

	row, err := repo.FindByID(ctx, created.GetLoadTestId())
	require.NoError(t, err)
	assert.Equal(t, LoadTestStateStopped, row.State)
}

func TestDeleteLoadTestStateMachine(t *testing.T) {
	// No runner here: the runner's startup recovery would flip the seeded
	// running row to failed before the delete is attempted.
	db := newInferTestDB(t)
	svc := NewForFVT(db, &recordingClient{})
	ctx := context.Background()
	repo := NewLoadTestRepository(db)

	// A running run cannot be deleted (10310).
	seedLoadTestRow(t, repo, "run-running", "svc", "model-1", LoadTestStateRunning, time.Now().UTC())
	_, err := svc.DeleteLoadTest(ctx, &inferv1.DeleteLoadTestRequest{LoadTestId: "run-running"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeLoadTestStateInvalid, ae.Code)

	// A terminal run is deleted.
	seedLoadTestRow(t, repo, "run-terminal", "svc", "model-1", LoadTestStateStopped, time.Now().UTC())
	_, err = svc.DeleteLoadTest(ctx, &inferv1.DeleteLoadTestRequest{LoadTestId: "run-terminal"})
	require.NoError(t, err)
	_, err = repo.FindByID(ctx, "run-terminal")
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeLoadTestNotFound, ae.Code)

	// A missing run → 10308.
	_, err = svc.DeleteLoadTest(ctx, &inferv1.DeleteLoadTestRequest{LoadTestId: "missing"})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeLoadTestNotFound, ae.Code)
}

func TestGetModelLoadTestsMaskedProjection(t *testing.T) {
	svc, db, _ := newLoadTestEnv(t)
	ctx := orgContext("org-a")
	repo := NewLoadTestRepository(db)
	modelID := seedModel(t, db, "qwen", "v1")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedLoadTestRowFull(t, repo, "run-completed", modelID, LoadTestStateCompleted, base)
	seedLoadTestRowFull(t, repo, "run-failed", modelID, LoadTestStateFailed, base.Add(time.Hour))
	seedLoadTestRowFull(t, repo, "run-running", modelID, LoadTestStateRunning, base.Add(2*time.Hour))

	resp, err := svc.GetModelLoadTests(ctx, &inferv1.GetModelLoadTestsRequest{ModelId: modelID})
	require.NoError(t, err)
	// Only completed runs, newest first.
	require.Len(t, resp.GetResults(), 1)
	assert.Greater(t, resp.GetResults()[0].GetThroughputRps(), 0.0)

	// Unknown model → 10101.
	_, err = svc.GetModelLoadTests(ctx, &inferv1.GetModelLoadTestsRequest{ModelId: "missing"})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)

	// Empty model id → 10101.
	_, err = svc.GetModelLoadTests(ctx, &inferv1.GetModelLoadTestsRequest{ModelId: ""})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelNotFound, ae.Code)
}

func TestGetModelLoadTestsUnauthorized(t *testing.T) {
	svc, db, _ := newLoadTestEnv(t)
	modelID := seedModel(t, db, "restricted", "v1")

	// Restrict the model by granting another organization access
	// (feature #13: a model with grants is restricted to those orgs).
	modelRepo := model.NewRepository(db)
	require.NoError(t, modelRepo.GrantAccess(context.Background(), modelID, "org-other", "tester"))

	_, err := svc.GetModelLoadTests(orgContext("org-a"), &inferv1.GetModelLoadTestsRequest{ModelId: modelID})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeModelUnauthorized, ae.Code)
}

func TestRunnerRecoversInterruptedRuns(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewLoadTestRepository(db)
	ctx := context.Background()
	seedLoadTestRow(t, repo, "run-interrupted", "svc", "model-1", LoadTestStateRunning, time.Now().UTC())

	runner := NewLoadTestRunner(repo, fakeCredProvider{cred: "sk"}, time.Second)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runner.Run(runCtx)
	}()

	require.Eventually(t, func() bool {
		row, err := repo.FindByID(ctx, "run-interrupted")
		return err == nil && row.State == LoadTestStateFailed
	}, 2*time.Second, 20*time.Millisecond)
	cancel()
	<-done

	row, err := repo.FindByID(ctx, "run-interrupted")
	require.NoError(t, err)
	require.NotNil(t, row.FailureReason)
	assert.Contains(t, *row.FailureReason, "restarted")
}

func TestRunnerFailsWhenTargetUnavailable(t *testing.T) {
	// An endpoint that always refuses connections.
	svc, db, _ := newLoadTestEnv(t)
	ctx := context.Background()
	modelID := seedModel(t, db, "qwen", "v1")
	target := seedRunningServiceWithEndpoint(t, db, modelID, "http://127.0.0.1:1")

	created, err := svc.CreateLoadTest(ctx, &inferv1.CreateLoadTestRequest{
		ServiceId:       target.ID,
		Concurrency:     1,
		DurationSeconds: 3600,
		PromptTemplate:  "hi",
	})
	require.NoError(t, err)

	repo := NewLoadTestRepository(db)
	require.Eventually(t, func() bool {
		row, err := repo.FindByID(ctx, created.GetLoadTestId())
		return err == nil && row.State == LoadTestStateFailed
	}, 10*time.Second, 50*time.Millisecond)

	row, err := repo.FindByID(ctx, created.GetLoadTestId())
	require.NoError(t, err)
	require.NotNil(t, row.FailureReason)
	assert.Contains(t, *row.FailureReason, "unavailable")
}

func TestRunnerCredentialFailureFailsRun(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewLoadTestRepository(db)
	runner := NewLoadTestRunner(repo, fakeCredProvider{err: assert.AnError}, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runner.Run(ctx)
	}()

	run := &LoadTest{
		ID:              "run-nocred",
		ServiceName:     "svc",
		ModelName:       "qwen",
		Concurrency:     1,
		DurationSeconds: 5,
		PromptTemplate:  "hi",
		MaxTokens:       8,
		State:           LoadTestStatePending,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	require.NoError(t, repo.Create(context.Background(), run))
	require.NoError(t, runner.Submit(run, "http://127.0.0.1:1"))

	require.Eventually(t, func() bool {
		row, err := repo.FindByID(context.Background(), "run-nocred")
		return err == nil && row.State == LoadTestStateFailed
	}, 5*time.Second, 20*time.Millisecond)

	cancel()
	<-done
}

func TestLoadTestAggregatorResult(t *testing.T) {
	agg := newLoadTestAggregator()
	for i := 1; i <= 100; i++ {
		agg.recordSuccess(float64(i), 2, 3)
	}
	for i := 0; i < 10; i++ {
		agg.recordFailure()
	}
	res := agg.result(110.0)
	assert.Equal(t, int64(110), res.TotalRequests)
	assert.Equal(t, int64(100), res.SuccessCount)
	assert.Equal(t, int64(10), res.FailureCount)
	assert.InDelta(t, 10.0/110.0, res.ErrorRate, 1e-9)
	assert.InDelta(t, 1.0, res.ThroughputRPS, 1e-9)
	assert.InDelta(t, 300.0/110.0, res.OutputTokensPerSec, 1e-9)
	assert.Equal(t, int64(200), res.InputTokens)
	assert.Equal(t, int64(300), res.OutputTokens)
	assert.Equal(t, 50.0, res.LatencyP50Ms)
	assert.Equal(t, 90.0, res.LatencyP90Ms)
	assert.Equal(t, 95.0, res.LatencyP95Ms)
	assert.Equal(t, 99.0, res.LatencyP99Ms)
}

func TestPercentileEdgeCases(t *testing.T) {
	assert.Equal(t, 0.0, percentile(nil, 95))
	assert.Equal(t, 7.0, percentile([]float64{7}, 50))
	assert.Equal(t, 1.0, percentile([]float64{1, 2, 3}, 1))
	assert.Equal(t, 3.0, percentile([]float64{1, 2, 3}, 100))
}

func TestChatCompletionsURL(t *testing.T) {
	assert.Equal(t, "http://svc:8000/v1/chat/completions", chatCompletionsURL("http://svc:8000"))
	assert.Equal(t, "http://svc:8000/v1/chat/completions", chatCompletionsURL("http://svc:8000/"))
	assert.Equal(t, "http://svc:8000/v1/chat/completions", chatCompletionsURL("http://svc:8000/v1"))
}

func TestLoadTestElapsedSeconds(t *testing.T) {
	assert.Equal(t, int64(0), elapsedSeconds(&LoadTest{}))
	start := time.Now().UTC().Add(-90 * time.Second)
	completed := time.Now().UTC()
	assert.InDelta(t, 90, elapsedSeconds(&LoadTest{StartedAt: &start, CompletedAt: &completed}), 1)
}

func TestDeleteBeforeRetention(t *testing.T) {
	db := newInferTestDB(t)
	repo := NewLoadTestRepository(db)
	ctx := context.Background()
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedLoadTestRow(t, repo, "run-old", "svc", "model-1", LoadTestStateCompleted, old)
	seedLoadTestRow(t, repo, "run-new", "svc", "model-1", LoadTestStateCompleted, time.Now().UTC())
	seedLoadTestRow(t, repo, "run-active", "svc", "model-1", LoadTestStateRunning, old)

	deleted, err := repo.DeleteBefore(ctx, time.Now().UTC().Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	// The active run is never deleted by retention.
	_, err = repo.FindByID(ctx, "run-active")
	require.NoError(t, err)
}

// seedLoadTestRow inserts a minimal run with the given state.
func seedLoadTestRow(t *testing.T, repo *LoadTestRepository, id, serviceName, modelID, state string, created time.Time) {
	t.Helper()
	require.NoError(t, repo.Create(context.Background(), &LoadTest{
		ID:              id,
		ServiceID:       "svc-" + id,
		ServiceName:     serviceName,
		ModelID:         modelID,
		ModelName:       "qwen-3b",
		Concurrency:     2,
		DurationSeconds: 30,
		PromptTemplate:  "hi",
		MaxTokens:       16,
		State:           state,
		CreatedAt:       created,
		UpdatedAt:       created,
	}))
}

// seedLoadTestRowFull inserts a completed run with a non-zero result set
// for the masked-projection tests.
func seedLoadTestRowFull(t *testing.T, repo *LoadTestRepository, id, modelID, state string, completed time.Time) {
	t.Helper()
	require.NoError(t, repo.Create(context.Background(), &LoadTest{
		ID:                 id,
		ServiceID:          "svc-" + id,
		ServiceName:        "svc",
		ModelID:            modelID,
		ModelName:          "qwen-3b",
		Concurrency:        4,
		DurationSeconds:    30,
		PromptTemplate:     "hi",
		MaxTokens:          16,
		State:              state,
		StartedAt:          &completed,
		CompletedAt:        &completed,
		TotalRequests:      100,
		SuccessCount:       100,
		ThroughputRPS:      25.5,
		OutputTokensPerSec: 500,
		LatencyP95Ms:       120,
		CreatedAt:          completed,
		UpdatedAt:          completed,
	}))
}
