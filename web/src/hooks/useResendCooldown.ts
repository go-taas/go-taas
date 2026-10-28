// useResendCooldown is the "send again in Ns" countdown shared by every
// email-verification form (login, registration, profile email change). The
// deadline is persisted per purpose, so a reload does not hand out a second
// code inside the server's cooldown window and the user still sees how long
// is left.

import { useCallback, useEffect, useState } from 'react';

export interface ResendCooldown {
  /** Remaining seconds; 0 when a code may be requested. */
  remaining: number;
  /** True while a code may be requested. */
  ready: boolean;
  /** Start (or restart) the countdown for the given window. */
  start: (seconds: number) => void;
  /** Clear the countdown; call it when the target address changes, because
   * the server tracks its window per address. */
  reset: () => void;
}

export function useResendCooldown(storageKey: string, defaultSeconds = 60): ResendCooldown {
  const [remaining, setRemaining] = useState(0);

  // Restore the deadline left by an earlier visit on mount.
  useEffect(() => {
    const until = parseInt(localStorage.getItem(storageKey) || '0', 10);
    const left = Math.max(0, Math.ceil((until - Date.now()) / 1000));
    if (left > 0) setRemaining(left);
  }, [storageKey]);

  useEffect(() => {
    if (remaining <= 0) {
      localStorage.removeItem(storageKey);
      return undefined;
    }
    const id = setInterval(() => setRemaining((c) => (c > 0 ? c - 1 : 0)), 1000);
    return () => clearInterval(id);
  }, [remaining, storageKey]);

  const start = useCallback(
    (seconds: number) => {
      const secs = seconds > 0 ? seconds : defaultSeconds;
      setRemaining(secs);
      localStorage.setItem(storageKey, String(Date.now() + secs * 1000));
    },
    [storageKey, defaultSeconds],
  );

  const reset = useCallback(() => {
    setRemaining(0);
    localStorage.removeItem(storageKey);
  }, [storageKey]);

  return { remaining, ready: remaining <= 0, start, reset };
}

/** windowSeconds reads the resend window from an email-code reply. */
export function windowSeconds(reply: { resendAfterSeconds?: string } | undefined): number {
  const secs = parseInt(reply?.resendAfterSeconds || '60', 10);
  return secs > 0 ? secs : 60;
}
