package optimization

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// newRepoDB opens a fresh in-memory SQLite database with the three
// optimization tables.
func newRepoDB(t *testing.T) *gorm.DB {
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

// entry builds one computed recommendation for the swap tests.
func entry(rule, orgID, suggestion string, saving float64) ComputedRecommendation {
	return ComputedRecommendation{
		Rule:                   rule,
		TargetType:             TargetOrgModel,
		TargetID:               orgID + "/model-1",
		TargetDisplay:          orgID + " / model-1",
		OrganizationID:         orgID,
		ModelID:                "model-1",
		Suggestion:             suggestion,
		EstimatedMonthlySaving: saving,
		Inputs:                 map[string]any{"requests": 100},
		Arithmetic:             "a - b = c",
		ActingPageAdmin:        ActingLink{Route: "/admin/pricing", Label: "View pricing"},
		ActingPageUser:         ActingLink{Route: "/pricing", Label: "View pricing"},
	}
}

// exec runs one raw statement and fails the test on error.
func exec(t *testing.T, db *gorm.DB, sql string, args ...any) {
	t.Helper()
	require.NoError(t, db.Exec(sql, args...).Error)
}

// TestEngineSwapPreservesIdentity covers the atomic swap (AD6/AD11):
// the upsert preserves recommendation_id and first_seen_at while the
// condition persists, refreshes the computed fields, deletes vanished
// fingerprints and records the run row.
func TestEngineSwapPreservesIdentity(t *testing.T) {
	db := newRepoDB(t)
	engine := NewEngine(db, config.OptimizationConfig{WindowDays: 30, TopNPerOrg: 50, Currency: "CNY"}, nil)

	// Seed one row directly, as an earlier snapshot would have left it.
	firstSeen := time.Now().UTC().Add(-2 * time.Hour)
	seed := entry(RuleSubstitution, "org-1", "switch to b", 500)
	seedFP := Fingerprint(seed.Rule, seed.TargetType, seed.TargetID, seed.Suggestion)
	seedRow := Recommendation{
		ID:                     "rec-existing",
		Fingerprint:            seedFP,
		Rule:                   seed.Rule,
		TargetType:             seed.TargetType,
		TargetID:               seed.TargetID,
		TargetDisplay:          seed.TargetDisplay,
		OrganizationID:         seed.OrganizationID,
		ModelID:                seed.ModelID,
		Suggestion:             seed.Suggestion,
		EstimatedMonthlySaving: 400, // the older, drifted estimate
		Currency:               "CNY",
		Inputs:                 []byte(`{}`),
		Alternatives:           []byte(`[]`),
		ActingPageAdmin:        []byte(`{}`),
		ActingPageUser:         []byte(`{}`),
		FirstSeenAt:            firstSeen,
		LastComputedAt:         firstSeen,
	}
	require.NoError(t, db.Create(&seedRow).Error)
	// A vanished condition from the older snapshot.
	vanished := entry(RuleCache, "org-2", "cache prompts", 300)
	require.NoError(t, db.Create(&Recommendation{
		ID:                     "rec-vanished",
		Fingerprint:            Fingerprint(vanished.Rule, vanished.TargetType, vanished.TargetID, vanished.Suggestion),
		Rule:                   vanished.Rule,
		TargetType:             vanished.TargetType,
		TargetID:               vanished.TargetID,
		TargetDisplay:          vanished.TargetDisplay,
		OrganizationID:         vanished.OrganizationID,
		Suggestion:             vanished.Suggestion,
		EstimatedMonthlySaving: 300,
		Currency:               "CNY",
		Inputs:                 []byte(`{}`),
		Alternatives:           []byte(`[]`),
		FirstSeenAt:            firstSeen,
		LastComputedAt:         firstSeen,
	}).Error)

	// One pass that recomputes the same condition (a drifted saving)
	// plus one new condition.
	ctx := context.Background()
	emitted, err := engine.Run(ctx, TriggerTicker)
	require.NoError(t, err)
	// The engine reads the (empty) sibling tables, so the pass emits
	// nothing; the swap must still delete the vanished rows and keep
	// the run log correct. To exercise the upsert path, run the swap
	// directly with the computed entries.
	_ = emitted

	repo := NewRepository(db)
	require.NoError(t, repo.UpsertSnapshot(ctx, []ComputedRecommendation{seed, entry(RuleBatch, "org-1", "migrate to batch", 250)}, "CNY", time.Now().UTC(), time.Now().UTC(), TriggerAdmin))

	var kept Recommendation
	require.NoError(t, db.Where("fingerprint = ?", seedFP).Take(&kept).Error)
	// The identity is preserved: same id, same first_seen_at.
	assert.Equal(t, "rec-existing", kept.ID)
	assert.WithinDuration(t, firstSeen, kept.FirstSeenAt, time.Second)
	// The computed fields are refreshed.
	assert.InDelta(t, 500.0, kept.EstimatedMonthlySaving, 0.001)
	assert.True(t, kept.LastComputedAt.After(firstSeen))

	// The vanished fingerprint is gone; the new one exists.
	var count int64
	require.NoError(t, db.Model(&Recommendation{}).Where("id = ?", "rec-vanished").Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, db.Model(&Recommendation{}).Where("rule = ?", RuleBatch).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

// TestEngineRunDeterminism covers AC6: the same settled fixture yields
// the same set and numbers on repeated passes, and the run row is
// recorded.
func TestEngineRunDeterminism(t *testing.T) {
	db := newRepoDB(t)
	// Seed the sibling tables the rules read: one catalog model pair
	// with prices, one charge group, one request log.
	exec(t, db, `CREATE TABLE models (id TEXT PRIMARY KEY, name TEXT)`)
	exec(t, db, `INSERT INTO models (id, name) VALUES ('m-big', 'qwen-72b'), ('m-small', 'qwen-7b')`)
	exec(t, db, `CREATE TABLE model_authorizations (id TEXT PRIMARY KEY, model_id TEXT, organization_id TEXT, granted_by TEXT, created_at DATETIME)`)
	exec(t, db, `CREATE TABLE organizations (id TEXT PRIMARY KEY, display_name TEXT)`)
	exec(t, db, `INSERT INTO organizations (id, display_name) VALUES ('org-1', 'Acme')`)
	exec(t, db, `CREATE TABLE price_entries (id TEXT PRIMARY KEY, model_id TEXT, accelerator_type TEXT,
		input_price_per_million REAL, output_price_per_million REAL, cached_price_per_million REAL,
		currency TEXT, effective_from INTEGER, tiers_json TEXT, created_at DATETIME, updated_at DATETIME)`)
	now := time.Now().UTC().Unix()
	exec(t, db, `INSERT INTO price_entries (id, model_id, accelerator_type, input_price_per_million, output_price_per_million, cached_price_per_million, currency, effective_from, tiers_json)
		VALUES ('p1', 'm-big', 'default', 20, 60, 2, 'CNY', ?, '[]')`, now-86400)
	exec(t, db, `INSERT INTO price_entries (id, model_id, accelerator_type, input_price_per_million, output_price_per_million, cached_price_per_million, currency, effective_from, tiers_json)
		VALUES ('p2', 'm-small', 'default', 2, 6, 0.2, 'CNY', ?, '[]')`, now-86400)
	exec(t, db, `CREATE TABLE charge_records (id TEXT PRIMARY KEY, organization_id TEXT, api_key_id TEXT, model_id TEXT,
		accelerator_type TEXT, period_start INTEGER, period_end INTEGER, prompt_tokens INTEGER, completion_tokens INTEGER,
		cached_tokens INTEGER, reasoning_tokens INTEGER, request_count INTEGER, amount REAL, currency TEXT, price_id TEXT,
		tier_index INTEGER, priced BOOLEAN, month_to_date_tokens INTEGER)`)
	// 20M prompt + 20M completion tokens on the expensive model:
	// spend = 20*20 + 20*60 = 1600; on the small model 20*2 + 20*6 = 160;
	// saving = 1440 >= the 100 floor.
	exec(t, db, `INSERT INTO charge_records (id, organization_id, api_key_id, model_id, accelerator_type, period_start, period_end,
		prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, request_count, amount, currency, price_id, tier_index, priced, month_to_date_tokens)
		VALUES ('c1', 'org-1', 'key-1', 'm-big', 'default', ?, ?, 20000000, 20000000, 0, 0, 2000, 1600, 'CNY', 'p1', -1, 1, 0)`,
		now-3600, now)
	cfg := config.OptimizationConfig{
		WindowDays: 30,
		TopNPerOrg: 50,
		Currency:   "CNY",
		R1: config.OptimizationR1Config{
			MinRequests:     1000,
			MinBilledTokens: 10000000,
			ContextRatio:    0.8,
			MinSaving:       100,
		},
		ModelContextLengths: map[string]int64{"qwen-72b": 131072, "qwen-7b": 131072},
	}
	engine := NewEngine(db, cfg, nil)
	ctx := context.Background()

	emitted, err := engine.Run(ctx, TriggerAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, emitted)

	// The exact expected recommendation (AC6): the substitution with
	// the verifiable arithmetic.
	repo := NewRepository(db)
	rows, total, err := repo.List(ctx, Filter{}, SurfaceAdmin, "")
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
	row := rows[0].Recommendation
	assert.Equal(t, RuleSubstitution, row.Rule)
	assert.Equal(t, "org-1/m-big", row.TargetID)
	assert.Equal(t, "Acme / qwen-72b", row.TargetDisplay)
	assert.InDelta(t, 1440.0, row.EstimatedMonthlySaving, 0.01)
	assert.Contains(t, row.Suggestion, "qwen-7b")
	assert.Contains(t, row.Arithmetic, "1440.00")

	// Determinism: a second pass over the same data yields the same
	// set, the same numbers and the same identity (no duplicate rows).
	firstID := row.ID
	emitted, err = engine.Run(ctx, TriggerAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, emitted)
	rows, total, err = repo.List(ctx, Filter{}, SurfaceAdmin, "")
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	assert.Equal(t, firstID, rows[0].Recommendation.ID)
	assert.InDelta(t, 1440.0, rows[0].Recommendation.EstimatedMonthlySaving, 0.01)

	// The run rows are logged (one per pass).
	var runs int64
	require.NoError(t, db.Model(&Run{}).Where("success = ?", true).Count(&runs).Error)
	assert.EqualValues(t, 2, runs)
}

// TestEngineRunFailureKeepsSnapshot covers AC10's failure semantics:
// an error before the swap leaves the previous active set intact.
func TestEngineRunFailureKeepsSnapshot(t *testing.T) {
	db := newRepoDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	// A healthy snapshot.
	require.NoError(t, repo.UpsertSnapshot(ctx, []ComputedRecommendation{entry(RuleCache, "org-1", "cache prompts", 300)}, "CNY", time.Now().UTC(), time.Now().UTC(), TriggerAdmin))
	var before int64
	require.NoError(t, db.Model(&Recommendation{}).Count(&before).Error)
	require.EqualValues(t, 1, before)

	// Break the swap's last step (the run-row insert): the whole
	// transaction rolls back, so the previous snapshot survives.
	exec(t, db, `DROP TABLE optimization_runs`)

	// A pass over a broken source (a missing table) fails; the
	// snapshot is untouched.
	engine := NewEngine(db, config.OptimizationConfig{WindowDays: 30, Currency: "CNY"}, nil)
	_, err := engine.Run(ctx, TriggerTicker)
	require.Error(t, err)
	engine.RunFailure(ctx, TriggerTicker, time.Now().UTC(), err)

	var after int64
	require.NoError(t, db.Model(&Recommendation{}).Count(&after).Error)
	assert.Equal(t, before, after)
}

// TestRepositoryDismissalDerivation covers the (surface, fingerprint)
// dismiss keying (AD11/FR3.4): one shared condition row, two
// independent surface states.
func TestRepositoryDismissalDerivation(t *testing.T) {
	db := newRepoDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.UpsertSnapshot(ctx, []ComputedRecommendation{entry(RuleCache, "org-1", "cache prompts", 300)}, "CNY", time.Now().UTC(), time.Now().UTC(), TriggerAdmin))
	rows, _, err := repo.List(ctx, Filter{}, SurfaceAdmin, "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	fp := rows[0].Recommendation.Fingerprint

	// Dismiss on the admin surface only.
	require.NoError(t, repo.InsertDismissal(ctx, SurfaceAdmin, fp, ReasonPlanned, "", "admin-1"))

	// The admin active list excludes it; the all-status list shows
	// it with its dismissal; the user surface still sees it active.
	activeAdmin, total, err := repo.List(ctx, Filter{}, SurfaceAdmin, "")
	require.NoError(t, err)
	assert.Zero(t, total)
	assert.Empty(t, activeAdmin)

	adminRows, _, err := repo.List(ctx, Filter{Status: StatusAll}, SurfaceAdmin, "")
	require.NoError(t, err)
	require.Len(t, adminRows, 1)
	require.NotNil(t, adminRows[0].Dismissal)
	assert.Equal(t, ReasonPlanned, adminRows[0].Dismissal.Reason)

	userRows, _, err := repo.List(ctx, Filter{}, SurfaceUser, "org-1")
	require.NoError(t, err)
	require.Len(t, userRows, 1)
	assert.Nil(t, userRows[0].Dismissal)

	// The status filter derives per surface.
	dismissedOnly, total, err := repo.List(ctx, Filter{Status: StatusDismissed}, SurfaceAdmin, "")
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Len(t, dismissedOnly, 1)
	_, total, err = repo.List(ctx, Filter{Status: StatusDismissed}, SurfaceUser, "org-1")
	require.NoError(t, err)
	assert.Zero(t, total)

	// Double dismiss on the same surface -> 13202; the other surface
	// is unaffected.
	err = repo.InsertDismissal(ctx, SurfaceAdmin, fp, ReasonOther, "x", "admin-1")
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRecommendationDismissed, apierrors.CodeOf(err))
	require.NoError(t, repo.InsertDismissal(ctx, SurfaceUser, fp, ReasonNotApplicable, "", "user-1"))

	// Restore on the admin surface -> active there, dismissed on user.
	require.NoError(t, repo.DeleteDismissal(ctx, SurfaceAdmin, fp))
	err = repo.DeleteDismissal(ctx, SurfaceAdmin, fp)
	require.Error(t, err)
	assert.Equal(t, apierrors.CodeRecommendationRestoreInvalid, apierrors.CodeOf(err))
}

// TestRepositoryOverviewAggregates covers the summary aggregates
// (FR2.2): totals over active rows, per-rule counts, the dismissed
// count and the top org.
func TestRepositoryOverviewAggregates(t *testing.T) {
	db := newRepoDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	now := time.Now().UTC()
	require.NoError(t, repo.UpsertSnapshot(ctx, []ComputedRecommendation{
		entry(RuleSubstitution, "org-1", "switch to b", 500),
		entry(RuleCache, "org-1", "cache prompts", 200),
		entry(RuleSubstitution, "org-2", "switch to c", 900),
	}, "CNY", now, now, TriggerAdmin))

	// Dismiss one org-1 row on the admin surface.
	rows, _, err := repo.List(ctx, Filter{Rule: RuleCache}, SurfaceAdmin, "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, repo.InsertDismissal(ctx, SurfaceAdmin, rows[0].Recommendation.Fingerprint, ReasonPlanned, "", "admin-1"))

	ov, err := repo.Overview(ctx, SurfaceAdmin, "")
	require.NoError(t, err)
	assert.InDelta(t, 1400.0, ov.TotalSaving, 0.001) // 500 + 900 active
	assert.EqualValues(t, 2, ov.ActiveCount)
	assert.EqualValues(t, 1, ov.DismissedCount)
	assert.Equal(t, "org-2", ov.TopOrgID)
	assert.InDelta(t, 900.0, ov.TopOrgSaving, 0.001)
	assert.EqualValues(t, 2, ov.CountByRule[RuleSubstitution])
	_, ok := ov.CountByRule[RuleCache]
	assert.False(t, ok) // dismissed rows do not count as active

	// The user-surface overview of org-1: the admin-surface
	// dismissal does not leak, so both org-1 rows stay active there.
	ovUser, err := repo.Overview(ctx, SurfaceUser, "org-1")
	require.NoError(t, err)
	assert.InDelta(t, 700.0, ovUser.TotalSaving, 0.001)
	assert.EqualValues(t, 2, ovUser.ActiveCount)
	assert.EqualValues(t, 0, ovUser.DismissedCount)
	assert.Empty(t, ovUser.TopOrgID) // the top-org card is admin-only
}

// TestRepositoryListPaginationAndOrder covers the ranking order and
// the pagination window.
func TestRepositoryListPaginationAndOrder(t *testing.T) {
	db := newRepoDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	entries := []ComputedRecommendation{
		entry(RuleSubstitution, "org-1", "s-a", 100),
		entry(RuleSubstitution, "org-1", "s-b", 300),
		entry(RuleCache, "org-1", "s-c", 200),
	}
	require.NoError(t, repo.UpsertSnapshot(ctx, entries, "CNY", time.Now().UTC(), time.Now().UTC(), TriggerAdmin))

	// Ranked by saving DESC.
	rows, total, err := repo.List(ctx, Filter{}, SurfaceAdmin, "")
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, rows, 3)
	assert.InDelta(t, 300.0, rows[0].Recommendation.EstimatedMonthlySaving, 0.001)
	assert.InDelta(t, 200.0, rows[1].Recommendation.EstimatedMonthlySaving, 0.001)
	assert.InDelta(t, 100.0, rows[2].Recommendation.EstimatedMonthlySaving, 0.001)

	// Pagination window.
	rows, total, err = repo.List(ctx, Filter{Offset: 1, Limit: 1}, SurfaceAdmin, "")
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	require.Len(t, rows, 1)
	assert.InDelta(t, 200.0, rows[0].Recommendation.EstimatedMonthlySaving, 0.001)

	// Min-saving floor.
	rows, total, err = repo.List(ctx, Filter{MinSaving: 250}, SurfaceAdmin, "")
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, rows, 1)
}

// TestContextLengthAndCardCostResolution covers the case-insensitive
// config-map lookups (viper lowercases keys; AD12/AD14).
func TestContextLengthAndCardCostResolution(t *testing.T) {
	lengths := map[string]int64{"qwen-72b": 131072}
	assert.EqualValues(t, 131072, contextLengthOf(lengths, "qwen-72b"))
	assert.EqualValues(t, 131072, contextLengthOf(lengths, "Qwen-72B"))
	assert.EqualValues(t, 0, contextLengthOf(lengths, "unknown"))

	costs := map[string]float64{"a800": 12.5}
	cost, ok := cardHourlyCostOf(costs, "a800")
	require.True(t, ok)
	assert.InDelta(t, 12.5, cost, 0.001)
	cost, ok = cardHourlyCostOf(costs, "A800")
	require.True(t, ok)
	assert.InDelta(t, 12.5, cost, 0.001)
	_, ok = cardHourlyCostOf(costs, "H800")
	assert.False(t, ok)
}
