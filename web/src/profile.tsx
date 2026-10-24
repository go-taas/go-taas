// Profile context (feature: real avatar everywhere). Loads the caller's
// own profile once per realm and caches the avatar in localStorage so the
// shells can render the real avatar immediately on the next visit. The
// profile page calls refresh() after a save so the shell updates too.

import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react';
import { useApi, useRealm } from './surface';
import { getSessionToken } from './api';

export interface UserProfile {
  userId: string;
  username: string;
  email: string;
  displayName: string;
  avatar: string;
  createdAt: string;
}

interface ProfileContextValue {
  profile: UserProfile | null;
  /** refresh re-fetches the profile and updates the cached avatar. */
  refresh: () => Promise<void>;
  /** setProfile applies a locally-known profile (e.g. right after save). */
  setProfile: (p: UserProfile) => void;
}

const ProfileContext = createContext<ProfileContextValue>({
  profile: null,
  refresh: async () => {},
  setProfile: () => {},
});

// profilePath returns the profile API path of a realm. The admin console
// serves it under /api/v1/admin so the request stays on its surface.
export function profilePath(realm: string): string {
  return realm === 'admin' ? '/api/v1/admin/auth/profile' : '/api/v1/auth/profile';
}

// avatarCacheKey scopes the cached avatar by realm so the two consoles
// never share a stale avatar.
function avatarCacheKey(realm: string): string {
  return `go-taas.${realm}.avatar`;
}

/** Read the cached avatar for a realm (empty when absent). */
export function getCachedAvatar(realm: string): string {
  try {
    return localStorage.getItem(avatarCacheKey(realm)) || '';
  } catch {
    return '';
  }
}

function setCachedAvatar(realm: string, avatar: string): void {
  try {
    if (avatar) {
      localStorage.setItem(avatarCacheKey(realm), avatar);
    } else {
      localStorage.removeItem(avatarCacheKey(realm));
    }
  } catch {
    // A full or disabled localStorage must not break the console.
  }
}

export function ProfileProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const realm = useRealm();
  const [profile, setProfileState] = useState<UserProfile | null>(null);

  const apply = useCallback(
    (p: UserProfile) => {
      setProfileState(p);
      setCachedAvatar(realm, p.avatar);
    },
    [realm],
  );

  const refresh = useCallback(async () => {
    if (!getSessionToken(realm)) return;
    try {
      const data = await api.get<{ profile: UserProfile }>(profilePath(realm), '');
      if (data.profile) apply(data.profile);
    } catch {
      // No session or transient failure: the cached avatar stays.
    }
  }, [api, realm, apply]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return (
    <ProfileContext.Provider value={{ profile, refresh, setProfile: apply }}>
      {children}
    </ProfileContext.Provider>
  );
}

export function useProfile() {
  return useContext(ProfileContext);
}

/** avatarFor returns the profile avatar or the cached one for the realm. */
export function useAvatar(realm: string): string {
  const { profile } = useProfile();
  return profile?.avatar || getCachedAvatar(realm);
}
