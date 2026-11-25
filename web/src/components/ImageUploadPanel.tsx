// Image upload panel: push a `docker save` archive into the platform's own
// Harbor project.
//
// It is the upload twin of the import panel and of the model download/upload
// panels: a section that expands below the images toolbar, a form that folds
// away once a transfer starts, and a history table underneath.
//
// The archive travels as one multipart request, with the form fields first so
// the server can open a history row before the bytes arrive. Progress is not
// drawn inside the form: the server records received bytes on the row and the
// history table polls it, exactly how the model upload list reports progress.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { navigate } from '../router';
import {
  api,
  ApiError,
  apiPrefix,
  formatTime,
  sessionAuthHeader,
  type ImageRegistryConfig,
  type ImageUploadTask,
  type PageMeta,
} from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { ErrorBanner, Pagination, RowActionsMenu, StatusError } from '../components';
import { FieldLabel, FormLegend, FormSection, RequiredStatus } from './FormField';
import { CPU_ACCELERATOR, useFleetAccelerators } from '../hooks/useFleetAccelerators';
import { formatBytes } from './ModelDownloadsPanel';

// ACTIVE_STATES are the task states that still change on their own, so the
// panel keeps polling while any of them is present.
const ACTIVE_STATES = ['uploading', 'processing'];

// CANCELLABLE_STATES are the states an operator may still stop.
const CANCELLABLE_STATES = ['uploading', 'processing'];

// ARCHIVE_ACCEPT is what the file picker offers. The server decides the
// format from the extension and rejects anything else.
const ARCHIVE_ACCEPT = '.tar,.tar.gz,.tgz,application/x-tar,application/gzip';

// derivedName mirrors the server's rule: the image name comes from the
// archive (or the typed reference) and only its last path segment is kept.
function derivedName(reference: string): string {
  const ref = reference.trim();
  if (!ref) return '';
  const withoutTag = ref.includes(':') ? ref.slice(0, ref.lastIndexOf(':')) : ref;
  return withoutTag.split('/').filter(Boolean).pop() || '';
}

