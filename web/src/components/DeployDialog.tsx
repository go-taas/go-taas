// Deploy dialog: the one-click deployment form.
// Implements docs/design/model-catalog-deployment.md FR3 (accelerator →
// image filtering, name, card type, replicas) and navigates to the
// service detail page on success (FR3.3).
// Implements docs/design/model-authorization.md FR5.3 (AC14): the model
// picker is filtered by the current organization via
// ListModels?organization_id, so a restricted model the organization is
// not granted is not selectable.

import { useEffect, useMemo, useState } from 'react';
import {
  api,
  ApiError,
  type AutoscalingPolicy,
  type GetAutoscalingPolicyResponse,
  type ModelSummary,
} from '../api';
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

interface ListModelsResponse {
  response: { code: number; message: string };
  models?: ModelSummary[];
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
  const [authorizedModels, setAuthorizedModels] = useState<ModelSummary[]>([]);
  const [selectedModelId, setSelectedModelId] = useState(modelId);
  const [version, setVersion] = useState(initialVersion);
  const [accelerator, setAccelerator] = useState('nvidia');
  const [images, setImages] = useState<ImageEntry[]>([]);
  const [imageId, setImageId] = useState('');
  const [name, setName] = useState('');
  const [acceleratorType, setAcceleratorType] = useState('');
  const [replicas, setReplicas] = useState('1');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  // Feature #16 FR2.1: the autoscaling section, pre-filled from the
  // global default.
  const [autoscaling, setAutoscaling] = useState<AutoscalingPolicy>({
    enabled: true,
    minReplicas: 1,
    maxReplicas: 10,
    targetConcurrency: 32,
    scaleToZero: false,
    cooldownSeconds: 300,
  });
  const [autoscalingErrors, setAutoscalingErrors] = useState<Record<string, string>>({});

  useEffect(() => {
    // Load the global default policy to pre-fill the autoscaling section.
    api
      .get<GetAutoscalingPolicyResponse>('/api/v1/admin/autoscaling/policy', orgId)
      .then((data) => {
        if (data.policy) setAutoscaling(data.policy);
      })
      .catch(() => {
        // Keep the shipped defaults on failure.
      });
  }, [orgId]);

  useEffect(() => {
    // Load the image catalog once; the dropdown filters by accelerator.
    api
      .get<ImagesResponse>('/api/v1/admin/images', orgId)
      .then((data) => setImages(data.images || []))
      .catch(() => setImages([]));
  }, [orgId]);

  useEffect(() => {
    // FR5.3: the picker only offers models the current organization may
    // use. A failure leaves the dialog usable with the pre-selected model
    // (the server rejects an unauthorized deploy anyway).
    api
      .get<ListModelsResponse>(
        `/api/v1/admin/models?organization_id=${encodeURIComponent(orgId)}&page.limit=100`,
        orgId,
      )
      .then((data) => setAuthorizedModels(data.models || []))
      .catch(() => setAuthorizedModels([]));
  }, [orgId]);

  // When the picker has a list, the selection must be one of its entries:
  // a restricted model the organization is not granted disappears.
  useEffect(() => {
    if (authorizedModels.length === 0) return;
    if (authorizedModels.some((m) => m.modelId === selectedModelId)) return;
    const first = authorizedModels[0];
    setSelectedModelId(first.modelId);
    setVersion(first.latestVersion);
  }, [authorizedModels, selectedModelId]);

