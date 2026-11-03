// Admin Observability page (feature #24): the fleet-wide model
// performance overview — summary cards, an inline-SVG time-series chart
// with a metric switcher, and a per-model table. Admin surface: route
// /admin/observability, API /api/v1/admin/observability.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { navigate } from '../router';
import { ErrorBanner, Pagination } from '../components';
import ObservabilityChart, { type ObservabilityMetric } from '../components/ObservabilityChart';
import ObservabilityCards from '../components/ObservabilityCards';
import {
  formatTime,
  type GetObservabilityOverviewResponse,
  type ModelObservabilityRow,
} from '../api';

const PAGE_SIZE = 20;
const RANGE_PRESETS = [
  { id: '24h', labelKey: 'observability.range24h', hours: 24 },
  { id: '7d', labelKey: 'observability.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'observability.range30d', hours: 30 * 24 },
  { id: 'custom', labelKey: 'observability.customRange', hours: 0 },
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
function errorRatePercent(row: ModelObservabilityRow): string {
  const req = parseInt(row.requestCount || '0', 10);
  if (req === 0) return '0.0%';
  return `${((parseInt(row.errorCount || '0', 10) / req) * 100).toFixed(1)}%`;
}

export default function ObservabilityPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [preset, setPreset] = useState('24h');
  const [customSince, setCustomSince] = useState('');
  const [customUntil, setCustomUntil] = useState('');
  const [modelFilter, setModelFilter] = useState('');
  const [metric, setMetric] = useState<ObservabilityMetric>('latency');
  const [data, setData] = useState<GetObservabilityOverviewResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [offset, setOffset] = useState(0);
  const [sortKey, setSortKey] = useState('requestCount');
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
      const next = await api.get<GetObservabilityOverviewResponse>(
        `/api/v1/admin/observability?${params.toString()}`,
        orgId,
      );
      setData(next);
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('observability.loadFailed'));
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
  const models = data?.models || [];

  // Sort the per-model table.
  const sortedModels = useMemo(() => {
    const arr = [...models];
    arr.sort((a, b) => {
      const av = parseInt(a[sortKey as keyof ModelObservabilityRow] as string || '0', 10);
      const bv = parseInt(b[sortKey as keyof ModelObservabilityRow] as string || '0', 10);
      const cmp = av - bv;
      return sortDir === 'asc' ? cmp : -cmp;
    });
    return arr;
  }, [models, sortKey, sortDir]);

  const pageRows = sortedModels.slice(offset, offset + PAGE_SIZE);

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
    <div data-testid="observability-page">
      <div className="page-header">
        <div>
          <h1>{t('observability.title')}</h1>
          <div className="subtitle">{t('observability.subtitle')}</div>
        </div>
        <button
          className="secondary"
          data-testid="observability-refresh"
          disabled={loading}
          onClick={() => void load()}
        >
          {t('observability.refresh')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}
      {stale && data && (
        <div className="error-banner" data-testid="observability-stale-banner">
          {t('observability.staleData')}
        </div>
      )}
      {pending && (
        <div className="pending-badge" data-testid="observability-pending-badge">
          {t('observability.pending')}
        </div>
      )}

      <div className="toolbar" style={{ marginBottom: 14 }} data-testid="observability-filter-range">
        {RANGE_PRESETS.map((p) => (
          <button
            key={p.id}
            className={preset === p.id ? '' : 'secondary'}
            data-testid={`observability-range-${p.id}`}
            onClick={() => setPreset(p.id)}
          >
            {t(p.labelKey)}
          </button>
        ))}
        {preset === 'custom' && (
          <>
            <input
              type="datetime-local"
              data-testid="observability-custom-since"
              value={customSince}
              onChange={(e) => setCustomSince(e.target.value)}
            />
            <input
              type="datetime-local"
              data-testid="observability-custom-until"
              value={customUntil}
              onChange={(e) => setCustomUntil(e.target.value)}
            />
          </>
        )}
        <span className="toolbar-spacer" />
        <label className="muted" htmlFor="observability-model-filter">
          {t('observability.modelFilter')}
        </label>
        <select
          id="observability-model-filter"
          data-testid="observability-filter-model"
          value={modelFilter}
          onChange={(e) => {
            setModelFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('observability.allModels')}</option>
          {models.map((m) => (
            <option key={m.modelId} value={m.modelId}>
              {m.modelName || m.modelId}
            </option>
          ))}
        </select>
      </div>

      <ObservabilityCards
        cards={cards}
        dataThroughLabel={t('observability.dataThrough')}
      />

      <div className="panel" style={{ marginTop: 16 }}>
        <div className="toolbar" style={{ marginBottom: 8 }}>
          <span className="muted">{t('observability.chartTitle')}</span>
          <span className="toolbar-spacer" />
          <div data-testid="observability-metric-toggle">
            {(['latency', 'throughput', 'errorRate', 'tokens'] as ObservabilityMetric[]).map((m) => (
              <button
                key={m}
                className={metric === m ? '' : 'secondary'}
                data-testid={`observability-metric-${m}`}
                disabled={loading}
                onClick={() => setMetric(m)}
              >
                {t(`observability.metric.${m}`)}
              </button>
            ))}
          </div>
        </div>
        <ObservabilityChart series={series} metric={metric} />
      </div>

      <div className="panel" style={{ marginTop: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('observability.modelsTitle')}</h3>
        {models.length === 0 ? (
          <p className="muted" data-testid="observability-empty">
            {t('observability.empty')}
          </p>
        ) : (
          <>
            <table className="data" data-testid="observability-table">
              <thead>
                <tr>
                  <th>{t('observability.colModel')}</th>
                  <th>
                    <button className="sortable" data-testid="observability-sort-requests" onClick={() => toggleSort('requestCount')}>
                      {t('observability.colRequests')}
                    </button>
                  </th>
                  <th>
                    <button className="sortable" data-testid="observability-sort-error" onClick={() => toggleSort('errorCount')}>
                      {t('observability.colErrorRate')}
                    </button>
                  </th>
                  <th>
                    <button className="sortable" data-testid="observability-sort-avg" onClick={() => toggleSort('avgLatencyMs')}>
                      {t('observability.colAvgLatency')}
                    </button>
                  </th>
                  <th>
                    <button className="sortable" data-testid="observability-sort-p95" onClick={() => toggleSort('p95LatencyMs')}>
                      {t('observability.colP95')}
                    </button>
                  </th>
                  <th>
                    <button className="sortable" data-testid="observability-sort-tokens" onClick={() => toggleSort('outputTokensPerSec')}>
                      {t('observability.colTokensSec')}
                    </button>
                  </th>
                  <th>{t('observability.colDataThrough')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {pageRows.map((m) => (
                  <tr key={m.modelId} data-testid={`observability-row-${m.modelId}`}>
                    <td>
                      <a
                        href={`/admin/observability/models/${m.modelId}`}
                        onClick={(e) => {
                          e.preventDefault();
                          navigate(`/admin/observability/models/${m.modelId}`);
                        }}
                      >
                        {m.modelName || m.modelId}
                      </a>
                    </td>
                    <td>{parseInt(m.requestCount || '0', 10).toLocaleString()}</td>
                    <td>{errorRatePercent(m)}</td>
                    <td>{m.avgLatencyMs || '0'} ms</td>
                    <td>{m.p95LatencyMs || '0'} ms</td>
                    <td>{(m.outputTokensPerSec || '0').toLocaleString()}</td>
                    <td>{m.dataThrough ? formatTime(m.dataThrough) : '—'}</td>
                    <td>
                      <button
                        className="link"
                        data-testid={`observability-view-${m.modelId}`}
                        onClick={() => navigate(`/admin/observability/models/${m.modelId}`)}
                      >
                        {t('observability.view')}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Pagination
              offset={offset}
              limit={PAGE_SIZE}
              total={sortedModels.length}
              onPageChange={setOffset}
            />
          </>
        )}
      </div>
    </div>
  );
}