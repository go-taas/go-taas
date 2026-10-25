// CostBreakdownTable renders the cost dimension breakdown (feature #29,
// AD5): the dimension values with cost, tokens, cost-per-token and share
// bars. cost_per_token and share_pct are derived client-side.

import type { CostBreakdownRow } from '../api';
import { useI18n } from '../i18n';

// costDollars formats integer cents as dollars.
function costDollars(cents: string): string {
  return `$${(parseInt(cents || '0', 10) / 100).toFixed(2)}`;
}

// costPerToken derives cost-per-token as cents per 1000 tokens.
function costPerToken(row: CostBreakdownRow): string {
  const tokens = parseInt(row.totalTokens || '0', 10);
  if (tokens === 0) return '—';
  const cents = parseInt(row.totalCostCents || '0', 10);
  return `$${((cents / tokens) * 1000 / 100).toFixed(4)}/1k`;
}

export default function CostBreakdownTable({
  rows,
  onSelect,
}: {
  rows: CostBreakdownRow[];
  onSelect?: (value: string) => void;
}) {
  const { t } = useI18n();
  if (rows.length === 0) {
    return <div className="empty-state">{t('cost.empty')}</div>;
  }
  return (
    <table className="data" data-testid="cost-breakdown">
      <thead>
        <tr>
          <th>{t('cost.colDimensionValue')}</th>
          <th>{t('cost.colCost')}</th>
          <th>{t('cost.colTokens')}</th>
          <th>{t('cost.colCostPerToken')}</th>
          <th>{t('common.shareCol')}</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => {
          const share = parseInt(r.sharePct || '0', 10);
          return (
            <tr key={r.dimensionValue} data-testid={`cost-breakdown-row-${r.dimensionValue}`}>
              <td>
                <button
                  className="link"
                  data-testid={`cost-breakdown-link-${r.dimensionValue}`}
                  onClick={() => onSelect?.(r.dimensionValue)}
                >
                  {r.dimensionName || r.dimensionValue}
                </button>
              </td>
              <td>{costDollars(r.totalCostCents)}</td>
              <td>{parseInt(r.totalTokens || '0', 10).toLocaleString()}</td>
              <td>{costPerToken(r)}</td>
              <td>
                <div className="share-bar">
                  <div className="share-bar-fill" style={{ width: `${Math.min(share, 100)}%` }} />
                </div>
                <span className="muted">{share}%</span>
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}