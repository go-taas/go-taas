// StatusBadge renders the healthy / degraded / unhealthy status badge
// (feature #30, AD6), following the accelerator-inventory health-badge
// pattern.

import { useI18n } from '../i18n';

export default function StatusBadge({ status }: { status: string }) {
  const { tEnum } = useI18n();
  const cls = status === 'healthy' ? 'status-badge healthy' : status === 'degraded' ? 'status-badge degraded' : 'status-badge unhealthy';
  return (
    <span className={cls} data-testid={`status-badge-${status}`}>
      {tEnum('state', status)}
    </span>
  );
}