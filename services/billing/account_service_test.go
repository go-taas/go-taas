package billing

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	commonv1 "github.com/go-taas/go-taas/proto/taas/common/v1"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// createTestAccount creates a prepaid account with an opening balance
// under the given org and returns its id.
func createTestAccount(t *testing.T, svc *Service, orgID, mode string, initialCents, quotaCents int64) string {
	t.Helper()
	resp, err := svc.CreateAccount(withOrg(orgID), &billingv1.CreateAccountRequest{
		Mode:                mode,
		MonthlyQuotaCents:   quotaCents,
		OverdrawPolicy:      OverdrawBlock,
		InitialBalanceCents: initialCents,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.GetAccount())
	return resp.GetAccount().GetAccountId()
}

// AC1: create prepaid with opening balance; the opening credit is a
// recharge transaction; a second account for the same org is 10509.
func TestCreateAccountService(t *testing.T) {
	svc := newBillingTestService(t)

	resp, err := svc.CreateAccount(withOrg("org-1"), &billingv1.CreateAccountRequest{
		Mode:                AccountModePrepaid,
		OverdrawPolicy:      OverdrawBlock,
		InitialBalanceCents: 5000,
	})
	require.NoError(t, err)
	acc := resp.GetAccount()
	assert.NotEmpty(t, acc.GetAccountId())
	assert.Equal(t, AccountModePrepaid, acc.GetMode())
	assert.Equal(t, int64(5000), acc.GetBalanceCents())
	assert.Equal(t, int64(5000), acc.GetRemainingCents())
	assert.Equal(t, "USD", acc.GetCurrency())
	assert.NotZero(t, acc.GetCycleStartedAt())

	// The opening balance is one recharge ledger row.
	ledger, err := svc.ListTransactions(withOrg("org-1"), &billingv1.ListTransactionsRequest{})
	require.NoError(t, err)
	require.Len(t, ledger.GetTransactions(), 1)
	assert.Equal(t, TransactionTypeRecharge, ledger.GetTransactions()[0].GetType())

	// One account per org (AD1): a second create is 10509.
	_, err = svc.CreateAccount(withOrg("org-1"), &billingv1.CreateAccountRequest{
		Mode:           AccountModePostpaid,
		OverdrawPolicy: OverdrawBlock,
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeAccountInvalid, ae.Code)
}

// AC2: the CreateAccount validation matrix — every failure is 10509
// and no account row is written.
func TestCreateAccountValidationMatrix(t *testing.T) {
	svc := newBillingTestService(t)

	cases := []struct {
		name string
		req  *billingv1.CreateAccountRequest
	}{
		{"bad mode", &billingv1.CreateAccountRequest{Mode: "hybrid", OverdrawPolicy: OverdrawBlock}},
		{"empty mode", &billingv1.CreateAccountRequest{Mode: "", OverdrawPolicy: OverdrawBlock}},
		{"bad policy", &billingv1.CreateAccountRequest{Mode: AccountModePrepaid, OverdrawPolicy: "maybe"}},
		{"negative quota", &billingv1.CreateAccountRequest{Mode: AccountModePostpaid, MonthlyQuotaCents: -1, OverdrawPolicy: OverdrawBlock}},
		{"negative opening balance", &billingv1.CreateAccountRequest{Mode: AccountModePrepaid, InitialBalanceCents: -1, OverdrawPolicy: OverdrawBlock}},
		{"postpaid opening balance", &billingv1.CreateAccountRequest{Mode: AccountModePostpaid, InitialBalanceCents: 100, OverdrawPolicy: OverdrawBlock}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateAccount(withOrg("org-1"), tc.req)
			require.Error(t, err)
			ae, ok := apierrors.As(err)
			require.True(t, ok, "expected an api error")
			assert.Equal(t, apierrors.CodeAccountInvalid, ae.Code)

			accounts, err := svc.ListAccounts(withOrg("org-1"), &billingv1.ListAccountsRequest{})
			require.NoError(t, err)
			assert.Empty(t, accounts.GetAccounts(), "nothing written on validation failure")
		})
	}
}

// Missing org header is unauthorized on every account RPC.
func TestAccountOrgHeaderRequired(t *testing.T) {
	svc := newBillingTestService(t)

	_, err := svc.CreateAccount(context.Background(), &billingv1.CreateAccountRequest{Mode: AccountModePrepaid})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeUnauthorized, ae.Code)
}

