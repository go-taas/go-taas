// SummaryCards renders the optimization overview cards (feature #50,
// design D8): total estimated monthly saving, active count with
// per-rule chips, dismissed count, and the last refresh time with the
// admin-only "Refresh now" trigger.

import { useI18n } from '../../i18n';
import { formatTime } from '../../api';
import type { OptimizationOverview } from '../../api';
import { formatMajor } from '../../display';

export default function SummaryCards({
  overview,
  onRefresh,
  refreshing,
}: {
  overview: OptimizationOverview | null;
  onRefresh?: () => void;
  refreshing?: boolean;
}) {
  const { t } = useI18n();
  const total = overview?.totalEstimatedMonthlySaving || '0.00';
  const active = overview?.activeCount || '0';
  const dismissed = overview?.dismissedCount || '0';
  const lastRefresh = overview?.lastRefreshAt || '0';
  const byRule = overview?.countByRule || [];

  return (
    <div className="card-row" data-testid="optimization-summary-cards">
      <div className="card" data-testid="optimization-total-saving">
        <div className="card-value">
          {formatMajor(total)}
        </div>
        <div className="card-label">{t('optimization.totalSaving')}</div>
        <div className="card-note">{t('optimization.perMonth')}</div>
      </div>
      <div className="card" data-testid="optimization-active-count">
        <div className="card-value">{active}</div>
        <div className="card-label">{t('optimization.active')}</div>
        <div className="card-note">
          {byRule.map((rc) => (
            <span key={rc.rule} className={`badge rule-${rc.rule}`} data-testid={`optimization-rule-chip-${rc.rule}`}>
              {t(`optimization.rule.${rc.rule}`)} × {rc.count}
            </span>
          ))}
        </div>
      </div>
      <div className="card" data-testid="optimization-dismissed-count">
        <div className="card-value">{dismissed}</div>
        <div className="card-label">{t('optimization.dismissed')}</div>
      </div>
      <div className="card" data-testid="optimization-last-refresh">
        <div className="card-value">
          {parseInt(lastRefresh, 10) > 0 ? formatTime(lastRefresh) : '—'}
        </div>
        <div className="card-label">{t('optimization.lastRefresh')}</div>
        {onRefresh && (
          <button
            className="secondary"
            data-testid="optimization-refresh-now"
            disabled={refreshing}
            onClick={onRefresh}
          >
            {refreshing ? t('common.loading') : t('optimization.refreshNow')}
          </button>
        )}
      </div>
    </div>
  );
}
