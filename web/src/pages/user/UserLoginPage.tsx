// End-user login page (feature-17/22): realm-pinned sign-in against the
// user surface's SSO providers, plus a local username/password form.

import { useEffect, useState } from 'react';
import { Eye, EyeSlash } from '@phosphor-icons/react';
import { useApi } from '../../surface';
import { navigate } from '../../router';
import { setSessionToken, type Realm } from '../../api';
import { realmCustomLoginPath, realmHome } from '../../surface-routes';
import { useI18n } from '../../i18n';
import brandLogo from '../../assets/brand/logo.svg';
import { useResendCooldown, windowSeconds } from '../../hooks/useResendCooldown';

interface PublicProvider {
  providerId: string;
  type: string;
  displayName: string;
  passwordLogin: boolean;
}

interface LoginResponse {
  sessionToken?: string;
  realm?: string;
  expiresAt?: string;
}

interface SendCodeResponse {
  resendAfterSeconds?: string;
}

interface CaptchaResponse {
  captchaId?: string;
  image?: string;
  expiresAt?: string;
}

// The localStorage key for the login resend cooldown deadline.
const COOLDOWN_KEY = 'go-taas.login.cooldown-until';

export default function UserLoginPage() {
  const api = useApi();
  const { t } = useI18n();
  const [providers, setProviders] = useState<PublicProvider[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [email, setEmail] = useState('');
  const [emailCode, setEmailCode] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [sendingCode, setSendingCode] = useState(false);
  const [showPassword, setShowPassword] = useState(false);
  const [captchaId, setCaptchaId] = useState('');
  const [captchaImage, setCaptchaImage] = useState('');
  const [captchaCode, setCaptchaCode] = useState('');
  // 1-minute resend countdown, persisted to localStorage.
  const cooldown = useResendCooldown(COOLDOWN_KEY);

  // loadCaptcha asks the server whether a CAPTCHA is needed for the typed
  // account and fetches a fresh puzzle. Only platform administrators are
  // challenged; an empty image means the field stays hidden.
  const loadCaptcha = async (account: string) => {
    try {
      const data = await api.get<CaptchaResponse>(
        `/api/v1/auth/captcha?username=${encodeURIComponent(account.trim())}`,
        '',
      );
      setCaptchaId(data.captchaId || '');
      setCaptchaImage(data.image || '');
      setCaptchaCode('');
    } catch {
      setCaptchaId('');
      setCaptchaImage('');
    }
  };

  // refreshCaptcha reloads the puzzle for the account already typed.
  const refreshCaptcha = () => loadCaptcha(username);

  useEffect(() => {
    api
      .get<{ providers?: PublicProvider[] }>('/api/v1/auth/sso/providers', '')
      .then((data) => setProviders(data.providers || []))
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load providers'))
      .finally(() => setLoading(false));
  }, [api]);

  // The challenge depends on the account: switching between an ordinary
  // user and an administrator adds or removes the CAPTCHA field. The
  // debounce keeps a typed username from issuing one request per keystroke.
  useEffect(() => {
    const timer = setTimeout(() => {
      void loadCaptcha(username);
    }, 350);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [username, api]);

  const signIn = async (providerId: string, type: string, passwordLogin: boolean) => {
    // OIDC/LDAP providers that support the password grant → the TaaS
    // custom login page (feature-22 AD7); SAML and password-less
    // providers (e.g. GitHub OAuth) → the redirect flow.
    if ((type === 'oidc' || type === 'ldap') && passwordLogin) {
      navigate(realmCustomLoginPath('user', providerId));
      return;
    }
    try {
      const data = await api.get<{ redirectUrl?: string }>(
        `/api/v1/auth/sso/${providerId}/authorize`,
        '',
      );
      if (data.redirectUrl) {
        window.location.href = data.redirectUrl;
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'sign-in failed');
    }
  };

  const sendCode = async () => {
    if (!email.trim() || sendingCode || !cooldown.ready) return;
    setSendingCode(true);
    setError('');
    try {
      const data = await api.post<SendCodeResponse>('/api/v1/auth/email-code', '', {
        email: email.trim(),
        purpose: 'login',
      });
      cooldown.start(windowSeconds(data));
    } catch (e) {
      const code = (e as { code?: number }).code;
      setError(code === 10037 ? t('login.errorCooldown') : t('login.errorSendCode'));
    } finally {
      setSendingCode(false);
    }
  };

  // The bootstrap `admin` account signs in with a password only: no
  // email verification code (the server exempts platform admins). Hide
  // the email and code fields and skip their validation for it.
  const requiresEmailCode = username.trim().toLowerCase() !== 'admin';
  const canLogin =
    !!username.trim() &&
    !!password &&
    (requiresEmailCode ? !!email.trim() && !!emailCode.trim() : true) &&
    (!captchaImage || !!captchaCode.trim());

  const localLogin = async () => {
    if (!canLogin || submitting) return;
    setSubmitting(true);
    setError('');
    try {
      const data = await api.post<LoginResponse>('/api/v1/auth/login', '', {
        username: username.trim(),
        password,
        emailCode: requiresEmailCode ? emailCode.trim() : '',
        captchaId,
        captchaCode: captchaCode.trim(),
      });
      const token = data.sessionToken;
      const realm = (data.realm as Realm) || 'user';
      if (!token) {
        setError(t('login.errorIncomplete'));
        return;
      }
      setSessionToken(realm, token);
      navigate(realmHome(realm));
    } catch (e) {
      const code = (e as { code?: number }).code;
      if (code === 10043) {
        setError(t('login.errorCaptcha'));
      } else {
        setError(code === 10002 ? t('login.errorBadCredentials') : t('login.errorIncomplete'));
      }
      // Every attempt consumes the puzzle (right or wrong), so a retry
      // always needs a fresh image.
      void refreshCaptcha();
    } finally {
      setSubmitting(false);
    }
  };

  if (loading) {
    return <div className="login" data-testid="login-loading">{t('login.loading')}</div>;
  }

  return (
    <div className="login" data-testid="sso-login-list">
      <div className="login-brand" aria-hidden="true">
        <img src={brandLogo} alt="" className="login-brand-logo" />
      </div>
      <h1>{t('login.signIn')}</h1>
      <p className="login-subtitle">{t('login.subtitle')}</p>
      {error && <div className="error" data-testid="login-error">{error}</div>}
      <form className="custom-login-form" onSubmit={(e) => { e.preventDefault(); void localLogin(); }}>
        <div className="field">
          <label htmlFor="login-username">{t('login.username')}</label>
          <input
            id="login-username"
            data-testid="login-username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoComplete="username"
          />
        </div>
        {requiresEmailCode && (
        <div className="field">
          <label htmlFor="login-email">{t('login.email')}</label>
          <div className="code-row">
            <input
              id="login-email"
              data-testid="login-email"
              value={email}
              onChange={(e) => {
                setEmail(e.target.value);
                // The resend window belongs to the address, not the form.
                cooldown.reset();
              }}
              autoComplete="email"
            />
            <button
              type="button"
              className="secondary"
              data-testid="login-send-code"
              disabled={!email.trim() || sendingCode || !cooldown.ready}
              onClick={() => void sendCode()}
            >
              {!cooldown.ready
                ? t('login.resendIn', { seconds: cooldown.remaining })
                : sendingCode
                  ? t('common.working')
                  : t('login.sendCode')}
            </button>
          </div>
        </div>
        )}
        {requiresEmailCode && (
        <div className="field">
          <label htmlFor="login-code">{t('login.code')}</label>
          <input
            id="login-code"
            data-testid="login-code"
            value={emailCode}
            onChange={(e) => setEmailCode(e.target.value)}
            autoComplete="one-time-code"
            inputMode="numeric"
          />
        </div>
        )}
        <div className="field">
          <label htmlFor="login-password">{t('login.password')}</label>
          <div className="password-row">
            <input
              id="login-password"
              data-testid="login-password"
              type={showPassword ? 'text' : 'password'}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
            />
            <button
              type="button"
              className="password-toggle"
              data-testid="login-password-toggle"
              onClick={() => setShowPassword((v) => !v)}
              aria-label={showPassword ? t('common.hidePassword') : t('common.showPassword')}
            >
              {showPassword ? <EyeSlash size={18} /> : <Eye size={18} />}
            </button>
          </div>
        </div>
        {captchaImage && (
          <div className="field">
            <label htmlFor="login-captcha">{t('login.captcha')}</label>
            <div className="captcha-row">
              <input
                id="login-captcha"
                data-testid="login-captcha"
                value={captchaCode}
                onChange={(e) => setCaptchaCode(e.target.value)}
                autoComplete="off"
                inputMode="text"
              />
              <img
                src={captchaImage}
                alt={t('login.captcha')}
                className="captcha-image"
                data-testid="login-captcha-image"
                onClick={() => void refreshCaptcha()}
              />
              <button
                type="button"
                className="secondary"
                data-testid="login-captcha-refresh"
                onClick={() => void refreshCaptcha()}
              >
                {t('login.refreshCaptcha')}
              </button>
            </div>
          </div>
        )}
        <button
          type="submit"
          data-testid="login-local-submit"
          disabled={!canLogin || submitting}
        >
          {submitting ? t('common.working') : t('login.submit')}
        </button>
      </form>
      <div className="login-divider">{t('login.or')}</div>
      {providers.length === 0 ? (
        <div data-testid="login-no-providers">{t('login.noProviders')}</div>
      ) : (
        <div className="provider-list">
          {providers.map((p) => (
            <button
              key={p.providerId}
              className="provider-button"
              data-testid={`sso-login-${p.providerId}`}
              onClick={() => void signIn(p.providerId, p.type, p.passwordLogin)}
            >
              {t('login.signInWith', { name: p.displayName })}
            </button>
          ))}
        </div>
      )}
      <div className="login-alt" data-testid="login-register-link">
        <button className="link" onClick={() => navigate('/register')}>
          {t('login.register')}
        </button>
      </div>
    </div>
  );
}
