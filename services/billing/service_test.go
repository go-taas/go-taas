package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// withOrg returns a context carrying the caller organization metadata
// (the gateway sets it from X-Organization-Id).
func withOrg(orgID string) context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs(organizationMetadataKey, orgID))
}

// AC2: the SetPrice validation matrix — every failure returns 10507
// and nothing is written.
func TestSetPriceValidationMatrix(t *testing.T) {
	svc := newBillingTestService(t)
	db := svc.repo.db.DB(context.Background())

	valid := func() *billingv1.SetPriceRequest {
		return &billingv1.SetPriceRequest{
			ModelId: "model-a", AcceleratorType: "A800",
			InputPricePerMillion: 1, OutputPricePerMillion: 2,
		}
	}

	cases := []struct {
		name string
		mut  func(req *billingv1.SetPriceRequest)
	}{
		{"empty model", func(r *billingv1.SetPriceRequest) { r.ModelId = "" }},
		{"model too long", func(r *billingv1.SetPriceRequest) {
			r.ModelId = string(make([]byte, 129))
		}},
		{"empty card", func(r *billingv1.SetPriceRequest) { r.AcceleratorType = "" }},
		{"card too long", func(r *billingv1.SetPriceRequest) {
			r.AcceleratorType = string(make([]byte, 65))
		}},
		{"negative input rate", func(r *billingv1.SetPriceRequest) { r.InputPricePerMillion = -1 }},
		{"negative output rate", func(r *billingv1.SetPriceRequest) { r.OutputPricePerMillion = -1 }},
		{"negative cached rate", func(r *billingv1.SetPriceRequest) { r.CachedPricePerMillion = -0.1 }},
		{"currency too long", func(r *billingv1.SetPriceRequest) { r.Currency = "TOOLONGCUR" }},
		{"currency mismatch", func(r *billingv1.SetPriceRequest) { r.Currency = "EUR" }},
		{"negative effective_from", func(r *billingv1.SetPriceRequest) { r.EffectiveFrom = -1 }},
		{"last tier bounded", func(r *billingv1.SetPriceRequest) {
			r.Tiers = []*billingv1.PriceTier{{UpToTokens: 100, InputPricePerMillion: 1, OutputPricePerMillion: 1}}
		}},
		{"non-last tier unbounded", func(r *billingv1.SetPriceRequest) {
			r.Tiers = []*billingv1.PriceTier{
				{UpToTokens: 0, InputPricePerMillion: 1, OutputPricePerMillion: 1},
				{UpToTokens: 0, InputPricePerMillion: 1, OutputPricePerMillion: 1},
			}
		}},
		{"non-ascending bounds", func(r *billingv1.SetPriceRequest) {
			r.Tiers = []*billingv1.PriceTier{
				{UpToTokens: 500, InputPricePerMillion: 1, OutputPricePerMillion: 1},
				{UpToTokens: 100, InputPricePerMillion: 1, OutputPricePerMillion: 1},
				{UpToTokens: 0, InputPricePerMillion: 1, OutputPricePerMillion: 1},
			}
		}},
		{"negative tier rate", func(r *billingv1.SetPriceRequest) {
			r.Tiers = []*billingv1.PriceTier{
				{UpToTokens: 100, InputPricePerMillion: -1, OutputPricePerMillion: 1},
				{UpToTokens: 0, InputPricePerMillion: 1, OutputPricePerMillion: 1},
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := valid()
			tc.mut(req)
			_, err := svc.SetPrice(context.Background(), req)
			require.Error(t, err)
			ae, ok := apierrors.As(err)
			require.True(t, ok, "expected an api error")
			assert.Equal(t, apierrors.CodePriceInvalid, ae.Code)

			var count int64
			require.NoError(t, db.Table("price_entries").Count(&count).Error)
			assert.Equal(t, int64(0), count, "nothing written on validation failure")
		})
	}
}

// AC1: SetPrice stores a version and returns its price_id; a repeat
// save keeps the id.
func TestSetPriceRoundTrip(t *testing.T) {
	svc := newBillingTestService(t)

	req := &billingv1.SetPriceRequest{
		ModelId: "model-a", AcceleratorType: "A800",
		InputPricePerMillion: 3, OutputPricePerMillion: 6,
		CachedPricePerMillion: 0.5,
		Tiers: []*billingv1.PriceTier{
			{UpToTokens: 1_000_000, InputPricePerMillion: 4, OutputPricePerMillion: 8},
			{UpToTokens: 0, InputPricePerMillion: 2, OutputPricePerMillion: 4},
		},
	}
	resp, err := svc.SetPrice(context.Background(), req)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetPriceId())

	// Repeat: same id, updated rates.
	req.InputPricePerMillion = 5
	resp2, err := svc.SetPrice(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, resp.GetPriceId(), resp2.GetPriceId())
}

