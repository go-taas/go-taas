// Service detail page: state, spec, endpoints with copy + curl snippet,
// failure reason, delete-and-retry for failed services.
// Implements docs/design/model-catalog-deployment.md FR4.2, FR4.3, FR7.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  formatTime,
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

interface ServiceResponse {
  response: { code: number; message: string };
  service: InferenceServiceSummary & { accelerator?: string; acceleratorType?: string };
  endpoints: string[];
  autoscaling?: AutoscalingPolicy;
  autoscalingStatus?: AutoscalingStatus;
}

export default function ServiceDetailPage() {
  const { orgId } = useOrg();
  const id = window.location.pathname.split('/').pop() || '';
  const [data, setData] = useState<ServiceResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setError('');
    try {
      const res = await api.get<ServiceResponse>(
        `/api/v1/admin/inference-services/${id}`,
        orgId,
      );
      setData(res);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load service');
    } finally {
      setLoading(false);
    }
  }, [id, orgId]);

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

  if (loading) return <div className="loading">Loading…</div>;
  if (error)
    return (
      <div>
        <BackLink to="/admin/inference-services" label="Back to Inference Services" />
        <ErrorBanner message={error} />
      </div>
    );
  if (!data) return null;

  const s = data.service;
  const running = s.state === 'running';

  return (
    <div>
      <BackLink to="/admin/inference-services" label="Back to Inference Services" />
      <div className="page-header">
        <div>
          <h1 data-testid="service-detail-name">{s.name}</h1>
          <div className="subtitle mono">{s.serviceId}</div>
        </div>
        <StateBadge state={s.state} />
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>Specification</h3>
        <div className="detail-grid">
          <div className="detail-item">
            <div className="label">Model</div>
            <div className="value mono">
              {s.modelId.slice(0, 8)}… @ {s.modelVersion}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">Image</div>
            <div className="value mono">{s.imageId}</div>
          </div>
          <div className="detail-item">
            <div className="label">Accelerator</div>
            <div className="value">
              {s.accelerator || '—'}
              {s.acceleratorType ? ` (${s.acceleratorType})` : ''}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">Replicas</div>
            <div className="value" data-testid="service-detail-replicas">
              {s.replicas}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">Updated</div>
            <div className="value">{formatTime(s.updatedAt)}</div>
          </div>
        </div>
      </div>

      {data.autoscaling && (
        <div className="panel" style={{ marginBottom: 16 }} data-testid="autoscaling-card">
          <h3 style={{ marginTop: 0 }}>Autoscaling</h3>
          <div className="detail-grid">
            <div className="detail-item">
              <div className="label">Enabled</div>
              <div className="value">{data.autoscaling.enabled ? 'On' : 'Off'}</div>
            </div>
            <div className="detail-item">
              <div className="label">Min / Max</div>
              <div className="value">
                {data.autoscaling.minReplicas} / {data.autoscaling.maxReplicas}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">Target concurrency</div>
              <div className="value">{data.autoscaling.targetConcurrency}</div>
            </div>
            <div className="detail-item">
              <div className="label">Scale to zero</div>
              <div className="value">{data.autoscaling.scaleToZero ? 'On' : 'Off'}</div>
            </div>
            <div className="detail-item">
              <div className="label">Cooldown</div>
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
          Deployment failed. The service can be deleted and re-created from the
          Models page.
        </div>
      )}

      <div className="panel">
        <h3 style={{ marginTop: 0 }}>Endpoints</h3>
        {!running && (
          <p className="muted" data-testid="endpoints-placeholder">
            {s.state === 'pending' || s.state === 'deploying'
              ? 'Endpoints appear when the service reaches the running state…'
              : 'Endpoints are available only while the service is running.'}
          </p>
        )}
        {running &&
          (data.endpoints.length === 0 ? (
            <p className="muted">No endpoints registered.</p>
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
            <h3>Try it</h3>
            <div className="curl-snippet" data-testid="curl-snippet">
              {`curl ${data.endpoints[0]}/v1/chat/completions \\\n  -H "Authorization: Bearer sk-..." \\\n  -H "Content-Type: application/json" \\\n  -d '{"model": "${s.name}", "messages": [{"role": "user", "content": "Hello!"}]}'`}
            </div>
            <CopyButton
              text={`curl ${data.endpoints[0]}/v1/chat/completions -H "Authorization: Bearer sk-..." -H "Content-Type: application/json" -d '{"model": "${s.name}", "messages": [{"role": "user", "content": "Hello!"}]}'`}
              label="Copy curl"
            />
          </>
        )}
      </div>
    </div>
  );
}

// AutoscalingStatusBlock renders the live autoscaling status (feature
// #16, FR3.2-FR3.4).
function AutoscalingStatusBlock({ status }: { status: AutoscalingStatus }) {
  const state = status.state || 'disabled';
  return (
    <div className="detail-grid" style={{ marginTop: 12 }} data-testid="autoscaling-status-block">
      <div className="detail-item">
        <div className="label">State</div>
        <div className="value">
          <AutoscalingStateBadge state={state} />
        </div>
      </div>
      <div className="detail-item">
        <div className="label">Current / Desired</div>
        <div className="value">
          {status.currentReplicas} / {status.desiredReplicas}
        </div>
      </div>
      <div className="detail-item">
        <div className="label">Concurrency</div>
        <div className="value">
          {status.currentConcurrency} / {status.targetConcurrency}
        </div>
      </div>
      <div className="detail-item">
        <div className="label">Last scaling event</div>
        <div className="value">{formatTime(status.lastScalingEventAt)}</div>
      </div>
      {state === 'cold-starting' && (
        <div className="detail-item full">
          <div className="value muted" data-testid="cold-starting-hint">
            A request arrived — spinning up from zero.
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

function AutoscalingStateBadge({ state }: { state: string }) {
  const labels: Record<string, string> = {
    disabled: 'Disabled',
    steady: 'Steady',
    'scaling-up': 'Scaling up',
    'scaling-down': 'Scaling down',
    'scaled-to-zero': 'Scaled to zero',
    'cold-starting': 'Cold-starting',
    error: 'Error',
  };
  return (
    <span className={`badge ${state}`} data-testid={`autoscaling-state-${state}`}>
      {labels[state] || state}
    </span>
  );
}
