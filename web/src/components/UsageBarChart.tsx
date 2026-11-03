// UsageBarChart renders a day-bucketed series as single-series bars with a
// labelled value axis and date ticks.
//
// It is the headline chart of a usage page: one bar per day, the vertical
// scale printed on the left and a few dates along the bottom, so a reader can
// tell "how much" and "when" without hovering. The bar count is fixed by the
// range, so the geometry is computed from the data and the widget fills its
// container width through a viewBox.
//
// No charting dependency: a small, testable SVG, like the other console
// charts.

import { useMemo } from 'react';

export interface UsageBarChartPoint {
  /** dayStart is the bucket's day start, unix seconds. */
  dayStart: number;
  value: number;
}

/** formatValue renders one scalar for the axis and the tooltip. */
export type UsageBarValueFormatter = (value: number) => string;

// CHART_WIDTH/CHART_HEIGHT are the viewBox the SVG is drawn in; the element
// scales to its container, so they only fix the aspect ratio.
const CHART_WIDTH = 900;
const CHART_HEIGHT = 220;
const PADDING_LEFT = 56;
const PADDING_RIGHT = 12;
const PADDING_TOP = 14;
const PADDING_BOTTOM = 28;

// TICK_COUNT is how many value gridlines are drawn, and MAX_DATE_TICKS bounds
// the date labels so a 92-day range does not overlap them.
const TICK_COUNT = 4;
const MAX_DATE_TICKS = 5;

/**
 * niceMax rounds a maximum up to a readable axis top: 1, 2 or 5 times a power
 * of ten, so the labels are round numbers instead of the raw maximum.
 */
export function niceMax(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 1;
  const magnitude = Math.pow(10, Math.floor(Math.log10(value)));
  const normalized = value / magnitude;
  const step = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10;
  return step * magnitude;
}

/** shortDay renders a bucket day as M/D, the compact form an axis needs. */
function shortDay(dayStart: number): string {
  const date = new Date(dayStart * 1000);
  return `${date.getUTCMonth() + 1}/${date.getUTCDate()}`;
}

export default function UsageBarChart({
  points,
  formatValue,
  ariaLabel,
  emptyLabel,
}: {
  points: UsageBarChartPoint[];
  formatValue: UsageBarValueFormatter;
  ariaLabel: string;
  emptyLabel: string;
}) {
  const { bars, ticks, dates, plotWidth, plotHeight } = useMemo(() => {
    const plotWidthInner = CHART_WIDTH - PADDING_LEFT - PADDING_RIGHT;
    const plotHeightInner = CHART_HEIGHT - PADDING_TOP - PADDING_BOTTOM;
    const max = niceMax(Math.max(0, ...points.map((p) => p.value)));

    const slot = plotWidthInner / Math.max(points.length, 1);
    const barWidth = Math.max(2, Math.min(30, slot * 0.62));

    const bars = points.map((point, i) => {
      const height = max > 0 ? (point.value / max) * plotHeightInner : 0;
      return {
        key: point.dayStart,
        x: PADDING_LEFT + i * slot + (slot - barWidth) / 2,
        y: PADDING_TOP + plotHeightInner - height,
        width: barWidth,
        height: Math.max(height, point.value > 0 ? 1 : 0),
        label: `${shortDay(point.dayStart)} · ${formatValue(point.value)}`,
      };
    });

    const ticks = Array.from({ length: TICK_COUNT + 1 }, (_, i) => {
      const value = (max / TICK_COUNT) * i;
      return {
        value,
        label: formatValue(value),
        y: PADDING_TOP + plotHeightInner - (value / max) * plotHeightInner,
      };
    });

    const stride = Math.max(1, Math.ceil(points.length / MAX_DATE_TICKS));
    const dates = points
      .map((point, i) => ({ point, i }))
      .filter(({ i }) => i % stride === 0 || i === points.length - 1)
      .map(({ point, i }) => ({
        key: point.dayStart,
        label: shortDay(point.dayStart),
        x: PADDING_LEFT + i * slot + slot / 2,
      }));

    return { bars, ticks, dates, plotWidth: plotWidthInner, plotHeight: plotHeightInner };
  }, [points, formatValue]);

  if (points.length === 0) {
    return <div className="empty-state">{emptyLabel}</div>;
  }

  return (
    <svg
      className="usage-bar-chart"
      viewBox={`0 0 ${CHART_WIDTH} ${CHART_HEIGHT}`}
      role="img"
      aria-label={ariaLabel}
      data-testid="usage-bar-chart"
    >
      {ticks.map((tick) => (
        <g key={`tick-${tick.value}`}>
          <line
            x1={PADDING_LEFT}
            y1={tick.y}
            x2={PADDING_LEFT + plotWidth}
            y2={tick.y}
            className="usage-chart-gridline"
          />
          <text x={PADDING_LEFT - 8} y={tick.y + 4} textAnchor="end" className="usage-chart-axis-label">
            {tick.label}
          </text>
        </g>
      ))}
      {bars.map((bar) => (
        <rect key={bar.key} x={bar.x} y={bar.y} width={bar.width} height={bar.height} className="usage-chart-bar">
          <title>{bar.label}</title>
        </rect>
      ))}
      {dates.map((date) => (
        <text
          key={`date-${date.key}`}
          x={date.x}
          y={CHART_HEIGHT - 8}
          textAnchor="middle"
          className="usage-chart-axis-label"
        >
          {date.label}
        </text>
      ))}
      <line
        x1={PADDING_LEFT}
        y1={PADDING_TOP + plotHeight}
        x2={PADDING_LEFT + plotWidth}
        y2={PADDING_TOP + plotHeight}
        className="usage-chart-axis"
      />
    </svg>
  );
}