// AC10: account reads and writes are scoped to the caller org; a
// foreign account id is 10503.
func TestAccountOrgScoping(t *testing.T) {
	svc := newBillingTestService(t)
	accountID := createTestAccount(t, svc, "org-1", AccountModePrepaid, 1000, 0)

	// Another org cannot see, update, recharge or list its ledger.
	_, err := svc.GetAccount(withOrg("org-2"), &billingv1.GetAccountRequest{AccountId: accountID})
	require.Error(t, err)
	ae, _ := apierrors.As(err)
	assert.Equal(t, apierrors.CodeAccountNotFound, ae.Code)

	_, err = svc.UpdateAccount(withOrg("org-2"), &billingv1.UpdateAccountRequest{
		AccountId: accountID, Mode: AccountModePostpaid, OverdrawPolicy: OverdrawBlock,
	})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeAccountNotFound, ae.Code)

	_, err = svc.Recharge(withOrg("org-2"), &billingv1.RechargeRequest{
		AccountId: accountID, AmountCents: 100, IdempotencyKey: "k1",
	})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeAccountNotFound, ae.Code)

	_, err = svc.ListTransactions(withOrg("org-2"), &billingv1.ListTransactionsRequest{AccountId: accountID})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeAccountNotFound, ae.Code)

	// org-2 has no accounts of its own.
	accounts, err := svc.ListAccounts(withOrg("org-2"), &billingv1.ListAccountsRequest{})
	require.NoError(t, err)
	assert.Empty(t, accounts.GetAccounts())
}

// AD12: UpdateAccount replaces mode/quota/policy; balance and usage
// are never settable.
func TestUpdateAccountService(t *testing.T) {
	svc := newBillingTestService(t)
	accountID := createTestAccount(t, svc, "org-1", AccountModePrepaid, 1000, 0)

	resp, err := svc.UpdateAccount(withOrg("org-1"), &billingv1.UpdateAccountRequest{
		AccountId:         accountID,
		Mode:              AccountModePostpaid,
		MonthlyQuotaCents: 20000,
		OverdrawPolicy:    OverdrawWarn,
	})
	require.NoError(t, err)
	acc := resp.GetAccount()
	assert.Equal(t, AccountModePostpaid, acc.GetMode())
	assert.Equal(t, int64(20000), acc.GetMonthlyQuotaCents())
	assert.Equal(t, OverdrawWarn, acc.GetOverdrawPolicy())
	// The prepaid balance survives the mode switch.
	assert.Equal(t, int64(1000), acc.GetBalanceCents())

	// Validation matrix: bad mode / quota / policy is 10509.
	_, err = svc.UpdateAccount(withOrg("org-1"), &billingv1.UpdateAccountRequest{
		AccountId: accountID, Mode: "hybrid", OverdrawPolicy: OverdrawBlock,
	})
	require.Error(t, err)
	ae, _ := apierrors.As(err)
	assert.Equal(t, apierrors.CodeAccountInvalid, ae.Code)
}

