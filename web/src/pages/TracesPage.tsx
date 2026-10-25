// Admin Traces page (feature #27): the fleet-wide trace explorer —
// request-id lookup, filters (time range, model, API key, status), a
// trace table, and drill-down to the per-trace detail. Admin surface:
// route /admin/traces, API /api/v1/admin/traces.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { navigate } from '../router';
import { ErrorBanner, Pagination } from '../components';
import { formatTime, type ListTracesResponse } from '../api';

const PAGE_SIZE = 20;
const RANGE_PRESETS = [
  { id: '24h', labelKey: 'traces.range24h', hours: 24 },
  { id: '7d', labelKey: 'traces.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'traces.range30d', hours: 30 * 24 },
  { id: 'custom', labelKey: 'traces.customRange', hours: 0 },
];

function rangeFor(preset: string, customSince: string, customUntil: string): { since: number; until: number } {
  const now = Math.floor(Date.now() / 1000);
  if (preset === 'custom') {
    const until = customUntil ? Math.floor(new Date(customUntil).getTime() / 1000) : now;
    const since = customSince
      ? Math.floor(new Date(customSince).getTime() / 1000)
      : until - 24 * 3600;
    return { since, until };
  }
  const hours = RANGE_PRESETS.find((p) => p.id === preset)?.hours || 24;
  return { since: now - hours * 3600, until: now };
}

export default function TracesPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t, tEnum } = useI18n();
  const [preset, setPreset] = useState('24h');
  const [customSince, setCustomSince] = useState('');
  const [customUntil, setCustomUntil] = useState('');
  const [modelFilter, setModelFilter] = useState('');
  const [keyFilter, setKeyFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [lookupId, setLookupId] = useState('');
  const [lookupNotFound, setLookupNotFound] = useState('');
  const [data, setData] = useState<ListTracesResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [offset, setOffset] = useState(0);

  const range = useMemo(
    () => rangeFor(preset, customSince, customUntil),
    [preset, customSince, customUntil],
  );

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    setLookupNotFound('');
    try {
      const params = new URLSearchParams({
        since: String(range.since),
        until: String(range.until),
      });
      if (modelFilter) params.set('model_id', modelFilter);
      if (keyFilter) params.set('api_key_id', keyFilter);
      if (statusFilter) params.set('status', statusFilter);
      const next = await api.get<ListTracesResponse>(
        `/api/v1/admin/traces?${params.toString()}`,
        orgId,
      );
      setData(next);
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('traces.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [api, orgId, range.since, range.until, modelFilter, keyFilter, statusFilter, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const doLookup = async () => {
    const id = lookupId.trim();
    if (!id) return;
    setLoading(true);
    setError('');
    setLookupNotFound('');
    try {
      const params = new URLSearchParams({ request_id: id });
      const next = await api.get<ListTracesResponse>(
        `/api/v1/admin/traces?${params.toString()}`,
        orgId,
      );
      if (next.traces && next.traces.length > 0) {
        navigate(`/admin/traces/${next.traces[0].traceId}`);
      } else {
        setLookupNotFound(id);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : t('traces.loadFailed'));
    } finally {
      setLoading(false);
    }
  };

  const traces = data?.traces || [];
  const pageRows = traces.slice(offset, offset + PAGE_SIZE);

  return (
    <div data-testid="traces-page">
      <div className="page-header">
        <div>
          <h1>{t('traces.title')}</h1>
          <div className="subtitle">{t('traces.subtitle')}</div>
        </div>
        <button
          className="secondary"
          data-testid="traces-refresh"
          disabled={loading}
          onClick={() => void load()}
        >
          {t('traces.refresh')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}
      {stale && data && (
        <div className="error-banner" data-testid="traces-stale-banner">
          {t('traces.staleData')}
        </div>
      )}
      {lookupNotFound && (
        <div className="error-banner" data-testid="traces-lookup-notfound">
          {t('traces.lookupNotFound', { id: lookupNotFound })}
        </div>
      )}

      <div className="toolbar" style={{ marginBottom: 14 }} data-testid="traces-lookup">
        <input
          type="text"
          placeholder={t('traces.lookupPlaceholder')}
          data-testid="traces-lookup-input"
          value={lookupId}
          onChange={(e) => setLookupId(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') void doLookup();
          }}
        />
        <button
          className="primary"
          data-testid="traces-lookup-button"
          disabled={loading || !lookupId.trim()}
          onClick={() => void doLookup()}
        >
          {t('traces.lookup')}
        </button>
      </div>

      <div className="toolbar" style={{ marginBottom: 14 }} data-testid="traces-filter-range">
        {RANGE_PRESETS.map((p) => (
          <button
            key={p.id}
            className={preset === p.id ? '' : 'secondary'}
            data-testid={`traces-range-${p.id}`}
            onClick={() => setPreset(p.id)}
          >
            {t(p.labelKey)}
          </button>
        ))}
        {preset === 'custom' && (
          <>
            <input
              type="datetime-local"
              data-testid="traces-custom-since"
              value={customSince}
              onChange={(e) => setCustomSince(e.target.value)}
            />
            <input
              type="datetime-local"
              data-testid="traces-custom-until"
              value={customUntil}
              onChange={(e) => setCustomUntil(e.target.value)}
            />
          </>
        )}
        <span className="toolbar-spacer" />
        <select
          data-testid="traces-filter-model"
          value={modelFilter}
          onChange={(e) => {
            setModelFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('traces.allModels')}</option>
          {Array.from(new Set(traces.map((tr) => tr.modelId))).map((m) => (
            <option key={m} value={m}>
              {traces.find((tr) => tr.modelId === m)?.modelName || m}
            </option>
          ))}
        </select>
        <select
          data-testid="traces-filter-key"
          value={keyFilter}
          onChange={(e) => {
            setKeyFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('traces.allKeys')}</option>
          {Array.from(new Set(traces.map((tr) => tr.apiKeyId))).map((k) => (
            <option key={k} value={k}>
              {traces.find((tr) => tr.apiKeyId === k)?.apiKeyName || k}
            </option>
          ))}
        </select>
        <select
          data-testid="traces-filter-status"
          value={statusFilter}
          onChange={(e) => {
            setStatusFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('traces.allStatus')}</option>
          <option value="success">{t('traces.statusSuccess')}</option>
          <option value="error">{t('traces.statusError')}</option>
        </select>
      </div>

      <div className="panel">
        {traces.length === 0 ? (
          <p className="muted" data-testid="traces-empty">
            {t('traces.empty')}
          </p>
        ) : (
          <>
            <table className="data" data-testid="traces-table">
              <thead>
                <tr>
                  <th>{t('traces.colTraceId')}</th>
                  <th>{t('traces.colTime')}</th>
                  <th>{t('traces.colModel')}</th>
                  <th>{t('traces.colKey')}</th>
                  <th>{t('traces.colStatus')}</th>
                  <th>{t('traces.colTotal')}</th>
                  <th>{t('traces.colTTFT')}</th>
                  <th>{t('traces.colGeneration')}</th>
                  <th>{t('traces.colError')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {pageRows.map((tr) => (
                  <tr key={tr.traceId} data-testid={`traces-row-${tr.traceId}`}>
                    <td>
                      <a
                        href={`/admin/traces/${tr.traceId}`}
                        onClick={(e) => {
                          e.preventDefault();
                          navigate(`/admin/traces/${tr.traceId}`);
                        }}
                      >
                        {tr.traceId}
                      </a>
                    </td>
                    <td>{formatTime(tr.createdAt)}</td>
                    <td>{tr.modelName || tr.modelId}</td>
                    <td>{tr.apiKeyName || tr.apiKeyId}</td>
                    <td>
                      <span className={`status-badge ${tr.status}`}>{tEnum('state', tr.status)}</span>
                    </td>
                    <td>{tr.totalLatencyMs} ms</td>
                    <td>{tr.ttftMs} ms</td>
                    <td>{tr.generationMs} ms</td>
                    <td className="muted">{tr.error}</td>
                    <td>
                      <button
                        className="secondary"
                        data-testid={`traces-view-${tr.traceId}`}
                        onClick={() => navigate(`/admin/traces/${tr.traceId}`)}
                      >
                        {t('traces.view')}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Pagination
              offset={offset}
              limit={PAGE_SIZE}
              total={traces.length}
              onPageChange={setOffset}
            />
          </>
        )}
      </div>
    </div>
  );
}