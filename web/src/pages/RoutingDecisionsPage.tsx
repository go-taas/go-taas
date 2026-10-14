// Admin Routing decisions page (feature #49): the decision log with
// filters and a per-decision drawer.
// Implements docs/design/intelligent-model-routing.md §5.5.
// Admin surface only: route /admin/routing-groups/decisions,
// API /api/v1/admin/routing-groups/*.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import {
  formatTime,
  type ListRoutingDecisionsResponse,
  type RoutingDecision,
  type RoutingGroup,
} from '../api';
import { ErrorBanner, Pagination } from '../components';
import { navigate } from '../router';
import { useI18n } from '../i18n';

const PAGE_SIZE = 20;
const STRATEGIES = ['manual', 'weighted', 'cost_priority', 'latency_priority'];
const RANGES = ['1h', '24h', '7d'];

export default function RoutingDecisionsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [decisions, setDecisions] = useState<RoutingDecision[]>([]);
  const [groups, setGroups] = useState<RoutingGroup[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [groupFilter, setGroupFilter] = useState('');
  const [strategyFilter, setStrategyFilter] = useState('');
  const [matchedRuleSearch, setMatchedRuleSearch] = useState('');
  const [selectedModelSearch, setSelectedModelSearch] = useState('');
  const [outcomeFilter, setOutcomeFilter] = useState('');
  const [range, setRange] = useState('24h');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [selected, setSelected] = useState<RoutingDecision | null>(null);

  // The URL accepts ?group=<group_id> to pre-filter (design §5.5).
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const group = params.get('group');
    if (group) setGroupFilter(group);
  }, []);

  const load = useCallback(
    async (showLoading: boolean) => {
      if (showLoading) setLoading(true);
      setError('');
      try {
        const params = new URLSearchParams({
          'page.offset': String(offset),
          'page.limit': String(PAGE_SIZE),
          range,
        });
        if (groupFilter) params.set('group_id', groupFilter);
        if (strategyFilter) params.set('strategy', strategyFilter);
        if (matchedRuleSearch) params.set('matched_rule', matchedRuleSearch);
        if (selectedModelSearch) params.set('selected_model', selectedModelSearch);
        if (outcomeFilter) params.set('outcome', outcomeFilter);
        const data = await api.get<ListRoutingDecisionsResponse>(
          `/api/v1/admin/routing-groups/decisions?${params.toString()}`,
          orgId,
        );
        setDecisions(data.decisions || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
        setPermissionDenied(false);
      } catch (e) {
        const message = e instanceof Error ? e.message : '';
        if (message.includes('10036') || message.includes('10001')) {
          setPermissionDenied(true);
        } else {
          setError(t('routingDecisions.loadFailed'));
        }
      } finally {
        setLoading(false);
      }
    },
    [
      api,
      orgId,
      offset,
      groupFilter,
      strategyFilter,
      matchedRuleSearch,
      selectedModelSearch,
      outcomeFilter,
      range,
      t,
    ],
  );

  useEffect(() => {
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [offset, groupFilter, strategyFilter, outcomeFilter, range]);

  // The group filter options come from the groups list.
  useEffect(() => {
    api
      .get<{ groups?: RoutingGroup[] }>(
        '/api/v1/admin/routing-groups?page.limit=200',
        orgId,
      )
      .then((data) => setGroups(data.groups || []))
      .catch(() => {
        // The filter simply stays empty; the decisions list still loads.
      });
  }, [api, orgId]);

  if (permissionDenied) {
    return (
      <div className="page" data-testid="routing-decisions-page">
        <div className="page-header">
          <div>
            <h1>{t('routingDecisions.title')}</h1>
          </div>
        </div>
        <div className="empty-state" data-testid="routing-decisions-permission-denied">
          <p>{t('routingDecisions.permissionDenied')}</p>
          <a
            href="/admin"
            onClick={(e) => {
              e.preventDefault();
              navigate('/admin');
            }}
          >
            {t('routingDecisions.backToAdmin')}
          </a>
        </div>
      </div>
    );
  }

  return (
    <div className="page" data-testid="routing-decisions-page">
      <div className="page-header">
        <div>
          <h1>{t('routingDecisions.title')}</h1>
          <div className="subtitle">{t('routingDecisions.subtitle')}</div>
        </div>
        <div className="actions">
          <button
            className="secondary"
            onClick={() => void load(false)}
            disabled={loading}
            data-testid="routing-decisions-refresh"
          >
            {t('common.refresh')}
          </button>
        </div>
      </div>

      {error && (
        <div data-testid="routing-decisions-error">
          <ErrorBanner message={error} />
          <button
            className="secondary"
            onClick={() => void load(false)}
            data-testid="routing-decisions-retry"
          >
            {t('common.retry')}
          </button>
        </div>
      )}

      <div className="panel" data-testid="routing-decisions-list">
        <div className="filters">
          <select
            value={groupFilter}
            onChange={(e) => {
              setOffset(0);
              setGroupFilter(e.target.value);
            }}
            disabled={loading}
            data-testid="routing-decisions-group-filter"
          >
            <option value="">{t('routingDecisions.filterAllGroups')}</option>
            {groups.map((g) => (
              <option key={g.groupId} value={g.groupId}>
                {g.name}
              </option>
            ))}
          </select>
          <select
            value={strategyFilter}
            onChange={(e) => {
              setOffset(0);
              setStrategyFilter(e.target.value);
            }}
            disabled={loading}
            data-testid="routing-decisions-strategy-filter"
          >
            <option value="">{t('routingDecisions.filterAllStrategies')}</option>
            {STRATEGIES.map((s) => (
              <option key={s} value={s}>
                {t(`routingGroups.strategy.${s}`)}
              </option>
            ))}
          </select>
          <input
            placeholder={t('routingDecisions.matchedRuleSearch')}
            value={matchedRuleSearch}
            onChange={(e) => setMatchedRuleSearch(e.target.value)}
            disabled={loading}
            data-testid="routing-decisions-matched-rule-search"
          />
          <input
            placeholder={t('routingDecisions.selectedModelSearch')}
            value={selectedModelSearch}
            onChange={(e) => setSelectedModelSearch(e.target.value)}
            disabled={loading}
            data-testid="routing-decisions-selected-model-search"
          />
          <select
            value={outcomeFilter}
            onChange={(e) => {
              setOffset(0);
              setOutcomeFilter(e.target.value);
            }}
            disabled={loading}
            data-testid="routing-decisions-outcome-filter"
          >
            <option value="">{t('routingDecisions.filterAllOutcomes')}</option>
            <option value="served">{t('routingDecisions.outcome.served')}</option>
            <option value="failed">{t('routingDecisions.outcome.failed')}</option>
          </select>
          <select
            value={range}
            onChange={(e) => {
              setOffset(0);
              setRange(e.target.value);
            }}
            disabled={loading}
            data-testid="routing-decisions-range-filter"
          >
            {RANGES.map((r) => (
              <option key={r} value={r}>
                {t(`routingDecisions.range.${r}`)}
              </option>
            ))}
          </select>
          <button
            className="secondary"
            onClick={() => void load(false)}
            disabled={loading}
            data-testid="routing-decisions-apply-filters"
          >
            {t('common.refresh')}
          </button>
        </div>

        {loading ? (
          <div className="loading" data-testid="routing-decisions-loading">
            {t('common.loading')}
          </div>
        ) : decisions.length === 0 ? (
          <div className="empty-state" data-testid="routing-decisions-empty">
            {t('routingDecisions.empty')}
          </div>
        ) : (
          <>
            <table className="data" data-testid="routing-decisions-table">
              <thead>
                <tr>
                  <th>{t('routingDecisions.colTime')}</th>
                  <th>{t('routingDecisions.colGroup')}</th>
                  <th>{t('routingDecisions.colStrategy')}</th>
                  <th>{t('routingDecisions.colMatchedRule')}</th>
                  <th>{t('routingDecisions.colSelectedModel')}</th>
                  <th>{t('routingDecisions.colOutcome')}</th>
                  <th>{t('routingDecisions.colRequest')}</th>
                </tr>
              </thead>
              <tbody>
                {decisions.map((d) => (
                  <tr
                    key={d.decisionId}
                    onClick={() => setSelected(d)}
                    style={{ cursor: 'pointer' }}
                    data-testid={`routing-decision-row-${d.decisionId}`}
                  >
                    <td>{formatTime(String(d.createdAt))}</td>
                    <td className="mono">{d.groupName}</td>
                    <td>{t(`routingGroups.strategy.${d.strategy}`)}</td>
                    <td>{d.matchedRuleName || '—'}</td>
                    <td className="mono">{d.selectedModelName || '—'}</td>
                    <td>
                      <span
                        className={`badge ${d.selectedModelId ? 'running' : 'terminated'}`}
                      >
                        {d.selectedModelId
                          ? t('routingDecisions.outcome.served')
                          : t('routingDecisions.outcome.failed')}
                      </span>
                    </td>
                    <td className="mono" title={d.requestId}>
                      {d.requestId.length > 12
                        ? `${d.requestId.slice(0, 12)}…`
                        : d.requestId}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Pagination
              offset={offset}
              limit={PAGE_SIZE}
              total={total}
              onPageChange={setOffset}
            />
          </>
        )}
      </div>

      {selected && (
        <div className="dialog-backdrop" data-testid="routing-decision-drawer-backdrop">
          <div className="dialog drawer" data-testid="routing-decision-drawer">
            <div className="page-header">
              <div>
                <h2>{t('routingDecisions.drawerTitle')}</h2>
              </div>
              <button
                className="secondary"
                onClick={() => setSelected(null)}
                data-testid="routing-decision-drawer-close"
              >
                {t('common.close')}
              </button>
            </div>
            <div className="detail-grid" data-testid="routing-decision-detail">
              <div>
                <div className="label">{t('routingDecisions.colGroup')}</div>
                <div className="value mono">{selected.groupName}</div>
              </div>
              <div>
                <div className="label">{t('routingDecisions.colStrategy')}</div>
                <div className="value">
                  {t(`routingGroups.strategy.${selected.strategy}`)}
                </div>
              </div>
              <div>
                <div className="label">{t('routingDecisions.colMatchedRule')}</div>
                <div className="value">{selected.matchedRuleName || '—'}</div>
              </div>
              <div>
                <div className="label">{t('routingDecisions.colSelectedModel')}</div>
                <div className="value mono">{selected.selectedModelName || '—'}</div>
              </div>
              <div>
                <div className="label">{t('routingDecisions.apiKey')}</div>
                <div className="value mono">{selected.apiKeyId || '—'}</div>
              </div>
              <div>
                <div className="label">{t('routingDecisions.organization')}</div>
                <div className="value mono">{selected.organizationId || '—'}</div>
              </div>
              <div>
                <div className="label">{t('routingDecisions.request')}</div>
                <div className="value mono">{selected.requestId}</div>
              </div>
              <div>
                <div className="label">{t('routingDecisions.createdAt')}</div>
                <div className="value">{formatTime(String(selected.createdAt))}</div>
              </div>
            </div>
            <h3>{t('routingDecisions.candidatesConsidered')}</h3>
            {(selected.candidatesConsidered || []).length === 0 ? (
              <p className="muted">—</p>
            ) : (
              <table className="data" data-testid="routing-decision-candidates-table">
                <thead>
                  <tr>
                    <th>{t('routingDecisions.colCandidateModel')}</th>
                    <th>{t('routingDecisions.colCandidateOutcome')}</th>
                    <th>{t('routingDecisions.colCandidateDetail')}</th>
                  </tr>
                </thead>
                <tbody>
                  {(selected.candidatesConsidered || []).map((c, i) => (
                    <tr
                      key={`${c.modelId}-${i}`}
                      data-testid={`routing-decision-candidate-row-${c.modelId}`}
                    >
                      <td className="mono">{c.modelName || c.modelId}</td>
                      <td>{c.outcome}</td>
                      <td>{c.detail || '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
