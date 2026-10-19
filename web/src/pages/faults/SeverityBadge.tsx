// SeverityBadge: the incident severity badge (feature #53). Critical
// red, warning amber, info blue.

export default function SeverityBadge({ severity }: { severity: string }) {
  const cls =
    severity === 'critical'
      ? 'badge failed'
      : severity === 'warning'
        ? 'badge steady'
        : 'badge running';
  return (
    <span className={cls} data-testid={`fault-severity-${severity}`}>
      {severity}
    </span>
  );
}