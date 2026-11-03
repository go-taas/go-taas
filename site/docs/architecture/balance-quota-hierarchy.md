# Balance, Account and Quota Hierarchy — Architecture and Detailed Design

| Attribute | Content |
| --- | --- |
| Feature point | One shared organization account, with a quota ladder from organization to project to the person in that project |
| Document scope | The account model, the quota ladder, enforcement points, the console surfaces, the API surface, the upgrade path, and the validation rules |
| Owning modules | `tenancy` (organization / project / member / personal budgets), `billing` (account, balance and the spend gate), `auth` (verdict propagation to the data plane), `wasm/gateway` (local enforcement) |
| Extends | [Balance (Prepaid) & Quota (Postpaid) Account Modes](./balance-quota.md), [Service Quota Management](./service-quota-management.md), [API Key Ownership and Three-Level Quotas](./api-key-ownership.md) |
| Status | Implemented |

---

## 1. Problem

Three questions kept being answered in the code but not in one place:

1. **Where does the money live?** An organization has one billing account —
   prepaid balance or postpaid quota — and its members spend from it. A person
   does not have their own wallet, and should not: a tenant buys credit once and
   distributes it with budgets.
2. **Who can be capped?** The organization (the tenant default), a project (a
   team's share), and one person inside a project (a member's share).
3. **What does the data plane actually enforce?** The minimum of the non-zero
   values on the ladder, resolved per API key from the key's organization,
   project and owner.

This document is the single reference for those three answers.

## 2. Architecture decisions

| # | Decision | Rationale |
| --- | --- | --- |
| AD1 | **The account belongs to the organization; members share it.** There is no per-user balance. | A tenant recharges once. Per-user wallets would need per-user payments, refunds and reconciliation, and would fragment the credit a team actually shares. |
| AD2 | **The quota ladder is organization → project → user**, evaluated per dimension as the minimum of the non-zero values. | Every tier can only tighten. An organization budget of 100 requests cannot be raised to 200 by a project budget of 200. |
| AD3 | **The user tier has two sources: the org-wide personal budget (`user_quotas`) and the member budget (`project_members`). Both apply.** | A person is one spend subject across the tenant, and also one member inside each project. Taking the stricter of the two keeps both promises: an org-wide cap cannot be bypassed by joining a project, and a project cap cannot be loosened by the org-wide one. |
| AD4 | **`0` means unlimited at every tier.** | It is the only value that can mean "no limit" without inventing a sentinel, and it keeps an unset tier neutral in the minimum. |
| AD5 | **The member budget lives on the membership row.** | The budget is scoped to `(project, user)`; dropping the membership must drop the budget, and the same person can hold a different budget per project. |
| AD6 | **The spend gate evaluates the same ladder the request gate does.** | Two gates that disagree are worse than one: a key rejected for concurrency but allowed to overspend (or the reverse) is unexplainable to a tenant. |
| AD7 | **A read failure degrades that tier to unlimited and logs a warning.** | An authorization or tenancy outage must not stop inference; the remaining tiers still apply. |
| AD8 | **Every change is audited and attributed.** | `quota_updated_by` / `quota_updated_at` on each row, plus an audit event per edit, so "who raised this budget" is answerable. |

## 3. The model

### 3.1 Account (money)

| Concern | Table | Scope | Meaning |
| --- | --- | --- | --- |
| Prepaid balance | `accounts.balance_cents` | one row per organization | Shared funds; settlement deducts from it |
| Postpaid quota | `accounts.monthly_quota_cents` | one row per organization | Monthly cap; `used_this_cycle_cents` accumulates |
| Cross-mode spend cap | `accounts.monthly_spend_limit_cents` | one row per organization | Caps both modes together |
| Ledger | `transactions` | one row per movement | Append-only: recharge, deduction, refund |

All four are organization-level. A member's `GetBalance` reads the same row as
every other member of their tenant: what differs per person is the budget they
are allowed to consume, never the wallet they consume it from.

### 3.2 Quota ladder (limits)

```text
             organizations                 (tenant default)
                    │
                 projects                     (team share)
                    │
    ┌───────────────┴────────────────┐
 user_quotas                    project_members
 (org-wide personal)            (this project, this person)
    └───────────────┬────────────────┘
                    ▼
        effective = minNonZero(each tier, per dimension)
```

| Dimension | `organizations` | `projects` | `user_quotas` | `project_members` |
| --- | --- | --- | --- | --- |
| concurrent requests | ✓ | ✓ | ✓ | ✓ |
| batch jobs per day | ✓ | ✓ | ✓ | ✓ |
| output tokens per request | ✓ | ✓ | ✓ | ✓ |
| monthly spend (cents) | `accounts` | ✓ | ✓ | ✓ |

Resolution rules:

- A missing row reads as unlimited, never as an error.
- An empty project id or user id skips that tier (a key with no project, a key
  with no owner).
- The personal tiers are read only when the scope supplies both an organization
  and a user; the member tier additionally needs the project.
- Batch jobs carry no project, so their create-time check uses the organization
  and personal tiers.

### 3.3 Enforcement points

