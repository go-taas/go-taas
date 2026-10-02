// API Docs page (end-user): an interactive API reference with an
// endpoint list grouped by category, a detail pane with request/response
// examples and error codes, and try-it-in-console for every catalog
// endpoint. Implements docs/design/api-docs-explorer.md FR1-FR3 and
// docs/architecture/api-docs-explorer.md §6.5.

import { useEffect, useMemo, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { ErrorBanner } from '../components';
import MethodBadge from '../components/MethodBadge';
import {
  type ApiDocsCategory,
  type ApiDocsEndpoint,
  type AvailableModel,
  type ApiKeySummary,
  type PlaygroundInferResponse,
} from '../api';

type Lang = 'python' | 'node' | 'curl';

// buildExample renders a language-specific example for an endpoint.
function buildExample(endpoint: ApiDocsEndpoint, lang: Lang, baseUrl: string): string {
  const method = endpoint.method;
  const path = endpoint.path;
  const req = endpoint.requestExample || '{}';
  switch (lang) {
    case 'python':
      return `import requests\n\nresp = requests.${method.toLowerCase()}(\n    "${baseUrl}${path}",\n    json=${req},\n    headers={"Authorization": "Bearer YOUR_API_KEY"},\n)\nprint(resp.json())`;
    case 'node':
      return `const res = await fetch("${baseUrl}${path}", {\n  method: "${method}",\n  headers: { "Authorization": "Bearer YOUR_API_KEY" },\n  body: JSON.stringify(${req}),\n});\nconsole.log(await res.json());`;
    default:
      return `curl -X ${method} "${baseUrl}${path}" \\\n  -H "Authorization: Bearer YOUR_API_KEY" \\\n  -d '${req}'`;
  }
}

function CopyControl({ text, testId, label }: { text: string; testId: string; label: string }) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  return (
    <button
      className="secondary"
      data-testid={testId}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
        } catch {
          // Clipboard may be unavailable; text remains selectable.
        }
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
    >
      {copied ? t('common.copied') : label}
    </button>
  );
}

