// Clusters page (admin): the cluster registry with health and workload
// counts, and the Register cluster action.
// Implements docs/design/multi-cluster-management.md FR1-FR2 and
// docs/architecture/multi-cluster-management.md §6.5.

import { useCallback, useEffect, useState } from 'react';
import { api, type ClusterSummary } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { BackLink, ErrorBanner, StateBadge } from '../components';
import { navigate } from '../router';

const STATE_FILTERS = ['', 'active', 'disabled'];
const HEALTH_FILTERS = ['', 'healthy', 'degraded', 'unknown'];

export default function ClustersPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [clusters, setClusters] = useState<ClusterSummary[]>([]);
  const [stateFilter, setStateFilter] = useState('');
  const [healthFilter, setHealthFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  // Register dialog.
  const [regOpen, setRegOpen] = useState(false);
  const [regName, setRegName] = useState('');
  const [regRegion, setRegRegion] = useState('');
  const [regKubeconfig, setRegKubeconfig] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.get<{ clusters?: ClusterSummary[] }>('/api/v1/admin/clusters', orgId);
      setClusters(data.clusters || []);
      setError('');
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('clusters.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const register = async () => {
    try {
      await api.post<{ clusterId: string }>('/api/v1/admin/clusters', orgId, {
        name: regName,
        region: regRegion,
        kubeconfigRef: regKubeconfig,
      });
      setRegOpen(false);
      setRegName('');
      setRegRegion('');
      setRegKubeconfig('');
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('clusters.registerFailed'));
    }
  };

  const filtered = clusters.filter(
    (c) => (!stateFilter || c.state === stateFilter) && (!healthFilter || c.health === healthFilter),
  );

  return (
    <div>
      <BackLink to="/admin/inference-services" label={t('clusters.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="clusters-title">{t('clusters.title')}</h1>
          <div className="subtitle">{t('clusters.subtitle')}</div>
        </div>
        <button className="secondary" data-testid="clusters-refresh" onClick={() => void load()} disabled={loading}>
          {t('clusters.refresh')}
        </button>
        <button className="primary" data-testid="clusters-register" onClick={() => setRegOpen(true)}>
          {t('clusters.register')}
        </button>
      </div>

      {error && (
        <div>
          <ErrorBanner message={error} />
          {stale && <div className="muted">{t('clusters.stale')}</div>}
          <button className="secondary" data-testid="clusters-retry" onClick={() => void load()}>
            {t('clusters.retry')}
          </button>
        </div>
      )}

      <div className="filter-bar" data-testid="clusters-filter-bar">
        <label>
          {t('clusters.stateFilter')}
          <select data-testid="clusters-state-filter" value={stateFilter} onChange={(e) => setStateFilter(e.target.value)}>
            {STATE_FILTERS.map((s) => (
              <option key={s} value={s}>
                {s === '' ? t('clusters.allStates') : s}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('clusters.healthFilter')}
          <select data-testid="clusters-health-filter" value={healthFilter} onChange={(e) => setHealthFilter(e.target.value)}>
            {HEALTH_FILTERS.map((h) => (
              <option key={h} value={h}>
                {h === '' ? t('clusters.allHealth') : h}
              </option>
            ))}
          </select>
        </label>
      </div>

      {!loading && !error && filtered.length === 0 && (
        <div className="panel" data-testid="clusters-empty">
          <p>{t('clusters.empty')}</p>
          <p className="muted">{t('clusters.emptyHint')}</p>
        </div>
      )}

      {!loading && !error && filtered.length > 0 && (
        <div className="panel" data-testid="clusters-list">
          <table className="table">
            <thead>
              <tr>
                <th>{t('clusters.name')}</th>
                <th>{t('clusters.region')}</th>
                <th>{t('clusters.state')}</th>
                <th>{t('clusters.health')}</th>
                <th>{t('clusters.nodes')}</th>
                <th>{t('clusters.services')}</th>
                <th>{t('clusters.lastChecked')}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((c) => (
                <tr key={c.clusterId} data-testid={`clusters-cluster-${c.clusterId}`}>
                  <td>
                    <a
                      href={`/admin/clusters/${c.clusterId}`}
                      onClick={(e) => {
                        e.preventDefault();
                        navigate(`/admin/clusters/${c.clusterId}`);
                      }}
                    >
                      {c.name}
                    </a>
                  </td>
                  <td>{c.region || '—'}</td>
                  <td>
                    <StateBadge state={c.state} />
                  </td>
                  <td>
                    <span className={`badge ${c.health}`} data-testid={`clusters-health-${c.health}`}>
                      {c.health}
                    </span>
                  </td>
                  <td>{c.nodeCount}</td>
                  <td>{c.serviceCount}</td>
                  <td>{c.lastCheckedAt ? new Date(parseInt(c.lastCheckedAt, 10) * 1000).toLocaleString() : '—'}</td>
                  <td>
                    <button
                      className="secondary"
                      data-testid={`clusters-view-${c.clusterId}`}
                      onClick={() => navigate(`/admin/clusters/${c.clusterId}`)}
                    >
                      {t('clusters.view')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {regOpen && (
        <div className="dialog-backdrop">
          <div className="dialog" data-testid="clusters-register-dialog">
            <h3>{t('clusters.register')}</h3>
            <label>
              {t('clusters.name')}
              <input data-testid="clusters-reg-name" value={regName} onChange={(e) => setRegName(e.target.value)} />
            </label>
            <label>
              {t('clusters.region')}
              <input data-testid="clusters-reg-region" value={regRegion} onChange={(e) => setRegRegion(e.target.value)} />
            </label>
            <label>
              {t('clusters.kubeconfigRef')}
              <input data-testid="clusters-reg-kubeconfig" value={regKubeconfig} onChange={(e) => setRegKubeconfig(e.target.value)} />
            </label>
            <div className="dialog-actions">
              <button className="secondary" onClick={() => setRegOpen(false)}>
                {t('common.cancel')}
              </button>
              <button className="primary" data-testid="clusters-reg-submit" onClick={() => void register()}>
                {t('clusters.register')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}