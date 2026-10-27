// Model downloads page: import a model from a public model hub
// (ModelScope / HuggingFace) into the shared weights filesystem. The
// download runs as a background job on the server, so the page starts it
// and polls the job list for progress and the outcome.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { navigate } from '../router';
import { api, ApiError, formatTime, type ModelDownload, type PageMeta } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { ErrorBanner, Pagination } from '../components';

interface ListResponse {
  response: { code: number; message: string };
  downloads: ModelDownload[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

// ACTIVE_STATES are the job states that still change on their own, so
// the page keeps polling while any of them is present.
const ACTIVE_STATES = ['queued', 'downloading'];

// HUBS are the model hubs the platform can download from.
const HUBS = [
  { value: 'modelscope', label: 'ModelScope', hint: 'models.downloads.hubModelScopeHint' },
  { value: 'huggingface', label: 'HuggingFace', hint: 'models.downloads.hubHuggingFaceHint' },
];

export default function ModelDownloadsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [downloads, setDownloads] = useState<ModelDownload[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const [source, setSource] = useState('modelscope');
  const [hubModelId, setHubModelId] = useState('');
  const [name, setName] = useState('');
  const [version, setVersion] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState('');
  const pollRef = useRef<number | null>(null);

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true);
      try {
        const data = await api.get<ListResponse>(
          `/api/v1/admin/model-downloads?page.offset=${offset}&page.limit=${PAGE_SIZE}`,
          orgId,
        );
        setDownloads(data.downloads || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
        setError('');
      } catch (e) {
        setError(e instanceof Error ? e.message : t('models.downloads.loadFailed'));
      } finally {
        if (!quiet) setLoading(false);
      }
    },
    [orgId, offset, t],
  );

  useEffect(() => {
    void load();
  }, [load]);

  // Poll while a job is still running so the progress bar advances. The
  // interval is cleared as soon as every job reached a terminal state.
  const hasActive = useMemo(
    () => downloads.some((d) => ACTIVE_STATES.includes(d.state)),
    [downloads],
  );
  useEffect(() => {
    if (!hasActive) return undefined;
    pollRef.current = window.setInterval(() => void load(true), 2000);
    return () => {
      if (pollRef.current !== null) window.clearInterval(pollRef.current);
      pollRef.current = null;
    };
  }, [hasActive, load]);

