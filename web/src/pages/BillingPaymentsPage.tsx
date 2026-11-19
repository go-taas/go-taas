// Admin Payments page (feature-14): payment channels and payment intents.

import { useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { formatMoney } from '../display';
import { formatTime, type PageMeta } from '../api';
import { Pagination } from '../components';

const PAGE_SIZE = 20;

interface PaymentChannel {
  channelId: string;
  type: string;
  displayName: string;
  enabled: boolean;
  createdAt: string;
}

interface PaymentIntent {
  intentId: string;
  accountId: string;
  amountCents: string;
  channel: string;
  status: string;
  idempotencyKey: string;
  reference: string;
  createdAt: string;
  paidAt: string;
  payUrl: string;
}

export default function BillingPaymentsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t, tEnum } = useI18n();
  const [channels, setChannels] = useState<PaymentChannel[]>([]);
  const [intents, setIntents] = useState<PaymentIntent[]>([]);
  const [intentTotal, setIntentTotal] = useState(0);
  const [intentOffset, setIntentOffset] = useState(0);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .get<{ channels?: PaymentChannel[] }>('/api/v1/admin/billing/payment-channels?page.limit=100', orgId)
      .then((data) => setChannels(data.channels || []))
      .catch((e) => setError(e instanceof Error ? e.message : t('payments.loadFailed')));
  }, [api, orgId, t]);

  useEffect(() => {
    const params = new URLSearchParams({
      'page.offset': String(intentOffset),
      'page.limit': String(PAGE_SIZE),
    });
    api
      .get<{ intents?: PaymentIntent[]; pageMeta?: PageMeta }>(
        `/api/v1/admin/billing/payment-intents?${params.toString()}`,
        orgId,
      )
      .then((data) => {
        setIntents(data.intents || []);
        setIntentTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
      })
      .catch(() => {
        // Intents may be empty; not fatal.
      });
  }, [api, orgId, intentOffset, t]);

  // A different organization is a different ledger: start at its first page.
  useEffect(() => {
    setIntentOffset(0);
  }, [orgId]);

  const payIntent = async (intentId: string) => {
    try {
      await api.post(`/api/v1/admin/billing/payment-intents/${intentId}:pay`, orgId, {});
      setIntents((prev) =>
        prev.map((i) => (i.intentId === intentId ? { ...i, status: 'paid' } : i)),
      );
    } catch (e) {
      setError(e instanceof Error ? e.message : t('payments.paymentFailed'));
    }
  };

  return (
    <div className="page">
      <h1>{t('payments.title')}</h1>
      {error && <div className="error">{error}</div>}
      <h2>{t('payments.channels')}</h2>
      <table className="table" data-testid="payments-table">
        <thead>
          <tr>
            <th>{t('payments.colChannel')}</th>
            <th>{t('payments.colType')}</th>
            <th>{t('payments.colStatus')}</th>
          </tr>
        </thead>
        <tbody>
          {channels.map((c) => (
            <tr key={c.channelId}>
              <td>{c.displayName}</td>
              <td>{tEnum('state', c.type)}</td>
              <td>{c.enabled ? t('common.enabled') : t('common.disabled')}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <h2>{t('payments.intents')}</h2>
      {intents.length === 0 ? (
        <div className="empty">{t('payments.intentsEmpty')}</div>
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>{t('payments.colAmount')}</th>
              <th>{t('payments.colChannel')}</th>
              <th>{t('payments.colStatus')}</th>
              <th>{t('payments.colCreated')}</th>
              <th>{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {intents.map((i) => (
              <tr key={i.intentId} data-testid={`payment-intent-row-${i.intentId}`}>
                <td>{formatMoney(i.amountCents)}</td>
                <td>{i.channel}</td>
                <td data-testid={`payment-intent-status-${i.intentId}`}>{tEnum('state', i.status)}</td>
                <td>{formatTime(i.createdAt)}</td>
                <td>
                  {i.status === 'pending' && (
                    <button
                      data-testid={`payment-intent-pay-${i.intentId}`}
                      onClick={() => void payIntent(i.intentId)}
                    >
                      {t('payments.simulate')}
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {intentTotal > PAGE_SIZE && (
        <Pagination
          offset={intentOffset}
          limit={PAGE_SIZE}
          total={intentTotal}
          onPageChange={setIntentOffset}
        />
      )}
    </div>
  );
}
