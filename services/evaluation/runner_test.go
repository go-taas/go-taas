package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakeCompletions is a scriptable CompletionProvider for tests.
type fakeCompletions struct {
	result *CompletionResult
	err    error
	calls  []*CompletionInput
}

func (f *fakeCompletions) CompleteModelCall(_ context.Context, in *CompletionInput) (*CompletionResult, error) {
	f.calls = append(f.calls, in)
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// fakeMetering captures published metering events.
type fakeMetering struct {
	events [][]byte
}

func (f *fakeMetering) PublishMeteringEvent(_ context.Context, event []byte) error {
	f.events = append(f.events, event)
	return nil
}

// fakeCosts is a scriptable CostAttributor.
type fakeCosts struct {
	cost int64
}

func (f *fakeCosts) EstimateCaseCost(_ context.Context, _, _ string, _, _ int64, _ time.Time) (int64, bool) {
	return f.cost, f.cost > 0
}

func newRunnerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateSchemaForFVT(db))
	return db
}

func seedPendingRun(t *testing.T, db *gorm.DB, checks string) *EvaluationRun {
	t.Helper()
	now := time.Now().UTC()
	run := &EvaluationRun{
		RunID: "11111111-1111-4111-8111-111111111111", EvaluationID: "22222222-2222-4222-8222-222222222222",
		OrganizationID: "org-a", Status: RunPending, PromptID: "33333333-3333-4333-8333-333333333333",
		PromptVersion: 1, PromptContentSnapshot: "Answer ${question}", PromptVariables: `["${question}"]`,
		ModelID: "model-a", APIKeyID: "44444444-4444-4444-8444-444444444444", CaseCount: 1,
		CreatedBy: "user-a", CreatedAt: now, RetainedUntil: now.Add(2160 * time.Hour), Currency: "USD",
	}
	require.NoError(t, db.Create(run).Error)
	require.NoError(t, db.Create(&EvaluationRunCase{
		ResultID: "66666666-6666-4666-8666-666666666666", RunID: run.RunID, CasePosition: 0,
		CaseName: "case-1", Variables: `{"question":"hi"}`, Checks: checks, Status: CasePending, CreatedAt: now,
	}).Error)
	return run
}

func TestRunnerExecutesPendingRunWithRealCompletion(t *testing.T) {
	db := newRunnerTestDB(t)
	run := seedPendingRun(t, db, `[{"type":"contains","expected":"answer"}]`)
	completions := &fakeCompletions{result: &CompletionResult{
		Completion: "the answer is 42", PromptTokens: 10, CompletionTokens: 20, LatencyMS: 120, ServiceID: "svc-1",
	}}
	meteringBus := &fakeMetering{}
	svc := NewForFVT(db, nil)
	svc.SetCompletionProvider(completions)
	svc.SetMeteringPublisher(meteringBus)
	svc.SetCostAttributor(&fakeCosts{cost: 25})

	runner := NewRunner(svc)
	claimed, err := runner.sweep(context.Background())
	require.NoError(t, err)
	require.True(t, claimed)

	// The provider received the rendered prompt with org/model/key
	// identity (AD5: model_id + api_key_id, never the secret).
	require.Len(t, completions.calls, 1)
	require.Equal(t, "Answer hi", completions.calls[0].Prompt)
	require.Equal(t, "org-a", completions.calls[0].OrganizationID)
	require.Equal(t, "model-a", completions.calls[0].ModelID)
	require.Equal(t, "44444444-4444-4444-8444-444444444444", completions.calls[0].APIKeyID)

	var stored EvaluationRun
	require.NoError(t, db.Where("run_id = ?", run.RunID).First(&stored).Error)
	require.Equal(t, RunCompleted, stored.Status)
	require.Equal(t, 1, stored.PassedCount)
	require.Equal(t, 0, stored.ErrorCount)
	require.Equal(t, int64(25), stored.TotalCostMinor)
	require.Equal(t, int64(120), stored.MedianLatencyMS)
	require.NotNil(t, stored.StartedAt)
	require.NotNil(t, stored.CompletedAt)

	var result EvaluationRunCase
	require.NoError(t, db.Where("run_id = ?", run.RunID).First(&result).Error)
	require.Equal(t, CasePassed, result.Status)
	require.Equal(t, "the answer is 42", result.Completion)
	require.Equal(t, int64(10), result.InputTokens)
	require.Equal(t, int64(20), result.OutputTokens)
	require.Equal(t, int64(25), result.CostMinor)
	var checkResults []CheckResult
	require.NoError(t, json.Unmarshal([]byte(result.CheckResults), &checkResults))
	require.Len(t, checkResults, 1)
	require.True(t, checkResults[0].Passed)

	// The standard metering event carries the org/key/model
	// attribution (AD5).
	require.Len(t, meteringBus.events, 1)
	var event map[string]any
	require.NoError(t, json.Unmarshal(meteringBus.events[0], &event))
	require.Equal(t, "org-a", event["organization_id"])
	require.Equal(t, "44444444-4444-4444-8444-444444444444", event["api_key_id"])
	require.Equal(t, "model-a", event["model_id"])
	require.Equal(t, "success", event["status"])
	usage := event["usage"].(map[string]any)
	require.Equal(t, float64(10), usage["prompt_tokens"])
	require.Equal(t, float64(20), usage["completion_tokens"])
}

func TestRunnerCheckFailureMarksCaseFailed(t *testing.T) {
	db := newRunnerTestDB(t)
	seedPendingRun(t, db, `[{"type":"exact","expected":"different"}]`)
	svc := NewForFVT(db, nil)
	svc.SetCompletionProvider(&fakeCompletions{result: &CompletionResult{Completion: "the answer is 42"}})

	runner := NewRunner(svc)
	claimed, err := runner.sweep(context.Background())
	require.NoError(t, err)
	require.True(t, claimed)

	var stored EvaluationRun
	require.NoError(t, db.First(&stored).Error)
	require.Equal(t, RunCompleted, stored.Status)
	require.Equal(t, 1, stored.FailedCount)
	require.Equal(t, 0, stored.PassedCount)
}

