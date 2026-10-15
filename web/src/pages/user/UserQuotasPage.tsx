// UserQuotasPage is the end-user console "My quotas" page (feature #51,
// design §5.4): four QuotaDimensionCards, a Request-increase mailto
// (hidden when the operator mailbox is empty), and the note that quotas
// are set by the platform operator.

import { useCallback, useEffect, useState } from 'react';
import { ErrorBanner } from '../../components';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import type { GetMyQuotaResponse } from '../../api';
import QuotaDimensionCard from './quotas/QuotaDimensionCard';

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
  const allUnlimited =
    q &&
    parseInt(q.maxConcurrentRequests, 10) === 0 &&
    parseInt(q.maxInferenceServices, 10) === 0 &&
    parseInt(q.maxBatchJobsPerDay, 10) === 0 &&
    parseInt(q.maxOutputTokensPerRequest, 10) === 0;

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
            <QuotaDimensionCard dimension="inference_services" quota={q} usage={u!} />
            <QuotaDimensionCard dimension="batch_jobs_today" quota={q} usage={u!} />
            <QuotaDimensionCard dimension="output_tokens_per_request" quota={q} usage={u!} />
          </div>
          {mailto && (
            <a className="button" data-testid="quota-request-increase" href={mailto}>
              {t('quota.requestIncrease')}
            </a>
          )}
          <p className="muted">{t('quota.operatorNote')}</p>
        </>
      )}
    </div>
  );
}