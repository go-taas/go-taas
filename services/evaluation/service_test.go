// Copyright 2025 The go-taas Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package evaluation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	evaluationv1 "github.com/go-taas/go-taas/proto/taas/evaluation/v1"
	promptv1 "github.com/go-taas/go-taas/proto/taas/prompt/v1"
)

// fakePrompts is a scriptable PromptResolver serving one prompt with
// one version.
type fakePrompts struct {
	promptID string
	name     string
	content  string
	vars     []string
}

func (f *fakePrompts) ListPromptVersions(_ context.Context, req *promptv1.ListPromptVersionsRequest) (*promptv1.ListPromptVersionsResponse, error) {
	if req.GetPromptId() != f.promptID {
		return &promptv1.ListPromptVersionsResponse{}, nil
	}
	return &promptv1.ListPromptVersionsResponse{Versions: []*promptv1.PromptVersion{{
		PromptId: f.promptID, Version: 1, Content: f.content, Variables: f.vars,
	}}}, nil
}

func (f *fakePrompts) GetPrompt(_ context.Context, req *promptv1.GetPromptRequest) (*promptv1.GetPromptResponse, error) {
	if req.GetPromptId() != f.promptID {
		return nil, ErrEvaluationNotFound
	}
	return &promptv1.GetPromptResponse{Prompt: &promptv1.Prompt{PromptId: f.promptID, Name: f.name, Version: 1}}, nil
}

// fakeAPIKeys is a scriptable APIKeyValidator.
type fakeAPIKeys struct {
	valid map[string]bool
}

func (f *fakeAPIKeys) ValidateActiveAPIKey(_ context.Context, _, keyID string) error {
	if f.valid[keyID] {
		return nil
	}
	return ErrEvaluationNotFound
}

func newServiceTestCtx(org string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-organization-id", org))
}

// setTestConfig installs a minimal configuration so config-dependent
// paths (MaxInFlightRunsPerOrg) work in unit tests; GetConfig returns
// nil until something loads or installs a configuration. The in-flight
// limit mirrors the shipped default (10) from configs/server.yaml.
func setTestConfig(t *testing.T) {
	t.Helper()
	config.SetConfigForTest(&config.Configuration{Evaluation: config.EvaluationConfig{
		MaxInFlightRunsPerOrg: 10,
	}})
	t.Cleanup(func() { config.SetConfigForTest(nil) })
}

func newTestService(db *gorm.DB) *Service {
	return NewForFVT(db, &fakePrompts{promptID: "prompt-1", name: "p", content: "Answer ${q}", vars: []string{"q"}})
}

func mustCreateSuite(ctx context.Context, t *testing.T, svc *Service, name string) string {
	t.Helper()
	resp, err := svc.CreateEvaluation(ctx, &evaluationv1.CreateEvaluationRequest{Name: name, PromptId: "prompt-1", PromptVersion: 1})
	require.NoError(t, err)
	return resp.GetEvaluation().GetEvaluationId()
}

func mustAddCase(ctx context.Context, t *testing.T, svc *Service, evaluationID, name string) string {
	t.Helper()
	resp, err := svc.CreateEvaluationCase(ctx, &evaluationv1.CreateEvaluationCaseRequest{
		EvaluationId: evaluationID, Name: name, Variables: map[string]string{"q": "hi"},
		Checks: []*evaluationv1.EvaluationCheck{{Type: "contains", Expected: "42"}},
	})
	require.NoError(t, err)
	return resp.GetCase().GetCaseId()
}

func TestServiceCreateEvaluationValidatesInput(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := newTestService(db)
	ctx := newServiceTestCtx("org-a")

	// Missing name.
	_, err := svc.CreateEvaluation(ctx, &evaluationv1.CreateEvaluationRequest{Name: "", PromptId: "prompt-1", PromptVersion: 1})
	require.Error(t, err)
	// Unknown prompt version.
	_, err = svc.CreateEvaluation(ctx, &evaluationv1.CreateEvaluationRequest{Name: "s", PromptId: "prompt-1", PromptVersion: 9})
	require.Error(t, err)
	// Valid create returns the suite (prompt name resolves on read).
	resp, err := svc.CreateEvaluation(ctx, &evaluationv1.CreateEvaluationRequest{Name: "s", PromptId: "prompt-1", PromptVersion: 1})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetEvaluation().GetEvaluationId())
	require.Equal(t, "s", resp.GetEvaluation().GetName())
	got, err := svc.GetEvaluation(ctx, &evaluationv1.GetEvaluationRequest{EvaluationId: resp.GetEvaluation().GetEvaluationId()})
	require.NoError(t, err)
	require.Equal(t, "p", got.GetEvaluation().GetPromptName())
}

