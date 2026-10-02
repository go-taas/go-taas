// ResourceChart renders the service resource-metrics time-series as an
// inline-SVG bar chart (feature #37, AD8). One bar per time bucket, with
// a metric switcher (CPU / Memory / GPU) toggling the plotted metric
// client-side with no refetch. No charting dependency — a small,
// testable SVG component matching the observability chart pattern.

import { useMemo } from 'react';
import type { ResourceMetricsSeriesPoint } from '../api';

export type ResourceMetric = 'cpu' | 'memory' | 'gpu';

// metricValue extracts the metric's numeric value from a series point.
function metricValue(p: ResourceMetricsSeriesPoint, metric: ResourceMetric): number {
  switch (metric) {
    case 'cpu':
      return parseFloat(p.cpuPercent || '0');
    case 'memory':
      return parseInt(p.memoryBytes || '0', 10);
    default:
      return parseFloat(p.gpuPercent || '0');
  }
}

export default function ResourceChart({
  series,
  metric,
}: {
  series: ResourceMetricsSeriesPoint[];
  metric: ResourceMetric;
}) {
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
        h,
        w: barW,
        value: v,
      };
    });
    return { width: W, height: H, bars };
  }, [series, metric]);

  return (
    <svg
      data-testid="resource-chart"
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      role="img"
      aria-label="resource utilization chart"
    >
      {bars.map((b) => (
        <rect
          key={b.bucket}
          x={b.x}
          y={b.y}
          width={b.w}
          height={b.h}
          fill="#007F86"
          data-testid={`resource-chart-bar-${b.bucket}`}
        />
      ))}
    </svg>
  );
}