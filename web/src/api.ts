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

// Session token storage (feature #7). The console stores the SSO session
// token and sends it as Authorization: Bearer; when no session exists it
// falls back to the transitional X-Organization-Id header.
const SESSION_KEY = 'go-taas.session-token';

export function getSessionToken(): string {
  return localStorage.getItem(SESSION_KEY) || '';
}

export function setSessionToken(token: string): void {
  if (token) {
    localStorage.setItem(SESSION_KEY, token);
  } else {
    localStorage.removeItem(SESSION_KEY);
  }
}

async function request<T>(
  method: string,
  path: string,
  orgId: string,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const sessionToken = getSessionToken();
  if (sessionToken) {
    // Session present: the session's active org is authoritative (D6).
    headers['Authorization'] = `Bearer ${sessionToken}`;
  } else {
    // No session: transitional header for CLI/transitional access.
    headers['X-Organization-Id'] = orgId;
  }
  const res = await fetch(path, {
    method,
    headers,
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

// ---- sso federation (feature #7) ----

export interface SSOProvider {
  providerId: string;
  type: string; // oidc | saml | ldap
  displayName: string;
  issuer?: string;
  clientId?: string;
  clientSecret?: string;
  redirectUri?: string;
  scopes?: string;
  metadataUrl?: string;
  entityId?: string;
  acsUrl?: string;
  host?: string;
  port?: number;
  bindDn?: string;
  baseDn?: string;
  userFilter?: string;
  enabled: boolean;
  defaultOrg?: string;
  allowAutoProvision: boolean;
  attributeMapping?: string;
  createdAt: string;
  updatedAt: string;
}

export interface IdentityBinding {
  bindingId: string;
  providerId: string;
  externalSubject: string;
  userId: string;
  createdAt: string;
}

export interface SessionInfo {
  userId: string;
  username: string;
  roles: string[];
  accessibleOrgs: string[];
  activeOrg: string;
  expiresAt: string;
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

// ---- billing accounts (feature #8) ----
// int64 cents fields serialize as JSON strings.

export interface BillingAccount {
  accountId: string;
  organizationId: string;
  mode: string; // prepaid | postpaid
  balanceCents: string;
  monthlyQuotaCents: string;
  usedThisCycleCents: string;
  cycleStartedAt: string;
  overdrawPolicy: string; // block | warn
  currency: string;
  remainingCents: string;
  quotaUsagePercent: number;
  createdAt: string;
  updatedAt: string;
}

export interface BillingTransaction {
  transactionId: string;
  accountId: string;
  type: string; // recharge | deduction | refund
  amountCents: string;
  balanceAfterCents: string;
  idempotencyKey: string;
  reference: string;
  createdAt: string;
}

// formatCents renders an int64-cents string as a major-unit amount.
export function formatCents(cents: string | number | undefined): string {
  if (cents === undefined || cents === null || cents === '') return '0.00';
  const n = typeof cents === 'string' ? parseInt(cents, 10) : cents;
  if (!isFinite(n)) return '0.00';
  return (n / 100).toFixed(2);
}

export function formatTime(unixSeconds: string | number | undefined): string {
  if (unixSeconds === undefined || unixSeconds === null || unixSeconds === '0') return '—';
  const n = typeof unixSeconds === 'string' ? parseInt(unixSeconds, 10) : unixSeconds;
  if (!isFinite(n) || n <= 0) return '—';
  return new Date(n * 1000).toLocaleString();
}
