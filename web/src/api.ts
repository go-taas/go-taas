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
  patch: <T>(path: string, orgId: string, body?: unknown) =>
    request<T>('PATCH', path, orgId, body),
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

export interface ImageSummary {
  imageId: string;
  name: string;
  tag: string;
  accelerator: string;
  engine: string;
  description?: string;
  inUseCount: string;
  lastWarmupState?: string;
  lastWarmupAt?: string;
  createdAt: string;
}

export interface InUseService {
  serviceId: string;
  name: string;
  state: string;
}

export interface WarmupNodeResult {
  node: string;
  state: string;
  message?: string;
}

export interface WarmupTaskSummary {
  taskId: string;
  imageId: string;
  state: string;
  nodeSelector?: Record<string, string>;
  nodeResults?: WarmupNodeResult[];
  failureReason?: string;
  createdAt: string;
  updatedAt: string;
}

// ---- metering ----

export interface TokenUsage {
  promptTokens: string;
  completionTokens: string;
  cachedTokens: string;
  reasoningTokens: string;
}

export interface VoucherSummary {
  voucherId: string;
  requestId: string;
  organizationId: string;
  modelId: string;
  usage?: TokenUsage;
  completedAt: string;
  apiKeyId: string;
  serviceId: string;
  settled: boolean;
}

export interface UsageSummaryRow {
  groupKey: string;
  promptTokens: string;
  completionTokens: string;
  cachedTokens: string;
  reasoningTokens: string;
  requestCount: string;
  settledHours: string;
  pendingHours: string;
}

export interface UsageRecordSummary {
  usageRecordId: string;
  organizationId: string;
  apiKeyId: string;
  periodStart: string;
  periodEnd: string;
  promptTokens: string;
  completionTokens: string;
  cachedTokens: string;
  reasoningTokens: string;
  requestCount: string;
  settledAt: string;
}

// ---- tenancy ----

export interface OrganizationSummary {
  organizationId: string;
  displayName: string;
  description: string;
  state: string;
  apiKeyCount: string;
  inferenceServiceCount: string;
  projectCount: string;
  createdAt: string;
  updatedAt: string;
}

export interface ProjectSummary {
  projectId: string;
  organizationId: string;
  displayName: string;
  description: string;
  state: string;
  createdAt: string;
  updatedAt: string;
}

// ---- billing ----

export interface PriceTier {
  upToTokens: string;
  inputPricePerMillion: number;
  outputPricePerMillion: number;
}
export interface PriceEntry {
  priceId: string;
  modelId: string;
  acceleratorType: string;
  inputPricePerMillion: number;
  outputPricePerMillion: number;
  cachedPricePerMillion: number;
  currency: string;
  effectiveFrom: string;
  tiers: PriceTier[];
  updatedAt: string;
}

export interface ChargeRecordSummary {
  chargeId: string;
  organizationId: string;
  apiKeyId: string;
  modelId: string;
  acceleratorType: string;
  periodStart: string;
  periodEnd: string;
  promptTokens: string;
  completionTokens: string;
  cachedTokens: string;
  reasoningTokens: string;
  requestCount: string;
  amount: number;
  currency: string;
  priceId: string;
  tierIndex: number;
  priced: boolean;
  chargedAt: string;
}

export interface BillSummary {
  billId: string;
  organizationId: string;
  amount: number;
  currency: string;
  periodStart: string;
  periodEnd: string;
  chargeCount: string;
  unpricedCount: string;
}

export function formatTime(unixSeconds: string | number | undefined): string {
  if (unixSeconds === undefined || unixSeconds === null || unixSeconds === '0') return '—';
  const n = typeof unixSeconds === 'string' ? parseInt(unixSeconds, 10) : unixSeconds;
  if (!isFinite(n) || n <= 0) return '—';
  return new Date(n * 1000).toLocaleString();
}
