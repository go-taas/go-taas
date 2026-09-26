// Admin Accelerators page (feature #18): the read-only accelerator fleet
// inventory — a card-type summary strip plus a per-node table with
// vendor/health filters, node-name search, pagination, and 30s polling.
// Implements docs/design/accelerator-inventory.md FR1, FR2, FR5.

import { useCallback, useEffect, useState } from 'react';
import { navigate } from '../router';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime, type PageMeta } from '../api';
import { ErrorBanner, Pagination, usePolling } from '../components';

interface AcceleratorNodeSummary {
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
}

interface CardTypeSummary {
  vendor: string;
  cardType: string;
  total: string;
  allocated: string;
  free: string;
  nodeCount: string;
}

interface NodesResponse {
  response: { code: number; message: string };
  nodes: AcceleratorNodeSummary[];
  pageMeta?: PageMeta;
}

interface CardTypesResponse {
  response: { code: number; message: string };
  cardTypes: CardTypeSummary[];
}

const PAGE_SIZE = 20;
const POLL_MS = 30_000;
const VENDORS = ['nvidia', 'iluvatar', 'metax', 'unspecified'];
const HEALTHS = ['healthy', 'degraded', 'unknown'];

// vendorBadgeClass maps a vendor to a stable badge style.
function vendorBadgeClass(vendor: string): string {
  switch (vendor) {
    case 'nvidia':
      return 'badge active';
    case 'iluvatar':
      return 'badge autoscaled';
    case 'metax':
      return 'badge steady';
    default:
      return 'badge terminated';
  }
}

// healthBadgeClass maps a composite health to a badge style.
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

