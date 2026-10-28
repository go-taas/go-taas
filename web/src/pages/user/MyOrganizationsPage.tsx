// MyOrganizationsPage is the end-user console "My organizations" page:
// the caller's own organizations and roles.

import { useCallback, useEffect, useState } from 'react';
import { ErrorBanner, StateBadge } from '../../components';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import type { MyOrganization } from '../../api';

export default function MyOrganizationsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [orgs, setOrgs] = useState<MyOrganization[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{ organizations: MyOrganization[] }>(
        '/api/v1/tenancy/my-organizations',
        orgId,
      );
      setOrgs(data.organizations || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('myorgs.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div className="page" data-testid="my-organizations-page">
      <h1>{t('myorgs.title')}</h1>
      {error && <ErrorBanner message={error} />}
      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : orgs.length === 0 ? (
          <div className="empty-state" data-testid="my-orgs-empty">
            {t('myorgs.empty')}
          </div>
        ) : (
          <table className="data" data-testid="my-orgs-table">
            <thead>
              <tr>
                <th>{t('myorgs.colOrganization')}</th>
                <th>{t('myorgs.colRole')}</th>
              </tr>
            </thead>
            <tbody>
              {orgs.map((org) => (
                <tr key={org.organizationId} data-testid={`my-org-row-${org.organizationId}`}>
                  <td>
                    <strong>{org.displayName || org.organizationId}</strong>
                    <div className="muted small mono">{org.organizationId}</div>
                  </td>
                  <td>
                    <StateBadge state={org.role} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}