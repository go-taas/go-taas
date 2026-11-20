// Model cache page: node-local warmup state and warm/cancel actions.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { api, ApiError, formatTime, type ModelCacheEntry, type ModelSummary, type PageMeta, type WarmupNode, type WarmupNodeGroup } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination, RowActionsMenu, StatusError, Toast } from '../components';
import { formatBytes } from '../components/ModelDownloadsPanel';

interface ListResponse {
  response: { code: number; message: string };
  caches: ModelCacheEntry[];
  pageMeta?: PageMeta;
}
interface ModelsResponse {
  models: ModelSummary[];
}
interface NodesResponse {
  nodes: WarmupNode[];
}
interface GroupsResponse {
  nodeGroups: WarmupNodeGroup[];
}

const PAGE_SIZE = 20;
const ACTIVE_STATES = ['queued', 'warming'];

export default function ModelCachesPage({ embedded = false }: { embedded?: boolean }) {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [tab, setTab] = useState<'models' | 'nodes'>('models');
  const [caches, setCaches] = useState<ModelCacheEntry[]>([]);
  const [nodes, setNodes] = useState<WarmupNode[]>([]);
  const [nodesLoading, setNodesLoading] = useState(false);
  const [nodeGroups, setNodeGroups] = useState<WarmupNodeGroup[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [warmOpen, setWarmOpen] = useState(false);
  const [models, setModels] = useState<ModelSummary[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true);
      try {
        const data = await api.get<ListResponse>(
          `/api/v1/admin/model-warmups?page.offset=${offset}&page.limit=${PAGE_SIZE}`,
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

  const loadNodes = useCallback(async () => {
    setNodesLoading(true);
    try {
      const [nodeData, groupData] = await Promise.all([
        api.get<NodesResponse>('/api/v1/admin/model-warmup-nodes', orgId),
        api.get<GroupsResponse>('/api/v1/admin/model-warmup-node-groups', orgId),
      ]);
      setNodes(nodeData.nodes || []);
      setNodeGroups(groupData.nodeGroups || []);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('caches.loadFailed'));
    } finally {
      setNodesLoading(false);
    }
  }, [orgId, t]);

  useEffect(() => {
    if (tab === 'nodes') void loadNodes();
  }, [tab, loadNodes]);

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
    setModelsLoading(true);
    void Promise.all([
      api.get<ModelsResponse>('/api/v1/admin/models?page.offset=0&page.limit=200', orgId),
      api.get<GroupsResponse>('/api/v1/admin/model-warmup-node-groups', orgId),
    ])
      .then(([modelData, groupData]) => {
        setModels(modelData.models || []);
        setNodeGroups(groupData.nodeGroups || []);
      })
      .catch((e) => setError(e instanceof Error ? e.message : t('caches.loadFailed')))
      .finally(() => setModelsLoading(false));
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
      {embedded ? (
        <div className="toolbar" style={{ marginBottom: 14, justifyContent: 'flex-end' }}>
          <button className="secondary" onClick={() => void (tab === 'nodes' ? loadNodes() : load())}>
            {t('common.refresh')}
          </button>
          <button data-testid="warm-model" onClick={() => setWarmOpen(true)}>
            {t('caches.warm')}
          </button>
        </div>
      ) : (
        <div className="page-header">
          <div>
            <h1>{t('caches.title')}</h1>
            <div className="subtitle">{t('caches.subtitle')}</div>
          </div>
          <div className="toolbar">
            <button className="secondary" onClick={() => void (tab === 'nodes' ? loadNodes() : load())}>
              {t('common.refresh')}
            </button>
            <button data-testid="warm-model" onClick={() => setWarmOpen(true)}>
              {t('caches.warm')}
            </button>
          </div>
        </div>
      )}
      <div className="tab-strip" role="tablist" style={{ marginTop: 0 }}>
        <button
          className={`tab ${tab === 'models' ? 'active' : ''}`}
          role="tab"
          aria-selected={tab === 'models'}
          data-testid="warmup-tab-models"
          onClick={() => setTab('models')}
        >
          {t('caches.tabModels')}
        </button>
        <button
          className={`tab ${tab === 'nodes' ? 'active' : ''}`}
          role="tab"
          aria-selected={tab === 'nodes'}
          data-testid="warmup-tab-nodes"
          onClick={() => setTab('nodes')}
        >
          {t('caches.tabNodes')}
        </button>
      </div>
      {error && <ErrorBanner message={error} />}
      <Toast message={notice} onDismiss={() => setNotice('')} testId="caches-toast" />
      {tab === 'nodes' ? (
        <NodesPanel nodes={nodes} loading={nodesLoading} />
      ) : (
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
                    <StatusError message={cache.message} testId={`cache-error-${cache.cacheId}`} />
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
      )}
      {warmOpen && (
        <WarmModelDialog
          orgId={orgId}
          models={models}
          modelsLoading={modelsLoading}
          nodeGroups={nodeGroups}
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

function NodesPanel({ nodes, loading }: { nodes: WarmupNode[]; loading: boolean }) {
  const { t } = useI18n();
  if (loading) return <div className="panel"><div className="loading">{t('common.loading')}</div></div>;
  if (nodes.length === 0) {
    return <div className="panel"><div className="empty-state" data-testid="warmup-nodes-empty">{t('caches.nodesEmpty')}</div></div>;
  }
  return (
    <div className="panel">
      <div className="panel-header panel-header-stacked">
        <h2>{t('caches.nodesTitle')}</h2>
        <p className="panel-hint">{t('caches.nodesHint')}</p>
      </div>
      <table className="data" data-testid="warmup-nodes-table">
        <thead>
          <tr>
            <th>{t('caches.colNode')}</th>
            <th>{t('caches.colNodeGroups')}</th>
            <th>{t('caches.colCopies')}</th>
            <th>{t('caches.colNodeModels')}</th>
            <th>{t('common.updated')}</th>
          </tr>
        </thead>
        <tbody>
          {nodes.map((node) => (
            <tr key={node.name} data-testid={`warmup-node-${node.name}`}>
              <td><strong className="mono">{node.name}</strong></td>
              <td className="mono muted">{(node.nodeGroups || []).join(', ') || '—'}</td>
              <td>{node.downloadedCount || 0} / {node.modelCount || 0}</td>
              <td>
                {(node.models || []).length === 0 ? <span className="muted">—</span> : (
                  <div className="node-model-list">
                    {(node.models || []).map((model) => (
                      <div key={model.cacheName || model.modelName} className="node-model-row">
                        <span className="mono">{model.modelName || model.cacheName}</span>
                        <span className={`state state-${(model.state || '').replace('Model', '').toLowerCase()}`}>
                          {model.state || '—'}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </td>
              <td>{formatTime(node.updatedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function WarmModelDialog({
  orgId,
  models,
  modelsLoading,
  nodeGroups,
  onClose,
  onDone,
}: {
  orgId: string;
  models: ModelSummary[];
  modelsLoading: boolean;
  nodeGroups: WarmupNodeGroup[];
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  // The dialog mounts before the model list arrives, so the selected id must
  // follow the first model once it loads instead of staying empty forever.
  const fallbackModelId = models[0]?.modelId || '';
  const [modelId, setModelId] = useState(fallbackModelId);
  const [selectedGroups, setSelectedGroups] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    if (!modelId && fallbackModelId) setModelId(fallbackModelId);
  }, [fallbackModelId, modelId]);
  const selectedModelId = modelId || fallbackModelId;
  const disabledReason = modelsLoading
    ? t('caches.modelsLoading')
    : models.length === 0
      ? t('caches.noModels')
      : '';

  const submit = async () => {
    if (!selectedModelId) return;
    setBusy(true);
    setError('');
    try {
      await api.post('/api/v1/admin/model-warmups', orgId, {
        modelId: selectedModelId,
        nodeGroups: selectedGroups,
      });
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
        <select id="warm-model-id" value={selectedModelId} onChange={(e) => setModelId(e.target.value)}>
          {models.map((model) => (
            <option key={model.modelId} value={model.modelId}>{model.name}</option>
          ))}
        </select>
        {disabledReason && (
          <div className="muted" data-testid="warm-disabled-reason">{disabledReason}</div>
        )}
      </div>
      <div className="form-field full">
        <label>{t('caches.nodeGroups')}</label>
        {nodeGroups.length === 0 ? (
          <div className="muted">{t('caches.noNodeGroups')}</div>
        ) : (
          <div className="checkbox-list">
            {nodeGroups.map((group) => (
              <label key={group.name} className="checkbox-row">
                <input
                  type="checkbox"
                  checked={selectedGroups.includes(group.name)}
                  onChange={(e) => setSelectedGroups((current) =>
                    e.target.checked ? [...current, group.name] : current.filter((name) => name !== group.name),
                  )}
                />
                <span>{group.name}</span>
              </label>
            ))}
          </div>
        )}
        <div className="muted">{t('caches.nodeGroupsHint')}</div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button disabled={busy || !selectedModelId} onClick={() => void submit()}>
          {busy ? t('caches.warming') : t('caches.warm')}
        </button>
      </div>
    </Dialog>
  );
}
