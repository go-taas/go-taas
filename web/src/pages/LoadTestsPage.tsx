// Admin Load Tests page (feature #20): configure and run a load test
// against a running inference service and inspect the history of past
// runs. Implements docs/design/load-testing.md FR1-FR5, AC7/AC8/AC10.
// Admin surface only: route /admin/load-tests,
// API /api/v1/admin/load-tests/*.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatPercent, formatRate, formatTime, type PageMeta } from '../api';
import { Dialog, ErrorBanner, Pagination, usePolling } from '../components';
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

interface ServiceSummary {
  serviceId: string;
  name: string;
  state: string;
}

interface ListResponse {
  response: { code: number; message: string };
  runs: LoadTestSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;
const ACTIVE_POLL_MS = 5_000;
const IDLE_POLL_MS = 30_000;
const STATES = ['pending', 'running', 'completed', 'failed', 'stopped'];

function stateBadgeClass(state: string): string {
  switch (state) {
    case 'completed':
      return 'badge running';
    case 'failed':
      return 'badge terminated';
    case 'stopped':
      return 'badge terminated';
    case 'running':
      return 'badge deploying';
    default:
      return 'badge pending';
  }
}

// NewLoadTestDialog collects and validates the run configuration
// (design §5.2, FR2).
function NewLoadTestDialog({
  services,
  onClose,
  onCreated,
}: {
  services: ServiceSummary[];
  onClose: () => void;
  onCreated: (loadTestId: string) => void;
}) {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [serviceId, setServiceId] = useState('');
  const [concurrency, setConcurrency] = useState('1');
  const [duration, setDuration] = useState('60');
  const [rate, setRate] = useState('0');
  const [prompt, setPrompt] = useState(t('loadtests.defaultPrompt'));
  const [maxTokens, setMaxTokens] = useState('256');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const validate = (
    t: (key: string, vars?: Record<string, string | number>) => string,
  ): string => {
    if (!serviceId) return t('loadtests.validationService');
    const c = Number(concurrency);
    if (!Number.isInteger(c) || c < 1 || c > 1000)
      return t('loadtests.validationConcurrency');
    const d = Number(duration);
    if (!Number.isInteger(d) || d < 5 || d > 3600)
      return t('loadtests.validationDuration');
    const r = Number(rate);
    if (!Number.isInteger(r) || r < 0 || r > 10000)
      return t('loadtests.validationRequestRate');
    if (!prompt.trim()) return t('loadtests.validationPromptRequired');
    if (prompt.length > 4096) return t('loadtests.validationPromptLength');
    const m = Number(maxTokens);
    if (!Number.isInteger(m) || m < 1 || m > 8192)
      return t('loadtests.validationMaxTokens');
    return '';
  };

  const submit = async () => {
    const problem = validate(t);
    if (problem) {
      setError(problem);
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const res = await api.post<{ loadTestId: string }>(
        '/api/v1/admin/load-tests',
        orgId,
        {
          service_id: serviceId,
          concurrency: Number(concurrency),
          duration_seconds: Number(duration),
          request_rate: Number(rate),
          prompt_template: prompt,
          max_tokens: Number(maxTokens),
        },
      );
      onCreated(res.loadTestId);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('loadtests.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('loadtests.newTitle')} onClose={onClose} testId="new-load-test-dialog">
      {error && <ErrorBanner message={error} />}
      <label>
        {t('loadtests.fieldService')}
        <select
          value={serviceId}
          onChange={(e) => setServiceId(e.target.value)}
          data-testid="load-test-service"
        >
          <option value="">{t('loadtests.placeholderService')}</option>
          {services.map((s) => (
            <option key={s.serviceId} value={s.serviceId}>
              {s.name}
            </option>
          ))}
        </select>
      </label>
      <label>
        {t('loadtests.fieldConcurrency')}
        <input
          type="number"
          min={1}
          max={1000}
          value={concurrency}
          onChange={(e) => setConcurrency(e.target.value)}
          data-testid="load-test-concurrency"
        />
      </label>
      <label>
        {t('loadtests.fieldDuration')}
        <input
          type="number"
          min={5}
          max={3600}
          value={duration}
          onChange={(e) => setDuration(e.target.value)}
          data-testid="load-test-duration"
        />
      </label>
      <label>
        {t('loadtests.fieldRequestRate')}
        <input
          type="number"
          min={0}
          max={10000}
          value={rate}
          onChange={(e) => setRate(e.target.value)}
          data-testid="load-test-rate"
        />
      </label>
      <label>
        {t('loadtests.fieldPromptTemplate')}
        <textarea
          rows={3}
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          data-testid="load-test-prompt"
        />
      </label>
      <label>
        {t('loadtests.fieldMaxTokens')}
        <input
          type="number"
          min={1}
          max={8192}
          value={maxTokens}
          onChange={(e) => setMaxTokens(e.target.value)}
          data-testid="load-test-max-tokens"
        />
      </label>
      {services.length === 0 && (
        <p className="muted" data-testid="load-test-no-services">
          {t('loadtests.mutedNoServices')}
        </p>
      )}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button onClick={submit} disabled={submitting} data-testid="load-test-submit">
          {submitting ? t('loadtests.starting') : t('loadtests.start')}
        </button>
      </div>
    </Dialog>
  );
}

export default function LoadTestsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t, tEnum } = useI18n();
  const [runs, setRuns] = useState<LoadTestSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [statusFilter, setStatusFilter] = useState('');
  const [search, setSearch] = useState('');
  const [services, setServices] = useState<ServiceSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<number | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);

