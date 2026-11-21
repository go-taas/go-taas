# Multi-Backend Model Storage, Asynchronous Transfer Jobs and Model Warmup

| Attribute | Content |
| --- | --- |
| Feature point | Pluggable model-weight storage, asynchronous transfer/extraction Jobs, and node-local model warmup scheduling |
| Document scope | Storage abstraction, delivery modes, stateless transfer jobs, warmup index and scheduling, APIs, data model, Helm configuration, console pages, failure handling and acceptance criteria |
| Owning modules | `model` (catalog, downloads, uploads, transfers, cache rows), `controller` (Kubernetes Jobs and KServe cache resources), `storage` / `transfer` / `warmup` packages |
| Related documents | [Model Catalog & One-Click Deployment](./model-catalog-deployment.md), [Architecture Design](./architecture.md) |
| Status | Implemented |

---

## 1. Background

The control plane originally assumed one hard-wired storage shape: a single
ReadWriteMany PVC mounted at `weightsDir`, written by an in-process download
goroutine and read by every inference pod. That assumption blocks three
real deployment patterns:

1. **Object storage is the source of truth.** S3/MinIO/OSS/COS buckets are
   cheaper and more durable than a POSIX filesystem, but an inference pod
   cannot `mmap` an object; the bytes must be materialised somewhere.
2. **Multiple storage classes coexist.** A platform may keep a fast
   JuiceFS pool for hot models, an NFS/CephFS archive for cold models, and
   an S3 bucket for imported checkpoints. A model version must name the
   backend it lives on.
3. **Big transfers do not belong in the API server.** Downloading a 100 GiB
   checkpoint or copying between buckets inside the server process makes
   the control plane stateful, memory-hungry and impossible to scale
   horizontally.

The feature therefore splits "where the bytes live" from "how a pod reads
them", moves every large byte movement into a dedicated Kubernetes Job, and
adds a node-local cache so a model that is already warm schedules onto the
nodes that hold it.

## 2. Goals and non-goals

Goals:

- Declare any number of filesystem or object-store backends in Helm.
- Give each catalog version a storage backend and a delivery mode
  (`mount`, `download`, `cache`) without changing the deployment API shape.
- Make hub download, cross-store copy and delete asynchronous, observable,
  cancellable Jobs.
- Keep `taas-server` stateless: no weights mounts required for transfers,
  no transfer goroutines, no Kubernetes client for jobs.
- Build a model → warmed-node index from KServe `LocalModelNode` resources
  and inject `nodeAffinity` into `LLMInferenceService`, `preferred` by
  default and `required` when an operator asks for strict locality.
- Keep backward compatibility: an install that declares no `storage` keeps
  the legacy single-PVC behaviour.

Non-goals:

- Tenant-level storage isolation. Storage backends are platform-wide and
  only platform administrators may register, download, transfer or warm
  models.
- A general-purpose data mover for arbitrary buckets. The job moves model
  directory prefixes between declared backends.
- Replacing KServe's own model cache agent: the platform creates
  `LocalModelNodeGroup`/`LocalModelCache` and consumes `LocalModelNode`
  status; the KServe `localmodelnode` agent performs the node copy.
- Asynchronous local archive upload. The admin upload endpoint keeps its
  bounded, streamed synchronous extract; it now names a target backend and
  either extracts into that backend's filesystem or stages the archive and
  uploads it to the object store. Moving archive extraction to a Job is a
  separate follow-up because it needs a staging volume and a new
  upload-session API.

## 3. Storage model

### 3.1 Backend types

| Type | Fields | Used by |
| --- | --- | --- |
| `filesystem` | `existingClaim`, `provision{enabled,storageClass,accessMode,size}`, `mountPath` | JuiceFS, NFS, CephFS, EFS, Lustre, any RWX/RWO PVC |
| `objectstore` | `provider`, `endpoint`, `region`, `bucket`, `prefix`, `forcePathStyle`, `tls`, `credentials` | S3, MinIO, OSS, COS, GCS S3-interop |

