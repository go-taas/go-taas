package fault

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"
	faultv1 "github.com/go-taas/go-taas/proto/taas/fault/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/mq"
	"github.com/go-taas/go-taas/services/audit"
)

// fakeSessionOrgResolver returns a fixed org for the session.
type fakeSessionOrgResolver struct{ org string }

func (f fakeSessionOrgResolver) SessionActiveOrg(context.Context) (string, error) { return f.org, nil }

// fakeSessionResolver returns a fixed user id for the session.
type fakeSessionResolver struct{ user string }

func (f fakeSessionResolver) SessionUserID(context.Context) (string, error) { return f.user, nil }

// fakeRoleGuard enforces the admin role.
type fakeRoleGuard struct{ allowed bool }

func (f fakeRoleGuard) RequireRole(context.Context, string, string, string) error {
	if f.allowed {
		return nil
	}
	return apierrors.New(apierrors.CodeForbidden)
}

// fakeAuditRecorder records audit events.
type fakeAuditRecorder struct{ events []*audit.AuditEvent }

func (f *fakeAuditRecorder) Record(_ context.Context, ev *audit.AuditEvent) {
	f.events = append(f.events, ev)
}

// fakeDeploymentTrail records deployment events.
type fakeDeploymentTrail struct{ events []string }

func (f *fakeDeploymentTrail) RecordDeploymentEvent(_ context.Context, serviceID, eventType, actor, detail string) error {
	f.events = append(f.events, serviceID+":"+eventType+":"+actor+":"+detail)
	return nil
}

// newTestDB opens a fresh in-memory sqlite database with the fault schema.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, MigrateSchemaForFVT(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

// newTestEnv wires a fault service with the seams.
func newTestEnv(t *testing.T, roleAllowed bool) *Service {
	t.Helper()
	db := newTestDB(t)
	svc := NewForFVT(db)
	svc.SetSessionOrgResolver(fakeSessionOrgResolver{org: "org-a"})
	svc.SetSessionResolver(fakeSessionResolver{user: "u-admin"})
	svc.SetRoleGuard(fakeRoleGuard{allowed: roleAllowed})
	svc.SetAuditRecorder(&fakeAuditRecorder{})
	svc.SetDeploymentTrail(&fakeDeploymentTrail{})
	return svc
}

// seedIncident creates an incident and returns its id.
func seedIncident(t *testing.T, svc *Service, cause, targetType, targetName string) string {
	t.Helper()
	repo := svc.repo
	now := time.Now().UTC()
	inc := &Incident{
		ID: newUUID(), Severity: SeverityForCause(cause), Status: StatusDetected,
		RootCause: cause, TargetType: targetType, TargetName: targetName,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateIncident(context.Background(), inc))
	return inc.ID
}

func TestClassifier(t *testing.T) {
	c := NewClassifier()
	assert.Equal(t, CauseServiceCrashLoop, c.Classify("crash_loop"))
	assert.Equal(t, CauseNodeNotReady, c.Classify("node_not_ready"))
	assert.Equal(t, CauseAcceleratorDevicePluginFailure, c.Classify("device_plugin_unhealthy"))
	assert.Equal(t, CauseOOM, c.Classify("oom_killed"))
	assert.Equal(t, CauseImagePullFailure, c.Classify("image_pull_backoff"))
	assert.Equal(t, CauseUnknown, c.Classify("bogus"))
}