export default function AcceleratorsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const [nodes, setNodes] = useState<AcceleratorNodeSummary[]>([]);
  const [cardTypes, setCardTypes] = useState<CardTypeSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [vendor, setVendor] = useState('');
  const [health, setHealth] = useState('');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(
    async (showLoading: boolean) => {
      if (showLoading) setLoading(true);
      setRefreshing(true);
      setError('');
      try {
        const params = new URLSearchParams({
          'page.offset': String(offset),
          'page.limit': String(PAGE_SIZE),
        });
        if (vendor) params.set('vendor', vendor);
        if (health) params.set('health', health);
        if (search) params.set('search', search);
        const [nodesData, cardData] = await Promise.all([
          api.get<NodesResponse>(
            `/api/v1/admin/accelerators/nodes?${params.toString()}`,
            orgId,
          ),
          api.get<CardTypesResponse>('/api/v1/admin/accelerators/card-types', orgId),
        ]);
        setNodes(nodesData.nodes || []);
        setTotal(parseInt(nodesData.pageMeta?.total || '0', 10) || 0);
        setCardTypes(cardData.cardTypes || []);
        setLastUpdated(Math.floor(Date.now() / 1000));
        setStale(false);
      } catch (e) {
        setError(e instanceof Error ? e.message : 'failed to load accelerators');
        // A failed poll keeps the last good data (FR5.3).
        if (nodes.length > 0) setStale(true);
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [api, orgId, offset, vendor, health, search, nodes.length],
  );

  useEffect(() => {
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [offset, vendor, health, search]);

  usePolling(() => void load(false), POLL_MS, !loading);

  const refresh = () => void load(false);

  const zeroNodeVendors = VENDORS.filter(
    (v) => !cardTypes.some((c) => c.vendor === v),
  );

  return (
    <div data-testid="accelerators-page">
      <div className="page-header">
        <div>
          <h1>Accelerators</h1>
          <div className="subtitle">
            GPU fleet capacity and health across NVIDIA, Iluvatar CoreX, and MetaX.
          </div>
        </div>
        <div className="toolbar">
          <span className="muted" data-testid="accelerators-last-updated">
            Last updated: {lastUpdated ? formatTime(lastUpdated) : '—'}
          </span>
          <button
            className="secondary"
            data-testid="accelerators-refresh"
            disabled={refreshing}
            onClick={refresh}
          >
            {refreshing ? 'Refreshing…' : 'Refresh'}
          </button>
        </div>
      </div>

      {error && (
        <div data-testid="accelerators-error">
          <ErrorBanner message={error} />
        </div>
      )}
      {stale && (
        <div className="warning-banner" data-testid="accelerators-stale-banner">
          Showing stale data — the last poll failed.{' '}
          <button className="link" data-testid="accelerators-stale-retry" onClick={refresh}>
            Retry
          </button>
        </div>
      )}

      {zeroNodeVendors.map((v) => (
        <div className="warning-banner" data-testid={`accelerators-vendor-banner-${v}`}>
          {v}: 0 nodes
        </div>
      ))}

      <div className="dashboard-cards" data-testid="accelerators-summary-strip">
        {cardTypes.map((c) => {
          const free = parseInt(c.free || '0', 10);
          const totalGpus = parseInt(c.total || '0', 10);
          const pct = totalGpus > 0 ? Math.round((free / totalGpus) * 100) : 0;
          const noFree = free === 0;
          return (
            <div
              key={`${c.vendor}-${c.cardType}`}
              className={`dashboard-card${noFree ? ' muted' : ''}`}
              data-testid={`accelerator-card-${c.vendor}-${c.cardType}`}
            >
              <div className="detail-item">
                <span className={vendorBadgeClass(c.vendor)}>{c.vendor}</span>
              </div>
              <div className="detail-item">
                <div className="label">Card type</div>
                <div className="value">{c.cardType}</div>
              </div>
              <div className="detail-item">
                <div className="label">Free / Total</div>
                <div className="value">
                  {c.free} / {c.total}
                </div>
              </div>
              <div className="detail-item">
                <div className="label">Utilization</div>
                <div className="value">{pct}% free</div>
              </div>
              <div className="detail-item">
                <div className="label">Nodes</div>
                <div className="value">{c.nodeCount}</div>
              </div>
              {noFree && (
                <div
                  className="badge failed"
                  data-testid={`accelerator-card-no-free-${c.vendor}-${c.cardType}`}
                >
                  No free capacity
                </div>
              )}
            </div>
          );
        })}
        {cardTypes.length === 0 && !loading && (
          <div className="empty-state" data-testid="accelerators-summary-empty">
            No card types yet.
          </div>
        )}
      </div>

      <div className="toolbar" style={{ marginBottom: 14 }} data-testid="accelerators-filters">
        <select
          data-testid="accelerators-vendor-filter"
          value={vendor}
          onChange={(e) => {
            setVendor(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">All vendors</option>
          {VENDORS.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
        <select
          data-testid="accelerators-health-filter"
          value={health}
          onChange={(e) => {
            setHealth(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">All health</option>
          {HEALTHS.map((h) => (
            <option key={h} value={h}>
              {h}
            </option>
          ))}
        </select>
        <input
          data-testid="accelerators-search"
          placeholder="Search node name"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setOffset(0);
          }}
        />
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">Loading…</div>
        ) : nodes.length === 0 ? (
          <div className="empty-state" data-testid="accelerators-empty">
            No accelerator nodes found — install a GPU Operator and label your
            compute nodes.
          </div>
        ) : (
          <table className="data" data-testid="accelerators-table">
            <thead>
              <tr>
                <th>Node</th>
                <th>Vendor</th>
                <th>Card type(s)</th>
                <th>GPUs (alloc/free)</th>
                <th>Driver</th>
                <th>Device plugin</th>
                <th>Readiness</th>
                <th>Health</th>
                <th>Last updated</th>
              </tr>
            </thead>
            <tbody>
              {nodes.map((n) => (
                <tr key={n.nodeId} data-testid={`accelerator-node-${n.nodeId}`}>
                  <td>
                    <a
                      href={`/admin/accelerators/${n.nodeId}`}
                      onClick={(e) => {
                        e.preventDefault();
                        navigate(`/admin/accelerators/${n.nodeId}`);
                      }}
                    >
                      {n.name}
                    </a>
                  </td>
                  <td>
                    <span className={vendorBadgeClass(n.vendor)}>{n.vendor}</span>
                  </td>
                  <td>{(n.cardTypes || []).join(', ')}</td>
                  <td>
                    {n.gpusAllocated} / {n.gpusFree}
                  </td>
                  <td className="mono">{n.driverVersion || '—'}</td>
                  <td>
                    <span className={healthBadgeClass(n.devicePluginState)}>
                      {n.devicePluginState}
                    </span>
                  </td>
                  <td>
                    <span className={healthBadgeClass(n.readiness)}>{n.readiness}</span>
                  </td>
                  <td>
                    <span className={healthBadgeClass(n.health)}>{n.health}</span>
                  </td>
                  <td className="muted">{formatTime(n.lastUpdatedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {!loading && nodes.length > 0 && (
          <Pagination
            offset={offset}
            limit={PAGE_SIZE}
            total={total}
            onPageChange={setOffset}
          />
        )}
      </div>
    </div>
  );
}