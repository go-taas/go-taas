// CostChart renders the cost analytics time-series as an inline-SVG bar
// chart (feature #29, AD7). One bar per time bucket, with a metric
// switcher (cost / tokens / cost-per-token) toggling the plotted metric
// client-side with no refetch. No charting dependency — a small, testable
// SVG component matching the usage-dashboard UsageChart pattern.

import { useMemo } from 'react';
import type { CostSeriesPoint } from '../api';
import { useI18n } from '../i18n';

export type CostMetric = 'cost' | 'tokens' | 'costPerToken';

const PALETTE = ['#007F86', '#4E9CA0', '#C9A227', '#D96C6C'];

// metricValue extracts the metric's numeric value from a series point.
function metricValue(p: CostSeriesPoint, metric: CostMetric): number {
  switch (metric) {
    case 'tokens':
      return parseInt(p.totalTokens || '0', 10);
    case 'costPerToken': {
      const tokens = parseInt(p.totalTokens || '0', 10);
      if (tokens === 0) return 0;
      return parseInt(p.totalCostCents || '0', 10) / tokens;
    }
    default:
      return parseInt(p.totalCostCents || '0', 10) / 100;
  }
}

export default function CostChart({
  series,
  metric,
}: {
  series: CostSeriesPoint[];
  metric: CostMetric;
}) {
  const { t } = useI18n();
  const { width, height, bars } = useMemo(() => {
    const W = 720;
    const H = 220;
    const pad = 8;
    const barW = Math.max(4, Math.min(28, (W - pad * 2) / Math.max(series.length, 1) - 4));

    let maxValue = 0;
    const values = series.map((p) => {
      const v = metricValue(p, metric);
      if (v > maxValue) maxValue = v;
      return v;
    });
    if (maxValue === 0) maxValue = 1;

    const bars = series.map((p, i) => {
      const v = values[i];
      const x = pad + i * (barW + 4);
      const h = (v / maxValue) * (H - pad * 2);
      const y = H - pad - h;
      return {
        bucket: p.bucket,
        x,
        y,
        h: Math.max(h, 0.5),
        value: v,
        color: PALETTE[i % PALETTE.length],
      };
    });

    return { width: W, height: H, bars, maxValue };
  }, [series, metric]);

  if (series.length === 0) {
    return <div className="empty-state">{t('cost.empty')}</div>;
  }

  return (
    <div data-testid="cost-chart">
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={t('cost.chartAria')}
      >
        {bars.map((b) => (
          <rect
            key={b.bucket}
            x={b.x}
            y={b.y}
            width={barWidth(bars.length)}
            height={b.h}
            fill={b.color}
          >
            <title>{`${new Date(parseInt(b.bucket, 10) * 1000).toISOString()}: ${b.value.toLocaleString()}`}</title>
          </rect>
        ))}
      </svg>
      <div className="chart-legend">
        <span className="chart-legend-item">
          <span className="chart-legend-swatch" style={{ background: PALETTE[0] }} />
          <span className="mono">{metricLabel(metric, t)}</span>
        </span>
      </div>
    </div>
  );
}

// barWidth recomputes the bar width from the bar count.
function barWidth(count: number): number {
  const W = 720;
  const pad = 8;
  return Math.max(4, Math.min(28, (W - pad * 2) / Math.max(count, 1) - 4));
}

// metricLabel returns the localized legend label of a metric.
function metricLabel(metric: CostMetric, t: (key: string) => string): string {
  switch (metric) {
    case 'tokens':
      return t('cost.metric.tokens');
    case 'costPerToken':
      return t('cost.metricCostPerToken');
    default:
      return t('cost.metricCost');
  }
}