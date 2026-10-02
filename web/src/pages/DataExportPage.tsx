// Data Export page (end-user): GDPR-style data export of usage, billing,
// request logs, and account data as JSON/CSV.
// Implements docs/design/data-export-privacy.md FR1-FR3 and
// docs/architecture/data-export-privacy.md §6.5.

import { useCallback, useEffect, useState } from 'react';
import { api, type DataExport } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { ErrorBanner, StateBadge, usePolling } from '../components';

const EXPORT_TYPES = ['usage', 'billing', 'request_logs', 'account'];
const RANGE_PRESETS = [
  { id: '24h', labelKey: 'usage.range24h', hours: 24 },
  { id: '7d', labelKey: 'usage.range7d', hours: 7 * 24 },
  { id: '30d', labelKey: 'usage.range30d', hours: 30 * 24 },
  { id: '90d', labelKey: 'usage.range90d', hours: 90 * 24 },
];

export default function DataExportPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [exports, setExports] = useState<DataExport[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  // Builder state.
  const [builderOpen, setBuilderOpen] = useState(false);
  const [exportType, setExportType] = useState('usage');
  const [preset, setPreset] = useState('30d');
  const [format, setFormat] = useState('json');

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.get<{ exports?: DataExport[] }>('/api/v1/account/export', orgId);
      setExports(data.exports || []);
      setError('');
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('dataexport.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  }, [orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // Poll while any export is pending.
  const hasPending = exports.some((e) => e.status === 'pending');
  usePolling(() => void load(), 3000, hasPending);

  const requestExport = async () => {
    try {
      const now = Math.floor(Date.now() / 1000);
      const hours = RANGE_PRESETS.find((p) => p.id === preset)?.hours || 30 * 24;
      const body: Record<string, unknown> = { type: exportType, format };
      if (exportType !== 'account') {
        body.since = now - hours * 3600;
        body.until = now;
      }
      await api.post<{ export: DataExport }>('/api/v1/account/export', orgId, body);
      setBuilderOpen(false);
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('dataexport.requestFailed'));
    }
  };

  const download = async (exportId: string) => {
    try {
      const data = await api.get<{ file: string; filename: string; contentType: string }>(
        `/api/v1/account/export/${exportId}/download`,
        orgId,
      );
      const blob = new Blob([data.file], { type: data.contentType });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = data.filename;
      a.click();
      URL.revokeObjectURL(url);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('dataexport.downloadFailed'));
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1 data-testid="data-export-title">{t('dataexport.title')}</h1>
          <div className="subtitle">{t('dataexport.subtitle')}</div>
        </div>
        <button className="secondary" data-testid="export-refresh" onClick={() => void load()} disabled={loading}>
          {t('dataexport.refresh')}
        </button>
        <button className="primary" data-testid="export-new" onClick={() => setBuilderOpen(true)}>
          {t('dataexport.newExport')}
        </button>
      </div>

      {error && (
        <div>
          <ErrorBanner message={error} />
          {stale && <div className="muted">{t('dataexport.stale')}</div>}
          <button className="secondary" data-testid="export-retry" onClick={() => void load()}>
            {t('dataexport.retry')}
          </button>
        </div>
      )}

      {!loading && !error && exports.length === 0 && (
        <div className="panel" data-testid="export-empty">
          <p>{t('dataexport.empty')}</p>
          <p className="muted">{t('dataexport.emptyHint')}</p>
        </div>
      )}

      {!loading && !error && exports.length > 0 && (
        <div className="panel" data-testid="export-history">
          <table className="table">
            <thead>
              <tr>
                <th>{t('dataexport.type')}</th>
                <th>{t('dataexport.range')}</th>
                <th>{t('dataexport.format')}</th>
                <th>{t('dataexport.status')}</th>
                <th>{t('dataexport.rows')}</th>
                <th>{t('dataexport.created')}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {exports.map((e) => (
                <tr key={e.exportId} data-testid={`export-row-${e.exportId}`}>
                  <td>{t(`dataexport.type${e.type[0].toUpperCase()}${e.type.slice(1)}`)}</td>
                  <td>
                    {e.type === 'account'
                      ? '—'
                      : `${new Date(parseInt(e.since, 10) * 1000).toLocaleDateString()} – ${new Date(parseInt(e.until, 10) * 1000).toLocaleDateString()}`}
                  </td>
                  <td>{e.format}</td>
                  <td>
                    <StateBadge state={e.status} />
                  </td>
                  <td>{e.rowCount}</td>
                  <td>{new Date(parseInt(e.createdAt, 10) * 1000).toLocaleString()}</td>
                  <td>
                    <button
                      className="secondary"
                      data-testid={`export-download-${e.exportId}`}
                      disabled={e.status !== 'ready'}
                      onClick={() => void download(e.exportId)}
                    >
                      {t('dataexport.download')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {builderOpen && (
        <div className="dialog-backdrop">
          <div className="dialog" data-testid="export-builder">
            <h3>{t('dataexport.newExport')}</h3>
            <div className="field-group">
              <div className="label">{t('dataexport.type')}</div>
              {EXPORT_TYPES.map((type) => (
                <label key={type} className="radio">
                  <input
                    type="radio"
                    name="export-type"
                    data-testid={`export-type-${type}`}
                    checked={exportType === type}
                    onChange={() => setExportType(type)}
                  />
                  {t(`dataexport.type${type[0].toUpperCase()}${type.slice(1)}`)}
                </label>
              ))}
            </div>
            {exportType !== 'account' && (
              <label>
                {t('dataexport.range')}
                <select data-testid="export-range" value={preset} onChange={(e) => setPreset(e.target.value)}>
                  {RANGE_PRESETS.map((p) => (
                    <option key={p.id} value={p.id}>
                      {t(p.labelKey)}
                    </option>
                  ))}
                </select>
              </label>
            )}
            <div className="field-group">
              <div className="label">{t('dataexport.format')}</div>
              <label className="radio">
                <input
                  type="radio"
                  name="export-format"
                  data-testid="export-format-json"
                  checked={format === 'json'}
                  onChange={() => setFormat('json')}
                />
                JSON
              </label>
              <label className="radio">
                <input
                  type="radio"
                  name="export-format"
                  data-testid="export-format-csv"
                  checked={format === 'csv'}
                  disabled={exportType === 'account'}
                  onChange={() => setFormat('csv')}
                />
                CSV
              </label>
            </div>
            <div className="dialog-actions">
              <button className="secondary" onClick={() => setBuilderOpen(false)}>
                {t('common.cancel')}
              </button>
              <button className="primary" data-testid="export-request" onClick={() => void requestExport()}>
                {t('dataexport.request')}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}