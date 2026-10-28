# StormService Serving Topology — Architecture and Detailed Design

| Attribute | Content |
| --- | --- |
| Feature point | Serve every inference task through an AIBrix `StormService`: single-node, distributed (tensor/pipeline parallel across a pod group) and PD-disaggregated (prefill + decode roles); move autoscaling to the AIBrix `PodAutoscaler` only; show the configured Harbor registry on the image page; replace the end-user organization switcher with a plain organization + project identity block |
| Document scope | Architecture and detailed design: the serving-topology data model, the create/update API, the controller's StormService desired state, the autoscaler retargeting, the registry-config projection, and the console interactions for both surfaces |
| Owning modules | `services/infer` (topology validation, create/update, change event) · `internal/controller` (`stormservice.go`, `reconciler.go`, `modelclaim.go`, `engine_ready.go`) · `services/image` (`GetImageRegistryConfig`) · `web` admin console (`DeployForm`, `ServiceDetailPage`, `InferenceServicesPage`, `ImagesPage`) and user console (`OrgIdentity`) · `pkg/errors` (10319) |
| Related documents | [Distributed Inference Runtime (AIBrix Integration)](./distributed-inference-runtime.md) (the `ModelClaim` path this builds on) · [Model Catalog & One-Click Deployment](./model-catalog-deployment.md) (the deploy form and service pages) · [Inference Autoscaling](./inference-autoscaling.md) (the old HPA path, superseded here) · [Image Management](./image-management.md) (the catalog and import flow) |
| Status | Implemented |

---

## 1. Overview

Before this change the controller reconciled one plain `apps/v1 Deployment` plus a ClusterIP `Service` per inference service and, when autoscaling was enabled, a Kubernetes `HorizontalPodAutoscaler`. A Deployment can only express "N identical pods": it cannot gang-schedule the shards of one replica, cannot run two different roles (prefill and decode) under one service, and the HPA cannot scale anything that is not a built-in workload.

AIBrix ships exactly the missing primitive: **`StormService`** (`orchestration.aibrix.ai/v1alpha1`). One storm owns a *role set*; each role runs `replicas` pod groups of `podGroupSize` pods, and `spec.replicas` is the number of role-set replicas. That single object expresses all three topologies the platform supports:

| Topology | Storm shape | Pods per storm replica |
| --- | --- | --- |
| `single` | one role `worker`, `podGroupSize = 1` | 1 |
| `distributed` | one role `worker`; the pod shards the model across TP × PP devices | 1 (requesting TP × PP devices) |
| `pd` | roles `prefill` and `decode`, each with its own replica count and TP × PP devices | prefill + decode |

Serving is now **always** a StormService. The Deployment builder remains only for a controller started without a dynamic client (unit tests, a CRD-less install) and to delete a Deployment left by an older controller.

## 2. Architecture decisions

**AD1 — One serving object per service.** `spec.replicas` of the storm is the service's `replicas` for every topology; a PD storm replica is one prefill + decode pair.
**AD1a — Parallelism shards inside the pod, not across pods.** A tensor/pipeline-parallel replica runs as **one** pod that requests one device per shard (`TP × PP` devices); `podGroupSize` stays 1. A multi-pod shard group needs a per-rank rendezvous (world size, node rank, master address) that the storm controller does not inject, and vLLM refuses to start when its world size exceeds the devices the pod can see — this was observed on the live cluster, where per-pod sharding failed with "World size (2) is larger than the number of available GPUs (1)". Multi-node serving therefore needs the operator's own rank configuration and is out of scope. The Service (`<name>-svc`), the `ModelClaim` pod selector and the status follower keep selecting on the shared resource labels (`go-taas.com/service-id`, `app`, `app.kubernetes.io/managed-by`), which the role templates carry. Nothing downstream of the pods changed.

**AD2 — The topology is data, not code.** `inference_services.topology` stores `single` / `distributed` / `pd` with `tensor_parallel_size`, `pipeline_parallel_size`, `prefill_replicas`, `decode_replicas`. Reads normalize an empty value to `single`, which is what a row written before the column existed means. The wire contract uses the same lowercase names, so the console and the API speak one vocabulary.

