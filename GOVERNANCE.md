# Go TaaS Governance

This document defines how Go TaaS grants community roles, what each role may do,
how decisions are made, and how the rules can be changed. It is normative: when
this document and an informal practice disagree, this document wins.

- Chinese version: [GOVERNANCE.zh-cn.md](./GOVERNANCE.zh-cn.md)
- Current role holders: [MAINTAINERS.md](./MAINTAINERS.md)
- Contribution workflow: [CONTRIBUTING.md](./CONTRIBUTING.md)
- Behavioural standards: [CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md)

> **Scope.** This document governs the open-source project — the GitHub
> organization, repository permissions, review, releases, and community roles.
> It does **not** describe the product's tenant RBAC (`owner`, `admin`,
> `member`, `viewer`), which is part of the Go TaaS platform and is documented
> in [Organization Members, Roles & Invitations](./site/docs/design/org-members-rbac.md).

## 1. Principles

1. **Merit, evidenced.** Authority is earned through visible contributions and
   the judgement of people who already carry responsibility. Volume alone is
   never merit.
2. **Least privilege, granted in steps.** Every role maps to a GitHub permission
   that is no broader than the role needs.
3. **Two channels.** Code and community work (triage, documentation, design,
   testing, advocacy) carry equal weight in promotion.
4. **Path ownership.** Responsibility is scoped to areas, not handed out
   repository-wide, whenever that is practical.
5. **Default to openness.** Discussions, evidence, and votes happen in public
   unless a Code of Conduct case or a security report requires confidentiality.
6. **Human accountability.** A person, not a tool, is responsible for every
   merged change.

## 2. Roles

Roles are a closed set. There is no free-text role.

| Role | GitHub permission | Scope |
| --- | --- | --- |
| Contributor | Read | Whole project |
| Triager | Triage | Whole repository, or a named area |
| Reviewer | Write + area team | A named area |
| Maintainer | Maintain + `CODEOWNERS` | A named area, or the whole repository |
| Steering Committee | Admin (shared) | Whole organization |
| Emeritus | None beyond public read | Honour only |

### 2.1 Contributor

Anyone who participates: opening issues, sending pull requests, joining
discussions, reviewing as a community member, reporting bugs, improving
documentation, or answering questions. No application is required.

**May:** do everything a public GitHub user can do on a public repository.
**May not:** label, assign, approve, merge, or change repository settings.

### 2.2 Triager

Triagers keep the backlog usable. This role exists so that a newcomer can earn
trust without writing code.

**May:**

- apply and remove labels, set milestones and assignees;
- close duplicates, stale issues, and questions answered elsewhere;
- reproduce and classify reports, and request missing information;
- moderate Discussions (categories, pins, locks) and route reports to the right
  area;
- be assigned issues and pull requests.

**May not:**

- approve or merge pull requests;
- change repository settings, teams, or workflows.

### 2.3 Reviewer

Reviewers are the quality gate for an area.

**May:**

- approve pull requests in the owned area, or request changes;
- be a required reviewer through `CODEOWNERS`;
- nominate candidates for Triager and Reviewer;
- participate in design review and be consulted on governance votes.

**May not:**

- merge alone on a high-risk path (see §6);
- publish releases, change branch protection, or manage secrets.

### 2.4 Maintainer

Maintainers carry the responsibility for an area: its direction, its quality,
and its releases.

**May:**

- merge pull requests in the owned area (subject to §6);
- own the area roadmap, milestones, and labels;
- nominate candidates for Triager, Reviewer, and Maintainer;
- vote on governance matters and promotions;
- serve as release manager when appointed;
- propose new areas and area teams.

**May not:** change secrets, branch protection, `GOVERNANCE.md`,
`CODEOWNERS`, or the license without a second reviewer (§6).

### 2.5 Steering Committee

The Steering Committee is the final appeal body and the custodian of this
document. It has three to five seats, and at most two members may share an
employer.

**Responsibilities:**

- resolve deadlocks that Maintainer consensus cannot settle;
- review and approve amendments to this document;
- escalate Code of Conduct cases together with the moderation group;
- hold organization-level settings: teams, repository settings, secrets,
  branch protection, and integrations;
- act on release and security escalations.

**Constraints:**

- may not bypass the Code of Conduct process;
- may not amend this document unilaterally (§8);
- must recuse from any matter involving their own or their employer's interest.

### 2.6 Emeritus

A former role holder who has stepped back. Emeritus status is an honour, not a
demotion: it keeps the title, the roster entry, and the credit, and releases the
permissions.

- Granted after six consecutive months without substantive contribution, or on
  request.
