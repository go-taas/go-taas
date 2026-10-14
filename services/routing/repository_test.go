package routing

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
	auditapi "github.com/go-taas/go-taas/services/audit"
)

// newTestDB opens a fresh in-memory database with the routing schema.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	require.NoError(t, MigrateSchemaForFVT(db))
	return db
}

// TestRepositoryGroupCRUD covers the group repository directly.
func TestRepositoryGroupCRUD(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	// Create generates the id.
	g := &Group{Name: "team-a", Strategy: StrategyWeighted}
	require.NoError(t, repo.Create(ctx, g))
	assert.NotEmpty(t, g.ID)

	// A duplicate name maps to 13102.
	err := repo.Create(ctx, &Group{Name: "team-a", Strategy: StrategyWeighted})
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRoutingGroupNameConflict, apierrors.CodeOf(err))

	// GetByID / FindByName.
	row, err := repo.GetByID(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, "team-a", row.Name)
	_, err = repo.GetByID(ctx, "missing")
	assert.Equal(t, apierrors.CodeRoutingGroupNotFound, apierrors.CodeOf(err))
	found, err := repo.FindByName(ctx, "team-a")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, g.ID, found.ID)
	miss, err := repo.FindByName(ctx, "nope")
	require.NoError(t, err)
	assert.Nil(t, miss)

	// Update.
	row.Description = "updated"
	require.NoError(t, repo.Update(ctx, row))
	row, err = repo.GetByID(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated", row.Description)

	// List with pagination.
	g2 := &Group{Name: "team-b", Strategy: StrategyCostPriority}
	require.NoError(t, repo.Create(ctx, g2))
	rows, total, err := repo.List(ctx, GroupFilter{Offset: 0, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)
	rows, total, err = repo.List(ctx, GroupFilter{Name: "team-b", Offset: 0, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, "team-b", rows[0].Name)

	// Delete removes the group and its children.
	require.NoError(t, repo.ReplaceCandidates(ctx, g.ID, []*Candidate{{ModelID: "m1", ModelName: "qwen", Weight: 10}}))
	require.NoError(t, repo.Delete(ctx, g.ID))
	_, err = repo.GetByID(ctx, g.ID)
	assert.Equal(t, apierrors.CodeRoutingGroupNotFound, apierrors.CodeOf(err))
	cands, err := repo.ListCandidates(ctx, g.ID)
	require.NoError(t, err)
	assert.Empty(t, cands)
}

// TestRepositoryCandidatesAndRules covers the ordered replace paths.
func TestRepositoryCandidatesAndRules(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	g := &Group{Name: "team-a", Strategy: StrategyWeighted}
	require.NoError(t, repo.Create(ctx, g))

	// Replace assigns 0-based positions in declared order.
	require.NoError(t, repo.ReplaceCandidates(ctx, g.ID, []*Candidate{
		{ModelID: "m1", ModelName: "qwen", Weight: 10},
		{ModelID: "m2", ModelName: "deepseek", Weight: 20},
	}))
	cands, err := repo.ListCandidates(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, cands, 2)
	assert.Equal(t, 0, cands[0].Position)
	assert.Equal(t, 1, cands[1].Position)

	// A second replace is atomic (replaces, not appends).
	require.NoError(t, repo.ReplaceCandidates(ctx, g.ID, []*Candidate{
		{ModelID: "m3", ModelName: "llama", Weight: 5},
	}))
	cands, err = repo.ListCandidates(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "m3", cands[0].ModelID)

	// Rules: replace + list in declared order.
	require.NoError(t, repo.ReplaceRules(ctx, g.ID, []*Rule{
		{Name: "code", MatchType: MatchKeyword, Pattern: "code", TargetModelID: "m3", TargetModelName: "llama", Enabled: true},
		{Name: "math", MatchType: MatchKeyword, Pattern: "math", TargetModelID: "m3", TargetModelName: "llama", Enabled: false},
	}))
	rules, err := repo.ListRules(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, rules, 2)
	assert.Equal(t, "code", rules[0].Name)
	assert.Equal(t, 0, rules[0].Position)
}

// TestRepositoryGrants covers the grant queries.
func TestRepositoryGrants(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	g1 := &Group{Name: "team-a", Strategy: StrategyWeighted}
	require.NoError(t, repo.Create(ctx, g1))
	g2 := &Group{Name: "team-b", Strategy: StrategyWeighted}
	require.NoError(t, repo.Create(ctx, g2))

	// GrantAccess is idempotent.
	require.NoError(t, repo.GrantAccess(ctx, g1.ID, "org-1", "admin"))
	require.NoError(t, repo.GrantAccess(ctx, g1.ID, "org-1", "admin"))

	// HasGrant / IsGroupAuthorized.
	ok, err := repo.HasGrant(ctx, g1.ID, "org-1")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = repo.HasGrant(ctx, g1.ID, "org-2")
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = repo.IsGroupAuthorized(ctx, g1.ID, "org-1")
	require.NoError(t, err)
	assert.True(t, ok)
	// Default-allow: a group with zero grants authorizes every org.
	ok, err = repo.IsGroupAuthorized(ctx, g2.ID, "org-any")
	require.NoError(t, err)
	assert.True(t, ok)

	// ListGrants pagination.
	rows, total, err := repo.ListGrants(ctx, g1.ID, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, rows, 1)
	assert.Equal(t, "org-1", rows[0].OrganizationID)

	// CountGrants.
	count, err := repo.CountGrants(ctx, g1.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// RevokeAccess is idempotent.
	require.NoError(t, repo.RevokeAccess(ctx, g1.ID, "org-1"))
	require.NoError(t, repo.RevokeAccess(ctx, g1.ID, "org-1"))
	count, err = repo.CountGrants(ctx, g1.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestDecisionRepository covers the decision repository directly.
func TestDecisionRepository(t *testing.T) {
	db := newTestDB(t)
	repo := NewDecisionRepository(db)
	ctx := context.Background()

	// Insert + GetByID.
	d := &Decision{
		RequestID: "req-1", GroupID: "g1", GroupName: "team-a", Strategy: StrategyWeighted,
		SelectedModelID: "m1", SelectedModelName: "qwen",
		Candidates: DecisionCandidates{{ModelID: "m1", ModelName: "qwen", Outcome: OutcomeServed}},
	}
	require.NoError(t, repo.Insert(ctx, d))
	assert.NotEmpty(t, d.ID)
	got, err := repo.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, "req-1", got.RequestID)
	require.Len(t, got.Candidates, 1)
	assert.Equal(t, OutcomeServed, got.Candidates[0].Outcome)
	_, err = repo.GetByID(ctx, "missing")
	assert.Equal(t, apierrors.CodeRoutingGroupNotFound, apierrors.CodeOf(err))

	// List with filters.
	d2 := &Decision{
		RequestID: "req-2", GroupID: "g1", GroupName: "team-a", Strategy: StrategyManual,
		Candidates: DecisionCandidates{{ModelID: "m2", ModelName: "deepseek", Outcome: OutcomeFailedRetryable}},
	}
	require.NoError(t, repo.Insert(ctx, d2))
	rows, total, err := repo.List(ctx, DecisionFilter{Offset: 0, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)
	// Newest first.
	assert.Equal(t, "req-2", rows[0].RequestID)
	rows, total, err = repo.List(ctx, DecisionFilter{Strategy: StrategyManual, Offset: 0, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, "req-2", rows[0].RequestID)
	// The outcome filter: served requires a selected model.
	rows, total, err = repo.List(ctx, DecisionFilter{Outcome: "served", Offset: 0, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, "req-1", rows[0].RequestID)
	rows, total, err = repo.List(ctx, DecisionFilter{Outcome: "failed", Offset: 0, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, "req-2", rows[0].RequestID)
	// The since filter excludes old entries.
	rows, _, err = repo.List(ctx, DecisionFilter{Since: time.Now().UTC().Add(time.Hour), Offset: 0, Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, rows)

	// CountSince.
	count, err := repo.CountSince(ctx, "g1", time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	// DeleteBefore prunes old entries.
	old := &Decision{
		RequestID: "req-old", GroupID: "g1", GroupName: "team-a", Strategy: StrategyWeighted,
		CreatedAt: time.Now().UTC().Add(-48 * time.Hour),
	}
	require.NoError(t, repo.Insert(ctx, old))
	deleted, err := repo.DeleteBefore(ctx, time.Now().UTC().Add(-24*time.Hour), 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	_, err = repo.GetByID(ctx, old.ID)
	assert.Equal(t, apierrors.CodeRoutingGroupNotFound, apierrors.CodeOf(err))
}

// TestDecisionCandidatesJSON covers the jsonb Valuer/Scanner round trip.
func TestDecisionCandidatesJSON(t *testing.T) {
	in := DecisionCandidates{
		{ModelID: "m1", ModelName: "qwen", Outcome: OutcomeServed, Detail: "ok"},
		{ModelID: "m2", ModelName: "deepseek", Outcome: SkipReasonUnavailable},
	}
	val, err := in.Value()
	require.NoError(t, err)
	var out DecisionCandidates
	require.NoError(t, out.Scan(val))
	require.Len(t, out, 2)
	assert.Equal(t, "m1", out[0].ModelID)
	assert.Equal(t, "ok", out[0].Detail)
	assert.Equal(t, SkipReasonUnavailable, out[1].Outcome)

	// A nil scan clears; a bad payload errors.
	var empty DecisionCandidates
	require.NoError(t, empty.Scan(nil))
	assert.Nil(t, empty)
	assert.Error(t, empty.Scan("not-json"))
}

// TestRetentionRunnerRun covers the runner loop with a short interval.
func TestRetentionRunnerRun(t *testing.T) {
	db := newTestDB(t)
	repo := NewDecisionRepository(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := NewDecisionRetentionRunner(repo, 1, 100, 5*time.Millisecond)
	go func() {
		_ = runner.Run(ctx)
	}()
	// The loop ticks at least once without panicking.
	time.Sleep(30 * time.Millisecond)
	cancel()
}

// TestNewDecisionRetentionRunnerDefaults covers the constructor defaults.
func TestNewDecisionRetentionRunnerDefaults(t *testing.T) {
	r := NewDecisionRetentionRunner(nil, 0, 0, 0)
	assert.Equal(t, 7, r.retentionDays)
	assert.Equal(t, 1000, r.batchSize)
	assert.Equal(t, time.Hour, r.interval)
}

// TestServiceWiring covers the server.Service surface: name, attach,
// migrate and the nil-component guards.
func TestServiceWiring(t *testing.T) {
	svc := New(nil)
	assert.Equal(t, "routing", svc.ServiceName())

	grpcServer := grpc.NewServer()
	svc.AttachToServer(grpcServer)

	// Migrate without a database component fails closed.
	err := svc.Migrate(context.Background())
	require.Error(t, err)

	// The lazy accessors fail closed too.
	_, err = svc.groups()
	require.Error(t, err)
	_, err = svc.decisions()
	require.Error(t, err)

	// The setters are nil-safe.
	svc.SetSessionOrgResolver(nil)
	svc.SetSessionUserResolver(nil)
	svc.SetRoleGuard(nil)
	svc.SetAuditRecorder(nil)
	svc.SetModelCatalog(nil)
	svc.SetKeyScopeLister(nil)

	// The component-based runner returns nil without components.
	assert.Nil(t, NewDecisionRetentionRunnerFromComponents(nil))
}

// TestServiceMigrateWithDB covers Migrate over a wired database.
func TestServiceMigrateWithDB(t *testing.T) {
	db := newTestDB(t)
	svc := NewForFVT(db)
	require.NoError(t, svc.Migrate(context.Background()))
}

// TestRequireAdminRole covers the role guard paths (the #45 fail-closed
// pattern: a missing session or guard is unauthorized).
func TestRequireAdminRole(t *testing.T) {
	svc, _, _ := newRoutingSvc(t)
	ctx := context.Background()

	// The default wiring (session user + org + permissive guard)
	// authorizes.
	require.NoError(t, svc.requireAdminRole(ctx))

	// A denying guard forbids.
	svc.SetRoleGuard(fakeRoleGuard{err: apierrors.New(apierrors.CodeForbidden)})
	require.Error(t, svc.requireAdminRole(ctx))

	// A missing session user resolver is unauthorized.
	svc2, _, _ := newRoutingSvc(t)
	svc2.SetSessionUserResolver(nil)
	require.Error(t, svc2.requireAdminRole(ctx))

	// An empty session user is unauthorized.
	svc2.SetSessionUserResolver(fakeSessionUser{id: ""})
	require.Error(t, svc2.requireAdminRole(ctx))

	// A missing role guard is unauthorized.
	svc3, _, _ := newRoutingSvc(t)
	svc3.SetRoleGuard(nil)
	require.Error(t, svc3.requireAdminRole(ctx))

	// A session error propagates.
	svc4, _, _ := newRoutingSvc(t)
	svc4.SetSessionUserResolver(fakeSessionUserErr{})
	require.Error(t, svc4.requireAdminRole(ctx))
}

// fakeRoleGuard is a RoleGuard double.
type fakeRoleGuard struct{ err error }

func (g fakeRoleGuard) RequireRole(context.Context, string, string, string) error { return g.err }

// fakeSessionOrg is a SessionOrgResolver double.
type fakeSessionOrg struct{ org string }

func (r fakeSessionOrg) SessionActiveOrg(context.Context) (string, error) { return r.org, nil }

// fakeSessionUser is a SessionUserResolver double.
type fakeSessionUser struct{ id string }

func (r fakeSessionUser) SessionUserID(context.Context) (string, error) { return r.id, nil }

// fakeSessionUserErr is a SessionUserResolver double that always fails.
type fakeSessionUserErr struct{}

func (r fakeSessionUserErr) SessionUserID(context.Context) (string, error) {
	return "", apierrors.New(apierrors.CodeSessionInvalid)
}

// TestRecordAudit covers the best-effort audit path.
func TestRecordAudit(t *testing.T) {
	svc, _, _ := newRoutingSvc(t)
	ctx := context.Background()

	// No recorder: no-op.
	svc.recordAudit(ctx, "routing.group_created", "g1", "success")

	events := make([]*auditapi.AuditEvent, 0, 1)
	svc.SetAuditRecorder(fakeAuditRecorder{&events})
	svc.recordAudit(ctx, "routing.group_created", "g1", "success")
	require.Len(t, events, 1)
	assert.Equal(t, "routing.group_created", events[0].Action)
	assert.Equal(t, "routing_group", events[0].ResourceType)
}

// fakeAuditRecorder is an AuditRecorder double.
type fakeAuditRecorder struct{ events *[]*auditapi.AuditEvent }

func (r fakeAuditRecorder) Record(_ context.Context, ev *auditapi.AuditEvent) {
	*r.events = append(*r.events, ev)
}

// TestObservedP50 covers the request_logs latency query.
func TestObservedP50(t *testing.T) {
	db := newTestDB(t)
	// The request_logs table is owned by metering; create the minimal
	// shape for the read-only query.
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS request_logs (
		id TEXT PRIMARY KEY, model_id TEXT, status TEXT, latency_ms INTEGER, created_at DATETIME)`).Error)
	repo := NewDecisionRepository(db)
	ctx := context.Background()

	since := time.Now().UTC().Add(-time.Hour)
	p50, err := repo.ObservedP50(ctx, []string{"m1", "m2"}, since)
	require.NoError(t, err)
	assert.Empty(t, p50)

	// Two samples for m1 (10, 20) → p50 10; one for m2 (80).
	now := time.Now().UTC()
	for _, s := range []struct {
		model string
		lat   int64
	}{
		{"m1", 10}, {"m1", 20}, {"m2", 80},
	} {
		require.NoError(t, db.Exec(`INSERT INTO request_logs (id, model_id, status, latency_ms, created_at) VALUES (?, ?, 'success', ?, ?)`,
			s.model+"-"+fmtInt(s.lat), s.model, s.lat, now).Error)
	}
	p50, err = repo.ObservedP50(ctx, []string{"m1", "m2"}, since)
	require.NoError(t, err)
	assert.Equal(t, int64(10), p50["m1"])
	assert.Equal(t, int64(80), p50["m2"])
}

// fmtInt is a tiny strconv wrapper to keep the import list small.
func fmtInt(v int64) string {
	return time.Duration(v).String()
}

// TestLatencyOrderUnsampled covers the latency order's unsampled branch.
func TestLatencyOrderUnsampled(t *testing.T) {
	r := &resolver{latency: &fakeLatency{p50: map[string]int64{}}}
	rows := []*Candidate{
		{ModelID: "m1", LatencyTier: 3},
		{ModelID: "m2", LatencyTier: 1},
		{ModelID: "m3", LatencyTier: 2},
	}
	ordered, err := r.latencyOrder(context.Background(), rows)
	require.NoError(t, err)
	require.Len(t, ordered, 3)
	assert.Equal(t, "m2", ordered[0].ModelID)
	assert.Equal(t, "m3", ordered[1].ModelID)
	assert.Equal(t, "m1", ordered[2].ModelID)
}

// TestStableSortHelpers covers the sort helpers directly.
func TestStableSortHelpers(t *testing.T) {
	rows := []*Candidate{
		{ModelID: "a", Weight: 5},
		{ModelID: "b", Weight: 10},
		{ModelID: "c", Weight: 10},
	}
	stableSortByWeightDesc(rows)
	assert.Equal(t, "b", rows[0].ModelID)
	assert.Equal(t, "c", rows[1].ModelID)
	assert.Equal(t, "a", rows[2].ModelID)

	tiers := []*Candidate{
		{ModelID: "a", CostTier: 3},
		{ModelID: "b", CostTier: 0},
		{ModelID: "c", CostTier: 1},
	}
	stableSortByTier(tiers, func(c *Candidate) int {
		if c.CostTier >= minTier && c.CostTier <= maxTier {
			return c.CostTier
		}
		return missingTier
	})
	assert.Equal(t, "c", tiers[0].ModelID)
	assert.Equal(t, "a", tiers[1].ModelID)
	assert.Equal(t, "b", tiers[2].ModelID) // missing tier sorts last
}

// TestWeightedDrawDistribution covers the weighted draw's proportionality
// at the extremes.
func TestWeightedDrawDistribution(t *testing.T) {
	r := &resolver{rng: rand.New(rand.NewSource(42))} // #nosec G404 -- test sampling only.
	// A single candidate always draws itself.
	solo := []*Candidate{{ModelID: "only", Weight: 10}}
	for i := 0; i < 20; i++ {
		assert.Equal(t, "only", r.weightedDraw(solo).ModelID)
	}
	// A dominant weight wins the large majority of draws.
	rows := []*Candidate{
		{ModelID: "heavy", Weight: 99},
		{ModelID: "light", Weight: 1},
	}
	heavy := 0
	for i := 0; i < 1000; i++ {
		if r.weightedDraw(rows).ModelID == "heavy" {
			heavy++
		}
	}
	assert.Greater(t, heavy, 900)
}

// TestServerComponentsInterface pins the Service to server.Service.
func TestServerComponentsInterface(t *testing.T) {
	_ = t
	var _ server.Service = (*Service)(nil)
}

// TestGetServiceHandlerRegisterFn covers the gateway registration hook.
func TestGetServiceHandlerRegisterFn(t *testing.T) {
	svc := New(nil)
	require.NotNil(t, svc.GetServiceHandlerRegisterFn())
}

// TestLatencyWindow covers the observed-latency window default.
func TestLatencyWindow(t *testing.T) {
	config.SetConfigForTest(&config.Configuration{})
	t.Cleanup(func() { config.SetConfigForTest(nil) })
	assert.Equal(t, 15*time.Minute, latencyWindow())
}

// fakeDBComponent is a DBComponent double.
type fakeDBComponent struct{ db any }

func (c fakeDBComponent) GormDB() any { return c.db }

// fakeComponents is a Components double.
type fakeComponents struct{ db server.DBComponent }

func (c fakeComponents) DB() server.DBComponent       { return c.db }
func (c fakeComponents) Redis() server.RedisComponent { return nil }
func (c fakeComponents) MQ() server.MQComponent       { return nil }

// TestNewDecisionRetentionRunnerFromComponents covers the
// component-based runner factory: nil components, disabled retention and
// the happy path.
func TestNewDecisionRetentionRunnerFromComponents(t *testing.T) {
	// Every branch reads the global config; pin it for this test.
	cfg := &config.Configuration{}
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(nil) })

	// Nil components: nil runner.
	assert.Nil(t, NewDecisionRetentionRunnerFromComponents(nil))

	// Retention disabled: nil runner.
	db := newTestDB(t)
	comps := fakeComponents{db: fakeDBComponent{db: db}}
	assert.Nil(t, NewDecisionRetentionRunnerFromComponents(comps))

	// Enabled: a runner wired to the component's database.
	cfg.Routing.Retention.Enabled = true
	runner := NewDecisionRetentionRunnerFromComponents(comps)
	require.NotNil(t, runner)
	require.NotNil(t, runner.repo)
	require.NotNil(t, runner.repo.db)
}
