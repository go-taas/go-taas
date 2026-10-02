// Fine-tuning job detail page (admin): the status, hyperparameters, and
// deploy card for a fine-tuning job.
// Implements docs/design/model-finetuning.md FR3 and
// docs/architecture/model-finetuning.md §6.5.

import { useCallback, useEffect, useState } from 'react';
import { api, type FineTuningJob } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { BackLink, ErrorBanner, StateBadge, usePolling } from '../components';
import { navigate } from '../router';

export default function FineTuningJobDetailPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const jobId = window.location.pathname.split('/')[3] || '';
  const [job, setJob] = useState<FineTuningJob | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  // Deploy dialog.
  const [deployOpen, setDeployOpen] = useState(false);
  const [imageId, setImageId] = useState('');
  const [accelerator, setAccelerator] = useState('nvidia');
  const [replicas, setReplicas] = useState('1');
  const [deployedService, setDeployedService] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.get<{ job?: FineTuningJob }>(`/api/v1/admin/finetuning/jobs/${jobId}`, orgId);
      setJob(data.job || null);
      setError('');
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('finetuning.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [jobId, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // Poll while the job is pending/running.
  usePolling(() => void load(), 3000, !!job && (job.summary.state === 'pending' || job.summary.state === 'running'));

  const deploy = async () => {
    try {
      const data = await api.post<{ serviceId: string }>(`/api/v1/admin/finetuning/jobs/${jobId}:deploy`, orgId, {
        imageId,
        accelerator,
        replicas: parseInt(replicas, 10),
      });
      setDeployedService(data.serviceId);
      setDeployOpen(false);
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('finetuning.deployFailed'));
    }
  };

  const summary = job?.summary;
  const hp = job?.hyperparameters;
  const isSucceeded = summary?.state === 'succeeded';
  const alreadyDeployed = !!summary?.fineTunedModelId;

  return (
    <div>
      <BackLink to="/admin/finetuning" label={t('finetuning.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="finetuning-job-title">{t('finetuning.jobDetail')}</h1>
          <div className="subtitle mono">{jobId}</div>
        </div>
        <button className="secondary" data-testid="finetuning-refresh" onClick={() => void load()} disabled={loading}>
          {t('finetuning.refresh')}
        </button>
      </div>

      {error && (
        <div>
          <ErrorBanner message={error} />
          {stale && <div className="muted">{t('finetuning.stale')}</div>}
          <button className="secondary" data-testid="finetuning-retry" onClick={() => void load()}>
            {t('finetuning.retry')}
          </button>
        </div>
      )}

      {!loading && !error && !job && (
        <div className="panel" data-testid="finetuning-job-empty">
          <p>{t('finetuning.noJobData')}</p>
          <p className="muted">{t('finetuning.noJobHint')}</p>
        </div>
      )}

      {summary && (
        <>
          <div className="panel" data-testid="finetuning-status">
            <h3 style={{ marginTop: 0 }}>{t('finetuning.status')}</h3>
            <div className="detail-grid">
              <div className="detail-item">
                <div className="label">{t('finetuning.jobName')}</div>
                <div className="value">{summary.name}</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('finetuning.jobState')}</div>
                <div className="value">
                  <StateBadge state={summary.state} />
                </div>
              </div>
              <div className="detail-item">
                <div className="label">{t('finetuning.jobBaseModel')}</div>
                <div className="value mono">
                  {summary.baseModelId} @ {summary.baseModelVersion}
                </div>
              </div>
              <div className="detail-item">
                <div className="label">{t('finetuning.jobDataset')}</div>
                <div className="value mono">{summary.datasetId}</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('finetuning.jobCreated')}</div>
                <div className="value">{new Date(parseInt(summary.createdAt, 10) * 1000).toLocaleString()}</div>
              </div>
              <div className="detail-item">
                <div className="label">{t('finetuning.jobUpdated')}</div>
                <div className="value">{new Date(parseInt(summary.updatedAt, 10) * 1000).toLocaleString()}</div>
              </div>
              {summary.state === 'failed' && job?.failureReason && (
                <div className="detail-item">
                  <div className="label">{t('finetuning.failureReason')}</div>
                  <div className="value">{job.failureReason}</div>
                </div>
              )}
            </div>
          </div>

          {hp && (
            <div className="panel" data-testid="finetuning-hyperparameters">
              <h3 style={{ marginTop: 0 }}>{t('finetuning.hyperparameters')}</h3>
              <div className="detail-grid">
                <div className="detail-item">
                  <div className="label">{t('finetuning.epochs')}</div>
                  <div className="value">{hp.epochs}</div>
                </div>
                <div className="detail-item">
                  <div className="label">{t('finetuning.batchSize')}</div>
                  <div className="value">{hp.batchSize}</div>
                </div>
                <div className="detail-item">
                  <div className="label">{t('finetuning.learningRate')}</div>
                  <div className="value">{hp.learningRate}</div>
                </div>
              </div>
            </div>
          )}

          <div className="panel" data-testid="finetuning-deploy">
            <h3 style={{ marginTop: 0 }}>{t('finetuning.deploy')}</h3>
            {alreadyDeployed ? (
              <p className="muted">
                {t('finetuning.alreadyDeployed')}{' '}
                <a
                  href={`/admin/inference-services/${summary.fineTunedModelId}`}
                  onClick={(e) => {
                    e.preventDefault();
                    navigate(`/admin/inference-services/${summary.fineTunedModelId}`);
                  }}
                >
                  {summary.fineTunedModelId}
                </a>
              </p>
            ) : isSucceeded ? (
              <button className="primary" data-testid="finetuning-deploy-action" onClick={() => setDeployOpen(true)}>
                {t('finetuning.deploy')}
              </button>
            ) : (
              <p className="muted" data-testid="finetuning-deploy-disabled">
                {t('finetuning.deployDisabled')}
              </p>
            )}
          </div>

          {deployedService && (
            <div className="panel" data-testid="finetuning-deployed">
              <p>
                {t('finetuning.deployedService')}{' '}
                <a
                  href={`/admin/inference-services/${deployedService}`}
                  onClick={(e) => {
                    e.preventDefault();
                    navigate(`/admin/inference-services/${deployedService}`);
                  }}
                >
                  {deployedService}
                </a>
              </p>
            </div>
          )}
        </>
      )}

      {deployOpen && (
        <div className="dialog-backdrop">
          <div className="dialog" data-testid="finetuning-deploy-dialog">
            <h3>{t('finetuning.deploy')}</h3>
            <label>
              {t('finetuning.imageId')}
              <input data-testid="finetuning-deploy-image" value={imageId} onChange={(e) => setImageId(e.target.value)} />
            </label>
            <label>
              {t('finetuning.accelerator')}
              <select data-testid="finetuning-deploy-accelerator" value={accelerator} onChange={(e) => setAccelerator(e.target.value)}>
                <option value="nvidia">nvidia</option>
                <option value="iluvatar">iluvatar</option>
                <option value="metax">metax</option>
              </select>
            </label>
            <label>
              {t('finetuning.replicas')}
              <input data-testid="finetuning-deploy-replicas" type="number" value={replicas} onChange={(e) => setReplicas(e.target.value)} />
            </label>
            <div className="dialog-actions">
              <button className="secondary" onClick={() => setDeployOpen(false)}>
                {t('common.cancel')}
              </button>
              <button className="primary" data-testid="finetuning-deploy-submit" onClick={() => void deploy()}>
                {t('finetuning.deploy')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}