package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"

	"github.com/go-taas/go-taas/pkg/authctx"
	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// billingPlatformAdminCtx builds a context authenticated as a platform
// administrator, which is the only role allowed to fund a tenant.
func billingPlatformAdminCtx() context.Context {
	return authctx.WithResolver(context.Background(), fakeBillingRoles{roles: []string{"platform-admin"}})
}

// seedOrganization writes the billing module's read-only projection of an
// organization row (the tenant table belongs to the tenancy module).
func seedOrganization(t *testing.T, svc *Service, id, name, state string) {
	t.Helper()
	require.NoError(t, svc.repo.DB(context.Background()).Exec(
		"INSERT INTO organizations (id, display_name, state) VALUES (?, ?, ?)", id, name, state).Error)
}

// newOrgBalanceTestService builds a billing service over a fresh database with
// the organizations projection table and one prepaid account.
func newOrgBalanceTestService(t *testing.T) *Service {
	t.Helper()
	svc := newBillingTestService(t)
	ctx := context.Background()
	require.NoError(t, svc.repo.DB(ctx).Exec(
		"CREATE TABLE organizations (id TEXT PRIMARY KEY, display_name TEXT, state TEXT)").Error)
	seedOrganization(t, svc, "org-a", "Alpha", "active")
	seedOrganization(t, svc, "org-b", "Beta", "active")
	return svc
}

// TestListOrganizationBalancesCoversEveryTenant covers the platform
// administrator's money view: every organization appears, with its account
// when it has one and has_account=false when it does not, plus the cycle
// spend. A non-administrator is refused.
func TestListOrganizationBalancesCoversEveryTenant(t *testing.T) {
	svc := newOrgBalanceTestService(t)
	ctx := billingPlatformAdminCtx()

	// org-a has a prepaid account with 100.00; org-b has none.
	accRepo, err := svc.accountRepo()
	require.NoError(t, err)
	account, _, err := accRepo.CreateAccount(ctx, &Account{
		ID: "acc-a", OrganizationID: "org-a", Mode: AccountModePrepaid,
		Currency: "CNY", CycleStartedAt: monthStartOf(time.Now().UTC()),
	}, 10000)
	require.NoError(t, err)
	require.NotNil(t, account)

	resp, err := svc.ListOrganizationBalances(ctx, &billingv1.ListOrganizationBalancesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetBalances(), 2)

	byOrg := map[string]*billingv1.OrganizationBalance{}
	for _, b := range resp.GetBalances() {
		byOrg[b.GetOrganizationId()] = b
	}

	a := byOrg["org-a"]
	require.NotNil(t, a)
	assert.Equal(t, "Alpha", a.GetOrganizationName())
	assert.True(t, a.GetHasAccount())
	assert.Equal(t, "prepaid", a.GetMode())
	assert.EqualValues(t, 10000, a.GetBalanceCents())
	assert.EqualValues(t, 10000, a.GetRemainingCents())

	b := byOrg["org-b"]
	require.NotNil(t, b)
	assert.False(t, b.GetHasAccount(), "an organization without an account is still listed")
	assert.Equal(t, "active", b.GetOrganizationState())
	assert.Empty(t, b.GetMode())

	// A platform administrator funding flow needs the org state, and an
	// ordinary caller never sees the fleet's money.
	_, err = svc.ListOrganizationBalances(context.Background(), &billingv1.ListOrganizationBalancesRequest{})
	assert.Equal(t, apierrors.CodeForbidden, apierrors.CodeOf(err))
}

// TestRechargeOrganizationCreatesTheAccount covers the funding flow: a
// platform administrator credits an organization that has no account yet, and
// the created account carries the movement with the administrator recorded.
func TestRechargeOrganizationCreatesTheAccount(t *testing.T) {
	svc := newOrgBalanceTestService(t)
	ctx := billingPlatformAdminCtx()
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "admin-1"})

	resp, err := svc.RechargeOrganization(ctx, &billingv1.RechargeOrganizationRequest{
		OrganizationId: "org-b", AmountCents: 50000, IdempotencyKey: "topup-1",
		Note: "第一笔充值", Source: SourceAdminManual,
	})
	require.NoError(t, err)
	assert.True(t, resp.GetAccountCreated())
	require.NotNil(t, resp.GetBalance())
	assert.Equal(t, "prepaid", resp.GetBalance().GetMode())
	assert.EqualValues(t, 50000, resp.GetBalance().GetBalanceCents())
	assert.EqualValues(t, 50000, resp.GetBalance().GetRemainingCents())

	tx := resp.GetTransaction()
	require.NotNil(t, tx)
	assert.Equal(t, "recharge", tx.GetType())
	assert.Equal(t, SourceAdminManual, tx.GetSource())
	assert.Equal(t, "admin-1", tx.GetActorUserId(), "the acting administrator is recorded")
	assert.Equal(t, "第一笔充值", tx.GetReference())

	// A second recharge tops the same account up instead of creating one.
	resp, err = svc.RechargeOrganization(ctx, &billingv1.RechargeOrganizationRequest{
		OrganizationId: "org-b", AmountCents: 2500, IdempotencyKey: "topup-2", Source: SourceAdminCorrection,
	})
	require.NoError(t, err)
	assert.False(t, resp.GetAccountCreated())
	assert.EqualValues(t, 52500, resp.GetBalance().GetBalanceCents())
	assert.Equal(t, SourceAdminCorrection, resp.GetTransaction().GetSource())

	// Idempotent: replaying the key returns the same movement, not a second
	// credit.
	resp, err = svc.RechargeOrganization(ctx, &billingv1.RechargeOrganizationRequest{
		OrganizationId: "org-b", AmountCents: 2500, IdempotencyKey: "topup-2", Source: SourceAdminCorrection,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 52500, resp.GetBalance().GetBalanceCents())
}

