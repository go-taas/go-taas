// Users page: the platform-level user directory with state filter and
// disable/enable lifecycle control. Users are created by SSO JIT
// provisioning or invitations; this page gives operators a read-only
// directory plus lifecycle control.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, formatTime, type PageMeta, type UserSummary } from '../api';
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
  const { t } = useI18n();
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
          <option value="active">active</option>
          <option value="disabled">disabled</option>
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
                <th>{t('users.colEmail')}</th>
                <th>{t('users.colState')}</th>
                <th>{t('users.colOrgs')}</th>
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
                  <td>{user.email || '—'}</td>
                  <td>
                    <StateBadge state={user.state} />
                  </td>
                  <td>{user.organizationCount}</td>
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