// ResourceChart renders the service resource-metrics time series as a
// monitoring-style chart: a smooth area line with a gradient fill, a
// value axis with unit-aware ticks, a time axis, subtle grid lines, and a
// hover crosshair with a tooltip. One metric at a time (the switcher
// toggles it client-side, no refetch), so the axis can carry the right
// unit instead of a normalized 0-100.
//
// Inline SVG, no charting dependency: the whole component is testable and
// the console stays one bundle.

import { useMemo, useRef, useState } from 'react';
import type { ResourceMetricsSeriesPoint } from '../api';
import { useI18n } from '../i18n';

export type ResourceMetric = 'cpu' | 'memory' | 'gpu';

// CHART geometry. The viewBox is fixed and the SVG scales to its
// container, so the chart is responsive without a resize observer.
const VIEW_W = 880;
const VIEW_H = 260;
const PAD_LEFT = 56;
const PAD_RIGHT = 16;
const PAD_TOP = 16;
const PAD_BOTTOM = 32;

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

// formatMemory renders bytes with a binary unit.
function formatMemory(bytes: number): string {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return unit === 0 ? `${Math.round(value)} B` : `${value.toFixed(value < 10 ? 1 : 0)} ${units[unit]}`;
}

// axisFormatter returns the tick label and a scale ceiling for a metric.
// Percent metrics are fixed to 0-100 so a quiet service does not look
// busy, and so the axis does not rescale on every refresh.
function axisFor(metric: ResourceMetric, max: number): { ceiling: number; format: (v: number) => string } {
  if (metric === 'memory') {
    return { ceiling: max <= 0 ? 1 : max * 1.15, format: (v) => formatMemory(v) };
  }
  return {
    ceiling: 100,
    format: (v) => `${v.toFixed(0)}%`,
  };
}

