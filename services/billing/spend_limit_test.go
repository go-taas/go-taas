package billing

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// fakeSpendLimits is the SpendLimitResolver seam for the funds tests.
type fakeSpendLimits struct {
	userLimit    int64
	projectLimit int64
}

func (f fakeSpendLimits) ProjectMonthlySpendLimitCents(context.Context, string) (int64, error) {
	return f.projectLimit, nil
}

func (f fakeSpendLimits) UserMonthlySpendLimitCents(context.Context, string, string) (int64, error) {
	return f.userLimit, nil
}

func TestMinNonZeroLimit(t *testing.T) {
	assert.EqualValues(t, 5, minNonZeroLimit(0, 5))
	assert.EqualValues(t, 5, minNonZeroLimit(5, 0))
	assert.EqualValues(t, 5, minNonZeroLimit(5, 9))
	assert.EqualValues(t, 0, minNonZeroLimit(0, 0))
}

func TestCheckFundsAppliesScopedSpendLimits(t *testing.T) {
	db := newBillingTestDB(t)
	svc := NewForFVT(db, nil)
	ctx := context.Background()
	cycleStart := monthStartOf(time.Now().UTC())
	require.NoError(t, db.Create(&Account{
		ID:                     uuid.NewString(),
		OrganizationID:         "org-a",
		Mode:                   AccountModePostpaid,
		Currency:               "USD",
		CycleStartedAt:         cycleStart,
		MonthlySpendLimitCents: 10000,
	}).Error)

	owner := "u1"
	project := "p1"
	// 30.00 for the owner (3000 cents).
	require.NoError(t, db.Create(&ChargeRecord{
		ID: uuid.NewString(), OrganizationID: "org-a", APIKeyID: "k1", ModelID: "m1",
		AcceleratorType: "default", PeriodStart: cycleStart + 60,
		PeriodEnd: cycleStart + 3660, Amount: 30, Currency: "USD", TierIndex: -1,
		OwnerUserID: &owner, ProjectID: &project, ChargedAt: time.Now().UTC(),
	}).Error)

	svc.SetSpendLimitResolver(fakeSpendLimits{userLimit: 4000, projectLimit: 5000})

	// 3000 spent against a 4000 personal cap: allowed.
	resp, err := svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{
		OrganizationId: "org-a", OwnerUserId: owner, ProjectId: project,
	})
	require.NoError(t, err)
	assert.True(t, resp.GetAllowed())
	assert.EqualValues(t, 4000, resp.GetEffectiveMonthlySpendLimitCents())
	assert.EqualValues(t, 3000, resp.GetOwnerSpentThisCycleCents())
	assert.EqualValues(t, 3000, resp.GetProjectSpentThisCycleCents())

	// Another 15.00 pushes the owner to 4500 >= 4000: blocked.
	require.NoError(t, db.Create(&ChargeRecord{
		ID: uuid.NewString(), OrganizationID: "org-a", APIKeyID: "k2", ModelID: "m1",
		AcceleratorType: "default", PeriodStart: cycleStart + 120,
		PeriodEnd: cycleStart + 3720, Amount: 15, Currency: "USD", TierIndex: -1,
		OwnerUserID: &owner, ProjectID: &project, ChargedAt: time.Now().UTC(),
	}).Error)
	resp, err = svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{
		OrganizationId: "org-a", OwnerUserId: owner,
	})
	require.NoError(t, err)
	assert.False(t, resp.GetAllowed(), "the personal cap blocks")

	// The project total is 4500 against a 5000 cap: still allowed.
	resp, err = svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{
		OrganizationId: "org-a", ProjectId: project,
	})
	require.NoError(t, err)
	assert.True(t, resp.GetAllowed())
	assert.EqualValues(t, 4500, resp.GetProjectSpentThisCycleCents())
}

func TestCheckFundsWithoutAccountIsUngated(t *testing.T) {
	db := newBillingTestDB(t)
	svc := NewForFVT(db, nil)
	resp, err := svc.CheckFunds(context.Background(), &billingv1.CheckFundsRequest{OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.True(t, resp.GetAllowed())
	assert.Empty(t, resp.GetMode())
}

func TestScopedSpentCentsFiltersScope(t *testing.T) {
	db := newBillingTestDB(t)
	repo := NewAccountRepository(db)
	owner := "u1"
	project := "p1"
	now := time.Now().UTC().Unix()
	for _, row := range []*ChargeRecord{
		{ID: uuid.NewString(), OrganizationID: "org-a", APIKeyID: "k1", ModelID: "m", AcceleratorType: "default",
			PeriodStart: now, PeriodEnd: now + 3600, Amount: 10, Currency: "USD", TierIndex: -1,
			OwnerUserID: &owner, ProjectID: &project},
		{ID: uuid.NewString(), OrganizationID: "org-a", APIKeyID: "k2", ModelID: "m", AcceleratorType: "default",
			PeriodStart: now, PeriodEnd: now + 3600, Amount: 5, Currency: "USD", TierIndex: -1},
	} {
		require.NoError(t, db.Create(row).Error)
	}
	cents, err := repo.ScopedSpentCents(context.Background(), "org-a", now-60, owner, "")
	require.NoError(t, err)
	assert.EqualValues(t, 1000, cents)

	cents, err = repo.ScopedSpentCents(context.Background(), "org-a", now-60, "", project)
	require.NoError(t, err)
	assert.EqualValues(t, 1000, cents)

	cents, err = repo.ScopedSpentCents(context.Background(), "org-a", now-60, "", "")
	require.NoError(t, err)
	assert.EqualValues(t, 1500, cents)
}

// TestDeletePriceRemovesOnlyTheVersion covers the admin delete: the row
// disappears, an unknown id answers 10509, and a committed charge keeps
// its amount.
func TestDeletePriceRemovesOnlyTheVersion(t *testing.T) {
	db := newBillingTestDB(t)
	svc := NewForFVT(db, nil)
	repo := NewRepository(db)
	ctx := context.Background()

	stored, err := repo.UpsertPrice(ctx, &PriceEntry{
		ModelID: "m1", AcceleratorType: "default",
		InputPricePerMillion: 1, OutputPricePerMillion: 1,
		Currency: "CNY", EffectiveFrom: 1, TiersJSON: []byte("[]"),
	})
	require.NoError(t, err)

	// A committed charge references the version.
	require.NoError(t, db.Create(&ChargeRecord{
		ID: uuid.NewString(), OrganizationID: "org-a", APIKeyID: "k1", ModelID: "m1",
		AcceleratorType: "default", PeriodStart: 1, PeriodEnd: 3601,
		Amount: 0.5, Currency: "CNY", TierIndex: -1, Priced: true, PriceID: &stored.ID,
	}).Error)

	_, err = svc.DeletePrice(ctx, &billingv1.DeletePriceRequest{PriceId: stored.ID})
	require.NoError(t, err)

	remaining, err := repo.FindPrice(ctx, stored.ID)
	require.NoError(t, err)
	assert.Nil(t, remaining)

	var charge ChargeRecord
	require.NoError(t, db.Where("api_key_id = ?", "k1").First(&charge).Error)
	assert.Equal(t, 0.5, charge.Amount, "a committed charge keeps its amount")

	_, err = svc.DeletePrice(ctx, &billingv1.DeletePriceRequest{PriceId: stored.ID})
	assert.Equal(t, apierrors.CodePriceInvalid, apierrors.CodeOf(err))
}
