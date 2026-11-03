// Organization balances: the platform administrator's money page.
//
// It answers the two questions an operator has about a tenant's funds — "how
// much is left" and "fund it" — in one place. Every organization is listed,
// including the ones without a billing account yet: a tenant that has never
// been funded has no account row, and hiding it would force a create-then-fund
// two-step ritual the recharge endpoint already collapses into one.
//
// Every movement recorded here carries the source the operator chose and the
// operator's own id, which is what makes the ledger answer "who credited this
// tenant, when and why" without cross-reading the audit log.

import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ApiError,
  formatTime,
  type BillingTransaction,
  type ListOrganizationBalancesResponse,
  type OrganizationBalance,
} from '../api';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination, RowActionsMenu, usePolling } from '../components';
import { formatMoney, displayCurrencyLabel } from '../display';

const PAGE_SIZE = 20;

/** MovementSource is a source an operator may set explicitly. */
type MovementSource = 'admin_manual' | 'admin_correction';

interface LedgerResponse {
  response: { code: number; message: string };
  transactions?: BillingTransaction[];
  pageMeta?: { total?: string };
}

export default function OrganizationBalancesPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t, tEnum } = useI18n();
  const [rows, setRows] = useState<OrganizationBalance[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [mode, setMode] = useState('');
  const [query, setQuery] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [fund, setFund] = useState<{ balance: OrganizationBalance; direction: 'credit' | 'debit' } | null>(null);
  const [ledger, setLedger] = useState<OrganizationBalance | null>(null);

  const load = useCallback(async () => {
    setError('');
    try {
      const params = new URLSearchParams({
        'page.offset': String(offset),
        'page.limit': String(PAGE_SIZE),
      });
      if (mode) params.set('mode', mode);
      if (query.trim()) params.set('query', query.trim());
      const data = await api.get<ListOrganizationBalancesResponse>(
        `/api/v1/admin/billing/organization-balances?${params.toString()}`,
        orgId,
      );
      setRows(data.balances || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('orgBalances.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, offset, mode, query, t]);

  useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  usePolling(() => void load(), 60_000, true);

  const summary = useMemo(() => {
    const funded = rows.filter((r) => r.hasAccount);
    const prepaid = funded.filter((r) => r.mode === 'prepaid');
    const totalPrepaid = prepaid.reduce((sum, r) => sum + Number(r.balanceCents || 0), 0);
    const noAccount = rows.length - funded.length;
    return { funded: funded.length, prepaid: prepaid.length, totalPrepaid, noAccount };
  }, [rows]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('orgBalances.title')}</h1>
          <div className="subtitle">{t('orgBalances.subtitle', { currency: displayCurrencyLabel() })}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="summary-strip" data-testid="org-balances-summary">
        <span>
          <span className="stat-label">{t('orgBalances.summaryFunded')}</span>
          <strong>{summary.funded}</strong>
        </span>
        <span>
          <span className="stat-label">{t('orgBalances.summaryNoAccount')}</span>
          <strong>{summary.noAccount}</strong>
        </span>
        <span>
          <span className="stat-label">{t('orgBalances.summaryPrepaid')}</span>
          <strong>{formatMoney(summary.totalPrepaid)}</strong>
        </span>
      </div>

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <select
          data-testid="org-balances-mode-filter"
          value={mode}
          onChange={(e) => {
            setMode(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('orgBalances.allModes')}</option>
          <option value="prepaid">{tEnum('state', 'prepaid')}</option>
          <option value="postpaid">{tEnum('state', 'postpaid')}</option>
        </select>
        <input
          type="text"
          data-testid="org-balances-search"
          placeholder={t('orgBalances.searchPlaceholder')}
          value={query}
          onChange={(e) => {
            setQuery(e.target.value);
            setOffset(0);
          }}
        />
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : rows.length === 0 ? (
          <div className="empty-state" data-testid="org-balances-empty">
            {t('orgBalances.empty')}
          </div>
        ) : (
          <table className="data" data-testid="org-balances-table">
            <thead>
              <tr>
                <th>{t('orgBalances.colOrganization')}</th>
                <th>{t('orgBalances.colMode')}</th>
                <th>{t('orgBalances.colBalance')}</th>
                <th>{t('orgBalances.colSpentCycle')}</th>
                <th className="col-actions">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.organizationId} data-testid={`org-balance-row-${row.organizationId}`}>
                  <td>
                    <strong>{row.organizationName || row.organizationId}</strong>
                    <div className="muted small mono" title={row.organizationId}>
                      {row.organizationId}
                    </div>
                    {row.organizationState !== 'active' && (
                      <span className="badge disabled">{tEnum('state', row.organizationState)}</span>
                    )}
                  </td>
                  <td>
                    {row.hasAccount ? (
                      <span className={`badge ${row.mode}`} data-testid={`org-mode-${row.organizationId}`}>
                        {tEnum('state', row.mode)}
                      </span>
                    ) : (
                      <span className="muted small" data-testid={`org-mode-${row.organizationId}`}>
                        {t('orgBalances.noAccount')}
                      </span>
                    )}
                  </td>
                  <td data-testid={`org-balance-${row.organizationId}`}>
                    {row.hasAccount ? (
                      row.mode === 'postpaid' ? (
                        <span>
                          {formatMoney(row.usedThisCycleCents)} /{' '}
                          {Number(row.monthlyQuotaCents) > 0
                            ? formatMoney(row.monthlyQuotaCents)
                            : t('common.unlimited')}
                        </span>
                      ) : (
                        <span>{formatMoney(row.balanceCents, row.currency)}</span>
                      )
                    ) : (
                      <span className="muted">—</span>
                    )}
                  </td>
                  <td data-testid={`org-spent-${row.organizationId}`}>
                    {row.hasAccount ? formatMoney(row.spentThisCycleCents, row.currency) : '—'}
                    {row.hasAccount && formatTime(row.cycleStartedAt) !== '—' && (
                      <div className="muted small">
                        {t('orgBalances.cycleFrom', { date: formatTime(row.cycleStartedAt) })}
                      </div>
                    )}
                  </td>
                  <td>
                    <RowActionsMenu
                      label={t('common.actionsFor', {
                        name: row.organizationName || row.organizationId,
                      })}
                      testId={`org-actions-${row.organizationId}`}
                      actions={[
                        {
                          key: `org-recharge-${row.organizationId}`,
                          label: t('orgBalances.recharge'),
                          onClick: () => setFund({ balance: row, direction: 'credit' }),
                        },
                        {
                          key: `org-debit-${row.organizationId}`,
                          label: t('orgBalances.debit'),
                          danger: true,
                          disabled: !row.hasAccount || row.mode !== 'prepaid',
                          title: !row.hasAccount || row.mode !== 'prepaid'
                            ? t('orgBalances.debitPrepaidOnly')
                            : undefined,
                          onClick: () => setFund({ balance: row, direction: 'debit' }),
                        },
                        {
                          key: `org-ledger-${row.organizationId}`,
                          label: t('orgBalances.ledger'),
                          disabled: !row.hasAccount,
                          onClick: () => setLedger(row),
                        },
                      ]}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {total > PAGE_SIZE && (
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        )}
      </div>

      {fund && (
        <FundDialog
          orgId={orgId}
          balance={fund.balance}
          direction={fund.direction}
          onClose={() => setFund(null)}
          onDone={() => {
            setFund(null);
            void load();
          }}
        />
      )}
      {ledger && <LedgerDialog orgId={orgId} balance={ledger} onClose={() => setLedger(null)} />}
    </div>
  );
}

/**
 * FundDialog credits or debits one organization's prepaid balance.
 *
 * The two directions differ on purpose: a credit is a top-up and its reason is
 * optional, while a debit is a correction — it requires a reason and cannot
 * exceed the balance, because a negative balance is a settlement-lag state and
 * never something an operator should create by hand.
 */
function FundDialog({
  orgId,
  balance,
  direction,
  onClose,
  onDone,
}: {
  orgId: string;
  balance: OrganizationBalance;
  direction: 'credit' | 'debit';
  onClose: () => void;
  onDone: () => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const [amount, setAmount] = useState('');
  const [source, setSource] = useState<MovementSource>('admin_manual');
  const [note, setNote] = useState('');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const isDebit = direction === 'debit';
  const cents = Math.round(Number(amount || '0') * 100);
  const invalid = !Number.isFinite(cents) || cents <= 0 || (isDebit && !note.trim());

  const submit = async () => {
    setSaving(true);
    setError('');
    try {
      const path = isDebit
        ? `/api/v1/admin/billing/organizations/${balance.organizationId}/debit`
        : `/api/v1/admin/billing/organizations/${balance.organizationId}/recharge`;
      await api.post(path, orgId, {
        amountCents: cents,
        idempotencyKey: `${isDebit ? 'debit' : 'recharge'}-${balance.organizationId}-${Date.now()}`,
        note: note.trim(),
        source,
      });
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('orgBalances.fundFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      title={t(isDebit ? 'orgBalances.debitTitle' : 'orgBalances.rechargeTitle', {
        organization: balance.organizationName || balance.organizationId,
      })}
      onClose={onClose}
      testId="org-fund-dialog"
    >
      {error && <ErrorBanner message={error} />}
      <div className="form">
        <p className="muted" data-testid="org-fund-current">
          {balance.hasAccount
            ? t('orgBalances.currentBalance', {
                balance: formatMoney(balance.balanceCents, balance.currency),
                mode: balance.mode,
              })
            : t('orgBalances.willCreate')}
        </p>
        <label>
          {t('orgBalances.amount', { currency: displayCurrencyLabel() })}
          <input
            data-testid="org-fund-amount"
            inputMode="decimal"
            value={amount}
            placeholder="0.00"
            onChange={(e) => setAmount(e.target.value)}
          />
        </label>
        <label>
          {t('orgBalances.source')}
          <select
            data-testid="org-fund-source"
            value={source}
            onChange={(e) => setSource(e.target.value as MovementSource)}
          >
            <option value="admin_manual">{t('orgBalances.sourceAdminManual')}</option>
            <option value="admin_correction">{t('orgBalances.sourceAdminCorrection')}</option>
          </select>
          <span className="field-hint">{t('orgBalances.sourceHint')}</span>
        </label>
        <label>
          {isDebit ? t('orgBalances.reasonRequired') : t('orgBalances.noteOptional')}
          <input
            data-testid="org-fund-note"
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
        </label>
        <p className="muted small">{t('orgBalances.auditHint')}</p>
      </div>
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          className={isDebit ? 'danger' : ''}
          data-testid="org-fund-submit"
          disabled={saving || invalid}
          onClick={() => void submit()}
        >
          {t(isDebit ? 'orgBalances.debit' : 'orgBalances.recharge')}
        </button>
      </div>
    </Dialog>
  );
}

/** LedgerDialog shows one organization's movements, newest first. */
function LedgerDialog({
  orgId,
  balance,
  onClose,
}: {
  orgId: string;
  balance: OrganizationBalance;
  onClose: () => void;
}) {
  const api = useApi();
  const { t, tEnum } = useI18n();
  const [rows, setRows] = useState<BillingTransaction[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .get<LedgerResponse>(
        `/api/v1/admin/billing/transactions?account_id=${balance.accountId}&page.limit=50`,
        orgId,
      )
      .then((data) => setRows(data.transactions || []))
      .catch((e) => setError(e instanceof Error ? e.message : t('orgBalances.ledgerFailed')))
      .finally(() => setLoading(false));
  }, [api, orgId, balance.accountId, t]);

  return (
    <Dialog
      title={t('orgBalances.ledgerTitle', {
        organization: balance.organizationName || balance.organizationId,
      })}
      onClose={onClose}
      testId="org-ledger-dialog"
    >
      {error && <ErrorBanner message={error} />}
      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : rows.length === 0 ? (
        <div className="empty-state">{t('orgBalances.ledgerEmpty')}</div>
      ) : (
        <table className="data" data-testid="org-ledger-table">
          <thead>
            <tr>
              <th>{t('orgBalances.colWhen')}</th>
              <th>{t('orgBalances.colType')}</th>
              <th>{t('orgBalances.colAmount')}</th>
              <th>{t('orgBalances.colSource')}</th>
              <th>{t('orgBalances.colActor')}</th>
              <th>{t('orgBalances.colNote')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((tx) => (
              <tr key={tx.transactionId} data-testid={`ledger-row-${tx.transactionId}`}>
                <td>{formatTime(tx.createdAt)}</td>
                <td>{tEnum('transactionType', tx.type)}</td>
                <td>
                  {tx.type === 'refund' || tx.type === 'deduction' ? '-' : '+'}
                  {formatMoney(tx.amountCents, balance.currency)}
                </td>
                <td>{tx.source ? tEnum('txSource', tx.source) : '—'}</td>
                <td data-testid={`ledger-actor-${tx.transactionId}`}>
                  {tx.actorDisplayName || tx.actorUserId || '—'}
                </td>
                <td className="muted">{tx.reference || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.close')}
        </button>
      </div>
    </Dialog>
  );
}
