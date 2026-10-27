// Models page: the catalog with search, pagination, register action and
// per-card deploy entry. Implements docs/design/model-catalog-deployment.md
// FR1, FR2, AC1, AC2.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { navigate } from '../router';
import { api, ApiError, formatTime, type ModelSummary, type PageMeta } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination } from '../components';
import DeployDialog from '../components/DeployDialog';

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
  const [search, setSearch] = useState('');
  const [registerOpen, setRegisterOpen] = useState(false);
  const [deployTarget, setDeployTarget] = useState<{
    modelId: string;
    name: string;
    version: string;
  } | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ModelSummary | null>(null);
  const [deleteError, setDeleteError] = useState('');
  const [deleting, setDeleting] = useState(false);

  const confirmDelete = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    setDeleteError('');
    try {
      await api.del(`/api/v1/admin/models/${deleteTarget.modelId}`, orgId);
      setDeleteTarget(null);
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
        <div className="toolbar">
          <button
            className="secondary"
            data-testid="open-model-downloads"
            onClick={() => navigate('/admin/model-downloads')}
          >
            {t('models.downloads.open')}
          </button>
          <button data-testid="register-model" onClick={() => setRegisterOpen(true)}>
            {t('models.register')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <input
          type="text"
          data-testid="model-search"
          placeholder={t('common.searchByName')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>

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
                <th>{t('models.colLatestVersion')}</th>
                <th>{t('models.colWeightPath')}</th>
                <th>{t('models.colCreated')}</th>
                <th>{t('common.actions')}</th>
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
                    {m.external && (
                      <span className="badge external" data-testid="model-external-badge">
                        {t('modeldetail.external')}
                      </span>
                    )}
                  </td>
                  <td className="mono muted" data-testid={`model-hub-id-${m.name}`}>
                    {m.hubModelId || '—'}
                  </td>
                  <td className="mono">{m.external ? '—' : m.latestVersion}</td>
                  <td className="mono muted">{m.external ? `${m.providerName} · ${m.upstreamModelId}` : m.weightPath}</td>
                  <td>{formatTime(m.createdAt)}</td>
                  <td onClick={(e) => e.stopPropagation()}>
                    <button
                      className="link"
                      data-testid={`deploy-${m.name}`}
                      onClick={() =>
                        setDeployTarget({
                          modelId: m.modelId,
                          name: m.name,
                          version: m.latestVersion,
                        })
                      }
                    >
                      {t('models.deploy')}
                    </button>
                    <button
                      className="link danger"
                      data-testid={`delete-model-${m.name}`}
                      onClick={() => {
                        setDeleteError('');
                        setDeleteTarget(m);
                      }}
                    >
                      {t('common.delete')}
                    </button>
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

      {registerOpen && (
        <RegisterDialog
          orgId={orgId}
          onClose={() => setRegisterOpen(false)}
          onRegistered={() => {
            setRegisterOpen(false);
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

      {deployTarget && (
        <DeployDialog
          orgId={orgId}
          modelId={deployTarget.modelId}
          modelName={deployTarget.name}
          initialVersion={deployTarget.version}
          onClose={() => setDeployTarget(null)}
          onDeployed={(serviceId) => {
            setDeployTarget(null);
            navigate(`/admin/inference-services/${serviceId}`);
          }}
        />
      )}
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
  onRegistered: () => void;
}) {
  const [name, setName] = useState('');
  const [version, setVersion] = useState('');
  const [weightPath, setWeightPath] = useState('');
  const [description, setDescription] = useState('');
  // The hub id binds the model to its identity on a public hub; weights
  // themselves are imported from the model downloads page.
  const [hubModelId, setHubModelId] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  const submit = async () => {
    // FR1.1 validation: name 1-128, version 1-64, weight path required.
    if (!name.trim() || name.trim().length > 128) {
      setError(t('models.validationName'));
      return;
    }
    if (!version.trim() || version.trim().length > 64) {
      setError(t('models.validationVersion'));
      return;
    }
    if (!weightPath.trim()) {
      setError(t('models.validationWeightPath'));
      return;
    }
    if (hubModelId.trim() && !/^[^/\s]+\/[^/\s]+$/.test(hubModelId.trim())) {
      setError(t('models.downloads.validationHubModelId'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const body: Record<string, string> = {
        name: name.trim(),
        version: version.trim(),
        description: description.trim(),
      };
      body.weightPath = weightPath.trim();
      if (hubModelId.trim()) {
        body.hubModelId = hubModelId.trim();
      }
      await api.post('/api/v1/admin/models', orgId, body);
      onRegistered();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('models.registerFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('models.registerTitle')} onClose={onClose} testId="register-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="model-name">{t('models.fieldName')}</label>
          <input
            id="model-name"
            data-testid="model-name-input"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('models.placeholderName')}
          />
        </div>
        <div className="form-field">
          <label htmlFor="model-version">{t('models.fieldVersion')}</label>
          <input
            id="model-version"
            data-testid="model-version-input"
            value={version}
            maxLength={64}
            onChange={(e) => setVersion(e.target.value)}
            placeholder={t('models.placeholderVersion')}
          />
        </div>
        <div className="form-field full">
          <label htmlFor="model-hub-id">{t('models.fieldHubModelId')}</label>
          <input
            id="model-hub-id"
            data-testid="model-hub-id-input"
            value={hubModelId}
            maxLength={255}
            onChange={(e) => setHubModelId(e.target.value)}
            placeholder={t('models.placeholderHubModelId')}
          />
          <div className="field-hint">{t('models.hubModelIdHint')}</div>
        </div>
        <div className="form-field full">
          <label htmlFor="model-weight-path">{t('models.fieldWeightPath')}</label>
          <input
            id="model-weight-path"
            data-testid="model-weight-path-input"
            value={weightPath}
            onChange={(e) => setWeightPath(e.target.value)}
            placeholder={t('models.placeholderWeightPath')}
          />
          <div className="field-hint">{t('models.weightPathHint')}</div>
        </div>
        <div className="form-field full">
          <label htmlFor="model-description">{t('common.descriptionOptional')}</label>
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
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
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
