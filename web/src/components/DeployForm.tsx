// Deploy form: the inline create-service panel (no dialog: the inference
// services page expands it in place, and the model list links here with the
// model preselected).
// Implements docs/design/model-catalog-deployment.md FR3 (accelerator →
// image filtering, name, card type, replicas) and navigates to the
// service detail page on success (FR3.3).
// Implements docs/design/model-authorization.md FR5.3 (AC14): the model
// picker is filtered by the current organization via
// ListModels?organization_id, so a restricted model the organization is
// not granted is not selectable.

import { useEffect, useMemo, useRef, useState } from 'react';
import {
  api,
  ApiError,
  type AutoscalingPolicy,
  type GetAutoscalingPolicyResponse,
  type ListVGPUPoolsResponse,
  type EngineParamTemplate,
  type ModelSummary,
  type ServingTopology,
  type TenantVGPUPool,
  type VGPUPool,
} from '../api';
import { Dialog, ErrorBanner } from '../components';
import { useI18n } from '../i18n';
import { engineParams, type EngineParam } from './engineParams';
import { CPU_ACCELERATOR, useFleetAccelerators } from '../hooks/useFleetAccelerators';

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

// The accelerator vendors and card types come from the live fleet (the
// accelerator inventory), never from a constant: offering hardware the
// cluster does not have only produces an unschedulable pod.

// RoleArgState is one role's own engine parameters.
interface RoleArgState {
  args: Record<string, string>;
  extra: string[];
}

// rolesForTopology returns the roles the chosen layout runs. A single-node
// deploy has the worker role only, so it needs no per-role section.
function rolesForTopology(topology: ServingTopology): string[] {
  switch (topology) {
    case 'pd':
      return ['prefill', 'decode'];
    case 'multinode':
      return ['worker'];
    default:
      return [];
  }
}

// roleArgPayload turns the per-role state into the wire list, dropping roles
// the operator left empty so the request carries only real overrides.
function roleArgPayload(state: Record<string, RoleArgState>): Array<{
  role: string;
  engineArgs: Record<string, string>;
  extraArgs: string[];
}> {
  return Object.entries(state)
    .map(([role, value]) => ({
      role,
      engineArgs: Object.fromEntries(
        Object.entries(value.args).filter(([, v]) => v !== undefined && v !== ''),
      ),
      extraArgs: value.extra.map((a) => a.trim()).filter(Boolean),
    }))
    .filter((entry) => Object.keys(entry.engineArgs).length > 0 || entry.extraArgs.length > 0);
}

// topologySummary renders how many pods the layout will create, so an
// operator sees the footprint before deploying: a PD storm runs prefill and
// decode pods per replica, a distributed one runs one pod group per replica.
function topologySummary(
  t: (key: string, vars?: Record<string, string | number>) => string,
  topology: ServingTopology,
  replicas: number,
  tp: number,
  pp: number,
  prefill: number,
  decode: number,
): string {
  if (topology === 'pd') {
    // Each role runs one pod per worker, so the pod count is the role counts
    // times the shard count.
    const podsPerReplica = (prefill + decode) * tp * pp;
    return t('deploy.topologySummaryPd', {
      replicas: String(replicas),
      prefill: String(prefill),
      decode: String(decode),
      pods: String(podsPerReplica * replicas),
      cards: String(tp * pp),
    });
  }
  return t('deploy.topologySummaryMultiNode', {
    replicas: String(replicas),
    pods: String(replicas * tp * pp),
    cards: String(tp * pp),
  });
}