// ListPrices returns the current view; include_history returns every
// version.
func TestListPricesCurrentAndHistory(t *testing.T) {
	svc := newBillingTestService(t)
	ctx := context.Background()

	past := time.Now().Add(-48 * time.Hour).Unix()
	future := time.Now().Add(48 * time.Hour).Unix()

	// Two versions in effect (past) and one scheduled (future).
	_, err := svc.SetPrice(ctx, &billingv1.SetPriceRequest{
		ModelId: "model-a", AcceleratorType: "A800",
		InputPricePerMillion: 1, OutputPricePerMillion: 1,
		EffectiveFrom: past,
	})
	require.NoError(t, err)
	_, err = svc.SetPrice(ctx, &billingv1.SetPriceRequest{
		ModelId: "model-a", AcceleratorType: "A800",
		InputPricePerMillion: 2, OutputPricePerMillion: 2,
		EffectiveFrom: past + 3600,
	})
	require.NoError(t, err)
	_, err = svc.SetPrice(ctx, &billingv1.SetPriceRequest{
		ModelId: "model-a", AcceleratorType: "A800",
		InputPricePerMillion: 9, OutputPricePerMillion: 9,
		EffectiveFrom: future,
	})
	require.NoError(t, err)

	// Current view: the latest effective version + the scheduled one.
	resp, err := svc.ListPrices(ctx, &billingv1.ListPricesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetPrices(), 2)
	rates := map[float64]bool{}
	for _, p := range resp.GetPrices() {
		rates[p.GetInputPricePerMillion()] = true
	}
	assert.True(t, rates[2], "latest effective version present")
	assert.True(t, rates[9], "scheduled version present")

	// History: all three versions.
	resp, err = svc.ListPrices(ctx, &billingv1.ListPricesRequest{IncludeHistory: true})
	require.NoError(t, err)
	assert.Len(t, resp.GetPrices(), 3)

	// Tiers round-trip through the summary.
	_, err = svc.SetPrice(ctx, &billingv1.SetPriceRequest{
		ModelId: "model-b", AcceleratorType: "default",
		InputPricePerMillion: 1, OutputPricePerMillion: 1,
		Tiers: []*billingv1.PriceTier{
			{UpToTokens: 100, InputPricePerMillion: 1.5, OutputPricePerMillion: 2.5},
			{UpToTokens: 0, InputPricePerMillion: 1, OutputPricePerMillion: 1},
		},
	})
	require.NoError(t, err)
	resp, err = svc.ListPrices(ctx, &billingv1.ListPricesRequest{ModelId: "model-b"})
	require.NoError(t, err)
	require.Len(t, resp.GetPrices(), 1)
	require.Len(t, resp.GetPrices()[0].GetTiers(), 2)
	assert.Equal(t, int64(100), resp.GetPrices()[0].GetTiers()[0].GetUpToTokens())
}

