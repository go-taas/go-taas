// RecommendationDrawer renders the full recommendation detail
// (feature #50, design D8): the What (suggestion), the Why (inputs
// table), the arithmetic line, the R1 alternatives with prices, the
// one-click acting link and the dismiss/restore controls.

import { useMemo } from 'react';
import { navigate } from '../../router';
import { useI18n } from '../../i18n';
import type { OptimizationRecommendation } from '../../api';

// INPUT_LABELS maps the rule-specific inputs keys to i18n labels; the
// values render verbatim (numbers, booleans, nested objects as JSON).
const INPUT_LABEL_KEYS: Record<string, string> = {
  window_days: 'optimization.inputs.windowDays',
  requests: 'optimization.inputs.requests',
  prompt_tokens: 'optimization.inputs.promptTokens',
  completion_tokens: 'optimization.inputs.completionTokens',
  spend: 'optimization.inputs.spend',
  offline_ratio: 'optimization.inputs.offlineRatio',
  large_prompt_ratio: 'optimization.inputs.largePromptRatio',
  batch_discount: 'optimization.inputs.batchDiscount',
  replicas: 'optimization.inputs.replicas',
  hourly_cost: 'optimization.inputs.hourlyCost',
  idle_requests_per_day: 'optimization.inputs.idleRequestsPerDay',
  idle_window_days: 'optimization.inputs.idleWindowDays',
  accelerator_type: 'optimization.inputs.acceleratorType',
  cache_hit_rate: 'optimization.inputs.cacheHitRate',
  assumed_hit_rate: 'optimization.inputs.assumedHitRate',
  cached_tokens: 'optimization.inputs.cachedTokens',
  model_a: 'optimization.inputs.modelA',
  model_b: 'optimization.inputs.modelB',
  model_a_context_length: 'optimization.inputs.modelAContextLength',
  model_b_context_length: 'optimization.inputs.modelBContextLength',
  model_b_grant: 'optimization.inputs.modelBGrant',
  service_id: 'optimization.inputs.serviceId',
  service_name: 'optimization.inputs.serviceName',
};

export default function RecommendationDrawer({
  rec,
  onClose,
  onDismiss,
  onRestore,
}: {
  rec: OptimizationRecommendation;
  onClose: () => void;
  onDismiss: (rec: OptimizationRecommendation) => void;
  onRestore: (rec: OptimizationRecommendation) => void;
}) {
  const { t } = useI18n();

  // Parse the rule-specific inputs object for the Why table.
  const inputs = useMemo<Record<string, unknown>>(() => {
    try {
      return rec.inputsJson ? (JSON.parse(rec.inputsJson) as Record<string, unknown>) : {};
    } catch {
      return {};
    }
  }, [rec.inputsJson]);

  const acting = rec.actingPage;

  return (
    <div className="dialog-backdrop" data-testid="optimization-drawer-backdrop">
      <div className="dialog drawer" data-testid="optimization-drawer">
        <div className="page-header">
          <div>
            <h2>
              <span className={`badge rule-${rec.rule}`}>{t(`optimization.rule.${rec.rule}`)}</span>{' '}
              {rec.targetDisplay}
            </h2>
          </div>
          <button className="secondary" onClick={onClose} data-testid="optimization-drawer-close">
            {t('common.close')}
          </button>
        </div>

        {/* What */}
        <h3>{t('optimization.what')}</h3>
        <p data-testid="optimization-drawer-what">{rec.suggestion}</p>
        <p className="card-value">
          ≈ {rec.currency} {rec.estimatedMonthlySaving} / {t('optimization.month')}
        </p>

        {/* Why — the rule inputs */}
        <h3>{t('optimization.why')}</h3>
        <table className="data" data-testid="optimization-drawer-inputs">
          <tbody>
            {Object.entries(inputs)
              .filter(([k]) => INPUT_LABEL_KEYS[k])
              .map(([k, v]) => (
                <tr key={k}>
                  <td>{t(INPUT_LABEL_KEYS[k])}</td>
                  <td className="mono">
                    {typeof v === 'object' ? JSON.stringify(v) : String(v)}
                  </td>
                </tr>
              ))}
          </tbody>
        </table>

        {/* The arithmetic line */}
        <h3>{t('optimization.arithmeticTitle')}</h3>
        <p className="mono" data-testid="optimization-drawer-arithmetic">
          {rec.arithmetic}
        </p>

        {/* R1 alternatives */}
        {(rec.alternatives || []).length > 0 && (
          <>
            <h3>{t('optimization.alternativesTitle')}</h3>
            <table className="data" data-testid="optimization-drawer-alternatives">
              <thead>
                <tr>
                  <th>{t('optimization.colAlternativeModel')}</th>
                  <th>{t('optimization.colAlternativeSaving')}</th>
                  <th>{t('optimization.colAlternativeInputPrice')}</th>
                  <th>{t('optimization.colAlternativeOutputPrice')}</th>
                </tr>
              </thead>
              <tbody>
                {rec.alternatives.map((alt) => (
                  <tr key={alt.modelId}>
                    <td className="mono">{alt.modelName}</td>
                    <td>
                      {rec.currency} {alt.estimatedMonthlySaving}
                    </td>
                    <td className="mono">{alt.inputPricePerMillion}</td>
                    <td className="mono">{alt.outputPricePerMillion}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        )}

        {/* The acting link */}
        {acting && acting.route && (
          <>
            <h3>{t('optimization.actTitle')}</h3>
            <button
              data-testid="optimization-drawer-acting-link"
              onClick={() => {
                onClose();
                navigate(acting.route);
              }}
            >
              {acting.label} →
            </button>
          </>
        )}

        {/* Dismiss / restore */}
        <div className="filter-bar">
          {rec.status === 'dismissed' ? (
            <button
              className="secondary"
              data-testid={`optimization-restore-${rec.recommendationId}`}
              onClick={() => onRestore(rec)}
            >
              {t('optimization.restore')}
            </button>
          ) : (
            <button
              className="secondary"
              data-testid={`optimization-dismiss-open-${rec.recommendationId}`}
              onClick={() => onDismiss(rec)}
            >
              {t('optimization.dismiss')}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
