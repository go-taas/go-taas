// The model-upload engine, kept outside React on purpose.
//
// An upload is a browser-driven stream: the console creates a session, slices
// the archive into parts and PUTs them with a bounded number of XMLHttpRequests
// (which is what gives progress events). If that engine lived in a component,
// any in-console navigation that unmounts the panel — switching tabs, opening
// another model — would abort every part in flight, even though the page is
// still alive. Only a real page unload may interrupt an upload.
//
// So the engine is a module-level singleton: the panel starts/resumes/cancels
// it and subscribes for display, but never owns it. A pagehide handler marks
// the session interrupted so a refresh leaves a resumable row (the browser is
// genuinely gone), while SPA navigation changes nothing.

import { ApiError, apiPrefix, sessionAuthHeader, type ModelUploadSession } from '../api';

export type UploadJobStatus = 'idle' | 'uploading' | 'processing' | 'succeeded' | 'failed' | 'cancelled';

export interface UploadJobState {
  status: UploadJobStatus;
  uploadId: string;
  fileName: string;
  totalBytes: number;
  bytesSent: number;
  error: string;
}

// MAX_IN_FLIGHT caps how many part PUTs run at once. Four parallel streams
// saturate most links without overwhelming the server or the browser.
const MAX_IN_FLIGHT = 4;

// POLL_MS is how often the engine re-checks a completed session that is
// unpacking on the server.
const POLL_MS = 2000;

const CANCELLED = Symbol('cancelled');

const IDLE: UploadJobState = {
  status: 'idle',
  uploadId: '',
  fileName: '',
  totalBytes: 0,
  bytesSent: 0,
  error: '',
};

type Listener = () => void;

class UploadManager {
  private state: UploadJobState = IDLE;
  private listeners = new Set<Listener>();
  private xhrs = new Set<XMLHttpRequest>();
  private cancelled = false;
  private pollTimer: number | null = null;
  private pageHideBound = false;

  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  getState = (): UploadJobState => this.state;

  /** busy reports whether an upload is being driven right now. */
  busy(): boolean {
    return this.state.status === 'uploading' || this.state.status === 'processing';
  }

  /** reset clears a settled job so the UI can act on it once. */
  reset(): void {
    if (this.busy()) return;
    this.state = IDLE;
    this.emit();
  }

  /** start opens a session and streams its parts. */
  async start(
    file: File,
    meta: { name: string; hubModelId: string; description: string; storageBackend: string },
  ): Promise<void> {
    if (this.busy()) return;
    this.begin(file);
    try {
      const session = await this.request<ModelUploadSession>('POST', '/models/uploads', {
        name: meta.name,
        hubModelId: meta.hubModelId,
        description: meta.description,
        storageBackend: meta.storageBackend,
        archiveName: file.name,
        totalBytes: file.size,
      });
      await this.stream(session, file);
    } catch (err) {
      this.settle(err);
    }
  }

  /** resume continues a session the server still holds parts for. */
  async resume(session: ModelUploadSession, file: File): Promise<void> {
    if (this.busy()) return;
    this.begin(file);
    try {
      await this.stream(session, file);
    } catch (err) {
      this.settle(err);
    }
  }

  /** cancel stops the engine and removes the session and its staged parts. */
  async cancel(): Promise<void> {
    this.cancelled = true;
    for (const xhr of this.xhrs) xhr.abort();
    this.xhrs.clear();
    this.stopPolling();
    const uploadId = this.state.uploadId;
    if (uploadId) {
      try {
        await this.deleteSession(uploadId);
      } catch {
        // Best effort: the server also cleans up abandoned sessions.
      }
    }
    this.setState({ status: 'cancelled', error: '' });
  }

  /**
   * cancelSession stops a row the engine may or may not be driving: an
   * interrupted row has no live request, so only the session is deleted (the
   * server aborts the multipart upload and drops the staged bytes).
   */
  async cancelSession(uploadId: string): Promise<void> {
    if (this.state.uploadId === uploadId && this.busy()) {
      await this.cancel();
      return;
    }
    await this.deleteSession(uploadId);
  }

  private begin(file: File): void {
    this.cancelled = false;
    this.bindPageHide();
    this.setState({
      status: 'uploading',
      uploadId: '',
      fileName: file.name,
      totalBytes: file.size,
      bytesSent: 0,
      error: '',
    });
  }

  private async stream(session: ModelUploadSession, file: File): Promise<void> {
    this.setState({ uploadId: session.uploadId, totalBytes: session.totalBytes });
    await this.uploadParts(session, file);
    if (this.cancelled) throw CANCELLED;
    this.setState({ status: 'processing' });
    await this.request('POST', `/models/uploads/${session.uploadId}:complete`);
    const outcome = await this.waitForOutcome(session.uploadId);
    if (this.cancelled) throw CANCELLED;
    switch (outcome) {
      case 'succeeded':
        this.setState({ status: 'succeeded', bytesSent: session.totalBytes });
        break;
      case 'cancelled':
        this.setState({ status: 'cancelled' });
        break;
      case 'failed':
        this.setState({ status: 'failed' });
        break;
      default:
        throw new ApiError(0, 'upload outcome unknown');
    }
  }

