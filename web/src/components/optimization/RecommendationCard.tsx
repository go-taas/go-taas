// RecommendationCard renders one ranked recommendation row (feature
// #50, design D8): the rule badge, target, suggestion one-liner, the
// "≈ ¥X / month" estimate, status, first-seen time and a chevron that
// opens the drawer.

import { useI18n } from '../../i18n';
import { formatTime } from '../../api';
import type { OptimizationRecommendation } from '../../api';
import { formatMajor } from '../../display';

export default function RecommendationCard({
  rec,
  onOpen,
}: {
  rec: OptimizationRecommendation;
  onOpen: (rec: OptimizationRecommendation) => void;
}) {
  const { t } = useI18n();
  return (
    <div
      className="card clickable"
      data-testid={`optimization-recommendation-card-${rec.recommendationId}`}
      onClick={() => onOpen(rec)}
    >
      <div className="card-row-inner">
        <span className={`badge rule-${rec.rule}`} data-testid={`optimization-card-rule-${rec.recommendationId}`}>
          {t(`optimization.rule.${rec.rule}`)}
        </span>
        <span className="mono" data-testid={`optimization-card-target-${rec.recommendationId}`}>
          {rec.targetDisplay}
        </span>
        {rec.status === 'dismissed' && (
          <span className="badge dismissed" data-testid={`optimization-card-status-${rec.recommendationId}`}>
            {t('optimization.dismissed')}
          </span>
        )}
      </div>
      <div className="card-note">{rec.suggestion}</div>
      <div className="card-row-inner">
        <span className="card-value" data-testid={`optimization-card-saving-${rec.recommendationId}`}>
          ≈ {formatMajor(rec.estimatedMonthlySaving)} / {t('optimization.month')}
        </span>
        <span className="muted">
          {t('optimization.firstSeen')} {formatTime(rec.firstSeenAt)}
        </span>
        <span className="chevron">›</span>
      </div>
    </div>
  );
}
