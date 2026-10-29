// UsageKeysCards renders the per-key usage analytics summary cards
// (feature #28, AD7): Requests, Error rate, Total tokens, Total cost,
// Avg latency, p95 latency, each with a "data through <time>" freshness
// note. The wire carries integer counts, integer milliseconds and integer
// cents only; error rate and cost are derived client-side.

import type { UsageKeysCard } from '../api';
import { formatTime } from '../api';
import { useI18n } from '../i18n';
import { formatMoney } from '../display';

// errorRatePercent derives the error rate as a percentage.
function errorRatePercent(card: UsageKeysCard): string {
  const req = parseInt(card.requestCount || '0', 10);
  if (req === 0) return '0.0%';
  return `${((parseInt(card.errorCount || '0', 10) / req) * 100).toFixed(1)}%`;
}

// costDollars formats integer cents in the platform's display currency.
function costDollars(cents: string): string {
  return formatMoney(cents);
}

export default function UsageKeysCards({ cards }: { cards: UsageKeysCard }) {
  const { t } = useI18n();
  const dataThrough = cards.dataThrough ? formatTime(cards.dataThrough) : '—';
  return (
    <div className="card-row" data-testid="usage-keys-cards">
      <div className="card" data-testid="usage-keys-card-requests">
        <div className="card-value">{parseInt(cards.requestCount || '0', 10).toLocaleString()}</div>
        <div className="card-label">{t('usageKeys.requests')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="usage-keys-card-error-rate">
        <div className="card-value">{errorRatePercent(cards)}</div>
        <div className="card-label">{t('errors.rate')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="usage-keys-card-tokens">
        <div className="card-value">{parseInt(cards.totalTokens || '0', 10).toLocaleString()}</div>
        <div className="card-label">{t('costCards.totalTokens')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="usage-keys-card-cost">
        <div className="card-value">{costDollars(cards.totalCostCents)}</div>
        <div className="card-label">{t('costCards.totalCost')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="usage-keys-card-avg-latency">
        <div className="card-value">{parseInt(cards.avgLatencyMs || '0', 10).toLocaleString()} ms</div>
        <div className="card-label">{t('usageKeys.avgLatency')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="usage-keys-card-p95">
        <div className="card-value">{parseInt(cards.p95LatencyMs || '0', 10).toLocaleString()} ms</div>
        <div className="card-label">{t('usageKeys.p95Latency')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
    </div>
  );
}