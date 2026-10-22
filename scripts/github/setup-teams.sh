#!/usr/bin/env bash
#
# Manages the GitHub organization teams, repository permissions, and the
# default-branch ruleset that back GOVERNANCE.md and MAINTAINERS.md.
#
# Nothing is changed unless the command says so: `plan` (the default) only
# prints the API calls, and `verify` only reads.
#
# Usage:
#   scripts/github/setup-teams.sh plan
#   scripts/github/setup-teams.sh apply
#   scripts/github/setup-teams.sh add-member    <team> <username> [maintainer]
#   scripts/github/setup-teams.sh remove-member <team> <username>
#   scripts/github/setup-teams.sh protect [--enforce]
#   scripts/github/setup-teams.sh verify
#
# Environment:
#   ORG     organization login                (default: go-taas)
#   REPO    repository name                   (default: go-taas)
#   BRANCH  protected branch                  (default: main)
#   RULESET name of the branch ruleset        (default: main-branch-protection)
#
# Requirements: gh (authenticated as an organization owner) with the
# `admin:org` and `repo` scopes. Refresh with:
#   gh auth refresh -s admin:org,repo
#
# See GITHUB-TEAMS.md for the rationale, the UI equivalent of every step, and
# the day-2 operations (promotion, retirement, offboarding).

set -euo pipefail

ORG="${ORG:-go-taas}"
REPO="${REPO:-go-taas}"
BRANCH="${BRANCH:-main}"
RULESET="${RULESET:-main-branch-protection}"

# slug|display name|repository permission|description
#
# Permissions are GitHub repository roles, not community roles:
#   triage   - Triagers (labels, assignments, closing issues)
#   push     - Reviewers and area Maintainers (approve and merge in the area)
#   maintain - Maintainers (repository management without sensitive settings)
#   admin    - Steering Committee (settings, teams, secrets, releases)
#
# Teams are intentionally flat. Nesting an area team under `maintainers` would
# make every area member inherit the parent's `maintain` permission, which is
# exactly the repository-wide grant the area model exists to avoid.
TEAMS=(
  "steering|Steering Committee|admin|Final appeals, governance amendments, high-risk repository settings"
  "maintainers|Maintainers|maintain|Default CODEOWNERS owner; merges in owned areas; release management"
  "area-core|Area: Core Services|push|Owns services/, pkg/, and internal/"
  "area-api|Area: API Contract|push|Owns proto/"
  "area-web|Area: Web Console|push|Owns web/"
  "area-test|Area: Tests|push|Owns test/"
  "area-docs|Area: Docs and Site|push|Owns site/ and the root markdown files"
  "triagers|Triagers|triage|Backlog management; no write access"
)

info() { printf '%s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

require_gh() {
  command -v gh >/dev/null 2>&1 || die "gh is not installed: https://cli.github.com"
  gh auth status >/dev/null 2>&1 || die "gh is not authenticated. Run: gh auth login"
}

team_exists() {
  gh api "orgs/${ORG}/teams/$1" --silent >/dev/null 2>&1
}

# Prints the permission the team has on the repository, or an empty string.
team_repo_permission() {
  gh api "orgs/${ORG}/teams/$1/repos/${ORG}/${REPO}" --jq '.permission' 2>/dev/null || true
}

perm_rank() {
  case "$1" in
    admin) echo 5 ;;
    maintain) echo 4 ;;
    push) echo 3 ;;
    triage) echo 2 ;;
    pull) echo 1 ;;
    *) echo 0 ;;
  esac
}

cmd_plan() {
  info "Organization: ${ORG}    Repository: ${ORG}/${REPO}    Branch: ${BRANCH}"
  info ""
  info "Teams (create when absent, then grant the repository permission):"
  for row in "${TEAMS[@]}"; do
    IFS='|' read -r slug name perm desc <<<"${row}"
    if team_exists "${slug}"; then
      current="$(team_repo_permission "${slug}")"
      if [[ "${current}" == "${perm}" ]]; then
        info "  = ${slug} (${name}); already ${perm}"
      else
        info "  ~ ${slug} (${name}); set permission ${current:-<none>} -> ${perm}"
      fi
    else
      info "  + ${slug} (${name}); create and grant ${perm}"
    fi
  done
  info ""
  info "Ruleset '${RULESET}' on ${BRANCH}: run 'protect' to create or update it."
  info "  Default mode is 'evaluate' (observes, does not block). Add --enforce to block."
  info ""
  info "No changes were made. Run 'apply' to create teams and permissions."
}

