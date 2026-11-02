// QuotaDimensionCard renders one quota dimension card for the /quotas
// page (feature #51, design §5.4): name, plain-language description,
// usage/limit pair (or "Unlimited"), a progress bar (amber ≥ 80%, red
// 100%), and for the token cap a constant display.

import { useI18n } from '../../../i18n';
import type { QuotaUsage } from '../../../api';

// QuotaLimits is the shared shape of the organization, personal and
// effective quota projections: every dimension is a string on the wire.
export interface QuotaLimits {
  maxConcurrentRequests: string;
  maxBatchJobsPerDay: string;
  maxOutputTokensPerRequest: string;
}

export type QuotaDimension =
  | 'concurrent_requests'
  | 'batch_jobs_today'
  | 'output_tokens_per_request';

export default function QuotaDimensionCard({
  dimension,
  quota,
  usage,
  label,
}: {
  dimension: QuotaDimension;
  quota: QuotaLimits;
  usage?: QuotaUsage;
  label?: string;
}) {
  const { t } = useI18n();

  const limit = parseInt(
    dimension === 'concurrent_requests'
      ? quota.maxConcurrentRequests
        : dimension === 'batch_jobs_today'
          ? quota.maxBatchJobsPerDay
          : quota.maxOutputTokensPerRequest,
    10,
  );
  const current = parseInt(
    dimension === 'concurrent_requests'
      ? usage?.concurrentRequests || '0'
        : dimension === 'batch_jobs_today'
          ? usage?.batchJobsToday || '0'
          : '0',
    10,
  );

  const unlimited = limit === 0;
  const hasUsage = Boolean(usage);
  const pct = unlimited || !hasUsage ? 0 : Math.min(100, Math.round((current / limit) * 100));
  const barClass = pct >= 100 ? 'quota-bar-full' : pct >= 80 ? 'quota-bar-warn' : '';

  return (
    <div className="card" data-testid={`quota-card-${dimension}`}>
      <div className="card-label">{label || t(`quota.dimension.${dimension}`)}</div>
      <div className="card-note">{t(`quota.dimensionDesc.${dimension}`)}</div>
      <div className="card-value" data-testid={`quota-card-usage-${dimension}`}>
        {unlimited ? t('quota.unlimited') : hasUsage ? `${current} / ${limit}` : String(limit)}
      </div>
      {!unlimited && hasUsage && (
        <div className="quota-bar">
          <div className={`quota-bar-fill ${barClass}`} style={{ width: `${pct}%` }} />
        </div>
      )}
    </div>
  );
}