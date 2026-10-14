// Admin Routing group detail page (feature #49): overview form, ordered
// candidate editor, intent-rule editor and the dry-run test panel.
// Implements docs/design/intelligent-model-routing.md §5.4.
// Admin surface only: route /admin/routing-groups/:groupId,
// API /api/v1/admin/routing-groups/*.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import {
  type GetRoutingGroupResponse,
  type ModelSummary,
  type RoutingGroup,
  type RoutingGroupCandidate,
  type RoutingGroupRule,
  type TestRoutingResolutionResponse,
} from '../api';
import { BackLink, Dialog, ErrorBanner } from '../components';
import { navigate, useParams } from '../router';
import { useI18n } from '../i18n';

const STRATEGIES = ['manual', 'weighted', 'cost_priority', 'latency_priority'];
const NAME_PATTERN = /^[a-z][a-z0-9-]{2,63}$/;

// CandidateDraft is the editable row state of the candidate editor.
interface CandidateDraft {
  modelId: string;
  weight: string;
  costTier: string;
  latencyTier: string;
  pinned: boolean;
}

// RuleDraft is the editable row state of the rule editor.
interface RuleDraft {
  name: string;
  matchType: string;
  pattern: string;
  targetModelId: string;
  enabled: boolean;
}

function strategyBadgeClass(strategy: string): string {
  switch (strategy) {
    case 'manual':
      return 'badge pending';
    case 'cost_priority':
      return 'badge deploying';
    case 'latency_priority':
      return 'badge running';
    default:
      return 'badge';
  }
}

