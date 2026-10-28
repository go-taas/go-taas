// Images page: the engine image catalog with accelerator/engine filters,
// register, edit, delete and warmup actions.
// Implements docs/design/image-management.md FR1, FR2, AC12, AC13.

import { useCallback, useEffect, useState } from 'react';
import { navigate } from '../router';
import {
  api,
  ApiError,
  formatTime,
  type ImageRegistryConfig,
  type ImageSummary,
  type PageMeta,
} from '../api';
import { useOrg } from '../org';
import { Dialog, ErrorBanner, Pagination, StateBadge } from '../components';
import { useI18n } from '../i18n';
import { CPU_ACCELERATOR, useFleetAccelerators } from '../hooks/useFleetAccelerators';
import UploadImageDialog from '../components/UploadImageDialog';

interface ListResponse {
  response: { code: number; message: string };
  images: ImageSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;
// The vendor list is whatever the cluster reports (plus the CPU marker),
// never a constant: an operator must not be able to register an image for
// hardware the fleet does not have.

export default function ImagesPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [images, setImages] = useState<ImageSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [accelerator, setAccelerator] = useState('all');
  const fleet = useFleetAccelerators(orgId);
  const [engine, setEngine] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [uploadOpen, setUploadOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<ImageSummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ImageSummary | null>(null);
  const [warmupTarget, setWarmupTarget] = useState<ImageSummary | null>(null);
  // The Harbor registry imported images land in, as configured by the chart.
  // Read from the server so the page never hard-codes a host or project.
  const [registry, setRegistry] = useState<ImageRegistryConfig | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams({
        [`page.offset`]: String(offset),
        [`page.limit`]: String(PAGE_SIZE),
      });
      if (accelerator !== 'all') params.set('accelerator', accelerator);
      if (engine.trim()) params.set('engine', engine.trim());
      const data = await api.get<ListResponse>(
        `/api/v1/admin/images?${params.toString()}`,
        orgId,
      );
      setImages(data.images || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('images.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, accelerator, engine, t]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    api
      .get<ImageRegistryConfig>('/api/v1/admin/image-registry-config', orgId)
      .then((data) => setRegistry(data))
      .catch(() => setRegistry(null));
  }, [orgId]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('images.title')}</h1>
          <div className="subtitle">{t('images.subtitle')}</div>
        </div>
        <div className="toolbar">
          <button
            className="secondary"
            data-testid="import-image"
            disabled={registry !== null && !registry.configured}
            title={registry && !registry.configured ? t('images.registryUnconfigured') : undefined}
            onClick={() => setImportOpen(true)}
          >
            {t('images.import')}
          </button>
          <button
            data-testid="upload-image"
            disabled={registry !== null && !registry.configured}
            title={registry && !registry.configured ? t('images.registryUnconfigured') : undefined}
            onClick={() => setUploadOpen(true)}
          >
            {t('images.upload')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel" style={{ marginBottom: 14 }} data-testid="image-registry-card">
        <h3 style={{ marginTop: 0 }}>{t('images.registryTitle')}</h3>
        {registry === null ? (
          <div className="muted" data-testid="image-registry-loading">
            {t('common.loading')}
          </div>
        ) : registry.configured ? (
          <div className="detail-grid">
            <div className="detail-item">
              <div className="label">{t('images.registryHostname')}</div>
              <div className="value mono" data-testid="image-registry-hostname">
                {registry.hostname}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('images.registryProject')}</div>
              <div className="value mono" data-testid="image-registry-project">
                {registry.project}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('images.registryExample')}</div>
              <div className="value mono" data-testid="image-registry-example">
                {registry.exampleReference}
              </div>
            </div>
          </div>
        ) : (
          <div className="muted" data-testid="image-registry-unconfigured">
            {t('images.registryUnconfigured')}
          </div>
        )}
      </div>

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <select
          data-testid="image-accelerator-filter"
          value={accelerator}
          onChange={(e) => {
            setAccelerator(e.target.value);
            setOffset(0);
          }}
        >
          <option value="all">{t('images.filterAccelerator')}</option>
          {[...fleet.vendors, CPU_ACCELERATOR].map((a) => (
            <option key={a} value={a}>
              {a === CPU_ACCELERATOR ? t('images.acceleratorCpu') : a}
            </option>
          ))}
        </select>
        <input
          type="text"
          data-testid="image-engine-filter"
          placeholder={t('images.filterEngine')}
          value={engine}
          onChange={(e) => {
            setEngine(e.target.value);
            setOffset(0);
          }}
        />
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : images.length === 0 ? (
          <div className="empty-state" data-testid="images-empty">
            {t('images.empty')}
          </div>
        ) : (
          <table className="data" data-testid="images-table">
            <thead>
              <tr>
                <th>{t('images.colImage')}</th>
                <th>{t('images.colAccelerator')}</th>
                <th>{t('images.colEngine')}</th>
                <th>{t('images.colInUse')}</th>
                <th>{t('images.colLastWarmup')}</th>
                <th>{t('images.colCreated')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {images.map((img) => (
                <tr
                  key={img.imageId}
                  data-testid={`image-row-${img.name}`}
                  style={{ cursor: 'pointer' }}
                  onClick={() => navigate(`/admin/images/${img.imageId}`)}
                >
                  <td>
                    <strong className="mono">{img.name}</strong>
                    <span className="mono muted">:{img.tag}</span>
                  </td>
                  <td>{img.accelerator}</td>
                  <td>{img.engine}</td>
                  <td data-testid={`image-in-use-${img.name}`}>{img.inUseCount}</td>
                  <td>
                    {img.lastWarmupState ? (
                      <StateBadge state={img.lastWarmupState} />
                    ) : (
                      <span className="muted">—</span>
                    )}
                  </td>
                  <td>{formatTime(img.createdAt)}</td>
                  <td onClick={(e) => e.stopPropagation()}>
                    <button
                      className="link"
                      data-testid={`warmup-${img.name}`}
                      onClick={() => setWarmupTarget(img)}
                    >
                      {t('images.warmup')}
                    </button>
                    <button
                      className="link"
                      data-testid={`edit-${img.name}`}
                      onClick={() => setEditTarget(img)}
                    >
                      {t('common.edit')}
                    </button>
                    <button
                      className="link danger"
                      data-testid={`delete-${img.name}`}
                      onClick={() => setDeleteTarget(img)}
                    >
                      {t('common.delete')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
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

      {uploadOpen && (
        <UploadImageDialog
          orgId={orgId}
          registry={registry}
          onClose={() => setUploadOpen(false)}
          onDone={() => {
            setUploadOpen(false);
            void load();
          }}
        />
      )}

      {importOpen && (
        <ImportImageDialog
          orgId={orgId}
          registry={registry}
          onClose={() => setImportOpen(false)}
          onDone={() => {
            setImportOpen(false);
            void load();
          }}
        />
      )}

      {editTarget && (
        <EditImageDialog
          orgId={orgId}
          image={editTarget}
          onClose={() => setEditTarget(null)}
          onDone={() => {
            setEditTarget(null);
            void load();
          }}
        />
      )}

      {deleteTarget && (
        <DeleteImageDialog
          orgId={orgId}
          image={deleteTarget}
          onClose={() => setDeleteTarget(null)}
          onDone={() => {
            setDeleteTarget(null);
            void load();
          }}
        />
      )}

      {warmupTarget && (
        <WarmupDialog
          orgId={orgId}
          image={warmupTarget}
          onClose={() => setWarmupTarget(null)}
          onDone={() => {
            setWarmupTarget(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

// ImportImageDialog pulls an external image into the cluster's own Harbor
// project and registers the local reference. The destination is decided by
// the server from the chart's `image.harbor` settings, so the form shows
// where the image will land instead of asking for a registry.
function ImportImageDialog({
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
  const [sourceReference, setSourceReference] = useState('');
  const [accelerator, setAccelerator] = useState('');
  const [engine, setEngine] = useState('');
  const [description, setDescription] = useState('');
  const [sourceUsername, setSourceUsername] = useState('');
  const [sourcePassword, setSourcePassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!accelerator) setAccelerator(fleet.vendors[0] || CPU_ACCELERATOR);
  }, [accelerator, fleet.vendors]);

  // The local name the server will derive: the image's last path segment,
  // in the Harbor project. Shown so the operator sees the result before the
  // transfer starts.
  const localName = (() => {
    const ref = sourceReference.trim();
    if (!ref) return '';
    const withoutTag = ref.includes(':') ? ref.slice(0, ref.lastIndexOf(':')) : ref;
    const leaf = withoutTag.split('/').filter(Boolean).pop() || '';
    return leaf;
  })();

  const submit = async () => {
    const ref = sourceReference.trim();
    if (!ref) {
      setError(t('images.validationSource'));
      return;
    }
    if (!engine.trim()) {
      setError(t('images.validationEngine'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const res = await api.post<{ reference?: string }>('/api/v1/admin/images:import', orgId, {
        sourceReference: ref,
        accelerator,
        engine: engine.trim(),
        description: description.trim(),
        sourceUsername: sourceUsername.trim() || undefined,
        sourcePassword: sourceUsername.trim() ? sourcePassword : undefined,
      });
      void res;
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('images.importFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  // The destination is a fact, not a placeholder: the platform's own registry
  // and project, as the server reports them.
  const destination = registry?.configured
    ? `${registry.hostname}/${registry.project}`
    : '';

  return (
    <Dialog title={t('images.importTitle')} onClose={onClose} testId="import-image-dialog">
      <p className="muted">
        {registry?.configured
          ? t('images.importHintConfigured', { registry: destination })
          : t('images.importHint')}
      </p>
      <div className="kv-grid settings-summary" data-testid="import-destination">
        <div>
          <span className="kv-label">{t('images.importDestinationLabel')}</span>
          <span className="mono settings-url">
            {destination || t('images.registryUnconfigured')}
          </span>
        </div>
      </div>
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="import-source">{t('images.sourceReference')}</label>
          <input
            id="import-source"
            className="mono"
            data-testid="import-source-input"
            value={sourceReference}
            onChange={(e) => setSourceReference(e.target.value)}
            placeholder="docker.io/library/a/b/hello:1.0.0"
          />
          <div className="field-hint">
            {localName && registry?.configured
              ? t('images.importTarget', {
                  name: localName,
                  registry: `${registry.hostname}/${registry.project}/`,
                })
              : registry?.configured
                ? t('images.importTargetHintNamed', { registry: `${registry.hostname}/${registry.project}/` })
                : t('images.registryUnconfigured')}
          </div>
        </div>
        <div className="form-field">
          <label htmlFor="import-accelerator">{t('images.fieldAccelerator')}</label>
          <select
            id="import-accelerator"
            data-testid="import-accelerator-select"
            value={accelerator}
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
          <label htmlFor="import-engine">{t('images.fieldEngine')}</label>
          <input
            id="import-engine"
            data-testid="import-engine-input"
            value={engine}
            maxLength={64}
            onChange={(e) => setEngine(e.target.value)}
            placeholder={t('images.placeholderEngine')}
          />
        </div>
        <div className="form-field">
          <label htmlFor="import-src-user">{t('images.sourceUsername')}</label>
          <input
            id="import-src-user"
            data-testid="import-source-username"
            value={sourceUsername}
            onChange={(e) => setSourceUsername(e.target.value)}
            placeholder={t('images.sourceAuthOptional')}
            autoComplete="off"
          />
        </div>
        <div className="form-field">
          <label htmlFor="import-src-pass">{t('images.sourcePassword')}</label>
          <input
            id="import-src-pass"
            type="password"
            data-testid="import-source-password"
            value={sourcePassword}
            onChange={(e) => setSourcePassword(e.target.value)}
            disabled={!sourceUsername.trim()}
            autoComplete="off"
          />
        </div>
        <div className="form-field full">
          <label htmlFor="import-desc">{t('common.descriptionOptional')}</label>
          <textarea
            id="import-desc"
            data-testid="import-description"
            rows={2}
            maxLength={1024}
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}

      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="submit-import" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('images.importing') : t('images.import')}
        </button>
      </div>
    </Dialog>
  );
}

function EditImageDialog({
  orgId,
  image,
  onClose,
  onDone,
}: {
  orgId: string;
  image: ImageSummary;
  onClose: () => void;
  onDone: () => void;
}) {
  const [description, setDescription] = useState(image.description || '');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.patch(`/api/v1/admin/images/${image.imageId}`, orgId, {
        description: description.trim(),
      });
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('images.updateFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={t('images.editTitle', { name: image.name, tag: image.tag })}
      onClose={onClose}
      testId="edit-image-dialog"
    >
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="edit-image-description">{t('common.description')}</label>
          <textarea
            id="edit-image-description"
            data-testid="edit-image-description-input"
            value={description}
            maxLength={1024}
            rows={3}
            onChange={(e) => setDescription(e.target.value)}
          />
          <div className="muted">{t('images.editNote')}</div>
        </div>
      </div>
      {error && <ErrorBanner message={error} />}

      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="edit-image-submit"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}

function DeleteImageDialog({
  orgId,
  image,
  onClose,
  onDone,
}: {
  orgId: string;
  image: ImageSummary;
  onClose: () => void;
  onDone: () => void;
}) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.del(`/api/v1/admin/images/${image.imageId}`, orgId);
      onDone();
    } catch (e) {
      // 10206: the image is referenced by inference services.
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('images.deleteFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={t('images.deleteTitle', { name: image.name, tag: image.tag })}
      onClose={onClose}
      testId="delete-image-dialog"
    >
      <p>{t('images.deleteBody')}</p>
      {error && <ErrorBanner message={error} />}

      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          className="danger"
          data-testid="delete-image-confirm"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? t('common.deleting') : t('common.delete')}
        </button>
      </div>
    </Dialog>
  );
}

function WarmupDialog({
  orgId,
  image,
  onClose,
  onDone,
}: {
  orgId: string;
  image: ImageSummary;
  onClose: () => void;
  onDone: () => void;
}) {
  // Default: warm every node (an empty selector). Filtering by a node
  // label is opt-in, because a wrong label key used to fail the task in
  // the controller after the fact.
  const [filterNodes, setFilterNodes] = useState(false);
  const [selectorKey, setSelectorKey] = useState('');
  const [selectorValue, setSelectorValue] = useState('');
  const [labelKeys, setLabelKeys] = useState<string[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  // Suggested selector keys: the accelerator labels the local cluster's
  // nodes actually carry (fetched once when the dialog opens).
  useEffect(() => {
    let alive = true;
    api
      .get<{ clusters?: { local?: boolean; nodeSelectorLabels?: string[] }[] }>(
        '/api/v1/admin/clusters',
        orgId,
      )
      .then((data) => {
        if (!alive) return;
        const labels = (data.clusters || [])
          .filter((c) => c.local)
          .flatMap((c) => c.nodeSelectorLabels || []);
        setLabelKeys(Array.from(new Set(labels)).sort());
      })
      .catch(() => {
        // Suggestions are best-effort; the fields stay free-text.
      });
    return () => {
      alive = false;
    };
  }, [api, orgId]);

  const submit = async () => {
    const selector: Record<string, string> = {};
    if (filterNodes) {
      const key = selectorKey.trim();
      const value = selectorValue.trim();
      if (!key || !value) {
        setError(t('images.warmupSelectorRequired'));
        return;
      }
      selector[key] = value;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.post(`/api/v1/admin/images/${image.imageId}:warmup`, orgId, {
        nodeSelector: selector,
      });
      onDone();
    } catch (e) {
      // 10205: a warmup task is already active for this image; 10204: the
      // selector matches no node (the message names the valid labels).
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('images.warmupFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={t('images.warmupTitle', { name: image.name, tag: image.tag })}
      onClose={onClose}
      testId="warmup-dialog"
    >
      <p className="muted">{t('images.warmupNote')}</p>
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="warmup-filter-nodes">
            <input
              id="warmup-filter-nodes"
              type="checkbox"
              data-testid="warmup-filter-nodes"
              checked={filterNodes}
              onChange={(e) => setFilterNodes(e.target.checked)}
            />{' '}
            {t('images.warmupFilterNodes')}
          </label>
          <div className="field-hint">{t('images.warmupFilterNodesHint')}</div>
        </div>
        {filterNodes && (
          <>
            <div className="form-field">
              <label htmlFor="warmup-selector-key">{t('images.fieldSelectorKey')}</label>
              <input
                id="warmup-selector-key"
                data-testid="warmup-selector-key-input"
                list="warmup-label-keys"
                value={selectorKey}
                onChange={(e) => setSelectorKey(e.target.value)}
                placeholder="nvidia.com/gpu.product"
              />
              <datalist id="warmup-label-keys">
                {labelKeys.map((k) => (
                  <option key={k} value={k} />
                ))}
              </datalist>
              <div className="field-hint">{t('images.warmupSelectorKeyHint')}</div>
            </div>
            <div className="form-field">
              <label htmlFor="warmup-selector-value">{t('images.fieldSelectorValue')}</label>
              <input
                id="warmup-selector-value"
                data-testid="warmup-selector-value-input"
                value={selectorValue}
                onChange={(e) => setSelectorValue(e.target.value)}
                placeholder="NVIDIA-GeForce-RTX-4090"
              />
            </div>
          </>
        )}
      </div>
      {error && <ErrorBanner message={error} />}

      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="warmup-submit"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? t('images.triggering') : t('images.triggerWarmup')}
        </button>
      </div>
    </Dialog>
  );
}
