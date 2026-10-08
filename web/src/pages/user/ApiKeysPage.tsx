// End-user API keys page (feature-17): own API keys against the user
// surface's auth API.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { ApiError, formatTime, type ApiKeySummary } from '../../api';
import { CopyButton, Dialog, ErrorBanner, StateBadge } from '../../components';

interface CreateResponse {
  keyId: string;
  apiKey: string;
}

function rateLimitLabel(key: ApiKeySummary, t: (key: string) => string): string {
  const rpm = Number(key.rateLimitRpm || 0);
  const tpm = Number(key.rateLimitTpm || 0);
  if (!rpm && !tpm) return t('uapikeys.unlimited');
  return [rpm ? `${rpm} RPM` : '', tpm ? `${tpm.toLocaleString()} TPM` : ''].filter(Boolean).join(' · ');
}

export default function ApiKeysPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [keys, setKeys] = useState<ApiKeySummary[]>([]);
  const [error, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<ApiKeySummary | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<ApiKeySummary | null>(null);
  const [created, setCreated] = useState('');
  const [saved, setSaved] = useState(false);

  const load = useCallback(async () => {
    try {
      const data = await api.get<{ keys?: ApiKeySummary[] }>('/api/v1/auth/api-keys?page.limit=100', orgId);
      setKeys(data.keys || []);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('uapikeys.loadFailed'));
    }
  }, [api, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="page">
      <div className="page-header">
        <h1>{t('uapikeys.title')}</h1>
        <button data-testid="create-api-key" onClick={() => setCreateOpen(true)}>{t('uapikeys.create')}</button>
      </div>
      {error && <ErrorBanner message={error} />}
      {keys.length === 0 ? (
        <div className="empty" data-testid="api-keys-empty">{t('uapikeys.empty')}</div>
      ) : (
        <table className="table" data-testid="api-keys-table">
          <thead><tr>
            <th>{t('uapikeys.colName')}</th><th>{t('uapikeys.colPrefix')}</th><th>{t('uapikeys.colRateLimit')}</th>
            <th>{t('uapikeys.colCreated')}</th><th>{t('uapikeys.colExpires')}</th><th>{t('uapikeys.colStatus')}</th><th>{t('uapikeys.actions')}</th>
          </tr></thead>
          <tbody>{keys.map((key) => (
            <tr key={key.keyId} data-testid={`api-key-row-${key.keyId}`}>
              <td>{key.name}</td><td>{key.prefix}</td>
              <td data-testid={`rate-limit-cell-${key.keyId}`}>{rateLimitLabel(key, t)}</td>
              <td>{formatTime(key.createdAt)}</td>
              <td>{key.expiresAt && key.expiresAt !== '0' ? formatTime(key.expiresAt) : t('common.never')}</td>
              <td><StateBadge state={key.revoked ? 'revoked' : 'active'} /></td>
              <td>{!key.revoked && <>
                <button className="link" data-testid={`edit-rate-limit-${key.keyId}`} onClick={() => setEditTarget(key)}>{t('common.edit')}</button>
                <button className="link danger" data-testid={`revoke-${key.keyId}`} onClick={() => setRevokeTarget(key)}>{t('uapikeys.revoke')}</button>
              </>}</td>
            </tr>
          ))}</tbody>
        </table>
      )}
      {createOpen && <KeyDialog api={api} orgId={orgId} t={t} onClose={() => setCreateOpen(false)} onSaved={(secret) => { setCreateOpen(false); setCreated(secret || ''); setSaved(false); void load(); }} />}
      {editTarget && <KeyDialog api={api} orgId={orgId} t={t} apiKey={editTarget} onClose={() => setEditTarget(null)} onSaved={() => { setEditTarget(null); void load(); }} />}
      {revokeTarget && <Dialog title={t('uapikeys.revokeTitle')} testId="revoke-dialog" onClose={() => setRevokeTarget(null)}>
        <p>{t('uapikeys.revokeConfirm', { name: revokeTarget.name })}</p>
        <div className="dialog-actions">
          <button className="secondary" onClick={() => setRevokeTarget(null)}>{t('common.cancel')}</button>
          <button className="danger" data-testid="confirm-revoke" onClick={async () => {
            try {
              await api.post(`/api/v1/auth/api-keys/${revokeTarget.keyId}:revoke`, orgId, {});
              setRevokeTarget(null);
              void load();
            } catch (e) {
              setError(e instanceof Error ? e.message : t('uapikeys.revokeFailed'));
            }
          }}>{t('uapikeys.revoke')}</button>
        </div>
      </Dialog>}
      {created && <Dialog title={t('uapikeys.createdTitle')} testId="created-dialog" onClose={() => saved && setCreated('')}>
        <p>{t('uapikeys.createdNote')}</p><div className="secret-box" data-testid="secret-display">{created}</div>
        <CopyButton text={created} label={t('uapikeys.copy')} />
        <div className="dialog-actions"><label><input type="checkbox" data-testid="saved-checkbox" checked={saved} onChange={(event) => setSaved(event.target.checked)} /> {t('uapikeys.saved')}</label>
          <button data-testid="close-created-dialog" disabled={!saved} onClick={() => setCreated('')}>{t('common.done')}</button></div>
      </Dialog>}
    </div>
  );
}

