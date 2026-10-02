// Service Metrics page (admin): per-service CPU/memory/GPU utilization
// over time with summary cards, a metric-switcher time-series chart, a
// per-replica breakdown, and time-range/replica filters.
// Implements docs/design/service-resource-metrics.md FR1-FR3 and
// docs/architecture/service-resource-metrics.md §6.5.

import { useCallback, useEffect, useState } from 'react';
import { api, type GetServiceResourceMetricsResponse, type ResourceMetricsReplicaRow } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { BackLink, ErrorBanner } from '../components';
import ResourceChart, { type ResourceMetric } from '../components/ResourceChart';

const RANGE_PRESETS = [
  { id: '24h', labelKey: 'usage.range24h', hours: 24 },
  { id: '7d', labelKey: 'usage.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'usage.range30d', hours: 30 * 24 },
];

function formatBytes(bytes: number): string {
  if (!isFinite(bytes) || bytes <= 0) return '—';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

function formatTime(unix: string): string {
  const n = parseInt(unix || '0', 10);
  if (!n) return '—';
  return new Date(n * 1000).toLocaleString();
}

export default function ServiceMetricsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const serviceId = window.location.pathname.split('/')[3] || '';
  const [preset, setPreset] = useState('24h');
  const [replica, setReplica] = useState('');
  const [metric, setMetric] = useState<ResourceMetric>('cpu');
  const [data, setData] = useState<GetServiceResourceMetricsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  const sinceFor = useCallback(() => {
    const now = Math.floor(Date.now() / 1000);
    const hours = RANGE_PRESETS.find((p) => p.id === preset)?.hours || 24;
    return now - hours * 3600;
  }, [preset]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams();
      params.set('since', String(sinceFor()));
      if (replica) params.set('replica', replica);
      const data = await api.get<GetServiceResourceMetricsResponse>(
        `/api/v1/admin/services/${serviceId}/metrics?${params.toString()}`,
        orgId,
      );
      setData(data);
      setError('');
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('servicemetrics.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [serviceId, orgId, replica, sinceFor, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const cards = data?.cards;
  const series = data?.series || [];
  const replicas = data?.replicas || [];
  const hasGPU = series.some((p) => parseFloat(p.gpuPercent || '0') > 0) || (cards && parseFloat(cards.currentGpuPercent || '0') > 0);

  return (
    <div>
      <BackLink to={`/admin/inference-services/${serviceId}`} label={t('servicemetrics.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="service-metrics-title">{t('servicemetrics.title')}</h1>
          <div className="subtitle mono">{serviceId}</div>
        </div>
        <button className="secondary" data-testid="metrics-refresh" onClick={() => void load()} disabled={loading}>
          {t('servicemetrics.refresh')}
        </button>
      </div>

      {error && (
        <div>
          <ErrorBanner message={error} />
          {stale && <div className="muted">{t('servicemetrics.stale')}</div>}
          <button className="secondary" data-testid="metrics-retry" onClick={() => void load()}>
            {t('servicemetrics.retry')}
          </button>
        </div>
      )}

      <div className="filter-bar" data-testid="metrics-filter-bar">
        <label>
          {t('servicemetrics.timeRange')}
          <select
            data-testid="metrics-range"
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
          {t('servicemetrics.replica')}
          <select
            data-testid="metrics-replica"
            value={replica}
            onChange={(e) => setReplica(e.target.value)}
          >
            <option value="">{t('servicemetrics.allReplicas')}</option>
            {replicas.map((r) => (
              <option key={r.replicaIndex} value={r.replicaIndex}>
                {r.replicaIndex}
              </option>
            ))}
          </select>
        </label>
      </div>

      {!loading && !error && series.length === 0 && (
        <div className="panel" data-testid="metrics-empty">
          <p>{t('servicemetrics.empty')}</p>
          <p className="muted">{t('servicemetrics.emptyHint')}</p>
        </div>
      )}

      {cards && (
        <div className="card-row" data-testid="metrics-cards">
          <div className="card" data-testid="metrics-card-cpu">
            <div className="label">{t('servicemetrics.cpu')}</div>
            <div className="value">{parseFloat(cards.currentCpuPercent || '0').toFixed(1)}%</div>
            <div className="muted" data-testid="metrics-data-through">
              {t('servicemetrics.dataThrough')} {formatTime(cards.dataThrough)}
            </div>
          </div>
          <div className="card" data-testid="metrics-card-memory">
            <div className="label">{t('servicemetrics.memory')}</div>
            <div className="value">{formatBytes(parseInt(cards.currentMemoryBytes || '0', 10))}</div>
            <div className="muted">{t('servicemetrics.dataThrough')} {formatTime(cards.dataThrough)}</div>
          </div>
          <div className="card" data-testid="metrics-card-gpu">
            <div className="label">{t('servicemetrics.gpu')}</div>
            <div className="value">
              {hasGPU ? `${parseFloat(cards.currentGpuPercent || '0').toFixed(1)}%` : t('servicemetrics.unavailable')}
            </div>
            <div className="muted">{t('servicemetrics.dataThrough')} {formatTime(cards.dataThrough)}</div>
          </div>
          <div className="card" data-testid="metrics-card-replicas">
            <div className="label">{t('servicemetrics.replicas')}</div>
            <div className="value">{cards.replicaCount}</div>
            <div className="muted">{t('servicemetrics.dataThrough')} {formatTime(cards.dataThrough)}</div>
          </div>
        </div>
      )}

      {!loading && !error && series.length > 0 && (
        <div className="panel" data-testid="metrics-chart-panel">
          <div className="metric-switcher" data-testid="metrics-metric-switcher">
            {(['cpu', 'memory', 'gpu'] as ResourceMetric[]).map((m) => (
              <button
                key={m}
                className={metric === m ? 'primary' : 'secondary'}
                data-testid={`metrics-metric-${m}`}
                onClick={() => setMetric(m)}
              >
                {t(`servicemetrics.${m}`)}
              </button>
            ))}
          </div>
          {!hasGPU && metric === 'gpu' && (
            <div className="muted" data-testid="metrics-gpu-unavailable">
              {t('servicemetrics.gpuUnavailable')}
            </div>
          )}
          <ResourceChart series={series} metric={metric} />
        </div>
      )}

      {!loading && !error && replicas.length > 0 && (
        <div className="panel" data-testid="metrics-replicas">
          <h3 style={{ marginTop: 0 }}>{t('servicemetrics.replicas')}</h3>
          <table className="table">
            <thead>
              <tr>
                <th>{t('servicemetrics.replicaCol')}</th>
                <th>{t('servicemetrics.cpuCol')}</th>
                <th>{t('servicemetrics.memoryCol')}</th>
                <th>{t('servicemetrics.gpuCol')}</th>
                <th>{t('servicemetrics.dataThroughCol')}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {replicas.map((r: ResourceMetricsReplicaRow) => (
                <tr key={r.replicaIndex} data-testid={`metrics-replica-${r.replicaIndex}`}>
                  <td className="mono">{r.replicaIndex}</td>
                  <td>{parseFloat(r.currentCpuPercent || '0').toFixed(1)}%</td>
                  <td>{formatBytes(parseInt(r.currentMemoryBytes || '0', 10))}</td>
                  <td>
                    {parseFloat(r.currentGpuPercent || '0') > 0
                      ? `${parseFloat(r.currentGpuPercent || '0').toFixed(1)}%`
                      : t('servicemetrics.unavailable')}
                  </td>
                  <td>{formatTime(r.dataThrough)}</td>
                  <td>
                    <button
                      className="secondary"
                      data-testid={`metrics-view-${r.replicaIndex}`}
                      onClick={() => setReplica(r.replicaIndex)}
                    >
                      {t('servicemetrics.view')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}