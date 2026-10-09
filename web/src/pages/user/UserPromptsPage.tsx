// End-user Prompts page (feature #43): create, version, organize, search,
// and reuse prompts, and browse and copy shared templates. End-user
// surface: route /prompts, API /api/v1/prompts/*. No organization
// dropdown (the caller's org is implicit, AD4).

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../../surface';
import { useOrg } from '../../org';
import { useI18n } from '../../i18n';
import { navigate } from '../../router';
import { formatTime, type PageMeta } from '../../api';
import { Dialog, ErrorBanner, Pagination } from '../../components';

export interface Prompt {
  promptId: string;
  organizationId: string;
  name: string;
  content: string;
  modelId: string;
  folderId: string;
  version: string;
  variables: string[];
  timesUsed: string;
  lastUsedAt: string;
  createdAt: string;
  updatedAt: string;
}

export interface PromptFolder {
  folderId: string;
  name: string;
  promptCount: string;
}

export interface PromptTemplate {
  templateId: string;
  name: string;
  content: string;
  modelId: string;
  variables: string[];
  timesUsed: string;
  createdAt: string;
  updatedAt: string;
}

interface ListPromptsResponse {
  response: { code: number; message: string };
  prompts: Prompt[];
  pageMeta?: PageMeta;
}

interface ListFoldersResponse {
  response: { code: number; message: string };
  folders: PromptFolder[];
}

interface ListTemplatesResponse {
  response: { code: number; message: string };
  templates: PromptTemplate[];
}

interface CreatePromptResponse {
  response: { code: number; message: string };
  prompt: Prompt;
}

interface CreateFolderResponse {
  response: { code: number; message: string };
  folder: PromptFolder;
}

interface CopyTemplateResponse {
  response: { code: number; message: string };
  prompt: Prompt;
}

const PAGE_SIZE = 20;

