// Image import panel: pull an engine image from an external registry into the
// platform's own Harbor project. The copy runs as a background task on the
// server, so the panel starts it and polls the task list for progress.
//
// It expands in place below the images toolbar, exactly like the model
// download panel: the form folds away once a copy starts so the history table
// takes the stage, and reopening it is one click on the header.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { navigate } from '../router';
import {
  api,
  ApiError,
  formatTime,
  type ImageImportTask,
  type ImageRegistryConfig,
  type PageMeta,
} from '../api';
import { useOrg } from '../org';
import { useI18n } from '../i18n';
import { ErrorBanner, Pagination, StatusError } from '../components';
import { FieldLabel, FormLegend, FormSection, RequiredStatus } from './FormField';
import { CPU_ACCELERATOR, useFleetAccelerators } from '../hooks/useFleetAccelerators';

interface ListResponse {
  response: { code: number; message: string };
  tasks?: ImageImportTask[];
  pageMeta?: PageMeta;
}

const PAGE_SIZE = 10;
// ACTIVE_STATES are the task states that still change on their own, so the
// panel keeps polling while any of them is present.
const ACTIVE_STATES = ['pending', 'running'];

export default function ImageImportPanel({
  defaultOpen = false,
  registry,
  onDone,
}: {
  defaultOpen?: boolean;
  registry: ImageRegistryConfig | null;
  // onDone lets the page reload the catalog once the panel is happy.
  onDone?: () => void;
}) {
  const { orgId } = useOrg();
  const { t } = useI18n();
  const fleet = useFleetAccelerators(orgId);

  const [tasks, setTasks] = useState<ImageImportTask[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // The form is collapsed until asked for: the history is what an operator
  // returns to, and the form is a detour.
  const [formOpen, setFormOpen] = useState(defaultOpen);

  const [sourceReference, setSourceReference] = useState('');
  const [accelerator, setAccelerator] = useState('');
  const [engine, setEngine] = useState('');
  const [description, setDescription] = useState('');
  const [sourceUsername, setSourceUsername] = useState('');
  const [sourcePassword, setSourcePassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState('');
  const pollRef = useRef<number | null>(null);

  useEffect(() => {
    if (!accelerator) setAccelerator(fleet.vendors[0] || CPU_ACCELERATOR);
  }, [accelerator, fleet.vendors]);

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true);
      try {
        const data = await api.get<ListResponse>(
          `/api/v1/admin/image-import-tasks?page.offset=${offset}&page.limit=${PAGE_SIZE}`,
          orgId,
        );
        setTasks(data.tasks || []);
        setTotal(parseInt(data.pageMeta?.total || '0', 10) || 0);
        setError('');
      } catch (e) {
        setError(e instanceof Error ? e.message : t('images.importHistoryLoadFailed'));
      } finally {
        if (!quiet) setLoading(false);
      }
    },
    [orgId, offset, t],
  );

  useEffect(() => {
    void load();
  }, [load]);

  // Poll while a task still runs so its state and progress advance. The
  // interval is cleared as soon as every task reached a terminal state.
  const hasActive = useMemo(
    () => tasks.some((task) => ACTIVE_STATES.includes(task.state)),
    [tasks],
  );
  useEffect(() => {
    if (!hasActive) return undefined;
    pollRef.current = window.setInterval(() => void load(true), 2000);
    return () => {
      if (pollRef.current !== null) window.clearInterval(pollRef.current);
      pollRef.current = null;
    };
  }, [hasActive, load]);

  const submit = async () => {
    const ref = sourceReference.trim();
    if (!ref) {
      setFormError(t('images.validationSource'));
      return;
    }
    if (!engine.trim()) {
      setFormError(t('images.validationEngine'));
      return;
    }
    setSubmitting(true);
    setFormError('');
    try {
      await api.post('/api/v1/admin/images:import', orgId, {
        sourceReference: ref,
        accelerator,
        engine: engine.trim(),
        description: description.trim(),
        sourceUsername: sourceUsername.trim() || undefined,
        sourcePassword: sourceUsername.trim() ? sourcePassword : undefined,
      });
      setSourceReference('');
      setEngine('');
      setDescription('');
      setSourceUsername('');
      setSourcePassword('');
      setOffset(0);
      // A started import folds the form away so the history table takes the
      // stage, matching the model download panel.
      setFormOpen(false);
      await load();
      onDone?.();
    } catch (e) {
      setFormError(
        e instanceof ApiError ? `${e.message} (code ${e.code})` : t('images.importFailed'),
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <section data-testid="image-import-panel">
      <div className="section-header">
        <div>
          <h2 style={{ margin: 0 }}>{t('images.importTitle')}</h2>
          <div className="subtitle">{t('images.importSubtitle')}</div>
        </div>
        <button
          className={formOpen ? 'secondary' : undefined}
          data-testid="toggle-import-form"
          onClick={() => setFormOpen((open) => !open)}
        >
          {formOpen ? t('common.cancel') : t('images.importOpen')}
        </button>
      </div>

      {error && <ErrorBanner message={error} />}

      {formOpen && (
        <div className="panel" style={{ marginBottom: 16 }} data-testid="import-form">
          <h2>{t('images.importFormTitle')}</h2>
          <p className="muted">
            {registry?.configured
              ? t('images.importHintConfigured', {
                  registry: `${registry.hostname}/${registry.project}/`,
                })
              : t('images.importHint')}
          </p>
          <p className="muted" data-testid="import-async-hint">
            {t('images.importAsyncHint')}
          </p>
          <FormLegend testId="import-image-legend" />
          <FormSection
            step={1}
            title={t('images.importSectionSource')}
            hint={t('images.importSectionSourceHint')}
            requirement="required"
            testId="import-image-section-source"
          >
            <div className="form-grid">
              <div className="form-field full">
                <FieldLabel
                  htmlFor="import-source"
                  text={t('images.sourceReference')}
                  requirement="required"
                />
                <input
                  id="import-source"
                  className="mono"
                  data-testid="import-source-input"
                  value={sourceReference}
                  onChange={(e) => setSourceReference(e.target.value)}
                  placeholder="docker.io/library/vllm:v0.6.3"
                />
                <div className="field-hint">
                  {sourceReference.trim() && registry?.configured
                    ? t('images.importTarget', {
                        registry: `${registry.hostname}/${registry.project}/`,
                        name: derivedName(sourceReference),
                      })
                    : t('images.importTargetHint')}
                </div>
              </div>
            </div>
          </FormSection>

          <FormSection
            step={2}
            title={t('images.importSectionRuntime')}
            hint={t('images.importSectionRuntimeHint')}
            requirement="required"
            testId="import-image-section-runtime"
          >
            <div className="form-grid">
              <div className="form-field">
                <FieldLabel
                  htmlFor="import-accelerator"
                  text={t('images.fieldAccelerator')}
                  requirement="defaulted"
                />
                <select
                  id="import-accelerator"
                  data-testid="import-accelerator-select"
                  value={accelerator}
                  onChange={(e) => setAccelerator(e.target.value)}
                >
                  {[...fleet.vendors, CPU_ACCELERATOR].map((a) => (
                    <option key={a} value={a}>
                      {a === CPU_ACCELERATOR ? t('images.acceleratorCpu') : a}
                    </option>
                  ))}
                </select>
              </div>
              <div className="form-field">
                <FieldLabel
                  htmlFor="import-engine"
                  text={t('images.fieldEngine')}
                  requirement="required"
                />
                <input
                  id="import-engine"
                  data-testid="import-engine-input"
                  value={engine}
                  maxLength={64}
                  onChange={(e) => setEngine(e.target.value)}
                  placeholder={t('images.placeholderEngine')}
                />
              </div>
            </div>
          </FormSection>

          <FormSection
            step={3}
            title={t('images.importSectionAuth')}
            hint={t('images.importSectionAuthHint')}
            requirement="optional"
            testId="import-image-section-auth"
          >
            <div className="form-grid">
              <div className="form-field">
                <FieldLabel
                  htmlFor="import-src-user"
                  text={t('images.sourceUsername')}
                  requirement="optional"
                />
                <input
                  id="import-src-user"
                  data-testid="import-source-username"
                  value={sourceUsername}
                  maxLength={255}
                  autoComplete="off"
                  onChange={(e) => setSourceUsername(e.target.value)}
                  placeholder={t('images.sourceAuthOptional')}
                />
              </div>
              <div className="form-field">
                <FieldLabel
                  htmlFor="import-src-pass"
                  text={t('images.sourcePassword')}
                  requirement="optional"
                />
                <input
                  id="import-src-pass"
                  type="password"
                  data-testid="import-source-password"
                  value={sourcePassword}
                  disabled={!sourceUsername.trim()}
                  autoComplete="off"
                  onChange={(e) => setSourcePassword(e.target.value)}
                />
              </div>
              <div className="form-field full">
                <FieldLabel
                  htmlFor="import-desc"
                  text={t('common.descriptionOptional')}
                  requirement="optional"
                />
                <textarea
                  id="import-desc"
                  data-testid="import-description"
                  rows={2}
                  maxLength={1024}
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
              </div>
            </div>
          </FormSection>

          {formError && <ErrorBanner message={formError} />}

          <div className="deploy-actions">
            <RequiredStatus
              missing={[
                !sourceReference.trim() && t('images.sourceReference'),
                !engine.trim() && t('images.fieldEngine'),
              ].filter((entry): entry is string => Boolean(entry))}
            />
            <button
              data-testid="import-image-submit"
              disabled={submitting}
              onClick={() => void submit()}
            >
              {submitting ? t('images.importing') : t('images.import')}
            </button>
          </div>
        </div>
      )}

      <div className="panel" data-testid="import-history">
        <h2>{t('images.importHistoryTitle')}</h2>
        {loading ? (
          <div className="loading">{t('common.loading')}</div>
        ) : tasks.length === 0 ? (
          <div className="empty-state" data-testid="import-history-empty">
            {t('images.importHistoryEmpty')}
          </div>
        ) : (
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{t('images.importColSource')}</th>
                  <th>{t('images.importColTarget')}</th>
                  <th>{t('images.importColState')}</th>
                  <th>{t('images.importColProgress')}</th>
                  <th>{t('images.importColCreated')}</th>
                </tr>
              </thead>
              <tbody>
                {tasks.map((task) => (
                  <tr key={task.taskId} data-testid={`import-row-${task.taskId}`}>
                    <td className="mono">{shortReference(task.sourceReference)}</td>
                    <td className="mono muted">
                      {task.destinationReference
                        ? shortReference(task.destinationReference)
                        : '—'}
                    </td>
                    <td className="download-state">
                      <span className={`badge ${stateBadgeClass(task.state)}`}>
                        {t(`images.importState.${task.state}`)}
                      </span>
                      <StatusError
                        message={task.failureReason}
                        testId={`import-error-${task.taskId}`}
                      />
                      {task.imageId && (
                        <button
                          className="link"
                          onClick={() => navigate(`/admin/images/${task.imageId}`)}
                        >
                          {t('images.viewImage')}
                        </button>
                      )}
                    </td>
                    <td style={{ minWidth: 160 }}>
                      {ACTIVE_STATES.includes(task.state) ? (
                        // The copy reports no byte count, so the bar is
                        // indeterminate rather than stuck at zero.
                        <div className="progress-track">
                          <div
                            className="progress-bar indeterminate"
                            data-testid={`import-progress-${task.taskId}`}
                          />
                        </div>
                      ) : (
                        <span className="muted">—</span>
                      )}
                    </td>
                    <td>{formatTime(task.createdAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {total > PAGE_SIZE && (
          <Pagination
            offset={offset}
            limit={PAGE_SIZE}
            total={total}
            onPageChange={setOffset}
          />
        )}
      </div>
    </section>
  );
}

// derivedName mirrors the server's destination rule: only the image's last
// path segment and tag survive.
function derivedName(reference: string): string {
  const ref = reference.trim();
  if (!ref) return '';
  const withoutTag = ref.includes(':') ? ref.slice(0, ref.lastIndexOf(':')) : ref;
  const leaf = withoutTag.split('/').filter(Boolean).pop() || '';
  const tag = ref.includes(':') ? ref.slice(ref.lastIndexOf(':') + 1) : 'latest';
  return `${leaf}:${tag}`;
}

// shortReference drops the registry host so a long source reference still
// fits a table cell; the full value stays in the title attribute.
function shortReference(reference: string): string {
  const trimmed = reference.trim();
  if (!trimmed) return '—';
  const slash = trimmed.indexOf('/');
  if (slash < 0) return trimmed;
  const first = trimmed.slice(0, slash);
  if (!first.includes('.') && !first.includes(':') && first !== 'localhost') {
    return trimmed;
  }
  const rest = trimmed.slice(slash + 1);
  return rest || trimmed;
}

// stateBadgeClass maps an import task state to its badge modifier.
function stateBadgeClass(state: string): string {
  switch (state) {
    case 'succeeded':
      return 'active';
    case 'failed':
      return 'error';
    default:
      return 'info';
  }
}
