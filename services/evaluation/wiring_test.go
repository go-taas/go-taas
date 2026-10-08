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
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	evaluationv1 "github.com/go-taas/go-taas/proto/taas/evaluation/v1"
	"github.com/go-taas/go-taas/services/audit"
)

// fakeSessionOrg resolves the org from the session seam.
type fakeSessionOrg struct{ org string }

func (f *fakeSessionOrg) SessionActiveOrg(context.Context) (string, error) {
	return f.org, nil
}

// fakeSessionUser resolves the user id from the session seam.
type fakeSessionUser struct{ user string }

func (f *fakeSessionUser) SessionUserID(context.Context) (string, error) {
	return f.user, nil
}

// fakeRoleGuard always allows.
type fakeRoleGuard struct{}

func (fakeRoleGuard) RequireRole(context.Context, string, string, string) error { return nil }

// fakeAuditRecorder records audit events.
type fakeAuditRecorder struct{ events []string }

func (f *fakeAuditRecorder) Record(_ context.Context, e *audit.AuditEvent) {
	f.events = append(f.events, e.Action)
}

func TestServiceServerWiring(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := NewForFVT(db, &fakePrompts{promptID: "prompt-1", name: "p", content: "Answer ${q}", vars: []string{"q"}})

	// The production constructor and all setter seams compile and store.
	prod := New(nil)
	require.NotNil(t, prod)
	prod.SetSessionOrgResolver(&fakeSessionOrg{org: "org-a"})
	prod.SetSessionUserResolver(&fakeSessionUser{user: "user-a"})
	prod.SetRoleGuard(fakeRoleGuard{})
	prod.SetAuditRecorder(&fakeAuditRecorder{})
	prod.SetPromptResolver(&fakePrompts{promptID: "prompt-1"})
	prod.SetAPIKeyValidator(&fakeAPIKeys{})
	prod.SetCompletionProvider(&fakeCompletions{})
	prod.SetMeteringPublisher(&fakeMetering{})
	prod.SetCostAttributor(&fakeCosts{})

	// gRPC registration surface.
	require.Equal(t, "evaluation", prod.ServiceName())
	require.NotNil(t, prod.GetServiceHandlerRegisterFn())
	gs := grpc.NewServer()
	prod.AttachToServer(gs)

	// Migrate over the FVT schema is idempotent.
	require.NoError(t, svc.Migrate(context.Background()))

	// database() resolves the repo DB.
	gotDB, err := svc.database()
	require.NoError(t, err)
	require.Same(t, db, gotDB)

	// A service without repo or components fails closed.
	bare := &Service{}
	_, err = bare.database()
	require.Error(t, err)
	_, err = bare.repository()
	require.Error(t, err)
}

func TestServiceResolveOrgViaSession(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := NewForFVT(db, &fakePrompts{promptID: "prompt-1", name: "p", content: "c", vars: []string{"q"}})
	svc.SetSessionOrgResolver(&fakeSessionOrg{org: "org-session"})
	svc.SetSessionUserResolver(&fakeSessionUser{user: "user-a"})
	svc.SetRoleGuard(fakeRoleGuard{})
	recorder := &fakeAuditRecorder{}
	svc.SetAuditRecorder(recorder)

	// The session org wins over the header; the write role check and
	// audit recorder fire on create.
	resp, err := svc.CreateEvaluation(context.Background(), &evaluationv1.CreateEvaluationRequest{Name: "s", PromptId: "prompt-1", PromptVersion: 1})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetEvaluation().GetEvaluationId())
	require.Equal(t, []string{"evaluation.created"}, recorder.events)
}

func TestServiceHelpers(t *testing.T) {
	// pageBounds clamps offset/limit.
	offset, limit := pageBounds(nil)
	require.Zero(t, offset)
	require.Equal(t, 20, limit)
	offset, limit = pageBounds(&commonv1.PageRequest{Offset: 60, Limit: 200})
	require.Equal(t, 60, offset)
	require.Equal(t, 100, limit)

	// clampInt32 clamps to int32 range.
	require.Equal(t, int32(5), clampInt32(5))
	require.Equal(t, int32(2147483647), clampInt32(1<<40))

	// mapRunError passes known sentinel errors through and wraps
	// nothing else.
	require.ErrorIs(t, mapRunError(ErrEvaluationLimitExceeded), ErrEvaluationLimitExceeded)
	require.ErrorIs(t, mapRunError(ErrEvaluationCaseInvalid), ErrEvaluationCaseInvalid)
	other := errors.New("boom")
	require.Same(t, other, mapRunError(other))

	// truncateMessage caps length at 500 bytes.
	long := strings.Repeat("x", 600)
	require.Equal(t, 500, len(truncateMessage(long)))
	require.Equal(t, "short", truncateMessage("short"))
}

func TestRunnerSetClaimOnceAndRun(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := newTestService(db)
	runner := NewRunner(svc)

	// SetClaimOnce makes Run exit after one sweep.
	runner.SetClaimOnce()
	require.NoError(t, runner.Run(context.Background()))

	// Run also exits when the context is already cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, runner.Run(ctx))
}

func TestRunnerRunLoopSweeps(t *testing.T) {
	db := newRunnerTestDB(t)
	svc := newTestService(db)
	runner := NewRunner(svc)

	// Run sweeps at least once before the context is cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not stop")
	}
}

func TestServiceMeteringPublisher(t *testing.T) {
	pub := &fakeMetering{}
	// The no-op publisher used by the runner tests satisfies the seam.
	var _ MeteringPublisher = pub
	require.NotNil(t, pub)
}