// 10508: the billing range validation.
func TestBillingRangeValidation(t *testing.T) {
	svc := newBillingTestService(t)
	ctx := withOrg("org-1")

	// since > until.
	_, err := svc.ListCharges(ctx, &billingv1.ListChargesRequest{Since: 200, Until: 100})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeBillingRangeInvalid, ae.Code)

	// Range too wide.
	_, err = svc.ListCharges(ctx, &billingv1.ListChargesRequest{
		Since: 1000, Until: 1000 + maxRangeSeconds + 1,
	})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeBillingRangeInvalid, ae.Code)

	// until in the future.
	_, err = svc.ListCharges(ctx, &billingv1.ListChargesRequest{
		Until: time.Now().Add(time.Hour).Unix() + 1,
	})
	require.Error(t, err)

	// Negative since.
	_, err = svc.ListCharges(ctx, &billingv1.ListChargesRequest{Since: -1})
	require.Error(t, err)
}

// AC16: ListCharges/ListBills require the organization metadata and
// scope to it.
func TestListChargesOrgScoping(t *testing.T) {
	svc := newBillingTestService(t)
	db := svc.repo.db.DB(context.Background())

	// No metadata: unauthorized.
	_, err := svc.ListCharges(context.Background(), &billingv1.ListChargesRequest{})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeUnauthorized, ae.Code)

	// Seed two orgs' charges (distinct periods to avoid the group
	// unique index).
	now := time.Now().UTC()
	hour1 := now.Add(-2 * time.Hour).Truncate(time.Hour)
	hour2 := now.Add(-3 * time.Hour).Truncate(time.Hour)
	for i, org := range []string{"org-1", "org-2"} {
		h := []time.Time{hour1, hour2}[i]
		require.NoError(t, db.Create(&ChargeRecord{
			ID: "charge-" + org, OrganizationID: org, APIKeyID: "k",
			ModelID: "m", AcceleratorType: "A800",
			PeriodStart: h.Unix(), PeriodEnd: h.Add(time.Hour).Unix(),
			Amount: 1, Currency: "USD", Priced: true,
		}).Error)
	}

	resp, err := svc.ListCharges(withOrg("org-1"), &billingv1.ListChargesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetCharges(), 1)
	assert.Equal(t, "org-1", resp.GetCharges()[0].GetOrganizationId())
	assert.Equal(t, int64(1), resp.GetPageMeta().GetTotal())

	// Filters: api key and model.
	require.NoError(t, db.Create(&ChargeRecord{
		ID: "charge-1b", OrganizationID: "org-1", APIKeyID: "k2",
		ModelID: "m2", AcceleratorType: "A800",
		PeriodStart: now.Add(-2 * time.Hour).Truncate(time.Hour).Unix(),
		PeriodEnd:   now.Add(-2 * time.Hour).Truncate(time.Hour).Add(time.Hour).Unix(),
		Amount:      2, Currency: "USD", Priced: true,
	}).Error)

	resp, err = svc.ListCharges(withOrg("org-1"), &billingv1.ListChargesRequest{ApiKeyId: "k2"})
	require.NoError(t, err)
	require.Len(t, resp.GetCharges(), 1)
	assert.Equal(t, "m2", resp.GetCharges()[0].GetModelId())
}

