package billing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
	"github.com/go-taas/go-taas/pkg/config"
)

func newBillingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&PriceEntry{}, &UsageLine{}, &ChargeRecord{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	return db
}

func newBillingTestService(t *testing.T) *Service {
	t.Helper()
	// chargeGroup reads the billing currency from the process-wide
	// config; install a minimal one for the test.
	cfg := &config.Configuration{}
	cfg.Billing.Currency = "USD"
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(&config.Configuration{}) })
	return NewForFVT(newBillingTestDB(t), nil)
}

func seedLine(t *testing.T, db *gorm.DB, requestID, orgID, keyID, modelID, card string, completedAt time.Time, prompt, completion int64) *UsageLine {
	t.Helper()
	line := &UsageLine{
		RequestID:       requestID,
		OrganizationID:  orgID,
		APIKeyID:        keyID,
		ModelID:         modelID,
		AcceleratorType: card,
		PromptTokens:    prompt,
		CompletionTokens: completion,
		CompletedAt:     completedAt,
	}
	stored, err := NewRepository(db).IngestUsageLine(context.Background(), line)
	require.NoError(t, err)
	return stored
}

// AC1: UpsertPrice stores a version; a repeated save with the same
// (model, card, effective_from) updates in place and returns the same
// price_id.
func TestRepositoryUpsertPriceInPlace(t *testing.T) {
	db := newBillingTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	first, err := repo.UpsertPrice(ctx, &PriceEntry{
		ModelID: "model-a", AcceleratorType: "A800",
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Currency: "USD", EffectiveFrom: 1000, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, first.ID)

	second, err := repo.UpsertPrice(ctx, &PriceEntry{
		ModelID: "model-a", AcceleratorType: "A800",
		InputPricePerMillion: 3, OutputPricePerMillion: 4,
		Currency: "USD", EffectiveFrom: 1000, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "same cell version keeps the price_id")
	assert.Equal(t, 3.0, second.InputPricePerMillion, "rates updated in place")

	var count int64
	require.NoError(t, db.Model(&PriceEntry{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "no duplicate row")
}

// AC3: ApplicablePrice selects the greatest effective_from <= at.
func TestRepositoryApplicablePriceVersionSelection(t *testing.T) {
	db := newBillingTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	for _, ef := range []int64{1000, 2000, 3000} {
		_, err := repo.UpsertPrice(ctx, &PriceEntry{
			ModelID: "model-a", AcceleratorType: "A800",
			InputPricePerMillion: float64(ef), OutputPricePerMillion: 1,
			Currency: "USD", EffectiveFrom: ef, TiersJSON: []byte("[]"),
		})
		require.NoError(t, err)
	}

	// Before any version: no price.
	none, err := repo.ApplicablePrice(ctx, "model-a", "A800", 999)
	require.NoError(t, err)
	assert.Nil(t, none)

	// At 2500: the 2000 version applies.
	got, err := repo.ApplicablePrice(ctx, "model-a", "A800", 2500)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(2000), got.EffectiveFrom)

	// At 3000: the 3000 version applies.
	got, err = repo.ApplicablePrice(ctx, "model-a", "A800", 3000)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(3000), got.EffectiveFrom)

	// A different card misses.
	none, err = repo.ApplicablePrice(ctx, "model-a", "BI-V150", 2500)
	require.NoError(t, err)
	assert.Nil(t, none)
}

// AC4: IngestUsageLine is idempotent on request_id.
func TestRepositoryIngestUsageLineIdempotent(t *testing.T) {
	db := newBillingTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	first, err := repo.IngestUsageLine(ctx, &UsageLine{
		RequestID: "req-1", OrganizationID: "org-1", APIKeyID: "key-1",
		ModelID: "model-a", AcceleratorType: "A800",
		PromptTokens: 10, CompletedAt: time.Unix(1000, 0),
	})
	require.NoError(t, err)

	second, err := repo.IngestUsageLine(ctx, &UsageLine{
		RequestID: "req-1", OrganizationID: "org-1", APIKeyID: "key-1",
		ModelID: "model-a", AcceleratorType: "A800",
		PromptTokens: 10, CompletedAt: time.Unix(1000, 0),
	})
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID, "duplicate delivery converges")

	var count int64
	require.NoError(t, db.Model(&UsageLine{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// UnchargedGroups groups uncharged lines by (model, card) with summed
// tokens.
func TestRepositoryUnchargedGroups(t *testing.T) {
	db := newBillingTestDB(t)
	ctx := context.Background()
	hour := time.Unix(1761234400/3600*3600, 0).UTC()

	seedLine(t, db, "r1", "org-1", "key-1", "model-a", "A800", hour.Add(10*time.Minute), 100, 40)
	seedLine(t, db, "r2", "org-1", "key-1", "model-a", "A800", hour.Add(20*time.Minute), 200, 60)
	seedLine(t, db, "r3", "org-1", "key-1", "model-b", "A800", hour.Add(30*time.Minute), 50, 20)
	// A charged line is excluded.
	charged := seedLine(t, db, "r4", "org-1", "key-1", "model-a", "A800", hour.Add(40*time.Minute), 7, 3)
	require.NoError(t, db.Model(&UsageLine{}).Where("id = ?", charged.ID).
		Update("charged_charge_id", "charge-x").Error)
	// Another key's line is excluded.
	seedLine(t, db, "r5", "org-1", "key-2", "model-a", "A800", hour.Add(15*time.Minute), 999, 999)

	groups, err := NewRepository(db).UnchargedGroups(ctx, "key-1", hour.Unix(), hour.Unix()+3600)
	require.NoError(t, err)
	require.Len(t, groups, 2)

	byModel := map[string]UsageGroup{}
	for _, g := range groups {
		byModel[g.ModelID] = g
	}
	ma := byModel["model-a"]
	assert.Equal(t, int64(300), ma.PromptTokens)
	assert.Equal(t, int64(100), ma.CompletionTokens)
	assert.Equal(t, int64(2), ma.RequestCount)
	assert.Equal(t, "A800", ma.AcceleratorType)
	mb := byModel["model-b"]
	assert.Equal(t, int64(1), mb.RequestCount)
}

// AC10: ChargeGroup is idempotent — a re-run converges on the existing
// record and marks no lines twice.
func TestRepositoryChargeGroupIdempotent(t *testing.T) {
	db := newBillingTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()
	hour := time.Unix(1761234400/3600*3600, 0).UTC()

	seedLine(t, db, "r1", "org-1", "key-1", "model-a", "A800", hour.Add(10*time.Minute), 100, 40)

	group := UsageGroup{
		OrganizationID: "org-1", APIKeyID: "key-1", ModelID: "model-a",
		AcceleratorType: "A800", PeriodStart: hour.Unix(), PeriodEnd: hour.Unix() + 3600,
		PromptTokens: 100, CompletionTokens: 40, RequestCount: 1,
	}
	record := &ChargeRecord{
		ID: "charge-1", OrganizationID: "org-1", APIKeyID: "key-1",
		ModelID: "model-a", AcceleratorType: "A800",
		PeriodStart: hour.Unix(), PeriodEnd: hour.Unix() + 3600,
		PromptTokens: 100, CompletionTokens: 40, RequestCount: 1,
		Amount: 0.5, Currency: "USD", Priced: true,
	}
	require.NoError(t, repo.ChargeGroup(ctx, group, record))

	// A re-run with a fresh record converges on the existing row.
	dup := &ChargeRecord{ID: "charge-2", OrganizationID: "org-1", APIKeyID: "key-1",
		ModelID: "model-a", AcceleratorType: "A800",
		PeriodStart: hour.Unix(), PeriodEnd: hour.Unix() + 3600}
	require.NoError(t, repo.ChargeGroup(ctx, group, dup))

	var count int64
	require.NoError(t, db.Model(&ChargeRecord{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "no duplicate charge record")

	var line UsageLine
	require.NoError(t, db.Where("request_id = ?", "r1").First(&line).Error)
	assert.Equal(t, "charge-1", *line.ChargedChargeID)
}

// MonthToDateTokens scopes to the month and excludes the current hour.
func TestRepositoryMonthToDateTokens(t *testing.T) {
	db := newBillingTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	monthStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	h1 := monthStart.Add(48 * time.Hour)
	h2 := monthStart.Add(72 * time.Hour)

	for i, h := range []time.Time{h1, h2} {
		require.NoError(t, db.Create(&ChargeRecord{
			ID: fmt.Sprintf("c-%d", i), OrganizationID: "org-1", APIKeyID: "key-1",
			ModelID: "model-a", AcceleratorType: "A800",
			PeriodStart: h.Unix(), PeriodEnd: h.Add(time.Hour).Unix(),
			PromptTokens: 100, CompletionTokens: 50, RequestCount: 1,
			Amount: 1, Currency: "USD", Priced: true,
		}).Error)
	}

	// Before h2: only h1's tokens count.
	total, err := repo.MonthToDateTokens(ctx, "org-1", "model-a", "A800", monthStart.Unix(), h2.Unix())
	require.NoError(t, err)
	assert.Equal(t, int64(150), total)

	// Before the end of the month: both hours count.
	total, err = repo.MonthToDateTokens(ctx, "org-1", "model-a", "A800", monthStart.Unix(), monthStart.AddDate(0, 1, 0).Unix())
	require.NoError(t, err)
	assert.Equal(t, int64(300), total)

	// Another org sees nothing.
	total, err = repo.MonthToDateTokens(ctx, "org-2", "model-a", "A800", monthStart.Unix(), monthStart.AddDate(0, 1, 0).Unix())
	require.NoError(t, err)
	assert.Equal(t, int64(0), total)
}

// AC12: BillSummaries buckets by month, newest first, with priced
// counts; bill ids are deterministic.
func TestRepositoryBillSummaries(t *testing.T) {
	db := newBillingTestDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	sep := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

	records := []*ChargeRecord{
		{ID: "c1", OrganizationID: "org-1", APIKeyID: "k", ModelID: "m", AcceleratorType: "A800",
			PeriodStart: sep.Unix(), PeriodEnd: sep.Add(time.Hour).Unix(),
			Amount: 1.5, Currency: "USD", Priced: true},
		{ID: "c2", OrganizationID: "org-1", APIKeyID: "k", ModelID: "m", AcceleratorType: "A800",
			PeriodStart: sep.Add(24 * time.Hour).Unix(), PeriodEnd: sep.Add(25 * time.Hour).Unix(),
			Amount: 2.5, Currency: "USD", Priced: false},
		{ID: "c3", OrganizationID: "org-1", APIKeyID: "k", ModelID: "m", AcceleratorType: "A800",
			PeriodStart: oct.Unix(), PeriodEnd: oct.Add(time.Hour).Unix(),
			Amount: 4.0, Currency: "USD", Priced: true},
	}
	for _, r := range records {
		require.NoError(t, db.Create(r).Error)
	}

	rows, total, err := repo.BillSummaries(ctx, "org-1",
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix(),
		time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix(), 0, 20)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, rows, 2)

	// Newest month first.
	assert.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix(), rows[0].MonthStart)
	assert.Equal(t, 4.0, rows[0].Amount)
	assert.Equal(t, int64(1), rows[0].ChargeCount)
	assert.Equal(t, int64(0), rows[0].UnpricedCount)

	assert.Equal(t, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Unix(), rows[1].MonthStart)
	assert.Equal(t, 4.0, rows[1].Amount)
	assert.Equal(t, int64(2), rows[1].ChargeCount)
	assert.Equal(t, int64(1), rows[1].UnpricedCount)

	// bill_id determinism.
	assert.Equal(t, "org-1-202610", billIDOf("org-1", time.Unix(rows[0].MonthStart, 0).UTC()))
}

// UnchargedHourBuckets returns distinct (key, hour) buckets of
// uncharged lines.
func TestRepositoryUnchargedHourBuckets(t *testing.T) {
	db := newBillingTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	hour1 := now.Add(-3 * time.Hour).Truncate(time.Hour)
	hour2 := now.Add(-2 * time.Hour).Truncate(time.Hour)

	seedLine(t, db, "r1", "org-1", "key-1", "m", "A800", hour1.Add(10*time.Minute), 1, 1)
	seedLine(t, db, "r2", "org-1", "key-1", "m", "A800", hour1.Add(20*time.Minute), 1, 1)
	seedLine(t, db, "r3", "org-1", "key-1", "m", "A800", hour2.Add(10*time.Minute), 1, 1)
	seedLine(t, db, "r4", "org-1", "key-2", "m", "A800", hour1.Add(10*time.Minute), 1, 1)

	buckets, err := NewRepository(db).UnchargedHourBuckets(ctx, now)
	require.NoError(t, err)
	assert.Len(t, buckets, 3, "key-1 hour1, key-1 hour2, key-2 hour1")
}

// AC7: the D2 amount formula is exact to the cent.
func TestComputeAmount(t *testing.T) {
	entry := &PriceEntry{
		InputPricePerMillion:  3.5,
		OutputPricePerMillion: 7.25,
		CachedPricePerMillion: 0.5,
	}
	group := &UsageGroup{
		PromptTokens:     1_500_000,
		CompletionTokens: 400_000,
		CachedTokens:     200_000,
	}
	// 1.5M * 3.5 / 1M = 5.25; 0.4M * 7.25 / 1M = 2.9; 0.2M * 0.5 / 1M = 0.1
	// → 8.25
	assert.Equal(t, 8.25, computeAmount(group, entry, 0, 0))

	// Tier rates override the entry rates (input/output only).
	assert.Equal(t, 1.5+2.9+0.1, computeAmount(group, entry, 1.0, 7.25))

	// Rounding to 2 decimals.
	group2 := &UsageGroup{PromptTokens: 123_457, CompletionTokens: 0, CachedTokens: 0}
	amount := computeAmount(group2, entry, 0, 0)
	assert.Equal(t, 0.43, amount)
}

// AC8: tier selection by month-to-date volume boundaries.
func TestSelectTier(t *testing.T) {
	tiers := []PriceTier{
		{UpToTokens: 1_000_000, InputPricePerMillion: 4, OutputPricePerMillion: 8},
		{UpToTokens: 5_000_000, InputPricePerMillion: 3, OutputPricePerMillion: 6},
		{UpToTokens: 0, InputPricePerMillion: 2, OutputPricePerMillion: 4},
	}

	// Flat: no tiers.
	idx, in, out := selectTier(nil, 999_999_999)
	assert.Equal(t, -1, idx)
	assert.Equal(t, 0.0, in)
	assert.Equal(t, 0.0, out)

	// First charge: tier 0.
	idx, in, out = selectTier(tiers, 0)
	assert.Equal(t, 0, idx)
	assert.Equal(t, 4.0, in)
	assert.Equal(t, 8.0, out)

	// Just below the first boundary: still tier 0.
	idx, _, _ = selectTier(tiers, 999_999)
	assert.Equal(t, 0, idx)

	// Crossing 1M: tier 1.
	idx, in, out = selectTier(tiers, 1_000_000)
	assert.Equal(t, 1, idx)
	assert.Equal(t, 3.0, in)
	assert.Equal(t, 6.0, out)

	// Crossing 5M: the unbounded last tier.
	idx, in, out = selectTier(tiers, 5_000_000)
	assert.Equal(t, 2, idx)
	assert.Equal(t, 2.0, in)
	assert.Equal(t, 4.0, out)
}

// AC6/AC9: PriceOnce charges per (model, card) group with the D6
// fallback chain; unpriced groups charge 0 with priced=false.
func TestPriceOnceFallbackAndUnpriced(t *testing.T) {
	db := newBillingTestDB(t)
	svc := newBillingTestService(t)
	repo := NewRepository(db)
	ctx := context.Background()
	hour := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)

	// Price only (model-a, default).
	_, err := repo.UpsertPrice(ctx, &PriceEntry{
		ModelID: "model-a", AcceleratorType: "default",
		InputPricePerMillion: 2, OutputPricePerMillion: 4,
		CachedPricePerMillion: 0, Currency: "USD",
		EffectiveFrom: 1, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)

	seedLine(t, db, "r1", "org-1", "key-1", "model-a", "A800", hour.Add(10*time.Minute), 1_000_000, 500_000)
	seedLine(t, db, "r2", "org-1", "key-1", "model-b", "A800", hour.Add(10*time.Minute), 100, 50)

	require.NoError(t, svc.PriceOnce(ctx, "key-1", hour.Unix()))

	var charges []*ChargeRecord
	require.NoError(t, db.Order("model_id").Find(&charges).Error)
	require.Len(t, charges, 2)

	// model-a: default-card fallback applies (2*1 + 4*0.5 = 4).
	ma := charges[0]
	assert.Equal(t, "model-a", ma.ModelID)
	assert.Equal(t, "A800", ma.AcceleratorType, "the line's card is kept")
	assert.True(t, ma.Priced)
	assert.Equal(t, 4.0, ma.Amount)
	require.NotNil(t, ma.PriceID)

	// model-b: unpriced → amount 0, priced false (D8).
	mb := charges[1]
	assert.Equal(t, "model-b", mb.ModelID)
	assert.False(t, mb.Priced)
	assert.Equal(t, 0.0, mb.Amount)
	assert.Nil(t, mb.PriceID)

	// All lines are marked charged.
	var uncharged int64
	require.NoError(t, db.Model(&UsageLine{}).Where("charged_charge_id IS NULL").Count(&uncharged).Error)
	assert.Equal(t, int64(0), uncharged)
}

// AC6/AC10: PriceOnce is idempotent — a second pass charges nothing
// new.
func TestPriceOnceIdempotent(t *testing.T) {
	db := newBillingTestDB(t)
	svc := newBillingTestService(t)
	ctx := context.Background()
	hour := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)

	seedLine(t, db, "r1", "org-1", "key-1", "model-a", "A800", hour.Add(10*time.Minute), 100, 50)

	require.NoError(t, svc.PriceOnce(ctx, "key-1", hour.Unix()))
	require.NoError(t, svc.PriceOnce(ctx, "key-1", hour.Unix()))

	var count int64
	require.NoError(t, db.Model(&ChargeRecord{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// AC8 (end-to-end): tier selection uses the month-to-date volume of
// prior charges.
func TestPriceOnceTierProgression(t *testing.T) {
	db := newBillingTestDB(t)
	svc := newBillingTestService(t)
	repo := NewRepository(db)
	ctx := context.Background()
	hour := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Hour)

	tiers := []PriceTier{
		{UpToTokens: 1_000_000, InputPricePerMillion: 4, OutputPricePerMillion: 8},
		{UpToTokens: 0, InputPricePerMillion: 2, OutputPricePerMillion: 4},
	}
	tiersJSON, err := encodeTiers(tiers)
	require.NoError(t, err)
	_, err = repo.UpsertPrice(ctx, &PriceEntry{
		ModelID: "model-a", AcceleratorType: "A800",
		InputPricePerMillion: 4, OutputPricePerMillion: 8,
		Currency: "USD", EffectiveFrom: 1, TiersJSON: tiersJSON,
	})
	require.NoError(t, err)

	// First hour: 600K tokens → tier 0 (4/1M).
	seedLine(t, db, "r1", "org-1", "key-1", "model-a", "A800", hour.Add(10*time.Minute), 600_000, 0)
	require.NoError(t, svc.PriceOnce(ctx, "key-1", hour.Unix()))

	var first ChargeRecord
	require.NoError(t, db.Where("period_start = ?", hour.Unix()).First(&first).Error)
	assert.Equal(t, 0, first.TierIndex)
	assert.Equal(t, 2.4, first.Amount)

	// Second hour: month-to-date is 600K — still below the 1M
	// boundary, so tier 0 applies again (D3: the tier is selected by
	// the volume BEFORE this charge).
	hour2 := hour.Add(time.Hour)
	seedLine(t, db, "r2", "org-1", "key-1", "model-a", "A800", hour2.Add(10*time.Minute), 600_000, 0)
	require.NoError(t, svc.PriceOnce(ctx, "key-1", hour2.Unix()))

	var second ChargeRecord
	require.NoError(t, db.Where("period_start = ?", hour2.Unix()).First(&second).Error)
	assert.Equal(t, 0, second.TierIndex)
	assert.Equal(t, int64(600_000), second.MonthToDateTokens)
	assert.Equal(t, 2.4, second.Amount)

	// Third hour: month-to-date is now 1.2M — past the boundary, so
	// the unbounded tier 1 applies (2/1M).
	hour3 := hour.Add(2 * time.Hour)
	seedLine(t, db, "r3", "org-1", "key-1", "model-a", "A800", hour3.Add(10*time.Minute), 600_000, 0)
	require.NoError(t, svc.PriceOnce(ctx, "key-1", hour3.Unix()))

	var third ChargeRecord
	require.NoError(t, db.Where("period_start = ?", hour3.Unix()).First(&third).Error)
	assert.Equal(t, 1, third.TierIndex)
	assert.Equal(t, int64(1_200_000), third.MonthToDateTokens)
	assert.Equal(t, 1.2, third.Amount)
}

// AC5: the card resolution chain — event field, service lookup,
// default.
func TestHandleMeteringEventCardResolution(t *testing.T) {
	db := newBillingTestDB(t)
	svc := newBillingTestService(t)
	ctx := context.Background()

	// Event field wins.
	_, err := svc.handleMeteringEvent(ctx, &meteringEvent{
		RequestID: "r1", OrganizationID: "org-1", APIKeyID: "key-1",
		ModelID: "m", AcceleratorType: "A800",
		Usage:    tokenUsage{PromptTokens: 1},
		CompletedAt: time.Now().Add(-time.Hour).Unix(),
	})
	require.NoError(t, err)
	var line UsageLine
	require.NoError(t, db.Where("request_id = ?", "r1").First(&line).Error)
	assert.Equal(t, "A800", line.AcceleratorType)

	// No field, no service: default sentinel.
	_, err = svc.handleMeteringEvent(ctx, &meteringEvent{
		RequestID: "r2", OrganizationID: "org-1", APIKeyID: "key-1",
		ModelID: "m",
		Usage:    tokenUsage{PromptTokens: 1},
		CompletedAt: time.Now().Add(-time.Hour).Unix(),
	})
	require.NoError(t, err)
	// A fresh struct: GORM's First adds the destination's primary key
	// to the WHERE clause when reusing a loaded struct.
	var line2 UsageLine
	require.NoError(t, db.Where("request_id = ?", "r2").First(&line2).Error)
	assert.Equal(t, "default", line2.AcceleratorType)

	// Invalid event: 10401-equivalent, nothing written.
	_, err = svc.handleMeteringEvent(ctx, &meteringEvent{
		RequestID: "", OrganizationID: "org-1", APIKeyID: "key-1",
		ModelID: "m",
		CompletedAt: time.Now().Add(-time.Hour).Unix(),
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringEventInvalid, ae.Code)

	var count int64
	require.NoError(t, db.Model(&UsageLine{}).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}

// AC11: the reconciliation runner prices uncharged hours without
// settlement events, respecting the eligibility boundary.
func TestReconciliationRunnerPricesWithoutEvents(t *testing.T) {
	db := newBillingTestDB(t)
	svc := newBillingTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// An eligible closed hour and the still-open current hour.
	closedHour := now.Add(-3 * time.Hour).Truncate(time.Hour)
	currentHour := now.Truncate(time.Hour)
	seedLine(t, db, "r1", "org-1", "key-1", "model-a", "A800", closedHour.Add(10*time.Minute), 100, 50)
	seedLine(t, db, "r2", "org-1", "key-1", "model-a", "A800", currentHour.Add(1*time.Minute), 100, 50)

	runner := NewReconciliationRunner(NewRepository(db), svc, time.Minute, 0, 1)
	require.NoError(t, runner.ReconcileOnce(ctx))

	var charged int64
	require.NoError(t, db.Model(&UsageLine{}).Where("charged_charge_id IS NOT NULL").Count(&charged).Error)
	assert.Equal(t, int64(1), charged, "only the closed hour is charged")
}

// AC11: the eligibility boundary — an hour closed less than the grace
// period ago stays uncharged.
func TestReconciliationRunnerGraceBoundary(t *testing.T) {
	db := newBillingTestDB(t)
	svc := newBillingTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// The hour closed 2 minutes ago; grace is 5 minutes.
	recentHour := now.Add(-2 * time.Minute).Truncate(time.Hour)
	seedLine(t, db, "r1", "org-1", "key-1", "model-a", "A800", recentHour.Add(30*time.Second), 100, 50)

	runner := NewReconciliationRunner(NewRepository(db), svc, time.Minute, 5*time.Minute, 1)
	require.NoError(t, runner.ReconcileOnce(ctx))

	var charged int64
	require.NoError(t, db.Model(&UsageLine{}).Where("charged_charge_id IS NOT NULL").Count(&charged).Error)
	assert.Equal(t, int64(0), charged, "the hour is not yet eligible")
}
