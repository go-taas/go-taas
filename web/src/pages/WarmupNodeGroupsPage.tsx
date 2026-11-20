// Warmup node groups page: the platform-managed node pools whose local disks
// hold warmed model copies. These are deliberately not Helm values: an
// administrator creates and edits them here, the model service persists them
// and the controller materializes KServe LocalModelNodeGroup resources.

import { useCallback, useEffect, useState } from 'react';
import { api, ApiError, type WarmupNodeGroup } from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { Dialog, ErrorBanner, RowActionsMenu, Toast } from '../components';

interface ListResponse {
  response: { code: number; message: string };
  nodeGroups: WarmupNodeGroup[];
}

interface GroupForm {
  name: string;
  selector: string;
  storageLimit: string;
  hostPath: string;
  storageClass: string;
}

const EMPTY_FORM: GroupForm = {
  name: '',
  selector: 'kserve/localmodel=worker',
  storageLimit: '100Gi',
  hostPath: '/models',
  storageClass: '',
};

// parseSelector turns one "key=value" line per selector into the map the API
// stores. Blank lines are ignored; a line without "=" is reported.
function parseSelector(value: string): { selector: Record<string, string>; error: string } {
  const selector: Record<string, string> = {};
  for (const raw of value.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    const at = line.indexOf('=');
    if (at <= 0) return { selector, error: 'selector' };
    selector[line.slice(0, at).trim()] = line.slice(at + 1).trim();
  }
  return { selector, error: Object.keys(selector).length ? '' : 'selector' };
}

function selectorText(selector?: Record<string, string>): string {
  return Object.entries(selector || {})
    .map(([key, value]) => `${key}=${value}`)
    .join('\n');
}

