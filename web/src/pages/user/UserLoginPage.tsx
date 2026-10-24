// End-user login page (feature-17/22): realm-pinned sign-in against the
// user surface's SSO providers. An OIDC or LDAP provider navigates to
// the TaaS custom login page; a SAML provider keeps the redirect flow.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { navigate } from '../../router';
import { realmCustomLoginPath } from '../../surface-routes';
import { useI18n } from '../../i18n';
import brandLogo from '../../assets/brand/logo.svg';

interface PublicProvider {
  providerId: string;
  type: string;
  displayName: string;
  passwordLogin: boolean;
}

export default function UserLoginPage() {
  const api = useApi();
  const { t } = useI18n();
  const [providers, setProviders] = useState<PublicProvider[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api
      .get<{ providers?: PublicProvider[] }>('/api/v1/auth/sso/providers', '')
      .then((data) => setProviders(data.providers || []))
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load providers'))
      .finally(() => setLoading(false));
  }, [api]);

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
