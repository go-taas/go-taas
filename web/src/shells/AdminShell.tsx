// Admin console shell (feature-17): owns the admin realm's navigation,
// session guard and transitional banner. It never reads the user realm's
// keys (AD12).

import { useEffect, useState, type ReactNode } from 'react';
import { ArrowsLeftRight, SignOut } from '@phosphor-icons/react';
import { Router, navigate } from '../router';
import { getSessionToken, setSessionToken, type SessionInfo } from '../api';
import { useApi, useRealm } from '../surface';
import { realmLoginPath } from '../surface-routes';
import { OrgSwitcher } from '../org';
import { useI18n } from '../i18n';
import NotificationBell from '../components/NotificationBell';
import brandLogo from '../assets/brand/logo-dark.svg';
import { Avatar, ErrorBoundary } from '../components';
import { useAvatar } from '../profile';
import { ADMIN_NAV_CATEGORIES, categoryForPath } from './nav';

// navForRoles restricts the admin navigation by the caller's role:
// only platform admins see the full operator surface. Organization and
// project admins are limited to the access/membership pages.
function navForRoles(roles: string[]) {
  if (roles.includes('platform-admin')) return ADMIN_NAV_CATEGORIES;
  const hidden = ['/admin/users', '/admin/sso', '/admin/identity-bindings'];
  return ADMIN_NAV_CATEGORIES.filter((c) => c.id === 'access').map((c) => ({
    ...c,
    items: c.items.filter((i) => !hidden.includes(i.path)),
  }));
}

function isActive(path: string, current: string): boolean {
  return current === path || current.startsWith(`${path}/`);
}

export function AdminShell({ children }: { children: ReactNode }) {
  const realm = useRealm();
  const api = useApi();
  const { t, lang, setLang } = useI18n();
  const [path, setPath] = useState(window.location.pathname);
  const [session, setSession] = useState<SessionInfo | null>(null);
  const avatar = useAvatar(realm);
  const [switching, setSwitching] = useState(false);
  const [switchNotice, setSwitchNotice] = useState('');
  const navCategories = navForRoles(session?.roles || []);

  useEffect(() => {
    return Router.subscribe(() => setPath(window.location.pathname));
  }, []);

  useEffect(() => {
    const token = getSessionToken(realm);
    if (!token) {
      // No session: redirect to the login page, preserving the intended
      // destination so the operator returns here after signing in.
      navigate(`${realmLoginPath(realm)}?next=${encodeURIComponent(window.location.pathname)}&reason=unauthenticated`);
      return;
    }
    api
      .get<SessionInfo>('/api/v1/admin/auth/session', '')
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
      await api.post('/api/v1/admin/auth/logout', '', {});
    } catch {
      // Ignore logout errors; clear the session regardless.
    }
    setSessionToken(realm, '');
    navigate(realmLoginPath(realm));
  };

  const switchToUser = async () => {
    setSwitching(true);
    setSwitchNotice('');
    try {
      const data = await api.post<{ sessionToken?: string }>(
        '/api/v1/admin/auth/session:switch-to-user',
        '',
        {},
      );
      if (data.sessionToken) {
        // Store the user-realm token and navigate to the user home
        // (feature-22 FR3.3).
        setSessionToken('user', data.sessionToken);
        navigate('/usage');
      } else {
        setSwitchNotice(t('account.switchIncomplete'));
      }
    } catch {
      setSwitchNotice(t('account.switchFailed'));
    } finally {
      setSwitching(false);
    }
  };

  return (
    <div className="app" data-testid="admin-shell">
      <header className="topbar" data-testid="topbar">
        <div className="topbar-brand">
          <img src={brandLogo} alt="Go TaaS" className="brand-logo" />
          <span>Go TaaS</span>
        </div>
        <nav className="topbar-nav" aria-label="Admin categories">
          {navCategories.map((cat) => {
            const IconComp = cat.icon;
            const active = categoryForPath(navCategories, path).id === cat.id;
            return (
              <a
                key={cat.id}
                href={cat.items[0].path}
                className={active ? 'topbar-nav-item active' : 'topbar-nav-item'}
                data-testid={`nav-cat-${cat.id}`}
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
          <NotificationBell path="/api/v1/admin/notifications" testId="admin-bell-badge" />
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
          {t(categoryForPath(navCategories, path).shortLabelKey)}
        </div>
        <nav>
          {categoryForPath(navCategories, path).items.map((item) => {
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
        {getSessionToken(realm) && (
          <div className="account-card" data-testid="admin-account-block">
            {session && (
              <div className="account-identity">
                <Avatar
                  avatar={avatar}
                  name={session.username}
                  size={32}
                  testId={`${'admin'}-account-avatar`}
                />
                <span className="account-meta">
                  <span className="account-username" title={session.username}>
                    {session.username}
                  </span>
                  <span className="account-role">admin</span>
                </span>
              </div>
            )}
            <div className="account-actions">
              <button
                className="account-action"
                data-testid="switch-to-user"
                disabled={switching}
                onClick={() => void switchToUser()}
              >
                <ArrowsLeftRight size={15} weight="bold" aria-hidden="true" />
                <span>{switching ? t('account.switching') : t('account.switchToUser')}</span>
              </button>
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
