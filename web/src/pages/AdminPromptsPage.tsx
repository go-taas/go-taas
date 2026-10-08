// Admin Prompts page (feature #43): view all prompts across tenants
// (masked), see prompt usage analytics, and curate the shared template
// library. Admin surface: route /admin/prompts, API /api/v1/admin/prompts/*.
// Content is masked (D4).

import { useCallback, useEffect, useState } from 'react';
import { useApi } from '../surface';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { formatTime, type PageMeta } from '../api';
import { Dialog, ErrorBanner, Pagination } from '../components';
import type { Prompt, PromptTemplate } from './user/UserPromptsPage';

interface ListPromptsResponse {
  response: { code: number; message: string };
  prompts: Prompt[];
  pageMeta?: PageMeta;
}

interface UsageResponse {
  response: { code: number; message: string };
  totalPrompts: string;
  totalUses: string;
  mostUsed: PromptUsageRow[];
  byOrganization: OrgUsageRow[];
}

interface PromptUsageRow {
  promptId: string;
  name: string;
  organizationId: string;
  timesUsed: string;
}

interface OrgUsageRow {
  organizationId: string;
  promptCount: string;
  totalUses: string;
}

interface ListTemplatesResponse {
  response: { code: number; message: string };
  templates: PromptTemplate[];
}

interface CreateTemplateResponse {
  response: { code: number; message: string };
  template: PromptTemplate;
}

const PAGE_SIZE = 20;