export default function UserPromptsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [mode, setMode] = useState<'prompts' | 'templates'>('prompts');
  const [prompts, setPrompts] = useState<Prompt[]>([]);
  const [folders, setFolders] = useState<PromptFolder[]>([]);
  const [templates, setTemplates] = useState<PromptTemplate[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [search, setSearch] = useState('');
  const [folderFilter, setFolderFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [folderOpen, setFolderOpen] = useState(false);
  const [busyId, setBusyId] = useState('');
  const [deleteTarget, setDeleteTarget] = useState<Prompt | null>(null);
  const [copied, setCopied] = useState('');

  const loadPrompts = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams();
      params.set('page.offset', String(offset));
      params.set('page.limit', String(PAGE_SIZE));
      if (search) params.set('search', search);
      if (folderFilter) params.set('folder_id', folderFilter);
      const data = await api.get<ListPromptsResponse>(`/api/v1/prompts?${params.toString()}`, orgId);
      setPrompts(data.prompts || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, offset, search, folderFilter, t]);

  const loadFolders = useCallback(async () => {
    try {
      const data = await api.get<ListFoldersResponse>('/api/v1/prompts/folders', orgId);
      setFolders(data.folders || []);
    } catch {
      // Folders are auxiliary; ignore load errors.
    }
  }, [api, orgId]);

  const loadTemplates = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<ListTemplatesResponse>('/api/v1/prompts/templates', orgId);
      setTemplates(data.templates || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    void loadFolders();
  }, [loadFolders]);

  useEffect(() => {
    if (mode === 'prompts') void loadPrompts();
    else void loadTemplates();
  }, [mode, loadPrompts, loadTemplates]);

  const remove = async (p: Prompt) => {
    setBusyId(p.promptId);
    try {
      await api.del(`/api/v1/prompts/${p.promptId}`, orgId);
      setDeleteTarget(null);
      void loadPrompts();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.deleteFailed'));
    } finally {
      setBusyId('');
    }
  };

  const copyText = async (p: Prompt) => {
    try {
      await navigator.clipboard.writeText(p.content);
      setCopied(p.promptId);
      setTimeout(() => setCopied(''), 1500);
    } catch {
      // Clipboard may be unavailable; content remains selectable.
    }
  };

  const copyTemplate = async (tpl: PromptTemplate) => {
    setBusyId(tpl.templateId);
    try {
      await api.post<CopyTemplateResponse>(`/api/v1/prompts/templates/${tpl.templateId}/copy`, orgId, {});
      setMode('prompts');
      void loadPrompts();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.copyFailed'));
    } finally {
      setBusyId('');
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('prompts.title')}</h1>
          <div className="subtitle">{t('prompts.userSubtitle')}</div>
        </div>
        <div className="header-actions">
          <button className="secondary" data-testid="prompt-new-folder" onClick={() => setFolderOpen(true)}>
            {t('prompts.newFolder')}
          </button>
          <button data-testid="prompt-new" onClick={() => setCreateOpen(true)}>
            {t('prompts.new')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="toolbar">
        <input
          type="text"
          placeholder={t('prompts.searchPlaceholder')}
          value={search}
          data-testid="prompt-search"
          onChange={(e) => {
            setSearch(e.target.value);
            setOffset(0);
          }}
        />
        <select
          value={folderFilter}
          data-testid="prompt-folder-filter"
          onChange={(e) => {
            setFolderFilter(e.target.value);
            setOffset(0);
          }}
        >
          <option value="">{t('prompts.allFolders')}</option>
          {folders.map((f) => (
            <option key={f.folderId} value={f.folderId}>
              {f.name}
            </option>
          ))}
        </select>
        <div className="segmented">
          <button
            className={mode === 'prompts' ? 'active' : ''}
            data-testid="prompt-mode-mine"
            onClick={() => setMode('prompts')}
          >
            {t('prompts.myPrompts')}
          </button>
          <button
            className={mode === 'templates' ? 'active' : ''}
            data-testid="prompt-mode-templates"
            onClick={() => setMode('templates')}
          >
            {t('prompts.sharedTemplates')}
          </button>
        </div>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : mode === 'prompts' ? (
          prompts.length === 0 ? (
            <div className="empty-state" data-testid="prompt-empty">
              {t('prompts.userEmpty')}
            </div>
          ) : (
            <table className="data" data-testid="prompt-table">
              <thead>
                <tr>
                  <th>{t('prompts.colName')}</th>
                  <th>{t('prompts.colModel')}</th>
                  <th>{t('prompts.colFolder')}</th>
                  <th>{t('prompts.colVersion')}</th>
                  <th>{t('prompts.colUsed')}</th>
                  <th>{t('prompts.colLastUsed')}</th>
                  <th>{t('prompts.colUpdated')}</th>
                  <th>{t('common.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {prompts.map((p) => (
                  <tr key={p.promptId} data-testid={`prompt-row-${p.promptId}`}>
                    <td>
                      <a
                        href={`/prompts/${p.promptId}`}
                        onClick={(e) => {
                          e.preventDefault();
                          navigate(`/prompts/${p.promptId}`);
                        }}
                      >
                        {p.name}
                      </a>
                    </td>
                    <td>{p.modelId || '—'}</td>
                    <td>{folders.find((f) => f.folderId === p.folderId)?.name || '—'}</td>
                    <td>{p.version}</td>
                    <td>{p.timesUsed}</td>
                    <td>{formatTime(p.lastUsedAt)}</td>
                    <td>{formatTime(p.updatedAt)}</td>
                    <td>
                      <button
                        className="link"
                        data-testid={`prompt-playground-${p.promptId}`}
                        onClick={() => navigate(`/playground?prompt=${p.promptId}&version=${p.version}`)}
                      >
                        {t('prompts.openPlayground')}
                      </button>
                      <button className="link" data-testid={`prompt-copy-${p.promptId}`} onClick={() => void copyText(p)}>
                        {copied === p.promptId ? t('prompts.copied') : t('prompts.copy')}
                      </button>
                      <button
                        className="link"
                        data-testid={`prompt-edit-${p.promptId}`}
                        onClick={() => navigate(`/prompts/${p.promptId}`)}
                      >
                        {t('common.edit')}
                      </button>
                      <button
                        className="link danger"
                        disabled={busyId === p.promptId}
                        data-testid={`prompt-delete-${p.promptId}`}
                        onClick={() => setDeleteTarget(p)}
                      >
                        {t('common.delete')}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )
        ) : templates.length === 0 ? (
          <div className="empty-state" data-testid="template-empty">
            {t('prompts.templatesEmpty')}
          </div>
        ) : (
          <table className="data" data-testid="template-table">
            <thead>
              <tr>
                <th>{t('prompts.colName')}</th>
                <th>{t('prompts.colModel')}</th>
                <th>{t('prompts.colUsed')}</th>
                <th>{t('prompts.colUpdated')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {templates.map((tpl) => (
                <tr key={tpl.templateId} data-testid={`template-row-${tpl.templateId}`}>
                  <td>{tpl.name}</td>
                  <td>{tpl.modelId || '—'}</td>
                  <td>{tpl.timesUsed}</td>
                  <td>{formatTime(tpl.updatedAt)}</td>
                  <td>
                    <button
                      className="link"
                      disabled={busyId === tpl.templateId}
                      data-testid={`template-copy-${tpl.templateId}`}
                      onClick={() => void copyTemplate(tpl)}
                    >
                      {t('prompts.copyTemplate')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {mode === 'prompts' && total > PAGE_SIZE && (
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        )}
      </div>

      {createOpen && (
        <CreatePromptDialog
          orgId={orgId}
          folders={folders}
          onClose={() => setCreateOpen(false)}
          onCreated={(p) => {
            setCreateOpen(false);
            navigate(`/prompts/${p.promptId}`);
          }}
        />
      )}

      {folderOpen && (
        <CreateFolderDialog
          orgId={orgId}
          onClose={() => setFolderOpen(false)}
          onCreated={() => {
            setFolderOpen(false);
            void loadFolders();
          }}
        />
      )}

      {deleteTarget && (
        <Dialog title={t('prompts.deleteTitle')} testId="prompt-delete-dialog" onClose={() => setDeleteTarget(null)}>
          <p>{t('prompts.deleteConfirm')}</p>
          <div className="dialog-actions">
            <button className="danger" data-testid="prompt-delete-confirm" onClick={() => void remove(deleteTarget)}>
              {t('common.delete')}
            </button>
            <button className="secondary" onClick={() => setDeleteTarget(null)}>
              {t('prompts.keep')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

function CreatePromptDialog({
  orgId,
  folders,
  onClose,
  onCreated,
}: {
  orgId: string;
  folders: PromptFolder[];
  onClose: () => void;
  onCreated: (p: Prompt) => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [content, setContent] = useState('');
  const [modelId, setModelId] = useState('');
  const [folderId, setFolderId] = useState('');
  const [variables, setVariables] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    if (!name.trim()) {
      setError(t('prompts.needName'));
      return;
    }
    if (!content.trim()) {
      setError(t('prompts.needContent'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      // Variables use ${var} syntax (design FR1.4); a bare name typed
      // into the tag input is wrapped automatically so the server-side
      // 12706 validation only fires on genuinely malformed references.
      const vars = variables
        .split(',')
        .map((v) => v.trim())
        .filter(Boolean)
        .map((v) => (v.startsWith('${') && v.endsWith('}') ? v : `\${${v.replace(/^\$\{|\}$/g, '')}}`));
      const data = await api.post<CreatePromptResponse>('/api/v1/prompts', orgId, {
        name: name.trim(),
        content,
        model_id: modelId || undefined,
        folder_id: folderId || undefined,
        variables: vars,
      });
      onCreated(data.prompt);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('prompts.new')} testId="prompt-create-dialog" onClose={onClose}>
      <div className="form-field">
        <label>{t('prompts.colName')}</label>
        <input value={name} data-testid="prompt-name-input" onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="form-field">
        <label>{t('prompts.colContent')}</label>
        <textarea value={content} data-testid="prompt-content-input" onChange={(e) => setContent(e.target.value)} />
      </div>
      <div className="form-field">
        <label>{t('prompts.colModel')}</label>
        <input value={modelId} data-testid="prompt-model-input" onChange={(e) => setModelId(e.target.value)} />
      </div>
      <div className="form-field">
        <label>{t('prompts.colFolder')}</label>
        <select value={folderId} data-testid="prompt-folder-input" onChange={(e) => setFolderId(e.target.value)}>
          <option value="">{t('prompts.noFolder')}</option>
          {folders.map((f) => (
            <option key={f.folderId} value={f.folderId}>
              {f.name}
            </option>
          ))}
        </select>
      </div>
      <div className="form-field">
        <label>{t('prompts.colVariables')}</label>
        <input value={variables} data-testid="prompt-variables-input" onChange={(e) => setVariables(e.target.value)} />
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button disabled={submitting} data-testid="prompt-create-submit" onClick={() => void submit()}>
          {t('prompts.create')}
        </button>
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
      </div>
    </Dialog>
  );
}

function CreateFolderDialog({
  orgId,
  onClose,
  onCreated,
}: {
  orgId: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  const api = useApi();
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    if (!name.trim()) {
      setError(t('prompts.needFolderName'));
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      await api.post<CreateFolderResponse>('/api/v1/prompts/folders', orgId, { name: name.trim() });
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('prompts.newFolder')} testId="prompt-folder-dialog" onClose={onClose}>
      <div className="form-field">
        <label>{t('prompts.colName')}</label>
        <input value={name} data-testid="prompt-folder-name-input" onChange={(e) => setName(e.target.value)} />
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button disabled={submitting} data-testid="prompt-folder-submit" onClick={() => void submit()}>
          {t('prompts.create')}
        </button>
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
      </div>
    </Dialog>
  );
}