func TestListIncidents(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	seedIncident(t, svc, CauseServiceCrashLoop, TargetService, "svc-a")
	seedIncident(t, svc, CauseNodeNotReady, TargetNode, "node-a")

	resp, err := svc.ListIncidents(ctx, &faultv1.ListIncidentsRequest{
		Page: &commonv1.PageRequest{Offset: 0, Limit: 20},
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetIncidents(), 2)
	assert.Equal(t, int64(2), resp.GetPageMeta().GetTotal())

	// Status filter.
	resp, err = svc.ListIncidents(ctx, &faultv1.ListIncidentsRequest{Status: StatusDetected})
	require.NoError(t, err)
	assert.Len(t, resp.GetIncidents(), 2)

	// Invalid status filter -> 13502.
	_, err = svc.ListIncidents(ctx, &faultv1.ListIncidentsRequest{Status: "bogus"})
	assert.EqualValues(t, apierrors.CodeFaultPolicyInvalid, apierrors.CodeOf(err))
}

func TestGetIncident(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	id := seedIncident(t, svc, CauseServiceCrashLoop, TargetService, "svc-a")

	resp, err := svc.GetIncident(ctx, &faultv1.GetIncidentRequest{IncidentId: id})
	require.NoError(t, err)
	assert.Equal(t, id, resp.GetIncident().GetIncidentId())
	assert.Equal(t, CauseServiceCrashLoop, resp.GetIncident().GetRootCause())

	// Unknown -> 13506.
	_, err = svc.GetIncident(ctx, &faultv1.GetIncidentRequest{IncidentId: "missing"})
	assert.EqualValues(t, apierrors.CodeFaultNotFound, apierrors.CodeOf(err))

	// A malformed (non-UUID) id maps to the same business code instead
	// of surfacing a postgres uuid cast error (bug FAULT-1).
	_, err = svc.GetIncident(ctx, &faultv1.GetIncidentRequest{IncidentId: "not-a-uuid"})
	assert.EqualValues(t, apierrors.CodeFaultNotFound, apierrors.CodeOf(err))
}

func TestUpdateAutoRecoveryPolicy(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()

	resp, err := svc.UpdateAutoRecoveryPolicy(ctx, &faultv1.UpdateAutoRecoveryPolicyRequest{
		AutoRestart: map[string]bool{CauseOOM: false},
	})
	require.NoError(t, err)
	assert.False(t, resp.GetPolicy().GetAutoRestart()[CauseOOM])

	// Invalid category -> 13502.
	_, err = svc.UpdateAutoRecoveryPolicy(ctx, &faultv1.UpdateAutoRecoveryPolicyRequest{
		AutoRestart: map[string]bool{"bogus": true},
	})
	assert.EqualValues(t, apierrors.CodeFaultPolicyInvalid, apierrors.CodeOf(err))
}

func TestGetAutoRecoveryPolicy(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	resp, err := svc.GetAutoRecoveryPolicy(ctx, &faultv1.GetAutoRecoveryPolicyRequest{})
	require.NoError(t, err)
	assert.True(t, resp.GetPolicy().GetDrainAlwaysConfirm())
}

func TestTriggerRecoveryAction(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	id := seedIncident(t, svc, CauseServiceCrashLoop, TargetService, "svc-a")

	// Valid restart.
	resp, err := svc.TriggerRecoveryAction(ctx, &faultv1.TriggerRecoveryActionRequest{
		IncidentId: id, Action: ActionRestart,
	})
	require.NoError(t, err)
	assert.Equal(t, ActionRestart, resp.GetAction().GetAction())

	// Drain without confirm -> 13503.
	_, err = svc.TriggerRecoveryAction(ctx, &faultv1.TriggerRecoveryActionRequest{
		IncidentId: id, Action: ActionDrain,
	})
	assert.EqualValues(t, apierrors.CodeFaultRecoveryInvalid, apierrors.CodeOf(err))

	// Unknown action -> 13503.
	_, err = svc.TriggerRecoveryAction(ctx, &faultv1.TriggerRecoveryActionRequest{
		IncidentId: id, Action: "bogus",
	})
	assert.EqualValues(t, apierrors.CodeFaultRecoveryInvalid, apierrors.CodeOf(err))
}

func TestOverrideRecoveryAction(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	id := seedIncident(t, svc, CauseServiceCrashLoop, TargetService, "svc-a")

	// No action in flight -> 13504.
	_, err := svc.OverrideRecoveryAction(ctx, &faultv1.OverrideRecoveryActionRequest{IncidentId: id})
	assert.EqualValues(t, apierrors.CodeFaultNoActionInFlight, apierrors.CodeOf(err))

	// Add a recovery_started step, then override succeeds.
	repo := svc.repo
	require.NoError(t, repo.AddStep(ctx, id, &IncidentStep{
		ID: newUUID(), StepType: StepRecoveryStarted, Actor: "system", Detail: ActionRestart, CreatedAt: time.Now().UTC(),
	}))
	resp, err := svc.OverrideRecoveryAction(ctx, &faultv1.OverrideRecoveryActionRequest{IncidentId: id})
	require.NoError(t, err)
	assert.Equal(t, ActionStatusOverridden, resp.GetAction().GetStatus())
}

func TestRoleDenied(t *testing.T) {
	svc := newTestEnv(t, false)
	_, err := svc.ListIncidents(context.Background(), &faultv1.ListIncidentsRequest{})
	assert.EqualValues(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

func TestActiveIncidentForService(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	id := seedIncident(t, svc, CauseServiceCrashLoop, TargetService, "svc-a")

	inc, ok, err := svc.ActiveIncidentForService(ctx, "svc-a")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, id, inc.IncidentID)
	assert.Equal(t, CauseServiceCrashLoop, inc.RootCause)

	// Healthy service.
	_, ok, err = svc.ActiveIncidentForService(ctx, "svc-b")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSignalConsumerDebounce(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	client := mq.NewFake()
	recovery := NewRecoveryRunner(repo, client)
	consumer := NewSignalConsumer(client, repo, NewClassifier(), recovery, 1, 3, time.Minute)
	ctx := context.Background()

	// A crash-loop signal below the threshold does not create an incident.
	body, _ := json.Marshal(faultSignal{TargetType: TargetService, TargetName: "svc-a", SignalType: "crash_loop", Detail: "restart 1"})
	require.NoError(t, consumer.handle(ctx, mq.Message{Body: body}))
	rows, _, err := repo.ListIncidents(ctx, IncidentListFilter{Limit: 20})
	require.NoError(t, err)
	assert.Len(t, rows, 0)

	// Two more signals in the window reach the threshold (3 total) and
	// create an incident.
	for i := 0; i < 2; i++ {
		body, _ = json.Marshal(faultSignal{TargetType: TargetService, TargetName: "svc-a", SignalType: "crash_loop", Detail: "restart"})
		require.NoError(t, consumer.handle(ctx, mq.Message{Body: body}))
	}
	rows, _, err = repo.ListIncidents(ctx, IncidentListFilter{Limit: 20})
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	assert.Equal(t, CauseServiceCrashLoop, rows[0].RootCause)
}

func TestServicePlumbing(t *testing.T) {
	svc := New(nil)
	assert.Equal(t, ServiceName, svc.ServiceName())
	assert.NotNil(t, svc.GetServiceHandlerRegisterFn())

	db := newTestDB(t)
	svc2 := NewForFVT(db)
	require.NoError(t, svc2.Migrate(context.Background()))
	grpcServer := grpc.NewServer()
	svc2.AttachToServer(grpcServer)

	// A service without a database component fails closed on a read.
	svc3 := New(nil)
	_, err := svc3.ListIncidents(context.Background(), &faultv1.ListIncidentsRequest{})
	assert.Error(t, err)
}

func TestGetIncidentWithStepsAndActions(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	id := seedIncident(t, svc, CauseServiceCrashLoop, TargetService, "svc-a")
	repo := svc.repo
	require.NoError(t, repo.AddStep(ctx, id, &IncidentStep{
		ID: newUUID(), StepType: StepDetected, Actor: "system", Detail: "restart 3x", CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, repo.AddStep(ctx, id, &IncidentStep{
		ID: newUUID(), StepType: StepRecoveryStarted, Actor: "system", Detail: ActionRestart, CreatedAt: time.Now().UTC(),
	}))

	resp, err := svc.GetIncident(ctx, &faultv1.GetIncidentRequest{IncidentId: id})
	require.NoError(t, err)
	assert.Len(t, resp.GetSteps(), 2)
	assert.Len(t, resp.GetActions(), 1)
	assert.Equal(t, ActionRestart, resp.GetActions()[0].GetAction())
}

func TestIncidentMessages(t *testing.T) {
	assert.Contains(t, incidentMessage(&Incident{RootCause: CauseServiceCrashLoop}), "restarting")
	assert.Contains(t, incidentMessage(&Incident{RootCause: CauseOOM}), "memory")
	assert.Contains(t, incidentMessage(&Incident{RootCause: CauseImagePullFailure}), "image")
	assert.Contains(t, incidentMessage(&Incident{RootCause: CauseNodeNotReady}), "node")
	assert.Contains(t, incidentMessage(&Incident{RootCause: CauseAcceleratorDevicePluginFailure}), "accelerator")
	assert.Contains(t, incidentMessage(&Incident{RootCause: CauseUnknown}), "failure")
}

func TestListIncidentsFilters(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	seedIncident(t, svc, CauseServiceCrashLoop, TargetService, "svc-a")
	seedIncident(t, svc, CauseNodeNotReady, TargetNode, "node-a")

	// Root-cause filter.
	resp, err := svc.ListIncidents(ctx, &faultv1.ListIncidentsRequest{RootCause: CauseNodeNotReady})
	require.NoError(t, err)
	assert.Len(t, resp.GetIncidents(), 1)
	assert.Equal(t, CauseNodeNotReady, resp.GetIncidents()[0].GetRootCause())

	// Target-type filter.
	resp, err = svc.ListIncidents(ctx, &faultv1.ListIncidentsRequest{TargetType: TargetService})
	require.NoError(t, err)
	assert.Len(t, resp.GetIncidents(), 1)

	// Search.
	resp, err = svc.ListIncidents(ctx, &faultv1.ListIncidentsRequest{Search: "node-a"})
	require.NoError(t, err)
	assert.Len(t, resp.GetIncidents(), 1)
	assert.Equal(t, "node-a", resp.GetIncidents()[0].GetTargetName())

	// Invalid severity filter -> 13502.
	_, err = svc.ListIncidents(ctx, &faultv1.ListIncidentsRequest{Severity: "bogus"})
	assert.EqualValues(t, apierrors.CodeFaultPolicyInvalid, apierrors.CodeOf(err))

	// Invalid root-cause filter -> 13502.
	_, err = svc.ListIncidents(ctx, &faultv1.ListIncidentsRequest{RootCause: "bogus"})
	assert.EqualValues(t, apierrors.CodeFaultPolicyInvalid, apierrors.CodeOf(err))

	// Invalid target-type filter -> 13502.
	_, err = svc.ListIncidents(ctx, &faultv1.ListIncidentsRequest{TargetType: "bogus"})
	assert.EqualValues(t, apierrors.CodeFaultPolicyInvalid, apierrors.CodeOf(err))
}

func TestRepositoryUpdateIncidentStatusResolved(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	inc := &Incident{
		ID: newUUID(), Severity: SeverityInfo, Status: StatusDetected,
		RootCause: CauseUnknown, TargetType: TargetService, TargetName: "svc-a",
		StartedAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, repo.CreateIncident(ctx, inc))
	now := time.Now().UTC()
	require.NoError(t, repo.UpdateIncidentStatus(ctx, inc.ID, StatusResolved, &now))
	row, err := repo.GetIncident(ctx, inc.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusResolved, row.Status)
	assert.NotNil(t, row.ResolvedAt)
}

func TestGetPolicySeedsDefaults(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	policy, err := repo.GetPolicy(ctx, &FaultPolicy{
		AutoRestart:       datatypes.JSON(`{"service_crash_loop":true}`),
		AutoReschedule:    datatypes.JSON(`{"node_not_ready":true}`),
		DrainAlwaysConfirm: true,
	})
	require.NoError(t, err)
	assert.True(t, policy.DrainAlwaysConfirm)
	assert.Contains(t, string(policy.AutoRestart), "service_crash_loop")
}

func TestIncidentNotFoundHelper(t *testing.T) {
	err := incidentNotFound(gorm.ErrRecordNotFound)
	assert.EqualValues(t, apierrors.CodeFaultNotFound, apierrors.CodeOf(err))
	assert.Nil(t, incidentNotFound(nil))
}

func TestUpdateAutoRecoveryPolicyReschedule(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	resp, err := svc.UpdateAutoRecoveryPolicy(ctx, &faultv1.UpdateAutoRecoveryPolicyRequest{
		AutoReschedule: map[string]bool{CauseNodeNotReady: false},
	})
	require.NoError(t, err)
	assert.False(t, resp.GetPolicy().GetAutoReschedule()[CauseNodeNotReady])

	// Invalid reschedule category -> 13502.
	_, err = svc.UpdateAutoRecoveryPolicy(ctx, &faultv1.UpdateAutoRecoveryPolicyRequest{
		AutoReschedule: map[string]bool{"bogus": true},
	})
	assert.EqualValues(t, apierrors.CodeFaultPolicyInvalid, apierrors.CodeOf(err))
}

func TestTriggerRecoveryActionDrainConfirmed(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	id := seedIncident(t, svc, CauseNodeNotReady, TargetNode, "node-a")

	resp, err := svc.TriggerRecoveryAction(ctx, &faultv1.TriggerRecoveryActionRequest{
		IncidentId: id, Action: ActionDrain, Confirm: true,
	})
	require.NoError(t, err)
	assert.Equal(t, ActionDrain, resp.GetAction().GetAction())
}

func TestServiceNoDBFailsClosed(t *testing.T) {
	svc := New(nil)
	ctx := context.Background()
	_, err := svc.GetIncident(ctx, &faultv1.GetIncidentRequest{IncidentId: "x"})
	assert.Error(t, err)
	_, err = svc.GetAutoRecoveryPolicy(ctx, &faultv1.GetAutoRecoveryPolicyRequest{})
	assert.Error(t, err)
	_, _, err = svc.ActiveIncidentForService(ctx, "svc-a")
	assert.Error(t, err)
}

func TestActionStatusForStep(t *testing.T) {
	assert.Equal(t, ActionStatusRunning, actionStatusForStep(StepRecoveryStarted))
	assert.Equal(t, ActionStatusSucceeded, actionStatusForStep(StepRecoverySucceeded))
	assert.Equal(t, ActionStatusFailed, actionStatusForStep(StepRecoveryFailed))
	assert.Equal(t, ActionStatusOverridden, actionStatusForStep(StepOverridden))
	assert.Equal(t, ActionStatusSucceeded, actionStatusForStep(StepManualAction))
	assert.Equal(t, ActionStatusPending, actionStatusForStep("bogus"))
}

func TestMigrateNoDB(t *testing.T) {
	svc := New(nil)
	assert.Error(t, svc.Migrate(context.Background()))
}

func TestSignalConsumerRun(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	client := mq.NewFake()
	recovery := NewRecoveryRunner(repo, client)
	consumer := NewSignalConsumer(client, repo, NewClassifier(), recovery, 1, 3, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	cancel()
	<-done
}

func TestSignalConsumerRunnerNil(t *testing.T) {
	assert.Nil(t, NewSignalConsumerRunner(nil))
}

func TestUpdateAutoRecoveryPolicyAudit(t *testing.T) {
	svc := newTestEnv(t, true)
	ctx := context.Background()
	rec := svc.auditRecorder.(*fakeAuditRecorder)
	_, err := svc.UpdateAutoRecoveryPolicy(ctx, &faultv1.UpdateAutoRecoveryPolicyRequest{
		AutoRestart: map[string]bool{CauseOOM: false},
	})
	require.NoError(t, err)
	assert.Len(t, rec.events, 1)
	assert.Equal(t, "fault.policy.updated", rec.events[0].Action)
}