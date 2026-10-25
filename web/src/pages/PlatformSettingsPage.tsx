// Admin platform settings page: the installation-wide values an operator
// edits at runtime. Today it holds the data-plane (inference gateway)
// endpoint — the hostname users are told to call, and the optional
// in-cluster URL the platform itself uses. Control plane and data plane are
// separate gateways, so the address a tenant calls is a setting, not a
// build-time constant.
//
// Admin-surface only: it calls /api/v1/admin/platform-settings.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { ErrorBanner } from '../components';
import { useI18n } from '../i18n';

interface ResponseEnvelope {
  code: number;
  message: string;
}

interface PlatformSettings {
  inferenceGatewayHostname: string;
  inferenceGatewayScheme: string;
  inferenceGatewayInternalUrl: string;
  inferenceBaseUrl: string;
  updatedBy: string;
  updatedAt: string;
}

interface SettingsResponse {
  response: ResponseEnvelope;
  settings?: PlatformSettings;
}

const EMPTY: PlatformSettings = {
  inferenceGatewayHostname: '',
  inferenceGatewayScheme: 'https',
  inferenceGatewayInternalUrl: '',
  inferenceBaseUrl: '',
  updatedBy: '',
  updatedAt: '',
};

export default function PlatformSettingsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();

  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState('');
  const [permissionDenied, setPermissionDenied] = useState(false);

  const [hostname, setHostname] = useState('');
  const [scheme, setScheme] = useState('https');
  const [internalUrl, setInternalUrl] = useState('');
  const [meta, setMeta] = useState<PlatformSettings>(EMPTY);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<SettingsResponse>('/api/v1/admin/platform-settings', orgId);
      const s = data.settings || EMPTY;
      setHostname(s.inferenceGatewayHostname || '');
      setScheme(s.inferenceGatewayScheme || 'https');
      setInternalUrl(s.inferenceGatewayInternalUrl || '');
      setMeta(s);
      setPermissionDenied(false);
    } catch (e) {
      const code = (e as { code?: number } | undefined)?.code;
      if (code === 10036) {
        setPermissionDenied(true);
      } else {
        setError(e instanceof Error ? e.message : t('settings.loadFailed'));
      }
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
    setSaved('');
    try {
      const data = await api.put<SettingsResponse>('/api/v1/admin/platform-settings', orgId, {
        inferenceGatewayHostname: hostname.trim(),
        inferenceGatewayScheme: scheme,
        inferenceGatewayInternalUrl: internalUrl.trim(),
      });
      const s = data.settings || EMPTY;
      setHostname(s.inferenceGatewayHostname || '');
      setScheme(s.inferenceGatewayScheme || 'https');
      setInternalUrl(s.inferenceGatewayInternalUrl || '');
      setMeta(s);
      setSaved(t('settings.saved'));
    } catch (e) {
      setError(e instanceof Error ? e.message : t('settings.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  // The preview mirrors what the platform reports, so an operator sees the
  // exact string users will be handed before saving.
  const preview = hostname.trim()
    ? `${scheme}://${hostname.trim().replace(/\/+$/, '')}/v1`
    : '';

  if (permissionDenied) {
    return (
      <div className="page">
        <div className="page-header">
          <h1>{t('settings.title')}</h1>
        </div>
        <div className="empty" data-testid="settings-permission-denied">
          {t('settings.permissionDenied')}
        </div>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h1>{t('settings.title')}</h1>
          <div className="subtitle">{t('settings.subtitle')}</div>
        </div>
        <button className="secondary" data-testid="settings-reload" disabled={loading} onClick={() => void load()}>
          {t('common.retry')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}
      {saved && (
        <div className="success-banner" data-testid="settings-saved">
          {saved}
        </div>
      )}

      <section className="panel" data-testid="settings-inference-gateway">
        <h2>{t('settings.gatewayTitle')}</h2>
        <p className="hint" style={{ marginTop: 0 }}>
          {t('settings.gatewayBody')}
        </p>

        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : (
          <>
            <div className="form-row">
              <label htmlFor="settings-gateway-hostname">{t('settings.hostnameLabel')}</label>
              <input
                id="settings-gateway-hostname"
                data-testid="settings-gateway-hostname"
                value={hostname}
                placeholder="api.example.com"
                onChange={(e) => setHostname(e.target.value)}
              />
              <div className="hint">{t('settings.hostnameHint')}</div>
            </div>

            <div className="form-row">
              <label htmlFor="settings-gateway-scheme">{t('settings.schemeLabel')}</label>
              <select
                id="settings-gateway-scheme"
                data-testid="settings-gateway-scheme"
                value={scheme}
                onChange={(e) => setScheme(e.target.value)}
              >
                <option value="https">https</option>
                <option value="http">http</option>
              </select>
              <div className="hint">{t('settings.schemeHint')}</div>
            </div>

            <div className="form-row">
              <label htmlFor="settings-gateway-internal">{t('settings.internalLabel')}</label>
              <input
                id="settings-gateway-internal"
                data-testid="settings-gateway-internal"
                value={internalUrl}
                placeholder="http://10.43.253.124"
                onChange={(e) => setInternalUrl(e.target.value)}
              />
              <div className="hint">{t('settings.internalHint')}</div>
            </div>

            <div className="kv-grid" data-testid="settings-gateway-preview">
              <div>
                <span className="kv-label">{t('settings.previewLabel')}</span>
                <span className="mono">{preview || t('settings.previewEmpty')}</span>
              </div>
              <div>
                <span className="kv-label">{t('settings.savedValueLabel')}</span>
                <span className="mono">{meta.inferenceBaseUrl || t('settings.previewEmpty')}</span>
              </div>
              <div>
                <span className="kv-label">{t('settings.updatedByLabel')}</span>
                <span>{meta.updatedBy || '—'}</span>
              </div>
            </div>

            <button data-testid="settings-save" disabled={saving} onClick={() => void save()}>
              {saving ? t('common.saving') : t('common.save')}
            </button>
          </>
        )}
      </section>

      <section className="panel">
        <h2>{t('settings.planeTitle')}</h2>
        <div className="kv-grid">
          <div>
            <span className="kv-label">{t('settings.controlPlaneLabel')}</span>
            <span className="mono">{window.location.origin}</span>
          </div>
          <div>
            <span className="kv-label">{t('settings.dataPlaneLabel')}</span>
            <span className="mono">{preview || t('settings.previewEmpty')}</span>
          </div>
        </div>
        <p className="hint">{t('settings.planeBody')}</p>
      </section>
    </div>
  );
}
