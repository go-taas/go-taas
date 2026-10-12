// Admin Runtime page (feature #47): the distributed inference runtime
// overview (serving scheme + availability), the runtime component-health
// table, the serving-scheme switcher, and the registered-model table with
// register/deregister flows. Admin-surface only — it calls ONLY
// /api/v1/admin/runtime/*. Implements
// docs/design/distributed-inference-runtime.md §5.3.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime, type PageMeta } from '../api';
import { Dialog, ErrorBanner, Pagination, usePolling } from '../components';
import { useI18n } from '../i18n';

interface ResponseEnvelope {
  code: number;
  message: string;
}

interface RuntimeComponent {
  componentId: string;
  name: string;
  kind: string;
  namespace: string;
  status: string;
  readyReplicas: string;
  desiredReplicas: string;
  lastCheckedAt: string;
}

interface RuntimeStatus {
  scheme: string;
  availability: string;
  runtimeName: string;
  runtimeVersion: string;
  clusterName: string;
  lastCheckedAt: string;
  reason: string;
  components: RuntimeComponent[];
}

interface GetRuntimeStatusResponse {
  response: ResponseEnvelope;
  runtime?: RuntimeStatus;
}

interface RuntimeComponentListResponse {
  response: ResponseEnvelope;
  components?: RuntimeComponent[];
}

interface ServingScheme {
  scheme: string;
  updatedAt: string;
  updatedBy: string;
}

interface GetServingSchemeResponse {
  response: ResponseEnvelope;
  scheme?: ServingScheme;
}

interface RegisteredModel {
  modelName: string;
  engine: string;
  replicas: string;
  readyReplicas: string;
  readyCandidates: string;
  status: string;
  createdAt: string;
  ageSeconds: string;
}