cmd_apply() {
  info "Applying teams to ${ORG} (repository ${ORG}/${REPO})"
  for row in "${TEAMS[@]}"; do
    IFS='|' read -r slug name perm desc <<<"${row}"
    if team_exists "${slug}"; then
      info "  = team ${slug} exists"
    else
      gh api -X POST "orgs/${ORG}/teams" \
        -f name="${name}" \
        -f description="${desc}" \
        -f privacy=closed \
        -f notification_setting=notifications_enabled \
        --jq '"  + created team " + .slug'
    fi
    # PUT is idempotent: it also corrects a team whose permission drifted.
    gh api -X PUT "orgs/${ORG}/teams/${slug}/repos/${ORG}/${REPO}" \
      -f permission="${perm}" --silent
    info "    permission ${perm}"
  done
  info ""
  info "Teams are ready. Membership is per person:"
  info "  scripts/github/setup-teams.sh add-member triagers <username>"
  info "  scripts/github/setup-teams.sh add-member area-core <username>"
  info "  scripts/github/setup-teams.sh add-member maintainers <username>"
  info "  scripts/github/setup-teams.sh add-member steering <username>"
  info ""
  info "Next: scripts/github/setup-teams.sh protect   (default-branch ruleset)"
}

cmd_add_member() {
  local team="${1:-}" user="${2:-}" role="${3:-member}"
  [[ -n "${team}" && -n "${user}" ]] || die "usage: add-member <team> <username> [maintainer]"
  team_exists "${team}" || die "team ${team} does not exist; run 'apply' first"
  case "${role}" in
    member|maintainer) ;;
    *) die "role must be 'member' or 'maintainer', got '${role}'" ;;
  esac
  gh api -X PUT "orgs/${ORG}/teams/${team}/memberships/${user}" -f role="${role}" --silent
  info "added ${user} to ${team} as team ${role}"
  if [[ "${team}" == "steering" ]]; then
    warn "organization owner is a separate grant; add it in Organization settings if needed"
  fi
}

cmd_remove_member() {
  local team="${1:-}" user="${2:-}"
  [[ -n "${team}" && -n "${user}" ]] || die "usage: remove-member <team> <username>"
  # A removal from one team does not remove a direct collaborator grant.
  # Check Settings -> Collaborators and teams after an offboarding.
  gh api -X DELETE "orgs/${ORG}/teams/${team}/memberships/${user}" --silent
  info "removed ${user} from ${team}"
}

ruleset_payload() {
  local enforcement="$1"
  cat <<JSON
{
  "name": "${RULESET}",
  "target": "branch",
  "enforcement": "${enforcement}",
  "conditions": {
    "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] }
  },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "required_linear_history" },
    {
      "type": "pull_request",
      "parameters": {
        "required_approving_review_count": 1,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": true,
        "require_last_push_approval": true,
        "required_review_thread_resolution": true
      }
    }
  ],
  "bypass_actors": []
}
JSON
}

cmd_protect() {
  local enforcement="evaluate"
  if [[ "${1:-}" == "--enforce" ]]; then
    enforcement="active"
  fi

  local id
  id="$(gh api "repos/${ORG}/${REPO}/rulesets" \
        --jq ".[] | select(.name == \"${RULESET}\") | .id" 2>/dev/null | head -n1)"

  local payload
  payload="$(ruleset_payload "${enforcement}")"

  if [[ -n "${id}" ]]; then
    info "updating ruleset ${RULESET} (${id}) to enforcement=${enforcement}"
    printf '%s' "${payload}" | gh api -X PUT "repos/${ORG}/${REPO}/rulesets/${id}" --input - --silent
  else
    info "creating ruleset ${RULESET} with enforcement=${enforcement}"
    printf '%s' "${payload}" | gh api -X POST "repos/${ORG}/${REPO}/rulesets" --input - --silent
  fi

  if [[ "${enforcement}" == "evaluate" ]]; then
    info ""
    info "The ruleset only observes unless you pass --enforce."
    info "Stay in 'evaluate' while the project has a single Maintainer:"
    info "  approve-your-own-PR is impossible once code owner review is required."
    info "Switch to active after a second Maintainer can review:"
    info "  scripts/github/setup-teams.sh protect --enforce"
  fi
}

