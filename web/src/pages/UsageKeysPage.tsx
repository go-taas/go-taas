// Admin Usage Keys page (feature #28): the fleet-wide per-API-key usage
// and cost overview — summary cards, a top-keys ranking, a per-key table,
// and an inline-SVG trend chart with a metric switcher. Admin surface:
// route /admin/usage/keys, API /api/v1/admin/usage/keys.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { formatMoney } from '../display';
import { navigate } from '../router';
import { ErrorBanner, Pagination } from '../components';
import UsageKeysChart, { type UsageKeysMetric } from '../components/UsageKeysChart';
import UsageKeysCards from '../components/UsageKeysCards';
import TopKeysList from '../components/TopKeysList';
import {
  formatTime,
  type GetUsageKeysOverviewResponse,
  type UsageKeyRow,
} from '../api';

const PAGE_SIZE = 20;
const RANGE_PRESETS = [
  { id: '24h', labelKey: 'usageKeys.range24h', hours: 24 },
  { id: '7d', labelKey: 'usageKeys.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'usageKeys.range30d', hours: 30 * 24 },
  { id: 'custom', labelKey: 'usageKeys.customRange', hours: 0 },
];

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

// errorRatePercent derives the error rate as a percentage.
function errorRatePercent(row: UsageKeyRow): string {
  const req = parseInt(row.requestCount || '0', 10);
  if (req === 0) return '0.0%';
  return `${((parseInt(row.errorCount || '0', 10) / req) * 100).toFixed(1)}%`;
}

// costDollars renders integer cents in the platform's display currency.
function costDollars(cents: string): string {
  return formatMoney(cents);
}

export default function UsageKeysPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [preset, setPreset] = useState('24h');
  const [customSince, setCustomSince] = useState('');
  const [customUntil, setCustomUntil] = useState('');
  const [modelFilter, setModelFilter] = useState('');
  const [metric, setMetric] = useState<UsageKeysMetric>('requests');
  const [data, setData] = useState<GetUsageKeysOverviewResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [offset, setOffset] = useState(0);
  const [sortKey, setSortKey] = useState('totalCostCents');
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc');

  const range = useMemo(
    () => rangeFor(preset, customSince, customUntil),
    [preset, customSince, customUntil],
  );

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams({
        since: String(range.since),
        until: String(range.until),
      });
      if (modelFilter) params.set('model_id', modelFilter);
      const next = await api.get<GetUsageKeysOverviewResponse>(
        `/api/v1/admin/usage/keys?${params.toString()}`,
        orgId,
      );
      setData(next);
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('usageKeys.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [api, orgId, range.since, range.until, modelFilter, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const cards = data?.cards || null;
  const series = data?.series || [];
  const keys = data?.keys || [];
  const topKeys = data?.topKeys || [];

  // Sort the per-key table.
  const sortedKeys = useMemo(() => {
    const arr = [...keys];
    arr.sort((a, b) => {
      const av = parseInt(a[sortKey as keyof UsageKeyRow] as string || '0', 10);
      const bv = parseInt(b[sortKey as keyof UsageKeyRow] as string || '0', 10);
      const cmp = av - bv;
      return sortDir === 'asc' ? cmp : -cmp;
    });
    return arr;
  }, [keys, sortKey, sortDir]);

  const pageRows = sortedKeys.slice(offset, offset + PAGE_SIZE);

  const toggleSort = (key: string) => {
    if (sortKey === key) {
      setSortDir(sortDir === 'asc' ? 'desc' : 'asc');
    } else {
      setSortKey(key);
      setSortDir('desc');
    }
    setOffset(0);
  };

  const dataThrough = cards ? parseInt(cards.dataThrough || '0', 10) : 0;
  const pending = dataThrough > 0 && range.until > dataThrough + 3600;

  return (
    <div data-testid="usage-keys-page">
      <div className="page-header">
        <div>
          <h1>{t('usageKeys.title')}</h1>
          <div className="subtitle">{t('usageKeys.subtitle')}</div>
        </div>
        <button
          className="secondary"
          data-testid="usage-keys-refresh"
          disabled={loading}
          onClick={() => void load()}
        >
          {t('usageKeys.refresh')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}
      {stale && data && (
        <div className="error-banner" data-testid="usage-keys-stale-banner">
          {t('usageKeys.staleData')}
        </div>
      )}
      {pending && (
        <div className="pending-badge" data-testid="usage-keys-pending-badge">
          {t('usageKeys.pending')}
        </div>
      )}

      <div className="toolbar" style={{ marginBottom: 14 }} data-testid="usage-keys-filter-range">
        {RANGE_PRESETS.map((p) => (
          <button
            key={p.id}
            className={preset === p.id ? '' : 'secondary'}
            data-testid={`usage-keys-range-${p.id}`}
            onClick={() => setPreset(p.id)}
          >
            {t(p.labelKey)}
          </button>
        ))}
        {preset === 'custom' && (
          <>
            <input
              type="datetime-local"
              data-testid="usage-keys-custom-since"
              value={customSince}
              onChange={(e) => setCustomSince(e.target.value)}
            />
            <input
              type="datetime-local"
              data-testid="usage-keys-custom-until"
              value={customUntil}
              onChange={(e) => setCustomUntil(e.target.value)}
            />
          </>
        )}
        <span className="toolbar-spacer" />
        <label className="muted" htmlFor="usage-keys-model-filter">
          {t('usageKeys.modelFilter')}
        </label>
        <select
          id="usage-keys-model-filter"
          data-testid="usage-keys-filter-model"
          value={modelFilter}
          onChange={(e) => {
            setModelFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('usageKeys.allModels')}</option>
          {keys.map((k) => (
            <option key={k.apiKeyId} value={k.apiKeyId}>
              {k.apiKeyName || k.apiKeyId}
            </option>
          ))}
        </select>
      </div>

      {cards && <UsageKeysCards cards={cards} />}

      <div className="panel" style={{ marginTop: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('usageKeys.topTitle')}</h3>
        <TopKeysList
          topKeys={topKeys}
          onSelect={(id) => navigate(`/admin/usage/keys/${id}`)}
        />
      </div>

      <div className="panel" style={{ marginTop: 16 }}>
        <div className="toolbar" style={{ marginBottom: 8 }}>
          <span className="muted">{t('usageKeys.chartTitle')}</span>
          <span className="toolbar-spacer" />
          <div data-testid="usage-keys-metric-toggle">
            {(['requests', 'tokens', 'cost', 'errorRate'] as UsageKeysMetric[]).map((m) => (
              <button
                key={m}
                className={metric === m ? '' : 'secondary'}
                data-testid={`usage-keys-metric-${m}`}
                disabled={loading}
                onClick={() => setMetric(m)}
              >
                {t(`usageKeys.metric.${m}`)}
              </button>
            ))}
          </div>
        </div>
        <UsageKeysChart series={series} metric={metric} />
      </div>

      <div className="panel" style={{ marginTop: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('usageKeys.keysTitle')}</h3>
        {keys.length === 0 ? (
          <p className="muted" data-testid="usage-keys-empty">
            {t('usageKeys.empty')}
          </p>
        ) : (
          <>
            <table className="data" data-testid="usage-keys-table">
              <thead>
                <tr>
                  <th>{t('usageKeys.colKey')}</th>
                  <th>{t('usageKeys.colOrg')}</th>
                  <th>
                    <button className="sortable" data-testid="usage-keys-sort-requests" onClick={() => toggleSort('requestCount')}>
                      {t('usageKeys.colRequests')}
                    </button>
                  </th>
                  <th>
                    <button className="sortable" data-testid="usage-keys-sort-error" onClick={() => toggleSort('errorCount')}>
                      {t('usageKeys.colErrorRate')}
                    </button>
                  </th>
                  <th>
                    <button className="sortable" data-testid="usage-keys-sort-tokens" onClick={() => toggleSort('totalTokens')}>
                      {t('usageKeys.colTokens')}
                    </button>
                  </th>
                  <th>
                    <button className="sortable" data-testid="usage-keys-sort-cost" onClick={() => toggleSort('totalCostCents')}>
                      {t('usageKeys.colCost')}
                    </button>
                  </th>
                  <th>
                    <button className="sortable" data-testid="usage-keys-sort-avg" onClick={() => toggleSort('avgLatencyMs')}>
                      {t('usageKeys.colAvgLatency')}
                    </button>
                  </th>
                  <th>
                    <button className="sortable" data-testid="usage-keys-sort-p95" onClick={() => toggleSort('p95LatencyMs')}>
                      {t('usageKeys.colP95')}
                    </button>
                  </th>
                  <th>{t('usageKeys.colDataThrough')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {pageRows.map((k) => (
                  <tr key={k.apiKeyId} data-testid={`usage-keys-row-${k.apiKeyId}`}>
                    <td>
                      <a
                        href={`/admin/usage/keys/${k.apiKeyId}`}
                        onClick={(e) => {
                          e.preventDefault();
                          navigate(`/admin/usage/keys/${k.apiKeyId}`);
                        }}
                      >
                        {k.apiKeyName || k.apiKeyId}
                      </a>
                    </td>
                    <td>{k.organizationId || '—'}</td>
                    <td>{parseInt(k.requestCount || '0', 10).toLocaleString()}</td>
                    <td>{errorRatePercent(k)}</td>
                    <td>{parseInt(k.totalTokens || '0', 10).toLocaleString()}</td>
                    <td>{costDollars(k.totalCostCents)}</td>
                    <td>{k.avgLatencyMs || '0'} ms</td>
                    <td>{k.p95LatencyMs || '0'} ms</td>
                    <td>{k.dataThrough ? formatTime(k.dataThrough) : '—'}</td>
                    <td>
                      <button
                        className="secondary"
                        data-testid={`usage-keys-view-${k.apiKeyId}`}
                        onClick={() => navigate(`/admin/usage/keys/${k.apiKeyId}`)}
                      >
                        {t('usageKeys.view')}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Pagination
              offset={offset}
              limit={PAGE_SIZE}
              total={sortedKeys.length}
              onPageChange={setOffset}
            />
          </>
        )}
      </div>
    </div>
  );
}