export default function ImageUploadPanel({
  defaultOpen = false,
  registry,
  active = true,
  onDone,
}: {
  defaultOpen?: boolean;
  registry: ImageRegistryConfig | null;
  // active is false while the page hides the panel; the list then stops
  // polling and loading until it is shown again.
  active?: boolean;
  onDone?: () => void;
}) {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const fleet = useFleetAccelerators(orgId);

  const [formOpen, setFormOpen] = useState(defaultOpen);
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState('');
  const [tag, setTag] = useState('');
  const [sourceReference, setSourceReference] = useState('');
  const [accelerator, setAccelerator] = useState('');
  const [engine, setEngine] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  // listRefresh bumps when a transfer starts or ends, so the history table
  // reloads and starts polling the new row at once.
  const [listRefresh, setListRefresh] = useState(0);
  const xhrRef = useRef<XMLHttpRequest | null>(null);

  useEffect(() => {
    if (!accelerator) setAccelerator(fleet.vendors[0] || CPU_ACCELERATOR);
  }, [accelerator, fleet.vendors]);

  // Stop an in-flight transfer when the panel goes away: nothing would consume
  // the response and the server would keep staging the archive.
  useEffect(
    () => () => {
      xhrRef.current?.abort();
    },
    [],
  );

  const preview = name.trim() || derivedName(sourceReference) || t('images.uploadNamePending');

  const submit = async () => {
    if (!file) {
      setError(t('images.validationArchive'));
      return;
    }
    if (!engine.trim()) {
      setError(t('images.validationEngine'));
      return;
    }
    // A started transfer folds the form away so the history table takes the
    // stage and shows the progress, exactly like the model upload panel.
    setFormOpen(false);
    setSubmitting(true);
    setError('');
    setListRefresh((n) => n + 1);

    const form = new FormData();
    // Metadata first: the server opens the history row (and its progress
    // denominator) before the archive starts streaming.
    form.append('name', name.trim());
    form.append('tag', tag.trim());
    form.append('sourceReference', sourceReference.trim());
    form.append('accelerator', accelerator);
    form.append('engine', engine.trim());
    form.append('description', description.trim());
    form.append('archiveName', file.name);
    form.append('totalBytes', String(file.size));
    form.append('file', file);

    const path = `${apiPrefix('admin')}/images:upload`;
    try {
      await new Promise<void>((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhrRef.current = xhr;
        xhr.open('POST', path);
        const authorization = sessionAuthHeader('admin');
        if (authorization) xhr.setRequestHeader('Authorization', authorization);
        xhr.onload = () => {
          let body: Record<string, unknown> = {};
          try {
            body = JSON.parse(xhr.responseText) as Record<string, unknown>;
          } catch {
            // A non-JSON response is a transport failure; reported below.
          }
          const envelope = (body.response ?? body) as { code?: number; message?: string };
          if (xhr.status >= 200 && xhr.status < 300 && (envelope.code ?? 0) === 0) {
            resolve();
            return;
          }
          reject(new ApiError(envelope.code ?? 0, envelope.message || t('images.uploadFailed')));
        };
        xhr.onerror = () => reject(new ApiError(0, t('images.uploadFailed')));
        xhr.onabort = () => reject(new ApiError(0, t('common.cancel')));
        xhr.send(form);
      });
      setFile(null);
      setName('');
      setTag('');
      setSourceReference('');
      setDescription('');
      onDone?.();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('images.uploadFailed'));
      // A submit that never started has nothing to show in the list, so bring
      // the form back with the reason instead of leaving only a banner.
      setFormOpen(true);
    } finally {
      xhrRef.current = null;
      setSubmitting(false);
      setListRefresh((n) => n + 1);
    }
  };

  return (
    <section data-testid="image-upload-panel">
      <div className="section-header">
        <div>
          <h2 style={{ margin: 0 }}>{t('images.uploadTitle')}</h2>
          <div className="subtitle">{t('images.uploadSubtitle')}</div>
        </div>
        <button
          className={formOpen ? 'secondary' : undefined}
          data-testid="toggle-upload-image-form"
          onClick={() => setFormOpen((open) => !open)}
          disabled={submitting}
        >
          {formOpen ? t('common.cancel') : t('images.uploadOpen')}
        </button>
      </div>

      {!formOpen && error && <ErrorBanner message={error} />}

      {formOpen && (
        <div className="panel" style={{ marginBottom: 16 }} data-testid="upload-image-form">
          <h2>{t('images.uploadFormTitle')}</h2>
          <p className="muted">{t('images.uploadHint')}</p>
          <FormLegend testId="upload-image-legend" />
          <FormSection
            step={1}
            title={t('images.uploadSectionArchive')}
            hint={t('images.uploadSectionArchiveHint')}
            requirement="required"
            testId="upload-image-section-archive"
          >
            <div className="form-grid">
              <div className="form-field full">
                <FieldLabel
                  htmlFor="upload-archive"
                  text={t('images.archiveFile')}
                  requirement="required"
                />
                <input
                  id="upload-archive"
                  data-testid="upload-archive-input"
                  type="file"
                  accept={ARCHIVE_ACCEPT}
                  disabled={submitting}
                  onChange={(e) => setFile(e.target.files?.[0] || null)}
                />
                <div className="field-hint">
                  {registry?.configured
                    ? t('images.uploadTarget', {
                        registry: `${registry.hostname}/${registry.project}/`,
                        name: preview,
                      })
                    : t('images.registryUnconfigured')}
                </div>
              </div>
            </div>
          </FormSection>

          <FormSection
            step={2}
            title={t('images.uploadSectionIdentity')}
            hint={t('images.uploadSectionIdentityHint')}
            requirement="defaulted"
            testId="upload-image-section-identity"
          >
            <div className="form-grid">
              <div className="form-field">
                <FieldLabel
                  htmlFor="upload-name"
                  text={t('images.fieldNameOptional')}
                  requirement="optional"
                  flag={t('form.reqDerived')}
                />
                <input
                  id="upload-name"
                  data-testid="upload-name-input"
                  value={name}
                  maxLength={255}
                  disabled={submitting}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={t('images.uploadNamePlaceholder')}
                />
              </div>
              <div className="form-field">
                <FieldLabel
                  htmlFor="upload-tag"
                  text={t('images.fieldTagOptional')}
                  requirement="optional"
                  flag={t('form.reqDerived')}
                />
                <input
                  id="upload-tag"
                  data-testid="upload-tag-input"
                  value={tag}
                  maxLength={128}
                  disabled={submitting}
                  onChange={(e) => setTag(e.target.value)}
                  placeholder={t('images.uploadTagPlaceholder')}
                />
              </div>
              <div className="form-field full">
                <FieldLabel
                  htmlFor="upload-reference"
                  text={t('images.sourceReferenceOptional')}
                  requirement="optional"
                />
                <input
                  id="upload-reference"
                  className="mono"
                  data-testid="upload-source-reference"
                  value={sourceReference}
                  disabled={submitting}
                  onChange={(e) => setSourceReference(e.target.value)}
                  placeholder="docker.io/library/vllm:v0.6.3"
                />
                <div className="field-hint">{t('images.uploadReferenceHint')}</div>
              </div>
            </div>
          </FormSection>

          <FormSection
            step={3}
            title={t('images.uploadSectionRuntime')}
            hint={t('images.uploadSectionRuntimeHint')}
            requirement="required"
            testId="upload-image-section-runtime"
          >
            <div className="form-grid">
              <div className="form-field">
                <FieldLabel
                  htmlFor="upload-accelerator"
                  text={t('images.fieldAccelerator')}
                  requirement="defaulted"
                />
                <select
                  id="upload-accelerator"
                  data-testid="upload-accelerator-select"
                  value={accelerator}
                  disabled={submitting}
                  onChange={(e) => setAccelerator(e.target.value)}
                >
                  {[...fleet.vendors, CPU_ACCELERATOR].map((a) => (
                    <option key={a} value={a}>
                      {a === CPU_ACCELERATOR ? t('images.acceleratorCpu') : a}
                    </option>
                  ))}
                </select>
              </div>
              <div className="form-field">
                <FieldLabel
                  htmlFor="upload-engine"
                  text={t('images.fieldEngine')}
                  requirement="required"
                />
                <input
                  id="upload-engine"
                  data-testid="upload-engine-input"
                  value={engine}
                  maxLength={64}
                  disabled={submitting}
                  onChange={(e) => setEngine(e.target.value)}
                  placeholder={t('images.placeholderEngine')}
                />
              </div>
              <div className="form-field full">
                <FieldLabel
                  htmlFor="upload-description"
                  text={t('common.descriptionOptional')}
                  requirement="optional"
                />
                <textarea
                  id="upload-description"
                  data-testid="upload-description"
                  rows={2}
                  maxLength={1024}
                  disabled={submitting}
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </div>
            </div>
          </FormSection>

          {error && <div className="field-error">{error}</div>}

          <div className="deploy-actions">
            <RequiredStatus
              missing={[
                !file && t('images.archiveFile'),
                !engine.trim() && t('images.fieldEngine'),
              ].filter((entry): entry is string => Boolean(entry))}
            />
            <button
              data-testid="upload-image-cancel-form"
              className="secondary"
              disabled={submitting}
              onClick={() => setFormOpen(false)}
            >
              {t('common.cancel')}
            </button>
            <button
              data-testid="upload-image-submit"
              disabled={submitting}
              onClick={() => void submit()}
            >
              {submitting ? t('images.uploading') : t('images.upload')}
            </button>
          </div>
        </div>
      )}

      <ImageUploadTaskList active={active} refreshKey={listRefresh} />
    </section>
  );
}

