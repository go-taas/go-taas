// Register page: create a local account (username/password) and sign in.
// The account is stored with an Argon2id-hashed password; on success the
// returned session routes to the realm home.

import { useState } from 'react';
import { useApi } from '../surface';
import { setSessionToken, type Realm } from '../api';
import { navigate } from '../router';
import { realmHome } from '../surface-routes';
import { useI18n } from '../i18n';
import brandLogo from '../assets/brand/logo.svg';

interface RegisterResponse {
  userId?: string;
}

interface LoginResponse {
  sessionToken?: string;
  realm?: string;
  expiresAt?: string;
}

export default function RegisterPage() {
  const api = useApi();
  const { t } = useI18n();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [email, setEmail] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  const canSubmit = username.trim().length > 0 && password.length >= 8 && !submitting;

  const errorCopy = (code: number): string => {
    switch (code) {
      case 10004:
        return t('register.errorExists');
      case 10014:
        return t('register.errorPasswordPolicy');
      default:
        return t('register.errorFailed');
    }
  };

  const submit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setError('');
    try {
      // Register the local account.
      await api.post<RegisterResponse>('/api/v1/auth/users', '', {
        username: username.trim(),
        password,
        email: email.trim(),
      });
      // Sign in with the new account.
      const data = await api.post<LoginResponse>('/api/v1/auth/login', '', {
        username: username.trim(),
        password,
      });
      const token = data.sessionToken;
      const realm = (data.realm as Realm) || 'user';
      if (!token) {
        setError(t('register.errorFailed'));
        return;
      }
      setSessionToken(realm, token);
      navigate(realmHome(realm));
    } catch (e) {
      const code = (e as { code?: number }).code;
      setError(code ? errorCopy(code) : t('register.errorFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="login" data-testid="register-page">
      <div className="login-brand" aria-hidden="true">
        <img src={brandLogo} alt="" className="login-brand-logo" />
      </div>
      <h1>{t('register.title')}</h1>
      <p className="login-subtitle">{t('register.subtitle')}</p>
      {error && <div className="error" data-testid="register-error">{error}</div>}
      <form className="custom-login-form" onSubmit={(e) => { e.preventDefault(); void submit(); }}>
        <div className="field">
          <label htmlFor="register-username">{t('register.username')}</label>
          <input
            id="register-username"
            data-testid="register-username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
          />
        </div>
        <div className="field">
          <label htmlFor="register-email">{t('register.email')}</label>
          <input
            id="register-email"
            data-testid="register-email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            autoComplete="email"
          />
        </div>
        <div className="field">
          <label htmlFor="register-password">{t('register.password')}</label>
          <input
            id="register-password"
            data-testid="register-password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
          />
        </div>
        <button
          type="submit"
          data-testid="register-submit"
          disabled={!canSubmit}
        >
          {submitting ? t('common.working') : t('register.submit')}
        </button>
      </form>
    </div>
  );
}