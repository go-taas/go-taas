// Usage page: per-API-key token consumption with time-range presets,
// by-model and voucher drill-downs.
// Implements docs/design/metering.md FR6, AC12, AC13.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { api, formatTime, type PageMeta, type UsageSummaryRow, type VoucherSummary } from '../api';
import { useOrg } from '../org';
import { Dialog, ErrorBanner, Pagination, StateBadge, usePolling } from '../components';

interface SummaryResponse {
  response: { code: number; message: string };
  rows: UsageSummaryRow[];
}

interface VouchersResponse {
  response: { code: number; message: string };
  vouchers: VoucherSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;
const RANGE_PRESETS = [
  { id: '24h', label: 'Last 24 hours', hours: 24 },
  { id: '7d', label: 'Last 7 days', hours: 7 * 24 },
  { id: '30d', label: 'Last 30 days', hours: 30 * 24 },
  { id: 'custom', label: 'Custom range', hours: 0 },
];

// rangeFor resolves the since/until unix seconds for the selected preset.
function rangeFor(preset: string, customSince: string, customUntil: string): { since: number; until: number } {
  const now = Math.floor(Date.now() / 1000);
  if (preset === 'custom') {
    const until = customUntil ? Math.floor(new Date(customUntil).getTime() / 1000) : now;
    const since = customSince
      ? Math.floor(new Date(customSince).getTime() / 1000)
      : until - 24 * 3600;
    return { since, until };
  }
  const hours = RANGE_PRESETS.find((p) => p.id === preset)?.hours || 24;
  return { since: now - hours * 3600, until: now };
}

export default function UsagePage() {
  const { orgId } = useOrg();
  const [preset, setPreset] = useState('24h');
  const [customSince, setCustomSince] = useState('');
  const [customUntil, setCustomUntil] = useState('');
  const [rows, setRows] = useState<UsageSummaryRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [byModelKey, setByModelKey] = useState<string | null>(null);
  const [voucherKey, setVoucherKey] = useState<string | null>(null);

  const range = useMemo(
    () => rangeFor(preset, customSince, customUntil),
    [preset, customSince, customUntil],
  );

  const load = useCallback(async () => {
    setError('');
    try {
      const params = new URLSearchParams({
        since: String(range.since),
        until: String(range.until),
      });
      const data = await api.get<SummaryResponse>(
        `/api/v1/admin/metering/usage-summary?${params.toString()}`,
        orgId,
      );
      setRows(data.rows || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load usage summary');
    } finally {
      setLoading(false);
    }
  }, [orgId, range.since, range.until]);

  useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  // FR6.4: refresh while the page is visible.
  usePolling(() => void load(), 60_000, true);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Usage</h1>
          <div className="subtitle">
            Token consumption per API key. Usage appears within the hour; the
            current hour is pending.
          </div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="toolbar" style={{ marginBottom: 14 }} data-testid="usage-range">
        {RANGE_PRESETS.map((p) => (
          <button
            key={p.id}
            className={preset === p.id ? '' : 'secondary'}
            data-testid={`usage-range-${p.id}`}
            onClick={() => setPreset(p.id)}
          >
            {p.label}
          </button>
        ))}
        {preset === 'custom' && (
          <>
            <input
              type="datetime-local"
              data-testid="usage-custom-since"
              value={customSince}
              onChange={(e) => setCustomSince(e.target.value)}
            />
            <input
              type="datetime-local"
              data-testid="usage-custom-until"
              value={customUntil}
              onChange={(e) => setCustomUntil(e.target.value)}
            />
          </>
        )}
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">Loading…</div>
        ) : rows.length === 0 ? (
          <div className="empty-state" data-testid="usage-empty">
            No usage in this range. Usage appears after the first inference
            calls.
          </div>
        ) : (
          <table className="data" data-testid="usage-table">
            <thead>
              <tr>
                <th>API Key</th>
                <th>Input tokens</th>
                <th>Output tokens</th>
                <th>Cached tokens</th>
                <th>Requests</th>
                <th>Settled</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.groupKey} data-testid={`usage-row-${row.groupKey}`}>
                  <td>
                    <strong className="mono">{row.groupKey}</strong>
                  </td>
                  <td>{row.promptTokens}</td>
                  <td>{row.completionTokens}</td>
                  <td>{row.cachedTokens}</td>
                  <td>{row.requestCount}</td>
                  <td>
                    <StateBadge
                      state={Number(row.pendingHours) > 0 ? 'pending' : 'settled'}
                    />
                    <span className="muted">
                      {' '}
                      {row.settledHours}h settled / {row.pendingHours}h pending
                    </span>
                  </td>
                  <td>
                    <button
                      className="link"
                      data-testid={`by-model-${row.groupKey}`}
                      onClick={() => setByModelKey(row.groupKey)}
                    >
                      By model
                    </button>
                    <button
                      className="link"
                      data-testid={`vouchers-${row.groupKey}`}
                      onClick={() => setVoucherKey(row.groupKey)}
                    >
                      Vouchers
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {byModelKey && (
        <ByModelDialog
          orgId={orgId}
          apiKeyId={byModelKey}
          range={range}
          onClose={() => setByModelKey(null)}
        />
      )}
      {voucherKey && (
        <VouchersDialog
          orgId={orgId}
          apiKeyId={voucherKey}
          range={range}
          onClose={() => setVoucherKey(null)}
        />
      )}
    </div>
  );
}

