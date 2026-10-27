// Organizations page: the platform-level organization registry with
// create, edit, disable/enable and usage counters.
// Implements docs/design/multi-tenancy.md FR1, FR2, AC1-AC5.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, formatTime, type OrganizationSummary, type PageMeta } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination, StateBadge } from '../components';

interface ListResponse {
  response: { code: number; message: string };
  organizations: OrganizationSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

// PLATFORM_ORG_ID is the dedicated organization that carries the
// platform-admin membership. The server never deletes it, so the console
// does not offer the action.
const PLATFORM_ORG_ID = 'platform';

export default function OrganizationsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
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
  const [deleteTarget, setDeleteTarget] = useState<OrganizationSummary | null>(null);

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
      setError(e instanceof Error ? e.message : t('orgs.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, state, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('orgs.title')}</h1>
          <div className="subtitle">{t('orgs.subtitle')}</div>
        </div>
        <button data-testid="create-org" onClick={() => setCreateOpen(true)}>
          {t('orgs.create')}
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
          <option value="all">{t('common.allStates')}</option>
          <option value="active">active</option>
          <option value="disabled">disabled</option>
        </select>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : orgs.length === 0 ? (
          <div className="empty-state" data-testid="orgs-empty">
            {t('orgs.empty')}
          </div>
        ) : (
          <table className="data" data-testid="orgs-table">
            <thead>
              <tr>
                <th>{t('orgs.colOrganization')}</th>
                <th>{t('orgs.colState')}</th>
                <th>{t('orgs.colApiKeys')}</th>
                <th>{t('orgs.colServices')}</th>
                <th>{t('orgs.colProjects')}</th>
                <th>{t('orgs.colCreated')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {orgs.map((org) => (
                <tr key={org.organizationId} data-testid={`org-row-${org.organizationId}`}>
                  <td>
                    <strong>{org.displayName || org.organizationId}</strong>
                    <div className="muted small mono">{org.organizationId}</div>
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
                      {t('common.edit')}
                    </button>
                    {org.state === 'active' ? (
                      <button
                        className="link danger"
                        data-testid={`org-disable-${org.organizationId}`}
                        onClick={() =>
                          setConfirmTarget({ org, action: 'disable' })
                        }
                      >
                        {t('common.disable')}
                      </button>
                    ) : (
                      <button
                        className="link"
                        data-testid={`org-enable-${org.organizationId}`}
                        onClick={() =>
                          setConfirmTarget({ org, action: 'enable' })
                        }
                      >
                        {t('common.enable')}
                      </button>
                    )}
                    <button
                      className="link danger"
                      data-testid={`org-delete-${org.organizationId}`}
                      disabled={org.organizationId === PLATFORM_ORG_ID}
                      title={
                        org.organizationId === PLATFORM_ORG_ID
                          ? t('orgs.platformProtected')
                          : undefined
                      }
                      onClick={() => setDeleteTarget(org)}
                    >
                      {t('common.delete')}
                    </button>
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

      {deleteTarget && (
        <DeleteOrgDialog
          orgId={orgId}
          org={deleteTarget}
          onClose={() => setDeleteTarget(null)}
          onDone={() => {
            setDeleteTarget(null);
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
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    if (!name.trim()) {
      setError(t('orgs.validationDisplayName'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.post('/api/v1/admin/tenancy/organizations', orgId, {
        displayName: name.trim(),
        description: description.trim(),
      });
      onDone();
    } catch (e) {
      // 10015: the organization id already exists.
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('orgs.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('orgs.createTitle')} onClose={onClose} testId="create-org-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="org-name">{t('orgs.fieldDisplayName')}</label>
          <input
            id="org-name"
            data-testid="org-name-input"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('orgs.placeholderDisplayName')}
          />
        </div>
        <div className="form-field full">
          <label htmlFor="org-desc">{t('common.descriptionOptional')}</label>
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
          {t('common.cancel')}
        </button>
        <button data-testid="org-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.creating') : t('common.create')}
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
  const { t } = useI18n();
  const [name, setName] = useState(org.displayName);
  const [description, setDescription] = useState(org.description || '');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    if (!name.trim()) {
      setError(t('orgs.validationDisplayName'));
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
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('orgs.updateFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('orgs.editTitle', { orgId: org.organizationId })} onClose={onClose} testId="edit-org-dialog">
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="org-edit-name">{t('orgs.fieldDisplayName')}</label>
          <input
            id="org-edit-name"
            data-testid="org-edit-name-input"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <div className="form-field full">
          <label htmlFor="org-edit-desc">{t('orgs.fieldDescription')}</label>
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
          {t('common.cancel')}
        </button>
        <button data-testid="org-edit-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.saving') : t('common.save')}
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
  const { t } = useI18n();
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
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('orgs.actionFailed', { action }));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={t(action === 'disable' ? 'orgs.disableTitle' : 'orgs.enableTitle', { orgId: org.organizationId })}
      onClose={onClose}
      testId="org-confirm-dialog"
    >
      {action === 'disable' ? (
        <p>{t('orgs.disableBody')}</p>
      ) : (
        <p>{t('orgs.enableBody')}</p>
      )}
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" data-testid="org-confirm-cancel" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          className={action === 'disable' ? 'danger' : ''}
          data-testid="org-confirm-ok"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? t('common.working') : t(action === 'disable' ? 'common.disable' : 'common.enable')}
        </button>
      </div>
    </Dialog>
  );
}

// DeleteOrgDialog permanently deletes an organization. The server refuses
// the platform organization and any organization that still owns API keys
// or inference services; the error is shown inline.
function DeleteOrgDialog({
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
  const { t } = useI18n();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [force, setForce] = useState(false);

  // hasAssets drives both the warning and the force prompt: a plain delete
  // is refused while the organization still owns API keys or services.
  const hasAssets =
    parseInt(org.apiKeyCount || '0', 10) > 0 || parseInt(org.inferenceServiceCount || '0', 10) > 0;

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      const path = `/api/v1/admin/tenancy/organizations/${org.organizationId}`;
      await api.del(force ? `${path}?force=true` : path, orgId);
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('orgs.deleteFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={t('orgs.deleteTitle', { name: org.displayName || org.organizationId })}
      onClose={onClose}
      testId="org-delete-dialog"
    >
      <p>{t('orgs.deleteBody')}</p>
      {hasAssets && (
        <div className="warning-banner" data-testid="org-delete-assets-warning">
          <p>
            {t('orgs.deleteAssets', {
              keys: org.apiKeyCount,
              services: org.inferenceServiceCount,
            })}
          </p>
          <label className="checkbox-row">
            <input
              type="checkbox"
              data-testid="org-delete-force"
              checked={force}
              onChange={(e) => setForce(e.target.checked)}
            />
            <span>{t('orgs.deleteForce')}</span>
          </label>
        </div>
      )}
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" data-testid="org-delete-cancel" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          className="danger"
          data-testid="org-delete-confirm"
          disabled={submitting || (hasAssets && !force)}
          onClick={() => void submit()}
        >
          {submitting ? t('common.working') : t('common.delete')}
        </button>
      </div>
    </Dialog>
  );
}
