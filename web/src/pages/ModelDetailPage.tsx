// Model detail page: metadata + full version list with per-version deploy.
// Implements docs/design/model-catalog-deployment.md FR2.3, AC2.

import { useCallback, useEffect, useState } from 'react';
import { api, formatTime, type ModelSummary } from '../api';
import { useOrg } from '../org';
import { usePolling } from '../components';
import { BackLink, ErrorBanner } from '../components';
import DeployDialog from '../components/DeployDialog';

interface ModelResponse {
  response: { code: number; message: string };
  model: ModelSummary & { description?: string };
  versions: string[];
}

export default function ModelDetailPage() {
  const { orgId } = useOrg();
  const id = window.location.pathname.split('/').pop() || '';
  const [model, setModel] = useState<ModelResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [deployVersion, setDeployVersion] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<ModelResponse>(`/api/v1/models/${id}`, orgId);
      setModel(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load model');
    } finally {
      setLoading(false);
    }
  }, [id, orgId]);

  useEffect(() => {
    void load();
  }, [load]);

  // The version list can grow (registering another version elsewhere);
  // a light periodic refresh keeps the detail page honest.
  usePolling(() => void load(), 10000, !loading && !error);

  if (loading) return <div className="loading">Loading…</div>;
  if (error)
    return (
      <div>
        <BackLink to="/models" label="Back to Models" />
        <ErrorBanner message={error} />
      </div>
    );
  if (!model) return null;

  return (
    <div>
      <BackLink to="/models" label="Back to Models" />
      <div className="page-header">
        <div>
          <h1 data-testid="model-detail-name">{model.model.name}</h1>
          <div className="subtitle mono">{model.model.modelId}</div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3 style={{ marginTop: 0 }}>Metadata</h3>
        <div className="detail-grid">
          <div className="detail-item">
            <div className="label">Latest version</div>
            <div className="value mono" data-testid="model-latest-version">
              {model.model.latestVersion}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">Weight path</div>
            <div className="value mono">{model.model.weightPath}</div>
          </div>
          <div className="detail-item">
            <div className="label">Created</div>
            <div className="value">{formatTime(model.model.createdAt)}</div>
          </div>
          {model.model.description && (
            <div className="detail-item">
              <div className="label">Description</div>
              <div className="value">{model.model.description}</div>
            </div>
          )}
        </div>
      </div>

      <div className="panel">
        <h3 style={{ marginTop: 0 }}>Versions</h3>
        {model.versions.length === 0 ? (
          <div className="empty-state">No versions.</div>
        ) : (
          <table className="data" data-testid="model-versions-table">
            <thead>
              <tr>
                <th>Version</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {model.versions.map((v) => (
                <tr key={v} data-testid={`model-version-row-${v}`}>
                  <td className="mono">{v}</td>
                  <td>
                    <button
                      className="link"
                      data-testid={`deploy-version-${v}`}
                      onClick={() => setDeployVersion(v)}
                    >
                      Deploy this version
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {deployVersion && (
        <DeployDialog
          orgId={orgId}
          modelId={model.model.modelId}
          modelName={model.model.name}
          initialVersion={deployVersion}
          onClose={() => setDeployVersion(null)}
          onDeployed={() => setDeployVersion(null)}
        />
      )}
    </div>
  );
}
