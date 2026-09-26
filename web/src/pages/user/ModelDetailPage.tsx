// End-user Model detail page: a read-only autoscaling summary for a
// model (feature #16, FR4.2-FR4.4) and the masked compatibility
// projection (feature #19, FR5.1-FR5.2). No edit controls.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { BackLink } from '../../components';
import { type GetAvailableModelResponse } from '../../api';

interface ModelCompatibilityEntry {
  engine: string;
  cardType: string;
  cardVendor: string;
  status: string; // supported | experimental (masked)
}
interface ModelCompatibilityResponse {
  response: { code: number; message: string };
  entries: ModelCompatibilityEntry[];
  supportedCount: string;
  experimentalCount: string;
}

export default function ModelDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const id = window.location.pathname.split('/').pop() || '';
  const [data, setData] = useState<GetAvailableModelResponse | null>(null);
  const [compat, setCompat] = useState<ModelCompatibilityResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [compatError, setCompatError] = useState('');

  useEffect(() => {
    api
      .get<GetAvailableModelResponse>(`/api/v1/models/${id}`, orgId)
      .then(setData)
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load model'))
      .finally(() => setLoading(false));
    // Feature #19: the masked compatibility projection (FR5.2).
    api
      .get<ModelCompatibilityResponse>(`/api/v1/models/${id}/compatibility`, orgId)
      .then(setCompat)
      .catch((e) => setCompatError(e instanceof Error ? e.message : 'failed to load compatibility'));
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
  const entries = compat?.entries || [];
  const supported = parseInt(compat?.supportedCount || '0', 10) || 0;
  const experimental = parseInt(compat?.experimentalCount || '0', 10) || 0;

  return (
    <div className="page" data-testid="model-detail-page">
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

      <div className="panel" data-testid="model-detail-compatibility">
        <h3 style={{ marginTop: 0 }}>Compatibility</h3>
        {compatError ? (
          <div className="error" data-testid="model-detail-compat-error">{compatError}</div>
        ) : entries.length === 0 ? (
          <p className="muted" data-testid="model-detail-compat-empty">
            No supported engine/card-type combinations for this model yet.
          </p>
        ) : (
          <>
            <p className="muted" data-testid="model-detail-compatibility-summary">
              Supported on {supported} engine/card-type combination{supported === 1 ? '' : 's'} and
              experimental on {experimental}.
            </p>
            <table className="data" data-testid="model-detail-compatibility-table">
              <thead>
                <tr>
                  <th>Engine</th>
                  <th>Card type</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {entries.map((e) => (
                  <tr key={`${e.engine}-${e.cardType}`} data-testid={`model-detail-compat-row-${e.engine}-${e.cardType}`}>
                    <td>{e.engine}</td>
                    <td>
                      <span className="badge steady">{e.cardVendor}</span> {e.cardType}
                    </td>
                    <td>
                      <span className={`badge ${e.status === 'supported' ? 'running' : 'autoscaled'}`}>
                        {e.status === 'supported' ? 'Supported' : 'Experimental'}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <p className="muted">Compatibility is curated by the platform operator.</p>
          </>
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