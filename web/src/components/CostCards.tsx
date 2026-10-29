// CostCards renders the cost analytics summary cards (feature #29, AD8):
// Total cost, Total tokens, Cost per token, Top dimension, each with a
// "data through <time>" freshness note. The wire carries integer cents
// and integer tokens only; cost-per-token and share are derived
// client-side.

import type { CostAnalyticsCard } from '../api';
import { formatTime } from '../api';
import { formatAmount, formatMoney } from '../display';
import { useI18n } from '../i18n';

// costPerToken derives the price of 1000 tokens from the cents/token ratio,
// in the platform's display currency. It used to hard-code a dollar sign,
// which is what kept this page saying $ after the installation switched to
// CNY.
function costPerToken(card: CostAnalyticsCard): string {
  const tokens = parseInt(card.totalTokens || '0', 10);
  if (tokens === 0) return '—';
  const cents = parseInt(card.totalCostCents || '0', 10);
  return `${formatAmount((cents / tokens) * 1000, 4)}/1k`;
}

export default function CostCards({ cards }: { cards: CostAnalyticsCard }) {
  const { t } = useI18n();
  const dataThrough = cards.dataThrough ? formatTime(cards.dataThrough) : '—';
  return (
    <div className="card-row" data-testid="cost-cards">
      <div className="card" data-testid="cost-card-total-cost">
        <div className="card-value">{formatMoney(cards.totalCostCents)}</div>
        <div className="card-label">{t('costCards.totalCost')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="cost-card-tokens">
        <div className="card-value">{parseInt(cards.totalTokens || '0', 10).toLocaleString()}</div>
        <div className="card-label">{t('costCards.totalTokens')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="cost-card-per-token">
        <div className="card-value">{costPerToken(cards)}</div>
        <div className="card-label">{t('costCards.costPerToken')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="cost-card-top-dimension">
        <div className="card-value">{cards.topDimensionValue || '—'}</div>
        <div className="card-label">{t('costCards.topDimension')}</div>
        <div className="card-note">{cards.topDimensionSharePct ? t('costCards.share', { pct: cards.topDimensionSharePct }) : t('common.dataThrough', { time: dataThrough })}</div>
      </div>
    </div>
  );
}