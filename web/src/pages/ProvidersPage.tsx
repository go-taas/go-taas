// Admin Model Providers page (feature #48): the external model-provider
// registry with create/edit/delete, the five-state connectivity test and
// the link to the provider detail page. Admin-surface only — it calls ONLY
// /api/v1/admin/providers/*. Implements docs/design/external-model-providers.md
// §5.3.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime, type PageMeta } from '../api';
import { Dialog, ErrorBanner, Pagination, usePolling } from '../components';
import { navigate } from '../router';
import { useI18n } from '../i18n';

interface ResponseEnvelope {
  code: number;
  message: string;
}

export interface ProbeResult {
  status: string;
  latencyMs: string;
  httpStatus: number;
  message: string;
  checkedAt: string;
}

export interface Provider {
  providerId: string;
  name: string;
  type: string;
  baseUrl: string;
  enabled: boolean;
  requestTimeoutSeconds: string;
  extraHeaderNames?: string[];
  apiKeyHint: string;
  lastProbe?: ProbeResult;
  upstreamModelCount: string;
  publishedModelCount: string;
  createdAt: string;
  updatedAt: string;
}

interface ProviderListResponse {
  response: ResponseEnvelope;
  providers?: Provider[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;
const POLL_MS = 30_000;
const PROVIDER_TYPES = ['deepseek', 'openai', 'openai_compatible'];
const TYPE_LABELS: Record<string, string> = {
  deepseek: 'DeepSeek',
  openai: 'OpenAI',
  openai_compatible: 'OpenAI-compatible',
};
const PROBE_STATUSES = ['unknown', 'ok', 'unauthorized', 'unreachable', 'error'];
const PROBE_LABEL_KEYS: Record<string, string> = {
  unknown: 'provider.status.unknown',
  ok: 'provider.status.ok',
  unauthorized: 'provider.status.unauthorized',
  unreachable: 'provider.status.unreachable',
  error: 'provider.status.error',
};
const NAME_PATTERN = /^[a-z0-9][a-z0-9-]*$/;
const HEADER_NAME_PATTERN = /^[A-Za-z0-9-]+$/;

// probeBadgeClass maps a probe status to a stable badge style.
export function probeBadgeClass(status: string): string {
  switch (status) {
    case 'ok':
      return 'badge running';
    case 'unauthorized':
      return 'badge pending';
    case 'unreachable':
    case 'error':
      return 'badge failed';
    default:
      return 'badge terminated';
  }
}

// probeLabel returns the human label of a probe status.
// probeLabelKey returns the i18n key of a probe status label.
export function probeLabelKey(status: string): string {
  return PROBE_LABEL_KEYS[status] || PROBE_LABEL_KEYS.unknown;
}

// isNonHTTPS reports whether the base URL uses a plain-HTTP scheme.
export function isNonHTTPS(baseUrl: string): boolean {
  return baseUrl.startsWith('http://');
}

interface HeaderRow {
  name: string;
  value: string;
}

const emptyForm = {
  name: '',
  type: 'openai_compatible',
  baseUrl: '',
  apiKey: '',
  enabled: false,
  timeout: '10',
};

export default function ProvidersPage() {
  const { t } = useI18n();
  const api = useApi();
  const { orgId } = useOrg();

  const [toast, setToast] = useState('');
  const [actionError, setActionError] = useState('');
  const [rows, setRows] = useState<Provider[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [search, setSearch] = useState('');
  const [type, setType] = useState('');
  const [probeStatus, setProbeStatus] = useState('');
  const [enabled, setEnabled] = useState('');

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);
  const [permissionDenied, setPermissionDenied] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [busyId, setBusyId] = useState('');

  // Create/edit dialog state.
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Provider | null>(null);
  const [form, setForm] = useState({ ...emptyForm });
  const [headers, setHeaders] = useState<HeaderRow[]>([]);
  const [formError, setFormError] = useState('');

  // Delete confirmation state.
  const [deleteTarget, setDeleteTarget] = useState<Provider | null>(null);

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
        if (search) params.set('name', search);
        if (type) params.set('type', type);
        if (probeStatus) params.set('probeStatus', probeStatus);
        if (enabled) params.set('enabled', enabled);
        const data = await api.get<ProviderListResponse>(
          `/api/v1/admin/providers?${params.toString()}`,
          orgId,
        );
        setRows(data.providers || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
        setStale(false);
        setPermissionDenied(false);
      } catch (e) {
        const code = (e as { code?: number } | undefined)?.code;
        if (code === 10036) {
          setPermissionDenied(true);
          setLoading(false);
          setRefreshing(false);
          return;
        }
        setError(e instanceof Error ? e.message : 'Could not load providers.');
        if (rows.length > 0) setStale(true);
      } finally {
        setLoading(false);
        setRefreshing(false);
      }
    },
    [api, orgId, offset, search, type, probeStatus, enabled, rows.length],
  );

