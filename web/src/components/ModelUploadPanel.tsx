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


import { useEffect, useState, useSyncExternalStore } from 'react';
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
import { uploadManager } from '../upload/manager';
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

  // The transfer runs in a module-level engine, so navigating inside the
  // console (which unmounts this panel) does not abort it. The panel only
  // subscribes for display and starts/stops the job.
  const job = useSyncExternalStore(uploadManager.subscribe, uploadManager.getState);
  const busy = submitting || job.status === 'uploading' || job.status === 'processing';

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

  // submit validates the form, prepares the resume session (the server-side
  // checks need the stored session), then hands the transfer to the engine.
  // The engine keeps running if this panel unmounts; a settled job is applied
  // by the effect below.
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
    setError('');
    setSubmitting(true);

    try {
      if (resume) {
        // Re-read the session being resumed: the server decides whether it is
        // still resumable and what chunk size it expects.
        const session = await jsonRequest<ModelUploadSession>(
          'GET',
          `/models/uploads/${resume.uploadId}`,
        );
        if (!['creating', 'uploading', 'interrupted'].includes(session.state)) {
          setError(t('models.upload.resumeUnavailable', { state: session.state }));
          setSubmitting(false);
          return;
        }
        if (session.totalBytes > 0 && file.size !== session.totalBytes) {
          setError(
            t('models.upload.resumeSizeMismatch', {
              expected: sizeLabel(session.totalBytes),
              actual: sizeLabel(file.size),
            }),
          );
          setSubmitting(false);
          return;
        }
        void uploadManager.resume(session, file);
        return;
      }
      void uploadManager.start(file, {
        name: name.trim(),
        hubModelId: hubModelId.trim(),
        description: description.trim(),
        storageBackend,
      });
    } catch (e) {
      setError(e instanceof Error ? e.message : t('models.upload.failed'));
      setSubmitting(false);
      setFormOpen(true);
    }
  };

  // cancelUpload stops the engine, deletes the session and its staged parts,
  // and closes the panel.
  const cancelUpload = () => {
    void uploadManager.cancel();
    setSubmitting(false);
    onClose();
  };

  // A settled job is applied once: success clears the form and tells the page
  // to refresh the catalog; failure shows the server's reason and reopens the
  // form so the operator can retry.
  useEffect(() => {
    if (job.status === 'succeeded') {
      setSubmitting(false);
      setFile(null);
      setName('');
      setHubModelId('');
      setDescription('');
      setStorageBackend('');
      setListRefresh((n) => n + 1);
      onDone();
      uploadManager.reset();
      return;
    }
    if (job.status === 'failed') {
      setSubmitting(false);
      setError(job.error || t('models.upload.failed'));
      setFormOpen(true);
      setListRefresh((n) => n + 1);
      uploadManager.reset();
      return;
    }
    if (job.status === 'cancelled') {
      setSubmitting(false);
      setListRefresh((n) => n + 1);
      uploadManager.reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [job.status]);

  const showCancel = busy;

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
          disabled={busy}
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
              disabled={busy}
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
            disabled={busy}
          >
            {t('common.cancel')}
          </button>
        )}
        <button
          data-testid="upload-model-submit"
          onClick={() => void submit()}
          disabled={busy}
        >
          {busy ? t('models.upload.submitting') : t('models.upload.submit')}
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