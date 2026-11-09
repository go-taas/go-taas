# RDMA Devices for Inference Services — Architecture and Detailed Design

| Attribute | Content |
| --- | --- |
| Feature point | An inference service can request RDMA network devices for its pods, so a distributed or PD workload exchanges tensors and KV cache over RoCE/InfiniBand instead of TCP. The devices are allocated through Kubernetes DRA; the helm chart creates the `DeviceClass`, the controller writes one `ResourceClaimTemplate` per service |
| Document scope | The problem, the DRA model, the service contract, validation, the controller's pod wiring, teardown and name reuse, the chart resources, the console surface, error codes and acceptance criteria |
| Owning modules | `services/infer` (`rdma.go`, `infer_model.go`, `service.go`, `change_publisher.go`) · `internal/controller` (`rdma.go`, `reconciler.go`) · `proto/taas/infer/v1` · `deploy/helm/go-taas` · `web` (`DeployForm`, `ServiceDetailPage`) |
| Related documents | [StormService Serving Topology](./stormservice-serving-topology.md) (the roles that share one pod template and therefore one claim template) · [Model Catalog Deployment](./model-catalog-deployment.md) (the deploy contract this extends) |
| Status | Implemented |

---

## 1. Why RDMA at all

A single-node service never needs it: the model lives in one pod and tensor-parallel workers talk over shared memory. The moment the platform spreads a replica across machines (`multinode`) or splits the engine into `prefill` and `decode` roles (`pd`), every token crosses the network: activations during forward passes, and the KV cache during disaggregated prefill.

Over TCP that link becomes the bottleneck. A 27B model in FP8 moves hundreds of megabytes of KV cache per long request between a prefill pod and a decode pod; a lossless RDMA fabric carries it an order of magnitude faster with a fraction of the CPU. Every GPU node built for serving therefore ships one or more RDMA-capable NICs, and every serving engine (vLLM, SGLang) uses them through NCCL when the container can see the device.

The platform's job is narrow but essential: **make the device appear in the pod**, on a node that has one, exactly once per pod, without handing the workload every NIC on the host.

## 2. Why DRA and not an extended resource

The historical way to expose such a device is a device plugin publishing an extended resource (`nvidia.com/gpu`-style, e.g. `rdma/rdma_shared_device_a`). That works, but it cannot express what an operator needs to select on:

| Question | Extended resource | DRA |
| --- | --- | --- |
| Is this link up? | one opaque count | `device.attributes["dra.net"].state == "up"` |
| Which card / PCI address? | no | `dra.net/pciAddress`, `dra.net/pciDevice` |
| Which NUMA node? | no | `dra.net/numaNode` |
| More than one device per pod | count, no guarantee of distinct devices | `exactly.count: N` on one request |
| Who owns the allocation | the container's resource list | a `ResourceClaim` object, visible and auditable |

The fleet already runs a DRA network driver (`dra.net`, the NVIDIA DRA network driver), so the platform uses it. This also keeps the device request **declarative**: the request is data on the service row, and the controller renders it, instead of the platform shipping a device plugin of its own.

**AD1 — The `DeviceClass` is chart infrastructure; the claim template is workload output.** Which devices a fleet offers is a fact about the cluster, not about one service, so the chart creates the class once. The number of devices per pod *is* a service property, so the controller writes a per-service `ResourceClaimTemplate` and deletes it with the service. A single chart-level template would force every service to the same device count.

**AD2 — One template per service, not one claim per service.** A `ResourceClaim` is a single allocation: every pod referencing the same claim would fight over one device set. A `ResourceClaimTemplate` is the DRA answer — every pod created from it gets its own generated claim, which is exactly what a replicated StormService needs (N replicas × M pods per replica, each with its own devices).

## 3. The contract

```proto
message RdmaConfig {
  bool enabled = 1;          // the request is off unless this is true
  int32 devices = 2;         // devices per pod; 0 reads as 1, max 8
  string device_class = 3;   // DRA DeviceClass; empty = platform default
}
```

| RPC | Field | Behaviour |
| --- | --- | --- |
| `CreateInferenceService` | `rdma` | validated before any write; stored on the row |
| `GetInferenceService` / list summary | `rdma` | always present; `enabled=false` when the service asked for nothing |
| `UpdateInferenceServiceTopology` | `rdma` | replaces the request when present; **omitted keeps the stored one**, so changing the layout never silently detaches the devices |

`UpdateInferenceServiceTopology` carries it because the request is a property of the pod, like `scheduling`: the same dialog where an operator raises `gpu_per_pod` is where they add the interconnect.

## 4. Storage

`inference_services`

