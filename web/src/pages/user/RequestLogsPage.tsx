// End-user request logs page (feature-17): own request logs against the
// user surface's metering API.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { formatTime, type RequestLog } from '../../api';
import { ErrorBanner } from '../../components';

interface ListResponse {
  requestLogs?: RequestLog[];
}

const ranges = [
  { id: '24h', hours: 24, label: 'ureqlogs.range24h' },
  { id: '7d', hours: 168, label: 'ureqlogs.range7d' },
  { id: '30d', hours: 720, label: 'ureqlogs.range30d' },
];

export default function RequestLogsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [logs, setLogs] = useState<RequestLog[]>([]);
  const [error, setError] = useState('');
  const [status, setStatus] = useState('');
  const [keyId, setKeyId] = useState('');
  const [modelId, setModelId] = useState('');
  const [range, setRange] = useState('24h');

  const load = useCallback(async () => {
    const selectedRange = ranges.find((item) => item.id === range) || ranges[0];
    const until = Math.floor(Date.now() / 1000);
    const params = new URLSearchParams({
      since: String(until - selectedRange.hours * 3600),
      until: String(until),
      'page.limit': '100',
    });
    if (status) params.set('status', status);
    if (keyId.trim()) params.set('api_key_id', keyId.trim());
    if (modelId.trim()) params.set('model_id', modelId.trim());
    try {
      const data = await api.get<ListResponse>(`/api/v1/metering/request-logs?${params.toString()}`, orgId);
      setLogs(data.requestLogs || []);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('ureqlogs.loadFailed'));
    }
  }, [api, orgId, range, status, keyId, modelId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="page">
      <div className="page-header"><h1>{t('ureqlogs.title')}</h1></div>
      {error && <ErrorBanner message={error} />}
      <div className="toolbar" data-testid="request-log-filters">
        {ranges.map((item) => (
          <button key={item.id} className={range === item.id ? '' : 'secondary'} data-testid={`request-log-range-${item.id}`} onClick={() => setRange(item.id)}>
            {t(item.label)}
          </button>
        ))}
        <select data-testid="request-log-filter-status" value={status} onChange={(event) => setStatus(event.target.value)}>
          <option value="">{t('ureqlogs.allStatuses')}</option>
          <option value="success">{t('ureqlogs.success')}</option>
          <option value="error">{t('ureqlogs.error')}</option>
          <option value="streaming">{t('ureqlogs.streaming')}</option>
        </select>
        <input data-testid="request-log-filter-key" placeholder={t('ureqlogs.keyPlaceholder')} value={keyId} onChange={(event) => setKeyId(event.target.value)} />
        <input data-testid="request-log-filter-model" placeholder={t('ureqlogs.modelPlaceholder')} value={modelId} onChange={(event) => setModelId(event.target.value)} />
      </div>
      {logs.length === 0 ? (
        <div className="empty" data-testid="request-logs-empty">{t('ureqlogs.empty')}</div>
      ) : (
        <table className="table" data-testid="request-logs-table">
          <thead>
            <tr>
              <th>{t('ureqlogs.colTime')}</th>
              <th>{t('ureqlogs.colRequest')}</th>
              <th>{t('ureqlogs.colKey')}</th>
              <th>{t('ureqlogs.colModel')}</th>
              <th>{t('ureqlogs.colStatus')}</th>
              <th>{t('ureqlogs.colTokens')}</th>
              <th>{t('ureqlogs.colLatency')}</th>
            </tr>
          </thead>
          <tbody>
            {logs.map((l) => (
              <tr key={l.requestLogId} data-testid={`request-log-row-${l.requestLogId}`}>
                <td>{formatTime(l.createdAt)}</td>
                <td>{l.requestId}</td>
                <td>{l.apiKeyId}</td>
                <td>{l.modelId}</td>
                <td>{l.status}</td>
                <td>{Number(l.promptTokens) + Number(l.completionTokens)}</td>
                <td>{t('ureqlogs.latencyMs', { latencyMs: l.latencyMs })}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