// AC12: ListBills returns monthly summaries with deterministic ids.
func TestListBillsSummaries(t *testing.T) {
	svc := newBillingTestService(t)
	db := svc.repo.db.DB(context.Background())

	// Use the two months before the current one so the range stays in
	// the past.
	now := time.Now().UTC()
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	monthA := thisMonth.AddDate(0, -2, 0) // two months ago
	monthB := thisMonth.AddDate(0, -1, 0) // last month
	require.NoError(t, db.Create(&ChargeRecord{
		ID: "c1", OrganizationID: "org-1", APIKeyID: "k", ModelID: "m",
		AcceleratorType: "A800",
		PeriodStart:     monthA.Add(14 * 24 * time.Hour).Unix(),
		PeriodEnd:       monthA.Add(14*24*time.Hour + time.Hour).Unix(),
		Amount:          1.5, Currency: "USD", Priced: true,
	}).Error)
	require.NoError(t, db.Create(&ChargeRecord{
		ID: "c2", OrganizationID: "org-1", APIKeyID: "k", ModelID: "m",
		AcceleratorType: "A800",
		PeriodStart:     monthA.Add(15 * 24 * time.Hour).Unix(),
		PeriodEnd:       monthA.Add(15*24*time.Hour + time.Hour).Unix(),
		Amount:          2.5, Currency: "USD", Priced: false,
	}).Error)
	require.NoError(t, db.Create(&ChargeRecord{
		ID: "c3", OrganizationID: "org-1", APIKeyID: "k", ModelID: "m",
		AcceleratorType: "A800",
		PeriodStart:     monthB.Add(10 * 24 * time.Hour).Unix(),
		PeriodEnd:       monthB.Add(10*24*time.Hour + time.Hour).Unix(),
		Amount:          4.0, Currency: "USD", Priced: true,
	}).Error)

	resp, err := svc.ListBills(withOrg("org-1"), &billingv1.ListBillsRequest{
		Since: monthA.Unix(), Until: thisMonth.Unix(),
	})
	require.NoError(t, err)
	require.Len(t, resp.GetBills(), 2)

	// Newest month first.
	bill := resp.GetBills()[0]
	assert.Equal(t, "org-1-"+monthB.Format("200601"), bill.GetBillId())
	assert.Equal(t, 4.0, bill.GetAmount())
	assert.Equal(t, int64(1), bill.GetChargeCount())
	assert.Equal(t, int64(0), bill.GetUnpricedCount())
	assert.Equal(t, monthB.Unix(), bill.GetPeriodStart())
	assert.Equal(t, thisMonth.Unix(), bill.GetPeriodEnd())

	// The older month.
	bill = resp.GetBills()[1]
	assert.Equal(t, "org-1-"+monthA.Format("200601"), bill.GetBillId())
	assert.Equal(t, 4.0, bill.GetAmount())
	assert.Equal(t, int64(2), bill.GetChargeCount())
	assert.Equal(t, int64(1), bill.GetUnpricedCount())
}

// GetBalance is a stub until feature #8.
func TestGetBalanceStub(t *testing.T) {
	svc := newBillingTestService(t)
	_, err := svc.GetBalance(context.Background(), &billingv1.GetBalanceRequest{})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeAccountNotFound, ae.Code)
}

// Pagination defaults and clamping.
func TestPaginationClamping(t *testing.T) {
	svc := newBillingTestService(t)
	db := svc.repo.db.DB(context.Background())
	now := time.Now().UTC()

	for i := 0; i < 3; i++ {
		require.NoError(t, db.Create(&ChargeRecord{
			ID: string(rune('a' + i)), OrganizationID: "org-1", APIKeyID: "k",
			ModelID: "m", AcceleratorType: "A800",
			PeriodStart: now.Add(-time.Duration(i+1) * time.Hour).Truncate(time.Hour).Unix(),
			PeriodEnd:   now.Add(-time.Duration(i+1) * time.Hour).Truncate(time.Hour).Add(time.Hour).Unix(),
			Amount:      float64(i), Currency: "USD", Priced: true,
		}).Error)
	}

	// Default limit 20 returns all.
	resp, err := svc.ListCharges(withOrg("org-1"), &billingv1.ListChargesRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.GetCharges(), 3)
	assert.EqualValues(t, 20, resp.GetPageMeta().GetLimit())

	// Limit clamped to 100.
	resp, err = svc.ListCharges(withOrg("org-1"), &billingv1.ListChargesRequest{
		Page: &commonv1.PageRequest{Limit: 500},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 100, resp.GetPageMeta().GetLimit())

	// Offset pages.
	resp, err = svc.ListCharges(withOrg("org-1"), &billingv1.ListChargesRequest{
		Page: &commonv1.PageRequest{Offset: 2, Limit: 2},
	})
	require.NoError(t, err)
	assert.Len(t, resp.GetCharges(), 1)
}
