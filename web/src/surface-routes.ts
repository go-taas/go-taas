// Pure routing rules for the two console surfaces (feature-17 AD1/AD7).
// The surface is a pure function of the path: /admin/... is the admin web
// surface, every other path is the user web surface.

import type { Realm } from './api';

// isAdminPath reports whether a web path belongs to the admin console.
export function isAdminPath(path: string): boolean {
  return path === '/admin' || path.startsWith('/admin/');
}

// MOVED_ADMIN_ROUTES maps the admin URLs that moved to the end-user
// console to their new paths. Evaluated before any shell or API call
// (AD7), so a bookmark does not trigger the admin session guard.
export const MOVED_ADMIN_ROUTES: Record<string, string> = {
  '/admin/api-keys': '/api-keys',
  '/admin/request-logs': '/request-logs',
  '/admin/playground': '/playground',
  // Model downloads became a panel on the model catalog, so a bookmark to
  // the old page lands where the work happens.
  '/admin/model-downloads': '/admin/models',
};

// realmHome returns the landing route of a realm.
export function realmHome(realm: Realm): string {
  return realm === 'admin' ? '/admin/models' : '/usage';
}

// LoginPath is the single shared login route. Both surfaces sign in at
// the end-user login page; the session realm is derived from the
// account's role and the console routes to the matching surface.
export const LoginPath = '/login';

// realmLoginPath returns the login route. There is one shared login page
// for both surfaces.
// eslint-disable-next-line @typescript-eslint/no-unused-vars
export function realmLoginPath(_realm: Realm): string {
  return LoginPath;
}

// realmCustomLoginPath returns the shared custom login route for a
// provider: /login/{provider_id}. The provider's own session realm is
// derived server-side.
export function realmCustomLoginPath(_realm: Realm, providerId: string): string {
  return `${LoginPath}/${providerId}`;
}

// isAllowedNext validates the next parameter of a guard redirect so the
// login page cannot be used as a surface-crossing redirector (S7).
export function isAllowedNext(realm: Realm, next: string): boolean {
  if (realm === 'admin') {
    return next === '/admin' || next.startsWith('/admin/');
  }
  return next !== '/admin' && !next.startsWith('/admin/');
}
