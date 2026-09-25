// End-user Model detail page: a read-only autoscaling summary for a
// model (feature #16, FR4.2-FR4.4). No edit controls.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { BackLink } from '../../components';
import { type GetAvailableModelResponse } from '../../api';

export default function ModelDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const id = window.location.pathname.split('/').pop() || '';
  const [data, setData] = useState<GetAvailableModelResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .get<GetAvailableModelResponse>(`/api/v1/models/${id}`, orgId)
      .then(setData)
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load model'))
      .finally(() => setLoading(false));
  }, [api, orgId, id]);

  if (loading) return <div className="loading">Loading…</div>;
  if (error)
    return (
      <div>
        <BackLink to="/models" label="Back to Models" />
        <div className="error">{error}</div>
      </div>
    );
  if (!data?.model) return null;

  const m = data.model;
  const as = data.autoscaling;

  return (
    <div className="page">
      <BackLink to="/models" label="Back to Models" />
      <div className="page-header">
        <div>
          <h1 data-testid="model-detail-name">{m.name}</h1>
          <div className="subtitle mono">{m.modelId}</div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>Autoscaling</h3>
        {!as ? (
          <p className="muted" data-testid="model-autoscaling-none">No autoscaling information.</p>
        ) : (
          <div className="detail-grid" data-testid="model-autoscaling-summary">
            <div className="detail-item">
              <div className="label">Status</div>
              <div className="value">
                <AutoscalingStateBadge state={as.state} />
              </div>
            </div>
            <div className="detail-item">
              <div className="label">Autoscaling</div>
              <div className="value">{as.autoscaled ? 'On' : 'Off'}</div>
            </div>
            <div className="detail-item">
              <div className="label">Replicas</div>
              <div className="value" data-testid="model-autoscaling-replicas">
                {as.currentReplicas}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">Min / Max</div>
              <div className="value">
                {as.minReplicas} / {as.maxReplicas}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">Scale to zero</div>
              <div className="value">{as.scaleToZero ? 'On' : 'Off'}</div>
            </div>
          </div>
        )}
        {as?.state === 'warming-up' && (
          <p className="muted" data-testid="model-warming-up-hint">
            This model is warming up from zero — the first request may be slower
            than usual.
          </p>
        )}
      </div>
    </div>
  );
}

function AutoscalingStateBadge({ state }: { state: string }) {
  const label = state === 'warming-up' ? 'Warming up' : state === 'scaled-to-zero' ? 'Scaled to zero' : state;
  return (
    <span className={`badge ${state}`} data-testid={`model-autoscaling-state-${state}`}>
      {label}
    </span>
  );
}