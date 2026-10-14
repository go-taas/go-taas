// Admin Routing groups page (feature #49): the virtual-name registry.
// Implements docs/design/intelligent-model-routing.md §5.3.
// Admin surface only: route /admin/routing-groups,
// API /api/v1/admin/routing-groups/*.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime, type ListRoutingGroupsResponse, type RoutingGroup } from '../api';
import { Dialog, ErrorBanner, Pagination } from '../components';
import { navigate } from '../router';
import { useI18n } from '../i18n';

const PAGE_SIZE = 20;
const STRATEGIES = ['manual', 'weighted', 'cost_priority', 'latency_priority'];
const NAME_PATTERN = /^[a-z][a-z0-9-]{2,63}$/;

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

export default function RoutingGroupsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [groups, setGroups] = useState<RoutingGroup[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [search, setSearch] = useState('');
  const [strategyFilter, setStrategyFilter] = useState('');
  const [enabledFilter, setEnabledFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<RoutingGroup | null>(null);
  const [disableTarget, setDisableTarget] = useState<RoutingGroup | null>(null);
  const [toggling, setToggling] = useState('');
  const [toggleError, setToggleError] = useState('');
  const [toast, setToast] = useState('');

  const load = useCallback(
    async (showLoading: boolean) => {
      if (showLoading) setLoading(true);
      setError('');
      try {
        const params = new URLSearchParams({
          'page.offset': String(offset),
          'page.limit': String(PAGE_SIZE),
        });
        if (search) params.set('name', search);
        if (strategyFilter) params.set('strategy', strategyFilter);
        if (enabledFilter) params.set('enabled', enabledFilter);
        const data = await api.get<ListRoutingGroupsResponse>(
          `/api/v1/admin/routing-groups?${params.toString()}`,
          orgId,
        );
        setGroups(data.groups || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
        setStale(false);
        setPermissionDenied(false);
      } catch (e) {
        const message = e instanceof Error ? e.message : '';
        if (message.includes('10036') || message.includes('10001')) {
          setPermissionDenied(true);
        } else {
          setError(t('routingGroups.loadFailed'));
          if (groups.length > 0) setStale(true);
        }
      } finally {
        setLoading(false);
      }
    },
    [api, orgId, offset, search, strategyFilter, enabledFilter, groups.length, t],
  );

  useEffect(() => {
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [offset, search, strategyFilter, enabledFilter]);

  // Toast auto-dismiss.
  useEffect(() => {
    if (!toast) return;
    const timer = setTimeout(() => setToast(''), 3000);
    return () => clearTimeout(timer);
  }, [toast]);

  const setEnabled = async (group: RoutingGroup, enabled: boolean) => {
    setToggling(group.groupId);
    setToggleError('');
    try {
      await api.patch(
        `/api/v1/admin/routing-groups/${group.groupId}`,
        orgId,
        { group_id: group.groupId, enabled },
      );
      await load(false);
    } catch (e) {
      const message = e instanceof Error ? e.message : '';
      // 13107: enable validation failed (no candidates / manual without pin).
      setToggleError(
        message.includes('13107') ? message : t('routingGroups.enableFailed'),
      );
    } finally {
      setToggling('');
      setDisableTarget(null);
    }
  };

  const confirmDelete = async () => {
    if (!deleteTarget) return;
    setToggling(deleteTarget.groupId);
    try {
      await api.del(`/api/v1/admin/routing-groups/${deleteTarget.groupId}`, orgId);
      setToast(t('routingGroups.deleted'));
      setDeleteTarget(null);
      await load(false);
    } catch (e) {
      setToggleError(e instanceof Error ? e.message : t('routingGroups.loadFailed'));
      setDeleteTarget(null);
    } finally {
      setToggling('');
    }
  };

  if (permissionDenied) {
    return (
      <div className="page" data-testid="routing-groups-page">
        <div className="page-header">
          <div>
            <h1>{t('routingGroups.title')}</h1>
          </div>
        </div>
        <div className="empty-state" data-testid="routing-groups-permission-denied">
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

  return (
    <div className="page" data-testid="routing-groups-page">
      <div className="page-header">
        <div>
          <h1>{t('routingGroups.title')}</h1>
          <div className="subtitle">{t('routingGroups.subtitle')}</div>
        </div>
        <div className="actions">
          <button
            className="secondary"
            onClick={() => navigate('/admin/routing-groups/decisions')}
            disabled={loading}
            data-testid="routing-groups-view-decisions"
          >
            {t('routingGroups.viewDecisions')}
          </button>
          <button
            className="secondary"
            onClick={() => void load(false)}
            disabled={loading}
            data-testid="routing-groups-refresh"
          >
            {t('common.refresh')}
          </button>
          <button
            onClick={() => setCreateOpen(true)}
            disabled={loading}
            data-testid="routing-groups-add"
          >
            {t('routingGroups.create')}
          </button>
        </div>
      </div>

      {toast && (
        <div className="banner success" data-testid="routing-groups-toast">
          {toast}
        </div>
      )}
      {stale && (
        <div className="banner stale" data-testid="routing-groups-stale">
          {t('common.staleData')}
        </div>
      )}
      {error && (
        <div data-testid="routing-groups-error">
          <ErrorBanner message={error} />
          <button
            className="secondary"
            onClick={() => void load(false)}
            data-testid="routing-groups-retry"
          >
            {t('common.retry')}
          </button>
        </div>
      )}
      {toggleError && (
        <div data-testid="routing-groups-toggle-error">
          <ErrorBanner message={toggleError} />
        </div>
      )}

      <div className="panel" data-testid="routing-groups-list">
        <div className="filters">
          <input
            placeholder={t('routingGroups.searchPlaceholder')}
            value={search}
            onChange={(e) => {
              setOffset(0);
              setSearch(e.target.value);
            }}
            disabled={loading}
            data-testid="routing-groups-search"
          />
          <select
            value={strategyFilter}
            onChange={(e) => {
              setOffset(0);
              setStrategyFilter(e.target.value);
            }}
            disabled={loading}
            data-testid="routing-groups-strategy-filter"
          >
            <option value="">{t('routingGroups.filterAllStrategies')}</option>
            {STRATEGIES.map((s) => (
              <option key={s} value={s}>
                {t(`routingGroups.strategy.${s}`)}
              </option>
            ))}
          </select>
          <select
            value={enabledFilter}
            onChange={(e) => {
              setOffset(0);
              setEnabledFilter(e.target.value);
            }}
            disabled={loading}
            data-testid="routing-groups-enabled-filter"
          >
            <option value="">{t('routingGroups.filterAllStates')}</option>
            <option value="true">{t('routingGroups.filterEnabled')}</option>
            <option value="false">{t('routingGroups.filterDisabled')}</option>
          </select>
        </div>

        {loading ? (
          <div className="loading" data-testid="routing-groups-loading">
            {t('common.loading')}
          </div>
        ) : groups.length === 0 ? (
          search || strategyFilter || enabledFilter ? (
            <div className="empty-state" data-testid="routing-groups-empty-filter">
              {t('routingGroups.emptyFilter')}
            </div>
          ) : (
            <div className="empty-state" data-testid="routing-groups-empty">
              {t('routingGroups.empty')}
            </div>
          )
        ) : (
          <>
            <table className="data" data-testid="routing-groups-table">
              <thead>
                <tr>
                  <th>{t('routingGroups.colName')}</th>
                  <th>{t('routingGroups.colStrategy')}</th>
                  <th>{t('routingGroups.colCandidates')}</th>
                  <th>{t('routingGroups.colRules')}</th>
                  <th>{t('routingGroups.colRequests24h')}</th>
                  <th>{t('routingGroups.colEnabled')}</th>
                  <th>{t('routingGroups.colUpdated')}</th>
                  <th>{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {groups.map((g) => (
                  <tr key={g.groupId} data-testid={`routing-group-row-${g.groupId}`}>
                    <td>
                      <span className="mono">{g.name}</span>{' '}
                      <span className="badge">{t('umodels.auto')}</span>
                    </td>
                    <td>
                      <span className={strategyBadgeClass(g.strategy)}>
                        {t(`routingGroups.strategy.${g.strategy}`)}
                      </span>
                    </td>
                    <td>{g.candidateCount}</td>
                    <td>{g.ruleCount}</td>
                    <td>{g.requestCount24h}</td>
                    <td>
                      <label className="inline">
                        <input
                          type="checkbox"
                          checked={g.enabled}
                          disabled={loading || toggling === g.groupId}
                          onChange={(e) => {
                            if (e.target.checked) void setEnabled(g, true);
                            else setDisableTarget(g);
                          }}
                          data-testid={`routing-group-enabled-toggle-${g.groupId}`}
                        />
                      </label>
                    </td>
                    <td>{g.updatedAt ? formatTime(String(g.updatedAt)) : '—'}</td>
                    <td>
                      <button
                        className="secondary"
                        onClick={() => navigate(`/admin/routing-groups/${g.groupId}`)}
                        data-testid={`routing-group-view-${g.groupId}`}
                      >
                        {t('routingGroups.view')}
                      </button>{' '}
                      <button
                        className="secondary"
                        onClick={() => setDeleteTarget(g)}
                        disabled={toggling === g.groupId}
                        title={t('routingGroups.deleteBlockedTooltip')}
                        data-testid={`routing-group-delete-${g.groupId}`}
                      >
                        {t('routingGroups.delete')}
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

      {createOpen && (
        <CreateGroupDialog
          onClose={() => setCreateOpen(false)}
          onCreated={(groupId) => {
            setCreateOpen(false);
            setToast(t('routingGroups.created'));
            navigate(`/admin/routing-groups/${groupId}`);
          }}
        />
      )}

      {deleteTarget && (
        <Dialog
          title={t('routingGroups.deleteTitle')}
          onClose={() => setDeleteTarget(null)}
          testId="routing-group-delete-confirm"
        >
          <p data-testid="routing-group-delete-message">
            {t('routingGroups.deleteBody', { name: deleteTarget.name })}
          </p>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setDeleteTarget(null)}>
              {t('common.cancel')}
            </button>
            <button
              className="danger"
              onClick={() => void confirmDelete()}
              disabled={toggling === deleteTarget.groupId}
              data-testid="routing-group-delete-confirm"
            >
              {t('routingGroups.deleteAction')}
            </button>
          </div>
        </Dialog>
      )}

      {disableTarget && (
        <Dialog
          title={t('routingGroups.disableTitle')}
          onClose={() => setDisableTarget(null)}
          testId="routing-group-disable-confirm"
        >
          <p data-testid="routing-group-disable-message">
            {t('routingGroups.disableBody', { name: disableTarget.name })}
          </p>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setDisableTarget(null)}>
              {t('common.cancel')}
            </button>
            <button
              className="danger"
              onClick={() => void setEnabled(disableTarget, false)}
              disabled={toggling === disableTarget.groupId}
              data-testid="routing-group-disable-confirm"
            >
              {t('routingGroups.disableAction')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

// CreateGroupDialog is the create dialog of design §5.3: name,
// description, strategy; the group is created disabled.
function CreateGroupDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (groupId: string) => void;
}) {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [strategy, setStrategy] = useState('weighted');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const nameValid = NAME_PATTERN.test(name);
  const descriptionValid = description.length <= 256;
  const valid = nameValid && descriptionValid && STRATEGIES.includes(strategy);

  const create = async () => {
    setSaving(true);
    setError('');
    try {
      const data = await api.post<{ group?: { groupId?: string } }>(
        '/api/v1/admin/routing-groups',
        orgId,
        { name, description, strategy },
      );
      onCreated(data.group?.groupId || '');
    } catch (e) {
      const message = e instanceof Error ? e.message : '';
      if (message.includes('13102')) setError(t('routingGroups.nameConflict'));
      else if (message.includes('13113')) setError(t('routingGroups.nameReserved'));
      else if (message.includes('13103')) setError(t('routingGroups.nameInvalid'));
      else if (message.includes('13107')) setError(t('routingGroups.strategyInvalid'));
      else setError(message || t('routingGroups.saveFailed'));
      setSaving(false);
    }
  };

  return (
    <Dialog
      title={t('routingGroups.createTitle')}
      onClose={onClose}
      testId="routing-group-create-dialog"
    >
      <div className="form-row">
        <label htmlFor="routing-group-create-name">{t('routingGroups.fieldName')}</label>
        <input
          id="routing-group-create-name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t('routingGroups.namePlaceholder')}
          data-testid="routing-group-create-name"
        />
        {name && !nameValid && (
          <div className="error" data-testid="routing-group-create-name-error">
            {t('routingGroups.nameInvalid')}
          </div>
        )}
      </div>
      <div className="form-row">
        <label htmlFor="routing-group-create-description">
          {t('routingGroups.fieldDescription')}
        </label>
        <input
          id="routing-group-create-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          data-testid="routing-group-create-description"
        />
        {description && !descriptionValid && (
          <div className="error" data-testid="routing-group-create-description-error">
            {t('routingGroups.descriptionTooLong')}
          </div>
        )}
      </div>
      <div className="form-row">
        <label htmlFor="routing-group-create-strategy">
          {t('routingGroups.fieldStrategy')}
        </label>
        <select
          id="routing-group-create-strategy"
          value={strategy}
          onChange={(e) => setStrategy(e.target.value)}
          data-testid="routing-group-create-strategy"
        >
          {STRATEGIES.map((s) => (
            <option key={s} value={s}>
              {t(`routingGroups.strategy.${s}`)}
            </option>
          ))}
        </select>
      </div>
      <div className="banner" data-testid="routing-group-create-callout">
        {t('routingGroups.createCallout')}
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose} disabled={saving}>
          {t('common.cancel')}
        </button>
        <button
          onClick={() => void create()}
          disabled={!valid || saving}
          data-testid="routing-group-create-submit"
        >
          {saving ? t('routingGroups.creating') : t('routingGroups.create')}
        </button>
      </div>
    </Dialog>
  );
}
