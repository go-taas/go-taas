// Roster candidate labels.
//
// A member pick-list shows people, not ids: the display name first (the
// username when the account has none), with the username in parentheses only
// when it differs, so two accounts with the same display name stay
// distinguishable.

import type { MemberCandidate } from './api';

export function candidateLabel(candidate: MemberCandidate): string {
  const name = candidate.displayName || candidate.username || candidate.userId;
  if (candidate.username && candidate.username !== name) {
    return `${name} (${candidate.username})`;
  }
  return name;
}
