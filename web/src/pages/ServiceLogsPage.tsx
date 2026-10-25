// Service Logs page (admin): container logs of an inference service with
// pod/replica selector, level/time filters, tail window, follow toggle,
// client-side search, and load-more pagination.
// Implements docs/design/service-logs-viewer.md FR1-FR5 and
// docs/architecture/service-logs-viewer.md §6.5.

import { useCallback, useEffect, useRef, useState } from 'react';
import { api } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { BackLink, ErrorBanner, usePolling } from '../components';

interface ServiceLogPod {
  replicaIndex: string;
  container: string;
  state: string;
}

interface ListPodsResponse {
  response: { code: number; message: string };
  pods: ServiceLogPod[];
}

interface LogLine {
  timestamp: string;
  level: string;
  message: string;
}

interface LogsResponse {
  response: { code: number; message: string };
  lines: LogLine[];
  nextOffset: string;
  hasMore: boolean;
}

const LEVELS = ['', 'error', 'warn', 'info', 'debug'];
const RANGE_PRESETS = [
  { id: '24h', labelKey: 'usage.range24h', hours: 24 },
  { id: '7d', labelKey: 'usage.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'usage.range30d', hours: 30 * 24 },
];

export default function ServiceLogsPage() {
  const { orgId } = useOrg();
  const { t, tEnum } = useI18n();
  const serviceId = window.location.pathname.split('/')[3] || '';
  const [pods, setPods] = useState<ServiceLogPod[]>([]);
  const [replica, setReplica] = useState('');
  const [level, setLevel] = useState('');
  const [preset, setPreset] = useState('24h');
  const [follow, setFollow] = useState(false);
  const [search, setSearch] = useState('');
  const [lines, setLines] = useState<LogLine[]>([]);
  const [nextOffset, setNextOffset] = useState('');
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const paneRef = useRef<HTMLDivElement>(null);

  const loadPods = useCallback(async () => {
    try {
      const data = await api.get<ListPodsResponse>(
        `/api/v1/admin/services/${serviceId}/logs/pods`,
        orgId,
      );
      setPods(data.pods || []);
      if (!replica && data.pods && data.pods.length > 0) {
        setReplica(data.pods[0].replicaIndex);
      }
      setError('');
      setStale(false);
    } catch (e) {
      // Pod enumeration failed (e.g. no controller kubeconfig -> 10301).
      // Surface the error and leave the loading state so the page shows
      // the error banner with a Retry button instead of hanging on the
      // spinner (feature #33, §5.3 error state).
      setPods([]);
      setError(e instanceof Error ? e.message : t('servicelogs.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [serviceId, orgId, replica, t]);

  const sinceFor = useCallback(() => {
    const now = Math.floor(Date.now() / 1000);
    const hours = RANGE_PRESETS.find((p) => p.id === preset)?.hours || 24;
    return new Date((now - hours * 3600) * 1000).toISOString();
  }, [preset]);

  const loadLogs = useCallback(
    async (offset?: string) => {
      try {
        const params = new URLSearchParams();
        if (replica) params.set('pod', replica);
        if (level) params.set('level', level);
        params.set('tail', '500');
        params.set('since', sinceFor());
        if (offset) params.set('nextOffset', offset);
        const data = await api.get<LogsResponse>(
          `/api/v1/admin/services/${serviceId}/logs?${params.toString()}`,
          orgId,
        );
        if (offset) {
          setLines((prev) => [...(data.lines || []), ...prev]);
        } else {
          setLines(data.lines || []);
        }
        setNextOffset(data.nextOffset || '');
        setHasMore(data.hasMore || false);
        setError('');
        setStale(false);
      } catch (e) {
        setError(e instanceof Error ? e.message : t('servicelogs.loadFailed'));
        setStale(true);
      } finally {
        setLoading(false);
        setLoadingMore(false);
      }
    },
    [serviceId, orgId, replica, level, sinceFor, t],
  );

  useEffect(() => {
    void loadPods();
  }, [loadPods]);

  useEffect(() => {
    if (replica) void loadLogs();
  }, [replica, level, preset, loadLogs]);

  // Follow polls on a 2s interval while active.
  usePolling(() => void loadLogs(), 2000, follow && !!replica);

  // Auto-scroll to bottom when following.
  useEffect(() => {
    if (follow && paneRef.current) {
      paneRef.current.scrollTop = paneRef.current.scrollHeight;
    }
  }, [lines, follow]);

  const filtered = search
    ? lines.filter((l) => l.message.toLowerCase().includes(search.toLowerCase()))
    : lines;

  const hasRunningPods = pods.some((p) => p.state === 'Running');

  return (
    <div>
      <BackLink to={`/admin/inference-services/${serviceId}`} label={t('servicelogs.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="service-logs-title">{t('servicelogs.title')}</h1>
          <div className="subtitle mono">{serviceId}</div>
        </div>
      </div>

      {error && (
        <div>
          <ErrorBanner message={error} />
          {stale && <div className="muted">{t('servicelogs.stale')}</div>}
          <button className="secondary" data-testid="logs-retry" onClick={() => void loadLogs()}>
            {t('servicelogs.retry')}
          </button>
        </div>
      )}

      <div className="filter-bar" data-testid="logs-filter-bar">
        <label>
          {t('servicelogs.replica')}
          <select
            data-testid="logs-replica-select"
            value={replica}
            onChange={(e) => setReplica(e.target.value)}
          >
            {pods.length === 0 && <option value="">{t('servicelogs.noReplicas')}</option>}
            {pods.map((p) => (
              <option key={p.replicaIndex} value={p.replicaIndex}>
                {p.replicaIndex} ({p.container})
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('servicelogs.level')}
          <select
            data-testid="logs-level-select"
            value={level}
            onChange={(e) => setLevel(e.target.value)}
          >
            {LEVELS.map((l) => (
              <option key={l} value={l}>
                {l === '' ? t('servicelogs.levelAll') : t(`servicelogs.level${l[0].toUpperCase()}${l.slice(1)}`)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('servicelogs.timeRange')}
          <select
            data-testid="logs-range-select"
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
        <button
          className={follow ? '' : 'secondary'}
          data-testid="logs-follow-toggle"
          disabled={!hasRunningPods}
          onClick={() => setFollow((f) => !f)}
        >
          {t('servicelogs.follow')}
        </button>
        <input
          type="search"
          placeholder={t('servicelogs.search')}
          data-testid="logs-search-input"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>

      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : (
        <div className="panel">
          {!hasRunningPods && pods.length === 0 ? (
            <div className="empty-state" data-testid="logs-no-pods">
              {t('servicelogs.noPods')}
            </div>
          ) : filtered.length === 0 ? (
            <div className="empty-state" data-testid="logs-empty">
              {t('servicelogs.empty')}
              <div className="muted">{t('servicelogs.emptyHint')}</div>
            </div>
          ) : (
            <>
              {hasMore && (
                <button
                  className="secondary"
                  data-testid="logs-load-more"
                  disabled={loadingMore}
                  onClick={() => {
                    setLoadingMore(true);
                    void loadLogs(nextOffset);
                  }}
                >
                  {t('servicelogs.loadMore')}
                </button>
              )}
              <div className="log-pane" data-testid="logs-pane" ref={paneRef}>
                {filtered.map((l, i) => (
                  <div key={i} className="log-line" data-testid={`log-line-${i}`}>
                    <span className="log-time mono">{formatLogTime(l.timestamp)}</span>
                    {l.level && <span className={`badge ${l.level}`}>{tEnum('state', l.level)}</span>}
                    <span className="log-msg">{l.message}</span>
                  </div>
                ))}
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}

function formatLogTime(ts: string): string {
  const n = parseInt(ts || '0', 10);
  if (!n) return '';
  return new Date(n * 1000).toISOString();
}