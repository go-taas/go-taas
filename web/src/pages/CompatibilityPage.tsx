// Admin Compatibility Matrix page (feature #19): a curated
// model × engine × card-type grid/table with single-cell and bulk status
// editing, seeded from the accelerator inventory and image catalog.
// Implements docs/design/compatibility-matrix.md FR1-FR6, AC8-AC11, AC13.
// Admin surface only: route /admin/compatibility, API /api/v1/admin/compatibility/*.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime, type PageMeta } from '../api';
import { Dialog, ErrorBanner, Pagination, Toast, usePolling } from '../components';
import { useI18n } from '../i18n';

interface CompatibilityCell {
  modelId: string;
  modelName: string;
  engine: string;
  cardType: string;
  cardVendor: string;
  status: string; // supported | experimental | unsupported
  note: string;
  notInFleet: boolean;
  updatedAt: string;
}

interface DimensionModel {
  modelId: string;
  modelName: string;
}
interface DimensionEngine {
  engine: string;
  accelerator: string;
}
interface DimensionCardType {
  cardType: string;
  vendor: string;
  inFleet: boolean;
}
interface StatusCounts {
  supported: string;
  experimental: string;
  unsupported: string;
  notInFleet: string;
}

interface MatrixResponse {
  response: { code: number; message: string };
  cells: CompatibilityCell[];
  pageMeta?: PageMeta;
}
interface DimensionsResponse {
  response: { code: number; message: string };
  models: DimensionModel[];
  engines: DimensionEngine[];
  cardTypes: DimensionCardType[];
  statusCounts?: StatusCounts;
}

const PAGE_SIZE = 20;
const POLL_MS = 30_000;
const STATUSES = ['supported', 'experimental', 'unsupported'];

function statusBadgeClass(status: string): string {
  switch (status) {
    case 'supported':
      return 'badge running';
    case 'experimental':
      return 'badge autoscaled';
    default:
      return 'badge terminated';
  }
}

function statusLabel(
  status: string,
  t: (key: string, vars?: Record<string, string | number>) => string,
): string {
  switch (status) {
    case 'supported':
      return t('compat.supported');
    case 'experimental':
      return t('compat.experimental');
    default:
      return t('compat.unsupported');
  }
}

