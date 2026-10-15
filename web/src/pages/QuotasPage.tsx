// QuotasPage is the admin console service-quota page (feature #51,
// design §5.3): the org table with live usage columns, a filter bar
// (name search, limit state, pagination), an edit dialog with tier
// presets and inline validation, and a change-history drawer.

import { useCallback, useEffect, useState } from 'react';
import { Dialog, ErrorBanner, Pagination } from '../components';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { formatTime, type GetOrganizationQuotaResponse, type ListOrganizationQuotasResponse, type OrganizationQuotaRow } from '../api';

const PAGE_SIZE = 20;

// The tier presets (feature #51, AD17): UI constants, not a data model.
const PRESETS: Record<string, { label: string; values: Record<string, string> }> = {
  default: { label: 'quota.preset.default', values: { maxConcurrentRequests: '0', maxInferenceServices: '0', maxBatchJobsPerDay: '0', maxOutputTokensPerRequest: '0' } },
  starter: { label: 'quota.preset.starter', values: { maxConcurrentRequests: '10', maxInferenceServices: '3', maxBatchJobsPerDay: '5', maxOutputTokensPerRequest: '4096' } },
  growth: { label: 'quota.preset.growth', values: { maxConcurrentRequests: '50', maxInferenceServices: '20', maxBatchJobsPerDay: '50', maxOutputTokensPerRequest: '8192' } },
  enterprise: { label: 'quota.preset.enterprise', values: { maxConcurrentRequests: '200', maxInferenceServices: '100', maxBatchJobsPerDay: '500', maxOutputTokensPerRequest: '16384' } },
};

const DIMENSIONS = ['maxConcurrentRequests', 'maxInferenceServices', 'maxBatchJobsPerDay', 'maxOutputTokensPerRequest'] as const;