Object-store credentials are never inlined: `credentials.existingSecret`
names a Secret and `accessKeyKey` / `secretKeyKey` name its keys. A transfer
Job projects the Secret into the transfer namespace, while a serving pod
projects it into the inference namespace, so the same Secret must also exist
in `inference.namespace` (the chart does not copy Secret data across
namespaces; an operator or an external secret controller must do it). For the
server-side upload path, the chart projects each object-store backend into
the server as `TAAS_STORAGE_<BACKEND>_ACCESS_KEY` and
`TAAS_STORAGE_<BACKEND>_SECRET_KEY` (`<BACKEND>` upper-cased, every character
outside `A-Z0-9` replaced with `_`). The server prefers those names and falls
back to `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` for a process configured
outside the chart, so two object-store backends never shadow each other.

### 3.2 Delivery modes

`delivery.default` picks the mode a version gets unless the request
overrides it; `delivery.allowed` bounds what a request may choose.

| Mode | How a pod reads weights | Requires |
| --- | --- | --- |
| `mount` | a filesystem backend is mounted at `mountPath`, or an object store is mounted through a CSI driver (`delivery.mount.storageClass`) | a mountable backend |
| `download` | KServe storage-initializer fetches the model URI into the pod | `delivery.download.enabled` and a reachable object store |
| `cache` | the model is copied to node-local storage and the pod binds to a warmed node | `warmup.enabled`, a platform-managed warmup node group, and the KServe local-model agent |

A `filesystem` backend that declares `provision.enabled` gets a chart-managed
PVC; otherwise `existingClaim` must name an existing claim. A backend with
neither is rejected at render time.

The default logical URI for a version is `pvc://<claim>/<weightPath>` for a
filesystem backend and `s3://<bucket>/<prefix>/<weightPath>` for an object
store. `delivery.cache` rewrites the KServe source to the cache URI and
records the source so the cache can be rebuilt after eviction.

**Serving.** The control plane resolves each version's backend and delivery
mode into the change event, and the controller builds the pod accordingly:

- `mount` mounts the backend's claim at its `filesystem.mountPath`; this is
  also the fallback when a delivery cannot be applied.
- `download` adds a `storage-initializer` init container that fetches the
  object-store URI into a shared `emptyDir` at `/models`. The container gets
  the backend's endpoint/region/path-style settings and, when
  `objectStore.credentials.existingSecret` is set, `AWS_ACCESS_KEY_ID` and
  `AWS_SECRET_ACCESS_KEY` from that Secret. Only names are carried in the
  event; no credential value ever leaves the Secret.
- `cache` mounts the warmup node group's `hostPath` read-only and points the
  engine at `<hostPath>/models/<storageKey>`, the path the seed Job and the
  KServe local-model agent populate. The existing warm-aware affinity keeps
  the pod on a warmed node.

A version's `storage_backend` is recorded when it is registered, uploaded or
downloaded, and every one of those requests names the backend explicitly
(empty still means the platform default). The download job row stores the
chosen backend too, so a retried or recovered job lands where it started.
Rows written before this field existed resolve to the default backend. Warmup does not depend on the KServe agent's own object-store
credentials: the controller's seed Job covers both `pvc://` sources and
configured object-store URIs, projecting the backend Secret from the
inference namespace.

## 4. Stateless transfer jobs

### 4.1 Flow

```text
console / API
   |  POST /api/v1/admin/model-transfers
   v
model service ── row: model_transfers(state=queued) ──> MQ model.transfers
   |                                                     |
   |                       controller (taas-controller) <+
   |                              | build Job (spec JSON)
   |                              v
   |                     Job/taas-transfer-<id>  (namespace transfer.namespace)
   |                       | mount filesystem PVCs / project object-store env
   |                       | progress -> ConfigMap taas-transfer-progress-<id>
   |                       v
   |                     MQ model.transfers.status
   v                              |
model service <-------------------+
   | terminal: update row, register version, audit
   v
console polls GET /api/v1/admin/model-transfers/{id}
```

The server writes only the intent row and the catalog registration. The
controller owns the Job lifecycle and publishes observed state. A status
message for a row that is already terminal or no longer exists is ignored,
which makes duplicate delivery safe.

