# Service Quota Management — Requirement Analysis and UI/UX Design

| Attribute | Content |
| --- | --- |
| Feature point | Service quota management — per-organization **resource quotas** (max concurrent inference requests, max inference services, max batch jobs per day, per-request max output tokens) configured by the operator, enforced fail-closed at the gateway and the control plane, with a quota bar on the tenant surface and a quota management page on the admin surface (backlog row 51) |
| Document scope | Requirement analysis, competitive research and the quota-model decision (four quota dimensions, 0 = unlimited, fail-closed enforcement), the admin-surface page `/admin/quotas` (per-org quota configuration with presets and history), the end-user-surface quota bar and `/quotas` detail page, the page → API surface table, and numbered acceptance criteria |
| Owning modules | `tenancy` (org quota fields, `UpdateOrganizationQuotas`), `auth` (quota fields ride the `VerifyAPIKey` verdict — the gateway enforcement contract), `infer` (service-count and concurrency enforcement points, per-request token cap), `batch` (daily batch-job quota check), `metering` (unchanged — usage observation only), `audit` (quota changes), `pkg/server` gateway (admin + user prefix bindings), `web` admin + end-user console |
| Related documents | [Architecture Design](./architecture.md) — §2.4 `auth`/`infer`, §3.1 Control Gateway, §3.2 data plane · [Rate Limits & Spend Limits](./rate-limits-spend-limits.md) — the per-key RPM/TPM and org spend cap this feature composes with; its non-goals explicitly deferred per-service concurrency and QPS limits here · [Multi-Tenancy](./multi-tenancy.md) — the org/project model quotas were deferred from · [Balance & Quota Account Modes](./balance-quota.md) — the funds side (402) quotas never touch · [Batch Inference](./batch-inference.md) — the daily batch-job dimension · [Inference Autoscaling](./inference-autoscaling.md) — why concurrency is capped at the gateway, not by scaling · [API Key Management](./api-key-management.md) — the verdict-cache path quota fields ride · [Console Surface Separation](./console-surface-separation.md) — the two surfaces, the masked-projection rule |
| Status | Design complete, handed to the Architect agent |

---

## 1. Background and Competitive Research

### 1.1 Why Service Quota Management Comes Now

The platform's protective rails are one-dimensional today. Feature #6 gave per-key **rate limits** (RPM/TPM — how *fast* a key may call) and an org **spend limit** (how *much money* an org may burn per month), and its non-goals explicitly deferred "per-key QPS or concurrency limits, per-model or per-endpoint rate limits, rate-limit tiers or plans" to a later feature. Feature #5's multi-tenancy deferred "org/project quotas" the same way.

