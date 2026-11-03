// Organization budgets: the tenant administrator's view of the money.
//
// Members of an organization share one billing account — one prepaid balance or
// one postpaid quota — so this page answers two questions at that level: how
// much the tenant has, and how it is being handed out to its projects. The
// project column is the second rung of the ladder: the organization's balance is
// the pool, a project's monthly budget is a share of it, and a member's budget
// (set by the project administrator) is a share of the project's.
//
// The spend figures come from the settled ledger, which is the same window the
// data plane's spend gate compares against — showing "spent this month" from a
// different window would explain the wrong number.

import { useCallback, useEffect, useState } from 'react';
import {
  ApiError,
  formatTime,
  type BalanceResponse,
  type PageMeta,
  type ProjectSummary,
} from '../api';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, usePolling } from '../components';
import { formatMoney, displayCurrencyLabel } from '../display';

/** ProjectBudget is one project's budget row, with what it has consumed. */
interface ProjectBudget extends ProjectSummary {
  monthlySpendLimitCents?: string;
  spentThisCycleCents?: string;
  remainingCents?: string;
}

interface ListProjectsResponse {
  response: { code: number; message: string };
  projects?: ProjectSummary[];
  pageMeta?: PageMeta;
}

export default function OrganizationBudgetsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [balance, setBalance] = useState<BalanceResponse | null>(null);
  const [projects, setProjects] = useState<ProjectBudget[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState<ProjectBudget | null>(null);

  const loadBalance = useCallback(async () => {
    if (!orgId) {
      setBalance(null);
      return;
    }
    try {
      setBalance(await api.get<BalanceResponse>('/api/v1/admin/billing/balance', orgId));
    } catch {
      // A tenant without an account is not an error: the page then says
      // "no account yet" and the projects still list their budgets.
      setBalance(null);
    }
  }, [api, orgId]);

  const loadProjects = useCallback(async () => {
    if (!orgId) {
      setProjects([]);
      setLoading(false);
      return;
    }
    setError('');
    try {
      const data = await api.get<ListProjectsResponse>(
        '/api/v1/admin/tenancy/projects?page.limit=100',
        orgId,
      );
      const base = data.projects || [];
      // One quota read per project: the budget and its consumption live in the
      // quota view, and a page of projects is small enough that this stays a
      // handful of requests.
      const enriched = await Promise.all(
        base.map(async (project) => {
          try {
            const res = await api.get<{ quota: Record<string, string> }>(
              `/api/v1/admin/tenancy/projects/${project.projectId}/quota`,
              orgId,
            );
            return {
              ...project,
              monthlySpendLimitCents: res.quota.monthlySpendLimitCents || '0',
              spentThisCycleCents: res.quota.spentThisCycleCents || '0',
              remainingCents: res.quota.remainingCents || '0',
            };
          } catch {
            return project;
          }
        }),
      );
      setProjects(enriched);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('budgets.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    setLoading(true);
    void loadProjects();
  }, [loadProjects]);

  useEffect(() => {
    void loadBalance();
  }, [loadBalance]);

  usePolling(() => void loadBalance(), 60_000, Boolean(orgId));

  const prepaid = balance?.mode !== 'postpaid';
  const currency = balance?.currency || undefined;

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('budgets.title')}</h1>
          <div className="subtitle">{t('budgets.subtitle', { currency: displayCurrencyLabel() })}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}
      {!orgId && (
        <div className="empty-state" data-testid="budgets-no-org">
          {t('budgets.noOrg')}
        </div>
      )}

      {orgId && (
        <>
          <div className="usage-summary-row" data-testid="budgets-account-cards">
            <div className="card usage-summary-card">
              <div className="card-label">
                {prepaid ? t('budgets.balanceLabel') : t('budgets.quotaLabel')}
              </div>
              <div className="card-value" data-testid="budgets-balance-value">
                {balance
                  ? prepaid
                    ? formatMoney(balance.balanceCents, currency)
                    : formatMoney(balance.monthlyQuotaCents, currency)
                  : '—'}
              </div>
              <div className="card-note">
                {balance
                  ? prepaid
                    ? t('budgets.balanceNote', { currency: displayCurrencyLabel() })
                    : t('budgets.quotaUsed', {
                        used: formatMoney(balance.usedThisCycleCents, currency),
                      })
                  : t('budgets.noAccountNote')}
              </div>
            </div>
            <div className="card usage-summary-card">
              <div className="card-label">{t('budgets.spentCycleLabel')}</div>
              <div className="card-value" data-testid="budgets-spent-value">
                {balance
                  ? formatMoney(balance.spentThisCycleCents ?? balance.usedThisCycleCents, currency)
                  : '—'}
              </div>
              <div className="card-note">
                {balance?.cycleStartedAt
                  ? t('budgets.cycleFrom', { date: formatTime(balance.cycleStartedAt) })
                  : t('budgets.cycleUnknown')}
              </div>
            </div>
            <div className="card usage-summary-card">
              <div className="card-label">{t('budgets.allocatedLabel')}</div>
              <div className="card-value" data-testid="budgets-allocated-value">
                {formatMoney(
                  projects.reduce((sum, p) => sum + Number(p.monthlySpendLimitCents || 0), 0),
                  currency,
                )}
              </div>
              <div className="card-note">{t('budgets.allocatedNote')}</div>
            </div>
          </div>

          <div className="panel">
            <div className="panel-header">
              <h2>{t('budgets.projectsTitle')}</h2>
            </div>
            {loading ? (
              <div className="loading">{t('common.loading')}</div>
            ) : projects.length === 0 ? (
              <div className="empty-state" data-testid="budgets-empty">
                {t('budgets.empty')}
              </div>
            ) : (
              <table className="data" data-testid="budgets-table">
                <thead>
                  <tr>
                    <th>{t('budgets.colProject')}</th>
                    <th>{t('budgets.colBudget')}</th>
                    <th>{t('budgets.colSpent')}</th>
                    <th>{t('budgets.colRemaining')}</th>
                    <th className="col-actions">{t('common.actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {projects.map((project) => {
                    const limit = Number(project.monthlySpendLimitCents || 0);
                    const spent = Number(project.spentThisCycleCents || 0);
                    const percent = limit > 0 ? Math.min(100, (spent / limit) * 100) : 0;
                    return (
                      <tr key={project.projectId} data-testid={`budget-row-${project.projectId}`}>
                        <td>
                          <strong>{project.displayName}</strong>
                          <div className="muted small mono">{project.projectId}</div>
                        </td>
                        <td data-testid={`budget-limit-${project.projectId}`}>
                          {limit > 0 ? formatMoney(limit, currency) : t('common.unlimited')}
                        </td>
                        <td data-testid={`budget-spent-${project.projectId}`}>
                          {formatMoney(spent, currency)}
                          {limit > 0 && <progress value={percent} max={100} />}
                        </td>
                        <td data-testid={`budget-remaining-${project.projectId}`}>
                          {limit > 0 ? formatMoney(project.remainingCents, currency) : '—'}
                        </td>
                        <td>
                          <button
                            className="link"
                            data-testid={`budget-edit-${project.projectId}`}
                            onClick={() => setEditing(project)}
                          >
                            {t('budgets.setBudget')}
                          </button>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            )}
          </div>
          <p className="muted">{t('budgets.ladderNote')}</p>
        </>
      )}

      {editing && (
        <ProjectBudgetDialog
          orgId={orgId}
          project={editing}
          onClose={() => setEditing(null)}
          onDone={() => {
            setEditing(null);
            void loadProjects();
          }}
        />
      )}
    </div>
  );
}

/**
 * ProjectBudgetDialog sets one project's monthly budget.
 *
 * It writes the project quota's spend dimension only: the service quotas
 * (concurrency, batch, output tokens) are a different concern with their own
 * editor, and mixing them into a money dialog would invite an accidental reset.
 */
function ProjectBudgetDialog({
  orgId,
  project,
  onClose,
  onDone,
}: {
  orgId: string;
  project: ProjectBudget;
  onClose: () => void;
  onDone: () => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const [amount, setAmount] = useState(
    (Number(project.monthlySpendLimitCents || 0) / 100).toString(),
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const cents = Math.round(Number(amount || '0') * 100);

  const submit = async () => {
    setSaving(true);
    setError('');
    try {
      await api.patch(`/api/v1/admin/tenancy/projects/${project.projectId}/quota`, orgId, {
        monthlySpendLimitCents: Number.isFinite(cents) && cents > 0 ? cents : 0,
      });
      onDone();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : t('budgets.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog
      title={t('budgets.dialogTitle', { project: project.displayName })}
      onClose={onClose}
      testId="budget-dialog"
    >
      {error && <ErrorBanner message={error} />}
      <div className="form">
        <p className="muted" data-testid="budget-dialog-current">
          {t('budgets.dialogCurrent', {
            spent: formatMoney(project.spentThisCycleCents),
            limit:
              Number(project.monthlySpendLimitCents || 0) > 0
                ? formatMoney(project.monthlySpendLimitCents)
                : t('common.unlimited'),
          })}
        </p>
        <label>
          {t('budgets.amount', { currency: displayCurrencyLabel() })}
          <input
            data-testid="budget-amount"
            inputMode="decimal"
            value={amount}
            onChange={(e) => setAmount(e.target.value)}
          />
          <span className="field-hint">{t('budgets.amountHint')}</span>
        </label>
      </div>
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button data-testid="budget-save" disabled={saving} onClick={() => void submit()}>
          {t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}
