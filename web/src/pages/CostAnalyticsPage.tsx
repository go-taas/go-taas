// Admin Cost Analytics page (feature #29): the fleet-wide cost
// attribution by dimension — summary cards, a cost trend chart with a
// metric switcher, and a dimension breakdown. Admin surface: route
// /admin/cost, API /api/v1/admin/cost.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { navigate } from '../router';
import { ErrorBanner, Pagination } from '../components';
import CostChart, { type CostMetric } from '../components/CostChart';
import CostCards from '../components/CostCards';
import CostBreakdownTable from '../components/CostBreakdownTable';
import { type GetCostAnalyticsOverviewResponse } from '../api';

const PAGE_SIZE = 20;
const RANGE_PRESETS = [
  { id: '24h', labelKey: 'cost.range24h', hours: 24 },
  { id: '7d', labelKey: 'cost.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'cost.range30d', hours: 30 * 24 },
  { id: 'custom', labelKey: 'cost.customRange', hours: 0 },
];
const DIMENSIONS = ['organization', 'model', 'api_key', 'user', 'project'];

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

export default function CostAnalyticsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [preset, setPreset] = useState('24h');
  const [customSince, setCustomSince] = useState('');
  const [customUntil, setCustomUntil] = useState('');
  const [dimension, setDimension] = useState('organization');
  const [metric, setMetric] = useState<CostMetric>('cost');
  const [data, setData] = useState<GetCostAnalyticsOverviewResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [offset, setOffset] = useState(0);

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
        dimension,
      });
      const next = await api.get<GetCostAnalyticsOverviewResponse>(
        `/api/v1/admin/cost?${params.toString()}`,
        orgId,
      );
      setData(next);
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('cost.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [api, orgId, range.since, range.until, dimension, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const cards = data?.cards || null;
  const series = data?.series || [];
  const breakdown = data?.breakdown || [];

  const pageRows = breakdown.slice(offset, offset + PAGE_SIZE);

  const dataThrough = cards ? parseInt(cards.dataThrough || '0', 10) : 0;
  const pending = dataThrough > 0 && range.until > dataThrough + 3600;

  return (
    <div data-testid="cost-page">
      <div className="page-header">
        <div>
          <h1>{t('cost.title')}</h1>
          <div className="subtitle">{t('cost.subtitle')}</div>
        </div>
        <button
          className="secondary"
          data-testid="cost-refresh"
          disabled={loading}
          onClick={() => void load()}
        >
          {t('cost.refresh')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}
      {stale && data && (
        <div className="error-banner" data-testid="cost-stale-banner">
          {t('cost.staleData')}
        </div>
      )}
      {pending && (
        <div className="pending-badge" data-testid="cost-pending-badge">
          {t('cost.pending')}
        </div>
      )}

      <div className="toolbar" style={{ marginBottom: 14 }} data-testid="cost-filter-range">
        {RANGE_PRESETS.map((p) => (
          <button
            key={p.id}
            className={preset === p.id ? '' : 'secondary'}
            data-testid={`cost-range-${p.id}`}
            onClick={() => setPreset(p.id)}
          >
            {t(p.labelKey)}
          </button>
        ))}
        {preset === 'custom' && (
          <>
            <input
              type="datetime-local"
              data-testid="cost-custom-since"
              value={customSince}
              onChange={(e) => setCustomSince(e.target.value)}
            />
            <input
              type="datetime-local"
              data-testid="cost-custom-until"
              value={customUntil}
              onChange={(e) => setCustomUntil(e.target.value)}
            />
          </>
        )}
        <span className="toolbar-spacer" />
        <label className="muted" htmlFor="cost-dimension-filter">
          {t('cost.dimensionFilter')}
        </label>
        <select
          id="cost-dimension-filter"
          data-testid="cost-filter-dimension"
          value={dimension}
          onChange={(e) => {
            setDimension(e.target.value);
            setOffset(0);
          }}
        >
          {DIMENSIONS.map((d) => (
            <option key={d} value={d}>
              {t(`cost.dimension.${d}`)}
            </option>
          ))}
        </select>
      </div>

      {cards && <CostCards cards={cards} />}

      <div className="panel" style={{ marginTop: 16 }}>
        <div className="toolbar" style={{ marginBottom: 8 }}>
          <span className="muted">{t('cost.chartTitle')}</span>
          <span className="toolbar-spacer" />
          <div data-testid="cost-metric-toggle">
            {(['cost', 'tokens', 'costPerToken'] as CostMetric[]).map((m) => (
              <button
                key={m}
                className={metric === m ? '' : 'secondary'}
                data-testid={`cost-metric-${m}`}
                disabled={loading}
                onClick={() => setMetric(m)}
              >
                {t(`cost.metric.${m}`)}
              </button>
            ))}
          </div>
        </div>
        <CostChart series={series} metric={metric} />
      </div>

      <div className="panel" style={{ marginTop: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('cost.breakdownTitle')}</h3>
        {breakdown.length === 0 ? (
          <p className="muted" data-testid="cost-empty">
            {t('cost.empty')}
          </p>
        ) : (
          <>
            <CostBreakdownTable
              rows={pageRows}
              onSelect={(value) => navigate(`/admin/cost/${dimension}/${value}`)}
            />
            <Pagination
              offset={offset}
              limit={PAGE_SIZE}
              total={breakdown.length}
              onPageChange={setOffset}
            />
          </>
        )}
      </div>
    </div>
  );
}