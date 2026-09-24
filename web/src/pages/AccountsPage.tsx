// Billing accounts page: per-org prepaid/postpaid accounts with
// recharge, quota editing and the transaction ledger.
// Implements docs/design/balance-quota.md FR1-FR6.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  formatCents,
  formatTime,
  type BillingAccount,
  type BillingTransaction,
  type PageMeta,
} from '../api';
import { useOrg } from '../org';
import { Dialog, ErrorBanner, Pagination, usePolling } from '../components';

interface AccountsResponse {
  response: { code: number; message: string };
  accounts: BillingAccount[];
  pageMeta?: PageMeta;
}

interface TransactionsResponse {
  response: { code: number; message: string };
  transactions: BillingTransaction[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

// modeBadge renders the prepaid/postpaid mode badge.
function modeBadge(mode: string) {
  return <span className={`badge ${mode}`} data-testid={`mode-badge-${mode}`}>{mode}</span>;
}

export default function AccountsPage() {
  const { orgId } = useOrg();
  const [accounts, setAccounts] = useState<BillingAccount[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [rechargeFor, setRechargeFor] = useState<BillingAccount | null>(null);
  const [quotaFor, setQuotaFor] = useState<BillingAccount | null>(null);
  const [detail, setDetail] = useState<BillingAccount | null>(null);
  const [creating, setCreating] = useState(false);

  const load = useCallback(async () => {
    setError('');
    try {
      const data = await api.get<AccountsResponse>('/api/v1/admin/billing/accounts', orgId);
      setAccounts(data.accounts || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load accounts');
    } finally {
      setLoading(false);
    }
  }, [orgId]);

  useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  usePolling(() => void load(), 60_000, true);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Billing Accounts</h1>
          <div className="subtitle">
            One account per organization: prepaid balance or postpaid monthly
            quota, with an append-only transaction ledger.
          </div>
        </div>
        <button data-testid="create-account-button" onClick={() => setCreating(true)}>
          Create Account
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">Loading…</div>
        ) : accounts.length === 0 ? (
          <div className="empty-state" data-testid="accounts-empty">
            No billing accounts yet — create the first one
          </div>
        ) : (
          <table className="data" data-testid="accounts-table">
            <thead>
              <tr>
                <th>Account</th>
                <th>Mode</th>
                <th>Balance / Quota</th>
                <th>Cycle start</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((a) => (
                <tr key={a.accountId} data-testid={`account-row-${a.accountId}`}>
                  <td>
                    <strong className="mono">{a.accountId}</strong>
                  </td>
                  <td>{modeBadge(a.mode)}</td>
                  <td>
                    {a.mode === 'prepaid' ? (
                      <span>
                        {formatCents(a.balanceCents)} {a.currency}
                      </span>
                    ) : (
                      <span>
                        {formatCents(a.usedThisCycleCents)} /{' '}
                        {Number(a.monthlyQuotaCents) > 0
                          ? formatCents(a.monthlyQuotaCents)
                          : 'unlimited'}{' '}
                        {a.currency}
                        {Number(a.monthlyQuotaCents) > 0 && (
                          <progress
                            value={a.quotaUsagePercent}
                            max={100}
                            data-testid={`quota-progress-${a.accountId}`}
                          />
                        )}
                      </span>
                    )}
                  </td>
                  <td>{formatTime(a.cycleStartedAt)}</td>
                  <td>
                    <button
                      className="link"
                      data-testid={`recharge-button-${a.accountId}`}
                      onClick={() => setRechargeFor(a)}
                    >
                      Recharge
                    </button>
                    <button
                      className="link"
                      data-testid={`quota-button-${a.accountId}`}
                      onClick={() => setQuotaFor(a)}
                    >
                      Set Quota
                    </button>
                    <button
                      className="link"
                      data-testid={`detail-button-${a.accountId}`}
                      onClick={() => setDetail(a)}
                    >
                      Detail
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {creating && (
        <CreateAccountDialog
          orgId={orgId}
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            void load();
          }}
        />
      )}
      {rechargeFor && (
        <RechargeDialog
          orgId={orgId}
          account={rechargeFor}
          onClose={() => setRechargeFor(null)}
          onDone={() => {
            setRechargeFor(null);
            void load();
          }}
        />
      )}
      {quotaFor && (
        <QuotaDialog
          orgId={orgId}
          account={quotaFor}
          onClose={() => setQuotaFor(null)}
          onDone={() => {
            setQuotaFor(null);
            void load();
          }}
        />
      )}
      {detail && (
        <AccountDetailDialog
          orgId={orgId}
          account={detail}
          onClose={() => setDetail(null)}
        />
      )}
    </div>
  );
}

// CreateAccountDialog creates the org's first account (FR1).
function CreateAccountDialog({
  orgId,
  onClose,
  onCreated,
}: {
  orgId: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  const [mode, setMode] = useState('prepaid');
  const [initialBalance, setInitialBalance] = useState('');
  const [quota, setQuota] = useState('');
  const [policy, setPolicy] = useState('block');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setBusy(true);
    setError('');
    try {
      const body: Record<string, unknown> = {
        mode,
        overdrawPolicy: policy,
      };
      if (mode === 'prepaid' && initialBalance !== '') {
        body.initialBalanceCents = Math.round(parseFloat(initialBalance) * 100);
      }
      if (mode === 'postpaid' && quota !== '') {
        body.monthlyQuotaCents = Math.round(parseFloat(quota) * 100);
      }
      await api.post('/api/v1/admin/billing/accounts', orgId, body);
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to create account');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog title="Create Billing Account" onClose={onClose} testId="create-account-dialog">
      {error && <ErrorBanner message={error} />}
      <label>
        Mode
        <select
          value={mode}
          data-testid="create-mode-select"
          onChange={(e) => setMode(e.target.value)}
        >
          <option value="prepaid">prepaid</option>
          <option value="postpaid">postpaid</option>
        </select>
      </label>
      {mode === 'prepaid' ? (
        <label>
          Opening balance
          <input
            type="number"
            min="0"
            step="0.01"
            placeholder="0.00"
            data-testid="create-initial-balance"
            value={initialBalance}
            onChange={(e) => setInitialBalance(e.target.value)}
          />
        </label>
      ) : (
        <>
          <label>
            Monthly quota (0 = unlimited)
            <input
              type="number"
              min="0"
              step="0.01"
              placeholder="0.00"
              data-testid="create-quota"
              value={quota}
              onChange={(e) => setQuota(e.target.value)}
            />
          </label>
          <label>
            Overdraw policy
            <select
              value={policy}
              data-testid="create-policy-select"
              onChange={(e) => setPolicy(e.target.value)}
            >
              <option value="block">block</option>
              <option value="warn">warn</option>
            </select>
          </label>
        </>
      )}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          data-testid="create-account-submit"
          disabled={busy}
          onClick={() => void submit()}
        >
          Create
        </button>
      </div>
    </Dialog>
  );
}

// RechargeDialog credits a prepaid balance; one idempotency key is
// generated per submit so retries converge (FR3).
function RechargeDialog({
  orgId,
  account,
  onClose,
  onDone,
}: {
  orgId: string;
  account: BillingAccount;
  onClose: () => void;
  onDone: () => void;
}) {
  const [amount, setAmount] = useState('');
  const [note, setNote] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setBusy(true);
    setError('');
    try {
      await api.post(
        `/api/v1/admin/billing/accounts/${account.accountId}/recharge`,
        orgId,
        {
          amountCents: Math.round(parseFloat(amount) * 100),
          idempotencyKey: `console-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`,
          note,
        },
      );
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to recharge');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      title={`Recharge — ${account.accountId}`}
      onClose={onClose}
      testId="recharge-dialog"
    >
      {error && <ErrorBanner message={error} />}
      <div className="muted">
        Current balance: {formatCents(account.balanceCents)} {account.currency}
      </div>
      <label>
        Amount ({account.currency})
        <input
          type="number"
          min="0.01"
          step="0.01"
          data-testid="recharge-amount"
          value={amount}
          onChange={(e) => setAmount(e.target.value)}
        />
      </label>
      <label>
        Note
        <input
          type="text"
          maxLength={128}
          placeholder="optional reference"
          data-testid="recharge-note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
      </label>
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          data-testid="recharge-submit"
          disabled={busy}
          onClick={() => void submit()}
        >
          Recharge
        </button>
      </div>
    </Dialog>
  );
}

// QuotaDialog edits mode, quota and overdraw policy (FR2).
function QuotaDialog({
  orgId,
  account,
  onClose,
  onDone,
}: {
  orgId: string;
  account: BillingAccount;
  onClose: () => void;
  onDone: () => void;
}) {
  const [mode, setMode] = useState(account.mode);
  const [quota, setQuota] = useState(
    Number(account.monthlyQuotaCents) > 0
      ? (Number(account.monthlyQuotaCents) / 100).toFixed(2)
      : '0',
  );
  const [policy, setPolicy] = useState(account.overdrawPolicy || 'block');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setBusy(true);
    setError('');
    try {
      await api.put(`/api/v1/admin/billing/accounts/${account.accountId}`, orgId, {
        mode,
        monthlyQuotaCents: Math.round(parseFloat(quota || '0') * 100),
        overdrawPolicy: policy,
      });
      onDone();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to update account');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog title={`Set Quota — ${account.accountId}`} onClose={onClose} testId="quota-dialog">
      {error && <ErrorBanner message={error} />}
      <label>
        Mode
        <select
          value={mode}
          data-testid="quota-mode-select"
          onChange={(e) => setMode(e.target.value)}
        >
          <option value="prepaid">prepaid</option>
          <option value="postpaid">postpaid</option>
        </select>
      </label>
      <label>
        Monthly quota ({account.currency}, 0 = unlimited)
        <input
          type="number"
          min="0"
          step="0.01"
          data-testid="quota-input"
          value={quota}
          onChange={(e) => setQuota(e.target.value)}
        />
      </label>
      <label>
        Overdraw policy
        <select
          value={policy}
          data-testid="quota-policy-select"
          onChange={(e) => setPolicy(e.target.value)}
        >
          <option value="block">block</option>
          <option value="warn">warn</option>
        </select>
      </label>
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button data-testid="quota-submit" disabled={busy} onClick={() => void submit()}>
          Save
        </button>
      </div>
    </Dialog>
  );
}

// AccountDetailDialog shows the account snapshot and its ledger with
// a type filter (FR4/FR6).
function AccountDetailDialog({
  orgId,
  account,
  onClose,
}: {
  orgId: string;
  account: BillingAccount;
  onClose: () => void;
}) {
  const [transactions, setTransactions] = useState<BillingTransaction[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [typeFilter, setTypeFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    setLoading(true);
    const params = new URLSearchParams({
      account_id: account.accountId,
      'page.offset': String(offset),
      'page.limit': String(PAGE_SIZE),
    });
    if (typeFilter) params.set('type', typeFilter);
    api
      .get<TransactionsResponse>(
        `/api/v1/admin/billing/transactions?${params.toString()}`,
        orgId,
      )
      .then((data) => {
        setTransactions(data.transactions || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
      })
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load transactions'))
      .finally(() => setLoading(false));
  }, [orgId, account.accountId, offset, typeFilter]);

  usePolling(
    () => {
      const params = new URLSearchParams({
        account_id: account.accountId,
        'page.offset': String(offset),
        'page.limit': String(PAGE_SIZE),
      });
      if (typeFilter) params.set('type', typeFilter);
      api
        .get<TransactionsResponse>(
          `/api/v1/admin/billing/transactions?${params.toString()}`,
          orgId,
        )
        .then((data) => setTransactions(data.transactions || []))
        .catch(() => {
          // Polling errors keep the last snapshot.
        });
    },
    60_000,
    true,
  );

  return (
    <Dialog title={`Account — ${account.accountId}`} onClose={onClose} testId="account-detail">
      {error && <ErrorBanner message={error} />}
      <div className="detail-grid">
        <div>
          <div className="muted">Mode</div>
          <div>{modeBadge(account.mode)}</div>
        </div>
        <div>
          <div className="muted">Balance</div>
          <div data-testid="detail-balance">
            {formatCents(account.balanceCents)} {account.currency}
          </div>
        </div>
        <div>
          <div className="muted">Monthly quota</div>
          <div data-testid="detail-quota">
            {Number(account.monthlyQuotaCents) > 0
              ? `${formatCents(account.monthlyQuotaCents)} ${account.currency}`
              : 'unlimited'}
          </div>
        </div>
        <div>
          <div className="muted">Used this cycle</div>
          <div data-testid="detail-used">
            {formatCents(account.usedThisCycleCents)} {account.currency}
          </div>
        </div>
        <div>
          <div className="muted">Remaining</div>
          <div data-testid="detail-remaining">
            {formatCents(account.remainingCents)} {account.currency}
          </div>
        </div>
        <div>
          <div className="muted">Cycle start</div>
          <div>{formatTime(account.cycleStartedAt)}</div>
        </div>
      </div>

      <h3>Transactions</h3>
      <label>
        Type
        <select
          value={typeFilter}
          data-testid="transaction-type-filter"
          onChange={(e) => {
            setOffset(0);
            setTypeFilter(e.target.value);
          }}
        >
          <option value="">all</option>
          <option value="recharge">recharge</option>
          <option value="deduction">deduction</option>
          <option value="refund">refund</option>
        </select>
      </label>
      {loading ? (
        <div className="loading">Loading…</div>
      ) : transactions.length === 0 ? (
        <div className="empty-state" data-testid="transactions-empty">
          No transactions in this range
        </div>
      ) : (
        <table className="data" data-testid="transactions-table">
          <thead>
            <tr>
              <th>Time</th>
              <th>Type</th>
              <th>Amount</th>
              <th>Balance after</th>
              <th>Reference</th>
            </tr>
          </thead>
          <tbody>
            {transactions.map((tx) => (
              <tr key={tx.transactionId} data-testid={`transaction-row-${tx.transactionId}`}>
                <td>{formatTime(tx.createdAt)}</td>
                <td>
                  <span className={`badge ${tx.type}`}>{tx.type}</span>
                </td>
                <td>{formatCents(tx.amountCents)}</td>
                <td>{formatCents(tx.balanceAfterCents)}</td>
                <td className="mono">{tx.reference || tx.idempotencyKey}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {total > PAGE_SIZE && (
        <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
      )}
    </Dialog>
  );
}
