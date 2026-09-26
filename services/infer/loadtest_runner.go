package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/server"
)

// latencySampleCap bounds the per-run latency reservoir used for
// percentile estimation (AD6). A run can emit millions of requests; the
// reservoir keeps memory flat while the percentiles stay representative.
const latencySampleCap = 100000

// runChannelBuffer bounds the number of queued run requests.
const runChannelBuffer = 64

// maxConsecutiveUnavailableFailures is how many transport-level failures
// in a row mark a run failed before the duration elapses (AD3 "the
// service becomes unavailable mid-run").
const maxConsecutiveUnavailableFailures = 5

// SystemCredentialProvider resolves the synthetic platform credential
// the runner presents to the inference endpoint (feature #20, AD11). It
// is implemented by the auth module and injected at wiring time, so the
// infer module never reads the api_keys table.
type SystemCredentialProvider interface {
	// GetSystemCredential returns the plaintext system credential.
	GetSystemCredential(ctx context.Context) (string, error)
}

// loadTestRunRequest is a queued run handed from CreateLoadTest to the
// runner.
type loadTestRunRequest struct {
	run      *LoadTest
	endpoint string
}

// runningLoadTest is the in-memory state of one in-flight run (AD12).
type runningLoadTest struct {
	cancel  context.CancelFunc
	agg     *loadTestAggregator
	stopped atomic.Bool
	done    chan struct{}
}

// LoadTestRunner executes load tests asynchronously (AD10, AD12). It is
// a server.Runner: the framework starts Run with the server and cancels
// it on shutdown.
type LoadTestRunner struct {
	repo     *LoadTestRepository
	creds    SystemCredentialProvider
	client   *http.Client
	requests chan loadTestRunRequest
	runs     sync.Map // string -> *runningLoadTest
	progress time.Duration
	clock    func() time.Time
}

// NewLoadTestRunner constructs a runner bound to a repository and
// credential provider. progressInterval <= 0 falls back to 5s.
func NewLoadTestRunner(repo *LoadTestRepository, creds SystemCredentialProvider, progressInterval time.Duration) *LoadTestRunner {
	if progressInterval <= 0 {
		progressInterval = 5 * time.Second
	}
	return &LoadTestRunner{
		repo:     repo,
		creds:    creds,
		client:   &http.Client{Timeout: 5 * time.Minute},
		requests: make(chan loadTestRunRequest, runChannelBuffer),
		progress: progressInterval,
		clock:    func() time.Time { return time.Now().UTC() },
	}
}

// RunnerName implements server.Runner.
func (r *LoadTestRunner) RunnerName() string { return "infer-load-test-runner" }

// Submit queues a run for execution. It fails closed (10311) when the
// queue is full or the runner has not been started, so CreateLoadTest
// never leaves a pending row with no executor.
func (r *LoadTestRunner) Submit(run *LoadTest, endpoint string) error {
	select {
	case r.requests <- loadTestRunRequest{run: run, endpoint: endpoint}:
		return nil
	default:
		return apierrors.New(apierrors.CodeLoadTestTargetInvalid)
	}
}

// Run implements server.Runner: it recovers interrupted runs, then
// serves the run-request queue until ctx is cancelled.
func (r *LoadTestRunner) Run(ctx context.Context) error {
	r.recoverInterrupted(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case req := <-r.requests:
			go r.execute(ctx, req)
		}
	}
}

// recoverInterrupted marks any pending/running runs failed: a run cannot
// survive a server restart because its worker pool is in-memory
// (Section 4.2).
func (r *LoadTestRunner) recoverInterrupted(ctx context.Context) {
	if r.repo == nil {
		return
	}
	rows, err := r.repo.ListActive(ctx)
	if err != nil {
		logger.S().Warnw("infer: load-test recovery failed to list active runs", "err", err)
		return
	}
	for _, row := range rows {
		reason := "server restarted mid-run"
		if err := r.repo.Complete(ctx, row.ID, LoadTestResult{}, LoadTestStateFailed, &reason); err != nil {
			logger.S().Warnw("infer: load-test recovery failed to mark run failed",
				"load_test_id", row.ID, "err", err)
		}
	}
}

