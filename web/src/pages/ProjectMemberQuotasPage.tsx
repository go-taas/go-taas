// ProjectMemberQuotasPage sets per-member quotas inside a project. A
// project administrator budgets the people in the project, not the project
// as a whole: each row is one member with their own limits, so a generous
// cap for one person never silently applies to the next.
//
// In the organization console the page lists the organization's projects and
// edits the selected one; in the project console it edits that project.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type ProjectSummary } from '../api';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner } from '../components';
import { useAdminScope } from '../admin-scope';

interface MemberQuota {
  projectId: string;
  organizationId: string;
  userId: string;
  displayName: string;
  role: string;
  maxConcurrentRequests: string;
  maxInferenceServices: string;
  maxBatchJobsPerDay: string;
  maxOutputTokensPerRequest: string;
  updatedAt: string;
  updatedBy: string;
  updatedByDisplayName: string;
}

const DIMENSIONS = [
  'maxConcurrentRequests',
  'maxInferenceServices',
  'maxBatchJobsPerDay',
  'maxOutputTokensPerRequest',
] as const;

type Dimension = (typeof DIMENSIONS)[number];

const EMPTY_VALUES: Record<Dimension, string> = {
  maxConcurrentRequests: '0',
  maxInferenceServices: '0',
  maxBatchJobsPerDay: '0',
  maxOutputTokensPerRequest: '0',
};

export default function ProjectMemberQuotasPage() {
  const { scope } = useAdminScope();
  const { t, tEnum } = useI18n();
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [projectId, setProjectId] = useState(scope?.projectId || '');
  const [quotas, setQuotas] = useState<MemberQuota[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [editTarget, setEditTarget] = useState<MemberQuota | null>(null);

  const orgId = scope?.organizationId || '';

  // Load the project list in the organization console; the project console
  // already knows its project.
  useEffect(() => {
    if (scope?.scopeType === 'project') {
      setProjectId(scope.projectId);
      return undefined;
    }
    if (!orgId) return undefined;
    let cancelled = false;
    api
      .get<{ projects: ProjectSummary[] }>(
        `/api/v1/admin/tenancy/projects?organizationId=${encodeURIComponent(orgId)}&page.limit=100`,
        orgId,
      )
      .then((data) => {
        if (cancelled) return;
        const list = data.projects || [];
        setProjects(list);
        setProjectId((prev) => prev || list[0]?.projectId || '');
      })
      .catch(() => setProjects([]));
    return () => {
      cancelled = true;
    };
  }, [api, orgId, scope?.scopeType, scope?.projectId]);

  const load = useCallback(async () => {
    if (!projectId) return;
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{ quotas: MemberQuota[] }>(
        `/api/v1/admin/tenancy/projects/${projectId}/member-quotas`,
        orgId,
      );
      setQuotas(data.quotas || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('memberQuota.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, projectId, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="page" data-testid="project-member-quotas-page">
      <h1>{t('memberQuota.title')}</h1>
      <p className="subtitle">{t('memberQuota.subtitle')}</p>
      {error && <ErrorBanner message={error} />}

      <div className="panel" style={{ marginBottom: 16 }}>
        <label className="field">
          <span>{t('memberQuota.project')}</span>
          <select
            data-testid="member-quota-project"
            value={projectId}
            onChange={(e) => setProjectId(e.target.value)}
            disabled={scope?.scopeType === 'project'}
          >
            {projects.length === 0 && <option value={projectId}>{projectId || '—'}</option>}
            {projects.map((p) => (
              <option key={p.projectId} value={p.projectId}>
                {p.displayName && p.displayName !== p.projectId
                  ? `${p.displayName} (${p.projectId})`
                  : p.projectId}
              </option>
            ))}
          </select>
        </label>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : quotas.length === 0 ? (
          <div className="empty-state" data-testid="member-quotas-empty">
            {t('memberQuota.empty')}
          </div>
        ) : (
          <table className="data" data-testid="member-quotas-table">
            <thead>
              <tr>
                <th>{t('memberQuota.colMember')}</th>
                <th>{t('memberQuota.colRole')}</th>
                {DIMENSIONS.map((dim) => (
                  <th key={dim}>{t(`quota.dimension.${dim}`)}</th>
                ))}
                <th>{t('memberQuota.colUpdated')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {quotas.map((q) => (
                <tr key={q.userId} data-testid={`member-quota-row-${q.userId}`}>
                  <td>
                    <strong>{q.displayName || q.userId}</strong>
                    <div className="muted small mono">{q.userId}</div>
                  </td>
                  <td>{tEnum('state', q.role)}</td>
                  {DIMENSIONS.map((dim) => (
                    <td key={dim} className="mono">
                      {q[dim] && q[dim] !== '0' ? q[dim] : t('memberQuota.unlimited')}
                    </td>
                  ))}
                  <td className="muted small">
                    {q.updatedBy ? q.updatedByDisplayName || q.updatedBy : '—'}
                  </td>
                  <td>
                    <button
                      className="link"
                      data-testid={`member-quota-edit-${q.userId}`}
                      onClick={() => setEditTarget(q)}
                    >
                      {t('common.edit')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {editTarget && (
        <EditMemberQuotaDialog
          orgId={orgId}
          projectId={projectId}
          quota={editTarget}
          onClose={() => setEditTarget(null)}
          onDone={() => {
            setEditTarget(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

// EditMemberQuotaDialog edits one member's limits. 0 means unlimited, which
// the hint spells out so an empty-looking field is not mistaken for "no
// access".
function EditMemberQuotaDialog({
  orgId,
  projectId,
  quota,
  onClose,
  onDone,
}: {
  orgId: string;
  projectId: string;
  quota: MemberQuota;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [values, setValues] = useState<Record<Dimension, string>>({
    maxConcurrentRequests: quota.maxConcurrentRequests || '0',
    maxInferenceServices: quota.maxInferenceServices || '0',
    maxBatchJobsPerDay: quota.maxBatchJobsPerDay || '0',
    maxOutputTokensPerRequest: quota.maxOutputTokensPerRequest || '0',
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const save = async () => {
    setSaving(true);
    setError('');
    try {
      await api.patch(
        `/api/v1/admin/tenancy/projects/${projectId}/member-quotas/${quota.userId}`,
        orgId,
        {
          maxConcurrentRequests: Number(values.maxConcurrentRequests) || 0,
          maxInferenceServices: Number(values.maxInferenceServices) || 0,
          maxBatchJobsPerDay: Number(values.maxBatchJobsPerDay) || 0,
          maxOutputTokensPerRequest: Number(values.maxOutputTokensPerRequest) || 0,
        },
      );
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('memberQuota.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      title={t('memberQuota.editTitle', { name: quota.displayName || quota.userId })}
      onClose={onClose}
      testId="member-quota-dialog"
    >
      <p className="muted">{t('memberQuota.editHint')}</p>
      <div className="form-grid">
        {DIMENSIONS.map((dim) => (
          <label key={dim}>
            {t(`quota.dimension.${dim}`)}
            <input
              data-testid={`member-quota-${dim}`}
              type="number"
              min="0"
              value={values[dim] ?? EMPTY_VALUES[dim]}
              onChange={(e) => setValues((prev) => ({ ...prev, [dim]: e.target.value }))}
            />
          </label>
        ))}
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="member-quota-save" disabled={saving} onClick={() => void save()}>
          {saving ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}
