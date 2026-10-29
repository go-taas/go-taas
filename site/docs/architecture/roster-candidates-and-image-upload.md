# Roster Pick-Lists and Image Upload — Architecture and Detailed Design

| Attribute | Content |
| --- | --- |
| Feature point | Rosters add people by picking them from the accounts that may still join; the image catalog gains an upload path (a `docker save` archive pushed into Harbor) and loses registration; the invitations page names its organization; the cluster page groups its actions |
| Document scope | The two roster-candidate RPCs and their eligibility rules, the account-directory seam, the multipart upload endpoint, the archive handling and its destination rule, the console interactions, error handling, configuration and acceptance criteria |
| Owning modules | `services/tenancy` (candidate RPCs, membership filtering) · `services/auth` (`ListActiveUsers`) · `services/image` (`UploadImage`) · `pkg/registry` (archive reader, `skopeo copy docker-archive:`) · `pkg/server` (multipart endpoint, upload seam) · `web` (`MembersPage`, `ProjectMembersPage`, `InvitationsPage`, `ClustersPage`, `ImagesPage`, `UploadImageDialog`) |
| Related documents | [Image Management](./image-management.md) (the catalog and warmup) · [Image Import](./image-management.md) (the registry-to-registry flow the upload mirrors) · [StormService Serving Topology](./stormservice-serving-topology.md) (the registry-config projection) |
| Status | Implemented |

---

## 1. Roster pick-lists

A person belongs to at most one organization and one project (requirement 5 of the RBAC increment), so "who may I add" is not a free-text question: it is exactly the accounts that have not joined yet.

| Roster | Candidate set |
| --- | --- |
| organization | active accounts that belong to **no** organization |
| project | active accounts of the project's **owning organization** that belong to **no** project |

Two RPCs return that set:

```proto
rpc ListOrgMemberCandidates(...)     // GET /api/v1/admin/tenancy/organizations/{id}/member-candidates
rpc ListProjectMemberCandidates(...) // GET /api/v1/admin/tenancy/projects/{id}/member-candidates
```

`MemberCandidate { user_id, username, display_name, email }`.

**AD1 — Tenancy filters, auth supplies.** The membership tables (`org_members`, `project_members`) live in the tenancy module; the account rows live in `auth`. The tenancy service therefore takes a `CandidateUserDirectory` seam (`ListActiveUsers`), implemented by `auth.ListActiveUsers`, and does the subtraction itself:

- `AllOrgMemberUserIDs` — every user that belongs to any organization;
- `OrgMemberUserIDs(orgID)` — one organization's roster;
- `AllProjectMemberUserIDs` — every user that belongs to any project.

A nil seam yields an empty list rather than a wrong one, so a partially wired build cannot leak accounts.

**AD2 — Same gate as the roster.** `ListOrgMemberCandidates` uses the roster-admin gate (`requireRosterAdmin`, which a platform administrator also satisfies), and `ListProjectMemberCandidates` uses the project-admin gate (project admin, or admin/owner of the owning organization). An unauthorized caller gets 10007/10036 as usual, never a candidate list.

**AD3 — The consoles offer a pick-list.** The members dialog and the project roster render a `<select>` of candidates labelled by display name (username in parentheses when it differs, `web/src/candidates.ts`) with a hint explaining the eligibility rule. Empty lists say so (`members.noCandidates`, `projectMembers.noCandidates`) instead of showing an empty dropdown.

**AD4 — One action per dialog.** The members dialog previously repeated the page's "add member" label on its submit button; it now has a normal `取消 / 添加` action row, and the submit stays disabled until an account is chosen.

## 2. Image upload

### 2.1 Transport

The API is gRPC-JSON transcoded, and transcoding cannot carry a binary body: a `docker save` archive is hundreds of megabytes and base64 would triple it. The upload is a plain streaming endpoint:

```
POST /api/v1/admin/images:upload      multipart/form-data
  file              the archive (.tar, .tar.gz, .tgz)
  name, tag         optional; the archive (or the reference) supplies them
  sourceReference   optional; the archive's image reference as the operator typed it
  accelerator, engine, description
```

It is mounted on the gateway mux with `HandlePath`, so the realm guard, the console handler and the admin prefix treat it like any other admin call.

**AD5 — Stream, never buffer.** `pkg/server` reads the body with `r.MultipartReader()`, copies the file part straight to a temp file and drops the form fields into a map (1 MiB each). Nothing is parsed into memory and the archive is never extracted, so a crafted archive cannot write outside that one file. `http.MaxBytesReader` bounds the request at `image.upload.maxArchiveBytes` (default 8 GiB) before a byte is read.

**AD6 — Same identity rules as gRPC.** The handler forwards `Authorization` and `X-Organization-Id` into the incoming gRPC metadata and parks the session-role resolver on the context, exactly as the unary interceptor does, then calls the image service in-process. The platform-administrator gate therefore sees the same caller it would for a transcoded call. The actor for the audit trail is resolved by the wiring adapter (`auth.SessionUserID`).

**AD7 — One destination rule.** An upload lands where a registry import lands: `image.harbor.hostname` + `image.harbor.project` + the image's **own** name. The source registry and its namespaces are dropped, so `docker.io/library/a/b/hello:1.0.0` becomes `<harbor>/<project>/hello:1.0.0`.

**AD8 — The archive names the image; the form is the fallback.** `pkg/registry.ReadArchiveReference` reads `manifest.json` from the (optionally gzipped) tar and returns the first `RepoTags` entry. Two different uses must not be confused:

- **selector** — only a reference the archive carries can select a member inside it (`docker-archive:<path>:<reference>`); an untagged archive is copied wholesale and named by the destination;
- **name/tag** — the archive's reference, then the form's `sourceReference`, then the explicit `name`/`tag` fields.