  private settle(err: unknown): void {
    if (err === CANCELLED || this.cancelled) return;
    this.setState({
      status: 'failed',
      error: err instanceof Error ? err.message : 'upload failed',
    });
  }

  private async uploadParts(session: ModelUploadSession, file: File): Promise<void> {
    const { uploadId, partSizeBytes, totalBytes } = session;
    const part = partSizeBytes > 0 ? partSizeBytes : Math.max(totalBytes, 1);
    const partCount = Math.max(1, Math.ceil(totalBytes / part));
    const { parts } = await this.request<{ parts?: number[] }>(
      'GET',
      `/models/uploads/${uploadId}/parts`,
    );
    const received = new Set(parts || []);
    const missing: number[] = [];
    for (let n = 1; n <= partCount; n += 1) {
      if (!received.has(n)) missing.push(n);
    }

    let sent = 0;
    const sendPart = (n: number) =>
      new Promise<void>((resolve, reject) => {
        const start = (n - 1) * part;
        const end = Math.min(start + part, totalBytes);
        const blob = file.slice(start, end);
        const xhr = new XMLHttpRequest();
        this.xhrs.add(xhr);
        xhr.open('PUT', `${apiPrefix('admin')}/models/uploads/${uploadId}/parts/${n}`);
        const authorization = sessionAuthHeader('admin');
        if (authorization) xhr.setRequestHeader('Authorization', authorization);
        xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream');
        xhr.upload.onprogress = (event) => {
          if (!event.lengthComputable) return;
          this.setState({ bytesSent: sent + event.loaded });
        };
        xhr.onload = () => {
          if (this.cancelled) {
            reject(CANCELLED);
            return;
          }
          this.xhrs.delete(xhr);
          if (xhr.status >= 200 && xhr.status < 300) {
            sent += blob.size;
            this.setState({ bytesSent: sent });
            resolve();
            return;
          }
          let message = 'upload failed';
          try {
            const body = JSON.parse(xhr.responseText) as { message?: string };
            if (body.message) message = body.message;
          } catch {
            // Keep the generic message.
          }
          reject(new ApiError(0, message));
        };
        xhr.onerror = () => {
          this.xhrs.delete(xhr);
          reject(new ApiError(0, 'upload failed'));
        };
        xhr.onabort = () => {
          this.xhrs.delete(xhr);
          reject(CANCELLED);
        };
        xhr.send(blob);
      });

    // Worker pool: each worker claims the next pending part when one finishes.
    let next = 0;
    const workers = Array.from({ length: Math.min(MAX_IN_FLIGHT, missing.length) }, async () => {
      while (!this.cancelled) {
        const n = missing[next];
        if (n === undefined) return;
        next += 1;
        await sendPart(n);
      }
    });
    await Promise.all(workers);
    if (this.cancelled) throw CANCELLED;
  }

  private waitForOutcome(
    uploadId: string,
  ): Promise<'succeeded' | 'failed' | 'cancelled'> {
    return new Promise((resolve, reject) => {
      const check = async () => {
        let session: ModelUploadSession;
        try {
          session = await this.request<ModelUploadSession>('GET', `/models/uploads/${uploadId}`);
        } catch (err) {
          this.stopPolling();
          reject(err);
          return;
        }
        if (session.state === 'succeeded') {
          this.stopPolling();
          resolve('succeeded');
          return;
        }
        if (session.state === 'failed') {
          this.stopPolling();
          this.setState({ error: session.message || 'upload failed' });
          resolve('failed');
          return;
        }
        if (session.state === 'cancelled') {
          this.stopPolling();
          resolve('cancelled');
          return;
        }
        // Still unpacking on the server: keep polling.
      };
      void check();
      this.pollTimer = window.setInterval(() => void check(), POLL_MS);
    });
  }

  private stopPolling(): void {
    if (this.pollTimer !== null) {
      window.clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
  }

  private async deleteSession(uploadId: string): Promise<void> {
    await this.request('DELETE', `/models/uploads/${uploadId}`);
  }

  private bindPageHide(): void {
    if (this.pageHideBound) return;
    this.pageHideBound = true;
    // A real page unload (refresh, tab close) kills the part requests; tell the
    // server at once so the row becomes resumable instead of looking stuck.
    // SPA navigation never fires pagehide, which is the point.
    window.addEventListener('pagehide', () => {
      if (!this.busy() || !this.state.uploadId) return;
      const headers: Record<string, string> = {};
      const authorization = sessionAuthHeader('admin');
      if (authorization) headers['Authorization'] = authorization;
      void fetch(`${apiPrefix('admin')}/models/uploads/${this.state.uploadId}:interrupt`, {
        method: 'POST',
        headers,
        keepalive: true,
      }).catch(() => undefined);
    });
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
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
      throw new ApiError(0, 'upload request failed');
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
      throw new ApiError(res.status, message || 'upload failed');
    }
    return data as T;
  }

  private setState(patch: Partial<UploadJobState>): void {
    this.state = { ...this.state, ...patch };
    this.emit();
  }

  private emit(): void {
    for (const listener of this.listeners) listener();
  }
}

export const uploadManager = new UploadManager();
