// AdminScopePage asks a multi-role administrator which console to enter
// (platform / organization / project) and routes to its home. A single
// scope skips the choice.

import { useEffect } from 'react';
import { Buildings, Folder, SquaresFour } from '@phosphor-icons/react';
import { navigate } from '../router';
import { useI18n } from '../i18n';
import { ErrorBanner } from '../components';
import { scopeHome, scopeTypeLabelKey, useAdminScope } from '../admin-scope';
import { getSessionToken, type AdminScope } from '../api';
import { useRealm } from '../surface';
import { realmLoginPath } from '../surface-routes';

function scopeIcon(scopeType: string) {
  switch (scopeType) {
    case 'organization':
      return Buildings;
    case 'project':
      return Folder;
    default:
      return SquaresFour;
  }
}

export default function AdminScopePage() {
  const { t } = useI18n();
  const realm = useRealm();
  const { scopes, loading, error, scope, selectScope } = useAdminScope();

  // A single scope needs no choice: go straight to its console.
  useEffect(() => {
    if (loading || error) return;
    if (!getSessionToken(realm)) {
      navigate(`${realmLoginPath(realm)}?next=${encodeURIComponent('/admin')}&reason=unauthenticated`);
      return;
    }
    if (scopes.length === 1) {
      selectScope(scopes[0]);
      navigate(scopeHome(scopes[0]));
    }
  }, [loading, error, scopes, realm, selectScope]);

  const choose = (s: AdminScope) => {
    selectScope(s);
    navigate(scopeHome(s));
  };

  return (
    <div className="page" data-testid="admin-scope-page">
      <h1>{t('adminScope.title')}</h1>
      <p className="subtitle">{t('adminScope.subtitle')}</p>
      {error && <ErrorBanner message={error} />}

      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : scopes.length === 0 ? (
        <div className="empty-state" data-testid="admin-scope-empty">
          {t('adminScope.empty')}
        </div>
      ) : (
        <div className="scope-grid" data-testid="admin-scope-list">
          {scopes.map((s) => {
            const Icon = scopeIcon(s.scopeType);
            const active = scope && scope.scopeType === s.scopeType &&
              scope.organizationId === s.organizationId &&
              scope.projectId === s.projectId;
            return (
              <button
                key={`${s.scopeType}:${s.organizationId}:${s.projectId}`}
                className={active ? 'scope-card active' : 'scope-card'}
                data-testid={`admin-scope-${s.scopeType}`}
                onClick={() => choose(s)}
              >
                <Icon size={28} weight="duotone" aria-hidden="true" />
                <span className="scope-card-title">{t(scopeTypeLabelKey(s.scopeType))}</span>
                <span className="scope-card-name">
                  {s.displayName || s.organizationId || s.projectId || ''}
                </span>
                <span className="scope-card-role">
                  {s.role ? t(`state.${s.role}`) : ''}
                </span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