### 4.2 Job spec and idempotency

The spec contains the transfer id, operation, source/target backend names,
resolved URIs and in-container paths, object-store refs, optional hub
download identity, and the logical weight path. Every part is serialized
as `TRANSFER_SPEC_JSON`; the Job name is derived from the transfer id, so
applying the same message twice is a no-op. Credentials are projected from
Secrets as `TRANSFER_SOURCE_*` / `TRANSFER_TARGET_*` environment variables,
never written into the spec.

Operations:

| Operation | Effect |
| --- | --- |
| `copy` | copy a model prefix between two backends; object-to-object copies use temp staging when endpoint/bucket/prefix differ |
| `download` | fetch a hub model into the target backend and register the version on completion |
| `delete` | remove a model prefix from a backend after the catalog row is deleted |
| `cancel` | controller deletes the Job with background propagation; the row moves to `cancelled` |

Progress is a small JSON document (`state`, `bytes`, `totalBytes`,
`message`) written by the Job and read by the controller; progress is
advisory and never the source of truth for success. A Job's terminal
condition, not its last progress write, decides the row state. A hub
download reports the model's hub size before it moves bytes and samples its
target directory while it runs, so progress is a percentage instead of a
single final sample; the model service mirrors each non-terminal report into
the linked `model_downloads` row so the download leaves `queued` and shows
the same byte counters. The Job's TTL cleans up the Job; the progress
ConfigMap is small and can be garbage collected with the same TTL or by an
operator policy.

A Job runs as root so it can read hub caches and uploaded weights owned by
another uid. After a successful filesystem copy or download it hands the
target tree to the server's uid/gid (`65532` by default, overridable with
`TRANSFER_TARGET_UID` / `TRANSFER_TARGET_GID`), so deleting the model from
the catalog can remove its weights without a privileged helper. Object-store
targets have no local tree and are skipped.

### 4.3 Concurrency and failure handling

- `transfer.concurrency` bounds active Jobs; the default 4 keeps a cluster
  from being flooded by simultaneous model pulls.
- The Job has a backoff limit, active deadline and TTL, all configurable
  per release. A deadline breach surfaces as `failed` with a message from
  the Job condition.
- Missing image or service account is a configuration error: the server
  falls back to the legacy inline path only when `transfer.mode=inline` or
  no `taas-transfer` image is configured, so an air-gapped install still
  works.
- An object-store backend whose endpoint/bucket/prefix differs from the
  source uses a temporary staging prefix inside the target store; the
  staged copy is removed after a successful target copy.

## 5. Model warmup and warm-aware scheduling

### 5.1 Resources

| Resource | Owner | Purpose |
| --- | --- | --- |
| `LocalModelNodeGroup` | controller, from the `warmup_node_groups` row | one node pool that can hold cached weights, with a storage limit and a hostPath/PVC volume |
| `LocalModelCache` | controller, from a `WarmModel` request | one model to copy onto the group's nodes |
| `LocalModelNode` | KServe localmodel controller (per-node status maintained by the agent) | per-node status: which models are downloaded |
| `LLMInferenceService` | controller | receives the injected `nodeAffinity` |

Warmup node groups are **not Helm values**. A platform administrator creates,
edits and deletes them in **Operations > Warmup node groups**; the model
service validates and stores them in `warmup_node_groups`, and the controller
watches `model.warmups.groups` to create, update or delete the matching KServe
`LocalModelNodeGroup`. The server also publishes the complete desired set
every 30 seconds; the controller applies every group and removes managed
groups that are no longer in the set, so a restart converges without a
database round-trip and a deletion made while it was down still takes effect.
Deleting a group is rejected while a queued, warming or ready warmup still
references it.

`WarmModel` validates the model version and its resolved source URI, accepts
an optional `node_groups` selection (empty means every managed group),
writes a `model_cache_entries` row in `queued`, and publishes
`model.warmups` with the full group settings. The controller ensures the
requested groups and the cache, then reports state (`queued` → `warming` →
`ready`, or `failed`/`cancelled`) with warmed node names, total nodes and
bytes.

