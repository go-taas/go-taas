// Fine-tuning page (admin): the dataset registry and the fine-tuning job
// list, with New job and Register dataset actions.
// Implements docs/design/model-finetuning.md FR1-FR3 and
// docs/architecture/model-finetuning.md §6.5.

import { useCallback, useEffect, useState } from 'react';
import { api, type FineTuningDataset, type FineTuningJobSummary } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { BackLink, ErrorBanner, StateBadge } from '../components';
import { navigate } from '../router';

const JOB_STATES = ['', 'pending', 'running', 'succeeded', 'failed'];

export default function FineTuningPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [datasets, setDatasets] = useState<FineTuningDataset[]>([]);
  const [jobs, setJobs] = useState<FineTuningJobSummary[]>([]);
  const [stateFilter, setStateFilter] = useState('');
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  // Register dataset dialog.
  const [regOpen, setRegOpen] = useState(false);
  const [regName, setRegName] = useState('');
  const [regFormat, setRegFormat] = useState('jsonl');
  const [regPath, setRegPath] = useState('');

  // New job dialog.
  const [jobOpen, setJobOpen] = useState(false);
  const [jobName, setJobName] = useState('');
  const [jobBaseModel, setJobBaseModel] = useState('');
  const [jobBaseVersion, setJobBaseVersion] = useState('');
  const [jobDataset, setJobDataset] = useState('');
  const [jobEpochs, setJobEpochs] = useState('3');
  const [jobBatch, setJobBatch] = useState('4');
  const [jobLR, setJobLR] = useState('0.001');

  const load = useCallback(async () => {
    try {
      const [ds, jb] = await Promise.all([
        api.get<{ datasets?: FineTuningDataset[] }>('/api/v1/admin/finetuning/datasets', orgId),
        api.get<{ jobs?: FineTuningJobSummary[] }>('/api/v1/admin/finetuning/jobs', orgId),
      ]);
      setDatasets(ds.datasets || []);
      setJobs(jb.jobs || []);
      setError('');
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('finetuning.loadFailed'));
      setStale(true);
    }
  }, [orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const registerDataset = async () => {
    try {
      await api.post<{ datasetId: string }>('/api/v1/admin/finetuning/datasets', orgId, {
        name: regName,
        format: regFormat,
        objectPath: regPath,
      });
      setRegOpen(false);
      setRegName('');
      setRegPath('');
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('finetuning.registerFailed'));
    }
  };

  const createJob = async () => {
    try {
      await api.post<{ jobId: string }>('/api/v1/admin/finetuning/jobs', orgId, {
        name: jobName,
        baseModelId: jobBaseModel,
        baseModelVersion: jobBaseVersion,
        datasetId: jobDataset,
        hyperparameters: {
          epochs: parseInt(jobEpochs, 10),
          batchSize: parseInt(jobBatch, 10),
          learningRate: parseFloat(jobLR),
        },
      });
      setJobOpen(false);
      setJobName('');
      setJobBaseModel('');
      setJobBaseVersion('');
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('finetuning.createFailed'));
    }
  };

  const filteredJobs = stateFilter ? jobs.filter((j) => j.state === stateFilter) : jobs;

  return (
    <div>
      <BackLink to="/admin/models" label={t('finetuning.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="finetuning-title">{t('finetuning.title')}</h1>
          <div className="subtitle">{t('finetuning.subtitle')}</div>
        </div>
        <button className="secondary" data-testid="finetuning-register-dataset" onClick={() => setRegOpen(true)}>
          {t('finetuning.registerDataset')}
        </button>
        <button className="primary" data-testid="finetuning-new-job" onClick={() => setJobOpen(true)}>
          {t('finetuning.newJob')}
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

      <div className="panel" data-testid="finetuning-datasets">
        <h3 style={{ marginTop: 0 }}>{t('finetuning.datasets')}</h3>
        {datasets.length === 0 ? (
          <p className="muted" data-testid="finetuning-no-datasets">
            {t('finetuning.noDatasets')}
          </p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>{t('finetuning.datasetName')}</th>
                <th>{t('finetuning.datasetFormat')}</th>
                <th>{t('finetuning.datasetPath')}</th>
                <th>{t('finetuning.datasetCreated')}</th>
              </tr>
            </thead>
            <tbody>
              {datasets.map((d) => (
                <tr key={d.datasetId} data-testid={`finetuning-dataset-${d.datasetId}`}>
                  <td>{d.name}</td>
                  <td>
                    <span className="badge">{d.format}</span>
                  </td>
                  <td className="mono">{d.objectPath}</td>
                  <td>{new Date(parseInt(d.createdAt, 10) * 1000).toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="panel" data-testid="finetuning-jobs">
        <h3 style={{ marginTop: 0 }}>{t('finetuning.jobs')}</h3>
        <label>
          {t('finetuning.stateFilter')}
          <select data-testid="finetuning-state-filter" value={stateFilter} onChange={(e) => setStateFilter(e.target.value)}>
            {JOB_STATES.map((s) => (
              <option key={s} value={s}>
                {s === '' ? t('finetuning.allStates') : s}
              </option>
            ))}
          </select>
        </label>
        {filteredJobs.length === 0 ? (
          <p className="muted" data-testid="finetuning-no-jobs">
            {t('finetuning.noJobs')}
          </p>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>{t('finetuning.jobName')}</th>
                <th>{t('finetuning.jobBaseModel')}</th>
                <th>{t('finetuning.jobState')}</th>
                <th>{t('finetuning.jobFineTuned')}</th>
                <th>{t('finetuning.jobCreated')}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {filteredJobs.map((j) => (
                <tr key={j.jobId} data-testid={`finetuning-job-${j.jobId}`}>
                  <td>
                    <a
                      href={`/admin/finetuning/${j.jobId}`}
                      onClick={(e) => {
                        e.preventDefault();
                        navigate(`/admin/finetuning/${j.jobId}`);
                      }}
                    >
                      {j.name}
                    </a>
                  </td>
                  <td className="mono">
                    {j.baseModelId.slice(0, 8)}… @ {j.baseModelVersion}
                  </td>
                  <td>
                    <StateBadge state={j.state} />
                  </td>
                  <td className="mono">{j.fineTunedModelId ? j.fineTunedModelId.slice(0, 8) : '—'}</td>
                  <td>{new Date(parseInt(j.createdAt, 10) * 1000).toLocaleString()}</td>
                  <td>
                    <button
                      className="secondary"
                      data-testid={`finetuning-view-${j.jobId}`}
                      onClick={() => navigate(`/admin/finetuning/${j.jobId}`)}
                    >
                      {t('finetuning.view')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {regOpen && (
        <div className="dialog-backdrop">
          <div className="dialog" data-testid="finetuning-register-dialog">
            <h3>{t('finetuning.registerDataset')}</h3>
            <label>
              {t('finetuning.datasetName')}
              <input data-testid="finetuning-reg-name" value={regName} onChange={(e) => setRegName(e.target.value)} />
            </label>
            <label>
              {t('finetuning.datasetFormat')}
              <select data-testid="finetuning-reg-format" value={regFormat} onChange={(e) => setRegFormat(e.target.value)}>
                <option value="jsonl">jsonl</option>
                <option value="csv">csv</option>
              </select>
            </label>
            <label>
              {t('finetuning.datasetPath')}
              <input data-testid="finetuning-reg-path" value={regPath} onChange={(e) => setRegPath(e.target.value)} />
            </label>
            <div className="dialog-actions">
              <button className="secondary" onClick={() => setRegOpen(false)}>
                {t('common.cancel')}
              </button>
              <button className="primary" data-testid="finetuning-reg-submit" onClick={() => void registerDataset()}>
                {t('finetuning.register')}
              </button>
            </div>
          </div>
        </div>
      )}

      {jobOpen && (
        <div className="dialog-backdrop">
          <div className="dialog" data-testid="finetuning-job-dialog">
            <h3>{t('finetuning.newJob')}</h3>
            <label>
              {t('finetuning.jobName')}
              <input data-testid="finetuning-job-name" value={jobName} onChange={(e) => setJobName(e.target.value)} />
            </label>
            <label>
              {t('finetuning.jobBaseModel')}
              <input data-testid="finetuning-job-model" value={jobBaseModel} onChange={(e) => setJobBaseModel(e.target.value)} />
            </label>
            <label>
              {t('finetuning.jobBaseVersion')}
              <input data-testid="finetuning-job-version" value={jobBaseVersion} onChange={(e) => setJobBaseVersion(e.target.value)} />
            </label>
            <label>
              {t('finetuning.jobDataset')}
              <select data-testid="finetuning-job-dataset" value={jobDataset} onChange={(e) => setJobDataset(e.target.value)}>
                <option value="">{t('finetuning.selectDataset')}</option>
                {datasets.map((d) => (
                  <option key={d.datasetId} value={d.datasetId}>
                    {d.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t('finetuning.epochs')}
              <input data-testid="finetuning-job-epochs" type="number" value={jobEpochs} onChange={(e) => setJobEpochs(e.target.value)} />
            </label>
            <label>
              {t('finetuning.batchSize')}
              <input data-testid="finetuning-job-batch" type="number" value={jobBatch} onChange={(e) => setJobBatch(e.target.value)} />
            </label>
            <label>
              {t('finetuning.learningRate')}
              <input data-testid="finetuning-job-lr" type="number" step="0.0001" value={jobLR} onChange={(e) => setJobLR(e.target.value)} />
            </label>
            <div className="dialog-actions">
              <button className="secondary" onClick={() => setJobOpen(false)}>
                {t('common.cancel')}
              </button>
              <button className="primary" data-testid="finetuning-job-submit" onClick={() => void createJob()}>
                {t('finetuning.create')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}