// Usage page: per-API-key token consumption with time-range presets,
// by-model and voucher drill-downs, plus the usage dashboard (cards,
// chart, metric toggle, group-by, CSV export, balance widget).
// Implements docs/design/metering.md FR6 and docs/design/usage-dashboard.md.

import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  api,
  formatTime,
  type DailyBucket,
  type PageMeta,
  type UsageDashboardResponse,
  type UsageSummaryRow,
  type VoucherSummary,
} from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination, StateBadge, usePolling } from '../components';
import UsageChart, { type ChartMetric } from '../components/UsageChart';
import BalanceWidget from '../components/BalanceWidget';

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
  { id: '24h', labelKey: 'usage.range24h', hours: 24 },
  { id: '7d', labelKey: 'usage.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'usage.range30d', hours: 30 * 24 },
  { id: 'custom', labelKey: 'usage.customRange', hours: 0 },
];

// formatCents renders integer minor units as a currency string.
function formatCents(cents: string): string {
  const n = parseInt(cents || '0', 10);
  return (n / 100).toFixed(2);
}

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

// exportCSV downloads the current dashboard table view as a CSV file
// (feature #9, AD10).
function exportCSV(buckets: DailyBucket[]) {
  const rows: string[][] = [['Date', 'Group', 'Cost', 'Tokens', 'Requests']];
  for (const bucket of buckets) {
    const date = new Date(parseInt(bucket.date, 10) * 1000).toISOString().slice(0, 10);
    for (const g of bucket.groups || []) {
      const tokens =
        parseInt(g.promptTokens || '0', 10) +
        parseInt(g.completionTokens || '0', 10) +
        parseInt(g.cachedTokens || '0', 10);
      rows.push([
        date,
        g.groupKey,
        formatCents(g.costCents),
        String(tokens),
        g.requestCount,
      ]);
    }
  }
  const csv = rows.map((r) => r.map((c) => `"${c}"`).join(',')).join('\n');
  const blob = new Blob(['\uFEFF' + csv], { type: 'text/csv;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = `usage-${Date.now()}.csv`;
  a.click();
  URL.revokeObjectURL(url);
}

export default function UsagePage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [preset, setPreset] = useState('24h');
  const [customSince, setCustomSince] = useState('');
  const [customUntil, setCustomUntil] = useState('');
  const [rows, setRows] = useState<UsageSummaryRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [byModelKey, setByModelKey] = useState<string | null>(null);
  const [voucherKey, setVoucherKey] = useState<string | null>(null);

  // Dashboard state (feature #9).
  const [dashboard, setDashboard] = useState<UsageDashboardResponse | null>(null);
  const [metric, setMetric] = useState<ChartMetric>('cost');
  const [groupBy, setGroupBy] = useState('api_key');

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
      setError(e instanceof Error ? e.message : t('usage.summaryFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, range.since, range.until, t]);

  // Load the dashboard cards + daily buckets (feature #9, AC1).
  const loadDashboard = useCallback(async () => {
    const params = new URLSearchParams({
      since: String(range.since),
      until: String(range.until),
      group_by: groupBy,
    });
    try {
      const data = await api.get<UsageDashboardResponse>(
        `/api/v1/admin/metering/usage-dashboard?${params.toString()}`,
        orgId,
      );
      setDashboard(data);
    } catch (e) {
      // The dashboard is additive; a failure should not blank the page.
      setError(e instanceof Error ? e.message : t('usage.dashboardFailed'));
    }
  }, [orgId, range.since, range.until, groupBy, t]);

  useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  useEffect(() => {
    void loadDashboard();
  }, [loadDashboard]);

  // FR6.4: refresh while the page is visible.
  usePolling(() => {
    void load();
    void loadDashboard();
  }, 60_000, true);

  const cards = dashboard?.cards;
  const buckets = dashboard?.dailyBuckets || [];
  const dataThrough = cards ? parseInt(cards.dataThrough || '0', 10) : 0;
  const pending = dataThrough > 0 && range.until > dataThrough + 3600;
  const unpriced = cards ? parseInt(cards.unpricedRequestCount || '0', 10) > 0 : false;

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('usage.title')}</h1>
          <div className="subtitle">{t('usage.subtitle')}</div>
        </div>
        <BalanceWidget />
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
            {t(p.labelKey)}
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
        <span className="toolbar-spacer" />
        <label className="muted" htmlFor="usage-groupby">
          {t('usage.groupBy')}
        </label>
        <select
          id="usage-groupby"
          data-testid="usage-groupby-select"
          value={groupBy}
          onChange={(e) => setGroupBy(e.target.value)}
        >
          <option value="api_key">{t('usage.groupApiKey')}</option>
          <option value="model">{t('usage.groupModel')}</option>
          <option value="accelerator_type">{t('usage.groupAccelerator')}</option>
        </select>
        <button
          className="secondary"
          data-testid="usage-export-csv"
          onClick={() => exportCSV(buckets)}
        >
          {t('usage.exportCsv')}
        </button>
      </div>

      {/* Dashboard cards (feature #9, AC1) */}
      <div className="dashboard-cards" data-testid="usage-dashboard-cards">
        <div className="dashboard-card">
          <div className="label">{t('usage.totalCost')}</div>
          <div className="value">
            {cards ? `${formatCents(cards.totalCostCents)} ${cards.currency || 'USD'}` : '—'}
          </div>
          {unpriced && (
            <div className="sub">
              <span className="unpriced-badge" data-testid="usage-unpriced-badge">
                {t('usage.unpriced')}
              </span>
            </div>
          )}
        </div>
        <div className="dashboard-card">
          <div className="label">{t('usage.tokens')}</div>
          <div className="value">
            {cards
              ? (
                  parseInt(cards.promptTokens || '0', 10) +
                  parseInt(cards.completionTokens || '0', 10) +
                  parseInt(cards.cachedTokens || '0', 10)
                ).toLocaleString()
              : '—'}
          </div>
          <div className="sub">
            {cards
              ? t('usage.inOutTokens', {
                  in: parseInt(cards.promptTokens || '0', 10).toLocaleString(),
                  out: parseInt(cards.completionTokens || '0', 10).toLocaleString(),
                })
              : ''}
          </div>
        </div>
        <div className="dashboard-card">
          <div className="label">{t('usage.requests')}</div>
          <div className="value">
            {cards ? parseInt(cards.requestCount || '0', 10).toLocaleString() : '—'}
          </div>
        </div>
        <div className="dashboard-card">
          <div className="label">{t('usage.avgCost')}</div>
          <div className="value">
            {cards && parseInt(cards.requestCount || '0', 10) > 0
              ? formatCents(
                  String(
                    Math.round(
                      parseInt(cards.totalCostCents || '0', 10) /
                        parseInt(cards.requestCount || '0', 10),
                    ),
                  ),
                )
              : '—'}
          </div>
        </div>
      </div>

      {/* Metric toggle + chart (feature #9, AC3) */}
      <div className="panel">
        <div className="toolbar" style={{ marginBottom: 10 }} data-testid="usage-metric-toggle">
          {(['cost', 'tokens', 'requests'] as ChartMetric[]).map((m) => (
            <button
              key={m}
              className={metric === m ? '' : 'secondary'}
              data-testid={`usage-metric-${m}`}
              onClick={() => setMetric(m)}
            >
              {m === 'cost' ? t('usage.metricCost') : m === 'tokens' ? t('usage.metricTokens') : t('usage.metricRequests')}
            </button>
          ))}
          {pending && (
            <span className="pending-badge" data-testid="usage-pending-badge">
              {t('usage.pending')}
            </span>
          )}
        </div>
        <UsageChart buckets={buckets} metric={metric} />
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : rows.length === 0 ? (
          <div className="empty-state" data-testid="usage-empty">
            {t('usage.empty')}
          </div>
        ) : (
          <table className="data" data-testid="usage-table">
            <thead>
              <tr>
                <th>{t('usage.colApiKey')}</th>
                <th>{t('usage.colInputTokens')}</th>
                <th>{t('usage.colOutputTokens')}</th>
                <th>{t('usage.colCachedTokens')}</th>
                <th>{t('usage.colRequests')}</th>
                <th>{t('usage.colSettled')}</th>
                <th>{t('common.actions')}</th>
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
                      {t('usage.settledText', {
                        settled: row.settledHours,
                        pending: row.pendingHours,
                      })}
                    </span>
                  </td>
                  <td>
                    <button
                      className="link"
                      data-testid={`by-model-${row.groupKey}`}
                      onClick={() => setByModelKey(row.groupKey)}
                    >
                      {t('usage.byModel')}
                    </button>
                    <button
                      className="link"
                      data-testid={`vouchers-${row.groupKey}`}
                      onClick={() => setVoucherKey(row.groupKey)}
                    >
                      {t('usage.vouchers')}
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
  const { t } = useI18n();
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
      .catch((e) => setError(e instanceof Error ? e.message : t('usage.loadFailed')))
      .finally(() => setLoading(false));
  }, [orgId, range.since, range.until, t]);

  return (
    <Dialog title={t('usage.byModelTitle', { apiKeyId })} onClose={onClose}>
      {error && <ErrorBanner message={error} />}
      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : rows.length === 0 ? (
        <div className="empty-state">{t('usage.byModelEmpty')}</div>
      ) : (
        <table className="data" data-testid="by-model-table">
          <thead>
            <tr>
              <th>{t('usage.colModel')}</th>
              <th>{t('usage.colInputTokens2')}</th>
              <th>{t('usage.colOutputTokens2')}</th>
              <th>{t('usage.colCachedTokens2')}</th>
              <th>{t('usage.colRequests2')}</th>
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
  const { t } = useI18n();
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
      .catch((e) => setError(e instanceof Error ? e.message : t('usage.loadFailed')))
      .finally(() => setLoading(false));
  }, [orgId, apiKeyId, range.since, range.until, offset, t]);

  return (
    <Dialog title={t('usage.vouchersTitle', { apiKeyId })} onClose={onClose}>
      {error && <ErrorBanner message={error} />}
      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : vouchers.length === 0 ? (
        <div className="empty-state">{t('usage.vouchersEmpty')}</div>
      ) : (
        <>
          <table className="data" data-testid="vouchers-table">
            <thead>
              <tr>
                <th>{t('usage.colCompleted')}</th>
                <th>{t('usage.colRequest')}</th>
                <th>{t('usage.colModel2')}</th>
                <th>{t('usage.colInOutCached')}</th>
                <th>{t('usage.colEstCost')}</th>
                <th>{t('usage.colSettled2')}</th>
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
                  <td data-testid={`voucher-cost-${v.voucherId}`}>
                    {v.priced === false ? (
                      <>
                        <span className="muted">—</span>
                        <span className="unpriced-badge">{t('usage.unpriced')}</span>
                      </>
                    ) : (
                      formatCents(v.estimatedCostCents || '0')
                    )}
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
