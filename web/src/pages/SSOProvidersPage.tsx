// SSO providers page (feature #7): the provider directory with create,
// edit, enable/disable, and delete actions.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type PageMeta, type SSOProvider } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { FieldLabel, FormLegend, FormSection, RequiredStatus } from '../components/FormField';
import { Dialog, ErrorBanner, Pagination, RowActionsMenu, StateBadge } from '../components';

interface ListResponse {
  response: { code: number; message: string };
  providers: SSOProvider[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 20;
const TYPES = ['oidc', 'saml', 'ldap'];

export default function SSOProvidersPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
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
      setError(e instanceof Error ? e.message : t('sso.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('sso.title')}</h1>
          <div className="subtitle">{t('sso.subtitle')}</div>
        </div>
        <button data-testid="create-sso-provider" onClick={() => setCreateOpen(true)}>
          {t('sso.create')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : providers.length === 0 ? (
          <div className="empty-state" data-testid="sso-providers-empty">
            {t('sso.empty')}
          </div>
        ) : (
          <table className="data" data-testid="sso-providers-table">
            <thead>
              <tr>
                <th>{t('sso.colProvider')}</th>
                <th>{t('sso.colType')}</th>
                <th>{t('sso.colState')}</th>
                <th>{t('sso.colDefaultOrg')}</th>
                <th>{t('sso.colJit')}</th>
                <th className="col-actions">{t('common.actions')}</th>
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
                    <RowActionsMenu
                      label={t('common.actionsFor', { name: p.displayName || p.providerId })}
                      testId={`sso-provider-actions-${p.providerId}`}
                      actions={[
                        {
                          key: `sso-provider-edit-${p.providerId}`,
                          label: t('common.edit'),
                          group: 'edit',
                          onClick: () => setEditTarget(p),
                        },
                        p.enabled
                          ? {
                              key: `sso-provider-disable-${p.providerId}`,
                              label: t('common.disable'),
                              group: 'lifecycle',
                              danger: true,
                              onClick: () => setConfirmTarget({ provider: p, action: 'disable' }),
                            }
                          : {
                              key: `sso-provider-enable-${p.providerId}`,
                              label: t('common.enable'),
                              group: 'lifecycle',
                              onClick: () => setConfirmTarget({ provider: p, action: 'enable' }),
                            },
                        {
                          key: `sso-provider-delete-${p.providerId}`,
                          label: t('common.delete'),
                          group: 'lifecycle',
                          danger: true,
                          onClick: () => setConfirmTarget({ provider: p, action: 'delete' }),
                        },
                      ]}
                    />
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

// requiredProviderFields returns the labels of the inputs a provider cannot be
// saved without, by protocol: the identity pair is common, the rest depends on
// what the protocol actually needs. The submit row uses it to name what is
// missing before the operator submits.
function requiredProviderFields(
  t: (key: string) => string,
  id: string,
  provider: Partial<SSOProvider>,
): string[] {
  const type = provider.type || 'oidc';
  const missing = [
    !/^[a-z0-9][a-z0-9-]{2,63}$/.test(id.trim()) && t('sso.fieldProviderId'),
    !(provider.displayName || '').trim() && t('sso.fieldDisplayName'),
  ];
  if (type === 'oidc') {
    missing.push(
      !(provider.issuer || '').trim() && t('sso.fieldIssuer'),
      !(provider.clientId || '').trim() && t('sso.fieldClientId'),
      !(provider.clientSecret || '').trim() && t('sso.fieldClientSecret'),
      !(provider.redirectUri || '').trim() && t('sso.fieldRedirectUri'),
    );
  }
  if (type === 'saml') {
    missing.push(!(provider.metadataUrl || '').trim() && t('sso.fieldMetadataUrl'));
  }
  if (type === 'ldap') {
    missing.push(
      !(provider.host || '').trim() && t('sso.fieldHost'),
      !(provider.baseDn || '').trim() && t('sso.fieldBaseDn'),
    );
  }
  return missing.filter((entry): entry is string => Boolean(entry));
}

function ProviderFields({
  provider,
  setProvider,
}: {
  provider: Partial<SSOProvider>;
  setProvider: (p: Partial<SSOProvider>) => void;
}) {
  const { t } = useI18n();
  const type = provider.type || 'oidc';
  return (
    <>
    <FormSection
      step={1}
      title={t('sso.sectionIdentity')}
      hint={t('sso.sectionIdentityHint')}
      requirement="required"
      testId="sso-section-identity"
    >
    <div className="form-grid">
      <div className="form-field">
        <FieldLabel
          htmlFor="sso-provider-type"
          text={t('sso.fieldType')}
          requirement="defaulted"
          flag={t('form.reqDefaultValue', { value: type })}
        />
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
        <FieldLabel
          htmlFor="sso-provider-name"
          text={t('sso.fieldDisplayName')}
          requirement="required"
        />
        <input
          id="sso-provider-name"
          data-testid="sso-provider-name-input"
          value={provider.displayName || ''}
          maxLength={128}
          onChange={(e) => setProvider({ ...provider, displayName: e.target.value })}
        />
      </div>
    </div>
    </FormSection>

    <FormSection
      step={2}
      title={t('sso.sectionProtocol')}
      hint={t('sso.sectionProtocolHint', { type: type.toUpperCase() })}
      requirement="required"
      testId="sso-section-protocol"
    >
    <div className="form-grid">
      {type === 'oidc' && (
        <>
          <div className="form-field">
            <FieldLabel
              htmlFor="sso-provider-issuer"
              text={t('sso.fieldIssuer')}
              requirement="required"
            />
            <input
              id="sso-provider-issuer"
              data-testid="sso-provider-issuer-input"
              value={provider.issuer || ''}
              onChange={(e) => setProvider({ ...provider, issuer: e.target.value })}
            />
          </div>
          <div className="form-field">
            <FieldLabel htmlFor="sso-provider-client-id" text={t('sso.fieldClientId')} requirement="required" />
            <input
              id="sso-provider-client-id"
              data-testid="sso-provider-client-id-input"
              value={provider.clientId || ''}
              onChange={(e) => setProvider({ ...provider, clientId: e.target.value })}
            />
          </div>
          <div className="form-field">
            <FieldLabel htmlFor="sso-provider-client-secret" text={t('sso.fieldClientSecret')} requirement="required" />
            <input
              id="sso-provider-client-secret"
              data-testid="sso-provider-client-secret-input"
              type="password"
              value={provider.clientSecret || ''}
              onChange={(e) => setProvider({ ...provider, clientSecret: e.target.value })}
            />
          </div>
          <div className="form-field">
            <FieldLabel htmlFor="sso-provider-redirect-uri" text={t('sso.fieldRedirectUri')} requirement="required" />
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
            <FieldLabel htmlFor="sso-provider-metadata-url" text={t('sso.fieldMetadataUrl')} requirement="required" />
            <input
              id="sso-provider-metadata-url"
              data-testid="sso-provider-metadata-url-input"
              value={provider.metadataUrl || ''}
              onChange={(e) => setProvider({ ...provider, metadataUrl: e.target.value })}
            />
          </div>
          <div className="form-field">
            <FieldLabel htmlFor="sso-provider-entity-id" text={t('sso.fieldEntityId')} requirement="optional" />
            <input
              id="sso-provider-entity-id"
              data-testid="sso-provider-entity-id-input"
              value={provider.entityId || ''}
              onChange={(e) => setProvider({ ...provider, entityId: e.target.value })}
            />
          </div>
          <div className="form-field">
            <FieldLabel htmlFor="sso-provider-acs-url" text={t('sso.fieldAcsUrl')} requirement="optional" />
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
            <FieldLabel htmlFor="sso-provider-host" text={t('sso.fieldHost')} requirement="required" />
            <input
              id="sso-provider-host"
              data-testid="sso-provider-host-input"
              value={provider.host || ''}
              onChange={(e) => setProvider({ ...provider, host: e.target.value })}
            />
          </div>
          <div className="form-field">
            <FieldLabel
              htmlFor="sso-provider-port"
              text={t('sso.fieldPort')}
              requirement="defaulted"
              flag={t('form.reqDefaultValue', { value: '389' })}
            />
            <input
              id="sso-provider-port"
              data-testid="sso-provider-port-input"
              type="number"
              value={provider.port || 389}
              onChange={(e) => setProvider({ ...provider, port: Number(e.target.value) })}
            />
          </div>
          <div className="form-field">
            <FieldLabel htmlFor="sso-provider-base-dn" text={t('sso.fieldBaseDn')} requirement="required" />
            <input
              id="sso-provider-base-dn"
              data-testid="sso-provider-base-dn-input"
              value={provider.baseDn || ''}
              onChange={(e) => setProvider({ ...provider, baseDn: e.target.value })}
            />
          </div>
        </>
      )}
    </div>
    </FormSection>

    <FormSection
      step={3}
      title={t('sso.sectionDefaults')}
      hint={t('sso.sectionDefaultsHint')}
      requirement="optional"
      testId="sso-section-defaults"
    >
    <div className="form-grid">
      <div className="form-field">
        <FieldLabel
          htmlFor="sso-provider-default-org"
          text={t('sso.fieldDefaultOrg')}
          requirement="optional"
        />
        <input
          id="sso-provider-default-org"
          data-testid="sso-provider-default-org-input"
          value={provider.defaultOrg || ''}
          onChange={(e) => setProvider({ ...provider, defaultOrg: e.target.value })}
        />
      </div>
      <div className="form-field">
        <FieldLabel
          htmlFor="sso-provider-jit-toggle"
          text={t('sso.fieldJit')}
          requirement="defaulted"
          flag={t('form.reqDefaultValue', { value: t('common.enabled') })}
        />
        <input
          id="sso-provider-jit-toggle"
          data-testid="sso-provider-jit-toggle"
          type="checkbox"
          checked={provider.allowAutoProvision !== false}
          onChange={(e) => setProvider({ ...provider, allowAutoProvision: e.target.checked })}
        />
      </div>
      <div className="form-field full">
        <FieldLabel
          htmlFor="sso-provider-mapping"
          text={t('sso.fieldMapping')}
          requirement="optional"
        />
        <textarea
          id="sso-provider-mapping"
          data-testid="sso-provider-mapping-input"
          value={provider.attributeMapping || ''}
          rows={3}
          onChange={(e) => setProvider({ ...provider, attributeMapping: e.target.value })}
          placeholder={t('sso.mappingPlaceholder')}
        />
      </div>
    </div>
    </FormSection>
    </>
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
  const { t } = useI18n();
  const [provider, setProvider] = useState<Partial<SSOProvider>>({ type: 'oidc' });
  const [id, setId] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    if (!/^[a-z0-9][a-z0-9-]{2,63}$/.test(id.trim())) {
      setError(t('sso.validationId'));
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
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('sso.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('sso.createTitle')} onClose={onClose} testId="create-sso-provider-dialog">
      <FormLegend testId="create-sso-legend" />
      <div className="form-grid">
        <div className="form-field">
          <FieldLabel
            htmlFor="sso-provider-id"
            text={t('sso.fieldProviderId')}
            requirement="required"
          />
          <input
            id="sso-provider-id"
            data-testid="sso-provider-id-input"
            value={id}
            maxLength={64}
            onChange={(e) => setId(e.target.value)}
            placeholder={t('sso.placeholderProviderId')}
          />
        </div>
      </div>
      <ProviderFields provider={provider} setProvider={setProvider} />
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <RequiredStatus missing={requiredProviderFields(t, id, provider)} />
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="sso-provider-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.creating') : t('common.create')}
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
  const { t } = useI18n();
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
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('sso.updateFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('sso.editTitle', { providerId: provider.providerId })} onClose={onClose} testId="edit-sso-provider-dialog">
      <ProviderFields provider={fields} setProvider={setFields} />
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <RequiredStatus
          missing={requiredProviderFields(t, provider.providerId, fields).filter(
            (entry) => entry !== t('sso.fieldProviderId'),
          )}
        />
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="sso-provider-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.saving') : t('common.save')}
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
  const { t } = useI18n();
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
        setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('sso.actionFailed', { action }));
      }
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={
        action === 'delete'
          ? t('sso.deleteTitle', { providerId: provider.providerId })
          : action === 'enable'
            ? t('sso.enableTitle', { providerId: provider.providerId })
            : t('sso.disableTitle', { providerId: provider.providerId })
      }
      onClose={onClose}
      testId="sso-provider-confirm-dialog"
    >
      {action === 'delete' ? (
        <p>{t('sso.deleteBody')}</p>
      ) : action === 'enable' ? (
        <p>{t('sso.enableBody')}</p>
      ) : (
        <p>{t('sso.disableBody')}</p>
      )}
      {blocked && (
        <div className="muted" data-testid="sso-provider-delete-blocked">
          {t('sso.blockedNote')}
        </div>
      )}
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          className={action === 'disable' || action === 'delete' ? 'danger' : ''}
          data-testid="sso-provider-confirm-ok"
          disabled={submitting}
          onClick={() => void submit()}
        >
          {submitting
            ? t('common.working')
            : action === 'delete'
              ? t('common.delete')
              : action === 'enable'
                ? t('common.enable')
                : t('common.disable')}
        </button>
      </div>
    </Dialog>
  );
}
