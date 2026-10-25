// Admin Audit Logs page (feature-15): the control-plane audit trail.

import { useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime } from '../api';
import { useI18n } from '../i18n';
import { StateBadge } from '../components';

interface AuditEvent {
  auditEventId: string;
  organizationId: string;
  actorUserId: string;
  actorDisplayName?: string;
  actorType: string;
  action: string;
  resourceType: string;
  resourceId: string;
  result: string;
  ipAddress: string;
  userAgent: string;
  metadata: string;
  createdAt: string;
}

export default function AuditLogsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // The governance view spans every organization; the filter narrows it on
  // demand (an operator investigating one tenant's trail).
  const [orgFilter, setOrgFilter] = useState('');
  const [orgList, setOrgList] = useState<string[]>([]);

  useEffect(() => {
    api
      .get<{ organizations?: { organizationId: string }[] }>(
        '/api/v1/admin/tenancy/organizations?page.limit=100',
        orgId,
      )
      .then((data) => setOrgList((data.organizations || []).map((o) => o.organizationId)))
      .catch(() => setOrgList([]));
  }, [api, orgId]);

  useEffect(() => {
    const query = orgFilter ? `organizationId=${encodeURIComponent(orgFilter)}&` : '';
    setLoading(true);
    setError('');
    api
      .get<{ auditEvents?: AuditEvent[] }>(`/api/v1/admin/audit/events?${query}page.limit=100`, orgId)
      .then((data) => setEvents(data.auditEvents || []))
      .catch((e) => setError(e instanceof Error ? e.message : t('audit.loadFailed')))
      .finally(() => setLoading(false));
  }, [api, orgId, orgFilter, t]);

  return (
    <div className="page">
      <h1>{t('audit.title')}</h1>
      <div className="toolbar" style={{ marginBottom: 14 }} data-testid="audit-filters">
        <label className="field">
          <span>{t('audit.filterOrganization')}</span>
          <select
            data-testid="audit-org-filter"
            value={orgFilter}
            onChange={(e) => setOrgFilter(e.target.value)}
          >
            <option value="">{t('audit.allOrganizations')}</option>
            {orgList.map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </select>
        </label>
      </div>
      {error && <div className="error">{error}</div>}
      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : events.length === 0 ? (
        <div className="empty" data-testid="audit-logs-empty">{t('audit.empty')}</div>
      ) : (
        <table className="table" data-testid="audit-logs-table">
          <thead>
            <tr>
              <th>{t('audit.colTime')}</th>
              <th>{t('audit.colAction')}</th>
              <th>{t('audit.colOrganization')}</th>
              <th>{t('audit.colActor')}</th>
              <th>{t('audit.colResource')}</th>
              <th>{t('audit.colResult')}</th>
              <th>{t('audit.colIp')}</th>
            </tr>
          </thead>
          <tbody>
            {events.map((e) => (
              <tr key={e.auditEventId} data-testid={`audit-event-row-${e.auditEventId}`}>
                <td>{formatTime(e.createdAt)}</td>
                <td>{e.action}</td>
                <td className="mono muted">{e.organizationId || '—'}</td>
                <td>
                  {e.actorDisplayName || e.actorUserId}
                  {e.actorDisplayName && (
                    <div className="muted small mono">{e.actorUserId}</div>
                  )}
                </td>
                <td>{e.resourceType}:{e.resourceId}</td>
                <td><StateBadge state={e.result} /></td>
                <td>{e.ipAddress}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
