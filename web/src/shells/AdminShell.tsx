// Admin console shell (feature-17): owns the admin realm's navigation,
// session guard and transitional banner. It never reads the user realm's
// keys (AD12).

import { useEffect, useState, type ReactNode } from 'react';
import {
  Buildings,
  Folder,
  Users,
  Envelope,
  Key,
  LinkSimple,
  Cube,
  Rocket,
  Image,
  ChartLine,
  Tag,
  Receipt,
  UserCircle,
  CreditCard,
  FileText,
  Scroll,
  Gauge,
  Cpu,
  PuzzlePiece,
  Bell,
  Globe,
  TextT,
  ArrowsSplit,
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

export const ADMIN_NAV_ITEMS: { path: string; labelKey: string; testid: string; icon: Icon }[] = [
  { path: '/admin/organizations', labelKey: 'nav.organizations', testid: 'nav-organizations', icon: Buildings },
  { path: '/admin/projects', labelKey: 'nav.projects', testid: 'nav-projects', icon: Folder },
  { path: '/admin/members', labelKey: 'nav.members', testid: 'nav-members', icon: Users },
  { path: '/admin/invitations', labelKey: 'nav.invitations', testid: 'nav-invitations', icon: Envelope },
  { path: '/admin/sso', labelKey: 'nav.ssoProviders', testid: 'nav-sso-providers', icon: Key },
  { path: '/admin/identity-bindings', labelKey: 'nav.identityBindings', testid: 'nav-identity-bindings', icon: LinkSimple },
  { path: '/admin/models', labelKey: 'nav.models', testid: 'nav-models', icon: Cube },
  { path: '/admin/inference-services', labelKey: 'nav.inferenceServices', testid: 'nav-inference-services', icon: Rocket },
  { path: '/admin/clusters', labelKey: 'nav.clusters', testid: 'nav-clusters', icon: Globe },
  { path: '/admin/deployments', labelKey: 'nav.deployments', testid: 'nav-deployments', icon: Scroll },
  { path: '/admin/images', labelKey: 'nav.images', testid: 'nav-images', icon: Image },
  { path: '/admin/usage', labelKey: 'nav.usage', testid: 'nav-usage', icon: ChartLine },
  { path: '/admin/usage/keys', labelKey: 'nav.usageKeys', testid: 'nav-usage-keys', icon: Key },
  { path: '/admin/cost', labelKey: 'nav.cost', testid: 'nav-cost', icon: ChartLine },
  { path: '/admin/forecast', labelKey: 'nav.forecast', testid: 'nav-forecast', icon: ChartLine },
  { path: '/admin/pricing', labelKey: 'nav.pricing', testid: 'nav-pricing', icon: Tag },
  { path: '/admin/billing', labelKey: 'nav.bills', testid: 'nav-bills', icon: Receipt },
  { path: '/admin/billing/accounts', labelKey: 'nav.accounts', testid: 'nav-accounts', icon: UserCircle },
  { path: '/admin/billing/payments', labelKey: 'nav.payments', testid: 'nav-payments', icon: CreditCard },
  { path: '/admin/billing/invoices', labelKey: 'nav.invoices', testid: 'nav-invoices', icon: FileText },
  { path: '/admin/billing/reports', labelKey: 'nav.billingReports', testid: 'nav-billing-reports', icon: FileText },
  { path: '/admin/audit-logs', labelKey: 'nav.auditLogs', testid: 'nav-audit-logs', icon: Scroll },
  { path: '/admin/autoscaling', labelKey: 'nav.autoscaling', testid: 'nav-autoscaling', icon: Gauge },
  { path: '/admin/accelerators', labelKey: 'nav.accelerators', testid: 'nav-accelerators', icon: Cpu },
  { path: '/admin/compatibility', labelKey: 'nav.compatibility', testid: 'nav-compatibility', icon: PuzzlePiece },
  { path: '/admin/load-tests', labelKey: 'nav.loadTests', testid: 'nav-load-tests', icon: Gauge },
  { path: '/admin/routing-policies', labelKey: 'nav.routingPolicies', testid: 'nav-routing-policies', icon: ArrowsSplit },
  { path: '/admin/webhooks', labelKey: 'nav.webhooks', testid: 'nav-webhooks', icon: LinkSimple },
  { path: '/admin/notifications', labelKey: 'nav.notifications', testid: 'nav-notifications', icon: Bell },
  { path: '/admin/observability', labelKey: 'nav.observability', testid: 'nav-observability', icon: ChartLine },
  { path: '/admin/traces', labelKey: 'nav.traces', testid: 'nav-traces', icon: ChartLine },
  { path: '/admin/errors', labelKey: 'nav.errors', testid: 'nav-errors', icon: ChartLine },
  { path: '/admin/batch', labelKey: 'nav.batch', testid: 'nav-batch', icon: Cube },
  { path: '/admin/prompts', labelKey: 'nav.prompts', testid: 'nav-prompts', icon: TextT },
  { path: '/admin/status', labelKey: 'nav.status', testid: 'nav-status', icon: Gauge },
];

function isActive(path: string, current: string): boolean {
  return current === path || current.startsWith(`${path}/`);
}

export function AdminShell({ children }: { children: ReactNode }) {
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
      <aside className="sidebar" data-testid="sidebar">
        <div className="brand">
          <img src={brandLogo} alt="Go TaaS" className="brand-logo" />
          <span>Go TaaS</span>
        </div>
        <nav>
          {ADMIN_NAV_ITEMS.map((item) => {
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
        <NotificationBell path="/api/v1/admin/notifications" testId="admin-bell-badge" />
        <button
          className="lang-switch"
          data-testid="lang-switch"
          onClick={() => setLang(lang === 'en' ? 'zh' : 'en')}
        >
          {t('lang.switch')}
        </button>
        {getSessionToken(realm) && (
          <div className="account-block" data-testid="admin-account-block">
            {session && (
              <div className="account-identity">
                <span className="account-username">{session.username}</span>
                <span className="badge">admin</span>
              </div>
            )}
            <button
              className="link"
              data-testid="switch-to-user"
              disabled={switching}
              onClick={() => void switchToUser()}
            >
              {switching ? t('account.switching') : t('account.switchToUser')}
            </button>
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