func TestServiceSuiteLifecycle(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := newTestService(db)
	ctx := newServiceTestCtx("org-a")

	id := mustCreateSuite(ctx, t, svc, "s")

	// List shows it.
	list, err := svc.ListEvaluations(ctx, &evaluationv1.ListEvaluationsRequest{})
	require.NoError(t, err)
	require.Len(t, list.GetEvaluations(), 1)

	// Get returns it with the prompt name.
	got, err := svc.GetEvaluation(ctx, &evaluationv1.GetEvaluationRequest{EvaluationId: id})
	require.NoError(t, err)
	require.Equal(t, id, got.GetEvaluation().GetEvaluationId())
	require.Equal(t, "p", got.GetEvaluation().GetPromptName())

	// Update renames.
	updated, err := svc.UpdateEvaluation(ctx, &evaluationv1.UpdateEvaluationRequest{EvaluationId: id, Name: "s2"})
	require.NoError(t, err)
	require.Equal(t, "s2", updated.GetEvaluation().GetName())

	// Delete removes it from the list.
	_, err = svc.DeleteEvaluation(ctx, &evaluationv1.DeleteEvaluationRequest{EvaluationId: id})
	require.NoError(t, err)
	list, err = svc.ListEvaluations(ctx, &evaluationv1.ListEvaluationsRequest{})
	require.NoError(t, err)
	require.Empty(t, list.GetEvaluations())
}

func TestServiceCaseLifecycle(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := newTestService(db)
	ctx := newServiceTestCtx("org-a")

	id := mustCreateSuite(ctx, t, svc, "s")
	caseID := mustAddCase(ctx, t, svc, id, "c1")

	// Update it.
	updated, err := svc.UpdateEvaluationCase(ctx, &evaluationv1.UpdateEvaluationCaseRequest{
		EvaluationId: id, CaseId: caseID, Name: "c1b", Variables: map[string]string{"q": "yo"},
		Checks: []*evaluationv1.EvaluationCheck{{Type: "exact", Expected: "42"}},
	})
	require.NoError(t, err)
	require.Equal(t, "c1b", updated.GetCase().GetName())

	// Reorder with the single case succeeds.
	_, err = svc.ReorderEvaluationCases(ctx, &evaluationv1.ReorderEvaluationCasesRequest{EvaluationId: id, CaseIds: []string{caseID}})
	require.NoError(t, err)

	// Delete it.
	_, err = svc.DeleteEvaluationCase(ctx, &evaluationv1.DeleteEvaluationCaseRequest{EvaluationId: id, CaseId: caseID})
	require.NoError(t, err)
	got, err := svc.GetEvaluation(ctx, &evaluationv1.GetEvaluationRequest{EvaluationId: id})
	require.NoError(t, err)
	require.Empty(t, got.GetEvaluation().GetCases())
}

func TestServiceCaseValidation(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := newTestService(db)
	ctx := newServiceTestCtx("org-a")

	id := mustCreateSuite(ctx, t, svc, "s")

	// Missing declared variable value.
	_, err := svc.CreateEvaluationCase(ctx, &evaluationv1.CreateEvaluationCaseRequest{
		EvaluationId: id, Name: "c", Variables: map[string]string{"other": "x"},
		Checks: []*evaluationv1.EvaluationCheck{{Type: "contains", Expected: "42"}},
	})
	require.Error(t, err)
	// Unknown check type.
	_, err = svc.CreateEvaluationCase(ctx, &evaluationv1.CreateEvaluationCaseRequest{
		EvaluationId: id, Name: "c", Variables: map[string]string{"q": "x"},
		Checks: []*evaluationv1.EvaluationCheck{{Type: "bogus", Expected: "42"}},
	})
	require.Error(t, err)
	// Empty name.
	_, err = svc.CreateEvaluationCase(ctx, &evaluationv1.CreateEvaluationCaseRequest{
		EvaluationId: id, Name: "", Variables: map[string]string{"q": "x"},
		Checks: []*evaluationv1.EvaluationCheck{{Type: "contains", Expected: "42"}},
	})
	require.Error(t, err)
}

