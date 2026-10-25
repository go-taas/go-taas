// Admin Invoices page (feature-14): invoice list, generate, download.

import { useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { formatTime } from '../api';
import { useI18n } from '../i18n';

interface Invoice {
  invoiceId: string;
  billId: string;
  organizationId: string;
  periodStart: string;
  periodEnd: string;
  totalCents: string;
  currency: string;
  status: string;
  issuedAt: string;
  paidAt: string;
}

export default function BillingInvoicesPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t, tEnum } = useI18n();
  const [invoices, setInvoices] = useState<Invoice[]>([]);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .get<{ invoices?: Invoice[] }>('/api/v1/admin/billing/invoices?page.limit=100', orgId)
      .then((data) => setInvoices(data.invoices || []))
      .catch((e) => setError(e instanceof Error ? e.message : t('invoices.loadFailed')));
  }, [api, orgId, t]);

  const download = async (invoiceId: string) => {
    try {
      const data = await api.get<{ content?: string }>(
        `/api/v1/admin/billing/invoices/${invoiceId}:download`,
        orgId,
      );
      window.alert(data.content || t('common.noContent'));
    } catch (e) {
      setError(e instanceof Error ? e.message : t('invoices.downloadFailed'));
    }
  };

  return (
    <div className="page">
      <h1>{t('invoices.title')}</h1>
      {error && <div className="error">{error}</div>}
      {invoices.length === 0 ? (
        <div className="empty" data-testid="invoices-empty">{t('invoices.empty')}</div>
      ) : (
        <table className="table" data-testid="invoices-table">
          <thead>
            <tr>
              <th>{t('invoices.colPeriod')}</th>
              <th>{t('invoices.colTotal')}</th>
              <th>{t('invoices.colStatus')}</th>
              <th>{t('invoices.colIssued')}</th>
              <th>{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {invoices.map((inv) => (
              <tr key={inv.invoiceId} data-testid={`invoice-row-${inv.invoiceId}`}>
                <td>{formatTime(inv.periodStart)} – {formatTime(inv.periodEnd)}</td>
                <td>{inv.totalCents} {inv.currency}</td>
                <td>{tEnum('state', inv.status)}</td>
                <td>{formatTime(inv.issuedAt)}</td>
                <td>
                  <button data-testid={`invoice-download-${inv.invoiceId}`} onClick={() => void download(inv.invoiceId)}>
                    {t('invoices.download')}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