// execute runs one load test to completion (or failure/stop). The
// runner-lifetime ctx is used for durable writes so a cancelled run
// context never blocks the terminal state from being persisted.
func (r *LoadTestRunner) execute(ctx context.Context, req loadTestRunRequest) {
	run := req.run
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(run.DurationSeconds)*time.Second)
	agg := newLoadTestAggregator()
	state := &runningLoadTest{cancel: cancel, agg: agg, done: make(chan struct{})}
	r.runs.Store(run.ID, state)
	defer func() {
		cancel()
		r.runs.Delete(run.ID)
		close(state.done)
	}()

	startedAt := r.clock()
	if err := r.repo.MarkRunning(ctx, run.ID, startedAt); err != nil {
		logger.S().Warnw("infer: load-test failed to mark running", "load_test_id", run.ID, "err", err)
		return
	}

	// Progress ticker persists a snapshot while the run is live.
	stopProgress := make(chan struct{})
	go r.reportProgress(ctx, runCtx, run.ID, agg, startedAt, stopProgress)

	credential, err := r.systemCredential(runCtx)
	if err != nil {
		close(stopProgress)
		reason := "system credential unavailable"
		r.finish(ctx, run.ID, agg, startedAt, LoadTestStateFailed, &reason)
		return
	}

	failureReason := r.drive(runCtx, cancel, run, req.endpoint, credential, agg)
	close(stopProgress)

	finalState := LoadTestStateCompleted
	var reason *string
	switch {
	case state.stopped.Load():
		finalState = LoadTestStateStopped
	case failureReason != "":
		finalState = LoadTestStateFailed
		reason = &failureReason
	}
	r.finish(ctx, run.ID, agg, startedAt, finalState, reason)
}

// systemCredential resolves the platform credential, honoring the
// configuration kill switch (AD11).
func (r *LoadTestRunner) systemCredential(ctx context.Context) (string, error) {
	if r.creds == nil {
		return "", apierrors.New(apierrors.CodeLoadTestTargetInvalid)
	}
	return r.creds.GetSystemCredential(ctx)
}

// drive runs the worker pool for the configured duration and returns a
// non-empty failure reason when the run must be marked failed.
func (r *LoadTestRunner) drive(ctx context.Context, cancel context.CancelFunc, run *LoadTest, endpoint, credential string, agg *loadTestAggregator) string {
	var limiter *rate.Limiter
	if run.RequestRate > 0 {
		limiter = rate.NewLimiter(rate.Limit(run.RequestRate), run.RequestRate)
	}

	var wg sync.WaitGroup
	unavailable := make(chan struct{})
	var unavailableOnce sync.Once
	// markUnavailable cancels the run context so every worker stops as
	// soon as the target is judged unavailable (fail fast, AD3).
	markUnavailable := func() {
		unavailableOnce.Do(func() {
			close(unavailable)
			cancel()
		})
	}

	for i := 0; i < run.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(ctx, run, endpoint, credential, limiter, agg, markUnavailable)
		}()
	}
	wg.Wait()

	select {
	case <-unavailable:
		return "target service unavailable"
	default:
		return ""
	}
}

// worker is one concurrency unit: it fires requests until the run
// context is cancelled (duration elapsed or stop).
func (r *LoadTestRunner) worker(
	ctx context.Context,
	run *LoadTest,
	endpoint, credential string,
	limiter *rate.Limiter,
	agg *loadTestAggregator,
	markUnavailable func(),
) {
	consecutive := 0
	for {
		if ctx.Err() != nil {
			return
		}
		if limiter != nil {
			if err := limiter.Wait(ctx); err != nil {
				return
			}
		}
		start := r.clock()
		status, inTok, outTok, transportErr := r.fire(ctx, run, endpoint, credential)
		latencyMs := float64(r.clock().Sub(start).Microseconds()) / 1000.0

		switch {
		case transportErr != nil:
			agg.recordFailure()
			consecutive++
			if consecutive >= maxConsecutiveUnavailableFailures {
				markUnavailable()
				return
			}
			continue
		case status >= 200 && status < 300:
			agg.recordSuccess(latencyMs, inTok, outTok)
			consecutive = 0
		default:
			agg.recordFailure()
			consecutive = 0
		}
	}
}

// fire sends one chat-completion request and returns the status code,
// token usage and any transport error.
func (r *LoadTestRunner) fire(ctx context.Context, run *LoadTest, endpoint, credential string) (int, int64, int64, error) {
	body, err := json.Marshal(chatCompletionRequest{
		Model:     run.ModelName,
		Messages:  []chatMessage{{Role: "user", Content: run.PromptTemplate}},
		MaxTokens: run.MaxTokens,
	})
	if err != nil {
		return 0, 0, 0, err
	}
	url := chatCompletionsURL(endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential)

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, 0, 0, nil
	}
	var parsed chatCompletionResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		// A 2xx with an unparseable body is still a success; token
		// counts are simply unknown.
		return resp.StatusCode, 0, 0, nil
	}
	if parsed.Usage != nil {
		return resp.StatusCode, parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, nil
	}
	return resp.StatusCode, 0, 0, nil
}

// reportProgress persists a live-progress snapshot on the configured
// interval until the run ends.
func (r *LoadTestRunner) reportProgress(ctx, runCtx context.Context, id string, agg *loadTestAggregator, startedAt time.Time, stop <-chan struct{}) {
	ticker := time.NewTicker(r.progress)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			requests, successes, failures := agg.snapshot()
			progress := LoadTestProgress{
				RequestsSent:   requests,
				SuccessCount:   successes,
				FailureCount:   failures,
				ElapsedSeconds: int64(r.clock().Sub(startedAt).Seconds()),
			}
			if err := r.repo.UpdateProgress(ctx, id, progress); err != nil {
				logger.S().Warnw("infer: load-test progress update failed", "load_test_id", id, "err", err)
			}
		}
	}
}