| Column | Type | Meaning |
| --- | --- | --- |
| `rdma_enabled` | bool, not null, default `false` | the service requests devices |
| `rdma_devices` | int, not null, default `0` | devices per pod (0 while disabled) |
| `rdma_device_class` | varchar(253), not null, default `''` | requested class; empty = platform default |

A disabled request stores zeros rather than a stale count, so a later enable with no explicit count starts from the documented default instead of resurrecting an old value.

## 5. Validation

`NormalizeRdma` rejects, with **10324 `INFER_RDMA_INVALID`**, before any row or event exists:

- `devices` outside `1..8` (a full node with one NIC per GPU is the widest real layout; the bound catches a typo before the scheduler sees it);
- a `device_class` longer than 253 characters or not a DNS subdomain (the shape a `DeviceClass` name must have);
- a disabled request keeps neither a count nor a class.

The controller adds one runtime check the API cannot make: the `DeviceClass` must exist when the deploy is applied. A typo therefore fails the deploy with "RDMA device class \"x\" does not exist" instead of leaving pods permanently unschedulable.

## 6. Controller behaviour

On every `upsert`/`start`/`update_*` event ([`applyRdmaClaims`](../../../internal/controller/rdma.go)):

1. **Enabled** — verify the `DeviceClass` exists; create or update `<service>-rdma` (`ResourceClaimTemplate`) with one device request, `allocationMode: ExactCount`, `count: devices`, labelled `go-taas.com/service-id`, and with the same labels under `spec.metadata` so the claims DRA generates carry them too. Then every role's pod template gets
   `spec.resourceClaims[{name: rdma, resourceClaimTemplateName: <service>-rdma}]` and the engine container gets `resources.claims[{name: rdma}]`.
2. **Disabled** — delete the template so toggling RDMA off does not leave an unused device request behind.

The claim reference is added to the pod template the storm builder produces, so **every topology and every role** gets it: single, multinode and pd all build their pods from the same function.

Teardown (`stop`/`delete`) deletes the template by name with an ownership check, sweeps `resourceclaimtemplates` **and** `resourceclaims` by the service-id label, and reclaims a stale template before a reused service name creates its own. A late delete for an old service therefore cannot detach a replacement's devices.

**AD3 — The template must exist before the workload.** A pod that references a missing template is rejected at admission; creating the workload first and the template second would turn a clear controller failure into a stalled deploy.

**AD4 — Reclaim keeps name reuse safe.** Service names are platform-unique and reusable after delete. The template carries `go-taas.com/service-id`, so owned-object checks (`ownedByOtherService`) distinguish "our stale object" from "the new owner's object".

**AD5 — No DRA driver means a clear failure, not a silent fallback.** `meta.IsNoMatchError` on the `DeviceClass` read reports that the cluster has no DRA support. The service fails with a message an operator can act on; it never starts without the devices it asked for.

## 7. Chart resources

`templates/deviceclass-rdma.yaml` renders:

```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: taas-rdma
spec:
  selectors:
    - cel:
        expression: 'device.driver == "dra.net" && device.attributes["dra.net"].rdma == true && device.attributes["dra.net"].state == "up"'
```

| Value | Default | Meaning |
| --- | --- | --- |
| `rdma.enabled` | `true` | render the `DeviceClass` |
| `rdma.deviceClass` | `taas-rdma` | class name services inherit |
| `rdma.driver` | `dra.net` | DRA driver whose devices the class accepts |
| `rdma.requireLinkUp` | `true` | accept only links the node reports up |
| `rdma.selector` | `""` | full CEL override (pin cards, PCI addresses, NUMA) |
| `controller.config.controller.rdma.deviceClass` | `taas-rdma` | `CONFIG_CONTROLLER_RDMA_DEVICECLASS`; must match |

The controller's `ClusterRole` gains `get/list/watch` on `deviceclasses` and full lifecycle on `resourceclaimtemplates`/`resourceclaims`.

**AD6 — `requireLinkUp` is on by default.** Handing a pod a down port produces a service that starts and then cannot do RDMA. A driver version that does not publish `dra.net/state` needs `rdma.requireLinkUp: false` (documented in the values file); the alternative default would silently allocate dead links.

## 8. Console surface

| Surface | Control |
| --- | --- |
| Deploy form | "RDMA devices" toggle, "Devices per pod" (1–8), "Device class" (empty = platform default) |
| Service detail → topology card | `RDMA: 2 device(s) from taas-rdma` or `not requested` |
| Service detail → topology dialog | the same three controls, since the request rides with the pod shape |

## 9. Error codes

