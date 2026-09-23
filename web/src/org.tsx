// Transitional organization context: the platform has no login yet (SSO is
// feature #7), so the console keeps the organization id in localStorage and
// sends it as X-Organization-Id on every request, exactly like the API
// design specifies for the transitional period.

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';

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
// sidebar until SSO lands (feature #7).
export function OrgSwitcher() {
  const { orgId, setOrgId } = useOrg();
  const [value, setValue] = useState(orgId);
  return (
    <div className="org-switcher">
      <label htmlFor="org-id-input">Organization (transitional)</label>
      <input
        id="org-id-input"
        data-testid="org-id-input"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onBlur={() => setOrgId(value.trim() || 'org-default')}
        onKeyDown={(e) => {
          if (e.key === 'Enter') setOrgId(value.trim() || 'org-default');
        }}
      />
    </div>
  );
}
