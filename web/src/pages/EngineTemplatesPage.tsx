// Engine parameter templates: named sets of inference-engine parameters plus
// the deployment context they apply to (engine, model, topology, accelerator).
// An operator saves a tuned combination once and the deploy form loads it
// instead of retyping a dozen flags.
//
// The whole fleet's engines are a platform concern, so this page is
// platform-admin only, and templates can be exported/imported as a JSON
// document to move a tuning between installations.

import { useCallback, useEffect, useState } from 'react';
import {
  api,
  ApiError,
  formatTime,
  type EngineParamTemplate,
  type ModelSummary,
} from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner } from '../components';
import { engineParams } from '../components/engineParams';
import { CPU_ACCELERATOR, useFleetAccelerators } from '../hooks/useFleetAccelerators';

interface ListResponse {
  response: { code: number; message: string };
  templates?: EngineParamTemplate[];
}

const ENGINES = ['vllm', 'sglang'];
const TOPOLOGIES = ['', 'single', 'multinode', 'pd'];

// argsToRows turns the stored flag map into editable rows. A valueless switch
// is stored as "flag:true"; it round-trips as the same string, so an operator
// never loses the distinction between "flag" and "flag=value".
function argsToRows(args?: Record<string, string>): Array<{ name: string; value: string }> {
  return Object.entries(args || {}).map(([name, value]) => ({ name, value }));
}

function rowsToArgs(rows: Array<{ name: string; value: string }>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const row of rows) {
    const name = row.name.trim();
    if (!name) continue;
    out[name] = row.value.trim();
  }
  return out;
}

