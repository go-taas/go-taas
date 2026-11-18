// End-user custom login page (feature-22): a TaaS-branded
// username/password form that authenticates against a selected OIDC or
// LDAP provider via SSOPasswordLogin. The realm is derived from the
// authenticated role; the console stores the token under the
// realm-matching key and routes by the returned realm.

import { useState } from 'react';
import { useApi } from '../../surface';
import { setSessionToken, type Realm } from '../../api';
import { navigate } from '../../router';
import { realmCustomLoginPath, realmHome } from '../../surface-routes';
import { useI18n } from '../../i18n';
import ThemeToggle from '../../components/ThemeToggle';
import brandLogo from '../../assets/brand/logo.svg';

interface PasswordLoginResponse {
  sessionToken?: string;
  realm?: string;
  expiresAt?: string;
}

export default function UserCustomLoginPage({ providerId }: { providerId: string }) {
  const api = useApi();
  const { t } = useI18n();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const canSubmit = username.trim().length > 0 && password.length > 0 && !submitting;

  // errorCopy maps business codes to sentences (feature-22 FR6.3).
  const errorCopy = (code: number): string => {
    switch (code) {
      case 10022:
        return t('login.errorDisabled');
      case 10024:
        return t('login.errorBadCredentials');
      case 10025:
        return t('login.errorNoAccount');
      case 10027:
        return t('login.errorIncomplete');
      default:
        return t('login.errorIncomplete');
    }
  };

  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setError('');
    try {
      const data = await api.post<PasswordLoginResponse>(
        `/api/v1/auth/sso/${providerId}/login`,
        '',
        { username: username.trim(), password },
      );
      const token = data.sessionToken;
      const realm = (data.realm as Realm) || 'user';
      if (!token) {
        setError(t('login.errorIncomplete'));
        return;
      }
      // Store the token under the realm-matching key (feature-22 FR2.3).
      setSessionToken(realm, token);
      // Route by the returned realm (feature-22 FR2.2).
      navigate(realmHome(realm));
    } catch (e) {
      const code = e && (e as { code?: number }).code;
      setError(errorCopy(typeof code === 'number' ? code : 0));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="login" data-testid="custom-login-form">
      <ThemeToggle testId="custom-login-theme-toggle" className="floating-theme-toggle" />
      <div className="login-brand" aria-hidden="true">
        <img src={brandLogo} alt="" className="login-brand-logo" />
      </div>
      <a
        href={realmCustomLoginPath('user', providerId)}
        className="back-link"
        data-testid="custom-login-back"
        onClick={(e) => {
          e.preventDefault();
          navigate('/login');
        }}
      >
        ← {t('login.back')}
      </a>
      <h1>{t('login.signInWith', { name: providerId })}</h1>
      <p className="login-subtitle">{t('login.credentialsNote', { name: providerId })}</p>
      {error && (
        <div className="error" data-testid="custom-login-error" role="alert">
          {error}
        </div>
      )}
      <form
        className="custom-login-form"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <label className="field">
          <span>{t('login.username')}</span>
          <input
            type="text"
            autoComplete="username"
            data-testid="custom-login-username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            disabled={submitting}
          />
        </label>
        <label className="field">
          <span>{t('login.password')}</span>
          <input
            type="password"
            autoComplete="current-password"
            data-testid="custom-login-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={submitting}
          />
        </label>
        <button
          type="submit"
          className="primary"
          data-testid="custom-login-submit"
          disabled={!canSubmit}
        >
          {submitting ? t('login.signingIn') : t('login.submit')}
        </button>
      </form>
    </div>
  );
}