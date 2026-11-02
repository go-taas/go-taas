// API Keys page: list, create (one-time reveal), revoke.
// Implements docs/design/api-key-management.md FR1-FR3, AC1-AC4, AC8, AC9.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  ApiError,
  formatTime,
  type ApiKeySummary,
  type PageMeta,
  type ProjectSummary,
  type UserSummary,
} from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import {
  CopyButton,
  Dialog,
  ErrorBanner,
  Pagination,
  StateBadge,
} from '../components';

interface ListResponse {
  response: { code: number; message: string };
  keys: ApiKeySummary[];
  pageMeta?: PageMeta;
}

interface CreateResponse {
  response: { code: number; message: string };
  keyId: string;
  apiKey: string;
}

const PAGE_SIZE = 20;

// Derive the display status from the raw fields: revoked wins, then
// expiry, else active.
function keyStatus(k: ApiKeySummary): string {
  if (k.revoked) return 'revoked';
  const exp = parseInt(k.expiresAt || '0', 10);
  if (exp > 0 && exp * 1000 < Date.now()) return 'expired';
  return 'active';
}

// formatRateLimit renders the key's rate limits or "Unlimited".
function formatRateLimit(
  k: ApiKeySummary,
  t: (key: string, vars?: Record<string, string | number>) => string,
): string {
  const rpm = parseInt(k.rateLimitRpm || '0', 10);
  const tpm = parseInt(k.rateLimitTpm || '0', 10);
  if (rpm === 0 && tpm === 0) return t('common.rateLimitUnlimited');
  const rpmStr = rpm > 0 ? `${rpm} rpm` : t('common.rateLimitRpm');
  const tpmStr = tpm > 0 ? `${(tpm / 1000).toFixed(0)}k tpm` : t('common.rateLimitTpm');
  return `${rpmStr} · ${tpmStr}`;
}

