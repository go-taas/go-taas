// Admin Load Test detail page (feature #20): the run configuration, live
// progress while running, the curated result set when completed, and the
// Stop / Delete actions. Implements docs/design/load-testing.md FR3, FR4,
// FR5.4, AC9. Admin surface only: route /admin/load-tests/:loadTestId,
// API /api/v1/admin/load-tests/{load_test_id}.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { ApiError, formatPercent, formatRate, formatTime } from '../api';
import { BackLink, ErrorBanner, usePolling } from '../components';
import { navigate } from '../router';
import { useI18n } from '../i18n';

interface LoadTestSummary {
  loadTestId: string;
  serviceId: string;
  serviceName: string;
  modelName: string;
  concurrency: number;
  durationSeconds: number;
  requestRate: number;
  state: string;
  startedAt: string;
  completedAt: string;
  throughputRps: number;
  latencyP95Ms: number;
  outputTokensPerSec: number;
  errorRate: number;
}

interface Progress {
  requestsSent: string;
  successCount: string;
  failureCount: string;
  elapsedSeconds: string;
}

interface Result {
  totalRequests: string;
  successCount: string;
  failureCount: string;
  errorRate: number;
  throughputRps: number;
  outputTokensPerSec: number;
  inputTokens: string;
  outputTokens: string;
  latencyP50Ms: number;
  latencyP90Ms: number;
  latencyP95Ms: number;
  latencyP99Ms: number;
}

interface DetailResponse {
  response: { code: number; message: string };
  summary: LoadTestSummary;
  promptTemplate: string;
  maxTokens: number;
  progress?: Progress;
  result?: Result;
  failureReason: string;
}

const POLL_MS = 5_000;

function stateBadgeClass(state: string): string {
  switch (state) {
    case 'completed':
      return 'badge running';
    case 'running':
      return 'badge deploying';
    case 'failed':
    case 'stopped':
      return 'badge terminated';
    default:
      return 'badge pending';
  }
}

