// Upload a model from the operator's own disk, in a panel that expands below the
// catalog toolbar exactly like the download panel above it.
//
// The catalog could only be filled from a public hub before this panel: the
// download panel fetched weights from ModelScope or HuggingFace. A fine-tuned
// checkpoint, an offline installation or a model no hub carries had no way in,
// so this is that way: pick an archive of the model directory, say what to call
// it, and the platform streams it to the weights filesystem, extracts it and
// registers the catalog entry.
//
// It expands in place rather than opening a modal so an operator keeps the
// catalog in view while filling the form, and a long transfer cannot trap them
// behind an overlay.
//
// The transfer is resumable and chunked rather than one long request. The
// client asks the server to create an upload session (so the server fixes the
// chunk size), slices the file into parts of exactly that size and streams
// them with a bounded number of in-flight XMLHttpRequests, which is what gives
// the browser upload-progress events. A part PUT carries the raw bytes and the
// server composes them in order, so a restart can ask which parts already
// arrived and only re-send the missing ones. After the last part the client
// calls the complete endpoint and the server unpacks and registers the model.


import { useEffect, useRef, useState } from 'react';
import {
  ApiError,
  apiPrefix,
  sessionAuthHeader,
  type ModelUploadSession,
} from '../api';
import { useI18n } from '../i18n';
import { ErrorBanner } from '../components';
import { FieldLabel, FormLegend, FormSection, RequiredStatus } from './FormField';
import ModelUploadTasksDrawer from './ModelUploadTasksDrawer';
import { StorageBackendSelect } from './StorageBackendSelect';

interface Props {
  onClose: () => void;
  onDone: () => void;
  // resume continues an interrupted session: the operator picks the same
  // archive again and only the missing parts are sent.
  resume?: ModelUploadSession;
  // visible reports whether the containing section is expanded; the embedded
  // task list stops polling while it is collapsed.
  visible?: boolean;
  // onResume hands an interrupted session to the page so the editor can pick
  // the same archive and continue.
  onResume?: (session: ModelUploadSession) => void;
}

// ARCHIVE_ACCEPT is what the file picker offers. The server decides the format
// from the extension and rejects anything else.
const ARCHIVE_ACCEPT = '.tar,.tar.gz,.tgz,.zip,application/x-tar,application/gzip,application/zip';

// MAX_IN_FLIGHT caps how many part PUTs run at once. Four parallel streams
// saturate most links without overwhelming the server or the browser's
// connection pool.
const MAX_IN_FLIGHT = 4;

// POLL_MS is how often the client re-checks a completed session that is
// unpacking on the server.
const POLL_MS = 2000;

// CANCELLED is a sentinel the part uploads throw when the operator hits
// cancel, so the submit loop can tell a deliberate abort from a transport
// error.
const CANCELLED = Symbol('cancelled');