// timeFormatter renders an axis tick for the range being shown.
function timeFormatter(spanSeconds: number) {
  const locale = 'zh-CN';
  return (unix: number): string => {
    const d = new Date(unix * 1000);
    if (spanSeconds <= 6 * 3600) {
      return d.toLocaleTimeString(locale, { hour: '2-digit', minute: '2-digit' });
    }
    if (spanSeconds <= 3 * 86400) {
      return d.toLocaleString(locale, { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
    }
    return d.toLocaleDateString(locale, { month: 'numeric', day: 'numeric' });
  };
}

export default function ResourceChart({
  series,
  metric,
}: {
  series: ResourceMetricsSeriesPoint[];
  metric: ResourceMetric;
}) {
  const { t } = useI18n();
  const [hover, setHover] = useState<number | null>(null);
  const svgRef = useRef<SVGSVGElement>(null);

  const model = useMemo(() => {
    const points = series
      .map((p) => ({ bucket: parseInt(p.bucket || '0', 10), value: metricValue(p, metric) }))
      .filter((p) => p.bucket > 0)
      .sort((a, b) => a.bucket - b.bucket);
    if (points.length === 0) return null;

    const first = points[0].bucket;
    const last = points[points.length - 1].bucket;
    // A single point has no span; give it a minute so it can be drawn.
    const span = Math.max(last - first, 60);
    const maxValue = Math.max(...points.map((p) => p.value));
    const { ceiling, format } = axisFor(metric, maxValue);

    const innerW = VIEW_W - PAD_LEFT - PAD_RIGHT;
    const innerH = VIEW_H - PAD_TOP - PAD_BOTTOM;
    const xAt = (bucket: number) => PAD_LEFT + ((bucket - first) / span) * innerW;
    const yAt = (value: number) => PAD_TOP + innerH - (Math.min(value, ceiling) / ceiling) * innerH;

    const coords = points.map((p) => ({ x: xAt(p.bucket), y: yAt(p.value), ...p }));

    // A smooth path through the points (Catmull-Rom → cubic Bezier) reads
    // as a monitoring line instead of a staircase, without overshooting
    // the data range the way a naive quadratic curve does.
    let line = '';
    coords.forEach((c, i) => {
      if (i === 0) {
        line += `M ${c.x.toFixed(2)} ${c.y.toFixed(2)}`;
        return;
      }
      const prev = coords[i - 1];
      const cpx = (prev.x + c.x) / 2;
      line += ` C ${cpx.toFixed(2)} ${prev.y.toFixed(2)}, ${cpx.toFixed(2)} ${c.y.toFixed(2)}, ${c.x.toFixed(2)} ${c.y.toFixed(2)}`;
    });
    const area = `${line} L ${coords[coords.length - 1].x.toFixed(2)} ${(PAD_TOP + innerH).toFixed(2)} L ${coords[0].x.toFixed(2)} ${(PAD_TOP + innerH).toFixed(2)} Z`;

    // Axis ticks: four value levels and up to six time labels.
    const valueTicks = [0, 0.25, 0.5, 0.75, 1].map((ratio) => ({
      value: ratio * ceiling,
      y: PAD_TOP + innerH - ratio * innerH,
      label: format(ratio * ceiling),
    }));
    const tickCount = Math.min(6, Math.max(2, Math.floor(innerW / 120)));
    const timeTicks = Array.from({ length: tickCount }, (_, i) => {
      const bucket = first + (span * i) / (tickCount - 1);
      return { x: xAt(bucket), label: timeFormatter(span)(Math.round(bucket)) };
    });

    return { coords, line, area, valueTicks, timeTicks, format, innerH, first, span };
  }, [series, metric]);

  if (!model) {
    return (
      <div className="chart-empty" data-testid="resource-chart-empty">
        {t('resource.noData')}
      </div>
    );
  }

  // Map a pointer position to the nearest sample. The SVG is scaled, so
  // the ratio of the rendered box to the viewBox gives the mapping.
  const onMove = (event: React.MouseEvent<SVGSVGElement>) => {
    const svg = svgRef.current;
    if (!svg || !model) return;
    const rect = svg.getBoundingClientRect();
    if (rect.width === 0) return;
    const vx = ((event.clientX - rect.left) / rect.width) * VIEW_W;
    let nearest = 0;
    let bestDistance = Infinity;
    model.coords.forEach((c, i) => {
      const distance = Math.abs(c.x - vx);
      if (distance < bestDistance) {
        bestDistance = distance;
        nearest = i;
      }
    });
    setHover(nearest);
  };

  const active = hover !== null ? model.coords[hover] : null;
  const activeTime = active ? new Date(active.bucket * 1000).toLocaleString('zh-CN') : '';
  // Keep the tooltip inside the plot: near the right edge it flips left.
  const tooltipX = active ? (active.x > VIEW_W - 200 ? active.x - 150 : active.x + 12) : 0;

  return (
    <div className="chart-wrap">
      <svg
        ref={svgRef}
        data-testid="resource-chart"
        viewBox={`0 0 ${VIEW_W} ${VIEW_H}`}
        preserveAspectRatio="none"
        className="chart-svg"
        role="img"
        aria-label={t('resource.chartAria')}
        onMouseMove={onMove}
        onMouseLeave={() => setHover(null)}
      >
        <defs>
          <linearGradient id={`fill-${metric}`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="var(--chart-line, #0080ff)" stopOpacity="0.35" />
            <stop offset="100%" stopColor="var(--chart-line, #0080ff)" stopOpacity="0.02" />
          </linearGradient>
        </defs>

        {/* Value grid + labels */}
        {model.valueTicks.map((tick) => (
          <g key={tick.y}>
            <line
              x1={PAD_LEFT}
              y1={tick.y}
              x2={VIEW_W - PAD_RIGHT}
              y2={tick.y}
              className="chart-grid"
            />
            <text x={PAD_LEFT - 10} y={tick.y + 4} className="chart-axis-label" textAnchor="end">
              {tick.label}
            </text>
          </g>
        ))}

        {/* Time labels */}
        {model.timeTicks.map((tick, i) => (
          <text
            key={`${tick.x}-${i}`}
            x={tick.x}
            y={VIEW_H - 10}
            className="chart-axis-label"
            textAnchor={i === 0 ? 'start' : i === model.timeTicks.length - 1 ? 'end' : 'middle'}
          >
            {tick.label}
          </text>
        ))}

        {/* Area + line */}
        <path d={model.area} fill={`url(#fill-${metric})`} />
        <path d={model.line} className="chart-line" />

        {/* Crosshair on hover */}
        {active && (
          <g>
            <line
              x1={active.x}
              y1={PAD_TOP}
              x2={active.x}
              y2={PAD_TOP + model.innerH}
              className="chart-crosshair"
            />
            <circle cx={active.x} cy={active.y} r={4} className="chart-dot" />
            <g transform={`translate(${tooltipX}, ${Math.max(PAD_TOP, active.y - 44)})`}>
              <rect width={140} height={44} rx={6} className="chart-tooltip-box" />
              <text x={10} y={17} className="chart-tooltip-time">
                {activeTime}
              </text>
              <text x={10} y={34} className="chart-tooltip-value">
                {model.format(active.value)}
              </text>
            </g>
          </g>
        )}
      </svg>
    </div>
  );
}
