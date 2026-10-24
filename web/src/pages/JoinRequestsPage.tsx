// JoinRequestsPage is the admin console page for approving or rejecting
// users' requests to join an organization or a project. Organization
// admins manage their org's requests; project admins manage their
// project's requests.

import { useCallback, useEffect, useState } from 'react';
import { ErrorBanner, StateBadge } from '../components';
import { api } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';

interface JoinRequest {
  requestId: string;
  userId: string;
  targetType: string;
  targetId: string;
  status: string;
  message: string;
  createdAt: string;
}

export default function JoinRequestsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [requests, setRequests] = useState<JoinRequest[]>([]);
  const [targetType, setTargetType] = useState('organization');
  const [targetId, setTargetId] = useState('');
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
          onChange={(e) => setTargetType(e.target.value)}
        >
          <option value="organization">{t('joinreq.organization')}</option>
          <option value="project">{t('joinreq.project')}</option>
        </select>
        <input
          data-testid="joinreq-target-id"
          value={targetId}
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
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {requests.map((r) => (
                <tr key={r.requestId} data-testid={`joinreq-row-${r.requestId}`}>
                  <td className="mono">{r.userId}</td>
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
                      <>
                        <button
                          className="link"
                          data-testid={`joinreq-approve-${r.requestId}`}
                          onClick={() => void decide(r.requestId, 'approve')}
                        >
                          {t('joinreq.approve')}
                        </button>
                        <button
                          className="link danger"
                          data-testid={`joinreq-reject-${r.requestId}`}
                          onClick={() => void decide(r.requestId, 'reject')}
                        >
                          {t('joinreq.reject')}
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
    </div>
  );
}
