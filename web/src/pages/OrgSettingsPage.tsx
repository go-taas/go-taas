// OrgSettingsPage is the organization console's metadata page: the org
// admin reads and edits the active organization's display name and
// description (requirement 3, "modify organization metadata").

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type OrganizationSummary } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { ErrorBanner } from '../components';

interface GetOrgResponse {
  response: { code: number; message: string };
  organization: OrganizationSummary;
}

export default function OrgSettingsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [displayName, setDisplayName] = useState('');
  const [description, setDescription] = useState('');
  const [state, setState] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [info, setInfo] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<GetOrgResponse>(
        `/api/v1/admin/tenancy/organizations/${orgId}`,
        orgId,
      );
      setDisplayName(data.organization?.displayName || '');
      setDescription(data.organization?.description || '');
      setState(data.organization?.state || '');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('orgSettings.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const save = async () => {
    setSaving(true);
    setError('');
    setInfo('');
    try {
      await api.patch(`/api/v1/admin/tenancy/organizations/${orgId}`, orgId, {
        displayName: displayName.trim(),
        description: description.trim(),
      });
      setInfo(t('orgSettings.saved'));
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('orgSettings.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="page" data-testid="org-settings-page">
      <h1>{t('orgSettings.title')}</h1>
      <div className="subtitle mono">{orgId}</div>
      {error && <ErrorBanner message={error} />}
      {info && <div className="success" data-testid="org-settings-info">{info}</div>}

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : (
          <div className="form-grid">
            <label>
              {t('orgSettings.displayName')}
              <input
                data-testid="org-settings-name"
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
              />
            </label>
            <label className="full">
              {t('orgSettings.description')}
              <input
                data-testid="org-settings-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </label>
            <label>
              {t('orgSettings.state')}
              <input value={state} disabled />
            </label>
          </div>
        )}
        <div className="dialog-actions">
          <button
            data-testid="org-settings-save"
            disabled={saving || loading || !displayName.trim()}
            onClick={() => void save()}
          >
            {saving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </div>
    </div>
  );
}
