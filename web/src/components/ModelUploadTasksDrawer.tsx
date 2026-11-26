// Model uploads task list: the resumable upload sessions of this console.
//
// It is embedded in ModelUploadPanel (embedded=true) so the local upload panel
// shows both the form and its sessions, the way the download panel shows its
// form and downloads. The standalone section mode is kept for pages that want
// the list on its own, and is what its section header and toggle render.
//
// Unlike downloads (which run entirely server-side) an upload session is driven
// in part by the browser it started in, so the list is a place the operator
// returns to: it shows what is still unpacking on the server, what succeeded
// into the catalog, and what failed with the server's reason.

import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { navigate } from '../router';
import { ApiError, apiPrefix, sessionAuthHeader, formatTime, type ModelUploadSession } from '../api';
import { useI18n } from '../i18n';
import { ErrorBanner, RowActionsMenu, StatusError } from '../components';
import { formatBytes } from './ModelDownloadsPanel';
import { uploadManager } from '../upload/manager';

// ACTIVE_STATES are the session states that still change on their own, so the
// panel keeps polling while any of them is present.
const ACTIVE_STATES = ['creating', 'uploading', 'completing', 'processing'];

// CANCELLABLE_STATES are the states an operator may still stop from here.
// An interrupted session is included: its staged parts are still on the
// server, and cancelling is how an operator abandons it and reclaims them.
const CANCELLABLE_STATES = ['uploading', 'processing', 'interrupted'];

// RESUMABLE_STATES are the states whose bytes are still on the server and can
// be continued by picking the same archive again.
const RESUMABLE_STATES = ['interrupted'];

interface ListResponse {
  uploads?: ModelUploadSession[];
}

