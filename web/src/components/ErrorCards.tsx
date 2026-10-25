// ErrorCards renders the error analysis summary cards (feature #31,
// AD7): Error rate, Error count, Request count, Top cause, each with a
// "data through <time>" freshness note. The wire carries integer counts
// only; error rate and share are derived client-side.

import type { ErrorAnalysisCard } from '../api';
import { formatTime } from '../api';
import { useI18n } from '../i18n';

// errorRatePercent derives the error rate as a percentage.
function errorRatePercent(card: ErrorAnalysisCard): string {
  const req = parseInt(card.requestCount || '0', 10);
  if (req === 0) return '0.0%';
  return `${((parseInt(card.errorCount || '0', 10) / req) * 100).toFixed(1)}%`;
}

export default function ErrorCards({ cards }: { cards: ErrorAnalysisCard }) {
  const { t } = useI18n();
  const dataThrough = cards.dataThrough ? formatTime(cards.dataThrough) : '—';
  return (
    <div className="card-row" data-testid="errors-cards">
      <div className="card" data-testid="errors-card-error-rate">
        <div className="card-value">{errorRatePercent(cards)}</div>
        <div className="card-label">{t('errors.rate')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="errors-card-error-count">
        <div className="card-value">{parseInt(cards.errorCount || '0', 10).toLocaleString()}</div>
        <div className="card-label">{t('errors.count')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="errors-card-request-count">
        <div className="card-value">{parseInt(cards.requestCount || '0', 10).toLocaleString()}</div>
        <div className="card-label">{t('errors.requestCount')}</div>
        <div className="card-note">{t('common.dataThrough', { time: dataThrough })}</div>
      </div>
      <div className="card" data-testid="errors-card-top-cause">
        <div className="card-value">{cards.topCause || '—'}</div>
        <div className="card-label">{t('errors.topCause')}</div>
        <div className="card-note">{cards.topCauseSharePct ? t('common.share', { pct: cards.topCauseSharePct }) : t('common.dataThrough', { time: dataThrough })}</div>
      </div>
    </div>
  );
}