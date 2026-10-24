// MyProjectsPage is the end-user console "My projects" page: the
// caller's own projects and roles.

import { useCallback, useEffect, useState } from 'react';
import { ErrorBanner, StateBadge } from '../../components';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import type { MyProject } from '../../api';

export default function MyProjectsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [projects, setProjects] = useState<MyProject[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{ projects: MyProject[] }>(
        '/api/v1/tenancy/my-projects',
        orgId,
      );
      setProjects(data.projects || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('myprojects.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="page" data-testid="my-projects-page">
      <h1>{t('myprojects.title')}</h1>
      {error && <ErrorBanner message={error} />}
      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : projects.length === 0 ? (
          <div className="empty-state" data-testid="my-projects-empty">
            {t('myprojects.empty')}
          </div>
        ) : (
          <table className="data" data-testid="my-projects-table">
            <thead>
              <tr>
                <th>{t('myprojects.colProject')}</th>
                <th>{t('myprojects.colOrganization')}</th>
                <th>{t('myprojects.colRole')}</th>
              </tr>
            </thead>
            <tbody>
              {projects.map((project) => (
                <tr key={project.projectId} data-testid={`my-project-row-${project.projectId}`}>
                  <td>
                    <strong className="mono">{project.projectId}</strong>
                    <div className="muted">{project.displayName}</div>
                  </td>
                  <td className="mono">{project.organizationId}</td>
                  <td>
                    <StateBadge state={project.role} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}