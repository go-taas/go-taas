// Organizations page: the platform-level organization registry with
// create, edit, disable/enable and usage counters.
// Implements docs/design/multi-tenancy.md FR1, FR2, AC1-AC5.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, formatTime, type OrganizationSummary, type PageMeta } from '../api';
import { useOrg } from '../org';
import { Dialog, ErrorBanner, Pagination, StateBadge } from '../components';

interface ListResponse {
  response: { code: number; message: string };
  organizations: OrganizationSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

export default function OrganizationsPage() {
  const { orgId } = useOrg();
  const [orgs, setOrgs] = useState<OrganizationSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [state, setState] = useState('all');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<OrganizationSummary | null>(null);
  const [confirmTarget, setConfirmTarget] = useState<{
    org: OrganizationSummary;
    action: 'disable' | 'enable';
  } | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams({
        [`page.offset`]: String(offset),
        [`page.limit`]: String(PAGE_SIZE),
      });
      if (state !== 'all') params.set('state', state);
      const data = await api.get<ListResponse>(
        `/api/v1/admin/tenancy/organizations?${params.toString()}`,
        orgId,
      );
      setOrgs(data.organizations || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load organizations');
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, state]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Organizations</h1>
          <div className="subtitle">
            The platform organization registry. Every API key, inference
            service and usage record belongs to exactly one organization.
          </div>
        </div>
        <button data-testid="create-org" onClick={() => setCreateOpen(true)}>
          Create Organization
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <select
          data-testid="org-state-filter"
          value={state}
          onChange={(e) => {
            setState(e.target.value);
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
        ) : orgs.length === 0 ? (
          <div className="empty-state" data-testid="orgs-empty">
            No organizations match your filters.
          </div>
        ) : (
          <table className="data" data-testid="orgs-table">
            <thead>
              <tr>
                <th>Organization</th>
                <th>State</th>
                <th>API keys</th>
                <th>Inference services</th>
                <th>Projects</th>
                <th>Created</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {orgs.map((org) => (
                <tr key={org.organizationId} data-testid={`org-row-${org.organizationId}`}>
                  <td>
                    <strong className="mono">{org.organizationId}</strong>
                    <div className="muted">{org.displayName}</div>
                  </td>
                  <td>
                    <StateBadge state={org.state} />
                  </td>
                  <td>{org.apiKeyCount}</td>
                  <td>{org.inferenceServiceCount}</td>
                  <td>{org.projectCount}</td>
                  <td>{formatTime(org.createdAt)}</td>
                  <td>
                    <button
                      className="link"
                      data-testid={`org-edit-${org.organizationId}`}
                      onClick={() => setEditTarget(org)}
                    >
                      Edit
                    </button>
                    {org.state === 'active' ? (
                      <button
                        className="link danger"
                        data-testid={`org-disable-${org.organizationId}`}
                        onClick={() =>
                          setConfirmTarget({ org, action: 'disable' })
                        }
                      >
                        Disable
                      </button>
                    ) : (
                      <button
                        className="link"
                        data-testid={`org-enable-${org.organizationId}`}
                        onClick={() =>
                          setConfirmTarget({ org, action: 'enable' })
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
        <CreateOrgDialog
          orgId={orgId}
          onClose={() => setCreateOpen(false)}
          onDone={() => {
            setCreateOpen(false);
            void load();
          }}
        />
      )}

      {editTarget && (
        <EditOrgDialog
          orgId={orgId}
          org={editTarget}
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
          org={confirmTarget.org}
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

function CreateOrgDialog({
  orgId,
  onClose,
  onDone,
}: {
  orgId: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const [id, setId] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
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
      await api.post('/api/v1/admin/tenancy/organizations', orgId, {
        organizationId: id.trim(),
        displayName: name.trim(),
        description: description.trim(),
      });
      onDone();
    } catch (e) {
      // 10015: the organization id already exists.
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to create organization');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title="Create Organization" onClose={onClose} testId="create-org-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="org-id">Organization ID</label>
          <input
            id="org-id"
            data-testid="org-id-input"
            value={id}
            maxLength={64}
            onChange={(e) => setId(e.target.value)}
            placeholder="e.g. org-acme"
          />
        </div>
        <div className="form-field">
          <label htmlFor="org-name">Display name</label>
          <input
            id="org-name"
            data-testid="org-name-input"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. ACME Inc."
          />
        </div>
        <div className="form-field full">
          <label htmlFor="org-desc">Description (optional)</label>
          <textarea
            id="org-desc"
            data-testid="org-desc-input"
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
        <button data-testid="org-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? 'Creating…' : 'Create'}
        </button>
      </div>
    </Dialog>
  );
}

function EditOrgDialog({
  orgId,
  org,
  onClose,
  onDone,
}: {
  orgId: string;
  org: OrganizationSummary;
  onClose: () => void;
  onDone: () => void;
}) {
  const [name, setName] = useState(org.displayName);
  const [description, setDescription] = useState(org.description || '');
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
        `/api/v1/admin/tenancy/organizations/${org.organizationId}`,
        orgId,
        {
          displayName: name.trim(),
          description: description.trim(),
        },
      );
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to update organization');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={`Edit ${org.organizationId}`} onClose={onClose} testId="edit-org-dialog">
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="org-edit-name">Display name</label>
          <input
            id="org-edit-name"
            data-testid="org-edit-name-input"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <div className="form-field full">
          <label htmlFor="org-edit-desc">Description</label>
          <textarea
            id="org-edit-desc"
            data-testid="org-edit-desc-input"
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
        <button data-testid="org-edit-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? 'Saving…' : 'Save'}
        </button>
      </div>
    </Dialog>
  );
}

function ConfirmStateDialog({
  orgId,
  org,
  action,
  onClose,
  onDone,
}: {
  orgId: string;
  org: OrganizationSummary;
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
        `/api/v1/admin/tenancy/organizations/${org.organizationId}:${action}`,
        orgId,
      );
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : `failed to ${action} organization`);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={`${action === 'disable' ? 'Disable' : 'Enable'} ${org.organizationId}?`}
      onClose={onClose}
      testId="org-confirm-dialog"
    >
      {action === 'disable' ? (
        <p>
          Disabling blocks new API keys and new inference service
          deployments for this organization. Existing resources stay
          readable and revocable.
        </p>
      ) : (
        <p>Enabling restores full write access for this organization.</p>
      )}
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" data-testid="org-confirm-cancel" onClick={onClose}>
          Cancel
        </button>
        <button
          className={action === 'disable' ? 'danger' : ''}
          data-testid="org-confirm-ok"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? 'Working…' : action === 'disable' ? 'Disable' : 'Enable'}
        </button>
      </div>
    </Dialog>
  );
}
