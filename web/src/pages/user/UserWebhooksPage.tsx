// End-user Webhooks page (feature #23): list, create, enable/disable,
// delete webhooks subscribed to tenant account events. Route /webhooks,
// API /api/v1/webhooks/*.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { navigate } from '../../router';
import { ApiError, formatTime, type PageMeta } from '../../api';
import { Dialog, ErrorBanner, Pagination, RowActionsMenu, StateBadge } from '../../components';
import SecretRevealDialog from '../../components/SecretRevealDialog';
import type { Webhook } from '../WebhooksPage';

interface ListResponse {
  response: { code: number; message: string };
  webhooks: Webhook[];
  pageMeta?: PageMeta;
}

interface CreateResponse {
  response: { code: number; message: string };
  webhook: Webhook;
  plaintextSecret: string;
}

const PAGE_SIZE = 20;

// The end-user event catalog (feature #23, §3.4).
const USER_EVENTS: { type: string; descKey: string }[] = [
  { type: 'billing.invoice_created', descKey: 'webhooks.eventInvoiceCreated' },
  { type: 'billing.invoice_paid', descKey: 'webhooks.eventInvoicePaid' },
  { type: 'billing.spend_limit_breached', descKey: 'webhooks.eventSpendLimit' },
  { type: 'billing.balance_low', descKey: 'webhooks.eventBalanceLow' },
];

