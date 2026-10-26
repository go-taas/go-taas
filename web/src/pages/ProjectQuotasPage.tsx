// ProjectQuotasPage sets the service quotas of a project. In the
// organization console it lists the organization's projects and edits
// the selected one; in the project console it edits that project
// (requirements 3 and 4).

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type ProjectSummary } from '../api';
import { useI18n } from '../i18n';
import { ErrorBanner } from '../components';
import { useAdminScope } from '../admin-scope';

interface ProjectQuota {
  projectId: string;
  organizationId: string;
  projectName: string;
  maxConcurrentRequests: string;
  maxInferenceServices: string;
  maxBatchJobsPerDay: string;
  maxOutputTokensPerRequest: string;
}

const DIMENSIONS = [
  'maxConcurrentRequests',
  'maxInferenceServices',
  'maxBatchJobsPerDay',
  'maxOutputTokensPerRequest',
] as const;

type Dimension = (typeof DIMENSIONS)[number];

export default function ProjectQuotasPage() {
  const { scope } = useAdminScope();
  const { t } = useI18n();
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [projectId, setProjectId] = useState(scope?.projectId || '');
  const [values, setValues] = useState<Record<Dimension, string>>({
    maxConcurrentRequests: '0',
    maxInferenceServices: '0',
    maxBatchJobsPerDay: '0',
    maxOutputTokensPerRequest: '0',
  });
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [info, setInfo] = useState('');

  const orgId = scope?.organizationId || '';

  // Load the project list in the organization console; the project
  // console already knows its project.
  useEffect(() => {
    if (scope?.scopeType === 'project') {
      setProjectId(scope.projectId);
      return;
    }
    if (!orgId) return;
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
      const data = await api.get<{ quota: ProjectQuota }>(
        `/api/v1/admin/tenancy/projects/${projectId}/quota`,
        orgId,
      );
      const q = data.quota;
      if (q) {
        setValues({
          maxConcurrentRequests: q.maxConcurrentRequests || '0',
          maxInferenceServices: q.maxInferenceServices || '0',
          maxBatchJobsPerDay: q.maxBatchJobsPerDay || '0',
          maxOutputTokensPerRequest: q.maxOutputTokensPerRequest || '0',
        });
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : t('projectQuota.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, projectId, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async () => {
    if (!projectId) return;
    setSaving(true);
    setError('');
    setInfo('');
    try {
      await api.patch(`/api/v1/admin/tenancy/projects/${projectId}/quota`, orgId, {
        maxConcurrentRequests: Number(values.maxConcurrentRequests) || 0,
        maxInferenceServices: Number(values.maxInferenceServices) || 0,
        maxBatchJobsPerDay: Number(values.maxBatchJobsPerDay) || 0,
        maxOutputTokensPerRequest: Number(values.maxOutputTokensPerRequest) || 0,
      });
      setInfo(t('projectQuota.saved'));
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('projectQuota.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="page" data-testid="project-quotas-page">
      <h1>{t('projectQuota.title')}</h1>
      <p className="subtitle">{t('projectQuota.subtitle')}</p>
      {error && <ErrorBanner message={error} />}
      {info && <div className="success" data-testid="project-quota-info">{info}</div>}

      <div className="panel" style={{ marginBottom: 16 }}>
        <label className="field">
          <span>{t('projectQuota.project')}</span>
          <select
            data-testid="project-quota-project"
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
        ) : (
          <div className="form-grid">
            {DIMENSIONS.map((dim) => (
              <label key={dim}>
                {t(`quota.dimension.${dim}`)}
                <input
                  data-testid={`project-quota-${dim}`}
                  type="number"
                  min="0"
                  value={values[dim]}
                  onChange={(e) =>
                    setValues((prev) => ({ ...prev, [dim]: e.target.value }))
                  }
                />
              </label>
            ))}
          </div>
        )}
        <div className="dialog-actions">
          <button
            data-testid="project-quota-save"
            disabled={saving || loading || !projectId}
            onClick={() => void save()}
          >
            {saving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </div>
    </div>
  );
}
