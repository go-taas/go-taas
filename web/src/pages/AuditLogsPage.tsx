// Admin Audit Logs page (feature-15): the control-plane audit trail.

import { useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime } from '../api';

interface AuditEvent {
  auditEventId: string;
  organizationId: string;
  actorUserId: string;
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
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .get<{ auditEvents?: AuditEvent[] }>('/api/v1/admin/audit/events?page.limit=100', orgId)
      .then((data) => setEvents(data.auditEvents || []))
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load audit events'));
  }, [api, orgId]);

  return (
    <div className="page">
      <h1>Audit Logs</h1>
      {error && <div className="error">{error}</div>}
      {events.length === 0 ? (
        <div className="empty" data-testid="audit-logs-empty">No audit events yet.</div>
      ) : (
        <table className="table" data-testid="audit-logs-table">
          <thead>
            <tr>
              <th>Time</th>
              <th>Action</th>
              <th>Actor</th>
              <th>Resource</th>
              <th>Result</th>
              <th>IP</th>
            </tr>
          </thead>
          <tbody>
            {events.map((e) => (
              <tr key={e.auditEventId} data-testid={`audit-event-row-${e.auditEventId}`}>
                <td>{formatTime(e.createdAt)}</td>
                <td>{e.action}</td>
                <td>{e.actorUserId}</td>
                <td>{e.resourceType}:{e.resourceId}</td>
                <td>{e.result}</td>
                <td>{e.ipAddress}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