function KeyDialog({ api, orgId, t, apiKey, onClose, onSaved }: {
  api: ReturnType<typeof useApi>;
  orgId: string;
  t: (key: string, vars?: Record<string, string | number>) => string;
  apiKey?: ApiKeySummary;
  onClose: () => void;
  onSaved: (secret?: string) => void;
}) {
  const [name, setName] = useState(apiKey?.name || '');
  const [rpm, setRpm] = useState(apiKey?.rateLimitRpm || '');
  const [tpm, setTpm] = useState(apiKey?.rateLimitTpm || '');
  const [expiry, setExpiry] = useState('never');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const edit = Boolean(apiKey);

  const submit = async () => {
    if (!name.trim()) {
      setError(t('uapikeys.nameRequired'));
      return;
    }
    setSaving(true);
    setError('');
    try {
      const body: Record<string, unknown> = { name: name.trim(), rateLimitRpm: rpm || '0', rateLimitTpm: tpm || '0' };
      if (apiKey) body.expiresAt = apiKey.expiresAt || '0';
      if (!edit && expiry !== 'never') body.expiresAt = String(Math.floor(Date.now() / 1000) + Number(expiry) * 86400);
      if (apiKey) {
        await api.put(`/api/v1/auth/api-keys/${apiKey.keyId}`, orgId, body);
        onSaved();
      } else {
        const result = await api.post<CreateResponse>('/api/v1/auth/api-keys', orgId, body);
        onSaved(result.apiKey);
      }
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t(edit ? 'uapikeys.updateFailed' : 'uapikeys.createFailed'));
    } finally {
      setSaving(false);
    }
  };

  return <Dialog title={t(edit ? 'uapikeys.editTitle' : 'uapikeys.createTitle')} testId={edit ? 'edit-dialog' : 'create-dialog'} onClose={onClose}>
    {edit && <p className="muted">{t('uapikeys.editNote')}</p>}
    <div className="form-grid">
      <div className="form-field full"><label htmlFor="user-key-name">{t('uapikeys.fieldName')}</label><input id="user-key-name" data-testid="key-name-input" maxLength={64} value={name} onChange={(event) => setName(event.target.value)} /></div>
      {!edit && <div className="form-field full"><label htmlFor="user-key-expiry">{t('uapikeys.fieldExpiry')}</label><select id="user-key-expiry" data-testid="key-expiry-select" value={expiry} onChange={(event) => setExpiry(event.target.value)}><option value="never">{t('common.never')}</option><option value="30">{t('common.30days')}</option><option value="90">{t('common.90days')}</option><option value="365">{t('common.365days')}</option></select></div>}
      <div className="form-field"><label htmlFor="rate-limit-rpm">{t('uapikeys.fieldRpm')}</label><input id="rate-limit-rpm" data-testid={edit ? 'edit-rate-limit-rpm' : 'rate-limit-rpm'} type="number" min={0} step={1} value={rpm} onChange={(event) => setRpm(event.target.value)} placeholder={t('uapikeys.placeholderZero')} /></div>
      <div className="form-field"><label htmlFor="rate-limit-tpm">{t('uapikeys.fieldTpm')}</label><input id="rate-limit-tpm" data-testid={edit ? 'edit-rate-limit-tpm' : 'rate-limit-tpm'} type="number" min={0} step={1} value={tpm} onChange={(event) => setTpm(event.target.value)} placeholder={t('uapikeys.placeholderZero')} /></div>
    </div>
    {error && <ErrorBanner message={error} />}
    <div className="dialog-actions"><button className="secondary" onClick={onClose}>{t('common.cancel')}</button><button data-testid={edit ? 'submit-edit-key' : 'submit-create-key'} disabled={saving} onClick={() => void submit()}>{saving ? t('common.saving') : t(edit ? 'common.save' : 'common.create')}</button></div>
  </Dialog>;
}