  const load = useCallback(
    async (showLoading: boolean) => {
      if (showLoading) setLoading(true);
      setError('');
      try {
        const params = new URLSearchParams({
          'page.offset': String(offset),
          'page.limit': String(PAGE_SIZE),
        });
        if (statusFilter) params.set('status', statusFilter);
        if (search) params.set('search', search);
        const data = await api.get<ListResponse>(
          `/api/v1/admin/load-tests?${params.toString()}`,
          orgId,
        );
        setRuns(data.runs || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
        setLastUpdated(Math.floor(Date.now() / 1000));
        setStale(false);
      } catch (e) {
        setError(e instanceof Error ? e.message : t('loadtests.loadFailed'));
        if (runs.length > 0) setStale(true);
      } finally {
        setLoading(false);
      }
    },
    [api, orgId, offset, statusFilter, search, runs.length, t],
  );

  const loadServices = useCallback(async () => {
    try {
      const data = await api.get<{ services: ServiceSummary[] }>(
        '/api/v1/admin/inference-services?page.limit=100',
        orgId,
      );
      setServices((data.services || []).filter((s) => s.state === 'running'));
    } catch {
      // The dialog simply shows no services when the list cannot load.
      setServices([]);
    }
  }, [api, orgId]);

  useEffect(() => {
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [offset, statusFilter, search]);

  useEffect(() => {
    void loadServices();
  }, [loadServices]);

  const hasActive = useMemo(
    () => runs.some((r) => r.state === 'pending' || r.state === 'running'),
    [runs],
  );
  usePolling(
    useCallback(() => {
      void load(false);
    }, [load]),
    hasActive ? ACTIVE_POLL_MS : IDLE_POLL_MS,
    true,
  );

  const activeRuns = runs.filter((r) => r.state === 'pending' || r.state === 'running');

  const stopRun = async (id: string) => {
    try {
      await api.post(`/api/v1/admin/load-tests/${id}:stop`, orgId, {});
      await load(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('loadtests.stopFailed'));
    }
  };

  const deleteRun = async (id: string) => {
    try {
      await api.del(`/api/v1/admin/load-tests/${id}`, orgId);
      await load(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('loadtests.deleteFailed'));
    }
  };

  return (
    <div className="page" data-testid="load-tests-page">
      <div className="page-header">
        <div>
          <h1>{t('loadtests.title')}</h1>
          <div className="subtitle">
            {t('loadtests.subtitle')}
            {lastUpdated && (
              <>
                {' · '}
                <span data-testid="load-tests-last-updated">
                  {t('loadtests.updated', { time: formatTime(lastUpdated) })}
                </span>
              </>
            )}
          </div>
        </div>
        <div className="actions">
          <button
            className="secondary"
            onClick={() => void load(false)}
            data-testid="load-tests-refresh"
          >
            {t('common.refresh')}
          </button>
          <button onClick={() => setDialogOpen(true)} data-testid="new-load-test">
            {t('loadtests.new')}
          </button>
        </div>
      </div>

      {stale && (
        <div className="banner stale" data-testid="load-tests-stale">
          {t('common.staleData')}
        </div>
      )}
      {error && (
        <div data-testid="load-tests-error">
          <ErrorBanner message={error} />
          <button className="secondary" onClick={() => void load(false)} data-testid="load-tests-retry">
            {t('common.retry')}
          </button>
        </div>
      )}

      {activeRuns.length > 0 && (
        <div className="panel" style={{ marginBottom: 16 }} data-testid="load-tests-active">
          <h3 style={{ marginTop: 0 }}>{t('loadtests.activeRuns')}</h3>
          <table className="data" data-testid="load-tests-active-table">
            <thead>
              <tr>
                <th>{t('loadtests.colService')}</th>
                <th>{t('loadtests.colState')}</th>
                <th>{t('loadtests.colConcurrency')}</th>
                <th>{t('loadtests.colStarted')}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {activeRuns.map((r) => (
                <tr key={r.loadTestId} data-testid={`load-test-row-${r.loadTestId}`}>
                  <td>
                    <a
                      href={`/admin/load-tests/${r.loadTestId}`}
                      onClick={(e) => {
                        e.preventDefault();
                        navigate(`/admin/load-tests/${r.loadTestId}`);
                      }}
                    >
                      {r.serviceName}
                    </a>
                  </td>
                  <td>
                    <span className={stateBadgeClass(r.state)}>{r.state}</span>
                  </td>
                  <td>{r.concurrency}</td>
                  <td>{formatTime(r.startedAt)}</td>
                  <td>
                    <button
                      className="secondary"
                      onClick={() => void stopRun(r.loadTestId)}
                      data-testid={`load-test-stop-${r.loadTestId}`}
                    >
                      {t('loadtest.stop')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="panel" data-testid="load-tests-history">
        <div className="filters">
          <select
            value={statusFilter}
            onChange={(e) => {
              setOffset(0);
              setStatusFilter(e.target.value);
            }}
            data-testid="load-test-status-filter"
          >
            <option value="">{t('common.allStatuses')}</option>
            {STATES.map((s) => (
              <option key={s} value={s}>
                {tEnum('state', s)}
              </option>
            ))}
          </select>
          <input
            placeholder={t('common.searchByServiceName')}
            value={search}
            onChange={(e) => {
              setOffset(0);
              setSearch(e.target.value);
            }}
            data-testid="load-test-search"
          />
        </div>

        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : runs.length === 0 ? (
          <p className="muted" data-testid="load-tests-empty">
            {t('loadtests.empty')}
          </p>
        ) : (
          <>
            <table className="data" data-testid="load-tests-table">
              <thead>
                <tr>
                  <th>{t('loadtests.colService2')}</th>
                  <th>{t('loadtests.colModel')}</th>
                  <th>{t('loadtests.colState2')}</th>
                  <th>{t('loadtests.colThroughput')}</th>
                  <th>{t('loadtests.colP95')}</th>
                  <th>{t('loadtests.colTokensSec')}</th>
                  <th>{t('loadtests.colErrorRate')}</th>
                  <th>{t('loadtests.colCreated')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {runs.map((r) => (
                  <tr key={r.loadTestId} data-testid={`load-test-row-${r.loadTestId}`}>
                    <td>
                      <a
                        href={`/admin/load-tests/${r.loadTestId}`}
                        onClick={(e) => {
                          e.preventDefault();
                          navigate(`/admin/load-tests/${r.loadTestId}`);
                        }}
                      >
                        {r.serviceName}
                      </a>
                    </td>
                    <td>{r.modelName}</td>
                    <td>
                      <span className={stateBadgeClass(r.state)}>{r.state}</span>
                    </td>
                    <td>{formatRate(r.throughputRps)} req/s</td>
                    <td>{formatRate(r.latencyP95Ms)} ms</td>
                    <td>{formatRate(r.outputTokensPerSec)}</td>
                    <td>{formatPercent(r.errorRate)}</td>
                    <td>{formatTime(r.startedAt || r.completedAt)}</td>
                    <td>
                      {r.state === 'pending' || r.state === 'running' ? (
                        <button
                          className="secondary"
                          onClick={() => void stopRun(r.loadTestId)}
                          data-testid={`load-test-stop-${r.loadTestId}`}
                        >
                          {t('loadtest.stop')}
                        </button>
                      ) : (
                        <button
                          className="secondary"
                          onClick={() => void deleteRun(r.loadTestId)}
                          data-testid={`load-test-delete-${r.loadTestId}`}
                        >
                          {t('common.delete')}
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
          </>
        )}
      </div>

      {dialogOpen && (
        <NewLoadTestDialog
          services={services}
          onClose={() => setDialogOpen(false)}
          onCreated={(id) => {
            setDialogOpen(false);
            navigate(`/admin/load-tests/${id}`);
          }}
        />
      )}
    </div>
  );
}
