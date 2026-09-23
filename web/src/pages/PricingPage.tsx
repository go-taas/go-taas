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
      setError(e instanceof Error ? e.message : 'failed to load prices');
    } finally {
      setLoading(false);
    }
  }, [orgId, offset, includeHistory]);

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
          <h1>Pricing</h1>
          <div className="subtitle">
            The model × accelerator price matrix. Prices are per one million
            tokens; the “default” card is the per-model fallback.
          </div>
        </div>
        <button data-testid="pricing-set-button" onClick={() => setDialogOpen(true)}>
          Set price
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
          Current
        </button>
        <button
          className={includeHistory ? '' : 'secondary'}
          data-testid="pricing-view-history"
          onClick={() => {
            setOffset(0);
            setIncludeHistory(true);
          }}
        >
          History
        </button>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">Loading…</div>
        ) : prices.length === 0 ? (
          <div className="empty-state" data-testid="pricing-empty">
            No prices configured. Set the first price with “Set price”.
          </div>
        ) : (
          <table className="data" data-testid="pricing-table">
            <thead>
              <tr>
                <th>Model</th>
                <th>Card</th>
                <th>Input / 1M</th>
                <th>Output / 1M</th>
                <th>Cached / 1M</th>
                <th>Tiers</th>
                <th>Currency</th>
                <th>Effective from</th>
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
                        {p.tiers.length} tiers
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
  const [modelId, setModelId] = useState('');
  const [card, setCard] = useState('');
  const [inputRate, setInputRate] = useState('');
  const [outputRate, setOutputRate] = useState('');
  const [cachedRate, setCachedRate] = useState('0');
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
      setError(e instanceof Error ? e.message : 'failed to save price');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog title="Set price" onClose={onClose} testId="set-price-dialog">
      {error && <ErrorBanner message={error} />}
      <div className="form-grid">
        <label>
          Model
          <input
            data-testid="set-price-model"
            value={modelId}
            onChange={(e) => setModelId(e.target.value)}
            placeholder="qwen-2.5-7b"
          />
        </label>
        <label>
          Accelerator card
          <input
            data-testid="set-price-card"
            value={card}
            onChange={(e) => setCard(e.target.value)}
            placeholder="A800 or default"
          />
        </label>
        <label>
          Input price / 1M tokens
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
          Output price / 1M tokens
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
          Cached price / 1M tokens
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

      <h3 style={{ marginTop: 16 }}>Volume tiers (optional)</h3>
      <div className="muted" style={{ marginBottom: 8 }}>
        Rates apply once the organization's month-to-date tokens for (model,
        card) pass each bound. The last tier is unbounded (leave 0).
      </div>
      {tiers.map((t, i) => (
        <div key={i} className="toolbar" style={{ marginBottom: 8 }} data-testid={`tier-row-${i}`}>
          <input
            style={{ width: 140 }}
            data-testid={`tier-bound-${i}`}
            type="number"
            min="0"
            placeholder="up to tokens"
            value={t.upToTokens}
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
            placeholder="in / 1M"
            value={t.inputPricePerMillion || ''}
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
            placeholder="out / 1M"
            value={t.outputPricePerMillion || ''}
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
            Remove
          </button>
        </div>
      ))}
      <button className="secondary" data-testid="tier-add" onClick={addTier}>
        Add tier
      </button>

      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>
          Cancel
        </button>
        <button
          data-testid="set-price-save"
          disabled={saving}
          onClick={() => void save()}
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </Dialog>
  );
}
