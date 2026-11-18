# Serving Runtime

| Attribute | Content |
| --- | --- |
| Feature point | Every inference service is one KServe `LLMInferenceService`; the platform owns the pod, KServe owns the workload and its Service |
| Document scope | What the runtime creates for a service, how a request reaches a replica, what an operator must install, what a change does, and what to check when a service serves nothing |
| Owning modules | `internal/controller` (`serving.go`, `routing.go`, `reconciler.go`) · `pkg/k8s` (the data-plane route) · the data-plane Gateway and the model router |
| Related documents | [LLMInferenceService Serving Topology](../architecture/llminferenceservice-serving-topology.md) · [Inference Autoscaling](../architecture/inference-autoscaling.md) · [Installation](../getting-started/installation.md) |
| Status | Implemented |

# Serving runtime

Every inference service the platform starts is a KServe `LLMInferenceService`.
This page explains what that means for an operator: what gets created, how a
request reaches a model, and what to do when it does not.

## Why this runtime

The platform serves models that range from a single GPU pod to a replicated
prefill/decode pair spread over several nodes. One object has to express all
of them, because everything else in the platform — quotas, the console, the
logs viewer, metering — is written against one shape:

- **One serving object.** A service is one `LLMInferenceService`. Switching
  from one GPU pod to a disaggregated prefill/decode pair changes fields on
  that object, not the kind of object the platform tracks.
- **Routing belongs to the serving layer.** The platform publishes one route
  and forwards to the model router, and the router sends a model request to
  the model's own workload Service. The path a request takes from there to a
  replica is the serving stack's own concern: the engine's replicas sit behind
  one Service, so nothing in the platform has to know how many pods answer.
- **Scaling comes from the same object.** A service's replica bounds and its
  scale signals sit with the workload, so a service that scales itself does
  not need a second controller the platform has to keep in sync.

## What gets created

For a service named `demo` in the inference namespace (`taas-infer` by
default) the controller writes:

| Object | Owner | Purpose |
| --- | --- | --- |
| `LLMInferenceService demo` | platform controller | the serving object: model, replicas, pod template |
| `Deployment demo-kserve` + `Service demo-kserve-workload-svc` | KServe | the engine replicas and their in-cluster address (port 8000) |
| `PodGroup demo-<role>` | platform controller | the gang a multi-pod replica is placed as (Volcano) |

The `PodGroup` is written by the platform controller; the workload and its
Service are written by KServe from the object's template. The platform does
not ask for the serving stack's own route (`spec.router`): that route declares
an `InferencePool` backend, which a Gateway without the Gateway API Inference
Extension refuses, so the object would stay `Ready=False` while the model
actually serves. The platform route is what serves the model, and the router
addresses the workload Service directly.

## How a request reaches a model

```
client ──TLS──▶ platform Gateway (https listener)
                   │  Wasm: API key, quota, usage metering
                   ▼
              model router ──▶ model's workload Service ──▶ a replica
```

1. The public hostname is served by the `https` listener of the data-plane
   Gateway. The Wasm plugin in front of the route verifies the API key, checks
   the balance and records usage. Nothing downstream checks a key again.
2. The route's only backend is the model router. The router is the one
   component that knows whether a model is served here or sold by an external
   provider, so the route cannot choose a backend from the path alone.
3. For a model served here, the router asks the platform which service answers
   the requested model name and forwards the request to it, keeping the OpenAI
   path (`/v1/chat/completions`) intact. A client therefore only needs the
   model name — the router owns the mapping, so the console does not have to
   expose a per-service path.
4. A self-hosted model is reached at its own workload Service,
   `<service>-kserve-workload-svc.<infer-ns>.svc:8000`. Every served model has
   one, it carries the engine's port, and nothing in front of it decides
   anything, so a request reaches a replica on any Gateway. The engine's own
   replicas share the load through that single Service.

