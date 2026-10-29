// UsageChart renders the daily usage dashboard as an inline-SVG stacked
// bar chart (feature #9, AD8). One bar per day, stacked by group, with
// a metric toggle switching cost / tokens / requests client-side (AD6).
// No charting dependency — a small, testable SVG component.

import { useMemo } from 'react';
import type { DailyBucket, DashboardGroup } from '../api';
import { useI18n } from '../i18n';
import { formatMajor } from '../display';

export type ChartMetric = 'cost' | 'tokens' | 'requests';

const PALETTE = ['#007F86', '#4E9CA0', '#2F7F86', '#8A6D0B', '#7A5C9E', '#D96C6C'];

// metricValue extracts the metric's numeric value from a group.
function metricValue(g: DashboardGroup, metric: ChartMetric): number {
  switch (metric) {
    case 'cost':
      return parseInt(g.costCents || '0', 10) / 100;
    case 'requests':
      return parseInt(g.requestCount || '0', 10);
    default:
      return (
        parseInt(g.promptTokens || '0', 10) +
        parseInt(g.completionTokens || '0', 10) +
        parseInt(g.cachedTokens || '0', 10)
      );
  }
}

// groupKeyIndex maps a group key to a stable palette index.
function groupKeyIndex(key: string): number {
  let h = 0;
  for (let i = 0; i < key.length; i++) {
    h = (h * 31 + key.charCodeAt(i)) >>> 0;
  }
  return h % PALETTE.length;
}

export default function UsageChart({
  buckets,
  metric,
}: {
  buckets: DailyBucket[];
  metric: ChartMetric;
}) {
  const { t } = useI18n();
  const { width, height, bars, legend } = useMemo(() => {
    const W = 720;
    const H = 220;
    const pad = 8;
    const barW = Math.max(4, Math.min(28, (W - pad * 2) / Math.max(buckets.length, 1) - 4));

    // Compute per-day stacked segments and the global max.
    const segments: { key: string; value: number; color: string }[][] = [];
    let maxValue = 0;
    const legendKeys = new Set<string>();
    for (const bucket of buckets) {
      const daySegs: { key: string; value: number; color: string }[] = [];
      let dayTotal = 0;
      for (const g of bucket.groups || []) {
        const v = metricValue(g, metric);
        if (v <= 0) continue;
        dayTotal += v;
        legendKeys.add(g.groupKey);
        daySegs.push({ key: g.groupKey, value: v, color: PALETTE[groupKeyIndex(g.groupKey)] });
      }
      segments.push(daySegs);
      if (dayTotal > maxValue) maxValue = dayTotal;
    }
    if (maxValue === 0) maxValue = 1;

    const bars = buckets.map((bucket, i) => {
      const x = pad + i * (barW + 4);
      let y = H - pad;
      const rects = (segments[i] || []).map((seg) => {
        const h = (seg.value / maxValue) * (H - pad * 2);
        y -= h;
        return (
          <rect
            key={`${bucket.date}-${seg.key}`}
            x={x}
            y={y}
            width={barW}
            height={Math.max(h, 0.5)}
            fill={seg.color}
          >
            <title>{`${seg.key}: ${metric === 'cost' ? formatMajor(seg.value) : seg.value.toLocaleString()}`}</title>
          </rect>
        );
      });
      return { date: bucket.date, x, rects };
    });

    return { width: W, height: H, bars, legend: Array.from(legendKeys) };
  }, [buckets, metric]);

  if (buckets.length === 0) {
    return <div className="empty-state">{t('usage.empty')}</div>;
  }

  return (
    <div data-testid="usage-chart">
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={t('usage.chartAria')}
      >
        {bars.map((b) => (
          <g key={b.date}>{b.rects}</g>
        ))}
      </svg>
      {legend.length > 0 && (
        <div className="chart-legend">
          {legend.map((key) => (
            <span key={key} className="chart-legend-item">
              <span
                className="chart-legend-swatch"
                style={{ background: PALETTE[groupKeyIndex(key)] }}
              />
              <span className="mono">{key}</span>
            </span>
          ))}
        </div>
      )}
    </div>
  );
}