// End-user model playground (feature-17 AD8): model-based, never names an
// inference service. Feature-43 AC10: /playground?prompt=<id>&version=<n>
// (opened from a prompt's "Open in playground" action) pre-fills the
// prompt editor with that prompt version's content.

import { useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { type AvailableModel, type PlaygroundInferResponse } from '../../api';

export default function PlaygroundPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [models, setModels] = useState<AvailableModel[]>([]);
  const [modelId, setModelId] = useState('');
  const [prompt, setPrompt] = useState('');
  const [response, setResponse] = useState<PlaygroundInferResponse | null>(null);
  const [error, setError] = useState('');

  // Feature-43 AC10: a prompt link (?prompt=<id>&version=<n>) pre-fills
  // the editor with that version's content.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const promptId = params.get('prompt');
    if (!promptId) return;
    const version = params.get('version');
    api
      .get<{ prompt?: { content?: string; version?: string | number } }>(
        `/api/v1/prompts/${promptId}`,
        orgId,
      )
      .then((data) => {
        const p = data.prompt;
        if (!p) return;
        // The gateway serializes version as a number while the URL param is
        // a string; compare loosely so the prefill is not silently skipped.
        if (version !== null && p.version !== undefined && String(p.version) !== version) return; // active version differs
        setPrompt(p.content || '');
      })
      .catch(() => {
        // A stale/deleted prompt link must not break the playground.
      });
  }, [api, orgId]);

  useEffect(() => {
    api
      .get<{ models?: AvailableModel[] }>('/api/v1/models?page.limit=100', orgId)
      .then((data) => {
        const list = data.models || [];
        setModels(list);
        if (list.length > 0) setModelId(list[0].modelId);
      })
      .catch((e) => setError(e instanceof Error ? e.message : t('uplayground.modelsFailed')));
  }, [api, orgId, t]);

  const send = async () => {
    setError('');
    setResponse(null);
    try {
      const data = await api.post<PlaygroundInferResponse>(
        `/api/v1/models/${modelId}:playground`,
        orgId,
        { modelId, prompt },
      );
      setResponse(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('uplayground.failed'));
    }
  };

  return (
    <div className="page">
      <h1>{t('uplayground.title')}</h1>
      {error && <div className="error">{error}</div>}
      {models.length === 0 ? (
        <div className="empty" data-testid="playground-no-models">{t('uplayground.empty')}</div>
      ) : (
        <>
          <div className="form-row">
            <label htmlFor="playground-model-select">{t('uplayground.model')}</label>
            <select
              id="playground-model-select"
              data-testid="playground-model-select"
              value={modelId}
              onChange={(e) => setModelId(e.target.value)}
            >
              {models.map((m) => (
                <option key={m.modelId} value={m.modelId}>
                  {m.name}
                </option>
              ))}
            </select>
          </div>
          <div className="form-row">
            <label htmlFor="playground-prompt-input">{t('uplayground.prompt')}</label>
            <textarea
              id="playground-prompt-input"
              data-testid="playground-prompt-input"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
            />
          </div>
          <button data-testid="playground-send" onClick={() => void send()}>
            {t('uplayground.send')}
          </button>
          {response && (
            <div className="response" data-testid="playground-response">
              <pre>{response.completion || t('uplayground.noCompletion')}</pre>
              <div>
                {t('uplayground.response', {
                  prompt: response.promptTokens,
                  completion: response.completionTokens,
                  latency: response.latencyMs,
                })}
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}
