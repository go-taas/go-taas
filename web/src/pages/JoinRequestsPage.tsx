// JoinRequestsPage is the admin console page for approving or rejecting
// users' requests to join an organization or a project. Organization
// admins manage their org's requests; project admins manage their
// project's requests.

import { useCallback, useEffect, useState } from 'react';
import { ErrorBanner, RowActionsMenu, StateBadge } from '../components';
import { api } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { useAdminScope } from '../admin-scope';

interface JoinRequest {
  requestId: string;
  userId: string;
  displayName?: string;
  targetType: string;
  targetId: string;
  status: string;
  message: string;
  createdAt: string;
}

export default function JoinRequestsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const { scope } = useAdminScope();
  // When the console is scoped to an organization or project, the target
  // is fixed to that scope so the admin cannot accidentally query another.
  const scopedTargetType =
    scope?.scopeType === 'project' ? 'project' : scope?.scopeType === 'organization' ? 'organization' : '';
  const scopedTargetId =
    scope?.scopeType === 'project' ? scope.projectId : scope?.scopeType === 'organization' ? scope.organizationId : '';
  const [requests, setRequests] = useState<JoinRequest[]>([]);
  const [targetType, setTargetType] = useState(scopedTargetType || 'organization');
  const [targetId, setTargetId] = useState(scopedTargetId);
  const [status, setStatus] = useState('pending');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    if (!targetId.trim()) {
      setRequests([]);
      return;
    }
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams({
        targetType,
        targetId: targetId.trim(),
        status,
      });
      const data = await api.get<{ requests: JoinRequest[] }>(
        `/api/v1/admin/tenancy/join-requests?${params.toString()}`,
        orgId,
      );
      setRequests(data.requests || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('joinreq.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, targetId, targetType, status, t]);

  useEffect(() => {
    if (scopedTargetType) {
      setTargetType(scopedTargetType);
      setTargetId(scopedTargetId);
    }
  }, [scopedTargetType, scopedTargetId]);

  useEffect(() => {
    void load();
  }, [load]);

  const decide = async (id: string, action: 'approve' | 'reject') => {
    setError('');
    try {
      await api.post(`/api/v1/admin/tenancy/join-requests/${id}:${action}`, orgId, {});
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('joinreq.decideFailed'));
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('joinreq.title')}</h1>
          <div className="subtitle">{t('joinreq.subtitle')}</div>
        </div>
      </div>
      {error && <ErrorBanner message={error} />}

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <select
          data-testid="joinreq-target-type"
          value={targetType}
          disabled={Boolean(scopedTargetType)}
          onChange={(e) => setTargetType(e.target.value)}
        >
          <option value="organization">{t('joinreq.organization')}</option>
          <option value="project">{t('joinreq.project')}</option>
        </select>
        <input
          data-testid="joinreq-target-id"
          value={targetId}
          disabled={Boolean(scopedTargetType)}
          onChange={(e) => setTargetId(e.target.value)}
          placeholder={t('joinreq.targetIdPlaceholder')}
        />
        <select
          data-testid="joinreq-status"
          value={status}
          onChange={(e) => setStatus(e.target.value)}
        >
          <option value="pending">pending</option>
          <option value="approved">approved</option>
          <option value="rejected">rejected</option>
        </select>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : requests.length === 0 ? (
          <div className="empty-state" data-testid="joinreq-empty">
            {t('joinreq.empty')}
          </div>
        ) : (
          <table className="data" data-testid="joinreq-table">
            <thead>
              <tr>
                <th>{t('joinreq.colUser')}</th>
                <th>{t('joinreq.colTarget')}</th>
                <th>{t('joinreq.colStatus')}</th>
                <th>{t('joinreq.colCreated')}</th>
                <th className="col-actions">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {requests.map((r) => (
                <tr key={r.requestId} data-testid={`joinreq-row-${r.requestId}`}>
                  <td>
                    <strong>{r.displayName || r.userId}</strong>
                    <div className="muted small mono">{r.userId}</div>
                  </td>
                  <td>
                    <span className="muted">{r.targetType}</span>{' '}
                    <strong className="mono">{r.targetId}</strong>
                  </td>
                  <td>
                    <StateBadge state={r.status} />
                  </td>
                  <td>{new Date(parseInt(r.createdAt, 10) * 1000).toLocaleString()}</td>
                  <td>
                    {r.status === 'pending' && (
                      <RowActionsMenu
                        label={t('common.actionsFor', { name: r.displayName || r.userId || r.requestId })}
                        testId={`joinreq-actions-${r.requestId}`}
                        actions={[
                          {
                            key: `joinreq-approve-${r.requestId}`,
                            label: t('joinreq.approve'),
                            onClick: () => void decide(r.requestId, 'approve'),
                          },
                          {
                            key: `joinreq-reject-${r.requestId}`,
                            label: t('joinreq.reject'),
                            danger: true,
                            onClick: () => void decide(r.requestId, 'reject'),
                          },
                        ]}
                      />
                    )}
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
