// End-user My Activity page (feature-15): the caller's own audit events.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { formatTime } from '../../api';

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

export default function ActivityPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .get<{ auditEvents?: AuditEvent[] }>('/api/v1/audit/activity?page.limit=100', orgId)
      .then((data) => setEvents(data.auditEvents || []))
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load activity'));
  }, [api, orgId]);

  return (
    <div className="page">
      <h1>My Activity</h1>
      {error && <div className="error">{error}</div>}
      {events.length === 0 ? (
        <div className="empty" data-testid="activity-empty">No activity yet.</div>
      ) : (
        <table className="table" data-testid="activity-table">
          <thead>
            <tr>
              <th>Time</th>
              <th>Action</th>
              <th>Resource</th>
              <th>Result</th>
            </tr>
          </thead>
          <tbody>
            {events.map((e) => (
              <tr key={e.auditEventId}>
                <td>{formatTime(e.createdAt)}</td>
                <td>{e.action}</td>
                <td>{e.resourceType}:{e.resourceId}</td>
                <td>{e.result}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
