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
} from '../api';
import { useOrg } from '../org';
import {
  BackLink,
  CopyButton,
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
    </div>
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
