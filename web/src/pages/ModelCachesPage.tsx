// Model cache page: node-local warmup state and warm/cancel actions.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { api, ApiError, formatTime, type ModelCacheEntry, type ModelSummary, type PageMeta } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination, RowActionsMenu, Toast } from '../components';
import { formatBytes } from '../components/ModelDownloadsPanel';

interface ListResponse {
  response: { code: number; message: string };
  caches: ModelCacheEntry[];
  pageMeta?: PageMeta;
}
interface ModelsResponse {
  models: ModelSummary[];
}

const PAGE_SIZE = 20;
const ACTIVE_STATES = ['queued', 'warming'];

export default function ModelCachesPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [caches, setCaches] = useState<ModelCacheEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [warmOpen, setWarmOpen] = useState(false);
  const [models, setModels] = useState<ModelSummary[]>([]);

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true);
      try {
        const data = await api.get<ListResponse>(
          `/api/v1/admin/model-cache-entries?page.offset=${offset}&page.limit=${PAGE_SIZE}`,
          orgId,
        );
        setCaches(data.caches || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
        setError('');
      } catch (e) {
        setError(e instanceof Error ? e.message : t('caches.loadFailed'));
      } finally {
        if (!quiet) setLoading(false);
      }
    },
    [orgId, offset, t],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const hasActive = useMemo(
    () => caches.some((cache) => ACTIVE_STATES.includes(cache.state)),
    [caches],
  );
  useEffect(() => {
    if (!hasActive) return undefined;
    const timer = window.setInterval(() => void load(true), 3000);
    return () => window.clearInterval(timer);
  }, [hasActive, load]);

  useEffect(() => {
    if (!warmOpen) return;
    api
      .get<ModelsResponse>('/api/v1/admin/models?page.offset=0&page.limit=200', orgId)
      .then((data) => setModels(data.models || []))
      .catch((e) => setError(e instanceof Error ? e.message : t('caches.loadFailed')));
  }, [warmOpen, orgId, t]);

  const cancel = async (cache: ModelCacheEntry) => {
    try {
      await api.post(`/api/v1/admin/model-warmups/${cache.cacheId}:cancel`, orgId, {});
      setNotice(t('caches.cancelled'));
      await load(true);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('caches.cancelFailed'));
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('caches.title')}</h1>
          <div className="subtitle">{t('caches.subtitle')}</div>
        </div>
        <div className="toolbar">
          <button className="secondary" onClick={() => void load()}>
            {t('common.refresh')}
          </button>
          <button data-testid="warm-model" onClick={() => setWarmOpen(true)}>
            {t('caches.warm')}
          </button>
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <Toast message={notice} onDismiss={() => setNotice('')} testId="caches-toast" />
      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : caches.length === 0 ? (
          <div className="empty-state" data-testid="caches-empty">{t('caches.empty')}</div>
        ) : (
          <table className="data" data-testid="caches-table">
            <thead>
              <tr>
                <th>{t('caches.colModel')}</th>
                <th>{t('caches.colBackend')}</th>
                <th>{t('caches.colNodes')}</th>
                <th>{t('caches.colSize')}</th>
                <th>{t('common.status')}</th>
                <th>{t('common.updated')}</th>
                <th className="col-actions">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {caches.map((cache) => (
                <tr key={cache.cacheId} data-testid={`cache-row-${cache.cacheId}`}>
                  <td>
                    <strong>{cache.modelName || '—'}</strong>
                    <div className="muted mono">{cache.version}</div>
                  </td>
                  <td className="mono muted">{cache.backend || '—'}</td>
                  <td>
                    {(cache.warmedNodes || []).length} / {cache.totalNodes || 0}
                    <div className="muted mono">{(cache.nodeGroups || []).join(', ')}</div>
                  </td>
                  <td className="mono">{formatBytes(cache.sizeBytes || 0)}</td>
                  <td>
                    <span className={`state state-${cache.state}`}>{t(`caches.state.${cache.state}`)}</span>
                    {cache.message && <div className="muted">{cache.message}</div>}
                  </td>
                  <td>{formatTime(cache.updatedAt)}</td>
                  <td>
                    {!['cancelled'].includes(cache.state) && (
                      <RowActionsMenu
                        label={t('common.actionsFor', { name: cache.cacheId })}
                        testId={`cache-actions-${cache.cacheId}`}
                        actions={[{
                          key: 'cancel',
                          label: t('common.cancel'),
                          danger: true,
                          onClick: () => void cancel(cache),
                        }]}
                      />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {total > PAGE_SIZE && (
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        )}
      </div>
      {warmOpen && (
        <WarmModelDialog
          orgId={orgId}
          models={models}
          onClose={() => setWarmOpen(false)}
          onDone={() => {
            setWarmOpen(false);
            setNotice(t('caches.started'));
            void load();
          }}
        />
      )}
    </div>
  );
}

function WarmModelDialog({
  orgId,
  models,
  onClose,
  onDone,
}: {
  orgId: string;
  models: ModelSummary[];
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [modelId, setModelId] = useState(models[0]?.modelId || '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    setBusy(true);
    setError('');
    try {
      await api.post('/api/v1/admin/model-warmups', orgId, { modelId });
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('caches.warmFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog title={t('caches.warm')} onClose={onClose} testId="warm-model-dialog">
      <div className="form-field full">
        <label htmlFor="warm-model-id">{t('caches.colModel')}</label>
        <select id="warm-model-id" value={modelId} onChange={(e) => setModelId(e.target.value)}>
          {models.map((model) => (
            <option key={model.modelId} value={model.modelId}>{model.name}</option>
          ))}
        </select>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button disabled={busy || !modelId} onClick={() => void submit()}>
          {busy ? t('caches.warming') : t('caches.warm')}
        </button>
      </div>
    </Dialog>
  );
}
