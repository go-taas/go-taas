// Inference Services page: list with state badges, scale and delete.
// Implements docs/design/model-catalog-deployment.md FR4, FR5, AC8-AC11.

import { useCallback, useEffect, useState } from 'react';
import { navigate } from '../router';
import {
  api,
  ApiError,
  formatTime,
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
                  <td>{formatTime(s.updatedAt)}</td>
                  <td onClick={(e) => e.stopPropagation()}>
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
    </div>
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