// ImageUploadTaskList is the history table under the upload form. It polls
// while a row reports progress, so the bar moves without the form holding the
// progress bar.
function ImageUploadTaskList({ active, refreshKey }: { active: boolean; refreshKey: number }) {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [tasks, setTasks] = useState<ImageUploadTask[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  const pollRef = useRef<number | null>(null);
  const PAGE_SIZE = 10;

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true);
      try {
        const data = await api.get<{
          uploads?: ImageUploadTask[];
          pageMeta?: PageMeta;
        }>(
          `/api/v1/admin/image-upload-tasks?page.offset=${offset}&page.limit=${PAGE_SIZE}`,
          orgId,
        );
        setTasks(data.uploads || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
        setError('');
      } catch (e) {
        setError(e instanceof Error ? e.message : t('images.uploadHistoryLoadFailed'));
      } finally {
        if (!quiet) setLoading(false);
      }
    },
    [orgId, offset, t],
  );

  useEffect(() => {
    if (!active) return;
    void load();
  }, [active, load]);

  // The form bumps refreshKey when a transfer starts or ends, so the new row
  // shows up (and its progress starts moving) at once.
  useEffect(() => {
    if (!active) return;
    void load(true);
  }, [active, refreshKey]);

  // Poll while a task is still live so progress and the outcome advance.
  const hasActive = useMemo(
    () => tasks.some((task) => ACTIVE_STATES.includes(task.state)),
    [tasks],
  );
  useEffect(() => {
    if (!active || !hasActive) return undefined;
    pollRef.current = window.setInterval(() => void load(true), 2000);
    return () => {
      if (pollRef.current !== null) window.clearInterval(pollRef.current);
      pollRef.current = null;
    };
  }, [active, hasActive, load]);

  const cancel = async (task: ImageUploadTask) => {
    setCancelling((s) => new Set(s).add(task.taskId));
    try {
      await api.del(`/api/v1/admin/image-upload-tasks/${task.taskId}`, orgId);
      await load(true);
    } catch (e) {
      setError(
        e instanceof ApiError ? `${e.message} (code ${e.code})` : t('images.uploadCancelFailed'),
      );
    } finally {
      setCancelling((s) => {
        const next = new Set(s);
        next.delete(task.taskId);
        return next;
      });
    }
  };

  return (
    <div className="panel" data-testid="upload-history">
      <h2>{t('images.uploadHistoryTitle')}</h2>
      {error && <ErrorBanner message={error} />}
      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : tasks.length === 0 ? (
        <div className="empty-state" data-testid="upload-history-empty">
          {t('images.uploadHistoryEmpty')}
        </div>
      ) : (
        <div className="table-scroll">
          <table className="table">
            <thead>
              <tr>
                <th>{t('images.uploadColImage')}</th>
                <th>{t('images.uploadColState')}</th>
                <th>{t('images.uploadColProgress')}</th>
                <th>{t('images.uploadColCreated')}</th>
                <th className="col-actions">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {tasks.map((task) => (
                <tr key={task.taskId} data-testid={`upload-image-row-${task.taskId}`}>
                  <td>
                    <strong className="mono">
                      {task.name || task.archiveName || t('images.uploadNamePending')}
                      {task.tag ? <span className="muted">:{task.tag}</span> : null}
                    </strong>
                    <div className="muted small">{task.archiveName}</div>
                  </td>
                  <td>
                    <span className={`badge ${stateBadgeClass(task.state)}`}>
                      {t(`images.uploadState.${task.state}`)}
                    </span>
                    <StatusError message={task.message} testId={`upload-image-error-${task.taskId}`} />
                    {task.imageId && (
                      <button
                        className="link"
                        onClick={() => navigate(`/admin/images/${task.imageId}`)}
                      >
                        {t('images.viewImage')}
                      </button>
                    )}
                  </td>
                  <td style={{ minWidth: 180 }}>
                    <div className="muted small" data-testid={`upload-image-bytes-${task.taskId}`}>
                      {progressLabel(task)}
                    </div>
                    {ACTIVE_STATES.includes(task.state) && <UploadTaskProgress task={task} />}
                  </td>
                  <td>{formatTime(task.createdAt)}</td>
                  <td>
                    {CANCELLABLE_STATES.includes(task.state) && (
                      <RowActionsMenu
                        label={t('common.actionsFor', { name: task.name || task.archiveName })}
                        testId={`upload-image-actions-${task.taskId}`}
                        actions={[
                          {
                            key: `cancel-${task.taskId}`,
                            label: t('images.uploadCancel'),
                            danger: true,
                            disabled: cancelling.has(task.taskId),
                            onClick: () => void cancel(task),
                          },
                        ]}
                      />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {total > PAGE_SIZE && (
        <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
      )}
    </div>
  );
}

// progressLabel renders "12.3 MB / 940.1 MB (1%)" while both byte counts are
// known, the bare count otherwise.
function progressLabel(task: ImageUploadTask): string {
  const total = task.totalBytes || 0;
  if (total <= 0) return formatBytes(task.bytesReceived || 0);
  const pct = uploadPercent(task);
  return `${formatBytes(task.bytesReceived || 0)} / ${formatBytes(total)} (${pct}%)`;
}

// uploadPercent converts the sampled byte count into a whole percent, clamped
// to 100 because a partly-written file can overshoot the advertised size.
function uploadPercent(task: ImageUploadTask): number {
  const total = task.totalBytes || 0;
  if (total <= 0) return 0;
  const done = task.bytesReceived || 0;
  const pct = Math.round((done / total) * 100);
  return pct < 0 ? 0 : pct > 100 ? 100 : pct;
}

// UploadTaskProgress draws a determinate bar while the archive streams in and
// an indeterminate one while the server pushes it; without a size there is
// nothing meaningful to measure.
function UploadTaskProgress({ task }: { task: ImageUploadTask }) {
  const total = task.totalBytes || 0;
  if (task.state === 'processing' || total <= 0) {
    return (
      <div className="progress-track">
        <div className="progress-bar indeterminate" data-testid="upload-image-progress-indeterminate" />
      </div>
    );
  }
  const pct = uploadPercent(task);
  return (
    <div
      className="progress-track"
      role="progressbar"
      aria-valuenow={pct}
      aria-valuemin={0}
      aria-valuemax={100}
      data-testid={`upload-image-progress-${task.taskId}`}
      data-percent={pct}
    >
      <div className="progress-fill" style={{ width: `${pct}%` }} />
    </div>
  );
}

// stateBadgeClass maps an upload task state to its badge modifier.
function stateBadgeClass(state: string): string {
  switch (state) {
    case 'succeeded':
      return 'active';
    case 'failed':
      return 'error';
    case 'cancelled':
      return 'muted';
    default:
      return 'info';
  }
}
