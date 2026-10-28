// Billing accounts page: per-org prepaid/postpaid accounts with
// recharge, quota editing and the transaction ledger.
// Implements docs/design/balance-quota.md FR1-FR6.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  formatTime,
  type BillingAccount,
  type BillingTransaction,
  type PageMeta,
} from '../api';
import { displayCurrencyLabel, formatMoney } from '../display';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
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
  const { t } = useI18n();
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
      setError(e instanceof Error ? e.message : t('accounts.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, t]);

  useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  usePolling(() => void load(), 60_000, true);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('accounts.title')}</h1>
          <div className="subtitle">{t('accounts.subtitle')}</div>
        </div>
        <button data-testid="create-account-button" onClick={() => setCreating(true)}>
          {t('accounts.create')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : accounts.length === 0 ? (
          <div className="empty-state" data-testid="accounts-empty">
            {t('accounts.empty')}
          </div>
        ) : (
          <table className="data" data-testid="accounts-table">
            <thead>
              <tr>
                <th>{t('accounts.colAccount')}</th>
                <th>{t('accounts.colMode')}</th>
                <th>{t('accounts.colBalance')}</th>
                <th>{t('accounts.colSpendLimit')}</th>
                <th>{t('accounts.colCycleStart')}</th>
                <th>{t('common.actions')}</th>
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
                      <span data-testid={`account-balance-${a.accountId}`}>
                        {formatMoney(a.balanceCents)}
                      </span>
                    ) : (
                      <span>
                        {formatMoney(a.usedThisCycleCents)} /{' '}
                        {Number(a.monthlyQuotaCents) > 0
                          ? formatMoney(a.monthlyQuotaCents)
                          : t('common.unlimited')}
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
                  <td data-testid={`spend-limit-cell-${a.accountId}`}>
                    {Number(a.monthlySpendLimitCents || '0') > 0 ? (
                      <span>
                        {formatMoney(a.spentThisCycleCents || '0')} /{' '}
                        {formatMoney(a.monthlySpendLimitCents || '0')}
                        <progress
                          value={a.spendLimitUsagePercent || 0}
                          max={100}
                          data-testid={`spend-limit-progress-${a.accountId}`}
                        />
                      </span>
                    ) : (
                      <span className="muted">{t('accounts.noSpendLimit')}</span>
                    )}
                  </td>
                  <td>{formatTime(a.cycleStartedAt)}</td>
                  <td>
                    <button
                      className="link"
                      data-testid={`recharge-button-${a.accountId}`}
                      onClick={() => setRechargeFor(a)}
                    >
                      {t('accounts.recharge')}
                    </button>
                    <button
                      className="link"
                      data-testid={`quota-button-${a.accountId}`}
                      onClick={() => setQuotaFor(a)}
                    >
                      {t('accounts.setQuota')}
                    </button>
                    <button
                      className="link"
                      data-testid={`detail-button-${a.accountId}`}
                      onClick={() => setDetail(a)}
                    >
                      {t('common.detail')}
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
  const { t } = useI18n();
  const [mode, setMode] = useState('prepaid');
  const [initialBalance, setInitialBalance] = useState('');
  const [quota, setQuota] = useState('');
  const [policy, setPolicy] = useState('block');
  const [spendLimit, setSpendLimit] = useState('');
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
      if (spendLimit !== '') {
        body.monthlySpendLimitCents = Math.round(parseFloat(spendLimit) * 100);
      }
      await api.post('/api/v1/admin/billing/accounts', orgId, body);
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('accounts.createFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog title={t('accounts.createTitle')} onClose={onClose} testId="create-account-dialog">
      {error && <ErrorBanner message={error} />}
      <label>
        {t('accounts.fieldMode')}
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
          {t('accounts.fieldOpeningBalance')}
          <input
            type="number"
            min="0"
            step="0.01"
            placeholder={t('accounts.placeholderAmount')}
            data-testid="create-initial-balance"
            value={initialBalance}
            onChange={(e) => setInitialBalance(e.target.value)}
          />
        </label>
      ) : (
        <>
          <label>
            {t('accounts.fieldMonthlyQuota')}
            <input
              type="number"
              min="0"
              step="0.01"
              placeholder={t('accounts.placeholderAmount')}
              data-testid="create-quota"
              value={quota}
              onChange={(e) => setQuota(e.target.value)}
            />
          </label>
          <label>
            {t('accounts.fieldOverdraw')}
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
      <label>
        {t('accounts.fieldSpendLimit')}
        <input
          type="number"
          min="0"
          step="0.01"
          placeholder={t('accounts.placeholderAmount')}
          data-testid="spend-limit-input"
          value={spendLimit}
          onChange={(e) => setSpendLimit(e.target.value)}
        />
      </label>
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="create-account-submit"
          disabled={busy}
          onClick={() => void submit()}
        >
          {t('common.create')}
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
  const { t } = useI18n();
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
      setError(e instanceof Error ? e.message : t('accounts.rechargeFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      title={t('accounts.rechargeTitle', { accountId: account.accountId })}
      onClose={onClose}
      testId="recharge-dialog"
    >
      {error && <ErrorBanner message={error} />}
      <div className="muted">
        {t('accounts.rechargeBalance', { balance: formatMoney(account.balanceCents), currency: displayCurrencyLabel() })}
      </div>
      <label>
        {t('accounts.fieldAmount', { currency: displayCurrencyLabel() })}
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
        {t('accounts.fieldNote')}
        <input
          type="text"
          maxLength={128}
          placeholder={t('accounts.placeholderNote')}
          data-testid="recharge-note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
      </label>
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="recharge-submit"
          disabled={busy}
          onClick={() => void submit()}
        >
          {t('accounts.recharge')}
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
  const { t } = useI18n();
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
      setError(e instanceof Error ? e.message : t('accounts.updateFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog title={t('accounts.quotaTitle', { accountId: account.accountId })} onClose={onClose} testId="quota-dialog">
      {error && <ErrorBanner message={error} />}
      <label>
        {t('accounts.fieldMode')}
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
        {t('accounts.fieldMonthlyQuota2', { currency: displayCurrencyLabel() })}
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
        {t('accounts.fieldOverdraw')}
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
          {t('common.cancel')}
        </button>
        <button data-testid="quota-submit" disabled={busy} onClick={() => void submit()}>
          {t('common.save')}
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
  const { t } = useI18n();
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
      .catch((e) => setError(e instanceof Error ? e.message : t('accounts.transactionsFailed')))
      .finally(() => setLoading(false));
  }, [orgId, account.accountId, offset, typeFilter, t]);

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
    <Dialog title={t('accounts.detailTitle', { accountId: account.accountId })} onClose={onClose} testId="account-detail">
      {error && <ErrorBanner message={error} />}
      <div className="detail-grid">
        <div>
          <div className="muted">{t('accounts.fieldMode')}</div>
          <div>{modeBadge(account.mode)}</div>
        </div>
        <div>
          <div className="muted">{t('accounts.balance')}</div>
          <div data-testid="detail-balance">
            {formatMoney(account.balanceCents)}
          </div>
        </div>
        <div>
          <div className="muted">{t('accounts.monthlyQuota')}</div>
          <div data-testid="detail-quota">
            {Number(account.monthlyQuotaCents) > 0
              ? formatMoney(account.monthlyQuotaCents)
              : t('common.unlimited')}
          </div>
        </div>
        <div>
          <div className="muted">{t('accounts.usedThisCycle')}</div>
          <div data-testid="detail-used">
            {formatMoney(account.usedThisCycleCents)}
          </div>
        </div>
        <div>
          <div className="muted">{t('accounts.remaining')}</div>
          <div data-testid="detail-remaining">
            {formatMoney(account.remainingCents)}
          </div>
        </div>
        <div>
          <div className="muted">{t('accounts.cycleStart')}</div>
          <div>{formatTime(account.cycleStartedAt)}</div>
        </div>
      </div>

      <h3>{t('accounts.transactions')}</h3>
      <label>
        {t('accounts.fieldType')}
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
        <div className="loading">{t('common.loading')}</div>
      ) : transactions.length === 0 ? (
        <div className="empty-state" data-testid="transactions-empty">
          {t('accounts.transactionsEmpty')}
        </div>
      ) : (
        <table className="data" data-testid="transactions-table">
          <thead>
            <tr>
              <th>{t('accounts.colTime')}</th>
              <th>{t('accounts.colType')}</th>
              <th>{t('accounts.colAmount')}</th>
              <th>{t('accounts.colBalanceAfter')}</th>
              <th>{t('accounts.colReference')}</th>
            </tr>
          </thead>
          <tbody>
            {transactions.map((tx) => (
              <tr key={tx.transactionId} data-testid={`transaction-row-${tx.transactionId}`}>
                <td>{formatTime(tx.createdAt)}</td>
                <td>
                  <span className={`badge ${tx.type}`}>{tx.type}</span>
                </td>
                <td>{formatMoney(tx.amountCents)}</td>
                <td>{formatMoney(tx.balanceAfterCents)}</td>
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
