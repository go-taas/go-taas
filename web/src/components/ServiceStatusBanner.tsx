// ServiceStatusBanner: the tenant degraded banner (feature #53, AD10).
// It calls the user-surface GetServiceStatus and shows a compact strip
// when the service has an active incident. It hides when healthy or on
// a status-call error.

import { useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { type ServiceStatus } from '../api';

export default function ServiceStatusBanner({ serviceId }: { serviceId: string }) {
  const api = useApi();
  const { orgId } = useOrg();
  const [status, setStatus] = useState<ServiceStatus | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .get<ServiceStatus>(`/api/v1/inference-services/${serviceId}/status`, orgId)
      .then((res) => {
        if (!cancelled) setStatus(res);
      })
      .catch(() => {
        if (!cancelled) setStatus(null);
      });
    return () => {
      cancelled = true;
    };
  }, [api, orgId, serviceId]);

  if (!status?.degraded || !status.incident) return null;
  const inc = status.incident;
  const cls = inc.severity === 'critical' ? 'error-banner' : 'warning-banner';
  return (
    <div className={cls} data-testid={`service-status-banner-${serviceId}`}>
      <span className="badge">{inc.rootCause}</span>
      <span style={{ marginLeft: 8 }}>{inc.message}</span>
    </div>
  );
}