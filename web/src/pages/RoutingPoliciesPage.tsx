// Admin Routing Policies page (feature #45): configure same-model
// deployment preference and bounded failover per catalog model.
// Implements docs/design/inference-routing-policies.md §5.1.
// Admin surface only: route /admin/routing-policies,
// API /api/v1/admin/routing-policies/*.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime, type PageMeta } from '../api';
import { Dialog, ErrorBanner, Pagination } from '../components';
import { navigate } from '../router';
import { useI18n } from '../i18n';

interface RoutingPolicySummary {
  modelId: string;
  modelName: string;
  modelVersion: string;
  enabled: boolean;
  policyState: string; // DEFAULT / ENABLED / UNAVAILABLE / UNKNOWN
  targetCount: number;
  readyCount: number;
  maxAttempts: number;
  updatedAt: string;
}

interface RoutingPolicy {
  modelId: string;
  modelVersion: string;
  enabled: boolean;
  serviceIds: string[];
  maxAttempts: number;
  retryOn: string[]; // RETRY_CATEGORY_* enum names
  revision: string;
  updatedAt: string;
  publicationState: string;
  eligibleCount: number;
  readyCount: number;
}

interface RoutingTargetHealth {
  serviceId: string;
  serviceName: string;
  accelerator: string;
  acceleratorType: string;
  clusterId: string;
  clusterName: string;
  desiredReplicas: number;
  readyReplicas: number;
  endpointReady: boolean;
  updatedAt: string;
}

interface RoutingPolicyRevision {
  revision: string;
  actor: string;
  updatedAt: string;
  summary: string;
  before?: RoutingPolicy;
  after?: RoutingPolicy;
}

interface ListResponse {
  response: { code: number; message: string };
  policies: RoutingPolicySummary[];
  pageMeta?: PageMeta;
}

interface PolicyResponse {
  response: { code: number; message: string };
  policy: RoutingPolicy;
}

interface HealthResponse {
  response: { code: number; message: string };
  targets: RoutingTargetHealth[];
}

