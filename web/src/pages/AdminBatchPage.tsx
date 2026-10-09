// Admin Batch page (feature #42): view all batch jobs across tenants,
// monitor batch load, and cancel stuck or abusive jobs. Admin surface:
// route /admin/batch, API /api/v1/admin/batch/*. Content is masked (D5).

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { navigate } from '../router';
import { formatTime, type PageMeta } from '../api';
import { Dialog, ErrorBanner, Pagination, StateBadge } from '../components';
import type { BatchJob } from './user/UserBatchPage';

interface ListResponse {
  response: { code: number; message: string };
  batchJobs: BatchJob[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

function canCancel(status: string): boolean {
  return status === 'validating' || status === 'in_progress';
}

// The gateway serializes the proto enum name (e.g.
// BATCH_JOB_STATUS_IN_PROGRESS); normalize to the short form the UI
// compares against so the cancel action enables correctly.
function shortStatus(status: string): string {
  return status.replace(/^BATCH_JOB_STATUS_/, '').toLowerCase();
}

export default function AdminBatchPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [jobs, setJobs] = useState<BatchJob[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [busyId, setBusyId] = useState('');
  const [cancelTarget, setCancelTarget] = useState<BatchJob | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams();
      params.set('page.offset', String(offset));
      params.set('page.limit', String(PAGE_SIZE));
      const data = await api.get<ListResponse>(`/api/v1/admin/batch?${params.toString()}`, orgId);
      setJobs((data.batchJobs || []).map((j) => ({ ...j, status: shortStatus(j.status) })));
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
      await api.post(`/api/v1/admin/batch/${job.batchId}/cancel`, orgId, {});
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
          <div className="subtitle">{t('batch.adminSubtitle')}</div>
        </div>
        <div className="header-actions">
          <button className="secondary" data-testid="admin-batch-refresh" onClick={() => void load()}>
            {t('common.refresh')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : jobs.length === 0 ? (
          <div className="empty-state" data-testid="admin-batch-empty">
            {t('batch.adminEmpty')}
          </div>
        ) : (
          <table className="data" data-testid="admin-batch-table">
            <thead>
              <tr>
                <th>{t('batch.colBatchId')}</th>
                <th>{t('batch.colOrganization')}</th>
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
                <tr key={job.batchId} data-testid={`admin-batch-row-${job.batchId}`}>
                  <td className="mono">
                    <a
                      href={`/admin/batch/${job.batchId}`}
                      onClick={(e) => {
                        e.preventDefault();
                        navigate(`/admin/batch/${job.batchId}`);
                      }}
                    >
                      {job.batchId.slice(0, 8)}
                    </a>
                  </td>
                  <td>{job.organizationId}</td>
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
                      data-testid={`admin-batch-view-${job.batchId}`}
                      onClick={() => navigate(`/admin/batch/${job.batchId}`)}
                    >
                      {t('common.view')}
                    </button>
                    <button
                      className="link danger"
                      disabled={!canCancel(job.status) || busyId === job.batchId}
                      data-testid={`admin-batch-cancel-${job.batchId}`}
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
          testId="admin-batch-cancel-dialog"
          onClose={() => setCancelTarget(null)}
        >
          <p>{t('batch.cancelConfirm')}</p>
          <div className="dialog-actions">
            <button className="danger" data-testid="admin-batch-cancel-confirm" onClick={() => void cancelJob(cancelTarget)}>
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