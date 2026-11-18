# Serving Runtime Architecture

| Attribute | Content |
| --- | --- |
| Feature point | The platform serves models through one serving stack: a KServe control plane writes the serving object, the workload's own `Service` load-balances the replicas, the data-plane Gateway terminates the public host and the model router makes the second hop |
| Document scope | What the runtime consists of, why this stack over the alternatives, how control and data plane are split, what the platform owns and does not own, how the runtime state is projected into the console, and what the verification covered |
| Owning modules | `internal/controller` (the serving object, the pod group, the data-plane route) · `pkg/modelrouter` + `apps/model-router` (the second hop) · `services/runtime` (the runtime projection the console reads) · `services/infer` (the desired state the controller converges to) |
| Related documents | [LLMInferenceService Serving Topology](./llminferenceservice-serving-topology.md) · [Inference Autoscaling](./inference-autoscaling.md) · [Data-Plane Gateway & Plugin](./data-plane-gateway-plugin.md) · [Serving Runtime guide](../guides/serving-runtime.md) |
| Status | Implemented |

---

## 1. What the runtime is

| Piece | Length |
| --- | --- |
| **KServe** (`serving.kserve.io`, LLM serving runtime and its presets) | the CRDs, the workload (leader-worker set), the `Service` that exposes it, and autoscaling |
| **the data-plane Gateway** (Envoy Gateway) | the public host, TLS and the Wasm chain (API key, quota, metering) |
| **the platform controller** | the engine pod (image, args, devices, weights, env), the pod group a multi-pod replica is placed as, and the store the console addresses |
| **the model router** | the one hop that knows whether a model is self-hosted or external, and the one place the platform's request path is decided |

One object per service, an `LLMInferenceService`, carries everything a service is: model, replicas, pod template and scaling policy. Field-level detail lives in [LLMInferenceService Serving Topology](./llminferenceservice-serving-topology.md); the operator-facing version is [Serving Runtime](../guides/serving-runtime.md).

## 2. Why this stack, and what was rejected

Three requirements killed the alternatives. Each is a thing the platform must do, not a preference:

1. **One object per service, whatever its shape.** The platform tracks a service by id and hands the cluster one object. A stack whose shape changes with the topology (an object for a pod, another for a replica group, a third for the router) makes every consumer — quota, console, logs, metering — branch on the shape. KServe's `LLMInferenceService` expresses one GPU pod, a leader-worker set and a prefill/decode pair as fields of the same object.
2. **Placement stays with the cluster, not with the platform.** A multi-pod replica must be placed as a whole or not start; that is a gang decision made with the cluster's free accelerator capacity. The platform therefore writes a pod group and lets the cluster's scheduler make the call, instead of trying to be a scheduler.
3. **The platform routes by model, the workload serves by replica.** A request names a model; the platform's route hands it to the model router, which resolves the model to the service that answers it and forwards to that service's workload `Service`. Which replica of that workload answers is the `Service`'s own load balancing, so the platform never has to know how many pods an engine runs.

A fourth requirement is why the split below is worth the two hops: the plane that terminates the public host is the plane that knows the API key, and the plane that knows the model is the plane that can route a request. Merging them means the model's own route is behind the key check, which would need a second check or a bypass.

What was rejected, and why it is not a close call:

- **A platform-owned object per service, with the platform writing the pod shape itself.** Then every placement decision is the platform's, and the platform becomes a scheduler it does not want to be. Placement is a cluster decision with cluster state; a platform scheduler loses to it.
- **A second Gateway per service.** The platform deploys one Gateway per cluster and a per-service object re-introduces an object-per-service on the plane that matters most, the public one.
- **A per-model `HTTPRoute`, whoever writes it.** The route's backend would be the runtime's pool, and this cluster's Gateway does not serve that backend kind: the route is never `Accepted`, so the runtime reports the object not ready while the model answers through its workload `Service` just fine. The platform writes one catch-all route and the router resolves the model to the service, so no per-model route exists at all.
- **A separate autoscaler object per service.** It is a second object to keep in step with the first, and its target is the object the previous item already owns. The policy goes on the service; the autoscaler goes with the workload.
- **A second check on the internal hop.** The first hop already checked the key, quota and metering, and a second check would need a trusted token the platform does not issue, so the second hop is plain HTTP inside the cluster and the internal listener is not reachable from outside. That last part is a deployment requirement, not a convention: an internal listener with a public address is a key bypass.

## 3. What the platform owns, and what it does not

| The platform decides | Because |
| --- | --- |
| the pod: image, engine args, devices, weights mount, env, probes | the model, its tuning and its limits are ours |
| the pod group for a multi-pod replica | a half-placed replica must not hold the GPUs it cannot use |
| `spec.replicas` when autoscaling is off | a manual scale is what the service ends up with |
| the model's URI and name | the weight layout and the client-facing name are the tenant's contract |
| the one data-plane route and the key check | the public path and what a request costs to admit are the platform's |

| Not the platform's | Because |
| --- | --- |
| which replica answers a request | that is the workload `Service`'s, one hop the platform does not need to reason about |
| the workload's kind, identity, rolling update | a serving object that changes kind per topology is what the platform refuses to track |
| the autoscaler's algorithm and triggers | the policy is ours, the mechanism is the workload's |
| the workload `Deployment` and the `Service` that exposes it | the runtime writes them from the object the platform already wrote |
| a second key check, a quota, or a meter on the second hop | the first hop did it; a second check needs a token the platform does not issue |
| a scheduler of its own | placement is a cluster decision with cluster state; a platform scheduler loses to it |

