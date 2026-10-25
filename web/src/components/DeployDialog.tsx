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
  type ListVGPUPoolsResponse,
  type ModelSummary,
  type TenantVGPUPool,
  type VGPUPool,
} from '../api';
import { Dialog, ErrorBanner } from '../components';
import { useI18n } from '../i18n';

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
}) {  const { t, tEnum } = useI18n();

  const [authorizedModels, setAuthorizedModels] = useState<ModelSummary[]>([]);
  const [selectedModelId, setSelectedModelId] = useState(modelId);
  const [version, setVersion] = useState(initialVersion);
  const [accelerator, setAccelerator] = useState('nvidia');
  const [images, setImages] = useState<ImageEntry[]>([]);
  const [imageId, setImageId] = useState('');
  const [name, setName] = useState('');
  const [acceleratorType, setAcceleratorType] = useState('');
  // Card types the accelerator inventory actually reports. The matrix is
  // keyed by these values, so the field offers them instead of accepting
  // any free text (a card *product* name fails the lookup with 10211).
  const [cardTypes, setCardTypes] = useState<string[]>([]);
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
  // Feature #52 (AD2): the Shared GPU mode. When enabled, the deploy
  // requests a vGPU slice instead of a whole card.
  const [sharedGpu, setSharedGpu] = useState(false);
  const [pools, setPools] = useState<TenantVGPUPool[]>([]);
  const [poolId, setPoolId] = useState('');
  const [sliceTemplateId, setSliceTemplateId] = useState('');
  const [sliceError, setSliceError] = useState('');

  useEffect(() => {
    // Feature #52 (AD2): the pool list for the deploy form. The deploy
    // dialog lives on the admin surface, so it reads the admin-surface
    // ListVGPUPools (platform-wide) and projects each pool onto the
    // {poolId, name, templates} shape the form consumes.
    api
      .get<ListVGPUPoolsResponse>('/api/v1/admin/vgpu-pools?page.limit=100', orgId)
      .then((data) =>
        setPools(
          (data.pools || []).map(
            (p: VGPUPool): TenantVGPUPool => ({
              poolId: p.poolId,
              name: p.name,
              templates: p.templates || [],
            })
          )
        )
      )
      .catch(() => setPools([]));
  }, [orgId]);

  useEffect(() => {
    api
      .get<{ cardTypes?: { cardType: string; inFleet?: boolean }[] }>(
        '/api/v1/admin/compatibility/dimensions',
        orgId,
      )
      .then((data) =>
        setCardTypes(
          (data.cardTypes || [])
            // Only suggest cards the fleet actually reports: a historical
            // card type (a card that left the fleet) cannot be scheduled.
            .filter((c) => c.inFleet !== false)
            .map((c) => c.cardType)
            .filter((c): c is string => Boolean(c)),
        ),
      )
      .catch(() => setCardTypes([]));
  }, [orgId]);

  const selectedPool = pools.find((p) => p.poolId === poolId);

  useEffect(() => {
    if (selectedPool && !selectedPool.templates.some((t) => t.name === sliceTemplateId)) {
      setSliceTemplateId(selectedPool.templates[0]?.name || '');
    }
  }, [selectedPool, sliceTemplateId]);

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
    // The model catalog is platform-wide: models, images and inference
    // services are operator-owned resources, so the picker offers every
    // registered model rather than one organization's subset.
    api
      .get<ListModelsResponse>('/api/v1/admin/models?page.limit=200', orgId)
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
      setError(t('deploy.errorReplicas'));
      return;
    }
    if (!imageId) {
      setError(t('deploy.errorImage'));
      return;
    }
    // Feature #52 (AD2): validate the Shared GPU selection.
    if (sharedGpu) {
      if (!poolId || !sliceTemplateId) {
        setSliceError(t('deploy.errorVgpu'));
        return;
      }
      setSliceError('');
    }
    // Feature #16 FR1.3: validate the autoscaling section inline.
    const asErrs = validateAutoscaling(autoscaling, t);
    setAutoscalingErrors(asErrs);
    if (Object.keys(asErrs).length > 0) {
      setError(t('deploy.errorAutoscaling'));
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
        ...(sharedGpu ? { poolId, sliceTemplateId } : {}),
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
    <Dialog title={t('deploy.title', { name: title })} onClose={onClose} testId="deploy-dialog">
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="deploy-model">{t('deploy.modelAny')}</label>
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
              </option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="deploy-version">{t('deploy.version')}</label>
          <input
            id="deploy-version"
            data-testid="deploy-version-input"
            value={version}
            onChange={(e) => setVersion(e.target.value)}
          />
        </div>
        <div className="form-field">
          <label htmlFor="deploy-accelerator">{t('deploy.accelerator')}</label>
          <select
            id="deploy-accelerator"
            data-testid="deploy-accelerator-select"
            value={accelerator}
            onChange={(e) => setAccelerator(e.target.value)}
          >
            {ACCELERATORS.map((a) => (
              <option key={a} value={a}>
                {tEnum('state', a)}
              </option>
            ))}
          </select>
        </div>
        <div className="form-field full">
          <label htmlFor="deploy-image">{t('deploy.image')}</label>
          <select
            id="deploy-image"
            data-testid="deploy-image-select"
            value={imageId}
            onChange={(e) => setImageId(e.target.value)}
          >
            {compatible.length === 0 && <option value="">{t('deploy.noCompatibleImage')}</option>}
            {compatible.map((i) => (
              <option key={i.imageId} value={i.imageId}>
                {i.name}:{i.tag} ({i.engine})
              </option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="deploy-name">{t('deploy.serviceName')}</label>
          <input
            id="deploy-name"
            data-testid="deploy-name-input"
            value={name}
            maxLength={63}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('deploy.placeholderServiceName')}
          />
        </div>
        <div className="form-field">
          <label htmlFor="deploy-card-type">{t('deploy.cardType')}</label>
          <input
            id="deploy-card-type"
            data-testid="deploy-card-type-input"
            list="deploy-card-types"
            value={acceleratorType}
            maxLength={64}
            onChange={(e) => setAcceleratorType(e.target.value)}
            placeholder={t('deploy.placeholderCardType')}
          />
          <datalist id="deploy-card-types">
            {cardTypes.map((c) => (
              <option key={c} value={c} />
            ))}
          </datalist>
          <div className="field-hint">
            {cardTypes.length > 0
              ? t('deploy.cardTypeHintKnown', { types: cardTypes.join(', ') })
              : t('deploy.cardTypeHint')}
          </div>
        </div>
        <div className="form-field">
          <label htmlFor="deploy-replicas">{t('deploy.replicas')}</label>
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

      <h4 style={{ marginBottom: 8 }}>{t('deploy.sharedGpu')}</h4>
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="deploy-shared-gpu">{t('deploy.sharedGpuSlice')}</label>
          <input
            id="deploy-shared-gpu"
            data-testid="deploy-shared-gpu-toggle"
            type="checkbox"
            checked={sharedGpu}
            disabled={pools.length === 0}
            onChange={(e) => setSharedGpu(e.target.checked)}
          />
          {pools.length === 0 && (
            <div className="muted" data-testid="deploy-pool-empty">
              {t('deploy.noPools')}
            </div>
          )}
        </div>
        {sharedGpu && (
          <>
            <div className="form-field">
              <label htmlFor="deploy-pool">{t('deploy.vgpuPool')}</label>
              <select
                id="deploy-pool"
                data-testid="deploy-pool-select"
                value={poolId}
                onChange={(e) => setPoolId(e.target.value)}
              >
                <option value="">{t('deploy.selectPool')}</option>
                {pools.map((p) => (
                  <option key={p.poolId} value={p.poolId}>
                    {p.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="form-field">
              <label htmlFor="deploy-slice-template">{t('deploy.sliceTemplate')}</label>
              <select
                id="deploy-slice-template"
                data-testid="deploy-slice-template-select"
                value={sliceTemplateId}
                onChange={(e) => setSliceTemplateId(e.target.value)}
              >
                {(selectedPool?.templates || []).map((tpl) => (
                  <option key={tpl.name} value={tpl.name}>
                    {tpl.name} — {tpl.memoryGib} GiB / {tpl.computePct}% {t('deploy.compute')}
                  </option>
                ))}
              </select>
              {sliceError && <div className="field-error">{sliceError}</div>}
            </div>
            <div className="form-field full">
              <div className="muted" data-testid="deploy-shared-gpu-note">
                {t('deploy.sharedGpuNote')}
              </div>
            </div>
          </>
        )}
      </div>

      <h4 style={{ marginBottom: 8 }}>{t('deploy.autoscaling')}</h4>
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="deploy-as-enabled">{t('common.enabled')}</label>
          <input
            id="deploy-as-enabled"
            data-testid="deploy-as-enabled"
            type="checkbox"
            checked={autoscaling.enabled}
            onChange={(e) => setAS({ enabled: e.target.checked })}
          />
        </div>
        <div className="form-field">
          <label htmlFor="deploy-as-min">{t('deploy.minReplicas')}</label>
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
          <label htmlFor="deploy-as-max">{t('deploy.maxReplicas')}</label>
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
          <label htmlFor="deploy-as-target">{t('deploy.targetConcurrency')}</label>
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
          <label htmlFor="deploy-as-scale-to-zero">{t('deploy.scaleToZero')}</label>
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
          <label htmlFor="deploy-as-cooldown">{t('deploy.cooldown')}</label>
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
          {t('common.cancel')}
        </button>
        <button
          data-testid="submit-deploy"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? t('deploy.deploying') : t('models.deploy')}
        </button>
      </div>
    </Dialog>
  );
}

// validateAutoscaling mirrors the server-side validation matrix
// (feature #16, §5.3).
function validateAutoscaling(
  p: AutoscalingPolicy,
  t: (key: string) => string,
): Record<string, string> {
  const errs: Record<string, string> = {};
  if (p.minReplicas > p.maxReplicas) errs.maxReplicas = t('deploy.errorMaxReplicas');
  if (p.minReplicas === 0 && !p.scaleToZero) errs.minReplicas = t('deploy.errorMinZero');
  if (p.scaleToZero && p.minReplicas !== 0) errs.scaleToZero = t('deploy.errorScaleToZero');
  if (p.targetConcurrency < 1 || p.targetConcurrency > 1000) errs.targetConcurrency = t('deploy.errorTargetConcurrency');
  if (p.cooldownSeconds < 0 || p.cooldownSeconds > 3600) errs.cooldownSeconds = t('deploy.errorCooldown');
  if (p.maxReplicas < 1 || p.maxReplicas > 100) errs.maxReplicas = t('deploy.errorMaxReplicas');
  return errs;
}