export default function ApiDocsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [categories, setCategories] = useState<ApiDocsCategory[]>([]);
  const [selectedId, setSelectedId] = useState('');
  const [lang, setLang] = useState<Lang>('python');
  const [baseUrl, setBaseUrl] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [stale, setStale] = useState(false);

  // Try-it state.
  const [models, setModels] = useState<AvailableModel[]>([]);
  const [keys, setKeys] = useState<ApiKeySummary[]>([]);
  const [modelId, setModelId] = useState('');
  const [keyId, setKeyId] = useState('');
  const [prompt, setPrompt] = useState('');
  const [tryResponse, setTryResponse] = useState('');
  const [tryError, setTryError] = useState('');
  const [sending, setSending] = useState(false);

  const load = async () => {
    setLoading(true);
    try {
      const [docs, endpoint] = await Promise.all([
        api.get<{ categories?: ApiDocsCategory[] }>('/api/v1/docs', orgId),
        api.get<{ baseUrl?: string }>('/api/v1/inference-endpoint', orgId).catch(() => ({ baseUrl: '' })),
      ]);
      const cats = docs.categories || [];
      setCategories(cats);
      setBaseUrl(endpoint.baseUrl || '');
      if (cats.length > 0 && cats[0].endpoints.length > 0) {
        setSelectedId(cats[0].endpoints[0].endpointId);
      }
      setError('');
      setStale(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('apidocs.loadFailed'));
      setStale(true);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, [api, orgId, t]);

  const selected = useMemo(() => {
    for (const c of categories) {
      for (const e of c.endpoints) {
        if (e.endpointId === selectedId) return e;
      }
    }
    return null;
  }, [categories, selectedId]);

  // Load models and keys for try-it when an inference endpoint is
  // selected.
  useEffect(() => {
    if (!selected || !selected.tryable || !selected.path.startsWith('/v1/')) return;
    api
      .get<{ models?: AvailableModel[] }>('/api/v1/models?page.limit=100', orgId)
      .then((data) => {
        const list = data.models || [];
        setModels(list);
        if (list.length > 0) setModelId(list[0].modelId);
      })
      .catch(() => undefined);
    api
      .get<{ keys?: ApiKeySummary[] }>('/api/v1/auth/api-keys', orgId)
      .then((data) => {
        const list = data.keys || [];
        setKeys(list);
        if (list.length > 0) setKeyId(list[0].keyId);
      })
      .catch(() => undefined);
  }, [selected, api, orgId]);

  const send = async () => {
    if (!selected) return;
    setSending(true);
    setTryError('');
    setTryResponse('');
    try {
      if (selected.path.startsWith('/v1/')) {
        // Inference try-it reuses the playground proxy (AD4).
        const data = await api.post<PlaygroundInferResponse>(
          `/api/v1/models/${modelId}:playground`,
          orgId,
          { modelId, prompt },
        );
        setTryResponse(
          `${data.completion || ''}\n\n[prompt_tokens=${data.promptTokens} completion_tokens=${data.completionTokens} latency_ms=${data.latencyMs}]`,
        );
      } else {
        // Control-plane try-it issues the actual request with the user's
        // session (AD4).
        const data = await api.get<Record<string, unknown>>(selected.path, orgId);
        setTryResponse(JSON.stringify(data, null, 2));
      }
    } catch (e) {
      setTryError(e instanceof Error ? e.message : t('apidocs.tryFailed'));
    } finally {
      setSending(false);
    }
  };

  const example = selected ? buildExample(selected, lang, baseUrl) : '';

  return (
    <div>
      <div className="page-header">
        <div>
          <h1 data-testid="api-docs-title">{t('apidocs.title')}</h1>
          <div className="subtitle">{t('apidocs.subtitle')}</div>
        </div>
        <button className="secondary" data-testid="docs-refresh" onClick={() => void load()} disabled={loading}>
          {t('apidocs.refresh')}
        </button>
      </div>

      {error && (
        <div>
          <ErrorBanner message={error} />
          {stale && <div className="muted">{t('apidocs.stale')}</div>}
          <button className="secondary" data-testid="docs-retry" onClick={() => void load()}>
            {t('apidocs.retry')}
          </button>
        </div>
      )}

      {!loading && !error && categories.length === 0 && (
        <div className="panel" data-testid="docs-empty">
          <p>{t('apidocs.empty')}</p>
          <p className="muted">{t('apidocs.emptyHint')}</p>
        </div>
      )}

      {!loading && !error && categories.length > 0 && (
        <div className="docs-layout" style={{ display: 'flex', gap: 16 }}>
          <div className="docs-endpoint-list" data-testid="docs-endpoint-list" style={{ width: 320, flexShrink: 0 }}>
            {categories.map((c) => (
              <div key={c.categoryId} className="docs-category">
                <h3 style={{ margin: '8px 0' }}>{c.categoryName}</h3>
                {c.endpoints.map((e) => (
                  <button
                    key={e.endpointId}
                    className={selectedId === e.endpointId ? 'primary' : 'secondary'}
                    data-testid={`docs-endpoint-${e.endpointId}`}
                    onClick={() => setSelectedId(e.endpointId)}
                    style={{ display: 'block', width: '100%', textAlign: 'left', marginBottom: 4 }}
                  >
                    <MethodBadge method={e.method} /> {e.path}
                  </button>
                ))}
              </div>
            ))}
          </div>

          {selected && (
            <div className="docs-detail" data-testid="docs-detail" style={{ flex: 1 }}>
              <h2>
                <MethodBadge method={selected.method} /> {selected.path}
              </h2>
              <p>{selected.summary}</p>
              <p className="muted">{selected.description}</p>

              {selected.parameters.length > 0 && (
                <div className="panel">
                  <h3 style={{ marginTop: 0 }}>{t('apidocs.parameters')}</h3>
                  <table className="table">
                    <thead>
                      <tr>
                        <th>{t('apidocs.paramName')}</th>
                        <th>{t('apidocs.paramIn')}</th>
                        <th>{t('apidocs.paramRequired')}</th>
                        <th>{t('apidocs.paramType')}</th>
                        <th>{t('apidocs.paramDesc')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {selected.parameters.map((p) => (
                        <tr key={p.name}>
                          <td className="mono">{p.name}</td>
                          <td>{p.in}</td>
                          <td>{p.required ? t('common.yes') : t('common.no')}</td>
                          <td>{p.type}</td>
                          <td>{p.description}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}

              <div className="panel">
                <h3 style={{ marginTop: 0 }}>{t('apidocs.request')}</h3>
                <div className="lang-tabs" data-testid="docs-lang-tabs">
                  {(['python', 'node', 'curl'] as Lang[]).map((l) => (
                    <button
                      key={l}
                      className={lang === l ? 'primary' : 'secondary'}
                      data-testid={`docs-lang-${l}`}
                      onClick={() => setLang(l)}
                    >
                      {l}
                    </button>
                  ))}
                </div>
                <pre data-testid="docs-request-example">{example}</pre>
                <CopyControl text={example} testId="docs-copy" label={t('apidocs.copy')} />
              </div>

              <div className="panel">
                <h3 style={{ marginTop: 0 }}>{t('apidocs.response')}</h3>
                <pre data-testid="docs-response-example">{selected.responseExample}</pre>
              </div>

              {selected.errorCodes.length > 0 && (
                <div className="panel">
                  <h3 style={{ marginTop: 0 }}>{t('apidocs.errorCodes')}</h3>
                  <table className="table">
                    <thead>
                      <tr>
                        <th>{t('apidocs.errorCode')}</th>
                        <th>{t('apidocs.errorConstant')}</th>
                        <th>{t('apidocs.errorMessage')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {selected.errorCodes.map((ec) => (
                        <tr key={ec.code}>
                          <td className="mono">{ec.code}</td>
                          <td className="mono">{ec.constant}</td>
                          <td>{ec.message}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}

              {selected.tryable && (
                <div className="panel" data-testid="docs-try-it">
                  <h3 style={{ marginTop: 0 }}>{t('apidocs.tryIt')}</h3>
                  {selected.path.startsWith('/v1/') ? (
                    <>
                      <label>
                        {t('apidocs.model')}
                        <select data-testid="docs-try-model" value={modelId} onChange={(e) => setModelId(e.target.value)}>
                          {models.map((m) => (
                            <option key={m.modelId} value={m.modelId}>
                              {m.name}
                            </option>
                          ))}
                        </select>
                      </label>
                      <label>
                        {t('apidocs.apiKey')}
                        <select data-testid="docs-try-key" value={keyId} onChange={(e) => setKeyId(e.target.value)}>
                          {keys.map((k) => (
                            <option key={k.keyId} value={k.keyId}>
                              {k.name}
                            </option>
                          ))}
                        </select>
                      </label>
                      <label>
                        {t('apidocs.prompt')}
                        <textarea
                          data-testid="docs-try-prompt"
                          value={prompt}
                          onChange={(e) => setPrompt(e.target.value)}
                          rows={3}
                        />
                      </label>
                    </>
                  ) : (
                    <p className="muted">{t('apidocs.controlPlaneHint')}</p>
                  )}
                  <button
                    className="primary"
                    data-testid="docs-try-send"
                    onClick={() => void send()}
                    disabled={sending || (selected.path.startsWith('/v1/') && (!modelId || !prompt))}
                  >
                    {t('apidocs.send')}
                  </button>
                  {tryError && <ErrorBanner message={tryError} />}
                  {tryResponse && (
                    <pre data-testid="docs-try-response" style={{ marginTop: 8 }}>
                      {tryResponse}
                    </pre>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}