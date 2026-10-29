// Pricing page: the model x accelerator price matrix with a set-price
// dialog (flat or volume tiers), a history toggle and inline 10507
// errors.
// Implements docs/design/pricing.md FR1/FR2, AC14.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  formatTime,
  type ModelSummary,
  type PageMeta,
  type PriceEntry,
  type PriceTier,
} from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { useFleetAccelerators } from '../hooks/useFleetAccelerators';
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

// DEFAULT_CARD is the price row that applies to every card type a model runs
// on. The pricing matrix falls back to it when no card-specific row matches,
// so it is offered next to the real card types.
const DEFAULT_CARD = 'default';

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
  // The catalog resolves a price row's model id to a readable name; the same
  // list fills the dialog's model picker.
  const [models, setModels] = useState<ModelSummary[]>([]);

  useEffect(() => {
    api
      .get<{ models?: ModelSummary[] }>('/api/v1/admin/models?page.limit=200', orgId)
      .then((data) => setModels(data.models || []))
      .catch(() => setModels([]));
  }, [api, orgId]);

  const modelName = useCallback(
    (id: string) => models.find((m) => m.modelId === id)?.name || id,
    [models],
  );

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
                    <strong>{modelName(p.modelId)}</strong>
                    <div className="muted small mono">{p.modelId}</div>
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
          models={models}
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
  models,
  onClose,
  onSaved,
}: {
  orgId: string;
  models: ModelSummary[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [modelId, setModelId] = useState('');
  const [card, setCard] = useState(DEFAULT_CARD);
  const [inputRate, setInputRate] = useState('');
  const [outputRate, setOutputRate] = useState('');
  const [cachedRate, setCachedRate] = useState('0');
  const [tiers, setTiers] = useState<PriceTier[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  // The model and the card type are picked from what the platform actually
  // has, not typed from memory: a price row pointing at a model id or card
  // name that does not exist prices nothing at all, and the operator would
  // only discover it when settlement silently left the usage unpriced.
  const fleet = useFleetAccelerators(orgId);

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
        // No currency is sent: a price is authored in the installation's
        // settlement currency, which the server knows (billing.currency). The
        // console prints it in the display currency, so the two must agree —
        // the system-preferences page warns when they do not, and picking a
        // currency here would let a price be stored in a unit no one converts.
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
          <select
            data-testid="set-price-model"
            value={modelId}
            onChange={(e) => setModelId(e.target.value)}
          >
            <option value="">{t('pricing.selectModel')}</option>
            {models.map((m) => (
              <option key={m.modelId} value={m.modelId}>
                {m.name}
                {m.external ? ` (${t('modeldetail.external')})` : ''}
              </option>
            ))}
          </select>
          {models.length === 0 && (
            <span className="field-hint">{t('pricing.noModels')}</span>
          )}
        </label>
        <label>
          {t('pricing.fieldCard')}
          <select
            data-testid="set-price-card"
            value={card}
            onChange={(e) => setCard(e.target.value)}
          >
            <option value={DEFAULT_CARD}>{t('pricing.cardDefault')}</option>
            {fleet.cardTypes.map((c) => (
              <option key={`${c.vendor}:${c.cardType}`} value={c.cardType}>
                {c.cardType}
              </option>
            ))}
          </select>
          <span className="field-hint">{t('pricing.cardHint')}</span>
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
