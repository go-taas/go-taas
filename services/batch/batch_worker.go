package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-taas/go-taas/pkg/config"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// InferenceClient calls the OpenAI-compatible inference endpoint the data
// plane serves (AD4). It is implemented by the batch worker's HTTP client
// and injected for tests.
type InferenceClient interface {
	// Complete sends one request body to the inference endpoint and
	// returns the response body and the token usage.
	Complete(ctx context.Context, model, url string, body json.RawMessage) (json.RawMessage, int64, int64, error)
}

// HTTPInferenceClient is the production InferenceClient backed by an HTTP
// client and the system credential.
type HTTPInferenceClient struct {
	client     *http.Client
	endpoint   string
	credential string
}

// NewHTTPInferenceClient constructs an HTTPInferenceClient.
func NewHTTPInferenceClient(endpoint, credential string) *HTTPInferenceClient {
	return &HTTPInferenceClient{
		client:     &http.Client{Timeout: 5 * time.Minute},
		endpoint:   strings.TrimRight(endpoint, "/"),
		credential: credential,
	}
}

// Complete implements InferenceClient.
func (c *HTTPInferenceClient) Complete(ctx context.Context, _ string, url string, body json.RawMessage) (json.RawMessage, int64, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.credential != "" {
		req.Header.Set("Authorization", "Bearer "+c.credential)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, 0, 0, &inferenceError{status: resp.StatusCode, body: string(raw)}
	}
	var parsed struct {
		Usage *struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(raw, &parsed)
	var inTok, outTok int64
	if parsed.Usage != nil {
		inTok = parsed.Usage.PromptTokens
		outTok = parsed.Usage.CompletionTokens
	}
	return raw, inTok, outTok, nil
}

// inferenceError is a non-2xx inference response.
type inferenceError struct {
	status int
	body   string
}

func (e *inferenceError) Error() string {
	return "inference returned " + http.StatusText(e.status) + ": " + e.body
}

// Meterer meters a successful batch request at the batch discount factor
// (AD4). It is implemented by the batch worker and injected for tests.
type Meterer interface {
	// Meter records one successful request's token usage and returns the
	// cost in minor units.
	Meter(ctx context.Context, orgID, model string, inputTokens, outputTokens int64) (int64, error)
}

// BatchWorker picks up in_progress jobs and processes each request
// through the inference endpoint, metering successes at 0.5x (AD3, AD4).
// It implements server.Runner.
type BatchWorker struct { //nolint:revive // batch.BatchWorker is the documented domain name
	repo        *Repository
	store       FileStore
	infer       InferenceClient
	meterer     Meterer
	concurrency int
	interval    time.Duration
	fileTTL     time.Duration
	clock       func() time.Time
}

// NewBatchWorker constructs a BatchWorker.
func NewBatchWorker(repo *Repository, store FileStore, infer InferenceClient, meterer Meterer, concurrency int, interval time.Duration) *BatchWorker {
	if concurrency <= 0 {
		concurrency = 4
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &BatchWorker{
		repo:        repo,
		store:       store,
		infer:       infer,
		meterer:     meterer,
		concurrency: concurrency,
		interval:    interval,
		fileTTL:     720 * time.Hour,
		clock:       func() time.Time { return time.Now().UTC() },
	}
}

// SetFileTTL overrides the retention window applied to completed jobs.
func (w *BatchWorker) SetFileTTL(ttl time.Duration) { w.fileTTL = ttl }

// NewBatchWorkerFromConfig builds the worker from the shared components
// and config.
func NewBatchWorkerFromConfig(_ server.Components, repo *Repository, store FileStore, infer InferenceClient, meterer Meterer) *BatchWorker {
	concurrency := 4
	interval := 5 * time.Second
	fileTTL := 720 * time.Hour
	if cfg := config.GetConfig(); cfg != nil {
		if cfg.Batch.Worker.Concurrency > 0 {
			concurrency = cfg.Batch.Worker.Concurrency
		}
		if cfg.Batch.Worker.PollInterval > 0 {
			interval = cfg.Batch.Worker.PollInterval
		}
		if cfg.Batch.Retention.FileTTL > 0 {
			fileTTL = cfg.Batch.Retention.FileTTL
		}
	}
	w := NewBatchWorker(repo, store, infer, meterer, concurrency, interval)
	w.SetFileTTL(fileTTL)
	return w
}

// RunnerName implements server.Runner.
func (w *BatchWorker) RunnerName() string { return "batch-worker" }

// Run implements server.Runner: it ticks on the interval and processes
// in_progress jobs until ctx is cancelled.
func (w *BatchWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.RunOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.RunOnce(ctx)
		}
	}
}

