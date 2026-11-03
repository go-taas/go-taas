// End-user bills page (feature-17): own bills (read-only) against the
// user surface's billing API.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { formatTime, type BalanceResponse, type BillSummary } from '../../api';
import { formatMajor } from '../../display';

export default function BillsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [bills, setBills] = useState<BillSummary[]>([]);
  // balance is the organization's shared account: members spend from one
  // wallet, so the page shows what is left next to the bills it produced.
  const [balance, setBalance] = useState<BalanceResponse | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!orgId) {
      setBalance(null);
      return;
    }
    api
      .get<BalanceResponse>('/api/v1/billing/balance', orgId)
      .then(setBalance)
      .catch(() => setBalance(null));
  }, [api, orgId]);

  useEffect(() => {
    api
      .get<{ bills?: BillSummary[] }>('/api/v1/billing/bills?page.limit=100', orgId)
      .then((data) => setBills(data.bills || []))
      .catch((e) => setError(e instanceof Error ? e.message : t('ubills.loadFailed')));
  }, [api, orgId, t]);

  return (
    <div className="page">
      <h1>{t('ubills.title')}</h1>
      {error && <div className="error">{error}</div>}
      {balance && (
        <div className="summary-strip" data-testid="ubills-balance">
          <span>
            <span className="stat-label">
              {balance.mode === 'postpaid' ? t('bills.quota') : t('bills.balance')}
            </span>
            <strong>
              {balance.hasAccount === false
                ? t('bills.noAccount')
                : balance.mode === 'postpaid'
                  ? formatMajor(Number(balance.monthlyQuotaCents || 0) / 100, 2, balance.currency)
                  : formatMajor(Number(balance.balanceCents || 0) / 100, 2, balance.currency)}
            </strong>
          </span>
          <span>
            <span className="stat-label">{t('bills.spentCycle')}</span>
            <strong>
              {formatMajor(
                Number(balance.spentThisCycleCents ?? balance.usedThisCycleCents ?? 0) / 100,
                2,
                balance.currency,
              )}
            </strong>
          </span>
        </div>
      )}
      {bills.length === 0 ? (
        <div className="empty" data-testid="bills-empty">{t('ubills.empty')}</div>
      ) : (
        <table className="table" data-testid="bills-table">
          <thead>
            <tr>
              <th>{t('ubills.colPeriod')}</th>
              <th>{t('ubills.colAmount')}</th>
              <th>{t('ubills.colCharges')}</th>
            </tr>
          </thead>
          <tbody>
            {bills.map((b) => (
              <tr key={b.billId}>
                <td>{formatTime(b.periodStart)} – {formatTime(b.periodEnd)}</td>
                <td>{formatMajor(b.amount)}</td>
                <td>{b.chargeCount}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
