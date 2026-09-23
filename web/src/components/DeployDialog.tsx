// Deploy dialog: the one-click deployment form.
// Implements docs/design/model-catalog-deployment.md FR3 (accelerator →
// image filtering, name, card type, replicas) and navigates to the
// service detail page on success (FR3.3).

import { useEffect, useState } from 'react';
import { api, ApiError } from '../api';
import { Dialog, ErrorBanner } from '../components';

interface ImageEntry {
  imageId: string;
  name: string;
  tag: string;
  accelerator: string;
  engine: string;
}

interface ImagesResponse {
  response: { code: number; message: string };
  images: ImageEntry[];
}

interface CreateResponse {
  response: { code: number; message: string };
  serviceId: string;
}

const ACCELERATORS = ['nvidia', 'iluvatar', 'metax'];

export default function DeployDialog({
  orgId,
  modelId,
  modelName,
  initialVersion,
  onClose,
  onDeployed,
}: {
  orgId: string;
  modelId: string;
  modelName: string;
  initialVersion: string;
  onClose: () => void;
  onDeployed: (serviceId: string) => void;
}) {
  const [version, setVersion] = useState(initialVersion);
  const [accelerator, setAccelerator] = useState('nvidia');
  const [images, setImages] = useState<ImageEntry[]>([]);
  const [imageId, setImageId] = useState('');
  const [name, setName] = useState('');
  const [acceleratorType, setAcceleratorType] = useState('');
  const [replicas, setReplicas] = useState('1');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    // Load the image catalog once; the dropdown filters by accelerator.
    api
      .get<ImagesResponse>('/api/v1/images', orgId)
      .then((data) => setImages(data.images || []))
      .catch(() => setImages([]));
  }, [orgId]);

  // FR3.2: image dropdown filtered by the chosen accelerator.
  const compatible = images.filter((i) => i.accelerator === accelerator);

  useEffect(() => {
    if (compatible.length > 0 && !compatible.some((i) => i.imageId === imageId)) {
      setImageId(compatible[0].imageId);
    }
  }, [compatible, imageId]);

  const submit = async () => {
    if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name.trim())) {
      setError(
        'Service name must be 1-63 chars: lowercase letters, digits and hyphens; it must not start or end with a hyphen.',
      );
      return;
    }
    const r = parseInt(replicas, 10);
    if (!Number.isInteger(r) || r < 1 || r > 100) {
      setError('Replicas must be an integer between 1 and 100.');
      return;
    }
    if (!imageId) {
      setError('Choose an image compatible with the accelerator.');
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const res = await api.post<CreateResponse>('/api/v1/inference-services', orgId, {
        name: name.trim(),
        modelId,
        modelVersion: version,
        imageId,
        accelerator,
        acceleratorType: acceleratorType.trim(),
        replicas: String(r),
      });
      onDeployed(res.serviceId);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to deploy');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={`Deploy ${modelName}`} onClose={onClose} testId="deploy-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="deploy-version">Model version</label>
          <input
            id="deploy-version"
            data-testid="deploy-version-input"
            value={version}
            onChange={(e) => setVersion(e.target.value)}
          />
        </div>
        <div className="form-field">
          <label htmlFor="deploy-accelerator">Accelerator</label>
          <select
            id="deploy-accelerator"
            data-testid="deploy-accelerator-select"
            value={accelerator}
            onChange={(e) => setAccelerator(e.target.value)}
          >
            {ACCELERATORS.map((a) => (
              <option key={a} value={a}>
                {a}
              </option>
            ))}
          </select>
        </div>
        <div className="form-field full">
          <label htmlFor="deploy-image">Image (filtered by accelerator)</label>
          <select
            id="deploy-image"
            data-testid="deploy-image-select"
            value={imageId}
            onChange={(e) => setImageId(e.target.value)}
          >
            {compatible.length === 0 && <option value="">No compatible image</option>}
            {compatible.map((i) => (
              <option key={i.imageId} value={i.imageId}>
                {i.name}:{i.tag} ({i.engine})
              </option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="deploy-name">Service name</label>
          <input
            id="deploy-name"
            data-testid="deploy-name-input"
            value={name}
            maxLength={63}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. qwen-32b-prod"
          />
        </div>
        <div className="form-field">
          <label htmlFor="deploy-card-type">Card type (optional)</label>
          <input
            id="deploy-card-type"
            data-testid="deploy-card-type-input"
            value={acceleratorType}
            maxLength={64}
            onChange={(e) => setAcceleratorType(e.target.value)}
            placeholder="e.g. A800"
          />
        </div>
        <div className="form-field">
          <label htmlFor="deploy-replicas">Replicas (1-100)</label>
          <input
            id="deploy-replicas"
            data-testid="deploy-replicas-input"
            type="number"
            min={1}
            max={100}
            value={replicas}
            onChange={(e) => setReplicas(e.target.value)}
          />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          data-testid="submit-deploy"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? 'Deploying…' : 'Deploy'}
        </button>
      </div>
    </Dialog>
  );
}