5. The request never leaves the cluster: it is written over plain HTTP to the
   model's workload Service, a `ClusterIP` name that only resolves inside the
   cluster.

Nothing in the second hop is authenticated or metered again. That is
deliberate: the first hop already did both, and a second check would need a
trusted-token bypass the platform does not have.

## What the platform still owns

The controller keeps writing the parts the serving stack does not know about:
the `PodGroup` (a multi-pod replica is one gang, so a half-placed replica
never holds GPUs while it waits), the weights volume, and the accelerator
request. Reclaim, teardown and sweeps address the same objects under the
service's owner labels, so a deleted service takes its gang and claims with
it.

## Install prerequisites

| Component | Why |
| --- | --- |
| KServe with the LLM serving runtime and its presets | writes the workload, its Service and the pod the engine runs in |
| Gateway API and a Gateway implementation | serves the public listener below |
| Volcano | gang scheduling of multi-pod replicas |
| A default `StorageClass` (or the weights PVC pre-created) | the weights volume |

The chart's data plane expects a Gateway class named `eg`. The platform does not
ask the serving stack for its own model route: that route declares an
`InferencePool` backend, which a Gateway without the Gateway API Inference
Extension refuses, and the object would then be reported not ready while the
model serves. One platform route serves every model, and the model router
addresses the model's workload Service — which any Gateway has, and which needs
no extension at all.

Two listeners matter and they are not interchangeable:

- `https` (443) is the public one. It terminates TLS for the inference
  hostname and accepts routes only from its own namespace.
- `internal` (80) serves the routes that are not public but must be reachable
  from inside the cluster, such as the server's own internal API. It carries no
  certificate and its hostname is in no DNS zone, so it must not be reachable
  through the load balancer. Publishing it publicly would let a caller skip the
  key check by addressing it directly: keep the Gateway Service internal (a
  `ClusterIP` load-balancer class, or a firewall rule that denies it).

The Envoy Service's name is pinned to the Gateway name (`taas-inference`),
because `Gateway.status.addresses` reports addresses, not the Service name, and
the runtime view and the internal routes have to address it by a name that
survives a rollout.

## Changing a service

| Change | Effect |
| --- | --- |
| image or a command that changes the pod template | the object is replaced in place: the old replica drains before the new one is scheduled |
| replicas or autoscaling policy | the workload is resized in place; the policy is what the replica count is set to, because the cluster ships no autoscaler |
| topology (single → multi-node or PD) | the service is rewritten; the workload is replaced |
| delete the service | the service, its workload, gang and claims are removed together |

A pod holds its device for its whole life, so a model is replaced rather than
surged: `maxSurge: 0` and `maxUnavailable: 1` are part of the serving object.
A surge replica would wait for a device the old pod still holds — on a node
with one device, that is a rollout that never finishes. An update therefore
briefly serves from no replica. A model that must not lose in-flight requests
during an update runs more than one replica.

A replica that is mid-generation when the object changes is drained by the
serving stack, not by the platform, so a long generation is allowed to finish.

## When a service serves nothing

| Symptom | Check |
| --- | --- |
| 404 or 503 on the public hostname | the model router names a service for the model (it does not answer for an unknown model name), and the workload Service the router addresses has ready endpoints |
| 401 before any pod is reached | the key is not in the platform or its balance is spent; the Wasm verdict is cached for `gatewayPlugin.cacheTTL` |
| requests reach no replica | the model's workload Service has ready endpoints, and the engine pod behind it passed its readiness probe |
| a pod never leaves `Pending` | a multi-pod replica waits for its whole gang: check the `PodGroup` and the cluster's free accelerator capacity; a whole-card request next to a running one needs the rollout policy to replace it, not to wait for a second device |
| the model never becomes ready | the weights volume mounts and the path in the service's model URI exists in it |

The console's runtime page reports the serving components and the model's
conditions; a condition is the first place to look before the pods.
