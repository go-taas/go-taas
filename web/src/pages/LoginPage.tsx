// Login page (feature #7): the console's new front door. Lists enabled
// SSO providers as "Sign in with {display_name}" buttons; clicking one
// calls SSOAuthorize and redirects to the IdP; the callback returns the
// console authenticated.

import { useCallback, useEffect, useState } from 'react';
import { api, setSessionToken, type SSOProvider } from '../api';
import { ErrorBanner } from '../components';

interface ListResponse {
  response: { code: number; message: string };
  providers: SSOProvider[];
}

export default function LoginPage() {
  const [providers, setProviders] = useState<SSOProvider[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<ListResponse>(
        '/api/v1/admin/auth/sso/providers?page.limit=100',
        '',
      );
      setProviders(data.providers || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load providers');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const signIn = async (providerId: string) => {
    setError('');
    try {
      const data = await api.get<{
        redirectUrl?: string;
        bindForm?: string;
      }>(`/api/v1/auth/sso/${providerId}/authorize`, '');
      if (data.redirectUrl) {
        window.location.href = data.redirectUrl;
      } else if (data.bindForm) {
        // LDAP bind form: prompt for username/password.
        const username = window.prompt('Username');
        if (!username) return;
        const password = window.prompt('Password');
        if (!password) return;
        const cb = await api.get<{
          sessionToken: string;
          expiresAt: string;
        }>(
          `/api/v1/auth/sso/${providerId}/callback?username=${encodeURIComponent(username)}&password=${encodeURIComponent(password)}`,
          '',
        );
        setSessionToken(cb.sessionToken);
        window.location.href = '/admin';
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'sign-in failed');
    }
  };

  const enabled = providers.filter((p) => p.enabled);

  return (
    <div className="login-page">
      <div className="login-card">
        <div className="brand">go-taas</div>
        <h1>Sign in</h1>
        <p className="muted">Choose an identity provider to continue.</p>
        {error && <ErrorBanner message={error} />}
        {loading ? (
          <div className="loading">Loading…</div>
        ) : enabled.length === 0 ? (
          <div className="empty-state" data-testid="sso-login-disabled-notice">
            No SSO providers are enabled. Contact your administrator.
          </div>
        ) : (
          <div className="sso-login-list">
            {enabled.map((p) => (
              <button
                key={p.providerId}
                className="sso-login-button"
                data-testid={`sso-login-${p.providerId}`}
                onClick={() => void signIn(p.providerId)}
              >
                Sign in with {p.displayName}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}