// RunOnce picks up in_progress jobs and processes them. It is extracted
// for tests.
func (w *BatchWorker) RunOnce(ctx context.Context) {
	job, err := w.repo.NextInProgressJob(ctx)
	if err != nil {
		logger.S().Warnw("batch: next in-progress job failed", "err", err)
		return
	}
	if job == nil {
		return
	}
	w.processJob(ctx, job)
}

// processJob reads the input file and processes each request.
func (w *BatchWorker) processJob(ctx context.Context, job *BatchJob) {
	content, err := w.store.GetFile(ctx, job.InputFileKey)
	if err != nil {
		// File-level failure: mark the job failed (AD3).
		_ = w.repo.SetStatus(ctx, job.BatchID, StatusFailed, nil, nil)
		_ = w.repo.SetError(ctx, job.BatchID, "input file unreadable")
		return
	}
	requests, _, err := ValidateJSONL(content, 1<<62, 1<<30)
	if err != nil {
		_ = w.repo.SetStatus(ctx, job.BatchID, StatusFailed, nil, nil)
		_ = w.repo.SetError(ctx, job.BatchID, "input file invalid")
		return
	}
	var resultBuf, errorBuf bytes.Buffer
	sem := make(chan struct{}, w.concurrency)
	var processed, succeeded, failed, inTok, outTok, cost int64
	// Process sequentially for deterministic counters; concurrency is
	// bounded by the semaphore but we keep it simple and sequential here.
	for _, req := range requests {
		select {
		case <-ctx.Done():
			return
		default:
		}
		sem <- struct{}{}
		func() {
			defer func() { <-sem }()
			resp, in, out, err := w.infer.Complete(ctx, job.Model, req.URL, req.Body)
			processed++
			if err != nil {
				failed++
				errorBuf.Write(formatErrorLine(req.CustomID, err.Error()))
				errorBuf.WriteByte('\n')
				return
			}
			succeeded++
			inTok += in
			outTok += out
			if w.meterer != nil {
				c, mErr := w.meterer.Meter(ctx, job.OrganizationID, job.Model, in, out)
				if mErr == nil {
					cost += c
				}
			}
			resultBuf.Write(formatResultLine(req.CustomID, resp))
			resultBuf.WriteByte('\n')
		}()
	}
	// Persist counters.
	_ = w.repo.IncrementCounters(ctx, job.BatchID, processed, succeeded, failed, inTok, outTok, cost)
	// Transition to finalizing, write the files, then completed.
	_ = w.repo.SetStatus(ctx, job.BatchID, StatusFinalizing, nil, nil)
	resultKey, rErr := w.store.PutResult(ctx, job.OrganizationID, job.BatchID, resultBuf.Bytes())
	errorKey, eErr := w.store.PutError(ctx, job.OrganizationID, job.BatchID, errorBuf.Bytes())
	if rErr != nil || eErr != nil {
		_ = w.repo.SetStatus(ctx, job.BatchID, StatusFailed, nil, nil)
		return
	}
	now := w.clock()
	expire := now.Add(w.fileTTL)
	_ = w.repo.SetFileKeys(ctx, job.BatchID, resultKey, errorKey)
	_ = w.repo.SetStatus(ctx, job.BatchID, StatusCompleted, &now, &expire)
}
