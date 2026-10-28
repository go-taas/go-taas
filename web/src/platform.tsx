// Platform display preference context.
//
// Reads the installation's time zone, default language and currency once per
// surface boot and pushes them into web/src/display.ts, so `formatTime` and
// `formatMoney` (which are plain functions, used outside React too) render the
// same units everywhere.
//
// The endpoint is served on both consoles, so the user console shows amounts in
// the platform's currency instead of a hard-coded one.

import { createContext, useContext, useEffect, useState, type ReactNode } from 'react';
import { setDisplayPreferences } from './display';
import { getSessionToken, sessionAuthHeader } from './api';
import { setLangIfUnset } from './i18n';

export interface PlatformDisplay {
  timezone: string;
  /** Default language for a first-time visitor ("en" or "zh"). */
  language: string;
  currency: string;
}

interface DisplayResponse {
  timezone?: string;
  defaultLanguage?: string;
  currency?: string;
}

const DEFAULTS: PlatformDisplay = { timezone: '', language: '', currency: 'CNY' };

const PlatformDisplayContext = createContext<PlatformDisplay>(DEFAULTS);

/** usePlatformDisplay returns the installation's display preferences. */
export function usePlatformDisplay(): PlatformDisplay {
  return useContext(PlatformDisplayContext);
}

export function PlatformDisplayProvider({ children }: { children: ReactNode }) {
  const [display, setDisplay] = useState<PlatformDisplay>(DEFAULTS);

  useEffect(() => {
    let cancelled = false;
    // The provider sits above the surface context (it must, so both consoles
    // inherit it), so the request is issued directly. Each realm's session is
    // only valid on its own prefix, hence the path follows the token.
    const admin = getSessionToken('admin');
    const path = admin ? '/api/v1/admin/platform-display-settings' : '/api/v1/platform-display-settings';
    const authorization = admin ? sessionAuthHeader('admin') : sessionAuthHeader('user');
    fetch(path, { headers: authorization ? { Authorization: authorization } : {} })
      .then((res) => (res.ok ? (res.json() as Promise<DisplayResponse>) : null))
      .then((data) => {
        if (!data) return;
        if (cancelled) return;
        const next: PlatformDisplay = {
          timezone: data.timezone || '',
          language: data.defaultLanguage || '',
          currency: data.currency || DEFAULTS.currency,
        };
        setDisplayPreferences(next.timezone, next.currency);
        setDisplay(next);
        // A visitor who never picked a language gets the installation's
        // default; an explicit choice always wins.
        if (next.language) setLangIfUnset(next.language);
      })
      .catch(() => {
        // The defaults already apply; a display-preference failure must not
        // block a console.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <PlatformDisplayContext.Provider value={display}>{children}</PlatformDisplayContext.Provider>
  );
}
