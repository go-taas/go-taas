// Realm-scoped organization context (feature-17). Each surface keeps its
// own organization key and resolves the org from its own session (or the
// transitional header). The user-realm selector is disabled in
// transitional mode (no operator API call).

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { getOrgId, getSessionToken, setOrgId, type OrganizationSummary, type SessionInfo } from './api';
import { useApi, useRealm } from './surface';
import { useI18n } from './i18n';

// No organization is a real state: an account that has joined none, and
// the platform administrator, whose role lives on the account rather than
// in a membership row.
const NO_ORG = '';

const OrgContext = createContext<{
  orgId: string;
  setOrgId: (id: string) => void;
}>({ orgId: NO_ORG, setOrgId: () => {} });

export function OrgProvider({ children }: { children: ReactNode }) {
  const realm = useRealm();
  const [orgId, setOrgIdState] = useState(() => getOrgId(realm) || NO_ORG);

  useEffect(() => {
    document.documentElement.dataset.org = orgId;
  }, [orgId]);

  const setOrg = (id: string) => {
    setOrgId(realm, id);
    setOrgIdState(id);
  };

  return (
    <OrgContext.Provider value={{ orgId, setOrgId: setOrg }}>{children}</OrgContext.Provider>
  );
}

export function useOrg() {
  return useContext(OrgContext);
}

// useOrganizationPicker is the per-page organization selector for the
// pages that are inherently scoped to one organization (its member roster,
// its invitation pipeline). The platform console renders a global
// switcher for the platform administrator, who owns no tenant of their own
// but reads organization-scoped pages (usage, billing, quotas, audit).
//
// The selected organization defaults to the session's active organization
// (so it matches what the server resolves) and falls back to the first
// organization in the directory.
export function useOrganizationPicker(initial = '') {
  const realm = useRealm();
  const api = useApi();
  const { orgId, setOrgId } = useOrg();
  const [orgs, setOrgs] = useState<OrganizationSummary[]>([]);
  const [selected, setSelected] = useState(initial || orgId);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const data = await api.get<{ organizations: OrganizationSummary[] }>(
          '/api/v1/admin/tenancy/organizations?page.limit=100',
          orgId,
        );
        if (cancelled) return;
        const list = data.organizations || [];
        setOrgs(list);
        setSelected((prev) => {
          if (prev && list.some((o) => o.organizationId === prev)) return prev;
          return list[0]?.organizationId || prev || '';
        });
      } catch {
        // The directory is unavailable: keep whatever was selected so the
        // page still renders (its requests will surface the real error).
      } finally {
        if (!cancelled) setLoaded(true);
      }
    };
    void load();
    return () => {
      cancelled = true;
    };
    // Load once: the organization directory does not change per render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const choose = (id: string) => {
    setSelected(id);
    // Keep the realm context in sync so child dialogs use the same org.
    setOrgId(id);
  };

  return { orgId: selected, orgs, loaded, choose, realm };
}

// OrganizationPicker renders the select of useOrganizationPicker.
export function OrganizationPicker({
  orgs,
  value,
  onChange,
  testId = 'org-picker',
}: {
  orgs: OrganizationSummary[];
  value: string;
  onChange: (id: string) => void;
  testId?: string;
}) {
  const { t } = useI18n();
  return (
    <label className="field">
      <span>{t('org.organization')}</span>
      <select data-testid={testId} value={value} onChange={(e) => onChange(e.target.value)}>
        {orgs.length === 0 ? (
          <option value={value}>{value || '—'}</option>
        ) : (
          orgs.map((o) => (
            <option key={o.organizationId} value={o.organizationId}>
              {o.displayName && o.displayName !== o.organizationId
                ? `${o.displayName} (${o.organizationId})`
                : o.organizationId}
            </option>
          ))
        )}
      </select>
    </label>
  );
}

