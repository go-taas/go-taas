// Service detail page: state, spec, endpoints with copy + curl snippet,
// failure reason, delete-and-retry for failed services.
// Implements docs/design/model-catalog-deployment.md FR4.2, FR4.3, FR7.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  formatTime,
  type AssignedSlice,
  type InferenceServiceAccessSubject,
  type AutoscalingPolicy,
  type AutoscalingStatus,
  type InferenceServiceSummary,
  type ServingTopology,
} from '../api';
import { useOrg } from '../org';
import {
  BackLink,
  CopyButton,
  Dialog,
  ErrorBanner,
  StateBadge,
  usePolling,
} from '../components';
import { useI18n } from '../i18n';
import { navigate } from '../router';
import ServiceStatusBanner from '../components/ServiceStatusBanner';

interface ServiceResponse {
  response: { code: number; message: string };
  service: InferenceServiceSummary & { accelerator?: string; acceleratorType?: string };
  endpoints: string[];
  autoscaling?: AutoscalingPolicy;
  autoscalingStatus?: AutoscalingStatus;
  assignedSlice?: AssignedSlice;
}

export default function ServiceDetailPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const id = window.location.pathname.split('/').pop() || '';
  const [data, setData] = useState<ServiceResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // servedModel is the catalog model name the serving gateway routes on, so
  // the curl snippet is runnable instead of naming the service.
  const [servedModel, setServedModel] = useState('');
  // The topology editor is a dialog: changing the layout restarts the pods.
  const [topologyOpen, setTopologyOpen] = useState(false);

  const load = useCallback(async () => {
    setError('');
    try {
      const res = await api.get<ServiceResponse>(
        `/api/v1/admin/inference-services/${id}`,
        orgId,
      );
      setData(res);
      const model = await api
        .get<{ model?: { name?: string } }>(`/api/v1/admin/models/${res.service.modelId}`, orgId)
        .catch(() => ({ model: undefined }));
      setServedModel(model.model?.name || '');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('service.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [id, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // FR3.3: poll while the state can still transition (pending/deploying).
  const active = data?.service.state === 'pending' || data?.service.state === 'deploying';
  // Feature #16 AC12: poll while autoscaling is scaling or cold-starting.
  const asState = data?.autoscalingStatus?.state;
  const asActive =
    asState === 'scaling-up' || asState === 'scaling-down' || asState === 'cold-starting';
  usePolling(() => void load(), 3000, active || asActive);

  if (loading) return <div className="loading">{t('common.loading')}</div>;
  if (error)
    return (
      <div>
        <BackLink to="/admin/inference-services" label={t('service.back')} />
        <ErrorBanner message={error} />
      </div>
    );
  if (!data) return null;

  const s = data.service;
  const running = s.state === 'running';

  return (
    <div>
      <BackLink to="/admin/inference-services" label={t('service.back')} />
      <ServiceStatusBanner serviceId={s.serviceId} />
      <div className="page-header">
        <div>
          <h1 data-testid="service-detail-name">{s.name}</h1>
          <div className="subtitle mono">{s.serviceId}</div>
        </div>
        <StateBadge state={s.state} />
        <a
          className="secondary"
          data-testid="service-logs-link"
          href={`/admin/services/${s.serviceId}/logs`}
          onClick={(e) => {
            e.preventDefault();
            navigate(`/admin/services/${s.serviceId}/logs`);
          }}
        >
          {t('servicelogs.title')}
        </a>
        <a
          className="secondary"
          data-testid="service-metrics-link"
          href={`/admin/services/${s.serviceId}/metrics`}
          onClick={(e) => {
            e.preventDefault();
            navigate(`/admin/services/${s.serviceId}/metrics`);
          }}
        >
          {t('servicemetrics.title')}
        </a>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('service.spec')}</h3>
        <div className="detail-grid">
          <div className="detail-item">
            <div className="label">{t('service.model')}</div>
            <div className="value mono">
              {s.modelId.slice(0, 8)}… @ {s.modelVersion}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">{t('service.image')}</div>
            <div className="value mono">{s.imageId}</div>
          </div>
          <div className="detail-item">
            <div className="label">{t('service.accelerator')}</div>
            <div className="value">
              {s.accelerator || '—'}
              {s.acceleratorType ? ` (${s.acceleratorType})` : ''}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">{t('service.replicas')}</div>
            <div className="value" data-testid="service-detail-replicas">
              {s.replicas}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">{t('service.updated')}</div>
            <div className="value">{formatTime(s.updatedAt)}</div>
          </div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }} data-testid="service-topology-card">
        <div className="panel-header">
          <h3 style={{ marginTop: 0 }}>{t('service.topology')}</h3>
          <button
            className="secondary"
            data-testid="service-topology-edit"
            onClick={() => setTopologyOpen(true)}
          >
            {t('service.topologyEdit')}
          </button>
        </div>
        <div className="detail-grid">
          <div className="detail-item">
            <div className="label">{t('service.topologyMode')}</div>
            <div className="value" data-testid="service-topology-mode">
              {t(topologyLabelKey(s.topology))}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">{t('service.parallelism')}</div>
            <div className="value" data-testid="service-parallelism">
              TP {s.tensorParallelSize || 1} · PP {s.pipelineParallelSize || 1}
            </div>
          </div>
          {(s.topology || 'single') === 'pd' && (
            <div className="detail-item">
              <div className="label">{t('service.pdRoles')}</div>
              <div className="value" data-testid="service-pd-roles">
                {t('service.pdRolesValue', {
                  prefill: String(s.prefillReplicas || 1),
                  decode: String(s.decodeReplicas || 1),
                })}
              </div>
            </div>
          )}
        </div>
      </div>

      {data.assignedSlice && (
        <div className="panel" style={{ marginBottom: 16 }} data-testid="service-slice-card">
          <h3 style={{ marginTop: 0 }}>{t('service.slice')}</h3>
          <div className="detail-grid">
            <div className="detail-item">
              <div className="label">{t('service.slicePool')}</div>
              <div className="value" data-testid="service-slice-pool">
                {data.assignedSlice.poolName || data.assignedSlice.poolId}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('service.sliceTemplate')}</div>
              <div className="value" data-testid="service-slice-template">
                {data.assignedSlice.templateName} — {data.assignedSlice.memoryGib} GiB /{' '}
                {data.assignedSlice.computePct}%
              </div>
            </div>
            {data.assignedSlice.nodeId && (
              <div className="detail-item">
                <div className="label">{t('service.sliceGpu')}</div>
                <div className="value" data-testid="service-slice-gpu">
                  {data.assignedSlice.nodeId} · GPU {data.assignedSlice.gpuIndex}
                </div>
              </div>
            )}
          </div>
        </div>
      )}

      {data.autoscaling && (
        <div className="panel" style={{ marginBottom: 16 }} data-testid="autoscaling-card">
          <h3 style={{ marginTop: 0 }}>{t('service.autoscaling')}</h3>
          <div className="detail-grid">
            <div className="detail-item">
              <div className="label">{t('service.enabled')}</div>
              <div className="value">{data.autoscaling.enabled ? t('common.on') : t('common.off')}</div>
            </div>
            <div className="detail-item">
              <div className="label">{t('service.minMax')}</div>
              <div className="value">
                {data.autoscaling.minReplicas} / {data.autoscaling.maxReplicas}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('service.targetConcurrency')}</div>
              <div className="value">{data.autoscaling.targetConcurrency}</div>
            </div>
            <div className="detail-item">
              <div className="label">{t('service.scaleToZero')}</div>
              <div className="value">{data.autoscaling.scaleToZero ? t('common.on') : t('common.off')}</div>
            </div>
            <div className="detail-item">
              <div className="label">{t('service.cooldown')}</div>
              <div className="value">{data.autoscaling.cooldownSeconds}s</div>
            </div>
          </div>
          {data.autoscalingStatus && (
            <AutoscalingStatusBlock status={data.autoscalingStatus} />
          )}
        </div>
      )}

      {s.state === 'failed' && (
        <div className="error-banner" data-testid="failure-reason">
          {t('service.deployFailed')}
        </div>
      )}

      <div className="panel">
        <h3 style={{ marginTop: 0 }}>{t('service.endpoints')}</h3>
        {!running && (
          <p className="muted" data-testid="endpoints-placeholder">
            {s.state === 'pending' || s.state === 'deploying'
              ? t('service.endpointsPending')
              : t('service.endpointsRunningOnly')}
          </p>
        )}
        {running &&
          (data.endpoints.length === 0 ? (
            <p className="muted">{t('service.endpointsEmpty')}</p>
          ) : (
            data.endpoints.map((ep) => (
              <div key={ep} className="endpoint-box" data-testid="endpoint-box">
                <span>{ep}</span>
                <CopyButton text={ep} />
              </div>
            ))
          ))}
        {running && data.endpoints.length > 0 && (
          <>
            <h3>{t('service.tryIt')}</h3>
            <div className="curl-snippet" data-testid="curl-snippet">
              {`curl ${data.endpoints[0]}/chat/completions \\\n  -H "Authorization: Bearer sk-..." \\\n  -H "Content-Type: application/json" \\\n  -d '{"model": "${servedModel || s.name}", "messages": [{"role": "user", "content": "Hello!"}]}'`}
            </div>
            <CopyButton
              text={`curl ${data.endpoints[0]}/chat/completions -H "Authorization: Bearer sk-..." -H "Content-Type: application/json" -d '{"model": "${servedModel || s.name}", "messages": [{"role": "user", "content": "Hello!"}]}'`}
              label={t('service.copyCurl')}
            />
          </>
        )}
      </div>

      <AccessPolicyCard serviceId={s.serviceId} />

      {topologyOpen && (
        <TopologyDialog
          service={s}
          orgId={orgId}
          onClose={() => setTopologyOpen(false)}
          onSaved={() => {
            setTopologyOpen(false);
            void load();
          }}
        />
      )}
    </div>
  );
}

