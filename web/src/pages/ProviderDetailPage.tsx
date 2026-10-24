// Admin Provider Detail page (feature #48): one provider's configuration,
// the five-state connectivity panel with the on-demand test, and the
// upstream-model table with add/edit/remove and publish/unpublish.
// Admin-surface only — it calls ONLY /api/v1/admin/providers/*. Implements
// docs/design/external-model-providers.md §5.4.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useI18n } from '../i18n';
import { useOrg } from '../org';
import { formatTime } from '../api';
import { Dialog, ErrorBanner, Pagination, usePolling } from '../components';
import { useParams } from '../router';
import {
  isNonHTTPS,
  probeBadgeClass,
  probeLabelKey,
  type ProbeResult,
  type Provider,
} from './ProvidersPage';

interface ResponseEnvelope {
  code: number;
  message: string;
}

interface ProviderModel {
  modelId: string;
  upstreamModelId: string;
  displayName: string;
  contextWindow: string;
  enabled: boolean;
  published: boolean;
  catalogModelId: string;
  catalogModelName: string;
  createdAt: string;
  updatedAt: string;
}

interface GetProviderResponse {
  response: ResponseEnvelope;
  provider?: Provider;
}

interface ModelListResponse {
  response: ResponseEnvelope;
  models?: ProviderModel[];
  pageMeta?: { total: string };
}

const PAGE_SIZE = 20;
const POLL_MS = 30_000;

const emptyModelForm = { upstreamModelId: '', displayName: '', contextWindow: '0', enabled: true };

