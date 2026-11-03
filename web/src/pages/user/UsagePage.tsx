// End-user usage page (feature-17, redesigned as a usage report).
//
// One page answers "what did I spend, on what, and when": the balance and the
// all-time total on top, then a range/API-key/model filter bar, the headline
// figures for that selection, a daily chart, and a per-model breakdown so the
// shape of one model's traffic is visible without changing the filter.
//
// The panel is scoped to the caller's own organization (the user surface never
// accepts an organization header) and every figure comes from the metering
// dashboard, which joins raw usage with the billing charge records: tokens are
// truthful even before settlement prices the hour.

import { useEffect, useMemo, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import {
  formatDay,
  type ApiKeySummary,
  type BalanceResponse,
  type DailyBucket,
  type DashboardGroup,
  type ModelSummary,
  type UsageDashboardResponse,
} from '../../api';
import { formatMajor, formatMicros, formatMoney } from '../../display';
import UsageBarChart, { type UsageBarValueFormatter } from '../../components/UsageBarChart';
import UsageAreaChart from '../../components/UsageAreaChart';

/** ChartMetric is the headline chart's subject. */
type ChartMetric = 'cost' | 'tokens' | 'requests';

/** RangePreset is one entry of the time-range menu. */
interface RangePreset {
  id: string;
  labelKey: string;
}

const RANGE_PRESETS: RangePreset[] = [
  { id: 'today', labelKey: 'uusage.rangeToday' },
  { id: 'yesterday', labelKey: 'uusage.rangeYesterday' },
  { id: '7d', labelKey: 'uusage.range7d' },
  { id: '30d', labelKey: 'uusage.range30d' },
  { id: 'month', labelKey: 'uusage.rangeMonth' },
  { id: 'lastMonth', labelKey: 'uusage.rangeLastMonth' },
  { id: 'custom', labelKey: 'uusage.rangeCustom' },
];

// PER_MODEL_LIMIT bounds the per-model breakdown. A long tail of one-request
// models would push the useful cards off the page, and the model filter
// already reaches any single one.
const PER_MODEL_LIMIT = 6;

const DAY = 86400;

/** utcDayStart returns the UTC midnight at or before the given second. */
function utcDayStart(seconds: number): number {
  return Math.floor(seconds / DAY) * DAY;
}

/** utcMonthStart returns the first UTC day of the given second's month. */
function utcMonthStart(seconds: number): number {
  const date = new Date(seconds * 1000);
  return Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), 1) / 1000;
}

/**
 * rangeFor resolves a preset to a [since, until) pair.
 *
 * The boundaries are UTC days because that is how the server buckets usage:
 * a local midnight would put the first bar half outside the requested range,
 * and the bar would then disagree with the cards beside it.
 */
export function rangeFor(
  preset: string,
  customSince: string,
  customUntil: string,
  now = Math.floor(Date.now() / 1000),
): { since: number; until: number } {
  const today = utcDayStart(now);
  switch (preset) {
    case 'today':
      return { since: today, until: now };
    case 'yesterday':
      return { since: today - DAY, until: today };
    case 'month':
      return { since: utcMonthStart(now), until: now };
    case 'lastMonth': {
      const thisMonth = utcMonthStart(now);
      return { since: utcMonthStart(thisMonth - DAY), until: thisMonth };
    }
    case '30d':
      return { since: today - 29 * DAY, until: now };
    case 'custom': {
      const until = customUntil ? Math.floor(new Date(customUntil).getTime() / 1000) : now;
      const since = customSince ? Math.floor(new Date(customSince).getTime() / 1000) : until - DAY;
      return { since, until: Math.max(until, since + 1) };
    }
    default:
      return { since: today - 6 * DAY, until: now };
  }
}

/** tokensOf is a group's prompt + completion tokens. */
function tokensOf(group: DashboardGroup): number {
  return parseInt(group.promptTokens || '0', 10) + parseInt(group.completionTokens || '0', 10);
}

/** microsOf is a group's cost in micro-units. */
function microsOf(group: DashboardGroup): number {
  return parseInt(group.costMicros || '0', 10);
}