export default function UserWebhooksPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [webhooks, setWebhooks] = useState<Webhook[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [createdSecret, setCreatedSecret] = useState<string | null>(null);
  const [createdId, setCreatedId] = useState('');
  const [busyId, setBusyId] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams();
      params.set('page.offset', String(offset));
      params.set('page.limit', String(PAGE_SIZE));
      const data = await api.get<ListResponse>(
        `/api/v1/webhooks?${params.toString()}`,
        orgId,
      );
      setWebhooks(data.webhooks || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('webhooks.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, offset, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const toggleEnabled = async (wh: Webhook) => {
    setBusyId(wh.webhookId);
    try {
      await api.post(`/api/v1/webhooks/${wh.webhookId}:set-enabled`, orgId, {
        enabled: !wh.enabled,
      });
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('webhooks.updateFailed'));
    } finally {
      setBusyId('');
    }
  };

  const remove = async (wh: Webhook) => {
    if (!window.confirm(t('webhooks.deleteConfirm', { name: wh.name }))) return;
    setBusyId(wh.webhookId);
    try {
      await api.del(`/api/v1/webhooks/${wh.webhookId}`, orgId);
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('webhooks.deleteFailed'));
    } finally {
      setBusyId('');
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('webhooks.title')}</h1>
          <div className="subtitle">{t('webhooks.userSubtitle')}</div>
        </div>
        <div className="header-actions">
          <button className="secondary" data-testid="webhook-refresh" onClick={() => void load()}>
            {t('common.refresh')}
          </button>
          <button data-testid="webhook-new" onClick={() => setCreateOpen(true)}>
            {t('webhooks.new')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel" data-testid="webhook-catalog">
        <h3>{t('webhooks.catalogTitle')}</h3>
        <ul className="event-catalog">
          {USER_EVENTS.map((ev) => (
            <li key={ev.type}>
              <code>{ev.type}</code>
              <span>{t(ev.descKey)}</span>
            </li>
          ))}
        </ul>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : webhooks.length === 0 ? (
          <div className="empty-state" data-testid="webhook-empty">
            {t('webhooks.userEmpty')}
          </div>
        ) : (
          <table className="data" data-testid="webhook-table">
            <thead>
              <tr>
                <th>{t('webhooks.colName')}</th>
                <th>{t('webhooks.colUrl')}</th>
                <th>{t('webhooks.colEvents')}</th>
                <th>{t('common.status')}</th>
                <th>{t('webhooks.colDeliveries')}</th>
                <th>{t('common.created')}</th>
                <th className="col-actions">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {webhooks.map((wh) => (
                <tr key={wh.webhookId} data-testid={`webhook-row-${wh.webhookId}`}>
                  <td>
                    <a
                      href={`/webhooks/${wh.webhookId}`}
                      onClick={(e) => {
                        e.preventDefault();
                        navigate(`/webhooks/${wh.webhookId}`);
                      }}
                    >
                      {wh.name}
                    </a>
                  </td>
                  <td className="mono">{wh.url}</td>
                  <td>{wh.enabledEventTypes.length}</td>
                  <td>
                    <StateBadge state={wh.enabled ? 'enabled' : 'disabled'} />
                  </td>
                  <td>
                    {wh.deliveredCount} / {wh.failedCount}
                  </td>
                  <td>{formatTime(wh.createdAt)}</td>
                  <td>
                    <RowActionsMenu
                      label={t('common.actionsFor', { name: wh.url || wh.webhookId })}
                      testId={`webhook-actions-${wh.webhookId}`}
                      actions={[
                        {
                          key: `webhook-view-${wh.webhookId}`,
                          label: t('common.view'),
                          onClick: () => navigate(`/webhooks/${wh.webhookId}`),
                        },
                        {
                          key: `webhook-toggle-${wh.webhookId}`,
                          label: wh.enabled ? t('common.disable') : t('common.enable'),
                          disabled: busyId === wh.webhookId,
                          onClick: () => void toggleEnabled(wh),
                        },
                        {
                          key: `webhook-delete-${wh.webhookId}`,
                          label: t('common.delete'),
                          danger: true,
                          disabled: busyId === wh.webhookId,
                          onClick: () => void remove(wh),
                        },
                      ]}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {total > PAGE_SIZE && (
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        )}
      </div>

      {createOpen && (
        <CreateDialog
          orgId={orgId}
          onClose={() => setCreateOpen(false)}
          onCreated={(res) => {
            setCreateOpen(false);
            setCreatedSecret(res.plaintextSecret);
            setCreatedId(res.webhook.webhookId);
            void load();
          }}
        />
      )}

      {createdSecret && (
        <SecretRevealDialog
          secret={createdSecret}
          onDone={() => {
            setCreatedSecret(null);
            navigate(`/webhooks/${createdId}`);
          }}
        />
      )}
    </div>
  );
}

function CreateDialog({
  orgId,
  onClose,
  onCreated,
}: {
  orgId: string;
  onClose: () => void;
  onCreated: (res: CreateResponse) => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [url, setUrl] = useState('');
  const [events, setEvents] = useState<string[]>([]);
  const [maxAttempts, setMaxAttempts] = useState('5');
  const [backoff, setBackoff] = useState('60');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const toggleEvent = (type: string) => {
    setEvents((prev) =>
      prev.includes(type) ? prev.filter((e) => e !== type) : [...prev, type],
    );
  };

  const submit = async () => {
    if (!name.trim()) {
      setError(t('webhooks.nameRequired'));
      return;
    }
    if (!/^https?:\/\/.+/.test(url.trim())) {
      setError(t('webhooks.urlRequired'));
      return;
    }
    if (events.length === 0) {
      setError(t('webhooks.eventsRequired'));
      return;
    }
    const ma = parseInt(maxAttempts, 10);
    const bo = parseInt(backoff, 10);
    if (Number.isNaN(ma) || ma < 1 || ma > 10) {
      setError(t('webhooks.maxAttemptsInvalid'));
      return;
    }
    if (Number.isNaN(bo) || bo < 1 || bo > 3600) {
      setError(t('webhooks.backoffInvalid'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const res = await api.post<CreateResponse>('/api/v1/webhooks', orgId, {
        name: name.trim(),
        url: url.trim(),
        enabledEventTypes: events,
        maxAttempts: ma,
        backoffSeconds: bo,
      });
      onCreated(res);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('webhooks.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('webhooks.newTitle')} onClose={onClose} testId="webhook-create-dialog">
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="wh-name">{t('webhooks.fieldName')}</label>
          <input
            id="wh-name"
            data-testid="webhook-dialog-name"
            value={name}
            maxLength={64}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <div className="form-field full">
          <label htmlFor="wh-url">{t('webhooks.fieldUrl')}</label>
          <input
            id="wh-url"
            data-testid="webhook-dialog-url"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://example.com/hook"
          />
        </div>
        <div className="form-field full">
          <label>{t('webhooks.fieldEvents')}</label>
          <div data-testid="webhook-dialog-events">
            {USER_EVENTS.map((ev) => (
              <label key={ev.type} className="checkbox-row">
                <input
                  type="checkbox"
                  checked={events.includes(ev.type)}
                  onChange={() => toggleEvent(ev.type)}
                />
                <code>{ev.type}</code>
              </label>
            ))}
          </div>
        </div>
        <div className="form-field">
          <label htmlFor="wh-max">{t('webhooks.fieldMaxAttempts')}</label>
          <input
            id="wh-max"
            data-testid="webhook-dialog-max-attempts"
            type="number"
            min={1}
            max={10}
            value={maxAttempts}
            onChange={(e) => setMaxAttempts(e.target.value)}
          />
        </div>
        <div className="form-field">
          <label htmlFor="wh-backoff">{t('webhooks.fieldBackoff')}</label>
          <input
            id="wh-backoff"
            data-testid="webhook-dialog-backoff"
            type="number"
            min={1}
            max={3600}
            value={backoff}
            onChange={(e) => setBackoff(e.target.value)}
          />
        </div>
      </div>
      {error && <div className="error" data-testid="webhook-dialog-error">{error}</div>}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="webhook-dialog-submit" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.creating') : t('webhooks.create')}
        </button>
      </div>
    </Dialog>
  );
}