export default function ApiKeysPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [keys, setKeys] = useState<ApiKeySummary[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const [createOpen, setCreateOpen] = useState(false);
  const [created, setCreated] = useState<{ keyId: string; apiKey: string } | null>(null);
  const [savedConfirmed, setSavedConfirmed] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<ApiKeySummary | null>(null);
  const [editTarget, setEditTarget] = useState<ApiKeySummary | null>(null);
  const [transferTarget, setTransferTarget] = useState<ApiKeySummary | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ApiKeySummary | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<ListResponse>(
        `/api/v1/admin/auth/api-keys?page.offset=${offset}&page.limit=${PAGE_SIZE}&scope=all`,
        orgId,
      );
      setKeys(data.keys || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('apikeys.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, t]);

  const [busy, setBusy] = useState('');

  const restoreKey = async (key: ApiKeySummary) => {
    setBusy(key.keyId);
    setError('');
    try {
      await api.post(`/api/v1/admin/auth/api-keys/${key.keyId}:restore`, orgId, {});
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('apikeys.restoreFailed'));
    } finally {
      setBusy('');
    }
  };

  const deleteKey = async (key: ApiKeySummary) => {
    setError('');
    try {
      await api.del(`/api/v1/admin/auth/api-keys/${key.keyId}`, orgId);
      setDeleteTarget(null);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('apikeys.deleteFailed'));
    }
  };

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('apikeys.title')}</h1>
          <div className="subtitle">{t('apikeys.subtitle')}</div>
        </div>
        <button data-testid="create-api-key" onClick={() => setCreateOpen(true)}>
          {t('apikeys.create')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : keys.length === 0 ? (
          <div className="empty-state" data-testid="api-keys-empty">
            {t('apikeys.empty')}
          </div>
        ) : (
          <table className="data" data-testid="api-keys-table">
            <thead>
              <tr>
                <th>{t('apikeys.colName')}</th>
                <th>{t('apikeys.colOwner')}</th>
                <th>{t('apikeys.colProject')}</th>
                <th>{t('apikeys.colKey')}</th>
                <th>{t('apikeys.colStatus')}</th>
                <th>{t('apikeys.colRateLimit')}</th>
                <th>{t('apikeys.colCreated')}</th>
                <th>{t('apikeys.colExpires')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {keys.map((k) => (
                <tr key={k.keyId} data-testid={`api-key-row-${k.keyId}`}>
                  <td>{k.name}</td>
                  <td data-testid={`owner-cell-${k.keyId}`}>
                    {k.shared || !k.ownerUserId
                      ? t('apikeys.ownerShared')
                      : (k.ownerDisplayName || k.ownerUserId)}
                  </td>
                  <td data-testid={`project-cell-${k.keyId}`}>
                    {k.projectDisplayName || k.projectId || t('apikeys.projectNone')}
                  </td>
                  <td className="mono">{k.prefix}…</td>
                  <td>
                    <StateBadge state={keyStatus(k)} />
                  </td>
                  <td data-testid={`rate-limit-cell-${k.keyId}`}>
                    {formatRateLimit(k, t)}
                  </td>
                  <td>{formatTime(k.createdAt)}</td>
                  <td>{k.expiresAt && k.expiresAt !== '0' ? formatTime(k.expiresAt) : t('common.never')}</td>
                  <td>
                    {!k.revoked ? (
                      <>
                        <button
                          className="link"
                          data-testid={`edit-rate-limit-${k.keyId}`}
                          onClick={() => setEditTarget(k)}
                        >
                          {t('common.edit')}
                        </button>
                        <button
                          className="link"
                          data-testid={`transfer-${k.keyId}`}
                          onClick={() => setTransferTarget(k)}
                        >
                          {t('apikeys.transfer')}
                        </button>
                        <button
                          className="link danger"
                          data-testid={`revoke-${k.keyId}`}
                          onClick={() => setRevokeTarget(k)}
                        >
                          {t('apikeys.revoke')}
                        </button>
                      </>
                    ) : (
                      <button
                        className="link"
                        data-testid={`restore-${k.keyId}`}
                        onClick={() => void restoreKey(k)}
                        disabled={busy === k.keyId}
                      >
                        {t('apikeys.restore')}
                      </button>
                    )}
                    <button
                      className="link danger"
                      data-testid={`delete-${k.keyId}`}
                      onClick={() => setDeleteTarget(k)}
                    >
                      {t('common.delete')}
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

      {createOpen && (
        <CreateDialog
          orgId={orgId}
          onClose={() => setCreateOpen(false)}
          onCreated={(res) => {
            setCreateOpen(false);
            setCreated(res);
            setSavedConfirmed(false);
            void load();
          }}
        />
      )}

      {created && (
        <CreatedDialog
          secret={created.apiKey}
          confirmed={savedConfirmed}
          onConfirm={() => setSavedConfirmed(true)}
          onClose={() => setCreated(null)}
        />
      )}

      {revokeTarget && (
        <RevokeDialog
          apiKey={revokeTarget}
          orgId={orgId}
          onClose={() => setRevokeTarget(null)}
          onRevoked={() => {
            setRevokeTarget(null);
            void load();
          }}
        />
      )}

      {editTarget && (
        <EditDialog
          apiKey={editTarget}
          orgId={orgId}
          onClose={() => setEditTarget(null)}
          onUpdated={() => {
            setEditTarget(null);
            void load();
          }}
        />
      )}

      {transferTarget && (
        <TransferDialog
          apiKey={transferTarget}
          orgId={orgId}
          onClose={() => setTransferTarget(null)}
          onTransferred={() => {
            setTransferTarget(null);
            void load();
          }}
        />
      )}

      {deleteTarget && (
        <Dialog
          title={t('apikeys.deleteTitle')}
          testId="delete-key-dialog"
          onClose={() => setDeleteTarget(null)}
        >
          <p>{t('apikeys.deleteConfirm', { name: deleteTarget.name })}</p>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setDeleteTarget(null)}>
              {t('common.cancel')}
            </button>
            <button
              className="danger"
              data-testid="confirm-delete-key"
              onClick={() => void deleteKey(deleteTarget)}
            >
              {t('common.delete')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

// TransferDialog moves a key to another member or project, or turns it
// into an organization-shared key. Only an organization administrator
// may transfer; the server enforces that.
function TransferDialog({
  apiKey,
  orgId,
  onClose,
  onTransferred,
}: {
  apiKey: ApiKeySummary;
  orgId: string;
  onClose: () => void;
  onTransferred: () => void;
}) {
  const { t } = useI18n();
  const [ownerUserId, setOwnerUserId] = useState(apiKey.ownerUserId || '');
  const [projectId, setProjectId] = useState(apiKey.projectId || '');
  const [shared, setShared] = useState(Boolean(apiKey.shared));
  const [users, setUsers] = useState<UserSummary[]>([]);
  const [projects, setProjects] = useState<ProjectSummary[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    api.get<{ users?: UserSummary[] }>('/api/v1/admin/auth/users?page.limit=100', orgId)
      .then((data) => setUsers(data.users || []))
      .catch(() => setUsers([]));
    api.get<{ projects?: ProjectSummary[] }>(`/api/v1/admin/tenancy/projects?organization_id=${orgId}&page.limit=100`, orgId)
      .then((data) => setProjects(data.projects || []))
      .catch(() => setProjects([]));
  }, [api, orgId]);

  const submit = async () => {
    setSaving(true);
    setError('');
    try {
      await api.put(`/api/v1/admin/auth/api-keys/${apiKey.keyId}/ownership`, orgId, {
        ownerUserId: shared ? '' : ownerUserId,
        projectId,
        shared,
      });
      onTransferred();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('apikeys.transferFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog title={t('apikeys.transferTitle')} testId="transfer-dialog" onClose={onClose}>
      <p className="muted">{t('apikeys.transferNote', { name: apiKey.name })}</p>
      <div className="form-grid">
        <div className="form-field full">
          <label className="checkbox">
            <input type="checkbox" data-testid="transfer-shared" checked={shared}
              onChange={(event) => { setShared(event.target.checked); if (event.target.checked) setOwnerUserId(''); }} />
            {t('apikeys.transferShared')}
          </label>
        </div>
        {!shared && (
          <div className="form-field full">
            <label htmlFor="transfer-owner">{t('apikeys.colOwner')}</label>
            <select id="transfer-owner" data-testid="transfer-owner-select" value={ownerUserId}
              onChange={(event) => setOwnerUserId(event.target.value)}>
              <option value="">{t('apikeys.ownerPlaceholder')}</option>
              {users.map((u) => (
                <option key={u.userId} value={u.userId}>{u.displayName || u.username || u.userId}</option>
              ))}
            </select>
          </div>
        )}
        <div className="form-field full">
          <label htmlFor="transfer-project">{t('apikeys.colProject')}</label>
          <select id="transfer-project" data-testid="transfer-project-select" value={projectId}
            onChange={(event) => setProjectId(event.target.value)}>
            <option value="">{t('apikeys.projectNone')}</option>
            {projects.map((p) => (
              <option key={p.projectId} value={p.projectId}>{p.displayName}</option>
            ))}
          </select>
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button data-testid="submit-transfer" disabled={saving} onClick={() => void submit()}>
          {saving ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}

function CreateDialog({
  orgId,
  onClose,
  onCreated,
}: {
  orgId: string;
  onClose: () => void;
  onCreated: (res: CreateResponse) => void;
}) {
  const [name, setName] = useState('');
  const [expiryDays, setExpiryDays] = useState('never');
  const [rpm, setRpm] = useState('');
  const [tpm, setTpm] = useState('');
  // Only the organization console creates a shared key: a personal key in
  // the user console always belongs to its owner.
  const [shared, setShared] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  const submit = async () => {
    if (!name.trim()) {
      setError(t('apikeys.nameRequired'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const body: Record<string, unknown> = { name: name.trim() };
      if (expiryDays !== 'never') {
        const days = parseInt(expiryDays, 10);
        body.expiresAt = String(Math.floor(Date.now() / 1000) + days * 86400);
      }
      if (rpm.trim() !== '') body.rateLimitRpm = String(parseInt(rpm, 10));
      if (tpm.trim() !== '') body.rateLimitTpm = String(parseInt(tpm, 10));
      if (shared) body.shared = true;
      const res = await api.post<CreateResponse>(
        '/api/v1/admin/auth/api-keys',
        orgId,
        body,
      );
      onCreated(res);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('apikeys.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('apikeys.createTitle')} onClose={onClose} testId="create-dialog">
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="key-name">{t('apikeys.fieldName')}</label>
          <input
            id="key-name"
            data-testid="key-name-input"
            value={name}
            maxLength={64}
            onChange={(e) => setName(e.target.value)}
            placeholder={t('apikeys.placeholderName')}
          />
        </div>
        <div className="form-field full">
          <label htmlFor="key-expiry">{t('apikeys.fieldExpiry')}</label>
          <select
            id="key-expiry"
            data-testid="key-expiry-select"
            value={expiryDays}
            onChange={(e) => setExpiryDays(e.target.value)}
          >
            <option value="never">{t('common.never')}</option>
            <option value="30">{t('common.30days')}</option>
            <option value="90">{t('common.90days')}</option>
            <option value="365">{t('common.365days')}</option>
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="rate-limit-rpm">{t('apikeys.fieldRpm')}</label>
          <input
            id="rate-limit-rpm"
            data-testid="rate-limit-rpm"
            type="number"
            min={0}
            value={rpm}
            onChange={(e) => setRpm(e.target.value)}
            placeholder={t('apikeys.placeholderZero')}
          />
        </div>
        <div className="form-field">
          <label htmlFor="rate-limit-tpm">{t('apikeys.fieldTpm')}</label>
          <input
            id="rate-limit-tpm"
            data-testid="rate-limit-tpm"
            type="number"
            min={0}
            value={tpm}
            onChange={(e) => setTpm(e.target.value)}
            placeholder={t('apikeys.placeholderZero')}
          />
        </div>
        <div className="form-field full">
          <label className="checkbox">
            <input
              type="checkbox"
              data-testid="key-shared-checkbox"
              checked={shared}
              onChange={(e) => setShared(e.target.checked)}
            />
            {t('apikeys.createShared')}
          </label>
          <div className="card-note">{t('apikeys.createSharedNote')}</div>
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="submit-create-key"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? t('common.creating') : t('common.create')}
        </button>
      </div>
    </Dialog>
  );
}

// EditDialog edits a key's name, expiry and rate limits post-creation
// (feature #11, AD4). The secret is never changed or shown.
function EditDialog({
  apiKey,
  orgId,
  onClose,
  onUpdated,
}: {
  apiKey: ApiKeySummary;
  orgId: string;
  onClose: () => void;
  onUpdated: () => void;
}) {
  const [name, setName] = useState(apiKey.name);
  const [rpm, setRpm] = useState(apiKey.rateLimitRpm || '');
  const [tpm, setTpm] = useState(apiKey.rateLimitTpm || '');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  const submit = async () => {
    if (!name.trim()) {
      setError(t('apikeys.nameRequired'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const body: Record<string, unknown> = { name: name.trim() };
      if (rpm.trim() !== '') body.rateLimitRpm = String(parseInt(rpm, 10));
      if (tpm.trim() !== '') body.rateLimitTpm = String(parseInt(tpm, 10));
      await api.put(`/api/v1/admin/auth/api-keys/${apiKey.keyId}`, orgId, body);
      onUpdated();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('apikeys.updateFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('apikeys.editTitle')} onClose={onClose} testId="edit-dialog">
      <p className="muted">{t('apikeys.editNote')}</p>
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="edit-key-name">{t('apikeys.fieldName')}</label>
          <input
            id="edit-key-name"
            data-testid="edit-key-name-input"
            value={name}
            maxLength={64}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <div className="form-field">
          <label htmlFor="edit-rate-limit-rpm">{t('apikeys.fieldRpm')}</label>
          <input
            id="edit-rate-limit-rpm"
            data-testid="edit-rate-limit-rpm"
            type="number"
            min={0}
            value={rpm}
            onChange={(e) => setRpm(e.target.value)}
            placeholder={t('apikeys.placeholderZero')}
          />
        </div>
        <div className="form-field">
          <label htmlFor="edit-rate-limit-tpm">{t('apikeys.fieldTpm')}</label>
          <input
            id="edit-rate-limit-tpm"
            data-testid="edit-rate-limit-tpm"
            type="number"
            min={0}
            value={tpm}
            onChange={(e) => setTpm(e.target.value)}
            placeholder={t('apikeys.placeholderZero')}
          />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="submit-edit-key"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}

// CreatedDialog enforces the reveal-once contract (FR1.3): it cannot be
// dismissed until the user confirms they saved the key.
function CreatedDialog({
  secret,
  confirmed,
  onConfirm,
  onClose,
}: {
  secret: string;
  confirmed: boolean;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  return (
    <Dialog
      title={t('apikeys.createdTitle')}
      onClose={confirmed ? onClose : () => undefined}
      testId="created-dialog"
    >
      <p>
        {t('apikeys.createdBody')} <strong>{t('apikeys.createdWarning')}</strong>
      </p>
      <div className="secret-box" data-testid="secret-display">
        {secret}
      </div>
      <CopyButton text={secret} label={t('apikeys.copyKey')} />
      <div style={{ marginTop: 18 }}>
        <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <input
            type="checkbox"
            data-testid="saved-checkbox"
            checked={confirmed}
            onChange={onConfirm}
          />
          {t('apikeys.savedCheckbox')}
        </label>
      </div>
      <div className="dialog-actions">
        <button
          data-testid="close-created-dialog"
          disabled={!confirmed}
          onClick={onClose}
        >
          {t('common.done')}
        </button>
      </div>
    </Dialog>
  );
}

function RevokeDialog({
  apiKey,
  orgId,
  onClose,
  onRevoked,
}: {
  apiKey: ApiKeySummary;
  orgId: string;
  onClose: () => void;
  onRevoked: () => void;
}) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const { t } = useI18n();

  const revoke = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.post(`/api/v1/admin/auth/api-keys/${apiKey.keyId}:revoke`, orgId);
      onRevoked();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('apikeys.revokeFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('apikeys.revokeTitle')} onClose={onClose} testId="revoke-dialog">
      <p
        dangerouslySetInnerHTML={{
          __html: t('apikeys.revokeBody', { name: apiKey.name, prefix: apiKey.prefix }),
        }}
      />
      <div className="error-banner">{t('apikeys.revokeWarning')}</div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          className="danger"
          data-testid="confirm-revoke"
          disabled={submitting}
          onClick={() => void revoke()}
        >
          {submitting ? t('apikeys.revoking') : t('apikeys.revoke')}
        </button>
      </div>
    </Dialog>
  );
}
