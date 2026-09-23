// Typed client for the control-plane REST gateway. All int64 fields arrive
// as JSON strings (grpc-gateway convention), so numeric-looking strings are
// parsed where the UI needs numbers.

export interface ResponseEnvelope {
  code: number;
  message: string;
}

export class ApiError extends Error {
  code: number;
  constructor(code: number, message: string) {
    super(message);
    this.code = code;
  }
}

async function request<T>(
  method: string,
  path: string,
  orgId: string,
  body?: unknown,
): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: {
      'Content-Type': 'application/json',
      'X-Organization-Id': orgId,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = (await res.json().catch(() => ({}))) as Record<string, unknown>;
  // Business errors come back as {code, message} with HTTP 500; transport
  // errors use other statuses. Branch on the body code, not the HTTP status.
  const envelope = (data.response ?? data) as ResponseEnvelope | undefined;
  if (envelope && typeof envelope.code === 'number' && envelope.code !== 0) {
    throw new ApiError(envelope.code, envelope.message || 'request failed');
  }
  return data as T;
}

export const api = {
  get: <T>(path: string, orgId: string) => request<T>('GET', path, orgId),
  post: <T>(path: string, orgId: string, body?: unknown) =>
    request<T>('POST', path, orgId, body),
  put: <T>(path: string, orgId: string, body?: unknown) =>
    request<T>('PUT', path, orgId, body),
  del: <T>(path: string, orgId: string) => request<T>('DELETE', path, orgId),
};

// ---- shared API shapes ----

export interface PageMeta {
  total: string;
  offset: string;
  limit: string;
}

export interface ApiKeySummary {
  keyId: string;
  name: string;
  prefix: string;
  createdAt: string;
  expiresAt: string;
  revoked: boolean;
  revokedAt: string;
}

export interface ModelSummary {
  modelId: string;
  name: string;
  latestVersion: string;
  weightPath: string;
  createdAt: string;
}

export interface InferenceServiceSummary {
  serviceId: string;
  name: string;
  modelId: string;
  modelVersion: string;
  imageId: string;
  replicas: number;
  state: string;
  updatedAt: string;
}

export function formatTime(unixSeconds: string | number | undefined): string {
  if (unixSeconds === undefined || unixSeconds === null || unixSeconds === '0') return '—';
  const n = typeof unixSeconds === 'string' ? parseInt(unixSeconds, 10) : unixSeconds;
  if (!isFinite(n) || n <= 0) return '—';
  return new Date(n * 1000).toLocaleString();
}
