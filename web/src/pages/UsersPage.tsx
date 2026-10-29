// Users page: the platform-level user directory with state filter and
// disable/enable lifecycle control. Users are created by SSO JIT
// provisioning or invitations; this page gives operators a read-only
// directory plus lifecycle control.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, formatTime, type OrganizationSummary, type PageMeta, type UserSummary } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination, StateBadge } from '../components';

interface ListResponse {
  response: { code: number; message: string };
  users: UserSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

export default function UsersPage() {
  const { orgId } = useOrg();
  const { t, tEnum } = useI18n();
  const [users, setUsers] = useState<UserSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [state, setState] = useState('all');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [confirmTarget, setConfirmTarget] = useState<{
    user: UserSummary;
    action: 'disable' | 'enable' | 'delete';
  } | null>(null);
  // orgTarget is the user whose organization placement is being edited.
  const [orgTarget, setOrgTarget] = useState<UserSummary | null>(null);

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
        `/api/v1/admin/auth/users?${params.toString()}`,
        orgId,
      );
      setUsers(data.users || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('users.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, state, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const setStateAction = async () => {
    if (!confirmTarget) return;
    setError('');
    try {
      const { user, action } = confirmTarget;
      if (action === 'delete') {
        await api.del(`/api/v1/admin/auth/users/${user.userId}`, orgId);
      } else {
        await api.post(
          `/api/v1/admin/auth/users/${user.userId}:${action}`,
          orgId,
          {},
        );
      }
      setConfirmTarget(null);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('users.stateFailed'));
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('users.title')}</h1>
          <div className="subtitle">{t('users.subtitle')}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <select
          data-testid="user-state-filter"
          value={state}
          onChange={(e) => {
            setState(e.target.value);
            setOffset(0);
          }}
        >
          <option value="all">{t('common.allStates')}</option>
          <option value="active">{tEnum('state', 'active')}</option>
          <option value="disabled">{tEnum('state', 'disabled')}</option>
        </select>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : users.length === 0 ? (
          <div className="empty-state" data-testid="users-empty">
            {t('users.empty')}
          </div>
        ) : (
          <table className="data" data-testid="users-table">
            <thead>
              <tr>
                <th>{t('users.colUser')}</th>
                <th>{t('users.colOrganization')}</th>
                <th>{t('users.colProject')}</th>
                <th>{t('users.colEmail')}</th>
                <th>{t('users.colState')}</th>
                <th>{t('users.colCreated')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {users.map((user) => (
                <tr key={user.userId} data-testid={`user-row-${user.userId}`}>
                  <td>
                    <strong>{user.displayName || user.username || user.userId}</strong>
                    <div className="muted small">
                      {user.username}
                      {user.displayName ? '' : ''}
                    </div>
                    <div className="muted small mono">{user.userId}</div>
                  </td>
                  {/* A platform administrator places people into an
                      organization and a project from this page, so the row
                      shows where each account currently sits. The API sends
                      the display name next to the id; an account that joined
                      nothing shows a dash rather than an empty cell. */}
                  <td data-testid={`user-org-cell-${user.userId}`}>
                    {user.organizationName || user.organizationId || '—'}
                    {user.organizationName && user.organizationId && (
                      <div className="muted small mono">{user.organizationId}</div>
                    )}
                  </td>
                  <td data-testid={`user-project-cell-${user.userId}`}>
                    {user.projectName || user.projectId || '—'}
                    {user.projectName && user.projectId && (
                      <div className="muted small mono">{user.projectId}</div>
                    )}
                  </td>
                  <td>{user.email || '—'}</td>
                  <td>
                    <StateBadge state={user.state} />
                  </td>
                  <td>{formatTime(user.createdAt)}</td>
                  <td>
                    {user.state === 'active' ? (
                      <button
                        className="link danger"
                        data-testid={`user-disable-${user.userId}`}
                        onClick={() => setConfirmTarget({ user, action: 'disable' })}
                      >
                        {t('common.disable')}
                      </button>
                    ) : (
                      <button
                        className="link"
                        data-testid={`user-enable-${user.userId}`}
                        onClick={() => setConfirmTarget({ user, action: 'enable' })}
                      >
                        {t('common.enable')}
                      </button>
                    )}
                    <button
                      className="link"
                      data-testid={`user-org-${user.userId}`}
                      onClick={() => setOrgTarget(user)}
                    >
                      {t('users.manageOrg')}
                    </button>
                    <button
                      className="link danger"
                      data-testid={`user-delete-${user.userId}`}
                      onClick={() => setConfirmTarget({ user, action: 'delete' })}
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

      {orgTarget && (
        <AddToOrgDialog
          orgId={orgId}
          user={orgTarget}
          onClose={() => setOrgTarget(null)}
          onDone={() => {
            setOrgTarget(null);
            void load();
          }}
        />
      )}

      {confirmTarget && (
        <Dialog
          title={t('users.stateTitle')}
          onClose={() => setConfirmTarget(null)}
          testId="user-state-dialog"
        >
          {confirmTarget.action === 'delete' ? (
            <p data-testid="user-delete-body">
              {t('users.deleteBody', { username: confirmTarget.user.username })}
            </p>
          ) : (
            <p
              dangerouslySetInnerHTML={{
                __html: t('users.stateBody', {
                  action: confirmTarget.action,
                  username: confirmTarget.user.username,
                }),
              }}
            />
          )}
          <div className="dialog-actions">
            <button
              className="secondary"
              data-testid="user-state-cancel"
              onClick={() => setConfirmTarget(null)}
            >
              {t('common.cancel')}
            </button>
            <button
              className={confirmTarget.action === 'enable' ? '' : 'danger'}
              data-testid="user-state-ok"
              onClick={() => void setStateAction()}
            >
              {confirmTarget.action === 'delete'
                ? t('common.delete')
                : t(confirmTarget.action === 'disable' ? 'common.disable' : 'common.enable')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}
// ASSIGNABLE_ROLES are the organization roles a platform administrator can
// grant. "owner" is protected: an organization has exactly one owner and
// ownership is not handed over from this dialog.
const ASSIGNABLE_ROLES = ['admin', 'member', 'viewer'];

// AddToOrgDialog places a platform user into an organization with a role,
// which is how a platform administrator assigns an organization's
// administrator. The server accepts a platform administrator for any
// organization, and enforces the single-organization rule.
function AddToOrgDialog({
  orgId,
  user,
  onClose,
  onDone,
}: {
  orgId: string;
  user: UserSummary;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t, tEnum } = useI18n();
  const [orgs, setOrgs] = useState<OrganizationSummary[]>([]);
  const [selected, setSelected] = useState('');
  const [role, setRole] = useState('member');
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let cancelled = false;
    api
      .get<{ organizations: OrganizationSummary[] }>(
        '/api/v1/admin/tenancy/organizations?page.limit=100&state=active',
        orgId,
      )
      .then((data) => {
        if (cancelled) return;
        // The system platform organization is not a place to put people:
        // it is the tenancy anchor for the platform itself.
        const list = (data.organizations || []).filter(
          (o) => o.organizationId !== 'platform',
        );
        setOrgs(list);
        // Open on the user's current organization when they have one, so the
        // dialog shows where they are before the operator changes it.
        setSelected(
          (prev) =>
            prev ||
            list.find((o) => o.organizationId === user.organizationId)?.organizationId ||
            list[0]?.organizationId ||
            '',
        );
      })
      .catch(() => setOrgs([]))
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [orgId, user.organizationId]);

  // A save moves the account: joining an organization clears the previous one
  // (and the projects the user held there), so the operator is told before,
  // not left wondering why the old roster still listed them.
  const movingFrom = user.organizationId && user.organizationId !== selected ? user.organizationId : '';
  // The warning names organizations, not ids: the operator picked them by
  // name and should read them back the same way.
  const orgLabel = (id: string) => {
    const org = orgs.find((o) => o.organizationId === id);
    return org?.displayName && org.displayName !== id ? org.displayName : id;
  };

  const submit = async () => {
    if (!selected) return;
    setSubmitting(true);
    setError('');
    try {
      if (selected === user.organizationId) {
        // The user is already in this organization, so the only thing that
        // can change is the role: posting a "new member" would be refused as
        // a duplicate. The owner's role is not editable here (the server
        // protects it with 10032), which is the same rule the roster applies.
        await api.patch(
          `/api/v1/admin/tenancy/organizations/${selected}/members/${user.userId}`,
          orgId,
          { role },
        );
      } else {
        await api.post(`/api/v1/admin/tenancy/organizations/${selected}/members`, orgId, {
          userId: user.userId,
          role,
        });
      }
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('users.addOrgFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={t('users.addOrgTitle', { username: user.username || user.userId })}
      onClose={onClose}
      testId="user-add-org-dialog"
    >
      <p className="muted">{t('users.addOrgHint')}</p>
      {error && <ErrorBanner message={error} />}
      <div className="form-grid">
        <label>
          {t('users.org')}
          <select
            data-testid="user-add-org-select"
            value={selected}
            disabled={loading || orgs.length === 0}
            onChange={(e) => setSelected(e.target.value)}
          >
            {orgs.length === 0 && <option value="">{t('users.noOrgs')}</option>}
            {orgs.map((o) => (
              <option key={o.organizationId} value={o.organizationId}>
                {o.displayName && o.displayName !== o.organizationId
                  ? `${o.displayName} (${o.organizationId})`
                  : o.organizationId}
              </option>
            ))}
          </select>
        </label>
        {movingFrom && (
          <div className="form-field full">
            <div className="error" data-testid="user-add-org-move-warning">
              {t('users.addOrgMoveWarning', {
                from: user.organizationName || orgLabel(movingFrom),
                to: orgLabel(selected),
              })}
            </div>
          </div>
        )}
        <label>
          {t('users.role')}
          <select
            data-testid="user-add-org-role"
            value={role}
            onChange={(e) => setRole(e.target.value)}
          >
            {ASSIGNABLE_ROLES.map((r) => (
              <option key={r} value={r}>
                {tEnum('state', r)}
              </option>
            ))}
          </select>
        </label>
      </div>
      <div className="dialog-actions">
        <button className="secondary" data-testid="user-add-org-cancel" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="user-add-org-confirm"
          disabled={submitting || loading || !selected}
          onClick={() => void submit()}
        >
          {submitting ? t('common.working') : t('users.addToOrg')}
        </button>
      </div>
    </Dialog>
  );
}
