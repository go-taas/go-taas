// Deployment History page (admin): inference-service lifecycle event
// trail with timestamps, actors, and field-level diffs; filters
// (service/event type/actor/time range), pagination, and rollback from
// history.
// Implements docs/design/deployment-history-audit.md FR1-FR3 and
// docs/architecture/deployment-history-audit.md §6.5.

import { useCallback, useEffect, useState } from 'react';
import { api, formatTime, type InferenceServiceSummary } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination } from '../components';

interface DeploymentEvent {
  eventId: string;
  serviceId: string;
  serviceName: string;
  eventType: string;
  actor: string;
  before: string;
  after: string;
  createdAt: string;
}

interface ListEventsResponse {
  response: { code: number; message: string };
  events: DeploymentEvent[];
  pageMeta: { total: string; offset: string; limit: string };
}

interface ListServicesResponse {
  response: { code: number; message: string };
  services: InferenceServiceSummary[];
}

const EVENT_TYPES = ['', 'create', 'update', 'scale', 'rollback', 'delete'];
const RANGE_PRESETS = [
  { id: '24h', labelKey: 'usage.range24h', hours: 24 },
  { id: '7d', labelKey: 'usage.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'usage.range30d', hours: 30 * 24 },
];

// diffSummary renders a compact before/after summary from the diff JSON,
// e.g. "replicas 2 → 4".
function diffSummary(before: string, after: string): string {
  let b: Record<string, unknown> = {};
  let a: Record<string, unknown> = {};
  try {
    b = before ? JSON.parse(before) : {};
    a = after ? JSON.parse(after) : {};
  } catch {
    return '';
  }
  const keys = new Set([...Object.keys(b), ...Object.keys(a)]);
  const parts: string[] = [];
  for (const k of keys) {
    const bv = b[k];
    const av = a[k];
    if (bv === undefined) {
      parts.push(`${k} → ${String(av)}`);
    } else if (av === undefined) {
      parts.push(`${k} ${String(bv)} → (removed)`);
    } else if (String(bv) !== String(av)) {
      parts.push(`${k} ${String(bv)} → ${String(av)}`);
    }
  }
  return parts.join(', ');
}

export default function DeploymentHistoryPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [services, setServices] = useState<InferenceServiceSummary[]>([]);
  const [serviceId, setServiceId] = useState('');
  const [eventType, setEventType] = useState('');
  const [actor, setActor] = useState('');
  const [preset, setPreset] = useState('24h');
  const [events, setEvents] = useState<DeploymentEvent[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [limit] = useState(20);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [rollbackEvent, setRollbackEvent] = useState<DeploymentEvent | null>(null);
  const [mutating, setMutating] = useState(false);
  const [notice, setNotice] = useState('');

  const loadServices = useCallback(async () => {
    try {
      const data = await api.get<ListServicesResponse>(
        '/api/v1/admin/inference-services',
        orgId,
      );
      setServices(data.services || []);
    } catch {
      setServices([]);
    }
  }, [orgId]);

  const sinceFor = useCallback(() => {
    const now = Math.floor(Date.now() / 1000);
    const hours = RANGE_PRESETS.find((p) => p.id === preset)?.hours || 24;
    return now - hours * 3600;
  }, [preset]);

  const load = useCallback(
    async (nextOffset?: number) => {
      try {
        const params = new URLSearchParams();
        if (serviceId) params.set('service_id', serviceId);
        if (eventType) params.set('event_type', eventType);
        if (actor) params.set('actor', actor);
        params.set('since', String(sinceFor()));
        params.set('page.offset', String(nextOffset ?? offset));
        params.set('page.limit', String(limit));
        const data = await api.get<ListEventsResponse>(
          `/api/v1/admin/deployments/events?${params.toString()}`,
          orgId,
        );
        setEvents(data.events || []);
        setTotal(Number(data.pageMeta?.total || 0));
        setOffset(nextOffset ?? offset);
        setError('');
        setStale(false);
      } catch (e) {
        setError(e instanceof Error ? e.message : t('deployhistory.loadFailed'));
        setStale(true);
      } finally {
        setLoading(false);
      }
    },
    [serviceId, eventType, actor, sinceFor, offset, limit, orgId, t],
  );

  useEffect(() => {
    void loadServices();
  }, [loadServices]);

  useEffect(() => {
    setOffset(0);
    void load(0);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serviceId, eventType, actor, preset]);

  const rollback = async () => {
    if (!rollbackEvent) return;
    setMutating(true);
    try {
      await api.post(
        `/api/v1/admin/deployments/${rollbackEvent.serviceId}:rollback`,
        orgId,
        { eventId: rollbackEvent.eventId },
      );
      setRollbackEvent(null);
      setNotice(t('deployhistory.rollbackSuccess'));
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('deployhistory.rollbackFailed'));
    } finally {
      setMutating(false);
    }
  };

  if (loading) {
    return (
      <div>
        <div className="page-header">
          <h1 data-testid="deploy-history-title">{t('deployhistory.title')}</h1>
        </div>
        <div className="loading">{t('common.loading')}</div>
      </div>
    );
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <h1 data-testid="deploy-history-title">{t('deployhistory.title')}</h1>
          <div className="subtitle">{t('deployhistory.subtitle')}</div>
        </div>
        <button
          className="secondary"
          data-testid="deploy-history-refresh"
          disabled={loading}
          onClick={() => void load()}
        >
          {t('deployhistory.refresh')}
        </button>
      </div>

      {notice && <div className="notice" data-testid="deploy-history-notice">{notice}</div>}
      {error && (
        <div>
          <ErrorBanner message={error} />
          {stale && <div className="muted">{t('deployhistory.stale')}</div>}
          <button className="secondary" data-testid="deploy-history-retry" onClick={() => void load()}>
            {t('deployhistory.retry')}
          </button>
        </div>
      )}

      <div className="filter-bar" data-testid="deploy-history-filter-bar">
        <label>
          {t('deployhistory.service')}
          <select
            data-testid="deploy-history-service-select"
            value={serviceId}
            onChange={(e) => setServiceId(e.target.value)}
          >
            <option value="">{t('deployhistory.allServices')}</option>
            {services.map((s) => (
              <option key={s.serviceId} value={s.serviceId}>
                {s.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('deployhistory.eventType')}
          <select
            data-testid="deploy-history-event-type-select"
            value={eventType}
            onChange={(e) => setEventType(e.target.value)}
          >
            {EVENT_TYPES.map((et) => (
              <option key={et} value={et}>
                {et === '' ? t('deployhistory.allEvents') : t(`deployhistory.type${et[0].toUpperCase()}${et.slice(1)}`)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('deployhistory.actor')}
          <input
            type="text"
            data-testid="deploy-history-actor-input"
            value={actor}
            onChange={(e) => setActor(e.target.value)}
          />
        </label>
        <label>
          {t('deployhistory.timeRange')}
          <select
            data-testid="deploy-history-range-select"
            value={preset}
            onChange={(e) => setPreset(e.target.value)}
          >
            {RANGE_PRESETS.map((p) => (
              <option key={p.id} value={p.id}>
                {t(p.labelKey)}
              </option>
            ))}
          </select>
        </label>
      </div>

      {events.length === 0 ? (
        <div className="empty-state" data-testid="deploy-history-empty">
          {t('deployhistory.empty')}
          <div className="muted">{t('deployhistory.emptyHint')}</div>
        </div>
      ) : (
        <div className="panel">
          <table className="table" data-testid="deploy-history-table">
            <thead>
              <tr>
                <th>{t('deployhistory.colTime')}</th>
                <th>{t('deployhistory.colService')}</th>
                <th>{t('deployhistory.colEvent')}</th>
                <th>{t('deployhistory.colActor')}</th>
                <th>{t('deployhistory.colDiff')}</th>
                <th>{t('deployhistory.colActions')}</th>
              </tr>
            </thead>
            <tbody>
              {events.map((ev, i) => (
                <tr key={ev.eventId} data-testid={`deploy-history-row-${i}`}>
                  <td className="mono">{formatTime(ev.createdAt)}</td>
                  <td>{ev.serviceName}</td>
                  <td>
                    <span className={`badge ${ev.eventType}`} data-testid={`deploy-history-event-${i}`}>
                      {ev.eventType}
                    </span>
                  </td>
                  <td>{ev.actor}</td>
                  <td className="mono">{diffSummary(ev.before, ev.after)}</td>
                  <td>
                    <button
                      className="link danger"
                      data-testid={`deploy-history-rollback-${i}`}
                      disabled={!ev.before || ev.before === '{}'}
                      onClick={() => setRollbackEvent(ev)}
                    >
                      {t('deployhistory.rollback')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <Pagination
            offset={offset}
            limit={limit}
            total={total}
            onPageChange={(o) => void load(o)}
          />
        </div>
      )}

      {rollbackEvent && (
        <Dialog
          title={t('deployhistory.rollbackTitle')}
          onClose={() => setRollbackEvent(null)}
          testId="deploy-history-rollback-dialog"
        >
          <p>
            {t('deployhistory.rollbackConfirm', {
              service: rollbackEvent.serviceName,
              diff: diffSummary(rollbackEvent.before, rollbackEvent.after),
            })}
          </p>
          <p className="muted">{t('deployhistory.rollbackWarning')}</p>
          <div className="dialog-actions">
            <button className="secondary" data-testid="deploy-history-rollback-cancel" onClick={() => setRollbackEvent(null)}>
              {t('deployhistory.cancel')}
            </button>
            <button data-testid="deploy-history-rollback-confirm" disabled={mutating} onClick={() => void rollback()}>
              {t('deployhistory.rollbackBtn')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}