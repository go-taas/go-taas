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

interface ListResponse {
  response: { code: number; message: string };
  services: InferenceServiceSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

export default function InferenceServicesPage() {
  const { orgId } = useOrg();
  const [services, setServices] = useState<InferenceServiceSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [scaleTarget, setScaleTarget] = useState<InferenceServiceSummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<InferenceServiceSummary | null>(null);
  const [autoscalingTarget, setAutoscalingTarget] = useState<InferenceServiceSummary | null>(null);

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
      setError(e instanceof Error ? e.message : 'failed to load services');
    } finally {
      setLoading(false);
    }
  }, [orgId, offset]);

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
          <h1>Inference Services</h1>
          <div className="subtitle">
            Deployed model endpoints. Terminated services are hidden.
          </div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">Loading…</div>
        ) : services.length === 0 ? (
          <div className="empty-state" data-testid="services-empty">
            No inference services. Deploy one from the Models page.
          </div>
        ) : (
          <table className="data" data-testid="services-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Model</th>
                <th>Replicas</th>
                <th>State</th>
                <th>Autoscaling</th>
                <th>Updated</th>
                <th>Actions</th>
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
                      Autoscaling
                    </button>{' '}
                    <button
                      className="link"
                      data-testid={`scale-${s.name}`}
                      onClick={() => setScaleTarget(s)}
                    >
                      Scale
                    </button>{' '}
                    <button
                      className="link danger"
                      data-testid={`delete-${s.name}`}
                      onClick={() => setDeleteTarget(s)}
                    >
                      Delete
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
  if (!service.autoscalingEnabled) {
    return <span className="badge fixed" data-testid={`autoscaling-badge-${service.name}`}>Off</span>;
  }
  const state = service.autoscalingState;
  if (state === 'cold-starting') {
    return (
      <span className="badge cold-starting" data-testid={`autoscaling-badge-${service.name}`}>
        Cold-starting
      </span>
    );
  }
  if (state === 'error') {
    return <span className="badge error" data-testid={`autoscaling-badge-${service.name}`}>Error</span>;
  }
  return (
    <span className="badge autoscaled" data-testid={`autoscaling-badge-${service.name}`}>
      On · {service.currentReplicas}/{service.minReplicas}–{service.maxReplicas}
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

  const validate = (p: AutoscalingPolicy): Record<string, string> => {
    const errs: Record<string, string> = {};
    if (p.minReplicas > p.maxReplicas) errs.maxReplicas = 'Max replicas must be ≥ min replicas and ≤ 100.';
    if (p.minReplicas === 0 && !p.scaleToZero) errs.minReplicas = 'Min replicas must be 0 to enable scale-to-zero.';
    if (p.scaleToZero && p.minReplicas !== 0) errs.scaleToZero = 'Set min replicas to 0 to enable scale-to-zero.';
    if (p.targetConcurrency < 1 || p.targetConcurrency > 1000) errs.targetConcurrency = 'Target concurrency must be between 1 and 1000.';
    if (p.cooldownSeconds < 0 || p.cooldownSeconds > 3600) errs.cooldownSeconds = 'Cooldown must be between 0 and 3600 seconds.';
    if (p.maxReplicas < 1 || p.maxReplicas > 100) errs.maxReplicas = 'Max replicas must be ≥ min replicas and ≤ 100.';
    return errs;
  };

  const submit = async () => {
    const errs = validate(policy);
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
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to update autoscaling');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={`Autoscaling · ${service.name}`} onClose={onClose} testId="autoscaling-dialog">
      {!policy.enabled && (
        <div className="warning-banner" data-testid="autoscaling-disable-warning">
          Autoscaling will be disabled and the service will run at a fixed
          replica count equal to its current count.
        </div>
      )}
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="asd-enabled">Enabled</label>
          <input
            id="asd-enabled"
            data-testid="asd-enabled"
            type="checkbox"
            checked={policy.enabled}
            onChange={(e) => set({ enabled: e.target.checked })}
          />
        </div>
        <div className="form-field">
          <label htmlFor="asd-min">Min replicas</label>
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
          <label htmlFor="asd-max">Max replicas</label>
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
          <label htmlFor="asd-target">Target concurrency</label>
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
          <label htmlFor="asd-scale-to-zero">Scale to zero</label>
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
          <label htmlFor="asd-cooldown">Cooldown seconds</label>
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
          Cancel
        </button>
        <button data-testid="submit-autoscaling" disabled={submitting} onClick={() => void submit()}>
          {submitting ? 'Saving…' : 'Save'}
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
  const [replicas, setReplicas] = useState(String(service.replicas));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    const r = parseInt(replicas, 10);
    if (!Number.isInteger(r) || r < 1 || r > 100) {
      setError('Replicas must be an integer between 1 and 100.');
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
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to scale');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={`Scale ${service.name}`} onClose={onClose} testId="scale-dialog">
      <p>
        Current replicas: <strong>{service.replicas}</strong>
      </p>
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="scale-replicas">New replicas (1-100)</label>
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
          Cancel
        </button>
        <button
          data-testid="submit-scale"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? 'Scaling…' : 'Scale'}
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
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const del = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.del(`/api/v1/admin/inference-services/${service.serviceId}`, orgId);
      onDeleted();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to delete');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={`Delete ${service.name}?`} onClose={onClose} testId="delete-dialog">
      <p>
        The Kubernetes Deployment and Service are removed. This action is
        idempotent but the service cannot be restarted — deploy again from the
        Models page if needed.
      </p>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          className="danger"
          data-testid="confirm-delete"
          disabled={submitting}
          onClick={() => void del()}
        >
          {submitting ? 'Deleting…' : 'Delete'}
        </button>
      </div>
    </Dialog>
  );
}
