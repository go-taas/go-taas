// Identity bindings page (feature #7): the binding directory with
// pre-assign and delete actions.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type IdentityBinding, type PageMeta, type SSOProvider } from '../api';
import { useOrg } from '../org';
import { Dialog, ErrorBanner, Pagination } from '../components';
import { useI18n } from '../i18n';

interface ListResponse {
  response: { code: number; message: string };
  bindings: IdentityBinding[];
  pageMeta?: PageMeta;
}

interface ProviderListResponse {
  response: { code: number; message: string };
  providers: SSOProvider[];
}

const PAGE_SIZE = 20;

export default function IdentityBindingsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [bindings, setBindings] = useState<IdentityBinding[]>([]);
  const [providers, setProviders] = useState<SSOProvider[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<IdentityBinding | null>(null);

  const loadProviders = useCallback(async () => {
    try {
      const data = await api.get<ProviderListResponse>(
        '/api/v1/admin/auth/sso/providers?page.limit=100',
        orgId,
      );
      setProviders(data.providers || []);
    } catch {
      // The provider dropdown falls back to empty.
    }
  }, [orgId]);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams({
        [`page.offset`]: String(offset),
        [`page.limit`]: String(PAGE_SIZE),
      });
      const data = await api.get<ListResponse>(
        `/api/v1/admin/auth/identity-bindings?${params.toString()}`,
        orgId,
      );
      setBindings(data.bindings || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('bindings.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, t]);

  useEffect(() => {
    void loadProviders();
  }, [loadProviders]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('bindings.title')}</h1>
          <div className="subtitle">
            {t('bindings.subtitle')}
          </div>
        </div>
        <button data-testid="create-identity-binding" onClick={() => setCreateOpen(true)}>
          {t('bindings.preassign')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : bindings.length === 0 ? (
          <div className="empty-state" data-testid="identity-bindings-empty">
            {t('bindings.empty')}
          </div>
        ) : (
          <table className="data" data-testid="identity-bindings-table">
            <thead>
              <tr>
                <th>{t('bindings.colProvider')}</th>
                <th>{t('bindings.colSubject')}</th>
                <th>{t('bindings.colUser')}</th>
                <th>{t('bindings.colCreated')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {bindings.map((b) => (
                <tr key={b.bindingId} data-testid={`identity-binding-row-${b.bindingId}`}>
                  <td className="mono">{b.providerId}</td>
                  <td className="mono">{b.externalSubject}</td>
                  <td>
                    <strong>{b.userDisplayName || b.userId}</strong>
                    <div className="muted small mono">{b.userId}</div>
                  </td>
                  <td>{new Date(Number(b.createdAt) * 1000).toLocaleString()}</td>
                  <td>
                    <button
                      className="link danger"
                      data-testid={`identity-binding-delete-${b.bindingId}`}
                      onClick={() => setDeleteTarget(b)}
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
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        )}
      </div>

      {createOpen && (
        <CreateBindingDialog
          orgId={orgId}
          providers={providers}
          onClose={() => setCreateOpen(false)}
          onDone={() => {
            setCreateOpen(false);
            void load();
          }}
        />
      )}

      {deleteTarget && (
        <DeleteBindingDialog
          orgId={orgId}
          binding={deleteTarget}
          onClose={() => setDeleteTarget(null)}
          onDone={() => {
            setDeleteTarget(null);
            void load();
          }}
        />
      )}
    </div>
  );
}

function CreateBindingDialog({
  orgId,
  providers,
  onClose,
  onDone,
}: {
  orgId: string;
  providers: SSOProvider[];
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [providerId, setProviderId] = useState('');
  const [subject, setSubject] = useState('');
  const [userId, setUserId] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    if (!providerId || !subject.trim() || !userId.trim()) {
      setError(t('bindings.validationRequired'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.post('/api/v1/admin/auth/identity-bindings', orgId, {
        providerId,
        externalSubject: subject.trim(),
        userId: userId.trim(),
      });
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('bindings.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('bindings.createTitle')} onClose={onClose} testId="create-identity-binding-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="identity-binding-provider">{t('bindings.fieldProvider')}</label>
          <select
            id="identity-binding-provider"
            data-testid="identity-binding-provider-select"
            value={providerId}
            onChange={(e) => setProviderId(e.target.value)}
          >
            <option value="">{t('bindings.placeholderProvider')}</option>
            {providers.map((p) => (
              <option
                key={p.providerId}
                value={p.providerId}
                data-testid={`identity-binding-provider-option-${p.providerId}`}
              >
                {p.providerId}
              </option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="identity-binding-subject">{t('bindings.fieldSubject')}</label>
          <input
            id="identity-binding-subject"
            data-testid="identity-binding-subject-input"
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
            placeholder={t('bindings.placeholderSubject')}
          />
        </div>
        <div className="form-field">
          <label htmlFor="identity-binding-user">{t('bindings.fieldUserId')}</label>
          <input
            id="identity-binding-user"
            data-testid="identity-binding-user-input"
            value={userId}
            onChange={(e) => setUserId(e.target.value)}
            placeholder={t('bindings.placeholderUserId')}
          />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="identity-binding-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.creating') : t('common.create')}
        </button>
      </div>
    </Dialog>
  );
}

function DeleteBindingDialog({
  orgId,
  binding,
  onClose,
  onDone,
}: {
  orgId: string;
  binding: IdentityBinding;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    setSubmitting(true);
    setError('');
    try {
      await api.del(`/api/v1/admin/auth/identity-bindings/${binding.bindingId}`, orgId);
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('bindings.deleteFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('bindings.deleteTitle')} onClose={onClose} testId="identity-binding-confirm-dialog">
      <p>
        {t('bindings.deleteBody')}
      </p>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button className="danger" data-testid="identity-binding-confirm-ok" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.deleting') : t('common.delete')}
        </button>
      </div>
    </Dialog>
  );
}