KServe's storage initializer does not accept `pvc://`, and the local-model
agent may not carry the platform's object-store credentials. The controller
therefore pre-populates every cache it can resolve to a configured backend:
on every scan it finds the `LocalModelNode` entries for the cache, resolves
the group's hostPath from the live `LocalModelNodeGroup`, and runs a one-shot
`taas-transfer` Job pinned to that node with that hostPath mounted at
`/target`. The same group hostPath is used when a `cache`-delivery serving pod
mounts the warm copy, so changing a group's host path affects both paths
without a Helm release. A filesystem source mounts the PVC read-only at
`/source`; an object-store source projects the backend's Secret as
`WARMUP_SOURCE_ACCESS_KEY` / `WARMUP_SOURCE_SECRET_KEY` and lets the transfer
binary fetch the prefix. Both copy to `/target/models/<storageKey>`, and only
after the Job succeeds does the controller mark the node's
`status.modelStatus[cacheName]` as `ModelDownloaded`. The storage key is the
first 16 hex characters of SHA-256 of the source URI, matching KServe's own
layout. The seed Job runs as root because uploaded weights can be owned by an
arbitrary uid with mode 0750; it writes world-readable files so non-root
serving runtimes can read them. HTTP sources that no backend owns continue to
use the KServe path.

### 5.2 Scheduling

Before creating or updating an `LLMInferenceService`, the controller lists
`LocalModelNode` resources and builds the model → downloaded-node index.
When the model has warmed nodes it injects a node affinity term on
`kubernetes.io/hostname`:

- `scheduling.mode: preferred` (default) uses
  `preferredDuringSchedulingIgnoredDuringExecution` with
  `scheduling.weight` (1-100). A cold node can still run the pod.
- `scheduling.mode: required` uses
  `requiredDuringSchedulingIgnoredDuringExecution`. The pod stays pending
  until a warmed node is schedulable, which is the right choice for
  latency-critical models but a capacity risk during node drains.
- `scheduling.mode: disabled` (or `warmup.enabled: false`) leaves placement
  to the cluster.

The affinity is merged into any existing affinity rather than replacing
it, so operator-supplied rules survive.

### 5.3 Eviction and automatic warmup

`warmup.eviction` sets the cache retention policy (`lru` by default) and the
per-node free-space floor. Each warmup node group's storage limit is edited
with the group in the console, not in Helm. `warmup.auto` records the
intent to warm a model when it becomes ready or when a service that uses it
is deployed; the operator-facing API remains `WarmModel`, and a deployment
without warmup configured behaves exactly as before.

## 6. Data model

| Table | Key columns | Notes |
| --- | --- | --- |
| `model_transfers` | `id`, `model_id`, `version`, `operation`, `source_backend`, `target_backend`, `source_uri`, `target_uri`, `state`, `message`, `bytes`, `total_bytes`, `job_name`, `download_id` | one row per transfer; `download_id` links an asynchronous download to the download row it completes |
| `model_cache_entries` | `id`, `model_id`, `model_name`, `version`, `backend`, `source_uri`, `weight_path`, `state`, `message`, `size_bytes`, `warmed_nodes`, `node_groups` | one row per `LocalModelCache` |
| `warmup_node_groups` | `name`, `node_selector_json`, `storage_limit`, `host_path`, `storage_class`, timestamps | platform-managed warmup pools; the source of truth for KServe `LocalModelNodeGroup` |
| `warmup_nodes` | `name`, `node_groups_json`, `models_json`, `model_count`, `downloaded_count`, `observed_at` | last full `LocalModelNode` snapshot; replaced atomically, so a deleted node disappears |
| `model_versions` | `storage_backend` | the backend a version lives on; empty means the default backend |
| `model_downloads` | `storage_backend` | the backend a download job writes to; empty means the default backend |

Rows are created by the server, never by the controller. The controller
publishes state only; a reconnect therefore does not need database access
and cannot diverge from the catalog.

