// Model transfers page: asynchronous move jobs the controller runs for model
// weights. Historical copy/download/delete rows remain visible.

import { useCallback, useEffect, useMemo, useState } from 'react';
import { api, ApiError, formatTime, type ModelSummary, type ModelTransfer, type PageMeta, type StorageBackend } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination, RowActionsMenu, StatusError, Toast } from '../components';
import { formatBytes } from '../components/ModelDownloadsPanel';

interface ListResponse {
  response: { code: number; message: string };
  transfers: ModelTransfer[];
  pageMeta?: PageMeta;
}
interface ModelsResponse {
  models: ModelSummary[];
}
interface BackendsResponse {
  backends: StorageBackend[];
  defaultBackend?: string;
}

const PAGE_SIZE = 20;
const ACTIVE_STATES = ['queued', 'running'];

export default function ModelTransfersPage({ embedded = false }: { embedded?: boolean }) {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [transfers, setTransfers] = useState<ModelTransfer[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [state, setState] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [startOpen, setStartOpen] = useState(false);
  const [models, setModels] = useState<ModelSummary[]>([]);
  const [backends, setBackends] = useState<StorageBackend[]>([]);
  const [defaultBackend, setDefaultBackend] = useState('');

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true);
      try {
        const suffix = state ? `&state=${encodeURIComponent(state)}` : '';
        const data = await api.get<ListResponse>(
          `/api/v1/admin/model-transfers?page.offset=${offset}&page.limit=${PAGE_SIZE}${suffix}`,
          orgId,
        );
        setTransfers(data.transfers || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
        setError('');
      } catch (e) {
        setError(e instanceof Error ? e.message : t('transfers.loadFailed'));
      } finally {
        if (!quiet) setLoading(false);
      }
    },
    [orgId, offset, state, t],
  );

  useEffect(() => {
    void load();
  }, [load]);

  const hasActive = useMemo(
    () => transfers.some((transfer) => ACTIVE_STATES.includes(transfer.state)),
    [transfers],
  );
  useEffect(() => {
    if (!hasActive) return undefined;
    const timer = window.setInterval(() => void load(true), 2000);
    return () => window.clearInterval(timer);
  }, [hasActive, load]);

  useEffect(() => {
    if (!startOpen) return;
    void Promise.all([
      api.get<ModelsResponse>(`/api/v1/admin/models?page.offset=0&page.limit=200`, orgId),
      api.get<BackendsResponse>('/api/v1/admin/storage-backends', orgId),
    ])
      .then(([modelData, backendData]) => {
        setModels(modelData.models || []);
        setBackends(backendData.backends || []);
        setDefaultBackend(backendData.defaultBackend || '');
      })
      .catch((e) => setError(e instanceof Error ? e.message : t('transfers.loadFailed')));
  }, [startOpen, orgId, t]);

  const cancel = async (transfer: ModelTransfer) => {
    try {
      await api.post(`/api/v1/admin/model-transfers/${transfer.transferId}:cancel`, orgId, {});
      setNotice(t('transfers.cancelled'));
      await load(true);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('transfers.cancelFailed'));
    }
  };

  return (
    <div>
      {!embedded && (
        <div className="page-header">
          <div>
            <h1>{t('transfers.title')}</h1>
            <div className="subtitle">{t('transfers.subtitle')}</div>
          </div>
          <div className="toolbar">
            <button className="secondary" onClick={() => void load()}>{t('common.refresh')}</button>
            <button data-testid="start-transfer" onClick={() => setStartOpen(true)}>{t('transfers.start')}</button>
          </div>
        </div>
      )}
      {error && <ErrorBanner message={error} />}
      <Toast message={notice} onDismiss={() => setNotice('')} testId="transfers-toast" />
      <div className="toolbar" style={{ marginBottom: 14 }}>
        <select value={state} onChange={(e) => { setState(e.target.value); setOffset(0); }}>
          <option value="">{t('transfers.allStates')}</option>
          {['queued', 'running', 'succeeded', 'failed', 'cancelled'].map((value) => (
            <option key={value} value={value}>{t(`transfers.state.${value}`)}</option>
          ))}
        </select>
        {embedded && (
          <>
            <span className="toolbar-spacer" />
            <button className="secondary" onClick={() => void load()}>{t('common.refresh')}</button>
            <button data-testid="start-transfer" onClick={() => setStartOpen(true)}>{t('transfers.start')}</button>
          </>
        )}
      </div>
      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : transfers.length === 0 ? (
          <div className="empty-state" data-testid="transfers-empty">{t('transfers.empty')}</div>
        ) : (
          <table className="data" data-testid="transfers-table">
            <thead>
              <tr>
                <th>{t('transfers.colModel')}</th>
                <th>{t('transfers.colOperation')}</th>
                <th>{t('transfers.colRoute')}</th>
                <th>{t('transfers.colProgress')}</th>
                <th>{t('common.status')}</th>
                <th>{t('common.updated')}</th>
                <th className="col-actions">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {transfers.map((transfer) => (
                <tr key={transfer.transferId} data-testid={`transfer-row-${transfer.transferId}`}>
                  <td>
                    <strong>{transfer.modelName || '—'}</strong>
                    <div className="muted mono">{transfer.version}</div>
                  </td>
                  <td>{t(`transfers.operation.${transfer.operation}`)}</td>
                  <td className="mono muted">
                    {transfer.sourceBackend} → {transfer.targetBackend || '—'}
                  </td>
                  <td className="mono">
                    {formatBytes(transfer.bytes)}
                    {Number(transfer.totalBytes || 0) > 0 ? ` / ${formatBytes(transfer.totalBytes || 0)}` : ''}
                  </td>
                  <td>
                    <span className={`state state-${transfer.state}`}>{t(`transfers.state.${transfer.state}`)}</span>
                    <StatusError message={transfer.message} testId={`transfer-error-${transfer.transferId}`} />
                  </td>
                  <td>{formatTime(transfer.updatedAt)}</td>
                  <td>
                    {!['succeeded', 'failed', 'cancelled'].includes(transfer.state) && (
                      <RowActionsMenu
                        label={t('common.actionsFor', { name: transfer.transferId })}
                        testId={`transfer-actions-${transfer.transferId}`}
                        actions={[{
                          key: 'cancel',
                          label: t('common.cancel'),
                          danger: true,
                          onClick: () => void cancel(transfer),
                        }]}
                      />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {total > PAGE_SIZE && (
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        )}
      </div>
      {startOpen && (
        <StartTransferDialog
          orgId={orgId}
          models={models}
          backends={backends}
          defaultBackend={defaultBackend}
          onClose={() => setStartOpen(false)}
          onDone={() => {
            setStartOpen(false);
            setNotice(t('transfers.started'));
            setOffset(0);
            void load();
          }}
        />
      )}
    </div>
  );
}

function StartTransferDialog({
  orgId,
  models,
  backends,
  defaultBackend,
  onClose,
  onDone,
}: {
  orgId: string;
  models: ModelSummary[];
  backends: StorageBackend[];
  defaultBackend: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const fallbackModelId = models[0]?.modelId || '';
  const [modelId, setModelId] = useState(fallbackModelId);
  const [targetBackend, setTargetBackend] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const selectedModelId = modelId || fallbackModelId;
  const selectedModel = models.find((model) => model.modelId === selectedModelId);
  // A move always starts from the backend that currently holds the model.
  // The model summary projects an empty version backend to the platform
  // default, so this value is displayable and matches the server's choice.
  const sourceBackend = selectedModel?.storageBackend || defaultBackend || '';
  const otherBackends = useMemo(
    () => backends.filter((backend) => backend.name !== sourceBackend),
    [backends, sourceBackend],
  );
  const disableReason = models.length === 0
    ? t('transfers.noModels')
    : !sourceBackend
      ? t('transfers.noSourceBackend')
      : otherBackends.length === 0
        ? t('transfers.noTargetBackend')
        : '';

  // The dialog mounts before the model and backend lists finish loading, so
  // seed the selection once they arrive instead of freezing the initial
  // empty value.
  useEffect(() => {
    if (!modelId && fallbackModelId) setModelId(fallbackModelId);
  }, [fallbackModelId, modelId]);

  // Only another backend is a valid move target. Keep a manually chosen
  // target when it remains valid, and otherwise fall back to the first one.
  useEffect(() => {
    if (otherBackends.some((backend) => backend.name === targetBackend)) return;
    setTargetBackend(otherBackends[0]?.name || '');
  }, [otherBackends, targetBackend]);

  const submit = async () => {
    if (!selectedModelId || !sourceBackend || !targetBackend) return;
    setBusy(true);
    setError('');
    try {
      await api.post('/api/v1/admin/model-transfers', orgId, {
        modelId: selectedModelId,
        operation: 'move',
        sourceBackend,
        targetBackend,
      });
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('transfers.startFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog title={t('transfers.start')} onClose={onClose} testId="start-transfer-dialog">
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="transfer-model">{t('transfers.colModel')}</label>
          <select id="transfer-model" value={selectedModelId} onChange={(e) => setModelId(e.target.value)}>
            {models.map((model) => (
              <option key={model.modelId} value={model.modelId}>{model.name}</option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="transfer-operation">{t('transfers.colOperation')}</label>
          <select id="transfer-operation" value="move" disabled>
            <option value="move">{t('transfers.operation.move')}</option>
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="transfer-source">{t('transfers.sourceBackend')}</label>
          <select id="transfer-source" value={sourceBackend} disabled>
            {sourceBackend ? (
              <option value={sourceBackend}>{sourceBackend}</option>
            ) : (
              <option value="">{t('transfers.noSourceBackend')}</option>
            )}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="transfer-target">{t('transfers.targetBackend')}</label>
          <select
            id="transfer-target"
            value={targetBackend}
            onChange={(e) => setTargetBackend(e.target.value)}
            disabled={otherBackends.length === 0}
          >
            {otherBackends.map((backend) => (
              <option key={backend.name} value={backend.name}>{backend.name}</option>
            ))}
            {otherBackends.length === 0 && (
              <option value="">{t('transfers.noTargetBackend')}</option>
            )}
          </select>
        </div>
      </div>
      {disableReason && <div className="muted" data-testid="transfer-disabled-reason">{disableReason}</div>}
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button disabled={busy || !selectedModelId || !sourceBackend || !targetBackend} onClick={() => void submit()}>
          {busy ? t('common.creating') : t('transfers.start')}
        </button>
      </div>
    </Dialog>
  );
}
