// ForecastChart renders the usage & cost forecast as an inline-SVG chart
// (feature #36, AD7): a historical line, a forecast line, and a shaded
// confidence band, with a metric switcher (Tokens / Cost). No charting
// dependency — a small, testable SVG component.

import { useMemo } from 'react';
import { useI18n } from '../i18n';

export type ForecastMetric = 'tokens' | 'cost';

interface ForecastBucket {
  bucket: string;
  totalTokens: string;
  totalCostCents: string;
}

interface ForecastPoint {
  bucket: string;
  totalTokens: string;
  totalCostCents: string;
  lowerTokens: string;
  upperTokens: string;
  lowerCostCents: string;
  upperCostCents: string;
}

function metricValue(tokens: string, cost: string, metric: ForecastMetric): number {
  return metric === 'tokens' ? parseInt(tokens || '0', 10) : parseInt(cost || '0', 10) / 100;
}

export default function ForecastChart({
  history,
  forecast,
  metric,
}: {
  history: ForecastBucket[];
  forecast: ForecastPoint[];
  metric: ForecastMetric;
}) {
  const { t } = useI18n();
  const { width, height, histPoints, forePoints, band } = useMemo(() => {
    const W = 720;
    const H = 220;
    const pad = 16;

    const histVals = history.map((h) => metricValue(h.totalTokens, h.totalCostCents, metric));
    const foreVals = forecast.map((f) => metricValue(f.totalTokens, f.totalCostCents, metric));
    const lowerVals = forecast.map((f) =>
      metric === 'tokens' ? parseInt(f.lowerTokens || '0', 10) : parseInt(f.lowerCostCents || '0', 10) / 100,
    );
    const upperVals = forecast.map((f) =>
      metric === 'tokens' ? parseInt(f.upperTokens || '0', 10) : parseInt(f.upperCostCents || '0', 10) / 100,
    );

    let maxValue = 0;
    for (const v of [...histVals, ...upperVals]) {
      if (v > maxValue) maxValue = v;
    }
    if (maxValue === 0) maxValue = 1;

    const allBuckets = [...history.map((h) => parseInt(h.bucket, 10)), ...forecast.map((f) => parseInt(f.bucket, 10))];
    const minB = allBuckets.length > 0 ? Math.min(...allBuckets) : 0;
    const maxB = allBuckets.length > 0 ? Math.max(...allBuckets) : 1;
    const span = maxB - minB || 1;

    const xFor = (b: number) => pad + ((b - minB) / span) * (W - pad * 2);
    const yFor = (v: number) => H - pad - (v / maxValue) * (H - pad * 2);

    const histPoints = history.map((h, i) => ({
      x: xFor(parseInt(h.bucket, 10)),
      y: yFor(histVals[i]),
    }));
    const forePoints = forecast.map((f, i) => ({
      x: xFor(parseInt(f.bucket, 10)),
      y: yFor(foreVals[i]),
    }));
    const band = forecast.map((f, i) => ({
      x: xFor(parseInt(f.bucket, 10)),
      yUpper: yFor(upperVals[i]),
      yLower: yFor(lowerVals[i]),
    }));

    return { width: W, height: H, histPoints, forePoints, band };
  }, [history, forecast, metric]);

  if (history.length === 0) {
    return <div className="empty-state">{t('forecast.empty')}</div>;
  }

  const histLine = histPoints.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x},${p.y}`).join(' ');
  const foreLine = forePoints.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x},${p.y}`).join(' ');
  const bandPath =
    band.length > 0
      ? `M${band[0].x},${band[0].yUpper} ` +
        band.map((p, i) => (i === 0 ? '' : `L${p.x},${p.yUpper}`)).join(' ') +
        ' ' +
        band
          .slice()
          .reverse()
          .map((p) => `L${p.x},${p.yLower}`)
          .join(' ') +
        ' Z'
      : '';

  return (
    <div data-testid="forecast-chart">
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={t('forecast.chartAria')}
      >
        {bandPath && <path d={bandPath} fill="#007F86" opacity="0.15" data-testid="forecast-band" />}
        {histLine && (
          <path d={histLine} fill="none" stroke="#007F86" strokeWidth="2" data-testid="forecast-history-line" />
        )}
        {foreLine && (
          <path d={foreLine} fill="none" stroke="#C9A227" strokeWidth="2" strokeDasharray="4 3" data-testid="forecast-line" />
        )}
      </svg>
    </div>
  );
}