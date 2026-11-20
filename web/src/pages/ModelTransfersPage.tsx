// Model transfers page: asynchronous copy/download/delete jobs the
// controller runs for model weights.

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
  onClose,
  onDone,
}: {
  orgId: string;
  models: ModelSummary[];
  backends: StorageBackend[];
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [modelId, setModelId] = useState(models[0]?.modelId || '');
  const [operation, setOperation] = useState('copy');
  const [sourceBackend, setSourceBackend] = useState('');
  const [targetBackend, setTargetBackend] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    setBusy(true);
    setError('');
    try {
      await api.post('/api/v1/admin/model-transfers', orgId, {
        modelId,
        operation,
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
          <select id="transfer-model" value={modelId} onChange={(e) => setModelId(e.target.value)}>
            {models.map((model) => (
              <option key={model.modelId} value={model.modelId}>{model.name}</option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="transfer-operation">{t('transfers.colOperation')}</label>
          <select id="transfer-operation" value={operation} onChange={(e) => setOperation(e.target.value)}>
            {['copy', 'download', 'delete'].map((value) => (
              <option key={value} value={value}>{t(`transfers.operation.${value}`)}</option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="transfer-source">{t('transfers.sourceBackend')}</label>
          <select id="transfer-source" value={sourceBackend} onChange={(e) => setSourceBackend(e.target.value)}>
            <option value="">{t('transfers.defaultBackend')}</option>
            {backends.map((backend) => (
              <option key={backend.name} value={backend.name}>{backend.name}</option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="transfer-target">{t('transfers.targetBackend')}</label>
          <select id="transfer-target" value={targetBackend} onChange={(e) => setTargetBackend(e.target.value)}>
            <option value="">{t('transfers.defaultBackend')}</option>
            {backends.map((backend) => (
              <option key={backend.name} value={backend.name}>{backend.name}</option>
            ))}
          </select>
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button disabled={busy || !modelId} onClick={() => void submit()}>
          {busy ? t('common.creating') : t('transfers.start')}
        </button>
      </div>
    </Dialog>
  );
}