## 7. API surface (platform administrator only)

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/admin/storage-backends` | list configured backends with type, delivery modes and capabilities |
| POST | `/api/v1/admin/model-transfers` | queue a copy or delete transfer; hub downloads go through the model downloads API |
| GET | `/api/v1/admin/model-transfers` | list transfers, newest first, paginated |
| GET | `/api/v1/admin/model-transfers/{transfer_id}` | one transfer with progress |
| POST | `/api/v1/admin/model-transfers/{transfer_id}:cancel` | cancel a queued/running transfer |
| POST | `/api/v1/admin/model-warmups` | warm a model version on the requested (or all) warmup node groups |
| GET | `/api/v1/admin/model-warmups` | list warmups as `warmups`, ready first then newest first, paginated |
| GET | `/api/v1/admin/model-cache-entries` | legacy alias of the same list, returned as `caches` |
| POST | `/api/v1/admin/model-warmups/{cache_id}:cancel` | remove a model warmup |
| GET | `/api/v1/admin/model-warmup-node-groups` | list platform-managed warmup node groups with ready/total node counts |
| POST | `/api/v1/admin/model-warmup-node-groups` | create a node group and ask the controller to materialize it |
| PUT | `/api/v1/admin/model-warmup-node-groups/{name}` | edit a node group; the name is immutable |
| DELETE | `/api/v1/admin/model-warmup-node-groups/{name}` | delete a node group once no live warmup uses it |
| GET | `/api/v1/admin/model-warmup-nodes` | list the last `LocalModelNode` observation per node |

`StartModelDownload`, `RegisterModel` and the multipart upload endpoint each
accept an optional `storage_backend`; the list and detail projections return
the latest version's backend so the console can show where the weights live.
With a transfer image configured a download returns immediately and the
transfer row carries the same `download_id`, so a client that polls the
download endpoint sees the state transition instead of a long HTTP request.

## 8. Console

The platform-administrator console groups the surfaces by operator task:

- **Operations > Storage backends** — read-only table of configured
  backends (type, endpoint/claim, mount path, delivery modes and whether
  node cache is allowed). Above the table, designed guide cards explain
  `mount`, `download` and `cache`, plus a callout explaining that node cache
  requires both a cache-capable backend and a warmup node group.
- **Operations > Warmup node groups** — creates, edits and deletes the
  platform-managed node groups (selector, storage limit, host path, storage
  class) and shows ready/total node counts. Guide cards state that groups
  are runtime configuration rather than Helm values.
- **Models** — one page with three tabs: **Model catalog**, **Warmup** and
  **Transfers**. Each tab indents its content from the tab strip and keeps
  its actions on the right. A catalog row's warm action switches to the
  warmup tab and opens the warm dialog with that model selected. The catalog
  keeps register/upload/download/deploy actions, and every import form names a
  storage backend (default first);
  the catalog table and the detail page show the backend of the latest
  version. Warmup owns the previous model-cache list and a new **Nodes** tab
  that shows each `LocalModelNode`, its groups and its model copies.
  Transfers owns the asynchronous copy/download/delete list and cancel
  action. A job's failure reason is a warning icon after its status with the
  message in a hover/focus tooltip, so a long error does not stretch the
  row.
  The old `/admin/model-transfers` and `/admin/model-cache-entries` routes
  redirect into the matching tab so bookmarks keep working.

All views follow the shared console table, empty-state, error and pagination
conventions (`PAGE_SIZE = 20`), and all copy is parameterized from API data
rather than hard-coded sample values.

## 9. Helm configuration

The chart exposes three top-level blocks and keeps the legacy
`controller.config.controller.weights.*` path working:

```yaml
storage:
  defaultBackend: shared
  backends:
    shared:
      type: filesystem
      filesystem:
        existingClaim: model-weights
        mountPath: /data/weights
      delivery:
        default: mount
        allowed: [mount, cache]
    archive:
      type: objectstore
      objectStore:
        provider: s3
        endpoint: https://s3.example.com
        bucket: taas-models
        prefix: weights
        forcePathStyle: false
        credentials:
          existingSecret: archive-keys
      delivery:
        default: cache
        allowed: [cache, download]