| Code | Meaning |
| --- | --- |
| 10324 | `INFER_RDMA_INVALID` — device count or class name is not usable |

Runtime failures (missing class, missing DRA API) surface as the service's `failure_reason` from the controller.

## 10. Acceptance criteria

| ID | Criterion |
| --- | --- |
| AC1 | A service created with `rdma.enabled=true, devices=2` stores both, returns them from `GetInferenceService` and the list summary, and carries them on the change event |
| AC2 | A disabled or omitted request stores no count and adds nothing to the pod spec |
| AC3 | For single, multinode and pd, every role's pod template references `<service>-rdma` and the engine container names the claim |
| AC4 | The `ResourceClaimTemplate` asks for `devices` distinct devices from the requested class (`ExactCount`), and from the platform default when none is named |
| AC5 | A missing `DeviceClass` fails the deploy with a message naming the class, and creates no workload |
| AC6 | Toggling RDMA off deletes the template; stop/delete removes it; a reused name reclaims a stale one instead of colliding |
| AC7 | `devices` outside `1..8`, or a malformed class, is refused with 10324 before any write |
| AC8 | `UpdateInferenceServiceTopology` with no `rdma` keeps the stored request; with one, it replaces it |
| AC9 | An RDMA request combined with `NCCL_IB_DISABLE=1` (shared or per role) is refused with 10324 on create, on the engine-argument update and when enabling RDMA on a service whose stored env disables the fabric |

## 11. How the engine learns the device name

The platform never passes a device name to vLLM. `rdma` in the pod spec is the
**claim's name inside the pod**, not a NIC name; `devices: 2` is a count. The
name is discovered at runtime, and what is discoverable depends on the runtimes
in play.

**What DRA does.** The driver (here `dra.net`) allocates, for example, PCI
`0000:0f:00.0` and `0000:0f:00.1`, then injects the matching
`/dev/infiniband/uverbsN` nodes into the container. `libibverbs` walks
`/sys/class/infiniband`, opens the `uverbsN` node of each entry, and silently
skips the entries it cannot open; NCCL uses what is left, named after the host
driver's own enumeration (`mlx5_N`).

**What the GPU runtime also does.** A container that requests a GPU is prepared
by the NVIDIA runtime (CDI on this fleet). Its device set includes
`/dev/infiniband/*` whenever the driver capabilities cover `network`, which is
the default. The nodes are created inside the container's own `/dev` tmpfs —
there is no `/dev/infiniband` mount to see in `mountinfo`.

**Measured on the `sd4090` node (six NICs, two GPUs).**

| Pod requests | `/dev/infiniband` | `ibv_get_device_list` |
| --- | --- | --- |
| 2 RDMA devices, no GPU (busybox probe) | `uverbs4`, `uverbs5` | 2 — `mlx5_4`, `mlx5_5` |
| 1 RDMA device + 1 GPU (real inference pod) | `uverbs0`…`uverbs5` | 6 — all openable |

So on a cluster whose GPU CDI spec carries the `network` capability, **a GPU pod
can open every NIC on the node regardless of what DRA granted it**. DRA still
guarantees the granted device is present and gives an auditable, schedulable
claim; it does not, by itself, restrict which NIC the engine picks.

**Consequence for the platform.** Device names are neither known before
allocation nor required by the engine, and they are not stable across pods or
nodes, so nothing is hard-coded. NCCL's default — use every device it can open —
is also the right default for a rail-optimized host. Pinning is an engine
concern, configured through the service's `env`:

| Variable | Effect |
| --- | --- |
| `NCCL_IB_HCA=mlx5_4` | use only that device (`mlx5_4:1` pins one port) |
| `NCCL_DEBUG=INFO` | the line `NET/IB : Using [0]mlx5_4:1/IB` names the device actually chosen |
| `NCCL_IB_DISABLE=1` | disables the fabric and silently falls back to TCP — **refused by the platform** on an RDMA service (10324) |

**AD7 — `NCCL_IB_DISABLE` is rejected, not ignored.** It is the only setting
that makes an RDMA request pointless without any error: the service starts, the
devices are allocated, and every transfer goes over TCP. `validateRdmaEnv`
refuses it (truthy values, shared or per-role) on create, on the engine-argument
update and when RDMA is enabled on a service whose stored env carries it.

**Making the grant exclusive.** When an operator needs the pod to see *only* the
granted NIC, the lever is the node's NVIDIA CDI/device set, not this chart: drop
`network` from the driver capabilities (or generate a CDI kind without
`/dev/infiniband`). Until then, the honest statement is "the granted device is
guaranteed and the request is auditable; the visible set is the node's".
