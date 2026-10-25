// SeverityBadge: the incident severity badge (feature #53). Critical
// red, warning amber, info blue.

import { useI18n } from '../../i18n';

export default function SeverityBadge({ severity }: { severity: string }) {
  const { tEnum } = useI18n();
  const cls =
    severity === 'critical'
      ? 'badge failed'
      : severity === 'warning'
        ? 'badge steady'
        : 'badge running';
  return (
    <span className={cls} data-testid={`fault-severity-${severity}`}>
      {tEnum('state', severity)}
    </span>
  );
}