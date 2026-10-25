// TraceWaterfall renders a trace's spans as an inline-SVG waterfall of
// start/duration bars (feature #27, AD6). One bar per span, positioned
// by start_offset_ms and sized by duration_ms, with a status/error
// label. No charting dependency.

import type { TraceSpan } from '../api';
import { useI18n } from '../i18n';

interface Props {
  spans: TraceSpan[];
  totalMs: number;
}

export default function TraceWaterfall({ spans, totalMs }: Props) {
  const { t } = useI18n();
  const width = 720;
  const rowHeight = 28;
  const labelWidth = 120;
  const chartWidth = width - labelWidth - 40;
  const height = Math.max(spans.length * rowHeight + 20, 60);
  const maxMs = Math.max(totalMs, ...spans.map((s) => parseInt(s.startOffsetMs || '0', 10) + parseInt(s.durationMs || '0', 10)), 1);

  const xFor = (ms: number) => labelWidth + (ms / maxMs) * chartWidth;
  const wFor = (ms: number) => (ms / maxMs) * chartWidth;

  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      data-testid="trace-waterfall"
      role="img"
      aria-label={t('traces.waterfallAria')}
    >
      {spans.map((span, i) => {
        const start = parseInt(span.startOffsetMs || '0', 10);
        const dur = parseInt(span.durationMs || '0', 10);
        const y = 10 + i * rowHeight;
        const isError = span.status === 'error';
        return (
          <g key={span.spanId || i}>
            <text x={0} y={y + 16} fontSize={12} fill="#23343A">
              {span.name}
            </text>
            <rect
              x={xFor(start)}
              y={y}
              width={Math.max(wFor(dur), 2)}
              height={16}
              rx={3}
              fill={isError ? '#C0392B' : '#007F86'}
              data-testid={`trace-span-${span.name}`}
            />
            <text x={xFor(start) + wFor(dur) + 6} y={y + 14} fontSize={11} fill="#5A6B72">
              {dur} ms{isError ? ' · error' : ''}
            </text>
          </g>
        );
      })}
    </svg>
  );
}