interface ModelListResponse {
  response: ResponseEnvelope;
  models?: RegisteredModel[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;
const POLL_MS = 30_000;
const ENGINES = ['vllm', 'sglang'];
const MODEL_STATUSES = ['ready', 'progressing', 'degraded', 'failed', 'unknown'];
const NAME_PATTERN = /^[a-z0-9][a-z0-9._-]*$/;

// statusBadgeClass maps a component or model status to a stable badge
// style.
function statusBadgeClass(status: string): string {
  switch (status) {
    case 'ready':
      return 'badge running';
    case 'available':
      return 'badge running';
    case 'progressing':
      return 'badge pending';
    case 'degraded':
      return 'badge failed';
    case 'failed':
      return 'badge failed';
    case 'unavailable':
      return 'badge failed';
    default:
      return 'badge terminated';
  }
}

// availabilityBadgeClass maps a runtime availability to a badge style.
function availabilityBadgeClass(availability: string): string {
  switch (availability) {
    case 'available':
      return 'badge running';
    case 'degraded':
      return 'badge failed';
    case 'unavailable':
      return 'badge failed';
    default:
      return 'badge terminated';
  }
}

// formatAge renders an age in seconds as a compact human string.
function formatAge(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}

export default function RuntimePage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();

  const [toast, setToast] = useState('');
  const [actionError, setActionError] = useState('');

  const [status, setStatus] = useState<RuntimeStatus | null>(null);
  const [components, setComponents] = useState<RuntimeComponent[]>([]);
  const [scheme, setScheme] = useState<ServingScheme | null>(null);
  const [models, setModels] = useState<RegisteredModel[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [engine, setEngine] = useState('');
  const [modelStatus, setModelStatus] = useState('');
  const [search, setSearch] = useState('');

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [refreshing, setRefreshing] = useState(false);

  // Scheme switcher dialog state.
  const [schemeDialogOpen, setSchemeDialogOpen] = useState(false);
  const [schemeSelection, setSchemeSelection] = useState('');
  const [schemeConfirmOpen, setSchemeConfirmOpen] = useState(false);

  // Register dialog state.
  const [registerOpen, setRegisterOpen] = useState(false);
  const [form, setForm] = useState({ name: '', engine: 'vllm', replicas: '1', artifact: '' });
  const [formError, setFormError] = useState('');

  // Deregister confirmation state.
  const [deregisterTarget, setDeregisterTarget] = useState('');

  const load = useCallback(
    async (showLoading: boolean) => {
      if (showLoading) setLoading(true);
      setRefreshing(true);
      setError('');
      try {
        const params = new URLSearchParams({
          'page.offset': String(offset),
          'page.limit': String(PAGE_SIZE),
        });
        if (engine) params.set('engine', engine);
        if (modelStatus) params.set('status', modelStatus);
        if (search) params.set('name', search);
        const [statusData, componentsData, schemeData, modelsData] = await Promise.all([
          api.get<GetRuntimeStatusResponse>('/api/v1/admin/runtime', orgId),
          api.get<RuntimeComponentListResponse>('/api/v1/admin/runtime/components', orgId),
          api.get<GetServingSchemeResponse>('/api/v1/admin/runtime/scheme', orgId),
          api.get<ModelListResponse>(`/api/v1/admin/runtime/models?${params.toString()}`, orgId),
        ]);
        setStatus(statusData.runtime || null);
        setComponents(componentsData.components || []);
        setScheme(schemeData.scheme || null);
        setModels(modelsData.models || []);
        setTotal(parseInt(modelsData.pageMeta?.total || '0', 10) || 0);
        setStale(false);
        setPermissionDenied(false);
      } catch (e) {
        const code = (e as { code?: number } | undefined)?.code;
        if (code === 10036) {
          setPermissionDenied(true);
          setLoading(false);
          setRefreshing(false);
          return;
        }
        setError(e instanceof Error ? e.message : t('runtime.loadFailed'));
        // A failed poll keeps the last good data (AC7, AC12).
        if (status || models.length > 0) setStale(true);
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [api, orgId, offset, engine, modelStatus, search, status, models.length, t],
  );

  useEffect(() => {
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [offset, engine, modelStatus, search]);

  usePolling(() => void load(false), POLL_MS, !loading);

  const refresh = () => void load(false);

  const availability = status?.availability || 'unknown';
  const currentScheme = scheme?.scheme || status?.scheme || 'plain';
  const runtimeReady = availability === 'available' && currentScheme === 'aibrix';
  const writesDisabled = availability !== 'available' || currentScheme !== 'aibrix';

  const schemeLabel = (value: string) =>
    value === 'aibrix' ? t('runtime.aibrix') : t('runtime.plainDeployment');

  const openSchemeDialog = () => {
    setSchemeSelection(currentScheme);
    setSchemeDialogOpen(true);
  };

  const applyScheme = () => {
    if (schemeSelection === currentScheme) return;
    setSchemeConfirmOpen(true);
  };

  const confirmSwitch = async () => {
    try {
      await api.put('/api/v1/admin/runtime/scheme', orgId, { scheme: schemeSelection });
      setToast(t('runtime.schemeUpdated'));
      setActionError('');
      setSchemeConfirmOpen(false);
      setSchemeDialogOpen(false);
      await load(false);
    } catch (e) {
      setActionError(e instanceof Error ? e.message : t('runtime.loadFailed'));
      setSchemeConfirmOpen(false);
    }
  };

  const validateForm = (): string => {
    const name = form.name.trim();
    if (name === '' || !NAME_PATTERN.test(name)) return t('runtime.nameInvalid');
    if (form.engine !== 'vllm' && form.engine !== 'sglang') return t('runtime.engineInvalid');
    const replicas = Number(form.replicas);
    if (!Number.isInteger(replicas) || replicas < 0 || replicas > 32) {
      return t('runtime.replicasInvalid');
    }
    if (form.artifact.trim() === '') return t('runtime.artifactHint');
    return '';
  };

  const submitRegister = async () => {
    const validationError = validateForm();
    if (validationError) {
      setFormError(validationError);
      return;
    }
    setFormError('');
    try {
      await api.post('/api/v1/admin/runtime/models', orgId, {
        modelName: form.name.trim(),
        engine: form.engine,
        replicas: Number(form.replicas),
        artifactUrl: form.artifact.trim(),
      });
      setToast(t('runtime.registered'));
      setActionError('');
      setRegisterOpen(false);
      setForm({ name: '', engine: 'vllm', replicas: '1', artifact: '' });
      await load(false);
    } catch (e) {
      const code = (e as { code?: number } | undefined)?.code;
      if (code === 12904) setFormError(t('runtime.nameExists'));
      else if (code === 12905) setFormError(t('runtime.engineInvalid'));
      else if (code === 12906) setFormError(t('runtime.replicasInvalid'));
      else if (code === 12907) setFormError(t('runtime.nameInvalid'));
      else setFormError(e instanceof Error ? e.message : t('runtime.loadFailed'));
    }
  };

  const confirmDeregister = async () => {
    const target = deregisterTarget;
    try {
      await api.del(`/api/v1/admin/runtime/models/${encodeURIComponent(target)}`, orgId);
      setToast(t('runtime.deregistered'));
      setActionError('');
      setDeregisterTarget('');
      await load(false);
    } catch (e) {
      setActionError(e instanceof Error ? e.message : t('runtime.loadFailed'));
      setDeregisterTarget('');
    }
  };

  if (permissionDenied) {
    return (
      <div data-testid="runtime-permission-denied">
        <ErrorBanner message={t('runtime.permissionDenied')} />
        <a href="/admin" className="link" data-testid="runtime-back-admin">
          {t('runtime.backToAdmin')}
        </a>
      </div>
    );
  }

  return (
    <div data-testid="runtime-page">
      <div className="page-header">
        <div>
          <h1>{t('runtime.title')}</h1>
          <div className="subtitle">{t('runtime.subtitle')}</div>
        </div>
        <div className="toolbar">
          <button
            className="secondary"
            data-testid="runtime-switch-scheme"
            disabled={refreshing || loading}
            onClick={openSchemeDialog}
          >
            {t('runtime.switchScheme')}
          </button>
          <button
            className="secondary"
            data-testid="runtime-refresh"
            disabled={refreshing}
            onClick={refresh}
          >
            {refreshing ? t('common.refreshing') : t('runtime.refresh')}
          </button>
        </div>
      </div>

      {error && (
        <div data-testid="runtime-error">
          <ErrorBanner message={error} />
        </div>
      )}
      {actionError && (
        <div data-testid="runtime-action-error">
          <ErrorBanner message={actionError} />
        </div>
      )}
      {toast && (
        <div className="success-banner" data-testid="runtime-toast">
          {toast}
        </div>
      )}
      {stale && (
        <div className="warning-banner" data-testid="runtime-stale-banner">
          {t('runtime.stale')}{' '}
          <button className="link" data-testid="runtime-stale-retry" onClick={refresh}>
            {t('runtime.retry')}
          </button>
        </div>
      )}

      <div className="panel" data-testid="runtime-overview">
        <div className="detail-item">
          <div className="label">{t('runtime.scheme')}</div>
          <div className="value">
            <span
              className={currentScheme === 'aibrix' ? 'badge autoscaled' : 'badge steady'}
              data-testid="runtime-scheme-badge"
            >
              {schemeLabel(currentScheme)}
            </span>
          </div>
        </div>
        <div className="detail-item">
          <div className="label">{t('runtime.availability')}</div>
          <div className="value">
            <span
              className={availabilityBadgeClass(availability)}
              data-testid="runtime-availability-badge"
            >
              {t(`runtime.${availability}`)}
            </span>
          </div>
        </div>
        <div className="detail-item">
          <div className="label">{t('runtime.runtimeName')}</div>
          <div className="value">
            {status?.runtimeName || currentScheme}
            {status?.runtimeVersion ? ` (${status.runtimeVersion})` : ''}
          </div>
        </div>
        <div className="detail-item">
          <div className="label">{t('runtime.cluster')}</div>
          <div className="value">{status?.clusterName || '—'}</div>
        </div>
        <div className="detail-item">
          <div className="label">{t('runtime.lastChecked')}</div>
          <div className="value muted" data-testid="runtime-last-checked">
            {status?.lastCheckedAt ? formatTime(status.lastCheckedAt) : '—'}
          </div>
        </div>
      </div>

      {availability !== 'available' && (
        <div className="warning-banner" data-testid="runtime-unavailable-notice">
          {status?.reason || t('runtime.unavailableNotice')}{' '}
          <button className="link" data-testid="runtime-retry" onClick={refresh}>
            {t('runtime.retry')}
          </button>
        </div>
      )}

      <div className="panel">
        <h2>{t('runtime.components')}</h2>
        {components.length === 0 ? (
          <div className="empty-state" data-testid="runtime-components-empty">
            {t('runtime.componentsEmpty')}
          </div>
        ) : (
          <table className="data" data-testid="runtime-components-table">
            <thead>
              <tr>
                <th>{t('runtime.colComponent')}</th>
                <th>{t('runtime.colKind')}</th>
                <th>{t('runtime.colNamespace')}</th>
                <th>{t('runtime.colStatus')}</th>
                <th>{t('runtime.colReady')}</th>
                <th>{t('runtime.colLastChecked')}</th>
              </tr>
            </thead>
            <tbody>
              {components.map((c) => (
                <tr key={c.componentId} data-testid={`runtime-component-${c.componentId}`}>
                  <td>{c.name}</td>
                  <td>
                    <span className="badge steady">{c.kind}</span>
                  </td>
                  <td className="mono">{c.namespace || '—'}</td>
                  <td>
                    <span className={statusBadgeClass(c.status)}>{c.status}</span>
                  </td>
                  <td>
                    {c.readyReplicas}/{c.desiredReplicas}
                  </td>
                  <td className="muted">{c.lastCheckedAt ? formatTime(c.lastCheckedAt) : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="panel">
        <div className="page-header">
          <h2>{t('runtime.models')}</h2>
          <button
            className="primary"
            data-testid="runtime-register-model"
            disabled={writesDisabled}
            title={writesDisabled ? t('runtime.switchToAibrixHint') : ''}
            onClick={() => {
              setFormError('');
              setRegisterOpen(true);
            }}
          >
            {t('runtime.registerModel')}
          </button>
        </div>

        <div className="toolbar" style={{ marginBottom: 14 }} data-testid="runtime-model-filters">
          <select
            data-testid="runtime-engine-filter"
            value={engine}
            onChange={(e) => {
              setEngine(e.target.value);
              setOffset(0);
            }}
          >
            <option value="">{t('runtime.allEngines')}</option>
            {ENGINES.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
          <select
            data-testid="runtime-status-filter"
            value={modelStatus}
            onChange={(e) => {
              setModelStatus(e.target.value);
              setOffset(0);
            }}
          >
            <option value="">{t('runtime.allStatuses')}</option>
            {MODEL_STATUSES.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
          <input
            data-testid="runtime-model-search"
            placeholder={t('runtime.searchModels')}
            value={search}
            onChange={(e) => {
              setSearch(e.target.value);
              setOffset(0);
            }}
          />
        </div>

        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : models.length === 0 ? (
          <div className="empty-state" data-testid="runtime-models-empty">
            {currentScheme === 'aibrix' ? t('runtime.modelsEmptyAibrix') : t('runtime.modelsEmptyPlain')}
          </div>
        ) : (
          <table className="data" data-testid="runtime-models-table">
            <thead>
              <tr>
                <th>{t('runtime.colModel')}</th>
                <th>{t('runtime.colEngine')}</th>
                <th>{t('runtime.colReplicas')}</th>
                <th>{t('runtime.colReadyCandidates')}</th>
                <th>{t('runtime.colStatus')}</th>
                <th>{t('runtime.colAge')}</th>
                <th>{t('runtime.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {models.map((m) => (
                <tr key={m.modelName} data-testid={`runtime-model-${m.modelName}`}>
                  <td className="mono">{m.modelName}</td>
                  <td>
                    <span className="badge steady">{m.engine}</span>
                  </td>
                  <td>{m.replicas}</td>
                  <td>
                    {m.readyCandidates}/{m.replicas}
                  </td>
                  <td>
                    <span className={statusBadgeClass(m.status)}>{m.status}</span>
                  </td>
                  <td className="muted">
                    {formatAge(Number(m.ageSeconds || '0'))}
                  </td>
                  <td>
                    <button
                      className="secondary danger"
                      disabled={!runtimeReady}
                      title={!runtimeReady ? t('runtime.switchToAibrixHint') : ''}
                      data-testid={`runtime-deregister-${m.modelName}`}
                      onClick={() => setDeregisterTarget(m.modelName)}
                    >
                      {t('runtime.deregister')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {!loading && models.length > 0 && (
          <div data-testid="runtime-models-pagination">
            <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
          </div>
        )}
      </div>

      {schemeDialogOpen && (
        <Dialog
          title={t('runtime.schemeDialogTitle')}
          testId="runtime-scheme-dialog"
          onClose={() => setSchemeDialogOpen(false)}
        >
          <label className="radio-row" data-testid="runtime-scheme-option-plain">
            <input
              type="radio"
              name="scheme"
              checked={schemeSelection === 'plain'}
              onChange={() => setSchemeSelection('plain')}
            />
            <span>
              <strong>{t('runtime.plainDeployment')}</strong>
              <div className="muted">{t('runtime.schemePlainDesc')}</div>
            </span>
          </label>
          <label className="radio-row" data-testid="runtime-scheme-option-aibrix">
            <input
              type="radio"
              name="scheme"
              checked={schemeSelection === 'aibrix'}
              disabled={availability !== 'available'}
              onChange={() => setSchemeSelection('aibrix')}
            />
            <span>
              <strong>{t('runtime.aibrix')}</strong>
              <div className="muted">{t('runtime.schemeAibrixDesc')}</div>
            </span>
          </label>
          {availability !== 'available' && (
            <div className="muted" data-testid="runtime-scheme-aibrix-hint">
              {t('runtime.aibrixUnavailableHint')}
            </div>
          )}
          <div className="dialog-actions">
            <button
              className="secondary"
              data-testid="runtime-scheme-cancel"
              onClick={() => setSchemeDialogOpen(false)}
            >
              {t('runtime.cancel')}
            </button>
            <button
              className="primary"
              data-testid="runtime-scheme-apply"
              disabled={schemeSelection === currentScheme}
              onClick={applyScheme}
            >
              {t('runtime.apply')}
            </button>
          </div>
        </Dialog>
      )}

      {schemeConfirmOpen && (
        <Dialog
          title={t('runtime.switchConfirmTitle')}
          testId="runtime-scheme-confirm"
          onClose={() => setSchemeConfirmOpen(false)}
        >
          <p>
            {t('runtime.switchConfirmBody', { scheme: schemeLabel(schemeSelection) })}
          </p>
          <div className="dialog-actions">
            <button
              className="secondary"
              data-testid="runtime-scheme-confirm-cancel"
              onClick={() => setSchemeConfirmOpen(false)}
            >
              {t('runtime.cancel')}
            </button>
            <button
              className="primary"
              data-testid="runtime-scheme-confirm-switch"
              onClick={() => void confirmSwitch()}
            >
              {t('runtime.switch')}
            </button>
          </div>
        </Dialog>
      )}

      {registerOpen && (
        <Dialog
          title={t('runtime.registerDialogTitle')}
          testId="runtime-register-dialog"
          onClose={() => setRegisterOpen(false)}
        >
          <div className="form-field">
            <label>{t('runtime.fieldName')}</label>
            <input
              data-testid="runtime-register-field-name"
              value={form.name}
              placeholder={t('runtime.nameHint')}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>
          <div className="form-field">
            <label>{t('runtime.fieldEngine')}</label>
            <select
              data-testid="runtime-register-field-engine"
              value={form.engine}
              onChange={(e) => setForm({ ...form, engine: e.target.value })}
            >
              {ENGINES.map((v) => (
                <option key={v} value={v}>
                  {v}
                </option>
              ))}
            </select>
          </div>
          <div className="form-field">
            <label>{t('runtime.fieldReplicas')}</label>
            <input
              type="number"
              min={0}
              max={32}
              data-testid="runtime-register-field-replicas"
              value={form.replicas}
              onChange={(e) => setForm({ ...form, replicas: e.target.value })}
            />
          </div>
          <div className="form-field">
            <label>{t('runtime.fieldArtifact')}</label>
            <input
              data-testid="runtime-register-field-artifact"
              value={form.artifact}
              placeholder={t('runtime.artifactHint')}
              onChange={(e) => setForm({ ...form, artifact: e.target.value })}
            />
          </div>
          {formError && (
            <div className="error-banner" data-testid="runtime-register-error">
              {formError}
            </div>
          )}
          <div className="dialog-actions">
            <button
              className="secondary"
              data-testid="runtime-register-cancel"
              onClick={() => setRegisterOpen(false)}
            >
              {t('runtime.cancel')}
            </button>
            <button
              className="primary"
              data-testid="runtime-register-submit"
              onClick={() => void submitRegister()}
            >
              {t('runtime.register')}
            </button>
          </div>
        </Dialog>
      )}

      {deregisterTarget && (
        <Dialog
          title={t('runtime.deregisterTitle')}
          testId="runtime-deregister-confirm"
          onClose={() => setDeregisterTarget('')}
        >
          <p>{t('runtime.deregisterConfirm', { model: deregisterTarget })}</p>
          <div className="dialog-actions">
            <button
              className="secondary"
              data-testid="runtime-deregister-cancel"
              onClick={() => setDeregisterTarget('')}
            >
              {t('runtime.cancel')}
            </button>
            <button
              className="primary danger"
              data-testid="runtime-deregister-submit"
              onClick={() => void confirmDeregister()}
            >
              {t('runtime.deregister')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}