export default function RoutingGroupDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const { groupId } = useParams();
  const [group, setGroup] = useState<RoutingGroup | null>(null);
  const [candidates, setCandidates] = useState<RoutingGroupCandidate[]>([]);
  const [rules, setRules] = useState<RoutingGroupRule[]>([]);
  const [catalog, setCatalog] = useState<ModelSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [permissionDenied, setPermissionDenied] = useState(false);

  // Overview form state.
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [strategy, setStrategy] = useState('weighted');
  const [enabled, setEnabled] = useState(false);
  const [savingOverview, setSavingOverview] = useState(false);
  const [overviewError, setOverviewError] = useState('');
  const [overviewToast, setOverviewToast] = useState('');

  // Candidate editor state.
  const [candidateDrafts, setCandidateDrafts] = useState<CandidateDraft[]>([]);
  const [savingCandidates, setSavingCandidates] = useState(false);
  const [candidatesError, setCandidatesError] = useState('');
  const [candidatesToast, setCandidatesToast] = useState('');

  // Rule editor state.
  const [ruleDrafts, setRuleDrafts] = useState<RuleDraft[]>([]);
  const [savingRules, setSavingRules] = useState(false);
  const [rulesError, setRulesError] = useState('');
  const [rulesToast, setRulesToast] = useState('');

  // Test panel state.
  const [testPrompt, setTestPrompt] = useState('');
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<TestRoutingResolutionResponse | null>(null);
  const [testError, setTestError] = useState('');

  // Delete dialog state.
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [groupRes, catalogRes] = await Promise.all([
        api.get<GetRoutingGroupResponse>(
          `/api/v1/admin/routing-groups/${groupId}`,
          orgId,
        ),
        api.get<{ models?: ModelSummary[] }>(
          '/api/v1/admin/models?page.limit=200',
          orgId,
        ),
      ]);
      const g = groupRes.group;
      setGroup(g || null);
      setCandidates(groupRes.candidates || []);
      setRules(groupRes.rules || []);
      setName(g?.name || '');
      setDescription(g?.description || '');
      setStrategy(g?.strategy || 'weighted');
      setEnabled(g?.enabled || false);
      setCandidateDrafts(
        (groupRes.candidates || []).map((c) => ({
          modelId: c.modelId,
          weight: String(c.weight || 10),
          costTier: c.costTier ? String(c.costTier) : '',
          latencyTier: c.latencyTier ? String(c.latencyTier) : '',
          pinned: c.pinned,
        })),
      );
      setRuleDrafts(
        (groupRes.rules || []).map((r) => ({
          name: r.name,
          matchType: r.matchType,
          pattern: r.pattern,
          targetModelId: r.targetModelId,
          enabled: r.enabled,
        })),
      );
      setCatalog(catalogRes.models || []);
      setPermissionDenied(false);
    } catch (e) {
      const message = e instanceof Error ? e.message : '';
      if (message.includes('10036') || message.includes('10001')) {
        setPermissionDenied(true);
      } else {
        setError(t('routingGroupDetail.loadFailed'));
      }
    } finally {
      setLoading(false);
    }
  }, [api, orgId, groupId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // Toast auto-dismiss helpers.
  useEffect(() => {
    if (!overviewToast && !candidatesToast && !rulesToast) return;
    const timer = setTimeout(() => {
      setOverviewToast('');
      setCandidatesToast('');
      setRulesToast('');
    }, 3000);
    return () => clearTimeout(timer);
  }, [overviewToast, candidatesToast, rulesToast]);

  const byModelId = useMemo(() => {
    const map = new Map<string, ModelSummary>();
    for (const m of catalog) map.set(m.modelId, m);
    return map;
  }, [catalog]);

  const overviewDirty = useMemo(
    () =>
      !!group &&
      (name !== group.name ||
        description !== group.description ||
        strategy !== group.strategy ||
        enabled !== group.enabled),
    [group, name, description, strategy, enabled],
  );

  const nameValid = NAME_PATTERN.test(name);
  const overviewValid = nameValid && description.length <= 256;

  // Candidate validation per design §5.4.
  const candidateErrors = useMemo(() => {
    const errors: string[] = [];
    const seen = new Set<string>();
    let pinned = 0;
    for (const draft of candidateDrafts) {
      if (!draft.modelId) errors.push(t('routingGroupDetail.selectModel'));
      if (seen.has(draft.modelId)) errors.push(t('routingGroupDetail.duplicateModel'));
      seen.add(draft.modelId);
      const weight = Number(draft.weight);
      if (!Number.isInteger(weight) || weight < 1 || weight > 100)
        errors.push(t('routingGroupDetail.weightInvalid'));
      if (draft.costTier !== '') {
        const tier = Number(draft.costTier);
        if (!Number.isInteger(tier) || tier < 1 || tier > 5)
          errors.push(t('routingGroupDetail.costTierInvalid'));
      }
      if (draft.latencyTier !== '') {
        const tier = Number(draft.latencyTier);
        if (!Number.isInteger(tier) || tier < 1 || tier > 5)
          errors.push(t('routingGroupDetail.latencyTierInvalid'));
      }
      if (draft.pinned) pinned++;
    }
    if (pinned > 1 || (pinned === 1 && strategy !== 'manual'))
      errors.push(t('routingGroupDetail.pinnedInvalid'));
    return errors;
  }, [candidateDrafts, strategy, t]);

  const candidatesDirty = useMemo(() => {
    if (candidateDrafts.length !== candidates.length) return true;
    return candidateDrafts.some((draft, i) => {
      const saved = candidates[i];
      if (!saved) return true;
      return (
        draft.modelId !== saved.modelId ||
        Number(draft.weight) !== saved.weight ||
        (draft.costTier ? Number(draft.costTier) : 0) !== saved.costTier ||
        (draft.latencyTier ? Number(draft.latencyTier) : 0) !== saved.latencyTier ||
        draft.pinned !== saved.pinned
      );
    });
  }, [candidateDrafts, candidates]);

  // Rule validation per design §5.4.
  const ruleErrors = useMemo(() => {
    const errors: string[] = [];
    const seen = new Set<string>();
    const candidateIds = new Set(candidateDrafts.map((d) => d.modelId));
    for (const draft of ruleDrafts) {
      if (!draft.name.trim()) errors.push(t('routingGroupDetail.ruleNameInvalid'));
      if (seen.has(draft.name)) errors.push(t('routingGroupDetail.ruleNameConflict'));
      seen.add(draft.name);
      if (draft.matchType !== 'keyword' && draft.matchType !== 'regex')
        errors.push(t('routingGroupDetail.matchTypeInvalid'));
      if (!draft.pattern) errors.push(t('routingGroupDetail.patternInvalid'));
      if (draft.matchType === 'regex') {
        try {
          new RegExp(draft.pattern);
        } catch {
          errors.push(t('routingGroupDetail.regexInvalid'));
        }
      }
      if (!candidateIds.has(draft.targetModelId))
        errors.push(t('routingGroupDetail.targetInvalid'));
    }
    return errors;
  }, [ruleDrafts, candidateDrafts, t]);

  const rulesDirty = useMemo(() => {
    if (ruleDrafts.length !== rules.length) return true;
    return ruleDrafts.some((draft, i) => {
      const saved = rules[i];
      if (!saved) return true;
      return (
        draft.name !== saved.name ||
        draft.matchType !== saved.matchType ||
        draft.pattern !== saved.pattern ||
        draft.targetModelId !== saved.targetModelId ||
        draft.enabled !== saved.enabled
      );
    });
  }, [ruleDrafts, rules]);

  const saveOverview = async () => {
    setSavingOverview(true);
    setOverviewError('');
    try {
      await api.patch(`/api/v1/admin/routing-groups/${groupId}`, orgId, {
        group_id: groupId,
        name,
        description,
        strategy,
        enabled,
      });
      setOverviewToast(t('routingGroupDetail.saved'));
      await load();
    } catch (e) {
      const message = e instanceof Error ? e.message : '';
      if (message.includes('13102')) setOverviewError(t('routingGroups.nameConflict'));
      else if (message.includes('13113')) setOverviewError(t('routingGroups.nameReserved'));
      else if (message.includes('13103')) setOverviewError(t('routingGroups.nameInvalid'));
      else setOverviewError(message || t('routingGroups.saveFailed'));
    } finally {
      setSavingOverview(false);
    }
  };

  const saveCandidates = async () => {
    setSavingCandidates(true);
    setCandidatesError('');
    try {
      await api.put(`/api/v1/admin/routing-groups/${groupId}/candidates`, orgId, {
        group_id: groupId,
        candidates: candidateDrafts.map((draft) => ({
          model_id: draft.modelId,
          weight: Number(draft.weight) || 10,
          cost_tier: draft.costTier ? Number(draft.costTier) : 0,
          latency_tier: draft.latencyTier ? Number(draft.latencyTier) : 0,
          pinned: draft.pinned,
        })),
      });
      setCandidatesToast(t('routingGroupDetail.candidatesSaved'));
      await load();
    } catch (e) {
      const message = e instanceof Error ? e.message : '';
      if (message.includes('13106')) setCandidatesError(t('routingGroupDetail.duplicateModel'));
      else if (message.includes('13104'))
        setCandidatesError(t('routingGroupDetail.candidatesInvalid'));
      else setCandidatesError(message || t('routingGroupDetail.candidatesInvalid'));
    } finally {
      setSavingCandidates(false);
    }
  };

  const saveRules = async () => {
    setSavingRules(true);
    setRulesError('');
    try {
      await api.put(`/api/v1/admin/routing-groups/${groupId}/rules`, orgId, {
        group_id: groupId,
        rules: ruleDrafts.map((draft) => ({
          name: draft.name,
          match_type: draft.matchType,
          pattern: draft.pattern,
          target_model_id: draft.targetModelId,
          enabled: draft.enabled,
        })),
      });
      setRulesToast(t('routingGroupDetail.rulesSaved'));
      await load();
    } catch (e) {
      const message = e instanceof Error ? e.message : '';
      if (message.includes('13112')) setRulesError(t('routingGroupDetail.ruleNameConflict'));
      else if (message.includes('13108')) setRulesError(t('routingGroupDetail.rulesInvalid'));
      else setRulesError(message || t('routingGroupDetail.rulesInvalid'));
    } finally {
      setSavingRules(false);
    }
  };

  const runTest = async () => {
    setTesting(true);
    setTestError('');
    setTestResult(null);
    try {
      const data = await api.post<TestRoutingResolutionResponse>(
        `/api/v1/admin/routing-groups/${groupId}:resolve`,
        orgId,
        { group_id: groupId, prompt: testPrompt },
      );
      setTestResult(data);
    } catch (e) {
      setTestError(e instanceof Error ? e.message : t('routingGroupDetail.testFailed'));
    } finally {
      setTesting(false);
    }
  };

  const confirmDelete = async () => {
    setDeleting(true);
    try {
      await api.del(`/api/v1/admin/routing-groups/${groupId}`, orgId);
      navigate('/admin/routing-groups');
    } catch (e) {
      setOverviewError(e instanceof Error ? e.message : t('routingGroups.loadFailed'));
      setDeleteOpen(false);
    } finally {
      setDeleting(false);
    }
  };

  const moveCandidate = (index: number, delta: number) => {
    const next = [...candidateDrafts];
    const target = index + delta;
    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target], next[index]];
    setCandidateDrafts(next);
  };

  const moveRule = (index: number, delta: number) => {
    const next = [...ruleDrafts];
    const target = index + delta;
    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target], next[index]];
    setRuleDrafts(next);
  };

  if (permissionDenied) {
    return (
      <div className="page" data-testid="routing-group-detail-page">
        <div className="empty-state" data-testid="routing-group-detail-permission-denied">
          <p>{t('routingGroups.permissionDenied')}</p>
          <a
            href="/admin"
            onClick={(e) => {
              e.preventDefault();
              navigate('/admin');
            }}
          >
            {t('routingGroups.backToAdmin')}
          </a>
        </div>
      </div>
    );
  }

  if (loading) {
    return (
      <div className="page" data-testid="routing-group-detail-page">
        <div className="loading">{t('common.loading')}</div>
      </div>
    );
  }

  if (error || !group) {
    return (
      <div className="page" data-testid="routing-group-detail-page">
        <BackLink to="/admin/routing-groups" label={t('routingGroupDetail.breadcrumb')} />
        <div data-testid="routing-group-detail-error">
          <ErrorBanner message={error || t('routingGroupDetail.notFound')} />
          <button className="secondary" onClick={() => void load()}>
            {t('common.retry')}
          </button>
        </div>
      </div>
    );
  }

  return (
    <div className="page" data-testid="routing-group-detail-page">
      <BackLink to="/admin/routing-groups" label={t('routingGroupDetail.breadcrumb')} />
      <div className="page-header">
        <div>
          <h1 data-testid="routing-group-detail-name">
            <span className="mono">{group.name}</span>{' '}
            <span className="badge">{t('umodels.auto')}</span>{' '}
            <span className={strategyBadgeClass(group.strategy)}>
              {t(`routingGroups.strategy.${group.strategy}`)}
            </span>{' '}
            <span className={`badge ${group.enabled ? 'running' : 'pending'}`}>
              {group.enabled
                ? t('routingGroups.filterEnabled')
                : t('routingGroups.filterDisabled')}
            </span>
          </h1>
        </div>
        <div className="actions">
          <button
            className="secondary"
            onClick={() =>
              navigate(`/admin/routing-groups/decisions?group=${groupId}`)
            }
            data-testid="routing-group-view-decisions"
          >
            {t('routingGroups.viewDecisions')}
          </button>
          <button
            className="secondary danger"
            onClick={() => setDeleteOpen(true)}
            disabled={deleting}
            title={t('routingGroups.deleteBlockedTooltip')}
            data-testid="routing-group-delete"
          >
            {t('routingGroupDetail.deleteGroup')}
          </button>
        </div>
      </div>

      {overviewToast && (
        <div className="banner success" data-testid="routing-group-overview-toast">
          {overviewToast}
        </div>
      )}
      {candidatesToast && (
        <div className="banner success" data-testid="routing-group-candidates-toast">
          {candidatesToast}
        </div>
      )}
      {rulesToast && (
        <div className="banner success" data-testid="routing-group-rules-toast">
          {rulesToast}
        </div>
      )}

      {/* Region 1: overview form. */}
      <div className="panel" data-testid="routing-group-overview">
        <h3 style={{ marginTop: 0 }}>{t('routingGroupDetail.overview')}</h3>
        <div className="form-row">
          <label htmlFor="routing-group-name">{t('routingGroups.fieldName')}</label>
          <input
            id="routing-group-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            data-testid="routing-group-name-input"
          />
          {name && !nameValid && (
            <div className="error" data-testid="routing-group-name-error">
              {t('routingGroups.nameInvalid')}
            </div>
          )}
        </div>
        <div className="form-row">
          <label htmlFor="routing-group-description">
            {t('routingGroups.fieldDescription')}
          </label>
          <input
            id="routing-group-description"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            data-testid="routing-group-description-input"
          />
        </div>
        <div className="form-row">
          <label htmlFor="routing-group-strategy">{t('routingGroups.fieldStrategy')}</label>
          <select
            id="routing-group-strategy"
            value={strategy}
            onChange={(e) => {
              const next = e.target.value;
              if (strategy === 'manual' && next !== 'manual') {
                // Switching away from manual clears the pin (design §5.4).
                setCandidateDrafts(
                  candidateDrafts.map((d) => ({ ...d, pinned: false })),
                );
              }
              setStrategy(next);
            }}
            data-testid="routing-group-strategy-select"
          >
            {STRATEGIES.map((s) => (
              <option key={s} value={s}>
                {t(`routingGroups.strategy.${s}`)}
              </option>
            ))}
          </select>
        </div>
        <p className="muted" data-testid="routing-group-strategy-hint">
          {t(`routingGroupDetail.strategyHint.${strategy}`)}
        </p>
        {strategy !== 'manual' && candidateDrafts.some((d) => d.pinned) && (
          <p className="muted" data-testid="routing-group-strategy-switch-notice">
            {t('routingGroupDetail.strategySwitchNotice')}
          </p>
        )}
        <div className="form-row">
          <label className="inline">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
              data-testid="routing-group-enabled-checkbox"
            />
            {t('routingGroups.filterEnabled')}
          </label>
        </div>
        {overviewError && <ErrorBanner message={overviewError} />}
        <button
          onClick={() => void saveOverview()}
          disabled={!overviewDirty || !overviewValid || savingOverview}
          data-testid="routing-group-save-overview"
        >
          {savingOverview
            ? t('routingGroupDetail.saving')
            : t('routingGroupDetail.saveChanges')}
        </button>
      </div>

      {/* Region 2: ordered candidate editor. */}
      <div className="panel" data-testid="routing-group-candidates-panel">
        <h3 style={{ marginTop: 0 }}>{t('routingGroupDetail.candidates')}</h3>
        {candidateDrafts.length === 0 ? (
          <div className="empty-state" data-testid="routing-group-candidates-empty">
            {t('routingGroupDetail.noCandidates')}
          </div>
        ) : (
          <table className="data" data-testid="routing-group-candidate-list">
            <thead>
              <tr>
                <th>{t('routingGroupDetail.colPosition')}</th>
                <th>{t('routingGroupDetail.colModel')}</th>
                <th>{t('routingGroupDetail.colWeight')}</th>
                <th>{t('routingGroupDetail.colCostTier')}</th>
                <th>{t('routingGroupDetail.colLatencyTier')}</th>
                <th>{t('routingGroupDetail.colPinned')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {candidateDrafts.map((draft, index) => (
                  <tr
                    key={`${draft.modelId}-${index}`}
                    data-testid={`routing-group-candidate-row-${draft.modelId}`}
                  >
                    <td>{index + 1}</td>
                    <td>
                      <a
                        href={`/admin/models/${draft.modelId}`}
                        onClick={(e) => {
                          e.preventDefault();
                          navigate(`/admin/models/${draft.modelId}`);
                        }}
                        className="mono"
                      >
                        {byModelId.get(draft.modelId)?.name || draft.modelId}
                      </a>
                      {byModelId.get(draft.modelId)?.external && (
                        <>
                          {' '}
                          <span className="badge external">External</span>
                        </>
                      )}
                    </td>
                    <td>
                      <input
                        style={{ width: 70 }}
                        value={draft.weight}
                        onChange={(e) => {
                          const next = [...candidateDrafts];
                          next[index] = { ...draft, weight: e.target.value };
                          setCandidateDrafts(next);
                        }}
                        data-testid={`routing-group-candidate-weight-${draft.modelId}`}
                      />
                    </td>
                    <td>
                      <input
                        style={{ width: 70 }}
                        value={draft.costTier}
                        onChange={(e) => {
                          const next = [...candidateDrafts];
                          next[index] = { ...draft, costTier: e.target.value };
                          setCandidateDrafts(next);
                        }}
                        data-testid={`routing-group-candidate-cost-tier-${draft.modelId}`}
                      />
                    </td>
                    <td>
                      <input
                        style={{ width: 70 }}
                        value={draft.latencyTier}
                        onChange={(e) => {
                          const next = [...candidateDrafts];
                          next[index] = { ...draft, latencyTier: e.target.value };
                          setCandidateDrafts(next);
                        }}
                        data-testid={`routing-group-candidate-latency-tier-${draft.modelId}`}
                      />
                    </td>
                    <td>
                      {strategy === 'manual' ? (
                        <label className="inline">
                          <input
                            type="checkbox"
                            checked={draft.pinned}
                            onChange={(e) => {
                              const next = candidateDrafts.map((d, i) =>
                                i === index
                                  ? { ...d, pinned: e.target.checked }
                                  : e.target.checked
                                    ? { ...d, pinned: false }
                                    : d,
                              );
                              setCandidateDrafts(next);
                            }}
                            data-testid={`routing-group-candidate-pinned-${draft.modelId}`}
                          />
                        </label>
                      ) : (
                        '—'
                      )}
                    </td>
                    <td>
                      <button
                        className="secondary"
                        disabled={index === 0}
                        onClick={() => moveCandidate(index, -1)}
                        aria-label={t('routingPolicies.moveUp')}
                        data-testid={`routing-group-candidate-up-${draft.modelId}`}
                      >
                        ↑
                      </button>{' '}
                      <button
                        className="secondary"
                        disabled={index === candidateDrafts.length - 1}
                        onClick={() => moveCandidate(index, 1)}
                        aria-label={t('routingPolicies.moveDown')}
                        data-testid={`routing-group-candidate-down-${draft.modelId}`}
                      >
                        ↓
                      </button>{' '}
                      <button
                        className="secondary"
                        onClick={() =>
                          setCandidateDrafts(
                            candidateDrafts.filter((_, i) => i !== index),
                          )
                        }
                        data-testid={`routing-group-candidate-remove-${draft.modelId}`}
                      >
                        {t('common.remove')}
                      </button>
                    </td>
                  </tr>
              ))}
            </tbody>
          </table>
        )}
        <div style={{ marginTop: 8 }}>
          <select
            value=""
            onChange={(e) => {
              if (!e.target.value) return;
              setCandidateDrafts([
                ...candidateDrafts,
                {
                  modelId: e.target.value,
                  weight: '10',
                  costTier: '',
                  latencyTier: '',
                  pinned: false,
                },
              ]);
            }}
            data-testid="routing-group-add-candidate"
          >
            <option value="">{t('routingGroupDetail.addCandidate')}…</option>
            {catalog
              .filter((m) => !candidateDrafts.some((d) => d.modelId === m.modelId))
              .map((m) => (
                <option key={m.modelId} value={m.modelId}>
                  {m.name}
                  {m.external ? ' (external)' : ''}
                </option>
              ))}
          </select>
        </div>
        <p className="muted">{t('routingGroupDetail.candidatesHint')}</p>
        {candidatesError && <ErrorBanner message={candidatesError} />}
        <button
          onClick={() => void saveCandidates()}
          disabled={
            savingCandidates ||
            candidateDrafts.length === 0 ||
            candidateErrors.length > 0 ||
            !candidatesDirty
          }
          data-testid="routing-group-save-candidates"
        >
          {savingCandidates
            ? t('routingGroupDetail.saving')
            : t('routingGroupDetail.saveCandidates')}
        </button>
      </div>

      {/* Region 3: intent-rule editor. */}
      <div className="panel" data-testid="routing-group-rules-panel">
        <h3 style={{ marginTop: 0 }}>{t('routingGroupDetail.rules')}</h3>
        {ruleDrafts.length === 0 ? (
          <div className="empty-state" data-testid="routing-group-rules-empty">
            {t('routingGroupDetail.noRules')}
          </div>
        ) : (
          <table className="data" data-testid="routing-group-rule-list">
            <thead>
              <tr>
                <th>{t('routingGroupDetail.colPosition')}</th>
                <th>{t('routingGroupDetail.colRuleName')}</th>
                <th>{t('routingGroupDetail.colType')}</th>
                <th>{t('routingGroupDetail.colPattern')}</th>
                <th>{t('routingGroupDetail.colTarget')}</th>
                <th>{t('routingGroupDetail.colEnabled')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {ruleDrafts.map((draft, index) => (
                  <tr
                    key={`${draft.name}-${index}`}
                    data-testid={`routing-group-rule-row-${draft.name}`}
                  >
                    <td>{index + 1}</td>
                    <td>
                      <input
                        style={{ width: 120 }}
                        value={draft.name}
                        onChange={(e) => {
                          const next = [...ruleDrafts];
                          next[index] = { ...draft, name: e.target.value };
                          setRuleDrafts(next);
                        }}
                        data-testid={`routing-group-rule-name-${draft.name}`}
                      />
                    </td>
                    <td>
                      <select
                        value={draft.matchType}
                        onChange={(e) => {
                          const next = [...ruleDrafts];
                          next[index] = { ...draft, matchType: e.target.value };
                          setRuleDrafts(next);
                        }}
                        data-testid={`routing-group-rule-type-${draft.name}`}
                      >
                        <option value="keyword">Keyword</option>
                        <option value="regex">Regex</option>
                      </select>
                    </td>
                    <td>
                      <input
                        style={{ width: 160 }}
                        value={draft.pattern}
                        title={draft.pattern}
                        onChange={(e) => {
                          const next = [...ruleDrafts];
                          next[index] = { ...draft, pattern: e.target.value };
                          setRuleDrafts(next);
                        }}
                        data-testid={`routing-group-rule-pattern-${draft.name}`}
                      />
                    </td>
                    <td>
                      <select
                        value={draft.targetModelId}
                        onChange={(e) => {
                          const next = [...ruleDrafts];
                          next[index] = { ...draft, targetModelId: e.target.value };
                          setRuleDrafts(next);
                        }}
                        data-testid={`routing-group-rule-target-${draft.name}`}
                      >
                        <option value="">—</option>
                        {candidateDrafts.map((d) => (
                          <option key={d.modelId} value={d.modelId}>
                            {byModelId.get(d.modelId)?.name || d.modelId}
                          </option>
                        ))}
                      </select>
                    </td>
                    <td>
                      <label className="inline">
                        <input
                          type="checkbox"
                          checked={draft.enabled}
                          onChange={(e) => {
                            const next = [...ruleDrafts];
                            next[index] = { ...draft, enabled: e.target.checked };
                            setRuleDrafts(next);
                          }}
                          data-testid={`routing-group-rule-enabled-${draft.name}`}
                        />
                      </label>
                    </td>
                    <td>
                      <button
                        className="secondary"
                        disabled={index === 0}
                        onClick={() => moveRule(index, -1)}
                        aria-label={t('routingPolicies.moveUp')}
                        data-testid={`routing-group-rule-up-${draft.name}`}
                      >
                        ↑
                      </button>{' '}
                      <button
                        className="secondary"
                        disabled={index === ruleDrafts.length - 1}
                        onClick={() => moveRule(index, 1)}
                        aria-label={t('routingPolicies.moveDown')}
                        data-testid={`routing-group-rule-down-${draft.name}`}
                      >
                        ↓
                      </button>{' '}
                      <button
                        className="secondary"
                        onClick={() => setRuleDrafts(ruleDrafts.filter((_, i) => i !== index))}
                        data-testid={`routing-group-rule-remove-${draft.name}`}
                      >
                        {t('common.remove')}
                      </button>
                    </td>
                  </tr>
              ))}
            </tbody>
          </table>
        )}
        <div style={{ marginTop: 8 }}>
          <button
            className="secondary"
            onClick={() =>
              setRuleDrafts([
                ...ruleDrafts,
                {
                  name: '',
                  matchType: 'keyword',
                  pattern: '',
                  targetModelId: '',
                  enabled: true,
                },
              ])
            }
            data-testid="routing-group-add-rule"
          >
            {t('routingGroupDetail.addRule')}
          </button>
        </div>
        <p className="muted">{t('routingGroupDetail.rulesHint')}</p>
        {rulesError && <ErrorBanner message={rulesError} />}
        <button
          onClick={() => void saveRules()}
          disabled={savingRules || ruleErrors.length > 0 || !rulesDirty}
          data-testid="routing-group-save-rules"
        >
          {savingRules ? t('routingGroupDetail.saving') : t('routingGroupDetail.saveRules')}
        </button>
      </div>

      {/* Region 4: dry-run test panel. */}
      <div className="panel" data-testid="routing-group-test-panel">
        <h3 style={{ marginTop: 0 }}>{t('routingGroupDetail.testRouting')}</h3>
        <div className="form-row">
          <label htmlFor="routing-group-test-prompt">
            {t('routingGroupDetail.testPromptLabel')}
          </label>
          <textarea
            id="routing-group-test-prompt"
            value={testPrompt}
            onChange={(e) => setTestPrompt(e.target.value)}
            placeholder={t('routingGroupDetail.testPromptPlaceholder')}
            data-testid="routing-group-test-prompt"
          />
        </div>
        <button
          onClick={() => void runTest()}
          disabled={testing || !testPrompt.trim()}
          data-testid="routing-group-test-preview"
        >
          {testing ? t('routingGroupDetail.testing') : t('routingGroupDetail.preview')}
        </button>
        {testError && <ErrorBanner message={testError} />}
        {testResult && (
          <div className="panel" data-testid="routing-group-test-result">
            <div>
              <strong>{t('routingGroupDetail.matchedRule')}:</strong>{' '}
              {testResult.matchedRuleName || t('routingGroupDetail.noMatchedRule')}
            </div>
            <div>
              <strong>{t('routingGroupDetail.attemptList')}:</strong>{' '}
              {(testResult.attempts || [])
                .map((a) => a.modelName || a.modelId)
                .join(' → ') || '—'}
            </div>
            <div>
              <strong>{t('routingGroupDetail.skippedCandidates')}:</strong>{' '}
              {(testResult.skipped || []).length === 0
                ? t('routingGroupDetail.noSkipped')
                : (testResult.skipped || [])
                    .map((s) => `${s.modelName || s.modelId} (${s.reason})`)
                    .join(', ')}
            </div>
            <p className="muted">{t('routingGroupDetail.testNote')}</p>
          </div>
        )}
      </div>

      {deleteOpen && (
        <Dialog
          title={t('routingGroups.deleteTitle')}
          onClose={() => setDeleteOpen(false)}
          testId="routing-group-delete-confirm"
        >
          <p data-testid="routing-group-delete-message">
            {t('routingGroups.deleteBody', { name: group.name })}
          </p>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setDeleteOpen(false)}>
              {t('common.cancel')}
            </button>
            <button
              className="danger"
              onClick={() => void confirmDelete()}
              disabled={deleting}
              data-testid="routing-group-delete-confirm"
            >
              {t('routingGroups.deleteAction')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}
