// Images page: the engine image catalog with accelerator/engine filters,
// register, edit, delete and warmup actions.
// Implements docs/design/image-management.md FR1, FR2, AC12, AC13.

import { useCallback, useEffect, useState } from 'react';
import { navigate } from '../router';
import { api, ApiError, formatTime, type ImageSummary, type PageMeta } from '../api';
import { useOrg } from '../org';
import { Dialog, ErrorBanner, Pagination, StateBadge } from '../components';

interface ListResponse {
  response: { code: number; message: string };
  images: ImageSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;
const ACCELERATORS = ['all', 'nvidia', 'iluvatar', 'metax'];

export default function ImagesPage() {
  const { orgId } = useOrg();
  const [images, setImages] = useState<ImageSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [accelerator, setAccelerator] = useState('all');
  const [engine, setEngine] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [registerOpen, setRegisterOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<ImageSummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ImageSummary | null>(null);
  const [warmupTarget, setWarmupTarget] = useState<ImageSummary | null>(null);

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
      setError(e instanceof Error ? e.message : 'failed to load images');
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, accelerator, engine]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Images</h1>
          <div className="subtitle">
            Engine images available for one-click deployment, with warmup
            pre-pull support.
          </div>
        </div>
        <button data-testid="register-image" onClick={() => setRegisterOpen(true)}>
          Register Image
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <select
          data-testid="image-accelerator-filter"
          value={accelerator}
          onChange={(e) => {
            setAccelerator(e.target.value);
            setOffset(0);
          }}
        >
          {ACCELERATORS.map((a) => (
            <option key={a} value={a}>
              {a === 'all' ? 'All accelerators' : a}
            </option>
          ))}
        </select>
        <input
          type="text"
          data-testid="image-engine-filter"
          placeholder="Filter by engine…"
          value={engine}
          onChange={(e) => {
            setEngine(e.target.value);
            setOffset(0);
          }}
        />
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">Loading…</div>
        ) : images.length === 0 ? (
          <div className="empty-state" data-testid="images-empty">
            No images match your filters.
          </div>
        ) : (
          <table className="data" data-testid="images-table">
            <thead>
              <tr>
                <th>Image</th>
                <th>Accelerator</th>
                <th>Engine</th>
                <th>In use</th>
                <th>Last warmup</th>
                <th>Created</th>
                <th>Actions</th>
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
                      Warmup
                    </button>
                    <button
                      className="link"
                      data-testid={`edit-${img.name}`}
                      onClick={() => setEditTarget(img)}
                    >
                      Edit
                    </button>
                    <button
                      className="link danger"
                      data-testid={`delete-${img.name}`}
                      onClick={() => setDeleteTarget(img)}
                    >
                      Delete
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

      {registerOpen && (
        <RegisterImageDialog
          orgId={orgId}
          onClose={() => setRegisterOpen(false)}
          onDone={() => {
            setRegisterOpen(false);
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

function RegisterImageDialog({
  orgId,
  onClose,
  onDone,
}: {
  orgId: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const [name, setName] = useState('');
  const [tag, setTag] = useState('');
  const [digest, setDigest] = useState('');
  const [accelerator, setAccelerator] = useState('nvidia');
  const [engine, setEngine] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    if (!name.trim()) {
      setError('Name is required (e.g. ghcr.io/go-taas/vllm).');
      return;
    }
    if (!tag.trim()) {
      setError('Tag is required (e.g. v0.6.3).');
      return;
    }
    if (digest.trim() && !/^sha256:[a-fA-F0-9]{64}$/.test(digest.trim())) {
      setError('Digest must look like sha256:<64 hex chars>.');
      return;
    }
    if (!engine.trim()) {
      setError('Engine is required (e.g. vllm).');
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.post('/api/v1/admin/images', orgId, {
        name: name.trim(),
        tag: tag.trim(),
        digest: digest.trim() || undefined,
        accelerator,
        engine: engine.trim(),
        description: description.trim(),
      });
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to register image');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title="Register Image" onClose={onClose} testId="register-image-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="image-name">Name</label>
          <input
            id="image-name"
            data-testid="image-name-input"
            value={name}
            maxLength={255}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. ghcr.io/go-taas/vllm"
          />
        </div>
        <div className="form-field">
          <label htmlFor="image-tag">Tag</label>
          <input
            id="image-tag"
            data-testid="image-tag-input"
            value={tag}
            maxLength={128}
            onChange={(e) => setTag(e.target.value)}
            placeholder="e.g. v0.6.3"
          />
        </div>
        <div className="form-field full">
          <label htmlFor="image-digest">Digest (optional)</label>
          <input
            id="image-digest"
            data-testid="image-digest-input"
            value={digest}
            onChange={(e) => setDigest(e.target.value)}
            placeholder="e.g. sha256:abcd…"
          />
        </div>
        <div className="form-field">
          <label htmlFor="image-accelerator">Accelerator</label>
          <select
            id="image-accelerator"
            data-testid="image-accelerator-select"
            value={accelerator}
            onChange={(e) => setAccelerator(e.target.value)}
          >
            {ACCELERATORS.filter((a) => a !== 'all').map((a) => (
              <option key={a} value={a}>
                {a}
              </option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="image-engine">Engine</label>
          <input
            id="image-engine"
            data-testid="image-engine-input"
            value={engine}
            maxLength={64}
            onChange={(e) => setEngine(e.target.value)}
            placeholder="e.g. vllm"
          />
        </div>
        <div className="form-field full">
          <label htmlFor="image-description">Description (optional)</label>
          <textarea
            id="image-description"
            data-testid="image-description-input"
            value={description}
            maxLength={1024}
            rows={3}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          data-testid="register-image-submit"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? 'Registering…' : 'Register'}
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

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.patch(`/api/v1/admin/images/${image.imageId}`, orgId, {
        description: description.trim(),
      });
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to update image');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={`Edit ${image.name}:${image.tag}`} onClose={onClose} testId="edit-image-dialog">
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="edit-image-description">Description</label>
          <textarea
            id="edit-image-description"
            data-testid="edit-image-description-input"
            value={description}
            maxLength={1024}
            rows={3}
            onChange={(e) => setDescription(e.target.value)}
          />
          <div className="muted">Only the description is editable.</div>
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          data-testid="edit-image-submit"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? 'Saving…' : 'Save'}
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

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.del(`/api/v1/admin/images/${image.imageId}`, orgId);
      onDone();
    } catch (e) {
      // 10206: the image is referenced by inference services.
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to delete image');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={`Delete ${image.name}:${image.tag}?`} onClose={onClose} testId="delete-image-dialog">
      <p>
        This removes the image from the catalog. Inference services
        referencing it must be deleted first.
      </p>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          className="danger"
          data-testid="delete-image-confirm"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? 'Deleting…' : 'Delete'}
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
  const [selectorKey, setSelectorKey] = useState(
    'taas.go-taas.github.io/accelerator',
  );
  const [selectorValue, setSelectorValue] = useState(image.accelerator);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.post(`/api/v1/admin/images/${image.imageId}:warmup`, orgId, {
        nodeSelector: { [selectorKey.trim()]: selectorValue.trim() },
      });
      onDone();
    } catch (e) {
      // 10205: a warmup task is already active for this image.
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to trigger warmup');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={`Warmup ${image.name}:${image.tag}`} onClose={onClose} testId="warmup-dialog">
      <p className="muted">
        Pre-pull the image on nodes matching the selector. The controller
        runs a short helper pod per matching node.
      </p>
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="warmup-selector-key">Node selector key</label>
          <input
            id="warmup-selector-key"
            data-testid="warmup-selector-key-input"
            value={selectorKey}
            onChange={(e) => setSelectorKey(e.target.value)}
          />
        </div>
        <div className="form-field">
          <label htmlFor="warmup-selector-value">Node selector value</label>
          <input
            id="warmup-selector-value"
            data-testid="warmup-selector-value-input"
            value={selectorValue}
            onChange={(e) => setSelectorValue(e.target.value)}
          />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          data-testid="warmup-submit"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? 'Triggering…' : 'Trigger Warmup'}
        </button>
      </div>
    </Dialog>
  );
}