**AD3 — Validation happens before any write.** `NormalizeTopology` rejects an unknown layout, out-of-range parallelism (`1..100`, `TP × PP ≤ 100`), a "distributed" request that is really one pod (`TP × PP < 2`), PD on an engine that cannot split (anything but vLLM/SGLang), and out-of-range role counts. Error **10319 INFER_TOPOLOGY_INVALID**. Nothing is published when a check fails.

**AD4 — The engine gets only the flags it cannot derive itself.** The controller appends `--tensor-parallel-size` / `--pipeline-parallel-size` when the operator did not set them, and for PD the role marker: vLLM's `--kv-transfer-config` (`kv_producer` for prefill, `kv_consumer` for decode, default connector `PyNcclConnector`) or SGLang's `--disaggregation-mode prefill|decode`. An explicit engine argument always wins, so a cluster with a different KV connector is configured through the existing advanced-args form.

**AD5 — Autoscaling is the AIBrix PodAutoscaler, never an HPA.** The `PodAutoscaler` targets the **StormService** (`scaleTargetRef.kind: StormService`), which the CRD accepts and the AIBrix controller drives (verified against the live cluster). Stale HPAs from an older controller are deleted on every reconcile; a PodAutoscaler write failure is reported as a failed deploy rather than silently pinning replicas, so the console never shows "autoscaled" for a service nothing is scaling. Role-level autoscaling (`subTargetSelector`) was rejected: the admission webhook forbids it with the `HPA` strategy and the alternative strategies need extra infrastructure this deployment does not define.

**AD6 — Updates are spec changes.** `UpdateInferenceServiceTopology` (`POST /api/v1/admin/inference-services/{id}:update-topology`) rewrites the layout, returns the service to `pending` and republishes the desired state; the storm is rolled out with the new roles and the service keeps its id and endpoints. `replicas = 0` keeps the current count.

**AD7 — Migration is implicit.** On every upsert the controller deletes a legacy Deployment of the same name before the storm's pods can be double-selected, and `applyDelete` tears down both. No manual migration step is required for a running service.

**AD8 — Readiness follows the pod group.** A distributed or PD storm exposes the API on one pod only (rank 0 / the decode role), so the readiness follower reports `starting` when any pod is scheduled and `running` when **any** pod answers the engine health endpoint; one pod that can never start fails the whole deploy.

## 3. Data model

`inference_services` gains five columns (GORM AutoMigrate, no hand-written DDL):

| Column | Type | Meaning |
| --- | --- | --- |
| `topology` | `varchar(16)` default `single` | layout name |
| `tensor_parallel_size` | `int` default `1` | TP degree of one replica |
| `pipeline_parallel_size` | `int` default `1` | PP degree of one replica |
| `prefill_replicas` | `int` default `0` | PD prefill workers |
| `decode_replicas` | `int` default `0` | PD decode workers |

Existing rows read as `single` with `1/1`, so no backfill is needed.

## 4. API

`CreateInferenceServiceRequest` gains `topology`, `tensor_parallel_size`, `pipeline_parallel_size`, `prefill_replicas`, `decode_replicas`; `InferenceServiceSummary` returns the same fields so list and detail render the layout.

```proto
rpc UpdateInferenceServiceTopology(UpdateInferenceServiceTopologyRequest)
    returns (UpdateInferenceServiceTopologyResponse) {
  option (google.api.http) = {
    post: "/api/v1/admin/inference-services/{service_id}:update-topology"
    body: "*"
  };
}
```

Validation order for the update: service exists (10301) → not terminated (10303) → image readable → layout valid (10319) → replicas in range (10305). The change event carries the layout, so the controller needs no database read.

`ImageService.GetImageRegistryConfig` (`GET /api/v1/admin/image-registry-config`) projects the configured Harbor registry: `configured`, `hostname`, `project`, `example_reference`. Credentials are never exposed; an unconfigured install reports `configured=false` and the console disables the import action.

## 5. Controller mapping

`buildStormService` composes:

- metadata: name = service name, the resource labels plus the gateway routing labels/annotations;
- `spec.replicas` = `clampReplicas(evt.Replicas)`, `stateful: false`, `updateStrategy: RollingUpdate(maxUnavailable: 1)`;
- `spec.selector.matchLabels` = the shared resource labels (immutable);
- `spec.template.spec.roles[]`, each with `name`, `replicas`, `podGroupSize`, and a `template` whose labels add `go-taas.com/role` and whose `spec` is the engine pod spec converted to the unstructured shape through JSON (so the k8s `omitempty` tags keep empty defaults out of the CRD).