- Confirmed by a Maintainer; recorded in `MAINTAINERS.md`.
- Restoration is available on request and confirmed by Maintainer consensus.
- A person removed for a Code of Conduct violation is not eligible for Emeritus
  status.

## 3. Permission Matrix

| Action | Contributor | Triager | Reviewer | Maintainer | Steering |
| --- | --- | --- | --- | --- | --- |
| Open issues, pull requests, comments | ✅ | ✅ | ✅ | ✅ | ✅ |
| Label, assign, close duplicates | ❌ | ✅ | ✅ | ✅ | ✅ |
| Moderate Discussions | ❌ | ✅ | ✅ | ✅ | ✅ |
| Approve a pull request in the owned area | ❌ | ❌ | ✅ | ✅ | ✅ |
| Merge a pull request in the owned area | ❌ | ❌ | ❌ | ✅ | ✅ |
| Merge a cross-area or high-risk pull request | ❌ | ❌ | ❌ | Two approvals, one non-author Maintainer | ✅ |
| Create a release or tag | ❌ | ❌ | ❌ | ✅ (release manager) | ✅ |
| Change CI/CD or release workflows | ❌ | ❌ | ❌ | Proposal + one other Maintainer | ✅ |
| Change `GOVERNANCE.md`, `CODEOWNERS`, branch protection | ❌ | ❌ | ❌ | Proposal + public comment period | ✅ (two-thirds vote) |
| Manage teams, repository settings, secrets | ❌ | ❌ | ❌ | ❌ | ✅ (two-person rule) |
| Nominate a promotion | ❌ | ❌ | ✅ | ✅ | ✅ |
| Governance vote | ❌ | ❌ | Consulted | ✅ | ✅ |
| Handle a Code of Conduct report | Report only | Report only | Report only | Report / recuse | ✅ (with moderation group) |

## 4. Promotion

### 4.1 Process

1. **Nomination.** A person holding the target role or higher opens a role
   request issue using the
   [role request template](./.github/ISSUE_TEMPLATE/role_request.yml), naming
   the candidate, the role, the area, and the evidence.
2. **Evidence.** The request links to the specific contributions that satisfy
   §4.2: pull requests, reviews, triage records, documents, designs, or test
   work. Self-nomination is allowed if a sponsor co-signs.
3. **Comment window.** The request stays open for at least seven days so any
   community member can raise a concern publicly.
4. **Confirmation.** Maintainers confirm by consensus; if consensus fails, by a
   simple majority vote of active Maintainers. A sustained objection on conduct
   or quality grounds blocks the promotion until it is resolved.
5. **Grant.** A Steering member (or, in bootstrap mode, an interim Steering
   member) applies the GitHub permission or team membership, updates
   `MAINTAINERS.md`, and announces the promotion in Discussions.

### 4.2 Criteria

**Triager** — at least 10 issues or pull requests classified, reproduced, or
de-duplicated; one Maintainer nomination; a short hand-over note describing the
area and its labels. No code contribution required.

**Reviewer** — at least 3 merged pull requests in the area, or equivalent
design, documentation, or test contributions; at least 5 substantive reviews;
two nominations from Reviewers or Maintainers; Maintainer consensus.

**Maintainer** — a sustained record as a Reviewer (at least 10 substantive
reviews or merges); demonstrated judgement on the area's architecture, releases,
and compatibility; two Maintainer nominations; a simple majority of active
Maintainers; Steering confirmation that no governance or conduct blocker exists.

**Steering Committee** — at least one term as a Maintainer; nominated by
Maintainers or Reviewers and elected by Maintainers and Reviewers; three to five
seats with at most two per employer.

Criteria may be satisfied by equivalent work: a documentation lead who has
reviewed 10 documentation pull requests meets the Reviewer bar the same way a
code reviewer does.

### 4.3 Volunteer work is not an obligation

A role is a grant of trust, not a contract. Anyone may step back at any time
without explanation, and is encouraged to move to Emeritus rather than
disappear silently.

## 5. Decision-Making

### 5.1 Default: lazy consensus

Day-to-day decisions — merging a pull request, accepting a design, closing an
issue — proceed by lazy consensus. A decision stands unless someone objects with
a reason. Significant RFCs and governance changes carry a public comment window
of at least 72 hours before they are considered accepted.

### 5.2 When a vote is required

- promotion, restoration, or removal of a role holder;
- amendments to this document;
- blocking a release for a technical or quality reason;
- a deadlock that the area Maintainers cannot resolve.

### 5.3 Quorum and thresholds