// finish aggregates the result set and writes the terminal state.
func (r *LoadTestRunner) finish(ctx context.Context, id string, agg *loadTestAggregator, startedAt time.Time, state string, reason *string) {
	elapsed := r.clock().Sub(startedAt).Seconds()
	result := agg.result(elapsed)
	if err := r.repo.Complete(ctx, id, result, state, reason); err != nil {
		logger.S().Warnw("infer: load-test terminal write failed", "load_test_id", id, "err", err)
	}
}

// Stop cancels a running run and waits briefly for its partial results
// to be persisted as stopped. A run that is not in flight (already
// terminal) reports false so the caller maps 10310.
func (r *LoadTestRunner) Stop(id string) bool {
	v, ok := r.runs.Load(id)
	if !ok {
		return false
	}
	state := v.(*runningLoadTest)
	state.stopped.Store(true)
	state.cancel()
	select {
	case <-state.done:
	case <-time.After(2 * time.Second):
	}
	return true
}

// Progress returns the live progress of a running run.
func (r *LoadTestRunner) Progress(id string) (LoadTestProgress, bool) {
	v, ok := r.runs.Load(id)
	if !ok {
		return LoadTestProgress{}, false
	}
	agg := v.(*runningLoadTest).agg
	requests, successes, failures := agg.snapshot()
	return LoadTestProgress{
		RequestsSent: requests,
		SuccessCount: successes,
		FailureCount: failures,
	}, true
}

// chatCompletionsURL composes the OpenAI-compatible path from a service
// endpoint, tolerating a trailing slash or an already-suffixed /v1.
func chatCompletionsURL(endpoint string) string {
	base := strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// chatCompletionRequest is the minimal OpenAI-compatible request body.
type chatCompletionRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

// chatMessage is one chat message.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatCompletionResponse is the subset of the response the runner reads.
type chatCompletionResponse struct {
	Usage *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

// loadTestAggregator accumulates per-run counters and a latency
// reservoir (AD6, AD12). It is safe for concurrent use.
type loadTestAggregator struct {
	mu           sync.Mutex
	requests     int64
	successes    int64
	failures     int64
	inputTokens  int64
	outputTokens int64
	latencies    []float64
	latencySeen  int64
	rng          *rand.Rand
}

// newLoadTestAggregator constructs an empty aggregator.
func newLoadTestAggregator() *loadTestAggregator {
	return &loadTestAggregator{
		latencies: make([]float64, 0, 1024),
		rng:       rand.New(rand.NewSource(time.Now().UnixNano())), // #nosec G404 -- sampling only, not security
	}
}

// recordSuccess counts a successful request and adds its latency to the
// reservoir.
func (a *loadTestAggregator) recordSuccess(latencyMs float64, inTok, outTok int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests++
	a.successes++
	a.inputTokens += inTok
	a.outputTokens += outTok
	a.addLatencyLocked(latencyMs)
}

// recordFailure counts a failed request.
func (a *loadTestAggregator) recordFailure() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests++
	a.failures++
}

// addLatencyLocked applies reservoir sampling (Algorithm R) so the
// percentiles stay representative without unbounded memory.
func (a *loadTestAggregator) addLatencyLocked(latencyMs float64) {
	a.latencySeen++
	if len(a.latencies) < latencySampleCap {
		a.latencies = append(a.latencies, latencyMs)
		return
	}
	j := a.rng.Int63n(a.latencySeen)
	if j < int64(latencySampleCap) {
		a.latencies[j] = latencyMs
	}
}

// snapshot returns the current counters.
func (a *loadTestAggregator) snapshot() (requests, successes, failures int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests, a.successes, a.failures
}

// result computes the final result set from the counters and reservoir.
func (a *loadTestAggregator) result(elapsedSeconds float64) LoadTestResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	res := LoadTestResult{
		TotalRequests: a.requests,
		SuccessCount:  a.successes,
		FailureCount:  a.failures,
		InputTokens:   a.inputTokens,
		OutputTokens:  a.outputTokens,
	}
	if a.requests > 0 {
		res.ErrorRate = float64(a.failures) / float64(a.requests)
	}
	if elapsedSeconds > 0 {
		res.ThroughputRPS = float64(a.requests) / elapsedSeconds
		res.OutputTokensPerSec = float64(a.outputTokens) / elapsedSeconds
	}
	latencies := make([]float64, len(a.latencies))
	copy(latencies, a.latencies)
	sort.Float64s(latencies)
	res.LatencyP50Ms = percentile(latencies, 50)
	res.LatencyP90Ms = percentile(latencies, 90)
	res.LatencyP95Ms = percentile(latencies, 95)
	res.LatencyP99Ms = percentile(latencies, 99)
	return res
}

// percentile returns the nearest-rank percentile of a sorted slice. An
// empty slice yields 0.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100.0 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// Ensure the runner satisfies server.Runner at compile time.
var _ server.Runner = (*LoadTestRunner)(nil)
