// ObservabilityCards renders the model observability summary card row
// (feature #24, AD8): Requests, Error rate, Avg latency, p95 latency,
// Output tokens/sec, Input tokens/sec, each with a "data through <time>"
// freshness note. error_rate is derived client-side as error ÷ requests.

import type { ObservabilityCard } from '../api';
import { formatTime } from '../api';
import { useI18n } from '../i18n';

// errorRatePercent derives the error rate as a percentage.
function errorRatePercent(cards: ObservabilityCard): string {
  const req = parseInt(cards.requestCount || '0', 10);
  if (req === 0) return '0.0%';
  return `${((parseInt(cards.errorCount || '0', 10) / req) * 100).toFixed(1)}%`;
}

export default function ObservabilityCards({
  cards,
  dataThroughLabel,
}: {
  cards: ObservabilityCard | null;
  dataThroughLabel: string;
}) {
  const { t } = useI18n();
  if (!cards) {
    return (
      <div className="dashboard-cards" data-testid="observability-cards">
        {[
          t('observability.requests'),
          t('errors.rate'),
          t('observability.avgLatency'),
          t('observability.p95Latency'),
          t('observability.outputTokensPerSec'),
          t('observability.inputTokensPerSec'),
        ].map((label) => (
          <div className="dashboard-card" key={label}>
            <div className="label">{label}</div>
            <div className="value">—</div>
          </div>
        ))}
      </div>
    );
  }

  const dataThrough = parseInt(cards.dataThrough || '0', 10);
  const freshness = dataThrough > 0 ? `${dataThroughLabel} ${formatTime(String(dataThrough))}` : '';

  const items = [
    { label: t('observability.requests'), value: parseInt(cards.requestCount || '0', 10).toLocaleString() },
    { label: t('errors.rate'), value: errorRatePercent(cards) },
    { label: t('observability.avgLatency'), value: `${cards.avgLatencyMs || '0'} ms` },
    { label: t('observability.p95Latency'), value: `${cards.p95LatencyMs || '0'} ms` },
    { label: t('observability.outputTokensPerSec'), value: (cards.outputTokensPerSec || '0').toLocaleString() },
    { label: t('observability.inputTokensPerSec'), value: (cards.inputTokensPerSec || '0').toLocaleString() },
  ];

  return (
    <div className="dashboard-cards" data-testid="observability-cards">
      {items.map((item) => (
        <div className="dashboard-card" key={item.label}>
          <div className="label">{item.label}</div>
          <div className="value">{item.value}</div>
          {freshness && <div className="sub">{freshness}</div>}
        </div>
      ))}
    </div>
  );
}