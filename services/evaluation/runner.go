package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/google/uuid"
)

// Runner executes pending evaluation runs (feature #44, AD5). It
// polls for pending runs, claims each via a database lease (so
// multiple replicas never execute the same run), executes every case
// through the real metered completion path, evaluates the
// deterministic checks, persists each result before advancing, and
// publishes one standard metering event per call so usage, cost,
// request logs and organization/key attribution flow through the
// normal metering path. The database is the source of truth; NATS is
// not used for run scheduling.
type Runner struct {
	svc *Service
	// pollInterval overrides the configured interval (tests).
	pollInterval time.Duration
	// claimOnce makes the loop exit after one sweep when there is
	// nothing to claim (tests).
	claimOnce bool
}

// NewRunner constructs the evaluation worker.
func NewRunner(svc *Service) *Runner { return &Runner{svc: svc} }

// SetPollInterval installs a custom poll interval (tests).
func (r *Runner) SetPollInterval(d time.Duration) { r.pollInterval = d }

// SetClaimOnce makes Run execute at most one claim sweep (tests).
func (r *Runner) SetClaimOnce() { r.claimOnce = true }

// Run implements server.Runner: it loops until ctx is cancelled,
// claiming and executing pending runs.
func (r *Runner) Run(ctx context.Context) error {
	interval := r.pollInterval
	if interval <= 0 {
		interval = 2 * time.Second
		if cfg := config.GetConfig(); cfg != nil && cfg.Evaluation.Worker.PollInterval > 0 {
			interval = cfg.Evaluation.Worker.PollInterval
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		claimed, err := r.sweep(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(interval):
			}
			continue
		}
		if r.claimOnce {
			return nil
		}
		if !claimed {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(interval):
			}
		}
	}
}

// sweep claims at most one pending run and executes it. It returns
// true when a run was claimed.
func (r *Runner) sweep(ctx context.Context) (bool, error) {
	repo, err := r.svc.repository()
	if err != nil {
		return false, err
	}
	leaseTTL := 10 * time.Minute
	owner := fmt.Sprintf("eval-worker-%s", uuid.NewString())
	run, err := repo.claimPendingRun(ctx, owner, time.Now().UTC().Add(leaseTTL))
	if err != nil || run == nil {
		return false, err
	}
	r.execute(ctx, repo, run)
	return true, nil
}

// execute runs every case of a claimed run to a terminal state
// (feature #44, AD5).
func (r *Runner) execute(ctx context.Context, repo *Repository, run *EvaluationRun) {
	cases, err := repo.pendingRunCases(ctx, run.RunID)
	if err != nil {
		return
	}
	var (
		completed, passed, failed, errored, cancelled int
		totalCostMinor                                int64
		latencies                                     []int64
	)
	for i := range cases {
		if ctx.Err() != nil {
			break
		}
		// Check cancellation between cases (AD5).
		if req, _ := repo.cancelRequested(ctx, run.RunID); req {
			for j := i; j < len(cases); j++ {
				now := time.Now().UTC()
				cases[j].Status = CaseCancelled
				cases[j].CompletedAt = &now
				_ = repo.saveCaseResult(ctx, &cases[j])
				cancelled++
			}
			break
		}
		c := &cases[i]
		result := r.executeCase(ctx, repo, run, c)
		switch result {
		case CasePassed:
			passed++
			completed++
		case CaseFailed:
			failed++
			completed++
		case CaseError:
			errored++
			completed++
		case CaseCancelled:
			cancelled++
		}
		if c.LatencyMS > 0 {
			latencies = append(latencies, c.LatencyMS)
		}
		totalCostMinor += c.CostMinor
	}
	// Any case still pending (context cancelled mid-run) stays pending;
	// the run remains running and a future sweep re-claims it after the
	// lease expires (AD5: persist each result before advancing).
	status := RunCompleted
	if errored > 0 {
		status = RunCompletedWithErrors
	}
	if cancelled > 0 && passed+failed+errored == 0 {
		status = RunCancelled
	}
	_ = repo.finalizeRun(ctx, run.RunID, status, completed, passed, failed, errored, cancelled, totalCostMinor, median(latencies), time.Now().UTC())
}