| Decision | Body | Threshold |
| --- | --- | --- |
| Promotion to Triager | Maintainers | Consensus; majority if consensus fails |
| Promotion to Reviewer | Maintainers | Consensus; majority if consensus fails |
| Promotion to Maintainer | Active Maintainers | Simple majority |
| Steering election | Maintainers + Reviewers | Plurality, at most two seats per employer |
| Release block | Active Maintainers | Simple majority |
| Governance amendment | Active Maintainers + Steering | Two-thirds in favour |
| Removal for cause | Steering + moderation group | Two-thirds of Steering |

Routine votes require two-thirds of active Maintainers to participate. A vote
that fails quorum is re-announced once; if it fails quorum again, the Steering
Committee decides.

### 5.4 Recusal

Anyone with a personal, employer, sponsorship, or financial interest in the
outcome must abstain and say so. This applies to the Steering Committee as well.

### 5.5 Escalation

Any community member may ask the Steering Committee to review a decision by
opening an issue in the repository and mentioning `@go-taas/steering`. The
Steering Committee may affirm, reverse, or return the decision with guidance. It
cannot bypass the Code of Conduct process, and it does not review the technical
merits of an ordinary pull request unless a process rule was broken.

## 6. Two-Person Rule for High-Risk Operations

The following changes require two approvals, and the second approver must not be
the author:

- creating a release or pushing a version tag;
- changing anything under `.github/workflows/`, `/build/`, or `/deploy/`;
- changing `GOVERNANCE.md`, `MAINTAINERS.md`, `CODEOWNERS`,
  `CODE_OF_CONDUCT.md`, or the license;
- changing branch protection, organization teams, secrets, deploy keys, or
  repository integrations.

The rule is enforced socially in v1 and reflected in `.github/CODEOWNERS`. If a
high-risk change is needed while only one Maintainer is available, the change is
announced in Discussions, left open for at least 24 hours, and reviewed by the
named backup reviewer from `MAINTAINERS.md`.

## 7. Bootstrap Mode

While the project has fewer than three Maintainers, the following adjustments
apply so that the rules are usable on day one:

- the founder and the current Maintainers form an **interim Steering Committee**;
- promotion confirmation requires a **simple majority of active Maintainers**
  instead of the thresholds in §5.3;
- Steering elections are deferred until the third Maintainer is confirmed;
- the two-person rule remains in force, with a named backup reviewer.

The mode ends automatically when the third Maintainer is confirmed. Within 30
days, the interim Steering Committee announces the first regular Steering
election.

## 8. Amendments

1. Open a pull request that changes this document and states the motivation.
2. Keep it open for public comment for at least seven days.
3. Obtain two-thirds approval from active Maintainers and the Steering
   Committee, with the two-person rule of §6 applied.
4. Record the change in the pull request and announce it in Discussions.

Editorial fixes (typos, broken links, formatting) need one Maintainer approval
and are exempt from the comment period.

## 9. Removal and Conflict Resolution

- A role holder may be removed for sustained inactivity (§2.6), repeated
  violations of this document, or a Code of Conduct decision.
- Removal for cause requires a two-thirds vote of the Steering Committee and a
  written reason shared with the person and, where appropriate, the community.
- Conduct cases are handled by the moderation group under the Code of Conduct;
  the Steering Committee only handles the escalation and the access revocation.
- Disagreements about this document are resolved by the Steering Committee
  after public discussion.

## 10. AI-Assisted Contributions

- AI tools may be used to write code, tests, and documentation. The human
  submitter is fully accountable for correctness, licensing, and test coverage.
- Pull requests state which AI tooling was used and what was verified by a
  human.
- AI cannot hold a role, sponsor a nomination, vote, or be listed as a
  maintainer.
- AI-generated work is reviewed against the same standard as human work.
  Maintainers may require additional tests, explanation, or human re-review.

## 11. Recognition

- Every role holder is listed in `MAINTAINERS.md`, with the area and the date
  the role was granted.
- Contributors are credited in release notes, and non-code contributions
  (triage, documentation, design, testing, community support) are credited the
  same way as code.
- Emeritus members keep their entry permanently.
- Recognition is reputation and governance power. The project does not pay
  bounties, and no role implies employment.

## 12. Evolution

When an area has two or more stable Maintainers, it becomes an area team:

1. create the `<area>` team and add it to `CODEOWNERS`;
2. move merge rights for the area from the global Maintainer team to the area
   team;
3. if review volume justifies it, split Reviewer into Reviewer and Approver
   within that area;
4. record the change in `MAINTAINERS.md` and announce it in Discussions.

The role vocabulary in §2 stays unchanged as the project grows.
