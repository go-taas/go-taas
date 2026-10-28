// Register page: create a local account (username/password) and sign in.
// The account is stored with an Argon2id-hashed password; on success the
// returned session routes to the realm home. When email verification is
// enabled, an email verification code is required. The resend countdown
// persists across page refreshes via localStorage.

import { useState } from 'react';
import { Eye, EyeSlash } from '@phosphor-icons/react';
import { useApi } from '../surface';
import { setSessionToken, type Realm } from '../api';
import { navigate } from '../router';
import { realmHome } from '../surface-routes';
import { useI18n } from '../i18n';
import brandLogo from '../assets/brand/logo.svg';
import { useResendCooldown, windowSeconds } from '../hooks/useResendCooldown';

interface RegisterResponse {
  userId?: string;
}

interface LoginResponse {
  sessionToken?: string;
  realm?: string;
  expiresAt?: string;
}

interface SendCodeResponse {
  resendAfterSeconds?: string;
}

// The localStorage key for the register resend cooldown deadline.
const COOLDOWN_KEY = 'go-taas.register.cooldown-until';

// passwordPolicyOK mirrors the server policy: at least 8 characters and
// at least three of uppercase, lowercase, digit, special.
export function passwordPolicyOK(pw: string): boolean {
  if (pw.length < 8) return false;
  const classes = [/[A-Z]/, /[a-z]/, /[0-9]/, /[^A-Za-z0-9]/].filter((re) => re.test(pw)).length;
  return classes >= 3;
}

export default function RegisterPage() {
  const api = useApi();
  const { t } = useI18n();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [email, setEmail] = useState('');
  const [emailCode, setEmailCode] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [sendingCode, setSendingCode] = useState(false);
  const [showPassword, setShowPassword] = useState(false);
  // 1-minute resend countdown, persisted to localStorage.
  const cooldown = useResendCooldown(COOLDOWN_KEY);

  const passwordsMatch = password === confirmPassword;
  const canSubmit =
    username.trim().length > 0 &&
    passwordPolicyOK(password) &&
    passwordsMatch &&
    email.trim().length > 0 &&
    emailCode.trim().length > 0 &&
    !submitting;

  const errorCopy = (code: number): string => {
    switch (code) {
      case 10004:
        return t('register.errorExists');
      case 10014:
        return t('register.errorPasswordPolicy');
      case 10002:
        return t('register.errorBadCode');
      default:
        return t('register.errorFailed');
    }
  };

  const sendCode = async () => {
    if (!email.trim() || sendingCode || !cooldown.ready) return;
    setSendingCode(true);
    setError('');
    try {
      const data = await api.post<SendCodeResponse>('/api/v1/auth/email-code', '', {
        email: email.trim(),
        purpose: 'register',
      });
      cooldown.start(windowSeconds(data));
    } catch (e) {
      const code = (e as { code?: number }).code;
      setError(code === 10037 ? t('register.errorCooldown') : t('register.errorSendCode'));
    } finally {
      setSendingCode(false);
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
        emailCode: emailCode.trim(),
      });
      // Sign in with the new account.
      const data = await api.post<LoginResponse>('/api/v1/auth/login', '', {
        username: username.trim(),
        password,
        emailCode: emailCode.trim(),
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
          <div className="code-row">
            <input
              id="register-email"
              data-testid="register-email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              autoComplete="email"
            />
            <button
              type="button"
              className="secondary"
              data-testid="register-send-code"
              disabled={!email.trim() || sendingCode || !cooldown.ready}
              onClick={() => void sendCode()}
            >
              {!cooldown.ready
                ? t('register.resendIn', { seconds: cooldown.remaining })
                : sendingCode
                  ? t('common.working')
                  : t('register.sendCode')}
            </button>
          </div>
        </div>
        <div className="field">
          <label htmlFor="register-code">{t('register.code')}</label>
          <input
            id="register-code"
            data-testid="register-code"
            value={emailCode}
            onChange={(e) => setEmailCode(e.target.value)}
            autoComplete="one-time-code"
            inputMode="numeric"
          />
        </div>
        <div className="field">
          <label htmlFor="register-password">{t('register.password')}</label>
          <div className="password-row">
            <input
              id="register-password"
              data-testid="register-password"
              type={showPassword ? 'text' : 'password'}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
            />
            <button
              type="button"
              className="password-toggle"
              data-testid="register-password-toggle"
              onClick={() => setShowPassword((v) => !v)}
              aria-label={showPassword ? t('common.hidePassword') : t('common.showPassword')}
            >
              {showPassword ? <EyeSlash size={18} /> : <Eye size={18} />}
            </button>
          </div>
        </div>
        <div className="field">
          <label htmlFor="register-confirm-password">{t('register.confirmPassword')}</label>
          <input
            id="register-confirm-password"
            data-testid="register-confirm-password"
            type={showPassword ? 'text' : 'password'}
            value={confirmPassword}
            onChange={(e) => setConfirmPassword(e.target.value)}
            autoComplete="new-password"
          />
          {password.length > 0 && !passwordPolicyOK(password) && (
            <div className="field-error" data-testid="register-password-policy">
              {t('register.passwordPolicy')}
            </div>
          )}
          {confirmPassword.length > 0 && !passwordsMatch && (
            <div className="field-error" data-testid="register-password-mismatch">
              {t('register.passwordMismatch')}
            </div>
          )}
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