interface RevisionsResponse {
  response: { code: number; message: string };
  revisions: RoutingPolicyRevision[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;
const ACCELERATORS = ['all', 'nvidia', 'iluvatar', 'metax'];
const POLICY_STATES = ['DEFAULT', 'ENABLED', 'UNAVAILABLE'];
const RETRY_CATEGORIES = [
  'RETRY_CATEGORY_CONNECT_TIMEOUT',
  'RETRY_CATEGORY_HTTP_429',
  'RETRY_CATEGORY_HTTP_5XX',
];

function stateBadgeClass(state: string): string {
  switch (state) {
    case 'ENABLED':
      return 'badge running';
    case 'UNAVAILABLE':
      return 'badge terminated';
    case 'UNKNOWN':
      return 'badge deploying';
    default:
      return 'badge pending';
  }
}

// RoutingPolicyDrawer is the configuration drawer of design §5.1:
// policy toggle, ordered targets, retry behavior, impact preview and
// the revision history with before/after diff.
function RoutingPolicyDrawer({
  summary,
  onClose,
  onSaved,
}: {
  summary: RoutingPolicySummary;
  onClose: () => void;
  onSaved: () => void;
}) {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [policy, setPolicy] = useState<RoutingPolicy | null>(null);
  const [targets, setTargets] = useState<RoutingTargetHealth[]>([]);
  const [revisions, setRevisions] = useState<RoutingPolicyRevision[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [enabled, setEnabled] = useState(false);
  const [serviceIds, setServiceIds] = useState<string[]>([]);
  const [maxAttempts, setMaxAttempts] = useState(1);
  const [retryOn, setRetryOn] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [confirmDisable, setConfirmDisable] = useState(false);
  const [selectedRevision, setSelectedRevision] = useState<RoutingPolicyRevision | null>(null);
  const [addOpen, setAddOpen] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError('');
    try {
      const [policyRes, healthRes, revisionsRes] = await Promise.all([
        api.get<PolicyResponse>(
          `/api/v1/admin/routing-policies/${summary.modelId}`,
          orgId,
        ),
        api.get<HealthResponse>(
          `/api/v1/admin/routing-policies/${summary.modelId}/health`,
          orgId,
        ),
        api.get<RevisionsResponse>(
          `/api/v1/admin/routing-policies/${summary.modelId}/revisions?page.limit=20`,
          orgId,
        ),
      ]);
      setPolicy(policyRes.policy);
      setTargets(healthRes.targets || []);
      setRevisions(revisionsRes.revisions || []);
      setEnabled(policyRes.policy.enabled);
      setServiceIds(policyRes.policy.serviceIds || []);
      setMaxAttempts(policyRes.policy.maxAttempts || 1);
      setRetryOn(policyRes.policy.retryOn || []);
    } catch (e) {
      setLoadError(e instanceof Error ? e.message : t('routingPolicies.loadDrawerFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, summary.modelId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const byId = useMemo(() => {
    const map = new Map<string, RoutingTargetHealth>();
    for (const target of targets) map.set(target.serviceId, target);
    return map;
  }, [targets]);

  const eligible = useMemo(
    () => targets.filter((target) => !serviceIds.includes(target.serviceId)),
    [targets, serviceIds],
  );

  const readyCount = useMemo(
    () => serviceIds.filter((id) => (byId.get(id)?.readyReplicas || 0) > 0).length,
    [serviceIds, byId],
  );

  const dirty = useMemo(() => {
    if (!policy) return false;
    return (
      enabled !== policy.enabled ||
      JSON.stringify(serviceIds) !== JSON.stringify(policy.serviceIds || []) ||
      maxAttempts !== policy.maxAttempts ||
      JSON.stringify(retryOn) !== JSON.stringify(policy.retryOn || [])
    );
  }, [policy, enabled, serviceIds, maxAttempts, retryOn]);

  // Validation copy per design §5.1.
  const validation = useMemo((): string => {
    if (enabled && readyCount === 0) return t('routingPolicies.validationNoReady');
    if (maxAttempts > 1 && retryOn.length === 0)
      return t('routingPolicies.validationRetryCategories');
    return '';
  }, [enabled, readyCount, maxAttempts, retryOn, t]);

  const move = (index: number, delta: number) => {
    const next = [...serviceIds];
    const target = index + delta;
    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target], next[index]];
    setServiceIds(next);
  };

  const save = async () => {
    if (!policy) return;
    setSaving(true);
    setSaveError('');
    try {
      await api.put<PolicyResponse>(
        `/api/v1/admin/routing-policies/${summary.modelId}`,
        orgId,
        {
          model_version: summary.modelVersion,
          enabled,
          service_ids: serviceIds,
          max_attempts: maxAttempts,
          retry_on: retryOn,
          expected_revision: policy.revision,
        },
      );
      setConfirmOpen(false);
      onSaved();
      await load();
    } catch (e) {
      const message = e instanceof Error ? e.message : '';
      if (message.includes('10313')) {
        setSaveError(t('routingPolicies.validationConflict'));
      } else {
        setSaveError(t('routingPolicies.saveFailed'));
      }
      setConfirmOpen(false);
    } finally {
      setSaving(false);
    }
  };

  const onSaveClick = () => {
    // Enabling or changing an enabled policy asks for confirmation;
    // disabling asks the disable question (design §5.1).
    if (enabled) setConfirmDisable(false);
    else setConfirmDisable(true);
    setConfirmOpen(true);
  };

  return (
    <div className="dialog-backdrop" data-testid="routing-policy-drawer-backdrop">
      <div className="dialog drawer" data-testid="routing-policy-drawer">
        <div className="page-header">
          <div>
            <h2 data-testid="drawer-model-name">
              {summary.modelName} <span className="muted">{summary.modelVersion}</span>
            </h2>
            <div className="subtitle">
              {t('routingPolicies.drawerRevision', {
                revision: policy?.revision || '0',
              })}
            </div>
          </div>
          <button className="secondary" onClick={onClose} data-testid="drawer-close">
            {t('common.close')}
          </button>
        </div>

        {loading ? (
          <div className="loading" data-testid="drawer-loading">
            {t('common.loading')}
          </div>
        ) : loadError ? (
          <div data-testid="drawer-error">
            <ErrorBanner message={loadError} />
            <button className="secondary" onClick={() => void load()} data-testid="drawer-retry">
              {t('common.retry')}
            </button>
          </div>
        ) : (
          <>
            <section>
              <h3>{t('routingPolicies.sectionPolicy')}</h3>
              <label className="inline">
                <input
                  type="checkbox"
                  checked={enabled}
                  onChange={(e) => setEnabled(e.target.checked)}
                  data-testid="policy-enabled-toggle"
                />
                {t('routingPolicies.enabledLabel')}
              </label>
              <p className="muted">{t('routingPolicies.policyHint')}</p>
            </section>

            <section>
              <h3>{t('routingPolicies.sectionTargets')}</h3>
              {targets.length === 0 ? (
                <p className="muted" data-testid="no-eligible-targets">
                  {t('routingPolicies.noEligibleTargets')}
                </p>
              ) : (
                <>
                  <ol className="target-list" data-testid="target-list">
                    {serviceIds.map((id, index) => {
                      const target = byId.get(id);
                      const unhealthy = (target?.readyReplicas || 0) === 0;
                      return (
                        <li key={id} data-testid={`target-row-${id}`}>
                          <span className={unhealthy ? 'warn' : ''}>
                            {target?.serviceName || id}
                            {target && (
                              <span className="muted">
                                {' '}
                                {target.accelerator}
                                {target.acceleratorType ? `/${target.acceleratorType}` : ''}
                                {target.clusterName ? ` · ${target.clusterName}` : ''} ·{' '}
                                {target.readyReplicas}/{target.desiredReplicas}
                              </span>
                            )}
                          </span>
                          <span className="target-actions">
                            <button
                              className="secondary"
                              disabled={index === 0}
                              onClick={() => move(index, -1)}
                              data-testid={`target-up-${id}`}
                              aria-label={t('routingPolicies.moveUp')}
                            >
                              ↑
                            </button>
                            <button
                              className="secondary"
                              disabled={index === serviceIds.length - 1}
                              onClick={() => move(index, 1)}
                              data-testid={`target-down-${id}`}
                              aria-label={t('routingPolicies.moveDown')}
                            >
                              ↓
                            </button>
                            <button
                              className="secondary"
                              onClick={() =>
                                setServiceIds(serviceIds.filter((sid) => sid !== id))
                              }
                              data-testid={`target-remove-${id}`}
                            >
                              {t('common.remove')}
                            </button>
                          </span>
                        </li>
                      );
                    })}
                  </ol>
                  <div className="target-actions">
                    <button
                      className="secondary"
                      onClick={() => setAddOpen(!addOpen)}
                      disabled={eligible.length === 0}
                      data-testid="add-target"
                    >
                      {t('routingPolicies.addTarget')}
                    </button>
                  </div>
                  {addOpen && (
                    <select
                      value=""
                      onChange={(e) => {
                        if (e.target.value) setServiceIds([...serviceIds, e.target.value]);
                        setAddOpen(false);
                      }}
                      data-testid="add-target-menu"
                    >
                      <option value="">{t('routingPolicies.addTargetPlaceholder')}</option>
                      {eligible.map((target) => (
                        <option key={target.serviceId} value={target.serviceId}>
                          {target.serviceName}
                        </option>
                      ))}
                    </select>
                  )}
                </>
              )}
            </section>

            <section>
              <h3>{t('routingPolicies.sectionRetry')}</h3>
              <label>
                {t('routingPolicies.fieldAttempts')}
                <select
                  value={maxAttempts}
                  onChange={(e) => setMaxAttempts(Number(e.target.value))}
                  data-testid="attempts-select"
                >
                  {[1, 2, 3].map((n) => (
                    <option key={n} value={n}>
                      {n}
                    </option>
                  ))}
                </select>
              </label>
              {RETRY_CATEGORIES.map((category) => (
                <label className="inline" key={category}>
                  <input
                    type="checkbox"
                    checked={retryOn.includes(category)}
                    onChange={(e) => {
                      if (e.target.checked) setRetryOn([...retryOn, category]);
                      else setRetryOn(retryOn.filter((c) => c !== category));
                    }}
                    data-testid={`retry-category-${category
                      .replace('RETRY_CATEGORY_', '')
                      .toLowerCase()}`}
                  />
                  {t(`routingPolicies.retryCategory.${category}`)}
                </label>
              ))}
              <p className="muted">{t('routingPolicies.retryHint')}</p>
            </section>

            <section>
              <h3>{t('routingPolicies.sectionPreview')}</h3>
              <div className="panel" data-testid="impact-preview">
                <div>
                  {t('routingPolicies.previewModel', {
                    model: summary.modelName,
                    version: summary.modelVersion,
                  })}
                </div>
                <div>
                  {t('routingPolicies.previewOrder', {
                    targets:
                      serviceIds
                        .map((id) => byId.get(id)?.serviceName || id)
                        .join(' → ') || t('routingPolicies.previewNoTargets'),
                  })}
                </div>
                <div>
                  {t('routingPolicies.previewAttempts', {
                    attempts: enabled ? maxAttempts : 1,
                  })}
                </div>
                <div className="muted">{t('routingPolicies.previewWarning')}</div>
              </div>
            </section>

            <section>
              <h3>{t('routingPolicies.sectionHistory')}</h3>
              {revisions.length === 0 ? (
                <p className="muted" data-testid="no-revisions">
                  {t('routingPolicies.noRevisions')}
                </p>
              ) : (
                <table className="data" data-testid="revision-table">
                  <thead>
                    <tr>
                      <th>{t('routingPolicies.colRevision')}</th>
                      <th>{t('routingPolicies.colChanged')}</th>
                      <th>{t('routingPolicies.colActor')}</th>
                      <th>{t('routingPolicies.colSummary')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {revisions.map((rev) => (
                      <tr
                        key={rev.revision}
                        onClick={() => setSelectedRevision(rev)}
                        data-testid={`revision-row-${rev.revision}`}
                        style={{ cursor: 'pointer' }}
                      >
                        <td>{rev.revision}</td>
                        <td>{formatTime(rev.updatedAt)}</td>
                        <td>{rev.actor}</td>
                        <td>{rev.summary}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </section>

            {validation && <ErrorBanner message={validation} />}
            {saveError && <ErrorBanner message={saveError} />}
            <div className="dialog-actions">
              <button className="secondary" onClick={onClose} disabled={saving}>
                {t('common.close')}
              </button>
              <button
                onClick={onSaveClick}
                disabled={!dirty || !!validation || saving || targets.length === 0}
                data-testid="save-policy"
              >
                {saving ? t('routingPolicies.saving') : t('routingPolicies.save')}
              </button>
            </div>
          </>
        )}
      </div>

      {confirmOpen && (
        <Dialog
          title={
            confirmDisable
              ? t('routingPolicies.confirmDisableTitle')
              : t('routingPolicies.confirmApplyTitle')
          }
          onClose={() => setConfirmOpen(false)}
          testId="apply-policy-confirm"
        >
          <p data-testid="apply-policy-message">
            {confirmDisable
              ? t('routingPolicies.confirmDisableBody')
              : t('routingPolicies.confirmApplyBody', {
                  model: summary.modelName,
                  version: summary.modelVersion,
                })}
          </p>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setConfirmOpen(false)}>
              {t('common.cancel')}
            </button>
            <button onClick={() => void save()} disabled={saving} data-testid="apply-policy">
              {confirmDisable
                ? t('routingPolicies.confirmDisableAction')
                : t('routingPolicies.confirmApplyAction')}
            </button>
          </div>
        </Dialog>
      )}

      {selectedRevision && (
        <Dialog
          title={t('routingPolicies.revisionDiffTitle', { revision: selectedRevision.revision })}
          onClose={() => setSelectedRevision(null)}
          testId="revision-diff-dialog"
        >
          <div className="panel" data-testid="revision-diff">
            <h4>{t('routingPolicies.diffBefore')}</h4>
            <pre>{JSON.stringify(selectedRevision.before, null, 2)}</pre>
            <h4>{t('routingPolicies.diffAfter')}</h4>
            <pre>{JSON.stringify(selectedRevision.after, null, 2)}</pre>
          </div>
        </Dialog>
      )}
    </div>
  );
}

export default function RoutingPoliciesPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [policies, setPolicies] = useState<RoutingPolicySummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [search, setSearch] = useState('');
  const [stateFilter, setStateFilter] = useState('');
  const [accelerator, setAccelerator] = useState('all');
  const [sort, setSort] = useState('-updated');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<number | null>(null);
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [drawerModel, setDrawerModel] = useState<RoutingPolicySummary | null>(null);

  const load = useCallback(
    async (showLoading: boolean) => {
      if (showLoading) setLoading(true);
      setError('');
      try {
        const params = new URLSearchParams({
          'page.offset': String(offset),
          'page.limit': String(PAGE_SIZE),
          sort,
        });
        if (search) params.set('search', search);
        if (stateFilter) params.set('state', stateFilter);
        if (accelerator !== 'all') params.set('accelerator', accelerator);
        const data = await api.get<ListResponse>(
          `/api/v1/admin/routing-policies?${params.toString()}`,
          orgId,
        );
        setPolicies(data.policies || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
        setLastUpdated(Math.floor(Date.now() / 1000));
        setStale(false);
        setPermissionDenied(false);
      } catch (e) {
        const message = e instanceof Error ? e.message : '';
        if (message.includes('10036') || message.includes('10001')) {
          setPermissionDenied(true);
        } else {
          setError(t('routingPolicies.loadFailed'));
          if (policies.length > 0) setStale(true);
        }
      } finally {
        setLoading(false);
      }
    },
    [api, orgId, offset, search, stateFilter, accelerator, sort, policies.length, t],
  );

  useEffect(() => {
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [offset, search, stateFilter, accelerator, sort]);

  const sortButton = (key: string, label: string) => (
    <button
      className="link"
      onClick={() => setSort(sort === key ? `-${key}` : key)}
      data-testid={`sort-${key}`}
    >
      {label} {sort === key ? '↑' : sort === `-${key}` ? '↓' : ''}
    </button>
  );

  if (permissionDenied) {
    return (
      <div className="page" data-testid="routing-policies-page">
        <div className="page-header">
          <div>
            <h1>{t('routingPolicies.title')}</h1>
          </div>
        </div>
        <div className="empty-state" data-testid="routing-policies-permission-denied">
          <p>{t('routingPolicies.permissionDenied')}</p>
          <a
            href="/admin"
            onClick={(e) => {
              e.preventDefault();
              navigate('/admin');
            }}
          >
            {t('routingPolicies.backToAdmin')}
          </a>
        </div>
      </div>
    );
  }

  return (
    <div className="page" data-testid="routing-policies-page">
      <div className="page-header">
        <div>
          <h1>{t('routingPolicies.title')}</h1>
          <div className="subtitle">
            {t('routingPolicies.subtitle')}
            {lastUpdated && !loading && (
              <>
                {' · '}
                <span data-testid="routing-policies-last-updated">
                  {t('routingPolicies.updated', { time: formatTime(lastUpdated) })}
                </span>
              </>
            )}
          </div>
        </div>
        <div className="actions">
          <button
            className="secondary"
            onClick={() => void load(false)}
            disabled={loading}
            data-testid="routing-policies-refresh"
          >
            {t('common.refresh')}
          </button>
        </div>
      </div>

      {stale && (
        <div className="banner stale" data-testid="routing-policies-stale">
          {t('common.staleData')}
        </div>
      )}
      {error && (
        <div data-testid="routing-policies-error">
          <ErrorBanner message={error} />
          <button
            className="secondary"
            onClick={() => void load(false)}
            data-testid="routing-policies-retry"
          >
            {t('common.retry')}
          </button>
        </div>
      )}

      <div className="panel" data-testid="routing-policies-list">
        <div className="filters">
          <input
            placeholder={t('routingPolicies.searchPlaceholder')}
            value={search}
            onChange={(e) => {
              setOffset(0);
              setSearch(e.target.value);
            }}
            disabled={loading}
            data-testid="routing-policy-search"
          />
          <select
            value={stateFilter}
            onChange={(e) => {
              setOffset(0);
              setStateFilter(e.target.value);
            }}
            disabled={loading}
            data-testid="routing-policy-state-filter"
          >
            <option value="">{t('routingPolicies.filterAllStates')}</option>
            {POLICY_STATES.map((s) => (
              <option key={s} value={s}>
                {t(`routingPolicies.state.${s}`)}
              </option>
            ))}
          </select>
          <select
            value={accelerator}
            onChange={(e) => {
              setOffset(0);
              setAccelerator(e.target.value);
            }}
            disabled={loading}
            data-testid="routing-policy-accelerator-filter"
          >
            {ACCELERATORS.map((a) => (
              <option key={a} value={a}>
                {a === 'all' ? t('routingPolicies.filterAllAccelerators') : a}
              </option>
            ))}
          </select>
        </div>

        {loading ? (
          <div className="loading" data-testid="routing-policies-loading">
            {t('common.loading')}
          </div>
        ) : policies.length === 0 ? (
          search || stateFilter || accelerator !== 'all' ? (
            <div className="empty-state" data-testid="routing-policies-empty-filter">
              {t('routingPolicies.emptyFilter')}
            </div>
          ) : (
            <div className="empty-state" data-testid="routing-policies-empty-catalog">
              {t('routingPolicies.emptyCatalog')}{' '}
              <a
                href="/admin/models"
                onClick={(e) => {
                  e.preventDefault();
                  navigate('/admin/models');
                }}
              >
                {t('routingPolicies.goToModels')}
              </a>
            </div>
          )
        ) : (
          <>
            <table className="data" data-testid="routing-policies-table">
              <thead>
                <tr>
                  <th>{sortButton('model', t('routingPolicies.colModel'))}</th>
                  <th>{t('routingPolicies.colVersion')}</th>
                  <th>{t('routingPolicies.colPolicy')}</th>
                  <th>{sortButton('ready', t('routingPolicies.colReadyTargets'))}</th>
                  <th>{t('routingPolicies.colAttempts')}</th>
                  <th>{sortButton('updated', t('routingPolicies.colUpdated'))}</th>
                  <th>{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {policies.map((p) => (
                  <tr key={p.modelId} data-testid={`routing-policy-row-${p.modelId}`}>
                    <td>{p.modelName}</td>
                    <td>{p.modelVersion}</td>
                    <td>
                      <span className={stateBadgeClass(p.policyState)}>
                        {t(`routingPolicies.state.${p.policyState}`)}
                      </span>
                    </td>
                    <td data-testid={`ready-count-${p.modelId}`}>
                      {p.readyCount}/{p.targetCount}
                    </td>
                    <td>{p.maxAttempts}</td>
                    <td>{p.updatedAt ? formatTime(p.updatedAt) : '—'}</td>
                    <td>
                      <button
                        className="secondary"
                        onClick={() => setDrawerModel(p)}
                        data-testid={`configure-policy-${p.modelId}`}
                      >
                        {t('routingPolicies.configure')}
                      </button>
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

      {drawerModel && (
        <RoutingPolicyDrawer
          summary={drawerModel}
          onClose={() => setDrawerModel(null)}
          onSaved={() => void load(false)}
        />
      )}
    </div>
  );
}