// sizeLabel renders a byte count with a binary unit, so the operator can
// confirm the archive they picked is the one they meant.
function sizeLabel(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(value >= 10 || unit === 0 ? 0 : 1)} ${units[unit]}`;
}

export default function ModelUploadPanel({
  onClose,
  onDone,
  resume,
  visible = true,
  onResume,
}: Props) {
  const { t } = useI18n();
  // A resumed upload only needs the archive; its catalog metadata is already
  // stored on the session being resumed, so the picker is the only editable field.
  const resuming = Boolean(resume);
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState('');
  const [hubModelId, setHubModelId] = useState('');
  const [description, setDescription] = useState('');
  const [storageBackend, setStorageBackend] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  // formOpen folds the form away after a successful submit so the upload list
  // is what the operator watches while bytes move; it reopens from the header.
  const [formOpen, setFormOpen] = useState(true);
  // listRefresh bumps when a session is created or ends so the embedded list
  // reloads and starts polling the new row at once.
  const [listRefresh, setListRefresh] = useState(0);
  // phase drives the extracting label; the progress bar lives in the list.
  const [phase, setPhase] = useState<'idle' | 'uploading' | 'processing'>('idle');
  const [cancelled, setCancelled] = useState(false);

  // In-flight part uploads, kept so cancel can abort them all at once.
  const activeXhrsRef = useRef<Set<XMLHttpRequest>>(new Set());
  // The current session id, needed by the cancel button to delete it.
  const uploadIdRef = useRef('');
  // Cancellation is a ref so the async workers see it immediately rather than
  // through stale closure state.
  const cancelledRef = useRef(false);
  const pollTimerRef = useRef<number | null>(null);
  // activeRef mirrors `submitting` for the unmount/pagehide handlers, whose
  // closures would otherwise see the initial false.
  const activeRef = useRef(false);
  // markInterrupted tells the server the browser driving this upload is gone,
  // so the session turns resumable (a row offers resume) instead of looking
  // stuck in "uploading" until the stale fallback notices. It must survive the
  // page going away, so it uses a keepalive request.
  const markInterrupted = (uploadId: string) => {
    if (!uploadId) return;
    const headers: Record<string, string> = {};
    const authorization = sessionAuthHeader('admin');
    if (authorization) headers['Authorization'] = authorization;
    void fetch(`${apiPrefix('admin')}/models/uploads/${uploadId}:interrupt`, {
      method: 'POST',
      headers,
      keepalive: true,
    }).catch(() => undefined);
  };

  // Abort any in-flight transfer and stop the completion poll when the dialog
  // unmounts (the operator closed it another way). Aborting alone would leave
  // the server row claiming to upload forever, so also mark it interrupted:
  // the parts it already holds are kept and the row offers resume.
  useEffect(
    () => () => {
      for (const xhr of activeXhrsRef.current) xhr.abort();
      activeXhrsRef.current.clear();
      if (pollTimerRef.current !== null) window.clearInterval(pollTimerRef.current);
      if (activeRef.current && !cancelledRef.current) markInterrupted(uploadIdRef.current);
    },
    [],
  );

  // Abort any in-flight transfer and stop the completion poll when the dialog
  // unmounts (the operator closed it another way).
  useEffect(
    () => () => {
      for (const xhr of activeXhrsRef.current) xhr.abort();
      activeXhrsRef.current.clear();
      if (pollTimerRef.current !== null) window.clearInterval(pollTimerRef.current);
    },
    [],
  );

  // A real page unload (refresh, tab close, navigation away from the console)
  // does not always run React cleanup in time, so report the browser is gone
  // from the page lifecycle hook as well. The keepalive request outlives the
  // page and turns the session resumable at once.
  useEffect(() => {
    if (!submitting) return undefined;
    const onPageHide = () => {
      if (!cancelledRef.current) markInterrupted(uploadIdRef.current);
    };
    window.addEventListener('pagehide', onPageHide);
    return () => window.removeEventListener('pagehide', onPageHide);
  }, [submitting]);

  // resume may arrive while the panel is already open: clicking resume on a
  // row of the embedded task list hands the session to this already-mounted
  // editor, so refill it here instead of only in the initial state.
  useEffect(() => {
    if (!resume) return;
    setName(resume.name || '');
    setHubModelId(resume.hubModelId || '');
    setDescription(resume.description || '');
    setStorageBackend(resume.storageBackend || '');
    setFile(null);
    setError('');
    setPhase('idle');
  }, [resume?.uploadId]);

  // Reopen the form whenever the panel is expanded again, so a finished
  // upload does not leave the header toggle looking inert.
  useEffect(() => {
    if (visible && !submitting) setFormOpen(true);
  }, [visible]);

  // A picked file fills the name when the operator has not typed one: the
  // archive usually carries the model's name, and asking twice is noise.
  const pickFile = (picked: File | null) => {
    setFile(picked);
    if (picked && !name.trim()) {
      const base = picked.name.replace(/\.(tar\.gz|tgz|tar|zip)$/i, '');
      setName(base.slice(0, 128));
    }
  };

  // applyAuth sets the Authorization header from the admin session onto a
  // request, mirroring how the api client authenticates every other call.
  const applyAuth = (xhr: XMLHttpRequest) => {
    const authorization = sessionAuthHeader('admin');
    if (authorization) xhr.setRequestHeader('Authorization', authorization);
  };

  // jsonRequest wraps a JSON call against the upload endpoints. Unlike the
  // api client (which envelops and needs an org id), these endpoints return
  // plain objects and authenticate solely by the admin session token.
  const jsonRequest = async <T,>(method: string, path: string, body?: unknown): Promise<T> => {
    const headers: Record<string, string> = { 'Content-Type': 'application/json' };
    const authorization = sessionAuthHeader('admin');
    if (authorization) headers['Authorization'] = authorization;
    let res: Response;
    try {
      res = await fetch(`${apiPrefix('admin')}${path}`, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
      });
    } catch {
      throw new ApiError(0, t('models.upload.failed'));
    }
    let data: Record<string, unknown> = {};
    try {
      data = (await res.json()) as Record<string, unknown>;
    } catch {
      // A non-JSON response is a transport failure.
    }
    if (!res.ok) {
      const message =
        (data.message as string | undefined) || (data.response as { message?: string } | undefined)?.message;
      throw new ApiError(res.status, message || t('models.upload.failed'));
    }
    return data as T;
  };

  // uploadParts slices the file into partSizeBytes chunks, skips the parts the
  // server already holds (resume), and pushes the missing ones with bounded
  // concurrency. It resolves once every missing part is on the server or
  // throws when a part fails or the operator cancels.
  const uploadParts = async (session: ModelUploadSession, target: File) => {
    const { uploadId, partSizeBytes, totalBytes } = session;
    const part = partSizeBytes > 0 ? partSizeBytes : Math.max(totalBytes, 1);
    const partCount = Math.max(1, Math.ceil(totalBytes / part));
    const { parts } = await jsonRequest<{ parts?: number[] }>(
      'GET',
      `/models/uploads/${uploadId}/parts`,
    );
    const received = new Set(parts || []);
    const missing: number[] = [];
    for (let n = 1; n <= partCount; n += 1) {
      if (!received.has(n)) missing.push(n);
    }

    const sendPart = (n: number) =>
      new Promise<void>((resolve, reject) => {
        const start = (n - 1) * part;
        const end = Math.min(start + part, totalBytes);
        const blob = target.slice(start, end);
        const xhr = new XMLHttpRequest();
        activeXhrsRef.current.add(xhr);
        xhr.open('PUT', `${apiPrefix('admin')}/models/uploads/${uploadId}/parts/${n}`);
        applyAuth(xhr);
        xhr.setRequestHeader('Content-Type', target.type || 'application/octet-stream');
        xhr.onload = () => {
          if (cancelledRef.current) {
            reject(CANCELLED);
            return;
          }
          activeXhrsRef.current.delete(xhr);
          if (xhr.status >= 200 && xhr.status < 300) {
            resolve();
            return;
          }
          let message = t('models.upload.failed');
          try {
            const body = JSON.parse(xhr.responseText) as { message?: string };
            if (body.message) message = body.message;
          } catch {
            // Keep the generic message.
          }
          reject(new ApiError(0, message));
        };
        xhr.onerror = () => {
          activeXhrsRef.current.delete(xhr);
          reject(new ApiError(0, t('models.upload.failed')));
        };
        xhr.onabort = () => {
          activeXhrsRef.current.delete(xhr);
          reject(CANCELLED);
        };
        xhr.send(blob);
      });

    // Worker pool: keep at most MAX_IN_FLIGHT part requests in the air by
    // letting each worker claim the next pending part when one finishes.
    let next = 0;
    const workers = Array.from({ length: Math.min(MAX_IN_FLIGHT, missing.length) }, async () => {
      while (!cancelledRef.current) {
        const n = missing[next];
        if (n === undefined) return;
        next += 1;
        await sendPart(n);
      }
    });
    await Promise.all(workers);
    if (cancelledRef.current) throw CANCELLED;
  };

  // waitForOutcome polls a completed session until the server is done
  // unpacking. It resolves with the terminal outcome so the caller can decide
  // what to do: succeeded hands off to onDone, failed surfaces the server's
  // message, and cancelled resets without a notice.
  const waitForOutcome = async (
    uploadId: string,
  ): Promise<'succeeded' | 'failed' | 'cancelled' | 'error'> =>
    new Promise((resolve) => {
      const check = async () => {
        let session: ModelUploadSession;
        try {
          session = await jsonRequest<ModelUploadSession>('GET', `/models/uploads/${uploadId}`);
        } catch (e) {
          setError(e instanceof Error ? e.message : t('models.upload.failed'));
          setPhase('idle');
          if (pollTimerRef.current !== null) window.clearInterval(pollTimerRef.current);
          pollTimerRef.current = null;
          resolve('error');
          return;
        }
        if (session.state === 'succeeded') {
          if (pollTimerRef.current !== null) window.clearInterval(pollTimerRef.current);
          pollTimerRef.current = null;
          resolve('succeeded');
          return;
        }
        if (session.state === 'failed') {
          if (pollTimerRef.current !== null) window.clearInterval(pollTimerRef.current);
          pollTimerRef.current = null;
          setError(session.message || t('models.upload.failed'));
          setPhase('idle');
          resolve('failed');
          return;
        }
        if (session.state === 'cancelled') {
          if (pollTimerRef.current !== null) window.clearInterval(pollTimerRef.current);
          pollTimerRef.current = null;
          setPhase('idle');
          resolve('cancelled');
          return;
        }
        // Still processing (or a transient state): keep polling.
      };
      void check();
      pollTimerRef.current = window.setInterval(() => void check(), POLL_MS);
    });

  // cancelUpload aborts the in-flight parts, deletes the session on the server
  // so no orphan parts linger, and closes the dialog.
  const cancelUpload = async () => {
    if (cancelledRef.current || !submitting) return;
    cancelledRef.current = true;
    setCancelled(true);
    for (const xhr of activeXhrsRef.current) xhr.abort();
    activeXhrsRef.current.clear();
    if (uploadIdRef.current) {
      try {
        const headers: Record<string, string> = {};
        const authorization = sessionAuthHeader('admin');
        if (authorization) headers['Authorization'] = authorization;
        await fetch(`${apiPrefix('admin')}/models/uploads/${uploadIdRef.current}`, {
          method: 'DELETE',
          headers,
        });
      } catch {
        // Best effort: the server also cleans up abandoned sessions.
      }
    }
    onClose();
  };

  const submit = async () => {
    if (!file) {
      setError(t('models.upload.validationFile'));
      return;
    }
    if (!name.trim()) {
      setError(t('models.upload.validationName'));
      return;
    }
    if (!hubModelId.trim()) {
      setError(t('models.validationHubModelIdRequired'));
      return;
    }
    if (!/^[^/\s]+\/[^/\s]+$/.test(hubModelId.trim())) {
      setError(t('models.validationHubModelId'));
      return;
    }
    setFormOpen(false);
    uploadIdRef.current = '';
    setSubmitting(true);
    setError('');
    setCancelled(false);
    cancelledRef.current = false;
    activeRef.current = true;
    setPhase('uploading');

    try {
      // 1. Create the session, or re-read the interrupted one being resumed;
      // the server answers with the chunk size the archive is cut at.
      let session: ModelUploadSession;
      if (resume) {
        session = await jsonRequest<ModelUploadSession>(
          'GET',
          `/models/uploads/${resume.uploadId}`,
        );
        if (!['creating', 'uploading', 'interrupted'].includes(session.state)) {
          setError(t('models.upload.resumeUnavailable', { state: session.state }));
          setPhase('idle');
          return;
        }
        if (session.totalBytes > 0 && file.size !== session.totalBytes) {
          setError(
            t('models.upload.resumeSizeMismatch', {
              expected: sizeLabel(session.totalBytes),
              actual: sizeLabel(file.size),
            }),
          );
          setPhase('idle');
          return;
        }
      } else {
        session = await jsonRequest<ModelUploadSession>('POST', '/models/uploads', {
          name: name.trim(),
          hubModelId: hubModelId.trim(),
          description: description.trim(),
          storageBackend,
          archiveName: file.name,
          totalBytes: file.size,
        });
      }
      uploadIdRef.current = session.uploadId;
      setListRefresh((n) => n + 1);

      // 2. Stream every missing part (resume-aware), with dashboard progress.
      await uploadParts(session, file);
      if (cancelledRef.current) return;

      // 3. All parts are on the server; ask it to compose and unpack.
      setPhase('processing');
      await jsonRequest<ModelUploadSession>('POST', `/models/uploads/${session.uploadId}:complete`);

      // 4. The server unpacks in the background; poll for the outcome. On
      // success the parent shows the success notice; on failure or cancel the
      // poll already showed the server message and reset the phase, so the
      // dialog stays open and retryable.
      const outcome = await waitForOutcome(session.uploadId);
      if (cancelledRef.current) return;
      if (outcome === 'succeeded') {
        // The panel stays mounted while collapsed, so clear the finished form
        // rather than leaving the previous archive selected for the next one.
        setFile(null);
        setName('');
        setHubModelId('');
        setDescription('');
        setStorageBackend('');
        setPhase('idle');
        onDone();
      }
    } catch (e) {
      if (cancelledRef.current) return;
      setError(e instanceof Error ? e.message : t('models.upload.failed'));
      setPhase('idle');
      // A submit that never created a session has nothing to show in the list,
      // so bring the form back with the reason instead of leaving only a banner.
      if (!uploadIdRef.current) setFormOpen(true);
    } finally {
      activeRef.current = false;
      if (!cancelledRef.current) setSubmitting(false);
      setListRefresh((n) => n + 1);
    }
  };

  const showCancel = submitting && phase === 'uploading' && !cancelled;

  return (
    <section data-testid="model-upload-panel">
      <div className="section-header">
        <div>
          <h2 style={{ margin: 0 }}>{t('models.upload.title')}</h2>
          <div className="subtitle">{t('models.upload.subtitle')}</div>
        </div>
        <button
          className={formOpen ? 'secondary' : undefined}
          data-testid="toggle-upload-form"
          onClick={() => setFormOpen((open) => !open)}
          disabled={submitting}
        >
          {formOpen ? t('common.cancel') : t('models.upload.open')}
        </button>
      </div>

      {!formOpen && error && <ErrorBanner message={error} />}

      {formOpen && (
      <div className="panel" style={{ marginBottom: 16 }} data-testid="upload-model-form">
      <h2>{t('models.upload.formTitle')}</h2>
      <p className="muted">{resuming ? t('models.upload.resumeHint') : t('models.upload.hint')}</p>
      <FormLegend testId="upload-model-legend" />
      <FormSection
        step={1}
        title={t('models.upload.sectionArchive')}
        hint={t('models.upload.sectionArchiveHint')}
        requirement="required"
        testId="upload-model-section-archive"
      >
        <div className="form-grid">
          <div className="form-field full">
            <FieldLabel
              htmlFor="upload-model-archive"
              text={t('models.upload.archive')}
              requirement="required"
            />
            <input
              id="upload-model-archive"
              data-testid="upload-model-archive"
              type="file"
              accept={ARCHIVE_ACCEPT}
              disabled={submitting}
              onChange={(e) => pickFile(e.target.files?.[0] || null)}
            />
            <div className="field-hint">
              {file
                ? t('models.upload.selected', { name: file.name, size: sizeLabel(file.size) })
                : t('models.upload.archiveHint')}
            </div>
          </div>
        </div>
      </FormSection>

      <FormSection
        step={2}
        title={t('models.upload.sectionCatalog')}
        hint={t('models.upload.sectionCatalogHint')}
        requirement="required"
        testId="upload-model-section-catalog"
      >
        <div className="form-grid">
          <div className="form-field">
            <FieldLabel
              htmlFor="upload-model-name"
              text={t('models.upload.name')}
              requirement="required"
            />
            <input
              id="upload-model-name"
              data-testid="upload-model-name"
              value={name}
              maxLength={128}
              disabled={submitting || resuming}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('models.upload.namePlaceholder')}
            />
            <div className="field-hint">{t('models.upload.nameHint')}</div>
          </div>
          <div className="form-field">
            <FieldLabel
              htmlFor="upload-model-hub-id"
              text={t('models.fieldHubModelId')}
              requirement="required"
            />
            <input
              id="upload-model-hub-id"
              data-testid="upload-model-hub-id"
              value={hubModelId}
              maxLength={255}
              disabled={submitting || resuming}
              onChange={(e) => setHubModelId(e.target.value)}
              placeholder={t('models.placeholderHubModelId')}
            />
            <div className="field-hint">{t('models.hubModelIdHint')}</div>
          </div>
          <StorageBackendSelect
            id="upload-storage-backend"
            testId="upload-storage-backend"
            value={storageBackend}
            onChange={setStorageBackend}
            disabled={submitting || resuming}
          />
          <div className="form-field full">
            <FieldLabel
              htmlFor="upload-model-description"
              text={t('models.upload.description')}
              requirement="optional"
            />
            <input
              id="upload-model-description"
              data-testid="upload-model-description"
              value={description}
              maxLength={1024}
              disabled={submitting || resuming}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
        </div>
      </FormSection>


      {error && <div className="field-error">{error}</div>}

      <div className="deploy-actions">
        <RequiredStatus
          missing={[
            !file && t('models.upload.archive'),
            !name.trim() && t('models.upload.name'),
            !hubModelId.trim() && t('models.fieldHubModelId'),
          ].filter((entry): entry is string => Boolean(entry))}
        />
        {showCancel ? (
          <button
            className="secondary danger"
            data-testid="upload-model-cancel"
            onClick={() => void cancelUpload()}
          >
            {t('models.upload.cancel')}
          </button>
        ) : (
          <button
            className="secondary"
            data-testid="upload-model-cancel"
            onClick={() => setFormOpen(false)}
            disabled={submitting}
          >
            {t('common.cancel')}
          </button>
        )}
        <button
          data-testid="upload-model-submit"
          onClick={() => void submit()}
          disabled={submitting}
        >
          {submitting ? t('models.upload.submitting') : t('models.upload.submit')}
        </button>
      </div>
      </div>
      )}

      <ModelUploadTasksDrawer
        embedded
        active={visible}
        refreshKey={listRefresh}
        onResume={onResume}
      />
    </section>
  );
}