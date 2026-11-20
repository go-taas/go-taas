// Storage backends page: the model-weights locations the platform can serve
// from and how inference pods read each one.

import { useCallback, useEffect, useState } from 'react';
import { api, type StorageBackend } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { ErrorBanner } from '../components';

interface ListResponse {
  response: { code: number; message: string };
  backends: StorageBackend[];
  defaultBackend: string;
}

export default function StorageBackendsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [backends, setBackends] = useState<StorageBackend[]>([]);
  const [defaultBackend, setDefaultBackend] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<ListResponse>('/api/v1/admin/storage-backends', orgId);
      setBackends(data.backends || []);
      setDefaultBackend(data.defaultBackend || '');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('storageBackends.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('storageBackends.title')}</h1>
          <div className="subtitle">{t('storageBackends.subtitle')}</div>
        </div>
        <div className="toolbar">
          <button className="secondary" onClick={() => void load()}>
            {t('common.refresh')}
          </button>
        </div>
      </div>
      <div className="guide-grid" data-testid="storage-delivery-guide">
        <article className="guide-card">
          <div className="guide-body">
            <strong>{t('storageBackends.guideMountTitle')}</strong>
            <p>{t('storageBackends.guideMountBody')}</p>
          </div>
        </article>
        <article className="guide-card">
          <div className="guide-body">
            <strong>{t('storageBackends.guideDownloadTitle')}</strong>
            <p>{t('storageBackends.guideDownloadBody')}</p>
          </div>
        </article>
        <article className="guide-card">
          <div className="guide-body">
            <strong>{t('storageBackends.guideCacheTitle')}</strong>
            <p>{t('storageBackends.guideCacheBody')}</p>
          </div>
        </article>
      </div>
      <div className="guide-note" data-testid="storage-node-cache-note">
        <strong>{t('storageBackends.nodeCacheTitle')}</strong>
        <span>{t('storageBackends.nodeCacheBody')}</span>
      </div>
      {error && <ErrorBanner message={error} />}
      {defaultBackend && (
        <div className="muted" style={{ marginBottom: 12 }}>
          {t('storageBackends.default')}: <strong>{defaultBackend}</strong>
        </div>
      )}
      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : backends.length === 0 ? (
          <div className="empty-state" data-testid="storage-backends-empty">
            {t('storageBackends.empty')}
          </div>
        ) : (
          <table className="data" data-testid="storage-backends-table">
            <thead>
              <tr>
                <th>{t('common.name')}</th>
                <th>{t('storageBackends.colType')}</th>
                <th>{t('storageBackends.colDelivery')}</th>
                <th>{t('storageBackends.colLocation')}</th>
                <th>{t('storageBackends.colWarmup')}</th>
              </tr>
            </thead>
            <tbody>
              {backends.map((backend) => (
                <tr key={backend.name} data-testid={`storage-backend-${backend.name}`}>
                  <td>
                    <strong>{backend.name}</strong>
                    {backend.isDefault && (
                      <span className="badge" style={{ marginLeft: 8 }}>
                        {t('storageBackends.default')}
                      </span>
                    )}
                  </td>
                  <td>{t(`storageBackends.type.${backend.type}`)}</td>
                  <td className="mono muted">
                    {(backend.allowedDeliveries || []).join(', ') || backend.defaultDelivery}
                  </td>
                  <td className="mono muted">
                    {backend.type === 'objectstore'
                      ? [backend.provider, backend.endpoint, backend.bucket].filter(Boolean).join(' · ')
                      : backend.mountPath || '—'}
                  </td>
                  <td>{backend.warmupEnabled ? t('common.yes') : t('common.no')}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
