// Admin management scope (feature #54): a user may be a platform,
// organization and/or project administrator. The console asks which
// console to enter and keeps the choice for the session, so the
// navigation only offers the functions of that scope.

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { getSessionToken, type AdminScope, type GetMyAdminScopesResponse } from './api';
import { useApi, useRealm } from './surface';
import { useOrg } from './org';

export type ScopeType = 'platform' | 'organization' | 'project';

// PLATFORM_ORG_ID is the tenancy anchor the platform itself belongs to. The
// platform scope has no organization of its own, so calls that require one
// (the notification bell, for example) use this organization.
export const PLATFORM_ORG_ID = 'platform';

const STORAGE_KEY = 'go-taas.admin.scope';

// PersistedScope is the selection kept across reloads. Only the identity
// is stored; the scopes themselves are re-fetched from the server so a
// revoked role cannot be resurrected from storage.
interface PersistedScope {
  scopeType: ScopeType;
  organizationId: string;
  projectId: string;
}

function loadPersisted(): PersistedScope | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as PersistedScope;
    if (
      parsed.scopeType !== 'platform' &&
      parsed.scopeType !== 'organization' &&
      parsed.scopeType !== 'project'
    ) {
      return null;
    }
    return parsed;
  } catch {
    return null;
  }
}

function scopeMatches(a: AdminScope, b: PersistedScope): boolean {
  return (
    a.scopeType === b.scopeType &&
    (a.organizationId || '') === (b.organizationId || '') &&
    (a.projectId || '') === (b.projectId || '')
  );
}

function persist(scope: AdminScope | null): void {
  if (!scope) {
    localStorage.removeItem(STORAGE_KEY);
    return;
  }
  localStorage.setItem(
    STORAGE_KEY,
    JSON.stringify({
      scopeType: scope.scopeType,
      organizationId: scope.organizationId || '',
      projectId: scope.projectId || '',
    }),
  );
}

// scopeKey is a stable identity for a scope, used as a React key and for
// equality checks.
export function scopeKey(scope: AdminScope): string {
  return `${scope.scopeType}:${scope.organizationId || ''}:${scope.projectId || ''}`;
}

// scopeHome is the landing route of a scope.
export function scopeHome(scope: AdminScope): string {
  switch (scope.scopeType) {
    case 'platform':
      return '/admin/models';
    case 'organization':
      return '/admin/members';
    case 'project':
      return '/admin/project-members';
    default:
      return '/admin/models';
  }
}

interface AdminScopeContextValue {
  scopes: AdminScope[];
  loading: boolean;
  error: string;
  // scope is the active choice, or null when the user must choose.
  scope: AdminScope | null;
  selectScope: (scope: AdminScope) => void;
  clearScope: () => void;
  reload: () => Promise<void>;
}

const AdminScopeContext = createContext<AdminScopeContextValue>({
  scopes: [],
  loading: true,
  error: '',
  scope: null,
  selectScope: () => {},
  clearScope: () => {},
  reload: async () => {},
});

export function AdminScopeProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const realm = useRealm();
  const { setOrgId } = useOrg();
  const [scopes, setScopes] = useState<AdminScope[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [scope, setScope] = useState<AdminScope | null>(null);

  const applyScope = useCallback(
    (next: AdminScope | null) => {
      setScope(next);
      persist(next);
      // Organization and project consoles are scoped to their org; the
      // page components read the active org from the org context.
      if (next?.organizationId) {
        setOrgId(next.organizationId);
      }
    },
    [setOrgId],
  );

  const load = useCallback(async () => {
    if (!getSessionToken(realm)) {
      setLoading(false);
      return;
    }
    setLoading(true);
    setError('');
    try {
      const data = await api.get<GetMyAdminScopesResponse>('/api/v1/admin/tenancy/my-admin-scopes', '');
      const list = data.scopes || [];
      setScopes(list);
      // Restore the persisted selection only when the server still grants
      // it; otherwise fall back to the single available scope.
      const persisted = loadPersisted();
      const restored = persisted ? list.find((s) => scopeMatches(s, persisted)) : undefined;
      if (restored) {
        applyScope(restored);
      } else if (list.length === 1) {
        applyScope(list[0]);
      } else {
        applyScope(null);
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'failed to load admin scopes');
    } finally {
      setLoading(false);
    }
  }, [api, realm, applyScope]);

  useEffect(() => {
    void load();
    // Load once per console mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const selectScope = useCallback((next: AdminScope) => applyScope(next), [applyScope]);
  const clearScope = useCallback(() => applyScope(null), [applyScope]);

  const value = useMemo<AdminScopeContextValue>(
    () => ({ scopes, loading, error, scope, selectScope, clearScope, reload: load }),
    [scopes, loading, error, scope, selectScope, clearScope, load],
  );

  return <AdminScopeContext.Provider value={value}>{children}</AdminScopeContext.Provider>;
}

export function useAdminScope() {
  return useContext(AdminScopeContext);
}

// scopeTypeLabelKey maps a scope type to its i18n label key.
export function scopeTypeLabelKey(scopeType: string): string {
  switch (scopeType) {
    case 'platform':
      return 'adminScope.platform';
    case 'organization':
      return 'adminScope.organization';
    case 'project':
      return 'adminScope.project';
    default:
      return scopeType;
  }
}
