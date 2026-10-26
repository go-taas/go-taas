// Invitations page: the active organization's invitation pipeline with
// invite, resend and revoke. Implements docs/design/org-members-rbac.md
// FR2, AC4-AC7.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type Invitation } from '../api';
import { OrganizationPicker, useOrganizationPicker } from '../org';
import { useAdminScope } from '../admin-scope';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, StateBadge } from '../components';

const ASSIGNABLE_ROLES = ['admin', 'member', 'viewer'];

export default function InvitationsPage() {
  const { scope } = useAdminScope();
  // In the organization console the pipeline is fixed to that organization.
  const scopedOrg = scope?.scopeType === 'organization' ? scope.organizationId : '';
  const { orgId: pickerOrg, orgs, choose } = useOrganizationPicker();
  const orgId = scopedOrg || pickerOrg;
  const { t, tEnum } = useI18n();
  const [invitations, setInvitations] = useState<Invitation[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [inviteOpen, setInviteOpen] = useState(false);
  const [inviteEmail, setInviteEmail] = useState('');
  const [inviteRole, setInviteRole] = useState('member');
  const [newToken, setNewToken] = useState('');
  const [revokeTarget, setRevokeTarget] = useState<Invitation | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{ response: { code: number; message: string }; invitations: Invitation[] }>(
        `/api/v1/admin/tenancy/organizations/${orgId}/invitations?page.limit=100`,
        orgId,
      );
      setInvitations(data.invitations || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('invitations.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const createInvite = async () => {
    setError('');
    setNewToken('');
    try {
      const data = await api.post<{ response: { code: number; message: string }; token: string }>(
        `/api/v1/admin/tenancy/organizations/${orgId}/invitations`,
        orgId,
        { email: inviteEmail, role: inviteRole },
      );
      setNewToken(data.token);
      setInviteOpen(false);
      setInviteEmail('');
      setInviteRole('member');
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('invitations.createFailed'));
    }
  };

  const resend = async (invitationId: string) => {
    setError('');
    setNewToken('');
    try {
      const data = await api.post<{ response: { code: number; message: string }; token: string }>(
        `/api/v1/admin/tenancy/invitations/${invitationId}:resend`,
        orgId,
        {},
      );
      setNewToken(data.token);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('invitations.resendFailed'));
    }
  };

  const revoke = async () => {
    if (!revokeTarget) return;
    setError('');
    try {
      await api.post(
        `/api/v1/admin/tenancy/invitations/${revokeTarget.invitationId}:revoke`,
        orgId,
        {},
      );
      setRevokeTarget(null);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('invitations.revokeFailed'));
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('invitations.title')}</h1>
          <div
            className="subtitle"
            dangerouslySetInnerHTML={{ __html: t('invitations.subtitle', { orgId }) }}
          />
        </div>
        <div className="toolbar">
          {!scopedOrg && (
            <OrganizationPicker
              orgs={orgs}
              value={orgId}
              onChange={choose}
              testId="invitations-org-picker"
            />
          )}
          <button data-testid="invite-button" onClick={() => setInviteOpen(true)}>
            {t('invitations.invite')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}
      {newToken && (
        <div
          className="notice"
          data-testid="invite-token"
          dangerouslySetInnerHTML={{ __html: t('invitations.tokenNotice', { token: newToken }) }}
        />
      )}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : invitations.length === 0 ? (
          <div className="empty-state" data-testid="invitations-empty">
            {t('invitations.empty')}
          </div>
        ) : (
          <table className="data" data-testid="invitations-table">
            <thead>
              <tr>
                <th>{t('invitations.colEmail')}</th>
                <th>{t('invitations.colRole')}</th>
                <th>{t('invitations.colStatus')}</th>
                <th>{t('invitations.colExpires')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {invitations.map((inv) => (
                <tr key={inv.invitationId} data-testid={`invitation-row-${inv.invitationId}`}>
                  <td>{inv.email}</td>
                  <td>
                    <StateBadge state={inv.role} />
                  </td>
                  <td>
                    <StateBadge state={inv.status} />
                  </td>
                  <td>{new Date(parseInt(inv.expiresAt, 10) * 1000).toLocaleString()}</td>
                  <td>
                    {inv.status === 'pending' && (
                      <>
                        <button
                          className="link"
                          data-testid={`invite-resend-${inv.invitationId}`}
                          onClick={() => void resend(inv.invitationId)}
                        >
                          {t('invitations.resend')}
                        </button>
                        <button
                          className="link danger"
                          data-testid={`invite-revoke-${inv.invitationId}`}
                          onClick={() => setRevokeTarget(inv)}
                        >
                          {t('invitations.revoke')}
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {inviteOpen && (
        <Dialog title={t('invitations.inviteTitle')} onClose={() => setInviteOpen(false)}>
          <div className="form">
            <label>
              {t('invitations.fieldEmail')}
              <input
                data-testid="invite-email-input"
                value={inviteEmail}
                onChange={(e) => setInviteEmail(e.target.value)}
                placeholder={t('invitations.placeholderEmail')}
              />
            </label>
            <label>
              {t('invitations.fieldRole')}
              <select
                data-testid="invite-role-select"
                value={inviteRole}
                onChange={(e) => setInviteRole(e.target.value)}
              >
                {ASSIGNABLE_ROLES.map((r) => (
                  <option key={r} value={r}>
                    {tEnum('state', r)}
                  </option>
                ))}
              </select>
            </label>
            <button data-testid="invite-save" onClick={() => void createInvite()}>
              {t('invitations.invite')}
            </button>
          </div>
        </Dialog>
      )}

      {revokeTarget && (
        <Dialog title={t('invitations.revokeTitle')} onClose={() => setRevokeTarget(null)}>
          <p
            dangerouslySetInnerHTML={{
              __html: t('invitations.revokeBody', { email: revokeTarget.email }),
            }}
          />
          <button
            className="danger"
            data-testid="invite-revoke-confirm"
            onClick={() => void revoke()}
          >
            {t('invitations.revoke')}
          </button>
        </Dialog>
      )}
    </div>
  );
}
