package infer

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	inferv1 "github.com/go-taas/go-taas/proto/taas/infer/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// maskedModelLoadTestLimit bounds the end-user projection (AD2).
const maskedModelLoadTestLimit = 10

// loadTestRepository lazily resolves the load-test repository.
func (s *Service) loadTestRepository() (*LoadTestRepository, error) {
	if s.loadTestRepo != nil {
		return s.loadTestRepo, nil
	}
	db, err := s.gormDB()
	if err != nil {
		// Fall back to the wired repository's DB (tests, FVT).
		if s.repo != nil {
			return NewLoadTestRepository(s.repo.db.DB(context.Background())), nil
		}
		return nil, err
	}
	s.loadTestRepo = NewLoadTestRepository(db)
	return s.loadTestRepo, nil
}

// loadTestConfig is the resolved, validated load-test configuration.
type loadTestConfig struct {
	concurrency     int
	durationSeconds int
	requestRate     int
	promptTemplate  string
	maxTokens       int
}

// normalizeLoadTestConfig applies the documented defaults and validates
// the configuration (AD5). Zero values on defaulted fields resolve to
// their defaults (proto3 cannot distinguish unset from zero); an
// out-of-range explicit value returns 10309.
func normalizeLoadTestConfig(req *inferv1.CreateLoadTestRequest) (loadTestConfig, error) {
	cfg := loadTestConfig{
		concurrency:     int(req.GetConcurrency()),
		durationSeconds: int(req.GetDurationSeconds()),
		requestRate:     int(req.GetRequestRate()),
		promptTemplate:  req.GetPromptTemplate(),
		maxTokens:       int(req.GetMaxTokens()),
	}
	if cfg.concurrency == 0 {
		cfg.concurrency = 1
	}
	if cfg.durationSeconds == 0 {
		cfg.durationSeconds = 60
	}
	if cfg.maxTokens == 0 {
		cfg.maxTokens = 256
	}
	if cfg.concurrency < loadTestMinConcurrency || cfg.concurrency > loadTestMaxConcurrency {
		return loadTestConfig{}, apierrors.New(apierrors.CodeLoadTestConfigInvalid)
	}
	if cfg.durationSeconds < loadTestMinDuration || cfg.durationSeconds > loadTestMaxDuration {
		return loadTestConfig{}, apierrors.New(apierrors.CodeLoadTestConfigInvalid)
	}
	if cfg.requestRate < loadTestMinRate || cfg.requestRate > loadTestMaxRate {
		return loadTestConfig{}, apierrors.New(apierrors.CodeLoadTestConfigInvalid)
	}
	if strings.TrimSpace(cfg.promptTemplate) == "" || len(cfg.promptTemplate) > loadTestMaxPromptLen {
		return loadTestConfig{}, apierrors.New(apierrors.CodeLoadTestConfigInvalid)
	}
	if cfg.maxTokens < loadTestMinMaxTokens || cfg.maxTokens > loadTestMaxMaxTokens {
		return loadTestConfig{}, apierrors.New(apierrors.CodeLoadTestConfigInvalid)
	}
	return cfg, nil
}

// CreateLoadTest configures and starts a load test against a running
// inference service (AD1, AD3, AD4).
func (s *Service) CreateLoadTest(ctx context.Context, req *inferv1.CreateLoadTestRequest) (*inferv1.CreateLoadTestResponse, error) {
	if s.loadTestRunner == nil {
		// No runner means there is nothing to drive the run with; the
		// kill switch is applied at wiring time (AD11).
		return nil, apierrors.New(apierrors.CodeLoadTestTargetInvalid)
	}
	cfg, err := normalizeLoadTestConfig(req)
	if err != nil {
		return nil, err
	}

	serviceRepo, err := s.repository()
	if err != nil {
		return nil, err
	}
	svc, err := serviceRepo.FindByID(ctx, strings.TrimSpace(req.GetServiceId()))
	if err != nil {
		return nil, err
	}
	if svc.State != StateRunning || len(svc.Endpoints) == 0 {
		return nil, apierrors.New(apierrors.CodeLoadTestTargetInvalid)
	}

	run := &LoadTest{
		ID:              uuid.NewString(),
		ServiceID:       svc.ID,
		ServiceName:     svc.Name,
		ModelID:         svc.ModelID,
		ModelName:       s.modelNameFor(ctx, svc.ModelID),
		Concurrency:     cfg.concurrency,
		DurationSeconds: cfg.durationSeconds,
		RequestRate:     cfg.requestRate,
		PromptTemplate:  cfg.promptTemplate,
		MaxTokens:       cfg.maxTokens,
		State:           LoadTestStatePending,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}

	repo, err := s.loadTestRepository()
	if err != nil {
		return nil, err
	}
	if err := repo.Create(ctx, run); err != nil {
		return nil, err
	}
	if err := s.loadTestRunner.Submit(run, svc.Endpoints[0]); err != nil {
		// A run that could not be queued must not linger as a pending row.
		_ = repo.Delete(ctx, run.ID)
		return nil, err
	}
	return &inferv1.CreateLoadTestResponse{
		Response:   okResponse(),
		LoadTestId: run.ID,
		State:      LoadTestStatePending,
	}, nil
}

