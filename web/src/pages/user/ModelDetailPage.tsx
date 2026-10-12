// End-user Model detail page: a read-only autoscaling summary for a
// model (feature #16, FR4.2-FR4.4) and the masked compatibility
// projection (feature #19, FR5.1-FR5.2). No edit controls.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { navigate } from '../../router';
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
  const { t } = useI18n();
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
      .catch((e) => setError(e instanceof Error ? e.message : t('umodeldetail.loadFailed')))
      .finally(() => setLoading(false));
    // Feature #19: the masked compatibility projection (FR5.2).
    api
      .get<ModelCompatibilityResponse>(`/api/v1/models/${id}/compatibility`, orgId)
      .then(setCompat)
      .catch((e) => setCompatError(e instanceof Error ? e.message : t('umodeldetail.compatFailed')));
    // Feature #20: the masked performance projection (AD2).
    api
      .get<ModelLoadTestsResponse>(`/api/v1/models/${id}/load-tests`, orgId)
      .then(setPerf)
      .catch((e) => setPerfError(e instanceof Error ? e.message : t('umodeldetail.perfFailed')));
  }, [api, orgId, id, t]);

  if (loading) return <div className="loading">{t('common.loading')}</div>;
  if (error)
    return (
      <div>
        <BackLink to="/models" label={t('umodeldetail.back')} />
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
      <BackLink to="/models" label={t('umodeldetail.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="model-detail-name">
            {m.name}
            {m.external && (
              <span className="badge external" data-testid="model-detail-external-marker">
                External
              </span>
            )}
          </h1>
          <div className="subtitle mono">
            {m.modelId}
            {m.external && (
              <span data-testid="model-detail-provider-name"> · Provided by {m.providerName} (external)</span>
            )}
          </div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>{t('umodeldetail.autoscaling')}</h3>
        {!as ? (
          <p className="muted" data-testid="model-autoscaling-none">{t('umodeldetail.asEmpty')}</p>
        ) : (
          <div className="detail-grid" data-testid="model-autoscaling-summary">
            <div className="detail-item">
              <div className="label">{t('umodeldetail.status')}</div>
              <div className="value">
                <AutoscalingStateBadge state={as.state} />
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('umodeldetail.autoscaling2')}</div>
              <div className="value">{as.autoscaled ? t('common.on') : t('common.off')}</div>
            </div>
            <div className="detail-item">
              <div className="label">{t('umodeldetail.replicas')}</div>
              <div className="value" data-testid="model-autoscaling-replicas">
                {as.currentReplicas}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('umodeldetail.minMax')}</div>
              <div className="value">
                {as.minReplicas} / {as.maxReplicas}
              </div>
            </div>
            <div className="detail-item">
              <div className="label">{t('umodeldetail.scaleToZero')}</div>
              <div className="value">{as.scaleToZero ? t('common.on') : t('common.off')}</div>
            </div>
          </div>
        )}
        {as?.state === 'warming-up' && (
          <p className="muted" data-testid="model-warming-up-hint">
            {t('umodeldetail.warmingHint')}
          </p>
        )}
      </div>

      <div className="panel" data-testid="model-detail-compatibility">
        <h3 style={{ marginTop: 0 }}>{t('umodeldetail.compatibility')}</h3>
        {compatError ? (
          <div className="error" data-testid="model-detail-compat-error">{compatError}</div>
        ) : entries.length === 0 ? (
          <p className="muted" data-testid="model-detail-compat-empty">
            {t('umodeldetail.compatEmpty')}
          </p>
        ) : (
          <>
            <p className="muted" data-testid="model-detail-compatibility-summary">
              {t('umodeldetail.compatSummary', {
                n: supported,
                s: supported === 1 ? '' : 's',
                n2: experimental,
              })}
            </p>
            <table className="data" data-testid="model-detail-compatibility-table">
              <thead>
                <tr>
                  <th>{t('umodeldetail.colEngine')}</th>
                  <th>{t('umodeldetail.colCardType')}</th>
                  <th>{t('umodeldetail.colStatus')}</th>
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
                        {e.status === 'supported' ? t('umodeldetail.supported') : t('umodeldetail.experimental')}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <p className="muted">{t('umodeldetail.compatMuted')}</p>
          </>
        )}
      </div>

      <div className="panel" data-testid="model-detail-performance">
        <h3 style={{ marginTop: 0 }}>{t('umodeldetail.performance')}</h3>
        <p className="muted">
          <a
            href={`/models/${id}/observability`}
            data-testid="user-model-observability-link"
            onClick={(e) => {
              e.preventDefault();
              navigate(`/models/${id}/observability`);
            }}
          >
            {t('umodeldetail.observabilityLink')}
          </a>
        </p>
        {perfError ? (
          <div className="error" data-testid="model-detail-performance-error">{perfError}</div>
        ) : (perf?.results || []).length === 0 ? (
          <p className="muted" data-testid="model-detail-performance-empty">
            {t('umodeldetail.perfEmpty')}
          </p>
        ) : (
          <table className="data" data-testid="model-detail-performance-table">
            <thead>
              <tr>
                <th>{t('umodeldetail.colThroughput')}</th>
                <th>{t('umodeldetail.colP95')}</th>
                <th>{t('umodeldetail.colTokensSec')}</th>
                <th>{t('umodeldetail.colErrorRate')}</th>
                <th>{t('umodeldetail.colConcurrency')}</th>
                <th>{t('umodeldetail.colMeasured')}</th>
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
  const { t } = useI18n();
  const label = state === 'warming-up' ? t('umodeldetail.warmingUp') : state === 'scaled-to-zero' ? t('umodeldetail.scaledToZero') : state;
  return (
    <span className={`badge ${state}`} data-testid={`model-autoscaling-state-${state}`}>
      {label}
    </span>
  );
}