  const submit = async () => {
    const id = hubModelId.trim();
    if (!/^[^/\s]+\/[^/\s]+$/.test(id)) {
      setFormError(t('models.downloads.validationHubModelId'));
      return;
    }
    setSubmitting(true);
    setFormError('');
    try {
      await api.post('/api/v1/admin/model-downloads', orgId, {
        source,
        hubModelId: id,
        name: name.trim(),
        version: version.trim(),
        description: description.trim(),
      });
      setHubModelId('');
      setName('');
      setVersion('');
      setDescription('');
      setOffset(0);
      await load();
    } catch (e) {
      setFormError(
        e instanceof ApiError ? `${e.message} (code ${e.code})` : t('models.downloads.startFailed'),
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('models.downloads.title')}</h1>
          <div className="subtitle">{t('models.downloads.subtitle')}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel" style={{ marginBottom: 16 }}>
        <h2>{t('models.downloads.formTitle')}</h2>
        <div className="form-grid">
          <div className="form-field">
            <label htmlFor="download-source">{t('models.downloads.hub')}</label>
            <select
              id="download-source"
              data-testid="download-source"
              value={source}
              onChange={(e) => setSource(e.target.value)}
            >
              {HUBS.map((h) => (
                <option key={h.value} value={h.value}>
                  {h.label}
                </option>
              ))}
            </select>
            <div className="field-hint">
              {t(HUBS.find((h) => h.value === source)?.hint || 'models.downloads.hubModelScopeHint')}
            </div>
          </div>
          <div className="form-field">
            <label htmlFor="download-hub-model-id">{t('models.downloads.hubModelId')}</label>
            <input
              id="download-hub-model-id"
              data-testid="download-hub-model-id"
              value={hubModelId}
              onChange={(e) => setHubModelId(e.target.value)}
              placeholder="MiniMax/MiniMax-M3.1-Flash"
            />
            <div className="field-hint">{t('models.downloads.hubModelIdHint')}</div>
          </div>
          <div className="form-field">
            <label htmlFor="download-name">{t('models.downloads.modelName')}</label>
            <input
              id="download-name"
              data-testid="download-name"
              value={name}
              maxLength={128}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('models.downloads.modelNamePlaceholder')}
            />
          </div>
          <div className="form-field">
            <label htmlFor="download-version">{t('models.downloads.modelVersion')}</label>
            <input
              id="download-version"
              data-testid="download-version"
              value={version}
              maxLength={64}
              onChange={(e) => setVersion(e.target.value)}
              placeholder={t('models.downloads.modelVersionPlaceholder')}
            />
          </div>
          <div className="form-field full">
            <label htmlFor="download-description">{t('common.descriptionOptional')}</label>
            <textarea
              id="download-description"
              data-testid="download-description"
              rows={2}
              maxLength={1024}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
        </div>
        {formError && <ErrorBanner message={formError} />}
        <div className="dialog-actions">
          <button data-testid="start-download" disabled={submitting} onClick={() => void submit()}>
            {submitting ? t('models.downloads.starting') : t('models.downloads.start')}
          </button>
        </div>
      </div>

      <div className="panel">
        <h2>{t('models.downloads.jobsTitle')}</h2>
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : downloads.length === 0 ? (
          <div className="empty-state" data-testid="downloads-empty">
            {t('models.downloads.empty')}
          </div>
        ) : (
          <div className="table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th>{t('models.downloads.colHubModelId')}</th>
                <th>{t('models.downloads.colTarget')}</th>
                <th>{t('models.downloads.colState')}</th>
                <th>{t('models.downloads.colProgress')}</th>
                <th>{t('models.downloads.colCreated')}</th>
              </tr>
            </thead>
            <tbody>
              {downloads.map((d) => (
                <tr key={d.downloadId} data-testid={`download-row-${d.hubModelId}`}>
                  <td>
                    <strong className="mono">{d.hubModelId}</strong>
                    <div className="muted small">{d.source}</div>
                  </td>
                  <td className="mono">
                    {d.name}
                    <div className="muted small">{d.version}</div>
                  </td>
                  <td className="download-state">
                    <span className={`badge ${stateBadgeClass(d.state)}`}>
                      {t(`models.downloads.state.${d.state}`)}
                    </span>
                    {d.message && <div className="muted small">{d.message}</div>}
                    {d.modelId && (
                      <button
                        className="link"
                        onClick={() => navigate(`/admin/models/${d.modelId}`)}
                      >
                        {t('models.downloads.viewModel')}
                      </button>
                    )}
                  </td>
                  <td style={{ minWidth: 180 }}>
                    <div className="muted small" data-testid={`download-bytes-${d.downloadId}`}>
                      {progressLabel(d)}
                    </div>
                    {d.state === 'downloading' && <DownloadProgress download={d} />}
                  </td>
                  <td>{formatTime(d.createdAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
        {total > PAGE_SIZE && (
          <Pagination
            offset={offset}
            limit={PAGE_SIZE}
            total={total}
            onPageChange={setOffset}
          />
        )}
      </div>
    </div>
  );
}

// asNumber normalizes an int64 proto field, which arrives as a JSON
// string.
function asNumber(input: string | number | undefined): number {
  if (typeof input === 'number') return input;
  return parseInt(input || '0', 10) || 0;
}

// progressLabel renders "12.3 MB / 940.1 MB (1%)" while the total is
// known, and the bare byte count otherwise.
function progressLabel(d: ModelDownload): string {
  const done = formatBytes(d.bytes);
  const total = asNumber(d.totalBytes);
  if (total <= 0) return done;
  return `${done} / ${formatBytes(total)} (${downloadPercent(d)}%)`;
}

// downloadPercent converts the sampled byte count into a whole percent,
// clamped to 100 because a partially written file can overshoot the hub's
// advertised size.
function downloadPercent(d: ModelDownload): number {
  const total = asNumber(d.totalBytes);
  if (total <= 0) return 0;
  const done = asNumber(d.bytes);
  const pct = Math.round((done / total) * 100);
  return pct < 0 ? 0 : pct > 100 ? 100 : pct;
}

// DownloadProgress draws a determinate bar once the hub reported a size.
// Without a size there is nothing meaningful to measure, so it keeps the
// indeterminate animation rather than showing a bar stuck at zero.
function DownloadProgress({ download }: { download: ModelDownload }) {
  const total = asNumber(download.totalBytes);
  if (total <= 0) {
    return (
      <div className="progress-track">
        <div className="progress-bar indeterminate" data-testid="download-progress-indeterminate" />
      </div>
    );
  }
  const pct = downloadPercent(download);
  return (
    <div
      className="progress-track"
      role="progressbar"
      aria-valuenow={pct}
      aria-valuemin={0}
      aria-valuemax={100}
      data-testid={`download-progress-${download.downloadId}`}
      data-percent={pct}
    >
      <div className="progress-bar" style={{ width: `${pct}%` }} />
    </div>
  );
}

// stateBadgeClass maps a job state to its badge modifier.
function stateBadgeClass(state: string): string {
  switch (state) {
    case 'succeeded':
      return 'active';
    case 'failed':
      return 'error';
    default:
      return 'info';
  }
}

// formatBytes renders a byte count with a binary unit. The int64 proto
// field arrives as a JSON string, so both forms are accepted.
export function formatBytes(input: number | string): string {
  const bytes = typeof input === 'number' ? input : parseInt(input || '0', 10) || 0;
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}