func TestServiceTenantIsolation(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := newTestService(db)
	ctxA := newServiceTestCtx("org-a")
	ctxB := newServiceTestCtx("org-b")

	id := mustCreateSuite(ctxA, t, svc, "s")

	// org-b cannot see org-a's suite.
	_, err := svc.GetEvaluation(ctxB, &evaluationv1.GetEvaluationRequest{EvaluationId: id})
	require.Error(t, err)
	list, err := svc.ListEvaluations(ctxB, &evaluationv1.ListEvaluationsRequest{})
	require.NoError(t, err)
	require.Empty(t, list.GetEvaluations())
}

func TestServiceRunLifecycleAndCompare(t *testing.T) {
	db := newRunnerTestDB(t)
	setTestConfig(t)
	svc := newTestService(db)
	svc.SetCompletionProvider(&fakeCompletions{result: &CompletionResult{Completion: "42", PromptTokens: 3, CompletionTokens: 4, LatencyMS: 50}})
	svc.SetAPIKeyValidator(&fakeAPIKeys{valid: map[string]bool{"key-1": true}})
	ctx := newServiceTestCtx("org-a")

	id := mustCreateSuite(ctx, t, svc, "s")
	caseID := mustAddCase(ctx, t, svc, id, "c1")

	// Unknown API key is rejected.
	_, err := svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "missing-key", PromptVersion: 1})
	require.Error(t, err)
	// Wrong prompt version is rejected.
	_, err = svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "key-1", PromptVersion: 2})
	require.Error(t, err)

	// Valid run is created pending with all cases.
	runResp, err := svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "key-1", PromptVersion: 1})
	require.NoError(t, err)
	runID := runResp.GetRun().GetRunId()
	require.Equal(t, RunPending, runResp.GetRun().GetStatus())
	require.Equal(t, int32(1), runResp.GetRun().GetCaseCount())

	// Case-subset selection.
	runResp2, err := svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "key-1", PromptVersion: 1, CaseIds: []string{caseID}})
	require.NoError(t, err)
	require.Equal(t, int32(1), runResp2.GetRun().GetCaseCount())

	// Unknown case id in selection is rejected.
	_, err = svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "key-1", PromptVersion: 1, CaseIds: []string{"nope"}})
	require.Error(t, err)

	// List runs.
	runs, err := svc.ListEvaluationRuns(ctx, &evaluationv1.ListEvaluationRunsRequest{EvaluationId: id})
	require.NoError(t, err)
	require.Len(t, runs.GetRuns(), 2)

	// Get run.
	got, err := svc.GetEvaluationRun(ctx, &evaluationv1.GetEvaluationRunRequest{EvaluationId: id, RunId: runID})
	require.NoError(t, err)
	require.Equal(t, runID, got.GetRun().GetRunId())

	// Cancel the pending run.
	cancelled, err := svc.CancelEvaluationRun(ctx, &evaluationv1.CancelEvaluationRunRequest{EvaluationId: id, RunId: runID})
	require.NoError(t, err)
	require.NotNil(t, cancelled.GetRun())

	// Execute both runs through the runner so they reach terminal state.
	runner := NewRunner(svc)
	for i := 0; i < 2; i++ {
		claimed, err := runner.sweep(context.Background())
		require.NoError(t, err)
		require.True(t, claimed)
	}

	// Compare the two completed runs.
	cmp, err := svc.CompareEvaluationRuns(ctx, &evaluationv1.CompareEvaluationRunsRequest{EvaluationId: id, LeftRunId: runID, RightRunId: runResp2.GetRun().GetRunId()})
	require.NoError(t, err)
	require.Equal(t, runID, cmp.GetLeft().GetRunId())
	require.Len(t, cmp.GetCases(), 1)
	require.False(t, cmp.GetCases()[0].GetOnlyInLeft())
	require.False(t, cmp.GetCases()[0].GetOnlyInRight())
}