/** requestsOf is a group's request count. */
function requestsOf(group: DashboardGroup): number {
  return parseInt(group.requestCount || '0', 10);
}

/**
 * formatCompact renders a token or request count for an axis label: exact up
 * to a thousand, then a scaled suffix, so "1.2M" fits where "1,234,567"
 * cannot.
 */
export function formatCompact(value: number): string {
  const abs = Math.abs(value);
  if (abs >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`;
  if (abs >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (abs >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return value.toFixed(0);
}

/** metricValue reads the chosen metric out of one group. */
function metricValue(group: DashboardGroup, metric: ChartMetric): number {
  switch (metric) {
    case 'cost':
      return microsOf(group) / 1_000_000;
    case 'requests':
      return requestsOf(group);
    default:
      return tokensOf(group);
  }
}

/** metricTotal sums the chosen metric over a day's groups. */
function metricTotal(groups: DashboardGroup[], metric: ChartMetric): number {
  return groups.reduce((sum, group) => sum + metricValue(group, metric), 0);
}

/** metricLabelKey is the locale key naming a metric. */
function metricLabelKey(metric: ChartMetric): string {
  if (metric === 'cost') return 'uusage.metricCost';
  if (metric === 'tokens') return 'uusage.metricTokens';
  return 'uusage.metricRequests';
}

/** csvCell quotes a value for a comma-separated file. */
function csvCell(value: string | number): string {
  const text = String(value);
  return /[",\n]/.test(text) ? `"${text.replace(/"/g, '""')}"` : text;
}

/**
 * downloadCsv hands a generated file to the browser.
 *
 * The link is attached to the document and the object URL is released on the
 * next tick: a detached link's click is ignored by some browsers, and revoking
 * the URL in the same tick can cancel the download it just started.
 */
function downloadCsv(filename: string, rows: string[][]): void {
  const body = rows.map((row) => row.map(csvCell).join(',')).join('\n');
  const url = URL.createObjectURL(new Blob([body], { type: 'text/csv;charset=utf-8' }));
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();
  setTimeout(() => {
    link.remove();
    URL.revokeObjectURL(url);
  }, 0);
}