export default function CompatibilityPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [cells, setCells] = useState<CompatibilityCell[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [models, setModels] = useState<DimensionModel[]>([]);
  const [engines, setEngines] = useState<DimensionEngine[]>([]);
  const [cardTypes, setCardTypes] = useState<DimensionCardType[]>([]);
  const [counts, setCounts] = useState<StatusCounts | null>(null);
  const [modelFilter, setModelFilter] = useState('');
  const [engineFilter, setEngineFilter] = useState('');
  const [cardFilter, setCardFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [search, setSearch] = useState('');
  const [view, setView] = useState<'grid' | 'table'>('grid');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [stale, setStale] = useState(false);
  const [lastUpdated, setLastUpdated] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [editCell, setEditCell] = useState<CompatibilityCell | null>(null);
  const [bulkOpen, setBulkOpen] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());

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
        if (modelFilter) params.set('model_id', modelFilter);
        if (engineFilter) params.set('engine', engineFilter);
        if (cardFilter) params.set('card_type', cardFilter);
        if (statusFilter) params.set('status', statusFilter);
        if (search) params.set('search', search);
        const [matrixData, dimData] = await Promise.all([
          api.get<MatrixResponse>(`/api/v1/admin/compatibility?${params.toString()}`, orgId),
          api.get<DimensionsResponse>('/api/v1/admin/compatibility/dimensions', orgId),
        ]);
        setCells(matrixData.cells || []);
        setTotal(parseInt(matrixData.pageMeta?.total || '0', 10) || 0);
        setModels(dimData.models || []);
        setEngines(dimData.engines || []);
        setCardTypes(dimData.cardTypes || []);
        setCounts(dimData.statusCounts || null);
        setLastUpdated(Math.floor(Date.now() / 1000));
        setStale(false);
      } catch (e) {
        setError(e instanceof Error ? e.message : t('compat.loadFailed'));
        if (cells.length > 0) setStale(true);
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [api, orgId, offset, modelFilter, engineFilter, cardFilter, statusFilter, search, cells.length, t],
  );

  useEffect(() => {
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [offset, modelFilter, engineFilter, cardFilter, statusFilter, search]);

  usePolling(() => void load(false), POLL_MS, !loading);

  const refresh = () => void load(false);

  // The grid renders rows = engines, columns = card types for the
  // selected model (defaults to the first model).
  const gridModelId = modelFilter || models[0]?.modelId || '';
  const gridCells = useMemo(() => {
    if (!gridModelId) return [];
    return cells.filter((c) => c.modelId === gridModelId);
  }, [cells, gridModelId]);

  const selectedCount = selected.size;

  const toggleSelect = (key: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const cellKey = (c: { modelId: string; engine: string; cardType: string }) =>
    `${c.modelId}\u0000${c.engine}\u0000${c.cardType}`;

  const saveCell = async (status: string, note: string) => {
    if (!editCell) return;
    const cell = editCell;
    await api.put<{ response: { code: number; message: string }; cell: CompatibilityCell }>(
      `/api/v1/admin/compatibility/${cell.modelId}/${cell.engine}/${cell.cardType}`,
      orgId,
      { status, note },
    );
    setEditCell(null);
    setNotice(
      t('compat.saveSuccess', {
        name: `${cell.modelName} · ${cell.engine} · ${cell.cardType}`,
      }),
    );
    void load(false);
  };

  const applyBulk = async (status: string, note: string) => {
    const refs = cells
      .filter((c) => selected.has(cellKey(c)))
      .map((c) => ({ modelId: c.modelId, engine: c.engine, cardType: c.cardType }));
    await api.post<{ response: { code: number; message: string }; updated: string }>(
      '/api/v1/admin/compatibility:bulk',
      orgId,
      { cells: refs, status, note },
    );
    setBulkOpen(false);
    setSelected(new Set());
    setNotice(t('compat.bulkSuccess', { count: refs.length }));
    void load(false);
  };

  const empty = !loading && cells.length === 0;

  return (
    <div data-testid="compatibility-page">
      <div className="page-header">
        <div>
          <h1>{t('compat.title')}</h1>
          <div className="subtitle">{t('compat.subtitle')}</div>
        </div>
        <div className="toolbar">
          <span className="muted" data-testid="compatibility-last-updated">
            {t('common.lastUpdated', {
              time: lastUpdated ? formatTime(lastUpdated) : '—',
            })}
          </span>
          <button
            className="secondary"
            data-testid="compatibility-refresh"
            disabled={refreshing}
            onClick={refresh}
          >
            {refreshing ? t('common.refreshing') : t('common.refresh')}
          </button>
          <button
            className="secondary"
            data-testid="compatibility-bulk-edit"
            disabled={selectedCount === 0}
            onClick={() => setBulkOpen(true)}
          >
            {selectedCount > 0
              ? t('compat.bulkEdit', { n: selectedCount })
              : t('compat.bulkEditShort')}
          </button>
        </div>
      </div>

      {error && (
        <div data-testid="compatibility-error">
          <ErrorBanner message={error} />
        </div>
      )}
      <Toast message={notice} onDismiss={() => setNotice('')} testId="compatibility-toast" />
      {stale && (
        <div className="warning-banner" data-testid="compatibility-stale-banner">
          {t('common.staleData')}{' '}
          <button className="link" data-testid="compatibility-stale-retry" onClick={refresh}>
            {t('common.retry')}
          </button>
        </div>
      )}

      <div className="dashboard-cards" data-testid="compatibility-summary-strip">
        <div className="dashboard-card" data-testid="compatibility-supported-count">
          <div className="label">{t('compat.summarySupported')}</div>
          <div className="value">{counts?.supported || '0'}</div>
        </div>
        <div className="dashboard-card" data-testid="compatibility-experimental-count">
          <div className="label">{t('compat.summaryExperimental')}</div>
          <div className="value">{counts?.experimental || '0'}</div>
        </div>
        <div className="dashboard-card" data-testid="compatibility-unsupported-count">
          <div className="label">{t('compat.summaryUnsupported')}</div>
          <div className="value">{counts?.unsupported || '0'}</div>
        </div>
        <div className="dashboard-card" data-testid="compatibility-not-in-fleet-count">
          <div className="label">{t('compat.summaryNotInFleet')}</div>
          <div className="value">{counts?.notInFleet || '0'}</div>
        </div>
      </div>

      <div className="toolbar" style={{ marginBottom: 14 }} data-testid="compatibility-filters">
        <select
          data-testid="compatibility-model-filter"
          value={modelFilter}
          onChange={(e) => {
            setModelFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('common.allModels')}</option>
          {models.map((m) => (
            <option key={m.modelId} value={m.modelId}>
              {m.modelName}
            </option>
          ))}
        </select>
        <select
          data-testid="compatibility-engine-filter"
          value={engineFilter}
          onChange={(e) => {
            setEngineFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('common.allEngines')}</option>
          {engines.map((e) => (
            <option key={e.engine} value={e.engine}>
              {e.engine}
            </option>
          ))}
        </select>
        <select
          data-testid="compatibility-card-filter"
          value={cardFilter}
          onChange={(e) => {
            setCardFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('common.allCardTypes')}</option>
          {cardTypes.map((c) => (
            <option key={c.cardType} value={c.cardType}>
              {c.cardType}
            </option>
          ))}
        </select>
        <select
          data-testid="compatibility-status-filter"
          value={statusFilter}
          onChange={(e) => {
            setStatusFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('common.allStatuses')}</option>
          {STATUSES.map((s) => (
            <option key={s} value={s}>
              {statusLabel(s, t)}
            </option>
          ))}
        </select>
        <input
          data-testid="compatibility-search"
          placeholder={t('common.searchByName')}
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setOffset(0);
          }}
        />
        <div className="segmented" data-testid="compatibility-view-toggle">
          <button
            className={view === 'grid' ? 'active' : ''}
            data-testid="compatibility-view-grid"
            onClick={() => setView('grid')}
          >
            {t('compat.viewGrid')}
          </button>
          <button
            className={view === 'table' ? 'active' : ''}
            data-testid="compatibility-view-table"
            onClick={() => setView('table')}
          >
            {t('compat.viewTable')}
          </button>
        </div>
      </div>

      {empty && (
        <div className="empty-state" data-testid="compatibility-empty">
          {t('compat.empty')}
        </div>
      )}

      {!empty && view === 'grid' && (
        <div className="panel" data-testid="compatibility-grid">
          {gridModelId ? (
            <CompatibilityGrid
              cells={gridCells}
              cardTypes={cardTypes}
              onCellClick={(c) => setEditCell(c)}
            />
          ) : (
            <div className="muted">{t('compat.gridEmpty')}</div>
          )}
        </div>
      )}

      {!empty && view === 'table' && (
        <>
          <table className="data" data-testid="compatibility-table">
            <thead>
              <tr>
                <th />
                <th>{t('compat.colModel')}</th>
                <th>{t('compat.colEngine')}</th>
                <th>{t('compat.colCardType')}</th>
                <th>{t('compat.colStatus')}</th>
                <th>{t('compat.colNote')}</th>
                <th>{t('compat.colNotInFleet')}</th>
                <th>{t('compat.colLastUpdated')}</th>
              </tr>
            </thead>
            <tbody>
              {cells.map((c) => {
                const key = cellKey(c);
                return (
                  <tr key={key} data-testid={`compatibility-row-${c.modelId}-${c.engine}-${c.cardType}`}>
                    <td>
                      <input
                        type="checkbox"
                        data-testid={`compatibility-row-checkbox-${c.modelId}-${c.engine}-${c.cardType}`}
                        checked={selected.has(key)}
                        onChange={() => toggleSelect(key)}
                      />
                    </td>
                    <td>{c.modelName}</td>
                    <td>{c.engine}</td>
                    <td>
                      <span className="badge steady">{c.cardVendor}</span> {c.cardType}
                    </td>
                    <td>
                      <span className={statusBadgeClass(c.status)}>{statusLabel(c.status, t)}</span>
                    </td>
                    <td className="muted">{c.note || '—'}</td>
                    <td>{c.notInFleet ? <span className="badge failed">not in fleet</span> : '—'}</td>
                    <td className="muted">{formatTime(c.updatedAt)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        </>
      )}

      {editCell && (
        <EditCellDialog
          cell={editCell}
          onClose={() => setEditCell(null)}
          onSave={saveCell}
        />
      )}
      {bulkOpen && (
        <BulkEditDialog
          count={selectedCount}
          onClose={() => setBulkOpen(false)}
          onApply={applyBulk}
        />
      )}
    </div>
  );
}

function CompatibilityGrid({
  cells,
  cardTypes,
  onCellClick,
}: {
  cells: CompatibilityCell[];
  cardTypes: DimensionCardType[];
  onCellClick: (c: CompatibilityCell) => void;
}) {
  const { t } = useI18n();
  // Rows = engines, columns = card types.
  const engines = Array.from(new Set(cells.map((c) => c.engine))).sort();
  const byKey = new Map(cells.map((c) => [`${c.engine}\u0000${c.cardType}`, c]));
  return (
    <table className="data" data-testid="compatibility-grid-table">
      <thead>
        <tr>
          <th>{t('compat.colEngine')}</th>
          {cardTypes.map((ct) => (
            <th key={ct.cardType}>
              {ct.cardType}
              {!ct.inFleet && <span className="badge failed">not in fleet</span>}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {engines.map((engine) => (
          <tr key={engine}>
            <td>{engine}</td>
            {cardTypes.map((ct) => {
              const cell = byKey.get(`${engine}\u0000${ct.cardType}`);
              if (!cell) return <td key={ct.cardType}>—</td>;
              return (
                <td
                  key={ct.cardType}
                  data-testid={`compatibility-cell-${cell.modelId}-${cell.engine}-${cell.cardType}`}
                  style={{ cursor: 'pointer' }}
                  onClick={() => onCellClick(cell)}
                >
                  <span className={statusBadgeClass(cell.status)}>{statusLabel(cell.status, t)}</span>
                  {cell.notInFleet && (
                    <span
                      className="badge failed"
                      data-testid={`compatibility-cell-not-in-fleet-${cell.modelId}-${cell.engine}-${cell.cardType}`}
                    >
                      not in fleet
                    </span>
                  )}
                </td>
              );
            })}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function EditCellDialog({
  cell,
  onClose,
  onSave,
}: {
  cell: CompatibilityCell;
  onClose: () => void;
  onSave: (status: string, note: string) => Promise<void>;
}) {
  const { t } = useI18n();
  const [status, setStatus] = useState(cell.status);
  const [note, setNote] = useState(cell.note || '');
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');

  const submit = async () => {
    setSaving(true);
    setErr('');
    try {
      await onSave(status, note);
    } catch (e) {
      setErr(e instanceof Error ? e.message : t('compat.saveFailed'));
      setSaving(false);
    }
  };

  return (
    <Dialog title={t('compat.editTitle')} onClose={onClose} testId="compatibility-edit-dialog">
      <div className="detail-grid">
        <div className="detail-item">
          <div className="label">{t('compat.fieldModel')}</div>
          <div className="value">{cell.modelName}</div>
        </div>
        <div className="detail-item">
          <div className="label">{t('compat.fieldEngine')}</div>
          <div className="value">{cell.engine}</div>
        </div>
        <div className="detail-item">
          <div className="label">{t('compat.fieldCardType')}</div>
          <div className="value">
            <span className="badge steady">{cell.cardVendor}</span> {cell.cardType}
          </div>
        </div>
      </div>
      <div className="form-field" data-testid="compatibility-edit-status">
        <label>{t('compat.fieldStatus')}</label>
        {STATUSES.map((s) => (
          <label key={s} className="radio">
            <input type="radio" name="status" value={s} checked={status === s} onChange={() => setStatus(s)} />
            {statusLabel(s, t)}
          </label>
        ))}
      </div>
      <div className="form-field">
        <label>{t('compat.fieldNote', { n: note.length })}</label>
        <textarea
          data-testid="compatibility-edit-note"
          value={note}
          maxLength={512}
          onChange={(e) => setNote(e.target.value)}
        />
      </div>
      {err && <ErrorBanner message={err} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button className="primary" data-testid="compatibility-edit-save" disabled={saving} onClick={() => void submit()}>
          {saving ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}

function BulkEditDialog({
  count,
  onClose,
  onApply,
}: {
  count: number;
  onClose: () => void;
  onApply: (status: string, note: string) => Promise<void>;
}) {
  const { t } = useI18n();
  const [status, setStatus] = useState('supported');
  const [note, setNote] = useState('');
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState('');

  const submit = async () => {
    setSaving(true);
    setErr('');
    try {
      await onApply(status, note);
    } catch (e) {
      setErr(e instanceof Error ? e.message : t('compat.applyFailed'));
      setSaving(false);
    }
  };

  return (
    <Dialog title={t('compat.bulkTitle')} onClose={onClose} testId="compatibility-bulk-dialog">
      <p className="muted">{t('compat.bulkNote', { count })}</p>
      <div className="form-field" data-testid="compatibility-bulk-status">
        <label>{t('compat.fieldStatus')}</label>
        {STATUSES.map((s) => (
          <label key={s} className="radio">
            <input type="radio" name="bulk-status" value={s} checked={status === s} onChange={() => setStatus(s)} />
            {statusLabel(s, t)}
          </label>
        ))}
      </div>
      <div className="form-field">
        <label>{t('compat.fieldNote', { n: note.length })}</label>
        <textarea
          data-testid="compatibility-bulk-note"
          value={note}
          maxLength={512}
          onChange={(e) => setNote(e.target.value)}
        />
      </div>
      {err && <ErrorBanner message={err} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button className="primary" data-testid="compatibility-bulk-apply" disabled={saving} onClick={() => void submit()}>
          {saving ? t('common.applying') : t('common.apply')}
        </button>
      </div>
    </Dialog>
  );
}
