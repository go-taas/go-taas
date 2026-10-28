// Inference Services page: list with state badges, scale and delete.
// Implements docs/design/model-catalog-deployment.md FR4, FR5, AC8-AC11.

import { useCallback, useEffect, useState } from 'react';
import { navigate } from '../router';
import {
  api,
  ApiError,
  formatTime,
  type AutoscalingPolicy,
  type InferenceServiceSummary,
  type PageMeta,
} from '../api';
import { useOrg } from '../org';
import {
  Dialog,
  ErrorBanner,
  Pagination,
  StateBadge,
  usePolling,
} from '../components';
import { useI18n } from '../i18n';
import ServiceStatusBanner from '../components/ServiceStatusBanner';
import DeployForm from '../components/DeployForm';

interface ListResponse {
  response: { code: number; message: string };
  services: InferenceServiceSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

export default function InferenceServicesPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [services, setServices] = useState<InferenceServiceSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [scaleTarget, setScaleTarget] = useState<InferenceServiceSummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<InferenceServiceSummary | null>(null);
  const [autoscalingTarget, setAutoscalingTarget] = useState<InferenceServiceSummary | null>(null);
  // The create form is an inline panel, not a dialog: it expands above the
  // list and collapses again once the service exists.
  const [createOpen, setCreateOpen] = useState(false);
  const [createModel, setCreateModel] = useState({ id: '', version: '', name: '' });

