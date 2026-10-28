// Realm-scoped organization context (feature-17). Each surface keeps its
// own organization key and resolves the org from its own session (or the
// transitional header). The user-realm selector is disabled in
// transitional mode (no operator API call).

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import {
  getOrgId,
  getSessionToken,
  setOrgId,
  type MyOrganization,
  type OrganizationSummary,
  type SessionInfo,
} from './api';
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
  const api = useApi();
  const [orgId, setOrgIdState] = useState(() => getOrgId(realm) || NO_ORG);

  // The session is authoritative for the organization context. A stored id
  // can outlive the membership that justified it (a roster removal, a
  // console whose seeded organization was retired), so the first load
  // reconciles the two: a session with no active organization clears the
  // stored one instead of framing the console around an organization the
  // account no longer belongs to.
  useEffect(() => {
    if (!getSessionToken(realm)) return undefined;
    let cancelled = false;
    api
      .get<SessionInfo>(`/api/v1${realm === 'admin' ? '/admin' : ''}/auth/session`, '')
      .then((sess) => {
        if (cancelled) return;
        const active = sess.activeOrg || NO_ORG;
        if (active !== getOrgId(realm)) {
          setOrgId(realm, active);
          setOrgIdState(active);
        }
      })
      .catch(() => {
        // A missing session leaves the stored value alone; the login flow
        // sets it again.
      });
    return () => {
      cancelled = true;
    };
    // Reconcile once per console mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

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
// its invitation pipeline). The platform console renders no switcher: a
// platform administrator owns no tenant, so its pages read fleet-wide.
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

// organizationLabel returns the display name of an organization id. It reads
// the directory first (it is the freshest source), then any caller-supplied
// fallback such as the console scope's name, and finally the id itself so a
// page never renders an empty label.
export function organizationLabel(
  orgs: OrganizationSummary[],
  orgId: string,
  ...fallbacks: string[]
): string {
  const found = orgs.find((o) => o.organizationId === orgId);
  return found?.displayName || fallbacks.find((f) => f) || orgId;
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

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        if (getSessionToken(realm)) {
          const sess = await api.get<SessionInfo>(`/api/v1${realm === 'admin' ? '/admin' : ''}/auth/session`, '');
          if (cancelled) return;
          setSession(sess);
          // The switcher lists the organizations the account belongs to,
          // by name: the user-surface directory answers the account's own
          // organizations, so the selector reads like every other list
          // instead of showing raw ids.
          const names = new Map<string, string>();
          try {
            const mine = await api.get<{ organizations: MyOrganization[] }>(
              '/api/v1/tenancy/my-organizations',
              '',
            );
            for (const o of mine.organizations || []) {
              names.set(o.organizationId, o.displayName);
            }
          } catch {
            // Names are cosmetic: fall back to the ids below.
          }
          if (cancelled) return;
          const list = (sess.accessibleOrgs || []).map((id) => ({
            organizationId: id,
            displayName: names.get(id) || id,
            state: 'active',
          } as OrganizationSummary));
          setOrgs(list);
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
