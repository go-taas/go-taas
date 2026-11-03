// End-user Notifications page (feature #26): the in-console notification
// center for tenant account events, with read/unread state, per-user
// preferences and threshold alerts. Route /notifications, API
// /api/v1/notifications/*.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { navigate } from '../../router';
import { formatTime, type PageMeta } from '../../api';
import { Dialog, ErrorBanner, Pagination, RowActionsMenu } from '../../components';
import { notifyUnreadChanged } from '../../notificationsBus';
import type { Notification, NotificationPreference, NotificationThreshold } from '../AdminNotificationsPage';

interface ListResponse {
  response: { code: number; message: string };
  notifications: Notification[];
  pageMeta?: PageMeta;
}

interface PrefsResponse {
  response: { code: number; message: string };
  preferences: NotificationPreference[];
}

interface ThresholdsResponse {
  response: { code: number; message: string };
  thresholds: NotificationThreshold[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

// The end-user event catalog (feature #26, §3.4).
const USER_EVENTS: { type: string; descKey: string }[] = [
  { type: 'billing.invoice_created', descKey: 'notifications.eventInvoiceCreated' },
  { type: 'billing.invoice_paid', descKey: 'notifications.eventInvoicePaid' },
  { type: 'billing.spend_limit_breached', descKey: 'notifications.eventSpendLimit' },
  { type: 'billing.balance_low', descKey: 'notifications.eventBalanceLow' },
];

// The end-user threshold metrics (feature #26, AD5).
const USER_METRICS: { metric: string; labelKey: string }[] = [
  { metric: 'balance_low', labelKey: 'notifications.metricBalanceLow' },
  { metric: 'spend_limit', labelKey: 'notifications.metricSpendLimit' },
];

export default function UserNotificationsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [tab, setTab] = useState<'inbox' | 'preferences' | 'thresholds'>('inbox');
  const [notifications, setNotifications] = useState<Notification[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [statusFilter, setStatusFilter] = useState('all');
  const [eventFilter, setEventFilter] = useState('');
  const [prefs, setPrefs] = useState<NotificationPreference[]>([]);
  const [thresholds, setThresholds] = useState<NotificationThreshold[]>([]);
  const [thTotal, setThTotal] = useState(0);
  const [thOffset, setThOffset] = useState(0);
  const [thLoading, setThLoading] = useState(false);
  const [busyId, setBusyId] = useState('');
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<NotificationThreshold | null>(null);
  const [dialogName, setDialogName] = useState('');
  const [dialogMetric, setDialogMetric] = useState(USER_METRICS[0].metric);
  const [dialogOperator, setDialogOperator] = useState('THRESHOLD_OPERATOR_GT');
  const [dialogValue, setDialogValue] = useState('');
  const [dialogEnabled, setDialogEnabled] = useState(true);
  const [dialogError, setDialogError] = useState('');

  const loadInbox = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams();
      params.set('page.offset', String(offset));
      params.set('page.limit', String(PAGE_SIZE));
      if (statusFilter !== 'all') {
        params.set('readFilter', 'true');
        params.set('read', statusFilter === 'read' ? 'true' : 'false');
      }
      if (eventFilter) params.set('eventType', eventFilter);
      const data = await api.get<ListResponse>(
        `/api/v1/notifications?${params.toString()}`,
        orgId,
      );
      setNotifications(data.notifications || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('notifications.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, offset, statusFilter, eventFilter, t]);

  const loadPrefs = useCallback(async () => {
    try {
      const data = await api.get<PrefsResponse>('/api/v1/notifications/preferences', orgId);
      setPrefs(data.preferences || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('notifications.loadFailed'));
    }
  }, [api, orgId, t]);

  const loadThresholds = useCallback(async () => {
    setThLoading(true);
    try {
      const params = new URLSearchParams();
      params.set('page.offset', String(thOffset));
      params.set('page.limit', String(PAGE_SIZE));
      const data = await api.get<ThresholdsResponse>(
        `/api/v1/notifications/thresholds?${params.toString()}`,
        orgId,
      );
      setThresholds(data.thresholds || []);
      setThTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('notifications.loadFailed'));
    } finally {
      setThLoading(false);
    }
  }, [api, orgId, thOffset, t]);

  useEffect(() => {
    if (tab === 'inbox') void loadInbox();
    if (tab === 'preferences') void loadPrefs();
    if (tab === 'thresholds') void loadThresholds();
  }, [tab, loadInbox, loadPrefs, loadThresholds]);

  const markRead = async (n: Notification) => {
    setBusyId(n.notificationId);
    try {
      await api.post(`/api/v1/notifications/${n.notificationId}:mark-read`, orgId, {});
      notifyUnreadChanged();
      if (n.link) navigate(n.link);
      void loadInbox();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('notifications.updateFailed'));
    } finally {
      setBusyId('');
    }
  };

  const markAllRead = async () => {
    setBusyId('all');
    try {
      await api.post('/api/v1/notifications:mark-all-read', orgId, {});
      notifyUnreadChanged();
      void loadInbox();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('notifications.updateFailed'));
    } finally {
      setBusyId('');
    }
  };

  const remove = async (n: Notification) => {
    if (!window.confirm(t('notifications.deleteConfirm'))) return;
    setBusyId(n.notificationId);
    try {
      await api.del(`/api/v1/notifications/${n.notificationId}`, orgId);
      void loadInbox();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('notifications.deleteFailed'));
    } finally {
      setBusyId('');
    }
  };