The line that matters: the platform owns **what a service is** and **what it costs to admit a request**; the cluster owns **where the pod goes** and the runtime owns **which replica answers**.

## 4. Control plane and data plane

```
control plane (the platform)                 data plane (inside the cluster)
────────────────────────────                ───────────────────────────────
infer module → change event → MQ  ─────▶  controller: one LLMInferenceService
                                              │ + pod group when a gang is needed
                                              ▼
                                          KServe: workload + its Service
                                              │
public host ◀── Gateway ◀── route ────────▶   model router → workload Service
```

The control plane never talks to a pod. It converges a service to one object and stops. The data plane never talks to the database. It answers requests with the state it already has from the object. The two meet at exactly one place: the `LLMInferenceService` the controller writes and the runtime reads. A second meeting point is a second thing to keep in step, and it is always the one that drifts.

The control plane's failures are the ones a console can show: a service that will not converge, a key that will not admit, a scheme that is not `kserve`. The data plane's failures are the ones a request shows: a 429 from a spent balance, a 503 when no replica is ready. Keeping them apart keeps each debuggable without the other.

## 5. Serving a model's own object

The controller writes one object per upsert and deletes it on teardown:

- `spec.model{name, uri}` — the name the caller sends as `model`, the URI the weights are mounted at (`pvc://<pvc>/<weight path>`);
- `spec.replicas` — the replica count, pinned when autoscaling is off;
- `spec.template` — the engine pod, the platform's own container overlaid onto the preset's model server by name;
- `spec.worker` and `spec.parallelism.data` when one replica is more than one pod;
- `spec.prefill{replicas,template}` when the topology is prefill/decode;
- no `spec.router` — the platform routes, so the runtime is not asked for a model route;
- `spec.storageInitializer.enabled: false` — the platform mounts the weights itself.

The pod group is a second object, written for a multi-pod replica only and only when the cluster's scheduler is gang-scheduling; a single-pod replica is placed as a pod. The pod group carries the service's owner labels, so a deleted service takes it with it. (Field names and their reasons are in [the topology document](./llminferenceservice-serving-topology.md); this page does not repeat them.)

## 6. Serving runtime health and the projection

The runtime page reads a projection, not the cluster:

1. the controller polls the KServe control plane's Deployments in the inference namespace and the data-plane Gateway's readiness, and publishes the snapshot on the runtime's own MQ subject;
2. the runtime module keeps the snapshot in memory, the same shape as the accelerator inventory, and answers the page from it;
3. a component that disappears disappears from the next snapshot, the same self-healing shape as the inventory: a stale row cannot outlive the source.

Ready means "its Deployment reports available replicas at least the desired count" — the same test for every component, so a component tabled in the page cannot disagree with the console.

**The scheme.** The scheme is `kserve` and it is the default. `plain` means "a Deployment and a Service, no serving object": the fall-back a CRD-less cluster or a unit test uses, with no workload written by the runtime and no per-model route either way. It is not a second mode of the same object, and the console says so on the page rather than offering a switch as if there were a choice.

## 7. Configuration

| Key | Default | Meaning |
| --- | --- | --- |
| `runtime.gateway.namespace` | controller namespace | namespace of the data-plane `Gateway` the platform's route attaches to |
| `runtime.gateway.name` | `taas-inference` (release name) | the Gateway's name; the Envoy Service is pinned to it |
| `runtime.gateway.deployment` | same as `name` | the Deployment the Gateway is backed by, for component health |
| `controller.scheduling.schedulerName` | empty | `volcano` turns gang scheduling on; empty leaves placement to the cluster's default |

The Gateway the server writes the data-plane route against and the Envoy Service that route and the model router address must be the same Gateway. A mismatch is not a route that reports a problem: the console shows a service `Ready` and the request still 404s, which is why `helm template` names both.

## 8. Errors

The runtime surface uses the platform's codes; the ones a runtime operator meets:

| Code | Name | When |
| --- | --- | --- |
| 12901 | `CodeRuntimeUnavailable` | the cluster or the KServe control plane is unreachable; the runtime page shows no components and the scheme stays what it was |
| 12902 | `CodeRuntimeSchemeInvalid` | a scheme other than `kserve` or `plain` |
| 12909 | `CodeRuntimeRouteNotPublished` | a service is `Ready` but no route is published for it yet |

## 9. Verification

Ran in this change: `go build ./...`, `go vet ./...`, `go test ./...` (the runtime and controller unit tests cover the projection's shape and the serving object's fields — see the topology document for the assertions), `helm template` / `helm lint` of `deploy/helm/go-taas` with the data plane, the gateway plugin and the model router enabled, and `node site/scripts/build-docs-manifest.mjs --check` for the docs index.

Also observed on a live cluster with a real model and its own image: the object turns `Ready`, the workload `Deployment` and its `Service` exist, one engine pod serves, and a completion through the public hostname returns generated text. A cluster check is that one request, plus `kubectl get llminferenceservice,podgroup -A` against the same service.

## 10. Limitations, stated

- A multi-pod replica needs a gang-capable scheduler and several machines with free devices; a cluster without them shows the service `Pending` and the pod group `Unschedulable`, which is the honest state, not a retry loop.
- PD needs an engine that can split prefill from decode; the prefill half has its own replica count and its own tuning, so a PD service is two role sections to get right, not one.
- The runtime collection is a poll: a component that flips between ready and not is only as fresh as the interval, and the console says when the snapshot was taken rather than pretending it is live.
