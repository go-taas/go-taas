// Quota change history: a sub-page of the quota list. It used to be a
// drawer over the table, which hid the organization the history belonged to
// and made the trail look like a detail of the selected row. As a page it
// keeps the quota list one breadcrumb away and uses the full width for the
// operator and dimension columns.

import { useCallback, useEffect, useState } from 'react';
import { Breadcrumb, ErrorBanner } from '../components';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { formatTime, type GetOrganizationQuotaResponse } from '../api';
import { useParams } from '../router';

// quotaDimensionLabel normalizes both the snake_case strings the server
// writes into audit rows and the camelCase names the console uses, so a
// history entry always reads as a human phrase.
export function quotaDimensionLabel(t: (key: string) => string, dimension: string): string {
  const normalized = dimension
    .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
    .toLowerCase();
  const key = `quota.dimension.${normalized}`;
  const label = t(key);
  return label === key ? dimension : label;
}

export default function QuotaHistoryPage() {
  const api = useApi();
  const { orgId: scopedOrgId } = useOrg();
  const { t } = useI18n();
  const params = useParams();
  const orgId = params.orgId || '';
  const [data, setData] = useState<GetOrganizationQuotaResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const next = await api.get<GetOrganizationQuotaResponse>(
        `/api/v1/admin/quotas/${encodeURIComponent(orgId)}`,
        scopedOrgId,
      );
      setData(next);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('quota.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, scopedOrgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const orgName = data?.quota?.organizationName || orgId;

  return (
    <div>
      <Breadcrumb
        items={[
          { label: t('quota.title'), to: '/admin/quotas' },
          { label: t('quota.historyTitle') },
        ]}
        testId="quota-history-breadcrumb"
      />
      <div className="page-header">
        <div>
          <h1 data-testid="quota-history-title">{t('quota.historyTitle')}</h1>
          <div className="subtitle">{t('quota.historySubtitle', { orgName })}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : !data || (data.changes || []).length === 0 ? (
          <div className="empty-state" data-testid="quota-history-empty">
            {t('quota.historyEmpty')}
          </div>
        ) : (
          <table className="data" data-testid="quota-history-table">
            <thead>
              <tr>
                <th>{t('quota.colTime')}</th>
                <th>{t('quota.colOperator')}</th>
                <th>{t('quota.colDimension')}</th>
                <th>{t('quota.colChange')}</th>
              </tr>
            </thead>
            <tbody>
              {data.changes.map((ch, i) => (
                <tr key={`${ch.changedAt}-${ch.dimension}-${i}`} data-testid={`quota-history-entry-${i}`}>
                  <td>{formatTime(ch.changedAt)}</td>
                  <td data-testid={`quota-history-operator-${i}`}>
                    {ch.operatorDisplayName || ch.operator || '—'}
                  </td>
                  <td data-testid={`quota-history-dimension-${i}`}>
                    {quotaDimensionLabel(t, ch.dimension)}
                  </td>
                  <td>
                    {ch.oldValue} → {ch.newValue}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="muted" style={{ padding: '0 16px 12px' }}>
          {t('quota.propagationNote')}
        </p>
      </div>
    </div>
  );
}
