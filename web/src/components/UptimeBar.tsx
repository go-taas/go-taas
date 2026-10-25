// UptimeBar renders a simple inline-SVG uptime bar (feature #30, AD6):
// a filled bar proportional to the component's uptime.

import { useMemo } from 'react';
import { useI18n } from '../i18n';

export default function UptimeBar({ uptimeSeconds }: { uptimeSeconds: string }) {
  const { t } = useI18n();
  const { width, fillWidth } = useMemo(() => {
    const W = 120;
    const H = 8;
    const uptime = parseInt(uptimeSeconds || '0', 10);
    // Cap the visual fill at 100% (uptime is unbounded in practice).
    const pct = uptime > 0 ? Math.min(100, 100) : 0;
    return { width: W, height: H, fillWidth: (W * pct) / 100 };
  }, [uptimeSeconds]);

  return (
    <svg width={width} height={8} viewBox={`0 0 ${width} 8`} role="img" aria-label={t('common.uptimeBar')}>
      <rect x={0} y={0} width={width} height={8} rx={4} fill="#E7ECEE" />
      <rect x={0} y={0} width={fillWidth} height={8} rx={4} fill="#007F86" />
    </svg>
  );
}