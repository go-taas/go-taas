// System preferences: one time zone, one default language and one display
// currency for the whole installation.
//
// This page used to live inside the gateway-settings page, which mixed two
// unrelated concerns: an address users are told to call, and the units every
// console renders. They are now two pages, and two calls: saving here writes
// only these three values (UpdatePlatformDisplaySettings), so it can never
// blank the gateway hostname it does not show.
//
// The currency is the one every console prints. It is separate from the
// platform's settlement currency (the billing configuration), so the page
// shows both: an installation that prices in USD and displays in CNY is
// almost always a mistake, and an operator should see it here rather than on
// a customer's invoice.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { ErrorBanner } from '../components';
import { useI18n } from '../i18n';
import { setDisplayPreferences } from '../display';

interface ResponseEnvelope {
  code: number;
  message: string;
}

interface DisplaySettings {
  timezone?: string;
  defaultLanguage?: string;
  currency?: string;
  billingCurrency?: string;
}

interface SettingsResponse extends ResponseEnvelope {
  timezone?: string;
  defaultLanguage?: string;
  currency?: string;
  billingCurrency?: string;
}

// TIMEZONES are the zones the operator can pick. A curated list beats a free
// text field: an invalid IANA name is rejected by the server, and typing one
// from memory is a common mistake. The empty entry keeps each viewer's zone.
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

export default function DisplaySettingsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();

  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState('');
  const [permissionDenied, setPermissionDenied] = useState(false);

  const [timezone, setTimezone] = useState('');
  const [language, setLanguage] = useState('zh');
  const [currency, setCurrency] = useState('CNY');
  const [billingCurrency, setBillingCurrency] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<SettingsResponse & { response?: ResponseEnvelope }>(
        '/api/v1/admin/platform-display-settings',
        orgId,
      );
      const s: DisplaySettings = (data as DisplaySettings) || {};
      setTimezone(s.timezone || '');
      setLanguage(s.defaultLanguage || 'zh');
      setCurrency(s.currency || 'CNY');
      setBillingCurrency(s.billingCurrency || '');
      setPermissionDenied(false);
    } catch (e) {
      const code = (e as { code?: number } | undefined)?.code;
      if (code === 10036 || code === 10038) {
        setPermissionDenied(true);
      } else {
        setError(e instanceof Error ? e.message : t('displaySettings.loadFailed'));
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
      const data = await api.put<SettingsResponse>(
        '/api/v1/admin/platform-display-settings',
        orgId,
        { timezone, defaultLanguage: language, currency },
      );
      setTimezone(data.timezone || '');
      setLanguage(data.defaultLanguage || 'zh');
      setCurrency(data.currency || 'CNY');
      setBillingCurrency(data.billingCurrency || '');
      // Apply the new units immediately: the provider only reads them once
      // per boot, and an operator who just saved expects to see the change.
      setDisplayPreferences(data.timezone || '', data.currency || 'CNY');
      setSaved(t('displaySettings.saved'));
    } catch (e) {
      setError(e instanceof Error ? e.message : t('displaySettings.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  if (permissionDenied) {
    return (
      <div className="page">
        <div className="page-header">
          <h1>{t('displaySettings.title')}</h1>
        </div>
        <div className="empty" data-testid="display-settings-permission-denied">
          {t('displaySettings.permissionDenied')}
        </div>
      </div>
    );
  }

  const mismatch =
    billingCurrency !== '' && currency !== '' && billingCurrency.toUpperCase() !== currency.toUpperCase();

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h1>{t('displaySettings.title')}</h1>
          <div className="subtitle">{t('displaySettings.subtitle')}</div>
        </div>
        <button
          className="secondary"
          data-testid="display-settings-reload"
          disabled={loading}
          onClick={() => void load()}
        >
          {t('common.retry')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}
      {saved && (
        <div className="success-banner" data-testid="display-settings-saved">
          {saved}
        </div>
      )}

      <section className="panel" data-testid="settings-display">
        <h2>{t('displaySettings.title')}</h2>
        <p className="hint" style={{ marginTop: 0 }}>
          {t('displaySettings.body')}
        </p>
        <div className="form-row">
          <label htmlFor="display-timezone">{t('displaySettings.timezoneLabel')}</label>
          <select
            id="display-timezone"
            data-testid="settings-timezone"
            value={timezone}
            disabled={loading}
            onChange={(e) => setTimezone(e.target.value)}
          >
            {TIMEZONES.map((zone) => (
              <option key={zone || 'browser'} value={zone}>
                {zone || t('displaySettings.timezoneBrowser')}
              </option>
            ))}
          </select>
          <div className="hint">{t('displaySettings.timezoneHint')}</div>
        </div>
        <div className="form-row">
          <label htmlFor="display-language">{t('displaySettings.languageLabel')}</label>
          <select
            id="display-language"
            data-testid="settings-language"
            value={language}
            disabled={loading}
            onChange={(e) => setLanguage(e.target.value)}
          >
            <option value="zh">{t('displaySettings.languageZh')}</option>
            <option value="en">{t('displaySettings.languageEn')}</option>
          </select>
          <div className="hint">{t('displaySettings.languageHint')}</div>
        </div>
        <div className="form-row">
          <label htmlFor="display-currency">{t('displaySettings.currencyLabel')}</label>
          <select
            id="display-currency"
            data-testid="settings-currency"
            value={currency}
            disabled={loading}
            onChange={(e) => setCurrency(e.target.value)}
          >
            {CURRENCIES.map((code) => (
              <option key={code} value={code}>
                {code}
              </option>
            ))}
          </select>
          <div className="hint">{t('displaySettings.currencyHint')}</div>
          {billingCurrency && (
            <div className="hint" data-testid="settings-billing-currency">
              {t('displaySettings.billingCurrencyHint', { code: billingCurrency })}
            </div>
          )}
          {mismatch && (
            <div className="error" data-testid="settings-currency-mismatch">
              {t('displaySettings.currencyMismatch', { display: currency, billing: billingCurrency })}
            </div>
          )}
        </div>
        <button data-testid="display-settings-save" disabled={saving || loading} onClick={() => void save()}>
          {saving ? t('common.saving') : t('common.save')}
        </button>
      </section>
    </div>
  );
}
