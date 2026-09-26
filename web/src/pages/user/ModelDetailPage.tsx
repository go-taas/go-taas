// End-user Model detail page: a read-only autoscaling summary for a
// model (feature #16, FR4.2-FR4.4) and the masked compatibility
// projection (feature #19, FR5.1-FR5.2). No edit controls.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { BackLink } from '../../components';
import { formatTime, type GetAvailableModelResponse } from '../../api';

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

// Feature #20: the masked load-test performance projection (no service
// ids, no operator internals).
interface ModelLoadTestResult {
  throughputRps: number;
  latencyP95Ms: number;
  outputTokensPerSec: number;
  errorRate: number;
  concurrency: number;
  durationSeconds: number;
  completedAt: string;
}
interface ModelLoadTestsResponse {
  response: { code: number; message: string };
  results: ModelLoadTestResult[];
}

export default function ModelDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const id = window.location.pathname.split('/').pop() || '';
  const [data, setData] = useState<GetAvailableModelResponse | null>(null);
  const [compat, setCompat] = useState<ModelCompatibilityResponse | null>(null);
  const [perf, setPerf] = useState<ModelLoadTestsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [compatError, setCompatError] = useState('');
  const [perfError, setPerfError] = useState('');

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
    // Feature #20: the masked performance projection (AD2).
    api
      .get<ModelLoadTestsResponse>(`/api/v1/models/${id}/load-tests`, orgId)
      .then(setPerf)
      .catch((e) => setPerfError(e instanceof Error ? e.message : 'failed to load performance'));
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

      <div className="panel" data-testid="model-detail-performance">
        <h3 style={{ marginTop: 0 }}>Performance</h3>
        {perfError ? (
          <div className="error" data-testid="model-detail-performance-error">{perfError}</div>
        ) : (perf?.results || []).length === 0 ? (
          <p className="muted" data-testid="model-detail-performance-empty">
            No performance results recorded for this model yet.
          </p>
        ) : (
          <table className="data" data-testid="model-detail-performance-table">
            <thead>
              <tr>
                <th>Throughput</th>
                <th>p95 latency</th>
                <th>Tokens/sec</th>
                <th>Error rate</th>
                <th>Concurrency</th>
                <th>Measured</th>
              </tr>
            </thead>
            <tbody>
              {(perf?.results || []).map((r, i) => (
                <tr key={`${r.completedAt}-${i}`} data-testid="model-detail-performance-row">
                  <td>{r.throughputRps.toFixed(1)} req/s</td>
                  <td>{r.latencyP95Ms.toFixed(1)} ms</td>
                  <td>{r.outputTokensPerSec.toFixed(1)}</td>
                  <td>{(r.errorRate * 100).toFixed(1)}%</td>
                  <td>{r.concurrency}</td>
                  <td>{formatTime(r.completedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
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