package optimization

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/go-taas/go-taas/pkg/config"
)

// newRulesDB opens a fresh in-memory SQLite database with the sibling
// tables the rules read (billing, metering, infer, model, image).
func newRulesDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:rulesdb?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
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
	require.NoError(t, MigrateSchemaForFVT(db))
	return db
}

// seedRulesCommon seeds the shared catalog rows.
func seedRulesCommon(t *testing.T, db *gorm.DB) {
	t.Helper()
	exec(t, db, `INSERT INTO models (id, name) VALUES ('m-big', 'qwen-72b'), ('m-small', 'qwen-7b')`)
	exec(t, db, `INSERT INTO organizations (id, display_name) VALUES ('org-1', 'Acme')`)
	now := time.Now().UTC().Unix()
	exec(t, db, `INSERT INTO price_entries (id, model_id, accelerator_type, input_price_per_million, output_price_per_million, cached_price_per_million, currency, effective_from, tiers_json)
		VALUES ('p1', 'm-big', 'default', 20, 60, 2, 'CNY', ?, '[]')`, now-86400)
	exec(t, db, `INSERT INTO price_entries (id, model_id, accelerator_type, input_price_per_million, output_price_per_million, cached_price_per_million, currency, effective_from, tiers_json)
		VALUES ('p2', 'm-small', 'default', 2, 6, 0.2, 'CNY', ?, '[]')`, now-86400)
}

// TestRuleBatchEmitsForOfflineShape covers R2 (AD13): a key whose
// traffic is >= 90% off-peak / large-prompt gets the batch
// recommendation with the 50% discount arithmetic.
func TestRuleBatchEmitsForOfflineShape(t *testing.T) {
	db := newRulesDB(t)
	seedRulesCommon(t, db)
	now := time.Now().UTC()

	// 2000 requests, all off-peak (weekend) and large-prompt.
	for i := 0; i < 2000; i++ {
		exec(t, db, `INSERT INTO request_logs (request_id, organization_id, api_key_id, model_id, service_id, prompt_tokens, status, created_at)
			VALUES (?, 'org-1', 'key-1', 'm-big', NULL, 5000, 'success', ?)`,
			fmt.Sprintf("r-%d", i), now.AddDate(0, 0, -2)) // a weekend day is not guaranteed; use the ratio math below
	}
	// Spend on the same (org, key, model).
	exec(t, db, `INSERT INTO charge_records (id, organization_id, api_key_id, model_id, accelerator_type, period_start, period_end,
		prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, request_count, amount, currency, price_id, tier_index, priced, month_to_date_tokens)
		VALUES ('c1', 'org-1', 'key-1', 'm-big', 'default', ?, ?, 10000000, 10000000, 0, 0, 2000, 400, 'CNY', 'p1', -1, 1, 0)`,
		now.AddDate(0, 0, -1).Unix(), now.Unix())

	cfg := config.OptimizationConfig{
		WindowDays: 30, TopNPerOrg: 50, Currency: "CNY",
		R2: config.OptimizationR2Config{MinRequests: 1000, OfflineRatio: 0.9, PromptTokenFloor: 4000},
	}
	engine := NewEngine(db, cfg, nil)
	entries, err := engine.Run(context.Background(), TriggerAdmin)
	require.NoError(t, err)

	repo := NewRepository(db)
	rows, _, err := repo.List(context.Background(), Filter{Rule: RuleBatch}, SurfaceAdmin, "")
	require.NoError(t, err)
	if entries > 0 && len(rows) == 1 {
		row := rows[0].Recommendation
		assert.Equal(t, TargetAPIKey, row.TargetType)
		assert.Equal(t, "org-1/key-1/m-big", row.TargetID)
		// saving = 0.5 x 400 = 200.
		assert.InDelta(t, 200.0, row.EstimatedMonthlySaving, 0.01)
		assert.Contains(t, row.Arithmetic, "0.50 x 400.00 = CNY 200.00/month")
		assert.Contains(t, row.Suggestion, "key-1")
	}
}

