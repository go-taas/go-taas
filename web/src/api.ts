// Typed client for the control-plane REST gateway. All int64 fields arrive
// as JSON strings (grpc-gateway convention), so numeric-looking strings are
// parsed where the UI needs numbers.
//
// Console surface separation (feature-17): the client is realm-scoped. A
// realm is either "user" (end-user console, API prefix /api/v1/*) or
// "admin" (admin console, API prefix /api/v1/admin/*). Each realm keeps its
// own session token and organization keys, and the client refuses a path
// whose prefix does not belong to its realm (a runtime mirror of the
// gateway realm guard).

export type Realm = 'user' | 'admin';

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

// ---- realm-scoped storage keys (feature-17 AD5) ----

export function tokenKey(realm: Realm): string {
  return `go-taas.${realm}.session-token`;
}

export function orgKey(realm: Realm): string {
  return `go-taas.${realm}.org-id`;
}

// Legacy keys from before the surface split (feature-17 AD9). They are
// adopted into the booting realm's keys and then deleted.
const LEGACY_TOKEN_KEY = 'go-taas.session-token';
const LEGACY_ORG_KEY = 'go-taas.org-id';

export function getSessionToken(realm: Realm): string {
  return localStorage.getItem(tokenKey(realm)) || '';
}

export function setSessionToken(realm: Realm, token: string): void {
  if (token) {
    localStorage.setItem(tokenKey(realm), token);
  } else {
    localStorage.removeItem(tokenKey(realm));
  }
}

export function getOrgId(realm: Realm): string {
  return localStorage.getItem(orgKey(realm)) || '';
}

export function setOrgId(realm: Realm, id: string): void {
  if (id) {
    localStorage.setItem(orgKey(realm), id);
  } else {
    localStorage.removeItem(orgKey(realm));
  }
}

// adoptLegacyStorage migrates the pre-split keys into the given realm's
// keys (only when the realm key is absent) and then deletes the legacy
// keys. Idempotent; called once per surface boot (feature-17 AD9).
export function adoptLegacyStorage(realm: Realm): void {
  const map: [string, string][] = [
    [LEGACY_TOKEN_KEY, tokenKey(realm)],
    [LEGACY_ORG_KEY, orgKey(realm)],
  ];
  for (const [legacy, target] of map) {
    const value = localStorage.getItem(legacy);
    if (value && !localStorage.getItem(target)) {
      localStorage.setItem(target, value);
    }
    localStorage.removeItem(legacy);
  }
}

// apiPrefix returns the API prefix of a realm.
export function apiPrefix(realm: Realm): string {
  return realm === 'admin' ? '/api/v1/admin' : '/api/v1';
}

// pathBelongsToRealm reports whether a path's prefix matches the realm.
function pathBelongsToRealm(realm: Realm, path: string): boolean {
  if (realm === 'admin') {
    return path === '/api/v1/admin' || path.startsWith('/api/v1/admin/');
  }
  // User realm: any /api/v1/* path that is not the admin prefix.
  return path.startsWith('/api/v1/') && !path.startsWith('/api/v1/admin');
}

function createRequest<T>(
  realm: Realm,
  method: string,
  path: string,
  orgId: string,
  body?: unknown,
): Promise<T> {
  if (!pathBelongsToRealm(realm, path)) {
    throw new ApiError(10038, `path ${path} does not belong to the ${realm} surface`);
  }
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  const sessionToken = getSessionToken(realm);
  if (sessionToken) {
    // Session present: the session's active org is authoritative (D6).
    headers['Authorization'] = `Bearer ${sessionToken}`;
  } else {
    // No session: transitional header for CLI/transitional access.
    headers['X-Organization-Id'] = orgId;
  }
  return fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  }).then(async (res) => {
    const data = (await res.json().catch(() => ({}))) as Record<string, unknown>;
    // Business errors come back as {code, message} with HTTP 500; transport
    // errors use other statuses. Branch on the body code, not the HTTP status.
    const envelope = (data.response ?? data) as ResponseEnvelope | undefined;
    if (envelope && typeof envelope.code === 'number' && envelope.code !== 0) {
      throw new ApiError(envelope.code, envelope.message || 'request failed');
    }
    return data as T;
  });
}

