// Admin Trace Detail page (feature #27): one trace's full detail — the
// summary strip, the latency-breakdown card (TTFT, generation, total),
// the span waterfall, and the metadata table. Admin surface: route
// /admin/traces/:traceId, API /api/v1/admin/traces/{trace_id}.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { BackLink, ErrorBanner } from '../components';
import TraceWaterfall from '../components/TraceWaterfall';
import { formatTime, type GetTraceResponse } from '../api';

export default function TraceDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t, tEnum } = useI18n();
  const pathParts = window.location.pathname.split('/');
  const traceId = pathParts[pathParts.length - 1] || '';
  const [data, setData] = useState<GetTraceResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notFound, setNotFound] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    setNotFound(false);
    try {
      const next = await api.get<GetTraceResponse>(
        `/api/v1/admin/traces/${traceId}`,
        orgId,
      );
      setData(next);
    } catch (e) {
      const code = e && (e as { code?: number }).code;
      if (code === 11101) {
        setNotFound(true);
      } else {
        setError(e instanceof Error ? e.message : t('traces.loadFailed'));
      }
    } finally {
      setLoading(false);
    }
  }, [api, orgId, traceId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  if (notFound) {
    return (
      <div data-testid="trace-detail-notfound">
        <BackLink to="/admin/traces" label={t('traces.backToTraces')} />
        <h1>{t('traces.notFoundTitle')}</h1>
        <p className="muted">{t('traces.notFoundBody', { id: traceId })}</p>
      </div>
    );
  }

  const trace = data?.trace;
  const spans = trace?.spans || [];
  const totalMs = trace ? parseInt(trace.totalLatencyMs || '0', 10) : 0;
  const ttft = trace ? parseInt(trace.ttftMs || '0', 10) : 0;
  const gen = trace ? parseInt(trace.generationMs || '0', 10) : 0;

  return (
    <div data-testid="trace-detail-page">
      <BackLink to="/admin/traces" label={t('traces.backToTraces')} />
      <div className="page-header">
        <div>
          <h1>{traceId}</h1>
          <div className="subtitle">
            {trace ? (
              <>
                <span className={`status-badge ${trace.status}`}>{tEnum('state', trace.status)}</span>
                <span className="muted" style={{ marginLeft: 8 }}>
                  {formatTime(trace.createdAt)}
                </span>
              </>
            ) : (
              t('traces.loading')
            )}
          </div>
        </div>
        <button
          className="secondary"
          data-testid="trace-detail-refresh"
          disabled={loading}
          onClick={() => void load()}
        >
          {t('traces.refresh')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      {trace && (
        <>
          <div className="panel" data-testid="trace-summary">
            <h3 style={{ marginTop: 0 }}>{t('traces.summaryTitle')}</h3>
            <div className="summary-strip">
              <span>
                <strong>{t('traces.colModel')}:</strong> {trace.modelName || trace.modelId}
              </span>
              <span>
                <strong>{t('traces.colKey')}:</strong> {trace.apiKeyName || trace.apiKeyId}
              </span>
              <span>
                <strong>{t('traces.colService')}:</strong> {trace.serviceId || '—'}
              </span>
              <span>
                <strong>{t('traces.colTotal')}:</strong> {totalMs} ms
              </span>
              {trace.error && (
                <span>
                  <strong>{t('traces.colError')}:</strong> {trace.error}
                </span>
              )}
            </div>
          </div>

          <div className="panel" style={{ marginTop: 16 }} data-testid="trace-latency-card">
            <h3 style={{ marginTop: 0 }}>{t('traces.latencyTitle')}</h3>
            <div className="latency-breakdown">
              <div className="latency-bar">
                <div
                  className="latency-segment ttft"
                  style={{ width: `${totalMs ? (ttft / totalMs) * 100 : 0}%` }}
                  data-testid="trace-latency-ttft"
                />
                <div
                  className="latency-segment gen"
                  style={{ width: `${totalMs ? (gen / totalMs) * 100 : 0}%` }}
                  data-testid="trace-latency-generation"
                />
              </div>
              <div className="latency-legend">
                <span>
                  <strong>{t('traces.colTTFT')}:</strong> {ttft} ms
                </span>
                <span>
                  <strong>{t('traces.colGeneration')}:</strong> {gen} ms
                </span>
                <span>
                  <strong>{t('traces.colTotal')}:</strong> {totalMs} ms
                </span>
              </div>
            </div>
          </div>

          <div className="panel" style={{ marginTop: 16 }}>
            <h3 style={{ marginTop: 0 }}>{t('traces.waterfallTitle')}</h3>
            {spans.length === 0 ? (
              <p className="muted" data-testid="trace-waterfall-empty">
                {t('traces.noSpans')}
              </p>
            ) : (
              <TraceWaterfall spans={spans} totalMs={totalMs} />
            )}
          </div>

          <div className="panel" style={{ marginTop: 16 }} data-testid="trace-metadata">
            <h3 style={{ marginTop: 0 }}>{t('traces.metadataTitle')}</h3>
            <table className="data">
              <tbody>
                <tr>
                  <td>{t('traces.colPromptTokens')}</td>
                  <td>{trace.promptTokens}</td>
                </tr>
                <tr>
                  <td>{t('traces.colCompletionTokens')}</td>
                  <td>{trace.completionTokens}</td>
                </tr>
                <tr>
                  <td>{t('traces.colCachedTokens')}</td>
                  <td>{trace.cachedTokens}</td>
                </tr>
                <tr>
                  <td>{t('traces.colReasoningTokens')}</td>
                  <td>{trace.reasoningTokens}</td>
                </tr>
                <tr>
                  <td>{t('traces.colStatus')}</td>
                  <td>{tEnum('state', trace.status)}</td>
                </tr>
                <tr>
                  <td>{t('traces.colError')}</td>
                  <td>{trace.error || '—'}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}