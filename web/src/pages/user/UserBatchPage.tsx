// End-user Batch page (feature #42): submit a JSONL batch of inference
// requests, monitor its progress, and download the result/error files.
// End-user surface: route /batch, API prefix /api/v1/batch/*. No
// organization dropdown (the caller's org is implicit, AD5). Implements
// docs/design/batch-inference.md §5.3 and
// docs/architecture/batch-inference.md §6.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { navigate } from '../../router';
import { formatTime, type PageMeta } from '../../api';
import { Dialog, ErrorBanner, Pagination, StateBadge } from '../../components';

export interface BatchJob {
  batchId: string;
  organizationId: string;
  model: string;
  status: string;
  completionWindow: string;
  totalRequests: string;
  processedRequests: string;
  succeededRequests: string;
  failedRequests: string;
  inputTokens: string;
  outputTokens: string;
  cost: string;
  currency: string;
  filesExpired: boolean;
  error: string;
  createdAt: string;
  completedAt: string;
  filesExpireAt: string;
}

interface ListResponse {
  response: { code: number; message: string };
  batchJobs: BatchJob[];
  pageMeta?: PageMeta;
}

interface CreateResponse {
  response: { code: number; message: string };
  batchJob: BatchJob;
}

const PAGE_SIZE = 20;
const WINDOWS = ['1h', '6h', '24h', '3d', '7d', '14d'];

function canCancel(status: string): boolean {
  return status === 'validating' || status === 'in_progress';
}

export default function UserBatchPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [jobs, setJobs] = useState<BatchJob[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [builderOpen, setBuilderOpen] = useState(true);
  const [busyId, setBusyId] = useState('');
  const [cancelTarget, setCancelTarget] = useState<BatchJob | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams();
      params.set('page.offset', String(offset));
      params.set('page.limit', String(PAGE_SIZE));
      const data = await api.get<ListResponse>(`/api/v1/batch?${params.toString()}`, orgId);
      setJobs(data.batchJobs || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('batch.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, offset, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const cancelJob = async (job: BatchJob) => {
    setBusyId(job.batchId);
    try {
      await api.post(`/api/v1/batch/${job.batchId}/cancel`, orgId, {});
      setCancelTarget(null);
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('batch.cancelFailed'));
    } finally {
      setBusyId('');
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('batch.title')}</h1>
          <div className="subtitle">{t('batch.userSubtitle')}</div>
        </div>
        <div className="header-actions">
          <button className="secondary" data-testid="batch-refresh" onClick={() => void load()}>
            {t('common.refresh')}
          </button>
          <button data-testid="batch-new" onClick={() => setBuilderOpen(true)}>
            {t('batch.new')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      {builderOpen && (
        <BatchBuilder
          orgId={orgId}
          onClose={() => setBuilderOpen(false)}
          onCreated={(job) => {
            setBuilderOpen(false);
            navigate(`/batch/${job.batchId}`);
          }}
        />
      )}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : jobs.length === 0 ? (
          <div className="empty-state" data-testid="batch-empty">
            {t('batch.userEmpty')}
          </div>
        ) : (
          <table className="data" data-testid="batch-table">
            <thead>
              <tr>
                <th>{t('batch.colBatchId')}</th>
                <th>{t('batch.colModel')}</th>
                <th>{t('common.status')}</th>
                <th>{t('batch.colProgress')}</th>
                <th>{t('batch.colRequests')}</th>
                <th>{t('batch.colCost')}</th>
                <th>{t('common.created')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {jobs.map((job) => (
                <tr key={job.batchId} data-testid={`batch-row-${job.batchId}`}>
                  <td className="mono">
                    <a
                      href={`/batch/${job.batchId}`}
                      onClick={(e) => {
                        e.preventDefault();
                        navigate(`/batch/${job.batchId}`);
                      }}
                    >
                      {job.batchId.slice(0, 8)}
                    </a>
                  </td>
                  <td>{job.model}</td>
                  <td>
                    <StateBadge state={job.status} />
                  </td>
                  <td>
                    {job.processedRequests}/{job.totalRequests}
                  </td>
                  <td>
                    {job.succeededRequests} / {job.failedRequests}
                  </td>
                  <td>
                    {job.cost} {job.currency}
                  </td>
                  <td>{formatTime(job.createdAt)}</td>
                  <td>
                    <button
                      className="link"
                      data-testid={`batch-view-${job.batchId}`}
                      onClick={() => navigate(`/batch/${job.batchId}`)}
                    >
                      {t('common.view')}
                    </button>
                    <button
                      className="link danger"
                      disabled={!canCancel(job.status) || busyId === job.batchId}
                      data-testid={`batch-cancel-${job.batchId}`}
                      onClick={() => setCancelTarget(job)}
                    >
                      {t('common.cancel')}
                    </button>
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

      {cancelTarget && (
        <Dialog
          title={t('batch.cancelTitle')}
          testId="batch-cancel-dialog"
          onClose={() => setCancelTarget(null)}
        >
          <p>{t('batch.cancelConfirm')}</p>
          <div className="dialog-actions">
            <button className="danger" data-testid="batch-cancel-confirm" onClick={() => void cancelJob(cancelTarget)}>
              {t('batch.cancelJob')}
            </button>
            <button className="secondary" onClick={() => setCancelTarget(null)}>
              {t('batch.keepJob')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

function BatchBuilder({
  orgId,
  onClose,
  onCreated,
}: {
  orgId: string;
  onClose: () => void;
  onCreated: (job: BatchJob) => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const [fileName, setFileName] = useState('');
  const [fileContent, setFileContent] = useState<Uint8Array | null>(null);
  const [window, setWindow] = useState('24h');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const onFile = (file: File | undefined) => {
    if (!file) return;
    if (!file.name.endsWith('.jsonl')) {
      setError(t('batch.invalidFile'));
      return;
    }
    setFileName(file.name);
    void file.arrayBuffer().then((buf) => setFileContent(new Uint8Array(buf)));
  };

  const submit = async () => {
    if (!fileContent) {
      setError(t('batch.needFile'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const base64 = btoa(String.fromCharCode(...fileContent));
      const data = await api.post<CreateResponse>('/api/v1/batch', orgId, {
        input_file: base64,
        completion_window: window,
      });
      onCreated(data.batchJob);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('batch.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('batch.new')} testId="batch-builder" onClose={onClose}>
      <div className="form-field">
        <label>{t('batch.inputFile')}</label>
        <input
          type="file"
          accept=".jsonl"
          data-testid="batch-file-input"
          onChange={(e) => onFile(e.target.files?.[0])}
        />
        {fileName && <div className="hint">{fileName}</div>}
      </div>
      <div className="form-field">
        <label>{t('batch.completionWindow')}</label>
        <select
          value={window}
          data-testid="batch-window-select"
          onChange={(e) => setWindow(e.target.value)}
        >
          {WINDOWS.map((w) => (
            <option key={w} value={w}>
              {w}
            </option>
          ))}
        </select>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button disabled={submitting} data-testid="batch-submit" onClick={() => void submit()}>
          {t('batch.submit')}
        </button>
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
      </div>
    </Dialog>
  );
}