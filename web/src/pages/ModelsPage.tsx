// Models page: the catalog with search, pagination, register action and
// per-card deploy entry. Implements docs/design/model-catalog-deployment.md
// FR1, FR2, AC1, AC2.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { navigate } from '../router';
import { api, ApiError, formatTime, type ModelSummary, type PageMeta } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { FieldLabel, FormLegend, FormSection, RequiredStatus } from '../components/FormField';
import { Dialog, ErrorBanner, Pagination, RowActionsMenu, Toast } from '../components';
import ModelDownloadsPanel from '../components/ModelDownloadsPanel';
import ModelCachesPage from './ModelCachesPage';
import ModelTransfersPage from './ModelTransfersPage';
import UploadModelDialog from '../components/UploadModelDialog';
import { StorageBackendSelect } from '../components/StorageBackendSelect';


interface ListResponse {
  response: { code: number; message: string };
  models: ModelSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

export default function ModelsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [models, setModels] = useState<ModelSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [search, setSearch] = useState('');
  // The page owns the three model-weight workflows: the catalog, node-local
  // warmup and asynchronous transfers. A query parameter lets the legacy
  // routes land on the right tab.
  const [tab, setTab] = useState<'catalog' | 'warmup' | 'transfers'>(() => {
    const requested = new URLSearchParams(window.location.search).get('tab');
    return requested === 'warmup' || requested === 'transfers' ? requested : 'catalog';
  });
  const selectTab = (next: 'catalog' | 'warmup' | 'transfers') => {
    setTab(next);
    const url = new URL(window.location.href);
    if (next === 'catalog') url.searchParams.delete('tab');
    else url.searchParams.set('tab', next);
    window.history.replaceState({}, '', `${url.pathname}${url.search}`);
  };
  const [registerOpen, setRegisterOpen] = useState(false);
  // Model downloads live on this page: importing a model and browsing the
  // catalog are the same job, so there is no separate navigation entry.
  const [downloadsOpen, setDownloadsOpen] = useState(false);
  // A local archive upload: the same catalog, filled from the operator's
  // own disk instead of a public hub.
  const [uploadOpen, setUploadOpen] = useState(false);
  // A row-level warm shortcut hands the model to the warmup tab, which opens
  // its dialog preselected; the value is consumed once that tab mounts.
  const [warmModelId, setWarmModelId] = useState('');
  const [deleteTarget, setDeleteTarget] = useState<ModelSummary | null>(null);
  const [deleteError, setDeleteError] = useState('');
  const [deleting, setDeleting] = useState(false);

