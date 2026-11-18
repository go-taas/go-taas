# LLMInferenceService Serving Topology — Architecture and Detailed Design

| Attribute | Content |
| --- | --- |
| Feature point | Every inference service is one KServe `LLMInferenceService`: single-node, multi-machine (one pod per worker) and PD-disaggregated (prefill + decode, each tuned on its own); autoscaling is KServe autoscaling, not a per-service HPA |
| Document scope | Architecture and detailed design: the serving-topology data model, the create/update API, the controller's desired state, the pod overlay, routing, autoscaling, teardown and the verification that was actually run |
| Owning modules | `services/infer` (topology validation, create/update, change event) · `internal/controller` (`serving.go`, `routing.go`, `reconciler.go`, `podgroup.go`) · `services/image` (`GetImageRegistryConfig`) · `web` admin console (`DeployForm`, `ServiceDetailPage`, `InferenceServicesPage`, `ImagesPage`) and user console (`OrgIdentity`) · `pkg/errors` (10319) |
| Related documents | [Serving Runtime](../guides/serving-runtime.md) (what gets created and how a request reaches a pod) · [Model Catalog & One-Click Deployment](./model-catalog-deployment.md) (the deploy form and service pages) · [Inference Autoscaling](./inference-autoscaling.md) (the scaling policy on a service) · [Image Management](./image-management.md) (the catalog and import flow) |
| Status | Implemented |

---

## 1. Overview

A service used to be one `apps/v1 Deployment` plus a ClusterIP `Service` per inference service, and a `HorizontalPodAutoscaler` when autoscaling was on. A Deployment can only express "N identical pods": it cannot place the shards of one replica on their own machines, cannot run two different roles (prefill and decode) under one service, and an HPA cannot target anything that is not a built-in workload.

The platform therefore reconciles exactly one object per service: a KServe **`LLMInferenceService`**. The object carries the engine pod template, so the platform keeps full control of the container, and KServe owns everything around it: the leader-worker set (LWS) for a multi-pod replica, the prefill/decode pair, the workload `Deployment`, the `Service` that exposes it, and the autoscaler.

| Topology | What the object declares | Pods per replica |
| --- | --- | --- |
| `single` | `spec.replicas`, one `main` template; the pod owns `TP × PP` devices | 1 |
| `multinode` | the same template plus `spec.worker` and `spec.parallelism.data = TP × PP / gpu_per_pod` | TP × PP / gpu_per_pod |
| `pd` | `spec.prefill{replicas,template}` plus the decode template in `spec.template`, both with the same `parallelism.data` | prefill pods + decode pods |

## 2. Decisions

**AD1 — One object per service, and the platform owns the pod.** `spec.template` is the platform's engine pod, converted to the unstructured shape through JSON so the Kubernetes `omitempty` tags keep empty defaults out of the CRD. KServe owns the workload, the pod identity and the rolling update; the platform never writes a `Deployment` for a service.

**AD2 — The container is an overlay, not a second container.** The preset templates the model server as `main` and KServe merges an overlay into a container **by name**. The platform's engine container is therefore renamed to `main`; named anything else it would be added next to the preset's server and the pod would run two model servers.

**AD3 — The overlay does not declare the model-server port.** The preset owns the port (and a disaggregated decode also runs KServe's routing sidecar on the preset's port), so a port on the overlay would either duplicate it or make the pod spec invalid. The engine keeps its own `--port`, and for a decode that port is `8001` because the routing sidecar already holds `8000`.

**AD4 — The weights are mounted, not fetched.** The platform mounts the shared weights claim and points `spec.model.uri` at the claim plus the model's directory. `spec.storageInitializer.enabled` is `false`: the initializer would copy the whole model a second time and make the pod wait for it.

**AD5 — A parallel replica gets its own shared memory.** `TP × PP > 1` mounts a 2 GiB memory-backed `emptyDir` at `/dev/shm`, because Kubernetes' 64 MiB default kills the engine ("Insufficient space in /dev/shm").

**AD6 — A multi-pod replica keeps the engine's own rendezvous.** The rank is the pod's ordinal, the master is ordinal 0 of the same group, and both are resolved at container start through `sh -c`; a shared pod template cannot carry per-pod flags. Engine arguments are passed as positional parameters so a JSON value such as `kv-transfer-config` is never re-parsed by the shell.

**AD7 — The object asks for the workload, not for a route.** The platform owns the data-plane route: one catch-all route hands every request to the model router, which forwards a model's request to the workload `Service` that serving stack created for that model. The object therefore carries no `spec.router`: that block makes the runtime write a per-model route whose backend is an `InferencePool`, and a Gateway without the Gateway API Inference Extension refuses that backend, so the object is reported not ready while the model serves perfectly well. A workload `Service` is written either way, and it is what the router addresses.

**AD8 — The router resolves the model, the path names the service.** The endpoint the console publishes is `<host>/<service>/v1`, so the caller has a stable URL per service, and the model is named in the body. The router drops that first segment and forwards to the service's workload, which answers the bare OpenAI path; a model it does not know keeps the data plane's internal listener as its fallback. No per-model route exists at any point.

**AD9 — Autoscaling is the object's, not a second object's.** A policy becomes the bounds in `spec.scaling`; when autoscaling is off the controller pins `spec.replicas` with a merge patch so a stale autoscaler does not fight a manual scale.

