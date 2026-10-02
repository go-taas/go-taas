// Cluster detail page (admin): the overview, nodes, and workloads cards
// for a cluster, with the Disable action.
// Implements docs/design/multi-cluster-management.md FR2-FR4 and
// docs/architecture/multi-cluster-management.md §6.5.

import { useCallback, useEffect, useState } from 'react';
import { api, type Cluster, type ClusterWorkload } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { BackLink, ErrorBanner, StateBadge } from '../components';
import { navigate } from '../router';

export default function ClusterDetailPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const clusterId = window.location.pathname.split('/')[3] || '';
  const [cluster, setCluster] = useState<Cluster | null>(null);
  const [workloads, setWorkloads] = useState<ClusterWorkload[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [c, w] = await Promise.all([
        api.get<{ cluster?: Cluster }>(`/api/v1/admin/clusters/${clusterId}`, orgId),
        api.get<{ workloads?: ClusterWorkload[] }>(`/api/v1/admin/clusters/${clusterId}/workloads`, orgId),
      ]);
      setCluster(c.cluster || null);
      setWorkloads(w.workloads || []);
      setError('');
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('clusters.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [clusterId, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const disable = async () => {
    try {
      await api.post<{ response: { code: number } }>(`/api/v1/admin/clusters/${clusterId}:disable`, orgId);
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('clusters.disableFailed'));
    }
  };

  const summary = cluster?.summary;
  const isActive = summary?.state === 'active';

  return (
    <div>
      <BackLink to="/admin/clusters" label={t('clusters.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="clusters-detail-title">{t('clusters.detail')}</h1>
          <div className="subtitle mono">{clusterId}</div>
        </div>
        <button className="secondary" data-testid="clusters-refresh" onClick={() => void load()} disabled={loading}>
          {t('clusters.refresh')}
        </button>
        {isActive && (
          <button className="secondary danger" data-testid="clusters-disable" onClick={() => void disable()}>
            {t('clusters.disable')}
          </button>
        )}
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

      {!loading && !error && !cluster && (
        <div className="panel" data-testid="clusters-detail-empty">
          <p>{t('clusters.noClusterData')}</p>
          <p className="muted">{t('clusters.noClusterHint')}</p>
        </div>
      )}

      {summary && (
        <>
          <div className="panel" data-testid="clusters-overview">
            <h3 style={{ marginTop: 0 }}>{t('clusters.overview')}</h3>
            <div className="detail-grid">
              <div className="detail-item">
                <div className="label">{t('clusters.name')}</div>
                <div className="value">{summary.name}</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('clusters.region')}</div>
                <div className="value">{summary.region || '—'}</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('clusters.state')}</div>
                <div className="value">
                  <StateBadge state={summary.state} />
                </div>
              </div>
              <div className="detail-item">
                <div className="label">{t('clusters.health')}</div>
                <div className="value">
                  <span className={`badge ${summary.health}`}>{summary.health}</span>
                </div>
              </div>
              <div className="detail-item">
                <div className="label">{t('clusters.nodes')}</div>
                <div className="value">{summary.nodeCount}</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('clusters.services')}</div>
                <div className="value">{summary.serviceCount}</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('clusters.lastChecked')}</div>
                <div className="value">
                  {summary.lastCheckedAt ? new Date(parseInt(summary.lastCheckedAt, 10) * 1000).toLocaleString() : '—'}
                </div>
              </div>
            </div>
          </div>

          <div className="panel" data-testid="clusters-nodes">
            <h3 style={{ marginTop: 0 }}>{t('clusters.nodes')}</h3>
            {cluster?.nodes && cluster.nodes.length > 0 ? (
              <table className="table">
                <thead>
                  <tr>
                    <th>{t('clusters.nodeName')}</th>
                    <th>{t('clusters.nodeStatus')}</th>
                  </tr>
                </thead>
                <tbody>
                  {cluster.nodes.map((n) => (
                    <tr key={n.nodeId} data-testid={`clusters-node-${n.nodeId}`}>
                      <td className="mono">{n.name}</td>
                      <td>
                        <StateBadge state={n.status} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <p className="muted">{t('clusters.noNodes')}</p>
            )}
          </div>

          <div className="panel" data-testid="clusters-workloads">
            <h3 style={{ marginTop: 0 }}>{t('clusters.workloads')}</h3>
            {workloads.length > 0 ? (
              <table className="table">
                <thead>
                  <tr>
                    <th>{t('clusters.service')}</th>
                    <th>{t('clusters.model')}</th>
                    <th>{t('clusters.state')}</th>
                    <th>{t('clusters.created')}</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {workloads.map((w) => (
                    <tr key={w.serviceId} data-testid={`clusters-workload-${w.serviceId}`}>
                      <td>
                        <a
                          href={`/admin/inference-services/${w.serviceId}`}
                          onClick={(e) => {
                            e.preventDefault();
                            navigate(`/admin/inference-services/${w.serviceId}`);
                          }}
                        >
                          {w.name}
                        </a>
                      </td>
                      <td className="mono">{w.modelId.slice(0, 8)}</td>
                      <td>
                        <StateBadge state={w.state} />
                      </td>
                      <td>{new Date(parseInt(w.createdAt, 10) * 1000).toLocaleString()}</td>
                      <td>
                        <button
                          className="secondary"
                          data-testid={`clusters-workload-view-${w.serviceId}`}
                          onClick={() => navigate(`/admin/inference-services/${w.serviceId}`)}
                        >
                          {t('clusters.view')}
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <p className="muted">{t('clusters.noWorkloads')}</p>
            )}
          </div>
        </>
      )}
    </div>
  );
}