// topologyLabelKey maps the wire topology to its message key.
function topologyLabelKey(topology?: string): string {
  switch (topology) {
    case 'distributed':
      return 'deploy.topologyDistributed';
    case 'pd':
      return 'deploy.topologyPd';
    default:
      return 'deploy.topologySingle';
  }
}

// TopologyDialog changes the serving layout in place: the service keeps its
// id and endpoints, and the controller rolls the StormService out with the
// new roles. Editing is only meaningful for the layout fields, so replicas
// are offered here too (a layout change restarts the pods anyway).
function TopologyDialog({
  service,
  orgId,
  onClose,
  onSaved,
}: {
  service: InferenceServiceSummary;
  orgId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [topology, setTopology] = useState<ServingTopology>(service.topology || 'single');
  const [tp, setTp] = useState(String(service.tensorParallelSize || 1));
  const [pp, setPp] = useState(String(service.pipelineParallelSize || 1));
  const [prefill, setPrefill] = useState(String(service.prefillReplicas || 1));
  const [decode, setDecode] = useState(String(service.decodeReplicas || 1));
  const [replicas, setReplicas] = useState(String(service.replicas || 1));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    const tpv = parseInt(tp, 10) || 1;
    const ppv = parseInt(pp, 10) || 1;
    if (topology === 'distributed' && tpv * ppv < 2) {
      setError(t('deploy.errorDistributedParallel'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.post(
        `/api/v1/admin/inference-services/${service.serviceId}:update-topology`,
        orgId,
        {
          serviceId: service.serviceId,
          topology,
          tensorParallelSize: String(tpv),
          pipelineParallelSize: String(ppv),
          prefillReplicas: String(parseInt(prefill, 10) || 1),
          decodeReplicas: String(parseInt(decode, 10) || 1),
          replicas: String(parseInt(replicas, 10) || 0),
        },
      );
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('service.topologyUpdateFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('service.topologyEdit')} onClose={onClose} testId="topology-dialog">
      <p className="muted">{t('service.topologyEditHint')}</p>
      {error && <div className="field-error">{error}</div>}
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="topology-mode">{t('service.topologyMode')}</label>
          <select
            id="topology-mode"
            data-testid="topology-mode-select"
            value={topology}
            onChange={(e) => setTopology(e.target.value as ServingTopology)}
          >
            <option value="single">{t('deploy.topologySingle')}</option>
            <option value="distributed">{t('deploy.topologyDistributed')}</option>
            <option value="pd">{t('deploy.topologyPd')}</option>
          </select>
        </div>
        {topology !== 'single' && (
          <>
            <div className="form-field">
              <label htmlFor="topology-tp">{t('deploy.tensorParallel')}</label>
              <input
                id="topology-tp"
                data-testid="topology-tp-input"
                type="number"
                min={1}
                max={100}
                value={tp}
                onChange={(e) => setTp(e.target.value)}
              />
            </div>
            <div className="form-field">
              <label htmlFor="topology-pp">{t('deploy.pipelineParallel')}</label>
              <input
                id="topology-pp"
                data-testid="topology-pp-input"
                type="number"
                min={1}
                max={100}
                value={pp}
                onChange={(e) => setPp(e.target.value)}
              />
            </div>
          </>
        )}
        {topology === 'pd' && (
          <>
            <div className="form-field">
              <label htmlFor="topology-prefill">{t('deploy.prefillReplicas')}</label>
              <input
                id="topology-prefill"
                data-testid="topology-prefill-input"
                type="number"
                min={1}
                max={100}
                value={prefill}
                onChange={(e) => setPrefill(e.target.value)}
              />
            </div>
            <div className="form-field">
              <label htmlFor="topology-decode">{t('deploy.decodeReplicas')}</label>
              <input
                id="topology-decode"
                data-testid="topology-decode-input"
                type="number"
                min={1}
                max={100}
                value={decode}
                onChange={(e) => setDecode(e.target.value)}
              />
            </div>
          </>
        )}
        <div className="form-field">
          <label htmlFor="topology-replicas">{t('service.replicas')}</label>
          <input
            id="topology-replicas"
            data-testid="topology-replicas-input"
            type="number"
            min={1}
            max={100}
            value={replicas}
            onChange={(e) => setReplicas(e.target.value)}
          />
        </div>
      </div>
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose} data-testid="topology-cancel">
          {t('common.cancel')}
        </button>
        <button onClick={() => void submit()} disabled={submitting} data-testid="topology-save">
          {submitting ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}

// AccessPolicyCard edits who may use the service. A service belongs to the
// platform, so this is the only access control it has: by default every
// platform user may use it, and an operator can restrict it to listed
// organizations, projects or users.
function AccessPolicyCard({ serviceId }: { serviceId: string }) {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [restricted, setRestricted] = useState(false);
  const [subjects, setSubjects] = useState<InferenceServiceAccessSubject[]>([]);
  const [draftType, setDraftType] = useState('org');
  const [draftId, setDraftId] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{ restricted?: boolean; subjects?: InferenceServiceAccessSubject[] }>(
        `/api/v1/admin/inference-services/${serviceId}/access`,
        orgId,
      );
      setRestricted(Boolean(data.restricted));
      setSubjects(data.subjects || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('services.accessLoadFailed'));
    } finally {
      setLoading(false);
    }
  }, [serviceId, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async (nextRestricted: boolean, nextSubjects: InferenceServiceAccessSubject[]) => {
    setSaving(true);
    setError('');
    setNotice('');
    try {
      const data = await api.put<{ restricted?: boolean; subjects?: InferenceServiceAccessSubject[] }>(
        `/api/v1/admin/inference-services/${serviceId}/access`,
        orgId,
        {
          restricted: nextRestricted,
          subjects: nextSubjects.map((subject) => ({
            subjectType: subject.subjectType,
            subjectId: subject.subjectId,
          })),
        },
      );
      setRestricted(Boolean(data.restricted));
      setSubjects(data.subjects || []);
      setNotice(t('services.accessSaved'));
    } catch (e) {
      setError(e instanceof Error ? e.message : t('services.accessSaveFailed'));
    } finally {
      setSaving(false);
    }
  };

  const addSubject = () => {
    const id = draftId.trim();
    if (!id) return;
    if (subjects.some((s) => s.subjectType === draftType && s.subjectId === id)) {
      setDraftId('');
      return;
    }
    const next = [...subjects, { subjectType: draftType, subjectId: id }];
    setSubjects(next);
    setDraftId('');
    setRestricted(true);
  };

  const typeLabel = (subjectType: string) => {
    if (subjectType === 'org') return t('services.accessTypeOrg');
    if (subjectType === 'project') return t('services.accessTypeProject');
    return t('services.accessTypeUser');
  };

  return (
    <div className="panel" data-testid="access-policy-card">
      <h3 style={{ marginTop: 0 }}>{t('services.accessTitle')}</h3>
      <p className="hint" style={{ marginTop: 0 }}>{t('services.accessBody')}</p>

      {error && <div className="error" data-testid="access-policy-error">{error}</div>}
      {notice && <div className="success-banner" data-testid="access-policy-saved">{notice}</div>}

      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : (
        <>
          <div className="form-row">
            <label htmlFor="access-mode">{t('services.accessModeLabel')}</label>
            <select
              id="access-mode"
              data-testid="access-mode"
              value={restricted ? 'restricted' : 'everyone'}
              onChange={(e) => {
                const next = e.target.value === 'restricted';
                // Leaving "restricted" clears the list: an empty policy is
                // the open default, and keeping stale subjects would be a
                // policy the operator cannot see.
                void save(next, next ? subjects : []);
              }}
            >
              <option value="everyone">{t('services.accessModeEveryone')}</option>
              <option value="restricted">{t('services.accessModeRestricted')}</option>
            </select>
          </div>

          {restricted && (
            <>
              <div className="form-row">
                <label htmlFor="access-subject-type">{t('services.accessSubjectId')}</label>
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                  <select
                    id="access-subject-type"
                    data-testid="access-subject-type"
                    value={draftType}
                    onChange={(e) => setDraftType(e.target.value)}
                  >
                    <option value="org">{t('services.accessTypeOrg')}</option>
                    <option value="project">{t('services.accessTypeProject')}</option>
                    <option value="user">{t('services.accessTypeUser')}</option>
                  </select>
                  <input
                    id="access-subject-id"
                    data-testid="access-subject-id"
                    value={draftId}
                    placeholder={t('services.accessSubjectId')}
                    onChange={(e) => setDraftId(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        e.preventDefault();
                        addSubject();
                      }
                    }}
                  />
                  <button
                    className="secondary"
                    data-testid="access-add-subject"
                    disabled={!draftId.trim() || saving}
                    onClick={addSubject}
                  >
                    {draftType === 'org'
                      ? t('services.accessAddOrg')
                      : draftType === 'project'
                        ? t('services.accessAddProject')
                        : t('services.accessAddUser')}
                  </button>
                </div>
              </div>

              {subjects.length === 0 ? (
                <p className="muted" data-testid="access-empty">{t('services.accessNeedSubject')}</p>
              ) : (
                <div className="kv-grid" data-testid="access-subjects">
                  {subjects.map((subject) => (
                    <div key={`${subject.subjectType}/${subject.subjectId}`}>
                      <span className="kv-label">{typeLabel(subject.subjectType)}</span>
                      <span>
                        {subject.label || subject.subjectId}
                        <button
                          className="link"
                          style={{ marginLeft: 8 }}
                          data-testid={`access-remove-${subject.subjectId}`}
                          disabled={saving}
                          onClick={() => {
                            const next = subjects.filter(
                              (s) => !(s.subjectType === subject.subjectType && s.subjectId === subject.subjectId),
                            );
                            // Removing the last subject would mean nobody;
                            // fall back to the open default instead.
                            if (next.length === 0) {
                              void save(false, []);
                              return;
                            }
                            void save(true, next);
                          }}
                        >
                          {t('services.accessRemove')}
                        </button>
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </>
          )}
        </>
      )}
    </div>
  );
}

// AutoscalingStatusBlock renders the live autoscaling status (feature
// #16, FR3.2-FR3.4).
function AutoscalingStatusBlock({ status }: { status: AutoscalingStatus }) {
  const { t } = useI18n();
  const state = status.state || 'disabled';
  return (
    <div className="detail-grid" style={{ marginTop: 12 }} data-testid="autoscaling-status-block">
      <div className="detail-item">
        <div className="label">{t('service.state')}</div>
        <div className="value">
          <AutoscalingStateBadge state={state} />
        </div>
      </div>
      <div className="detail-item">
        <div className="label">{t('service.currentDesired')}</div>
        <div className="value">
          {status.currentReplicas} / {status.desiredReplicas}
        </div>
      </div>
      <div className="detail-item">
        <div className="label">{t('service.concurrency')}</div>
        <div className="value">
          {status.currentConcurrency} / {status.targetConcurrency}
        </div>
      </div>
      <div className="detail-item">
        <div className="label">{t('service.lastScalingEvent')}</div>
        <div className="value">{formatTime(status.lastScalingEventAt)}</div>
      </div>
      {state === 'cold-starting' && (
        <div className="detail-item full">
          <div className="value muted" data-testid="cold-starting-hint">
            {t('service.coldStartHint')}
          </div>
        </div>
      )}
      {state === 'error' && status.errorReason && (
        <div className="detail-item full">
          <div className="value error" data-testid="autoscaling-error-reason">
            {status.errorReason}
          </div>
        </div>
      )}
    </div>
  );
}

// normalizeAutoscalingState maps the proto enum name the gateway emits
// (AUTOSCALING_STATE_SCALING_UP) onto the short form the labels use
// (scaling-up). An already-short value passes through.
function normalizeAutoscalingState(state: string): string {
  if (!state.startsWith('AUTOSCALING_STATE_')) return state;
  const short = state.slice('AUTOSCALING_STATE_'.length).toLowerCase();
  return short === 'unspecified' ? 'disabled' : short.replace(/_/g, '-');
}

function AutoscalingStateBadge({ state: rawState }: { state: string }) {
  const { t } = useI18n();
  const state = normalizeAutoscalingState(rawState);
  const labels: Record<string, string> = {
    disabled: t('service.asDisabled'),
    steady: t('service.asSteady'),
    'scaling-up': t('service.asScalingUp'),
    'scaling-down': t('service.asScalingDown'),
    'scaled-to-zero': t('service.asScaledToZero'),
    'cold-starting': t('service.asColdStarting'),
    error: t('service.asError'),
  };
  return (
    <span className={`badge ${state}`} data-testid={`autoscaling-state-${state}`}>
      {labels[state] || state}
    </span>
  );
}
