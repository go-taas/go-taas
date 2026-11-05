package billing

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
)

// seedAccrualLine writes an uncharged usage line carrying `micros` of
// in-flight spend, completed now (inside the account's current cycle).
func seedAccrualLine(t *testing.T, svc *Service, requestID, orgID, ownerID, projectID string, micros int64) *UsageLine {
	t.Helper()
	line := &UsageLine{
		ID:              uuid.NewString(),
		RequestID:       requestID,
		OrganizationID:  orgID,
		APIKeyID:        "key-" + requestID,
		ModelID:         "model-a",
		AcceleratorType: "default",
		CompletedAt:     time.Now().UTC(),
		AccrualMicros:   micros,
	}
	if ownerID != "" {
		line.OwnerUserID = &ownerID
	}
	if projectID != "" {
		line.ProjectID = &projectID
	}
	require.NoError(t, svc.repo.DB(context.Background()).Create(line).Error)
	return line
}

// A prepaid account is gated on balance minus what the current hour has
// already spent but not yet settled: without that, a drained balance keeps
// serving until the next settlement pass.
func TestCheckFundsCountsInFlightAccrual(t *testing.T) {
	svc := newBillingTestService(t)
	ctx := context.Background()
	createTestAccount(t, svc, "org-pre", AccountModePrepaid, 100, 0)

	// Nothing in flight: the opening balance is spendable.
	resp, err := svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{OrganizationId: "org-pre"})
	require.NoError(t, err)
	assert.True(t, resp.GetAllowed())

	// 60 of the 100 cents are already spent in the current, unsettled hour.
	seedAccrualLine(t, svc, "req-1", "org-pre", "", "", CentsToMicros(60))
	resp, err = svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{OrganizationId: "org-pre"})
	require.NoError(t, err)
	assert.True(t, resp.GetAllowed(), "40 cents remain spendable")

	// Another 40 cents in flight drains the balance exactly: blocked.
	seedAccrualLine(t, svc, "req-2", "org-pre", "", "", CentsToMicros(40))
	resp, err = svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{OrganizationId: "org-pre"})
	require.NoError(t, err)
	assert.False(t, resp.GetAllowed())
	assert.Equal(t, "insufficient_funds", resp.GetReason())
}

// Charging an hour retires its accrual in the same transaction, so the gate
// never subtracts the same money twice.
func TestChargeGroupRetiresAccrual(t *testing.T) {
	svc := newBillingTestService(t)
	ctx := context.Background()

	period := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	require.NoError(t, svc.repo.DB(ctx).Create(&PriceEntry{
		ModelID: "model-a", AcceleratorType: "default",
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Currency: "USD", EffectiveFrom: period.Unix() - 60, TiersJSON: []byte("[]"),
	}).Error)

	line := seedLine(t, svc.repo.DB(ctx), "req-charge", "org-1", "key-1", "model-a", "default", period, 1000, 1000)
	require.NoError(t, svc.repo.DB(ctx).Model(&UsageLine{}).
		Where("id = ?", line.ID).Update("accrual_micros", 5000).Error)

	require.NoError(t, svc.PriceOnce(ctx, "key-1", period.Unix()))

	var stored UsageLine
	require.NoError(t, svc.repo.DB(ctx).Where("id = ?", line.ID).First(&stored).Error)
	assert.NotNil(t, stored.ChargedChargeID)
	assert.Zero(t, stored.AccrualMicros, "a charged line owns its amount via the charge record")

	// The gate no longer sees the retired accrual.
	micros, err := svc.repo.Accounts().AccrualMicros(ctx, "org-1", 0, "", "")
	require.NoError(t, err)
	assert.Zero(t, micros)
}

// Ingestion prices the line immediately (flat rate, micro-unit precision) so
// the gate has a number before charging runs.
func TestIngestUsageLineAccruesFromPrice(t *testing.T) {
	svc := newBillingTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, svc.repo.DB(ctx).Create(&PriceEntry{
		ModelID: "model-a", AcceleratorType: "A100",
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Currency: "USD", EffectiveFrom: now.Unix() - 60, TiersJSON: []byte("[]"),
	}).Error)

	_, err := svc.handleMeteringEvent(ctx, &meteringEvent{
		RequestID:       "req-accrual",
		OrganizationID:  "org-1",
		APIKeyID:        "key-1",
		ModelID:         "model-a",
		AcceleratorType: "A100",
		Usage: tokenUsage{
			PromptTokens:     1000,
			CompletionTokens: 500,
		},
		CompletedAt: now.Unix(),
	})
	require.NoError(t, err)

	var stored UsageLine
	require.NoError(t, svc.repo.DB(ctx).Where("request_id = ?", "req-accrual").First(&stored).Error)
	// 1000 * 1/M + 500 * 2/M = 0.002 → 2000 micros.
	assert.Equal(t, int64(2000), stored.AccrualMicros)
}

// The in-flight estimate is an upper bound: with volume tiers it uses the
// highest rate rather than the tier the eventual charge will pick.
func TestEstimateAccrualMicrosUsesHighestTier(t *testing.T) {
	entry := &PriceEntry{
		InputPricePerMillion: 1, OutputPricePerMillion: 1,
		TiersJSON: []byte(`[{"up_to_tokens":1000000,"input_price_per_million":5,"output_price_per_million":5}]`),
	}
	// 1M prompt tokens at the tier's 5/M = 5 units → 5,000,000 micros.
	assert.Equal(t, int64(5_000_000), EstimateAccrualMicros(entry, 1_000_000, 0, 0))
	assert.Zero(t, EstimateAccrualMicros(nil, 1_000_000, 0, 0),
		"an unpriced request accrues nothing")
}