// modelNameFor resolves the model display name, tolerating a missing
// model (a run is a historical record and must not fail on lookups).
func (s *Service) modelNameFor(ctx context.Context, modelID string) string {
	modelRepo, err := s.modelRepository()
	if err != nil {
		return modelID
	}
	m, err := modelRepo.GetModel(ctx, modelID)
	if err != nil || m == nil {
		return modelID
	}
	return m.Name
}

// ListLoadTests returns the load-test history with filters, search, and
// pagination (AC3).
func (s *Service) ListLoadTests(ctx context.Context, req *inferv1.ListLoadTestsRequest) (*inferv1.ListLoadTestsResponse, error) {
	repo, err := s.loadTestRepository()
	if err != nil {
		return nil, err
	}
	offset, limit := normalizePagination(req.GetPage())
	rows, total, err := repo.List(ctx, LoadTestFilter{
		ServiceID: strings.TrimSpace(req.GetServiceId()),
		ModelID:   strings.TrimSpace(req.GetModelId()),
		State:     strings.TrimSpace(req.GetStatus()),
		Search:    strings.TrimSpace(req.GetSearch()),
	}, offset, limit)
	if err != nil {
		return nil, err
	}
	runs := make([]*inferv1.LoadTestSummary, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, summarizeLoadTest(row))
	}
	return &inferv1.ListLoadTestsResponse{
		Response: okResponse(),
		Runs:     runs,
		PageMeta: &commonv1.PageMeta{Total: total, Offset: int64(offset), Limit: clampToInt32(limit)},
	}, nil
}

// summarizeLoadTest maps a row to the list/detail summary.
func summarizeLoadTest(row *LoadTest) *inferv1.LoadTestSummary {
	summary := &inferv1.LoadTestSummary{
		LoadTestId:         row.ID,
		ServiceId:          row.ServiceID,
		ServiceName:        row.ServiceName,
		ModelId:            row.ModelID,
		ModelName:          row.ModelName,
		Concurrency:        clampToInt32(row.Concurrency),
		DurationSeconds:    clampToInt32(row.DurationSeconds),
		RequestRate:        clampToInt32(row.RequestRate),
		State:              row.State,
		ThroughputRps:      row.ThroughputRPS,
		LatencyP95Ms:       row.LatencyP95Ms,
		OutputTokensPerSec: row.OutputTokensPerSec,
		ErrorRate:          row.ErrorRate,
	}
	if row.StartedAt != nil {
		summary.StartedAt = row.StartedAt.Unix()
	}
	if row.CompletedAt != nil {
		summary.CompletedAt = row.CompletedAt.Unix()
	}
	return summary
}

// GetLoadTest returns one run with live progress and results (AC1).
func (s *Service) GetLoadTest(ctx context.Context, req *inferv1.GetLoadTestRequest) (*inferv1.GetLoadTestResponse, error) {
	repo, err := s.loadTestRepository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindByID(ctx, strings.TrimSpace(req.GetLoadTestId()))
	if err != nil {
		return nil, err
	}

	resp := &inferv1.GetLoadTestResponse{
		Response:       okResponse(),
		Summary:        summarizeLoadTest(row),
		PromptTemplate: row.PromptTemplate,
		MaxTokens:      clampToInt32(row.MaxTokens),
	}
	if row.FailureReason != nil {
		resp.FailureReason = *row.FailureReason
	}
	if !row.IsTerminal() {
		if progress, ok := s.loadTestRunner.Progress(row.ID); ok {
			resp.Progress = progressToProto(progress, row)
		} else {
			resp.Progress = &inferv1.LoadTestProgress{
				RequestsSent:   row.TotalRequests,
				SuccessCount:   row.SuccessCount,
				FailureCount:   row.FailureCount,
				ElapsedSeconds: elapsedSeconds(row),
			}
		}
		return resp, nil
	}
	resp.Result = resultToProto(row)
	return resp, nil
}

// progressToProto builds the live-progress block, filling the elapsed
// time from started_at.
func progressToProto(p LoadTestProgress, row *LoadTest) *inferv1.LoadTestProgress {
	elapsed := p.ElapsedSeconds
	if elapsed == 0 {
		elapsed = elapsedSeconds(row)
	}
	return &inferv1.LoadTestProgress{
		RequestsSent:   p.RequestsSent,
		SuccessCount:   p.SuccessCount,
		FailureCount:   p.FailureCount,
		ElapsedSeconds: elapsed,
	}
}

// elapsedSeconds returns the whole seconds since started_at (0 when the
// run has not started).
func elapsedSeconds(row *LoadTest) int64 {
	if row.StartedAt == nil {
		return 0
	}
	end := time.Now().UTC()
	if row.CompletedAt != nil {
		end = *row.CompletedAt
	}
	delta := end.Sub(*row.StartedAt).Seconds()
	if delta < 0 {
		return 0
	}
	return int64(delta)
}

