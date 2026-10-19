// VGPUPoolsPage: the admin vGPU slicing-pool list (feature #52, design
// §5.1). Pool table with selector, policy, templates, nodes, allocation
// and utilization; a filter bar (name search, policy filter,
// pagination); and a create dialog with the SliceTemplateEditor.

import { useCallback, useEffect, useState } from 'react';
import { Dialog, ErrorBanner, Pagination } from '../components';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { navigate } from '../router';
import {
  type ListVGPUPoolsResponse,
  type VGPUPool,
} from '../api';
import AllocationBar from './vgpu/AllocationBar';
import SliceTemplateEditor, {
  rowsToTemplates,
  type TemplateRow,
} from './vgpu/SliceTemplateEditor';

const PAGE_SIZE = 20;
const POLICIES = ['binpack', 'spread', 'mutex'];

export default function VGPUPoolsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [pools, setPools] = useState<VGPUPool[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [name, setName] = useState('');
  const [policy, setPolicy] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [showCreate, setShowCreate] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams();
      params.set('page.offset', String(offset));
      params.set('page.limit', String(PAGE_SIZE));
      if (name) params.set('name', name);
      if (policy) params.set('placement_policy', policy);
      const data = await api.get<ListVGPUPoolsResponse>(
        `/api/v1/admin/vgpu-pools?${params.toString()}`,
        orgId,
      );
      setPools(data.pools || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e: any) {
      if (e?.body?.code === 10036) setPermissionDenied(true);
      else setError(e?.message || t('vgpu.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, offset, name, policy, t]);

  useEffect(() => {
    void load();
  }, [load]);

  if (permissionDenied) {
    return (
      <div>
        <div className="page-header">
          <h1>{t('vgpu.title')}</h1>
        </div>
        <div className="empty-state" data-testid="vgpu-pool-permission-denied">
          {t('vgpu.permissionDenied')}
        </div>
      </div>
    );
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('vgpu.title')}</h1>
          <div className="subtitle">{t('vgpu.subtitle')}</div>
        </div>
        <button
          className="primary"
          data-testid="vgpu-pool-create"
          onClick={() => setShowCreate(true)}
        >
          {t('vgpu.create')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="filter-bar">
        <input
          data-testid="vgpu-pool-search"
          placeholder={t('vgpu.search')}
          value={name}
          onChange={(e) => {
            setName(e.target.value);
            setOffset(0);
          }}
        />
        <select
          data-testid="vgpu-pool-filter-policy"
          value={policy}
          onChange={(e) => {
            setPolicy(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('vgpu.allPolicies')}</option>
          {POLICIES.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
      </div>

      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : pools.length === 0 ? (
        <div className="empty-state" data-testid="vgpu-pool-empty">
          {t('vgpu.empty')}
        </div>
      ) : (
        <table className="data-table" data-testid="vgpu-pool-table">
          <thead>
            <tr>
              <th>{t('vgpu.name')}</th>
              <th>{t('vgpu.selector')}</th>
              <th>{t('vgpu.policy')}</th>
              <th>{t('vgpu.templates')}</th>
              <th>{t('vgpu.nodes')}</th>
              <th>{t('vgpu.allocation')}</th>
              <th>{t('vgpu.utilization')}</th>
            </tr>
          </thead>
          <tbody>
            {pools.map((p) => (
              <tr
                key={p.poolId}
                data-testid={`vgpu-pool-row-${p.poolId}`}
                onClick={() => navigate(`/admin/vgpu-pools/${p.poolId}`)}
                style={{ cursor: 'pointer' }}
              >
                <td data-testid={`vgpu-pool-name-${p.poolId}`}>{p.name}</td>
                <td className="mono" data-testid={`vgpu-pool-selector-${p.poolId}`}>
                  {p.labelKey}={p.labelValue}
                </td>
                <td data-testid={`vgpu-pool-policy-${p.poolId}`}>{p.placementPolicy}</td>
                <td data-testid={`vgpu-pool-templates-${p.poolId}`}>
                  {(p.templates || []).map((tpl) => tpl.name).join(', ')}
                </td>
                <td data-testid={`vgpu-pool-nodes-${p.poolId}`}>{p.nodeCount}</td>
                <td data-testid={`vgpu-pool-allocation-${p.poolId}`}>
                  {(p.allocation || [])
                    .map((a) => `${a.templateName} ${a.allocated}/${a.free}`)
                    .join(' · ')}
                </td>
                <td data-testid={`vgpu-pool-utilization-${p.poolId}`}>
                  <AllocationBar pct={parseInt(p.utilizationPct || '0', 10) || 0} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <Pagination
        total={total}
        offset={offset}
        limit={PAGE_SIZE}
        onPageChange={setOffset}
      />

      {showCreate && (
        <CreatePoolDialog
          onClose={() => setShowCreate(false)}
          onCreated={(id) => navigate(`/admin/vgpu-pools/${id}`)}
        />
      )}
    </div>
  );
}

function CreatePoolDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (poolId: string) => void;
}) {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [labelKey, setLabelKey] = useState('go-taas.io/vgpu-pool');
  const [labelValue, setLabelValue] = useState('');
  const [policy, setPolicy] = useState('binpack');
  const [rows, setRows] = useState<TemplateRow[]>([
    { name: 'small', memoryGib: '8', computePct: '25', description: '' },
  ]);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});

  const submit = async () => {
    setError('');
    setFieldErrors({});
    if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name.trim())) {
      setFieldErrors({ name: t('vgpu.nameInvalid') });
      return;
    }
    if (!labelKey.trim() || !labelValue.trim()) {
      setFieldErrors({ label: t('vgpu.labelRequired') });
      return;
    }
    const templates = rowsToTemplates(rows);
    if (templates.length === 0) {
      setFieldErrors({ templates: t('vgpu.templatesRequired') });
      return;
    }
    setSubmitting(true);
    try {
      const res = await api.post<{ response: { code: number; message: string }; pool: VGPUPool }>(
        '/api/v1/admin/vgpu-pools',
        orgId,
        {
          name: name.trim(),
          labelKey: labelKey.trim(),
          labelValue: labelValue.trim(),
          placementPolicy: policy,
          templates,
        },
      );
      onCreated(res.pool.poolId);
    } catch (e: any) {
      if (e?.body?.code === 13401 || e?.body?.code === 13402) {
        setError(e?.body?.message || t('vgpu.createFailed'));
      } else {
        setError(e?.message || t('vgpu.createFailed'));
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('vgpu.create')} onClose={onClose} testId="vgpu-pool-create-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="vgpu-pool-name">Name</label>
          <input
            id="vgpu-pool-name"
            data-testid="vgpu-pool-name-input"
            value={name}
            maxLength={63}
            onChange={(e) => setName(e.target.value)}
          />
          {fieldErrors.name && <div className="field-error">{fieldErrors.name}</div>}
        </div>
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
          {fieldErrors.label && <div className="field-error">{fieldErrors.label}</div>}
        </div>
        <SliceTemplateEditor rows={rows} onChange={setRows} />
        {fieldErrors.templates && <div className="field-error">{fieldErrors.templates}</div>}
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