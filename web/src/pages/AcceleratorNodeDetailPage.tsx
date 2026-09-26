// Admin Accelerator Node Detail page (feature #18): per-GPU breakdown,
// health signals, resources, labels/taints, and warmup-task context for
// one node. Implements docs/design/accelerator-inventory.md FR3.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime } from '../api';
import { BackLink, ErrorBanner } from '../components';

interface AcceleratorGPU {
  index: string;
  model: string;
  allocated: boolean;
  free: boolean;
  note: string;
}

interface AcceleratorResource {
  cardType: string;
  allocatable: string;
  allocated: string;
}

interface WarmupTaskRef {
  taskId: string;
  state: string;
  nodeOutcome: string;
}

interface AcceleratorNode {
  summary: {
    nodeId: string;
    name: string;
    vendor: string;
    cardTypes: string[];
    gpusAllocated: string;
    gpusFree: string;
    driverVersion: string;
    devicePluginState: string;
    readiness: string;
    health: string;
    lastUpdatedAt: string;
  };
  gpus: AcceleratorGPU[];
  labels: Record<string, string>;
  taints: string[];
  resources: AcceleratorResource[];
  warmupTasks: WarmupTaskRef[];
}

interface NodeResponse {
  response: { code: number; message: string };
  node: AcceleratorNode;
}

function healthBadgeClass(health: string): string {
  switch (health) {
    case 'healthy':
      return 'badge running';
    case 'degraded':
      return 'badge failed';
    default:
      return 'badge pending';
  }
}

export default function AcceleratorNodeDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const nodeId = window.location.pathname.split('/').pop() || '';
  const [node, setNode] = useState<AcceleratorNode | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notFound, setNotFound] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    setNotFound(false);
    try {
      const data = await api.get<NodeResponse>(
        `/api/v1/admin/accelerators/nodes/${nodeId}`,
        orgId,
      );
      setNode(data.node);
    } catch (e) {
      const code = (e as { code?: number }).code;
      if (code === 10208) {
        setNotFound(true);
      } else {
        setError(e instanceof Error ? e.message : 'failed to load node');
      }
    } finally {
      setLoading(false);
    }
  }, [api, orgId, nodeId]);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading) return <div className="loading">Loading…</div>;

  if (notFound) {
    return (
      <div data-testid="accelerator-node-not-found">
        <div data-testid="accelerator-node-back">
          <BackLink to="/admin/accelerators" label="Back to Accelerators" />
        </div>
        <div className="empty-state">
          Node not found — it may have been removed.
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div>
        <div data-testid="accelerator-node-back">
          <BackLink to="/admin/accelerators" label="Back to Accelerators" />
        </div>
        <ErrorBanner message={error} />
      </div>
    );
  }

  if (!node) return null;
  const s = node.summary;

  return (
    <div data-testid="accelerator-node-detail">
      <div data-testid="accelerator-node-back">
        <BackLink to="/admin/accelerators" label="Back to Accelerators" />
      </div>

      <div className="page-header">
        <div>
          <h1>
            {s.name}{' '}
            <span className={healthBadgeClass(s.health)} data-testid="accelerator-node-health">
              {s.health}
            </span>
          </h1>
          <div className="subtitle">Node {s.nodeId}</div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3>Overview</h3>
        <div className="detail-grid">
          <div className="detail-item">
            <div className="label">Vendor</div>
            <div className="value">{s.vendor}</div>
          </div>
          <div className="detail-item">
            <div className="label">Readiness</div>
            <div className="value">{s.readiness}</div>
          </div>
          <div className="detail-item">
            <div className="label">Device plugin</div>
            <div className="value">{s.devicePluginState}</div>
          </div>
          <div className="detail-item">
            <div className="label">Driver version</div>
            <div className="value mono">{s.driverVersion || '—'}</div>
          </div>
          <div className="detail-item">
            <div className="label">GPUs (alloc/free)</div>
            <div className="value">
              {s.gpusAllocated} / {s.gpusFree}
            </div>
          </div>
          <div className="detail-item">
            <div className="label">Last updated</div>
            <div className="value">{formatTime(s.lastUpdatedAt)}</div>
          </div>
        </div>
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3>GPU breakdown</h3>
        {node.gpus.length === 0 ? (
          <div className="muted">No per-GPU breakdown reported.</div>
        ) : (
          <table className="data" data-testid="accelerator-node-gpus">
            <thead>
              <tr>
                <th>Index</th>
                <th>Model</th>
                <th>Allocated</th>
                <th>Free</th>
                <th>Note</th>
              </tr>
            </thead>
            <tbody>
              {node.gpus.map((g) => (
                <tr key={g.index}>
                  <td>{g.index}</td>
                  <td>{g.model}</td>
                  <td>{g.allocated ? 'yes' : 'no'}</td>
                  <td>{g.free ? 'yes' : 'no'}</td>
                  <td className="muted">{g.note || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3>Resources</h3>
        {node.resources.length === 0 ? (
          <div className="muted">No accelerator resources reported.</div>
        ) : (
          <table className="data" data-testid="accelerator-node-resources">
            <thead>
              <tr>
                <th>Card type</th>
                <th>Allocatable</th>
                <th>Allocated</th>
                <th>Free</th>
              </tr>
            </thead>
            <tbody>
              {node.resources.map((r) => {
                const allocatable = parseInt(r.allocatable || '0', 10);
                const allocated = parseInt(r.allocated || '0', 10);
                return (
                  <tr key={r.cardType}>
                    <td>{r.cardType}</td>
                    <td>{r.allocatable}</td>
                    <td>{r.allocated}</td>
                    <td>{allocatable - allocated}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      <div className="panel" style={{ marginBottom: 16 }}>
        <h3>Labels &amp; taints</h3>
        <div className="detail-grid">
          <div className="detail-item" data-testid="accelerator-node-labels">
            <div className="label">Labels</div>
            <div className="value">
              {Object.keys(node.labels || {}).length === 0
                ? '—'
                : Object.entries(node.labels)
                    .map(([k, v]) => `${k}=${v}`)
                    .join(', ')}
            </div>
          </div>
          <div className="detail-item" data-testid="accelerator-node-taints">
            <div className="label">Taints</div>
            <div className="value">
              {node.taints.length === 0 ? '—' : node.taints.join(', ')}
            </div>
          </div>
        </div>
      </div>

      <div className="panel">
        <h3>Warmup context</h3>
        {node.warmupTasks.length === 0 ? (
          <div className="muted" data-testid="accelerator-node-warmup-empty">
            No warmup tasks targeted this node.
          </div>
        ) : (
          <table className="data" data-testid="accelerator-node-warmup">
            <thead>
              <tr>
                <th>Task</th>
                <th>State</th>
                <th>Node outcome</th>
              </tr>
            </thead>
            <tbody>
              {node.warmupTasks.map((t) => (
                <tr key={t.taskId}>
                  <td className="mono">{t.taskId}</td>
                  <td>{t.state}</td>
                  <td>{t.nodeOutcome || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}