| Dimension | Enforced at | Mechanism |
| --- | --- | --- |
| Concurrent requests | Data plane | `VerifyAPIKey` returns the effective cap; the Wasm plugin rejects locally |
| Output tokens per request | Data plane | Same verdict field; the plugin clamps `max_tokens` |
| Batch jobs per day | Control plane | `CreateBatchJob` compares the effective daily cap |
| Monthly spend | Control plane (`CheckFunds`) | Each tier's cap against that tier's scoped charge total |

The spend gate checks three scopes, because each cap is about a different
question:

| Tier | Cap read from | Compared against |
| --- | --- | --- |
| Organization | `accounts.monthly_spend_limit_cents` | `accounts.spent_this_cycle_cents` |
| Project | `projects.monthly_spend_limit_cents` | the project's cycle charges |
| Personal (org-wide) | `user_quotas.monthly_spend_limit_cents` | the owner's cycle charges across the tenant |
| Member | `project_members.monthly_spend_limit_cents` | the owner's cycle charges **in that project** |

The member scope is the reason a teammate's usage cannot consume another
member's budget: it is the same owner and the same project, not the project
total.

## 4. Console surfaces

| Page | Route | Who | What it edits |
| --- | --- | --- | --- |
| Organization quotas | `/admin/quotas` | platform administrator | the tenant defaults |
| Project quotas | `/admin/projects` → Quota | org owner/admin, project admin | the project share |
| User quotas | `/admin/user-quotas` | org owner/admin, platform administrator | the org-wide personal budget |
| Project members | project console → Members → Quota | project admin, org owner/admin | the member budget in that project |
| My quotas | `/quotas` | any member | read-only: the org quotas, the personal budget, the per-project budgets and the effective value for each |
| Usage / Balance | `/usage`, `/billing` | any member | read-only: the shared balance, the cycle spend, the per-project spend |

Every editor shows the stored value and, next to it, the effective value:
"stored 40, effective 9" is the console telling the operator that the personal
budget is the binding limit.

## 5. API surface

### tenancy (admin surface unless marked)

| RPC | HTTP | Notes |
| --- | --- | --- |
| `GetOrganizationQuota` / `UpdateOrganizationQuota` | `/api/v1/admin/tenancy/quota` | tenant defaults |
| `GetProjectQuota` / `UpdateProjectQuota` | `/api/v1/admin/tenancy/projects/{id}/quota` | project share, includes spend |
| `ListUserQuotas` / `GetUserQuota` / `UpdateUserQuota` | `/api/v1/admin/tenancy/user-quotas[/{user_id}]` | org-wide personal budget |
| `ListProjectMemberQuotas` | `/api/v1/admin/tenancy/projects/{id}/member-quotas` | roster with each member budget and its effective value |
| `UpdateProjectMemberQuota` | `PATCH /api/v1/admin/tenancy/projects/{id}/member-quotas/{user_id}` | member budget; omitted fields unchanged, `0` = unlimited |
| `GetMyQuota` | `GET /api/v1/quotas` | user surface: org quotas, personal budget, `project_member_quotas[]` with per-project effective values |

### billing

| RPC | HTTP | Notes |
| --- | --- | --- |
| `GetBalance` | `GET /api/v1/billing/balance` | the shared account of the caller's tenant |
| `CheckFunds` | internal | returns the effective spend cap, each tier's spent amount, and the allowed verdict |

## 6. Upgrade path

No SQL migration script is required: the schema is owned by each module's
`Migrate` (GORM `AutoMigrate`), which adds `project_members.monthly_spend_limit_cents`
with `NOT NULL DEFAULT 0` — existing members start unlimited, and the tier is
neutral in the minimum until an administrator sets a value.

The retired fold that copied member budgets into `user_quotas` is removed. Rows
it already wrote stay: they are valid org-wide personal budgets, and the two
tiers now combine instead of one replacing the other.

## 7. Validation

| Rule | Failure |
| --- | --- |
| A quota value is `>= 0` and inside the documented range | `13305` quota invalid |
| The user in a personal or member budget belongs to the organization / project | `10003` user not found |
| A member budget is set only for an existing membership | `10003` user not found |
| Editing a quota needs the matching role (project admin for a member budget, org owner/admin or platform administrator above it) | `10036` forbidden |
| The organization exists | `10005` organization not found |

## 8. Sequence: a member's key is verified

```mermaid
sequenceDiagram
    participant K as API key
    participant G as Gateway plugin
    participant A as auth
    participant T as tenancy
    G->>A: verify(digest, model)
    A->>T: EffectiveQuota(org, project, owner)
    T->>T: min(org, project, user_quotas, project_members)
    T-->>A: effective tuple
    A-->>G: verdict + caps
    G->>G: reject if concurrent cap reached, clamp max_tokens
```

## 9. Open questions

- **Should the member tier replace the org-wide personal tier once every key is
  project-bound?** Not today: a tenant can run keys outside any project, and the
  org-wide budget is the only personal cap those keys have.
- **Should the spend gate reserve (hold) rather than observe?** It compares
  settled charges, so a burst inside one settlement window can overshoot by one
  hour's usage. A hold-based gate is a separate feature (see
  [Balance & Quota](./balance-quota.md) §6.3).