// executeCase executes one case end-to-end: render the prompt with
// the case variables, call the real metered completion path, evaluate
// the deterministic checks, persist the immutable result and publish
// the standard metering event (feature #44, AD5).
func (r *Runner) executeCase(ctx context.Context, repo *Repository, run *EvaluationRun, c *EvaluationRunCase) string {
	rendered := renderPrompt(run.PromptContentSnapshot, c.Variables)
	input := &CompletionInput{
		OrganizationID: run.OrganizationID,
		ModelID:        run.ModelID,
		APIKeyID:       run.APIKeyID,
		Prompt:         rendered,
		MaxTokens:      1024,
	}
	requestID := fmt.Sprintf("eval-%s-%s", run.RunID, c.ResultID)
	now := time.Now().UTC()
	completion, err := r.svc.completions.CompleteModelCall(ctx, input)
	if err != nil {
		// Real failure: persist the case as an error with the business
		// code and publish the error metering event (never a simulated
		// completion).
		c.Status = CaseError
		c.ErrorCode = int32(apierrors.CodeOf(err))
		c.ErrorMessage = truncateMessage(err.Error())
		c.CompletedAt = &now
		r.publishMetering(ctx, requestID, run, c, 0, 0, now, "error", c.ErrorMessage)
		_ = repo.saveCaseResult(ctx, c)
		return CaseError
	}
	// Evaluate the deterministic checks against the real completion.
	checks := parseChecks(c.Checks)
	results, allPassed := EvaluateChecks(completion.Completion, checks)
	resultsJSON, _ := json.Marshal(results)
	c.Status = CaseFailed
	if allPassed {
		c.Status = CasePassed
	}
	c.Completion = completion.Completion
	c.CheckResults = string(resultsJSON)
	c.InputTokens = completion.PromptTokens
	c.OutputTokens = completion.CompletionTokens
	c.LatencyMS = completion.LatencyMS
	c.Currency = run.Currency
	if r.svc.costs != nil {
		if cost, _ := r.svc.costs.EstimateCaseCost(ctx, run.ModelID, defaultCardType, completion.PromptTokens, completion.CompletionTokens, now); cost > 0 {
			c.CostMinor = cost
		}
	}
	c.CompletedAt = &now
	r.publishMetering(ctx, requestID, run, c, completion.PromptTokens, completion.CompletionTokens, now, "success", "")
	if err := repo.saveCaseResult(ctx, c); err != nil {
		return CaseError
	}
	return c.Status
}

// publishMetering publishes the standard metering event for one case
// call (feature #44, AD5): the same payload the data-plane gateway
// emits, preserving organization/key/model attribution.
func (r *Runner) publishMetering(ctx context.Context, requestID string, run *EvaluationRun, c *EvaluationRunCase, promptTokens, completionTokens int64, completedAt time.Time, status, errMsg string) {
	if r.svc.metering == nil {
		return
	}
	event := buildMeteringEvent(requestID, run.OrganizationID, run.APIKeyID, run.ModelID, "", promptTokens, completionTokens, completedAt, c.LatencyMS, status, errMsg)
	_ = r.svc.metering.PublishMeteringEvent(ctx, event)
}

// renderPrompt substitutes ${variable} placeholders with the case's
// variable values (feature #44, FR3.2).
func renderPrompt(content string, variables string) string {
	vars := map[string]string{}
	if variables != "" {
		_ = json.Unmarshal([]byte(variables), &vars)
	}
	out := content
	for k, v := range vars {
		out = strings.ReplaceAll(out, "${"+k+"}", v)
	}
	return out
}

// parseChecks decodes the checks snapshot JSON.
func parseChecks(raw string) []Check {
	var checks []Check
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &checks)
	}
	return checks
}

// truncateMessage clamps an error message for persistence.
func truncateMessage(msg string) string {
	if len(msg) > 500 {
		return msg[:500]
	}
	return msg
}

// median returns the median of the given latencies (0 when empty).
func median(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)/2]
}
