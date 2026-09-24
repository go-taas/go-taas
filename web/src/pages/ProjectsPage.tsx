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
  type ProjectSummary,
} from '../api';
import { useOrg } from '../org';
import { Dialog, ErrorBanner, Pagination, StateBadge } from '../components';

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
      setError(e instanceof Error ? e.message : 'failed to load projects');
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, orgFilter, stateFilter]);

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
          <h1>Projects</h1>
          <div className="subtitle">
            Projects subdivide an organization's work. Project ids are
            globally unique and belong to exactly one organization.
          </div>
        </div>
        <button data-testid="create-project" onClick={() => setCreateOpen(true)}>
          Create Project
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
          <option value="all">All organizations</option>
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
          <option value="all">All states</option>
          <option value="active">active</option>
          <option value="disabled">disabled</option>
        </select>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">Loading…</div>
        ) : projects.length === 0 ? (
          <div className="empty-state" data-testid="projects-empty">
            No projects match your filters.
          </div>
        ) : (
          <table className="data" data-testid="projects-table">
            <thead>
              <tr>
                <th>Project</th>
                <th>Organization</th>
                <th>State</th>
                <th>Created</th>
                <th>Actions</th>
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
                      Edit
                    </button>
                    {project.state === 'active' ? (
                      <button
                        className="link danger"
                        data-testid={`project-disable-${project.projectId}`}
                        onClick={() =>
                          setConfirmTarget({ project, action: 'disable' })
                        }
                      >
                        Disable
                      </button>
                    ) : (
                      <button
                        className="link"
                        data-testid={`project-enable-${project.projectId}`}
                        onClick={() =>
                          setConfirmTarget({ project, action: 'enable' })
                        }
                      >
                        Enable
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

  const activeOrgs = orgs.filter((o) => o.state === 'active');

  const submit = async () => {
    if (!organizationId) {
      setError('Pick the owning organization.');
      return;
    }
    if (!/^[a-z0-9][a-z0-9-]{2,63}$/.test(id.trim())) {
      setError('ID must be 3-64 chars: lowercase letters, digits, hyphens; start with letter/digit.');
      return;
    }
    if (!name.trim()) {
      setError('Display name is required.');
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
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to create project');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title="Create Project" onClose={onClose} testId="create-project-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="project-org">Organization</label>
          <select
            id="project-org"
            data-testid="project-org-select"
            value={organizationId}
            onChange={(e) => setOrganizationId(e.target.value)}
          >
            <option value="">Select organization…</option>
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
          <div className="muted">Only active organizations can own new projects.</div>
        </div>
        <div className="form-field">
          <label htmlFor="project-id">Project ID</label>
          <input
            id="project-id"
            data-testid="project-id-input"
            value={id}
            maxLength={64}
            onChange={(e) => setId(e.target.value)}
            placeholder="e.g. proj-core"
          />
        </div>
        <div className="form-field">
          <label htmlFor="project-name">Display name</label>
          <input
            id="project-name"
            data-testid="project-name-input"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Core Platform"
          />
        </div>
        <div className="form-field full">
          <label htmlFor="project-desc">Description (optional)</label>
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
          Cancel
        </button>
        <button data-testid="project-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? 'Creating…' : 'Create'}
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

  const submit = async () => {
    if (!name.trim()) {
      setError('Display name is required.');
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
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to update project');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={`Edit ${project.projectId}`} onClose={onClose} testId="edit-project-dialog">
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="project-edit-name">Display name</label>
          <input
            id="project-edit-name"
            data-testid="project-edit-name-input"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <div className="form-field full">
          <label htmlFor="project-edit-desc">Description</label>
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
          Cancel
        </button>
        <button data-testid="project-edit-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? 'Saving…' : 'Save'}
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
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : `failed to ${action} project`);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={`${action === 'disable' ? 'Disable' : 'Enable'} ${project.projectId}?`}
      onClose={onClose}
      testId="project-confirm-dialog"
    >
      {action === 'disable' ? (
        <p>
          Disabling marks the project inactive. Existing resources stay
          readable; the project cannot own new activity until re-enabled.
        </p>
      ) : (
        <p>
          Enabling restores the project. The owning organization must be
          active.
        </p>
      )}
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          className={action === 'disable' ? 'danger' : ''}
          data-testid="project-confirm-ok"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? 'Working…' : action === 'disable' ? 'Disable' : 'Enable'}
        </button>
      </div>
    </Dialog>
  );
}
