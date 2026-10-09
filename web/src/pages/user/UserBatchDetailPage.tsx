// End-user Batch Detail page (feature #42): view a single batch job's
// progress, per-request stats, and download its result/error files.
// End-user surface: route /batch/:batchId, API /api/v1/batch/{batch_id}/*.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { useParams, navigate } from '../../router';
import { formatTime } from '../../api';
import { Dialog, ErrorBanner, StateBadge } from '../../components';
import type { BatchJob } from './UserBatchPage';

interface GetResponse {
  response: { code: number; message: string };
  batchJob: BatchJob;
}

interface DownloadResponse {
  response: { code: number; message: string };
  content: string;
  filename: string;
}

function canCancel(status: string): boolean {
  return status === 'validating' || status === 'in_progress';
}

// The gateway serializes the proto enum name (e.g.
// BATCH_JOB_STATUS_COMPLETED); normalize to the short form the UI
// compares against so download/cancel enable correctly.
function shortStatus(status: string): string {
  return status.replace(/^BATCH_JOB_STATUS_/, '').toLowerCase();
}

function isTerminal(status: string): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled';
}

function download(content: string, filename: string) {
  const bytes = Uint8Array.from(atob(content), (c) => c.charCodeAt(0));
  const blob = new Blob([bytes], { type: 'application/x-ndjson' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

export default function UserBatchDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const { batchId } = useParams();
  const [job, setJob] = useState<BatchJob | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [cancelOpen, setCancelOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<GetResponse>(`/api/v1/batch/${batchId}`, orgId);
      setJob({ ...data.batchJob, status: shortStatus(data.batchJob.status) });
    } catch (e) {
      setError(e instanceof Error ? e.message : t('batch.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, batchId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // The design polls GetBatchJob until the status is terminal so the
  // progress bar and the download/cancel actions track the worker.
  useEffect(() => {
    if (!job || isTerminal(job.status)) return;
    const timer = setInterval(() => void load(), 5000);
    return () => clearInterval(timer);
  }, [job, load]);

  const cancelJob = async () => {
    setBusy(true);
    try {
      await api.post(`/api/v1/batch/${batchId}/cancel`, orgId, {});
      setCancelOpen(false);
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('batch.cancelFailed'));
    } finally {
      setBusy(false);
    }
  };

  const downloadFile = async (kind: 'result' | 'error') => {
    try {
      const data = await api.get<DownloadResponse>(`/api/v1/batch/${batchId}/${kind}`, orgId);
      if (data.content) download(data.content, data.filename || `batch-${batchId}-${kind}.jsonl`);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('batch.downloadFailed'));
    }
  };

  if (loading) {
    return <div className="loading">{t('common.loading')}</div>;
  }

  if (!job) {
    return (
      <div>
        <div className="page-header">
          <div>
            <h1>{t('batch.detailTitle')}</h1>
          </div>
        </div>
        {error && <ErrorBanner message={error} />}
      </div>
    );
  }

  const total = parseInt(job.totalRequests || '0', 10) || 0;
  const processed = parseInt(job.processedRequests || '0', 10) || 0;
  const pct = total > 0 ? Math.round((processed / total) * 100) : 0;

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('batch.detailTitle')}</h1>
          <div className="subtitle mono">{job.batchId}</div>
        </div>
        <div className="header-actions">
          <button className="secondary" onClick={() => navigate('/batch')}>
            {t('batch.backToBatch')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel" data-testid="batch-detail-summary">
        <h3>{t('batch.summary')}</h3>
        <div className="kv-grid">
          <div>
            <span className="kv-label">{t('common.status')}</span>
            <StateBadge state={job.status} />
          </div>
          <div>
            <span className="kv-label">{t('batch.colModel')}</span>
            <span>{job.model}</span>
          </div>
          <div>
            <span className="kv-label">{t('batch.completionWindow')}</span>
            <span>{job.completionWindow}</span>
          </div>
          <div>
            <span className="kv-label">{t('common.created')}</span>
            <span>{formatTime(job.createdAt)}</span>
          </div>
          <div>
            <span className="kv-label">{t('batch.completedAt')}</span>
            <span>{formatTime(job.completedAt)}</span>
          </div>
          <div>
            <span className="kv-label">{t('batch.filesExpireAt')}</span>
            <span>{formatTime(job.filesExpireAt)}</span>
          </div>
        </div>
      </div>

      <div className="panel" data-testid="batch-detail-progress">
        <h3>{t('batch.progress')}</h3>
        <div className="progress-bar">
          <div className="progress-fill" style={{ width: `${pct}%` }} />
        </div>
        <div className="hint">
          {processed} / {total} · {t('batch.succeeded')}: {job.succeededRequests} ·{' '}
          {t('batch.failed')}: {job.failedRequests}
        </div>
      </div>

      <div className="panel" data-testid="batch-detail-billing">
        <h3>{t('batch.billing')}</h3>
        <div className="kv-grid">
          <div>
            <span className="kv-label">{t('batch.inputTokens')}</span>
            <span>{job.inputTokens}</span>
          </div>
          <div>
            <span className="kv-label">{t('batch.outputTokens')}</span>
            <span>{job.outputTokens}</span>
          </div>
          <div>
            <span className="kv-label">{t('batch.colCost')}</span>
            <span>
              {job.cost} {job.currency}
            </span>
          </div>
        </div>
        <div className="hint">{t('batch.discountNote')}</div>
      </div>

      <div className="panel">
        <h3>{t('common.actions')}</h3>
        <div className="dialog-actions">
          <button
            className="secondary"
            disabled={job.status !== 'completed'}
            data-testid="batch-download-result"
            onClick={() => void downloadFile('result')}
          >
            {t('batch.downloadResult')}
          </button>
          <button
            className="secondary"
            disabled={job.status !== 'completed' || parseInt(job.failedRequests || '0', 10) === 0}
            data-testid="batch-download-error"
            onClick={() => void downloadFile('error')}
          >
            {t('batch.downloadError')}
          </button>
          <button
            className="danger"
            disabled={!canCancel(job.status) || busy}
            data-testid="batch-cancel"
            onClick={() => setCancelOpen(true)}
          >
            {t('common.cancel')}
          </button>
        </div>
      </div>

      {cancelOpen && (
        <Dialog title={t('batch.cancelTitle')} testId="batch-cancel-dialog" onClose={() => setCancelOpen(false)}>
          <p>{t('batch.cancelConfirm')}</p>
          <div className="dialog-actions">
            <button className="danger" data-testid="batch-cancel-confirm" onClick={() => void cancelJob()}>
              {t('batch.cancelJob')}
            </button>
            <button className="secondary" onClick={() => setCancelOpen(false)}>
              {t('batch.keepJob')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}