export default function LoadTestDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const id = window.location.pathname.split('/').pop() || '';
  const [data, setData] = useState<DetailResponse | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  const load = useCallback(
    async (showLoading: boolean) => {
      if (showLoading) setLoading(true);
      try {
        const res = await api.get<DetailResponse>(`/api/v1/admin/load-tests/${id}`, orgId);
        setData(res);
        setError('');
        setStale(false);
      } catch (e) {
        if (e instanceof ApiError && e.code === 10308) {
          setNotFound(true);
          return;
        }
        setError(e instanceof Error ? e.message : t('loadtest.loadFailed'));
        setStale(true);
      } finally {
        setLoading(false);
      }
    },
    [api, orgId, id, t],
  );

  useEffect(() => {
    void load(true);
  }, [load]);

  const state = data?.summary.state || '';
  const active = state === 'pending' || state === 'running';
  usePolling(
    useCallback(() => {
      void load(false);
    }, [load]),
    POLL_MS,
    active,
  );

  const stop = async () => {
    try {
      await api.post(`/api/v1/admin/load-tests/${id}:stop`, orgId, {});
      await load(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('loadtest.stopFailed'));
    }
  };

  const remove = async () => {
    try {
      await api.del(`/api/v1/admin/load-tests/${id}`, orgId);
      navigate('/admin/load-tests');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('loadtest.deleteFailed'));
    }
  };

  if (loading) return <div className="loading">{t('common.loading')}</div>;
  if (notFound)
    return (
      <div className="page" data-testid="load-test-not-found">
        <BackLink to="/admin/load-tests" label={t('loadtest.back')} />
        <div className="error">{t('loadtest.notFound')}</div>
      </div>
    );
  if (!data) return null;

  const s = data.summary;
  const progress = data.progress;
  const result = data.result;
  const elapsed = progress ? parseInt(progress.elapsedSeconds || '0', 10) || 0 : 0;

  return (
    <div className="page" data-testid="load-test-detail-page">
      <BackLink to="/admin/load-tests" label={t('loadtest.back')} />

      <div className="page-header">
        <div>
          <h1 data-testid="load-test-detail-service">{s.serviceName}</h1>
          <div className="subtitle mono">{s.loadTestId}</div>
        </div>
        <div className="actions">
          {active && (
            <button onClick={() => void stop()} data-testid="load-test-stop">
              {t('loadtest.stop')}
            </button>
          )}
          {!active && (
            <button className="secondary" onClick={() => void remove()} data-testid="load-test-delete">
              {t('common.delete')}
            </button>
          )}
        </div>
      </div>

      {stale && (
        <div className="banner stale" data-testid="load-test-detail-stale">
          {t('loadtest.stale')}
        </div>
      )}
      {error && (
        <div data-testid="load-test-detail-error">
          <ErrorBanner message={error} />
        </div>
      )}

      <div className="panel" style={{ marginBottom: 16 }} data-testid="load-test-summary">
        <h3 style={{ marginTop: 0 }}>{t('loadtest.config')}</h3>
        <div className="detail-grid">
          <div className="detail-item">
            <div className="label">{t('loadtest.state')}</div>
            <div className="value">
              <span className={stateBadgeClass(s.state)} data-testid="load-test-detail-state">
                {s.state}
              </span>
            </div>
          </div>
          <div className="detail-item">
            <div className="label">{t('loadtest.model')}</div>
            <div className="value">{s.modelName}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('loadtest.concurrency')}</div>
            <div className="value">{s.concurrency}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('loadtest.duration')}</div>
            <div className="value">{t('loadtest.durationS', { n: s.durationSeconds })}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('loadtest.requestRate')}</div>
            <div className="value">{s.requestRate === 0 ? t('common.unlimited') : `${s.requestRate} req/s`}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('loadtest.maxTokens')}</div>
            <div className="value">{data.maxTokens}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('loadtest.started')}</div>
            <div className="value">{formatTime(s.startedAt)}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('loadtest.completed')}</div>
            <div className="value">{formatTime(s.completedAt)}</div>
          </div>
        </div>
        <div className="detail-item" style={{ marginTop: 12 }}>
          <div className="label">{t('loadtest.promptTemplate')}</div>
          <pre className="mono" data-testid="load-test-prompt-template">
            {data.promptTemplate}
          </pre>
        </div>
        {data.failureReason && (
          <p className="error" data-testid="load-test-failure-reason">
            {data.failureReason}
          </p>
        )}
      </div>

      {active && (
        <div className="panel" style={{ marginBottom: 16 }} data-testid="load-test-progress">
          <h3 style={{ marginTop: 0 }}>{t('loadtest.liveProgress')}</h3>
          <div className="detail-grid">
            <div className="detail-item">
              <div className="label">{t('loadtest.requestsSent')}</div>
              <div className="value" data-testid="load-test-requests-sent">
                {progress?.requestsSent ?? '0'}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('loadtest.successes')}</div>
              <div className="value" data-testid="load-test-successes">
                {progress?.successCount ?? '0'}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('loadtest.failures')}</div>
              <div className="value" data-testid="load-test-failures">
                {progress?.failureCount ?? '0'}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('loadtest.elapsed')}</div>
              <div className="value" data-testid="load-test-elapsed">
                {elapsed}s / {s.durationSeconds}s
              </div>
            </div>
          </div>
        </div>
      )}

      {result && (
        <div className="panel" data-testid="load-test-result">
          <h3 style={{ marginTop: 0 }}>{t('loadtest.results')}</h3>
          <div className="detail-grid">
            <div className="detail-item">
              <div className="label">{t('loadtest.throughput')}</div>
              <div className="value" data-testid="load-test-throughput">
                {formatRate(result.throughputRps)} req/s
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('loadtest.outputTokensSec')}</div>
              <div className="value" data-testid="load-test-tokens-per-sec">
                {formatRate(result.outputTokensPerSec)}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('loadtest.errorRate')}</div>
              <div className="value" data-testid="load-test-error-rate">
                {formatPercent(result.errorRate)}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('loadtest.totalRequests')}</div>
              <div className="value">{result.totalRequests}</div>
            </div>
            <div className="detail-item">
              <div className="label">{t('loadtest.successFailures')}</div>
              <div className="value">
                {result.successCount} / {result.failureCount}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('loadtest.inputOutputTokens')}</div>
              <div className="value">
                {result.inputTokens} / {result.outputTokens}
              </div>
            </div>
          </div>

          <table className="data" style={{ marginTop: 12 }} data-testid="load-test-latency-table">
            <thead>
              <tr>
                <th>{t('loadtest.colPercentile')}</th>
                <th>{t('loadtest.colLatency')}</th>
              </tr>
            </thead>
            <tbody>
              <tr data-testid="load-test-latency-p50">
                <td>p50</td>
                <td>{formatRate(result.latencyP50Ms)} ms</td>
              </tr>
              <tr data-testid="load-test-latency-p90">
                <td>p90</td>
                <td>{formatRate(result.latencyP90Ms)} ms</td>
              </tr>
              <tr data-testid="load-test-latency-p95">
                <td>p95</td>
                <td>{formatRate(result.latencyP95Ms)} ms</td>
              </tr>
              <tr data-testid="load-test-latency-p99">
                <td>p99</td>
                <td>{formatRate(result.latencyP99Ms)} ms</td>
              </tr>
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
