// Pricing page: the model x accelerator price matrix with a set-price
// dialog (flat or volume tiers), a history toggle and inline 10507
// errors.
// Implements docs/design/pricing.md FR1/FR2, AC14.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  formatTime,
  type PageMeta,
  type PriceEntry,
  type PriceTier,
} from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, Pagination, usePolling } from '../components';

interface PricesResponse {
  response: { code: number; message: string };
  prices: PriceEntry[];
  pageMeta?: PageMeta;
}

interface SetPriceResponse {
  response: { code: number; message: string };
  priceId: string;
}

const PAGE_SIZE = 20;

// SUPPORTED_CURRENCIES is the set of currencies the set-price dialog
// offers. It mirrors the backend billing.supportedCurrencies config.
const SUPPORTED_CURRENCIES = ['USD', 'CNY'];

// tierLabel renders one tier's bound compactly (1.2M style).
function tierBound(tokens: number): string {
  if (tokens >= 1_000_000) {
    const m = tokens / 1_000_000;
    return `${Number.isInteger(m) ? m : m.toFixed(1)}M`;
  }
  if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}K`;
  return String(tokens);
}

export default function PricingPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [prices, setPrices] = useState<PriceEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [includeHistory, setIncludeHistory] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [dialogOpen, setDialogOpen] = useState(false);

  const load = useCallback(async () => {
    setError('');
    try {
      const params = new URLSearchParams({
        'page.offset': String(offset),
        'page.limit': String(PAGE_SIZE),
      });
      if (includeHistory) params.set('include_history', 'true');
      const data = await api.get<PricesResponse>(
        `/api/v1/admin/billing/prices?${params.toString()}`,
        orgId,
      );
      setPrices(data.prices || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('pricing.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, includeHistory, t]);

  useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  // FR2.3: refresh while the page is visible.
  usePolling(() => void load(), 60_000, true);

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('pricing.title')}</h1>
          <div className="subtitle">{t('pricing.subtitle')}</div>
        </div>
        <button data-testid="pricing-set-button" onClick={() => setDialogOpen(true)}>
          {t('pricing.setPrice')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="toolbar" style={{ marginBottom: 14 }} data-testid="pricing-view-toggle">
        <button
          className={includeHistory ? 'secondary' : ''}
          data-testid="pricing-view-current"
          onClick={() => {
            setOffset(0);
            setIncludeHistory(false);
          }}
        >
          {t('pricing.current')}
        </button>
        <button
          className={includeHistory ? '' : 'secondary'}
          data-testid="pricing-view-history"
          onClick={() => {
            setOffset(0);
            setIncludeHistory(true);
          }}
        >
          {t('pricing.history')}
        </button>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : prices.length === 0 ? (
          <div className="empty-state" data-testid="pricing-empty">
            {t('pricing.empty')}
          </div>
        ) : (
          <table className="data" data-testid="pricing-table">
            <thead>
              <tr>
                <th>{t('pricing.colModel')}</th>
                <th>{t('pricing.colCard')}</th>
                <th>{t('pricing.colInput')}</th>
                <th>{t('pricing.colOutput')}</th>
                <th>{t('pricing.colCached')}</th>
                <th>{t('pricing.colTiers')}</th>
                <th>{t('pricing.colCurrency')}</th>
                <th>{t('pricing.colEffective')}</th>
              </tr>
            </thead>
            <tbody>
              {prices.map((p) => (
                <tr
                  key={p.priceId}
                  data-testid={`pricing-row-${p.modelId}-${p.acceleratorType}`}
                >
                  <td>
                    <strong className="mono">{p.modelId}</strong>
                  </td>
                  <td className="mono">{p.acceleratorType}</td>
                  <td>{p.inputPricePerMillion}</td>
                  <td>{p.outputPricePerMillion}</td>
                  <td>{p.cachedPricePerMillion}</td>
                  <td>
                    {p.tiers && p.tiers.length > 0 ? (
                      <span className="mono" title={p.tiers
                        .map((t) => `≤${tierBound(Number(t.upToTokens) || 0)}: ${t.inputPricePerMillion}/${t.outputPricePerMillion}`)
                        .join('  ')}>
                        {t('pricing.tiersCount', { n: p.tiers.length })}
                      </span>
                    ) : (
                      <span className="muted">flat</span>
                    )}
                  </td>
                  <td>{p.currency}</td>
                  <td>{formatTime(p.effectiveFrom)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {total > PAGE_SIZE && (
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        )}
      </div>

      {dialogOpen && (
        <SetPriceDialog
          orgId={orgId}
          onClose={() => setDialogOpen(false)}
          onSaved={() => {
            setDialogOpen(false);
            void load();
          }}
        />
      )}
    </div>
  );
}

// SetPriceDialog creates or updates one matrix cell (FR1). Validation
// failures (10507) render inline.
function SetPriceDialog({
  orgId,
  onClose,
  onSaved,
}: {
  orgId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [modelId, setModelId] = useState('');
  const [card, setCard] = useState('');
  const [inputRate, setInputRate] = useState('');
  const [outputRate, setOutputRate] = useState('');
  const [cachedRate, setCachedRate] = useState('0');
  const [currency, setCurrency] = useState('USD');
  const [tiers, setTiers] = useState<PriceTier[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const addTier = () => {
    setTiers((prev) => [
      ...prev,
      { upToTokens: '', inputPricePerMillion: 0, outputPricePerMillion: 0 },
    ]);
  };

  const save = async () => {
    setSaving(true);
    setError('');
    try {
      const body: Record<string, unknown> = {
        modelId: modelId.trim(),
        acceleratorType: card.trim(),
        inputPricePerMillion: Number(inputRate) || 0,
        outputPricePerMillion: Number(outputRate) || 0,
        cachedPricePerMillion: Number(cachedRate) || 0,
        currency,
      };
      if (tiers.length > 0) {
        body.tiers = tiers.map((t) => ({
          upToTokens: Number(t.upToTokens) || 0,
          inputPricePerMillion: Number(t.inputPricePerMillion) || 0,
          outputPricePerMillion: Number(t.outputPricePerMillion) || 0,
        }));
      }
      await api.put<SetPriceResponse>('/api/v1/admin/billing/prices', orgId, body);
      onSaved();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('pricing.saveFailed'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog title={t('pricing.setTitle')} onClose={onClose} testId="set-price-dialog">
      {error && <ErrorBanner message={error} />}
      <div className="form-grid">
        <label>
          {t('pricing.fieldModel')}
          <input
            data-testid="set-price-model"
            value={modelId}
            onChange={(e) => setModelId(e.target.value)}
            placeholder={t('pricing.placeholderModel')}
          />
        </label>
        <label>
          {t('pricing.fieldCard')}
          <input
            data-testid="set-price-card"
            value={card}
            onChange={(e) => setCard(e.target.value)}
            placeholder={t('pricing.placeholderCard')}
          />
        </label>
        <label>
          {t('pricing.fieldInput')}
          <input
            data-testid="set-price-input-rate"
            type="number"
            min="0"
            step="0.01"
            value={inputRate}
            onChange={(e) => setInputRate(e.target.value)}
          />
        </label>
        <label>
          {t('pricing.fieldOutput')}
          <input
            data-testid="set-price-output-rate"
            type="number"
            min="0"
            step="0.01"
            value={outputRate}
            onChange={(e) => setOutputRate(e.target.value)}
          />
        </label>
        <label>
          {t('pricing.fieldCached')}
          <input
            data-testid="set-price-cached-rate"
            type="number"
            min="0"
            step="0.01"
            value={cachedRate}
            onChange={(e) => setCachedRate(e.target.value)}
          />
        </label>
        <label>
          {t('pricing.fieldCurrency')}
          <select
            data-testid="set-price-currency"
            value={currency}
            onChange={(e) => setCurrency(e.target.value)}
          >
            {SUPPORTED_CURRENCIES.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
        </label>
      </div>

      <h3 style={{ marginTop: 16 }}>{t('pricing.tiersSection')}</h3>
      <div className="muted" style={{ marginBottom: 8 }}>
        {t('pricing.tiersNote')}
      </div>
      {tiers.map((tier, i) => (
        <div key={i} className="toolbar" style={{ marginBottom: 8 }} data-testid={`tier-row-${i}`}>
          <input
            style={{ width: 140 }}
            data-testid={`tier-bound-${i}`}
            type="number"
            min="0"
            placeholder={t('pricing.placeholderUpTo')}
            value={tier.upToTokens}
            onChange={(e) =>
              setTiers((prev) =>
                prev.map((x, j) => (j === i ? { ...x, upToTokens: e.target.value } : x)),
              )
            }
          />
          <input
            style={{ width: 120 }}
            data-testid={`tier-input-rate-${i}`}
            type="number"
            min="0"
            step="0.01"
            placeholder={t('pricing.placeholderIn')}
            value={tier.inputPricePerMillion || ''}
            onChange={(e) =>
              setTiers((prev) =>
                prev.map((x, j) =>
                  j === i ? { ...x, inputPricePerMillion: Number(e.target.value) || 0 } : x,
                ),
              )
            }
          />
          <input
            style={{ width: 120 }}
            data-testid={`tier-output-rate-${i}`}
            type="number"
            min="0"
            step="0.01"
            placeholder={t('pricing.placeholderOut')}
            value={tier.outputPricePerMillion || ''}
            onChange={(e) =>
              setTiers((prev) =>
                prev.map((x, j) =>
                  j === i ? { ...x, outputPricePerMillion: Number(e.target.value) || 0 } : x,
                ),
              )
            }
          />
          <button
            className="secondary"
            data-testid={`tier-remove-${i}`}
            onClick={() => setTiers((prev) => prev.filter((_, j) => j !== i))}
          >
            {t('common.remove')}
          </button>
        </div>
      ))}
      <button className="secondary" data-testid="tier-add" onClick={addTier}>
        {t('pricing.addTier')}
      </button>

      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
        <button
          data-testid="set-price-save"
          disabled={saving}
          onClick={() => void save()}
        >
          {saving ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}
