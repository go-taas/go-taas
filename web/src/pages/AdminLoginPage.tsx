// Admin login page (feature-17): realm-pinned sign-in against the admin
// surface's SSO providers.

import { useEffect, useState } from 'react';
import { useApi } from '../surface';

interface PublicProvider {
  providerId: string;
  type: string;
  displayName: string;
}

export default function AdminLoginPage() {
  const api = useApi();
  const [providers, setProviders] = useState<PublicProvider[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    api
      .get<{ providers?: PublicProvider[] }>('/api/v1/admin/auth/sso/providers', '')
      .then((data) => setProviders(data.providers || []))
      .catch((e) => setError(e instanceof Error ? e.message : 'failed to load providers'))
      .finally(() => setLoading(false));
  }, [api]);

  const signIn = async (providerId: string) => {
    try {
      const data = await api.get<{ redirectUrl?: string }>(
        `/api/v1/admin/auth/sso/${providerId}/authorize`,
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
    return <div className="login" data-testid="login-loading">Loading…</div>;
  }

  return (
    <div className="login" data-testid="sso-login-list">
      <div className="login-brand" aria-hidden="true" />
      <h1>Admin sign in</h1>
      <p className="login-subtitle">Manage organizations, models, billing and platform settings.</p>
      {error && <div className="error" data-testid="login-error">{error}</div>}
      {providers.length === 0 ? (
        <div data-testid="login-no-providers">No sign-in providers configured.</div>
      ) : (
        <div className="provider-list">
          {providers.map((p) => (
            <button
              key={p.providerId}
              className="provider-button"
              data-testid={`sso-login-${p.providerId}`}
              onClick={() => void signIn(p.providerId)}
            >
              Sign in with {p.displayName}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