  const selected = useMemo(
    () => authorizedModels.find((m) => m.modelId === selectedModelId),
    [authorizedModels, selectedModelId],
  );
  const title = selected?.name ?? modelName;

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
    // Feature #16 FR1.3: validate the autoscaling section inline.
    const asErrs = validateAutoscaling(autoscaling);
    setAutoscalingErrors(asErrs);
    if (Object.keys(asErrs).length > 0) {
      setError('Fix the autoscaling fields.');
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const res = await api.post<CreateResponse>('/api/v1/admin/inference-services', orgId, {
        name: name.trim(),
        modelId: selectedModelId,
        modelVersion: version,
        imageId,
        accelerator,
        acceleratorType: acceleratorType.trim(),
        replicas: String(r),
        autoscaling,
      });
      onDeployed(res.serviceId);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to deploy');
    } finally {
      setSubmitting(false);
    }
  };

  const setAS = (patch: Partial<AutoscalingPolicy>) => {
    setAutoscaling((p) => ({ ...p, ...patch }));
    setAutoscalingErrors({});
  };

  return (
    <Dialog title={`Deploy ${title}`} onClose={onClose} testId="deploy-dialog">
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="deploy-model">Model (authorized for this organization)</label>
          <select
            id="deploy-model"
            data-testid="deploy-model-select"
            value={selectedModelId}
            onChange={(e) => {
              setSelectedModelId(e.target.value);
              const next = authorizedModels.find((m) => m.modelId === e.target.value);
              setVersion(next?.latestVersion || version);
            }}
          >
            {authorizedModels.length === 0 && (
              <option value={modelId}>{modelName}</option>
            )}
            {authorizedModels.map((m) => (
              <option key={m.modelId} value={m.modelId}>
                {m.name}
                {m.restricted ? ' (restricted)' : ''}
              </option>
            ))}
          </select>
        </div>
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

      <h4 style={{ marginBottom: 8 }}>Autoscaling</h4>
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="deploy-as-enabled">Enabled</label>
          <input
            id="deploy-as-enabled"
            data-testid="deploy-as-enabled"
            type="checkbox"
            checked={autoscaling.enabled}
            onChange={(e) => setAS({ enabled: e.target.checked })}
          />
        </div>
        <div className="form-field">
          <label htmlFor="deploy-as-min">Min replicas</label>
          <input
            id="deploy-as-min"
            data-testid="deploy-as-min"
            type="number"
            min={0}
            max={100}
            disabled={!autoscaling.enabled}
            value={autoscaling.minReplicas}
            onChange={(e) => setAS({ minReplicas: parseInt(e.target.value, 10) || 0 })}
          />
          {autoscalingErrors.minReplicas && (
            <div className="field-error">{autoscalingErrors.minReplicas}</div>
          )}
        </div>
        <div className="form-field">
          <label htmlFor="deploy-as-max">Max replicas</label>
          <input
            id="deploy-as-max"
            data-testid="deploy-as-max"
            type="number"
            min={1}
            max={100}
            disabled={!autoscaling.enabled}
            value={autoscaling.maxReplicas}
            onChange={(e) => setAS({ maxReplicas: parseInt(e.target.value, 10) || 0 })}
          />
          {autoscalingErrors.maxReplicas && (
            <div className="field-error">{autoscalingErrors.maxReplicas}</div>
          )}
        </div>
        <div className="form-field">
          <label htmlFor="deploy-as-target">Target concurrency</label>
          <input
            id="deploy-as-target"
            data-testid="deploy-as-target"
            type="number"
            min={1}
            max={1000}
            disabled={!autoscaling.enabled}
            value={autoscaling.targetConcurrency}
            onChange={(e) => setAS({ targetConcurrency: parseInt(e.target.value, 10) || 0 })}
          />
          {autoscalingErrors.targetConcurrency && (
            <div className="field-error">{autoscalingErrors.targetConcurrency}</div>
          )}
        </div>
        <div className="form-field">
          <label htmlFor="deploy-as-scale-to-zero">Scale to zero</label>
          <input
            id="deploy-as-scale-to-zero"
            data-testid="deploy-as-scale-to-zero"
            type="checkbox"
            disabled={!autoscaling.enabled || autoscaling.minReplicas !== 0}
            checked={autoscaling.scaleToZero}
            onChange={(e) => setAS({ scaleToZero: e.target.checked })}
          />
          {autoscalingErrors.scaleToZero && (
            <div className="field-error">{autoscalingErrors.scaleToZero}</div>
          )}
        </div>
        <div className="form-field">
          <label htmlFor="deploy-as-cooldown">Cooldown seconds</label>
          <input
            id="deploy-as-cooldown"
            data-testid="deploy-as-cooldown"
            type="number"
            min={0}
            max={3600}
            disabled={!autoscaling.enabled}
            value={autoscaling.cooldownSeconds}
            onChange={(e) => setAS({ cooldownSeconds: parseInt(e.target.value, 10) || 0 })}
          />
          {autoscalingErrors.cooldownSeconds && (
            <div className="field-error">{autoscalingErrors.cooldownSeconds}</div>
          )}
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

// validateAutoscaling mirrors the server-side validation matrix
// (feature #16, §5.3).
function validateAutoscaling(p: AutoscalingPolicy): Record<string, string> {
  const errs: Record<string, string> = {};
  if (p.minReplicas > p.maxReplicas) errs.maxReplicas = 'Max replicas must be ≥ min replicas and ≤ 100.';
  if (p.minReplicas === 0 && !p.scaleToZero) errs.minReplicas = 'Min replicas must be 0 to enable scale-to-zero.';
  if (p.scaleToZero && p.minReplicas !== 0) errs.scaleToZero = 'Set min replicas to 0 to enable scale-to-zero.';
  if (p.targetConcurrency < 1 || p.targetConcurrency > 1000) errs.targetConcurrency = 'Target concurrency must be between 1 and 1000.';
  if (p.cooldownSeconds < 0 || p.cooldownSeconds > 3600) errs.cooldownSeconds = 'Cooldown must be between 0 and 3600 seconds.';
  if (p.maxReplicas < 1 || p.maxReplicas > 100) errs.maxReplicas = 'Max replicas must be ≥ min replicas and ≤ 100.';
  return errs;
}
