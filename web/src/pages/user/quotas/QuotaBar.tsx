// QuotaBar renders the compact per-dimension quota chips in the
// end-user org context area (feature #51, design §5.4): four chips
// ("In flight 12/50", "Services 3/20", "Batch today 5/50", "Tokens ≤
// 8192", or "Unlimited" per chip), amber ≥ 80%, red 100%. It hides
// entirely when every dimension is unlimited (the default). A failed
// poll keeps the last good chips (the stale convention).

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../../../surface';
import { useOrg } from '../../../org';
import { useI18n } from '../../../i18n';
import { navigate } from '../../../router';
import type { GetMyQuotaResponse } from '../../../api';

export default function QuotaBar() {
  const { t } = useI18n();
  const api = useApi();
  const { orgId } = useOrg();
  const [data, setData] = useState<GetMyQuotaResponse | null>(null);

  const load = useCallback(async () => {
    if (!orgId) return;
    try {
      const res = await api.get<GetMyQuotaResponse>('/api/v1/quotas', orgId);
      setData(res);
    } catch {
      // Keep the last good data (stale convention).
    }
  }, [api, orgId]);

  useEffect(() => {
    void load();
    const id = setInterval(() => void load(), 30000);
    return () => clearInterval(id);
  }, [load]);

  if (!data || !data.quota || !data.usage) return null;
  const q = data.quota;
  const u = data.usage;
  const allUnlimited =
    parseInt(q.maxConcurrentRequests, 10) === 0 &&
    parseInt(q.maxInferenceServices, 10) === 0 &&
    parseInt(q.maxBatchJobsPerDay, 10) === 0 &&
    parseInt(q.maxOutputTokensPerRequest, 10) === 0;
  if (allUnlimited) return null;

  const chips: { dim: string; label: string; current: number; limit: number; token?: boolean }[] = [
    {
      dim: 'concurrent_requests',
      label: t('quota.barInFlight'),
      current: parseInt(u.concurrentRequests, 10),
      limit: parseInt(q.maxConcurrentRequests, 10),
    },
    {
      dim: 'inference_services',
      label: t('quota.barServices'),
      current: parseInt(u.inferenceServices, 10),
      limit: parseInt(q.maxInferenceServices, 10),
    },
    {
      dim: 'batch_jobs_today',
      label: t('quota.barBatch'),
      current: parseInt(u.batchJobsToday, 10),
      limit: parseInt(q.maxBatchJobsPerDay, 10),
    },
    {
      dim: 'output_tokens_per_request',
      label: t('quota.barTokens'),
      current: 0,
      limit: parseInt(q.maxOutputTokensPerRequest, 10),
      token: true,
    },
  ];

  return (
    <div className="quota-bar-chips" data-testid="quota-bar" onClick={() => navigate('/quotas')}>
      {chips.map((c) => {
        const unlimited = c.limit === 0;
        const pct = unlimited ? 0 : Math.min(100, Math.round((c.current / c.limit) * 100));
        const cls = pct >= 100 ? 'quota-chip-full' : pct >= 80 ? 'quota-chip-warn' : '';
        return (
          <span key={c.dim} className={`quota-chip ${cls}`} data-testid={`quota-bar-chip-${c.dim}`}>
            {c.label} {unlimited ? t('quota.unlimited') : c.token ? `≤ ${c.limit}` : `${c.current}/${c.limit}`}
          </span>
        );
      })}
    </div>
  );
}