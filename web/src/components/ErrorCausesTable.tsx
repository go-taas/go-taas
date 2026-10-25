// ErrorCausesTable renders the top error causes (feature #31, AD3): the
// error codes with error count, error rate and share bars. error_rate and
// share_pct are derived client-side.

import type { ErrorCauseRow } from '../api';
import { useI18n } from '../i18n';

// errorRatePercent derives the error rate as a percentage.
function errorRatePercent(row: ErrorCauseRow): string {
  const req = parseInt(row.errorRate || '0', 10);
  return `${(req / 100).toFixed(1)}%`;
}

export default function ErrorCausesTable({
  rows,
  onSelect,
}: {
  rows: ErrorCauseRow[];
  onSelect?: (errorCode: string) => void;
}) {
  const { t } = useI18n();
  if (rows.length === 0) {
    return <div className="empty-state">{t('errors.empty')}</div>;
  }
  return (
    <table className="data" data-testid="errors-causes">
      <thead>
        <tr>
          <th>{t('errors.colCode')}</th>
          <th>{t('errors.colMessage')}</th>
          <th>{t('errors.colCount')}</th>
          <th>{t('errors.colRate')}</th>
          <th>{t('common.shareCol')}</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => {
          const share = parseInt(r.sharePct || '0', 10);
          return (
            <tr key={r.errorCode} data-testid={`errors-cause-${r.errorCode}`}>
              <td>
                <button
                  className="link"
                  data-testid={`errors-cause-link-${r.errorCode}`}
                  onClick={() => onSelect?.(r.errorCode)}
                >
                  {r.errorCode}
                </button>
              </td>
              <td>{r.errorMessage || '—'}</td>
              <td>{parseInt(r.errorCount || '0', 10).toLocaleString()}</td>
              <td>{errorRatePercent(r)}</td>
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