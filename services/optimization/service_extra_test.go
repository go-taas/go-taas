package optimization

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/server"
)

// TestServiceRefreshOptimization covers the admin refresh RPC: it
// runs the engine, records the run row and returns the fresh
// overview.
func TestServiceRefreshOptimization(t *testing.T) {
	svc := newSvc(t)
	db := svc.repo.db(context.Background())
	ctx := context.Background()

	// The engine needs the sibling tables; create the minimal set so
	// the pass succeeds with zero recommendations.
	exec(t, db, `CREATE TABLE models (id TEXT PRIMARY KEY, name TEXT)`)
	exec(t, db, `CREATE TABLE model_authorizations (id TEXT PRIMARY KEY, model_id TEXT, organization_id TEXT, granted_by TEXT, created_at DATETIME)`)
	exec(t, db, `CREATE TABLE organizations (id TEXT PRIMARY KEY, display_name TEXT)`)
	exec(t, db, `CREATE TABLE price_entries (id TEXT PRIMARY KEY, model_id TEXT, accelerator_type TEXT,
		input_price_per_million REAL, output_price_per_million REAL, cached_price_per_million REAL,
		currency TEXT, effective_from INTEGER, tiers_json TEXT, created_at DATETIME, updated_at DATETIME)`)
	exec(t, db, `CREATE TABLE charge_records (id TEXT PRIMARY KEY, organization_id TEXT, api_key_id TEXT, model_id TEXT,
		accelerator_type TEXT, period_start INTEGER, period_end INTEGER, prompt_tokens INTEGER, completion_tokens INTEGER,
		cached_tokens INTEGER, reasoning_tokens INTEGER, request_count INTEGER, amount REAL, currency TEXT, price_id TEXT,
		tier_index INTEGER, priced BOOLEAN, month_to_date_tokens INTEGER)`)
	exec(t, db, `CREATE TABLE request_logs (request_id TEXT PRIMARY KEY, organization_id TEXT, api_key_id TEXT, model_id TEXT,
		service_id TEXT, prompt_tokens INTEGER, status TEXT, created_at DATETIME)`)
	exec(t, db, `CREATE TABLE inference_services (id TEXT PRIMARY KEY, organization_id TEXT, name TEXT, model_id TEXT,
		image_id TEXT, replicas INTEGER, accelerator_type TEXT, state TEXT, autoscaling TEXT)`)
	exec(t, db, `CREATE TABLE autoscaling_policy (id INTEGER PRIMARY KEY, enabled BOOLEAN, scale_to_zero BOOLEAN)`)
	exec(t, db, `CREATE TABLE images (id TEXT PRIMARY KEY, name TEXT, tag TEXT, accelerator TEXT, engine TEXT)`)

	resp, err := svc.RefreshOptimization(ctx, nil)
	require.NoError(t, err)
	require.NotNil(t, resp.GetOverview())
	assert.EqualValues(t, 0, resp.GetOverview().GetActiveCount())

	// The run row is logged with the admin trigger.
	var trigger string
	require.NoError(t, db.Table("optimization_runs").Select("trigger").Take(&trigger).Error)
	assert.Equal(t, TriggerAdmin, trigger)

	// A non-admin caller is rejected.
	svc2 := newSvc(t)
	svc2.SetRoleGuard(fakeRoleGuard{err: apierrors.New(apierrors.CodeForbidden)})
	_, err = svc2.RefreshOptimization(ctx, nil)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestServiceEngineWiring covers the lazy engine wiring: the card
// lister seam reaches the engine, and the engine is reused.
func TestServiceEngineWiring(t *testing.T) {
	svc := newSvc(t)
	db := svc.repo.db(context.Background())

	// No components: ensureEngine returns nil (no db component).
	svcNoComponents := &Service{}
	assert.Nil(t, svcNoComponents.ensureEngine())

	// With the FVT db wired, the engine is built once and reused.
	e1 := svc.ensureEngine()
	require.NotNil(t, e1)
	e2 := svc.ensureEngine()
	assert.Same(t, e1, e2)

	// The card lister seam is injected into the engine.
	lister := &stubCardLister{}
	svc.SetCardTypeLister(lister)
	assert.NotNil(t, e1.cardTypes)

	// Migrate is idempotent when the repo is already wired.
	require.NoError(t, svc.Migrate(context.Background()))
	var count int64
	require.NoError(t, db.Table("optimization_recommendations").Count(&count).Error)
	assert.Zero(t, count)
}

// TestServiceGormDBErrors covers the component-less error paths.
func TestServiceGormDBErrors(t *testing.T) {
	svc := &Service{}
	_, err := svc.gormDB()
	require.Error(t, err)

	// repositories() fails without a db.
	_, err = svc.repositories()
	require.Error(t, err)

	// Migrate fails without a db.
	require.Error(t, svc.Migrate(context.Background()))
}

// TestNewRefreshRunnerFromComponents covers the runner factory: nil
// components and a missing refresh interval both yield nil.
func TestNewRefreshRunnerFromComponents(t *testing.T) {
	assert.Nil(t, NewRefreshRunnerFromComponents(nil))

	// A components double without a db and no refresh interval
	// configured yields nil.
	cfg := config.GetConfig()
	if cfg == nil {
		// No configuration loaded in unit tests: the factory reads
		// the global config, so only the nil-components path is
		// observable here.
		return
	}
	saved := cfg.Optimization.RefreshIntervalMinutes
	cfg.Optimization.RefreshIntervalMinutes = 0
	assert.Nil(t, NewRefreshRunnerFromComponents(&stubComponents{}))
	cfg.Optimization.RefreshIntervalMinutes = saved
}

// stubComponents is a minimal server.Components double.
type stubComponents struct{}

func (c *stubComponents) DB() server.DBComponent       { return nil }
func (c *stubComponents) Redis() server.RedisComponent { return nil }
func (c *stubComponents) MQ() server.MQComponent       { return nil }

// TestFormatPrice covers the currency symbol rendering.
func TestFormatPrice(t *testing.T) {
	assert.Equal(t, "¥", formatPrice("CNY"))
	assert.Equal(t, "$", formatPrice("USD"))
	assert.Equal(t, "EUR ", formatPrice("EUR"))
	assert.Equal(t, "0.50", formatAmount(0.5))
	assert.Equal(t, "18.00", formatAmount(18))
}

// TestFormatPrice2 covers the alternatives' price rendering.
func TestFormatPrice2(t *testing.T) {
	assert.Equal(t, "20.00", formatPrice2(20))
	assert.Equal(t, "6.50", formatPrice2(6.5))
	assert.Equal(t, "0.20", formatPrice2(0.2))
}

// TestInsertRun covers the run-row insert.
func TestInsertRun(t *testing.T) {
	db := newTestDB(t)
	repo := NewRepository(db)
	require.NoError(t, repo.InsertRun(context.Background(), &Run{ID: "run-1", Trigger: TriggerTicker, Success: true}))
	var run Run
	require.NoError(t, db.Where("id = ?", "run-1").Take(&run).Error)
	assert.True(t, run.Success)
}

// TestEngineString covers the engine's String (the runner label).
func TestEngineString(t *testing.T) {
	db := newTestDB(t)
	e := NewEngine(db, config.OptimizationConfig{}, nil)
	assert.NotEmpty(t, e.String())
}

// TestListRowDismissalDerivation covers the listJoinRow -> ListRow
// mapping for the dismissal columns.
func TestListRowDismissalDerivation(t *testing.T) {
	j := listJoinRow{
		ID: "rec-1",
	}
	row := j.toListRow(SurfaceAdmin)
	assert.Nil(t, row.Dismissal)

	// With a dismissal present.
	id, reason, note, by := "d-1", "planned", "note", "admin-1"
	at := time.Now().UTC()
	j.DismissalID = &id
	j.DismissalReason = &reason
	j.DismissalNote = &note
	j.DismissalBy = &by
	j.DismissalAt = &at
	row = j.toListRow(SurfaceAdmin)
	require.NotNil(t, row.Dismissal)
	assert.Equal(t, "d-1", row.Dismissal.ID)
	assert.Equal(t, "planned", row.Dismissal.Reason)
	assert.Equal(t, "note", row.Dismissal.Note)
	assert.Equal(t, "admin-1", row.Dismissal.DismissedBy)
	assert.WithinDuration(t, at, row.Dismissal.DismissedAt, time.Second)
}
