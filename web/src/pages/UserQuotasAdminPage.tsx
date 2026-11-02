// User quotas page (organization console): the personal tier of the
// three-level quota model. Each organization member carries their own
// limits, which combine with the organization and project limits by
// taking the minimum of the non-zero values. A 0 limit is unlimited.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type PageMeta, type UserQuota } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination } from '../components';

const PAGE_SIZE = 20;

interface ListResponse {
  quotas?: UserQuota[];
  pageMeta?: PageMeta;
}

// dims maps each editable field onto its wire name and label key.
const dims = [
  { field: 'maxConcurrentRequests', label: 'quotas.maxConcurrent' },
  { field: 'maxBatchJobsPerDay', label: 'quotas.maxBatch' },
  { field: 'maxOutputTokensPerRequest', label: 'quotas.maxTokens' },
  { field: 'monthlySpendLimitCents', label: 'quotas.maxSpend' },
] as const;

export default function UserQuotasPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [rows, setRows] = useState<UserQuota[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [name, setName] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editTarget, setEditTarget] = useState<UserQuota | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const query = new URLSearchParams({
        'page.offset': String(offset),
        'page.limit': String(PAGE_SIZE),
      });
      if (orgId) query.set('organization_id', orgId);
      if (name.trim()) query.set('name', name.trim());
      const data = await api.get<ListResponse>(`/api/v1/admin/tenancy/user-quotas?${query}`, orgId);
      setRows(data.quotas || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('quotas.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, offset, name, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('quotas.title')}</h1>
          <div className="subtitle">{t('quotas.subtitle')}</div>
        </div>
      </div>

      <div className="filters">
        <input
          data-testid="user-quota-search"
          placeholder={t('quotas.search')}
          value={name}
          onChange={(event) => { setName(event.target.value); setOffset(0); }}
        />
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : rows.length === 0 ? (
          <div className="empty-state" data-testid="user-quotas-empty">{t('quotas.empty')}</div>
        ) : (
          <table className="data" data-testid="user-quotas-table">
            <thead>
              <tr>
                <th>{t('quotas.colUser')}</th>
                <th>{t('quotas.colRole')}</th>
                <th>{t('quotas.maxConcurrent')}</th>
                <th>{t('quotas.maxBatch')}</th>
                <th>{t('quotas.maxTokens')}</th>
                <th>{t('quotas.maxSpend')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.userId} data-testid={`user-quota-row-${row.userId}`}>
                  <td>{row.displayName || row.userId}</td>
                  <td>{row.role || '—'}</td>
                  <td>{unlimitedOr(t, row.maxConcurrentRequests)}</td>
                  <td>{unlimitedOr(t, row.maxBatchJobsPerDay)}</td>
                  <td>{unlimitedOr(t, row.maxOutputTokensPerRequest)}</td>
                  <td>{unlimitedOr(t, row.monthlySpendLimitCents)}</td>
                  <td>
                    <button className="link" data-testid={`edit-user-quota-${row.userId}`}
                      onClick={() => setEditTarget(row)}>{t('common.edit')}</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {total > PAGE_SIZE && (
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        )}
      </div>

      {editTarget && (
        <QuotaDialog
          quota={editTarget}
          orgId={orgId}
          onClose={() => setEditTarget(null)}
          onSaved={() => { setEditTarget(null); void load(); }}
        />
      )}
    </div>
  );
}

// unlimitedOr renders a 0 limit as "Unlimited" and any other value
// verbatim.
function unlimitedOr(t: (key: string) => string, value: string): string {
  const parsed = parseInt(value || '0', 10);
  return parsed === 0 ? t('quotas.unlimited') : String(parsed);
}

function QuotaDialog({
  quota,
  orgId,
  onClose,
  onSaved,
}: {
  quota: UserQuota;
  orgId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [values, setValues] = useState<Record<string, string>>(() => {
    const out: Record<string, string> = {};
    for (const dim of dims) out[dim.field] = String(quota[dim.field] || '0');
    return out;
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const save = async () => {
    setSaving(true);
    setError('');
    try {
      await api.patch(`/api/v1/admin/tenancy/user-quotas/${quota.userId}`, orgId, {
        organizationId: orgId,
        ...values,
      });
      onSaved();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('quotas.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog title={t('quotas.dialogTitle', { name: quota.displayName || quota.userId })}
      testId="user-quota-dialog" onClose={onClose}>
      {error && <ErrorBanner message={error} />}
      <div className="form">
        {dims.map((dim) => (
          <label key={dim.field}>
            {t(dim.label)}
            <input
              data-testid={`quota-${dim.field}`}
              type="number"
              min={0}
              value={values[dim.field]}
              onChange={(event) => setValues((prev) => ({ ...prev, [dim.field]: event.target.value }))}
            />
          </label>
        ))}
        <p className="muted">{t('quotas.zeroNote')}</p>
      </div>
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button data-testid="save-user-quota" disabled={saving} onClick={() => void save()}>
          {saving ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}
