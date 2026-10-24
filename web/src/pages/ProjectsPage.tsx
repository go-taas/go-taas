// Projects page: the cross-organization project registry with create,
// edit, disable/enable and an organization filter.
// Implements docs/design/multi-tenancy.md FR2, AC6-AC11.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  ApiError,
  formatTime,
  type OrganizationSummary,
  type PageMeta,
  type ProjectMember,
  type ProjectSummary,
} from '../api';
import { useOrg } from '../org';
import { Dialog, ErrorBanner, Pagination, StateBadge } from '../components';
import { useI18n } from '../i18n';

interface ListResponse {
  response: { code: number; message: string };
  projects: ProjectSummary[];
  pageMeta?: PageMeta;
}

interface OrgListResponse {
  response: { code: number; message: string };
  organizations: OrganizationSummary[];
}

const PAGE_SIZE = 20;

export default function ProjectsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [orgs, setOrgs] = useState<OrganizationSummary[]>([]);
  const [orgFilter, setOrgFilter] = useState('all');
  const [stateFilter, setStateFilter] = useState('all');
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<ProjectSummary | null>(null);
  const [membersTarget, setMembersTarget] = useState<ProjectSummary | null>(null);
  const [quotaTarget, setQuotaTarget] = useState<ProjectSummary | null>(null);
  const [confirmTarget, setConfirmTarget] = useState<{
    project: ProjectSummary;
    action: 'disable' | 'enable';
  } | null>(null);

  const loadOrgs = useCallback(async () => {
    try {
      const data = await api.get<OrgListResponse>(
        '/api/v1/admin/tenancy/organizations?page.limit=100',
        orgId,
      );
      setOrgs(data.organizations || []);
    } catch {
      // The org filter falls back to "all"; a banner is not needed here.
    }
  }, [orgId]);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams({
        [`page.offset`]: String(offset),
        [`page.limit`]: String(PAGE_SIZE),
      });
      if (orgFilter !== 'all') params.set('organizationId', orgFilter);
      if (stateFilter !== 'all') params.set('state', stateFilter);
      const data = await api.get<ListResponse>(
        `/api/v1/admin/tenancy/projects?${params.toString()}`,
        orgId,
      );
      setProjects(data.projects || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('projects.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, orgFilter, stateFilter, t]);

  useEffect(() => {
    void loadOrgs();
  }, [loadOrgs]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('projects.title')}</h1>
          <div className="subtitle">{t('projects.subtitle')}</div>
        </div>
        <button data-testid="create-project" onClick={() => setCreateOpen(true)}>
          {t('projects.create')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <select
          data-testid="project-org-filter"
          value={orgFilter}
          onChange={(e) => {
            setOrgFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="all">{t('common.allOrganizations')}</option>
          {orgs.map((org) => (
            <option
              key={org.organizationId}
              value={org.organizationId}
              data-testid={`project-org-option-${org.organizationId}`}
            >
              {org.organizationId}
            </option>
          ))}
        </select>
        <select
          data-testid="project-state-filter"
          value={stateFilter}
          onChange={(e) => {
            setStateFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="all">{t('common.allStates')}</option>
          <option value="active">active</option>
          <option value="disabled">disabled</option>
        </select>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : projects.length === 0 ? (
          <div className="empty-state" data-testid="projects-empty">
            {t('projects.empty')}
          </div>
        ) : (
          <table className="data" data-testid="projects-table">
            <thead>
              <tr>
                <th>{t('projects.colProject')}</th>
                <th>{t('projects.colOrganization')}</th>
                <th>{t('projects.colState')}</th>
                <th>{t('projects.colCreated')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {projects.map((project) => (
                <tr key={project.projectId} data-testid={`project-row-${project.projectId}`}>
                  <td>
                    <strong className="mono">{project.projectId}</strong>
                    <div className="muted">{project.displayName}</div>
                  </td>
                  <td className="mono">{project.organizationId}</td>
                  <td>
                    <StateBadge state={project.state} />
                  </td>
                  <td>{formatTime(project.createdAt)}</td>
                  <td>
                    <button
                      className="link"
                      data-testid={`project-edit-${project.projectId}`}
                      onClick={() => setEditTarget(project)}
                    >
                      {t('common.edit')}
                    </button>
                    <button
                      className="link"
                      data-testid={`project-members-${project.projectId}`}
                      onClick={() => setMembersTarget(project)}
                    >
                      {t('projects.members')}
                    </button>
                    <button
                      className="link"
                      data-testid={`project-quota-${project.projectId}`}
                      onClick={() => setQuotaTarget(project)}
                    >
                      {t('projects.quota')}
                    </button>
                    {project.state === 'active' ? (
                      <button
                        className="link danger"
                        data-testid={`project-disable-${project.projectId}`}
                        onClick={() =>
                          setConfirmTarget({ project, action: 'disable' })
                        }
                      >
                        {t('common.disable')}
                      </button>
                    ) : (
                      <button
                        className="link"
                        data-testid={`project-enable-${project.projectId}`}
                        onClick={() =>
                          setConfirmTarget({ project, action: 'enable' })
                        }
                      >
                        {t('common.enable')}
                      </button>
                    )}
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

      {createOpen && (
        <CreateProjectDialog
          orgId={orgId}
          orgs={orgs}
          onClose={() => setCreateOpen(false)}
          onDone={() => {
            setCreateOpen(false);
            void load();
          }}
        />
      )}

      {editTarget && (
        <EditProjectDialog
          orgId={orgId}
          project={editTarget}
          onClose={() => setEditTarget(null)}
          onDone={() => {
            setEditTarget(null);
            void load();
          }}
        />
      )}

      {quotaTarget && (
        <ProjectQuotaDialog
          orgId={orgId}
          project={quotaTarget}
          onClose={() => setQuotaTarget(null)}
        />
      )}

      {membersTarget && (
        <ProjectMembersDialog
          orgId={orgId}
          project={membersTarget}
          onClose={() => setMembersTarget(null)}
        />
      )}

      {confirmTarget && (
        <ConfirmStateDialog
          orgId={orgId}
          project={confirmTarget.project}
          action={confirmTarget.action}
          onClose={() => setConfirmTarget(null)}
          onDone={() => {
            setConfirmTarget(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

function CreateProjectDialog({
  orgId,
  orgs,
  onClose,
  onDone,
}: {
  orgId: string;
  orgs: OrganizationSummary[];
  onClose: () => void;
  onDone: () => void;
}) {
  const [organizationId, setOrganizationId] = useState('');
  const [id, setId] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  const activeOrgs = orgs.filter((o) => o.state === 'active');

  const submit = async () => {
    if (!organizationId) {
      setError(t('projects.validationOrg'));
      return;
    }
    if (!/^[a-z0-9][a-z0-9-]{2,63}$/.test(id.trim())) {
      setError(t('projects.validationId'));
      return;
    }
    if (!name.trim()) {
      setError(t('projects.validationDisplayName'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.post('/api/v1/admin/tenancy/projects', orgId, {
        organizationId,
        projectId: id.trim(),
        displayName: name.trim(),
        description: description.trim(),
      });
      onDone();
    } catch (e) {
      // 10016: the project id already exists; 10017: the org is disabled.
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('projects.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('projects.createTitle')} onClose={onClose} testId="create-project-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="project-org">{t('projects.fieldOrganization')}</label>
          <select
            id="project-org"
            data-testid="project-org-select"
            value={organizationId}
            onChange={(e) => setOrganizationId(e.target.value)}
          >
            <option value="">{t('projects.placeholderSelectOrg')}</option>
            {activeOrgs.map((org) => (
              <option
                key={org.organizationId}
                value={org.organizationId}
                data-testid={`project-org-select-option-${org.organizationId}`}
              >
                {org.organizationId}
              </option>
            ))}
          </select>
          <div className="muted">{t('projects.mutedInactiveOrg')}</div>
        </div>
        <div className="form-field">
          <label htmlFor="project-id">{t('projects.fieldProjectId')}</label>
          <input
            id="project-id"
            data-testid="project-id-input"
            value={id}
            maxLength={64}
            onChange={(e) => setId(e.target.value)}
            placeholder={t('projects.placeholderProjectId')}
          />
        </div>
        <div className="form-field">
          <label htmlFor="project-name">{t('projects.fieldDisplayName')}</label>
          <input
            id="project-name"
            data-testid="project-name-input"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('projects.placeholderDisplayName')}
          />
        </div>
        <div className="form-field full">
          <label htmlFor="project-desc">{t('common.descriptionOptional')}</label>
          <textarea
            id="project-desc"
            data-testid="project-desc-input"
            value={description}
            maxLength={1024}
            rows={3}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="project-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.creating') : t('common.create')}
        </button>
      </div>
    </Dialog>
  );
}

function EditProjectDialog({
  orgId,
  project,
  onClose,
  onDone,
}: {
  orgId: string;
  project: ProjectSummary;
  onClose: () => void;
  onDone: () => void;
}) {
  const [name, setName] = useState(project.displayName);
  const [description, setDescription] = useState(project.description || '');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  const submit = async () => {
    if (!name.trim()) {
      setError(t('projects.validationDisplayName'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.patch(
        `/api/v1/admin/tenancy/projects/${project.projectId}`,
        orgId,
        {
          displayName: name.trim(),
          description: description.trim(),
        },
      );
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('projects.updateFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={t('projects.editTitle', { projectId: project.projectId })}
      onClose={onClose}
      testId="edit-project-dialog"
    >
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="project-edit-name">{t('projects.fieldDisplayName')}</label>
          <input
            id="project-edit-name"
            data-testid="project-edit-name-input"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <div className="form-field full">
          <label htmlFor="project-edit-desc">{t('projects.fieldDescription')}</label>
          <textarea
            id="project-edit-desc"
            data-testid="project-edit-desc-input"
            value={description}
            maxLength={1024}
            rows={3}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="project-edit-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}

function ConfirmStateDialog({
  orgId,
  project,
  action,
  onClose,
  onDone,
}: {
  orgId: string;
  project: ProjectSummary;
  action: 'disable' | 'enable';
  onClose: () => void;
  onDone: () => void;
}) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.post(
        `/api/v1/admin/tenancy/projects/${project.projectId}:${action}`,
        orgId,
      );
      onDone();
    } catch (e) {
      // 10017: enabling a project under a disabled organization.
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('projects.actionFailed', { action }));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={t(action === 'disable' ? 'projects.disableTitle' : 'projects.enableTitle', {
        projectId: project.projectId,
      })}
      onClose={onClose}
      testId="project-confirm-dialog"
    >
      {action === 'disable' ? (
        <p>{t('projects.disableBody')}</p>
      ) : (
        <p>{t('projects.enableBody')}</p>
      )}
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          className={action === 'disable' ? 'danger' : ''}
          data-testid="project-confirm-ok"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? t('common.working') : t(action === 'disable' ? 'common.disable' : 'common.enable')}
        </button>
      </div>
    </Dialog>
  );
}

// ProjectMembersDialog manages a project's roster: list, add, role
// change and remove.
function ProjectMembersDialog({
  orgId,
  project,
  onClose,
}: {
  orgId: string;
  project: ProjectSummary;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [members, setMembers] = useState<ProjectMember[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [addOpen, setAddOpen] = useState(false);
  const [addUserId, setAddUserId] = useState('');
  const [addRole, setAddRole] = useState('member');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{
        response: { code: number; message: string };
        members: ProjectMember[];
      }>(`/api/v1/admin/tenancy/projects/${project.projectId}/members?page.limit=100`, orgId);
      setMembers(data.members || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('projects.membersLoadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, project.projectId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const addMember = async () => {
    setError('');
    try {
      await api.post(
        `/api/v1/admin/tenancy/projects/${project.projectId}/members`,
        orgId,
        { userId: addUserId, role: addRole },
      );
      setAddOpen(false);
      setAddUserId('');
      setAddRole('member');
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('projects.membersAddFailed'));
    }
  };

  const changeRole = async (userId: string, role: string) => {
    setError('');
    try {
      await api.patch(
        `/api/v1/admin/tenancy/projects/${project.projectId}/members/${userId}`,
        orgId,
        { role },
      );
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('projects.membersRoleFailed'));
    }
  };

  const removeMember = async (userId: string) => {
    setError('');
    try {
      await api.del(
        `/api/v1/admin/tenancy/projects/${project.projectId}/members/${userId}`,
        orgId,
      );
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('projects.membersRemoveFailed'));
    }
  };

  return (
    <Dialog
      title={t('projects.membersTitle', { projectId: project.projectId })}
      onClose={onClose}
      testId="project-members-dialog"
    >
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions" style={{ justifyContent: 'flex-start', marginBottom: 12 }}>
        <button data-testid="project-member-add-button" onClick={() => setAddOpen(true)}>
          {t('projects.membersAdd')}
        </button>
      </div>
      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : members.length === 0 ? (
        <div className="empty-state" data-testid="project-members-empty">
          {t('projects.membersEmpty')}
        </div>
      ) : (
        <table className="data" data-testid="project-members-table">
          <thead>
            <tr>
              <th>{t('projects.membersColUser')}</th>
              <th>{t('projects.membersColRole')}</th>
              <th>{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {members.map((m) => (
              <tr key={m.userId} data-testid={`project-member-row-${m.userId}`}>
                <td>
                  <strong className="mono">{m.userId}</strong>
                </td>
                <td>
                  <select
                    className="member-role-select"
                    data-testid={`project-member-role-${m.userId}`}
                    value={m.role}
                    onChange={(e) => changeRole(m.userId, e.target.value)}
                  >
                    <option value="admin">admin</option>
                    <option value="member">member</option>
                    <option value="viewer">viewer</option>
                  </select>
                </td>
                <td>
                  <button
                    className="link danger"
                    data-testid={`project-member-remove-${m.userId}`}
                    onClick={() => void removeMember(m.userId)}
                  >
                    {t('common.remove')}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {addOpen && (
        <div className="form" style={{ marginTop: 12 }}>
          <label>
            {t('projects.membersFieldUserId')}
            <input
              data-testid="project-member-add-user-id"
              value={addUserId}
              onChange={(e) => setAddUserId(e.target.value)}
              placeholder={t('projects.membersPlaceholderUserId')}
            />
          </label>
          <label>
            {t('projects.membersFieldRole')}
            <select
              data-testid="project-member-add-role"
              value={addRole}
              onChange={(e) => setAddRole(e.target.value)}
            >
              <option value="admin">admin</option>
              <option value="member">member</option>
              <option value="viewer">viewer</option>
            </select>
          </label>
          <button data-testid="project-member-add-save" onClick={() => void addMember()}>
            {t('projects.membersAdd')}
          </button>
        </div>
      )}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
      </div>
    </Dialog>
  );
}

// ProjectQuotaDialog edits a project's quotas (project admin or org
// admin/owner).
function ProjectQuotaDialog({
  orgId,
  project,
  onClose,
}: {
  orgId: string;
  project: ProjectSummary;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [maxConcurrent, setMaxConcurrent] = useState('');
  const [maxServices, setMaxServices] = useState('');
  const [maxBatch, setMaxBatch] = useState('');
  const [maxTokens, setMaxTokens] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .get<{ quota: Record<string, string> }>(
        `/api/v1/admin/tenancy/projects/${project.projectId}/quota`,
        orgId,
      )
      .then((d) => {
        setMaxConcurrent(String(d.quota.maxConcurrentRequests || '0'));
        setMaxServices(String(d.quota.maxInferenceServices || '0'));
        setMaxBatch(String(d.quota.maxBatchJobsPerDay || '0'));
        setMaxTokens(String(d.quota.maxOutputTokensPerRequest || '0'));
      })
      .catch((e) => setError(e instanceof Error ? e.message : t('projects.quotaLoadFailed')))
      .finally(() => setLoading(false));
  }, [api, orgId, project.projectId, t]);

  const save = async () => {
    setSaving(true);
    setError('');
    try {
      await api.patch(`/api/v1/admin/tenancy/projects/${project.projectId}/quota`, orgId, {
        maxConcurrentRequests: maxConcurrent || '0',
        maxInferenceServices: maxServices || '0',
        maxBatchJobsPerDay: maxBatch || '0',
        maxOutputTokensPerRequest: maxTokens || '0',
      });
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('projects.quotaSaveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      title={t('projects.quotaTitle', { projectId: project.projectId })}
      onClose={onClose}
      testId="project-quota-dialog"
    >
      {error && <ErrorBanner message={error} />}
      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : (
        <div className="form">
          <label>
            {t('projects.quotaConcurrent')}
            <input data-testid="quota-concurrent" value={maxConcurrent}
              onChange={(e) => setMaxConcurrent(e.target.value)} />
          </label>
          <label>
            {t('projects.quotaServices')}
            <input data-testid="quota-services" value={maxServices}
              onChange={(e) => setMaxServices(e.target.value)} />
          </label>
          <label>
            {t('projects.quotaBatch')}
            <input data-testid="quota-batch" value={maxBatch}
              onChange={(e) => setMaxBatch(e.target.value)} />
          </label>
          <label>
            {t('projects.quotaTokens')}
            <input data-testid="quota-tokens" value={maxTokens}
              onChange={(e) => setMaxTokens(e.target.value)} />
          </label>
        </div>
      )}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button data-testid="quota-save" disabled={saving || loading} onClick={() => void save()}>
          {saving ? t('common.working') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}