// OrgSwitcher is the organization selector rendered in the sidebar. When
// a session is present it lists the session's accessible orgs and
// switching calls UpdateSessionOrg; when no session exists it falls back
// to the tenancy API list (transitional mode).
export function OrgSwitcher() {
  const realm = useRealm();
  const api = useApi();
  const { t } = useI18n();
  const { orgId, setOrgId } = useOrg();
  const [orgs, setOrgs] = useState<OrganizationSummary[]>([]);
  const [notice, setNotice] = useState('');
  const [loaded, setLoaded] = useState(false);
  const [session, setSession] = useState<SessionInfo | null>(null);

  // adoptOrganization sets the organization locally and, when a session is
  // present, makes it the session's active organization so the server
  // resolves the same one.
  const adoptOrganization = async (id: string) => {
    setOrgId(id);
    if (!getSessionToken(realm)) return;
    try {
      await api.post(`/api/v1${realm === 'admin' ? '/admin' : ''}/auth/session/org`, '', {
        organizationId: id,
      });
    } catch {
      // The switch failed server-side; the local id is still set.
    }
  };


  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        if (getSessionToken(realm)) {
          const sess = await api.get<SessionInfo>(`/api/v1${realm === 'admin' ? '/admin' : ''}/auth/session`, '');
          if (cancelled) return;
          setSession(sess);
          // A platform administrator belongs to no organization yet
          // administers every one, so the selector offers the whole
          // registry; anybody else sees only the organizations they joined.
          const isPlatform = (sess.roles || []).includes('platform-admin');
          if (isPlatform) {
            try {
              const data = await api.get<{ organizations: OrganizationSummary[] }>(
                '/api/v1/admin/tenancy/organizations?page.limit=100',
                '',
              );
              if (cancelled) return;
              setOrgs((data.organizations || []).filter((o) => o.organizationId !== 'platform'));
            } catch {
              // Fall through to the membership list below.
            }
          }
          const list = (sess.accessibleOrgs || []).map((id) => ({
            organizationId: id,
            displayName: id,
            state: 'active',
          } as OrganizationSummary));
          setOrgs((prev) => {
            const resolved = prev.length > 0 ? prev : list;
            // The platform administrator has no organization of its own.
            // Adopt the first one so the organization-scoped pages have a
            // context; the switcher above shows which one is in effect and
            // changes it, or clears it when there is none.
            if (isPlatform && !sess.activeOrg && resolved.length > 0) {
              void adoptOrganization(resolved[0].organizationId);
            }
            return resolved;
          });
          if (sess.activeOrg) {
            setOrgId(sess.activeOrg);
          }
          return;
        }
        // No session: transitional tenancy API list (admin surface only).
        if (realm === 'admin') {
          const data = await api.get<{ organizations: OrganizationSummary[] }>(
            '/api/v1/admin/tenancy/organizations?page.limit=100',
            orgId,
          );
          if (cancelled) return;
          const list = data.organizations || [];
          setOrgs(list);
          const stored = getOrgId(realm);
          if (list.length > 0) {
            const match = list.find((o) => o.organizationId === stored);
            if (!match) {
              setOrgId(list[0].organizationId);
              setNotice(
                `Previous organization "${stored || NO_ORG}" no longer exists; switched to the first available one.`,
              );
            }
          }
        }
      } catch {
        // The tenancy/session API is unavailable: keep the fallback below.
      } finally {
        if (!cancelled) setLoaded(true);
      }
    };
    void load();
    return () => {
      cancelled = true;
    };
    // orgId is intentionally not a dependency: load once on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const switchOrg = async (id: string) => {
    setOrgId(id);
    if (session) {
      try {
        await api.post(`/api/v1${realm === 'admin' ? '/admin' : ''}/auth/session/org`, '', { organizationId: id });
      } catch {
        // The switch failed server-side; the local org id is still set.
      }
    }
  };

  if (!loaded && orgs.length === 0) {
    return (
      <div className="org-switcher">
        <label htmlFor="org-switcher-select">{t('org.organization')}</label>
        <select id="org-switcher-select" data-testid="org-switcher-select" value={orgId} disabled>
          <option value={orgId}>{orgId || t('org.none')}</option>
        </select>
      </div>
    );
  }

  return (
    <div className="org-switcher">
      <label htmlFor="org-switcher-select">{t('org.organization')}</label>
      <select
        id="org-switcher-select"
        data-testid="org-switcher-select"
        value={orgId}
        onChange={(e) => void switchOrg(e.target.value)}
      >
        {orgs.length === 0 ? (
          <option value="">{t('org.none')}</option>
        ) : (
          orgs.map((o) => (
            <option
              key={o.organizationId}
              value={o.organizationId}
              data-testid={`org-switcher-option-${o.organizationId}`}
            >
              {o.displayName || o.organizationId}
            </option>
          ))
        )}
      </select>
      {notice && (
        <div className="org-switcher-notice" data-testid="org-switcher-notice">
          {notice}
        </div>
      )}
    </div>
  );
}