  const confirmDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    setDeleteError('');
    try {
      const deletedName = deleteTarget.name;
      await api.del(`/api/v1/admin/models/${deleteTarget.modelId}`, orgId);
      setDeleteTarget(null);
      setNotice(t('models.deleteSuccess', { name: deletedName }));
      await load();
    } catch (e) {
      // 10101 with a service name means the model is still deployed.
      setDeleteError(
        e instanceof ApiError ? `${e.message} (code ${e.code})` : t('models.deleteFailed'),
      );
    } finally {
      setDeleting(false);
    }
  };

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<ListResponse>(
        `/api/v1/admin/models?page.offset=${offset}&page.limit=${PAGE_SIZE}`,
        orgId,
      );
      setModels(data.models || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('models.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // FR2.2: client-side name filter over the current page.
  const filtered = useMemo(() => {
    if (!search.trim()) return models;
    const q = search.trim().toLowerCase();
    return models.filter((m) => m.name.toLowerCase().includes(q));
  }, [models, search]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('models.title')}</h1>
          <div className="subtitle">{t('models.subtitle')}</div>
        </div>
      </div>

      <div className="guide-grid" data-testid="model-workflows-guide">
        <article className="guide-card">
          <div className="guide-body">
            <strong>{t('models.guideCatalogTitle')}</strong>
            <p>{t('models.guideCatalogBody')}</p>
          </div>
        </article>
        <article className="guide-card">
          <div className="guide-body">
            <strong>{t('models.guideWarmupTitle')}</strong>
            <p>{t('models.guideWarmupBody')}</p>
          </div>
        </article>
        <article className="guide-card">
          <div className="guide-body">
            <strong>{t('models.guideTransferTitle')}</strong>
            <p>{t('models.guideTransferBody')}</p>
          </div>
        </article>
      </div>

      <div className="tab-strip" role="tablist">
        <button className={`tab ${tab === 'catalog' ? 'active' : ''}`} role="tab" aria-selected={tab === 'catalog'} data-testid="models-tab-catalog" onClick={() => selectTab('catalog')}>
          {t('models.tabCatalog')}
        </button>
        <button className={`tab ${tab === 'warmup' ? 'active' : ''}`} role="tab" aria-selected={tab === 'warmup'} data-testid="models-tab-warmup" onClick={() => selectTab('warmup')}>
          {t('models.tabWarmup')}
        </button>
        <button className={`tab ${tab === 'transfers' ? 'active' : ''}`} role="tab" aria-selected={tab === 'transfers'} data-testid="models-tab-transfers" onClick={() => selectTab('transfers')}>
          {t('models.tabTransfers')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}
      <Toast message={notice} onDismiss={() => setNotice('')} testId="models-toast" />

      <div className="models-tab-content">
      {tab === 'warmup' ? (
        <ModelCachesPage
          embedded
          initialWarmModelId={warmModelId || undefined}
          onInitialWarmConsumed={() => setWarmModelId('')}
        />
      ) : tab === 'transfers' ? (
        <ModelTransfersPage embedded />
      ) : (
        <>
          <div className="toolbar models-tab-toolbar">
            <input
              type="text"
              data-testid="model-search"
              placeholder={t('common.searchByName')}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            <div className="toolbar">
              <button
                className="secondary"
                data-testid="open-model-downloads"
                onClick={() => setDownloadsOpen((open) => !open)}
              >
                {downloadsOpen ? t('models.downloads.hide') : t('models.downloads.open')}
              </button>
              <button className="secondary" data-testid="open-model-upload" onClick={() => setUploadOpen(true)}>
                {t('models.upload.open')}
              </button>
              <button data-testid="register-model" onClick={() => setRegisterOpen(true)}>
                {t('models.register')}
              </button>
            </div>
          </div>

          {downloadsOpen && (
            <div className="downloads-section">
              <ModelDownloadsPanel defaultOpen />
            </div>
          )}

          {uploadOpen && (
            <UploadModelDialog
              onClose={() => setUploadOpen(false)}
              onDone={() => {
                setUploadOpen(false);
                setNotice(t('models.upload.success'));
                void load();
              }}
            />
          )}

          <div className="panel">
            {loading ? (
              <div className="loading">{t('common.loading')}</div>
            ) : filtered.length === 0 ? (
              <div className="empty-state" data-testid="models-empty">
                {search ? t('models.emptySearch') : t('models.empty')}
              </div>
            ) : (
              <table className="data" data-testid="models-table">
                <thead>
                  <tr>
                    <th>{t('models.colName')}</th>
                    <th>{t('models.colHubModelId')}</th>
                    <th>{t('models.colWeightPath')}</th>
                    <th>{t('models.colStorageBackend')}</th>
                    <th>{t('models.colCreated')}</th>
                    <th className="col-actions">{t('common.actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {filtered.map((m) => (
                    <tr
                      key={m.modelId}
                      data-testid={`model-row-${m.name}`}
                      style={{ cursor: 'pointer' }}
                      onClick={() => navigate(`/admin/models/${m.modelId}`)}
                    >
                      <td>
                        <strong>{m.name}</strong>
                      </td>
                      <td className="mono muted" data-testid={`model-hub-id-${m.name}`}>
                        {m.hubModelId || '—'}
                      </td>
                      <td className="mono muted">{m.weightPath}</td>
                      <td className="mono muted" data-testid={`model-storage-backend-${m.name}`}>
                        {m.storageBackend || '—'}
                      </td>
                      <td>{formatTime(m.createdAt)}</td>
                      <td onClick={(e) => e.stopPropagation()}>
                        <RowActionsMenu
                          label={t('common.actionsFor', { name: m.name })}
                          testId={`model-actions-${m.name}`}
                          actions={[
                            {
                              key: `deploy-${m.name}`,
                              label: t('models.deploy'),
                              onClick: () =>
                                navigate(
                                  `/admin/inference-services?create=1&modelId=${encodeURIComponent(m.modelId)}` +
                                    `&version=${encodeURIComponent(m.latestVersion)}`,
                                ),
                            },
                            {
                              key: `warm-${m.name}`,
                              label: t('caches.warm'),
                              onClick: () => {
                                setWarmModelId(m.modelId);
                                selectTab('warmup');
                              },
                            },
                            {
                              key: `delete-model-${m.name}`,
                              label: t('common.delete'),
                              danger: true,
                              onClick: () => {
                                setDeleteError('');
                                setDeleteTarget(m);
                              },
                            },
                          ]}
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            {total > PAGE_SIZE && (
              <Pagination
                offset={offset}
                limit={PAGE_SIZE}
                total={total}
                onPageChange={setOffset}
              />
            )}
          </div>
        </>
      )}

      {registerOpen && (
        <RegisterDialog
          orgId={orgId}
          onClose={() => setRegisterOpen(false)}
          onRegistered={(name) => {
            setRegisterOpen(false);
            setNotice(t('models.registerSuccess', { name }));
            void load();
          }}
        />
      )}

      {deleteTarget && (
        <Dialog
          title={t('models.deleteTitle', { name: deleteTarget.name })}
          onClose={() => setDeleteTarget(null)}
          testId="delete-model-dialog"
        >
          <p>{t('models.deleteBody')}</p>
          {deleteError && <ErrorBanner message={deleteError} />}
          <div className="dialog-actions">
            <button
              className="secondary"
              data-testid="delete-model-cancel"
              onClick={() => setDeleteTarget(null)}
            >
              {t('common.cancel')}
            </button>
            <button
              className="danger"
              data-testid="delete-model-confirm"
              disabled={deleting}
              onClick={() => void confirmDelete()}
            >
              {deleting ? t('common.working') : t('common.delete')}
            </button>
          </div>
        </Dialog>
      )}
      </div>
    </div>
  );

}

function RegisterDialog({
  orgId,
  onClose,
  onRegistered,
}: {
  orgId: string;
  onClose: () => void;
  onRegistered: (name: string) => void;
}) {
  const [name, setName] = useState('');
  const [weightPath, setWeightPath] = useState('');
  const [description, setDescription] = useState('');
  // The hub id binds the model to its identity on a public hub; weights
  // themselves are imported from the model downloads page.
  const [hubModelId, setHubModelId] = useState('');
  const [storageBackend, setStorageBackend] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  const submit = async () => {
    // FR1.1 validation: name 1-128, hub model id required and unique,
    // weight path required. Version is not a catalog concept any more.
    if (!name.trim() || name.trim().length > 128) {
      setError(t('models.validationName'));
      return;
    }
    if (!hubModelId.trim()) {
      setError(t('models.validationHubModelIdRequired'));
      return;
    }
    if (!/^[^/\s]+\/[^/\s]+$/.test(hubModelId.trim())) {
      setError(t('models.validationHubModelId'));
      return;
    }
    if (!weightPath.trim()) {
      setError(t('models.validationWeightPath'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const body: Record<string, string> = {
        name: name.trim(),
        description: description.trim(),
        weightPath: weightPath.trim(),
        hubModelId: hubModelId.trim(),
        storageBackend,
      };
      await api.post('/api/v1/admin/models', orgId, body);
      onRegistered(name.trim());
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('models.registerFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  // What the request cannot do without, checked as the operator types so a
  // missing value is visible before the round trip.
  const missing = [
    !name.trim() && t('models.fieldName'),
    !hubModelId.trim() && t('models.fieldHubModelId'),
    !weightPath.trim() && t('models.fieldWeightPath'),
  ].filter((entry): entry is string => Boolean(entry));

  return (
    <Dialog title={t('models.registerTitle')} onClose={onClose} testId="register-dialog">
      <FormLegend testId="register-legend" />
      <FormSection
        step={1}
        title={t('models.registerSectionIdentity')}
        hint={t('models.registerSectionIdentityHint')}
        requirement="required"
        testId="register-section-identity"
      >
        <div className="form-grid">
          <div className="form-field">
            <FieldLabel htmlFor="model-name" text={t('models.fieldName')} requirement="required" />
            <input
              id="model-name"
              data-testid="model-name-input"
              value={name}
              maxLength={128}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('models.placeholderName')}
            />
          </div>
          <div className="form-field full">
            <FieldLabel
              htmlFor="model-hub-id"
              text={t('models.fieldHubModelId')}
              requirement="required"
            />
            <input
              id="model-hub-id"
              data-testid="model-hub-id-input"
              className="mono"
              value={hubModelId}
              maxLength={255}
              onChange={(e) => setHubModelId(e.target.value)}
              placeholder={t('models.placeholderHubModelId')}
            />
            <div className="field-hint">{t('models.hubModelIdHint')}</div>
          </div>
        </div>
      </FormSection>

      <FormSection
        step={2}
        title={t('models.registerSectionWeights')}
        hint={t('models.registerSectionWeightsHint')}
        requirement="required"
        testId="register-section-weights"
      >
        <div className="form-grid">
          <div className="form-field full">
            <FieldLabel
              htmlFor="model-weight-path"
              text={t('models.fieldWeightPath')}
              requirement="required"
            />
            <input
              id="model-weight-path"
              data-testid="model-weight-path-input"
              className="mono"
              value={weightPath}
              onChange={(e) => setWeightPath(e.target.value)}
              placeholder={t('models.placeholderWeightPath')}
            />
            <div className="field-hint">{t('models.weightPathHint')}</div>
          </div>
          <StorageBackendSelect
            id="register-storage-backend"
            testId="register-storage-backend"
            value={storageBackend}
            onChange={setStorageBackend}
          />
          <div className="form-field full">
            <FieldLabel
              htmlFor="model-description"
              text={t('common.descriptionOptional')}
              requirement="optional"
            />
            <textarea
              id="model-description"
              data-testid="model-description-input"
              value={description}
              maxLength={1024}
              rows={3}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
        </div>
      </FormSection>

      {error && <ErrorBanner message={error} />}

      <div className="dialog-actions">
        <RequiredStatus missing={missing} />
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="submit-register-model"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? t('common.creating') : t('models.register')}
        </button>
      </div>
    </Dialog>
  );
}