What is still missing is the **resource dimension**: how many requests an organization may hold in flight at once (concurrency), how many inference services it may deploy, how many batch jobs it may submit per day, and how many tokens a single request may generate. Without these, one tenant can starve the fleet — a burst of long-running streaming requests holds GPUs that autoscaling (#16) will happily scale *up* for (the tenant pays, but the fleet's GPUs are gone), a tenant can deploy 50 idle services, and a runaway Agent loop can generate 8192-token completions forever. Rate limits do not catch any of these: 60 RPM of 10-minute streaming requests is 60 concurrent slots held indefinitely.

Every comparable platform separates these axes explicitly. This feature adds **per-organization resource quotas** — four bounded dimensions, operator-configured, enforced fail-closed at the gateway (concurrency, per-request tokens) and the control plane (service count, batch jobs/day), with a tenant-visible quota bar so the constraint is transparent, not mysterious.

### 1.2 Access Pattern: Platform-Managed Org Quotas vs the Alternatives

| Pattern | What it is | Why we adopt / reject it |
| --- | --- | --- |
| **Platform-managed per-org resource quotas, enforced at the gateway + control plane** (adopted) | The operator configures four quota dimensions per organization (concurrency, services, batch jobs/day, max output tokens/request); the `VerifyAPIKey` verdict carries them to the gateway, which enforces the hot-path dimensions locally; the control plane enforces the lifecycle dimensions at create time. | The only pattern that protects the **fleet** (not just the bill): concurrency and token caps are resource guards only the data plane can enforce in time, and service/batch counts are lifecycle guards only the control plane sees. Reuses the #6 verdict-riding contract unchanged — no new hot-path RPC. |
| Quota tiers / plans (Free: 2 services, Pro: 20, …) | Pre-bundled quota packages assigned to orgs. | Rejected for v1: tiers are a **packaging** decision on top of the same four fields; shipping raw per-org fields first keeps the enforcement honest and testable, and a tier preset is a UI convenience (D5) rather than a data model. |
| Tenant self-service quota management | Tenants raise their own quotas (possibly paid). | Rejected: quotas protect the fleet *from* tenants; the party being limited must not hold the lever. Quota changes are operator-only (D7). |
| Per-key quotas | The quota dimensions hang off the API key. | Rejected: the resource axes (concurrency, services, batch jobs) are **org-level** facts — a tenant with two keys would double every limit. Keys keep #6's RPM/TPM; orgs get this feature's resource quotas (D2). |
| Pure overage billing (no hard caps) | Let usage run and bill it. | Rejected for the resource axes: money cannot buy back GPUs another tenant is waiting for. Spend stays the money axis (#6/#8); resources get hard, fail-closed caps (§1.4). |
| Extending #6's rate-limit fields | Add concurrency to the key's RPM/TPM pair. | Rejected: different subject (org vs key), different enforcement point (in-flight tracking vs token bucket), different error semantics (queue-reject vs throttle). A separate org-quota object keeps both inspectable (D1). |

### 1.3 Comparable-Product Interaction Patterns

| Product | Quota pattern | go-taas decision |
| --- | --- | --- |
| **OpenAI Platform** | Per-org **rate limits** tiered by model and spend tier; `insufficient_quota` distinct from `rate_limit_exceeded`; per-request `max_tokens` capped by model | Adopt the **distinct error** discipline (quota ≠ rate ≠ funds — three codes, never conflated) and the per-request output-token cap as a first-class quota dimension; **reject** tier-by-spend packaging (D5 keeps tiers as presets, not a data model) |
| **Anthropic Console** | Usage limits with hard-stop or notify modes; per-model rate limits shown in-console before you hit them | Adopt the **visible-before-you-hit-it** pattern: the tenant quota bar shows current usage against every dimension, so the limit is transparent, not a surprise 429 |
| **AWS Bedrock** | Per-account model invocation quotas (TPS) managed in a Service Quotas console with a **request-increase** flow; default quotas per region | Adopt the **Service Quotas console shape** for the admin page (per-org rows, edit dialog, change history) and the request-increase **link** (a mailto/notification to the operator — no self-service approval flow in v1); **reject** the approval workflow (one-operator platform, the operator just edits) |
| **Azure AI Foundry** | Per-project quotas (TPM) allocated from a regional pool; quota allocation charts | Adopt per-org as the unit (project-level is future, matching #5's deferral); **reject** regional pools (single-region platform today) |
| **SiliconFlow** | Per-key concurrency limits shown on the key detail page; hard 429-style rejection with a clear Chinese error message | Adopt **concurrency as a headline dimension** with an explicit, documented rejection error — the dimension Chinese-language LLM platforms standardized first |
| **Modal / Baseten** | Per-workspace concurrent-replica or GPU caps; deployment count limits per plan | Adopt the **service-count dimension** (go-taas's equivalent of deployment caps) and the fail-closed create rejection with the current count in the error copy |

### 1.4 The Fail-Closed Resource Guard Invariant

The core product rule:

1. **Quotas protect the fleet, not the bill.** The four dimensions are resource axes; money (spend limits, funds) stays on #6/#8 and composes independently — a request can pass quotas and fail funds, or pass funds and fail quotas.
2. **Enforcement is fail-closed and immediate.** Concurrency and per-request tokens are enforced at the gateway **before** the request reaches a service; service count and batch jobs/day are enforced at create time. A quota rejection never queues, never waits, never partially executes.
3. **Three errors, never conflated.** Quota rejection (133xx block) is distinct from rate-limit 429 (#6) and funds 402 (#8) — an SDK author can tell exactly which lever was hit. The check order at the gateway is: rate limit → quota → funds (a throttled key never reaches the quota check; a quota-blocked request never reaches the funds check).
4. **0 = unlimited, default permissive.** An unset or zero quota means no limit on that dimension (the #6 convention); new orgs start unlimited and the operator tightens deliberately.
5. **Quotas are visible to the constrained party.** The tenant sees its own quota bar (current usage / limit per dimension) — a limit the tenant cannot see is indistinguishable from an outage.

### 1.5 Decisions and Scope

| # | Decision | Rationale |
| --- | --- | --- |
| D1 | **A new admin section** `/admin/quotas` + `/api/v1/admin/quotas/*` for management, and a new end-user page `/quotas` + `/api/v1/quotas/*` for the tenant's own view — **not** extensions of #6's rate-limit editor or #5's org editor | Quotas are a distinct object class (org-level resource axes with their own enforcement points and history); a dedicated page keeps #6's key-level throttles and this feature's org-level resources separately inspectable (§1.2) |
| D2 | **Four quota dimensions on the organization**: `max_concurrent_requests` (0 = unlimited, default 0; when set, 1–10000), `max_inference_services` (0 = unlimited, default 0; when set, 1–1000), `max_batch_jobs_per_day` (0 = unlimited, default 0; when set, 1–10000), `max_output_tokens_per_request` (0 = unlimited, default 0; when set, 64–32768). All four are **per-org**, not per-key (§1.2) | The four resource axes the platform can actually starve on: in-flight requests, deployed services, daily batch submissions, per-request generation. Bounded ranges keep values sane; 0 = unlimited keeps the default permissive (§1.4) |
| D3 | **Hot-path dimensions ride the `VerifyAPIKey` verdict** exactly as #6's RPM/TPM do: the verdict gains `max_concurrent_requests` and `max_output_tokens_per_request`; the gateway tracks in-flight requests per org (increment on accept, decrement on completion) and enforces the token cap by clamping/validating the request's `max_tokens` before dispatch. Lifecycle dimensions (`max_inference_services`, `max_batch_jobs_per_day`) are enforced by `infer` and `batch` at create time | Zero new hot-path RPCs (the #6 contract pattern); in-flight tracking is gateway-local state, the only place that sees accept/complete; create-time checks are naturally control-plane. The verdict cache TTL (30 s) bounds quota-change propagation — documented in the drawer |
| D4 | **Quota rejection is fail-closed with a dedicated error block 133xx**: **13301 `CodeQuotaConcurrencyExceeded`** (HTTP 429 + `Retry-After`), **13302 `CodeQuotaServiceCountExceeded`**, **13303 `CodeQuotaBatchJobsExceeded`**, **13304 `CodeQuotaOutputTokensExceeded`** (HTTP 400), **13305 `CodeQuotaInvalid`** (validation), **13306 `CodeQuotaNotFound`**. Concurrency rejection carries `Retry-After: 1` (the in-flight window is short); the others are immediate 4xx | Three-errors-never-conflated (§1.4): quota ≠ rate (10037) ≠ funds (10502). The 429-vs-400 split mirrors the semantics: concurrency is transient (retry later), the others are configuration (fix the request or the quota) |
| D5 | **The admin page offers tier presets as a convenience only**: Default (all unlimited), Starter (10 concurrent / 3 services / 5 batch/day / 4096 tokens), Growth (50 / 20 / 50 / 8192), Enterprise (200 / 100 / 500 / 16384). Selecting a preset fills the form; the operator can then edit any field before saving. Presets are UI constants, not a data model (§1.2) | Operators configure dozens of orgs; presets speed the common cases without freezing the model. Raw fields remain the source of truth |
| D6 | **Quota changes take effect within the verdict-cache TTL** (≤ 30 s for hot-path dimensions) and immediately for lifecycle dimensions; the drawer documents the propagation window. Tightening a quota below current usage does **not** kill in-flight requests or delete services — it blocks new ones (usage drains naturally) | Predictable, non-destructive tightening: the guard blocks at the boundary, never revokes what is already running — the same semantics #6's spend limit uses for in-flight requests |
| D7 | **Quota management is operator-only**; the tenant surface is read-only (the quota bar + `/quotas`). A tenant "request increase" action is a **mailto link** to the operator (configurable address), not a workflow (§1.3 Bedrock) | The constrained party must not hold the lever (§1.2); a mailto covers the one-operator platform without an approval-flow stack |
| D8 | **The tenant quota bar shows all four dimensions** (current usage / limit, or "Unlimited") on the end-user console (a compact bar in the org context area + the `/quotas` detail page); usage counters are: in-flight requests (gateway), service count (infer), batch jobs today (batch), and the per-request token cap (a constant display). No other tenant sees another org's quotas | Visible-before-you-hit-it (§1.3 Anthropic); the bar makes the constraint transparent and the 13301 error diagnosable |
| D9 | **All quota mutations are audited** (`quota.updated` with old → new values, acting operator) and the admin page shows a per-org change history (last 20 changes) | Quota changes are fleet-protection decisions with fairness implications; the history matches the audit convention (#22) and answers "who tightened this and when" |
| D10 | **Realm and surface binding**: management is admin-only (`/admin/quotas` + `/api/v1/admin/quotas/*`); the tenant view is end-user (`/quotas` + `/api/v1/quotas/*`). A user-realm session on an admin route gets **10038**; a session without the role gets **10036** | Consistent with the console-surface separation (#17); quota internals (fleet-wide comparisons) are operator knowledge, the org's own quotas are the tenant's |

**In scope**: the four org quota fields and their enforcement (gateway in-flight tracking + token cap; infer service-count check; batch daily check); the admin page `/admin/quotas` (org table with quota columns, edit dialog with presets, change history drawer); the tenant quota bar and `/quotas` page; the verdict-cache extension; audit events; the request-increase mailto.

**Out of scope** (tracked elsewhere or deliberately excluded): per-key quotas (deliberately excluded — org-level only, §1.2/D2), quota tiers/plans as a data model (D5 — presets are UI constants), tenant self-service quota changes and approval workflows (deliberately excluded, D7), project-level quotas (deferred with #5's project model), per-model or per-endpoint quotas (future — the org axis first), quota overage billing (deliberately excluded — hard caps, §1.4), rate limits RPM/TPM and spend limits (feature #6, reused unchanged), funds/balance enforcement (feature #8, reused unchanged), and autoscaling interaction (autoscaling still scales within the org's service quota; the quota caps the *ceiling*, #16 caps the *behavior*).

---

## 2. User Roles

| Role | Description | Interaction with quotas |
| --- | --- | --- |
| **Platform administrator** | The operator who runs the go-taas platform | Opens `/admin/quotas` → reviews per-org quota rows → edits an org's quotas (directly or via preset) → sees the change history → answers request-increase mails |
| **Tenant administrator** | Administers an organization | Sees the quota bar (usage / limit per dimension) → opens `/quotas` for detail → clicks "Request increase" (mailto) when a limit binds |
| **Tenant developer / Agent owner** | Writes the applications that call `/v1` | Reads `/quotas` to size the Agent's concurrency and `max_tokens` within the org's caps; handles 13301/13304 errors in the client |
| **Agent / SDK** | The programmatic consumer | Receives 13301 with `Retry-After` and backs off; receives 13304 and lowers `max_tokens` |

> Terminology: the consumer-side caller is called an **Agent** (English) / 「智能体」(Chinese), consistent with the repo convention. The quota bar and `/quotas` page are the tenant's window into limits that are otherwise invisible until hit.

---

## 3. User Stories

| # | As a… | I want to… | So that… |
| --- | --- | --- | --- |
| US1 | Platform administrator | set a max concurrent requests quota per organization | one tenant's streaming burst cannot hold the fleet's GPUs hostage |
| US2 | Platform administrator | cap the number of inference services and daily batch jobs per org | idle-service sprawl and batch floods are stopped at create time |
| US3 | Platform administrator | cap per-request output tokens per org | a runaway Agent loop cannot generate max-length completions forever |
| US4 | Platform administrator | apply a preset then fine-tune fields before saving | configuring dozens of orgs is fast without freezing the values |
| US5 | Platform administrator | see every quota change with old → new values and the acting operator | fleet-protection decisions are reviewable |
| US6 | Tenant administrator | see my org's quota usage (in-flight / limit, services / limit, batch today / limit, token cap) before I hit a limit | a visible limit is a plannable constraint, not a mystery 429 |
| US7 | Tenant administrator | request a quota increase through a mailto when a limit binds | there is a documented path to more capacity without a self-service lever |
| US8 | Tenant developer | know the org's concurrency and token caps and get distinct errors (13301 with Retry-After, 13304) | my client backs off correctly instead of retrying into the same wall |
| US9 | Platform administrator | tighten a quota without killing in-flight requests or deleting services | enforcement blocks at the boundary and usage drains naturally — no destructive surprises |

---

## 4. Functional Requirements

### FR1 — Quota model and configuration

- **FR1.1** An organization's quota is `{organization_id, organization_name, max_concurrent_requests (int64, 0 = unlimited, else 1–10000), max_inference_services (int64, 0 = unlimited, else 1–1000), max_batch_jobs_per_day (int64, 0 = unlimited, else 1–10000), max_output_tokens_per_request (int64, 0 = unlimited, else 64–32768), updated_at, updated_by}`. Defaults on org create: all 0 (unlimited, §1.4).
- **FR1.2** `ListOrganizationQuotas` (`GET /api/v1/admin/quotas`) returns the per-org quota rows with current usage (`concurrent_requests` (live), `inference_services`, `batch_jobs_today`) and `page_size`/`page_token` pagination, a `name` search, and an `unlimited_only` / `limited_only` filter. Usage counters are best-effort snapshots (the in-flight count is gateway state relayed through the quota RPC).
- **FR1.3** `GetOrganizationQuota` (`GET /api/v1/admin/quotas/{organization_id}`) returns one org's quota with usage and the last 20 change-history entries. An unknown org returns **13306 `CodeQuotaNotFound`**.
- **FR1.4** `UpdateOrganizationQuota` (`PATCH /api/v1/admin/quotas/{organization_id}`) sets any subset of the four dimensions. A value outside its range (or negative) returns **13305 `CodeQuotaInvalid`** with the dimension named in the message. Tightening below current usage is allowed and non-destructive (D6). The change is audited (`quota.updated`, old → new, operator) and appears in the history.
- **FR1.5** The edit dialog offers the four presets (D5) as one-click fills; selecting a preset populates the fields and the operator may edit before saving. Presets are UI constants labeled "Default / Starter / Growth / Enterprise".

### FR2 — Enforcement: hot-path dimensions (gateway)

- **FR2.1** The `VerifyAPIKey` verdict gains `max_concurrent_requests` and `max_output_tokens_per_request` (org-level, cached with the verdict, TTL ≤ 30 s — D3). The gateway reads them exactly as it reads RPM/TPM today.
- **FR2.2** **Concurrency**: the gateway maintains a per-org in-flight counter (increment when a request is accepted for dispatch, decrement on completion — stream end, error, or client disconnect). When the org's counter is at its limit, a new request is rejected with **13301 `CodeQuotaConcurrencyExceeded`**, HTTP 429, `Retry-After: 1`. The check runs **after** the #6 rate-limit check and **before** the funds check (§1.4 order). Unlimited (0) skips the check.
- **FR2.3** **Per-request output tokens**: when the org's `max_output_tokens_per_request` is set, a request whose `max_tokens` exceeds it is rejected with **13304 `CodeQuotaOutputTokensExceeded`**, HTTP 400, naming the cap in the message ("capped at N tokens per request"); a request without an explicit `max_tokens` is dispatched with `max_tokens` clamped to the cap (the clamp is the default behavior — only an explicit over-cap value is rejected). The check runs with the concurrency check.
- **FR2.4** The gateway exposes its per-org in-flight counters to the quota RPC (FR1.2) for the usage snapshot; a gateway restart resets counters to the live truth (in-flight requests are re-derived from active streams where possible, else start at zero — the window is bounded by the verdict TTL and stream duration, documented as best-effort).

### FR3 — Enforcement: lifecycle dimensions (control plane)

- **FR3.1** **Service count**: `CreateInferenceService` (and the deployment path) checks the org's `max_inference_services` against the org's current service count; at or above the limit the create is rejected with **13302 `CodeQuotaServiceCountExceeded`** naming the current count and the limit. Unlimited (0) skips the check. Existing services are never deleted by a tightening (D6).
- **FR3.2** **Batch jobs per day**: `CreateBatchJob` checks the org's `max_batch_jobs_per_day` against the count of jobs created since local midnight; at or above the limit the create is rejected with **13303 `CodeQuotaBatchJobsExceeded`** naming today's count and the limit. Unlimited (0) skips the check. Canceled and failed jobs still count (the guard is on submissions, not successes).
- **FR3.3** Both lifecycle checks run inside the existing create transactions (no new RPC); the rejection copy is user-facing and actionable ("Your organization has reached its limit of N inference services. Request an increase or remove an unused service.").

### FR4 — Tenant surface: quota bar and detail page

- **FR4.1** `GetMyQuota` (`GET /api/v1/quotas`) returns the caller's org quota with live usage: `{max_concurrent_requests, current_concurrent_requests, max_inference_services, current_inference_services, max_batch_jobs_per_day, batch_jobs_today, max_output_tokens_per_request}` — 0 rendered as "Unlimited". No other org's data appears in any field.
- **FR4.2** The end-user console shows a **quota bar** (a compact strip in the org context area): per dimension, "usage / limit" with a progress bar (or "Unlimited"), turning amber at ≥ 80% and red at 100%. The bar links to `/quotas`.
- **FR4.3** The `/quotas` page shows the four dimensions as cards (current usage, limit, a bar, and plain-language guidance: "Requests in flight right now", "Deployed inference services", "Batch jobs submitted today", "Max output tokens per request"), plus a **Request increase** button (a mailto to the configured operator address with the org name prefilled — D7). The page provides loading, empty (all unlimited — a friendly "No limits are set for your organization"), and error states.
- **FR4.4** The tenant surface exposes **no** quota mutation — the page and bar are read-only (D7).

### FR5 — Surface, realm and API binding

- **FR5.1** Management is admin-only (`/admin/quotas` + `/api/v1/admin/quotas/*`); the tenant view is end-user (`/quotas` + `/api/v1/quotas/*`). A user-realm session on an admin route gets **10038 `CodeRealmMismatch`**; a session without the platform-admin role gets **10036 `CodeForbidden`**.
- **FR5.2** The admin page renders: the org table (Org, Concurrency (usage/limit), Services (usage/limit), Batch today (usage/limit), Token cap, Updated, Updated by), search and filters, an edit dialog (presets + four fields with validation copy), and a change-history drawer (Time, Operator, Dimension, Old → New). All states: loading, empty, error, permission-denied.
- **FR5.3** All quota mutations are audited (FR1.4); list/read calls are not audited, consistent with the platform's read-path convention.
- **FR5.4** The gateway enforcement contract (FR2) is **pinned in this document** and verified synthetically (the established pattern for out-of-repository data-plane components): the verdict fields, the check order (rate → quota → funds), the error codes and the `Retry-After` header are the contract; the compose stack's mock gateway honors them for e2e.

---

## 5. Page and Flow Design

### 5.1 Surface Assignment

| Page | Surface | Route | API prefix |
| --- | --- | --- | --- |
| Service quotas (fleet) | admin | `/admin/quotas` | `/api/v1/admin/quotas/*` |
| My quotas | end-user | `/quotas` | `/api/v1/quotas/*` |
| Quota bar (component) | end-user | org context area | `GET /api/v1/quotas` |

### 5.2 Page Map

| Page | Surface | Route | Primary components |
| --- | --- | --- | --- |
| Service quotas | admin | `/admin/quotas` | Org table with usage columns, edit dialog (presets + fields), change-history drawer |
| My quotas | end-user | `/quotas` | Four dimension cards with bars, Request increase (mailto) |
| Quota bar | end-user | org context strip | Compact per-dimension usage/limit chips |

### 5.3 Page: `/admin/quotas` — Service Quotas (admin)

**Org table**: Org (name + id), Concurrency ("12 / 50" or "Unlimited"), Services ("3 / 20" or "Unlimited"), Batch today ("5 / 50" or "Unlimited"), Token cap ("8192" or "Unlimited"), Updated (relative), Updated by. Row click opens the edit dialog; a history icon opens the change-history drawer.

**Filter bar**: name search, Limit state (All / Limited / Unlimited), pagination.

**Edit dialog**: the four fields (labeled with their ranges and "0 = unlimited"), the preset row (Default / Starter / Growth / Enterprise — one click fills the fields), inline validation (out-of-range shows "Enter 0 (unlimited) or 1–10000" style copy), and Save (PATCH). Saving shows a success toast and refreshes the row; a **13305** error highlights the offending field.

**Change-history drawer** (right side): the last 20 changes for the org — Time, Operator, and per-dimension Old → New ("Concurrency: 50 → 100"), sourced from the audit trail.

**States**: loading skeletons; empty ("No organizations yet"); error (stale with retry); permission-denied (standard, link to `/admin`).

### 5.4 Page: `/quotas` — My Quotas (end-user) and the quota bar

**Quota bar** (org context area, all end-user pages): four compact chips — "In flight 12/50", "Services 3/20", "Batch today 5/50", "Tokens ≤ 8192" (or "Unlimited" per chip); amber at ≥ 80%, red at 100%; clicking opens `/quotas`. The bar hides entirely when every dimension is unlimited (the default — no visual noise for unconstrained orgs).

**`/quotas` page**: four cards, each with the dimension name, plain-language description ("Requests in flight right now across your organization"), the usage/limit number pair, a progress bar (amber ≥ 80%, red 100%), and for the token cap a constant display ("Each request may generate at most 8192 tokens"). Below the cards: the **Request increase** button (mailto with the org name prefilled) and a note that quotas are set by the platform operator.

**States**: loading; the friendly empty state when all dimensions are unlimited ("No limits are set for your organization — usage is unrestricted"); error (stale with retry); permission-denied is impossible on the user surface (any org member sees the org's quotas).

### 5.5 Flows

1. **Operator caps a noisy tenant** (happy): admin → `/admin/quotas` → searches the org → sees Concurrency "142 / Unlimited" → edit dialog → sets Concurrency 50 (or picks Growth preset and adjusts) → Save → the verdict cache propagates within 30 s → the org's 51st in-flight request gets 13301 + `Retry-After: 1` → the tenant's quota bar turns red → the tenant opens `/quotas` → requests an increase by mail → the operator raises the cap after review.
2. **Tenant sizes its Agent**: developer → `/quotas` → reads "In flight 12/50" and "Tokens ≤ 8192" → configures the Agent's client concurrency to 40 (headroom) and `max_tokens` to 4096 → no quota errors in production.
3. **Service-count guard**: tenant admin → creates a service → `CreateInferenceService` → at the limit → **13302** "Your organization has reached its limit of 20 inference services…" → the tenant removes an unused service or requests an increase.
4. **Batch flood guard**: a tenant's script submits batch jobs in a loop → the day's Nth+1 submission → **13303** with today's count → the script backs off until midnight; the quota bar's "Batch today" chip shows the usage.
5. **Token-cap guard**: an Agent sends `max_tokens: 32768` against an org capped at 8192 → **13304** "capped at 8192 tokens per request" → the client lowers `max_tokens`; a request with no explicit `max_tokens` is silently clamped to 8192 and completes normally.
6. **Tightening is non-destructive**: the operator lowers Services 20 → 10 while the org runs 14 → no deletion occurs; the next create is rejected with **13302**; the org decommissions 4 services naturally; creates resume.

---

## 6. API Surface Implications

The quota RPCs extend **`tenancy`** (org quota fields, admin surface) and add a small **user-prefix read** (`GetMyQuota`); the enforcement contract extends `auth`'s `VerifyAPIKey` verdict and the `infer`/`batch` create paths. The gateway component is out of repository scope — its contract is pinned here (FR5.4).

| RPC | Route | Prefix | Status | Purpose |
| --- | --- | --- | --- | --- |
| `ListOrganizationQuotas` (extends `taas.tenancy.v1`) | `GET /api/v1/admin/quotas` | admin | **new** | Per-org quota rows with live usage; pagination, search, limit-state filter |
| `GetOrganizationQuota` | `GET /api/v1/admin/quotas/{organization_id}` | admin | **new** | One org's quota + usage + change history; unknown org → 13306 |
| `UpdateOrganizationQuota` | `PATCH /api/v1/admin/quotas/{organization_id}` | admin | **new** | Set any subset of the four dimensions; audited; 13305 on bad values |
| `GetMyQuota` | `GET /api/v1/quotas` | user | **new** | The caller's org quota with live usage (masked — own org only) |
| `VerifyAPIKey` (`taas.auth.v1`) | internal (gateway → `auth`) | n/a | **changed** | Verdict gains `max_concurrent_requests`, `max_output_tokens_per_request` (org-level, D3) |
| `CreateInferenceService` (`taas.infer.v1`) | `POST /api/v1/inference-services` | user | **changed** | Gains the org service-count check; 13302 at the limit |
| `CreateBatchJob` (`taas.batch.v1`) | `POST /api/v1/batch/jobs` | user | **changed** | Gains the daily batch-job check; 13303 at the limit |

**Contract notes for the Architect agent**:

1. The four quota fields live on the organization row (additive AutoMigrate columns on `organizations`, mirroring #6's `api_keys`/`accounts` pattern); the quota RPCs are tenancy-surface admin bindings, not a new service module — the error block (133xx) is new regardless (D4).
2. The gateway check order is binding: **rate limit (10037) → quota concurrency (13301) / token cap (13304) → funds (10502)** (§1.4). The token-cap check validates/clamps `max_tokens` in the OpenAI-compatible body before dispatch.
3. In-flight tracking is gateway-local (increment on accept, decrement on completion including disconnects); the usage snapshot RPC relays it best-effort (FR2.4). A restart re-derives or zeroes — documented, bounded.
4. The batch daily counter is `COUNT(*) WHERE org AND created_at >= local midnight` (no new table); the timezone is the platform's configured timezone, documented in the drawer.
5. `Retry-After: 1` on 13301 (the in-flight window is short); no `Retry-After` on 13302/13303/13304 (they are configuration errors, not transient load).
6. Serialization conventions unchanged: HTTP 200 on success, business errors as `{"code": <int>, "message": "..."}`, int64 fields as JSON strings.
7. The end-user `GetMyQuota` must enforce org scoping at the service layer (the caller's organization from the session), never trusting a client-supplied org id.

Error codes (`pkg/errors/codes.go`):

| Condition | Code | Constant | Notes |
| --- | --- | --- | --- |
| Org concurrency at its limit | 13301 | `CodeQuotaConcurrencyExceeded` | **New** (D4) — HTTP 429 + `Retry-After: 1` |
| Org at its inference-service limit | 13302 | `CodeQuotaServiceCountExceeded` | **New** (D4) — create rejected |
| Org at its daily batch-job limit | 13303 | `CodeQuotaBatchJobsExceeded` | **New** (D4) — create rejected |
| Request `max_tokens` above the org cap | 13304 | `CodeQuotaOutputTokensExceeded` | **New** (D4) — HTTP 400, cap named |
| Quota value out of range | 13305 | `CodeQuotaInvalid` | **New** (D4) — validation, dimension named |
| Unknown organization (quota read) | 13306 | `CodeQuotaNotFound` | **New** (D4) |
| User-realm session on an admin route | 10038 | `CodeRealmMismatch` | Reused — realm separation (FR5.1) |
| Session without the required role | 10036 | `CodeForbidden` | Reused — permission denial (FR5.1) |

---

## 7. Acceptance Criteria

| # | Criterion | Level |
| --- | --- | --- |
| AC1 | `/admin/quotas` renders the org table with Concurrency / Services / Batch today (usage/limit or "Unlimited"), Token cap, Updated and Updated by columns, plus search, limit-state filter and pagination | Nightwatch E2E |
| AC2 | The edit dialog validates all four fields (range + "0 = unlimited" copy) — an out-of-range value shows inline copy and the server returns **13305** with the dimension named; the four presets fill the fields in one click and remain editable before Save | Nightwatch E2E + FVT |
| AC3 | **Concurrency enforcement (synthetic)**: with the org capped at N, the N+1st concurrent request is rejected **13301** with `Retry-After: 1`; completing a request frees the slot; unlimited (0) never rejects | FVT (gateway contract) |
| AC4 | **Token-cap enforcement (synthetic)**: with the org capped at T, an explicit `max_tokens > T` is rejected **13304** naming the cap; an absent `max_tokens` is clamped to T and completes; `max_tokens ≤ T` passes | FVT (gateway contract) |
| AC5 | **Service-count guard**: with the org capped at N services, the N+1st `CreateInferenceService` returns **13302** with the current count; existing services are untouched by a tightening below the running count | FVT |
| AC6 | **Batch daily guard**: with the org capped at N jobs/day, the N+1st `CreateBatchJob` returns **13303** with today's count; canceled/failed jobs still count | FVT |
| AC7 | **Check order**: a request that would fail both rate limit and quota gets **10037** (rate first); a request that would fail both quota and funds gets the quota code (quota before funds) | FVT (gateway contract) |
| AC8 | **Quota bar and `/quotas`**: the end-user console shows the four chips with usage/limit (amber ≥ 80%, red 100%) and the `/quotas` page shows the four cards with plain-language guidance and the Request increase mailto; the bar hides when all dimensions are unlimited | Nightwatch E2E |
| AC9 | **Tenant org scoping**: `GET /api/v1/quotas` returns only the caller's org quota; no field leaks another org's data; the tenant surface has no quota mutation route | FVT + Nightwatch E2E |
| AC10 | **Change history and audit**: every `UpdateOrganizationQuota` appears in the audit trail (`quota.updated`, old → new, operator) and in the admin change-history drawer (last 20) | FVT + Nightwatch E2E |
| AC11 | **Propagation window**: a hot-path quota change is enforced within the verdict TTL (≤ 30 s); a lifecycle change on the next create call; the drawer documents both windows | FVT |
| AC12 | **Negative realm separation**: a valid user-realm session token sent to `GET /api/v1/admin/quotas` is rejected with **10038**; the admin page contains no `/api/v1/*` call and the user pages no `/api/v1/admin/*` call | FVT + Nightwatch E2E |
| AC13 | A session without the required role receives **10036** and the admin page shows the standard permission-denied state with a link back to `/admin` | Nightwatch E2E |
| AC14 | **States**: both pages provide loading, empty (admin: "No organizations yet"; user: the all-unlimited friendly state) and error states as specified | Nightwatch E2E |

---

## 8. Out of Scope (tracked elsewhere)

| Item | Where |
| --- | --- |
| Per-key quotas | Deliberately excluded (§1.2, D2) — the resource axes are org-level facts |
| Quota tiers/plans as a data model | Deliberately excluded (D5) — presets are UI constants; raw fields are the truth |
| Tenant self-service quota changes and approval workflows | Deliberately excluded (D7) — operator-only, mailto for requests |
| Project-level quotas | Deferred with the project model (feature #5) |
| Per-model / per-endpoint quotas | Future work — the org axis ships first |
| Quota overage billing | Deliberately excluded (§1.4) — hard fail-closed caps, money stays on #6/#8 |
| Rate limits (RPM/TPM) and spend limits | Feature #6 (reused unchanged; composes per §1.4) |
| Funds/balance enforcement | Feature #8 (reused unchanged) |
| Autoscaling behavior | Feature #16 — the quota caps the service *ceiling*, autoscaling caps the *behavior*; no interaction change |
| The gateway implementation itself | Out of repository scope — the contract is pinned here (FR5.4), verified synthetically (the established pattern) |
