// Model detail page: metadata and deploy entry point.
// Implements docs/design/model-catalog-deployment.md FR2.3, AC2, and
// docs/design/model-authorization.md FR1, FR2, FR5.1 (AC12, AC13): the
// "Authorized organizations" panel and the "Restricted" badge.

import { useCallback, useEffect, useState } from 'react';
import { api, formatTime, type ModelSummary } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { usePolling } from '../components';
import { BackLink, ErrorBanner } from '../components';


interface ModelResponse {
  response: { code: number; message: string };
  model: ModelSummary & { description?: string };
  versions: string[];
}

export default function ModelDetailPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const id = window.location.pathname.split('/').pop() || '';
  const [model, setModel] = useState<ModelResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setError('');
    try {
      const data = await api.get<ModelResponse>(`/api/v1/admin/models/${id}`, orgId);
      setModel(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('modeldetail.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [id, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // The version list can grow (registering another version elsewhere);
  // a light periodic refresh keeps the detail page honest.
  usePolling(() => void load(), 10000, !loading && !error);

  if (loading) return <div className="loading">{t('common.loading')}</div>;
  if (error)
    return (
      <div>
        <BackLink to="/admin/models" label={t('modeldetail.back')} />
        <ErrorBanner message={error} />
      </div>
    );
  if (!model) return null;

  return (
    <div>
      <BackLink to="/admin/models" label={t('modeldetail.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="model-detail-name">
            {model.model.name}
            {model.model.external && (
              <span className="badge external" data-testid="model-detail-external-marker">
                {t('modeldetail.external')}
              </span>
            )}
          </h1>
          <div className="subtitle mono">
            {model.model.modelId}
            {model.model.external && (
              <span data-testid="model-detail-provider-name">
                {' '}
                · {t('modeldetail.providedBy', { name: model.model.providerName || '' })}
              </span>
            )}
          </div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('modeldetail.metadata')}</h3>
        <div className="detail-grid">
          <div className="detail-item">
            <div className="label">{t('modeldetail.hubModelId')}</div>
            <div className="value mono" data-testid="model-hub-model-id">
              {model.model.hubModelId || '—'}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">{t('modeldetail.weightPath')}</div>
            <div className="value mono">{model.model.weightPath}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('modeldetail.storageBackend')}</div>
            <div className="value mono" data-testid="model-detail-storage-backend">
              {model.model.storageBackend || '—'}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">{t('modeldetail.created')}</div>
            <div className="value">{formatTime(model.model.createdAt)}</div>
          </div>
          {model.model.description && (
            <div className="detail-item">
              <div className="label">{t('modeldetail.description')}</div>
              <div className="value">{model.model.description}</div>
            </div>
          )}
        </div>
      </div>

    </div>
  );
}
