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
import { Dialog, ErrorBanner, Pagination, StateBadge, usePolling } from '../components';

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
      setError(e instanceof Error ? e.message : 'failed to load bills');
    } finally {
      setLoading(false);
    }
  }, [orgId, offset]);

  useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  usePolling(() => void load(), 60_000, true);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Bills</h1>
          <div className="subtitle">
            Monthly bills computed from charge records. Unpriced groups are
            counted and billed at 0 until a price applies.
          </div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">Loading…</div>
        ) : bills.length === 0 ? (
          <div className="empty-state" data-testid="bills-empty">
            No bills yet. Bills appear after the first charged usage.
          </div>
        ) : (
          <table className="data" data-testid="bills-table">
            <thead>
              <tr>
                <th>Bill</th>
                <th>Month</th>
                <th>Amount</th>
                <th>Charges</th>
                <th>Unpriced</th>
                <th>Actions</th>
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
                    {b.amount.toFixed(2)} {b.currency}
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
                      Charges
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
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load charges'))
      .finally(() => setLoading(false));
  }, [orgId, bill.periodStart, bill.periodEnd, offset]);

  return (
    <Dialog title={`Charges — ${monthLabel(bill.periodStart)}`} onClose={onClose} testId="charges-dialog">
      {error && <ErrorBanner message={error} />}
      {loading ? (
        <div className="loading">Loading…</div>
      ) : charges.length === 0 ? (
        <div className="empty-state">No charges in this month.</div>
      ) : (
        <>
          <table className="data" data-testid="charges-table">
            <thead>
              <tr>
                <th>Hour</th>
                <th>API key</th>
                <th>Model</th>
                <th>Card</th>
                <th>In / Out tokens</th>
                <th>Requests</th>
                <th>Amount</th>
                <th>Tier</th>
                <th>Charged at</th>
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
                      `${c.amount.toFixed(2)} ${c.currency}`
                    ) : (
                      <StateBadge state="unpriced" />
                    )}
                  </td>
                  <td>{c.tierIndex >= 0 ? `#${c.tierIndex}` : 'flat'}</td>
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