Passing a form reference as a selector fails with `Tag "…" not found` — the reason this distinction is explicit in the code and covered by a test.

**AD9 — Registration is gone from the console.** The image page offers *Import* (from another registry) and *Upload* (from an archive). The `RegisterImage` RPC stays as the catalog's low-level API (FVT and CLI use it); no console entry point remains, and its dead locale keys were removed.

## 3. Console details

- **Upload dialog** (`components/UploadImageDialog.tsx`): the archive shard shows the resolved destination (`<host>/<project>/<name>`), then accelerator (fleet vendors + CPU), engine, description. The transfer uses `XMLHttpRequest` because it is the only browser API with upload progress; a `progress-fill` bar reports bytes sent, and once the bytes are in it switches to "pushing into Harbor" while the server runs `skopeo copy`. Aborting the dialog aborts the request.
- **Invitations** names the organization by display name (`organizationLabel`, falling back to the console scope's name) instead of printing its UUID.
- **Clusters** groups *discover / refresh / register* into one `.toolbar`, because the page header distributes its direct children with `space-between` — three sibling buttons were pushed to opposite edges.

## 3b. Joining is a move

The roster pick-lists assume the invariant "an account belongs to at most one organization and one project". It used to be documentation only: `AddOrgMember` inserted the new row and left the old one, and the directory — which resolves the *oldest* membership — kept reporting the organization the user had just left. Moving someone between organizations therefore looked like it did nothing.

`MoveUserToOrg` (`AddOrgMember`) and `MoveUserToProject` (`AddProjectMember`) make it true at write time, in one transaction with the insert that follows:

| Rule | Effect |
| --- | --- |
| other organizations cleared | `DELETE FROM org_members WHERE user_id = ? AND organization_id <> ? AND organization_id <> 'platform'` |
| projects of other organizations cleared | a member of org B cannot hold a project of org A, so those `project_members` rows go too |
| the platform anchor is spared | `platform` is the tenancy anchor of the platform, not an affiliation a person joins |
| the target organization's projects are kept | a project inside the organization being joined survives |
| other projects cleared (project add) | an account belongs to at most one project |
| a duplicate join is refused first | adding someone who is already in that organization answers 10029 *before* anything is deleted, so a request that returns an error has not moved the account in the meantime |

**An owner is not movable.** An organization must keep an owner, and the roster already refuses to remove one (10032). The move refuses the same way instead of finding a loophole through the platform directory: an owner must hand ownership over first.

The console reflects this: the users page's dialog opens on the account's current organization, shows a warning naming the organization it will leave (and that its projects there go with it), and — when the organization does not change — sends a role update (`PATCH`) instead of a duplicate "add" that would answer 10029.

## 4. Errors

| Code | Name | When |
| --- | --- | --- |
| 10207 | `IMAGE_REFERENCE_INVALID` | no file, unparsable archive, undecidable name, Harbor not configured, archive import not configured, `skopeo` failure |
| 10202 | `IMAGE_EXISTS` | the (name, tag, accelerator) triple is already registered |
| 10203 | `IMAGE_INCOMPATIBLE` | accelerator is not a known vendor or `cpu` |
| 10007 / 10036 | role errors | caller may not administer the roster |
| 10006 | `PROJECT_NOT_FOUND` | project candidate list for a missing project |
| 10029 | `MEMBER_EXISTS` | adding an account that is already in that organization (the console sends a role update instead) |
| 10032 | `OWNER_PROTECTED` | moving an account out of an organization it owns |

## 5. Configuration

```yaml
image:
  harbor:
    hostname: hub.example.com   # empty disables import and upload
    project: taas
  upload:
    maxArchiveBytes: 8589934592 # default 8 GiB
    tempDir: ""                 # OS temp directory
```

The chart exposes `harbor.uploadMaxArchiveBytes` (0 uses the default). The console route's request timeout must exceed the transfer time for large archives; the deployed route uses 900 s.

## 6. Acceptance criteria

- **AC1** `GET …/organizations/{id}/member-candidates` returns exactly the active accounts with no organization; a caller who is not a roster admin is refused.
- **AC2** `GET …/projects/{id}/member-candidates` returns the owning organization's members with no project, and nothing else; an unknown project answers 10006.
- **AC3** The members dialog offers the candidates in a dropdown, disables the confirm button until one is chosen, and shows a single cancel/confirm action row.
- **AC4** The project roster offers its organization's eligible members in a dropdown.
- **AC5** Uploading a `docker save` archive pushes it to `<harbor>/<project>/<name>:<tag>` and registers that reference in the catalog; the console reports upload progress and then the push.
- **AC6** An untagged archive uploads correctly (nothing selects a member by the form reference).
- **AC7** `image.upload.maxArchiveBytes` bounds the request; the chart exposes it.
- **AC8** The image page has no register action; the invitations page names the organization; the cluster actions are one group.
- **AC9** Adding a user to a second organization removes the first membership and any project membership held in the organization they left; the directory then reports the new organization.
- **AC10** A project of the organization being joined is kept across the move.
- **AC11** Moving an owner is refused with 10032 and nothing changes.

## 7. Verification

Verified on the target cluster: the candidate endpoints answer with and without eligible accounts (a temporary account was inserted and removed), the members dialog rendered its pick-list and action row, the invitations page showed the organization name, the cluster buttons were grouped, and an archive built with `skopeo copy … docker-archive:` (24 MB, no `RepoTags`) was uploaded through the browser and through `curl`: Harbor gained `<project>/curl-ui:1` and `<project>/curl:latest` and the catalog listed both. The test rows and Harbor repositories were then deleted.
