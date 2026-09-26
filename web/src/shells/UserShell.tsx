// End-user console shell (feature-17): owns the user realm's navigation,
// session guard and transitional banner. It never reads the admin realm's
// keys (AD12).

import { useEffect, useState, type ReactNode } from 'react';
import {
  ChartLine,
  Key,
  ListMagnifyingGlass,
  Play,
  Receipt,
  Pulse,
  Cube,
  Rocket,
  type Icon,
} from '@phosphor-icons/react';
import { Router, navigate } from '../router';
import { getSessionToken, setSessionToken, type SessionInfo } from '../api';
import { useApi, useRealm } from '../surface';
import { realmLoginPath } from '../surface-routes';
import { OrgSwitcher } from '../org';

export const USER_NAV_ITEMS: { path: string; label: string; testid: string; icon: Icon }[] = [
  { path: '/quickstart', label: 'Quickstart', testid: 'user-nav-quickstart', icon: Rocket },
  { path: '/usage', label: 'Usage', testid: 'user-nav-usage', icon: ChartLine },
  { path: '/api-keys', label: 'API Keys', testid: 'user-nav-api-keys', icon: Key },
  { path: '/request-logs', label: 'Request Logs', testid: 'user-nav-request-logs', icon: ListMagnifyingGlass },
  { path: '/playground', label: 'Playground', testid: 'user-nav-playground', icon: Play },
  { path: '/billing', label: 'Billing', testid: 'user-nav-billing', icon: Receipt },
  { path: '/activity', label: 'Activity', testid: 'user-nav-activity', icon: Pulse },
  { path: '/models', label: 'Models', testid: 'user-nav-models', icon: Cube },
];

function isActive(path: string, current: string): boolean {
  return current === path || current.startsWith(`${path}/`);
}

export function UserShell({ children }: { children: ReactNode }) {
  const realm = useRealm();
  const api = useApi();
  const [path, setPath] = useState(window.location.pathname);
  const [transitional, setTransitional] = useState(false);

  useEffect(() => {
    return Router.subscribe(() => setPath(window.location.pathname));
  }, []);

  useEffect(() => {
    const token = getSessionToken(realm);
    if (!token) {
      setTransitional(true);
      return;
    }
    api
      .get<SessionInfo>('/api/v1/auth/session', '')
      .catch((e) => {
        const code = e && e.code;
        if (code === 10027 || code === 10038) {
          setSessionToken(realm, '');
          navigate(`${realmLoginPath(realm)}?next=${encodeURIComponent(path)}&reason=${code === 10038 ? 'realm' : 'expired'}`);
        }
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const logout = async () => {
    try {
      await api.post('/api/v1/auth/logout', '', {});
    } catch {
      // Ignore logout errors; clear the session regardless.
    }
    setSessionToken(realm, '');
    navigate(realmLoginPath(realm));
  };

  return (
    <div className="app" data-testid="user-shell">
      <aside className="sidebar" data-testid="sidebar">
        <div className="brand">go-taas</div>
        <nav>
          {USER_NAV_ITEMS.map((item) => {
            const IconComp = item.icon;
            return (
              <a
                key={item.path}
                href={item.path}
                className={isActive(item.path, path) ? 'nav-item active' : 'nav-item'}
                data-testid={item.testid}
                onClick={(e) => {
                  e.preventDefault();
                  navigate(item.path);
                }}
              >
                <IconComp size={18} weight="duotone" aria-hidden="true" />
                <span>{item.label}</span>
              </a>
            );
          })}
        </nav>
        <OrgSwitcher />
        {getSessionToken(realm) && (
          <button className="link" data-testid="user-menu-logout" onClick={() => void logout()}>
            Sign out
          </button>
        )}
      </aside>
      <main className="main">
        {transitional && (
          <div className="banner" data-testid="user-console-transitional-banner">
            Transitional mode: no session. Requests use the stored organization.
          </div>
        )}
        {children}
      </main>
    </div>
  );
}