transfer:
  enabled: true
  mode: job
  namespace: taas-jobs
  concurrency: 4
  image:
    repository: taas-transfer
    tag: ""
  serviceAccount:
    create: true

warmup:
  enabled: true
  # Warmup node groups are not Helm values. Create them at runtime in
  # Operations > Warmup node groups; the model service stores them in
  # PostgreSQL and the controller creates the KServe LocalModelNodeGroup.
  scheduling:
    mode: preferred
    weight: 100
```

Validation rules:

- A `filesystem` backend must declare `existingClaim` or
  `provision.enabled`.
- `delivery.default` must appear in `delivery.allowed`; `cache` requires
  `warmup.enabled`.
- Transfer namespace and service account are created by the chart when
  `transfer.serviceAccount.create` is true; the Job's Role may read/write
  progress ConfigMaps in that namespace.
- The controller's ClusterRole gains `batch/jobs`, `configmaps` and the
  `serving.kserve.io` local-model resources only when the corresponding
  feature is enabled.
- `warmup.nodeGroups` is removed from `values.yaml` and
  `values.schema.json`; a Helm value that still carries it is ignored. An
  upgrade must create at least one group in **Operations > Warmup node
  groups** before the first warmup, otherwise the API returns a clear
  `no warmup node group is configured` error.

## 10. Compatibility and migration

- No `storage.backends` means the chart synthesizes one filesystem backend
  from `controller.weights.pvcName`, `storageClass` and `mountPath`, so an
  existing release upgrades without changing a single value.
- `model_versions.storage_backend` is additive; an empty value resolves to
  the default backend at read time.
- `model_transfers` and `model_cache_entries` are new tables and
  `model_versions.storage_backend` is a new column. They are GORM models
  applied by the model service's `Migrate` (`db.AutoMigrate`) at startup.
  This repository has no hand-written init-SQL scripts: an installation
  that bootstraps the schema out of band must apply the same `AutoMigrate`
  (or equivalent additive DDL) before starting the new server.
- Transfers default to `job`; `mode: inline` preserves the old in-process
  behaviour for a cluster that has not yet pushed the `taas-transfer`
  image. The server logs which path it took.

## 11. Security

- Every new RPC requires the platform administrator role; there is no
  tenant-scoped access to storage or transfers.
- Object-store credentials are only read from Secrets and are projected
  into the Job, never returned by an API, logged, or embedded in a spec.
- Archive extraction rejects absolute paths, `..` traversal, symlinks that
  escape the destination, and enforces file-count and byte limits.
- Transfer Jobs run with a dedicated service account and a Role scoped to
  progress ConfigMaps in the transfer namespace.

## 12. Observability and operations

- The transfer row carries bytes/total bytes and a human-readable message;
  the console and API surface both.
- Progress ConfigMaps are named by transfer id; a Job that dies before
  writing one simply has no progress, not a false success.
- Controller logs record job creation, cache reconciliation and affinity
  injection at info level; a missing LocalModelNode CRD is a warning, not
  a reconcile failure.
- Operators can inspect Jobs in the transfer namespace and the KServe
  local-model resources in the inference namespace.

## 13. Testing and acceptance

Unit tests cover storage normalization/validation, transfer service
transitions, upload extraction, warmup projections, transfer Job
construction and cancellation, warmup resource creation and affinity
injection, and the console build/type-check. FVT covers the new REST
endpoints and pagination. Acceptance requires:

1. A model version can be copied from a filesystem backend to an object
   store and back without touching the server process.
2. A download with a transfer image registers the version only after the
   Job reports success, and a failed Job leaves no registered version.
3. A `WarmModel` creates the node group/cache, reports `ready` with warmed
   nodes, and a deployment of that model receives the configured affinity.
4. An install with no `storage`/`transfer`/`warmup` values upgrades and
   behaves exactly as before.
5. The Models page renders catalog, warmup (models and nodes tabs) and
   transfer data from the API, paginates, and cancels non-terminal work; the
   two Operations pages render storage guidance and node-group CRUD.
6. Creating a warmup node group in the console creates a KServe
   `LocalModelNodeGroup`, and a warmup that selects it reports its nodes.