func TestRunnerProviderErrorFailsClosed(t *testing.T) {
	db := newRunnerTestDB(t)
	run := seedPendingRun(t, db, `[{"type":"contains","expected":"answer"}]`)
	meteringBus := &fakeMetering{}
	svc := NewForFVT(db, nil)
	svc.SetCompletionProvider(&fakeCompletions{err: errors.New("no ready service")})
	svc.SetMeteringPublisher(meteringBus)

	runner := NewRunner(svc)
	claimed, err := runner.sweep(context.Background())
	require.NoError(t, err)
	require.True(t, claimed)

	// A real provider failure persists a case error — never a
	// simulated completion (AD5).
	var stored EvaluationRun
	require.NoError(t, db.Where("run_id = ?", run.RunID).First(&stored).Error)
	require.Equal(t, RunCompletedWithErrors, stored.Status)
	require.Equal(t, 1, stored.ErrorCount)
	var result EvaluationRunCase
	require.NoError(t, db.Where("run_id = ?", run.RunID).First(&result).Error)
	require.Equal(t, CaseError, result.Status)
	require.Empty(t, result.Completion)
	require.NotEmpty(t, result.ErrorMessage)
	// The error is metered through the standard path too.
	require.Len(t, meteringBus.events, 1)
	var event map[string]any
	require.NoError(t, json.Unmarshal(meteringBus.events[0], &event))
	require.Equal(t, "error", event["status"])
}

func TestRunnerRespectsCancellationBetweenCases(t *testing.T) {
	db := newRunnerTestDB(t)
	run := seedPendingRun(t, db, `[{"type":"contains","expected":"answer"}]`)
	// Request cancellation before the sweep claims the run.
	now := time.Now().UTC()
	require.NoError(t, db.Model(&EvaluationRun{}).Where("run_id = ?", run.RunID).Update("cancel_requested_at", now).Error)

	svc := NewForFVT(db, nil)
	svc.SetCompletionProvider(&fakeCompletions{result: &CompletionResult{Completion: "x"}})
	runner := NewRunner(svc)
	claimed, err := runner.sweep(context.Background())
	require.NoError(t, err)
	require.True(t, claimed)

	var stored EvaluationRun
	require.NoError(t, db.Where("run_id = ?", run.RunID).First(&stored).Error)
	require.Equal(t, RunCancelled, stored.Status)
	require.Equal(t, 1, stored.CancelledCount)
	var result EvaluationRunCase
	require.NoError(t, db.Where("run_id = ?", run.RunID).First(&result).Error)
	require.Equal(t, CaseCancelled, result.Status)
}

func TestClaimPendingRunLeasesExactlyOnce(t *testing.T) {
	db := newRunnerTestDB(t)
	seedPendingRun(t, db, `[]`)
	repo := NewRepository(db)

	first, err := repo.claimPendingRun(context.Background(), "worker-1", time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, first)
	require.Equal(t, RunRunning, first.Status)
	require.Equal(t, "worker-1", first.LeaseOwner)

	// A second claim sweep finds nothing pending.
	second, err := repo.claimPendingRun(context.Background(), "worker-2", time.Now().UTC().Add(time.Minute))
	require.NoError(t, err)
	require.Nil(t, second)
}

func TestSaveCaseResultIsImmutableOnceWritten(t *testing.T) {
	db := newRunnerTestDB(t)
	run := seedPendingRun(t, db, `[]`)
	var result EvaluationRunCase
	require.NoError(t, db.Where("run_id = ?", run.RunID).First(&result).Error)

	result.Status = CasePassed
	result.Completion = "first"
	require.NoError(t, NewRepository(db).saveCaseResult(context.Background(), &result))

	// A second write to the same case is rejected.
	result.Completion = "second"
	err := NewRepository(db).saveCaseResult(context.Background(), &result)
	require.ErrorIs(t, err, ErrEvaluationRunStateInvalid)
}

func TestRenderPromptSubstitutesVariables(t *testing.T) {
	require.Equal(t, "Answer hello with 2 vars", renderPrompt("Answer ${q} with ${n} vars", `{"q":"hello","n":"2"}`))
	require.Equal(t, "no vars", renderPrompt("no vars", `{}`))
	require.Equal(t, "keep ${missing}", renderPrompt("keep ${missing}", `{"other":"x"}`))
}

func TestMedianLatency(t *testing.T) {
	require.Zero(t, median(nil))
	require.EqualValues(t, 5, median([]int64{5}))
	require.EqualValues(t, 20, median([]int64{10, 20, 30}))
	require.EqualValues(t, 20, median([]int64{30, 10, 20}))
}

func TestBuildMeteringEventPayload(t *testing.T) {
	raw := buildMeteringEvent("req-1", "org-a", "key-1", "model-a", "svc-1", 3, 4, time.Unix(1700000000, 0), 250, "success", "")
	var event map[string]any
	require.NoError(t, json.Unmarshal(raw, &event))
	require.Equal(t, "req-1", event["request_id"])
	require.Equal(t, "svc-1", event["service_id"])
	require.EqualValues(t, 1700000000, event["completed_at"])
	require.EqualValues(t, 250, event["latency_ms"])
	usage := event["usage"].(map[string]any)
	require.Equal(t, float64(3), usage["prompt_tokens"])
	require.Equal(t, float64(4), usage["completion_tokens"])
}