// ByModelDialog shows per-model consumption for one key (FR6.2).
function ByModelDialog({
  orgId,
  apiKeyId,
  range,
  onClose,
}: {
  orgId: string;
  apiKeyId: string;
  range: { since: number; until: number };
  onClose: () => void;
}) {
  const [rows, setRows] = useState<UsageSummaryRow[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const params = new URLSearchParams({
      since: String(range.since),
      until: String(range.until),
      group_by: 'model',
    });
    api
      .get<SummaryResponse>(`/api/v1/admin/metering/usage-summary?${params.toString()}`, orgId)
      .then((data) => setRows(data.rows || []))
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load'))
      .finally(() => setLoading(false));
  }, [orgId, range.since, range.until]);

  return (
    <Dialog title={`Usage by model — ${apiKeyId}`} onClose={onClose}>
      {error && <ErrorBanner message={error} />}
      {loading ? (
        <div className="loading">Loading…</div>
      ) : rows.length === 0 ? (
        <div className="empty-state">No usage in this range.</div>
      ) : (
        <table className="data" data-testid="by-model-table">
          <thead>
            <tr>
              <th>Model</th>
              <th>Input tokens</th>
              <th>Output tokens</th>
              <th>Cached tokens</th>
              <th>Requests</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.groupKey} data-testid={`by-model-row-${row.groupKey}`}>
                <td>
                  <strong className="mono">{row.groupKey}</strong>
                </td>
                <td>{row.promptTokens}</td>
                <td>{row.completionTokens}</td>
                <td>{row.cachedTokens}</td>
                <td>{row.requestCount}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Dialog>
  );
}

// VouchersDialog lists the vouchers of one key, pre-filtered to the
// selected range, with pagination (FR6.3).
function VouchersDialog({
  orgId,
  apiKeyId,
  range,
  onClose,
}: {
  orgId: string;
  apiKeyId: string;
  range: { since: number; until: number };
  onClose: () => void;
}) {
  const [vouchers, setVouchers] = useState<VoucherSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setLoading(true);
    const params = new URLSearchParams({
      since: String(range.since),
      until: String(range.until),
      api_key_id: apiKeyId,
      'page.offset': String(offset),
      'page.limit': String(PAGE_SIZE),
    });
    api
      .get<VouchersResponse>(`/api/v1/admin/metering/vouchers?${params.toString()}`, orgId)
      .then((data) => {
        setVouchers(data.vouchers || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
      })
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load'))
      .finally(() => setLoading(false));
  }, [orgId, apiKeyId, range.since, range.until, offset]);

  return (
    <Dialog title={`Vouchers — ${apiKeyId}`} onClose={onClose}>
      {error && <ErrorBanner message={error} />}
      {loading ? (
        <div className="loading">Loading…</div>
      ) : vouchers.length === 0 ? (
        <div className="empty-state">No vouchers in this range.</div>
      ) : (
        <>
          <table className="data" data-testid="vouchers-table">
            <thead>
              <tr>
                <th>Completed</th>
                <th>Request</th>
                <th>Model</th>
                <th>In / Out / Cached</th>
                <th>Settled</th>
              </tr>
            </thead>
            <tbody>
              {vouchers.map((v) => (
                <tr key={v.voucherId} data-testid={`voucher-row-${v.requestId}`}>
                  <td>{formatTime(v.completedAt)}</td>
                  <td className="mono">{v.requestId}</td>
                  <td className="mono">{v.modelId}</td>
                  <td>
                    {v.usage
                      ? `${v.usage.promptTokens} / ${v.usage.completionTokens} / ${v.usage.cachedTokens}`
                      : '—'}
                  </td>
                  <td>
                    <StateBadge state={v.settled ? 'settled' : 'pending'} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {total > PAGE_SIZE && (
            <Pagination
              offset={offset}
              limit={PAGE_SIZE}
              total={total}
              onPageChange={setOffset}
            />
          )}
        </>
      )}
    </Dialog>
  );
}
