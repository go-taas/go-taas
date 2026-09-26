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
  type Icon,
} from '@phosphor-icons/react';
import { Router, navigate } from '../router';
import { getSessionToken, setSessionToken, type SessionInfo } from '../api';
import { useApi, useRealm } from '../surface';
import { realmLoginPath } from '../surface-routes';
import { OrgSwitcher } from '../org';

export const ADMIN_NAV_ITEMS: { path: string; label: string; testid: string; icon: Icon }[] = [
  { path: '/admin/organizations', label: 'Organizations', testid: 'nav-organizations', icon: Buildings },
  { path: '/admin/projects', label: 'Projects', testid: 'nav-projects', icon: Folder },
  { path: '/admin/members', label: 'Members', testid: 'nav-members', icon: Users },
  { path: '/admin/invitations', label: 'Invitations', testid: 'nav-invitations', icon: Envelope },
  { path: '/admin/sso', label: 'SSO Providers', testid: 'nav-sso-providers', icon: Key },
  { path: '/admin/identity-bindings', label: 'Identity Bindings', testid: 'nav-identity-bindings', icon: LinkSimple },
  { path: '/admin/models', label: 'Models', testid: 'nav-models', icon: Cube },
  { path: '/admin/inference-services', label: 'Inference Services', testid: 'nav-inference-services', icon: Rocket },
  { path: '/admin/images', label: 'Images', testid: 'nav-images', icon: Image },
  { path: '/admin/usage', label: 'Usage', testid: 'nav-usage', icon: ChartLine },
  { path: '/admin/pricing', label: 'Pricing', testid: 'nav-pricing', icon: Tag },
  { path: '/admin/billing', label: 'Bills', testid: 'nav-bills', icon: Receipt },
  { path: '/admin/billing/accounts', label: 'Accounts', testid: 'nav-accounts', icon: UserCircle },
  { path: '/admin/billing/payments', label: 'Payments', testid: 'nav-payments', icon: CreditCard },
  { path: '/admin/billing/invoices', label: 'Invoices', testid: 'nav-invoices', icon: FileText },
  { path: '/admin/audit-logs', label: 'Audit Logs', testid: 'nav-audit-logs', icon: Scroll },
  { path: '/admin/autoscaling', label: 'Autoscaling', testid: 'nav-autoscaling', icon: Gauge },
  { path: '/admin/accelerators', label: 'Accelerators', testid: 'nav-accelerators', icon: Cpu },
  { path: '/admin/compatibility', label: 'Compatibility', testid: 'nav-compatibility', icon: PuzzlePiece },
];

function isActive(path: string, current: string): boolean {
  return current === path || current.startsWith(`${path}/`);
}

export function AdminShell({ children }: { children: ReactNode }) {
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
      .get<SessionInfo>('/api/v1/admin/auth/session', '')
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

  return (
    <div className="app" data-testid="admin-shell">
      <aside className="sidebar" data-testid="sidebar">
        <div className="brand">go-taas</div>
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
          <div className="banner" data-testid="admin-console-transitional-banner">
            Transitional mode: no session. Requests use the stored organization.
          </div>
        )}
        {children}
      </main>
    </div>
  );
}