// AC2/AC3: recharge credits a prepaid balance idempotently; the
// validation matrix is 10510.
func TestRechargeService(t *testing.T) {
	svc := newBillingTestService(t)
	accountID := createTestAccount(t, svc, "org-1", AccountModePrepaid, 1000, 0)

	resp, err := svc.Recharge(withOrg("org-1"), &billingv1.RechargeRequest{
		AccountId: accountID, AmountCents: 2500, IdempotencyKey: "rch-1", Note: "top up",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3500), resp.GetAccount().GetBalanceCents())
	assert.Equal(t, TransactionTypeRecharge, resp.GetTransaction().GetType())
	assert.Equal(t, int64(2500), resp.GetTransaction().GetAmountCents())

	// Replay converges: same key, same amount, no double credit.
	replay, err := svc.Recharge(withOrg("org-1"), &billingv1.RechargeRequest{
		AccountId: accountID, AmountCents: 2500, IdempotencyKey: "rch-1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3500), replay.GetAccount().GetBalanceCents())

	// Same key, different amount is 10510.
	_, err = svc.Recharge(withOrg("org-1"), &billingv1.RechargeRequest{
		AccountId: accountID, AmountCents: 999, IdempotencyKey: "rch-1",
	})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeTransactionInvalid, ae.Code)

	// Validation matrix: non-positive amount, empty key, long key,
	// long note, postpaid account.
	_, err = svc.Recharge(withOrg("org-1"), &billingv1.RechargeRequest{
		AccountId: accountID, AmountCents: 0, IdempotencyKey: "k",
	})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeTransactionInvalid, ae.Code)

	_, err = svc.Recharge(withOrg("org-1"), &billingv1.RechargeRequest{
		AccountId: accountID, AmountCents: 100, IdempotencyKey: "",
	})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeTransactionInvalid, ae.Code)

	_, err = svc.Recharge(withOrg("org-1"), &billingv1.RechargeRequest{
		AccountId: accountID, AmountCents: 100, IdempotencyKey: strings.Repeat("k", 129),
	})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeTransactionInvalid, ae.Code)

	_, err = svc.Recharge(withOrg("org-1"), &billingv1.RechargeRequest{
		AccountId: accountID, AmountCents: 100, IdempotencyKey: "k2", Note: strings.Repeat("n", 129),
	})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeTransactionInvalid, ae.Code)

	postpaidID := createTestAccount(t, svc, "org-2", AccountModePostpaid, 0, 5000)
	_, err = svc.Recharge(withOrg("org-2"), &billingv1.RechargeRequest{
		AccountId: postpaidID, AmountCents: 100, IdempotencyKey: "k3",
	})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeTransactionInvalid, ae.Code)
}

// D8: refund debits a prepaid balance; the ledger row is a refund.
func TestRefundService(t *testing.T) {
	svc := newBillingTestService(t)
	accountID := createTestAccount(t, svc, "org-1", AccountModePrepaid, 5000, 0)

	resp, err := svc.Refund(withOrg("org-1"), &billingv1.RefundRequest{
		AccountId: accountID, AmountCents: 1200, IdempotencyKey: "ref-1", Note: "goodwill",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3800), resp.GetAccount().GetBalanceCents())
	assert.Equal(t, TransactionTypeRefund, resp.GetTransaction().GetType())

	// Replay converges.
	replay, err := svc.Refund(withOrg("org-1"), &billingv1.RefundRequest{
		AccountId: accountID, AmountCents: 1200, IdempotencyKey: "ref-1",
	})
	require.NoError(t, err)
	assert.Equal(t, int64(3800), replay.GetAccount().GetBalanceCents())
}