export default function EngineTemplatesPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [templates, setTemplates] = useState<EngineParamTemplate[]>([]);
  const [models, setModels] = useState<ModelSummary[]>([]);
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState('');
  const [editTarget, setEditTarget] = useState<EngineParamTemplate | 'new' | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<EngineParamTemplate | null>(null);
  const [ioOpen, setIoOpen] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const params = new URLSearchParams();
      if (search.trim()) params.set('search', search.trim());
      const data = await api.get<ListResponse>(
        `/api/v1/admin/engine-param-templates?${params.toString()}`,
        orgId,
      );
      setTemplates(data.templates || []);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('engineTemplates.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, search, t]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    api
      .get<{ models?: ModelSummary[] }>('/api/v1/admin/models?page.limit=200', orgId)
      .then((data) => setModels((data.models || []).filter((m) => !m.external)))
      .catch(() => setModels([]));
  }, [orgId]);

  const remove = async () => {
    if (!deleteTarget) return;
    try {
      await api.del(`/api/v1/admin/engine-param-templates/${deleteTarget.templateId}`, orgId);
      setDeleteTarget(null);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('engineTemplates.deleteFailed'));
    }
  };

  return (
    <div className="page" data-testid="engine-templates-page">
      <div className="page-header">
        <div>
          <h1>{t('engineTemplates.title')}</h1>
          <div className="subtitle">{t('engineTemplates.subtitle')}</div>
        </div>
        <div className="toolbar">
          <button className="secondary" data-testid="engine-template-io" onClick={() => setIoOpen(true)}>
            {t('engineTemplates.importExport')}
          </button>
          <button data-testid="engine-template-create" onClick={() => setEditTarget('new')}>
            {t('engineTemplates.create')}
          </button>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}
      {saved && <div className="success-banner" data-testid="engine-template-saved">{saved}</div>}

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <input
          type="text"
          data-testid="engine-template-search"
          placeholder={t('common.searchByName')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : templates.length === 0 ? (
          <div className="empty-state" data-testid="engine-templates-empty">
            {t('engineTemplates.empty')}
          </div>
        ) : (
          <table className="data" data-testid="engine-templates-table">
            <thead>
              <tr>
                <th>{t('engineTemplates.colName')}</th>
                <th>{t('engineTemplates.colContext')}</th>
                <th>{t('engineTemplates.colParams')}</th>
                <th>{t('engineTemplates.colUpdated')}</th>
                <th>{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {templates.map((tpl) => (
                <tr key={tpl.templateId} data-testid={`engine-template-row-${tpl.name}`}>
                  <td>
                    <strong>{tpl.name}</strong>
                    {tpl.description && <div className="muted small">{tpl.description}</div>}
                  </td>
                  <td className="small">
                    <div>{tpl.engine}</div>
                    <div className="muted">
                      {[
                        tpl.modelName || tpl.modelId || t('engineTemplates.anyModel'),
                        tpl.topology ? t(`deploy.topology${topologySuffix(tpl.topology)}`) : t('engineTemplates.anyTopology'),
                        tpl.acceleratorType || tpl.accelerator || t('engineTemplates.anyAccelerator'),
                      ].join(' · ')}
                    </div>
                  </td>
                  <td className="small muted">
                    {Object.keys(tpl.engineArgs || {}).length + (tpl.extraArgs || []).length}
                    {t('engineTemplates.paramCount')}
                    {(tpl.roleEngineArgs || []).length > 0 && (
                      <div>{t('engineTemplates.withRoles')}</div>
                    )}
                  </td>
                  <td>{formatTime(tpl.updatedAt)}</td>
                  <td>
                    <button className="link" data-testid={`engine-template-edit-${tpl.name}`} onClick={() => setEditTarget(tpl)}>
                      {t('common.edit')}
                    </button>
                    <button className="link danger" data-testid={`engine-template-delete-${tpl.name}`} onClick={() => setDeleteTarget(tpl)}>
                      {t('common.delete')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {editTarget && (
        <TemplateDialog
          orgId={orgId}
          template={editTarget === 'new' ? null : editTarget}
          models={models}
          onClose={() => setEditTarget(null)}
          onSaved={(message) => {
            setEditTarget(null);
            setSaved(message);
            void load();
          }}
        />
      )}

      {deleteTarget && (
        <Dialog title={t('engineTemplates.deleteTitle')} onClose={() => setDeleteTarget(null)} testId="engine-template-delete-dialog">
          <p>{t('engineTemplates.deleteBody', { name: deleteTarget.name })}</p>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setDeleteTarget(null)}>{t('common.cancel')}</button>
            <button className="danger" data-testid="engine-template-delete-confirm" onClick={() => void remove()}>
              {t('common.delete')}
            </button>
          </div>
        </Dialog>
      )}

      {ioOpen && (
        <TemplateIODialog
          orgId={orgId}
          onClose={() => setIoOpen(false)}
          onImported={(message) => {
            setIoOpen(false);
            setSaved(message);
            void load();
          }}
        />
      )}
    </div>
  );
}

// topologySuffix maps a stored topology to the i18n key suffix the deploy form
// uses (deploy.topologySingle / MultiNode / Pd).
function topologySuffix(topology: string): string {
  switch (topology) {
    case 'multinode':
    case 'distributed':
      return 'MultiNode';
    case 'pd':
      return 'Pd';
    default:
      return 'Single';
  }
}

// TemplateDialog creates or edits one template. The parameter rows are the
// engine catalog's common flags plus whatever the operator adds by hand, so a
// template can hold exactly what the deploy form would have accepted.
function TemplateDialog({
  orgId,
  template,
  models,
  onClose,
  onSaved,
}: {
  orgId: string;
  template: EngineParamTemplate | null;
  models: ModelSummary[];
  onClose: () => void;
  onSaved: (message: string) => void;
}) {
  const { t } = useI18n();
  const fleet = useFleetAccelerators(orgId);
  const [name, setName] = useState(template?.name || '');
  const [description, setDescription] = useState(template?.description || '');
  const [engine, setEngine] = useState(template?.engine || 'vllm');
  const [modelId, setModelId] = useState(template?.modelId || '');
  const [topology, setTopology] = useState(template?.topology || '');
  const [accelerator, setAccelerator] = useState(template?.accelerator || '');
  const [acceleratorType, setAcceleratorType] = useState(template?.acceleratorType || '');
  const [rows, setRows] = useState(argsToRows(template?.engineArgs));
  const [extraArgs, setExtraArgs] = useState((template?.extraArgs || []).join('\n'));
  const [roleRows, setRoleRows] = useState(
    (template?.roleEngineArgs || []).map((r) => ({
      role: r.role,
      rows: argsToRows(r.engineArgs),
      extra: (r.extraArgs || []).join('\n'),
    })),
  );
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  const params = engineParams(engine);

  const addRow = (paramName: string, value: string) => {
    setRows((prev) => {
      if (prev.some((r) => r.name === paramName)) {
        return prev.map((r) => (r.name === paramName ? { name: r.name, value } : r));
      }
      return [...prev, { name: paramName, value }];
    });
  };

  const submit = async () => {
    if (!name.trim()) {
      setError(t('engineTemplates.nameRequired'));
      return;
    }
    setSubmitting(true);
    setError('');
    const payload = {
      name: name.trim(),
      description: description.trim(),
      engine,
      modelId,
      topology,
      accelerator,
      acceleratorType: acceleratorType.trim(),
      engineArgs: rowsToArgs(rows),
      extraArgs: extraArgs.split('\n').map((a) => a.trim()).filter(Boolean),
      roleEngineArgs: roleRows
        .map((r) => ({
          role: r.role,
          engineArgs: rowsToArgs(r.rows),
          extraArgs: r.extra.split('\n').map((a) => a.trim()).filter(Boolean),
        }))
        .filter((r) => Object.keys(r.engineArgs).length > 0 || r.extraArgs.length > 0),
    };
    try {
      if (template) {
        await api.patch(`/api/v1/admin/engine-param-templates/${template.templateId}`, orgId, payload);
        onSaved(t('engineTemplates.updated'));
      } else {
        await api.post('/api/v1/admin/engine-param-templates', orgId, payload);
        onSaved(t('engineTemplates.created'));
      }
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('engineTemplates.saveFailed'));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Dialog
      title={template ? t('engineTemplates.editTitle') : t('engineTemplates.createTitle')}
      onClose={onClose}
      testId="engine-template-dialog"
    >
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="tpl-name">{t('engineTemplates.fieldName')}</label>
          <input id="tpl-name" data-testid="tpl-name" value={name} maxLength={128} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="form-field">
          <label htmlFor="tpl-engine">{t('engineTemplates.fieldEngine')}</label>
          <select id="tpl-engine" data-testid="tpl-engine" value={engine} onChange={(e) => setEngine(e.target.value)}>
            {ENGINES.map((e) => (
              <option key={e} value={e}>{e}</option>
            ))}
          </select>
        </div>
        <div className="form-field full">
          <label htmlFor="tpl-description">{t('common.descriptionOptional')}</label>
          <input id="tpl-description" data-testid="tpl-description" value={description} maxLength={1024} onChange={(e) => setDescription(e.target.value)} />
        </div>
        <div className="form-field">
          <label htmlFor="tpl-model">{t('engineTemplates.fieldModel')}</label>
          <select id="tpl-model" data-testid="tpl-model" value={modelId} onChange={(e) => setModelId(e.target.value)}>
            <option value="">{t('engineTemplates.anyModel')}</option>
            {models.map((m) => (
              <option key={m.modelId} value={m.modelId}>{m.name}</option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="tpl-topology">{t('engineTemplates.fieldTopology')}</label>
          <select id="tpl-topology" data-testid="tpl-topology" value={topology} onChange={(e) => setTopology(e.target.value)}>
            {TOPOLOGIES.map((value) => (
              <option key={value || 'any'} value={value}>
                {value ? t(`deploy.topology${topologySuffix(value)}`) : t('engineTemplates.anyTopology')}
              </option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="tpl-accelerator">{t('engineTemplates.fieldAccelerator')}</label>
          <select id="tpl-accelerator" data-testid="tpl-accelerator" value={accelerator} onChange={(e) => setAccelerator(e.target.value)}>
            <option value="">{t('engineTemplates.anyAccelerator')}</option>
            {[...fleet.vendors, CPU_ACCELERATOR].map((a) => (
              <option key={a} value={a}>{a === CPU_ACCELERATOR ? t('deploy.acceleratorCpu') : a}</option>
            ))}
          </select>
        </div>
        <div className="form-field">
          <label htmlFor="tpl-card">{t('engineTemplates.fieldCardType')}</label>
          <input id="tpl-card" data-testid="tpl-card" value={acceleratorType} maxLength={64} onChange={(e) => setAcceleratorType(e.target.value)} placeholder={t('engineTemplates.anyAccelerator')} />
        </div>
      </div>

      <h4 style={{ marginBottom: 8 }}>{t('engineTemplates.paramsTitle')}</h4>
      <div className="form-grid" data-testid="tpl-param-grid">
        {params.map((p) => {
          const row = rows.find((r) => r.name === p.name);
          const inputId = `tpl-param-${p.name}`;
          return (
            <div className="form-field" key={p.name}>
              <label htmlFor={inputId}>{t(p.labelKey)}</label>
              {p.kind === 'select' ? (
                <select
                  id={inputId}
                  data-testid={inputId}
                  value={row?.value ?? ''}
                  onChange={(e) => (e.target.value ? addRow(p.name, e.target.value) : setRows((prev) => prev.filter((r) => r.name !== p.name)))}
                >
                  <option value="">{t('deploy.engineArgDefault')}</option>
                  {(p.options || []).map((o) => (
                    <option key={o} value={o}>{o}</option>
                  ))}
                </select>
              ) : p.kind === 'switch' ? (
                <input
                  id={inputId}
                  data-testid={inputId}
                  type="checkbox"
                  checked={row?.value === 'flag:true'}
                  onChange={(e) =>
                    e.target.checked
                      ? addRow(p.name, 'flag:true')
                      : setRows((prev) => prev.filter((r) => r.name !== p.name))
                  }
                />
              ) : (
                <input
                  id={inputId}
                  data-testid={inputId}
                  type={p.kind === 'number' ? 'number' : 'text'}
                  value={row?.value ?? ''}
                  placeholder={p.defaultValue}
                  onChange={(e) => (e.target.value ? addRow(p.name, e.target.value) : setRows((prev) => prev.filter((r) => r.name !== p.name)))}
                />
              )}
            </div>
          );
        })}
      </div>

      <h4 style={{ marginBottom: 8 }}>{t('engineTemplates.extraArgsTitle')}</h4>
      <div className="form-grid">
        <div className="form-field full">
          <textarea
            data-testid="tpl-extra-args"
            rows={3}
            value={extraArgs}
            onChange={(e) => setExtraArgs(e.target.value)}
            placeholder="--enable-chunked-prefill"
          />
          <div className="field-hint">{t('engineTemplates.extraArgsHint')}</div>
        </div>
      </div>

      <h4 style={{ marginBottom: 8 }}>{t('engineTemplates.roleTitle')}</h4>
      <div className="form-grid" data-testid="tpl-role-grid">
        {['worker', 'prefill', 'decode'].map((role) => {
          const existing = roleRows.find((r) => r.role === role);
          return (
            <div className="form-field full" key={role}>
              <label>
                <input
                  type="checkbox"
                  data-testid={`tpl-role-toggle-${role}`}
                  checked={!!existing}
                  onChange={(e) =>
                    setRoleRows((prev) =>
                      e.target.checked
                        ? [...prev, { role, rows: [], extra: '' }]
                        : prev.filter((r) => r.role !== role),
                    )
                  }
                />{' '}
                {t('deploy.engineParamsRole', { engine, role: t(`deploy.role.${role}`) })}
              </label>
              {existing && (
                <textarea
                  data-testid={`tpl-role-extra-${role}`}
                  rows={2}
                  value={existing.extra}
                  placeholder="--gpu-memory-utilization 0.5"
                  onChange={(e) =>
                    setRoleRows((prev) =>
                      prev.map((r) => (r.role === role ? { ...r, extra: e.target.value } : r)),
                    )
                  }
                />
              )}
            </div>
          );
        })}
        <div className="form-field full">
          <div className="field-hint">{t('engineTemplates.roleHint')}</div>
        </div>
      </div>

      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button data-testid="tpl-save" disabled={submitting} onClick={() => void submit()}>
          {submitting ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}

// TemplateIODialog exports the templates as a document and imports one back.
function TemplateIODialog({
  orgId,
  onClose,
  onImported,
}: {
  orgId: string;
  onClose: () => void;
  onImported: (message: string) => void;
}) {
  const { t } = useI18n();
  const [payload, setPayload] = useState('');
  const [strategy, setStrategy] = useState('skip');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const exportAll = async () => {
    setBusy(true);
    setError('');
    try {
      const data = await api.get<{ payload?: string }>('/api/v1/admin/engine-param-templates:export', orgId);
      setPayload(data.payload || '');
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('engineTemplates.exportFailed'));
    } finally {
      setBusy(false);
    }
  };

  const importAll = async () => {
    if (!payload.trim()) {
      setError(t('engineTemplates.payloadRequired'));
      return;
    }
    setBusy(true);
    setError('');
    try {
      const res = await api.post<{ created: number; overwritten: number; skipped: number }>(
        '/api/v1/admin/engine-param-templates:import',
        orgId,
        { payload, strategy },
      );
      onImported(
        t('engineTemplates.imported', {
          created: String(res.created ?? 0),
          overwritten: String(res.overwritten ?? 0),
          skipped: String(res.skipped ?? 0),
        }),
      );
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('engineTemplates.importFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog title={t('engineTemplates.importExport')} onClose={onClose} testId="engine-template-io-dialog">
      <p className="muted">{t('engineTemplates.ioHint')}</p>
      <div className="dialog-actions" style={{ justifyContent: 'flex-start' }}>
        <button className="secondary" data-testid="tpl-export" disabled={busy} onClick={() => void exportAll()}>
          {t('engineTemplates.export')}
        </button>
      </div>
      <div className="form-grid">
        <div className="form-field full">
          <label htmlFor="tpl-payload">{t('engineTemplates.payload')}</label>
          <textarea
            id="tpl-payload"
            data-testid="tpl-payload"
            className="mono"
            rows={12}
            value={payload}
            onChange={(e) => setPayload(e.target.value)}
            placeholder={t('engineTemplates.payloadPlaceholder')}
          />
        </div>
        <div className="form-field">
          <label htmlFor="tpl-strategy">{t('engineTemplates.strategy')}</label>
          <select id="tpl-strategy" data-testid="tpl-strategy" value={strategy} onChange={(e) => setStrategy(e.target.value)}>
            <option value="skip">{t('engineTemplates.strategySkip')}</option>
            <option value="overwrite">{t('engineTemplates.strategyOverwrite')}</option>
          </select>
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button data-testid="tpl-import" disabled={busy} onClick={() => void importAll()}>
          {busy ? t('common.saving') : t('engineTemplates.importAction')}
        </button>
      </div>
    </Dialog>
  );
}
