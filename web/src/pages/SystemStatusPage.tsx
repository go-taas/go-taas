// Admin System Status page (feature #30): the platform component health
// — an overall status banner, a component health list, and a status-page
// summary. Admin surface: route /admin/status, API /api/v1/admin/status.
// Admin-only (AD1): there is no end-user surface.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { ErrorBanner } from '../components';
import StatusBadge from '../components/StatusBadge';
import UptimeBar from '../components/UptimeBar';
import { formatTime, type GetSystemStatusResponse } from '../api';

// overallLabel maps the overall status to a localized display label.
function overallLabel(status: string, t: (key: string) => string): string {
  switch (status) {
    case 'operational':
      return t('status.operational');
    case 'degraded':
      return t('state.degraded');
    default:
      return t('status.outage');
  }
}

// formatUptime formats uptime seconds as a human-readable duration.
function formatUptime(seconds: string): string {
  const s = parseInt(seconds || '0', 10);
  if (s <= 0) return '—';
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  if (d > 0) return `${d}d ${h}h`;
  const m = Math.floor((s % 3600) / 60);
  return `${h}h ${m}m`;
}

export default function SystemStatusPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [data, setData] = useState<GetSystemStatusResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const next = await api.get<GetSystemStatusResponse>('/api/v1/admin/status', orgId);
      setData(next);
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('status.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const overall = data?.overallStatus || '';
  const components = data?.components || [];
  const statusPage = data?.statusPage;
  const lastChecked = data?.lastCheckedAt;

  return (
    <div data-testid="status-page">
      <div className="page-header">
        <div>
          <h1>{t('status.title')}</h1>
          <div className="subtitle">{t('status.subtitle')}</div>
        </div>
        <button
          className="secondary"
          data-testid="status-refresh"
          disabled={loading}
          onClick={() => void load()}
        >
          {t('status.refresh')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}
      {stale && data && (
        <div className="error-banner" data-testid="status-stale-banner">
          {t('status.staleData')}
        </div>
      )}

      {overall && (
        <div className="status-overall" data-testid="status-overall">
          <StatusBadge status={overall} />
          <span className="status-overall-label">{overallLabel(overall, t)}</span>
          <span className="muted" data-testid="status-last-checked">
            {t('status.lastChecked', { time: lastChecked ? formatTime(lastChecked) : '—' })}
          </span>
        </div>
      )}

      <div className="panel" style={{ marginTop: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('status.componentsTitle')}</h3>
        {components.length === 0 ? (
          <p className="muted" data-testid="status-empty">
            {t('status.empty')}
          </p>
        ) : (
          <table className="data" data-testid="status-components">
            <thead>
              <tr>
                <th>{t('status.colComponent')}</th>
                <th>{t('status.colType')}</th>
                <th>{t('status.colStatus')}</th>
                <th>{t('status.colUptime')}</th>
                <th>{t('status.colLastChecked')}</th>
                <th>{t('status.colDependencies')}</th>
              </tr>
            </thead>
            <tbody>
              {components.map((c) => (
                <tr key={c.componentId} data-testid={`status-component-${c.componentId}`}>
                  <td>{c.componentName || c.componentId}</td>
                  <td>{c.componentType}</td>
                  <td>
                    <StatusBadge status={c.status} />
                  </td>
                  <td>
                    <UptimeBar uptimeSeconds={c.uptimeSeconds} />
                    <span className="muted">{formatUptime(c.uptimeSeconds)}</span>
                  </td>
                  <td>{c.lastCheckedAt ? formatTime(c.lastCheckedAt) : '—'}</td>
                  <td>
                    {(c.dependencies || []).length === 0 ? (
                      <span className="muted">—</span>
                    ) : (
                      <div className="dependency-list">
                        {(c.dependencies || []).map((d) => (
                          <span key={d.dependencyId} className="dependency-item">
                            {d.dependencyName}
                            <StatusBadge status={d.status} />
                          </span>
                        ))}
                      </div>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {statusPage && (
        <div className="panel" style={{ marginTop: 16 }} data-testid="status-page-summary">
          <h3 style={{ marginTop: 0 }}>{t('status.summaryTitle')}</h3>
          <div className="card-row">
            <div className="card">
              <div className="card-value">{overallLabel(statusPage.overallStatus, t)}</div>
              <div className="card-label">{t('status.summaryOverall')}</div>
            </div>
            <div className="card">
              <div className="card-value">{statusPage.componentCount || '0'}</div>
              <div className="card-label">{t('status.summaryComponents')}</div>
            </div>
            <div className="card">
              <div className="card-value">{statusPage.lastCheckedAt ? formatTime(statusPage.lastCheckedAt) : '—'}</div>
              <div className="card-label">{t('status.summaryLastChecked')}</div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}