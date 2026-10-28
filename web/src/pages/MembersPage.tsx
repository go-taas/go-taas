// Members page: the active organization's roster with add, role change
// and remove. Implements docs/design/org-members-rbac.md FR1, AC1-AC3.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type OrgMember } from '../api';
import { OrganizationPicker, organizationLabel, useOrganizationPicker } from '../org';
import { useAdminScope } from '../admin-scope';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, StateBadge } from '../components';

const ASSIGNABLE_ROLES = ['admin', 'member', 'viewer'];

export default function MembersPage() {
  const { scope } = useAdminScope();
  // In the organization console the roster is fixed to that organization.
  const scopedOrg = scope?.scopeType === 'organization' ? scope.organizationId : '';
  // The organization may be preselected with ?org=<id> (the platform
  // console links here from an organization row).
  const requestedOrg =
    typeof window !== 'undefined'
      ? new URLSearchParams(window.location.search).get('org') || ''
      : '';
  const { orgId: pickerOrg, orgs, choose } = useOrganizationPicker(requestedOrg);
  const orgId = scopedOrg || pickerOrg;
  // The roster is described by the organization's name, never by its id.
  // The directory supplies it; the console scope carries one as a fallback
  // while the directory call is still in flight.
  const orgName = organizationLabel(
    orgs,
    orgId,
    scope?.scopeType === 'organization' && scope.organizationId === orgId ? scope.displayName : '',
  );
  const { t, tEnum } = useI18n();
  const [members, setMembers] = useState<OrgMember[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [addOpen, setAddOpen] = useState(false);
  const [addUserId, setAddUserId] = useState('');
  const [addRole, setAddRole] = useState('member');
  const [removeTarget, setRemoveTarget] = useState<OrgMember | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{ response: { code: number; message: string }; members: OrgMember[] }>(
        `/api/v1/admin/tenancy/organizations/${orgId}/members?page.limit=100`,
        orgId,
      );
      setMembers(data.members || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('members.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const addMember = async () => {
    setError('');
    try {
      await api.post(
        `/api/v1/admin/tenancy/organizations/${orgId}/members`,
        orgId,
        { userId: addUserId, role: addRole },
      );
      setAddOpen(false);
      setAddUserId('');
      setAddRole('member');
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('members.addFailed'));
    }
  };

  const changeRole = async (userId: string, role: string) => {
    setError('');
    try {
      await api.patch(
        `/api/v1/admin/tenancy/organizations/${orgId}/members/${userId}`,
        orgId,
        { role },
      );
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('members.roleFailed'));
    }
  };

  const removeMember = async () => {
    if (!removeTarget) return;
    setError('');
    try {
      await api.del(
        `/api/v1/admin/tenancy/organizations/${orgId}/members/${removeTarget.userId}`,
        orgId,
      );
      setRemoveTarget(null);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('members.removeFailed'));
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('members.title')}</h1>
          <div
            className="subtitle"
            dangerouslySetInnerHTML={{ __html: t('members.subtitle', { orgName }) }}
          />
        </div>
        <div className="toolbar">
          {!scopedOrg && (
            <OrganizationPicker
              orgs={orgs}
              value={orgId}
              onChange={choose}
              testId="members-org-picker"
            />
          )}
          <button data-testid="add-member-button" onClick={() => setAddOpen(true)}>
            {t('members.add')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : members.length === 0 ? (
          <div className="empty-state" data-testid="members-empty">
            {t('members.empty')}
          </div>
        ) : (
          <table className="data" data-testid="org-members-table">
            <thead>
              <tr>
                <th>{t('members.colUser')}</th>
                <th>{t('members.colRole')}</th>
                <th>{t('members.colJoined')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {members.map((m) => (
                <tr key={m.userId} data-testid={`org-member-row-${m.userId}`}>
                  <td>
                    <strong>{m.displayName || m.userId}</strong>
                    <div className="muted small mono">{m.userId}</div>
                  </td>
                  <td>
                    <StateBadge state={m.role} />
                    {m.role === 'owner' && (
                      <select
                        className="member-role-select"
                        data-testid={`member-role-select-${m.userId}`}
                        value={m.role}
                        disabled
                      >
                        <option value="owner">{t('state.owner')}</option>
                      </select>
                    )}
                    {m.role !== 'owner' && (
                      <select
                        className="member-role-select"
                        data-testid={`member-role-select-${m.userId}`}
                        value={m.role}
                        onChange={(e) => changeRole(m.userId, e.target.value)}
                      >
                        {ASSIGNABLE_ROLES.map((r) => (
                          <option key={r} value={r}>
                            {tEnum('state', r)}
                          </option>
                        ))}
                      </select>
                    )}
                  </td>
                  <td>{new Date(parseInt(m.joinedAt, 10) * 1000).toLocaleString()}</td>
                  <td>
                    {m.role !== 'owner' && (
                      <button
                        className="link danger"
                        data-testid={`member-remove-${m.userId}`}
                        onClick={() => setRemoveTarget(m)}
                      >
                        {t('members.remove')}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {addOpen && (
        <Dialog title={t('members.addTitle')} onClose={() => setAddOpen(false)}>
          <div className="form">
            <label>
              {t('members.fieldUserId')}
              <input
                data-testid="add-member-user-id"
                value={addUserId}
                onChange={(e) => setAddUserId(e.target.value)}
                placeholder={t('members.placeholderUserId')}
              />
            </label>
            <label>
              {t('members.fieldRole')}
              <select
                data-testid="add-member-role"
                value={addRole}
                onChange={(e) => setAddRole(e.target.value)}
              >
                {ASSIGNABLE_ROLES.map((r) => (
                  <option key={r} value={r}>
                    {tEnum('state', r)}
                  </option>
                ))}
              </select>
            </label>
            <button data-testid="add-member-save" onClick={() => void addMember()}>
              {t('members.add')}
            </button>
          </div>
        </Dialog>
      )}

      {removeTarget && (
        <Dialog title={t('members.removeTitle')} onClose={() => setRemoveTarget(null)}>
          <p
            dangerouslySetInnerHTML={{
              __html: t('members.removeBody', {
                userId: removeTarget.displayName || removeTarget.userId,
                orgName,
              }),
            }}
          />
          <button
            className="danger"
            data-testid="member-remove-confirm"
            onClick={() => void removeMember()}
          >
            {t('members.remove')}
          </button>
        </Dialog>
      )}
    </div>
  );
}
