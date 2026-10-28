// UploadImageDialog pushes a `docker save` archive into the platform's own
// Harbor project.
//
// The archive goes over a plain multipart request (the engine image is far too
// large for a JSON body), so the upload is done with XMLHttpRequest: it is the
// only browser API that reports upload progress, and a multi-hundred-megabyte
// transfer without a progress bar looks like a hang.
//
// The destination is decided by the server from the chart's Harbor settings;
// the dialog only previews it, so an operator sees where the image will land
// before starting the transfer.

import { useEffect, useRef, useState } from 'react';
import { apiPrefix, ApiError, sessionAuthHeader, type ImageRegistryConfig } from '../api';
import { Dialog, ErrorBanner } from '../components';
import { useI18n } from '../i18n';
import { CPU_ACCELERATOR, useFleetAccelerators } from '../hooks/useFleetAccelerators';

// derivedName mirrors the server's rule: the image name comes from the
// archive (or the typed reference) and only its last path segment is kept.
function derivedName(reference: string): string {
  const ref = reference.trim();
  if (!ref) return '';
  const withoutTag = ref.includes(':') ? ref.slice(0, ref.lastIndexOf(':')) : ref;
  return withoutTag.split('/').filter(Boolean).pop() || '';
}

export default function UploadImageDialog({
  orgId,
  registry,
  onClose,
  onDone,
}: {
  orgId: string;
  registry: ImageRegistryConfig | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const fleet = useFleetAccelerators(orgId);
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState('');
  const [tag, setTag] = useState('');
  const [sourceReference, setSourceReference] = useState('');
  const [accelerator, setAccelerator] = useState('');
  const [engine, setEngine] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [progress, setProgress] = useState(0);
  const [error, setError] = useState('');
  const xhrRef = useRef<XMLHttpRequest | null>(null);

  useEffect(() => {
    if (!accelerator) setAccelerator(fleet.vendors[0] || CPU_ACCELERATOR);
  }, [accelerator, fleet.vendors]);

  // Stop an in-flight transfer when the dialog goes away: nothing would
  // consume the response and the server would keep staging the archive.
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
    setSubmitting(true);
    setError('');
    setProgress(0);

    const form = new FormData();
    form.append('file', file);
    form.append('accelerator', accelerator);
    form.append('engine', engine.trim());
    if (name.trim()) form.append('name', name.trim());
    if (tag.trim()) form.append('tag', tag.trim());
    if (sourceReference.trim()) form.append('sourceReference', sourceReference.trim());
    if (description.trim()) form.append('description', description.trim());

    const path = `${apiPrefix('admin')}/images:upload`;
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
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('images.uploadFailed'));
    } finally {
      xhrRef.current = null;
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('images.uploadTitle')} onClose={onClose} testId="upload-image-dialog">
      <p className="muted">{t('images.uploadHint')}</p>
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="upload-archive">{t('images.archiveFile')}</label>
          <input
            id="upload-archive"
            data-testid="upload-archive-input"
            type="file"
            accept=".tar,.tar.gz,.tgz,application/x-tar,application/gzip"
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
        <div className="form-field">
          <label htmlFor="upload-name">{t('images.fieldNameOptional')}</label>
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
          <label htmlFor="upload-tag">{t('images.fieldTagOptional')}</label>
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
          <label htmlFor="upload-reference">{t('images.sourceReferenceOptional')}</label>
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
        <div className="form-field">
          <label htmlFor="upload-accelerator">{t('images.fieldAccelerator')}</label>
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
          <label htmlFor="upload-engine">{t('images.fieldEngine')}</label>
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
          <label htmlFor="upload-description">{t('common.descriptionOptional')}</label>
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

      {submitting && (
        <div className="upload-progress" data-testid="upload-progress">
          <div className="progress-bar">
            <div className="progress-fill" style={{ width: `${progress}%` }} />
          </div>
          <div className="muted">
            {progress < 100
              ? t('images.uploadingProgress', { percent: String(progress) })
              : t('images.uploadPushing')}
          </div>
        </div>
      )}
      {error && <ErrorBanner message={error} />}

      <div className="dialog-actions">
        <button className="secondary" onClick={onClose} disabled={submitting}>
          {t('common.cancel')}
        </button>
        <button data-testid="upload-image-submit" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('images.uploading') : t('images.upload')}
        </button>
      </div>
    </Dialog>
  );
}