export default function ProviderDetailPage() {
  const { t } = useI18n();
  const api = useApi();
  const { orgId } = useOrg();
  const params = useParams();
  const providerId = params.providerId || '';

  const [provider, setProvider] = useState<Provider | null>(null);
  const [models, setModels] = useState<ProviderModel[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [search, setSearch] = useState('');
  const [enabledFilter, setEnabledFilter] = useState('');
  const [publishedFilter, setPublishedFilter] = useState('');

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notFound, setNotFound] = useState(false);
  const [stale, setStale] = useState(false);
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [busy, setBusy] = useState(false);
  const [toast, setToast] = useState('');
  const [actionError, setActionError] = useState('');

  // Add/edit model dialog.
  const [modelDialogOpen, setModelDialogOpen] = useState(false);
  const [editingModel, setEditingModel] = useState<ProviderModel | null>(null);
  const [modelForm, setModelForm] = useState({ ...emptyModelForm });
  const [modelFormError, setModelFormError] = useState('');

  // Publish dialog.
  const [publishTarget, setPublishTarget] = useState<ProviderModel | null>(null);
  const [publishName, setPublishName] = useState('');
  const [publishError, setPublishError] = useState('');

  // Unpublish confirmation.
  const [unpublishTarget, setUnpublishTarget] = useState<ProviderModel | null>(null);

  const load = useCallback(
    async (showLoading: boolean) => {
      if (!providerId) return;
      if (showLoading) setLoading(true);
      setError('');
      try {
        const data = await api.get<GetProviderResponse>(
          `/api/v1/admin/providers/${encodeURIComponent(providerId)}`,
          orgId,
        );
        setProvider(data.provider || null);
        setNotFound(false);
      } catch (e) {
        const code = (e as { code?: number } | undefined)?.code;
        if (code === 13001) {
          setNotFound(true);
          setLoading(false);
          return;
        }
        if (code === 10036) {
          setPermissionDenied(true);
          setLoading(false);
          return;
        }
        setError(e instanceof Error ? e.message : 'Could not load the provider.');
        if (provider) setStale(true);
      } finally {
        setLoading(false);
      }
    },
    [api, orgId, providerId, provider],
  );

  const loadModels = useCallback(async () => {
    if (!providerId) return;
    try {
      const query = new URLSearchParams({
        'page.offset': String(offset),
        'page.limit': String(PAGE_SIZE),
      });
      if (search) query.set('upstreamModelId', search);
      if (enabledFilter) query.set('enabled', enabledFilter);
      if (publishedFilter) query.set('published', publishedFilter);
      const data = await api.get<ModelListResponse>(
        `/api/v1/admin/providers/${encodeURIComponent(providerId)}/models?${query.toString()}`,
        orgId,
      );
      setModels(data.models || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setActionError(e instanceof Error ? e.message : 'Could not load the upstream models.');
    }
  }, [api, orgId, providerId, offset, search, enabledFilter, publishedFilter]);

  useEffect(() => {
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [providerId]);

  useEffect(() => {
    void loadModels();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [providerId, offset, search, enabledFilter, publishedFilter]);

  usePolling(() => void load(false), POLL_MS, !loading);

  const runTest = async () => {
    if (!provider) return;
    setBusy(true);
    setActionError('');
    try {
      const data = await api.post<{ probe?: ProbeResult }>(
        `/api/v1/admin/providers/${encodeURIComponent(provider.providerId)}:test`,
        orgId,
        {},
      );
      setProvider({ ...provider, lastProbe: data.probe });
      setToast('Connectivity test complete.');
    } catch (e) {
      setActionError(e instanceof Error ? e.message : 'The connectivity test failed.');
    } finally {
      setBusy(false);
    }
  };

  const openAddModel = () => {
    setEditingModel(null);
    setModelForm({ ...emptyModelForm });
    setModelFormError('');
    setModelDialogOpen(true);
  };

  const openEditModel = (m: ProviderModel) => {
    setEditingModel(m);
    setModelForm({
      upstreamModelId: m.upstreamModelId,
      displayName: m.displayName,
      contextWindow: m.contextWindow || '0',
      enabled: m.enabled,
    });
    setModelFormError('');
    setModelDialogOpen(true);
  };

  const submitModel = async () => {
    const upstream = modelForm.upstreamModelId.trim();
    if (!editingModel && (upstream === '' || upstream.length > 128)) {
      setModelFormError('Enter the upstream model id.');
      return;
    }
    const window = Number(modelForm.contextWindow);
    if (!Number.isInteger(window) || window < 0 || window > 10_000_000) {
      setModelFormError('Enter a context window in tokens (0 for unknown).');
      return;
    }
    try {
      if (editingModel) {
        await api.patch(
          `/api/v1/admin/providers/${encodeURIComponent(providerId)}/models/${encodeURIComponent(editingModel.modelId)}`,
          orgId,
          { displayName: modelForm.displayName.trim(), contextWindow: window, enabled: modelForm.enabled },
        );
        setToast('Model updated.');
      } else {
        await api.post(`/api/v1/admin/providers/${encodeURIComponent(providerId)}/models`, orgId, {
          upstreamModelId: upstream,
          displayName: modelForm.displayName.trim(),
          contextWindow: window,
          enabled: modelForm.enabled,
        });
        setToast('Model added.');
      }
      setModelDialogOpen(false);
      setActionError('');
      await loadModels();
    } catch (e) {
      const code = (e as { code?: number } | undefined)?.code;
      if (code === 13009) setModelFormError('This upstream model is already curated.');
      else if (code === 13010) setModelFormError('Enter a valid upstream model id.');
      else setModelFormError(e instanceof Error ? e.message : 'Could not save the model.');
    }
  };

  const removeModel = async (m: ProviderModel) => {
    try {
      await api.del(
        `/api/v1/admin/providers/${encodeURIComponent(providerId)}/models/${encodeURIComponent(m.modelId)}`,
        orgId,
      );
      setToast('Model removed.');
      setActionError('');
      await loadModels();
    } catch (e) {
      setActionError(e instanceof Error ? e.message : 'Could not remove the model.');
    }
  };

  const openPublish = (m: ProviderModel) => {
    setPublishTarget(m);
    setPublishName(m.displayName || m.upstreamModelId);
    setPublishError('');
  };

  const submitPublish = async () => {
    if (!publishTarget) return;
    try {
      await api.post(
        `/api/v1/admin/providers/${encodeURIComponent(providerId)}/models/${encodeURIComponent(publishTarget.modelId)}:publish`,
        orgId,
        { tenantFacingName: publishName.trim() },
      );
      setToast('Model published.');
      setActionError('');
      setPublishTarget(null);
      await loadModels();
      await load(false);
    } catch (e) {
      const code = (e as { code?: number } | undefined)?.code;
      if (code === 13014) setPublishError(`A model with this name already exists. Try "${publishName.trim()}-provider".`);
      else if (code === 13010) setPublishError('Enter a valid tenant-facing name (lowercase letters, digits, . _ -).');
      else setPublishError(e instanceof Error ? e.message : 'Could not publish the model.');
    }
  };

  const submitUnpublish = async () => {
    if (!unpublishTarget) return;
    try {
      await api.post(
        `/api/v1/admin/providers/${encodeURIComponent(providerId)}/models/${encodeURIComponent(unpublishTarget.modelId)}:unpublish`,
        orgId,
        {},
      );
      setToast('Model unpublished.');
      setActionError('');
      setUnpublishTarget(null);
      await loadModels();
      await load(false);
    } catch (e) {
      setActionError(e instanceof Error ? e.message : 'Could not unpublish the model.');
      setUnpublishTarget(null);
    }
  };

  if (permissionDenied) {
    return (
      <div data-testid="provider-permission-denied">
        <ErrorBanner message="You do not have permission to manage providers." />
        <a href="/admin" className="link" data-testid="provider-back-admin">
          Back to admin
        </a>
      </div>
    );
  }
  if (notFound) {
    return <ErrorBanner message="Provider not found." />;
  }

  const status = provider?.lastProbe?.status || 'unknown';

  return (
    <div data-testid="provider-detail">
      <div className="page-header">
        <div>
          <h1 data-testid="provider-detail-name">{provider?.name || '…'}</h1>
          <div className="subtitle">
            {provider && <span className="badge steady">{provider.type}</span>}{' '}
            {provider && (
              <span className={provider.enabled ? 'badge running' : 'badge terminated'}>
                {provider.enabled ? t('common.enabled') : t('common.disabled')}
              </span>
            )}{' '}
            {provider && isNonHTTPS(provider.baseUrl) && (
              <span className="badge failed" data-testid="provider-detail-nonhttps-chip">
                {t('providers.nonHttps')}
              </span>
            )}
          </div>
        </div>
        <div className="toolbar">
          <button className="primary" data-testid="provider-test-now" disabled={busy} onClick={() => void runTest()}>
            {t('providers.testConnection')}
          </button>
          <a className="secondary" href="/admin/providers" data-testid="provider-detail-back">
            {t('providers.backToProviders')}
          </a>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}
      {actionError && (
        <div data-testid="provider-action-error">
          <ErrorBanner message={actionError} />
        </div>
      )}
      {toast && (
        <div className="success-banner" data-testid="provider-toast">
          {toast}
        </div>
      )}
      {stale && <div className="warning-banner" data-testid="provider-stale-banner">{t('providers.stale')}</div>}

      {loading ? (
        <div className="skeleton" data-testid="provider-detail-loading">
          Loading…
        </div>
      ) : (
        provider && (
          <>
            <div className="panel" data-testid="provider-detail-overview">
              <div className="detail-item">
                <div className="label">{t('providers.colBaseUrl')}</div>
                <div className="value mono" data-testid="provider-detail-base-url">
                  {provider.baseUrl}
                </div>
              </div>
              <div className="detail-item">
                <div className="label">{t('providers.fieldTimeoutShort')}</div>
                <div className="value">{provider.requestTimeoutSeconds} s</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('providers.fieldApiKey')}</div>
                <div className="value mono" data-testid="provider-detail-key-hint">
                  {provider.apiKeyHint}
                </div>
              </div>
              <div className="detail-item">
                <div className="label">{t('providers.fieldHeaders')}</div>
                <div className="value" data-testid="provider-detail-headers">
                  {(provider.extraHeaderNames || []).join(', ') || '—'} (values hidden)
                </div>
              </div>
              <div className="detail-item">
                <div className="label">{t('common.created')}</div>
                <div className="value">{formatTime(provider.createdAt)}</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('common.updated')}</div>
                <div className="value">{formatTime(provider.updatedAt)}</div>
              </div>
            </div>

            <div className="panel" data-testid="provider-test-panel">
              <div className="detail-item">
                <div className="label">{t('providers.colStatus')}</div>
                <div className="value">
                  <span className={probeBadgeClass(status)} data-testid="provider-test-status">
                    {t(probeLabelKey(status))}
                  </span>
                </div>
              </div>
              <div className="detail-item">
                <div className="label">{t('providers.latency')}</div>
                <div className="value">{provider.lastProbe?.latencyMs ?? '—'} ms</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('providers.httpStatus')}</div>
                <div className="value">{provider.lastProbe?.httpStatus ? provider.lastProbe.httpStatus : '—'}</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('providers.checkedAt')}</div>
                <div className="value">
                  {provider.lastProbe?.checkedAt ? formatTime(provider.lastProbe.checkedAt) : '—'}
                </div>
              </div>
              <div className="detail-item">
                <div className="label">{t('providers.message')}</div>
                <div className="value" data-testid="provider-test-message">
                  {provider.lastProbe?.message || '—'}
                </div>
              </div>
              <div className="subtitle">{t('providers.autoChecked')}</div>
              {status === 'unauthorized' && (
                <div className="warning-banner" data-testid="provider-test-unauthorized-notice">
                  The endpoint is reachable but the credentials were rejected. Fix or rotate the API key, then test
                  again.
                </div>
              )}
              {status === 'unreachable' && (
                <div className="warning-banner" data-testid="provider-test-unreachable-notice">
                  The endpoint could not be reached (network or DNS failure).
                </div>
              )}
            </div>

            <div className="panel" data-testid="provider-model-table">
              <div className="page-header">
                <div className="subtitle">{t('providers.upstreamModels')}</div>
                <button className="primary" data-testid="provider-model-add" onClick={openAddModel}>
                  Add model
                </button>
              </div>
              <div className="toolbar">
                <input
                  aria-label="search upstream id"
                  value={search}
                  placeholder={t('providers.searchUpstream')}
                  onChange={(e) => {
                    setOffset(0);
                    setSearch(e.target.value);
                  }}
                />
                <select
                  aria-label="enabled filter"
                  value={enabledFilter}
                  onChange={(e) => {
                    setOffset(0);
                    setEnabledFilter(e.target.value);
                  }}
                >
                  <option value="">{t('common.all')}</option>
                  <option value="true">{t('common.enabled')}</option>
                  <option value="false">{t('common.disabled')}</option>
                </select>
                <select
                  aria-label="published filter"
                  value={publishedFilter}
                  onChange={(e) => {
                    setOffset(0);
                    setPublishedFilter(e.target.value);
                  }}
                >
                  <option value="">{t('common.all')}</option>
                  <option value="true">{t('providers.published')}</option>
                  <option value="false">{t('providers.unpublished')}</option>
                </select>
              </div>
              {models.length === 0 ? (
                <div className="empty" data-testid="provider-model-empty">
                  No upstream models curated yet. Add the model ids this provider exposes.
                </div>
              ) : (
                <table className="table">
                  <thead>
                    <tr>
                      <th>{t('providers.colUpstreamId')}</th>
                      <th>{t('providers.colDisplayName')}</th>
                      <th>{t('providers.colContextWindow')}</th>
                      <th>{t('providers.colEnabled')}</th>
                      <th>{t('providers.colPublished')}</th>
                      <th>{t('common.updated')}</th>
                      <th>{t('common.actions')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {models.map((m) => (
                      <tr key={m.modelId} data-testid={`provider-model-row-${m.modelId}`}>
                        <td className="mono">{m.upstreamModelId}</td>
                        <td>{m.displayName || '—'}</td>
                        <td>{m.contextWindow && m.contextWindow !== '0' ? m.contextWindow : '—'}</td>
                        <td>
                          <span className={m.enabled ? 'badge running' : 'badge terminated'}>
                            {m.enabled ? t('common.enabled') : t('common.disabled')}
                          </span>
                        </td>
                        <td>
                          {m.published ? (
                            <a
                              className="link"
                              href={`/admin/models/${m.catalogModelId}`}
                              data-testid={`provider-model-catalog-${m.modelId}`}
                            >
                              {m.catalogModelName || m.catalogModelId}
                            </a>
                          ) : (
                            <span className="badge terminated">{t('providers.unpublished')}</span>
                          )}
                        </td>
                        <td>{formatTime(m.updatedAt)}</td>
                        <td className="actions">
                          <button className="link" onClick={() => openEditModel(m)} data-testid={`provider-model-edit-${m.modelId}`}>
                            {t('common.edit')}
                          </button>
                          {m.published ? (
                            <button
                              className="link"
                              data-testid={`provider-model-unpublish-${m.modelId}`}
                              onClick={() => setUnpublishTarget(m)}
                            >
                              {t('providers.unpublish')}
                            </button>
                          ) : (
                            <button
                              className="link"
                              data-testid={`provider-model-publish-${m.modelId}`}
                              disabled={!m.enabled}
                              title={!m.enabled ? t('providers.enableBeforePublish') : undefined}
                              onClick={() => openPublish(m)}
                            >
                              {t('providers.publish')}
                            </button>
                          )}
                          <button
                            className="link danger"
                            data-testid={`provider-model-remove-${m.modelId}`}
                            disabled={m.published}
                            title={m.published ? t('providers.unpublishBeforeRemove') : undefined}
                            onClick={() => void removeModel(m)}
                          >
                            {t('common.remove')}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
            </div>
          </>
        )
      )}

      {modelDialogOpen && (
        <Dialog
          title={editingModel ? 'Edit upstream model' : 'Add upstream model'}
          onClose={() => setModelDialogOpen(false)}
          testId="provider-model-dialog"
        >
          {modelFormError && <ErrorBanner message={modelFormError} />}
          <label className="field">
            <span>{t('providers.upstreamModelId')}</span>
            <input
              data-testid="provider-model-form-upstream"
              value={modelForm.upstreamModelId}
              disabled={!!editingModel}
              onChange={(e) => setModelForm({ ...modelForm, upstreamModelId: e.target.value })}
            />
          </label>
          <label className="field">
            <span>{t('providers.displayName')}</span>
            <input
              data-testid="provider-model-form-display"
              value={modelForm.displayName}
              onChange={(e) => setModelForm({ ...modelForm, displayName: e.target.value })}
            />
          </label>
          <label className="field">
            <span>{t('providers.contextWindow')}</span>
            <input
              data-testid="provider-model-form-window"
              value={modelForm.contextWindow}
              onChange={(e) => setModelForm({ ...modelForm, contextWindow: e.target.value })}
            />
          </label>
          <label className="field">
            <span>{t('providers.fieldEnabled')}</span>
            <input
              type="checkbox"
              data-testid="provider-model-form-enabled"
              checked={modelForm.enabled}
              onChange={(e) => setModelForm({ ...modelForm, enabled: e.target.checked })}
            />
          </label>
          <div className="dialog-actions">
            <button className="secondary" data-testid="provider-model-form-cancel" onClick={() => setModelDialogOpen(false)}>
              Cancel
            </button>
            <button className="primary" data-testid="provider-model-form-submit" onClick={() => void submitModel()}>
              {t('providers.saveModel')}
            </button>
          </div>
        </Dialog>
      )}

      {publishTarget && (
        <Dialog title={t('providers.publishTitle')} onClose={() => setPublishTarget(null)} testId="provider-publish-dialog">
          {publishError && <ErrorBanner message={publishError} />}
          <p className="subtitle">
            {t('providers.publishBody')}
          </p>
          <label className="field">
            <span>{t('providers.tenantName')}</span>
            <input
              data-testid="provider-publish-name"
              value={publishName}
              onChange={(e) => setPublishName(e.target.value)}
            />
          </label>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setPublishTarget(null)}>
              {t('common.cancel')}
            </button>
            <button className="primary" data-testid="provider-publish-confirm" onClick={() => void submitPublish()}>
              {t('providers.publish')}
            </button>
          </div>
        </Dialog>
      )}

      {unpublishTarget && (
        <Dialog title={t('providers.unpublishTitle')} onClose={() => setUnpublishTarget(null)} testId="provider-unpublish-dialog">
          <p>
            {t('providers.unpublishBodyA')} <strong>{unpublishTarget.displayName || unpublishTarget.upstreamModelId}</strong>{t('providers.unpublishBodyB')}
          </p>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setUnpublishTarget(null)}>
              {t('common.cancel')}
            </button>
            <button className="danger" data-testid="provider-unpublish-confirm" onClick={() => void submitUnpublish()}>
              {t('providers.unpublish')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}