// A scoped cap counts the same scope's in-flight spend, not only settled
// charges, so a per-user ceiling is enforced within the current hour.
func TestCheckFundsScopedAccrual(t *testing.T) {
	svc := newBillingTestService(t)
	ctx := context.Background()
	createTestAccount(t, svc, "org-a", AccountModePrepaid, 100000, 0)
	svc.SetSpendLimitResolver(fakeSpendLimits{userLimit: 100})

	seedAccrualLine(t, svc, "req-u1", "org-a", "user-1", "", CentsToMicros(100))

	resp, err := svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{
		OrganizationId: "org-a",
		OwnerUserId:    "user-1",
	})
	require.NoError(t, err)
	assert.False(t, resp.GetAllowed())
	assert.Equal(t, "insufficient_funds", resp.GetReason())

	// A different user is unaffected by user-1's in-flight spend.
	resp, err = svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{
		OrganizationId: "org-a",
		OwnerUserId:    "user-2",
	})
	require.NoError(t, err)
	assert.True(t, resp.GetAllowed())
}

// A charge path that cannot converge must not hold a tenant's money forever:
// a stale accrual is released, while the line stays uncharged so it is still
// billed once pricing recovers.
func TestReleaseStaleAccrual(t *testing.T) {
	svc := newBillingTestService(t)
	ctx := context.Background()
	old := &UsageLine{
		ID: uuid.NewString(), RequestID: "req-old", OrganizationID: "org-1", APIKeyID: "key-1",
		ModelID: "model-a", AcceleratorType: "default", AccrualMicros: 7500,
		CompletedAt: time.Now().UTC().Add(-3 * time.Hour),
	}
	fresh := &UsageLine{
		ID: uuid.NewString(), RequestID: "req-fresh", OrganizationID: "org-1", APIKeyID: "key-1",
		ModelID: "model-a", AcceleratorType: "default", AccrualMicros: 2500,
		CompletedAt: time.Now().UTC().Add(-5 * time.Minute),
	}
	require.NoError(t, svc.repo.DB(ctx).Create(old).Error)
	require.NoError(t, svc.repo.DB(ctx).Create(fresh).Error)

	rows, micros, err := svc.repo.ReleaseStaleAccrual(ctx, time.Now().UTC().Add(-staleAccrualAfter))
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows)
	assert.Equal(t, int64(7500), micros)

	var storedOld, storedFresh UsageLine
	require.NoError(t, svc.repo.DB(ctx).Where("id = ?", old.ID).First(&storedOld).Error)
	require.NoError(t, svc.repo.DB(ctx).Where("id = ?", fresh.ID).First(&storedFresh).Error)
	assert.Zero(t, storedOld.AccrualMicros, "the stuck accrual is released")
	assert.Nil(t, storedOld.ChargedChargeID, "the line is still billed once charging converges")
	assert.Equal(t, int64(2500), storedFresh.AccrualMicros, "a fresh accrual is untouched")

	// Releasing twice is a no-op: nothing is left to release.
	rows, micros, err = svc.repo.ReleaseStaleAccrual(ctx, time.Now().UTC().Add(-staleAccrualAfter))
	require.NoError(t, err)
	assert.Zero(t, rows)
	assert.Zero(t, micros)
}

// A reconciliation pass runs the release as its first step, so a tenant whose
// charge path is stuck regains the missing spendable balance.
func TestReconcileReleasesStaleAccrual(t *testing.T) {
	svc := newBillingTestService(t)
	ctx := context.Background()

	stale := &UsageLine{
		ID: uuid.NewString(), RequestID: "req-stale", OrganizationID: "org-1", APIKeyID: "key-1",
		ModelID: "model-a", AcceleratorType: "default", AccrualMicros: 7500,
		CompletedAt: time.Now().UTC().Add(-3 * time.Hour),
	}
	require.NoError(t, svc.repo.DB(ctx).Create(stale).Error)

	runner := NewReconciliationRunner(svc.repo, svc, time.Minute, 5*time.Minute, 1)
	require.NoError(t, runner.ReconcileOnce(ctx))

	var stored UsageLine
	require.NoError(t, svc.repo.DB(ctx).Where("id = ?", stale.ID).First(&stored).Error)
	assert.Zero(t, stored.AccrualMicros)

	micros, err := svc.repo.Accounts().AccrualMicros(ctx, "org-1", 0, "", "")
	require.NoError(t, err)
	assert.Zero(t, micros, "the gate stops subtracting released money")
}

// Fresh in-flight usage is untouched: releasing it early would under-count
// spend the tenant has really made.
func TestReconcileKeepsFreshAccrual(t *testing.T) {
	svc := newBillingTestService(t)
	ctx := context.Background()

	// Inside the grace window: the hour is not chargeable yet, so nothing has
	// retired this accrual.
	fresh := seedLine(t, svc.repo.DB(ctx), "req-fresh", "org-1", "key-1", "model-unpriced", "default",
		time.Now().UTC().Add(-1*time.Minute), 100, 100)
	require.NoError(t, svc.repo.DB(ctx).Model(&UsageLine{}).
		Where("id = ?", fresh.ID).Update("accrual_micros", 7500).Error)

	runner := NewReconciliationRunner(svc.repo, svc, time.Minute, 5*time.Minute, 1)
	require.NoError(t, runner.ReconcileOnce(ctx))

	var stored UsageLine
	require.NoError(t, svc.repo.DB(ctx).Where("id = ?", fresh.ID).First(&stored).Error)
	assert.Equal(t, int64(7500), stored.AccrualMicros)
}
