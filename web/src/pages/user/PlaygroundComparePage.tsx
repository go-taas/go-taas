// End-user model playground comparison (feature #35): compare multiple
// models side by side on the same prompt, with latency/token/cost per
// model and a comparison table.
// Implements docs/design/playground-comparison.md FR1-FR3 and
// docs/architecture/playground-comparison.md §6.5.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { formatMoney } from '../../display';
import { BackLink, ErrorBanner } from '../../components';
import { type AvailableModel, type ApiKeySummary } from '../../api';

interface CompareModelResult {
  modelId: string;
  modelName: string;
  completion: string;
  latencyMs: string;
  inputTokens: string;
  outputTokens: string;
  cost: string;
  error: string;
}

interface CompareModelsResponse {
  response: { code: number; message: string };
  results: CompareModelResult[];
}

export default function PlaygroundComparePage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [models, setModels] = useState<AvailableModel[]>([]);
  const [keys, setKeys] = useState<ApiKeySummary[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [keyId, setKeyId] = useState('');
  const [prompt, setPrompt] = useState('');
  const [results, setResults] = useState<CompareModelResult[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  useEffect(() => {
    api
      .get<{ models?: AvailableModel[] }>('/api/v1/models?page.limit=100', orgId)
      .then((data) => setModels(data.models || []))
      .catch((e) => setError(e instanceof Error ? e.message : t('ucompare.modelsFailed')));
    api
      .get<{ keys?: ApiKeySummary[] }>('/api/v1/auth/api-keys?active_only=true&page.limit=100', orgId)
      .then((data) => {
        const list = data.keys || [];
        setKeys(list);
        if (list.length > 0) setKeyId(list[0].keyId);
      })
      .catch(() => setKeys([]));
  }, [api, orgId, t]);

  const toggleModel = (modelId: string) => {
    setSelected((prev) =>
      prev.includes(modelId) ? prev.filter((m) => m !== modelId) : [...prev, modelId],
    );
  };

  const canCompare = selected.length >= 2 && selected.length <= 5 && !!keyId && prompt.trim().length > 0;

  const compare = async () => {
    setLoading(true);
    setError('');
    setStale(false);
    try {
      const data = await api.post<CompareModelsResponse>(
        '/api/v1/playground/compare',
        orgId,
        { modelIds: selected, apiKeyId: keyId, prompt },
      );
      setResults(data.results || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('ucompare.failed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="page">
      <BackLink to="/playground" label={t('ucompare.back')} />
      <div className="page-header">
        <div>
          <h1 data-testid="playground-compare-title">{t('ucompare.title')}</h1>
          <div className="subtitle">{t('ucompare.subtitle')}</div>
        </div>
      </div>

      {error && (
        <div>
          <ErrorBanner message={error} />
          {stale && <div className="muted">{t('ucompare.stale')}</div>}
          <button className="secondary" data-testid="playground-compare-retry" onClick={() => void compare()}>
            {t('ucompare.retry')}
          </button>
        </div>
      )}

      <div className="filter-bar" data-testid="playground-compare-controls">
        <label>
          {t('ucompare.models')}
          <div className="model-multiselect" data-testid="playground-compare-model-select">
            {models.map((m) => (
              <label key={m.modelId} className="checkbox-row">
                <input
                  type="checkbox"
                  data-testid={`playground-compare-model-${m.modelId}`}
                  checked={selected.includes(m.modelId)}
                  onChange={() => toggleModel(m.modelId)}
                />
                {m.name}
              </label>
            ))}
          </div>
        </label>
        <label>
          {t('ucompare.key')}
          <select
            data-testid="playground-compare-key-select"
            value={keyId}
            onChange={(e) => setKeyId(e.target.value)}
          >
            {keys.length === 0 && <option value="">{t('ucompare.noKeys')}</option>}
            {keys.map((k) => (
              <option key={k.keyId} value={k.keyId}>
                {k.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('ucompare.prompt')}
          <textarea
            data-testid="playground-compare-prompt"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
          />
        </label>
        <button
          data-testid="playground-compare-button"
          disabled={!canCompare || loading}
          onClick={() => void compare()}
        >
          {t('ucompare.compare')}
        </button>
      </div>

      {loading ? (
        <div className="loading">{t('common.loading')}</div>
      ) : results.length === 0 ? (
        <div className="empty-state" data-testid="playground-compare-empty">
          {t('ucompare.empty')}
          <div className="muted">{t('ucompare.emptyHint')}</div>
        </div>
      ) : (
        <>
          <div className="compare-panes" data-testid="playground-compare-panes">
            {results.map((r, i) => (
              <div className="panel compare-pane" key={r.modelId} data-testid={`playground-compare-pane-${i}`}>
                <h3>{r.modelName}</h3>
                {r.error ? (
                  <div className="muted" data-testid={`playground-compare-error-${i}`}>{r.error}</div>
                ) : (
                  <>
                    <pre className="completion">{r.completion}</pre>
                    <div className="muted">
                      {t('ucompare.metrics', {
                        latency: r.latencyMs,
                        input: r.inputTokens,
                        output: r.outputTokens,
                        cost: r.cost,
                      })}
                    </div>
                  </>
                )}
              </div>
            ))}
          </div>
          <div className="panel">
            <table className="table" data-testid="playground-compare-table">
              <thead>
                <tr>
                  <th>{t('ucompare.colModel')}</th>
                  <th>{t('ucompare.colLatency')}</th>
                  <th>{t('ucompare.colInput')}</th>
                  <th>{t('ucompare.colOutput')}</th>
                  <th>{t('ucompare.colCost')}</th>
                </tr>
              </thead>
              <tbody>
                {results.map((r, i) => (
                  <tr key={r.modelId} data-testid={`playground-compare-row-${i}`}>
                    <td>{r.modelName}</td>
                    <td>{r.error ? '—' : `${r.latencyMs} ms`}</td>
                    <td>{r.error ? '—' : r.inputTokens}</td>
                    <td>{r.error ? '—' : r.outputTokens}</td>
                    <td>{r.error ? '—' : formatMoney(r.cost)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}