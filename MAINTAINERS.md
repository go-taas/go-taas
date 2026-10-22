# Maintainers

This file is the authoritative roster of Go TaaS community roles, the area each
person owns, and the repository settings that back the rules in
[GOVERNANCE.md](./GOVERNANCE.md). The governance rules themselves live in
`GOVERNANCE.md`; this file records who currently holds each role.

- Chinese version: [MAINTAINERS.zh-cn.md](./MAINTAINERS.zh-cn.md)
- Role definitions and promotion criteria: [GOVERNANCE.md](./GOVERNANCE.md#4-promotion)
- How to request a role:
  [role request template](./.github/ISSUE_TEMPLATE/role_request.yml)

> **Status: bootstrap mode.** The project has fewer than three Maintainers, so
> the founder and the current Maintainers form an interim Steering Committee
> and promotion confirmation requires a simple majority of active Maintainers
> (see `GOVERNANCE.md` §7). The mode ends automatically when the third
> Maintainer is confirmed.

## 1. Steering Committee (interim)

| GitHub | Name | Since | Scope | Notes |
| --- | --- | --- | --- | --- |
| [@sanmuny](https://github.com/sanmuny) | Sen Wang | 2026-10-22 | Whole organization | Founder; interim chair until the first regular election |

Seats: 1 of 3–5 filled. The first regular election is announced within 30 days
of the third Maintainer being confirmed.

## 2. Maintainers

| GitHub | Name | Since | Area |
| --- | --- | --- | --- |
| [@sanmuny](https://github.com/sanmuny) | Sen Wang | 2026-10-22 | Whole repository (bootstrap) |

## 3. Reviewers

No Reviewers have been confirmed yet. Candidates should read the Reviewer
criteria in `GOVERNANCE.md` §4.2 and open a role request once they have three
merged contributions and five substantive reviews in an area.

## 4. Triagers

No Triagers have been confirmed yet. Triage is the intended first step for new
contributors: it requires no code, only a track record of classifying,
reproducing, or de-duplicating reports.

## 5. Emeritus

No Emeritus members yet. Emeritus entries are permanent and are never removed
from this file.

## 6. Areas and ownership

Areas are the unit of path ownership. Each area maps to a GitHub team and a
`.github/CODEOWNERS` entry. Until a team exists, the bootstrap owner is
responsible for review in that area.

| Area | Paths | GitHub team | Bootstrap owner | Status |
| --- | --- | --- | --- | --- |
| Core services | `/services/` | `@go-taas/area-core` | [@sanmuny](https://github.com/sanmuny) | Team to create |
| Shared packages | `/pkg/` | `@go-taas/area-core` | [@sanmuny](https://github.com/sanmuny) | Team to create |
| Controller | `/internal/` | `@go-taas/area-core` | [@sanmuny](https://github.com/sanmuny) | Team to create |
| API contract | `/proto/` | `@go-taas/area-api` | [@sanmuny](https://github.com/sanmuny) | Team to create |
| Web console | `/web/` | `@go-taas/area-web` | [@sanmuny](https://github.com/sanmuny) | Team to create |
| Deployment & CI | `/deploy/`, `/build/`, `/.github/workflows/` | `@go-taas/maintainers`, `@go-taas/steering` | [@sanmuny](https://github.com/sanmuny) | High-risk; two-person rule |
| Tests | `/test/` | `@go-taas/area-test` | [@sanmuny](https://github.com/sanmuny) | Team to create |
| Documentation & site | `/site/docs/`, root `*.md` | `@go-taas/area-docs` | [@sanmuny](https://github.com/sanmuny) | Team to create |

When an area gains a second stable Maintainer, it becomes a standalone area
team per `GOVERNANCE.md` §12.

## 7. Teams to create

The organization currently has no teams. Create these before relying on
`CODEOWNERS`:

| Team | Members | Repository permission | Purpose |
| --- | --- | --- | --- |
| `maintainers` | All Maintainers | Maintain | Default `CODEOWNERS` owner; merges in owned areas |
| `steering` | Steering Committee | Admin | Organization settings, releases, high-risk approvals |
| `area-core` | Maintainers of `services/`, `pkg/`, `internal/` | Write | Core service review |
| `area-api` | Maintainers of `proto/` | Write | API contract review |
| `area-web` | Maintainers of `web/` | Write | Console review |
| `area-test` | Maintainers of `test/` | Write | Test and e2e review |
| `area-docs` | Maintainers of `site/docs/` | Write | Documentation review |
| `triagers` | All Triagers | Triage | Backlog management |

Reviewers are added to the area team for their area. Triagers are added to
`triagers` only.

## 8. Review service levels

These are targets, not contracts. They exist so contributors know what to
expect and so maintainers notice when review is stalling.

| Item | Target | Escalation |
| --- | --- | --- |
| New issue first response | 3 business days | Triager, then a Maintainer |
| New pull request first review | 5 business days | Area Maintainer, then `@go-taas/maintainers` |
| Re-review after changes | 3 business days | Area Maintainer |
| Security report acknowledgement | 2 business days | Steering Committee, privately |
| Release-blocking regression | 1 business day | Release manager, then Steering |

If a review target is missed twice in a row for the same area, the Steering
Committee asks the area owner whether the area needs another Maintainer. This is
a capacity signal, never a penalty.

## 9. Repository settings that back the rules

Apply these in GitHub settings (or with `gh api` after confirmation). They are
tracked here because `GOVERNANCE.md` relies on them.

- [ ] Create the teams in §7 and grant the permissions listed.
- [ ] Enable branch protection on `main`:
  - [ ] Require a pull request before merging.
  - [ ] Require at least 1 approval.
  - [ ] Require review from Code Owners.
  - [ ] Dismiss stale approvals when new commits are pushed.
  - [ ] Require approval of the most recent reviewable push.
  - [ ] Require linear history.
  - [ ] Block force pushes and deletions.
  - [ ] Do not allow bypassing the above settings (including administrators).
- [ ] Require two-factor authentication for the organization.
- [ ] Restrict who can push to `main` to the `maintainers` team.
- [ ] Restrict who can create releases and tags to `maintainers` and `steering`.
- [ ] Limit Actions to the workflows in `.github/workflows/` and require approval
      for first-time contributors.
- [ ] Enable secret scanning and Dependabot alerts.
- [ ] Confirm Discussions categories match the areas in §6.
- [ ] Keep the `good first issue` and `help wanted` labels available for
      newcomer routing.

## 10. Changing this file

`MAINTAINERS.md` is a high-risk path under `GOVERNANCE.md` §6: changes need two
approvals, one of which is not the author. A promotion also updates the roster
and the GitHub team membership in the same change set.