// resultToProto maps the persisted result columns to the wire result.
func resultToProto(row *LoadTest) *inferv1.LoadTestResult {
	return &inferv1.LoadTestResult{
		TotalRequests:      row.TotalRequests,
		SuccessCount:       row.SuccessCount,
		FailureCount:       row.FailureCount,
		ErrorRate:          row.ErrorRate,
		ThroughputRps:      row.ThroughputRPS,
		OutputTokensPerSec: row.OutputTokensPerSec,
		InputTokens:        row.InputTokens,
		OutputTokens:       row.OutputTokens,
		LatencyP50Ms:       row.LatencyP50Ms,
		LatencyP90Ms:       row.LatencyP90Ms,
		LatencyP95Ms:       row.LatencyP95Ms,
		LatencyP99Ms:       row.LatencyP99Ms,
	}
}

// StopLoadTest stops a pending/running run, retaining partial results
// (AC4). A terminal run returns 10310.
func (s *Service) StopLoadTest(ctx context.Context, req *inferv1.StopLoadTestRequest) (*inferv1.StopLoadTestResponse, error) {
	repo, err := s.loadTestRepository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindByID(ctx, strings.TrimSpace(req.GetLoadTestId()))
	if err != nil {
		return nil, err
	}
	if row.State != LoadTestStatePending && row.State != LoadTestStateRunning {
		return nil, apierrors.New(apierrors.CodeLoadTestStateInvalid)
	}
	if s.loadTestRunner == nil || !s.loadTestRunner.Stop(row.ID) {
		// The run is not in flight (e.g. it finished between the read and
		// the stop): the state is no longer stoppable.
		return nil, apierrors.New(apierrors.CodeLoadTestStateInvalid)
	}
	return &inferv1.StopLoadTestResponse{Response: okResponse(), State: LoadTestStateStopped}, nil
}

// DeleteLoadTest deletes a terminal run (AC5). An active run returns
// 10310.
func (s *Service) DeleteLoadTest(ctx context.Context, req *inferv1.DeleteLoadTestRequest) (*inferv1.DeleteLoadTestResponse, error) {
	repo, err := s.loadTestRepository()
	if err != nil {
		return nil, err
	}
	row, err := repo.FindByID(ctx, strings.TrimSpace(req.GetLoadTestId()))
	if err != nil {
		return nil, err
	}
	if !row.IsTerminal() {
		return nil, apierrors.New(apierrors.CodeLoadTestStateInvalid)
	}
	if err := repo.Delete(ctx, row.ID); err != nil {
		return nil, err
	}
	return &inferv1.DeleteLoadTestResponse{Response: okResponse()}, nil
}

// GetModelLoadTests returns the masked user-realm performance projection
// (AC6, AD2). It resolves the caller's organization, applies the
// model-authorization default-allow rule, and returns only completed
// runs with no service ids.
func (s *Service) GetModelLoadTests(ctx context.Context, req *inferv1.GetModelLoadTestsRequest) (*inferv1.GetModelLoadTestsResponse, error) {
	orgID, err := s.resolveOrg(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkOrg(ctx, orgID, false); err != nil {
		return nil, err
	}
	modelID := strings.TrimSpace(req.GetModelId())
	if modelID == "" {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}
	// A malformed (non-UUID) id can never match a stored model; guard it
	// so the uuid column comparison cannot surface a cast error (10101
	// instead of 500).
	if _, err := uuid.Parse(modelID); err != nil {
		return nil, apierrors.New(apierrors.CodeModelNotFound)
	}

	modelRepo, err := s.modelRepository()
	if err != nil {
		return nil, err
	}
	if _, err := modelRepo.GetModel(ctx, modelID); err != nil {
		return nil, err
	}
	authorized, err := modelRepo.IsModelAuthorized(ctx, modelID, orgID)
	if err != nil {
		return nil, err
	}
	if !authorized {
		return nil, apierrors.New(apierrors.CodeModelUnauthorized)
	}

	repo, err := s.loadTestRepository()
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListCompletedForModel(ctx, modelID, maskedModelLoadTestLimit)
	if err != nil {
		return nil, err
	}
	results := make([]*inferv1.ModelLoadTestResult, 0, len(rows))
	for _, row := range rows {
		result := &inferv1.ModelLoadTestResult{
			ThroughputRps:      row.ThroughputRPS,
			LatencyP95Ms:       row.LatencyP95Ms,
			OutputTokensPerSec: row.OutputTokensPerSec,
			ErrorRate:          row.ErrorRate,
			Concurrency:        clampToInt32(row.Concurrency),
			DurationSeconds:    clampToInt32(row.DurationSeconds),
		}
		if row.CompletedAt != nil {
			result.CompletedAt = row.CompletedAt.Unix()
		}
		results = append(results, result)
	}
	return &inferv1.GetModelLoadTestsResponse{Response: okResponse(), Results: results}, nil
}
