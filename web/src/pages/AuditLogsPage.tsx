// Admin Audit Logs page (feature-15): the control-plane audit trail.
//
// Rows are rendered for humans, not as raw records: the action becomes a
// sentence, the organization shows its display name, and the resource
// shows its name (the API resolves names for the resource types that have
// one) instead of a UUID.

import { useEffect, useMemo, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime, type OrganizationSummary } from '../api';
import { useI18n } from '../i18n';
import { StateBadge } from '../components';

interface AuditEvent {
  auditEventId: string;
  organizationId: string;
  organizationDisplayName?: string;
  actorUserId: string;
  actorDisplayName?: string;
  actorType: string;
  action: string;
  resourceType: string;
  resourceId: string;
  resourceDisplayName?: string;
  result: string;
  ipAddress: string;
  userAgent: string;
  metadata: string;
  createdAt: string;
}

// humanizeAction turns an unknown action into readable words, so a new
// backend action still reads sensibly before its translation lands.
function humanizeAction(action: string): string {
  const words = action.replace(/[._]+/g, ' ').trim();
  if (!words) return action;
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export default function AuditLogsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t, tOr } = useI18n();
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // The governance view spans every organization; the filter narrows it on
  // demand (an operator investigating one tenant's trail).
  const [orgFilter, setOrgFilter] = useState('');
  const [orgList, setOrgList] = useState<OrganizationSummary[]>([]);

  useEffect(() => {
    api
      .get<{ organizations?: OrganizationSummary[] }>(
        '/api/v1/admin/tenancy/organizations?page.limit=100',
        orgId,
      )
      .then((data) => setOrgList(data.organizations || []))
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

  // Organization display names by id: the event carries one, and the
  // organization list backfills it for rows recorded before the backend
  // resolved the name.
  const orgNameById = useMemo(() => {
    const map = new Map<string, string>();
    for (const org of orgList) {
      if (org.displayName) map.set(org.organizationId, org.displayName);
    }
    return map;
  }, [orgList]);

  const orgLabel = (e: AuditEvent): string => {
    if (!e.organizationId) return t('audit.platformLevel');
    return e.organizationDisplayName || orgNameById.get(e.organizationId) || e.organizationId;
  };

  const actionLabel = (action: string): string =>
    tOr(`audit.action.${action}`, humanizeAction(action));

  const resourceTypeLabel = (resourceType: string): string =>
    tOr(`audit.resource.${resourceType}`, humanizeAction(resourceType));

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
              <option key={o.organizationId} value={o.organizationId}>
                {o.displayName || o.organizationId}
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
                <td data-testid={`audit-action-${e.auditEventId}`}>{actionLabel(e.action)}</td>
                <td>{orgLabel(e)}</td>
                <td>
                  {e.actorDisplayName || e.actorUserId || t('audit.systemActor')}
                  {e.actorDisplayName && e.actorUserId && (
                    <div className="muted small mono">{e.actorUserId}</div>
                  )}
                </td>
                <td>
                  <span data-testid={`audit-resource-${e.auditEventId}`}>
                    {resourceTypeLabel(e.resourceType)}
                    {e.resourceId && <> {e.resourceDisplayName || e.resourceId}</>}
                  </span>
                  {e.resourceDisplayName && e.resourceId && (
                    <div className="muted small mono">{e.resourceId}</div>
                  )}
                </td>
                <td><StateBadge state={e.result} /></td>
                <td className="mono">{e.ipAddress || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
