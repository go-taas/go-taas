package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apierrors "github.com/go-taas/go-taas/pkg/errors"
)

// newAccountRepo builds an AccountRepository over a fresh test DB.
func newAccountRepo(t *testing.T) (*AccountRepository, *gorm.DB) {
	t.Helper()
	db := newBillingTestDB(t)
	return NewAccountRepository(db), db
}

// seedAccount inserts a minimal account row for tests.
func seedAccount(t *testing.T, repo *AccountRepository, orgID, mode string, balance, quota, used int64) *Account {
	t.Helper()
	account := &Account{
		OrganizationID:    orgID,
		Mode:              mode,
		BalanceCents:      balance,
		MonthlyQuotaCents: quota,
		UsedThisCycleCents: used,
		OverdrawPolicy:    OverdrawBlock,
		Currency:          "USD",
		CycleStartedAt:    monthStartOf(time.Now().UTC()),
	}
	created, _, err := repo.CreateAccount(context.Background(), account, 0)
	require.NoError(t, err)
	return created
}

// AC1: one account per org — the unique index rejects a second insert
// with 10509.
func TestAccountRepositoryOnePerOrg(t *testing.T) {
	repo, _ := newAccountRepo(t)
	ctx := context.Background()

	first := seedAccount(t, repo, "org-1", AccountModePrepaid, 0, 0, 0)
	assert.NotEmpty(t, first.ID)

	dup := &Account{
		OrganizationID: "org-1",
		Mode:           AccountModePostpaid,
		Currency:       "USD",
		CycleStartedAt: monthStartOf(time.Now().UTC()),
	}
	_, _, err := repo.CreateAccount(ctx, dup, 0)
	assert.Equal(t, apierrors.CodeAccountInvalid, apierrors.CodeOf(err))
}

// AC1: the opening balance is credited as a recharge transaction with
// the "opening:{account_id}" key.
func TestAccountRepositoryOpeningBalance(t *testing.T) {
	repo, _ := newAccountRepo(t)
	ctx := context.Background()

	account := &Account{
		OrganizationID: "org-1",
		Mode:           AccountModePrepaid,
		Currency:       "USD",
		CycleStartedAt: monthStartOf(time.Now().UTC()),
	}
	created, opening, err := repo.CreateAccount(ctx, account, 5000)
	require.NoError(t, err)
	assert.Equal(t, int64(5000), created.BalanceCents)
	require.NotNil(t, opening)
	assert.Equal(t, TransactionTypeRecharge, opening.Type)
	assert.Equal(t, int64(5000), opening.AmountCents)
	assert.Equal(t, "opening:"+created.ID, opening.IdempotencyKey)

	// The stored row carries the balance.
	stored, err := repo.FindByOrg(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, int64(5000), stored.BalanceCents)
}

// AC2/AC3: recharge is idempotent on the key; a replayed key with a
// different amount is rejected with 10510.
func TestAccountRepositoryRechargeIdempotency(t *testing.T) {
	repo, _ := newAccountRepo(t)
	ctx := context.Background()
	account := seedAccount(t, repo, "org-1", AccountModePrepaid, 1000, 0, 0)

	updated, tx, err := repo.Recharge(ctx, account, 2500, "key-1", "first", TransactionTypeRecharge)
	require.NoError(t, err)
	assert.Equal(t, int64(3500), updated.BalanceCents)
	assert.Equal(t, int64(3500), tx.BalanceAfterCents)

	// Replay with the same amount converges: no double credit.
	again, txAgain, err := repo.Recharge(ctx, account, 2500, "key-1", "first", TransactionTypeRecharge)
	require.NoError(t, err)
	assert.Equal(t, int64(3500), again.BalanceCents)
	assert.Equal(t, tx.ID, txAgain.ID)

	// Same key, different amount: 10510.
	_, _, err = repo.Recharge(ctx, account, 9999, "key-1", "other", TransactionTypeRecharge)
	assert.Equal(t, apierrors.CodeTransactionInvalid, apierrors.CodeOf(err))

	// The balance is unchanged after the rejection.
	stored, err := repo.FindByOrg(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, int64(3500), stored.BalanceCents)
}

// Refund decreases the balance and is idempotent the same way.
func TestAccountRepositoryRefund(t *testing.T) {
	repo, _ := newAccountRepo(t)
	ctx := context.Background()
	account := seedAccount(t, repo, "org-1", AccountModePrepaid, 5000, 0, 0)

	updated, _, err := repo.Recharge(ctx, account, 1500, "refund-1", "partial refund", TransactionTypeRefund)
	require.NoError(t, err)
	assert.Equal(t, int64(3500), updated.BalanceCents)

	// Replay converges.
	again, _, err := repo.Recharge(ctx, account, 1500, "refund-1", "partial refund", TransactionTypeRefund)
	require.NoError(t, err)
	assert.Equal(t, int64(3500), again.BalanceCents)
}

