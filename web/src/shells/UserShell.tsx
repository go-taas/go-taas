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
  LinkSimple,
  FileText,
  Bell,
  BookOpen,
  type Icon,
} from '@phosphor-icons/react';
import { Router, navigate } from '../router';
import { getSessionToken, setSessionToken, type SessionInfo } from '../api';
import { useApi, useRealm } from '../surface';
import { realmLoginPath } from '../surface-routes';
import { OrgSwitcher } from '../org';
import { useI18n } from '../i18n';
import NotificationBell from '../components/NotificationBell';
import brandLogo from '../assets/brand/logo-dark.svg';

// adminRoles is the set of session roles that make a user an
// administrator (feature-22 AD3). It mirrors the server-side default so
// the switch-to-admin button is shown only to admins.
const adminRoles = ['platform-admin', 'org-admin', 'admin', 'owner'];

export const USER_NAV_ITEMS: { path: string; labelKey: string; testid: string; icon: Icon }[] = [
  { path: '/quickstart', labelKey: 'nav.quickstart', testid: 'user-nav-quickstart', icon: Rocket },
  { path: '/docs', labelKey: 'nav.apiDocs', testid: 'user-nav-api-docs', icon: BookOpen },
  { path: '/usage', labelKey: 'nav.usage', testid: 'user-nav-usage', icon: ChartLine },
  { path: '/usage/keys', labelKey: 'nav.usageKeys', testid: 'user-nav-usage-keys', icon: Key },
  { path: '/cost', labelKey: 'nav.cost', testid: 'user-nav-cost', icon: ChartLine },
  { path: '/forecast', labelKey: 'nav.forecast', testid: 'user-nav-forecast', icon: ChartLine },
  { path: '/api-keys', labelKey: 'nav.apiKeys', testid: 'user-nav-api-keys', icon: Key },
  { path: '/request-logs', labelKey: 'nav.requestLogs', testid: 'user-nav-request-logs', icon: ListMagnifyingGlass },
  { path: '/playground', labelKey: 'nav.playground', testid: 'user-nav-playground', icon: Play },
  { path: '/billing', labelKey: 'nav.billing', testid: 'user-nav-billing', icon: Receipt },
  { path: '/billing/reports', labelKey: 'nav.billingReports', testid: 'user-nav-billing-reports', icon: FileText },
  { path: '/activity', labelKey: 'nav.activity', testid: 'user-nav-activity', icon: Pulse },
  { path: '/webhooks', labelKey: 'nav.webhooks', testid: 'user-nav-webhooks', icon: LinkSimple },
  { path: '/notifications', labelKey: 'nav.notifications', testid: 'user-nav-notifications', icon: Bell },
  { path: '/models', labelKey: 'nav.models', testid: 'user-nav-models', icon: Cube },
  { path: '/traces', labelKey: 'nav.traces', testid: 'user-nav-traces', icon: ChartLine },
  { path: '/errors', labelKey: 'nav.errors', testid: 'user-nav-errors', icon: ChartLine },
];

function isActive(path: string, current: string): boolean {
  return current === path || current.startsWith(`${path}/`);
}

export function UserShell({ children }: { children: ReactNode }) {
  const realm = useRealm();
  const api = useApi();
  const { t, lang, setLang } = useI18n();
  const [path, setPath] = useState(window.location.pathname);
  const [session, setSession] = useState<SessionInfo | null>(null);
  const [switching, setSwitching] = useState(false);
  const [switchNotice, setSwitchNotice] = useState('');

  useEffect(() => {
    return Router.subscribe(() => setPath(window.location.pathname));
  }, []);

  useEffect(() => {
    const token = getSessionToken(realm);
    if (!token) {
      // No session: redirect to the login page, preserving the intended
      // destination so the tenant returns here after signing in.
      navigate(`${realmLoginPath(realm)}?next=${encodeURIComponent(window.location.pathname)}&reason=unauthenticated`);
      return;
    }
    api
      .get<SessionInfo>('/api/v1/auth/session', '')
      .then((info) => setSession(info))
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

  // The switch-to-admin button is shown only to users whose session has
  // an admin role (feature-22 FR3.1).
  const isAdmin = (session?.roles || []).some((r) => adminRoles.includes(r));

  const switchToAdmin = async () => {
    setSwitching(true);
    setSwitchNotice('');
    try {
      const data = await api.post<{ sessionToken?: string }>(
        '/api/v1/auth/session:switch-to-admin',
        '',
        {},
      );
      if (data.sessionToken) {
        // Store the admin-realm token and navigate to the admin home
        // (feature-22 FR3.2).
        setSessionToken('admin', data.sessionToken);
        navigate('/admin/models');
      } else {
        setSwitchNotice(t('account.switchIncomplete'));
      }
    } catch (e) {
      const code = e && (e as { code?: number }).code;
      setSwitchNotice(code === 10036 ? t('account.switchNoPermission') : t('account.switchFailed'));
    } finally {
      setSwitching(false);
    }
  };

  return (
    <div className="app" data-testid="user-shell">
      <aside className="sidebar" data-testid="sidebar">
        <div className="brand">
          <img src={brandLogo} alt="Go TaaS" className="brand-logo" />
          <span>Go TaaS</span>
        </div>
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
                <span>{t(item.labelKey)}</span>
              </a>
            );
          })}
        </nav>
        <OrgSwitcher />
        <NotificationBell path="/api/v1/notifications" testId="user-bell-badge" />
        <button
          className="lang-switch"
          data-testid="lang-switch"
          onClick={() => setLang(lang === 'en' ? 'zh' : 'en')}
        >
          {t('lang.switch')}
        </button>
        {getSessionToken(realm) && (
          <div className="account-block" data-testid="user-account-block">
            {session && (
              <div className="account-identity">
                <span className="account-username">{session.username}</span>
                {isAdmin && <span className="badge">admin</span>}
              </div>
            )}
            {isAdmin && (
              <button
                className="link"
                data-testid="switch-to-admin"
                disabled={switching}
                onClick={() => void switchToAdmin()}
              >
                {switching ? t('account.switching') : t('account.switchToAdmin')}
              </button>
            )}
            {switchNotice && (
              <div className="notice" data-testid="switch-notice">{switchNotice}</div>
            )}
            <button className="link" data-testid="user-menu-logout" onClick={() => void logout()}>
              {t('account.signOut')}
            </button>
          </div>
        )}
      </aside>
      <main className="main">
        {children}
      </main>
    </div>
  );
}
