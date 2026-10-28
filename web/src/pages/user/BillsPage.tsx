// End-user bills page (feature-17): own bills (read-only) against the
// user surface's billing API.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { formatTime, type BillSummary } from '../../api';
import { formatMajor } from '../../display';

export default function BillsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [bills, setBills] = useState<BillSummary[]>([]);
  const [error, setError] = useState('');

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