  // A link from the model catalog asks for the form to be open with a model
  // preselected, so deploying starts where the operator clicked.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (params.get('create') !== '1') return;
    setCreateModel({
      id: params.get('modelId') || '',
      version: params.get('version') || '',
      name: '',
    });
    setCreateOpen(true);
    // The query is consumed: a reload should not reopen the form.
    window.history.replaceState({}, '', window.location.pathname);
  }, []);

  const load = useCallback(async () => {
    setError('');
    try {
      const data = await api.get<ListResponse>(
        `/api/v1/admin/inference-services?page.offset=${offset}&page.limit=${PAGE_SIZE}`,
        orgId,
      );
      setServices(data.services || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('services.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // Services transition pending → deploying → running; refresh the list
  // periodically so badges stay current without manual reload.
  usePolling(() => void load(), 5000, !loading);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('services.title')}</h1>
          <div className="subtitle">
            {t('services.subtitle')}
          </div>
        </div>
        <button
          data-testid="create-service"
          onClick={() => {
            setCreateModel({ id: '', version: '', name: '' });
            setCreateOpen(true);
          }}
        >
          {t('services.create')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      {createOpen && (
        <DeployForm
          orgId={orgId}
          modelId={createModel.id}
          modelName={createModel.name}
          initialVersion={createModel.version}
          onCancel={() => setCreateOpen(false)}
          onDeployed={() => {
            // Collapse the form: the list is the page once the service
            // exists.
            setCreateOpen(false);
            void load();
          }}
        />
      )}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : services.length === 0 ? (
          <div className="empty-state" data-testid="services-empty">
            {t('services.empty')}
          </div>
        ) : (
          <table className="data" data-testid="services-table">
            <thead>
              <tr>
                <th>{t('services.colName')}</th>
                <th>{t('services.colAccess')}</th>
                <th>{t('services.colModel')}</th>
                <th>{t('services.colReplicas')}</th>
                <th>{t('services.colState')}</th>
                <th>{t('services.colAutoscaling')}</th>
                <th>{t('services.colUpdated')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {services.map((s) => (
                <tr
                  key={s.serviceId}
                  data-testid={`service-row-${s.name}`}
                  style={{ cursor: 'pointer' }}
                  onClick={() => navigate(`/admin/inference-services/${s.serviceId}`)}
                >
                  <td>
                    <strong>{s.name}</strong>
                    <ServiceStatusBanner serviceId={s.serviceId} />
                  </td>
                  <td data-testid={`service-access-${s.name}`}>
                    {s.accessRestricted ? (
                      <span className="badge">{t('services.accessRestricted')}</span>
                    ) : (
                      <span className="muted">{t('services.accessEveryone')}</span>
                    )}
                  </td>
                  <td className="mono muted">
                    {s.modelId.slice(0, 8)}… @ {s.modelVersion}
                  </td>
                  <td>{s.replicas}</td>
                  <td>
                    <StateBadge state={s.state} />
                  </td>
                  <td>
                    <AutoscalingBadge service={s} />
                  </td>
                  <td>{formatTime(s.updatedAt)}</td>
                  <td onClick={(e) => e.stopPropagation()}>
                    <button
                      className="link"
                      data-testid={`autoscaling-${s.name}`}
                      onClick={() => setAutoscalingTarget(s)}
                    >
                      {t('services.autoscaling')}
                    </button>{' '}
                    <button
                      className="link"
                      data-testid={`scale-${s.name}`}
                      onClick={() => setScaleTarget(s)}
                    >
                      {t('services.scale')}
                    </button>{' '}
                    <button
                      className="link danger"
                      data-testid={`delete-${s.name}`}
                      onClick={() => setDeleteTarget(s)}
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

      {scaleTarget && (
        <ScaleDialog
          service={scaleTarget}
          orgId={orgId}
          onClose={() => setScaleTarget(null)}
          onScaled={() => {
            setScaleTarget(null);
            void load();
          }}
        />
      )}

      {deleteTarget && (
        <DeleteDialog
          service={deleteTarget}
          orgId={orgId}
          onClose={() => setDeleteTarget(null)}
          onDeleted={() => {
            setDeleteTarget(null);
            void load();
          }}
        />
      )}

      {autoscalingTarget && (
        <AutoscalingDialog
          service={autoscalingTarget}
          orgId={orgId}
          onClose={() => setAutoscalingTarget(null)}
          onSaved={() => {
            setAutoscalingTarget(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

// AutoscalingBadge renders the autoscaling column badge (feature #16,
// FR3.1): On/Off with current/min-max.
function AutoscalingBadge({ service }: { service: InferenceServiceSummary }) {
  const { t } = useI18n();
  if (!service.autoscalingEnabled) {
    return <span className="badge fixed" data-testid={`autoscaling-badge-${service.name}`}>{t('common.off')}</span>;
  }
  const state = service.autoscalingState;
  if (state === 'cold-starting') {
    return (
      <span className="badge cold-starting" data-testid={`autoscaling-badge-${service.name}`}>
        {t('service.asColdStarting')}
      </span>
    );
  }
  if (state === 'error') {
    return <span className="badge error" data-testid={`autoscaling-badge-${service.name}`}>{t('service.asError')}</span>;
  }
  return (
    <span className="badge autoscaled" data-testid={`autoscaling-badge-${service.name}`}>
      {t('services.asBadge', {
        cur: service.currentReplicas ?? 0,
        min: service.minReplicas ?? 0,
        max: service.maxReplicas ?? 0,
      })}
    </span>
  );
}

// AutoscalingDialog edits a service's per-service autoscaling policy
// (feature #16, FR2.2-FR2.5).
function AutoscalingDialog({
  service,
  orgId,
  onClose,
  onSaved,
}: {
  service: InferenceServiceSummary;
  orgId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [policy, setPolicy] = useState<AutoscalingPolicy>({
    enabled: service.autoscalingEnabled ?? false,
    minReplicas: service.minReplicas ?? 1,
    maxReplicas: service.maxReplicas ?? 10,
    targetConcurrency: 32,
    scaleToZero: false,
    cooldownSeconds: 300,
  });
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

  const set = (patch: Partial<AutoscalingPolicy>) => {
    setPolicy((p) => ({ ...p, ...patch }));
    setFieldErrors({});
  };

  const validate = (
    p: AutoscalingPolicy,
    t: (key: string, vars?: Record<string, string | number>) => string,
  ): Record<string, string> => {
    const errs: Record<string, string> = {};
    if (p.minReplicas > p.maxReplicas) errs.maxReplicas = t('services.validationMax');
    if (p.minReplicas === 0 && !p.scaleToZero) errs.minReplicas = t('services.validationMinZero');
    if (p.scaleToZero && p.minReplicas !== 0) errs.scaleToZero = t('services.validationSetMinZero');
    if (p.targetConcurrency < 1 || p.targetConcurrency > 1000) errs.targetConcurrency = t('services.validationConcurrency');
    if (p.cooldownSeconds < 0 || p.cooldownSeconds > 3600) errs.cooldownSeconds = t('services.validationCooldown');
    if (p.maxReplicas < 1 || p.maxReplicas > 100) errs.maxReplicas = t('services.validationMax');
    return errs;
  };

  const submit = async () => {
    const errs = validate(policy, t);
    setFieldErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    setError('');
    try {
      await api.post(
        `/api/v1/admin/inference-services/${service.serviceId}:autoscaling`,
        orgId,
        { policy },
      );
      onSaved();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('services.asUpdateFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('services.asTitle', { name: service.name })} onClose={onClose} testId="autoscaling-dialog">
      {!policy.enabled && (
        <div className="warning-banner" data-testid="autoscaling-disable-warning">
          {t('services.asWarning')}
        </div>
      )}
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="asd-enabled">{t('services.fieldEnabled')}</label>
          <input
            id="asd-enabled"
            data-testid="asd-enabled"
            type="checkbox"
            checked={policy.enabled}
            onChange={(e) => set({ enabled: e.target.checked })}
          />
        </div>
        <div className="form-field">
          <label htmlFor="asd-min">{t('services.fieldMinReplicas')}</label>
          <input
            id="asd-min"
            data-testid="asd-min"
            type="number"
            min={0}
            max={100}
            disabled={!policy.enabled}
            value={policy.minReplicas}
            onChange={(e) => set({ minReplicas: parseInt(e.target.value, 10) || 0 })}
          />
          {fieldErrors.minReplicas && <div className="field-error">{fieldErrors.minReplicas}</div>}
        </div>
        <div className="form-field">
          <label htmlFor="asd-max">{t('services.fieldMaxReplicas')}</label>
          <input
            id="asd-max"
            data-testid="asd-max"
            type="number"
            min={1}
            max={100}
            disabled={!policy.enabled}
            value={policy.maxReplicas}
            onChange={(e) => set({ maxReplicas: parseInt(e.target.value, 10) || 0 })}
          />
          {fieldErrors.maxReplicas && <div className="field-error">{fieldErrors.maxReplicas}</div>}
        </div>
        <div className="form-field">
          <label htmlFor="asd-target">{t('services.fieldTargetConcurrency')}</label>
          <input
            id="asd-target"
            data-testid="asd-target"
            type="number"
            min={1}
            max={1000}
            disabled={!policy.enabled}
            value={policy.targetConcurrency}
            onChange={(e) => set({ targetConcurrency: parseInt(e.target.value, 10) || 0 })}
          />
          {fieldErrors.targetConcurrency && <div className="field-error">{fieldErrors.targetConcurrency}</div>}
        </div>
        <div className="form-field">
          <label htmlFor="asd-scale-to-zero">{t('services.fieldScaleToZero')}</label>
          <input
            id="asd-scale-to-zero"
            data-testid="asd-scale-to-zero"
            type="checkbox"
            disabled={!policy.enabled || policy.minReplicas !== 0}
            checked={policy.scaleToZero}
            onChange={(e) => set({ scaleToZero: e.target.checked })}
          />
          {fieldErrors.scaleToZero && <div className="field-error">{fieldErrors.scaleToZero}</div>}
        </div>
        <div className="form-field">
          <label htmlFor="asd-cooldown">{t('services.fieldCooldown')}</label>
          <input
            id="asd-cooldown"
            data-testid="asd-cooldown"
            type="number"
            min={0}
            max={3600}
            disabled={!policy.enabled}
            value={policy.cooldownSeconds}
            onChange={(e) => set({ cooldownSeconds: parseInt(e.target.value, 10) || 0 })}
          />
          {fieldErrors.cooldownSeconds && <div className="field-error">{fieldErrors.cooldownSeconds}</div>}
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="submit-autoscaling" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}

function ScaleDialog({
  service,
  orgId,
  onClose,
  onScaled,
}: {
  service: InferenceServiceSummary;
  orgId: string;
  onClose: () => void;
  onScaled: () => void;
}) {
  const { t } = useI18n();
  const [replicas, setReplicas] = useState(String(service.replicas));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    const r = parseInt(replicas, 10);
    if (!Number.isInteger(r) || r < 1 || r > 100) {
      setError(t('services.validationReplicas'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.post(
        `/api/v1/admin/inference-services/${service.serviceId}:scale`,
        orgId,
        { replicas: String(r) },
      );
      onScaled();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('services.scaleFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('services.scaleTitle', { name: service.name })} onClose={onClose} testId="scale-dialog">
      <p
        dangerouslySetInnerHTML={{
          __html: t('services.scaleBody', { n: service.replicas }),
        }}
      />
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="scale-replicas">{t('services.fieldNewReplicas')}</label>
          <input
            id="scale-replicas"
            data-testid="scale-replicas-input"
            type="number"
            min={1}
            max={100}
            value={replicas}
            onChange={(e) => setReplicas(e.target.value)}
          />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="submit-scale"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? t('common.working') : t('services.scale')}
        </button>
      </div>
    </Dialog>
  );
}

function DeleteDialog({
  service,
  orgId,
  onClose,
  onDeleted,
}: {
  service: InferenceServiceSummary;
  orgId: string;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const { t } = useI18n();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const del = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.del(`/api/v1/admin/inference-services/${service.serviceId}`, orgId);
      onDeleted();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('services.deleteFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('services.deleteTitle', { name: service.name })} onClose={onClose} testId="delete-dialog">
      <p>
        {t('services.deleteBody')}
      </p>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          className="danger"
          data-testid="confirm-delete"
          disabled={submitting}
          onClick={() => void del()}
        >
          {submitting ? t('common.deleting') : t('common.delete')}
        </button>
      </div>
    </Dialog>
  );
}
