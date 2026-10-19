package fault

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/mq"
)

// recordingClient records published messages.
type recordingClient struct {
	published []mq.Message
}

func (c *recordingClient) Publish(_ context.Context, subject string, body []byte, headers map[string]string) error {
	c.published = append(c.published, mq.Message{Subject: subject, Body: body, Headers: headers})
	return nil
}

func (c *recordingClient) Subscribe(_ context.Context, _ string, _ mq.Handler) error { return nil }
func (c *recordingClient) Close() error                                             { return nil }

func TestRecoveryRunnerRun(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	client := &recordingClient{}
	trail := &fakeDeploymentTrail{}
	recovery := NewRecoveryRunner(repo, client)
	recovery.SetDeploymentTrail(trail)
	ctx := context.Background()

	// Seed the policy with auto-restart on for crash loop.
	_, err := repo.UpdatePolicy(ctx, map[string]bool{CauseServiceCrashLoop: true}, nil, time.Now().UTC())
	require.NoError(t, err)

	inc := &Incident{
		ID: newUUID(), Severity: SeverityWarning, Status: StatusDiagnosing,
		RootCause: CauseServiceCrashLoop, TargetType: TargetService, TargetName: "svc-a",
		StartedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateIncident(ctx, inc))

	require.NoError(t, recovery.Run(ctx, inc))

	// The incident moved to recovering and is auto-recovered.
	row, err := repo.GetIncident(ctx, inc.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusRecovering, row.Status)
	assert.True(t, row.AutoRecovered)

	// The deployment trail recorded the recovery.
	assert.Len(t, trail.events, 1)

	// A recovery action was published on fault.actions.
	require.Len(t, client.published, 1)
	assert.Equal(t, mq.DefaultSubjects().FaultActions, client.published[0].Subject)
}

func TestRecoveryRunnerDisabled(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	client := &recordingClient{}
	recovery := NewRecoveryRunner(repo, client)
	ctx := context.Background()

	// Auto-restart off for crash loop.
	_, err := repo.UpdatePolicy(ctx, map[string]bool{CauseServiceCrashLoop: false}, nil, time.Now().UTC())
	require.NoError(t, err)

	inc := &Incident{
		ID: newUUID(), Severity: SeverityWarning, Status: StatusDiagnosing,
		RootCause: CauseServiceCrashLoop, TargetType: TargetService, TargetName: "svc-a",
		StartedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateIncident(ctx, inc))

	require.NoError(t, recovery.Run(ctx, inc))
	// No action published.
	assert.Len(t, client.published, 0)
}

func TestRepositoryCleanupExpired(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	old := &Incident{
		ID: newUUID(), Severity: SeverityInfo, Status: StatusResolved,
		RootCause: CauseUnknown, TargetType: TargetService, TargetName: "svc-old",
		StartedAt: time.Now().Add(-48 * time.Hour).UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateIncident(ctx, old))

	n, err := repo.CleanupExpired(ctx, time.Now().Add(-24*time.Hour).UTC())
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestRepositoryUpdateIncidentAutoRecovered(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	inc := &Incident{
		ID: newUUID(), Severity: SeverityInfo, Status: StatusDetected,
		RootCause: CauseUnknown, TargetType: TargetService, TargetName: "svc-a",
		StartedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateIncident(ctx, inc))
	require.NoError(t, repo.UpdateIncidentAutoRecovered(ctx, inc.ID, true))
	row, err := repo.GetIncident(ctx, inc.ID)
	require.NoError(t, err)
	assert.True(t, row.AutoRecovered)
}

func TestClassifierHelpers(t *testing.T) {
	assert.Equal(t, SeverityCritical, SeverityForCause(CauseNodeNotReady))
	assert.Equal(t, SeverityWarning, SeverityForCause(CauseOOM))
	assert.Equal(t, SeverityInfo, SeverityForCause(CauseUnknown))
	assert.Equal(t, ActionRestart, DefaultActionForCause(CauseOOM))
	assert.Equal(t, ActionReschedule, DefaultActionForCause(CauseNodeNotReady))
	assert.Equal(t, "", DefaultActionForCause(CauseUnknown))
	assert.True(t, ValidSeverity(SeverityCritical))
	assert.False(t, ValidSeverity("bogus"))
	assert.True(t, ValidTargetType(TargetNode))
	assert.False(t, ValidTargetType("bogus"))
	assert.True(t, ValidAction(ActionDrain))
	assert.False(t, ValidAction("bogus"))
}

func TestRepositoryListActions(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	inc := &Incident{
		ID: newUUID(), Severity: SeverityInfo, Status: StatusRecovering,
		RootCause: CauseServiceCrashLoop, TargetType: TargetService, TargetName: "svc-a",
		StartedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateIncident(ctx, inc))
	require.NoError(t, repo.AddStep(ctx, inc.ID, &IncidentStep{
		ID: newUUID(), StepType: StepRecoveryStarted, Actor: "system", Detail: ActionRestart, CreatedAt: time.Now().UTC(),
	}))
	actions, err := repo.ListActions(ctx, inc.ID)
	require.NoError(t, err)
	assert.Len(t, actions, 1)
	assert.Equal(t, ActionRestart, actions[0].Action)
	assert.Equal(t, ActionStatusRunning, actions[0].Status)
}