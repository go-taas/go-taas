// End-user console shell (feature-17): owns the user realm's navigation,
// session guard and transitional banner. It never reads the admin realm's
// keys (AD12).

import { useEffect, useState, type ReactNode } from 'react';
import { ArrowsLeftRight, SignOut } from '@phosphor-icons/react';
import { Router, navigate } from '../router';
import { getSessionToken, setSessionToken, type SessionInfo } from '../api';
import { useApi, useRealm } from '../surface';
import { realmLoginPath } from '../surface-routes';
import { OrgSwitcher } from '../org';
import QuotaBar from '../pages/user/quotas/QuotaBar';
import { useI18n } from '../i18n';
import NotificationBell from '../components/NotificationBell';
import brandLogo from '../assets/brand/logo-dark.svg';
import { ErrorBoundary } from '../components';
import { USER_NAV_CATEGORIES, categoryForPath } from './nav';

// adminRoles is the set of session roles that make a user an
// administrator (feature-22 AD3). It mirrors the server-side default so
// the switch-to-admin button is shown only to admins.
const adminRoles = ['platform-admin', 'org-admin', 'admin', 'owner'];

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
      <header className="topbar" data-testid="topbar">
        <div className="topbar-brand">
          <img src={brandLogo} alt="Go TaaS" className="brand-logo" />
          <span>Go TaaS</span>
        </div>
        <nav className="topbar-nav" aria-label="User categories">
          {USER_NAV_CATEGORIES.map((cat) => {
            const IconComp = cat.icon;
            const active = categoryForPath(USER_NAV_CATEGORIES, path).id === cat.id;
            return (
              <a
                key={cat.id}
                href={cat.items[0].path}
                className={active ? 'topbar-nav-item active' : 'topbar-nav-item'}
                data-testid={`user-nav-cat-${cat.id}`}
                onClick={(e) => {
                  e.preventDefault();
                  navigate(cat.items[0].path);
                }}
              >
                <IconComp size={16} weight="duotone" aria-hidden="true" />
                <span>{t(cat.labelKey)}</span>
              </a>
            );
          })}
        </nav>
        <div className="topbar-actions">
          <NotificationBell path="/api/v1/notifications" testId="user-bell-badge" />
          <button
            className="lang-switch"
            data-testid="lang-switch"
            onClick={() => setLang(lang === 'en' ? 'zh' : 'en')}
          >
            {t('lang.switch')}
          </button>
        </div>
      </header>
      <aside className="sidebar" data-testid="sidebar">
        <div className="sidebar-category-label">
          {t(categoryForPath(USER_NAV_CATEGORIES, path).shortLabelKey)}
        </div>
        <nav>
          {categoryForPath(USER_NAV_CATEGORIES, path).items.map((item) => {
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
        <QuotaBar />
        {getSessionToken(realm) && (
          <div className="account-card" data-testid="user-account-block">
            {session && (
              <div className="account-identity">
                <span className="account-avatar" aria-hidden="true">
                  {session.username.trim().charAt(0).toUpperCase()}
                </span>
                <span className="account-meta">
                  <span className="account-username" title={session.username}>
                    {session.username}
                  </span>
                  <span className={isAdmin ? 'account-role' : 'account-role muted'}>
                    {isAdmin ? 'admin' : 'user'}
                  </span>
                </span>
              </div>
            )}
            <div className="account-actions">
              {isAdmin && (
                <button
                  className="account-action"
                  data-testid="switch-to-admin"
                  disabled={switching}
                  onClick={() => void switchToAdmin()}
                >
                  <ArrowsLeftRight size={15} weight="bold" aria-hidden="true" />
                  <span>{switching ? t('account.switching') : t('account.switchToAdmin')}</span>
                </button>
              )}
              {switchNotice && (
                <div className="account-notice" data-testid="switch-notice">{switchNotice}</div>
              )}
              <button
                className="account-action account-action-danger"
                data-testid="user-menu-logout"
                onClick={() => void logout()}
              >
                <SignOut size={15} weight="bold" aria-hidden="true" />
                <span>{t('account.signOut')}</span>
              </button>
            </div>
          </div>
        )}
      </aside>
      <main className="main">
        <ErrorBoundary title={t('common.pageError')}>{children}</ErrorBoundary>
      </main>
    </div>
  );
}
