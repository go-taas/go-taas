package fvt

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	billingv1 "github.com/go-taas/go-taas/proto/taas/billing/v1"
	"github.com/go-taas/go-taas/services/billing"
)

// TestFVTBalanceQuotaAccounts walks the account management acceptance
// criteria through the production gateway path: create with opening
// balance (AC1), validation matrices (10509/10510), recharge/refund
// idempotency (AC2/AC3), the ledger query (AC9), org scoping (AC10)
// and GetBalance.
func TestFVTBalanceQuotaAccounts(t *testing.T) {
	env := newBillingEnv(t)

	// AC1: create a prepaid account with an opening balance.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/billing/accounts", map[string]any{
		"mode": "prepaid", "overdrawPolicy": "block", "initialBalanceCents": 10000,
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	account, _ := body["account"].(map[string]any)
	require.NotNil(t, account)
	accountID, _ := account["accountId"].(string)
	require.NotEmpty(t, accountID)
	assert.Equal(t, "prepaid", account["mode"])
	assert.EqualValues(t, "10000", fmt.Sprint(account["balanceCents"]))
	assert.EqualValues(t, "10000", fmt.Sprint(account["remainingCents"]))
	assert.Equal(t, "USD", account["currency"])

	// The opening balance is one recharge ledger row.
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/billing/transactions?account_id=%s", accountID), nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	txs, _ := body["transactions"].([]any)
	require.Len(t, txs, 1)
	opening, _ := txs[0].(map[string]any)
	assert.Equal(t, "recharge", opening["type"])
	assert.EqualValues(t, "10000", fmt.Sprint(opening["amountCents"]))

	// One account per org: a second create is 10509.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/billing/accounts", map[string]any{
		"mode": "postpaid", "overdrawPolicy": "block",
	}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code, "body: %v", body)
	assert.EqualValues(t, float64(10509), body["code"])

	// Validation matrix: bad mode is 10509.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/billing/accounts", map[string]any{
		"mode": "hybrid", "overdrawPolicy": "block",
	}, "org-a")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10509), body["code"])

	// AC2: recharge credits the balance.
	code, body = env.call(t, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/billing/accounts/%s/recharge", accountID), map[string]any{
			"amountCents": 2500, "idempotencyKey": "fvt-rch-1", "note": "top up",
		}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	acc, _ := body["account"].(map[string]any)
	assert.EqualValues(t, "12500", fmt.Sprint(acc["balanceCents"]))

	// AC3: a replayed key converges without a second credit.
	code, body = env.call(t, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/billing/accounts/%s/recharge", accountID), map[string]any{
			"amountCents": 2500, "idempotencyKey": "fvt-rch-1",
		}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	acc, _ = body["account"].(map[string]any)
	assert.EqualValues(t, "12500", fmt.Sprint(acc["balanceCents"]))

	// A conflicting amount on the same key is 10510.
	code, body = env.call(t, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/billing/accounts/%s/recharge", accountID), map[string]any{
			"amountCents": 999, "idempotencyKey": "fvt-rch-1",
		}, "org-fvt")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10510), body["code"])

	// Refund debits the balance.
	code, body = env.call(t, http.MethodPost,
		fmt.Sprintf("/api/v1/admin/billing/accounts/%s/refund", accountID), map[string]any{
			"amountCents": 500, "idempotencyKey": "fvt-ref-1",
		}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	acc, _ = body["account"].(map[string]any)
	assert.EqualValues(t, "12000", fmt.Sprint(acc["balanceCents"]))

	// AC9: the ledger lists all rows with type filters.
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/billing/transactions?account_id=%s&type=refund", accountID), nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	txs, _ = body["transactions"].([]any)
	assert.Len(t, txs, 1)

	// GetBalance returns the snapshot with the legacy double.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/billing/balance", nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	assert.Equal(t, "prepaid", body["mode"])
	assert.EqualValues(t, "12000", fmt.Sprint(body["balanceCents"]))
	assert.InDelta(t, 120.0, body["balance"], 1e-9)

	// AC10: org-b cannot touch org-fvt's account.
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/billing/accounts/%s", accountID), nil, "org-b")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10503), body["code"])

	// org-b has no accounts.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/billing/accounts", nil, "org-b")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	accounts, _ := body["accounts"].([]any)
	assert.Empty(t, accounts)

	// A missing org header is unauthorized.
	code, body = env.call(t, http.MethodGet, "/api/v1/admin/billing/accounts", nil, "")
	assert.NotEqual(t, http.StatusOK, code)
	assert.EqualValues(t, float64(10001), body["code"])
}

