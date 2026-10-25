// BalanceWidget shows the org's billing account snapshot beside the
// usage dashboard (feature #9, AD9). It reuses the billing GetBalance
// API unchanged: prepaid shows the balance, postpaid shows month-to-date
// spend vs quota; an org without an account renders a muted empty state.

import { useEffect, useState } from 'react';
import { api, type BalanceResponse } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';

// formatCents renders integer minor units as a currency string.
function formatCents(cents: string): string {
  const n = parseInt(cents || '0', 10);
  return (n / 100).toFixed(2);
}

export default function BalanceWidget() {
  const { t } = useI18n();
  const { orgId } = useOrg();
  const [balance, setBalance] = useState<BalanceResponse | null>(null);
  const [noAccount, setNoAccount] = useState(false);

  useEffect(() => {
    setNoAccount(false);
    setBalance(null);
    api
      .get<BalanceResponse>('/api/v1/admin/billing/balance', orgId)
      .then((data) => setBalance(data))
      .catch((e) => {
        // 10503 (account not found) renders the empty state, not an error.
        const code = (e as { code?: number }).code;
        if (code === 10503) {
          setNoAccount(true);
        }
      });
  }, [orgId]);

  if (noAccount) {
    return (
      <div className="balance-widget muted" data-testid="usage-balance-widget">
        {t('balance.noAccount')}
      </div>
    );
  }
  if (!balance) {
    return (
      <div className="balance-widget muted" data-testid="usage-balance-widget">
        —
      </div>
    );
  }

  const currency = balance.currency || 'USD';
  if (balance.mode === 'postpaid') {
    const quota = parseInt(balance.monthlyQuotaCents || '0', 10);
    const used = parseInt(balance.usedThisCycleCents || '0', 10);
    const pct = quota > 0 ? Math.round((used / quota) * 100) : 0;
    return (
      <div className="balance-widget" data-testid="usage-balance-widget">
        <span className="balance-widget-label">{t('balance.spendThisMonth')}</span>
        <strong>
          {formatCents(String(used))} {currency}
        </strong>
        {quota > 0 && (
          <span className="muted">
            {' '}
            / {formatCents(String(quota))} {currency} ({pct}%)
          </span>
        )}
      </div>
    );
  }
  return (
    <div className="balance-widget" data-testid="usage-balance-widget">
      <span className="balance-widget-label">{t('balance.balance')}</span>
      <strong>
        {formatCents(balance.balanceCents)} {currency}
      </strong>
    </div>
  );
}