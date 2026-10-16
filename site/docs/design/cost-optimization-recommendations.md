# Cost Optimization Recommendations — Requirement Analysis and UI/UX Design

| Attribute | Content |
| --- | --- |
| Feature point | Cost optimization recommendations — a deterministic, rule-based engine that reads the platform's own settled usage, price and deployment data and produces ranked, quantified, dismissible savings recommendations (cheaper-model substitution, batch migration, idle-service scale-to-zero, cache-hit improvement) with an estimated monthly saving and a one-click path to the acting page, on both the admin surface (`/admin/optimization`) and the tenant surface (`/optimization`) (backlog row 50) |
| Document scope | Requirement analysis, competitive research and the recommendation-engine decision (deterministic rules, no ML), the admin-surface page `/admin/optimization` (fleet-wide recommendations with org/model filters and a recommendation drawer), the end-user-surface page `/optimization` (the tenant's own recommendations), the page → API surface table, and numbered acceptance criteria |
| Owning modules | `optimization` (new — owns the recommendation engine, the recommendation store and the dismiss/restore lifecycle), `billing` (price matrix and charge records — read-only), `metering` (settled usage records — read-only), `infer` (inference-service state and autoscaling policies — read-only), `model` (catalog lookups), `batch` (batch-job history — read-only), `audit` (dismiss/restore events), `pkg/server` gateway (admin + user prefix bindings), `web` admin + end-user console |
| Related documents | [Architecture Design](./architecture.md) — §2.4 `billing`/`metering`, §3.1 Control Gateway · [Cost Analytics Dashboard](./cost-analytics-dashboard.md) — the cost attribution this feature turns into actions · [Usage & Cost Forecasting](./usage-cost-forecasting.md) — the forward-looking view; recommendations are the *acting* complement · [Pricing](./pricing.md) — the price matrix the cheaper-model rule reads · [Batch Inference](./batch-inference.md) — the 50% batch discount the batch-migration rule quantifies · [Inference Autoscaling](./inference-autoscaling.md) — the scale-to-zero policy the idle-service rule links to · [Model Authorization](./model-authorization.md) — the grant check a substitution suggestion must respect · [Intelligent Model Routing](./intelligent-model-routing.md) — routing groups as the *implementation* of a substitution, not a recommendation source · [Console Surface Separation](./console-surface-separation.md) — the two surfaces, the masked-projection rule |
| Status | Design complete, handed to the Architect agent |

---

## 1. Background and Competitive Research

### 1.1 Why Cost Optimization Comes Now

The platform's analytics spine is complete: the cost analytics dashboard (#29) answers *where is my money going*, the usage dashboard (#5) and per-key analytics (#28) attribute it, and forecasting (#36) answers *what will it be*. What no feature answers is the question every comparable platform's cost page ends with: **"what should I change?"**

Today an operator watching `/admin/cost` see a tenant burning budget on an expensive large model must manually cross-reference the price matrix (#25), guess whether a cheaper model would serve, check whether that tenant's workload is actually latency-sensitive, and remember the batch API (#42) discounts 50%. A tenant watching `/cost` has even less to act on. The data for every one of those judgments is already settled in the platform's own tables — prices, usage lines, service states, batch history — but nothing turns it into a recommendation.

This feature adds a **deterministic, rule-based recommendation engine**: a new `optimization` module periodically reads settled usage, the price matrix and inference-service state, applies a small set of **explicit, documented rules**, and produces ranked, quantified, dismissible recommendations — cheaper-model substitution, batch migration, idle-service scale-to-zero, cache-hit improvement — each with an estimated monthly saving and a one-click path to the page where the operator or tenant acts.

### 1.2 Access Pattern: Deterministic Rule Engine vs the Alternatives

| Pattern | What it is | Why we adopt / reject it |
| --- | --- | --- |
| **Deterministic rule engine over the platform's own data** (adopted) | A fixed, documented set of rules (§1.5 D3) evaluated on settled usage + prices + service state; every recommendation carries its rule, its inputs and its arithmetic, and the same inputs always produce the same recommendation. | The only pattern that is **explainable, testable and honest**: a recommendation is a spreadsheet the operator can verify, not a prophecy. It reuses data the platform already settles; no new instrumentation. Determinism makes the engine unit-testable and the UI reproducible in e2e. |
| ML / anomaly-detection advisor | A model trained on usage patterns that predicts waste or clusters workloads. | Rejected for v1: unexplainable output ("the model says switch") cannot be acted on with confidence, cannot be e2e-tested deterministically, and adds a training/serving stack for marginal gain. The platform's cost levers are few and known — rules cover them. |
| Live prompt-content analysis | Inspecting prompt/completion content to suggest compression, shorter system prompts, or distillation. | Rejected: prompt content is tenant data under the data-export/privacy boundary (#38); a cost advisor must not read it. Rules use **aggregate usage metadata only** (token counts, prices, service states). |
| Automatic enforcement (auto-apply) | The platform applies the recommendation itself — rewrites prices, migrates traffic, scales services. | Rejected: a recommendation is advice; acting on it (granting a model, editing a routing group, enabling scale-to-zero, submitting a batch job) is a human decision with billing and SLO consequences. The engine never mutates anything outside its own recommendation store (D6). |
| Extending `/admin/cost` with a recommendations tab | Fold recommendations into the analytics page. | Rejected: analytics answers *what happened*; recommendations are an actionable worklist with their own lifecycle (dismiss/restore, refresh cadence, filters). A separate page keeps both clean, mirrors how AWS separates Cost Explorer from Cost Recommendations (D1). |

### 1.3 Comparable-Product Interaction Patterns

| Product | Recommendation pattern | go-taas decision |
| --- | --- | --- |
| **AWS Cost Explorer / Cost Optimization Hub** | A dedicated recommendations surface next to the explorer; each recommendation has an estimated monthly saving, a resource link, and a "view actions" path; recommendations are ranked by savings; dismissible. | Adopt the shape wholesale: a **dedicated page**, ranked cards, estimated monthly saving, a link to the acting page, dismiss with a reason. AWS's ~20 recommendation types collapse to **four rules** for v1 — the platform's cost levers are fewer and known. |
| **OpenAI Platform usage page** | Per-project spend breakdowns and a monthly budget with alerts; no model-substitution advice (single-vendor pricing). | Adopt the budget-alert adjacency (recommendations sit beside the spend context); substitution advice is only meaningful in a **multi-model marketplace** like go-taas's catalog — a differentiator, not a copy. |
| **Anthropic Console** | Usage by model with cost per model; no recommendations. | Confirms the gap this feature fills; the console pattern (model-attributed spend) is already covered by #29. |
| **Together AI / SiliconFlow** | Model pages show price per 1M tokens side by side; the user does the comparison manually. | Adopt the **side-by-side price comparison inside the recommendation drawer** (current model vs suggested model, input/output/cache rates) — the drawer does the comparison the user would otherwise do by hand. |
| **Modal / Baseten** | Per-deployment GPU-hour cost with idle detection ("this deployment has been idle for N hours"). | Adopt idle detection as the **idle-service rule** — go-taas's equivalent is a service with scale-to-zero disabled and near-zero traffic; the recommendation links to the autoscaling page (#16). |
| **vLLM / AIBrix guidance** | Documentation-level advice: enable prefix caching, use shorter contexts, batch offline workloads. | Adopt the two measurable ones: **cache-hit improvement** (the metering voucher already records cached tokens) and **batch migration** (the batch API's 50% discount is a platform fact). Context-length advice is not measurable from settled data — excluded. |

### 1.4 The Advice-Only Invariant

The core product rule, mirroring #49's no-silent-substitution:

1. **The engine never acts.** It writes only to its own recommendation store. Granting models, editing routing groups, enabling scale-to-zero and submitting batch jobs remain human actions on their existing pages.
2. **Every number is verifiable.** A recommendation displays its rule, its inputs (usage window, token counts, prices) and its arithmetic; the same inputs always yield the same estimate. No ML, no hidden scoring.
3. **A recommendation respects the platform's own rules.** A substitution suggestion only proposes models the org is granted (or that are grantable); a batch-migration suggestion only fires when the batch API serves the model; an idle-service suggestion only when autoscaling is off.
4. **Recommendations are advisory, never blocking.** Nothing in the request path reads the recommendation store. Dismissing a recommendation has no effect outside the UI.

### 1.5 Decisions and Scope

| # | Decision | Rationale |
| --- | --- | --- |
| D1 | **A new section on both surfaces**: admin `/admin/optimization` + `/api/v1/admin/optimization/*` (fleet-wide, org/model filters), end-user `/optimization` + `/api/v1/optimization/*` (the tenant's own, org-scoped) — **not** a tab on `/admin/cost` | Recommendations are an actionable worklist with their own lifecycle, not analytics; both surfaces need them (the operator optimizes the fleet, the tenant optimizes its own spend) — mirrors AWS Cost Optimization Hub vs the account-level view (§1.2) |
| D2 | **A new `optimization` module** (`services/optimization`) owns the rule engine, the recommendation store and the dismiss/restore lifecycle, with its own error block **132xx**. It reads `billing` prices/charges, `metering` usage, `infer` service state and `batch` history **read-only**; it writes nothing outside its own tables | A distinct concern (rules, estimates, a worklist lifecycle) with its own cadence; a dedicated module keeps analytics modules read-only and the engine unit-testable in isolation |
| D3 | **Four deterministic rules for v1**: R1 **cheaper-model substitution** (an org's traffic on model A where a granted-or-grantable model B with ≥ 80% of A's context length and a lower effective price would save ≥ ¥100/month), R2 **batch migration** (an org's key with ≥ 1000 requests/month that are ≥ 90% non-interactive — no latency SLO observable — on a batch-served model), R3 **idle-service scale-to-zero** (a service with scale-to-zero disabled and < 10 requests/day over 7 days), R4 **cache-hit improvement** (an org/model pair with < 5% cache-hit rate and ≥ 1M prompt tokens/month where the model supports caching). Each rule's thresholds are **config constants**, documented in the drawer | The platform's cost levers are few and known; four rules cover substitution, workload shape, capacity and caching — the same axes AWS's twenty types reduce to. Explicit thresholds keep the engine deterministic and the copy honest (§1.2) |
| D4 | **Estimates are arithmetic, not forecasts**: monthly saving = (current effective price − alternative effective price) × observed monthly volume, using the **same 30-day settled window** for every rule; the drawer shows the inputs and the formula. No confidence bands, no growth assumptions — forecasting is #36 | A recommendation the operator can verify line-by-line; conflating advice with prediction would make both less trustworthy |
| D5 | **Ranking is by estimated monthly saving, descending**; ties break by rule then target name. The list shows the top 50; the summary cards show the total estimated monthly saving (sum of active recommendations) and the count by rule | The worklist metaphor: the biggest lever first; the total gives the page its headline number, computed from the same rows the user sees |
| D6 | **Recommendations are refreshed periodically** (default hourly, `Optimization.RefreshIntervalMinutes`) and **snapshotted**: each refresh recomputes from settled data and replaces the active set; **dismissed recommendations persist by fingerprint** (rule + target + suggestion) so a dismissed recommendation does not reappear while its fingerprint stays active. Dismiss takes a required reason (`not_applicable`, `planned`, `other` + free text ≤ 256); restore undoes it. All dismiss/restore actions are audited | Deterministic recomputation keeps estimates honest; fingerprint persistence respects the operator's judgment without freezing the worklist; the audit trail matches every other mutating surface (#22) |
| D7 | **Substitution suggestions respect grants**: R1 only proposes a model the org is **already granted**, or that is public/grantable — the drawer states which, and the acting path is the grant page (#13) then optionally a routing group (#49). The engine never proposes a model the org cannot legally call | A suggestion the tenant cannot act on is noise; the grant check is the platform's own authorization rule applied to advice (§1.4) |
| D8 | **Both surfaces share the rule set and the card/drawer components**; the admin surface adds org/model filters and the fleet total; the end-user surface is org-scoped with the masked projection — no service ids, replica counts or operator internals (the idle-service rule is **admin-only**: services are operator resources). A user-realm session on an admin route gets **10038**; a session without the role gets **10036** | Consistent with the console-surface separation and the masked-projection rule (#17); the tenant sees its own spend levers, the operator sees the fleet's |
| D9 | **New error codes in an optimization block (13201–13299)**: **13201 `CodeRecommendationNotFound`**, **13202 `CodeRecommendationDismissed`** (double dismiss), **13203 `CodeRecommendationDismissReasonInvalid`**, **13204 `CodeRecommendationRestoreInvalid`** (restore a non-dismissed recommendation), **13205 `CodeOptimizationRangeInvalid`** | A new module (D2) gets a fresh block after routing's 131xx; distinct codes keep each failure mode actionable |

**In scope**: the `optimization` module (rule engine, store, refresh loop, RPCs); the admin page `/admin/optimization` (summary cards, filterable ranked list, recommendation drawer with inputs/arithmetic/price comparison, dismiss dialog with reason, restore); the end-user page `/optimization` (the tenant's own cards/list/drawer, dismiss/restore); the acting-page links (grant, routing-group, autoscaling, batch-submit, pricing pages); audit events for dismiss/restore.

**Out of scope** (tracked elsewhere or deliberately excluded): automatic enforcement of any recommendation (deliberately excluded, §1.2/D6 — advice only), ML/anomaly advisors and prompt-content analysis (deliberately excluded, §1.2), forecasting and confidence bands (feature #36), cost analytics and attribution (feature #29, read-only input), budget alerts and spend limits (feature #6), per-rule threshold editing UI (thresholds are config constants; a tuning UI is future work), recommendation email/webhook delivery (the notification center #26 could carry it later), and multi-currency (the platform settles in one currency, #25).

---

## 2. User Roles

| Role | Description | Interaction with recommendations |
| --- | --- | --- |
| **Platform administrator** | The operator who runs the go-taas platform | Opens `/admin/optimization` → sees the fleet's ranked recommendations and the total estimated monthly saving → filters by org/model/rule → opens a drawer, verifies the arithmetic → dismisses with a reason or follows the acting link |
| **Tenant administrator** | Administers an organization and its budget | Opens `/optimization` → sees the org's own recommendations (substitution, batch, cache — never service internals) → acts on them through the linked pages |
| **Tenant developer / Agent owner** | Writes the applications that call `/v1` | Reads the org's recommendations to decide which model the Agent should call and whether its offline workload belongs on the batch API |
| **Platform auditor** | Reviews operational and billing attribution | Reads the dismiss/restore audit trail to see which recommendations were acted on or declined, and why |

> Terminology: the consumer-side caller is called an **Agent** (English) / 「智能体」(Chinese), consistent with the repo convention. The idle-service rule appears on the admin surface only — services are operator resources (D8).

---

## 3. User Stories

| # | As a… | I want to… | So that… |
| --- | --- | --- | --- |
| US1 | Platform administrator | see the fleet's cost optimizations ranked by estimated monthly saving with a total | I know the size of the waste and where the biggest lever is before reading a single detail |
| US2 | Platform administrator | open a recommendation and see its rule, inputs and arithmetic — the usage window, the token counts, both prices and the formula | I can verify the estimate myself and trust it before acting |
| US3 | Platform administrator | dismiss a recommendation with a reason and have it stay dismissed while its condition persists | my judgment is recorded and the worklist does not nag me with the same advice |
| US4 | Platform administrator | follow a one-click link from a recommendation to the page where I act on it | I don't hunt for the right console page — the recommendation takes me there |
| US5 | Tenant administrator | see my organization's own savings opportunities without seeing platform internals | I can cut my bill myself, with the same verifiable arithmetic the operator sees |
| US6 | Tenant administrator | be told when my traffic on an expensive model could move to a cheaper granted model | I act on a concrete, priced suggestion instead of comparing price pages by hand |
| US7 | Tenant developer | be told when my key's offline-shaped workload belongs on the batch API at half price | my bulk jobs stop paying real-time rates |
| US8 | Platform auditor | read the dismiss/restore trail with reasons | declined recommendations are explainable in the cost review |
| US9 | Platform administrator | never see a recommendation the platform cannot legally or technically deliver (an ungranted model, a batch-unsupported model, an already-scaled service) | the worklist contains only actionable advice |

---

## 4. Functional Requirements

### FR1 — Recommendation model and refresh

- **FR1.1** A recommendation is `{recommendation_id, rule (r1_substitution | r2_batch | r3_idle_service | r4_cache), target_type (org_model | api_key | service | org_model), target_id, target_display (org name + model name / key name / service name), suggestion (the proposed change, human-readable), estimated_monthly_saving (decimal, 2 places, platform currency), inputs (rule-specific: usage window, token counts, request counts, prices, cache rates), arithmetic (the formula with the numbers substituted), acting_page (route + label), status (active | dismissed), dismissed_reason?, dismissed_by?, dismissed_at?, fingerprint, first_seen_at, last_computed_at}`.
- **FR1.2** The engine runs on a fixed interval (default 60 minutes, `Optimization.RefreshIntervalMinutes`, 10–1440) and on an explicit `RefreshOptimization` admin trigger. Each run recomputes the active set from the **last 30 days of settled data** (D4) and replaces the active recommendations; dismissed fingerprints persist (D6). A run is idempotent — the same settled data yields the same set.
- **FR1.3** Rule R1 **cheaper-model substitution**: for each (org, model A) with ≥ 1000 requests and ≥ 10M billed tokens in the window, find candidate models B where: B ≠ A; B is granted to the org or public/grantable (D7); B's context length ≥ 80% of A's; B's effective price (blended input/output at the org's observed token mix) is lower; and the estimated monthly saving ≥ the configured floor (default ¥100). The suggestion names B; the drawer shows the side-by-side price comparison and the observed token mix. If multiple B qualify, the cheapest is suggested; the others are listed as alternatives.
- **FR1.4** Rule R2 **batch migration**: for each (org, api key) with ≥ 1000 requests in the window on a model the batch API serves (#42), where the key's traffic shows offline shape (≥ 90% of requests outside business-adjusted peak windows **or** ≥ 90% of requests with prompt tokens ≥ 512 and no streaming), suggest migrating to the batch API; the saving = 50% of the observed spend on that key/model. The drawer links to the batch-submit page.
- **FR1.5** Rule R3 **idle-service scale-to-zero** (admin surface only, D8): for each inference service with autoscaling disabled or scale-to-zero off and < 10 requests/day average over 7 days, suggest enabling scale-to-zero (#16); the saving = the service's idle GPU cost (replicas × card price × 24h × 30d, from the accelerator inventory #18 and the price matrix). The drawer links to the autoscaling page.
- **FR1.6** Rule R4 **cache-hit improvement**: for each (org, model) with ≥ 1M prompt tokens in the window, cache-hit rate < 5%, on a model whose engine supports prefix caching, suggest enabling/structuring prompts for caching; the saving = (observed cache-miss prompt tokens × cache discount on the model's price) estimated at the platform's default cache-hit assumption (50%). The drawer shows the current cache-hit rate and the pricing page link.
- **FR1.7** All refresh runs log a summary (rules evaluated, recommendations emitted, duration) at info level; failures log at error and leave the previous active set intact (a failed refresh never empties the worklist).

### FR2 — Admin surface: list and filters

- **FR2.1** `ListOptimizationRecommendations` (`GET /api/v1/admin/optimization/recommendations`) returns the ranked active recommendations (D5) with `page_size`/`page_token` pagination and filters: `rule`, `status` (active | dismissed | all, default active), `organization_id`, `model_id`, and `min_saving`. Each row carries the full FR1.1 projection.
- **FR2.2** `GetOptimizationOverview` (`GET /api/v1/admin/optimization`) returns the summary cards: total estimated monthly saving (active), count by rule, count dismissed, last refresh time, and the top org by estimated saving.
- **FR2.3** `GetOptimizationRecommendation` (`GET /api/v1/admin/optimization/recommendations/{recommendation_id}`) returns one recommendation with its full inputs, arithmetic, alternatives (R1) and dismiss state. An unknown id returns **13201 `CodeRecommendationNotFound`**.
- **FR2.4** `RefreshOptimization` (`POST /api/v1/admin/optimization:refresh`) triggers an immediate refresh (FR1.2) and returns the new summary. It is audited (`optimization.refreshed`).

### FR3 — Dismiss and restore lifecycle

- **FR3.1** `DismissRecommendation` (`POST /api/v1/admin/optimization/recommendations/{recommendation_id}:dismiss`) takes a required `reason` (`not_applicable` | `planned` | `other`) and, for `other`, a required free-text note (≤ 256 chars). Dismissing an already-dismissed recommendation returns **13202 `CodeRecommendationDismissed`**; an invalid reason or over-length note returns **13203 `CodeRecommendationDismissReasonInvalid`**. The action is audited (`optimization.recommendation_dismissed`) with the acting operator.
- **FR3.2** `RestoreRecommendation` (`POST /api/v1/admin/optimization/recommendations/{recommendation_id}:restore`) moves a dismissed recommendation back to active. Restoring a non-dismissed recommendation returns **13204 `CodeRecommendationRestoreInvalid`**. Audited (`optimization.recommendation_restored`).
- **FR3.3** A dismissed recommendation stays dismissed across refreshes while its **fingerprint** (rule + target + suggestion) remains in the computed set; when the condition disappears and later re-appears with the same fingerprint it **stays dismissed** (the fingerprint, not the row, is the identity). Dismissed rows are visible with the `status=dismissed` filter and restorable (FR3.2).
- **FR3.4** The end-user surface exposes the same dismiss/restore lifecycle scoped to the tenant's own recommendations (`POST /api/v1/optimization/recommendations/{recommendation_id}:dismiss` / `:restore`), with the same codes and audit events; the tenant's dismiss reason is its own — it does not affect the admin view of the same underlying condition (each surface keeps its own dismiss state, keyed by surface).

### FR4 — End-user surface (masked projection)

- **FR4.1** `GetMyOptimization` (`GET /api/v1/optimization`) returns the tenant's own summary cards and ranked active recommendations for the caller's organization: R1, R2 and R4 only (R3 is admin-only, D8). No service ids, replica counts, card prices or operator internals appear in any field; the idle-service rule is absent from the rule filter.
- **FR4.2** `GetMyOptimizationRecommendation` (`GET /api/v1/optimization/recommendations/{recommendation_id}`) returns one of the org's recommendations (an id belonging to another org returns **13201**); the drawer projection is identical to the admin drawer minus operator internals.
- **FR4.3** The end-user acting links point at end-user pages only: the model catalog (`/models`), the batch submit page (`/batch`), the pricing view (`/pricing`) and the playground — never `/admin/*`.

### FR5 — Surface, realm and API binding

- **FR5.1** Management of the engine (refresh trigger) and the fleet view are admin-only (`/admin/optimization` + `/api/v1/admin/optimization/*`); the tenant view is end-user (`/optimization` + `/api/v1/optimization/*`). A user-realm session on an admin route gets **10038 `CodeRealmMismatch`**; a session without the platform-admin role gets **10036 `CodeForbidden`**.
- **FR5.2** Both pages render: summary cards (total estimated monthly saving, counts by rule, last refresh), a ranked table/cards (Rule badge, Target, Suggestion, Estimated monthly saving, Status, First seen), filters (rule, status, org [admin], model [admin], min saving), a **Refresh now** button (admin), and a row drawer (inputs, arithmetic, price comparison [R1], alternatives [R1], acting link, dismiss/restore).
- **FR5.3** Every page provides loading, empty ("No recommendations — the engine found no savings opportunities in the last 30 days"), error, and permission-denied states; the empty state on the user surface adds "Check back after the next refresh".
- **FR5.4** All dismiss/restore/refresh mutations are audited with the acting identity (FR3.1–FR3.4, FR2.4); list/read calls are not audited, consistent with the platform's read-path convention.

---

## 5. Page and Flow Design

### 5.1 Surface Assignment

| Page | Surface | Route | API prefix |
| --- | --- | --- | --- |
| Cost optimization (fleet) | admin | `/admin/optimization` | `/api/v1/admin/optimization/*` |
| Cost optimization (my org) | end-user | `/optimization` | `/api/v1/optimization/*` |

### 5.2 Page Map

| Page | Surface | Route | Primary components |
| --- | --- | --- | --- |
| Optimization | admin | `/admin/optimization` | Summary cards, filter bar, ranked recommendation cards, recommendation drawer, dismiss dialog |
| Optimization | end-user | `/optimization` | Summary cards (masked), filter bar (rule/status/min-saving), ranked cards, drawer, dismiss dialog |

### 5.3 Page: `/admin/optimization` — Cost Optimization (admin)

**Summary cards** (top): "Estimated monthly savings" (the headline total, active recommendations), "Active recommendations" (count, with per-rule chips), "Dismissed" (count, link to the dismissed filter), "Last refresh" (relative time + Refresh now button).

**Filter bar**: Rule (All / Substitution / Batch / Idle service / Cache), Status (Active / Dismissed / All), Organization (select), Model (select), Min saving (number). Filters apply immediately.

**Recommendation cards** (ranked by estimated monthly saving desc): each card shows the Rule badge (color per rule), Target ("`<org>` → `<model>`" / "`<org>` / key `<name>`" / service `<name>`), the Suggestion one-liner, **"≈ ¥X / month"** (estimated saving, prominent), Status (Active / Dismissed with reason tooltip), First seen (relative), and a chevron opening the drawer.

**Drawer** (right side): the full recommendation —
1. **What**: the suggestion in plain language ("Switch `<org>`'s traffic on `<model A>` to `<model B>`").
2. **Why (verifiable)**: the inputs table (usage window, requests, prompt/completion tokens, observed token mix, both models' prices [R1], cache-hit rate [R4], idle GPU cost [R3]) and the arithmetic line ("(¥A − ¥B) × 12.4M tokens ≈ ¥X/month").
3. **Alternatives** (R1 only): the other qualifying models with their prices and savings.
4. **Act**: the acting-page link ("Configure grants" → `/admin/models/{id}/authorizations`, "Edit routing group" → `/admin/routing-groups`, "Enable scale-to-zero" → `/admin/autoscaling`, "Submit batch job" → `/admin/batch`, "View pricing" → `/admin/pricing").
5. **Dismiss**: the dismiss button opening the reason dialog (`not_applicable` / `planned` / `other` + note); a dismissed recommendation shows the reason and a Restore button.

**States**: loading skeletons; empty ("No recommendations — the engine found no savings opportunities in the last 30 days"); error (stale data with retry); permission-denied (standard, link to `/admin`).

### 5.4 Page: `/optimization` — Cost Optimization (end-user)

Identical layout to §5.3 minus: the Organization and Model filters (the org is the caller's), the idle-service rule (absent), the Refresh now button (the tenant cannot trigger refreshes), and operator internals in the drawer (no card prices in R3 — R3 is absent; no service ids anywhere). The acting links point at `/models`, `/batch`, `/pricing`, `/playground`. The empty state adds "Check back after the next refresh".

### 5.5 Flows

1. **Operator reviews the fleet's savings** (happy): admin → `/admin/optimization` → reads the headline total → opens the top card's drawer → verifies the arithmetic → clicks the acting link → acts on the target page → returns; the next refresh recomputes and the condition disappears.
2. **Operator dismisses with a reason**: drawer → Dismiss → reason dialog → `not_applicable` + confirm → the card moves to the Dismissed filter; the audit trail records the operator, reason and time; subsequent refreshes keep it dismissed (fingerprint).
3. **Tenant cuts its own bill**: tenant admin → `/optimization` → sees "Switch traffic on `<model A>` to `<model B>` ≈ ¥X/month" → verifies the price comparison → opens `/models` → switches the Agent's model → the next refresh drops the recommendation.
4. **Tenant migrates to batch**: tenant admin → `/optimization` → R2 card "Key `<name>`'s offline workload → batch API ≈ ¥X/month" → opens `/batch` → submits the JSONL job → verifies the 50% rate on the bill.
5. **Operator enables scale-to-zero**: admin → `/admin/optimization` → R3 card for an idle service → drawer shows the idle GPU cost arithmetic → "Enable scale-to-zero" → `/admin/autoscaling` → enables it → the next refresh drops the card.
6. **Refresh failure**: the engine errors → the page shows the stale banner with the last successful refresh time → the previous active set remains; the error is logged; retry on the next interval.

---

## 6. API Surface Implications

The recommendation RPCs belong to the new **`optimization` module** (D2), served as HTTP via the Control Gateway on the **admin prefix** `/api/v1/admin/optimization/*` and the **user prefix** `/api/v1/optimization/*`. The engine reads `billing`, `metering`, `infer` and `batch` data through internal RPCs or shared repositories (the Architect's choice); it writes only its own tables.

| RPC | Route | Prefix | Status | Purpose |
| --- | --- | --- | --- | --- |
| `GetOptimizationOverview` (new `taas.optimization.v1`) | `GET /api/v1/admin/optimization` | admin | **new** | Fleet summary cards: total saving, counts by rule/status, last refresh, top org |
| `ListOptimizationRecommendations` | `GET /api/v1/admin/optimization/recommendations` | admin | **new** | Ranked recommendations; pagination + rule/status/org/model/min-saving filters |
| `GetOptimizationRecommendation` | `GET /api/v1/admin/optimization/recommendations/{recommendation_id}` | admin | **new** | One recommendation with inputs, arithmetic, alternatives; unknown id → 13201 |
| `DismissRecommendation` | `POST /api/v1/admin/optimization/recommendations/{recommendation_id}:dismiss` | admin | **new** | Dismiss with a required reason; audited |
| `RestoreRecommendation` | `POST /api/v1/admin/optimization/recommendations/{recommendation_id}:restore` | admin | **new** | Restore a dismissed recommendation; audited |
| `RefreshOptimization` | `POST /api/v1/admin/optimization:refresh` | admin | **new** | Trigger an immediate engine run; audited |
| `GetMyOptimization` | `GET /api/v1/optimization` | user | **new** | The org's own summary (R1/R2/R4 only, masked) |
| `ListMyOptimizationRecommendations` | `GET /api/v1/optimization/recommendations` | user | **new** | The org's own ranked recommendations; rule/status/min-saving filters |
| `GetMyOptimizationRecommendation` | `GET /api/v1/optimization/recommendations/{recommendation_id}` | user | **new** | One of the org's recommendations; another org's id → 13201 |
| `DismissMyRecommendation` / `RestoreMyRecommendation` | `POST /api/v1/optimization/recommendations/{recommendation_id}:dismiss` / `:restore` | user | **new** | The tenant's own dismiss/restore lifecycle |

**Contract notes for the Architect agent**:

1. The engine is a **periodic batch job** inside the `optimization` service (a ticker, mirroring the settlement cadence pattern); it is not request-triggered except via `RefreshOptimization`.
2. The 30-day window, all rule thresholds (FR1.3–FR1.6) and the refresh interval are **config constants** (`Optimization.*`), documented in the drawer; no runtime threshold editing in v1.
3. `estimated_monthly_saving` is a decimal serialized as a JSON **string** with 2 decimal places (int64/decimal-as-string convention); the currency is the platform's single settlement currency.
4. The fingerprint is `sha256(rule | target_type | target_id | suggestion)`; dismiss state is keyed `(surface, fingerprint)` (FR3.4).
5. R3's idle GPU cost reads the accelerator inventory (#18) and the price matrix (#25); if either lacks data for a service's card type, the rule **skips the service** (never emits a zero-saving or unpriced recommendation).
6. Serialization conventions unchanged: HTTP 200 on success, business errors as `{"code": <int>, "message": "..."}`, int64 fields as JSON strings.
7. The end-user RPCs must enforce org scoping at the service layer (the caller's organization from the session), never trusting a client-supplied org id.

Error codes (`pkg/errors/codes.go`):

| Condition | Code | Constant | Notes |
| --- | --- | --- | --- |
| Unknown recommendation | 13201 | `CodeRecommendationNotFound` | **New** (D9) |
| Dismissing an already-dismissed recommendation | 13202 | `CodeRecommendationDismissed` | **New** (D9) |
| Invalid dismiss reason or over-length note | 13203 | `CodeRecommendationDismissReasonInvalid` | **New** (D9) |
| Restoring a non-dismissed recommendation | 13204 | `CodeRecommendationRestoreInvalid` | **New** (D9) |
| Invalid filter/range (e.g. min_saving < 0, bad status) | 13205 | `CodeOptimizationRangeInvalid` | **New** (D9) |
| User-realm session on an admin route | 10038 | `CodeRealmMismatch` | Reused — realm separation (FR5.1) |
| Session without the required role | 10036 | `CodeForbidden` | Reused — permission denial (FR5.1) |

---

## 7. Acceptance Criteria

| # | Criterion | Level |
| --- | --- | --- |
| AC1 | `/admin/optimization` renders the summary cards (total estimated monthly saving, active/dismissed counts, last refresh) and the ranked recommendation cards with Rule badge, Target, Suggestion, "≈ ¥X / month", Status and First seen | Nightwatch E2E |
| AC2 | The filter bar works: Rule, Status (Active/Dismissed/All), Organization, Model and Min saving filter the list; ranking is by estimated monthly saving descending | Nightwatch E2E |
| AC3 | The drawer shows the inputs table, the arithmetic line and (for R1) the side-by-side price comparison and alternatives; the acting link navigates to the correct page (`/admin/models/{id}/authorizations`, `/admin/routing-groups`, `/admin/autoscaling`, `/admin/batch`, `/admin/pricing`) | Nightwatch E2E |
| AC4 | **Dismiss/restore lifecycle**: dismissing requires a reason; `other` requires a note; a dismissed card moves to the Dismissed filter with the reason tooltip; restoring returns it to Active; double dismiss → **13202**; restore of an active card → **13204**; both actions appear in the audit log | Nightwatch E2E + FVT |
| AC5 | **Fingerprint persistence**: after a refresh that recomputes the same condition, a dismissed recommendation stays dismissed (does not reappear in Active) | FVT |
| AC6 | **Rule determinism (unit)**: given a seeded usage/price/service fixture, the engine emits exactly the expected recommendations with the expected estimates — the same fixture always yields the same set and numbers | Unit |
| AC7 | **R1 respects grants**: the engine never proposes a model the org is not granted and that is not grantable; the drawer states which case applies | FVT |
| AC8 | **R3 admin-only masking**: the end-user `/optimization` page and `GET /api/v1/optimization` return no idle-service recommendations and no service ids/replica counts/card prices in any field | FVT + Nightwatch E2E |
| AC9 | **End-user org scoping**: `GET /api/v1/optimization/recommendations/{id}` with another org's recommendation id returns **13201**; the user list contains only the caller's org rows | FVT |
| AC10 | **Refresh**: `POST /api/v1/admin/optimization:refresh` recomputes and the page reflects the new set and last-refresh time; a failed refresh leaves the previous active set and shows the stale banner | FVT + Nightwatch E2E |
| AC11 | **Negative realm separation**: a valid user-realm session token sent to `GET /api/v1/admin/optimization` is rejected with **10038**; the admin page contains no `/api/v1/*` call and the user page no `/api/v1/admin/*` call | FVT + Nightwatch E2E |
| AC12 | A session without the required role receives **10036** and both pages show the standard permission-denied state with a link back | Nightwatch E2E |
| AC13 | **Empty and error states**: with no qualifying conditions the pages show the specified empty copy; a backend failure shows the stale banner with retry | Nightwatch E2E |
| AC14 | **Audit trail**: `optimization.refreshed`, `optimization.recommendation_dismissed` and `optimization.recommendation_restored` events carry the acting identity, target and (for dismiss) the reason | FVT |

---

## 8. Out of Scope (tracked elsewhere)

| Item | Where |
| --- | --- |
| Automatic enforcement of any recommendation | Deliberately excluded (§1.2, D6) — advice only, the acting pages stay human-driven |
| ML / anomaly-detection advisors, prompt-content analysis | Deliberately excluded (§1.2) — deterministic rules over aggregate metadata only |
| Forecasting, confidence bands, growth assumptions | Feature #36 usage & cost forecasting |
| Cost attribution and analytics | Feature #29 cost analytics dashboard (read-only input) |
| Budget alerts and spend limits | Feature #6 rate limits & spend limits |
| Per-rule threshold editing UI | Future work — thresholds are config constants in v1 (D3) |
| Recommendation delivery via email/webhook | The notification center (#26) could carry it later |
| Multi-currency estimates | The platform settles in one currency (#25) |
| The acting pages themselves | Features #13, #49, #16, #42, #25 (reused unchanged; this feature only links to them) |