// TestRechargeOrganizationValidation covers the guard rails: only a platform
// administrator, only an existing active organization, and only an
// operator-set source.
func TestRechargeOrganizationValidation(t *testing.T) {
	svc := newOrgBalanceTestService(t)
	ctx := billingPlatformAdminCtx()
	seedOrganization(t, svc, "org-c", "Gamma", "disabled")

	cases := []struct {
		name string
		req  *billingv1.RechargeOrganizationRequest
		code apierrors.Code
	}{
		{"missing organization", &billingv1.RechargeOrganizationRequest{AmountCents: 100, IdempotencyKey: "k"}, apierrors.CodeOrganizationNotFound},
		{"unknown organization", &billingv1.RechargeOrganizationRequest{OrganizationId: "org-x", AmountCents: 100, IdempotencyKey: "k"}, apierrors.CodeOrganizationNotFound},
		{"disabled organization", &billingv1.RechargeOrganizationRequest{OrganizationId: "org-c", AmountCents: 100, IdempotencyKey: "k"}, apierrors.CodeOrganizationDisabled},
		{"zero amount", &billingv1.RechargeOrganizationRequest{OrganizationId: "org-a", AmountCents: 0, IdempotencyKey: "k"}, apierrors.CodeTransactionInvalid},
		{"missing idempotency key", &billingv1.RechargeOrganizationRequest{OrganizationId: "org-a", AmountCents: 100}, apierrors.CodeTransactionInvalid},
		{"non-operator source", &billingv1.RechargeOrganizationRequest{OrganizationId: "org-a", AmountCents: 100, IdempotencyKey: "k", Source: SourceSettlement}, apierrors.CodeTransactionInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.RechargeOrganization(ctx, tc.req)
			require.Error(t, err)
			assert.Equal(t, tc.code, apierrors.CodeOf(err))
		})
	}
}

// TestDebitOrganizationTakesCreditBack covers the correction direction: the
// amount comes off the balance, a reason is required, and an over-debit is
// refused instead of pushing the balance negative.
func TestDebitOrganizationTakesCreditBack(t *testing.T) {
	svc := newOrgBalanceTestService(t)
	ctx := billingPlatformAdminCtx()
	svc.SetSessionUserResolver(fakeSessionUserResolver{user: "admin-1"})

	_, err := svc.RechargeOrganization(ctx, &billingv1.RechargeOrganizationRequest{
		OrganizationId: "org-a", AmountCents: 10000, IdempotencyKey: "seed-1", Source: SourceAdminManual,
	})
	require.NoError(t, err)

	resp, err := svc.DebitOrganization(ctx, &billingv1.DebitOrganizationRequest{
		OrganizationId: "org-a", AmountCents: 3000, IdempotencyKey: "debit-1",
		Note: "重复充值冲正", Source: SourceAdminCorrection,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 7000, resp.GetBalance().GetBalanceCents())
	assert.Equal(t, "refund", resp.GetTransaction().GetType())
	assert.Equal(t, "admin-1", resp.GetTransaction().GetActorUserId())

	// A reason is required: a debit is a correction, not a silent withdrawal.
	_, err = svc.DebitOrganization(ctx, &billingv1.DebitOrganizationRequest{
		OrganizationId: "org-a", AmountCents: 100, IdempotencyKey: "debit-2",
	})
	assert.Equal(t, apierrors.CodeTransactionInvalid, apierrors.CodeOf(err))

	// Over-debiting is refused rather than driving the balance negative.
	_, err = svc.DebitOrganization(ctx, &billingv1.DebitOrganizationRequest{
		OrganizationId: "org-a", AmountCents: 99999, IdempotencyKey: "debit-3", Note: "too much",
	})
	assert.Equal(t, apierrors.CodeInsufficientFunds, apierrors.CodeOf(err))
}
