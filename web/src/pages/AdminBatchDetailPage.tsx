// Admin Batch Detail page (feature #42): view a single batch job's
// metadata and aggregate stats (masked), and cancel it. Admin surface:
// route /admin/batch/:batchId, API /api/v1/admin/batch/{batch_id}/*. No
// result/error download (D5 — the admin surface masks per-request
// content).

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { useParams, navigate } from '../router';
import { formatTime } from '../api';
import { Dialog, ErrorBanner, StateBadge } from '../components';
import type { BatchJob } from './user/UserBatchPage';

interface GetResponse {
  response: { code: number; message: string };
  batchJob: BatchJob;
}

function canCancel(status: string): boolean {
  return status === 'validating' || status === 'in_progress';
}

export default function AdminBatchDetailPage() {
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
      const data = await api.get<GetResponse>(`/api/v1/admin/batch/${batchId}`, orgId);
      setJob(data.batchJob);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('batch.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, batchId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const cancelJob = async () => {
    setBusy(true);
    try {
      await api.post(`/api/v1/admin/batch/${batchId}/cancel`, orgId, {});
      setCancelOpen(false);
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('batch.cancelFailed'));
    } finally {
      setBusy(false);
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
          <button className="secondary" onClick={() => navigate('/admin/batch')}>
            {t('batch.backToBatch')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel" data-testid="admin-batch-detail-summary">
        <h3>{t('batch.summary')}</h3>
        <div className="kv-grid">
          <div>
            <span className="kv-label">{t('common.status')}</span>
            <StateBadge state={job.status} />
          </div>
          <div>
            <span className="kv-label">{t('batch.colOrganization')}</span>
            <span>{job.organizationId}</span>
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

      <div className="panel" data-testid="admin-batch-detail-progress">
        <h3>{t('batch.progress')}</h3>
        <div className="progress-bar">
          <div className="progress-fill" style={{ width: `${pct}%` }} />
        </div>
        <div className="hint">
          {processed} / {total} · {t('batch.succeeded')}: {job.succeededRequests} ·{' '}
          {t('batch.failed')}: {job.failedRequests}
        </div>
      </div>

      <div className="panel" data-testid="admin-batch-detail-billing">
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
      </div>

      <div className="panel">
        <h3>{t('common.actions')}</h3>
        <div className="dialog-actions">
          <button
            className="danger"
            disabled={!canCancel(job.status) || busy}
            data-testid="admin-batch-cancel"
            onClick={() => setCancelOpen(true)}
          >
            {t('common.cancel')}
          </button>
        </div>
      </div>

      {cancelOpen && (
        <Dialog title={t('batch.cancelTitle')} testId="admin-batch-cancel-dialog" onClose={() => setCancelOpen(false)}>
          <p>{t('batch.cancelConfirm')}</p>
          <div className="dialog-actions">
            <button className="danger" data-testid="admin-batch-cancel-confirm" onClick={() => void cancelJob()}>
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