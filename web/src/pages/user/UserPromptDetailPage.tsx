// End-user Prompt Detail page (feature #43): view a single prompt's
// active version, version history, usage, and actions. End-user surface:
// route /prompts/:promptId, API /api/v1/prompts/{prompt_id}/*.

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { useParams, navigate } from '../../router';
import { formatTime } from '../../api';
import { Dialog, ErrorBanner } from '../../components';
import type { Prompt } from './UserPromptsPage';

interface GetPromptResponse {
  response: { code: number; message: string };
  prompt: Prompt;
}

interface ListVersionsResponse {
  response: { code: number; message: string };
  versions: PromptVersion[];
}

interface GetUsageResponse {
  response: { code: number; message: string };
  timesUsed: string;
  lastUsedAt: string;
}

interface PromptVersion {
  promptId: string;
  version: string;
  content: string;
  modelId: string;
  variables: string[];
  createdAt: string;
  createdBy: string;
}

export default function UserPromptDetailPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const { promptId } = useParams();
  const [prompt, setPrompt] = useState<Prompt | null>(null);
  const [versions, setVersions] = useState<PromptVersion[]>([]);
  const [usage, setUsage] = useState<GetUsageResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState(false);
  const [editContent, setEditContent] = useState('');
  const [editModel, setEditModel] = useState('');
  const [editVariables, setEditVariables] = useState('');
  const [saving, setSaving] = useState(false);
  const [rollbackTarget, setRollbackTarget] = useState<PromptVersion | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [p, v, u] = await Promise.all([
        api.get<GetPromptResponse>(`/api/v1/prompts/${promptId}`, orgId),
        api.get<ListVersionsResponse>(`/api/v1/prompts/${promptId}/versions`, orgId),
        api.get<GetUsageResponse>(`/api/v1/prompts/${promptId}/usage`, orgId),
      ]);
      setPrompt(p.prompt);
      setVersions(v.versions || []);
      setUsage(u);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, promptId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const startEdit = () => {
    if (!prompt) return;
    setEditContent(prompt.content);
    setEditModel(prompt.modelId || '');
    setEditVariables((prompt.variables || []).join(', '));
    setEditing(true);
  };

  const save = async () => {
    setSaving(true);
    setError('');
    try {
      // Variables use ${var} syntax (design FR1.4); a bare name typed
      // into the tag input is wrapped automatically so the server-side
      // 12706 validation only fires on genuinely malformed references.
      const vars = editVariables
        .split(',')
        .map((v) => v.trim())
        .filter(Boolean)
        .map((v) => (v.startsWith('${') && v.endsWith('}') ? v : `\${${v.replace(/^\$\{|\}$/g, '')}}`));
      await api.post(`/api/v1/prompts/${promptId}`, orgId, {
        content: editContent,
        model_id: editModel || undefined,
        variables: vars,
      });
      setEditing(false);
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.updateFailed'));
    } finally {
      setSaving(false);
    }
  };

  const rollback = async () => {
    if (!rollbackTarget) return;
    setBusy(true);
    try {
      await api.post(`/api/v1/prompts/${promptId}/rollback`, orgId, { version: rollbackTarget.version });
      setRollbackTarget(null);
      void load();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.rollbackFailed'));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setBusy(true);
    try {
      await api.del(`/api/v1/prompts/${promptId}`, orgId);
      navigate('/prompts');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.deleteFailed'));
    } finally {
      setBusy(false);
    }
  };

  const copyText = async () => {
    if (!prompt) return;
    try {
      await navigator.clipboard.writeText(prompt.content);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard may be unavailable.
    }
  };

  if (loading) {
    return <div className="loading">{t('common.loading')}</div>;
  }

  if (!prompt) {
    return (
      <div>
        <div className="page-header">
          <div>
            <h1>{t('prompts.detailTitle')}</h1>
          </div>
        </div>
        {error && <ErrorBanner message={error} />}
      </div>
    );
  }

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{prompt.name}</h1>
          <div className="subtitle mono">{prompt.promptId}</div>
        </div>
        <div className="header-actions">
          <button className="secondary" onClick={() => navigate('/prompts')}>
            {t('prompts.backToPrompts')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="panel" data-testid="prompt-detail-editor">
        <h3>{t('prompts.editor')}</h3>
        {editing ? (
          <>
            <div className="form-field">
              <label>{t('prompts.colContent')}</label>
              <textarea value={editContent} data-testid="prompt-edit-content" onChange={(e) => setEditContent(e.target.value)} />
            </div>
            <div className="form-field">
              <label>{t('prompts.colModel')}</label>
              <input value={editModel} data-testid="prompt-edit-model" onChange={(e) => setEditModel(e.target.value)} />
            </div>
            <div className="form-field">
              <label>{t('prompts.colVariables')}</label>
              <input value={editVariables} data-testid="prompt-edit-variables" onChange={(e) => setEditVariables(e.target.value)} />
            </div>
            <div className="dialog-actions">
              <button disabled={saving} data-testid="prompt-save" onClick={() => void save()}>
                {t('common.save')}
              </button>
              <button className="secondary" onClick={() => setEditing(false)}>
                {t('common.cancel')}
              </button>
            </div>
          </>
        ) : (
          <>
            <pre className="prompt-content" data-testid="prompt-detail-content">
              {prompt.content}
            </pre>
            <div className="kv-grid">
              <div>
                <span className="kv-label">{t('prompts.colModel')}</span>
                <span>{prompt.modelId || '—'}</span>
              </div>
              <div>
                <span className="kv-label">{t('prompts.colVariables')}</span>
                <span>{(prompt.variables || []).join(', ') || '—'}</span>
              </div>
            </div>
          </>
        )}
      </div>

      <div className="panel" data-testid="prompt-detail-usage">
        <h3>{t('prompts.usage')}</h3>
        <div className="kv-grid">
          <div>
            <span className="kv-label">{t('prompts.colUsed')}</span>
            <span>{usage?.timesUsed || '0'}</span>
          </div>
          <div>
            <span className="kv-label">{t('prompts.colLastUsed')}</span>
            <span>{formatTime(usage?.lastUsedAt)}</span>
          </div>
        </div>
      </div>

      <div className="panel" data-testid="prompt-detail-versions">
        <h3>{t('prompts.versionHistory')}</h3>
        <table className="data">
          <thead>
            <tr>
              <th>{t('prompts.colVersion')}</th>
              <th>{t('prompts.colCreatedBy')}</th>
              <th>{t('common.created')}</th>
              <th>{t('common.actions')}</th>
            </tr>
          </thead>
          <tbody>
            {versions.map((v) => (
              <tr key={v.version} data-testid={`prompt-version-${v.version}`}>
                <td>{v.version}</td>
                <td>{v.createdBy}</td>
                <td>{formatTime(v.createdAt)}</td>
                <td>
                  <button
                    className="link"
                    disabled={v.version === prompt.version || busy}
                    data-testid={`prompt-rollback-${v.version}`}
                    onClick={() => setRollbackTarget(v)}
                  >
                    {t('prompts.rollback')}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="panel">
        <h3>{t('common.actions')}</h3>
        <div className="dialog-actions">
          <button
            className="secondary"
            data-testid="prompt-open-playground"
            onClick={() => navigate(`/playground?prompt=${prompt.promptId}&version=${prompt.version}`)}
          >
            {t('prompts.openPlayground')}
          </button>
          <button className="secondary" data-testid="prompt-copy" onClick={() => void copyText()}>
            {copied ? t('prompts.copied') : t('prompts.copy')}
          </button>
          <button className="secondary" data-testid="prompt-edit" onClick={startEdit}>
            {t('common.edit')}
          </button>
          <button className="danger" disabled={busy} data-testid="prompt-delete" onClick={() => setDeleteOpen(true)}>
            {t('common.delete')}
          </button>
        </div>
      </div>

      {rollbackTarget && (
        <Dialog title={t('prompts.rollbackTitle')} testId="prompt-rollback-dialog" onClose={() => setRollbackTarget(null)}>
          <p>{t('prompts.rollbackConfirm', { version: rollbackTarget.version })}</p>
          <div className="dialog-actions">
            <button data-testid="prompt-rollback-confirm" onClick={() => void rollback()}>
              {t('prompts.rollback')}
            </button>
            <button className="secondary" onClick={() => setRollbackTarget(null)}>
              {t('prompts.keepCurrent')}
            </button>
          </div>
        </Dialog>
      )}

      {deleteOpen && (
        <Dialog title={t('prompts.deleteTitle')} testId="prompt-delete-dialog" onClose={() => setDeleteOpen(false)}>
          <p>{t('prompts.deleteConfirm')}</p>
          <div className="dialog-actions">
            <button className="danger" data-testid="prompt-delete-confirm" onClick={() => void remove()}>
              {t('common.delete')}
            </button>
            <button className="secondary" onClick={() => setDeleteOpen(false)}>
              {t('prompts.keep')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}