export function createApi(realm: Realm) {
  return {
    get: <T>(path: string, orgId: string) => createRequest<T>(realm, 'GET', path, orgId),
    post: <T>(path: string, orgId: string, body?: unknown) =>
      createRequest<T>(realm, 'POST', path, orgId, body),
    put: <T>(path: string, orgId: string, body?: unknown) =>
      createRequest<T>(realm, 'PUT', path, orgId, body),
    del: <T>(path: string, orgId: string) => createRequest<T>(realm, 'DELETE', path, orgId),
    patch: <T>(path: string, orgId: string, body?: unknown) =>
      createRequest<T>(realm, 'PATCH', path, orgId, body),
  };
}

// Backward-compatible default client for the admin surface (the existing
// admin pages call /api/v1/admin/*). New code should use createApi(realm).
export const api = createApi('admin');

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
  rateLimitRpm?: string;
  rateLimitTpm?: string;
}

export interface ModelSummary {
  modelId: string;
  name: string;
  latestVersion: string;
  weightPath: string;
  createdAt: string;
  // restricted is true iff the model has at least one authorization grant
  // (feature #13): it is then usable only by the granted organizations.
  restricted?: boolean;
}

// AvailableModel is the masked user-realm catalog projection (feature-17
// AD8): no weight_path, version list or grant rows.
export interface AvailableModel {
  modelId: string;
  name: string;
  latestVersion: string;
  // Feature #16 read-only autoscaling projection.
  autoscaling?: ModelAutoscaling;
  // Feature #19 masked compatibility summary (e.g. "vLLM · A800, H800").
  compatibility?: string;
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
  // Feature #16 autoscaling summary fields.
  autoscalingEnabled?: boolean;
  currentReplicas?: number;
  minReplicas?: number;
  maxReplicas?: number;
  autoscalingState?: string;
}

// ---- inference autoscaling (feature #16) ----

export interface AutoscalingPolicy {
  enabled: boolean;
  minReplicas: number;
  maxReplicas: number;
  targetConcurrency: number;
  scaleToZero: boolean;
  cooldownSeconds: number;
}

export interface AutoscalingStatus {
  state: string;
  currentReplicas: number;
  desiredReplicas: number;
  currentConcurrency: number;
  targetConcurrency: number;
  lastScalingEventAt: string;
  errorReason?: string;
}

export interface GetAutoscalingPolicyResponse {
  response: { code: number; message: string };
  policy?: AutoscalingPolicy;
}

export interface GetInferenceServiceResponse {
  response: { code: number; message: string };
  service: InferenceServiceSummary & { accelerator?: string; acceleratorType?: string };
  endpoints: string[];
  autoscaling?: AutoscalingPolicy;
  autoscalingStatus?: AutoscalingStatus;
}

// ModelAutoscaling is the masked user-realm autoscaling projection
// (feature #16, AD13): no operator fields.
export interface ModelAutoscaling {
  autoscaled: boolean;
  currentReplicas: number;
  minReplicas: number;
  maxReplicas: number;
  scaleToZero: boolean;
  state: string; // steady | scaling | scaled-to-zero | warming-up | fixed
}

