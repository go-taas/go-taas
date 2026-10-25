// "Authorized organizations" panel of the model detail page (admin
// surface). Implements docs/design/model-authorization.md FR1, FR2, FR5.1
// (AC1, AC2, AC3, AC10, AC12): it lists the model's grants, grants an
// organization, and revokes a grant. An empty grant list means the model
// is open to every organization (the default-allow rule).

import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  api,
  ApiError,
  formatTime,
  type ListModelAuthorizationsResponse,
  type ModelAuthorization,
  type OrganizationSummary,
} from '../api';
import { ErrorBanner, usePolling } from '../components';
import { useI18n } from '../i18n';

interface ListOrganizationsResponse {
  response: { code: number; message: string };
  organizations?: OrganizationSummary[];
}

// PANEL_POLL_MS refreshes the panel while it is visible so a grant made in
// another session shows up without a reload.
const PANEL_POLL_MS = 60_000;

export default function ModelAuthorizationsPanel({
  orgId,
  modelId,
  onChanged,
}: {
  orgId: string;
  modelId: string;
  onChanged?: () => void;
}) {  const { t } = useI18n();

  const [grants, setGrants] = useState<ModelAuthorization[]>([]);
  const [organizations, setOrganizations] = useState<OrganizationSummary[]>([]);
  const [selectedOrg, setSelectedOrg] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      const data = await api.get<ListModelAuthorizationsResponse>(
        `/api/v1/admin/models/${modelId}/authorizations?page.limit=100`,
        orgId,
      );
      setGrants(data.authorizations || []);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load authorizations');
    } finally {
      setLoading(false);
    }
  }, [modelId, orgId]);

  useEffect(() => {
    void load();
  }, [load]);

  usePolling(() => void load(), PANEL_POLL_MS, true);

  // The grant selector lists the platform directory, so an operator can
  // grant an organization they do not belong to.
  useEffect(() => {
    api
      .get<ListOrganizationsResponse>(
        '/api/v1/admin/tenancy/organizations?page.limit=100',
        orgId,
      )
      .then((data) => setOrganizations(data.organizations || []))
      .catch(() => setOrganizations([]));
  }, [orgId]);

// Only organizations without a grant are offered: re-granting is a no-op
	// server-side, but offering it would be misleading.
	const selectable = useMemo(
		() =>
			organizations.filter(
				(o) => !grants.some((g) => g.organizationId === o.organizationId),
			),
		[organizations, grants],
	);

	useEffect(() => {
		setSelectedOrg((prev) => {
			if (selectable.length === 0) return '';
			return selectable.some((o) => o.organizationId === prev)
				? prev
				: selectable[0].organizationId;
		});
	}, [selectable]);

  const grant = async () => {
    if (!selectedOrg) return;
    setBusy(true);
    setError('');
    try {
      await api.post(`/api/v1/admin/models/${modelId}:grant`, orgId, {
        organizationId: selectedOrg,
      });
      await load();
      onChanged?.();
    } catch (e) {
      setError(
        e instanceof ApiError
          ? `Failed to grant access: ${e.message} (code ${e.code})`
          : 'failed to grant access',
      );
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (organizationId: string) => {
    setBusy(true);
    setError('');
    try {
      await api.post(`/api/v1/admin/models/${modelId}:revoke`, orgId, {
        organizationId,
      });
      await load();
      onChanged?.();
    } catch (e) {
      setError(
        e instanceof ApiError
          ? `Failed to revoke access: ${e.message} (code ${e.code})`
          : 'failed to revoke access',
      );
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="panel" data-testid="model-auth-panel">
      <h3 style={{ marginTop: 0 }}>{t('modelAuth.title')}</h3>
      <div className="subtitle" style={{ marginBottom: 12 }}>
        {t('modelAuth.subtitle')}
      </div>

      {error && <ErrorBanner message={error} />}

      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : grants.length === 0 ? (
        <div className="empty-state" data-testid="model-auth-empty">
          {t('modelAuth.empty')}
        </div>
      ) : (
        <table className="data" data-testid="model-auth-table">
          <thead>
            <tr>
              <th>{t('modelAuth.colOrganization')}</th>
              <th>{t('modelAuth.colGrantedBy')}</th>
              <th>{t('modelAuth.colGrantedAt')}</th>
              <th>{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {grants.map((g) => (
              <tr
                key={g.organizationId}
                data-testid={`model-auth-row-${g.organizationId}`}
              >
                <td className="mono">{g.organizationId}</td>
                <td className="muted">{g.grantedByDisplayName || g.grantedBy}</td>
                <td>{formatTime(g.createdAt)}</td>
                <td>
                  <button
                    className="link danger"
                    data-testid={`model-auth-revoke-${g.organizationId}`}
                    disabled={busy}
                    onClick={() => void revoke(g.organizationId)}
                  >
                    {t('modelAuth.revoke')}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <div className="toolbar" style={{ marginTop: 14 }}>
        <select
          data-testid="model-auth-org-select"
          value={selectedOrg}
          disabled={busy || selectable.length === 0}
          onChange={(e) => setSelectedOrg(e.target.value)}
        >
          {selectable.length === 0 && (
            <option value="">
              {organizations.length === 0
                ? t('modelAuth.noOrganizations')
                : t('modelAuth.everyAuthorized')}
            </option>
          )}
          {selectable.map((o) => (
            <option key={o.organizationId} value={o.organizationId}>
              {o.organizationId}
            </option>
          ))}
        </select>
        <button
          data-testid="model-auth-grant"
          disabled={busy || !selectedOrg}
          onClick={() => void grant()}
        >
          {busy ? t('common.working') : t('modelAuth.grant')}
        </button>
      </div>
    </div>
  );
}
