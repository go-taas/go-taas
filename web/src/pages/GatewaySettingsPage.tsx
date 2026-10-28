// Admin gateway settings page: the data-plane (inference gateway) endpoint
// an operator edits at runtime — the hostname users are told to call, and
// the optional in-cluster URL the platform itself uses. Control plane and
// data plane are separate gateways, so the address a tenant calls is a
// setting, not a build-time constant.
//
// Admin-surface only: it calls /api/v1/admin/platform-settings (the API
// name is kept for compatibility; the page is about the gateway).

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
  // Hostnames the data-plane route actually serves right now. When the
  // saved hostname is missing here, the setting is not in effect.
  servedHostnames?: string[];
  // Installation-wide display preferences: every console renders timestamps
  // in this zone and amounts in this currency.
  timezone?: string;
  defaultLanguage?: string;
  currency?: string;
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
  servedHostnames: [],
  timezone: '',
  defaultLanguage: 'zh',
  currency: 'CNY',
};

// TIMEZONES are the zones the operator can pick. A curated list beats a free
// text field: an invalid IANA name is rejected by the server, and typing one
// from memory is a common mistake. "Empty" keeps each viewer's own zone.
const TIMEZONES = [
  '',
  'Asia/Shanghai',
  'Asia/Hong_Kong',
  'Asia/Tokyo',
  'Asia/Singapore',
  'UTC',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Los_Angeles',
];

// CURRENCIES are the display currencies the platform can price in.
const CURRENCIES = ['CNY', 'USD', 'EUR', 'HKD', 'JPY'];

export default function GatewaySettingsPage() {
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
  const [timezone, setTimezone] = useState('');
  const [language, setLanguage] = useState('zh');
  const [currency, setCurrency] = useState('CNY');
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
      setTimezone(s.timezone || '');
      setLanguage(s.defaultLanguage || 'zh');
      setCurrency(s.currency || 'CNY');
      setMeta(s);
      setPermissionDenied(false);
    } catch (e) {
      const code = (e as { code?: number } | undefined)?.code;
      // 10036 is a missing role; 10038 is a session from the other console
      // (for example a user-realm session opening an admin URL). Both mean
      // "sign in to this console as a platform administrator", which is more
      // useful than the raw error.
      if (code === 10036 || code === 10038) {
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
        timezone,
        defaultLanguage: language,
        currency,
      });
      const s = data.settings || EMPTY;
      setHostname(s.inferenceGatewayHostname || '');
      setScheme(s.inferenceGatewayScheme || 'https');
      setInternalUrl(s.inferenceGatewayInternalUrl || '');
      setTimezone(s.timezone || '');
      setLanguage(s.defaultLanguage || 'zh');
      setCurrency(s.currency || 'CNY');
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

  // A saved hostname is only real when the data-plane route serves it.
  const served = meta.servedHostnames || [];
  const savedHost = (meta.inferenceGatewayHostname || '').toLowerCase();
  const inEffect = savedHost !== '' && served.some((h) => h.toLowerCase() === savedHost);
  const notInEffect = savedHost !== '' && served.length > 0 && !inEffect;

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

            {/* A grid of wide, wrap-anywhere cells: these values are URLs,
                and a narrow cell silently truncates them into a wall of
                text. The preview is hidden when it equals the saved value,
                because two identical URLs side by side read as a bug. */}
            <div className="kv-grid settings-summary" data-testid="settings-gateway-preview">
              {preview && preview !== meta.inferenceBaseUrl && (
                <div>
                  <span className="kv-label">{t('settings.previewLabel')}</span>
                  <span className="mono settings-url">{preview}</span>
                </div>
              )}
              <div>
                <span className="kv-label">{t('settings.savedValueLabel')}</span>
                <span className="mono settings-url">{meta.inferenceBaseUrl || t('settings.previewEmpty')}</span>
              </div>
              <div>
                <span className="kv-label">{t('settings.servedLabel')}</span>
                <span className="mono settings-url">
                  {served.length > 0 ? served.join(', ') : t('settings.previewEmpty')}
                </span>
              </div>
              <div>
                <span className="kv-label">{t('settings.updatedByLabel')}</span>
                <span>{meta.updatedBy || '—'}</span>
              </div>
            </div>

            {notInEffect && (
              <div className="error" data-testid="settings-not-in-effect">
                {t('settings.notInEffect', { host: meta.inferenceGatewayHostname })}
              </div>
            )}

            <button data-testid="settings-save" disabled={saving} onClick={() => void save()}>
              {saving ? t('common.saving') : t('common.save')}
            </button>
          </>
        )}
      </section>

      <section className="panel" data-testid="settings-display">
        <h2>{t('settings.displayTitle')}</h2>
        <p className="hint" style={{ marginTop: 0 }}>
          {t('settings.displayBody')}
        </p>
        <div className="form-row">
          <label htmlFor="settings-timezone">{t('settings.timezoneLabel')}</label>
          <select
            id="settings-timezone"
            data-testid="settings-timezone"
            value={timezone}
            onChange={(e) => setTimezone(e.target.value)}
          >
            {TIMEZONES.map((zone) => (
              <option key={zone || 'browser'} value={zone}>
                {zone || t('settings.timezoneBrowser')}
              </option>
            ))}
          </select>
          <div className="hint">{t('settings.timezoneHint')}</div>
        </div>
        <div className="form-row">
          <label htmlFor="settings-language">{t('settings.languageLabel')}</label>
          <select
            id="settings-language"
            data-testid="settings-language"
            value={language}
            onChange={(e) => setLanguage(e.target.value)}
          >
            <option value="zh">{t('settings.languageZh')}</option>
            <option value="en">{t('settings.languageEn')}</option>
          </select>
          <div className="hint">{t('settings.languageHint')}</div>
        </div>
        <div className="form-row">
          <label htmlFor="settings-currency">{t('settings.currencyLabel')}</label>
          <select
            id="settings-currency"
            data-testid="settings-currency"
            value={currency}
            onChange={(e) => setCurrency(e.target.value)}
          >
            {CURRENCIES.map((code) => (
              <option key={code} value={code}>
                {code}
              </option>
            ))}
          </select>
          <div className="hint">{t('settings.currencyHint')}</div>
        </div>
        <button data-testid="settings-display-save" disabled={saving} onClick={() => void save()}>
          {saving ? t('common.saving') : t('common.save')}
        </button>
      </section>

      <section className="panel">
        <h2>{t('settings.planeTitle')}</h2>
        <div className="kv-grid settings-summary">
          <div>
            <span className="kv-label">{t('settings.controlPlaneLabel')}</span>
            <span className="mono settings-url">{window.location.origin}</span>
          </div>
          <div>
            <span className="kv-label">{t('settings.dataPlaneLabel')}</span>
            <span className="mono settings-url">{meta.inferenceBaseUrl || t('settings.previewEmpty')}</span>
          </div>
        </div>
        <p className="hint">{t('settings.planeBody')}</p>
      </section>
    </div>
  );
}