// TestFVTBalanceQuotaSettlement walks the settlement-driven deduction
// path (AC4/AC5): a priced charge deducts the prepaid balance inside
// the same transaction, a redelivered settlement converges without a
// second deduction, and a postpaid account accrues cycle usage with
// the balance untouched.
func TestFVTBalanceQuotaSettlement(t *testing.T) {
	env := newBillingEnv(t)
	ctx := context.Background()

	now := time.Now().UTC()
	hour := now.Add(-3 * time.Hour).Truncate(time.Hour)
	priceFrom := hour.Add(-time.Hour).Unix()

	// A price so charges are non-zero: 4/1M on prompt tokens.
	code, body := env.call(t, http.MethodPut, "/api/v1/admin/billing/prices", map[string]any{
		"modelId": "model-a", "acceleratorType": "A800",
		"inputPricePerMillion": 4, "outputPricePerMillion": 8,
		"effectiveFrom": priceFrom,
	}, "")
	require.Equal(t, http.StatusOK, code, "body: %v", body)

	// A prepaid account with a 100.00 balance.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/billing/accounts", map[string]any{
		"mode": "prepaid", "overdrawPolicy": "block", "initialBalanceCents": 10000,
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	account, _ := body["account"].(map[string]any)
	prepaidID, _ := account["accountId"].(string)

	// 600K prompt tokens → 2.4 → 240 cents.
	env.publishMeteringEvent(t, "bq-r1", "org-fvt", "key-1", "model-a", "A800", hour.Add(10*time.Minute), 600_000, 0)
	env.waitForLines(t, 1)
	env.publishSettlement(t, "bq-ur-1", "key-1", "org-fvt", hour.Unix())
	env.waitForCharges(t, 1)
	env.waitForIdle(t)

	// AC4: the balance dropped by exactly the charge.
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/billing/accounts/%s", prepaidID), nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	account, _ = body["account"].(map[string]any)
	assert.EqualValues(t, "9760", fmt.Sprint(account["balanceCents"]), "10000 - 240")

	// One deduction ledger row referencing the charge.
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/billing/transactions?account_id=%s&type=deduction", prepaidID), nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	txs, _ := body["transactions"].([]any)
	require.Len(t, txs, 1)
	deduction, _ := txs[0].(map[string]any)
	assert.EqualValues(t, "240", fmt.Sprint(deduction["amountCents"]))
	assert.EqualValues(t, "9760", fmt.Sprint(deduction["balanceAfterCents"]))

	// AC4: a redelivered settlement converges — no second deduction.
	env.publishSettlement(t, "bq-ur-1", "key-1", "org-fvt", hour.Unix())
	env.waitForIdle(t)
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/billing/accounts/%s", prepaidID), nil, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	account, _ = body["account"].(map[string]any)
	assert.EqualValues(t, "9760", fmt.Sprint(account["balanceCents"]), "redelivery does not double-deduct")

	// AC5: a postpaid account accrues cycle usage, balance untouched.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/billing/accounts", map[string]any{
		"mode": "postpaid", "overdrawPolicy": "block", "monthlyQuotaCents": 50000,
	}, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	account, _ = body["account"].(map[string]any)
	postpaidID, _ := account["accountId"].(string)

	env.publishMeteringEvent(t, "bq-r2", "org-a", "key-2", "model-a", "A800", hour.Add(10*time.Minute), 600_000, 0)
	env.waitForLines(t, 2)
	env.publishSettlement(t, "bq-ur-2", "key-2", "org-a", hour.Unix())
	env.waitForCharges(t, 2)
	env.waitForIdle(t)

	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/billing/accounts/%s", postpaidID), nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	account, _ = body["account"].(map[string]any)
	assert.EqualValues(t, "0", fmt.Sprint(account["balanceCents"]), "postpaid balance untouched")
	assert.EqualValues(t, "240", fmt.Sprint(account["usedThisCycleCents"]), "usage accrues")
	assert.EqualValues(t, "49760", fmt.Sprint(account["remainingCents"]))
	assert.EqualValues(t, float64(0), account["quotaUsagePercent"], "240/50000 < 1%")

	// AC8: the cycle reset zeroes usage for aged cycles.
	past := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Unix()
	require.NoError(t, env.db.Exec("UPDATE accounts SET cycle_started_at = ? WHERE id = ?", past, postpaidID).Error)
	runner := billing.NewCycleResetRunner(billing.NewAccountRepository(env.db), time.Minute)
	require.NoError(t, runner.ResetOnce(ctx))
	code, body = env.call(t, http.MethodGet,
		fmt.Sprintf("/api/v1/admin/billing/accounts/%s", postpaidID), nil, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	account, _ = body["account"].(map[string]any)
	assert.EqualValues(t, "0", fmt.Sprint(account["usedThisCycleCents"]), "usage zeroed after reset")
}

// TestFVTCheckFunds walks the internal gating truth table (AC6/AC7/AC12)
// through the gRPC service directly (CheckFunds has no HTTP binding).
func TestFVTCheckFunds(t *testing.T) {
	env := newBillingEnv(t)
	ctx := context.Background()

	// No account: ungated (AC12).
	resp, err := env.svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{OrganizationId: "org-fvt"})
	require.NoError(t, err)
	assert.True(t, resp.GetAllowed())
	assert.Empty(t, resp.GetMode())

	// Prepaid with balance: allowed.
	code, body := env.call(t, http.MethodPost, "/api/v1/admin/billing/accounts", map[string]any{
		"mode": "prepaid", "overdrawPolicy": "block", "initialBalanceCents": 100,
	}, "org-fvt")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	resp, err = env.svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{OrganizationId: "org-fvt"})
	require.NoError(t, err)
	assert.True(t, resp.GetAllowed())
	assert.Equal(t, "prepaid", resp.GetMode())

	// Postpaid at quota under block: blocked; under warn: allowed.
	code, body = env.call(t, http.MethodPost, "/api/v1/admin/billing/accounts", map[string]any{
		"mode": "postpaid", "overdrawPolicy": "block", "monthlyQuotaCents": 1000,
	}, "org-a")
	require.Equal(t, http.StatusOK, code, "body: %v", body)
	require.NoError(t, env.db.Exec("UPDATE accounts SET used_this_cycle_cents = 1000 WHERE organization_id = ?", "org-a").Error)
	resp, err = env.svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.False(t, resp.GetAllowed())
	assert.Equal(t, "insufficient_funds", resp.GetReason())

	require.NoError(t, env.db.Exec("UPDATE accounts SET overdraw_policy = 'warn' WHERE organization_id = ?", "org-a").Error)
	resp, err = env.svc.CheckFunds(ctx, &billingv1.CheckFundsRequest{OrganizationId: "org-a"})
	require.NoError(t, err)
	assert.True(t, resp.GetAllowed())
}
