package metering

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newDashboardTestDB creates an in-memory sqlite DB with the charge
// records table (the read-only projection) plus the metering tables.
func newDashboardTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Voucher{}, &UsageRecord{}, &chargeRecordRow{}, &usageLineRow{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

// mustInsertCharge inserts a charge record row directly.
func mustInsertCharge(t *testing.T, db *gorm.DB, orgID, keyID, modelID, card string, periodStart int64, amount float64, priced bool) {
	t.Helper()
	row := chargeRecordRow{
		OrganizationID:   orgID,
		APIKeyID:         keyID,
		ModelID:          modelID,
		AcceleratorType:  card,
		PeriodStart:      periodStart,
		PromptTokens:     1000,
		CompletionTokens: 500,
		CachedTokens:     100,
		ReasoningTokens:  50,
		RequestCount:     3,
		Amount:           amount,
		Priced:           priced,
	}
	require.NoError(t, db.Create(&row).Error)
}

// AC1: the dashboard aggregate sums charge records by day x group with
// cost cents rounding and the bool_and priced flag.
func TestDashboardAggregateByAPIKey(t *testing.T) {
	db := newDashboardTestDB(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	h1 := day.Unix()
	h2 := day.Add(time.Hour).Unix()

	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", h1, 1.25, true)
	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", h2, 0.75, true)
	mustInsertCharge(t, db, "org-1", "key-2", "model-b", "default", h1, 2.00, false)

	repo := NewDashboardRepository(db)
	rows, err := repo.DashboardAggregate(ctx, "org-1", day.Unix(), day.Add(24*time.Hour).Unix(), GroupByAPIKey)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	byKey := map[string]DashboardAggregateRow{}
	for _, r := range rows {
		byKey[r.GroupKey] = r
	}
	// key-1: 1.25 + 0.75 = 2.00 -> 200 cents, priced true.
	k1 := byKey["key-1"]
	assert.Equal(t, int64(200), k1.CostCents)
	assert.Equal(t, int64(2000), k1.PromptTokens)
	assert.Equal(t, int64(6), k1.RequestCount)
	assert.True(t, k1.Priced)
	// key-2: 2.00 -> 200 cents, priced false.
	k2 := byKey["key-2"]
	assert.Equal(t, int64(200), k2.CostCents)
	assert.False(t, k2.Priced)
}

// AC2: group-by model regroups the same charge records by model_id.
func TestDashboardAggregateByModel(t *testing.T) {
	db := newDashboardTestDB(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", day.Unix(), 1.00, true)
	mustInsertCharge(t, db, "org-1", "key-2", "model-a", "A800", day.Unix(), 1.00, true)
	mustInsertCharge(t, db, "org-1", "key-3", "model-b", "default", day.Unix(), 3.00, true)

	repo := NewDashboardRepository(db)
	rows, err := repo.DashboardAggregate(ctx, "org-1", day.Unix(), day.Add(24*time.Hour).Unix(), GroupByModel)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byModel := map[string]DashboardAggregateRow{}
	for _, r := range rows {
		byModel[r.GroupKey] = r
	}
	assert.Equal(t, int64(200), byModel["model-a"].CostCents)
	assert.Equal(t, int64(300), byModel["model-b"].CostCents)
}

// AC3: org scoping — a second org's charge records never leak in.
func TestDashboardAggregateOrgScoping(t *testing.T) {
	db := newDashboardTestDB(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", day.Unix(), 1.00, true)
	mustInsertCharge(t, db, "org-2", "key-9", "model-z", "default", day.Unix(), 99.00, true)

	repo := NewDashboardRepository(db)
	rows, err := repo.DashboardAggregate(ctx, "org-1", day.Unix(), day.Add(24*time.Hour).Unix(), GroupByAPIKey)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "key-1", rows[0].GroupKey)
	assert.Equal(t, int64(100), rows[0].CostCents)
}

// AC4: the charge watermark is the max period_start of the org's charge
// records; 0 when none exist.
func TestChargeWatermark(t *testing.T) {
	db := newDashboardTestDB(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	// No records yet -> 0.
	repo := NewDashboardRepository(db)
	wm, err := repo.ChargeWatermark(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), wm)

	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", day.Unix(), 1.00, true)
	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", day.Add(2*time.Hour).Unix(), 1.00, true)
	wm, err = repo.ChargeWatermark(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, day.Add(2*time.Hour).Unix(), wm)
}

// mustInsertUsageLine inserts one raw usage line.
func mustInsertUsageLine(t *testing.T, db *gorm.DB, orgID, keyID string, completedAt time.Time, prompt, completion int64) {
	t.Helper()
	require.NoError(t, db.Create(&usageLineRow{
		OrganizationID: orgID, APIKeyID: keyID, ModelID: "model-a", AcceleratorType: "A800",
		PromptTokens: prompt, CompletionTokens: completion, CompletedAt: completedAt,
	}).Error)
}

// TestDashboardAggregateFallsBackToRawUsage covers the reported bug: an
// installation with no billing account (so no charge records) showed zero
// usage even though every call wrote a usage line.
func TestDashboardAggregateFallsBackToRawUsage(t *testing.T) {
	db := newDashboardTestDB(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	mustInsertUsageLine(t, db, "org-1", "key-1", day.Add(2*time.Hour), 21, 619)
	mustInsertUsageLine(t, db, "org-1", "key-1", day.Add(3*time.Hour), 10, 90)

	repo := NewDashboardRepository(db)
	rows, err := repo.DashboardAggregate(ctx, "org-1", day.Unix(), day.Add(24*time.Hour).Unix(), GroupByAPIKey)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "key-1", rows[0].GroupKey)
	assert.EqualValues(t, 31, rows[0].PromptTokens)
	assert.EqualValues(t, 709, rows[0].CompletionTokens)
	assert.EqualValues(t, 2, rows[0].RequestCount)
	assert.EqualValues(t, 0, rows[0].CostCents, "unpriced usage costs nothing yet")
	assert.False(t, rows[0].Priced)

	// The watermark follows the raw lines when nothing is priced.
	watermark, err := repo.ChargeWatermark(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, day.Add(3*time.Hour).Unix(), watermark)
}

// TestDashboardAggregateMergesChargeAndRawUsage covers the priced case: the
// cost comes from the charge record, the tokens from the raw lines, and a
// charge without raw lines (retention-pruned usage) still shows.
func TestDashboardAggregateMergesChargeAndRawUsage(t *testing.T) {
	db := newDashboardTestDB(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	h1 := day.Unix()

	mustInsertUsageLine(t, db, "org-1", "key-1", day.Add(time.Hour), 21, 619)
	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", h1, 1.50, true)
	// A pruned hour: the charge survives, the raw line does not.
	mustInsertCharge(t, db, "org-1", "key-2", "model-b", "A800", h1, 2.00, true)

	repo := NewDashboardRepository(db)
	rows, err := repo.DashboardAggregate(ctx, "org-1", day.Unix(), day.Add(24*time.Hour).Unix(), GroupByAPIKey)
	require.NoError(t, err)
	require.Len(t, rows, 2)

	byKey := map[string]DashboardAggregateRow{}
	for _, r := range rows {
		byKey[r.GroupKey] = r
	}
	assert.EqualValues(t, 150, byKey["key-1"].CostCents)
	assert.True(t, byKey["key-1"].Priced)
	assert.EqualValues(t, 21, byKey["key-1"].PromptTokens)
	assert.EqualValues(t, 200, byKey["key-2"].CostCents, "a charge without raw usage still counts")
	assert.EqualValues(t, 1000, byKey["key-2"].PromptTokens, "and its own token count stands in")
}
