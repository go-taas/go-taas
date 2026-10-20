// VGPUPoolDetailPage: the admin vGPU pool detail (feature #52, design
// §5.2). Node membership, per-GPU slice allocation, services using
// slices, an edit dialog, and the delete flow (blocked when in use).

import { useCallback, useEffect, useState } from 'react';
import { BackLink, Dialog, ErrorBanner, StateBadge } from '../components';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { navigate } from '../router';
import {
  type GetVGPUPoolResponse,
  type PoolNode,
  type VGPUPool,
} from '../api';
import AllocationBar from './vgpu/AllocationBar';
import SliceTemplateEditor, {
  rowsToTemplates,
  templatesToRows,
  type TemplateRow,
} from './vgpu/SliceTemplateEditor';

const POLICIES = ['binpack', 'spread', 'mutex'];

export default function VGPUPoolDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const id = window.location.pathname.split('/').pop() || '';
  const [data, setData] = useState<GetVGPUPoolResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [showEdit, setShowEdit] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [deleteBlocked, setDeleteBlocked] = useState('');

  const load = useCallback(async () => {
    setError('');
    try {
      const res = await api.get<GetVGPUPoolResponse>(
        `/api/v1/admin/vgpu-pools/${id}`,
        orgId,
      );
      setData(res);
    } catch (e: any) {
      if (e?.code === 10036) setPermissionDenied(true);
      else setError(e?.message || t('vgpu.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, id, t]);

  useEffect(() => {
    void load();
  }, [load]);

  if (permissionDenied) {
    return (
      <div>
        <BackLink to="/admin/vgpu-pools" label={t('vgpu.back')} />
        <div className="empty-state" data-testid="vgpu-pool-permission-denied">
          {t('vgpu.permissionDenied')}
        </div>
      </div>
    );
  }
  if (loading) return <div className="loading">{t('common.loading')}</div>;
  if (error)
    return (
      <div>
        <BackLink to="/admin/vgpu-pools" label={t('vgpu.back')} />
        <ErrorBanner message={error} />
      </div>
    );
  if (!data) return null;

  const pool = data.pool;
  const nodes = data.nodes || [];
  const services = data.services || [];

  const doDelete = async () => {
    setDeleteBlocked('');
    try {
      await api.del<{ response: { code: number; message: string } }>(
        `/api/v1/admin/vgpu-pools/${id}`,
        orgId,
      );
      navigate('/admin/vgpu-pools');
    } catch (e: any) {
      if (e?.code === 13405) {
        setDeleteBlocked(e?.message || t('vgpu.deleteBlocked'));
      } else {
        setError(e?.message || t('vgpu.deleteFailed'));
      }
    }
  };

  return (
    <div>
      <BackLink to="/admin/vgpu-pools" label={t('vgpu.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="vgpu-pool-detail-name">{pool.name}</h1>
          <div className="subtitle mono">{pool.poolId}</div>
        </div>
        <button
          className="secondary"
          data-testid={`vgpu-pool-edit-${pool.poolId}`}
          onClick={() => setShowEdit(true)}
        >
          {t('vgpu.edit')}
        </button>
        <button
          className="danger"
          data-testid={`vgpu-pool-delete-${pool.poolId}`}
          onClick={() => setShowDelete(true)}
        >
          {t('vgpu.delete')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('vgpu.overview')}</h3>
        <div className="detail-grid">
          <div className="detail-item">
            <div className="label">{t('vgpu.selector')}</div>
            <div className="value mono">
              {pool.labelKey}={pool.labelValue}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">{t('vgpu.policy')}</div>
            <div className="value">{pool.placementPolicy}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('vgpu.nodes')}</div>
            <div className="value">{pool.nodeCount}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('vgpu.utilization')}</div>
            <div className="value">
              <AllocationBar pct={parseInt(pool.utilizationPct || '0', 10) || 0} />
            </div>
          </div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('vgpu.nodeMembership')}</h3>
        {nodes.length === 0 ? (
          <div className="empty-state" data-testid="vgpu-pool-nodes-empty">
            {t('vgpu.nodesEmpty')}
          </div>
        ) : (
          <table className="data-table" data-testid="vgpu-pool-node-table">
            <thead>
              <tr>
                <th>{t('vgpu.node')}</th>
                <th>{t('vgpu.health')}</th>
                <th>{t('vgpu.gpus')}</th>
              </tr>
            </thead>
            <tbody>
              {nodes.map((n) => (
                <NodeRow key={n.nodeId} node={n} />
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('vgpu.services')}</h3>
        {services.length === 0 ? (
          <div className="empty-state" data-testid="vgpu-pool-services-empty">
            {t('vgpu.servicesEmpty')}
          </div>
        ) : (
          <table className="data-table" data-testid="vgpu-pool-service-table">
            <thead>
              <tr>
                <th>{t('vgpu.service')}</th>
                <th>{t('vgpu.template')}</th>
                <th>{t('vgpu.gpu')}</th>
                <th>{t('vgpu.state')}</th>
              </tr>
            </thead>
            <tbody>
              {services.map((s) => (
                <tr key={s.serviceId} data-testid={`vgpu-pool-service-row-${s.serviceId}`}>
                  <td>{s.name}</td>
                  <td>{s.templateName}</td>
                  <td>
                    {s.nodeId ? `${s.nodeId} · GPU ${s.gpuIndex}` : '—'}
                  </td>
                  <td>
                    <StateBadge state={s.state} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {showEdit && (
        <EditPoolDialog
          pool={pool}
          onClose={() => setShowEdit(false)}
          onSaved={() => {
            setShowEdit(false);
            void load();
          }}
        />
      )}

      {showDelete && (
        <Dialog
          title={t('vgpu.delete')}
          onClose={() => setShowDelete(false)}
          testId="vgpu-pool-delete-dialog"
        >
          <p>{t('vgpu.deleteConfirm')}</p>
          {deleteBlocked && (
            <div className="error-banner" data-testid={`vgpu-pool-delete-blocked-${pool.poolId}`}>
              {deleteBlocked}
            </div>
          )}
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setShowDelete(false)}>
              {t('common.cancel')}
            </button>
            <button className="danger" data-testid="vgpu-pool-delete-confirm" onClick={doDelete}>
              {t('vgpu.delete')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

function NodeRow({ node }: { node: PoolNode }) {
  return (
    <tr data-testid={`vgpu-pool-node-row-${node.nodeId}`}>
      <td>{node.name}</td>
      <td>
        <StateBadge state={node.health} />
      </td>
      <td>
        {(node.gpus || []).map((g) => (
          <div key={g.index} data-testid={`vgpu-pool-gpu-row-${node.nodeId}-${g.index}`}>
            GPU {g.index} ({g.model})
            {g.full ? ' · full' : ''}
            {(g.slices || []).map((s) => (
              <div key={s.serviceId} className="muted">
                {s.templateName} · {s.memoryGib} GiB / {s.computePct}% · {s.serviceId.slice(0, 8)}
              </div>
            ))}
          </div>
        ))}
      </td>
    </tr>
  );
}

function EditPoolDialog({
  pool,
  onClose,
  onSaved,
}: {
  pool: VGPUPool;
  onClose: () => void;
  onSaved: () => void;
}) {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [policy, setPolicy] = useState(pool.placementPolicy);
  const [labelKey, setLabelKey] = useState(pool.labelKey);
  const [labelValue, setLabelValue] = useState(pool.labelValue);
  const [rows, setRows] = useState<TemplateRow[]>(templatesToRows(pool.templates));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    setError('');
    setSubmitting(true);
    try {
      await api.patch<{ response: { code: number; message: string }; pool: VGPUPool }>(
        `/api/v1/admin/vgpu-pools/${pool.poolId}`,
        orgId,
        {
          placementPolicy: policy,
          labelKey,
          labelValue,
          templates: rowsToTemplates(rows),
        },
      );
      onSaved();
    } catch (e: any) {
      setError(e?.message || t('vgpu.updateFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('vgpu.edit')} onClose={onClose} testId="vgpu-pool-edit-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="vgpu-pool-policy">Placement policy</label>
          <select
            id="vgpu-pool-policy"
            data-testid="vgpu-pool-policy-select"
            value={policy}
            onChange={(e) => setPolicy(e.target.value)}
          >
            {POLICIES.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="vgpu-pool-label-key">Node label key</label>
          <input
            id="vgpu-pool-label-key"
            data-testid="vgpu-pool-label-key-input"
            value={labelKey}
            onChange={(e) => setLabelKey(e.target.value)}
          />
        </div>
        <div className="form-field">
          <label htmlFor="vgpu-pool-label-value">Node label value</label>
          <input
            id="vgpu-pool-label-value"
            data-testid="vgpu-pool-label-value-input"
            value={labelValue}
            onChange={(e) => setLabelValue(e.target.value)}
          />
        </div>
        <SliceTemplateEditor rows={rows} onChange={setRows} />
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          className="primary"
          data-testid="vgpu-pool-save"
          disabled={submitting}
          onClick={submit}
        >
          {t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}