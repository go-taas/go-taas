// Models page: the catalog with search, pagination, register action and
// per-card deploy entry. Implements docs/design/model-catalog-deployment.md
// FR1, FR2, AC1, AC2.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { navigate } from '../router';
import { api, ApiError, formatTime, type ModelSummary, type PageMeta } from '../api';
import { useOrg } from '../org';
import { Dialog, ErrorBanner, Pagination } from '../components';
import DeployDialog from '../components/DeployDialog';

interface ListResponse {
  response: { code: number; message: string };
  models: ModelSummary[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;

export default function ModelsPage() {
  const { orgId } = useOrg();
  const [models, setModels] = useState<ModelSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [search, setSearch] = useState('');
  const [registerOpen, setRegisterOpen] = useState(false);
  const [deployTarget, setDeployTarget] = useState<{
    modelId: string;
    name: string;
    version: string;
  } | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<ListResponse>(
        `/api/v1/models?page.offset=${offset}&page.limit=${PAGE_SIZE}`,
        orgId,
      );
      setModels(data.models || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load models');
    } finally {
      setLoading(false);
    }
  }, [orgId, offset]);

  useEffect(() => {
    void load();
  }, [load]);

  // FR2.2: client-side name filter over the current page.
  const filtered = useMemo(() => {
    if (!search.trim()) return models;
    const q = search.trim().toLowerCase();
    return models.filter((m) => m.name.toLowerCase().includes(q));
  }, [models, search]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>Models</h1>
          <div className="subtitle">
            Registered open-source models with their weight paths in object
            storage.
          </div>
        </div>
        <button data-testid="register-model" onClick={() => setRegisterOpen(true)}>
          Register Model
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <input
          type="text"
          data-testid="model-search"
          placeholder="Search by name…"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">Loading…</div>
        ) : filtered.length === 0 ? (
          <div className="empty-state" data-testid="models-empty">
            {search
              ? 'No models match your search.'
              : 'No models registered yet. Register a model to deploy it.'}
          </div>
        ) : (
          <table className="data" data-testid="models-table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Latest version</th>
                <th>Weight path</th>
                <th>Created</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((m) => (
                <tr
                  key={m.modelId}
                  data-testid={`model-row-${m.name}`}
                  style={{ cursor: 'pointer' }}
                  onClick={() => navigate(`/models/${m.modelId}`)}
                >
                  <td>
                    <strong>{m.name}</strong>
                  </td>
                  <td className="mono">{m.latestVersion}</td>
                  <td className="mono muted">{m.weightPath}</td>
                  <td>{formatTime(m.createdAt)}</td>
                  <td onClick={(e) => e.stopPropagation()}>
                    <button
                      className="link"
                      data-testid={`deploy-${m.name}`}
                      onClick={() =>
                        setDeployTarget({
                          modelId: m.modelId,
                          name: m.name,
                          version: m.latestVersion,
                        })
                      }
                    >
                      Deploy
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {total > PAGE_SIZE && (
          <Pagination
            offset={offset}
            limit={PAGE_SIZE}
            total={total}
            onPageChange={setOffset}
          />
        )}
      </div>

      {registerOpen && (
        <RegisterDialog
          orgId={orgId}
          onClose={() => setRegisterOpen(false)}
          onRegistered={() => {
            setRegisterOpen(false);
            void load();
          }}
        />
      )}

      {deployTarget && (
        <DeployDialog
          orgId={orgId}
          modelId={deployTarget.modelId}
          modelName={deployTarget.name}
          initialVersion={deployTarget.version}
          onClose={() => setDeployTarget(null)}
          onDeployed={(serviceId) => {
            setDeployTarget(null);
            navigate(`/inference-services/${serviceId}`);
          }}
        />
      )}
    </div>
  );
}

function RegisterDialog({
  orgId,
  onClose,
  onRegistered,
}: {
  orgId: string;
  onClose: () => void;
  onRegistered: () => void;
}) {
  const [name, setName] = useState('');
  const [version, setVersion] = useState('');
  const [weightPath, setWeightPath] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    // FR1.1 validation: name 1-128, version 1-64, weight path required.
    if (!name.trim() || name.trim().length > 128) {
      setError('Name is required (1-128 characters).');
      return;
    }
    if (!version.trim() || version.trim().length > 64) {
      setError('Version is required (1-64 characters).');
      return;
    }
    if (!weightPath.trim()) {
      setError('Weight path is required (e.g. models/qwen2.5-32b/).');
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.post('/api/v1/models', orgId, {
        name: name.trim(),
        version: version.trim(),
        weightPath: weightPath.trim(),
        description: description.trim(),
      });
      onRegistered();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to register model');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title="Register Model" onClose={onClose} testId="register-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="model-name">Name</label>
          <input
            id="model-name"
            data-testid="model-name-input"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. qwen2.5-32b"
          />
        </div>
        <div className="form-field">
          <label htmlFor="model-version">Version</label>
          <input
            id="model-version"
            data-testid="model-version-input"
            value={version}
            maxLength={64}
            onChange={(e) => setVersion(e.target.value)}
            placeholder="e.g. v1"
          />
        </div>
        <div className="form-field full">
          <label htmlFor="model-weight-path">Weight path</label>
          <input
            id="model-weight-path"
            data-testid="model-weight-path-input"
            value={weightPath}
            onChange={(e) => setWeightPath(e.target.value)}
            placeholder="e.g. models/qwen2.5-32b/"
          />
        </div>
        <div className="form-field full">
          <label htmlFor="model-description">Description (optional)</label>
          <textarea
            id="model-description"
            data-testid="model-description-input"
            value={description}
            maxLength={1024}
            rows={3}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          data-testid="submit-register-model"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? 'Registering…' : 'Register'}
        </button>
      </div>
    </Dialog>
  );
}