func TestServiceCompareRejectsNonTerminalRuns(t *testing.T) {
	db := newRunnerTestDB(t)
	setTestConfig(t)
	svc := newTestService(db)
	svc.SetCompletionProvider(&fakeCompletions{result: &CompletionResult{Completion: "42"}})
	svc.SetAPIKeyValidator(&fakeAPIKeys{valid: map[string]bool{"key-1": true}})
	ctx := newServiceTestCtx("org-a")

	id := mustCreateSuite(ctx, t, svc, "s")
	mustAddCase(ctx, t, svc, id, "c1")

	r1, err := svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "key-1", PromptVersion: 1})
	require.NoError(t, err)
	r2, err := svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "key-1", PromptVersion: 1})
	require.NoError(t, err)

	// Both pending -> compare rejected.
	_, err = svc.CompareEvaluationRuns(ctx, &evaluationv1.CompareEvaluationRunsRequest{EvaluationId: id, LeftRunId: r1.GetRun().GetRunId(), RightRunId: r2.GetRun().GetRunId()})
	require.Error(t, err)
}

func TestServiceRunFailsClosedWithoutProvider(t *testing.T) {
	db := newRunnerTestDB(t)
	setTestConfig(t)
	svc := newTestService(db)
	svc.SetAPIKeyValidator(&fakeAPIKeys{valid: map[string]bool{"key-1": true}})
	ctx := newServiceTestCtx("org-a")

	id := mustCreateSuite(ctx, t, svc, "s")
	mustAddCase(ctx, t, svc, id, "c1")

	// No completion provider wired -> run creation fails closed (AD5).
	_, err := svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "key-1", PromptVersion: 1})
	require.Error(t, err)
}

func TestServiceRunInFlightLimit(t *testing.T) {
	db := newRunnerTestDB(t)
	setTestConfig(t)
	svc := newTestService(db)
	svc.SetCompletionProvider(&fakeCompletions{result: &CompletionResult{Completion: "42"}})
	svc.SetAPIKeyValidator(&fakeAPIKeys{valid: map[string]bool{"key-1": true}})
	ctx := newServiceTestCtx("org-a")

	id := mustCreateSuite(ctx, t, svc, "s")
	mustAddCase(ctx, t, svc, id, "c1")

	// The default MaxInFlightRunsPerOrg is 10; the 11th concurrent run
	// must be rejected with the limit error.
	for i := 0; i < 10; i++ {
		_, err := svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "key-1", PromptVersion: 1})
		require.NoError(t, err)
	}
	_, err := svc.CreateEvaluationRun(ctx, &evaluationv1.CreateEvaluationRunRequest{EvaluationId: id, ModelId: "model-a", ApiKeyId: "key-1", PromptVersion: 1})
	require.Error(t, err)
}

func TestServiceResolveOrgRequiresHeader(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := newTestService(db)

	// No organization header and no session resolver -> unauthorized.
	_, err := svc.ListEvaluations(context.Background(), &evaluationv1.ListEvaluationsRequest{})
	require.Error(t, err)
}

func TestServiceSuiteNotFound(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := newTestService(db)
	ctx := newServiceTestCtx("org-a")

	_, err := svc.GetEvaluation(ctx, &evaluationv1.GetEvaluationRequest{EvaluationId: "missing"})
	require.Error(t, err)
	_, err = svc.GetEvaluationRun(ctx, &evaluationv1.GetEvaluationRunRequest{EvaluationId: "missing", RunId: "missing"})
	require.Error(t, err)
	_, err = svc.ListEvaluationRuns(ctx, &evaluationv1.ListEvaluationRunsRequest{EvaluationId: "missing"})
	require.Error(t, err)
}
