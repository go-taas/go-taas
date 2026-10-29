// End-user Forecast page (feature #36): the tenant's own usage & cost
// forecast — historical trend, forecast line, and confidence band, with
// a metric switcher and a horizon control. User surface: route /forecast,
// API /api/v1/forecast. Tenant-scoped and masked (AD1): the dimension
// control offers Model / API key only.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { formatMoney } from '../../display';
import { ErrorBanner } from '../../components';
import ForecastChart, { type ForecastMetric } from '../../components/ForecastChart';

interface ForecastBucket {
  bucket: string;
  totalTokens: string;
  totalCostCents: string;
}

interface ForecastPoint {
  bucket: string;
  totalTokens: string;
  totalCostCents: string;
  lowerTokens: string;
  upperTokens: string;
  lowerCostCents: string;
  upperCostCents: string;
}

interface GetForecastResponse {
  response: { code: number; message: string };
  method: string;
  horizonDays: string;
  dataThrough: string;
  history: ForecastBucket[];
  forecast: ForecastPoint[];
  summary: { totalTokens: string; totalCostCents: string };
}

const RANGE_PRESETS = [
  { id: '24h', labelKey: 'forecast.range24h', hours: 24 },
  { id: '7d', labelKey: 'forecast.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'forecast.range30d', hours: 30 * 24 },
];
const DIMENSIONS = ['model', 'api_key'];
const HORIZONS = [7, 30, 90];

function rangeFor(preset: string): { since: number; until: number } {
  const now = Math.floor(Date.now() / 1000);
  const hours = RANGE_PRESETS.find((p) => p.id === preset)?.hours || 24;
  return { since: now - hours * 3600, until: now };
}

export default function UserForecastPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [preset, setPreset] = useState('30d');
  const [dimension, setDimension] = useState('model');
  const [dimensionValue, setDimensionValue] = useState('');
  const [horizon, setHorizon] = useState(30);
  const [metric, setMetric] = useState<ForecastMetric>('tokens');
  const [data, setData] = useState<GetForecastResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  const range = useMemo(() => rangeFor(preset), [preset]);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams({
        since: String(range.since),
        until: String(range.until),
        dimension,
        horizon_days: String(horizon),
      });
      if (dimensionValue) params.set('dimension_value', dimensionValue);
      const next = await api.get<GetForecastResponse>(
        `/api/v1/forecast?${params.toString()}`,
        orgId,
      );
      setData(next);
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('forecast.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [api, orgId, range.since, range.until, dimension, dimensionValue, horizon, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const history = data?.history || [];
  const forecast = data?.forecast || [];
  const summary = data?.summary;
  const dataThrough = data ? parseInt(data.dataThrough || '0', 10) : 0;

  return (
    <div data-testid="forecast-page">
      <div className="page-header">
        <div>
          <h1 data-testid="forecast-title">{t('forecast.title')}</h1>
          <div className="subtitle">{t('forecast.subtitle')}</div>
        </div>
        <button
          className="secondary"
          data-testid="forecast-refresh"
          disabled={loading}
          onClick={() => void load()}
        >
          {t('forecast.refresh')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}
      {stale && data && (
        <div className="error-banner" data-testid="forecast-stale-banner">
          {t('forecast.staleData')}
        </div>
      )}

      <div className="filter-bar" data-testid="forecast-filter-bar">
        <label>
          {t('forecast.timeRange')}
          <select
            data-testid="forecast-range-select"
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
        <label>
          {t('forecast.dimension')}
          <select
            data-testid="forecast-dimension-select"
            value={dimension}
            onChange={(e) => {
              setDimension(e.target.value);
              setDimensionValue('');
            }}
          >
            {DIMENSIONS.map((d) => (
              <option key={d} value={d}>
                {t(`forecast.dim${d[0].toUpperCase()}${d.slice(1)}`)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('forecast.horizon')}
          <select
            data-testid="forecast-horizon-select"
            value={horizon}
            disabled={loading}
            onChange={(e) => setHorizon(Number(e.target.value))}
          >
            {HORIZONS.map((h) => (
              <option key={h} value={h}>
                {h} {t('forecast.days')}
              </option>
            ))}
          </select>
        </label>
      </div>

      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : history.length === 0 ? (
        <div className="empty-state" data-testid="forecast-empty">
          {t('forecast.empty')}
          <div className="muted">{t('forecast.emptyHint')}</div>
        </div>
      ) : (
        <>
          <div className="cards-row" data-testid="forecast-cards">
            <div className="card">
              <div className="card-label">{t('forecast.forecastTokens')}</div>
              <div className="card-value" data-testid="forecast-tokens">
                {summary ? summary.totalTokens : '0'}
              </div>
            </div>
            <div className="card">
              <div className="card-label">{t('forecast.forecastCost')}</div>
              <div className="card-value" data-testid="forecast-cost">
                {formatMoney(summary ? summary.totalCostCents : '0')}
              </div>
            </div>
            <div className="card">
              <div className="card-label">{t('forecast.confidence')}</div>
              <div className="card-value" data-testid="forecast-confidence">
                {forecast.length > 0
                  ? `${Math.round((parseInt(forecast[forecast.length - 1].upperTokens || '0', 10) - parseInt(forecast[forecast.length - 1].lowerTokens || '0', 10)) / 2)}`
                  : '0'}
              </div>
            </div>
            <div className="card">
              <div className="card-label">{t('forecast.dataThrough')}</div>
              <div className="card-value mono" data-testid="forecast-data-through">
                {dataThrough > 0 ? new Date(dataThrough * 1000).toLocaleString() : '—'}
              </div>
            </div>
          </div>

          <div className="panel">
            <div className="metric-switcher">
              <button
                className={metric === 'tokens' ? '' : 'secondary'}
                data-testid="forecast-metric-tokens"
                onClick={() => setMetric('tokens')}
              >
                {t('forecast.tokens')}
              </button>
              <button
                className={metric === 'cost' ? '' : 'secondary'}
                data-testid="forecast-metric-cost"
                onClick={() => setMetric('cost')}
              >
                {t('forecast.cost')}
              </button>
            </div>
            <ForecastChart history={history} forecast={forecast} metric={metric} />
          </div>
        </>
      )}
    </div>
  );
}