export default function ModelUploadTasksDrawer({
  defaultOpen = false,
  embedded = false,
  active = true,
  refreshKey = 0,
  onResume,
}: {
  defaultOpen?: boolean;
  // embedded drops the standalone section header and toggle: the upload panel
  // owns them and shows this list inside itself, so the two cannot drift apart.
  embedded?: boolean;
  // active is false while the containing panel is collapsed; the list then
  // stops loading and polling until it is shown again.
  active?: boolean;
  refreshKey?: number;
  // onResume hands an interrupted session back to the page, which opens the
  // upload panel on it so the operator can pick the same archive and continue.
  onResume?: (session: ModelUploadSession) => void;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(defaultOpen || embedded);
  const [uploads, setUploads] = useState<ModelUploadSession[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  // A small set of session ids whose cancel is in flight, so the action shows
  // a progressing state instead of double-firing.
  const [cancelling, setCancelling] = useState<Set<string>>(new Set());
  const pollRef = useRef<number | null>(null);
  // The engine may be driving a resume this list cannot see from the rows
  // alone (a just-created session, or an interrupted row flipped back to
  // uploading). Subscribing keeps the list polling and refreshed.
  const job = useSyncExternalStore(uploadManager.subscribe, uploadManager.getState);
  const managerBusy = job.status === 'uploading' || job.status === 'processing';

  // jsonRequest talks to the upload endpoints directly: they return plain
  // objects and authenticate with the admin session token (like the dialog).
  const jsonRequest = useCallback(
    async <T,>(method: string, path: string, body?: unknown): Promise<T> => {
      const headers: Record<string, string> = { 'Content-Type': 'application/json' };
      const authorization = sessionAuthHeader('admin');
      if (authorization) headers['Authorization'] = authorization;
      let res: Response;
      try {
        res = await fetch(`${apiPrefix('admin')}${path}`, {
          method,
          headers,
          body: body === undefined ? undefined : JSON.stringify(body),
        });
      } catch {
        throw new ApiError(0, t('models.uploads.loadFailed'));
      }
      let data: Record<string, unknown> = {};
      try {
        data = (await res.json()) as Record<string, unknown>;
      } catch {
        // A non-JSON response is a transport failure.
      }
      if (!res.ok) {
        const message =
          (data.message as string | undefined) ||
          (data.response as { message?: string } | undefined)?.message;
        throw new ApiError(res.status, message || t('models.uploads.loadFailed'));
      }
      return data as T;
    },
    [t],
  );

  const load = useCallback(
    async (quiet = false) => {
      if (!quiet) setLoading(true);
      try {
        const data = await jsonRequest<ListResponse>('GET', '/models/uploads?limit=20');
        setUploads(data.uploads || []);
        setError('');
      } catch (e) {
        setError(e instanceof Error ? e.message : t('models.uploads.loadFailed'));
      } finally {
        if (!quiet) setLoading(false);
      }
    },
    [jsonRequest, t],
  );

  const visible = embedded || open;
  useEffect(() => {
    if (!visible || !active) return;
    void load();
  }, [visible, active, load]);

  // The form that embeds this list bumps refreshKey when a session starts or
  // ends, so the new row shows up (and its progress starts moving) at once
  // instead of waiting for the next poll tick.
  useEffect(() => {
    if (!active) return;
    void load(true);
  }, [refreshKey]);

  // Poll while any session is still live so progress and the outcome advance.
  // The interval is cleared as soon as every session reached a terminal state.
  const hasActive = useMemo(
    () => managerBusy || uploads.some((u) => ACTIVE_STATES.includes(u.state)),
    [managerBusy, uploads],
  );
  useEffect(() => {
    if (!visible || !active || !hasActive) return undefined;
    pollRef.current = window.setInterval(() => void load(true), 3000);
    return () => {
      if (pollRef.current !== null) window.clearInterval(pollRef.current);
      pollRef.current = null;
    };
  }, [visible, active, hasActive, load]);

  // reload when the engine moves to a new phase, so a resumed row stops
  // reading as interrupted the moment its parts start landing again.
  useEffect(() => {
    if (!active) return;
    void load(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [job.status]);

  const cancel = async (session: ModelUploadSession) => {
    setCancelling((s) => new Set(s).add(session.uploadId));
    try {
      await uploadManager.cancelSession(session.uploadId);
      await load(true);
    } catch (e) {
      setError(
        e instanceof ApiError ? `${e.message} (code ${e.code})` : t('models.uploads.cancelFailed'),
      );
    } finally {
      setCancelling((s) => {
        const next = new Set(s);
        next.delete(session.uploadId);
        return next;
      });
    }
  };

  return (
    <section data-testid={embedded ? "upload-tasks" : "model-uploads-panel"}>
      {!embedded && (
      <div className="section-header">
        <div>
          <h2 style={{ margin: 0 }}>{t('models.uploads.title')}</h2>
          <div className="subtitle">{t('models.uploads.subtitle')}</div>
        </div>
        <button className="secondary" data-testid="toggle-uploads-list" onClick={() => setOpen((o) => !o)}>
          {open ? t('models.uploads.hide') : t('models.uploads.open')}
        </button>
      </div>

      )}

      {error && <ErrorBanner message={error} />}

      {visible && (
        <div className="panel" data-testid="uploads-list">
          <h2>{t('models.uploads.jobsTitle')}</h2>
          {loading ? (
            <div className="loading">{t('common.loading')}</div>
          ) : uploads.length === 0 ? (
            <div className="empty-state" data-testid="uploads-empty">
              {t('models.uploads.empty')}
            </div>
          ) : (
            <div className="table-scroll">
              <table className="table">
                <thead>
                  <tr>
                    <th>{t('models.uploads.colModel')}</th>
                    <th>{t('models.uploads.colVersion')}</th>
                    <th>{t('models.uploads.colState')}</th>
                    <th>{t('models.uploads.colProgress')}</th>
                    <th>{t('models.uploads.colCreated')}</th>
                    <th className="col-actions">{t('common.actions')}</th>
                  </tr>
                </thead>
                <tbody>
                  {uploads.map((session) => (
                    <tr key={session.uploadId} data-testid={`upload-row-${session.name}`}>
                      <td>
                        <strong className="mono">{session.hubModelId || session.name}</strong>
                        <div className="muted small">{session.name}</div>
                        {session.storageBackend && (
                          <div className="muted small">{session.storageBackend}</div>
                        )}
                      </td>
                      <td className="mono muted">{session.version || '—'}</td>
                      <td>
                        <span className={`badge ${stateBadgeClass(session.state)}`}>
                          {t(`models.uploads.state.${session.state}`)}
                        </span>
                        <StatusError message={session.message} testId={`upload-error-${session.uploadId}`} />
                        {session.modelId && (
                          <button
                            className="link"
                            onClick={() => navigate(`/admin/models/${session.modelId}`)}
                          >
                            {t('models.uploads.viewModel')}
                          </button>
                        )}
                      </td>
                      <td style={{ minWidth: 180 }}>
                        <div className="muted small" data-testid={`upload-bytes-${session.uploadId}`}>
                          {progressLabel(session)}
                        </div>
                        {ACTIVE_STATES.includes(session.state) && (
                          <UploadProgress session={session} />
                        )}
                      </td>
                      <td>{formatTime(session.createdAt)}</td>
                      <td>
                        {(CANCELLABLE_STATES.includes(session.state) ||
                          RESUMABLE_STATES.includes(session.state)) && (
                          <RowActionsMenu
                            label={t('common.actionsFor', { name: session.name })}
                            testId={`upload-actions-${session.uploadId}`}
                            actions={[
                              // An interrupted session keeps its parts on the
                              // server, so resuming it only sends the missing
                              // chunks instead of starting from zero.
                              ...(RESUMABLE_STATES.includes(session.state)
                                ? [
                                    {
                                      key: `resume-${session.uploadId}`,
                                      label: t('models.uploads.resume'),
                                      onClick: () => onResume?.(session),
                                    },
                                  ]
                                : []),
                              ...(CANCELLABLE_STATES.includes(session.state)
                                ? [
                                    {
                                      key: `cancel-${session.uploadId}`,
                                      label: t('models.uploads.cancel'),
                                      danger: true,
                                      disabled: cancelling.has(session.uploadId),
                                      onClick: () => void cancel(session),
                                    },
                                  ]
                                : []),
                            ]}
                          />
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

// asNumber normalizes a byte field that may arrive as a JSON string.
function asNumber(input: string | number | undefined): number {
  if (typeof input === 'number') return input;
  return parseInt(input || '0', 10) || 0;
}

// progressLabel renders "12.3 MB / 940.1 MB (1%)" while both byte counts are
// known.
function progressLabel(session: ModelUploadSession): string {
  const total = asNumber(session.totalBytes);
  if (total <= 0) return formatBytes(session.bytesReceived);
  const pct = uploadPercent(session);
  return `${formatBytes(session.bytesReceived)} / ${formatBytes(total)} (${pct}%)`;
}

// uploadPercent converts the sampled byte count into a whole percent, clamped
// to 100 because a partly-written file can overshoot the advertised size.
function uploadPercent(session: ModelUploadSession): number {
  const total = asNumber(session.totalBytes);
  if (total <= 0) return 0;
  const done = asNumber(session.bytesReceived);
  const pct = Math.round((done / total) * 100);
  return pct < 0 ? 0 : pct > 100 ? 100 : pct;
}

// UploadProgress draws a determinate bar while a session is still moving;
// before the first byte it shows the indeterminate animation instead of a bar
// stuck at zero.
function UploadProgress({ session }: { session: ModelUploadSession }) {
  const total = asNumber(session.totalBytes);
  if (total <= 0) {
    return (
      <div className="progress-track">
        <div className="progress-bar indeterminate" data-testid="upload-progress-indeterminate" />
      </div>
    );
  }
  const pct = uploadPercent(session);
  return (
    <div
      className="progress-track"
      role="progressbar"
      aria-valuenow={pct}
      aria-valuemin={0}
      aria-valuemax={100}
      data-testid={`upload-progress-${session.uploadId}`}
      data-percent={pct}
    >
      <div className="progress-fill" style={{ width: `${pct}%` }} />
    </div>
  );
}

// stateBadgeClass maps a session state to its badge modifier.
function stateBadgeClass(state: string): string {
  switch (state) {
    case 'succeeded':
      return 'active';
    case 'failed':
      return 'error';
    case 'cancelled':
      return 'muted';
    default:
      return 'info';
  }
}