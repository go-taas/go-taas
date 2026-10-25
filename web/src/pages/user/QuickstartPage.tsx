// End-user SDK / Quickstart page (feature #21): a linear four-step wizard
// that walks a tenant from "I have access" to "my first inference call
// succeeds" — (1) create/select an API key, (2) pick an authorized model,
// (3) copy the inference base URL, (4) copy a pre-filled OpenAI-compatible
// snippet (Python / Node / curl) and run a test request. End-user surface
// only: route /quickstart, API /api/v1/*.

import { useEffect, useMemo, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { ErrorBanner } from '../../components';
import {
  type ApiKeySummary,
  type AvailableModel,
  type InferenceEndpointResponse,
  type PlaygroundInferResponse,
} from '../../api';

interface CreateKeyResponse {
  response: { code: number; message: string };
  keyId: string;
  apiKey: string;
}

type Lang = 'python' | 'node' | 'curl';

const SAMPLE_PROMPT = 'Hello!';

// buildSnippet templates the OpenAI-compatible snippet for the active
// language from the selected model, base URL and key (AD4/AD6). The key is
// the just-created plaintext (held in component state) or the
// YOUR_API_KEY placeholder for an existing key.
function buildSnippet(lang: Lang, baseUrl: string, model: string, key: string): string {
  const k = key || 'YOUR_API_KEY';
  if (lang === 'python') {
    return `from openai import OpenAI

client = OpenAI(
    api_key="${k}",
    base_url="${baseUrl}",
)

response = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "${SAMPLE_PROMPT}"}],
)
print(response.choices[0].message.content)`;
  }
  if (lang === 'node') {
    return `import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "${k}",
  baseURL: "${baseUrl}",
});

const response = await client.chat.completions.create({
  model: "${model}",
  messages: [{ role: "user", content: "${SAMPLE_PROMPT}" }],
});
console.log(response.choices[0].message.content);`;
  }
  return `curl ${baseUrl}/chat/completions \\
  -H "Authorization: Bearer ${k}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${model}",
    "messages": [{"role": "user", "content": "${SAMPLE_PROMPT}"}]
  }'`;
}

// CopyControl is a copy button with a stable data-testid and a transient
// "Copied!" state.
function CopyControl({ text, testId, label }: { text: string; testId: string; label?: string }) {
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
          // Clipboard API may be unavailable (insecure context); the text
          // remains selectable as a fallback.
        }
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      }}
    >
      {copied ? t('common.copied') : label}
    </button>
  );
}

