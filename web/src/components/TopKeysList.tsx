// TopKeysList renders the top-keys ranking (feature #28, AD2): the top N
// keys by cost, each with a share bar. share_pct is derived client-side
// as key cost / total cost.

import type { UsageKeyTopRow } from '../api';
import { useI18n } from '../i18n';
import { formatMoney } from '../display';

// costDollars formats integer cents in the platform's display currency.
function costDollars(cents: string): string {
  return formatMoney(cents);
}

export default function TopKeysList({
  topKeys,
  onSelect,
}: {
  topKeys: UsageKeyTopRow[];
  onSelect?: (apiKeyId: string) => void;
}) {
  const { t } = useI18n();
  if (topKeys.length === 0) {
    return <div className="empty-state">{t('usageKeys.empty')}</div>;
  }
  return (
    <div data-testid="usage-keys-top">
      {topKeys.map((k) => {
        const share = parseInt(k.sharePct || '0', 10);
        return (
          <div
            key={k.apiKeyId}
            className="top-row"
            data-testid={`usage-keys-top-${k.apiKeyId}`}
          >
            <div className="top-row-main">
              <button
                className="link"
                data-testid={`usage-keys-top-link-${k.apiKeyId}`}
                onClick={() => onSelect?.(k.apiKeyId)}
              >
                {k.apiKeyName || k.apiKeyId}
              </button>
              <span className="muted mono">{costDollars(k.totalCostCents)}</span>
            </div>
            <div className="share-bar">
              <div className="share-bar-fill" style={{ width: `${Math.min(share, 100)}%` }} />
            </div>
            <div className="muted">{share}%</div>
          </div>
        );
      })}
    </div>
  );
}