export default function DeployForm({
  orgId,
  modelId,
  modelName,
  initialVersion,
  onCancel,
  onDeployed,
}: {
  orgId: string;
  modelId: string;
  modelName: string;
  initialVersion: string;
  onCancel: () => void;
  onDeployed: (serviceId: string) => void;
}) {  const { t } = useI18n();

  const [authorizedModels, setAuthorizedModels] = useState<ModelSummary[]>([]);
  const [selectedModelId, setSelectedModelId] = useState(modelId);
  const [version, setVersion] = useState(initialVersion);
  const [accelerator, setAccelerator] = useState('nvidia');
  const [images, setImages] = useState<ImageEntry[]>([]);
  const [imageId, setImageId] = useState('');
  const [name, setName] = useState('');
  const [acceleratorType, setAcceleratorType] = useState('');
  const [replicas, setReplicas] = useState('1');
  // Serving topology (single-node / distributed / PD). Every layout is one
  // AIBrix StormService; the topology decides its roles.
  const [topology, setTopology] = useState<ServingTopology>('single');
  const [tensorParallel, setTensorParallel] = useState('1');
  const [pipelineParallel, setPipelineParallel] = useState('1');
  const [prefillReplicas, setPrefillReplicas] = useState('1');
  const [decodeReplicas, setDecodeReplicas] = useState('1');
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
  // Engine parameters: the catalog's values plus the free-form rows. The
  // engine is a property of the chosen image, so the catalog follows it.
  const [engineArgs, setEngineArgs] = useState<Record<string, string>>({});
  const [extraArgs, setExtraArgs] = useState<string[]>([]);
  // Per-role engine parameters: a multi-machine or PD layout runs several
  // roles, and each may need its own tuning (memory fraction, sequence
  // limits). The shared section above acts as the default they inherit.
  const [roleArgs, setRoleArgs] = useState<Record<string, RoleArgState>>({});
  // Which role's parameters the tab strip shows. Tabs rather than a stack of
  // sections: a PD service has three parameter sets (shared, prefill,
  // decode), and three open forms at once push the submit button off screen.
  const [paramsTab, setParamsTab] = useState('shared');
  // Per-pod resource shape and scheduling priority. They apply to every role
  // of the service; Volcano schedules one replica's pods as a gang and
  // preempts a lower priority gang with a higher one.
  const [gpuPerPod, setGpuPerPod] = useState('1');
  const [cpuCores, setCpuCores] = useState('4');
  const [memoryGiB, setMemoryGiB] = useState('16');
  const [priority, setPriority] = useState('normal');
  // Parameter templates that fit the current engine/model/topology/card. The
  // operator loads one instead of retyping a dozen flags.
  const [templates, setTemplates] = useState<EngineParamTemplate[]>([]);
  const [templateId, setTemplateId] = useState('');
  // Saving the current parameters as a template: the other half of the
  // feature. Tuning a service and only then realising the values are worth
  // keeping is the normal order of events, so the save lives next to the
  // load rather than on a separate page the operator has to go hunting for.
  const [saveTemplateOpen, setSaveTemplateOpen] = useState(false);
  // templateEpoch forces the match query to re-run after a save, so a new
  // template appears in the picker without a page reload.
  const [templateEpoch, setTemplateEpoch] = useState(0);
  // keepTemplateId is the template to select once the reload finishes. A
  // context change clears the selection (the old template no longer applies),
  // but a save happens *in* the current context, so the row it produced must
  // stay selected instead of snapping back to "none".
  const keepTemplateId = useRef('');

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

  // The vendors and card types the cluster actually carries, plus the CPU
  // marker for an image that needs no accelerator.
  const fleet = useFleetAccelerators(orgId);
  const vendorOptions = [...fleet.vendors, CPU_ACCELERATOR];
  const cardTypeOptions = fleet.cardTypesFor(accelerator);

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
      .then((data) =>
        // An external model is served by its provider over that provider's
        // API: there are no weights to place on this platform, so it cannot
        // be deployed here and must not be offered.
        setAuthorizedModels((data.models || []).filter((m) => !m.external)),
      )
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
  const title = selected?.name ?? modelName ?? '';

  // FR3.2: image dropdown filtered by the chosen accelerator.
  const compatible = images.filter((i) => i.accelerator === accelerator);
  const selectedImage = images.find((i) => i.imageId === imageId);
  const engine = selectedImage?.engine || '';
  const params = engineParams(engine);

  useEffect(() => {
    // The match context is what the operator has chosen so far; the server
    // filters and ranks, so a template for another engine or model never shows
    // up. No engine yet (no image chosen) means nothing to match against.
    if (!engine) {
      setTemplates([]);
      setTemplateId('');
      return;
    }
    const params = new URLSearchParams({ engine });
    if (selectedModelId) params.set('modelId', selectedModelId);
    if (topology) params.set('topology', topology);
    if (accelerator && accelerator !== CPU_ACCELERATOR) {
      params.set('accelerator', accelerator);
      if (acceleratorType) params.set('acceleratorType', acceleratorType);
    }
    let cancelled = false;
    api
      .get<{ templates?: EngineParamTemplate[] }>(
        `/api/v1/admin/engine-param-templates?${params.toString()}`,
        orgId,
      )
      .then((data) => {
        if (cancelled) return;
        const list = data.templates || [];
        setTemplates(list);
        const keep = keepTemplateId.current;
        setTemplateId(keep && list.some((tpl) => tpl.templateId === keep) ? keep : '');
        keepTemplateId.current = '';
      })
      .catch(() => {
        if (!cancelled) setTemplates([]);
      });
    return () => {
      cancelled = true;
    };
  }, [api, accelerator, acceleratorType, engine, orgId, selectedModelId, topology, templateEpoch]);

  // applyTemplate copies a template's parameters into the form. A role the
  // template defines replaces that role's rows; the shared parameters replace
  // the shared rows. Nothing else changes, so the operator keeps any manual
  // edit the template does not cover.
  const applyTemplate = (id: string) => {
    setTemplateId(id);
    const tpl = templates.find((x) => x.templateId === id);
    if (!tpl) return;
    setEngineArgs({ ...(tpl.engineArgs || {}) });
    setExtraArgs([...(tpl.extraArgs || [])]);
    const nextRoles: Record<string, RoleArgState> = {};
    for (const role of tpl.roleEngineArgs || []) {
      nextRoles[role.role] = { args: { ...(role.engineArgs || {}) }, extra: [...(role.extraArgs || [])] };
    }
    setRoleArgs(nextRoles);
  };


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
    // Topology: a distributed deploy needs more than one pod per replica,
    // and PD needs an engine that speaks the prefill/decode split (the
    // server enforces the same rules).
    if (topology === 'multinode' && tp * pp < 2) {
      setError(t('deploy.errorMultiNodeParallel'));
      return;
    }
    if (topology === 'pd') {
      if (!['vllm', 'sglang'].includes(engine)) {
        setError(t('deploy.errorPdEngine'));
        return;
      }
      const prefill = parseInt(prefillReplicas, 10) || 1;
      const decode = parseInt(decodeReplicas, 10) || 1;
      if (prefill < 1 || decode < 1) {
        setError(t('deploy.errorPdReplicas'));
        return;
      }
    }
    if (!imageId) {
      setError(t('deploy.errorImage'));
      return;
    }
    // One pod owns gpu_per_pod devices, so a multi-pod replica's total
    // (tensor x pipeline) must divide evenly by it: an uneven split gives the
    // engine a world size its pods cannot see (the server rejects it too).
    const perPod = parseInt(gpuPerPod, 10) || 1;
    if (topology !== 'single' && (tp * pp) % perPod !== 0) {
      setError(t('deploy.errorGpuPerPod', { devices: tp * pp, perPod }));
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
        engineArgs: Object.fromEntries(
          Object.entries(engineArgs).filter(([, v]) => v !== undefined && v !== ''),
        ),
        extraArgs: extraArgs.map((a) => a.trim()).filter(Boolean),
        topology,
        tensorParallelSize: String(tp),
        pipelineParallelSize: String(pp),
        roleEngineArgs: roleArgPayload(roleArgs),
        prefillReplicas: String(parseInt(prefillReplicas, 10) || 1),
        decodeReplicas: String(parseInt(decodeReplicas, 10) || 1),
        scheduling: {
          gpuPerPod: parseInt(gpuPerPod, 10) || 1,
          cpuCores: Number(cpuCores) || 0,
          memoryMib: Math.round((Number(memoryGiB) || 0) * 1024),
          priority,
        },
        ...(sharedGpu ? { poolId, sliceTemplateId } : {}),
      });
      onDeployed(res.serviceId);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to deploy');
    } finally {
      setSubmitting(false);
    }
  };

  // The parallelism as the form currently shows it, for the summary line.
  const tp = parseInt(tensorParallel, 10) || 1;
  const pp = parseInt(pipelineParallel, 10) || 1;

  // The parameter tabs: "shared" plus one per role of the current layout. A
  // role that no longer exists (a PD service switched back to single) drops
  // its tab, and the strip falls back to the shared tab instead of showing a
  // section nothing will be submitted with.
  const roleNames = rolesForTopology(topology);
  const paramTabs = ['shared', ...roleNames].map((id) => ({
    id,
    label:
      id === 'shared'
        ? t('deploy.paramsTabShared')
        : t('deploy.paramsTabRole', { role: t(`deploy.role.${id}`) }),
    changed:
      id === 'shared'
        ? Object.values(engineArgs).some((v) => v !== undefined && v !== '') ||
          extraArgs.some((a) => a.trim() !== '')
        : roleArgPayload(roleArgs).some((entry) => entry.role === id),
  }));
  const activeTab = paramTabs.some((tab) => tab.id === paramsTab) ? paramsTab : 'shared';

  const setAS = (patch: Partial<AutoscalingPolicy>) => {
    setAutoscaling((p) => ({ ...p, ...patch }));
    setAutoscalingErrors({});
  };

  return (
    <div className="panel" data-testid="deploy-panel">
      <div className="panel-header">
        <h2>{t('deploy.title', { name: title })}</h2>
        <button className="link" data-testid="deploy-panel-close" onClick={onCancel}>
          {t('common.cancel')}
        </button>
      </div>
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
              <option value={modelId}>{modelName || modelId || '—'}</option>
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
            onChange={(e) => {
              setAccelerator(e.target.value);
              // A card type belongs to one vendor, so the previous choice
              // cannot survive a vendor change.
              setAcceleratorType('');
            }}
          >
            {vendorOptions.map((a) => (
              <option key={a} value={a}>
                {a === CPU_ACCELERATOR ? t('deploy.acceleratorCpu') : a}
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
          <select
            id="deploy-card-type"
            data-testid="deploy-card-type-select"
            value={acceleratorType}
            disabled={accelerator === CPU_ACCELERATOR}
            onChange={(e) => setAcceleratorType(e.target.value)}
          >
            <option value="">{t('deploy.cardTypeAny')}</option>
            {cardTypeOptions.map((c) => (
              <option key={c.cardType} value={c.cardType}>
                {c.cardType} ({t('deploy.cardTypeFree', { free: String(c.free), total: String(c.total) })})
              </option>
            ))}
          </select>
          <div className="field-hint">
            {accelerator === CPU_ACCELERATOR
              ? t('deploy.cardTypeCpu')
              : cardTypeOptions.length > 0
                ? t('deploy.cardTypeHintFleet')
                : t('deploy.cardTypeHintEmpty')}
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

      <h4 style={{ marginBottom: 8 }}>{t('deploy.topology')}</h4>
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="deploy-topology">{t('deploy.topologyMode')}</label>
          <select
            id="deploy-topology"
            data-testid="deploy-topology-select"
            value={topology}
            onChange={(e) => setTopology(e.target.value as ServingTopology)}
          >
            <option value="single">{t('deploy.topologySingle')}</option>
            <option value="multinode">{t('deploy.topologyMultiNode')}</option>
            <option value="pd">{t('deploy.topologyPd')}</option>
          </select>
          <div className="field-hint">{t('deploy.topologyHint')}</div>
        </div>
        {topology !== 'single' && (
          <>
            <div className="form-field">
              <label htmlFor="deploy-tp">{t('deploy.tensorParallel')}</label>
              <input
                id="deploy-tp"
                data-testid="deploy-tp-input"
                type="number"
                min={1}
                max={100}
                value={tensorParallel}
                onChange={(e) => setTensorParallel(e.target.value)}
              />
              <div className="field-hint">{t('engine.tensorParallelHint')}</div>
            </div>
            <div className="form-field">
              <label htmlFor="deploy-pp">{t('deploy.pipelineParallel')}</label>
              <input
                id="deploy-pp"
                data-testid="deploy-pp-input"
                type="number"
                min={1}
                max={100}
                value={pipelineParallel}
                onChange={(e) => setPipelineParallel(e.target.value)}
              />
              <div className="field-hint">{t('engine.pipelineParallelHint')}</div>
            </div>
          </>
        )}
        {topology === 'pd' && (
          <>
            <div className="form-field">
              <label htmlFor="deploy-prefill">{t('deploy.prefillReplicas')}</label>
              <input
                id="deploy-prefill"
                data-testid="deploy-prefill-input"
                type="number"
                min={1}
                max={100}
                value={prefillReplicas}
                onChange={(e) => setPrefillReplicas(e.target.value)}
              />
            </div>
            <div className="form-field">
              <label htmlFor="deploy-decode">{t('deploy.decodeReplicas')}</label>
              <input
                id="deploy-decode"
                data-testid="deploy-decode-input"
                type="number"
                min={1}
                max={100}
                value={decodeReplicas}
                onChange={(e) => setDecodeReplicas(e.target.value)}
              />
            </div>
            <div className="form-field full">
              <div className="muted" data-testid="deploy-topology-summary">
                {topologySummary(t, topology, parseInt(replicas, 10) || 1, tp, pp,
                  parseInt(prefillReplicas, 10) || 1, parseInt(decodeReplicas, 10) || 1)}
              </div>
            </div>
          </>
        )}
        {topology === 'multinode' && (
          <div className="form-field full">
            <div className="muted" data-testid="deploy-topology-summary">
              {topologySummary(t, topology, parseInt(replicas, 10) || 1, tp, pp, 1, 1)}
            </div>
          </div>
        )}
      </div>

      <h4 style={{ marginBottom: 8 }}>{t('deploy.podShape')}</h4>
      <div className="form-grid" data-testid="deploy-pod-shape">
        <div className="form-field">
          <label htmlFor="deploy-gpu-per-pod">{t('deploy.gpuPerPod')}</label>
          <input
            id="deploy-gpu-per-pod"
            data-testid="deploy-gpu-per-pod"
            type="number"
            min={1}
            max={8}
            value={gpuPerPod}
            onChange={(e) => setGpuPerPod(e.target.value)}
          />
          <div className="field-hint">{t('deploy.gpuPerPodHint')}</div>
        </div>
        <div className="form-field">
          <label htmlFor="deploy-cpu">{t('deploy.cpuCores')}</label>
          <input
            id="deploy-cpu"
            data-testid="deploy-cpu-cores"
            type="number"
            min={0}
            step="0.5"
            value={cpuCores}
            onChange={(e) => setCpuCores(e.target.value)}
          />
          <div className="field-hint">{t('deploy.cpuCoresHint')}</div>
        </div>
        <div className="form-field">
          <label htmlFor="deploy-memory">{t('deploy.memoryGiB')}</label>
          <input
            id="deploy-memory"
            data-testid="deploy-memory-gib"
            type="number"
            min={0}
            step="1"
            value={memoryGiB}
            onChange={(e) => setMemoryGiB(e.target.value)}
          />
          <div className="field-hint">{t('deploy.memoryGiBHint')}</div>
        </div>
        <div className="form-field">
          <label htmlFor="deploy-priority">{t('deploy.priority')}</label>
          <select
            id="deploy-priority"
            data-testid="deploy-priority-select"
            value={priority}
            onChange={(e) => setPriority(e.target.value)}
          >
            <option value="high">{t('deploy.priorityHigh')}</option>
            <option value="normal">{t('deploy.priorityNormal')}</option>
            <option value="low">{t('deploy.priorityLow')}</option>
          </select>
          <div className="field-hint">{t('deploy.priorityHint')}</div>
        </div>
      </div>

      {/* The template row is always present once an engine is known: the
          save button is how the first template comes into existence, so
          hiding the row until one exists would be a chicken-and-egg. */}
      {engine && (
        <div className="form-grid" style={{ marginBottom: 12 }} data-testid="deploy-template-picker">
          <div className="form-field full">
            <label htmlFor="deploy-template">{t('deploy.templateLabel')}</label>
            <div className="toolbar" style={{ gap: 8 }}>
              <select
                id="deploy-template"
                data-testid="deploy-template-select"
                style={{ flex: '1 1 auto' }}
                value={templateId}
                onChange={(e) => applyTemplate(e.target.value)}
                disabled={templates.length === 0}
              >
                <option value="">
                  {templates.length === 0 ? t('deploy.templateNoMatch') : t('deploy.templateNone')}
                </option>
                {templates.map((tpl) => (
                  <option key={tpl.templateId} value={tpl.templateId}>
                    {tpl.name}
                    {tpl.topology ? ` · ${t(`deploy.topology${tpl.topology === 'pd' ? 'Pd' : tpl.topology === 'single' ? 'Single' : 'MultiNode'}`)}` : ''}
                  </option>
                ))}
              </select>
              <button
                type="button"
                className="secondary"
                data-testid="deploy-template-save"
                onClick={() => setSaveTemplateOpen(true)}
              >
                {t('deploy.templateSave')}
              </button>
            </div>
            <div className="field-hint">
              {templateId
                ? t('deploy.templateApplied')
                : t('deploy.templateHint')}
            </div>
          </div>
        </div>
      )}

      {saveTemplateOpen && (
        <SaveTemplateDialog
          orgId={orgId}
          engine={engine}
          modelId={selectedModelId}
          topology={topology}
          accelerator={accelerator === CPU_ACCELERATOR ? '' : accelerator}
          acceleratorType={acceleratorType}
          engineArgs={engineArgs}
          extraArgs={extraArgs}
          roleEngineArgs={roleArgPayload(roleArgs)}
          onClose={() => setSaveTemplateOpen(false)}
          onSaved={(saved) => {
            setSaveTemplateOpen(false);
            // Re-run the match query and select what was just saved: the
            // operator's next question is "did it work", and the row above
            // answers it.
            keepTemplateId.current = saved;
            setTemplateEpoch((n) => n + 1);
            setParamsTab('shared');
          }}
        />
      )}

      {/* One tab per parameter set: the shared defaults plus one per role of
          the chosen layout. A role left empty inherits the shared value, and
          a tab whose role overrides something says so, so an operator does
          not have to open all of them to find out. */}
      <EngineParamTabs
        tabs={paramTabs}
        active={activeTab}
        onSelect={setParamsTab}
      />
      {activeTab === 'shared' ? (
        <EngineArgsSection
          engine={engine}
          params={params}
          values={engineArgs}
          extraArgs={extraArgs}
          title={t('deploy.engineParamsShared', { engine })}
          onChange={(name, value) => setEngineArgs((prev) => ({ ...prev, [name]: value }))}
          onExtraChange={setExtraArgs}
        />
      ) : (
        <EngineArgsSection
          engine={engine}
          params={params}
          values={roleArgs[activeTab]?.args ?? {}}
          extraArgs={roleArgs[activeTab]?.extra ?? []}
          title={t('deploy.engineParamsRole', {
            engine,
            role: t(`deploy.role.${activeTab}`),
          })}
          testId={`deploy-engine-args-${activeTab}`}
          onChange={(name, value) =>
            setRoleArgs((prev) => ({
              ...prev,
              [activeTab]: {
                args: { ...(prev[activeTab]?.args ?? {}), [name]: value },
                extra: prev[activeTab]?.extra ?? [],
              },
            }))
          }
          onExtraChange={(next) =>
            setRoleArgs((prev) => ({
              ...prev,
              [activeTab]: { args: prev[activeTab]?.args ?? {}, extra: next },
            }))
          }
        />
      )}

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
        <button className="secondary" onClick={onCancel}>
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
    </div>
  );
}

// SaveTemplateDialog stores the deploy form's current engine parameters as a
// named template, tagged with the context they were tuned for.
//
// The context matters as much as the flags: a value tuned for one model on one
// card type is rarely transferable, so the dialog captures the engine, model,
// topology and accelerator the operator currently has selected. That is what
// makes the template show up again for the same combination and stay out of the
// way for others, and it is why the summary line is shown before saving — the
// operator should see what the template will claim to apply to.
function SaveTemplateDialog({
  orgId,
  engine,
  modelId,
  topology,
  accelerator,
  acceleratorType,
  engineArgs,
  extraArgs,
  roleEngineArgs,
  onClose,
  onSaved,
}: {
  orgId: string;
  engine: string;
  modelId: string;
  topology: ServingTopology;
  accelerator: string;
  acceleratorType: string;
  engineArgs: Record<string, string>;
  extraArgs: string[];
  roleEngineArgs: ReturnType<typeof roleArgPayload>;
  onClose: () => void;
  onSaved: (templateId: string) => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  // Whether the template claims to apply to the selected model only or to any
  // model on this engine. Values tuned per model are the common case, but
  // "vLLM on one 4090" is worth reusing across models, so the operator
  // decides instead of the form guessing.
  const [scope, setScope] = useState<'model' | 'any'>('model');
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  // Only what the form actually holds is saved: an input left at its default
  // is not a tuned value, and a template full of defaults hides the three
  // flags that matter.
  const filledArgs = Object.fromEntries(
    Object.entries(engineArgs).filter(([, v]) => v !== undefined && v !== ''),
  );
  const filledExtra = extraArgs.map((a) => a.trim()).filter(Boolean);
  const filledRoles = roleEngineArgs;
  const hasValues =
    Object.keys(filledArgs).length > 0 || filledExtra.length > 0 || filledRoles.length > 0;

  const save = async () => {
    if (!name.trim()) {
      setError(t('deploy.templateNameRequired'));
      return;
    }
    if (!hasValues) {
      setError(t('deploy.templateEmpty'));
      return;
    }
    setSaving(true);
    setError('');
    try {
      const res = await api.post<{ template?: EngineParamTemplate }>(
        '/api/v1/admin/engine-param-templates',
        orgId,
        {
          name: name.trim(),
          description: description.trim(),
          engine,
          modelId: scope === 'model' ? modelId : '',
          topology,
          accelerator,
          acceleratorType,
          engineArgs: filledArgs,
          extraArgs: filledExtra,
          roleEngineArgs: filledRoles,
        },
      );
      onSaved(res.template?.templateId || '');
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('deploy.templateSaveFailed'));
    } finally {
      setSaving(false);
    }
  };

  const context = [
    engine,
    scope === 'model' ? t('deploy.templateThisModel') : t('deploy.templateAnyModel'),
    t(`deploy.topology${topology === 'pd' ? 'Pd' : topology === 'single' ? 'Single' : 'MultiNode'}`),
    acceleratorType || accelerator || undefined,
  ]
    .filter(Boolean)
    .join(' · ');

  return (
    <Dialog title={t('deploy.templateSaveTitle')} onClose={onClose} testId="deploy-template-dialog">
      <p className="muted">{t('deploy.templateSaveHint')}</p>
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="template-name">{t('deploy.templateName')}</label>
          <input
            id="template-name"
            data-testid="template-name-input"
            value={name}
            maxLength={128}
            disabled={saving}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('deploy.templateNamePlaceholder')}
          />
        </div>
        <div className="form-field">
          <label htmlFor="template-scope">{t('deploy.templateScope')}</label>
          <select
            id="template-scope"
            data-testid="template-scope-select"
            value={scope}
            disabled={saving}
            onChange={(e) => setScope(e.target.value as 'model' | 'any')}
          >
            <option value="model">{t('deploy.templateScopeModel')}</option>
            <option value="any">{t('deploy.templateScopeAny')}</option>
          </select>
          <div className="field-hint">{t('deploy.templateScopeHint')}</div>
        </div>
        <div className="form-field">
          <label htmlFor="template-description">{t('deploy.templateDescription')}</label>
          <input
            id="template-description"
            data-testid="template-description-input"
            value={description}
            maxLength={512}
            disabled={saving}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>
      </div>
      <div className="kv-grid" data-testid="template-context">
        <div>
          <span className="kv-label">{t('deploy.templateContextLabel')}</span>
          <span className="mono">{context}</span>
        </div>
        <div>
          <span className="kv-label">{t('deploy.templateParamsLabel')}</span>
          <span>
            {t('deploy.templateParamsValue', {
              args: String(Object.keys(filledArgs).length),
              extra: String(filledExtra.length),
              roles: String(filledRoles.length),
            })}
          </span>
        </div>
      </div>
      {error && <div className="field-error">{error}</div>}
      <div className="dialog-actions">
        <button className="secondary" data-testid="template-cancel" onClick={onClose} disabled={saving}>
          {t('common.cancel')}
        </button>
        <button data-testid="template-save" onClick={() => void save()} disabled={saving}>
          {saving ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}

// EngineParamTabs is the strip that switches between the shared parameter set
// and each role's overrides. It replaces a stack of always-open sections: a PD
// service has three of them, and stacking three full parameter forms makes the
// page scroll to a submit button the operator cannot see while editing.
//
// A dot marks a tab that carries values, so an override hidden behind an
// inactive tab is still visible as "something was set here".
function EngineParamTabs({
  tabs,
  active,
  onSelect,
}: {
  tabs: Array<{ id: string; label: string; changed: boolean }>;
  active: string;
  onSelect: (id: string) => void;
}) {
  return (
    <div className="tab-strip" role="tablist" data-testid="deploy-params-tabs">
      {tabs.map((tab) => (
        <button
          key={tab.id}
          type="button"
          role="tab"
          aria-selected={tab.id === active}
          className={`tab${tab.id === active ? ' active' : ''}`}
          data-testid={`deploy-params-tab-${tab.id}`}
          onClick={() => onSelect(tab.id)}
        >
          {tab.label}
          {tab.changed && <span className="tab-dot" aria-hidden="true" />}
        </button>
      ))}
    </div>
  );
}

// EngineArgsSection renders the chosen engine's common parameters as typed
// fields plus the free-form rows for everything the catalog does not know.
// A switch sends "flag:true"; an empty field is simply not sent, so the
// engine keeps its own default.
function EngineArgsSection({
  engine,
  params,
  values,
  extraArgs,
  title,
  testId = 'deploy-engine-args',
  onChange,
  onExtraChange,
}: {
  engine: string;
  params: EngineParam[];
  values: Record<string, string>;
  extraArgs: string[];
  title?: string;
  testId?: string;
  onChange: (name: string, value: string) => void;
  onExtraChange: (next: string[]) => void;
}) {
  const { t } = useI18n();
  if (!engine) return null;
  return (
    <div data-testid={testId}>
      <h4 style={{ marginBottom: 8 }}>{title ?? t('deploy.engineParams', { engine })}</h4>
      {params.length === 0 && (
        <div className="muted" data-testid="deploy-engine-args-none">
          {t('deploy.engineParamsNone', { engine })}
        </div>
      )}
      <div className="form-grid">
        {params.map((p) => {
          const value = values[p.name] ?? '';
          const inputId = `engine-arg-${p.name}`;
          return (
            <div className="form-field" key={p.name}>
              <label htmlFor={inputId}>{t(p.labelKey)}</label>
              {p.kind === 'switch' ? (
                <input
                  id={inputId}
                  data-testid={`engine-arg-${p.name}`}
                  type="checkbox"
                  checked={value === 'flag:true'}
                  onChange={(e) => onChange(p.name, e.target.checked ? 'flag:true' : '')}
                />
              ) : p.kind === 'select' ? (
                <select
                  id={inputId}
                  data-testid={`engine-arg-${p.name}`}
                  value={value}
                  onChange={(e) => onChange(p.name, e.target.value)}
                >
                  <option value="">{t('deploy.engineArgDefault')}</option>
                  {(p.options || []).map((o) => (
                    <option key={o} value={o}>
                      {o}
                    </option>
                  ))}
                </select>
              ) : (
                <input
                  id={inputId}
                  data-testid={`engine-arg-${p.name}`}
                  type={p.kind === 'number' ? 'number' : 'text'}
                  value={value}
                  placeholder={p.defaultValue ? `(${p.defaultValue})` : ''}
                  onChange={(e) => onChange(p.name, e.target.value)}
                />
              )}
              <div className="field-hint">{t(p.hintKey)}</div>
            </div>
          );
        })}
      </div>

      <div className="form-field full">
        <label>{t('deploy.extraArgs')}</label>
        <div className="field-hint">{t('deploy.extraArgsHint')}</div>
        {extraArgs.map((arg, i) => (
          <div className="toolbar" key={i} style={{ marginTop: 6 }}>
            <input
              className="mono"
              data-testid={`deploy-extra-arg-${i}`}
              value={arg}
              placeholder="--enable-chunked-prefill"
              onChange={(e) => {
                const next = [...extraArgs];
                next[i] = e.target.value;
                onExtraChange(next);
              }}
            />
            <button
              type="button"
              className="secondary"
              data-testid={`deploy-extra-arg-remove-${i}`}
              onClick={() => onExtraChange(extraArgs.filter((_, j) => j !== i))}
            >
              {t('common.delete')}
            </button>
          </div>
        ))}
        <button
          type="button"
          className="secondary"
          data-testid="deploy-extra-arg-add"
          onClick={() => onExtraChange([...extraArgs, ''])}
        >
          {t('deploy.addExtraArg')}
        </button>
      </div>
    </div>
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