export default function QuickstartPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();

  // Step 1 — API key.
  const [keys, setKeys] = useState<ApiKeySummary[]>([]);
  const [keysLoaded, setKeysLoaded] = useState(false);
  const [selectedKeyId, setSelectedKeyId] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [keyName, setKeyName] = useState('');
  const [keyExpiry, setKeyExpiry] = useState('never');
  const [creating, setCreating] = useState(false);
  const [createdSecret, setCreatedSecret] = useState('');
  const [createdConfirmed, setCreatedConfirmed] = useState(false);

  // Step 2 — model.
  const [models, setModels] = useState<AvailableModel[]>([]);
  const [modelsLoaded, setModelsLoaded] = useState(false);
  const [selectedModelId, setSelectedModelId] = useState('');

  // Step 3 — base URL.
  const [baseUrl, setBaseUrl] = useState('');
  // endpointError carries the reason the base URL could not be determined
  // (e.g. the platform has no gateway configured), shown in step 3 where
  // the user needs it instead of only in the page-level banner.
  const [endpointError, setEndpointError] = useState('');
  const [endpointLoaded, setEndpointLoaded] = useState(false);

  // Step 4 — code & test.
  const [lang, setLang] = useState<Lang>('python');
  const [testing, setTesting] = useState(false);
  const [testResponse, setTestResponse] = useState<PlaygroundInferResponse | null>(null);
  const [testError, setTestError] = useState('');

  // Shared error state.
  const [error, setError] = useState('');

  // Load keys, models and the base URL on mount (design §7.1).
  useEffect(() => {
    let cancelled = false;
    api
      .get<{ keys?: ApiKeySummary[] }>('/api/v1/auth/api-keys?page.limit=100', orgId)
      .then((data) => {
        if (cancelled) return;
        setKeys(data.keys || []);
        if ((data.keys || []).length > 0) setSelectedKeyId(data.keys![0].keyId);
      })
      .catch((e) => {
        if (!cancelled) setError(e instanceof Error ? e.message : t('quickstart.keysFailed'));
      })
      .finally(() => {
        if (!cancelled) setKeysLoaded(true);
      });

    api
      .get<{ models?: AvailableModel[] }>('/api/v1/models?page.limit=100', orgId)
      .then((data) => {
        if (cancelled) return;
        setModels(data.models || []);
        if ((data.models || []).length > 0) setSelectedModelId(data.models![0].modelId);
      })
      .catch((e) => {
        if (!cancelled) setError(e instanceof Error ? e.message : t('quickstart.modelsFailed'));
      })
      .finally(() => {
        if (!cancelled) setModelsLoaded(true);
      });

    api
      .get<InferenceEndpointResponse>('/api/v1/inference-endpoint', orgId)
      .then((data) => {
        if (cancelled) return;
        setBaseUrl(data.baseUrl || '');
      })
      .catch((e) => {
        if (cancelled) return;
        setEndpointError(e instanceof Error ? e.message : t('quickstart.baseUrlFailed'));
      })
      .finally(() => {
        if (!cancelled) setEndpointLoaded(true);
      });

    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // The snippet key: the just-created plaintext (held in component state,
  // AD10) or the YOUR_API_KEY placeholder for an existing key (AD4).
  const snippetKey = createdSecret || 'YOUR_API_KEY';

  const selectedModel = models.find((m) => m.modelId === selectedModelId);
  const snippet = useMemo(
    () => buildSnippet(lang, baseUrl || '{base_url}', selectedModel?.modelId || '{model}', snippetKey),
    [lang, baseUrl, selectedModel, snippetKey],
  );

  const canTest = selectedModelId !== '' && (selectedKeyId !== '' || createdSecret !== '');

  const createKey = async () => {
    if (!keyName.trim()) {
      setError(t('quickstart.nameRequired'));
      return;
    }
    if (keyName.trim().length > 64) {
      setError(t('quickstart.nameTooLong'));
      return;
    }
    setCreating(true);
    setError('');
    try {
      const body: Record<string, unknown> = { name: keyName.trim() };
      if (keyExpiry !== 'never') {
        const days = parseInt(keyExpiry, 10);
        body.expiresAt = String(Math.floor(Date.now() / 1000) + days * 86400);
      }
      const res = await api.post<CreateKeyResponse>('/api/v1/auth/api-keys', orgId, body);
      // Hold the plaintext in component state only (AD10); it is dropped
      // on unmount and never persisted.
      setCreatedSecret(res.apiKey);
      setCreatedConfirmed(false);
      setCreateOpen(false);
      setKeyName('');
      setKeyExpiry('never');
      // Refresh the key list so the new key appears in the selector.
      const data = await api.get<{ keys?: ApiKeySummary[] }>('/api/v1/auth/api-keys?page.limit=100', orgId);
      setKeys(data.keys || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('quickstart.createFailed'));
    } finally {
      setCreating(false);
    }
  };

  const runTest = async () => {
    if (!canTest) return;
    setTesting(true);
    setTestError('');
    setTestResponse(null);
    try {
      const data = await api.post<PlaygroundInferResponse>(
        `/api/v1/models/${selectedModelId}:playground`,
        orgId,
        { modelId: selectedModelId, prompt: SAMPLE_PROMPT },
      );
      setTestResponse(data);
    } catch (e) {
      setTestError(e instanceof Error ? e.message : t('quickstart.testFailed'));
    } finally {
      setTesting(false);
    }
  };

  const retry = () => {
    setError('');
    setKeysLoaded(false);
    setModelsLoaded(false);
    setEndpointLoaded(false);
    // Re-run the load effect by forcing a remount of the loaders.
    window.location.reload();
  };

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h1>{t('quickstart.title')}</h1>
          <div className="subtitle">{t('quickstart.subtitle')}</div>
        </div>
      </div>

      {error && (
        <div>
          <ErrorBanner message={error} />
          <button className="secondary" data-testid="quickstart-retry" onClick={retry}>
            {t('common.retry')}
          </button>
        </div>
      )}

      {/* Step 1 — API key */}
      <section className="panel" data-testid="quickstart-step-key">
        <h2>{t('quickstart.step1')}</h2>
        {!keysLoaded ? (
          <div className="loading">{t('quickstart.loadingKeys')}</div>
        ) : keys.length === 0 && !createdSecret ? (
          <div>
            <p>{t('quickstart.emptyKeys')}</p>
            <div className="form-row">
              <label htmlFor="quickstart-key-name">{t('quickstart.fieldName')}</label>
              <input
                id="quickstart-key-name"
                data-testid="quickstart-key-name"
                value={keyName}
                maxLength={64}
                onChange={(e) => setKeyName(e.target.value)}
                placeholder={t('quickstart.placeholderName')}
              />
            </div>
            <div className="form-row">
              <label htmlFor="quickstart-key-expiry">{t('quickstart.fieldExpiry')}</label>
              <select
                id="quickstart-key-expiry"
                data-testid="quickstart-key-expiry"
                value={keyExpiry}
                onChange={(e) => setKeyExpiry(e.target.value)}
              >
                <option value="never">{t('common.never')}</option>
                <option value="30">{t('common.30days')}</option>
                <option value="90">{t('common.90days')}</option>
                <option value="365">{t('common.365days')}</option>
              </select>
            </div>
            <button data-testid="quickstart-key-create-submit" disabled={creating} onClick={() => void createKey()}>
              {creating ? t('common.creating') : t('quickstart.createKey')}
            </button>
          </div>
        ) : (
          <div>
            <div className="form-row">
              <label htmlFor="quickstart-key-select">{t('quickstart.fieldApiKey')}</label>
              <select
                id="quickstart-key-select"
                data-testid="quickstart-key-select"
                value={selectedKeyId}
                onChange={(e) => setSelectedKeyId(e.target.value)}
              >
                {keys.map((k) => (
                  <option key={k.keyId} value={k.keyId}>
                    {k.name} ({k.prefix}…)
                  </option>
                ))}
              </select>
            </div>
            <button className="link" data-testid="quickstart-key-create" onClick={() => setCreateOpen(true)}>
              {t('quickstart.createNew')}
            </button>
          </div>
        )}

        {createOpen && (
          <div className="panel" data-testid="quickstart-create-form">
            <div className="form-row">
              <label htmlFor="quickstart-key-name">{t('quickstart.fieldName')}</label>
              <input
                id="quickstart-key-name"
                data-testid="quickstart-key-name"
                value={keyName}
                maxLength={64}
                onChange={(e) => setKeyName(e.target.value)}
                placeholder={t('quickstart.placeholderName')}
              />
            </div>
            <div className="form-row">
              <label htmlFor="quickstart-key-expiry">{t('quickstart.fieldExpiry')}</label>
              <select
                id="quickstart-key-expiry"
                data-testid="quickstart-key-expiry"
                value={keyExpiry}
                onChange={(e) => setKeyExpiry(e.target.value)}
              >
                <option value="never">{t('common.never')}</option>
                <option value="30">{t('common.30days')}</option>
                <option value="90">{t('common.90days')}</option>
                <option value="365">{t('common.365days')}</option>
              </select>
            </div>
            <button data-testid="quickstart-key-create-submit" disabled={creating} onClick={() => void createKey()}>
              {creating ? t('common.creating') : t('quickstart.createKey')}
            </button>
            <button className="secondary" onClick={() => setCreateOpen(false)}>
              {t('common.cancel')}
            </button>
          </div>
        )}

        {createdSecret && (
          <div className="panel" data-testid="quickstart-created-secret">
            <p>
              {t('quickstart.createdBody')} <strong>{t('quickstart.createdWarning')}</strong>
            </p>
            <div className="secret-box" data-testid="quickstart-created-secret-value">
              {createdSecret}
            </div>
            <CopyControl text={createdSecret} testId="quickstart-created-copy" label={t('quickstart.copyKey')} />
            <div style={{ marginTop: 18 }}>
              <label style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <input
                  type="checkbox"
                  data-testid="quickstart-created-confirm"
                  checked={createdConfirmed}
                  onChange={(e) => setCreatedConfirmed(e.target.checked)}
                />
                {t('quickstart.savedCheckbox')}
              </label>
            </div>
            <button
              data-testid="quickstart-created-done"
              disabled={!createdConfirmed}
              onClick={() => setCreatedSecret('')}
            >
              {t('common.done')}
            </button>
          </div>
        )}
      </section>

      {/* Step 2 — Model */}
      <section className="panel" data-testid="quickstart-step-model">
        <h2>{t('quickstart.step2')}</h2>
        {!modelsLoaded ? (
          <div className="loading">{t('quickstart.loadingModels')}</div>
        ) : models.length === 0 ? (
          <div className="empty-state" data-testid="quickstart-no-models">
            {t('quickstart.emptyModels')}
          </div>
        ) : (
          <div className="form-row">
            <label htmlFor="quickstart-model-select">{t('quickstart.fieldModel')}</label>
            <select
              id="quickstart-model-select"
              data-testid="quickstart-model-select"
              value={selectedModelId}
              onChange={(e) => setSelectedModelId(e.target.value)}
            >
              {models.map((m) => (
                <option key={m.modelId} value={m.modelId}>
                  {m.name} ({m.latestVersion})
                </option>
              ))}
            </select>
          </div>
        )}
      </section>

      {/* Step 3 — Base URL */}
      <section className="panel" data-testid="quickstart-step-baseurl">
        <h2>{t('quickstart.step3')}</h2>
        {!endpointLoaded ? (
          <div className="loading">{t('quickstart.loadingBaseUrl')}</div>
        ) : baseUrl ? (
          <div className="endpoint-box">
            <span className="mono" data-testid="quickstart-base-url">
              {baseUrl}
            </span>
            <CopyControl text={baseUrl} testId="quickstart-base-url-copy" label={t('common.copy')} />
          </div>
        ) : (
          <div className="error" data-testid="quickstart-base-url-error">
            {endpointError || t('quickstart.baseUrlFailed')}
          </div>
        )}
      </section>

      {/* Step 4 — Code & test */}
      <section className="panel" data-testid="quickstart-step-code">
        <h2>{t('quickstart.step4')}</h2>
        <div className="segmented" role="tablist">
          <button
            className={lang === 'python' ? 'active' : ''}
            data-testid="quickstart-lang-python"
            onClick={() => setLang('python')}
          >
            {t('quickstart.langPython')}
          </button>
          <button
            className={lang === 'node' ? 'active' : ''}
            data-testid="quickstart-lang-node"
            onClick={() => setLang('node')}
          >
            {t('quickstart.langNode')}
          </button>
          <button
            className={lang === 'curl' ? 'active' : ''}
            data-testid="quickstart-lang-curl"
            onClick={() => setLang('curl')}
          >
            {t('quickstart.langCurl')}
          </button>
        </div>
        <div className="curl-snippet" data-testid="quickstart-snippet">
          {snippet}
        </div>
        <CopyControl text={snippet} testId="quickstart-copy" label={t('quickstart.copySnippet')} />
        <div style={{ marginTop: 20 }}>
          <button data-testid="quickstart-test" disabled={!canTest || testing} onClick={() => void runTest()}>
            {testing ? t('quickstart.testing') : t('quickstart.test')}
          </button>
        </div>
        {testError && (
          <div className="error" data-testid="quickstart-test-error">
            {testError}
          </div>
        )}
        {testResponse && (
          <div className="response" data-testid="quickstart-test-response">
            <pre>{testResponse.completion || t('quickstart.noCompletion')}</pre>
            <div>
              {t('quickstart.response', {
                prompt: testResponse.promptTokens,
                completion: testResponse.completionTokens,
                latency: testResponse.latencyMs,
              })}
            </div>
          </div>
        )}
      </section>
    </div>
  );
}