cmd_verify() {
  local problems=0
  info "Verifying ${ORG}/${REPO}"
  info ""

  for row in "${TEAMS[@]}"; do
    IFS='|' read -r slug name perm desc <<<"${row}"
    if ! team_exists "${slug}"; then
      warn "missing team ${slug} (${name})"
      problems=$((problems + 1))
      continue
    fi
    local current
    current="$(team_repo_permission "${slug}")"
    if [[ -z "${current}" ]]; then
      warn "team ${slug} has no repository permission (expected ${perm})"
      problems=$((problems + 1))
    elif (( $(perm_rank "${current}") < $(perm_rank "${perm}") )); then
      warn "team ${slug} has ${current}, expected at least ${perm}"
      problems=$((problems + 1))
    else
      info "  ok  ${slug}: ${current}"
    fi
  done

  info ""
  info "CODEOWNERS team references:"
  local owners
  owners="$(grep -oE "@${ORG}/[A-Za-z0-9_.-]+" .github/CODEOWNERS 2>/dev/null | sort -u || true)"
  if [[ -z "${owners}" ]]; then
    warn "no team references found in .github/CODEOWNERS"
    problems=$((problems + 1))
  fi
  for handle in ${owners}; do
    local slug="${handle#@${ORG}/}"
    local current
    if ! team_exists "${slug}"; then
      warn "  ${handle}: team does not exist (CODEOWNERS entry is ignored)"
      problems=$((problems + 1))
      continue
    fi
    current="$(team_repo_permission "${slug}")"
    if (( $(perm_rank "${current}") < $(perm_rank "push") )); then
      warn "  ${handle}: permission ${current:-<none>}; CODEOWNERS needs write or higher"
      problems=$((problems + 1))
    else
      info "  ok  ${handle}: ${current}"
    fi
  done

  info ""
  info "Default-branch ruleset '${RULESET}':"
  local id
  id="$(gh api "repos/${ORG}/${REPO}/rulesets" \
        --jq ".[] | select(.name == \"${RULESET}\") | .id" 2>/dev/null | head -n1)"
  if [[ -z "${id}" ]]; then
    warn "  ruleset not found; run: scripts/github/setup-teams.sh protect"
    problems=$((problems + 1))
  else
    gh api "repos/${ORG}/${REPO}/rulesets/${id}" --jq '
      "  enforcement: " + .enforcement,
      "  code owner review: " + ([.rules[] | select(.type=="pull_request") | .parameters.require_code_owner_review] | tostring),
      "  required approvals: " + ([.rules[] | select(.type=="pull_request") | .parameters.required_approving_review_count] | tostring),
      "  bypass actors: " + (.bypass_actors | length | tostring)'
  fi

  info ""
  info "Organization state:"
  gh api "orgs/${ORG}" --jq '
    "  default repository permission: " + .default_repository_permission,
    "  two-factor required: " + (.two_factor_requirement_enabled | tostring)'

  info ""
  if (( problems > 0 )); then
    warn "${problems} problem(s) found"
    return 1
  fi
  info "all checks passed"
}

main() {
  local cmd="${1:-plan}"
  case "${cmd}" in
    plan)          require_gh; cmd_plan ;;
    apply)         require_gh; cmd_apply ;;
    add-member)    require_gh; shift; cmd_add_member "$@" ;;
    remove-member) require_gh; shift; cmd_remove_member "$@" ;;
    protect)       require_gh; shift; cmd_protect "$@" ;;
    verify)        require_gh; cmd_verify ;;
    -h|--help|help) sed -n '2,40p' "$0" ;;
    *) die "unknown command '${cmd}'; run with --help" ;;
  esac
}

main "$@"
