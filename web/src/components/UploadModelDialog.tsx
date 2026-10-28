// Upload a model from the operator's own disk.
//
// The catalog could only be filled from a public hub before this dialog: the
// download panel fetched weights from ModelScope or HuggingFace. A fine-tuned
// checkpoint, an offline installation or a model no hub carries had no way in,
// so this is that way: pick an archive of the model directory, say what to call
// it, and the platform streams it to the weights filesystem, extracts it and
// registers the version.
//
// The transfer is an XMLHttpRequest rather than the api client: a model
// archive is gigabytes, and only the browser's own upload progress events can
// tell an operator that something is happening. The request stays open while
// the server extracts, so the last few percent can pause on a large model;
// the label says so instead of leaving a stuck bar.

import { useEffect, useRef, useState } from 'react';
import { Dialog } from '../components';
import { ApiError, apiPrefix, sessionAuthHeader } from '../api';
import { useI18n } from '../i18n';

interface Props {
  onClose: () => void;
  onDone: () => void;
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

export default function UploadModelDialog({ onClose, onDone }: Props) {
  const { t } = useI18n();
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState('');
  const [version, setVersion] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [progress, setProgress] = useState(0);
  const [phase, setPhase] = useState<'idle' | 'uploading' | 'processing'>('idle');
  const xhrRef = useRef<XMLHttpRequest | null>(null);

  useEffect(
    () => () => {
      xhrRef.current?.abort();
    },
    [],
  );

  // A picked file fills the name when the operator has not typed one: the
  // archive usually carries the model's name, and asking twice is noise.
  const pickFile = (picked: File | null) => {
    setFile(picked);
    if (picked && !name.trim()) {
      const base = picked.name.replace(/\.(tar\.gz|tgz|tar|zip)$/i, '');
      setVersion((prev) => prev);
      setName(base.slice(0, 128));
    }
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
    setSubmitting(true);
    setError('');
    setProgress(0);
    setPhase('uploading');

    const form = new FormData();
    form.append('file', file);
    form.append('name', name.trim());
    if (version.trim()) form.append('version', version.trim());
    if (description.trim()) form.append('description', description.trim());

    const path = `${apiPrefix('admin')}/models:upload`;
    try {
      await new Promise<void>((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhrRef.current = xhr;
        xhr.open('POST', path);
        const authorization = sessionAuthHeader('admin');
        if (authorization) xhr.setRequestHeader('Authorization', authorization);
        xhr.upload.onprogress = (event) => {
          if (event.lengthComputable) {
            setProgress(Math.round((event.loaded / event.total) * 100));
          }
        };
        // The body is sent before the server finishes extracting, so the bar
        // switches meaning here rather than sitting at 100%.
        xhr.upload.onload = () => setPhase('processing');
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
          reject(new ApiError(envelope.code ?? 0, envelope.message || t('models.upload.failed')));
        };
        xhr.onerror = () => reject(new ApiError(0, t('models.upload.failed')));
        xhr.onabort = () => reject(new ApiError(0, t('common.cancel')));
        xhr.send(form);
      });
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('models.upload.failed'));
      setPhase('idle');
    } finally {
      xhrRef.current = null;
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('models.upload.title')} onClose={onClose} testId="upload-model-dialog">
      <p className="muted">{t('models.upload.hint')}</p>
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="upload-model-archive">{t('models.upload.archive')}</label>
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
        <div className="form-field">
          <label htmlFor="upload-model-name">{t('models.upload.name')}</label>
          <input
            id="upload-model-name"
            data-testid="upload-model-name"
            value={name}
            maxLength={128}
            disabled={submitting}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('models.upload.namePlaceholder')}
          />
        </div>
        <div className="form-field">
          <label htmlFor="upload-model-version">{t('models.upload.version')}</label>
          <input
            id="upload-model-version"
            data-testid="upload-model-version"
            value={version}
            maxLength={64}
            disabled={submitting}
            onChange={(e) => setVersion(e.target.value)}
            placeholder={t('models.upload.versionPlaceholder')}
          />
        </div>
        <div className="form-field full">
          <label htmlFor="upload-model-description">{t('models.upload.description')}</label>
          <input
            id="upload-model-description"
            data-testid="upload-model-description"
            value={description}
            maxLength={1024}
            disabled={submitting}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>
      </div>

      {submitting && (
        <div style={{ marginBottom: 12 }} data-testid="upload-model-progress">
          <div className="progress-bar">
            <div className="progress-fill" style={{ width: `${phase === 'processing' ? 100 : progress}%` }} />
          </div>
          <div className="field-hint">
            {phase === 'processing'
              ? t('models.upload.extracting')
              : t('models.upload.progress', { percent: String(progress) })}
          </div>
        </div>
      )}

      {error && <div className="field-error">{error}</div>}

      <div className="dialog-actions">
        <button className="secondary" data-testid="upload-model-cancel" onClick={onClose} disabled={submitting}>
          {t('common.cancel')}
        </button>
        <button data-testid="upload-model-submit" onClick={() => void submit()} disabled={submitting}>
          {submitting ? t('models.upload.submitting') : t('models.upload.submit')}
        </button>
      </div>
    </Dialog>
  );
}