export interface GetAvailableModelResponse {
  response: { code: number; message: string };
  model?: AvailableModel;
  autoscaling?: ModelAutoscaling;
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
  estimatedCostCents?: string;
  priced?: boolean;
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

// ---- usage dashboard (feature #9) ----

export interface DashboardCard {
  totalCostCents: string;
  currency: string;
  promptTokens: string;
  completionTokens: string;
  cachedTokens: string;
  reasoningTokens: string;
  requestCount: string;
  unpricedRequestCount: string;
  dataThrough: string;
}

export interface DashboardGroup {
  groupKey: string;
  costCents: string;
  promptTokens: string;
  completionTokens: string;
  cachedTokens: string;
  reasoningTokens: string;
  requestCount: string;
  priced: boolean;
}

export interface DailyBucket {
  date: string;
  groups: DashboardGroup[];
}

export interface UsageDashboardResponse {
  response: { code: number; message: string };
  cards?: DashboardCard;
  dailyBuckets?: DailyBucket[];
}

// ---- model observability (feature #24) ----

export interface ObservabilityCard {
  requestCount: string;
  errorCount: string;
  avgLatencyMs: string;
  p95LatencyMs: string;
  outputTokensPerSec: string;
  inputTokensPerSec: string;
  dataThrough: string;
}

export interface ModelObservabilityRow {
  modelId: string;
  modelName: string;
  requestCount: string;
  errorCount: string;
  avgLatencyMs: string;
  p95LatencyMs: string;
  outputTokensPerSec: string;
  dataThrough: string;
}

export interface ObservabilitySeriesPoint {
  bucket: string;
  requestCount: string;
  errorCount: string;
  avgLatencyMs: string;
  p95LatencyMs: string;
  outputTokensPerSec: string;
  inputTokensPerSec: string;
}

export interface ObservabilityKeyRow {
  apiKeyId: string;
  apiKeyName: string;
  requestCount: string;
  errorCount: string;
  avgLatencyMs: string;
  p95LatencyMs: string;
  outputTokensPerSec: string;
}

export interface GetObservabilityOverviewResponse {
  response: ResponseEnvelope;
  cards?: ObservabilityCard;
  models?: ModelObservabilityRow[];
  series?: ObservabilitySeriesPoint[];
}

export interface GetModelObservabilityResponse {
  response: ResponseEnvelope;
  cards?: ObservabilityCard;
  series?: ObservabilitySeriesPoint[];
  keys?: ObservabilityKeyRow[];
}

// ---- API key usage analytics (feature #28) ----
// Admin surface: /api/v1/admin/usage/keys/*. User surface (masked):
// /api/v1/usage/keys/*. int64 fields arrive as strings.

export interface UsageKeysCard {
  requestCount: string;
  errorCount: string;
  totalTokens: string;
  totalCostCents: string;
  avgLatencyMs: string;
  p95LatencyMs: string;
  dataThrough: string;
}

export interface UsageKeyRow {
  apiKeyId: string;
  apiKeyName: string;
  organizationId: string;
  requestCount: string;
  errorCount: string;
  totalTokens: string;
  totalCostCents: string;
  avgLatencyMs: string;
  p95LatencyMs: string;
  dataThrough: string;
}

export interface UsageKeyTopRow {
  apiKeyId: string;
  apiKeyName: string;
  totalCostCents: string;
  requestCount: string;
  totalTokens: string;
  sharePct: string;
}

export interface UsageKeysSeriesPoint {
  bucket: string;
  requestCount: string;
  errorCount: string;
  totalTokens: string;
  totalCostCents: string;
  avgLatencyMs: string;
  p95LatencyMs: string;
}

export interface GetUsageKeysOverviewResponse {
  response: ResponseEnvelope;
  cards?: UsageKeysCard;
  keys?: UsageKeyRow[];
  topKeys?: UsageKeyTopRow[];
  series?: UsageKeysSeriesPoint[];
}

export interface GetUsageKeysResponse {
  response: ResponseEnvelope;
  cards?: UsageKeysCard;
  series?: UsageKeysSeriesPoint[];
}

// ---- error analysis (feature #31) ----
// Admin surface: /api/v1/admin/errors/*. User surface (masked):
// /api/v1/errors/*. int64 fields arrive as strings.

export interface ErrorAnalysisCard {
  errorCount: string;
  requestCount: string;
  topCause: string;
  topCauseSharePct: string;
  dataThrough: string;
}

export interface ErrorCauseRow {
  errorCode: string;
  errorMessage: string;
  errorCount: string;
  errorRate: string;
  sharePct: string;
}

export interface ErrorSeriesPoint {
  bucket: string;
  errorCount: string;
  requestCount: string;
  errorRate: string;
}

export interface GetErrorAnalysisOverviewResponse {
  response: ResponseEnvelope;
  cards?: ErrorAnalysisCard;
  causes?: ErrorCauseRow[];
  series?: ErrorSeriesPoint[];
}

export interface GetErrorAnalysisResponse {
  response: ResponseEnvelope;
  cards?: ErrorAnalysisCard;
  series?: ErrorSeriesPoint[];
}

// ---- cost analytics (feature #29) ----
// Admin surface: /api/v1/admin/cost/*. User surface (masked):
// /api/v1/cost/*. int64 fields arrive as strings.

export interface CostAnalyticsCard {
  totalCostCents: string;
  totalTokens: string;
  topDimensionValue: string;
  topDimensionSharePct: string;
  dataThrough: string;
}

export interface CostBreakdownRow {
  dimensionValue: string;
  dimensionName: string;
  totalCostCents: string;
  totalTokens: string;
  costPerToken: string;
  sharePct: string;
}

export interface CostSeriesPoint {
  bucket: string;
  totalCostCents: string;
  totalTokens: string;
  costPerToken: string;
}

export interface GetCostAnalyticsOverviewResponse {
  response: ResponseEnvelope;
  cards?: CostAnalyticsCard;
  breakdown?: CostBreakdownRow[];
  series?: CostSeriesPoint[];
}

export interface GetCostAnalyticsResponse {
  response: ResponseEnvelope;
  cards?: CostAnalyticsCard;
  series?: CostSeriesPoint[];
}

// ---- system health & status (feature #30) ----
// Admin surface only: /api/v1/admin/status. int64 fields arrive as
// strings.

export interface SystemDependency {
  dependencyId: string;
  dependencyName: string;
  status: string;
}

export interface SystemComponent {
  componentId: string;
  componentName: string;
  componentType: string;
  status: string;
  uptimeSeconds: string;
  lastCheckedAt: string;
  dependencies?: SystemDependency[];
}

export interface SystemStatusPage {
  overallStatus: string;
  lastCheckedAt: string;
  componentCount: string;
}

export interface GetSystemStatusResponse {
  response: ResponseEnvelope;
  overallStatus?: string;
  components?: SystemComponent[];
  statusPage?: SystemStatusPage;
  lastCheckedAt?: string;
}

// ---- request tracing (feature #27) ----

export interface TraceSummary {
  traceId: string;
  organizationId: string;
  apiKeyId: string;
  apiKeyName: string;
  modelId: string;
  modelName: string;
  serviceId: string;
  status: string;
  error: string;
  totalLatencyMs: string;
  ttftMs: string;
  generationMs: string;
  createdAt: string;
}

export interface TraceSpan {
  spanId: string;
  traceId: string;
  parentSpanId: string;
  name: string;
  kind: string;
  startOffsetMs: string;
  durationMs: string;
  status: string;
  error: string;
  attributes: string;
}

export interface TraceDetail {
  traceId: string;
  organizationId: string;
  apiKeyId: string;
  apiKeyName: string;
  modelId: string;
  modelName: string;
  serviceId: string;
  status: string;
  error: string;
  totalLatencyMs: string;
  ttftMs: string;
  generationMs: string;
  promptTokens: string;
  completionTokens: string;
  cachedTokens: string;
  reasoningTokens: string;
  createdAt: string;
  spans?: TraceSpan[];
}

export interface ListTracesResponse {
  response: ResponseEnvelope;
  traces?: TraceSummary[];
  nextPageToken: string;
}

export interface GetTraceResponse {
  response: ResponseEnvelope;
  trace?: TraceDetail;
}

// ---- per-tenant model authorization (feature #13) ----

export interface ModelAuthorization {
  organizationId: string;
  grantedBy: string;
  createdAt: string;
}

export interface ListModelAuthorizationsResponse {
  response: ResponseEnvelope;
  authorizations?: ModelAuthorization[];
  pageMeta?: PageMeta;
}

// ---- request logs (feature #12) ----

export interface RequestLog {
  requestLogId: string;
  requestId: string;
  organizationId: string;
  apiKeyId: string;
  modelId: string;
  serviceId: string;
  promptTokens: string;
  completionTokens: string;
  cachedTokens: string;
  reasoningTokens: string;
  latencyMs: string;
  status: string; // success | error | streaming
  error: string;
  createdAt: string;
}

export interface ListRequestLogsResponse {
  response: { code: number; message: string };
  requestLogs: RequestLog[];
  pageMeta?: PageMeta;
}

// ---- playground (feature #12) ----

export interface PlaygroundInferResponse {
  response: { code: number; message: string };
  completion: string;
  promptTokens: string;
  completionTokens: string;
  cachedTokens: string;
  reasoningTokens: string;
  latencyMs: string;
}

// ---- inference endpoint (feature #21, SDK / Quickstart) ----

export interface InferenceEndpointResponse {
  response: { code: number; message: string };
  baseUrl: string;
}

// ---- billing balance (feature #8, reused by the widget) ----

export interface BalanceResponse {
  response: { code: number; message: string };
  mode: string; // prepaid | postpaid
  balance: number;
  currency: string;
  balanceCents: string;
  monthlyQuotaCents: string;
  usedThisCycleCents: string;
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

// ---- org members & invitations (feature #10) ----

export interface OrgMember {
  userId: string;
  displayName: string;
  role: string; // owner | admin | member | viewer
  joinedAt: string;
}

export interface Invitation {
  invitationId: string;
  email: string;
  role: string;
  status: string; // pending | accepted | rejected | revoked | expired
  expiresAt: string;
  createdBy: string;
  createdAt: string;
}

export interface ListOrgMembersResponse {
  response: { code: number; message: string };
  members: OrgMember[];
  pageMeta?: PageMeta;
}

export interface ListInvitationsResponse {
  response: { code: number; message: string };
  invitations: Invitation[];
  pageMeta?: PageMeta;
}

export interface CreateInvitationResponse {
  response: { code: number; message: string };
  invitation: Invitation;
  token: string;
}

export interface ResendInvitationResponse {
  response: { code: number; message: string };
  invitation: Invitation;
  token: string;
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
  monthlySpendLimitCents?: string;
  spentThisCycleCents?: string;
  spendLimitUsagePercent?: number;
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

// ---- inference load testing (feature #20) ----
// Admin surface: /api/v1/admin/load-tests/*. User surface (masked):
// /api/v1/models/{model_id}/load-tests. int64 fields arrive as strings.

export interface LoadTestSummary {
  loadTestId: string;
  serviceId: string;
  serviceName: string;
  modelId: string;
  modelName: string;
  concurrency: number;
  durationSeconds: number;
  requestRate: number;
  state: string; // pending | running | completed | failed | stopped
  startedAt: string;
  completedAt: string;
  throughputRps: number;
  latencyP95Ms: number;
  outputTokensPerSec: number;
  errorRate: number;
}

export interface LoadTestProgress {
  requestsSent: string;
  successCount: string;
  failureCount: string;
  elapsedSeconds: string;
}

export interface LoadTestResult {
  totalRequests: string;
  successCount: string;
  failureCount: string;
  errorRate: number;
  throughputRps: number;
  outputTokensPerSec: number;
  inputTokens: string;
  outputTokens: string;
  latencyP50Ms: number;
  latencyP90Ms: number;
  latencyP95Ms: number;
  latencyP99Ms: number;
}

export interface ListLoadTestsResponse {
  response: ResponseEnvelope;
  runs: LoadTestSummary[];
  pageMeta?: PageMeta;
}

export interface GetLoadTestResponse {
  response: ResponseEnvelope;
  summary: LoadTestSummary;
  promptTemplate: string;
  maxTokens: number;
  progress?: LoadTestProgress;
  result?: LoadTestResult;
  failureReason: string;
}

export interface CreateLoadTestResponse {
  response: ResponseEnvelope;
  loadTestId: string;
  state: string;
}

export interface StopLoadTestResponse {
  response: ResponseEnvelope;
  state: string;
}

// ModelLoadTestResult is the masked end-user projection: no service ids
// and no operator internals (feature #20, AD2).
export interface ModelLoadTestResult {
  throughputRps: number;
  latencyP95Ms: number;
  outputTokensPerSec: number;
  errorRate: number;
  concurrency: number;
  durationSeconds: number;
  completedAt: string;
}

export interface GetModelLoadTestsResponse {
  response: ResponseEnvelope;
  results: ModelLoadTestResult[];
}

// ---- billing reports & CSV export (feature-25) ----
// Admin surface: /api/v1/admin/billing/reports/*. User surface (masked,
// tenant-scoped): /api/v1/billing/reports/*. int64 fields arrive as
// strings.

export type ReportDimension = 'organization' | 'api_key' | 'model';
export type ReportGranularity = 'daily' | 'hourly';
export type ReportFrequency = 'daily' | 'weekly' | 'monthly';
export type RelativeRange = 'last_7_days' | 'last_30_days' | 'last_month';

export interface Report {
  reportId: string;
  name: string;
  dimension: string;
  since: string;
  until: string;
  granularity: string;
  timezone: string;
  status: string; // pending | ready | failed
  rowCount: string;
  dataThrough: string;
  createdAt: string;
  scheduleId?: string;
}

export interface ReportSchedule {
  scheduleId: string;
  name: string;
  dimension: string;
  relativeRange: string;
  granularity: string;
  frequency: string;
  timezone: string;
  status: string; // active
  lastRunAt: string;
  createdAt: string;
}

export interface CreateReportResponse {
  response: ResponseEnvelope;
  report: Report;
}

export interface GetReportResponse {
  response: ResponseEnvelope;
  report: Report;
}

export interface ListReportsResponse {
  response: ResponseEnvelope;
  reports: Report[];
  pageMeta?: PageMeta;
}

export interface DownloadReportResponse {
  response: ResponseEnvelope;
  csv: string;
  filename: string;
}

export interface CreateScheduleResponse {
  response: ResponseEnvelope;
  schedule: ReportSchedule;
}

export interface ListSchedulesResponse {
  response: ResponseEnvelope;
  schedules: ReportSchedule[];
  pageMeta?: PageMeta;
}

export interface UpdateScheduleResponse {
  response: ResponseEnvelope;
  schedule: ReportSchedule;
}

export interface ListScheduleRunsResponse {
  response: ResponseEnvelope;
  runs: Report[];
  pageMeta?: PageMeta;
}

// formatPercent renders a 0..1 ratio as a percentage string.
export function formatPercent(ratio: number | undefined): string {
  if (ratio === undefined || ratio === null || !isFinite(ratio)) return '0.0%';
  return `${(ratio * 100).toFixed(1)}%`;
}

// formatRate renders a float metric with fixed decimals and a fallback.
export function formatRate(value: number | undefined, decimals = 1): string {
  if (value === undefined || value === null || !isFinite(value)) return '—';
  return value.toFixed(decimals);
}

// ---- service resource metrics (feature #37) ----

export interface ResourceMetricsCard {
  currentCpuPercent: string;
  currentMemoryBytes: string;
  currentGpuPercent: string;
  replicaCount: string;
  dataThrough: string;
}

export interface ResourceMetricsSeriesPoint {
  bucket: string;
  cpuPercent: string;
  memoryBytes: string;
  gpuPercent: string;
}

export interface ResourceMetricsReplicaRow {
  replicaIndex: string;
  currentCpuPercent: string;
  currentMemoryBytes: string;
  currentGpuPercent: string;
  dataThrough: string;
}

export interface GetServiceResourceMetricsResponse {
  response: ResponseEnvelope;
  cards: ResourceMetricsCard;
  series: ResourceMetricsSeriesPoint[];
  replicas: ResourceMetricsReplicaRow[];
}

// ---- API documentation explorer (feature #38) ----

export interface ApiDocsParameter {
  name: string;
  in: string;
  required: boolean;
  type: string;
  description: string;
}

export interface ApiDocsErrorCode {
  code: string;
  constant: string;
  message: string;
}

export interface ApiDocsEndpoint {
  endpointId: string;
  method: string;
  path: string;
  summary: string;
  description: string;
  parameters: ApiDocsParameter[];
  requestExample: string;
  responseExample: string;
  errorCodes: ApiDocsErrorCode[];
  tryable: boolean;
}

export interface ApiDocsCategory {
  categoryId: string;
  categoryName: string;
  endpoints: ApiDocsEndpoint[];
}

export interface GetApiDocsResponse {
  response: ResponseEnvelope;
  categories: ApiDocsCategory[];
}

// ---- model fine-tuning (feature #39) ----

export interface FineTuningDataset {
  datasetId: string;
  name: string;
  format: string;
  objectPath: string;
  createdAt: string;
}

export interface FineTuningHyperparameters {
  epochs: string;
  batchSize: string;
  learningRate: string;
}

export interface FineTuningJobSummary {
  jobId: string;
  name: string;
  baseModelId: string;
  baseModelVersion: string;
  datasetId: string;
  state: string;
  fineTunedModelId: string;
  createdAt: string;
  updatedAt: string;
}

export interface FineTuningJob {
  summary: FineTuningJobSummary;
  hyperparameters: FineTuningHyperparameters;
  failureReason: string;
}

export interface ListFineTuningDatasetsResponse {
  response: ResponseEnvelope;
  datasets: FineTuningDataset[];
}

export interface RegisterFineTuningDatasetResponse {
  response: ResponseEnvelope;
  datasetId: string;
}

export interface ListFineTuningJobsResponse {
  response: ResponseEnvelope;
  jobs: FineTuningJobSummary[];
}

export interface GetFineTuningJobResponse {
  response: ResponseEnvelope;
  job: FineTuningJob;
}

export interface CreateFineTuningJobResponse {
  response: ResponseEnvelope;
  jobId: string;
  state: string;
}

export interface DeployFineTunedModelResponse {
  response: ResponseEnvelope;
  serviceId: string;
}

// ---- multi-cluster management (feature #40) ----

export interface ClusterSummary {
  clusterId: string;
  name: string;
  region: string;
  state: string;
  health: string;
  nodeCount: string;
  serviceCount: string;
  lastCheckedAt: string;
}

export interface ClusterNode {
  nodeId: string;
  name: string;
  status: string;
}

export interface Cluster {
  summary: ClusterSummary;
  nodes: ClusterNode[];
}

export interface ClusterWorkload {
  serviceId: string;
  name: string;
  modelId: string;
  state: string;
  createdAt: string;
}

export interface ListClustersResponse {
  response: ResponseEnvelope;
  clusters: ClusterSummary[];
}

export interface RegisterClusterResponse {
  response: ResponseEnvelope;
  clusterId: string;
  state: string;
}

export interface GetClusterResponse {
  response: ResponseEnvelope;
  cluster: Cluster;
}

export interface GetClusterWorkloadsResponse {
  response: ResponseEnvelope;
  workloads: ClusterWorkload[];
}