**AD10 — Teams come from labels, gang scheduling from the scheduler.** A PodGroup and its Volcano annotation are written for a multi-pod replica only when Volcano gang scheduling is on and the replica is not a HAMi slice, because a HAMi slice is placed by HAMi's scheduler, not Volcano.

## 3. Data model

`inference_services.topology` stores `single` / `multinode` / `pd` next to `tensor_parallel_size`, `pipeline_parallel_size`, `prefill_replicas` and `decode_replicas`. Reads normalize an empty value to `single`, which is what a row written before the column existed means, and the wire contract uses the same lowercase names.

| Column | Type | Meaning |
| --- | --- | --- |
| `topology` | `text` default `single` | serving layout |
| `tensor_parallel_size` / `pipeline_parallel_size` | `int` default `1` | parallelism of one replica |
| `gpu_per_pod` | `int` default `1` | devices one pod owns; 0 reads as 1 |
| `prefill_replicas` / `decode_replicas` | `int` default `0` | PD role counts |

## 4. API

`CreateInferenceServiceRequest` carries `topology`, `tensor_parallel_size`, `pipeline_parallel_size`, `prefill_replicas`, `decode_replicas`; `InferenceServiceSummary` returns the same fields so list and detail render the layout.

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

`buildLLMInferenceService` composes one object:

- metadata: name = service name, namespace = the inference namespace, the shared resource labels, the routing labels and annotations, and an empty annotations map;
- `spec.replicas` = the role's replica count (`clampReplicas(evt.Replicas)`);
- `spec.model{name, uri}` — `name` is the served model name, `uri` is `pvc://<pvc>/<weight path>`;
- `spec.storageInitializer.enabled: false` (AD4);
- `spec.router` is absent: the platform routes, the runtime only runs the workload (AD7);
- `spec.template` = the engine pod overlay; `spec.worker` and `spec.parallelism.data` when a replica spans more than one pod (AD6);
- `spec.prefill{replicas,template}` when the topology is `pd`;
- `spec.scaling{minReplicas, maxReplicas}` when the service autoscales (min `0` when it may scale to zero) — see §7.

`applyLLMInferenceService` creates or updates with a bounded conflict retry (the KServe controller writes status and defaults concurrently, so a stale resource version would fail every apply after the first). `deleteLLMInferenceService` ignores not-found, and `pinLLMInferenceReplicas` merge-patches `spec.replicas` when autoscaling is off.

## 6. Routing

The endpoint the platform publishes is `/<service>/v1`: `endpointFor(name)` returns `<endpoint base>/<name>/v1`, and the host is the data-plane hostname. The model router drops that first segment and forwards the request to the workload `Service` the serving stack created for the service (`<service>-kserve-workload-svc.<inference namespace>.svc:8000`), which answers the bare OpenAI path. Consequences worth stating because they are what keeps the data plane small:

- no per-model `HTTPRoute` and no `ReferenceGrant` for a model's route: the platform writes one catch-all route (`/` → the model router's Service) and the router resolves the model to a service and addresses that service's workload;
- the model-to-service table is what makes that possible: the control plane publishes the model each service answers, so a caller may name the model only in the body;
- a model the platform does not report has no known workload, so it keeps the internal listener as its fallback instead of a route the serving stack never wrote.

## 7. Autoscaling

A policy on a service becomes the bounds on the object, `spec.scaling{minReplicas,maxReplicas}`; a service allowed to scale to zero has `minReplicas: 0`. The signal the workload scales on is the runtime's own, so the platform writes no trigger: which metric a replica reacts to is the runtime's decision, and a platform-written query would go stale the moment the runtime changed its metric name. With autoscaling off the controller pins `spec.replicas` after each apply, so a manual scale is what the object ends up with. The autoscaler is the runtime's own, not a separate HPA.

## 8. Errors

| Code | Name | When |
| --- | --- | --- |
| 10319 | `INFER_TOPOLOGY_INVALID` | unknown layout, parallelism out of bounds, `multinode` with one pod, PD on an engine that cannot split, role count out of bounds |
| 10318 | `INFER_ENGINE_ARGS_INVALID` | an unknown role name, a role given twice, or an invalid flag/value in a role override |
| 10305 | `INFER_REPLICAS_INVALID` | `replicas` outside `1..100` on update |
| 10303 | `INFER_SERVICE_STATE_INVALID` | update of a terminated service |
| 10301 | `INFER_SERVICE_NOT_FOUND` | update of an unknown service |

## 9. Verification

What was run: `go build ./...`, `go vet ./...`, `go test ./...` (the controller package's unit tests cover the desired object: model name, URI and disabled storage initializer, one replica per single-node service, no `spec.router`, no leader-worker set for a single-node replica, the container overlay merged into `main` with no declared port, and create-then-update of the same object) and `helm template` / `helm lint` of `deploy/helm/go-taas` with the data plane, the gateway plugin and the model router enabled.

Also observed on a live cluster, with a real model and its own image: the object comes up `Ready=True`, the workload `Deployment` and its `Service` exist, one engine pod runs (no router-scheduler pod is started), and a completion through the public hostname returns generated text. The replica count reported on the service is the number of ready engine pods, so a pod that is still loading is not counted as served.