`applyStormService` creates or updates with a bounded conflict retry (the AIBrix controller writes status concurrently). `pinStormServiceReplicas` merge-patches `spec.replicas` when autoscaling is off. `deleteStormService` ignores not-found.

## 6. Console

**Create** (`DeployForm`, inline panel): a *Serving topology* section with the layout select, TP/PP inputs (distributed and PD), prefill/decode inputs (PD) and a summary line stating how many pods the layout creates (`replicas × roles × TP × PP`). The same validation rules run client-side; the server remains authoritative.

**Update** (`ServiceDetailPage`): a *Serving topology* card shows the layout, parallelism and PD role counts, with an *Edit layout* dialog that posts `:update-topology`. The list page gains a *Topology* column (`Distributed · TP 4 / PP 2`, `PD · P2/D3`).

**Images** (`ImagesPage`): a *Import registry (Harbor)* card shows the configured host and project and the reference shape an import produces; the import dialog previews the concrete destination (`<host>/<project>/<image>`).

**End-user console** (`UserShell`): the organization dropdown is replaced by `OrgIdentity`, a read-only block showing the account's organization (display name plus id) and its project. A user belongs to at most one of each, so there is nothing to switch; joining happens on the join-request pages.

## 7. Errors

| Code | Name | When |
| --- | --- | --- |
| 10319 | `INFER_TOPOLOGY_INVALID` | unknown layout, parallelism out of bounds, `distributed` with one pod, PD on a non-vLLM/SGLang engine, role count out of bounds |
| 10305 | `INFER_REPLICAS_INVALID` | `replicas` outside `1..100` on update |
| 10303 | `INFER_SERVICE_STATE_INVALID` | update of a terminated service |
| 10301 | `INFER_SERVICE_NOT_FOUND` | update of an unknown service |

## 8. Verification

Verified on the target cluster (`sd4090`, AIBrix in `aibrix-system`):

- a probe storm with two roles and `podGroupSize: 2` produced exactly `replicas × Σ(role.replicas × podGroupSize)` pods, confirming the role semantics this design relies on;
- a `PodAutoscaler` targeting a `StormService` is accepted, reconciles (`actualScale`, `ReconcilingScaleDiff`) and is rejected only when `subTargetSelector` is combined with the `HPA` strategy;
- single-node services deploy, reach `running` and serve through the gateway;
- the console renders the topology card, the layout editor, the topology column, the registry card and the identity block.

Limitations stated plainly: PD and multi-pod distributed serving need more than one GPU (or several nodes) to actually start; the cluster used for verification has one GPU, so those layouts are covered by unit/FVT tests and by the CRD semantics probe rather than by a live end-to-end generation. The KV-transfer connector default is vLLM's `PyNcclConnector`; a fleet that uses a different connector supplies `kv-transfer-config` through the advanced engine arguments, which always take precedence.

## 9. Acceptance criteria

- **AC1** Creating a service with `topology=distributed, TP=4, PP=2` stores the layout, publishes it, produces one storm role with `podGroupSize: 1`, and the pod requests 8 devices.
- **AC2** Creating with `topology=distributed, TP=PP=1` fails with 10319 and publishes nothing.
- **AC3** Creating a PD service produces two roles, `prefill` and `decode`, with the requested replica counts, each pod requesting TP × PP devices, and the engine command carries the role's KV-transfer mode.
- **AC4** `:update-topology` returns the service to `pending`, republishes the upsert and keeps the service id.
- **AC5** A service with autoscaling enabled gets a `PodAutoscaler` whose `scaleTargetRef.kind` is `StormService` and no HPA; disabling autoscaling deletes it and pins the storm's replicas.
- **AC6** Deleting a service removes the storm, the Service, the `ModelClaim`, the route and the autoscaler.
- **AC7** `GET /api/v1/admin/image-registry-config` reports the configured host and project, and `configured=false` when no registry is set.
- **AC8** The end-user sidebar shows the organization and project as text, with no selector.
