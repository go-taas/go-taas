// ProjectMembersPage is the project console's roster page: the project
// admin adds organization users to the project, changes roles and removes
// members (requirement 4).

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type MemberCandidate } from '../api';
import { useI18n } from '../i18n';
import { ErrorBanner } from '../components';
import { useAdminScope } from '../admin-scope';
import { candidateLabel } from '../candidates';

interface ProjectMember {
  userId: string;
  displayName?: string;
  role: string;
  joinedAt: string;
}

const ASSIGNABLE_ROLES = ['admin', 'member', 'viewer'];

export default function ProjectMembersPage() {
  const { scope } = useAdminScope();
  const projectId = scope?.projectId || '';
  const { t } = useI18n();
  const [members, setMembers] = useState<ProjectMember[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [userId, setUserId] = useState('');
  const [role, setRole] = useState('member');
  // The pick-list: this organization's members who belong to no project yet.
  // The API decides eligibility, so the console never offers an account that
  // cannot join.
  const [candidates, setCandidates] = useState<MemberCandidate[]>([]);
  const [candidatesLoading, setCandidatesLoading] = useState(false);

  const load = useCallback(async () => {
    if (!projectId) {
      setMembers([]);
      setLoading(false);
      return;
    }
    setLoading(true);
    setError('');
    try {
      const data = await api.get<{ members: ProjectMember[] }>(
        `/api/v1/admin/tenancy/projects/${projectId}/members?page.limit=100`,
        '',
      );
      setMembers(data.members || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('projectMembers.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, projectId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const loadCandidates = useCallback(async () => {
    if (!projectId) {
      setCandidates([]);
      return;
    }
    setCandidatesLoading(true);
    try {
      const data = await api.get<{ candidates?: MemberCandidate[] }>(
        `/api/v1/admin/tenancy/projects/${projectId}/member-candidates`,
        '',
      );
      setCandidates(data.candidates || []);
    } catch {
      setCandidates([]);
    } finally {
      setCandidatesLoading(false);
    }
  }, [api, projectId]);

  // Members change the candidate set, so it is reloaded with the roster.
  useEffect(() => {
    void loadCandidates();
  }, [loadCandidates, members]);

  const add = async () => {
    if (!userId.trim()) return;
    setError('');
    try {
      await api.post(`/api/v1/admin/tenancy/projects/${projectId}/members`, '', {
        userId: userId.trim(),
        role,
      });
      setUserId('');
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('projectMembers.addFailed'));
    }
  };

  const changeRole = async (memberId: string, nextRole: string) => {
    setError('');
    try {
      await api.patch(
        `/api/v1/admin/tenancy/projects/${projectId}/members/${memberId}`,
        '',
        { role: nextRole },
      );
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('projectMembers.roleFailed'));
    }
  };

  const remove = async (memberId: string) => {
    setError('');
    try {
      await api.del(`/api/v1/admin/tenancy/projects/${projectId}/members/${memberId}`, '');
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('projectMembers.removeFailed'));
    }
  };

  return (
    <div className="page" data-testid="project-members-page">
      <h1>{t('projectMembers.title')}</h1>
      <div className="subtitle mono">{projectId || t('projectMembers.noProject')}</div>
      {error && <ErrorBanner message={error} />}

      <div className="panel" style={{ marginBottom: 16 }}>
        <div className="form-grid">
          <label>
            {t('projectMembers.userId')}
            <select
              data-testid="project-member-user"
              value={userId}
              disabled={candidatesLoading}
              onChange={(e) => setUserId(e.target.value)}
            >
              <option value="">
                {candidatesLoading
                  ? t('common.loading')
                  : candidates.length === 0
                    ? t('projectMembers.noCandidates')
                    : t('projectMembers.selectUser')}
              </option>
              {candidates.map((c) => (
                <option key={c.userId} value={c.userId}>
                  {candidateLabel(c)}
                </option>
              ))}
            </select>
            <span className="field-hint">{t('projectMembers.candidatesHint')}</span>
          </label>
          <label>
            {t('projectMembers.role')}
            <select
              data-testid="project-member-role"
              value={role}
              onChange={(e) => setRole(e.target.value)}
            >
              {ASSIGNABLE_ROLES.map((r) => (
                <option key={r} value={r}>
                  {t(`state.${r}`)}
                </option>
              ))}
            </select>
          </label>
          <div className="form-field" style={{ alignSelf: 'end' }}>
            <button data-testid="project-member-add" disabled={!userId.trim()} onClick={() => void add()}>
              {t('projectMembers.add')}
            </button>
          </div>
        </div>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : members.length === 0 ? (
          <div className="empty-state" data-testid="project-members-empty">
            {t('projectMembers.empty')}
          </div>
        ) : (
          <table className="data" data-testid="project-members-table">
            <thead>
              <tr>
                <th>{t('projectMembers.colMember')}</th>
                <th>{t('projectMembers.colRole')}</th>
                <th>{t('projectMembers.colJoined')}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {members.map((m) => (
                <tr key={m.userId} data-testid={`project-member-row-${m.userId}`}>
                  <td>
                    {/* Name first, id underneath: the same reading order as
                        every other directory list in the console. */}
                    <strong>{m.displayName || m.userId}</strong>
                    {m.displayName && (
                      <div className="muted mono">{m.userId}</div>
                    )}
                  </td>
                  <td>
                    <select
                      data-testid={`project-member-role-${m.userId}`}
                      value={m.role}
                      onChange={(e) => void changeRole(m.userId, e.target.value)}
                    >
                      {ASSIGNABLE_ROLES.map((r) => (
                        <option key={r} value={r}>
                          {t(`state.${r}`)}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td>
                    {m.joinedAt
                      ? new Date(parseInt(m.joinedAt, 10) * 1000).toLocaleString()
                      : '—'}
                  </td>
                  <td>
                    <button
                      className="secondary danger"
                      data-testid={`project-member-remove-${m.userId}`}
                      onClick={() => void remove(m.userId)}
                    >
                      {t('common.remove')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
