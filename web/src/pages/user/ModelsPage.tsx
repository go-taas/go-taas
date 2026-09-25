// End-user Models page: the masked user-realm model catalog with a
// read-only autoscaling indicator (feature #16, FR4.1).

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { navigate } from '../../router';
import { type AvailableModel, type PageMeta } from '../../api';

interface ListResponse {
  response: { code: number; message: string };
  models: AvailableModel[];
  pageMeta?: PageMeta;
}

export default function ModelsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const [models, setModels] = useState<AvailableModel[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .get<ListResponse>('/api/v1/models?page.limit=100', orgId)
      .then((data) => setModels(data.models || []))
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load models'))
      .finally(() => setLoading(false));
  }, [api, orgId]);

  return (
    <div className="page">
      <h1>Models</h1>
      {error && <div className="error">{error}</div>}
      {loading ? (
        <div className="loading">Loading…</div>
      ) : models.length === 0 ? (
        <div className="empty" data-testid="models-empty">No models available.</div>
      ) : (
        <table className="data" data-testid="models-table">
          <thead>
            <tr>
              <th>Model</th>
              <th>Version</th>
              <th>Autoscaling</th>
            </tr>
          </thead>
          <tbody>
            {models.map((m) => (
              <tr
                key={m.modelId}
                data-testid={`model-row-${m.name}`}
                style={{ cursor: 'pointer' }}
                onClick={() => navigate(`/models/${m.modelId}`)}
              >
                <td>
                  <strong>{m.name}</strong>
                </td>
                <td className="muted">{m.latestVersion}</td>
                <td>
                  <AutoscalingIndicator autoscaling={m.autoscaling} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

export function AutoscalingIndicator({ autoscaling }: { autoscaling?: AvailableModel['autoscaling'] }) {
  if (!autoscaling) {
    return <span className="badge fixed" data-testid="autoscaling-fixed">Fixed</span>;
  }
  if (!autoscaling.autoscaled) {
    return <span className="badge fixed" data-testid="autoscaling-fixed">Fixed</span>;
  }
  if (autoscaling.state === 'scaled-to-zero') {
    return <span className="badge scaled-to-zero" data-testid="autoscaling-scaled-to-zero">Scaled to zero</span>;
  }
  if (autoscaling.state === 'warming-up') {
    return <span className="badge cold-starting" data-testid="autoscaling-warming-up">Warming up</span>;
  }
  return (
    <span className="badge autoscaled" data-testid="autoscaling-autoscaled">
      Autoscaled · {autoscaling.currentReplicas} replicas
    </span>
  );
}