export default function AdminPromptsPage() {
  const api = useApi();
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [tab, setTab] = useState<'prompts' | 'usage' | 'templates'>('prompts');
  const [prompts, setPrompts] = useState<Prompt[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [usage, setUsage] = useState<UsageResponse | null>(null);
  const [templates, setTemplates] = useState<PromptTemplate[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [createOpen, setCreateOpen] = useState(false);
  const [busyId, setBusyId] = useState('');
  const [deleteTarget, setDeleteTarget] = useState<PromptTemplate | null>(null);

  const loadPrompts = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const params = new URLSearchParams();
      params.set('page.offset', String(offset));
      params.set('page.limit', String(PAGE_SIZE));
      const data = await api.get<ListPromptsResponse>(`/api/v1/admin/prompts?${params.toString()}`, orgId);
      setPrompts(data.prompts || []);
      setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, offset, t]);

  const loadUsage = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<UsageResponse>('/api/v1/admin/prompts/usage', orgId);
      setUsage(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  const loadTemplates = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await api.get<ListTemplatesResponse>('/api/v1/admin/prompts/templates', orgId);
      setTemplates(data.templates || []);
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [api, orgId, t]);

  useEffect(() => {
    if (tab === 'prompts') void loadPrompts();
    else if (tab === 'usage') void loadUsage();
    else void loadTemplates();
  }, [tab, loadPrompts, loadUsage, loadTemplates]);

  const removeTemplate = async (tpl: PromptTemplate) => {
    setBusyId(tpl.templateId);
    try {
      await api.del(`/api/v1/admin/prompts/templates/${tpl.templateId}`, orgId);
      setDeleteTarget(null);
      void loadTemplates();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.deleteFailed'));
    } finally {
      setBusyId('');
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('prompts.title')}</h1>
          <div className="subtitle">{t('prompts.adminSubtitle')}</div>
        </div>
        <div className="header-actions">
          {tab === 'templates' && (
            <button data-testid="admin-template-new" onClick={() => setCreateOpen(true)}>
              {t('prompts.newTemplate')}
            </button>
          )}
        </div>
      </div>

      {error && <ErrorBanner message={error} />}

      <div className="tabs">
        <button
          className={tab === 'prompts' ? 'active' : ''}
          data-testid="admin-prompts-tab-prompts"
          onClick={() => setTab('prompts')}
        >
          {t('prompts.allPrompts')}
        </button>
        <button
          className={tab === 'usage' ? 'active' : ''}
          data-testid="admin-prompts-tab-usage"
          onClick={() => setTab('usage')}
        >
          {t('prompts.usage')}
        </button>
        <button
          className={tab === 'templates' ? 'active' : ''}
          data-testid="admin-prompts-tab-templates"
          onClick={() => setTab('templates')}
        >
          {t('prompts.templates')}
        </button>
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : tab === 'prompts' ? (
          prompts.length === 0 ? (
            <div className="empty-state" data-testid="admin-prompts-empty">
              {t('prompts.adminEmpty')}
            </div>
          ) : (
            <table className="data" data-testid="admin-prompts-table">
              <thead>
                <tr>
                  <th>{t('prompts.colName')}</th>
                  <th>{t('prompts.colOrganization')}</th>
                  <th>{t('prompts.colModel')}</th>
                  <th>{t('prompts.colVersion')}</th>
                  <th>{t('prompts.colUsed')}</th>
                  <th>{t('prompts.colLastUsed')}</th>
                  <th>{t('prompts.colUpdated')}</th>
                </tr>
              </thead>
              <tbody>
                {prompts.map((p) => (
                  <tr key={p.promptId} data-testid={`admin-prompt-row-${p.promptId}`}>
                    <td>{p.name}</td>
                    <td>{p.organizationId}</td>
                    <td>{p.modelId || '—'}</td>
                    <td>{p.version}</td>
                    <td>{p.timesUsed}</td>
                    <td>{formatTime(p.lastUsedAt)}</td>
                    <td>{formatTime(p.updatedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )
        ) : tab === 'usage' ? (
          !usage ? (
            <div className="empty-state" data-testid="admin-prompts-usage-empty">
              {t('prompts.usageEmpty')}
            </div>
          ) : (
            <div data-testid="admin-prompts-usage">
              <div className="kv-grid">
                <div>
                  <span className="kv-label">{t('prompts.totalPrompts')}</span>
                  <span>{usage.totalPrompts}</span>
                </div>
                <div>
                  <span className="kv-label">{t('prompts.totalUses')}</span>
                  <span>{usage.totalUses}</span>
                </div>
              </div>
              <h4>{t('prompts.mostUsed')}</h4>
              <table className="data">
                <thead>
                  <tr>
                    <th>{t('prompts.colName')}</th>
                    <th>{t('prompts.colOrganization')}</th>
                    <th>{t('prompts.colUsed')}</th>
                  </tr>
                </thead>
                <tbody>
                  {usage.mostUsed.map((row) => (
                    <tr key={row.promptId}>
                      <td>{row.name}</td>
                      <td>{row.organizationId}</td>
                      <td>{row.timesUsed}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <h4>{t('prompts.byOrganization')}</h4>
              <table className="data">
                <thead>
                  <tr>
                    <th>{t('prompts.colOrganization')}</th>
                    <th>{t('prompts.promptCount')}</th>
                    <th>{t('prompts.totalUses')}</th>
                  </tr>
                </thead>
                <tbody>
                  {usage.byOrganization.map((row) => (
                    <tr key={row.organizationId}>
                      <td>{row.organizationId}</td>
                      <td>{row.promptCount}</td>
                      <td>{row.totalUses}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        ) : templates.length === 0 ? (
          <div className="empty-state" data-testid="admin-templates-empty">
            {t('prompts.templatesEmpty')}
          </div>
        ) : (
          <table className="data" data-testid="admin-templates-table">
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
                <tr key={tpl.templateId} data-testid={`admin-template-row-${tpl.templateId}`}>
                  <td>{tpl.name}</td>
                  <td>{tpl.modelId || '—'}</td>
                  <td>{tpl.timesUsed}</td>
                  <td>{formatTime(tpl.updatedAt)}</td>
                  <td>
                    <button
                      className="link danger"
                      disabled={busyId === tpl.templateId}
                      data-testid={`admin-template-delete-${tpl.templateId}`}
                      onClick={() => setDeleteTarget(tpl)}
                    >
                      {t('common.delete')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {tab === 'prompts' && total > PAGE_SIZE && (
          <Pagination offset={offset} limit={PAGE_SIZE} total={total} onPageChange={setOffset} />
        )}
      </div>

      {createOpen && (
        <CreateTemplateDialog
          orgId={orgId}
          onClose={() => setCreateOpen(false)}
          onCreated={() => {
            setCreateOpen(false);
            void loadTemplates();
          }}
        />
      )}

      {deleteTarget && (
        <Dialog title={t('prompts.deleteTemplateTitle')} testId="admin-template-delete-dialog" onClose={() => setDeleteTarget(null)}>
          <p>{t('prompts.deleteTemplateConfirm')}</p>
          <div className="dialog-actions">
            <button className="danger" data-testid="admin-template-delete-confirm" onClick={() => void removeTemplate(deleteTarget)}>
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

function CreateTemplateDialog({
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
  const [content, setContent] = useState('');
  const [modelId, setModelId] = useState('');
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
      const vars = variables
        .split(',')
        .map((v) => v.trim())
        .filter(Boolean);
      await api.post<CreateTemplateResponse>('/api/v1/admin/prompts/templates', orgId, {
        name: name.trim(),
        content,
        model_id: modelId || undefined,
        variables: vars,
      });
      onCreated();
    } catch (e) {
      setError(e instanceof Error ? e.message : t('prompts.createFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog title={t('prompts.newTemplate')} testId="admin-template-create-dialog" onClose={onClose}>
      <div className="form-field">
        <label>{t('prompts.colName')}</label>
        <input value={name} data-testid="admin-template-name-input" onChange={(e) => setName(e.target.value)} />
      </div>
      <div className="form-field">
        <label>{t('prompts.colContent')}</label>
        <textarea value={content} data-testid="admin-template-content-input" onChange={(e) => setContent(e.target.value)} />
      </div>
      <div className="form-field">
        <label>{t('prompts.colModel')}</label>
        <input value={modelId} data-testid="admin-template-model-input" onChange={(e) => setModelId(e.target.value)} />
      </div>
      <div className="form-field">
        <label>{t('prompts.colVariables')}</label>
        <input value={variables} data-testid="admin-template-variables-input" onChange={(e) => setVariables(e.target.value)} />
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button disabled={submitting} data-testid="admin-template-create-submit" onClick={() => void submit()}>
          {t('prompts.create')}
        </button>
        <button className="secondary" onClick={onClose}>
          {t('common.cancel')}
        </button>
      </div>
    </Dialog>
  );
}