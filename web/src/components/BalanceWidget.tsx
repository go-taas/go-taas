// BalanceWidget shows the org's billing account snapshot beside the
// usage dashboard (feature #9, AD9). It reuses the billing GetBalance
// API unchanged: prepaid shows the balance, postpaid shows month-to-date
// spend vs quota; an org without an account renders a muted empty state.

import { useEffect, useState } from 'react';
import { api, type BalanceResponse } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { formatMoney } from '../display';

export default function BalanceWidget() {
  const { t } = useI18n();
  const { orgId } = useOrg();
  const [balance, setBalance] = useState<BalanceResponse | null>(null);
  const [noAccount, setNoAccount] = useState(false);

  // A balance belongs to one organization. The platform console has no
  // organization of its own, so there is nothing to show and nothing to
  // ask for: a platform administrator reads the per-organization accounts
  // on the billing-accounts page instead.
  const scoped = Boolean(orgId);

  useEffect(() => {
    setNoAccount(false);
    setBalance(null);
    if (!scoped) return undefined;
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
  }, [orgId, scoped]);

  if (!scoped || noAccount) {
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

  if (balance.mode === 'postpaid') {
    const quota = parseInt(balance.monthlyQuotaCents || '0', 10);
    const used = parseInt(balance.usedThisCycleCents || '0', 10);
    const pct = quota > 0 ? Math.round((used / quota) * 100) : 0;
    return (
      <div className="balance-widget" data-testid="usage-balance-widget">
        <span className="balance-widget-label">{t('balance.spendThisMonth')}</span>
        <strong>
          {formatMoney(String(used))}
        </strong>
        {quota > 0 && (
          <span className="muted">
            {' '}
            / {formatMoney(String(quota))} ({pct}%)
          </span>
        )}
      </div>
    );
  }
  return (
    <div className="balance-widget" data-testid="usage-balance-widget">
      <span className="balance-widget-label">{t('balance.balance')}</span>
      <strong>
        {formatMoney(balance.balanceCents)}
      </strong>
    </div>
  );
}