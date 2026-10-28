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

	meteringv1 "github.com/go-taas/go-taas/proto/taas/metering/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// newDashboardServiceEnv builds a service with the charge_records
// projection migrated so the dashboard RPC can be exercised.
func newDashboardServiceEnv(t *testing.T) *Service {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Voucher{}, &UsageRecord{}, &chargeRecordRow{}, &priceEntryRow{}, &usageLineRow{}))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
	})
	svc := NewForFVT(db, nil)
	svc.SetCostAttributor(NewCostAttributor(db))
	return svc
}

// AC1: GetUsageDashboard returns cards and daily buckets summing the
// charge records.
func TestServiceGetUsageDashboard(t *testing.T) {
	svc := newDashboardServiceEnv(t)
	ctx := withOrg(context.Background(), "org-1")
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	// Seed charge records via the repository's DB handle.
	db := svc.repo.db.DB(context.Background())
	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", day.Unix(), 1.25, true)
	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", day.Add(time.Hour).Unix(), 0.75, true)
	mustInsertCharge(t, db, "org-1", "key-2", "model-b", "default", day.Unix(), 2.00, false)

	resp, err := svc.GetUsageDashboard(ctx, &meteringv1.GetUsageDashboardRequest{
		Since: day.Unix(),
		Until: day.Add(24 * time.Hour).Unix(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetCards())
	// 1.25 + 0.75 + 2.00 = 4.00 -> 400 cents.
	assert.Equal(t, int64(400), resp.GetCards().GetTotalCostCents())
	assert.Equal(t, int64(9), resp.GetCards().GetRequestCount())
	// key-2 is unpriced -> 3 requests unpriced.
	assert.Equal(t, int64(3), resp.GetCards().GetUnpricedRequestCount())
	// One daily bucket for the single day.
	require.Len(t, resp.GetDailyBuckets(), 1)
	assert.Equal(t, day.Unix(), resp.GetDailyBuckets()[0].GetDate())
	require.Len(t, resp.GetDailyBuckets()[0].GetGroups(), 2)
}

// AC2: group-by model regroups the buckets by model_id.
func TestServiceGetUsageDashboardGroupByModel(t *testing.T) {
	svc := newDashboardServiceEnv(t)
	ctx := withOrg(context.Background(), "org-1")
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	db := svc.repo.db.DB(context.Background())
	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", day.Unix(), 1.00, true)
	mustInsertCharge(t, db, "org-1", "key-2", "model-a", "A800", day.Unix(), 1.00, true)
	mustInsertCharge(t, db, "org-1", "key-3", "model-b", "default", day.Unix(), 3.00, true)

	resp, err := svc.GetUsageDashboard(ctx, &meteringv1.GetUsageDashboardRequest{
		Since:   day.Unix(),
		Until:   day.Add(24 * time.Hour).Unix(),
		GroupBy: "model",
	})
	require.NoError(t, err)
	require.Len(t, resp.GetDailyBuckets(), 1)
	groups := resp.GetDailyBuckets()[0].GetGroups()
	require.Len(t, groups, 2)
	byModel := map[string]*meteringv1.DashboardGroup{}
	for _, g := range groups {
		byModel[g.GetGroupKey()] = g
	}
	assert.Equal(t, int64(200), byModel["model-a"].GetCostCents())
	assert.Equal(t, int64(300), byModel["model-b"].GetCostCents())
}

// AC7: an invalid range returns 10404.
func TestServiceGetUsageDashboardRangeInvalid(t *testing.T) {
	svc := newDashboardServiceEnv(t)
	ctx := withOrg(context.Background(), "org-1")
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	// since > until.
	_, err := svc.GetUsageDashboard(ctx, &meteringv1.GetUsageDashboardRequest{
		Since: day.Add(24 * time.Hour).Unix(),
		Until: day.Unix(),
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, ae.Code)

	// range > 92 days.
	_, err = svc.GetUsageDashboard(ctx, &meteringv1.GetUsageDashboardRequest{
		Since: day.Unix(),
		Until: day.Add(93 * 24 * time.Hour).Unix(),
	})
	require.Error(t, err)
	ae, ok = apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeMeteringRangeInvalid, ae.Code)
}

// AC8: the data_through watermark is the max charged hour.
func TestServiceGetUsageDashboardWatermark(t *testing.T) {
	svc := newDashboardServiceEnv(t)
	ctx := withOrg(context.Background(), "org-1")
	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	db := svc.repo.db.DB(context.Background())
	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", day.Unix(), 1.00, true)
	mustInsertCharge(t, db, "org-1", "key-1", "model-a", "A800", day.Add(3*time.Hour).Unix(), 1.00, true)

	resp, err := svc.GetUsageDashboard(ctx, &meteringv1.GetUsageDashboardRequest{
		Since: day.Unix(),
		Until: day.Add(24 * time.Hour).Unix(),
	})
	require.NoError(t, err)
	assert.Equal(t, day.Add(3*time.Hour).Unix(), resp.GetCards().GetDataThrough())
}

// AC4: vouchers carry estimated cost and priced when the attributor is
// wired.
func TestServiceVoucherCostAttribution(t *testing.T) {
	svc := newDashboardServiceEnv(t)
	ctx := withOrg(context.Background(), "org-1")
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

	// Seed a price entry and a voucher.
	db := svc.repo.db.DB(context.Background())
	mustInsertPrice(t, db, "model-a", "default", 0, 3.00, 6.00, 1.00)
	repo := svc.repo
	stored, err := repo.IngestVoucher(ctx, &Voucher{
		RequestID:        "req-cost-1",
		OrganizationID:   "org-1",
		APIKeyID:         "key-1",
		ModelID:          "model-a",
		PromptTokens:     1000,
		CompletionTokens: 500,
		CachedTokens:     100,
		CompletedAt:      at,
	})
	require.NoError(t, err)

	resp, err := svc.GetVoucher(ctx, &meteringv1.GetVoucherRequest{VoucherId: stored.ID})
	require.NoError(t, err)
	assert.True(t, resp.GetVoucher().GetPriced())
	assert.Equal(t, int64(1), resp.GetVoucher().GetEstimatedCostCents())
}
