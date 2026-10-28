// Bills page: monthly bill summaries with a charge drill-down dialog.
// Implements docs/design/pricing.md FR5, AC15.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  formatTime,
  type BillSummary,
  type ChargeRecordSummary,
  type PageMeta,
} from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination, StateBadge, usePolling } from '../components';
import { formatMajor } from '../display';

interface BillsResponse {
  response: { code: number; message: string };
  bills: BillSummary[];
  pageMeta?: PageMeta;
}

interface ChargesResponse {
  response: { code: number; message: string };
  charges: ChargeRecordSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

// monthLabel renders "2026-09" for a bill period start.
function monthLabel(periodStart: string): string {
  const d = new Date(Number(periodStart) * 1000);
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}`;
}

export default function BillsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [bills, setBills] = useState<BillSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [drillMonth, setDrillMonth] = useState<BillSummary | null>(null);

  const load = useCallback(async () => {
    setError('');
    try {
      const params = new URLSearchParams({
        'page.offset': String(offset),
        'page.limit': String(PAGE_SIZE),
      });
      const data = await api.get<BillsResponse>(
        `/api/v1/admin/billing/bills?${params.toString()}`,
        orgId,
      );
      setBills(data.bills || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bills.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, t]);

  useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  usePolling(() => void load(), 60_000, true);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('bills.title')}</h1>
          <div className="subtitle">{t('bills.subtitle')}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : bills.length === 0 ? (
          <div className="empty-state" data-testid="bills-empty">
            {t('bills.empty')}
          </div>
        ) : (
          <table className="data" data-testid="bills-table">
            <thead>
              <tr>
                <th>{t('bills.colBill')}</th>
                <th>{t('bills.colMonth')}</th>
                <th>{t('bills.colAmount')}</th>
                <th>{t('bills.colCharges')}</th>
                <th>{t('bills.colUnpriced')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {bills.map((b) => (
                <tr key={b.billId} data-testid={`bill-row-${b.billId}`}>
                  <td>
                    <strong className="mono">{b.billId}</strong>
                  </td>
                  <td>{monthLabel(b.periodStart)}</td>
                  <td>
                    {formatMajor(b.amount)}
                  </td>
                  <td>{b.chargeCount}</td>
                  <td>
                    {Number(b.unpricedCount) > 0 ? (
                      <StateBadge state="unpriced" />
                    ) : (
                      <span className="muted">0</span>
                    )}{' '}
                    {b.unpricedCount}
                  </td>
                  <td>
                    <button
                      className="link"
                      data-testid={`bill-charges-${b.billId}`}
                      onClick={() => setDrillMonth(b)}
                    >
                      {t('bills.charges')}
                    </button>
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

      {drillMonth && (
        <ChargesDialog
          orgId={orgId}
          bill={drillMonth}
          onClose={() => setDrillMonth(null)}
        />
      )}
    </div>
  );
}

// ChargesDialog lists the charge records of one bill month (FR5.1).
function ChargesDialog({
  orgId,
  bill,
  onClose,
}: {
  orgId: string;
  bill: BillSummary;
  onClose: () => void;
}) {
  const [charges, setCharges] = useState<ChargeRecordSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const { t } = useI18n();

  useEffect(() => {
    setLoading(true);
    const params = new URLSearchParams({
      since: bill.periodStart,
      until: bill.periodEnd,
      'page.offset': String(offset),
      'page.limit': String(PAGE_SIZE),
    });
    api
      .get<ChargesResponse>(`/api/v1/admin/billing/charges?${params.toString()}`, orgId)
      .then((data) => {
        setCharges(data.charges || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
      })
      .catch((e) => setError(e instanceof Error ? e.message : t('bills.chargesFailed')))
      .finally(() => setLoading(false));
  }, [orgId, bill.periodStart, bill.periodEnd, offset, t]);

  return (
    <Dialog
      title={t('bills.chargesTitle', { month: monthLabel(bill.periodStart) })}
      onClose={onClose}
      testId="charges-dialog"
    >
      {error && <ErrorBanner message={error} />}
      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : charges.length === 0 ? (
        <div className="empty-state">{t('bills.chargesEmpty')}</div>
      ) : (
        <>
          <table className="data" data-testid="charges-table">
            <thead>
              <tr>
                <th>{t('bills.colHour')}</th>
                <th>{t('bills.colApiKey')}</th>
                <th>{t('bills.colModel')}</th>
                <th>{t('bills.colCard')}</th>
                <th>{t('bills.colTokens')}</th>
                <th>{t('bills.colRequests')}</th>
                <th>{t('bills.colAmount2')}</th>
                <th>{t('bills.colTier')}</th>
                <th>{t('bills.colChargedAt')}</th>
              </tr>
            </thead>
            <tbody>
              {charges.map((c) => (
                <tr key={c.chargeId} data-testid={`charge-row-${c.chargeId}`}>
                  <td>{formatTime(c.periodStart)}</td>
                  <td className="mono">{c.apiKeyId}</td>
                  <td className="mono">{c.modelId}</td>
                  <td className="mono">{c.acceleratorType}</td>
                  <td>
                    {c.promptTokens} / {c.completionTokens}
                  </td>
                  <td>{c.requestCount}</td>
                  <td>
                    {c.priced ? (
                      formatMajor(c.amount)
                    ) : (
                      <StateBadge state="unpriced" />
                    )}
                  </td>
                  <td>{c.tierIndex >= 0 ? `#${c.tierIndex}` : t('common.flat')}</td>
                  <td>{formatTime(c.chargedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {total > PAGE_SIZE && (
            <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
          )}
        </>
      )}
    </Dialog>
  );
}
