# Contributing to Go TaaS

Thanks for your interest in contributing! This document covers the development setup, coding conventions, and the process for submitting issues and pull requests.

## Table of Contents

- [Getting Started](#getting-started)
- [Development Workflow](#development-workflow)
- [Commit Conventions](#commit-conventions)
- [Pull Request Guidelines](#pull-request-guidelines)
- [Issue Reporting](#issue-reporting)
- [Design Changes](#design-changes)
- [Community Roles and Growth](#community-roles-and-growth)
- [Reviewing Pull Requests](#reviewing-pull-requests)
- [AI-Assisted Contributions](#ai-assisted-contributions)
- [License](#license)

## Getting Started

### Prerequisites

- Go 1.25+
- golangci-lint v2
- [buf](https://buf.build/) (for Protobuf code generation)
- Git

### Build from Source

```bash
git clone https://github.com/go-taas/go-taas.git
cd go-taas

make pbgen   # generate Protobuf code (gRPC stubs + grpc-gateway + OpenAPI)
make deps    # update dependencies
make lint    # static checks
make ut      # unit tests
make build   # build the binary
```

## Development Workflow

1. Fork the repository and create a branch from `main`:

   ```bash
   git checkout -b feat/my-feature
   ```

2. Make your changes. Keep commits small and focused; each commit should pass `make lint` and `make ut`.

3. Before opening a pull request, make sure the full check suite passes:

   ```bash
   make lint
   make ut
   ```

4. Push your branch and open a pull request against `main`.

### Code Style

- All code must pass `golangci-lint v2` checks; fix linter findings rather than suppressing them.
- Follow standard Go conventions ([Effective Go](https://go.dev/doc/effective_go), `gofmt`/`goimports` formatting).
- New code requires unit tests. Critical modules (billing, protocol conversion) must cover edge cases; the project targets at least 80% unit test coverage.
- Protobuf contracts are the source of truth for APIs: change the `.proto` files first, then regenerate with `make pbgen`. Generated code (`*.pb.go`, `*.pb.gw.go`, `site/docs/api/`) is **not** committed — only `*.proto` files are tracked.

### Documentation

- User-facing and design documentation is bilingual: English documents live under `site/docs/`, Chinese versions use the `.zh-cn.md` suffix. When you change terminology or architecture diagrams, update both sides, including Mermaid node labels.

## Commit Conventions

Commits must follow [Conventional Commits](https://www.conventionalcommits.org/), enforced by commitlint:

```
<type>(<optional scope>): <subject>

<optional body>
<optional footer>
```

Common types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `perf`.

Example:

```
feat(infer): publish deployment changes to the message queue
```

## Pull Request Guidelines

- One logical change per PR. Avoid mixing refactors with feature work.
- Fill in the pull request template, including the motivation and a summary of the change.
- New features and bug fixes should come with tests that demonstrate the fix or behavior.
- If the PR affects the architecture or public APIs, link the relevant design document and describe the impact.
- Direct pushes to `main` are not allowed; all changes go through pull request review.

## Issue Reporting

- **Bug reports**: use the bug report template and include reproduction steps, expected behavior, and actual behavior.
- **Feature requests**: use the feature request template and describe the use case, not just the solution.
- Search existing issues before opening a new one; add a comment on the existing issue if you hit the same problem.

## Design Changes

Go TaaS is in the design and early-development stage, so design-level changes (new modules, changed responsibility boundaries, protocol changes) should start with an issue or discussion before implementation. Large changes are much easier to review as a design document than as a large code diff.

## Community Roles and Growth

Contribution is not only code. Triage, documentation, design, testing, and
answering questions all count, and all of them can lead to a role. The rules are
in [GOVERNANCE.md](./GOVERNANCE.md); the current holders are in
[MAINTAINERS.md](./MAINTAINERS.md).

| Role | What it grants | How to get there |
| --- | --- | --- |
| Contributor | Participation | Open an issue or pull request — no application |
| Triager | Triage permission: labels, assignments, closing duplicates, Discussions moderation | Classify or reproduce 10 reports, one Maintainer nomination, a short hand-over note |
| Reviewer | Write access in one area, approves pull requests there | 3 merged contributions in the area, 5 substantive reviews, two nominations |
| Maintainer | Merge rights in the area, roadmap, release manager, governance vote | Sustained Reviewer record, two Maintainer nominations, majority confirmation |
| Steering Committee | Final appeals, governance amendments, high-risk settings | Elected by Maintainers and Reviewers from sitting Maintainers |

Start with a `good first issue` if you want a bounded task; comment on it and a
maintainer will assign it and answer questions. If you are unsure where to
begin, open a discussion and ask.

Recognition is reputation and governance power, not payment: role holders are
listed in `MAINTAINERS.md`, contributors are credited in release notes, and
non-code work is credited the same way as code. Emeritus status keeps the credit
after someone steps back.

## Reviewing Pull Requests

Reviews are the main way to earn trust, and a review is more than an approval.
When reviewing:

- check behaviour, error paths, and tests — not only style, which the linters cover;
- say what you verified and what you did not;
- distinguish blocking comments from suggestions;
- for changes under `.github/workflows/`, `/deploy/`, `/build/`, or the
  governance files, remember the two-person rule in `GOVERNANCE.md` §6.

## AI-Assisted Contributions

You may use AI tools to write code, tests, or documentation. You remain fully
accountable for the result: correctness, licensing, and test coverage are yours.
In the pull request, state which tooling was used and what you verified by hand.
AI cannot hold a community role, sponsor a nomination, or vote, and AI-authored
work is reviewed to the same standard as any other contribution.

## License

By contributing to Go TaaS, you agree that your contributions will be licensed under the [Apache License 2.0](./LICENSE).
