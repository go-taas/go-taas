// UserOptimizationPage is the end-user console cost-optimization page
// (feature #50, architecture §6): same components as the admin page but
// scoped to the caller's organization via /api/v1/optimization, without
// organization/model filters, the idle-service rule option, or
// refresh-now (refresh is admin-triggered).

import { useCallback, useEffect, useMemo, useState } from 'react';
import { ErrorBanner, usePolling } from '../../components';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import type {
  DismissRecommendationResponse,
  GetOptimizationRecommendationResponse,
  GetMyOptimizationResponse,
  ListOptimizationRecommendationsResponse,
  OptimizationRecommendation,
} from '../../api';
import SummaryCards from '../../components/optimization/SummaryCards';
import RecommendationCard from '../../components/optimization/RecommendationCard';
import RecommendationDrawer from '../../components/optimization/RecommendationDrawer';
import DismissDialog, { type DismissReason } from '../../components/optimization/DismissDialog';

// r3_idle_service targets platform services and never appears on the
// user surface, so it is excluded from the user rule filter.
const USER_RULES = ['r1_substitution', 'r2_batch', 'r4_cache'] as const;

export default function UserOptimizationPage() {
  const { t } = useI18n();
  const api = useApi();
  const { orgId } = useOrg();

  const [overview, setOverview] = useState<GetMyOptimizationResponse['overview'] | null>(null);  const [recommendations, setRecommendations] = useState<OptimizationRecommendation[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  const [rule, setRule] = useState('');
  const [status, setStatus] = useState('active');
  const [minSaving, setMinSaving] = useState('');

  const [selected, setSelected] = useState<OptimizationRecommendation | null>(null);
  const [dismissTarget, setDismissTarget] = useState<OptimizationRecommendation | null>(null);

  const query = useMemo(() => {
    const q = new URLSearchParams();
    if (rule) q.set('rule', rule);
    if (status && status !== 'all') q.set('status', status);
    if (minSaving) q.set('min_saving', minSaving);
    return q.toString();
  }, [rule, status, minSaving]);

  const load = useCallback(async () => {
    if (!orgId) return;
    setLoading(true);
    setError('');
    try {
      const [mine, list] = await Promise.all([
        api.get<GetMyOptimizationResponse>(`/api/v1/optimization`, orgId),
        api.get<ListOptimizationRecommendationsResponse>(
          `/api/v1/optimization/recommendations${query ? `?${query}` : ''}`,
          orgId,
        ),
      ]);
      setOverview(mine.overview);
      setRecommendations(list.recommendations || []);
      setStale(false);
    } catch (e: any) {
      setError(e?.message || t('optimization.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [api, orgId, query, t]);

  useEffect(() => {
    void load();
  }, [load]);

  usePolling(() => void load(), 30000, !loading);

  const applyUpdate = useCallback((updated: OptimizationRecommendation) => {
    setRecommendations((prev) => prev.map((r) => (r.recommendationId === updated.recommendationId ? updated : r)));
    setSelected((prev) => (prev && prev.recommendationId === updated.recommendationId ? updated : prev));
  }, []);

  const dismiss = useCallback(
    async (reason: DismissReason, note: string) => {
      if (!dismissTarget || !orgId) return;
      try {
        const res = await api.post<DismissRecommendationResponse>(
          `/api/v1/optimization/recommendations/${dismissTarget.recommendationId}:dismiss`,
          orgId,
          { reason, note },
        );
        if (res.recommendation) applyUpdate(res.recommendation);
        setDismissTarget(null);
        await load();
      } catch (e: any) {
        setError(e?.message || t('optimization.loadFailed'));
      }
    },
    [api, orgId, dismissTarget, applyUpdate, load, t],
  );

  const restore = useCallback(
    async (rec: OptimizationRecommendation) => {
      if (!orgId) return;
      try {
        const res = await api.post<GetOptimizationRecommendationResponse>(
          `/api/v1/optimization/recommendations/${rec.recommendationId}:restore`,
          orgId,
          {},
        );
        if (res.recommendation) applyUpdate(res.recommendation);
        await load();
      } catch (e: any) {
        setError(e?.message || t('optimization.loadFailed'));
      }
    },
    [api, orgId, applyUpdate, load, t],
  );

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('optimization.title')}</h1>
          <p className="subtitle">{t('optimization.subtitle')}</p>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}
      {stale && !error && (
        <div className="error-banner" data-testid="optimization-stale-banner">
          {t('optimization.staleData')}
        </div>
      )}

      <div className="filter-bar">
        <select data-testid="optimization-filter-rule" value={rule} onChange={(e) => setRule(e.target.value)}>
          <option value="">{t('optimization.allRules')}</option>
          {USER_RULES.map((r) => (
            <option key={r} value={r}>
              {t(`optimization.rule.${r}`)}
            </option>
          ))}
        </select>
        <select data-testid="optimization-filter-status" value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="active">{t('optimization.statusActive')}</option>
          <option value="dismissed">{t('optimization.statusDismissed')}</option>
          <option value="all">{t('optimization.statusAll')}</option>
        </select>
        <input
          data-testid="optimization-filter-min-saving"
          type="number"
          min="0"
          placeholder={t('optimization.filterMinSaving')}
          value={minSaving}
          onChange={(e) => setMinSaving(e.target.value)}
        />
      </div>

      {overview && <SummaryCards overview={overview} />}

      {loading && recommendations.length === 0 ? (
        <div className="loading">{t('common.loading')}</div>
      ) : recommendations.length === 0 ? (
        <div className="empty-state" data-testid="optimization-empty">
          <p>{t('optimization.empty')}</p>
          <p className="muted">{t('optimization.emptyHint')}</p>
        </div>
      ) : (
        <div className="cards-row">
          {recommendations.map((rec) => (
            <RecommendationCard key={rec.recommendationId} rec={rec} onOpen={() => setSelected(rec)} />
          ))}
        </div>
      )}

      {selected && (
        <RecommendationDrawer
          rec={selected}
          onClose={() => setSelected(null)}
          onDismiss={(r) => {
            setDismissTarget(r);
            setSelected(null);
          }}
          onRestore={restore}
        />
      )}

      {dismissTarget && (
        <DismissDialog
          targetDisplay={dismissTarget.targetDisplay}
          onCancel={() => setDismissTarget(null)}
          onSubmit={(reason, note) => void dismiss(reason, note)}
        />
      )}
    </div>
  );
}