  const savePrefs = async () => {
    setBusyId('prefs');
    try {
      await api.put('/api/v1/notifications/preferences', orgId, {
        preferences: prefs,
      });
    } catch (e) {
      setError(e instanceof Error ? e.message : t('notifications.updateFailed'));
    } finally {
      setBusyId('');
    }
  };

  const togglePref = (eventType: string) => {
    setPrefs((prev) =>
      prev.map((p) => (p.eventType === eventType ? { ...p, enabled: !p.enabled } : p)),
    );
  };

  const openNew = () => {
    setEditing(null);
    setDialogName('');
    setDialogMetric(USER_METRICS[0].metric);
    setDialogOperator('THRESHOLD_OPERATOR_GT');
    setDialogValue('');
    setDialogEnabled(true);
    setDialogError('');
    setDialogOpen(true);
  };

  const openEdit = (th: NotificationThreshold) => {
    setEditing(th);
    setDialogName(th.name);
    setDialogMetric(th.metric);
    setDialogOperator(th.operator === 'lt' ? 'THRESHOLD_OPERATOR_LT' : 'THRESHOLD_OPERATOR_GT');
    setDialogValue(String(th.value));
    setDialogEnabled(th.enabled);
    setDialogError('');
    setDialogOpen(true);
  };

  const saveThreshold = async () => {
    setBusyId('threshold');
    setDialogError('');
    try {
      const body = {
        name: dialogName,
        metric: dialogMetric,
        operator: dialogOperator,
        value: parseFloat(dialogValue),
        enabled: dialogEnabled,
      };
      if (editing) {
        await api.put(`/api/v1/notifications/thresholds/${editing.thresholdId}`, orgId, body);
      } else {
        await api.post('/api/v1/notifications/thresholds', orgId, body);
      }
      setDialogOpen(false);
      void loadThresholds();
    } catch (e) {
      const code = e && (e as { code?: number }).code;
      setDialogError(code === 11004 ? t('notifications.thresholdInvalid') : t('notifications.updateFailed'));
    } finally {
      setBusyId('');
    }
  };

