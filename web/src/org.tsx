// Transitional organization context: the platform has no login yet (SSO is
// feature #7), so the console keeps the organization id in localStorage and
// sends it as X-Organization-Id on every request, exactly like the API
// design specifies for the transitional period.

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { api, type OrganizationSummary } from './api';

const STORAGE_KEY = 'go-taas.org-id';
const DEFAULT_ORG = 'org-default';

const OrgContext = createContext<{
  orgId: string;
  setOrgId: (id: string) => void;
}>({ orgId: DEFAULT_ORG, setOrgId: () => {} });

export function OrgProvider({ children }: { children: ReactNode }) {
  const [orgId, setOrgIdState] = useState(
    () => localStorage.getItem(STORAGE_KEY) || DEFAULT_ORG,
  );

  useEffect(() => {
    document.documentElement.dataset.org = orgId;
  }, [orgId]);

  const setOrgId = (id: string) => {
    localStorage.setItem(STORAGE_KEY, id);
    setOrgIdState(id);
  };

  return (
    <OrgContext.Provider value={{ orgId, setOrgId }}>{children}</OrgContext.Provider>
  );
}

export function useOrg() {
  return useContext(OrgContext);
}

// OrgSwitcher is the transitional organization selector rendered in the
// sidebar until SSO lands (feature #7). It lists the real organizations
// from the tenancy API; the stored organization wins, otherwise the first
// organization is selected and a notice explains the fallback.
export function OrgSwitcher() {
  const { orgId, setOrgId } = useOrg();
  const [orgs, setOrgs] = useState<OrganizationSummary[]>([]);
  const [notice, setNotice] = useState('');
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const data = await api.get<{
          organizations: OrganizationSummary[];
        }>('/api/v1/admin/tenancy/organizations?page.limit=100', orgId);
        if (cancelled) return;
        const list = data.organizations || [];
        setOrgs(list);
        const stored = localStorage.getItem(STORAGE_KEY);
        if (list.length > 0) {
          const match = list.find((o) => o.organizationId === stored);
          if (match) {
            // Stored organization still exists: keep it.
          } else {
            setOrgId(list[0].organizationId);
            setNotice(
              `Previous organization "${stored || DEFAULT_ORG}" no longer exists; switched to the first available one.`,
            );
          }
        }
      } catch {
        // The tenancy API is unavailable (e.g. older deployment): keep the
        // free-text fallback below.
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

  if (!loaded && orgs.length === 0) {
    // Still loading: render the plain selector without options.
    return (
      <div className="org-switcher">
        <label htmlFor="org-switcher-select">Organization (transitional)</label>
        <select id="org-switcher-select" data-testid="org-switcher-select" value={orgId} disabled>
          <option value={orgId}>{orgId}</option>
        </select>
      </div>
    );
  }

  if (orgs.length === 0) {
    // Tenancy API unavailable: free-text fallback (pre-feature-#6 behavior).
    return <OrgSwitcherFallback orgId={orgId} setOrgId={setOrgId} />;
  }

  return (
    <div className="org-switcher">
      <label htmlFor="org-switcher-select">Organization (transitional)</label>
      <select
        id="org-switcher-select"
        data-testid="org-switcher-select"
        value={orgId}
        onChange={(e) => setOrgId(e.target.value)}
      >
        {orgs.map((org) => (
          <option
            key={org.organizationId}
            value={org.organizationId}
            data-testid={`org-switcher-option-${org.organizationId}`}
          >
            {org.organizationId}
            {org.state !== 'active' ? ' (disabled)' : ''}
          </option>
        ))}
      </select>
      {notice && (
        <div className="muted" data-testid="org-switcher-notice">
          {notice}
        </div>
      )}
    </div>
  );
}

// OrgSwitcherFallback keeps the pre-tenancy free-text behavior for
// deployments where the tenancy API is not reachable.
function OrgSwitcherFallback({
  orgId,
  setOrgId,
}: {
  orgId: string;
  setOrgId: (id: string) => void;
}) {
  const [value, setValue] = useState(orgId);
  return (
    <div className="org-switcher">
      <label htmlFor="org-id-input">Organization (transitional)</label>
      <input
        id="org-id-input"
        data-testid="org-id-input"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onBlur={() => setOrgId(value.trim() || DEFAULT_ORG)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') setOrgId(value.trim() || DEFAULT_ORG);
        }}
      />
    </div>
  );
}