export default function WarmupNodeGroupsPage() {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const [groups, setGroups] = useState<WarmupNodeGroup[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [editing, setEditing] = useState<WarmupNodeGroup | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<WarmupNodeGroup | null>(null);
  const [deleting, setDeleting] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.get<ListResponse>('/api/v1/admin/model-warmup-node-groups', orgId);
      setGroups(data.nodeGroups || []);
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : t('warmupGroups.loadFailed'));
    } finally {
      setLoading(false);
    }
  }, [orgId, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const remove = async () => {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await api.del(`/api/v1/admin/model-warmup-node-groups/${encodeURIComponent(deleteTarget.name)}`, orgId);
      setNotice(t('warmupGroups.deleted', { name: deleteTarget.name }));
      setDeleteTarget(null);
      await load();
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('warmupGroups.deleteFailed'));
    } finally {
      setDeleting(false);
    }
  };

  return (
    <div>
      <div className="page-header">
        <div>
          <h1>{t('warmupGroups.title')}</h1>
          <div className="subtitle">{t('warmupGroups.subtitle')}</div>
        </div>
        <div className="toolbar">
          <button className="secondary" onClick={() => void load()}>{t('common.refresh')}</button>
          <button data-testid="create-warmup-node-group" onClick={() => setCreateOpen(true)}>
            {t('warmupGroups.create')}
          </button>
        </div>
      </div>

      <div className="guide-grid" data-testid="warmup-group-guide">
        <article className="guide-card">
          <div className="guide-body">
            <strong>{t('warmupGroups.guideTitle')}</strong>
            <p>{t('warmupGroups.guideBody')}</p>
          </div>
        </article>
        <article className="guide-card">
          <div className="guide-body">
            <strong>{t('warmupGroups.selectorTitle')}</strong>
            <p>{t('warmupGroups.selectorBody')}</p>
          </div>
        </article>
        <article className="guide-card">
          <div className="guide-body">
            <strong>{t('warmupGroups.storageTitle')}</strong>
            <p>{t('warmupGroups.storageBody')}</p>
          </div>
        </article>
      </div>

      {error && <ErrorBanner message={error} />}
      <Toast message={notice} onDismiss={() => setNotice('')} testId="warmup-groups-toast" />

      <div className="panel">
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : groups.length === 0 ? (
          <div className="empty-state" data-testid="warmup-groups-empty">{t('warmupGroups.empty')}</div>
        ) : (
          <table className="data" data-testid="warmup-groups-table">
            <thead>
              <tr>
                <th>{t('common.name')}</th>
                <th>{t('warmupGroups.colSelector')}</th>
                <th>{t('warmupGroups.colStorage')}</th>
                <th>{t('warmupGroups.colHostPath')}</th>
                <th>{t('warmupGroups.colNodes')}</th>
                <th className="col-actions">{t('common.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {groups.map((group) => (
                <tr key={group.name} data-testid={`warmup-group-${group.name}`}>
                  <td><strong>{group.name}</strong></td>
                  <td className="mono muted">{Object.entries(group.nodeSelector || {}).map(([k, v]) => `${k}=${v}`).join(', ') || '—'}</td>
                  <td className="mono">{group.storageLimit || '—'}{group.storageClass ? <div className="muted">{group.storageClass}</div> : null}</td>
                  <td className="mono muted">{group.hostPath || '—'}</td>
                  <td>{group.readyNodes || 0} / {group.totalNodes || 0}</td>
                  <td>
                    <RowActionsMenu
                      label={t('common.actionsFor', { name: group.name })}
                      testId={`warmup-group-actions-${group.name}`}
                      actions={[
                        { key: 'edit', label: t('common.edit'), onClick: () => setEditing(group) },
                        { key: 'delete', label: t('common.delete'), danger: true, onClick: () => setDeleteTarget(group) },
                      ]}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {(createOpen || editing) && (
        <NodeGroupDialog
          orgId={orgId}
          existing={editing}
          onClose={() => { setCreateOpen(false); setEditing(null); }}
          onDone={(name) => {
            setCreateOpen(false);
            setEditing(null);
            setNotice(t(editing ? 'warmupGroups.updated' : 'warmupGroups.created', { name }));
            void load();
          }}
        />
      )}

      {deleteTarget && (
        <Dialog title={t('warmupGroups.deleteTitle', { name: deleteTarget.name })} onClose={() => setDeleteTarget(null)} testId="delete-warmup-group-dialog">
          <p>{t('warmupGroups.deleteBody')}</p>
          <div className="dialog-actions">
            <button className="secondary" onClick={() => setDeleteTarget(null)}>{t('common.cancel')}</button>
            <button className="danger" disabled={deleting} onClick={() => void remove()}>
              {deleting ? t('common.working') : t('common.delete')}
            </button>
          </div>
        </Dialog>
      )}
    </div>
  );
}

function NodeGroupDialog({ orgId, existing, onClose, onDone }: {
  orgId: string;
  existing: WarmupNodeGroup | null;
  onClose: () => void;
  onDone: (name: string) => void;
}) {
  const { t } = useI18n();
  const [form, setForm] = useState<GroupForm>(existing ? {
    name: existing.name,
    selector: selectorText(existing.nodeSelector),
    storageLimit: existing.storageLimit || '100Gi',
    hostPath: existing.hostPath || '/models',
    storageClass: existing.storageClass || '',
  } : EMPTY_FORM);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    setBusy(true);
    setError('');
    const parsed = parseSelector(form.selector);
    if (parsed.error) {
      setError(t('warmupGroups.selectorInvalid'));
      setBusy(false);
      return;
    }
    const payload = {
      name: form.name.trim(),
      nodeSelector: parsed.selector,
      storageLimit: form.storageLimit.trim(),
      hostPath: form.hostPath.trim(),
      storageClass: form.storageClass.trim(),
    };
    try {
      if (existing) {
        await api.put(`/api/v1/admin/model-warmup-node-groups/${encodeURIComponent(existing.name)}`, orgId, payload);
        onDone(existing.name);
      } else {
        await api.post('/api/v1/admin/model-warmup-node-groups', orgId, payload);
        onDone(form.name.trim());
      }
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (code ${e.code})` : t('warmupGroups.saveFailed'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog title={existing ? t('warmupGroups.editTitle') : t('warmupGroups.createTitle')} onClose={onClose} testId="warmup-group-dialog">
      <div className="form-grid">
        <div className="form-field">
          <label htmlFor="warmup-group-name">{t('common.name')}</label>
          <input id="warmup-group-name" value={form.name} disabled={Boolean(existing)} onChange={(e) => setForm({ ...form, name: e.target.value })} />
        </div>
        <div className="form-field">
          <label htmlFor="warmup-group-storage">{t('warmupGroups.storageLimit')}</label>
          <input id="warmup-group-storage" value={form.storageLimit} onChange={(e) => setForm({ ...form, storageLimit: e.target.value })} />
        </div>
        <div className="form-field full">
          <label htmlFor="warmup-group-selector">{t('warmupGroups.colSelector')}</label>
          <textarea id="warmup-group-selector" rows={3} value={form.selector} onChange={(e) => setForm({ ...form, selector: e.target.value })} />
          <div className="muted">{t('warmupGroups.selectorHint')}</div>
        </div>
        <div className="form-field">
          <label htmlFor="warmup-group-host-path">{t('warmupGroups.colHostPath')}</label>
          <input id="warmup-group-host-path" value={form.hostPath} onChange={(e) => setForm({ ...form, hostPath: e.target.value })} />
        </div>
        <div className="form-field">
          <label htmlFor="warmup-group-class">{t('warmupGroups.storageClass')}</label>
          <input id="warmup-group-class" value={form.storageClass} onChange={(e) => setForm({ ...form, storageClass: e.target.value })} />
        </div>
      </div>
      {error && <ErrorBanner message={error} />}
      <div className="dialog-actions">
        <button className="secondary" onClick={onClose}>{t('common.cancel')}</button>
        <button disabled={busy || !form.name.trim()} onClick={() => void submit()}>
          {busy ? t('common.saving') : t('common.save')}
        </button>
      </div>
    </Dialog>
  );
}