  const toggleThreshold = async (th: NotificationThreshold) => {
    setBusyId(th.thresholdId);
    try {
      await api.put(`/api/v1/notifications/thresholds/${th.thresholdId}`, orgId, {
        enabled: !th.enabled,
      });
      void loadThresholds();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('notifications.updateFailed'));
    } finally {
      setBusyId('');
    }
  };

  const deleteThreshold = async (th: NotificationThreshold) => {
    if (!window.confirm(t('notifications.deleteThresholdConfirm', { name: th.name }))) return;
    setBusyId(th.thresholdId);
    try {
      await api.del(`/api/v1/notifications/thresholds/${th.thresholdId}`, orgId);
      void loadThresholds();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('notifications.deleteFailed'));
    } finally {
      setBusyId('');
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('notifications.title')}</h1>
          <div className="subtitle">{t('notifications.userSubtitle')}</div>
        </div>
        <div className="header-actions">
          <button
            className="secondary"
            data-testid="notifications-refresh"
            onClick={() => void loadInbox()}
          >
            {t('common.refresh')}
          </button>
          <button data-testid="notifications-mark-all-read" onClick={() => void markAllRead()}>
            {t('notifications.markAllRead')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="tabs" data-testid="notifications-tabs">
        <button
          className={tab === 'inbox' ? 'tab active' : 'tab'}
          data-testid="notifications-tab-inbox"
          onClick={() => setTab('inbox')}
        >
          {t('notifications.tabInbox')}
        </button>
        <button
          className={tab === 'preferences' ? 'tab active' : 'tab'}
          data-testid="notifications-tab-preferences"
          onClick={() => setTab('preferences')}
        >
          {t('notifications.tabPreferences')}
        </button>
        <button
          className={tab === 'thresholds' ? 'tab active' : 'tab'}
          data-testid="notifications-tab-thresholds"
          onClick={() => setTab('thresholds')}
        >
          {t('notifications.tabThresholds')}
        </button>
      </div>

      {tab === 'inbox' && (
        <div className="panel" data-testid="notification-table">
          <div className="filter-bar">
            <select
              data-testid="notification-filter-status"
              value={statusFilter}
              onChange={(e) => {
                setStatusFilter(e.target.value);
                setOffset(0);
              }}
            >
              <option value="all">{t('common.allStatuses')}</option>
              <option value="unread">{t('notifications.unread')}</option>
              <option value="read">{t('notifications.read')}</option>
            </select>
            <select
              data-testid="notification-filter-event"
              value={eventFilter}
              onChange={(e) => {
                setEventFilter(e.target.value);
                setOffset(0);
              }}
            >
              <option value="">{t('common.allEvents')}</option>
              {USER_EVENTS.map((ev) => (
                <option key={ev.type} value={ev.type}>
                  {ev.type}
                </option>
              ))}
            </select>
          </div>
          {loading ? (
            <div className="loading">{t('common.loading')}</div>
          ) : notifications.length === 0 ? (
            <div className="empty-state" data-testid="notification-empty">
              {t('notifications.empty')}
            </div>
          ) : (
            <ul className="notification-list">
              {notifications.map((n) => (
                <li
                  key={n.notificationId}
                  className={n.read ? 'notification-row' : 'notification-row unread'}
                  data-testid={`notification-row-${n.notificationId}`}
                >
                  <span className={`badge ${n.severity}`}>{n.severity}</span>
                  {!n.read && <span className="unread-dot" data-testid={`notification-unread-${n.notificationId}`} />}
                  <div className="notification-content">
                    <div className="notification-title">{n.title}</div>
                    <div className="notification-body">{n.body}</div>
                    <div className="notification-meta">
                      <code>{n.eventType}</code>
                      <span>{formatTime(n.createdAt)}</span>
                    </div>
                  </div>
                  <RowActionsMenu
                    label={t('common.actionsFor', { name: n.title || n.notificationId })}
                    testId={`notification-actions-${n.notificationId}`}
                    actions={[
                      ...(n.read
                        ? []
                        : [
                            {
                              key: `notification-mark-read-${n.notificationId}`,
                              label: t('notifications.markRead'),
                              disabled: busyId === n.notificationId,
                              onClick: () => void markRead(n),
                            },
                          ]),
                      {
                        key: `notification-delete-${n.notificationId}`,
                        label: t('common.delete'),
                        danger: true,
                        disabled: busyId === n.notificationId,
                        onClick: () => void remove(n),
                      },
                    ]}
                  />
                </li>
              ))}
            </ul>
          )}
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        </div>
      )}

      {tab === 'preferences' && (
        <div className="panel" data-testid="notification-preferences">
          <ul className="preference-list">
            {USER_EVENTS.map((ev) => {
              const pref = prefs.find((p) => p.eventType === ev.type);
              const enabled = pref ? pref.enabled : true;
              return (
                <li key={ev.type} className="preference-row">
                  <div>
                    <code>{ev.type}</code>
                    <span className="preference-desc">{t(ev.descKey)}</span>
                  </div>
                  <label className="switch">
                    <input
                      type="checkbox"
                      data-testid={`preference-toggle-${ev.type}`}
                      checked={enabled}
                      onChange={() => togglePref(ev.type)}
                    />
                    <span>{enabled ? t('common.on') : t('common.off')}</span>
                  </label>
                </li>
              );
            })}
          </ul>
          <button
            data-testid="preferences-save"
            disabled={busyId === 'prefs'}
            onClick={() => void savePrefs()}
          >
            {busyId === 'prefs' ? t('common.saving') : t('common.save')}
          </button>
        </div>
      )}

      {tab === 'thresholds' && (
        <div className="panel" data-testid="threshold-table">
          <div className="header-actions">
            <button data-testid="threshold-new" onClick={openNew}>
              {t('notifications.newThreshold')}
            </button>
          </div>
          {thLoading ? (
            <div className="loading">{t('common.loading')}</div>
          ) : thresholds.length === 0 ? (
            <div className="empty-state" data-testid="threshold-empty">
              {t('notifications.thresholdEmpty')}
            </div>
          ) : (
            <table>
              <thead>
                <tr>
                  <th>{t('common.name')}</th>
                  <th>{t('notifications.colMetric')}</th>
                  <th>{t('notifications.colCondition')}</th>
                  <th>{t('common.status')}</th>
                  <th>{t('common.updated')}</th>
                  <th>{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {thresholds.map((th) => (
                  <tr key={th.thresholdId} data-testid={`threshold-row-${th.thresholdId}`}>
                    <td>{th.name}</td>
                    <td>{th.metric}</td>
                    <td>
                      {th.operator === 'lt' ? '<' : '>'} {th.value}
                    </td>
                    <td>
                      <span className={`badge ${th.enabled ? 'enabled' : 'disabled'}`}>
                        {th.enabled ? t('common.enabled') : t('common.disabled')}
                      </span>
                    </td>
                    <td>{formatTime(th.updatedAt)}</td>
                    <td className="col-actions">
                      <RowActionsMenu
                        label={t('common.actionsFor', { name: th.metric || th.thresholdId })}
                        testId={`threshold-actions-${th.thresholdId}`}
                        actions={[
                          {
                            key: `threshold-edit-${th.thresholdId}`,
                            label: t('common.edit'),
                            onClick: () => openEdit(th),
                          },
                          {
                            key: `threshold-toggle-${th.thresholdId}`,
                            label: th.enabled ? t('common.disable') : t('common.enable'),
                            disabled: busyId === th.thresholdId,
                            onClick: () => void toggleThreshold(th),
                          },
                          {
                            key: `threshold-delete-${th.thresholdId}`,
                            label: t('common.delete'),
                            danger: true,
                            disabled: busyId === th.thresholdId,
                            onClick: () => void deleteThreshold(th),
                          },
                        ]}
                      />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <Pagination offset={thOffset} limit={PAGE_SIZE} total={thTotal} onPageChange={setThOffset} />
        </div>
      )}

      {dialogOpen && (
        <Dialog
          title={editing ? t('notifications.editThreshold') : t('notifications.newThreshold')}
          onClose={() => setDialogOpen(false)}
          testId="threshold-dialog"
        >
          <div className="form">
            <label>
              {t('common.name')}
              <input
                data-testid="threshold-dialog-name"
                value={dialogName}
                onChange={(e) => setDialogName(e.target.value)}
              />
            </label>
            <label>
              {t('notifications.colMetric')}
              <select
                data-testid="threshold-dialog-metric"
                value={dialogMetric}
                onChange={(e) => setDialogMetric(e.target.value)}
              >
                {USER_METRICS.map((m) => (
                  <option key={m.metric} value={m.metric}>
                    {t(m.labelKey)}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t('notifications.colOperator')}
              <div className="radio-group" data-testid="threshold-dialog-operator">
                <label>
                  <input
                    type="radio"
                    name="operator"
                    value="THRESHOLD_OPERATOR_GT"
                    checked={dialogOperator === 'THRESHOLD_OPERATOR_GT'}
                    onChange={() => setDialogOperator('THRESHOLD_OPERATOR_GT')}
                  />
                  {t('notifications.operatorGt')}
                </label>
                <label>
                  <input
                    type="radio"
                    name="operator"
                    value="THRESHOLD_OPERATOR_LT"
                    checked={dialogOperator === 'THRESHOLD_OPERATOR_LT'}
                    onChange={() => setDialogOperator('THRESHOLD_OPERATOR_LT')}
                  />
                  {t('notifications.operatorLt')}
                </label>
              </div>
            </label>
            <label>
              {t('notifications.colValue')}
              <input
                type="number"
                data-testid="threshold-dialog-value"
                value={dialogValue}
                onChange={(e) => setDialogValue(e.target.value)}
              />
            </label>
            <label className="switch">
              <input
                type="checkbox"
                checked={dialogEnabled}
                onChange={(e) => setDialogEnabled(e.target.checked)}
              />
              <span>{t('common.enabled')}</span>
            </label>
            {dialogError && <div className="error-banner">{dialogError}</div>}
            <div className="dialog-actions">
              <button className="secondary" onClick={() => setDialogOpen(false)}>
                {t('common.cancel')}
              </button>
              <button
                data-testid="threshold-dialog-submit"
                disabled={busyId === 'threshold'}
                onClick={() => void saveThreshold()}
              >
                {busyId === 'threshold' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </div>
  );
}