// AC4/AC5: ApplyDeduction writes the ledger row and updates the
// account inside the caller's transaction; a replayed charge key
// converges without a second write.
func TestAccountRepositoryApplyDeduction(t *testing.T) {
	repo, _ := newAccountRepo(t)
	ctx := context.Background()
	account := seedAccount(t, repo, "org-1", AccountModePrepaid, 10000, 0, 0)

	err := repo.ApplyDeduction(ctx, DeductionSpec{
		AccountID: account.ID, ChargeID: "charge-1", AmountCents: 1200,
	})
	require.NoError(t, err)

	stored, err := repo.FindByOrg(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, int64(8800), stored.BalanceCents)

	// Replay the same charge: converges, no double deduction.
	err = repo.ApplyDeduction(ctx, DeductionSpec{
		AccountID: account.ID, ChargeID: "charge-1", AmountCents: 1200,
	})
	require.NoError(t, err)
	stored, err = repo.FindByOrg(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, int64(8800), stored.BalanceCents)

	// A different charge deducts again.
	err = repo.ApplyDeduction(ctx, DeductionSpec{
		AccountID: account.ID, ChargeID: "charge-2", AmountCents: 300,
	})
	require.NoError(t, err)
	stored, err = repo.FindByOrg(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, int64(8500), stored.BalanceCents)

	// Exactly two deduction rows exist.
	rows, total, err := repo.ListTransactions(ctx, TransactionFilter{AccountID: account.ID, Type: TransactionTypeDeduction})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, rows, 2)
}

// AC5: a postpaid deduction accumulates used_this_cycle_cents and
// leaves the balance untouched.
func TestAccountRepositoryPostpaidDeduction(t *testing.T) {
	repo, _ := newAccountRepo(t)
	ctx := context.Background()
	account := seedAccount(t, repo, "org-1", AccountModePostpaid, 0, 10000, 0)

	err := repo.ApplyDeduction(ctx, DeductionSpec{
		AccountID: account.ID, ChargeID: "charge-1", AmountCents: 2500,
	})
	require.NoError(t, err)

	stored, err := repo.FindByOrg(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), stored.BalanceCents)
	assert.Equal(t, int64(2500), stored.UsedThisCycleCents)
}

// AC8: ResetCycle zeroes postpaid usage only for stale cycles and is
// idempotent.
func TestAccountRepositoryResetCycle(t *testing.T) {
	repo, db := newAccountRepo(t)
	ctx := context.Background()
	account := seedAccount(t, repo, "org-1", AccountModePostpaid, 0, 10000, 7000)

	// Age the cycle into the previous month.
	past := monthStartOf(time.Now().UTC()) - 3600
	require.NoError(t, db.Model(&Account{}).Where("id = ?", account.ID).
		Update("cycle_started_at", past).Error)

	now := monthStartOf(time.Now().UTC())
	count, err := repo.ResetCycle(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	stored, err := repo.FindByOrg(ctx, "org-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), stored.UsedThisCycleCents)
	assert.Equal(t, now, stored.CycleStartedAt)

	// Second pass: idempotent no-op.
	count, err = repo.ResetCycle(ctx, now)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// AC12: FindByOrg returns nil for an org without an account.
func TestAccountRepositoryFindByOrgAbsent(t *testing.T) {
	repo, _ := newAccountRepo(t)
	account, err := repo.FindByOrg(context.Background(), "no-such-org")
	require.NoError(t, err)
	assert.Nil(t, account)
}

// ListTransactions filters by type and time range, newest first.
func TestAccountRepositoryListTransactionsFilters(t *testing.T) {
	repo, _ := newAccountRepo(t)
	ctx := context.Background()
	account := seedAccount(t, repo, "org-1", AccountModePrepaid, 1000, 0, 0)

	_, _, err := repo.Recharge(ctx, account, 100, "k1", "", TransactionTypeRecharge)
	require.NoError(t, err)
	_, _, err = repo.Recharge(ctx, account, 200, "k2", "", TransactionTypeRecharge)
	require.NoError(t, err)
	err = repo.ApplyDeduction(ctx, DeductionSpec{AccountID: account.ID, ChargeID: "c1", AmountCents: 50})
	require.NoError(t, err)

	all, total, err := repo.ListTransactions(ctx, TransactionFilter{AccountID: account.ID})
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, all, 3)

	onlyRecharges, total, err := repo.ListTransactions(ctx, TransactionFilter{AccountID: account.ID, Type: TransactionTypeRecharge})
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	assert.Len(t, onlyRecharges, 2)

	// Pagination.
	page, total, err := repo.ListTransactions(ctx, TransactionFilter{AccountID: account.ID, Offset: 0, Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Len(t, page, 1)
}
