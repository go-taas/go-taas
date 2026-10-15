// OptimizationPage is the admin console cost-optimization page
// (feature #50, architecture §6): overview cards, filterable
// recommendation list, refresh-now, dismiss/restore lifecycle.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { ErrorBanner, usePolling } from '../components';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import type {
  DismissRecommendationResponse,
  GetOptimizationRecommendationResponse,
  ListOptimizationRecommendationsResponse,
  OptimizationOverview,
  OptimizationRecommendation,
  RefreshOptimizationResponse,
} from '../api';
import SummaryCards from '../components/optimization/SummaryCards';
import RecommendationCard from '../components/optimization/RecommendationCard';
import RecommendationDrawer from '../components/optimization/RecommendationDrawer';
import DismissDialog, { type DismissReason } from '../components/optimization/DismissDialog';

const RULES = ['r1_substitution', 'r2_batch', 'r3_idle_service', 'r4_cache'] as const;

export default function OptimizationPage() {
  const { t } = useI18n();
  const api = useApi();
  const { orgId } = useOrg();

  const [overview, setOverview] = useState<OptimizationOverview | null>(null);
  const [recommendations, setRecommendations] = useState<OptimizationRecommendation[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [permissionDenied, setPermissionDenied] = useState(false);

  const [rule, setRule] = useState('');
  const [status, setStatus] = useState('active');
  const [organization, setOrganization] = useState('');
  const [model, setModel] = useState('');
  const [minSaving, setMinSaving] = useState('');

  const [selected, setSelected] = useState<OptimizationRecommendation | null>(null);
  const [dismissTarget, setDismissTarget] = useState<OptimizationRecommendation | null>(null);

  const query = useMemo(() => {
    const q = new URLSearchParams();
    if (rule) q.set('rule', rule);
    if (status && status !== 'all') q.set('status', status);
    if (organization) q.set('organization_id', organization);
    if (model) q.set('model_id', model);
    if (minSaving) q.set('min_saving', minSaving);
    return q.toString();
  }, [rule, status, organization, model, minSaving]);

  const load = useCallback(async () => {
    if (!orgId) return;
    setLoading(true);
    setError('');
    try {
      const [ov, list] = await Promise.all([
        api.get<{ overview: OptimizationOverview }>(`/api/v1/admin/optimization`, orgId),
        api.get<ListOptimizationRecommendationsResponse>(
          `/api/v1/admin/optimization/recommendations${query ? `?${query}` : ''}`,
          orgId,
        ),
      ]);
      setOverview(ov.overview);
      setRecommendations(list.recommendations || []);
      setStale(false);
      setPermissionDenied(false);
    } catch (e: any) {
      if (e?.body?.code === 10036) {
        setPermissionDenied(true);
      } else {
        setError(e?.message || t('optimization.loadFailed'));
        setStale(true);
      }
    } finally {
      setLoading(false);
    }
  }, [api, orgId, query, t]);

  useEffect(() => {
    void load();
  }, [load]);

  usePolling(() => void load(), 30000, !loading);

  const refreshNow = useCallback(async () => {
    if (!orgId) return;
    setRefreshing(true);
    try {
      await api.post<RefreshOptimizationResponse>(`/api/v1/admin/optimization:refresh`, orgId, {});
      await load();
    } catch (e: any) {
      setError(e?.message || t('optimization.loadFailed'));
    } finally {
      setRefreshing(false);
    }
  }, [api, orgId, load, t]);

  const applyUpdate = useCallback((updated: OptimizationRecommendation) => {
    setRecommendations((prev) => prev.map((r) => (r.recommendationId === updated.recommendationId ? updated : r)));
    setSelected((prev) => (prev && prev.recommendationId === updated.recommendationId ? updated : prev));
  }, []);

  const dismiss = useCallback(
    async (reason: DismissReason, note: string) => {
      if (!dismissTarget || !orgId) return;
      try {
        const res = await api.post<DismissRecommendationResponse>(
          `/api/v1/admin/optimization/recommendations/${dismissTarget.recommendationId}:dismiss`,
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
          `/api/v1/admin/optimization/recommendations/${rec.recommendationId}:restore`,
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

  if (permissionDenied) {
    return (
      <div>
        <div className="page-header">
          <h1>{t('optimization.title')}</h1>
        </div>
        <div className="empty-state" data-testid="optimization-permission-denied">
          {t('optimization.permissionDenied')}
        </div>
      </div>
    );
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('optimization.title')}</h1>
          <p className="subtitle">{t('optimization.subtitle')}</p>
        </div>
        <button data-testid="optimization-refresh-now" onClick={() => void refreshNow()} disabled={refreshing}>
          {refreshing ? t('common.loading') : t('optimization.refreshNow')}
        </button>
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
          {RULES.map((r) => (
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
        <select
          data-testid="optimization-filter-organization"
          value={organization}
          onChange={(e) => setOrganization(e.target.value)}
        >
          <option value="">{t('optimization.allOrganizations')}</option>
          {(overview?.topOrganizationId
            ? [
                {
                  id: overview.topOrganizationId,
                  name: overview.topOrganizationName || overview.topOrganizationId,
                },
              ]
            : []
          ).map((o) => (
            <option key={o.id} value={o.id}>
              {o.name}
            </option>
          ))}
        </select>
        <select data-testid="optimization-filter-model" value={model} onChange={(e) => setModel(e.target.value)}>
          <option value="">{t('optimization.allModels')}</option>
          {[...new Set(recommendations.map((r) => r.targetId).filter(Boolean))].map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
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

      {overview && (
        <SummaryCards overview={overview} onRefresh={() => void refreshNow()} refreshing={refreshing} />
      )}

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
