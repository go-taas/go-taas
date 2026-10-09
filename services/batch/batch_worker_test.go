package batch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeInferenceClient returns a fixed response and token usage.
type fakeInferenceClient struct{}

func (f *fakeInferenceClient) Complete(_ context.Context, _ string, _ string, _ json.RawMessage) (json.RawMessage, int64, int64, error) {
	return json.RawMessage(`{"id":"x","choices":[]}`), 10, 5, nil
}

// fakeMeterer records metered calls and returns a fixed cost.
type fakeMeterer struct {
	calls int
	cost  int64
}

func (f *fakeMeterer) Meter(_ context.Context, _ string, _ string, _ int64, _ int64) (int64, error) {
	f.calls++
	return f.cost, nil
}

func TestBatchWorkerProcessesJob(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	store := NewMemFileStore()
	ctx := context.Background()

	content := validJSONL()
	job := seedJob(t, db, "org-a")
	key, err := store.PutInput(ctx, "org-a", job.BatchID, content)
	require.NoError(t, err)
	require.NoError(t, repo.UpdateBatchJob(ctx, &BatchJob{BatchID: job.BatchID, InputFileKey: key}))
	require.NoError(t, repo.SetStatus(ctx, job.BatchID, StatusInProgress, nil, nil))

	infer := &fakeInferenceClient{}
	meterer := &fakeMeterer{cost: 25}
	worker := NewBatchWorker(repo, store, infer, meterer, 2, time.Second)
	worker.RunOnce(ctx)

	got, err := repo.FindBatchJobByID(ctx, "org-a", job.BatchID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, got.Status)
	assert.Equal(t, int64(2), got.SucceededRequests)
	assert.Equal(t, int64(2), got.ProcessedRequests)
	assert.Equal(t, int64(20), got.InputTokens)
	assert.Equal(t, int64(10), got.OutputTokens)
	assert.Equal(t, int64(50), got.Cost)
	assert.Equal(t, 2, meterer.calls)
	assert.NotNil(t, got.CompletedAt)
	assert.NotNil(t, got.FilesExpireAt)

	// Result file written.
	result, err := store.GetFile(ctx, resultKey("org-a", job.BatchID))
	require.NoError(t, err)
	assert.Contains(t, string(result), "custom_id")
}

// TestBatchWorkerPromotesValidatingJob verifies the worker owns the
// validating -> in_progress transition (AD3): a freshly created job
// (status validating, the only status CreateBatchJob writes) is picked
// up and driven to completed without any external status writer.
func TestBatchWorkerPromotesValidatingJob(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	store := NewMemFileStore()
	ctx := context.Background()

	content := validJSONL()
	job := seedJob(t, db, "org-a")
	key, err := store.PutInput(ctx, "org-a", job.BatchID, content)
	require.NoError(t, err)
	// No manual SetStatus: the job stays validating as created.
	// UpdateBatchJob writes the status field too, so carry it over.
	require.NoError(t, repo.UpdateBatchJob(ctx, &BatchJob{
		BatchID:      job.BatchID,
		Status:       StatusValidating,
		InputFileKey: key,
	}))

	worker := NewBatchWorker(repo, store, &fakeInferenceClient{}, &fakeMeterer{cost: 25}, 2, time.Second)
	worker.RunOnce(ctx)

	got, err := repo.FindBatchJobByID(ctx, "org-a", job.BatchID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, got.Status, "validating job must be promoted and processed")
	assert.Equal(t, int64(2), got.SucceededRequests)
}

func TestBatchWorkerFileFailure(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	store := NewMemFileStore()
	ctx := context.Background()

	job := seedJob(t, db, "org-a")
	require.NoError(t, repo.SetStatus(ctx, job.BatchID, StatusInProgress, nil, nil))

	worker := NewBatchWorker(repo, store, &fakeInferenceClient{}, &fakeMeterer{}, 2, time.Second)
	worker.RunOnce(ctx)

	got, err := repo.FindBatchJobByID(ctx, "org-a", job.BatchID)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, got.Status)
}

func TestBatchRetentionRunnerDeletesExpired(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	store := NewMemFileStore()
	ctx := context.Background()

	job := seedJob(t, db, "org-a")
	now := time.Now().UTC()
	past := now.Add(-48 * time.Hour)
	require.NoError(t, repo.SetStatus(ctx, job.BatchID, StatusCompleted, &now, &past))
	_, err := store.PutResult(ctx, "org-a", job.BatchID, []byte("result"))
	require.NoError(t, err)
	_, err = store.PutError(ctx, "org-a", job.BatchID, []byte("error"))
	require.NoError(t, err)

	runner := NewBatchRetentionRunner(repo, store, 24*time.Hour, time.Hour)
	runner.clock = func() time.Time { return now }
	runner.RetainOnce(ctx)

	got, err := repo.FindBatchJobByID(ctx, "org-a", job.BatchID)
	require.NoError(t, err)
	assert.True(t, got.FilesExpired)

	_, err = store.GetFile(ctx, resultKey("org-a", job.BatchID))
	require.Error(t, err)
}