// AC9: ListTransactions filters by account, type and time range with
// pagination, newest first.
func TestListTransactionsService(t *testing.T) {
	svc := newBillingTestService(t)
	accountID := createTestAccount(t, svc, "org-1", AccountModePrepaid, 1000, 0)

	for i := 0; i < 3; i++ {
		_, err := svc.Recharge(withOrg("org-1"), &billingv1.RechargeRequest{
			AccountId: accountID, AmountCents: 100, IdempotencyKey: "rch-" + string(rune('a'+i)),
		})
		require.NoError(t, err)
	}
	_, err := svc.Refund(withOrg("org-1"), &billingv1.RefundRequest{
		AccountId: accountID, AmountCents: 50, IdempotencyKey: "ref-1",
	})
	require.NoError(t, err)

	// All rows for the account: opening + 3 recharges + 1 refund = 5.
	all, err := svc.ListTransactions(withOrg("org-1"), &billingv1.ListTransactionsRequest{AccountId: accountID})
	require.NoError(t, err)
	assert.Equal(t, int64(5), all.GetPageMeta().GetTotal())
	assert.Len(t, all.GetTransactions(), 5)

	// Type filter.
	recharges, err := svc.ListTransactions(withOrg("org-1"), &billingv1.ListTransactionsRequest{
		AccountId: accountID, Type: TransactionTypeRecharge,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(4), recharges.GetPageMeta().GetTotal())

	refunds, err := svc.ListTransactions(withOrg("org-1"), &billingv1.ListTransactionsRequest{
		AccountId: accountID, Type: TransactionTypeRefund,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), refunds.GetPageMeta().GetTotal())

	// Pagination: page size 2, second page.
	page2, err := svc.ListTransactions(withOrg("org-1"), &billingv1.ListTransactionsRequest{
		AccountId: accountID,
		Page:      &commonv1.PageRequest{Offset: 2, Limit: 2},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(5), page2.GetPageMeta().GetTotal())
	assert.Len(t, page2.GetTransactions(), 2)

	// Bad type is 10510; bad range is 10508.
	_, err = svc.ListTransactions(withOrg("org-1"), &billingv1.ListTransactionsRequest{
		AccountId: accountID, Type: "donation",
	})
	require.Error(t, err)
	ae, _ := apierrors.As(err)
	assert.Equal(t, apierrors.CodeTransactionInvalid, ae.Code)

	_, err = svc.ListTransactions(withOrg("org-1"), &billingv1.ListTransactionsRequest{
		AccountId: accountID, Since: 200, Until: 100,
	})
	require.Error(t, err)
	ae, _ = apierrors.As(err)
	assert.Equal(t, apierrors.CodeBillingRangeInvalid, ae.Code)
}

// AC6/AC7/AC12: the CheckFunds truth table.
func TestCheckFundsTruthTable(t *testing.T) {
	svc := newBillingTestService(t)

	// No account: ungated.
	none, err := svc.CheckFunds(context.Background(), &billingv1.CheckFundsRequest{OrganizationId: "org-none"})
	require.NoError(t, err)
	assert.True(t, none.GetAllowed())
	assert.Empty(t, none.GetMode())

	// Prepaid with balance: allowed.
	prepaidID := createTestAccount(t, svc, "org-pre", AccountModePrepaid, 100, 0)
	_ = prepaidID
	allowed, err := svc.CheckFunds(context.Background(), &billingv1.CheckFundsRequest{OrganizationId: "org-pre"})
	require.NoError(t, err)
	assert.True(t, allowed.GetAllowed())
	assert.Equal(t, AccountModePrepaid, allowed.GetMode())

	// Prepaid drained: blocked.
	_, err = svc.Refund(withOrg("org-pre"), &billingv1.RefundRequest{
		AccountId: prepaidID, AmountCents: 100, IdempotencyKey: "drain",
	})
	require.NoError(t, err)
	blocked, err := svc.CheckFunds(context.Background(), &billingv1.CheckFundsRequest{OrganizationId: "org-pre"})
	require.NoError(t, err)
	assert.False(t, blocked.GetAllowed())
	assert.Equal(t, "insufficient_funds", blocked.GetReason())

	// Postpaid under quota: allowed.
	postpaidID := createTestAccount(t, svc, "org-post", AccountModePostpaid, 0, 10000)
	_ = postpaidID
	underQuota, err := svc.CheckFunds(context.Background(), &billingv1.CheckFundsRequest{OrganizationId: "org-post"})
	require.NoError(t, err)
	assert.True(t, underQuota.GetAllowed())
	assert.Equal(t, AccountModePostpaid, underQuota.GetMode())

	// Postpaid at quota with block policy: blocked.
	repo := svc.repo
	require.NoError(t, repo.db.DB(context.Background()).
		Exec("UPDATE accounts SET used_this_cycle_cents = 10000 WHERE id = ?", postpaidID).Error)
	atQuota, err := svc.CheckFunds(context.Background(), &billingv1.CheckFundsRequest{OrganizationId: "org-post"})
	require.NoError(t, err)
	assert.False(t, atQuota.GetAllowed())
	assert.Equal(t, "insufficient_funds", atQuota.GetReason())

	// Postpaid at quota with warn policy: still allowed.
	require.NoError(t, repo.db.DB(context.Background()).
		Exec("UPDATE accounts SET overdraw_policy = ? WHERE id = ?", OverdrawWarn, postpaidID).Error)
	warnQuota, err := svc.CheckFunds(context.Background(), &billingv1.CheckFundsRequest{OrganizationId: "org-post"})
	require.NoError(t, err)
	assert.True(t, warnQuota.GetAllowed())

	// Postpaid unlimited quota: always allowed.
	unlimitedID := createTestAccount(t, svc, "org-unl", AccountModePostpaid, 0, 0)
	_ = unlimitedID
	require.NoError(t, repo.db.DB(context.Background()).
		Exec("UPDATE accounts SET used_this_cycle_cents = 999999 WHERE id = ?", unlimitedID).Error)
	unlimited, err := svc.CheckFunds(context.Background(), &billingv1.CheckFundsRequest{OrganizationId: "org-unl"})
	require.NoError(t, err)
	assert.True(t, unlimited.GetAllowed())

	// Empty org id is unauthorized.
	_, err = svc.CheckFunds(context.Background(), &billingv1.CheckFundsRequest{})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeUnauthorized, ae.Code)
}

// GetBalance returns the account snapshot with the legacy double
// field kept for console compatibility.
func TestGetBalanceAccount(t *testing.T) {
	svc := newBillingTestService(t)
	accountID := createTestAccount(t, svc, "org-1", AccountModePrepaid, 12345, 0)
	_ = accountID

	resp, err := svc.GetBalance(withOrg("org-1"), &billingv1.GetBalanceRequest{})
	require.NoError(t, err)
	assert.Equal(t, AccountModePrepaid, resp.GetMode())
	assert.Equal(t, int64(12345), resp.GetBalanceCents())
	assert.InDelta(t, 123.45, resp.GetBalance(), 0.001)
	assert.Equal(t, "USD", resp.GetCurrency())

	// No account: 10503.
	_, err = svc.GetBalance(withOrg("org-2"), &billingv1.GetBalanceRequest{})
	require.Error(t, err)
	ae, ok := apierrors.As(err)
	require.True(t, ok)
	assert.Equal(t, apierrors.CodeAccountNotFound, ae.Code)
}

// summarizeAccount computes remaining/usage for both modes.
func TestSummarizeAccount(t *testing.T) {
	prepaid := &Account{Mode: AccountModePrepaid, BalanceCents: 5000}
	s := summarizeAccount(prepaid)
	assert.Equal(t, int64(5000), s.GetRemainingCents())
	assert.Equal(t, int32(0), s.GetQuotaUsagePercent())

	postpaid := &Account{Mode: AccountModePostpaid, MonthlyQuotaCents: 10000, UsedThisCycleCents: 2500}
	s = summarizeAccount(postpaid)
	assert.Equal(t, int64(7500), s.GetRemainingCents())
	assert.Equal(t, int32(25), s.GetQuotaUsagePercent())

	// Over-quota clamps remaining at 0.
	over := &Account{Mode: AccountModePostpaid, MonthlyQuotaCents: 10000, UsedThisCycleCents: 12000}
	s = summarizeAccount(over)
	assert.Equal(t, int64(0), s.GetRemainingCents())
	assert.Equal(t, int32(120), s.GetQuotaUsagePercent())

	// Unlimited postpaid reports 0 remaining / 0 usage percent.
	unlimited := &Account{Mode: AccountModePostpaid, MonthlyQuotaCents: 0, UsedThisCycleCents: 500}
	s = summarizeAccount(unlimited)
	assert.Equal(t, int64(0), s.GetRemainingCents())
	assert.Equal(t, int32(0), s.GetQuotaUsagePercent())
}
