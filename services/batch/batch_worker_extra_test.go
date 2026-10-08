package batch

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingInferenceClient always fails.
type failingInferenceClient struct{}

func (f *failingInferenceClient) Complete(_ context.Context, _ string, _ string, _ json.RawMessage) (json.RawMessage, int64, int64, error) {
	return nil, 0, 0, errors.New("inference down")
}

// TestBatchWorkerPerRequestFailure covers the per-request error path:
// failed requests land in the error file and the job still completes.
func TestBatchWorkerPerRequestFailure(t *testing.T) {
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

	worker := NewBatchWorker(repo, store, &failingInferenceClient{}, &fakeMeterer{}, 2, time.Second)
	worker.RunOnce(ctx)

	got, err := repo.FindBatchJobByID(ctx, "org-a", job.BatchID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, got.Status)
	assert.Equal(t, int64(2), got.FailedRequests)
	assert.Equal(t, int64(0), got.SucceededRequests)

	// Error file written.
	errContent, err := store.GetFile(ctx, errorKey("org-a", job.BatchID))
	require.NoError(t, err)
	assert.Contains(t, string(errContent), "inference down")
}

// TestHTTPInferenceClientSuccess covers the HTTP client success path.
func TestHTTPInferenceClientSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	defer srv.Close()

	client := NewHTTPInferenceClient(srv.URL, "secret")
	resp, in, out, err := client.Complete(context.Background(), "gpt-4o", "/v1/chat/completions", []byte(`{}`))
	require.NoError(t, err)
	assert.Contains(t, string(resp), "id")
	assert.Equal(t, int64(10), in)
	assert.Equal(t, int64(5), out)
}

// TestHTTPInferenceClientError covers the HTTP client non-2xx path.
func TestHTTPInferenceClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	client := NewHTTPInferenceClient(srv.URL, "secret")
	_, _, _, err := client.Complete(context.Background(), "gpt-4o", "/v1/chat/completions", []byte(`{}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Internal Server Error")
}

// TestInferenceError covers the inferenceError type.
func TestInferenceError(t *testing.T) {
	e := &inferenceError{status: http.StatusBadGateway, body: "bad"}
	assert.Contains(t, e.Error(), "Bad Gateway")
}

// TestNewBatchWorkerDefaults covers the default fallback for zero
// concurrency/interval.
func TestNewBatchWorkerDefaults(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	store := NewMemFileStore()
	worker := NewBatchWorker(repo, store, &fakeInferenceClient{}, &fakeMeterer{}, 0, 0)
	assert.NotNil(t, worker)
	assert.Equal(t, 4, worker.concurrency)
	assert.Equal(t, 5*time.Second, worker.interval)
}

// TestNewBatchRetentionRunnerDefaults covers the default fallback for
// zero TTL/interval.
func TestNewBatchRetentionRunnerDefaults(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	store := NewMemFileStore()
	runner := NewBatchRetentionRunner(repo, store, 0, 0)
	assert.NotNil(t, runner)
	assert.Equal(t, 720*time.Hour, runner.fileTTL)
	assert.Equal(t, time.Hour, runner.interval)
}