  useEffect(() => {
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [offset, search, type, probeStatus, enabled]);

  usePolling(() => void load(false), POLL_MS, !loading);
  const refresh = () => void load(false);

  const openCreate = () => {
    setEditing(null);
    setForm({ ...emptyForm });
    setHeaders([]);
    setFormError('');
    setDialogOpen(true);
  };

  const openEdit = (p: Provider) => {
    setEditing(p);
    setForm({
      name: p.name,
      type: p.type,
      baseUrl: p.baseUrl,
      apiKey: '',
      enabled: p.enabled,
      timeout: p.requestTimeoutSeconds || '10',
    });
    setHeaders((p.extraHeaderNames || []).map((name) => ({ name, value: '' })));
    setFormError('');
    setDialogOpen(true);
  };

  const validateForm = (): string => {
    const name = form.name.trim();
    if (name === '' || name.length > 64 || !NAME_PATTERN.test(name)) {
      return 'Enter a lowercase name (letters, digits, -).';
    }
    if (!PROVIDER_TYPES.includes(form.type)) return 'Select a supported provider type.';
    if (form.baseUrl.trim() === '' || !/^https?:\/\/[^/]+/.test(form.baseUrl.trim())) {
      return 'Enter a valid absolute URL (https://…).';
    }
    if (!editing && form.apiKey.trim() === '') return 'Enter the provider API key.';
    const timeout = Number(form.timeout);
    if (!Number.isInteger(timeout) || timeout < 1 || timeout > 120) {
      return 'Enter a timeout between 1 and 120 seconds.';
    }
    for (const row of headers) {
      if (row.name.trim() === '' || !HEADER_NAME_PATTERN.test(row.name.trim())) {
        return 'Header names must be letters, digits and -.';
      }
    }
    return '';
  };

  const submitForm = async () => {
    const validationError = validateForm();
    if (validationError) {
      setFormError(validationError);
      return;
    }
    const extraHeaders: Record<string, string> = {};
    for (const row of headers) {
      if (row.value) extraHeaders[row.name.trim()] = row.value;
    }
    const body = {
      name: form.name.trim(),
      type: form.type,
      baseUrl: form.baseUrl.trim(),
      enabled: form.enabled,
      requestTimeoutSeconds: Number(form.timeout),
      extraHeaders,
      ...(form.apiKey.trim() ? { apiKey: form.apiKey.trim() } : {}),
    };
    try {
      if (editing) {
        await api.patch(`/api/v1/admin/providers/${encodeURIComponent(editing.providerId)}`, orgId, body);
        setToast('Provider updated.');
      } else {
        await api.post('/api/v1/admin/providers', orgId, body);
        setToast('Provider created.');
      }
      setActionError('');
      setDialogOpen(false);
      await load(false);
    } catch (e) {
      const code = (e as { code?: number } | undefined)?.code;
      if (code === 13002) setFormError('A provider with this name already exists.');
      else if (code === 13003) setFormError('Enter a lowercase name (letters, digits, -).');
      else if (code === 13004) setFormError('Enter a valid absolute URL (https://…).');
      else if (code === 13005) setFormError('Select a supported provider type.');
      else if (code === 13006) setFormError('Enter the provider API key.');
      else if (code === 13012) setFormError('Credentials were rejected — fix or rotate the key, then test again.');
      else setFormError(e instanceof Error ? e.message : 'Could not save the provider.');
    }
  };

  const toggleEnabled = async (p: Provider) => {
    setBusyId(p.providerId);
    setActionError('');
    try {
      await api.patch(`/api/v1/admin/providers/${encodeURIComponent(p.providerId)}`, orgId, {
        name: p.name,
        type: p.type,
        baseUrl: p.baseUrl,
        enabled: !p.enabled,
        requestTimeoutSeconds: Number(p.requestTimeoutSeconds) || 10,
      });
      await load(false);
    } catch (e) {
      const code = (e as { code?: number } | undefined)?.code;
      if (code === 13012) {
        setActionError('Credentials were rejected — fix or rotate the API key, then test again.');
      } else {
        setActionError(e instanceof Error ? e.message : 'Could not update the provider.');
      }
    } finally {
      setBusyId('');
    }
  };

  const runTest = async (p: Provider) => {
    setBusyId(p.providerId);
    setActionError('');
    try {
      const data = await api.post<{ probe?: ProbeResult }>(
        `/api/v1/admin/providers/${encodeURIComponent(p.providerId)}:test`,
        orgId,
        {},
      );
      const probe = data.probe;
      setRows((prev) =>
        prev.map((row) => (row.providerId === p.providerId ? { ...row, lastProbe: probe } : row)),
      );
      setToast(
        probe
          ? `${t(probeLabelKey(probe.status))} · ${probe.latencyMs} ms · ${probe.httpStatus ? `HTTP ${probe.httpStatus}` : t('providers.noResponse')} · ${t('providers.justNow')}`
          : t('providers.testComplete'),
      );
    } catch (e) {
      setActionError(e instanceof Error ? e.message : t('providers.testFailed'));
    } finally {
      setBusyId('');
    }
  };

  const confirmDelete = async () => {
    if (!deleteTarget) return;
    try {
      await api.del(`/api/v1/admin/providers/${encodeURIComponent(deleteTarget.providerId)}`, orgId);
      setToast('Provider deleted.');
      setActionError('');
      setDeleteTarget(null);
      await load(false);
    } catch (e) {
      setActionError(e instanceof Error ? e.message : 'Could not delete the provider.');
      setDeleteTarget(null);
    }
  };

  if (permissionDenied) {
    return (
      <div data-testid="provider-permission-denied">
        <ErrorBanner message="You do not have permission to manage providers." />
        <a href="/admin" className="link" data-testid="provider-back-admin">
          Back to admin
        </a>
      </div>
    );
  }

  return (
    <div data-testid="provider-list">
      <div className="page-header">
        <div>
          <h1>{t('providers.title')}</h1>
          <div className="subtitle">{t('providers.subtitle')}</div>
        </div>
        <div className="toolbar">
          <button className="primary" data-testid="provider-add" onClick={openCreate}>
            Add provider
          </button>
          <button className="secondary" data-testid="provider-refresh" disabled={refreshing} onClick={refresh}>
            {refreshing ? 'Refreshing…' : 'Refresh'}
          </button>
        </div>
      </div>

      {error && (
        <div data-testid="provider-error">
          <ErrorBanner message={error} />
        </div>
      )}
      {actionError && (
        <div data-testid="provider-action-error">
          <ErrorBanner message={actionError} />
        </div>
      )}
      {toast && (
        <div className="success-banner" data-testid="provider-toast">
          {toast}
        </div>
      )}
      {stale && (
        <div className="warning-banner" data-testid="provider-stale-banner">
          Showing stale data.{' '}
          <button className="link" data-testid="provider-stale-retry" onClick={refresh}>
            Retry
          </button>
        </div>
      )}

      <div className="panel">
        <div className="toolbar">
          <input
            data-testid="provider-search"
            placeholder="Search by name"
            value={search}
            onChange={(e) => {
              setOffset(0);
              setSearch(e.target.value);
            }}
          />
          <select
            data-testid="provider-filter-type"
            value={type}
            onChange={(e) => {
              setOffset(0);
              setType(e.target.value);
            }}
          >
            <option value="">{t('providers.allTypes')}</option>
            {PROVIDER_TYPES.map((v) => (
              <option key={v} value={v}>
                {TYPE_LABELS[v]}
              </option>
            ))}
          </select>
          <select
            data-testid="provider-filter-status"
            value={probeStatus}
            onChange={(e) => {
              setOffset(0);
              setProbeStatus(e.target.value);
            }}
          >
            <option value="">{t('providers.allStatuses')}</option>
            {PROBE_STATUSES.map((v) => (
              <option key={v} value={v}>
                {t(PROBE_LABEL_KEYS[v])}
              </option>
            ))}
          </select>
          <select
            data-testid="provider-filter-enabled"
            value={enabled}
            onChange={(e) => {
              setOffset(0);
              setEnabled(e.target.value);
            }}
          >
            <option value="">{t('common.all')}</option>
            <option value="true">{t('common.enabled')}</option>
            <option value="false">{t('common.disabled')}</option>
          </select>
        </div>

        {loading ? (
          <div className="skeleton" data-testid="provider-loading">
            {t('common.loading')}
          </div>
        ) : rows.length === 0 ? (
          <div className="empty" data-testid="provider-empty">
            <p>{t('providers.empty')}</p>
            <button className="primary" data-testid="provider-empty-add" onClick={openCreate}>
              {t('providers.add')}
            </button>
          </div>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>{t('providers.colName')}</th>
                <th>{t('providers.colType')}</th>
                <th>{t('providers.colBaseUrl')}</th>
                <th>{t('providers.colStatus')}</th>
                <th>{t('providers.colEnabled')}</th>
                <th>{t('providers.colModels')}</th>
                <th>{t('providers.colLastChecked')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((p) => {
                const status = p.lastProbe?.status || 'unknown';
                return (
                  <tr key={p.providerId} data-testid={`provider-row-${p.providerId}`}>
                    <td>{p.name}</td>
                    <td>
                      <span className="badge steady">{TYPE_LABELS[p.type] || p.type}</span>
                    </td>
                    <td>
                      <span className="mono" title={p.baseUrl}>
                        {p.baseUrl.length > 40 ? `${p.baseUrl.slice(0, 40)}…` : p.baseUrl}
                      </span>
                      {isNonHTTPS(p.baseUrl) && (
                        <span className="badge failed" data-testid={`provider-nonhttps-chip-${p.providerId}`}>
                          {t('providers.nonHttps')}
                        </span>
                      )}
                    </td>
                    <td>
                      <span className={probeBadgeClass(status)} data-testid={`provider-status-${p.providerId}`}>
                        {t(probeLabelKey(status))}
                      </span>
                    </td>
                    <td>
                      <button
                        className="link"
                        data-testid={`provider-enabled-${p.providerId}`}
                        disabled={busyId === p.providerId}
                        onClick={() => void toggleEnabled(p)}
                      >
                        {p.enabled ? t('common.enabled') : t('common.disabled')}
                      </button>
                    </td>
                    <td>
                      {p.publishedModelCount} / {p.upstreamModelCount}
                    </td>
                    <td>
                      {p.lastProbe?.checkedAt ? formatTime(p.lastProbe.checkedAt) : '—'}
                    </td>
                    <td className="actions">
                      <button
                        className="link"
                        data-testid={`provider-view-${p.providerId}`}
                        onClick={() => navigate(`/admin/providers/${encodeURIComponent(p.providerId)}`)}
                      >
                        {t('common.view')}
                      </button>
                      <button
                        className="link"
                        data-testid={`provider-test-${p.providerId}`}
                        disabled={busyId === p.providerId}
                        onClick={() => void runTest(p)}
                      >
                        {t('providers.test')}
                      </button>
                      <button className="link" data-testid={`provider-edit-${p.providerId}`} onClick={() => openEdit(p)}>
                        {t('common.edit')}
                      </button>
                      <button
                        className="link danger"
                        data-testid={`provider-delete-${p.providerId}`}
                        disabled={Number(p.publishedModelCount) > 0}
                        title={Number(p.publishedModelCount) > 0 ? t('providers.deleteBlocked') : undefined}
                        onClick={() => setDeleteTarget(p)}
                      >
                        {t('common.delete')}
                      </button>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
        <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
      </div>

      {dialogOpen && (
        <Dialog
          title={editing ? t('providers.editTitle') : t('providers.addTitle')}
          onClose={() => setDialogOpen(false)}
          testId="provider-dialog"
        >
          {formError && <ErrorBanner message={formError} />}
          <label className="field">
            <span>{t('providers.fieldName')}</span>
            <input
              data-testid="provider-form-name"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </label>
          <label className="field">
            <span>{t('providers.fieldType')}</span>
            <select
              data-testid="provider-form-type"
              value={form.type}
              onChange={(e) => setForm({ ...form, type: e.target.value })}
            >
              {PROVIDER_TYPES.map((v) => (
                <option key={v} value={v}>
                  {TYPE_LABELS[v]}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span>{t('providers.fieldBaseUrl')}</span>
            <input
              data-testid="provider-form-base-url"
              value={form.baseUrl}
              placeholder={t('providers.placeholderBaseUrl')}
              onChange={(e) => setForm({ ...form, baseUrl: e.target.value })}
            />
          </label>
          <label className="field">
            <span>{t('providers.fieldApiKey')} {editing ? `(${editing.apiKeyHint} — ${t('providers.leaveBlankToKeep')})` : ''}</span>
            <input
              type="password"
              data-testid="provider-form-api-key"
              value={form.apiKey}
              placeholder={editing ? t('providers.placeholderApiKeyKeep') : 'sk-…'}
              onChange={(e) => setForm({ ...form, apiKey: e.target.value })}
            />
          </label>
          <label className="field">
            <span>{t('providers.fieldEnabled')}</span>
            <input
              type="checkbox"
              data-testid="provider-form-enabled"
              checked={form.enabled}
              onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
            />
          </label>
          <label className="field">
            <span>{t('providers.fieldTimeout')}</span>
            <input
              data-testid="provider-form-timeout"
              value={form.timeout}
              onChange={(e) => setForm({ ...form, timeout: e.target.value })}
            />
          </label>
          <div className="field" data-testid="provider-form-headers">
            <span>{t('providers.fieldHeaders')}</span>
            {headers.map((row, i) => (
              <div key={i} className="toolbar">
                <input
                  aria-label={t('providers.headerName')}
                  value={row.name}
                  onChange={(e) => setHeaders(headers.map((r, j) => (j === i ? { ...r, name: e.target.value } : r)))}
                />
                <input
                  aria-label={t('providers.headerValue')}
                  value={row.value}
                  onChange={(e) => setHeaders(headers.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))}
                />
                <button className="link" onClick={() => setHeaders(headers.filter((_, j) => j !== i))}>
                  {t('common.remove')}
                </button>
              </div>
            ))}
            <button className="secondary" onClick={() => setHeaders([...headers, { name: '', value: '' }])}>
              {t('providers.addHeader')}
            </button>
          </div>
          <div className="dialog-actions">
            <button className="secondary" data-testid="provider-form-cancel" onClick={() => setDialogOpen(false)}>
              {t('common.cancel')}
            </button>
            <button className="primary" data-testid="provider-form-submit" onClick={() => void submitForm()}>
              {editing ? t('common.save') : t('common.create')}
            </button>
          </div>
        </Dialog>
      )}

      {deleteTarget && (
        <Dialog title={t('providers.deleteTitle')} onClose={() => setDeleteTarget(null)} testId="provider-delete-dialog">
          <p>
            {t('providers.deleteBodyA')} <strong>{deleteTarget.name}</strong>{t('providers.deleteBodyB')}
          </p>
          <div className="dialog-actions">
            <button className="secondary" data-testid="provider-delete-cancel" onClick={() => setDeleteTarget(null)}>
              {t('common.cancel')}
            </button>
            <button className="danger" data-testid="provider-delete-confirm" onClick={() => void confirmDelete()}>
              Delete
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}
