// End-user usage page (feature-17): own usage, cost and balance against
// the user surface's metering/billing APIs.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { formatTime, type BalanceResponse, type UsageDashboardResponse } from '../../api';
import { formatMoney } from '../../display';

export default function UsagePage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [dashboard, setDashboard] = useState<UsageDashboardResponse | null>(null);
  const [balance, setBalance] = useState<BalanceResponse | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .get<UsageDashboardResponse>('/api/v1/metering/usage-dashboard', orgId)
      .then(setDashboard)
      .catch((e) => setError(e instanceof Error ? e.message : t('uusage.loadFailed')));
    api
      .get<BalanceResponse>('/api/v1/billing/balance', orgId)
      .then(setBalance)
      .catch(() => {
        // Balance may be absent (no account); not fatal.
      });
  }, [api, orgId, t]);

  const cards = dashboard?.cards;

  return (
    <div className="page" data-testid="usage-dashboard-cards">
      <h1>{t('uusage.title')}</h1>
      {error && <div className="error">{error}</div>}
      {balance && (
        <div className="balance-widget" data-testid="usage-balance-widget">
          {balance.mode === 'prepaid'
            ? t('uusage.balance', { balance: formatMoney(balance.balanceCents) })
            : t('uusage.quota', {
                quota: formatMoney(balance.monthlyQuotaCents),
                used: formatMoney(balance.usedThisCycleCents),
              })}
        </div>
      )}
      {cards && (
        <div className="cards">
          <div className="card" data-testid="usage-metric-cost">
            {t('uusage.cost', { n: formatMoney(cards.totalCostCents) })}
          </div>
          <div className="card" data-testid="usage-metric-tokens">
            {t('uusage.tokens', { n: Number(cards.promptTokens) + Number(cards.completionTokens) })}
          </div>
          <div className="card" data-testid="usage-metric-requests">
            {t('uusage.requests', { n: cards.requestCount })}
          </div>
        </div>
      )}
      {dashboard && Number(cards?.unpricedRequestCount || 0) > 0 && (
        <div className="hint" data-testid="usage-unpriced-note">
          {t('uusage.unpriced', { n: cards?.unpricedRequestCount ?? '0' })}
        </div>
      )}
      {dashboard?.dailyBuckets && dashboard.dailyBuckets.length > 0 ? (
        <table className="table" data-testid="usage-table">
          <thead>
            <tr>
              <th>{t('uusage.colDate')}</th>
              <th>{t('uusage.colGroup')}</th>
              <th>{t('uusage.colCost')}</th>
              <th>{t('uusage.colTokens')}</th>
            </tr>
          </thead>
          <tbody>
            {dashboard.dailyBuckets.map((bucket) =>
              (bucket.groups || []).map((g) => (
                <tr key={`${bucket.date}-${g.groupKey}`}>
                  <td>{formatTime(bucket.date)}</td>
                  <td>{g.groupKey}</td>
                  <td>{formatMoney(g.costCents)}</td>
                  <td>{Number(g.promptTokens) + Number(g.completionTokens)}</td>
                </tr>
              )),
            )}
          </tbody>
        </table>
      ) : (
        <div className="empty" data-testid="usage-empty">{t('uusage.empty')}</div>
      )}
    </div>
  );
}