export default function QuotasPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [rows, setRows] = useState<OrganizationQuotaRow[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [name, setName] = useState('');
  const [limitState, setLimitState] = useState('all');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [editTarget, setEditTarget] = useState<OrganizationQuotaRow | null>(null);
  const [historyTarget, setHistoryTarget] = useState<OrganizationQuotaRow | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams();
      params.set('page.offset', String(offset));
      params.set('page.limit', String(PAGE_SIZE));
      if (name) params.set('name', name);
      if (limitState && limitState !== 'all') params.set('limit_state', limitState);
      const data = await api.get<ListOrganizationQuotasResponse>(`/api/v1/admin/quotas?${params.toString()}`, orgId);
      setRows(data.quotas || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e: any) {
      if (e?.body?.code === 10036) setPermissionDenied(true);
      else setError(e?.message || t('quota.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, offset, name, limitState, t]);

  useEffect(() => {
    void load();
  }, [load]);

  if (permissionDenied) {
    return (
      <div>
        <div className="page-header">
          <h1>{t('quota.title')}</h1>
        </div>
        <div className="empty-state" data-testid="quota-permission-denied">
          {t('quota.permissionDenied')}
        </div>
      </div>
    );
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('quota.title')}</h1>
          <div className="subtitle">{t('quota.subtitle')}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="filter-bar">
        <input
          data-testid="quota-search"
          placeholder={t('quota.search')}
          value={name}
          onChange={(e) => {
            setName(e.target.value);
            setOffset(0);
          }}
        />
        <select
          data-testid="quota-filter-limit-state"
          value={limitState}
          onChange={(e) => {
            setLimitState(e.target.value);
            setOffset(0);
          }}
        >
          <option value="all">{t('quota.filterAll')}</option>
          <option value="limited">{t('quota.filterLimited')}</option>
          <option value="unlimited">{t('quota.filterUnlimited')}</option>
        </select>
      </div>

      {loading && rows.length === 0 ? (
        <div className="loading">{t('common.loading')}</div>
      ) : rows.length === 0 ? (
        <div className="empty-state" data-testid="quota-empty">
          {t('quota.empty')}
        </div>
      ) : (
        <table className="data" data-testid="quota-table">
          <thead>
            <tr>
              <th>{t('quota.colOrg')}</th>
              <th>{t('quota.colConcurrency')}</th>
              <th>{t('quota.colServices')}</th>
              <th>{t('quota.colBatch')}</th>
              <th>{t('quota.colTokenCap')}</th>
              <th>{t('quota.colUpdated')}</th>
              <th>{t('quota.colUpdatedBy')}</th>
              <th>{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const q = row.quota;
              const u = row.usage;
              return (
                <tr key={q.organizationId} data-testid={`quota-row-${q.organizationId}`}>
                  <td>
                    <div>{q.organizationName}</div>
                    <div className="muted mono">{q.organizationId}</div>
                  </td>
                  <td data-testid={`quota-usage-concurrency-${q.organizationId}`}>
                    {usageOrUnlimited(u.concurrentRequests, q.maxConcurrentRequests)}
                  </td>
                  <td data-testid={`quota-usage-services-${q.organizationId}`}>
                    {usageOrUnlimited(u.inferenceServices, q.maxInferenceServices)}
                  </td>
                  <td data-testid={`quota-usage-batch-${q.organizationId}`}>
                    {usageOrUnlimited(u.batchJobsToday, q.maxBatchJobsPerDay)}
                  </td>
                  <td data-testid={`quota-token-cap-${q.organizationId}`}>
                    {limitOrUnlimited(q.maxOutputTokensPerRequest)}
                  </td>
                  <td data-testid={`quota-updated-${q.organizationId}`}>
                    {q.updatedAt && q.updatedAt !== '0' ? formatTime(q.updatedAt) : '—'}
                  </td>
                  <td data-testid={`quota-updated-by-${q.organizationId}`}>{q.updatedBy || '—'}</td>
                  <td>
                    <button className="link" data-testid={`quota-edit-${q.organizationId}`} onClick={() => setEditTarget(row)}>
                      {t('quota.edit')}
                    </button>
                    <button className="link" data-testid={`quota-history-${q.organizationId}`} onClick={() => setHistoryTarget(row)}>
                      {t('quota.history')}
                    </button>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}

      <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />

      {editTarget && (
        <QuotaEditDialog
          row={editTarget}
          onClose={() => setEditTarget(null)}
          onSaved={() => {
            setEditTarget(null);
            void load();
          }}
        />
      )}

      {historyTarget && (
        <QuotaHistoryDrawer row={historyTarget} onClose={() => setHistoryTarget(null)} />
      )}
    </div>
  );
}

function usageOrUnlimited(current: string, limit: string) {
  const l = parseInt(limit, 10);
  if (l === 0) return 'Unlimited';
  return `${current} / ${l}`;
}

function limitOrUnlimited(limit: string) {
  const l = parseInt(limit, 10);
  return l === 0 ? 'Unlimited' : String(l);
}

function QuotaEditDialog({
  row,
  onClose,
  onSaved,
}: {
  row: OrganizationQuotaRow;
  onClose: () => void;
  onSaved: () => void;
}) {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [values, setValues] = useState<Record<string, string>>({
    maxConcurrentRequests: row.quota.maxConcurrentRequests,
    maxInferenceServices: row.quota.maxInferenceServices,
    maxBatchJobsPerDay: row.quota.maxBatchJobsPerDay,
    maxOutputTokensPerRequest: row.quota.maxOutputTokensPerRequest,
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);

  const applyPreset = (key: string) => {
    setValues({ ...PRESETS[key].values });
    setErrors({});
  };

  const validate = (): boolean => {
    const next: Record<string, string> = {};
    const ranges: Record<string, [number, number]> = {
      maxConcurrentRequests: [1, 10000],
      maxInferenceServices: [1, 1000],
      maxBatchJobsPerDay: [1, 10000],
      maxOutputTokensPerRequest: [64, 32768],
    };
    for (const dim of DIMENSIONS) {
      const v = parseInt(values[dim], 10);
      const [lo, hi] = ranges[dim];
      if (Number.isNaN(v) || v < 0 || (v !== 0 && (v < lo || v > hi))) {
        next[dim] = t('quota.rangeError', { lo, hi });
      }
    }
    setErrors(next);
    return Object.keys(next).length === 0;
  };

  const save = async () => {
    if (!validate()) return;
    setSaving(true);
    try {
      const body: Record<string, number> = {};
      for (const dim of DIMENSIONS) body[dim] = parseInt(values[dim], 10);
      await api.patch(`/api/v1/admin/quotas/${row.quota.organizationId}`, orgId, body);
      onSaved();
    } catch (e: any) {
      if (e?.body?.code === 13305) {
        setErrors({ maxConcurrentRequests: e?.body?.message || t('quota.invalidValue') });
      } else {
        setErrors({ maxConcurrentRequests: e?.message || t('quota.saveFailed') });
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog title={t('quota.editTitle')} onClose={onClose} testId="quota-edit-dialog">
      <p className="muted">{row.quota.organizationName}</p>
      <div className="filter-bar">
        {Object.entries(PRESETS).map(([key, p]) => (
          <button key={key} className="secondary" data-testid={`quota-preset-${key}`} onClick={() => applyPreset(key)}>
            {t(p.label)}
          </button>
        ))}
      </div>
      {DIMENSIONS.map((dim) => (
        <label key={dim}>
          {t(`quota.field.${dim}`)}
          <input
            type="number"
            data-testid={`quota-field-${dim}`}
            value={values[dim]}
            onChange={(e) => setValues({ ...values, [dim]: e.target.value })}
          />
          {errors[dim] && (
            <span className="error-text" data-testid={`quota-field-error-${dim}`}>
              {errors[dim]}
            </span>
          )}
        </label>
      ))}
      <div className="filter-bar">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="quota-save" disabled={saving} onClick={() => void save()}>
          {saving ? t('common.loading') : t('quota.save')}
        </button>
      </div>
    </Dialog>
  );
}

function QuotaHistoryDrawer({ row, onClose }: { row: OrganizationQuotaRow; onClose: () => void }) {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [changes, setChanges] = useState<GetOrganizationQuotaResponse['changes']>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let active = true;
    (async () => {
      try {
        const data = await api.get<GetOrganizationQuotaResponse>(`/api/v1/admin/quotas/${row.quota.organizationId}`, orgId);
        if (active) setChanges(data.changes || []);
      } catch {
        // keep empty
      } finally {
        if (active) setLoading(false);
      }
    })();
    return () => {
      active = false;
    };
  }, [api, orgId, row.quota.organizationId]);

  return (
    <div className="dialog-backdrop" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="dialog drawer" data-testid="quota-history-drawer">
        <h2>{t('quota.historyTitle')}</h2>
        <p className="muted">{row.quota.organizationName}</p>
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : changes.length === 0 ? (
          <div className="empty-state">{t('quota.historyEmpty')}</div>
        ) : (
          <table className="data">
            <thead>
              <tr>
                <th>{t('quota.colTime')}</th>
                <th>{t('quota.colOperator')}</th>
                <th>{t('quota.colDimension')}</th>
                <th>{t('quota.colChange')}</th>
              </tr>
            </thead>
            <tbody>
              {changes.map((ch, i) => (
                <tr key={i} data-testid={`quota-history-entry-${i}`}>
                  <td>{formatTime(ch.changedAt)}</td>
                  <td>{ch.operator}</td>
                  <td>{t(`quota.dimension.${ch.dimension}`)}</td>
                  <td>
                    {ch.oldValue} → {ch.newValue}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        <p className="muted">{t('quota.propagationNote')}</p>
        <div className="filter-bar">
          <button className="secondary" onClick={onClose}>
            {t('common.close')}
          </button>
        </div>
      </div>
    </div>
  );
}