// ProjectMembersPage is the project console's roster page: the project
// admin adds organization users to the project, changes roles and removes
// members (requirement 4).

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type MemberCandidate } from '../api';
import { useApi } from '../surface';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner } from '../components';
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
  // The console scope carries the project's name, so the roster is described
  // the way the rest of the console names it.
  const projectName = scope?.displayName || '';
  const { t } = useI18n();
  const [members, setMembers] = useState<ProjectMember[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [userId, setUserId] = useState('');
  const [role, setRole] = useState('member');
  // Adding a member is a dialog, exactly like the organization roster: the
  // page shows the roster, the dialog does the one-off action.
  const [addOpen, setAddOpen] = useState(false);
  // The pick-list: this organization's members who belong to no project yet.
  // The API decides eligibility, so the console never offers an account that
  // cannot join.
  const [candidates, setCandidates] = useState<MemberCandidate[]>([]);
  const [candidatesLoading, setCandidatesLoading] = useState(false);
  // The quota dialog edits one member's budget inside this project: the
  // member tier of the ladder.
  const [quotaTarget, setQuotaTarget] = useState<ProjectMember | null>(null);

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
      setAddOpen(false);
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
      <div className="page-header">
        <div>
          <h1>{t('projectMembers.title')}</h1>
          <div className="subtitle">{projectName || projectId || t('projectMembers.noProject')}</div>
        </div>
        <button data-testid="project-member-add-open" onClick={() => setAddOpen(true)}>
          {t('projectMembers.add')}
        </button>
      </div>
      {error && <ErrorBanner message={error} />}

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
                      className="secondary"
                      data-testid={`project-member-quota-${m.userId}`}
                      onClick={() => setQuotaTarget(m)}
                    >
                      {t('memberQuota.quota')}
                    </button>
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

      {quotaTarget && (
        <MemberQuotaDialog
          projectId={projectId}
          member={quotaTarget}
          onClose={() => setQuotaTarget(null)}
        />
      )}

      {addOpen && (
        <Dialog title={t('projectMembers.add')} onClose={() => setAddOpen(false)} testId="project-member-add-dialog">
          <div className="form">
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
            <div className="dialog-actions">
              <button className="secondary" data-testid="project-member-add-cancel" onClick={() => setAddOpen(false)}>
                {t('common.cancel')}
              </button>
              <button
                data-testid="project-member-add"
                disabled={!userId.trim() || candidatesLoading}
                onClick={() => void add()}
              >
                {t('projectMembers.addConfirm')}
              </button>
            </div>
          </div>
        </Dialog>
      )}
    </div>
  );
}

/**
 * MemberQuotaDialog edits one member's budget inside this project and shows
 * the combined limit that budget produces.
 *
 * The member tier sits between the project budget and the person: it tightens
 * that person's own keys for this project only, and the effective value is the
 * tighter of it and the organization-wide personal budget. Showing both is the
 * point — a member cap looser than the personal one changes nothing, and an
 * administrator should see that before saving.
 */
export function MemberQuotaDialog({
  projectId,
  member,
  onClose,
}: {
  projectId: string;
  member: ProjectMember;
  onClose: () => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const [maxConcurrent, setMaxConcurrent] = useState('0');
  const [maxBatch, setMaxBatch] = useState('0');
  const [maxTokens, setMaxTokens] = useState('0');
  const [maxSpend, setMaxSpend] = useState('0');
  const [effective, setEffective] = useState<Record<string, string> | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    api
      .get<{ quotas?: Array<Record<string, string>> }>(
        `/api/v1/admin/tenancy/projects/${projectId}/member-quotas`,
        '',
      )
      .then((data) => {
        if (!active) return;
        const row = (data.quotas || []).find((q) => q.userId === member.userId);
        if (!row) return;
        setMaxConcurrent(String(row.maxConcurrentRequests || '0'));
        setMaxBatch(String(row.maxBatchJobsPerDay || '0'));
        setMaxTokens(String(row.maxOutputTokensPerRequest || '0'));
        setMaxSpend(String(row.monthlySpendLimitCents || '0'));
        const eff = row.effective as unknown as Record<string, string> | undefined;
        if (eff) setEffective(eff);
      })
      .catch((e) => setError(e instanceof Error ? e.message : t('memberQuota.loadFailed')))
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [api, projectId, member.userId, t]);

  const save = async () => {
    setSaving(true);
    setError('');
    try {
      const res = await api.patch<{ quota?: { effective?: Record<string, string> } }>(
        `/api/v1/admin/tenancy/projects/${projectId}/member-quotas/${member.userId}`,
        '',
        {
          maxConcurrentRequests: Number(maxConcurrent || 0),
          maxBatchJobsPerDay: Number(maxBatch || 0),
          maxOutputTokensPerRequest: Number(maxTokens || 0),
          monthlySpendLimitCents: Number(maxSpend || 0),
        },
      );
      if (res.quota?.effective) setEffective(res.quota.effective);
      onClose();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('memberQuota.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      title={t('memberQuota.editTitle', { name: member.displayName || member.userId })}
      onClose={onClose}
      testId="project-member-quota-dialog"
    >
      {error && <ErrorBanner message={error} />}
      <div className="form">
        <label>
          {t('memberQuota.concurrent')}
          <input data-testid="member-quota-concurrent" value={maxConcurrent}
            disabled={loading} onChange={(e) => setMaxConcurrent(e.target.value)} />
        </label>
        <label>
          {t('memberQuota.batch')}
          <input data-testid="member-quota-batch" value={maxBatch}
            disabled={loading} onChange={(e) => setMaxBatch(e.target.value)} />
        </label>
        <label>
          {t('memberQuota.tokens')}
          <input data-testid="member-quota-tokens" value={maxTokens}
            disabled={loading} onChange={(e) => setMaxTokens(e.target.value)} />
        </label>
        <label>
          {t('memberQuota.spend')}
          <input data-testid="member-quota-spend" value={maxSpend}
            disabled={loading} onChange={(e) => setMaxSpend(e.target.value)} />
          <span className="field-hint">{t('memberQuota.spendHint')}</span>
        </label>
        {effective && (
          <p className="muted" data-testid="member-quota-effective">
            {t('memberQuota.effective', {
              concurrent: effective.maxConcurrentRequests || '0',
              spend: effective.monthlySpendLimitCents || '0',
            })}
          </p>
        )}
      </div>
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button data-testid="member-quota-save" disabled={saving || loading} onClick={() => void save()}>
          {t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}
