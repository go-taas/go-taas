// UserQuotasPage is the end-user console "My quotas" page (feature #51,
// design §5.4): four QuotaDimensionCards, a Request-increase mailto
// (hidden when the operator mailbox is empty), and the note that quotas
// are set by the platform operator.

import { useCallback, useEffect, useState } from 'react';
import { ErrorBanner } from '../../components';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import type { GetMyQuotaResponse, MyProjectQuota } from '../../api';
import { formatMoney, quotaRemaining } from '../../display';
import QuotaDimensionCard, { type QuotaLimits as QuotaLimitsLike } from './quotas/QuotaDimensionCard';

export default function UserQuotasPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [data, setData] = useState<GetMyQuotaResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    if (!orgId) return;
    setLoading(true);
    setError('');
    try {
      const res = await api.get<GetMyQuotaResponse>('/api/v1/quotas', orgId);
      setData(res);
    } catch (e: any) {
      setError(e?.message || t('quota.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const q = data?.quota;
  const u = data?.usage;
  const personal = data?.personalQuota;
  const effective = data?.effectiveQuota;
  // The effective tuple is what actually applies: an unlimited
  // organization budget with a personal cap is a limited account, so the
  // empty state must look at the effective value, not the org row.
  const effectiveUnlimited = (quota?: QuotaLimitsLike | null) =>
    !quota ||
    (parseInt(quota.maxConcurrentRequests, 10) === 0 &&
      parseInt(quota.maxBatchJobsPerDay, 10) === 0 &&
      parseInt(quota.maxOutputTokensPerRequest, 10) === 0);
  const allUnlimited = q && effectiveUnlimited(effective) && effectiveUnlimited(q);

  // A project budget the member did not set themselves inherits the value the
  // ladder resolves, so the remaining column reports what is still spendable
  // there instead of "unlimited".
  const projectRemaining = (row: MyProjectQuota) => {
    const left = quotaRemaining(
      row.quota.monthlySpendLimitCents,
      row.effective.monthlySpendLimitCents,
      row.spentThisCycleCents,
    );
    return left.limitCents > 0
      ? formatMoney(left.remainingCents, data?.currency)
      : t('common.unlimited');
  };

  const mailto = data?.requestIncreaseEmail
    ? `mailto:${data.requestIncreaseEmail}?subject=${encodeURIComponent(t('quota.increaseSubject', { org: q?.organizationName || '' }))}&body=${encodeURIComponent(t('quota.increaseBody', { org: q?.organizationName || '' }))}`
    : '';

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('quota.myTitle')}</h1>
          <div className="subtitle">{t('quota.mySubtitle')}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      {loading && !data ? (
        <div className="loading">{t('common.loading')}</div>
      ) : !q ? (
        <div className="empty-state" data-testid="quota-error">
          {t('quota.loadFailed')}
        </div>
      ) : allUnlimited ? (
        <div className="empty-state" data-testid="quota-unlimited-empty">
          {t('quota.unlimitedEmpty')}
        </div>
      ) : (
        <>
          <div className="cards-row" data-testid="quota-cards">
            <QuotaDimensionCard dimension="concurrent_requests" quota={q} usage={u!} />
            <QuotaDimensionCard dimension="batch_jobs_today" quota={q} usage={u!} />
            <QuotaDimensionCard dimension="output_tokens_per_request" quota={q} usage={u!} />
          </div>
          {mailto && (
            <a className="button" data-testid="quota-request-increase" href={mailto}>
              {t('quota.requestIncrease')}
            </a>
          )}
          {personal && (
            <>
              <h2>{t('quota.personalTitle')}</h2>
              <div className="hint" data-testid="quota-personal-budget">
                {t('quota.spentThisCycle', {
                  spent: formatMoney(data?.personalSpentThisCycleCents, data?.currency),
                  remaining:
                    Number(personal.monthlySpendLimitCents || '0') > 0
                      ? formatMoney(data?.personalRemainingCents, data?.currency)
                      : t('common.unlimited'),
                })}
              </div>
              <div className="cards-row" data-testid="personal-quota-cards">
                <QuotaDimensionCard dimension="concurrent_requests" quota={personal} label={t('quota.personalLabel')} />
                <QuotaDimensionCard dimension="batch_jobs_today" quota={personal} label={t('quota.personalLabel')} />
                <QuotaDimensionCard dimension="output_tokens_per_request" quota={personal} label={t('quota.personalLabel')} />
              </div>
            </>
          )}
          {effective && (
            <>
              <h2>{t('quota.effectiveTitle')}</h2>
              <div className="cards-row" data-testid="effective-quota-cards">
                <QuotaDimensionCard dimension="concurrent_requests" quota={effective} label={t('quota.effectiveLabel')} />
                <QuotaDimensionCard dimension="batch_jobs_today" quota={effective} label={t('quota.effectiveLabel')} />
                <QuotaDimensionCard dimension="output_tokens_per_request" quota={effective} label={t('quota.effectiveLabel')} />
              </div>
            </>
          )}
          {data?.projectMemberQuotas && data.projectMemberQuotas.length > 0 && (
            <>
              <h2>{t('quota.projectTitle')}</h2>
              <div className="subtitle">{t('quota.projectSubtitle')}</div>
              <table className="data" data-testid="project-member-quota-table">
                <thead>
                  <tr>
                    <th>{t('quota.colProject')}</th>
                    <th>{t('quota.colConcurrent')}</th>
                    <th>{t('quota.colSpend')}</th>
                    <th>{t('quota.colSpent')}</th>
                    <th>{t('quota.colRemaining')}</th>
                    <th>{t('quota.colEffective')}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.projectMemberQuotas.map((row) => (
                    <tr key={row.projectId} data-testid={`project-quota-row-${row.projectId}`}>
                      <td>
                        <strong>{row.projectName || row.projectId}</strong>
                        <div className="muted mono">{row.role}</div>
                      </td>
                      <td>{t('quota.limitOrUnlimited', { n: row.quota.maxConcurrentRequests })}</td>
                      <td>{t('quota.limitOrUnlimited', { n: row.quota.monthlySpendLimitCents })}</td>
                      <td data-testid={`project-spent-${row.projectId}`}>
                        {formatMoney(row.spentThisCycleCents, data.currency)}
                      </td>
                      <td data-testid={`project-remaining-${row.projectId}`}>
                        {projectRemaining(row)}
                      </td>
                      <td data-testid={`project-effective-${row.projectId}`}>
                        {t('quota.effectiveValue', {
                          concurrent: row.effective.maxConcurrentRequests,
                          spend: row.effective.monthlySpendLimitCents,
                        })}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </>
          )}
          <p className="muted">{t('quota.operatorNote')}</p>
        </>
      )}
    </div>
  );
}