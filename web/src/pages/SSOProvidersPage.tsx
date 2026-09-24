// SSO providers page (feature #7): the provider directory with create,
// edit, enable/disable, and delete actions.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type PageMeta, type SSOProvider } from '../api';
import { useOrg } from '../org';
import { Dialog, ErrorBanner, Pagination, StateBadge } from '../components';

interface ListResponse {
  response: { code: number; message: string };
  providers: SSOProvider[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;
const TYPES = ['oidc', 'saml', 'ldap'];

export default function SSOProvidersPage() {
  const { orgId } = useOrg();
  const [providers, setProviders] = useState<SSOProvider[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [editTarget, setEditTarget] = useState<SSOProvider | null>(null);
  const [confirmTarget, setConfirmTarget] = useState<{
    provider: SSOProvider;
    action: 'enable' | 'disable' | 'delete';
  } | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams({
        [`page.offset`]: String(offset),
        [`page.limit`]: String(PAGE_SIZE),
      });
      const data = await api.get<ListResponse>(
        `/api/v1/admin/auth/sso/providers?${params.toString()}`,
        orgId,
      );
      setProviders(data.providers || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load providers');
    } finally {
      setLoading(false);
    }
  }, [orgId, offset]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>SSO Providers</h1>
          <div className="subtitle">
            Identity providers for federated sign-in (OIDC, SAML, LDAP).
          </div>
        </div>
        <button data-testid="create-sso-provider" onClick={() => setCreateOpen(true)}>
          Create Provider
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">Loading…</div>
        ) : providers.length === 0 ? (
          <div className="empty-state" data-testid="sso-providers-empty">
            No SSO providers configured.
          </div>
        ) : (
          <table className="data" data-testid="sso-providers-table">
            <thead>
              <tr>
                <th>Provider</th>
                <th>Type</th>
                <th>State</th>
                <th>Default org</th>
                <th>JIT</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {providers.map((p) => (
                <tr key={p.providerId} data-testid={`sso-provider-row-${p.providerId}`}>
                  <td>
                    <strong className="mono">{p.providerId}</strong>
                    <div className="muted">{p.displayName}</div>
                  </td>
                  <td>
                    <span className="badge">{p.type}</span>
                  </td>
                  <td>
                    <StateBadge state={p.enabled ? 'active' : 'disabled'} />
                  </td>
                  <td className="mono">{p.defaultOrg || '—'}</td>
                  <td>{p.allowAutoProvision ? 'yes' : 'no'}</td>
                  <td>
                    <button
                      className="link"
                      data-testid={`sso-provider-edit-${p.providerId}`}
                      onClick={() => setEditTarget(p)}
                    >
                      Edit
                    </button>
                    {p.enabled ? (
                      <button
                        className="link danger"
                        data-testid={`sso-provider-disable-${p.providerId}`}
                        onClick={() => setConfirmTarget({ provider: p, action: 'disable' })}
                      >
                        Disable
                      </button>
                    ) : (
                      <button
                        className="link"
                        data-testid={`sso-provider-enable-${p.providerId}`}
                        onClick={() => setConfirmTarget({ provider: p, action: 'enable' })}
                      >
                        Enable
                      </button>
                    )}
                    <button
                      className="link danger"
                      data-testid={`sso-provider-delete-${p.providerId}`}
                      onClick={() => setConfirmTarget({ provider: p, action: 'delete' })}
                    >
                      Delete
                    </button>
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

      {createOpen && (
        <CreateProviderDialog
          orgId={orgId}
          onClose={() => setCreateOpen(false)}
          onDone={() => {
            setCreateOpen(false);
            void load();
          }}
        />
      )}

      {editTarget && (
        <EditProviderDialog
          orgId={orgId}
          provider={editTarget}
          onClose={() => setEditTarget(null)}
          onDone={() => {
            setEditTarget(null);
            void load();
          }}
        />
      )}

      {confirmTarget && (
        <ConfirmDialog
          orgId={orgId}
          provider={confirmTarget.provider}
          action={confirmTarget.action}
          onClose={() => setConfirmTarget(null)}
          onDone={() => {
            setConfirmTarget(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

function ProviderFields({
  provider,
  setProvider,
}: {
  provider: Partial<SSOProvider>;
  setProvider: (p: Partial<SSOProvider>) => void;
}) {
  const type = provider.type || 'oidc';
  return (
    <div className="form-grid">
      <div className="form-field">
        <label htmlFor="sso-provider-type">Type</label>
        <select
          id="sso-provider-type"
          data-testid="sso-provider-type-select"
          value={type}
          onChange={(e) => setProvider({ ...provider, type: e.target.value })}
        >
          {TYPES.map((t) => (
            <option key={t} value={t} data-testid={`sso-provider-type-option-${t}`}>
              {t}
            </option>
          ))}
        </select>
      </div>
      <div className="form-field">
        <label htmlFor="sso-provider-name">Display name</label>
        <input
          id="sso-provider-name"
          data-testid="sso-provider-name-input"
          value={provider.displayName || ''}
          maxLength={128}
          onChange={(e) => setProvider({ ...provider, displayName: e.target.value })}
        />
      </div>
      {type === 'oidc' && (
        <>
          <div className="form-field">
            <label htmlFor="sso-provider-issuer">Issuer</label>
            <input
              id="sso-provider-issuer"
              data-testid="sso-provider-issuer-input"
              value={provider.issuer || ''}
              onChange={(e) => setProvider({ ...provider, issuer: e.target.value })}
            />
          </div>
          <div className="form-field">
            <label htmlFor="sso-provider-client-id">Client ID</label>
            <input
              id="sso-provider-client-id"
              data-testid="sso-provider-client-id-input"
              value={provider.clientId || ''}
              onChange={(e) => setProvider({ ...provider, clientId: e.target.value })}
            />
          </div>
          <div className="form-field">
            <label htmlFor="sso-provider-client-secret">Client secret</label>
            <input
              id="sso-provider-client-secret"
              data-testid="sso-provider-client-secret-input"
              type="password"
              value={provider.clientSecret || ''}
              onChange={(e) => setProvider({ ...provider, clientSecret: e.target.value })}
            />
          </div>
          <div className="form-field">
            <label htmlFor="sso-provider-redirect-uri">Redirect URI</label>
            <input
              id="sso-provider-redirect-uri"
              data-testid="sso-provider-redirect-uri-input"
              value={provider.redirectUri || ''}
              onChange={(e) => setProvider({ ...provider, redirectUri: e.target.value })}
            />
          </div>
        </>
      )}
      {type === 'saml' && (
        <>
          <div className="form-field">
            <label htmlFor="sso-provider-metadata-url">Metadata URL</label>
            <input
              id="sso-provider-metadata-url"
              data-testid="sso-provider-metadata-url-input"
              value={provider.metadataUrl || ''}
              onChange={(e) => setProvider({ ...provider, metadataUrl: e.target.value })}
            />
          </div>
          <div className="form-field">
            <label htmlFor="sso-provider-entity-id">Entity ID</label>
            <input
              id="sso-provider-entity-id"
              data-testid="sso-provider-entity-id-input"
              value={provider.entityId || ''}
              onChange={(e) => setProvider({ ...provider, entityId: e.target.value })}
            />
          </div>
          <div className="form-field">
            <label htmlFor="sso-provider-acs-url">ACS URL</label>
            <input
              id="sso-provider-acs-url"
              data-testid="sso-provider-acs-url-input"
              value={provider.acsUrl || ''}
              onChange={(e) => setProvider({ ...provider, acsUrl: e.target.value })}
            />
          </div>
        </>
      )}
      {type === 'ldap' && (
        <>
          <div className="form-field">
            <label htmlFor="sso-provider-host">Host</label>
            <input
              id="sso-provider-host"
              data-testid="sso-provider-host-input"
              value={provider.host || ''}
              onChange={(e) => setProvider({ ...provider, host: e.target.value })}
            />
          </div>
          <div className="form-field">
            <label htmlFor="sso-provider-port">Port</label>
            <input
              id="sso-provider-port"
              data-testid="sso-provider-port-input"
              type="number"
              value={provider.port || 389}
              onChange={(e) => setProvider({ ...provider, port: Number(e.target.value) })}
            />
          </div>
          <div className="form-field">
            <label htmlFor="sso-provider-base-dn">Base DN</label>
            <input
              id="sso-provider-base-dn"
              data-testid="sso-provider-base-dn-input"
              value={provider.baseDn || ''}
              onChange={(e) => setProvider({ ...provider, baseDn: e.target.value })}
            />
          </div>
        </>
      )}
      <div className="form-field">
        <label htmlFor="sso-provider-default-org">Default org</label>
        <input
          id="sso-provider-default-org"
          data-testid="sso-provider-default-org-input"
          value={provider.defaultOrg || ''}
          onChange={(e) => setProvider({ ...provider, defaultOrg: e.target.value })}
        />
      </div>
      <div className="form-field">
        <label htmlFor="sso-provider-jit-toggle">Auto-provision (JIT)</label>
        <input
          id="sso-provider-jit-toggle"
          data-testid="sso-provider-jit-toggle"
          type="checkbox"
          checked={provider.allowAutoProvision !== false}
          onChange={(e) => setProvider({ ...provider, allowAutoProvision: e.target.checked })}
        />
      </div>
      <div className="form-field full">
        <label htmlFor="sso-provider-mapping">Attribute mapping (JSON)</label>
        <textarea
          id="sso-provider-mapping"
          data-testid="sso-provider-mapping-input"
          value={provider.attributeMapping || ''}
          rows={3}
          onChange={(e) => setProvider({ ...provider, attributeMapping: e.target.value })}
          placeholder='{"username":"preferred_username","email":"email","org":"groups","role":"groups"}'
        />
      </div>
    </div>
  );
}

function CreateProviderDialog({
  orgId,
  onClose,
  onDone,
}: {
  orgId: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const [provider, setProvider] = useState<Partial<SSOProvider>>({ type: 'oidc' });
  const [id, setId] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    if (!/^[a-z0-9][a-z0-9-]{2,63}$/.test(id.trim())) {
      setError('ID must be 3-64 chars: lowercase letters, digits, hyphens.');
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.post('/api/v1/admin/auth/sso/providers', orgId, {
        provider: { ...provider, providerId: id.trim() },
      });
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to create provider');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title="Create SSO Provider" onClose={onClose} testId="create-sso-provider-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="sso-provider-id">Provider ID</label>
          <input
            id="sso-provider-id"
            data-testid="sso-provider-id-input"
            value={id}
            maxLength={64}
            onChange={(e) => setId(e.target.value)}
            placeholder="e.g. okta"
          />
        </div>
      </div>
      <ProviderFields provider={provider} setProvider={setProvider} />
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button data-testid="sso-provider-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? 'Creating…' : 'Create'}
        </button>
      </div>
    </Dialog>
  );
}

function EditProviderDialog({
  orgId,
  provider,
  onClose,
  onDone,
}: {
  orgId: string;
  provider: SSOProvider;
  onClose: () => void;
  onDone: () => void;
}) {
  const [fields, setFields] = useState<Partial<SSOProvider>>({ ...provider });
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.patch(
        `/api/v1/admin/auth/sso/providers/${provider.providerId}`,
        orgId,
        { provider: fields },
      );
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : 'failed to update provider');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={`Edit ${provider.providerId}`} onClose={onClose} testId="edit-sso-provider-dialog">
      <ProviderFields provider={fields} setProvider={setFields} />
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button data-testid="sso-provider-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? 'Saving…' : 'Save'}
        </button>
      </div>
    </Dialog>
  );
}

function ConfirmDialog({
  orgId,
  provider,
  action,
  onClose,
  onDone,
}: {
  orgId: string;
  provider: SSOProvider;
  action: 'enable' | 'disable' | 'delete';
  onClose: () => void;
  onDone: () => void;
}) {
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [blocked, setBlocked] = useState(false);

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      if (action === 'delete') {
        await api.del(`/api/v1/admin/auth/sso/providers/${provider.providerId}`, orgId);
      } else {
        await api.post(
          `/api/v1/admin/auth/sso/providers/${provider.providerId}:${action}`,
          orgId,
        );
      }
      onDone();
    } catch (e) {
      if (e instanceof ApiError && e.code === 10026) {
        setBlocked(true);
      } else {
        setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : `failed to ${action}`);
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={`${action === 'delete' ? 'Delete' : action === 'enable' ? 'Enable' : 'Disable'} ${provider.providerId}?`}
      onClose={onClose}
      testId="sso-provider-confirm-dialog"
    >
      {action === 'delete' ? (
        <p>This removes the provider. Providers with identity bindings cannot be deleted.</p>
      ) : action === 'enable' ? (
        <p>Enabling lets users sign in with this provider.</p>
      ) : (
        <p>Disabling pauses sign-in with this provider. The configuration is kept.</p>
      )}
      {blocked && (
        <div className="muted" data-testid="sso-provider-delete-blocked">
          This provider has identity bindings and cannot be deleted.
        </div>
      )}
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          className={action === 'disable' || action === 'delete' ? 'danger' : ''}
          data-testid="sso-provider-confirm-ok"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting ? 'Working…' : action === 'delete' ? 'Delete' : action === 'enable' ? 'Enable' : 'Disable'}
        </button>
      </div>
    </Dialog>
  );
}