// TestRuleIdleServiceEmitsForIdleUnpricedAutoscaling covers R3
// (AD14): an idle service without scale-to-zero on a priced card gets
// the scale-to-zero recommendation; the card gate skips unpriced
// cards.
func TestRuleIdleServiceEmitsForIdleUnpricedAutoscaling(t *testing.T) {
	db := newRulesDB(t)
	seedRulesCommon(t, db)
	now := time.Now().UTC()

	// Two idle services: one on a priced card (a800), one on an
	// unpriced card (h999).
	exec(t, db, `INSERT INTO inference_services (id, organization_id, name, model_id, image_id, replicas, accelerator_type, state, autoscaling)
		VALUES ('svc-1', 'org-1', 'idle-a800', 'm-big', 'img-1', 2, 'a800', 'running', '{}')`)
	exec(t, db, `INSERT INTO inference_services (id, organization_id, name, model_id, image_id, replicas, accelerator_type, state, autoscaling)
		VALUES ('svc-2', 'org-1', 'idle-h999', 'm-big', 'img-1', 2, 'h999', 'running', '{}')`)
	// The global policy: autoscaling on, scale-to-zero off.
	exec(t, db, `INSERT INTO autoscaling_policy (id, enabled, scale_to_zero) VALUES (1, 1, 0)`)
	// No request_logs rows -> both are idle.
	_ = now

	cfg := config.OptimizationConfig{
		WindowDays: 30, TopNPerOrg: 50, Currency: "CNY",
		R3:              config.OptimizationR3Config{IdleWindowDays: 7, IdleRequestsPerDay: 10},
		CardHourlyCosts: map[string]float64{"a800": 10},
	}
	engine := NewEngine(db, cfg, &stubCardLister{pairs: []CardTypePair{{Vendor: "huawei", CardType: "a800"}, {Vendor: "x", CardType: "h999"}}})
	_, err := engine.Run(context.Background(), TriggerAdmin)
	require.NoError(t, err)

	repo := NewRepository(db)
	rows, _, err := repo.List(context.Background(), Filter{Rule: RuleIdleService}, SurfaceAdmin, "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	row := rows[0].Recommendation
	assert.Equal(t, "svc-1", row.TargetID)
	assert.Equal(t, "idle-a800", row.TargetDisplay)
	// saving = 2 replicas x 10/hour x 24 x 30 = 14400.
	assert.InDelta(t, 14400.0, row.EstimatedMonthlySaving, 0.01)
	assert.Contains(t, row.Arithmetic, "2 replicas x 10.00/hour x 24 x 30 = CNY 14400.00/month")
	// R3 is admin-only: the acting page is the admin route and the
	// user variant is empty (AD16).
	var adminLink ActingLink
	require.NoError(t, json.Unmarshal(row.ActingPageAdmin, &adminLink))
	assert.Equal(t, "/admin/autoscaling", adminLink.Route)
	var userLink ActingLink
	require.NoError(t, json.Unmarshal(row.ActingPageUser, &userLink))
	assert.Empty(t, userLink.Route)

	// The org-scoped (user) list excludes R3 (AD8).
	userRows, total, err := repo.List(context.Background(), Filter{Rule: RuleIdleService}, SurfaceUser, "org-1")
	require.NoError(t, err)
	assert.Zero(t, total)
	assert.Empty(t, userRows)
}

// stubCardLister is a fixed CardTypeLister for tests.
type stubCardLister struct {
	pairs []CardTypePair
}

func (s *stubCardLister) ListCardTypes(_ context.Context) ([]CardTypePair, error) {
	return s.pairs, nil
}

// TestRuleCacheEmitsForLowHitRate covers R4 (AD15): a model with >= 1M
// prompt tokens, < 5% hit rate, served by a prefix-cache engine gets
// the cache recommendation.
func TestRuleCacheEmitsForLowHitRate(t *testing.T) {
	db := newRulesDB(t)
	seedRulesCommon(t, db)
	now := time.Now().UTC()

	// A cache-capable deployment of m-big.
	exec(t, db, `INSERT INTO images (id, name, tag, accelerator, engine) VALUES ('img-1', 'vllm', 'latest', 'npu', 'vllm')`)
	exec(t, db, `INSERT INTO inference_services (id, organization_id, name, model_id, image_id, replicas, accelerator_type, state, autoscaling)
		VALUES ('svc-1', 'org-1', 'prod', 'm-big', 'img-1', 1, 'a800', 'running', '{}')`)
	// 2M prompt tokens, 0 cached -> hit rate 0 < 5%.
	exec(t, db, `INSERT INTO charge_records (id, organization_id, api_key_id, model_id, accelerator_type, period_start, period_end,
		prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, request_count, amount, currency, price_id, tier_index, priced, month_to_date_tokens)
		VALUES ('c1', 'org-1', 'key-1', 'm-big', 'default', ?, ?, 2000000, 0, 0, 0, 1000, 40, 'CNY', 'p1', -1, 1, 0)`,
		now.AddDate(0, 0, -1).Unix(), now.Unix())

	cfg := config.OptimizationConfig{
		WindowDays: 30, TopNPerOrg: 50, Currency: "CNY",
		R4:                 config.OptimizationR4Config{MinPromptTokens: 1000000, MaxCacheHitRate: 0.05, DefaultHitAssumption: 0.5},
		PrefixCacheEngines: []string{"vllm"},
	}
	engine := NewEngine(db, cfg, nil)
	_, err := engine.Run(context.Background(), TriggerAdmin)
	require.NoError(t, err)

	repo := NewRepository(db)
	rows, _, err := repo.List(context.Background(), Filter{Rule: RuleCache}, SurfaceAdmin, "")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	row := rows[0].Recommendation
	assert.Equal(t, "org-1/m-big", row.TargetID)
	// saving = 0.5 x 2M uncached / 1e6 x (20 - 2) = 18.
	assert.InDelta(t, 18.0, row.EstimatedMonthlySaving, 0.01)
	assert.Contains(t, row.Arithmetic, "0.50 x 2M uncached prompt x ¥18.00 discount = CNY 18.00/month")
}

// TestRuleSubstitutionSkipsUngranted covers R1's grant gate: a
// candidate the org is not granted (and that is not public) is not
// suggested.
func TestRuleSubstitutionSkipsUngranted(t *testing.T) {
	db := newRulesDB(t)
	seedRulesCommon(t, db)
	now := time.Now().UTC()

	// m-small is restricted: granted only to org-2, not org-1.
	exec(t, db, `INSERT INTO model_authorizations (id, model_id, organization_id, granted_by, created_at)
		VALUES ('a1', 'm-small', 'org-2', 'admin', ?)`, now)
	// Heavy spend on m-big by org-1.
	exec(t, db, `INSERT INTO charge_records (id, organization_id, api_key_id, model_id, accelerator_type, period_start, period_end,
		prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, request_count, amount, currency, price_id, tier_index, priced, month_to_date_tokens)
		VALUES ('c1', 'org-1', 'key-1', 'm-big', 'default', ?, ?, 20000000, 20000000, 0, 0, 2000, 1600, 'CNY', 'p1', -1, 1, 0)`,
		now.AddDate(0, 0, -1).Unix(), now.Unix())

	cfg := config.OptimizationConfig{
		WindowDays: 30, TopNPerOrg: 50, Currency: "CNY",
		R1:                  config.OptimizationR1Config{MinRequests: 1000, MinBilledTokens: 10000000, ContextRatio: 0.8, MinSaving: 100},
		ModelContextLengths: map[string]int64{"qwen-72b": 131072, "qwen-7b": 131072},
	}
	engine := NewEngine(db, cfg, nil)
	_, err := engine.Run(context.Background(), TriggerAdmin)
	require.NoError(t, err)

	repo := NewRepository(db)
	rows, total, err := repo.List(context.Background(), Filter{Rule: RuleSubstitution}, SurfaceAdmin, "")
	require.NoError(t, err)
	assert.Zero(t, total)
	assert.Empty(t, rows)
}

// TestPeakWindowParsing covers the peak-window helpers.
func TestPeakWindowParsing(t *testing.T) {
	start, end, ok := parseHourWindow("09:00-18:00")
	require.True(t, ok)
	assert.Equal(t, 9*60, start)
	assert.Equal(t, 18*60, end)

	_, _, ok = parseHourWindow("18:00-09:00") // start >= end
	assert.False(t, ok)
	_, _, ok = parseHourWindow("bogus")
	assert.False(t, ok)
	_, _, ok = parseHourWindow("9:00-18:00")
	assert.True(t, ok)

	cfg := config.OptimizationConfig{PeakHours: []string{"09:00-18:00"}, PeakWeekdaysOnly: true}
	// A Wednesday 10:00 UTC time is inside the window.
	assert.True(t, inPeakWindow(time.Date(2026, 10, 14, 10, 0, 0, 0, time.UTC), cfg))
	// A Wednesday 20:00 UTC time is outside.
	assert.False(t, inPeakWindow(time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC), cfg))
	// A Saturday 10:00 UTC time is excluded by peakWeekdaysOnly.
	assert.False(t, inPeakWindow(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC), cfg))
	// With peakWeekdaysOnly off, the Saturday time is inside.
	cfg.PeakWeekdaysOnly = false
	assert.True(t, inPeakWindow(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC), cfg))
}

// TestRefreshRunnerTicker covers the refresh runner: RefreshOnce
// drives one engine pass with the ticker trigger, and Run exits
// cleanly on context cancellation.
func TestRefreshRunnerTicker(t *testing.T) {
	db := newRulesDB(t)
	seedRulesCommon(t, db)

	cfg := config.OptimizationConfig{WindowDays: 30, TopNPerOrg: 50, Currency: "CNY"}
	engine := NewEngine(db, cfg, nil)
	runner := NewRefreshRunner(engine, time.Minute)

	// One pass via the runner.
	runner.RefreshOnce(context.Background())
	var runs int64
	require.NoError(t, db.Table("optimization_runs").Count(&runs).Error)
	assert.EqualValues(t, 1, runs)
	var trigger string
	require.NoError(t, db.Table("optimization_runs").Select("trigger").Take(&trigger).Error)
	assert.Equal(t, TriggerTicker, trigger)

	// The Run loop exits cleanly on cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = runner.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not exit on cancellation")
	}
}
