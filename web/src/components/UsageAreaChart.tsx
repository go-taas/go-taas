// UsageAreaChart renders a day-bucketed series as a filled line.
//
// It is the per-model chart of a usage page: the same days as the headline
// chart, but one small card per model so a reader can compare shapes ("this
// model spikes on Mondays") instead of digits. The area makes a low-traffic
// day visible without a second axis.
//
// No charting dependency: a small, testable SVG.

import { useMemo } from 'react';

export interface UsageAreaChartPoint {
  /** dayStart is the bucket's day start, unix seconds. */
  dayStart: number;
  value: number;
}

/** UsageAreaValueFormatter renders one scalar for the axis. */
export type UsageAreaValueFormatter = (value: number) => string;

const CHART_WIDTH = 420;
const CHART_HEIGHT = 130;
const PADDING_LEFT = 44;
const PADDING_RIGHT = 8;
const PADDING_TOP = 10;
const PADDING_BOTTOM = 20;

const TICK_COUNT = 2;

// niceMax rounds a maximum up to a readable axis top (1, 2 or 5 times a power
// of ten), so the labels stay round numbers.
function niceMax(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 1;
  const magnitude = Math.pow(10, Math.floor(Math.log10(value)));
  const normalized = value / magnitude;
  const step = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10;
  return step * magnitude;
}

/** shortDay renders a bucket day as M/D. */
function shortDay(dayStart: number): string {
  const date = new Date(dayStart * 1000);
  return `${date.getUTCMonth() + 1}/${date.getUTCDate()}`;
}

export default function UsageAreaChart({
  points,
  formatValue,
  ariaLabel,
}: {
  points: UsageAreaChartPoint[];
  formatValue: UsageAreaValueFormatter;
  ariaLabel: string;
}) {
  const geometry = useMemo(() => {
    const plotWidth = CHART_WIDTH - PADDING_LEFT - PADDING_RIGHT;
    const plotHeight = CHART_HEIGHT - PADDING_TOP - PADDING_BOTTOM;
    const max = niceMax(Math.max(0, ...points.map((p) => p.value)));
    const stepX = points.length > 1 ? plotWidth / (points.length - 1) : 0;

    const coords = points.map((point, i) => ({
      key: point.dayStart,
      x: PADDING_LEFT + i * stepX,
      y: PADDING_TOP + plotHeight - (point.value / max) * plotHeight,
      value: point.value,
    }));

    const line = coords.map((c, i) => `${i === 0 ? 'M' : 'L'}${c.x.toFixed(1)},${c.y.toFixed(1)}`).join(' ');
    const baseline = PADDING_TOP + plotHeight;
    const area =
      coords.length > 0
        ? `${line} L${coords[coords.length - 1].x.toFixed(1)},${baseline} L${coords[0].x.toFixed(1)},${baseline} Z`
        : '';

    const ticks = Array.from({ length: TICK_COUNT + 1 }, (_, i) => {
      const value = (max / TICK_COUNT) * i;
      return {
        value,
        label: formatValue(value),
        y: PADDING_TOP + plotHeight - (value / max) * plotHeight,
      };
    });

    const stride = Math.max(1, Math.ceil(points.length / 2));
    const dates = points
      .map((point, i) => ({ point, i }))
      .filter(({ i }) => i % stride === 0 || i === points.length - 1)
      .map(({ point, i }) => ({ key: point.dayStart, label: shortDay(point.dayStart), x: PADDING_LEFT + i * stepX }));

    return { line, area, ticks, dates, coords, plotWidth, plotHeight, baseline };
  }, [points, formatValue]);

  if (points.length === 0) {
    return null;
  }

  return (
    <svg
      className="usage-area-chart"
      viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`}
      role="img"
      aria-label={ariaLabel}
      data-testid="usage-area-chart"
    >
      {geometry.ticks.map((tick) => (
        <g key={`tick-${tick.value}`}>
          <line
            x1={PADDING_LEFT}
            y1={tick.y}
            x2={PADDING_LEFT + geometry.plotWidth}
            y2={tick.y}
            className="usage-chart-gridline"
          />
          <text x={PADDING_LEFT - 6} y={tick.y + 3.5} textAnchor="end" className="usage-chart-axis-label">
            {tick.label}
          </text>
        </g>
      ))}
      <path d={geometry.area} className="usage-chart-area" />
      <path d={geometry.line} className="usage-chart-line" />
      {geometry.coords.map((c) => (
        <circle key={c.key} cx={c.x} cy={c.y} r={1.6} className="usage-chart-dot">
          <title>{`${shortDay(c.key)} · ${formatValue(c.value)}`}</title>
        </circle>
      ))}
      {geometry.dates.map((date) => (
        <text
          key={`date-${date.key}`}
          x={date.x}
          y={CHART_HEIGHT - 6}
          textAnchor="middle"
          className="usage-chart-axis-label"
        >
          {date.label}
        </text>
      ))}
    </svg>
  );
}
