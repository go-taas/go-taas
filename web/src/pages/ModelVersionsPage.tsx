// Model Versions page (admin): version history with active/latest badges,
// weight path, created date, per-version deployment counts; register,
// activate and rollback actions.
// Implements docs/design/model-versioning.md FR1-FR4 and
// docs/architecture/model-versioning.md §6.5.

import { useCallback, useEffect, useState } from 'react';
import { navigate } from '../router';
import { api, formatTime, type InferenceServiceSummary } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { BackLink, Dialog, ErrorBanner } from '../components';

interface ModelVersion {
  version: string;
  weightPath: string;
  createdAt: string;
  isActive: boolean;
  deploymentCount: string;
}

interface ListVersionsResponse {
  response: { code: number; message: string };
  modelId: string;
  name: string;
  activeVersion: string;
  versions: ModelVersion[];
}

interface ListServicesResponse {
  response: { code: number; message: string };
  services: InferenceServiceSummary[];
}

export default function ModelVersionsPage() {
  const { orgId } = useOrg();
  const { t, tEnum } = useI18n();
  const modelId = window.location.pathname.split('/')[3] || '';
  const [data, setData] = useState<ListVersionsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [registerOpen, setRegisterOpen] = useState(false);
  const [activateVersion, setActivateVersion] = useState<string | null>(null);
  const [rollbackVersion, setRollbackVersion] = useState<string | null>(null);
  const [services, setServices] = useState<InferenceServiceSummary[]>([]);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [mutating, setMutating] = useState(false);
  const [notice, setNotice] = useState('');

  const load = useCallback(async () => {
    try {
      const data = await api.get<ListVersionsResponse>(
        `/api/v1/admin/models/${modelId}/versions`,
        orgId,
      );
      setData(data);
      setError('');
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('modelversions.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [modelId, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const loadServices = useCallback(async () => {
    try {
      const data = await api.get<ListServicesResponse>(
        '/api/v1/admin/inference-services',
        orgId,
      );
      setServices(data.services || []);
    } catch {
      setServices([]);
    }
  }, [orgId]);

  const openRollback = (version: string) => {
    setRollbackVersion(version);
    setSelected(new Set());
    void loadServices();
  };

  const register = async (version: string, weightPath: string, description: string) => {
    setMutating(true);
    try {
      await api.post('/api/v1/admin/models', orgId, {
        name: data?.name,
        version,
        weightPath,
        description,
      });
      setRegisterOpen(false);
      setNotice(t('modelversions.registerSuccess'));
      await load();
    } catch (e) {
      const msg = e instanceof Error ? e.message : '';
      if (msg.includes('10102') || msg.toLowerCase().includes('exists')) {
        throw new Error(t('modelversions.duplicate'));
      }
      throw e;
    } finally {
      setMutating(false);
    }
  };

  const activate = async (version: string) => {
    setMutating(true);
    try {
      await api.post(
        `/api/v1/admin/models/${modelId}/versions/${version}:activate`,
        orgId,
        {},
      );
      setActivateVersion(null);
      setNotice(t('modelversions.activateSuccess'));
      await load();
    } finally {
      setMutating(false);
    }
  };

  const rollback = async () => {
    setMutating(true);
    try {
      for (const serviceId of selected) {
        await api.post(
          `/api/v1/admin/inference-services/${serviceId}:update-version`,
          orgId,
          { modelVersion: rollbackVersion },
        );
      }
      setRollbackVersion(null);
      setNotice(t('modelversions.rollbackSuccess'));
      await load();
    } finally {
      setMutating(false);
    }
  };

  if (loading) {
    return (
      <div>
        <BackLink to="/admin/models" label={t('modelversions.back')} />
        <div className="loading">{t('common.loading')}</div>
      </div>
    );
  }

  const latestVersion = data?.versions[0]?.version;

  return (
    <div>
      <BackLink to="/admin/models" label={t('modelversions.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="model-versions-title">{t('modelversions.title')}</h1>
          <div className="subtitle mono">
            {data?.name} · {data?.modelId}
          </div>
        </div>
        <button
          className="primary"
          data-testid="register-version-button"
          disabled={loading || mutating}
          onClick={() => setRegisterOpen(true)}
        >
          {t('modelversions.register')}
        </button>
      </div>

      {error && (
        <div>
          <ErrorBanner message={error} />
          {stale && <div className="muted">{t('modelversions.stale')}</div>}
          <button className="secondary" data-testid="retry-button" onClick={() => void load()}>
            {t('modelversions.retry')}
          </button>
        </div>
      )}
      {notice && <div className="success-banner" data-testid="notice">{notice}</div>}

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('modelversions.activeBanner')}</h3>
        <div data-testid="active-version-banner">
          {data?.activeVersion ? (
            <span className="badge active" data-testid="active-version-value">
              {data.activeVersion}
            </span>
          ) : (
            <span className="muted" data-testid="no-active-version">
              {t('modelversions.noActive')}
            </span>
          )}
        </div>
        <div className="muted" style={{ marginTop: 8 }}>{t('modelversions.activeNote')}</div>
      </div>

      <div className="panel">
        <h3 style={{ marginTop: 0 }}>{t('modelversions.colVersion')}</h3>
        {!data || data.versions.length === 0 ? (
          <div className="empty-state" data-testid="versions-empty">
            {t('modelversions.empty')}
            <div className="muted">{t('modelversions.emptyHint')}</div>
          </div>
        ) : (
          <table className="data" data-testid="model-versions-table">
            <thead>
              <tr>
                <th>{t('modelversions.colVersion')}</th>
                <th>{t('modelversions.colStatus')}</th>
                <th>{t('modelversions.colWeightPath')}</th>
                <th>{t('modelversions.colCreated')}</th>
                <th>{t('modelversions.colDeployments')}</th>
                <th>{t('modelversions.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {data.versions.map((v) => (
                <tr key={v.version} data-testid={`model-version-row-${v.version}`}>
                  <td className="mono">{v.version}</td>
                  <td>
                    {v.isActive && (
                      <span className="badge active" data-testid={`active-badge-${v.version}`}>
                        {t('modelversions.active')}
                      </span>
                    )}
                    {v.version === latestVersion && (
                      <span className="badge latest" data-testid={`latest-badge-${v.version}`}>
                        {t('modelversions.latest')}
                      </span>
                    )}
                  </td>
                  <td className="mono">{v.weightPath}</td>
                  <td>{formatTime(v.createdAt)}</td>
                  <td data-testid={`deployments-${v.version}`}>{v.deploymentCount}</td>
                  <td>
                    {!v.isActive && (
                      <button
                        className="link"
                        data-testid={`activate-${v.version}`}
                        disabled={mutating}
                        onClick={() => setActivateVersion(v.version)}
                      >
                        {t('modelversions.activate')}
                      </button>
                    )}
                    <button
                      className="link"
                      data-testid={`rollback-${v.version}`}
                      disabled={mutating}
                      onClick={() => openRollback(v.version)}
                    >
                      {t('modelversions.rollback')}
                    </button>
                    <button
                      className="link"
                      data-testid={`deploy-${v.version}`}
                      onClick={() => navigate(`/admin/models/${modelId}`)}
                    >
                      {t('modelversions.deploy')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {registerOpen && (
        <RegisterVersionDialog
          modelName={data?.name || ''}
          onClose={() => setRegisterOpen(false)}
          onSubmit={register}
          submitting={mutating}
        />
      )}

      {activateVersion && (
        <Dialog
          title={t('modelversions.activateTitle')}
          onClose={() => setActivateVersion(null)}
          testId="activate-dialog"
        >
          <p>{t('modelversions.activateConfirm', { version: activateVersion })}</p>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setActivateVersion(null)}>
              {t('modelversions.cancel')}
            </button>
            <button
              className="primary"
              data-testid="activate-confirm"
              disabled={mutating}
              onClick={() => void activate(activateVersion)}
            >
              {t('modelversions.activateBtn')}
            </button>
          </div>
        </Dialog>
      )}

      {rollbackVersion && (
        <Dialog
          title={t('modelversions.rollbackTitle')}
          onClose={() => setRollbackVersion(null)}
          testId="rollback-dialog"
        >
          <p className="warning-text" data-testid="rollback-warning">
            {t('modelversions.rollbackWarning')}
          </p>
          {services.filter((s) => s.modelVersion !== rollbackVersion && s.state !== 'terminated')
            .length === 0 ? (
            <div className="empty-state" data-testid="rollback-empty">
              {t('modelversions.rollbackEmpty')}
            </div>
          ) : (
            <table className="data">
              <thead>
                <tr>
                  <th></th>
                  <th>{t('modelversions.colService')}</th>
                  <th>{t('modelversions.colCurrentVersion')}</th>
                  <th>{t('modelversions.colState')}</th>
                </tr>
              </thead>
              <tbody>
                {services
                  .filter((s) => s.modelVersion !== rollbackVersion && s.state !== 'terminated')
                  .map((s) => (
                    <tr key={s.serviceId}>
                      <td>
                        <input
                          type="checkbox"
                          data-testid={`rollback-select-${s.serviceId}`}
                          checked={selected.has(s.serviceId)}
                          onChange={(e) => {
                            const next = new Set(selected);
                            if (e.target.checked) next.add(s.serviceId);
                            else next.delete(s.serviceId);
                            setSelected(next);
                          }}
                        />
                      </td>
                      <td>{s.name}</td>
                      <td className="mono">{s.modelVersion}</td>
                      <td>{tEnum('state', s.state)}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          )}
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setRollbackVersion(null)}>
              {t('modelversions.cancel')}
            </button>
            <button
              className="primary"
              data-testid="rollback-confirm"
              disabled={mutating || selected.size === 0}
              onClick={() => void rollback()}
            >
              {t('modelversions.rollbackBtn')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

function RegisterVersionDialog({
  modelName,
  onClose,
  onSubmit,
  submitting,
}: {
  modelName: string;
  onClose: () => void;
  onSubmit: (version: string, weightPath: string, description: string) => Promise<void>;
  submitting: boolean;
}) {
  const { t } = useI18n();
  const [version, setVersion] = useState('');
  const [weightPath, setWeightPath] = useState('');
  const [description, setDescription] = useState('');
  const [error, setError] = useState('');

  const submit = async () => {
    if (!version || version.length > 64) {
      setError(t('modelversions.versionRequired'));
      return;
    }
    if (!weightPath) {
      setError(t('modelversions.weightPathRequired'));
      return;
    }
    setError('');
    try {
      await onSubmit(version, weightPath, description);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <Dialog title={t('modelversions.registerTitle')} onClose={onClose} testId="register-dialog">
      <div className="form-field">
        <label>{t('modelversions.modelName')}</label>
        <input value={modelName} readOnly data-testid="register-model-name" />
      </div>
      <div className="form-field">
        <label>{t('modelversions.fieldVersion')}</label>
        <input
          value={version}
          data-testid="register-version-input"
          onChange={(e) => setVersion(e.target.value)}
        />
      </div>
      <div className="form-field">
        <label>{t('modelversions.fieldWeightPath')}</label>
        <input
          value={weightPath}
          data-testid="register-weight-path-input"
          onChange={(e) => setWeightPath(e.target.value)}
        />
      </div>
      <div className="form-field">
        <label>{t('modelversions.fieldDescription')}</label>
        <textarea
          value={description}
          data-testid="register-description-input"
          onChange={(e) => setDescription(e.target.value)}
        />
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('modelversions.cancel')}
        </button>
        <button
          className="primary"
          data-testid="register-submit"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {t('modelversions.submit')}
        </button>
      </div>
    </Dialog>
  );
}