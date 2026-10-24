// MyRequestsPage is the end-user console "Join requests" page: apply to
// join an organization or a project, and track the requests' status.

import { useCallback, useEffect, useState } from 'react';
import { ErrorBanner, StateBadge } from '../../components';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';

interface JoinRequest {
  requestId: string;
  targetType: string;
  targetId: string;
  status: string;
  message: string;
  createdAt: string;
}

export default function MyRequestsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [requests, setRequests] = useState<JoinRequest[]>([]);
  const [targetType, setTargetType] = useState('organization');
  const [targetId, setTargetId] = useState('');
  const [message, setMessage] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [info, setInfo] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{ requests: JoinRequest[] }>(
        '/api/v1/tenancy/join-requests',
        orgId,
      );
      setRequests(data.requests || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('myrequests.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const apply = async () => {
    if (!targetId.trim()) return;
    setError('');
    setInfo('');
    try {
      await api.post('/api/v1/tenancy/join-requests', orgId, {
        targetType,
        targetId: targetId.trim(),
        message: message.trim(),
      });
      setInfo(t('myrequests.submitted'));
      setTargetId('');
      setMessage('');
      await load();
    } catch (e) {
      const code = (e as { code?: number }).code;
      setError(code === 10029 ? t('myrequests.errorDuplicate') : t('myrequests.submitFailed'));
    }
  };

  return (
    <div className="page" data-testid="my-requests-page">
      <h1>{t('myrequests.title')}</h1>
      {error && <ErrorBanner message={error} />}
      {info && <div className="success" data-testid="my-requests-info">{info}</div>}

      <div className="panel" style={{ marginBottom: 16 }}>
        <h2>{t('myrequests.applyTitle')}</h2>
        <div className="form">
          <div className="field">
            <label>{t('myrequests.targetType')}</label>
            <select
              data-testid="join-target-type"
              value={targetType}
              onChange={(e) => setTargetType(e.target.value)}
            >
              <option value="organization">{t('myrequests.organization')}</option>
              <option value="project">{t('myrequests.project')}</option>
            </select>
          </div>
          <div className="field">
            <label>{t('myrequests.targetId')}</label>
            <input
              data-testid="join-target-id"
              value={targetId}
              onChange={(e) => setTargetId(e.target.value)}
              placeholder={t('myrequests.targetIdPlaceholder')}
            />
          </div>
          <div className="field">
            <label>{t('myrequests.message')}</label>
            <input
              data-testid="join-message"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
            />
          </div>
          <button data-testid="join-submit" disabled={!targetId.trim()} onClick={() => void apply()}>
            {t('myrequests.submit')}
          </button>
        </div>
      </div>

      <div className="panel">
        <h2>{t('myrequests.listTitle')}</h2>
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : requests.length === 0 ? (
          <div className="empty-state" data-testid="my-requests-empty">
            {t('myrequests.empty')}
          </div>
        ) : (
          <table className="data" data-testid="my-requests-table">
            <thead>
              <tr>
                <th>{t('myrequests.colTarget')}</th>
                <th>{t('myrequests.colStatus')}</th>
                <th>{t('myrequests.colCreated')}</th>
              </tr>
            </thead>
            <tbody>
              {requests.map((r) => (
                <tr key={r.requestId} data-testid={`my-request-row-${r.requestId}`}>
                  <td>
                    <span className="muted">{r.targetType}</span>{' '}
                    <strong className="mono">{r.targetId}</strong>
                  </td>
                  <td>
                    <StateBadge state={r.status} />
                  </td>
                  <td>{new Date(parseInt(r.createdAt, 10) * 1000).toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