export default function UsagePage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [preset, setPreset] = useState('7d');
  const [customSince, setCustomSince] = useState('');
  const [customUntil, setCustomUntil] = useState('');
  const [modelId, setModelId] = useState('');
  const [keyId, setKeyId] = useState('');
  const [metric, setMetric] = useState<ChartMetric>('cost');
  const [dashboard, setDashboard] = useState<UsageDashboardResponse | null>(null);
  const [balance, setBalance] = useState<BalanceResponse | null>(null);
  const [keys, setKeys] = useState<ApiKeySummary[]>([]);
  const [models, setModels] = useState<ModelSummary[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  const range = useMemo(
    () => rangeFor(preset, customSince, customUntil),
    [preset, customSince, customUntil],
  );
  const { since, until } = range;

  // The pickers are loaded once: they change rarely and reloading them on
  // every filter change would make the menus flicker.
  useEffect(() => {
    if (!orgId) {
      setKeys([]);
      setModels([]);
      return;
    }
    api
      .get<{ keys?: ApiKeySummary[] }>('/api/v1/auth/api-keys', orgId)
      .then((data) => setKeys(data.keys || []))
      .catch(() => setKeys([]));
    api
      .get<{ models?: ModelSummary[] }>('/api/v1/models?page.limit=100', orgId)
      .then((data) => setModels(data.models || []))
      .catch(() => setModels([]));
  }, [api, orgId]);

  useEffect(() => {
    if (!orgId) {
      // No organization: there is nothing to aggregate. Say so instead of
      // showing an empty report that looks like a data problem.
      setDashboard(null);
      setBalance(null);
      setError('');
      return;
    }
    let active = true;
    setLoading(true);
    const params = new URLSearchParams({
      since: String(since),
      until: String(until),
      group_by: 'model',
    });
    if (modelId) params.set('model_id', modelId);
    if (keyId) params.set('api_key_id', keyId);
    api
      .get<UsageDashboardResponse>(`/api/v1/metering/usage-dashboard?${params.toString()}`, orgId)
      .then((data) => {
        if (!active) return;
        setDashboard(data);
        setError('');
      })
      .catch((e) => {
        if (!active) return;
        setError(e instanceof Error ? e.message : t('uusage.loadFailed'));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [api, orgId, since, until, modelId, keyId, t]);

  useEffect(() => {
    if (!orgId) return;
    api
      .get<BalanceResponse>('/api/v1/billing/balance', orgId)
      .then(setBalance)
      .catch(() => {
        // Balance may be absent (no account); not fatal.
      });
  }, [api, orgId]);

  const buckets: DailyBucket[] = dashboard?.dailyBuckets || [];
  const cards = dashboard?.cards;

  // modelNames resolves a group key (a model id) to its catalog name, so the
  // breakdown reads "qwen" instead of a uuid.
  const modelNames = useMemo(() => {
    const names = new Map<string, string>();
    for (const model of models) names.set(model.modelId, model.name);
    return names;
  }, [models]);

  const chartPoints = useMemo(
    () =>
      buckets.map((bucket) => ({
        dayStart: parseInt(bucket.date || '0', 10),
        value: metricTotal(bucket.groups || [], metric),
      })),
    [buckets, metric],
  );

  // perModel folds the daily groups into one series per model, then keeps the
  // busiest few: the page is a report, not an inventory.
  const perModel = useMemo(() => {
    const series = new Map<string, { dayStart: number; requests: number; tokens: number }[]>();
    for (const bucket of buckets) {
      const dayStart = parseInt(bucket.date || '0', 10);
      for (const group of bucket.groups || []) {
        const key = group.groupKey || '';
        const rows = series.get(key) || [];
        rows.push({ dayStart, requests: requestsOf(group), tokens: tokensOf(group) });
        series.set(key, rows);
      }
    }
    return Array.from(series.entries())
      .map(([key, rows]) => ({
        key,
        rows: rows.sort((a, b) => a.dayStart - b.dayStart),
        requests: rows.reduce((sum, row) => sum + row.requests, 0),
        tokens: rows.reduce((sum, row) => sum + row.tokens, 0),
      }))
      .sort((a, b) => b.requests - a.requests || b.tokens - a.tokens)
      .slice(0, PER_MODEL_LIMIT);
  }, [buckets]);

  const modelOptions = useMemo(() => {
    const seen = new Map<string, string>();
    for (const model of models) seen.set(model.modelId, model.name);
    for (const bucket of buckets) {
      for (const group of bucket.groups || []) {
        const key = group.groupKey || '';
        if (key && !seen.has(key)) seen.set(key, key);
      }
    }
    return Array.from(seen.entries()).map(([id, name]) => ({ id, name }));
  }, [models, buckets]);

  const currency = cards?.currency || undefined;
  const unpriced = Number(cards?.unpricedRequestCount || 0);
  const hasFilters = Boolean(modelId || keyId);
  const totalTokens = cards
    ? parseInt(cards.promptTokens || '0', 10) + parseInt(cards.completionTokens || '0', 10)
    : 0;

  // The chart's axis follows the metric: an amount for cost, a compact count
  // otherwise. The amount's precision comes from the axis maximum, not from
  // each tick: a sub-cent range would otherwise label every gridline "0.00".
  const axisCostMax = Math.max(0, ...chartPoints.map((point) => point.value));
  const costDigits =
    axisCostMax === 0 ? 2 : axisCostMax >= 10 ? 0 : axisCostMax >= 1 ? 2 : axisCostMax >= 0.001 ? 4 : 6;
  const formatAxis: UsageBarValueFormatter = (value) =>
    metric === 'cost' ? formatMajor(value, costDigits, currency) : formatCompact(value);

  const chartTotal = () => {
    if (metric === 'cost') return formatMicros(cards?.totalCostMicros, currency);
    if (metric === 'requests') return Number(cards?.requestCount || 0).toLocaleString();
    return totalTokens.toLocaleString();
  };

  const exportCsv = () => {
    const rows: string[][] = [
      [t('uusage.colDate'), t('uusage.colModel'), t('uusage.colCost'), t('uusage.colTokens'), t('uusage.colRequests')],
    ];
    for (const bucket of buckets) {
      for (const group of bucket.groups || []) {
        rows.push([
          formatDay(bucket.date),
          modelNames.get(group.groupKey || '') || group.groupKey || '',
          (microsOf(group) / 1_000_000).toFixed(6),
          String(tokensOf(group)),
          String(requestsOf(group)),
        ]);
      }
    }
    downloadCsv(`usage-${formatDay(String(since))}-${formatDay(String(until))}.csv`, rows);
  };

  return (
    <div className="page" data-testid="usage-dashboard-cards">
      <div className="page-header">
        <div>
          <h1>{t('uusage.title')}</h1>
          <div className="subtitle">{t('uusage.subtitle')}</div>
        </div>
      </div>

      {error && <div className="error">{error}</div>}
      {!orgId && (
        <div className="empty-state" data-testid="usage-no-org">
          {t('uusage.noOrg')}
        </div>
      )}

      {orgId && (
        <>
          <div className="usage-summary-row" data-testid="usage-balance-widget">
            <div className="card usage-summary-card">
              <div className="card-label">
                {balance?.mode === 'postpaid' ? t('uusage.quotaLabel') : t('uusage.balanceLabel')}
              </div>
              <div className="card-value">
                {balance?.mode === 'postpaid'
                  ? formatMoney(balance.monthlyQuotaCents, currency)
                  : formatMoney(balance?.balanceCents, currency)}
              </div>
              <div className="card-note">
                {balance?.mode === 'postpaid'
                  ? t('uusage.quotaUsed', { used: formatMoney(balance.usedThisCycleCents, currency) })
                  : t('uusage.balanceNote')}
              </div>
            </div>
            <div className="card usage-summary-card" data-testid="usage-lifetime">
              <div className="card-label">{t('uusage.lifetime')}</div>
              <div className="card-value">{formatMicros(cards?.lifetimeCostMicros, currency)}</div>
              <div className="card-note">{t('uusage.lifetimeNote')}</div>
            </div>
          </div>

          <div className="usage-filter-bar" data-testid="usage-filter-bar">
            <label className="usage-filter">
              <span className="usage-filter-label">{t('uusage.range')}</span>
              <select
                data-testid="usage-filter-range"
                value={preset}
                onChange={(e) => setPreset(e.target.value)}
              >
                {RANGE_PRESETS.map((p) => (
                  <option key={p.id} value={p.id}>
                    {t(p.labelKey)}
                  </option>
                ))}
              </select>
            </label>
            <label className="usage-filter">
              <span className="usage-filter-label">{t('uusage.apiKey')}</span>
              <select data-testid="usage-filter-key" value={keyId} onChange={(e) => setKeyId(e.target.value)}>
                <option value="">{t('uusage.allKeys')}</option>
                {keys.map((key) => (
                  <option key={key.keyId} value={key.keyId}>
                    {key.name} ({key.prefix})
                  </option>
                ))}
              </select>
            </label>
            <label className="usage-filter">
              <span className="usage-filter-label">{t('uusage.model')}</span>
              <select data-testid="usage-filter-model" value={modelId} onChange={(e) => setModelId(e.target.value)}>
                <option value="">{t('uusage.allModels')}</option>
                {modelOptions.map((model) => (
                  <option key={model.id} value={model.id}>
                    {model.name}
                  </option>
                ))}
              </select>
            </label>
            {preset === 'custom' && (
              <>
                <input
                  type="datetime-local"
                  data-testid="usage-filter-since"
                  value={customSince}
                  onChange={(e) => setCustomSince(e.target.value)}
                />
                <input
                  type="datetime-local"
                  data-testid="usage-filter-until"
                  value={customUntil}
                  onChange={(e) => setCustomUntil(e.target.value)}
                />
              </>
            )}
            {hasFilters && (
              <button
                className="link"
                data-testid="usage-clear-filters"
                onClick={() => {
                  setModelId('');
                  setKeyId('');
                }}
              >
                {t('uusage.clearFilters')}
              </button>
            )}
            <span className="usage-filter-spacer" />
            <button
              className="secondary"
              data-testid="usage-export"
              disabled={buckets.length === 0}
              onClick={exportCsv}
            >
              {t('uusage.export')}
            </button>
          </div>

          <div className="cards-row" data-testid="usage-metric-row">
            <div className="card" data-testid="usage-metric-cost">
              <div className="card-label">{t('uusage.metricCost')}</div>
              <div className="card-value">{formatMicros(cards?.totalCostMicros, currency)}</div>
              <div className="card-note">{currency || ''}</div>
            </div>
            <div className="card" data-testid="usage-metric-requests">
              <div className="card-label">{t('uusage.metricRequests')}</div>
              <div className="card-value">{Number(cards?.requestCount || 0).toLocaleString()}</div>
              <div className="card-note">{t('uusage.requestsNote')}</div>
            </div>
            <div className="card" data-testid="usage-metric-tokens">
              <div className="card-label">{t('uusage.metricTokens')}</div>
              <div className="card-value">{totalTokens.toLocaleString()}</div>
              <div className="card-note">
                {t('uusage.tokensSplit', {
                  prompt: Number(cards?.promptTokens || 0).toLocaleString(),
                  completion: Number(cards?.completionTokens || 0).toLocaleString(),
                })}
              </div>
            </div>
          </div>

          {unpriced > 0 && (
            <div className="hint" data-testid="usage-unpriced-note">
              {t('uusage.unpriced', { n: unpriced.toLocaleString() })}
            </div>
          )}

          <div className="panel usage-chart-panel" data-testid="usage-chart-panel">
            <div className="panel-header usage-chart-header">
              <div>
                <div className="usage-chart-title">{t(metricLabelKey(metric))}</div>
                <div className="usage-chart-total" data-testid="usage-chart-total">
                  {chartTotal()}
                </div>
              </div>
              <div className="segmented" role="group" aria-label={t('uusage.metric')}>
                {(['cost', 'tokens', 'requests'] as ChartMetric[]).map((m) => (
                  <button
                    key={m}
                    className={metric === m ? 'active' : ''}
                    data-testid={`usage-metric-toggle-${m}`}
                    onClick={() => setMetric(m)}
                  >
                    {t(metricLabelKey(m))}
                  </button>
                ))}
              </div>
            </div>
            {loading && !dashboard ? (
              <div className="empty-state">{t('uusage.loading')}</div>
            ) : (
              <UsageBarChart
                points={chartPoints}
                formatValue={formatAxis}
                ariaLabel={t('uusage.chartAria')}
                emptyLabel={t('uusage.empty')}
              />
            )}
          </div>

          {perModel.length > 0 && (
            <div className="usage-per-model" data-testid="usage-per-model">
              {perModel.map((model) => (
                <section key={model.key} className="usage-model-section" data-testid={`usage-model-${model.key}`}>
                  <h2 className="usage-model-title">{modelNames.get(model.key) || model.key}</h2>
                  <div className="usage-model-charts">
                    <div className="card">
                      <div className="card-label">
                        {t('uusage.metricRequests')} <strong>{model.requests.toLocaleString()}</strong>
                      </div>
                      <UsageAreaChart
                        points={model.rows.map((row) => ({ dayStart: row.dayStart, value: row.requests }))}
                        formatValue={formatCompact}
                        ariaLabel={t('uusage.chartAriaModel', { model: modelNames.get(model.key) || model.key })}
                      />
                    </div>
                    <div className="card">
                      <div className="card-label">
                        {t('uusage.metricTokens')} <strong>{model.tokens.toLocaleString()}</strong>
                      </div>
                      <UsageAreaChart
                        points={model.rows.map((row) => ({ dayStart: row.dayStart, value: row.tokens }))}
                        formatValue={formatCompact}
                        ariaLabel={t('uusage.chartAriaModel', { model: modelNames.get(model.key) || model.key })}
                      />
                    </div>
                  </div>
                </section>
              ))}
            </div>
          )}

          {!loading && buckets.every((bucket) => (bucket.groups || []).length === 0) && (
            <div className="empty" data-testid="usage-empty">
              {t('uusage.empty')}
            </div>
          )}
        </>
      )}
    </div>
  );
}
