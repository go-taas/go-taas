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
import { Dialog, ErrorBanner, Pagination, RowActionsMenu, StateBadge, Toast } from '../components';
import { useI18n } from '../i18n';
import { FieldLabel } from '../components/FormField';
import { CPU_ACCELERATOR, useFleetAccelerators } from '../hooks/useFleetAccelerators';
import ImageImportPanel from '../components/ImageImportPanel';
import ImageUploadPanel from '../components/ImageUploadPanel';

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
  const [notice, setNotice] = useState('');
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
            onClick={() => {
              setImportOpen((open) => {
                const next = !open;
                if (next) setUploadOpen(false);
                return next;
              });
            }}
          >
            {importOpen ? t('images.importHide') : t('images.importOpen')}
          </button>
          <button
            className="secondary"
            data-testid="upload-image"
            disabled={registry !== null && !registry.configured}
            title={registry && !registry.configured ? t('images.registryUnconfigured') : undefined}
            onClick={() => {
              if (uploadOpen) {
                setUploadOpen(false);
                return;
              }
              setImportOpen(false);
              setUploadOpen(true);
            }}
          >
            {uploadOpen ? t('images.uploadHide') : t('images.uploadOpen')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}
      <Toast message={notice} onDismiss={() => setNotice('')} testId="images-toast" />

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

      {importOpen && (
        <div className="downloads-section">
          <ImageImportPanel
            defaultOpen
            registry={registry}
            onDone={() => {
              setNotice(t('images.importSuccess'));
              void load();
            }}
          />
        </div>
      )}

      {/* Kept mounted while collapsed: the panel is only a view, so hiding it
          must not abort a running transfer — collapsing then reopening shows
          the progress again. */}
      <div className="downloads-section" style={{ display: uploadOpen ? undefined : 'none' }}>
        <ImageUploadPanel
          defaultOpen
          active={uploadOpen}
          registry={registry}
          onDone={() => {
            setNotice(t('images.uploadSuccess'));
            void load();
          }}
        />
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
                <th className="col-actions">{t('common.actions')}</th>
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
                    <RowActionsMenu
                      label={t('common.actionsFor', { name: img.name })}
                      testId={`image-actions-${img.name}`}
                      actions={[
                        {
                          key: `warmup-${img.name}`,
                          label: t('images.warmup'),
                          onClick: () => setWarmupTarget(img),
                        },
                        {
                          key: `edit-${img.name}`,
                          label: t('common.edit'),
                          onClick: () => setEditTarget(img),
                        },
                        {
                          key: `delete-${img.name}`,
                          label: t('common.delete'),
                          danger: true,
                          onClick: () => setDeleteTarget(img),
                        },
                      ]}
                    />
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

      {editTarget && (
        <EditImageDialog
          orgId={orgId}
          image={editTarget}
          onClose={() => setEditTarget(null)}
          onDone={() => {
            const name = editTarget.name;
            setEditTarget(null);
            setNotice(t('images.updateSuccess', { name }));
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
            const name = deleteTarget.name;
            setDeleteTarget(null);
            setNotice(t('images.deleteSuccess', { name }));
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
            const name = warmupTarget.name;
            setWarmupTarget(null);
            setNotice(t('images.warmupSuccess', { name }));
            void load();
          }}
        />
      )}
    </div>
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
          <FieldLabel
            htmlFor="edit-image-description"
            text={t('common.description')}
            requirement="optional"
          />
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
