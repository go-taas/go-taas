# Per-Pod Resources, Priority and Volcano Gang Scheduling — Architecture and Detailed Design

| Attribute | Content |
| --- | --- |
| Feature point | Every inference topology can be given a per-pod resource shape (accelerator cards, CPU, memory) and a priority (high/normal/low); the platform schedules the pods of one replica as a Volcano gang and lets a higher priority preempt a lower one. Role parameters switch through tabs instead of stacking |
| Document scope | The wire and stored shape, validation, the pod spec the controller writes, the Volcano PodGroup, priority classes, preemption, the console forms and tabs, configuration, errors and acceptance criteria |
| Owning modules | `services/infer` (`scheduling.go`, `service.go`, `change_publisher.go`) · `internal/controller` (`podgroup.go`, `reconciler.go`, `stormservice.go`) · `pkg/config` (`controller.scheduling`) · `web` (`DeployForm`, `ServiceDetailPage`) · helm (`rbac-controller`, values) |
| Related documents | [StormService Serving Topology](./stormservice-serving-topology.md) (the roles a replica is made of) · [Accelerator Inventory](./accelerator-inventory.md) (the devices the requests name) · [Engine Parameter Templates](./engine-parameter-templates.md) (the other deploy-form input) |
| Status | Implemented |

---

## 1. Why

A serving pod used to ask for exactly one device and nothing else. The cluster's scheduler then placed it wherever a GPU was free, with no promise about CPU or memory and no way to say which workload matters more. On a single-tenant cluster with one model that is fine; on a shared fleet it produces two failures:

- **A pod that cannot hold its own model.** A 7B model needs its weights plus KV cache in RAM; a pod with no memory request can land on a node whose free memory is smaller than what the engine will allocate, and the engine is OOM-killed after loading for minutes.
- **Half a replica, indefinitely.** Kubernetes places pods one at a time. A PD pair needs both roles up to serve, and a multi-machine replica needs every shard before the engine can start (they rendezvous). The default scheduler happily binds half a gang on a full cluster: the bound half holds GPUs while serving nothing, and the pending half waits for capacity that the bound half is itself blocking. Nothing recovers without a human deleting the workload.

`PodScheduling` is the missing input — how many devices one pod takes, how much CPU and memory it needs, and its priority — and Volcano is what turns "these pods belong together" and "this one matters more" into a scheduling decision.

## 2. The wire and stored shape

```proto
message PodScheduling {
  int32  gpu_per_pod = 1;   // devices per pod (0 = 1)
  double cpu_cores   = 2;   // 0 = no request
  int64  memory_mib  = 3;   // 0 = no request
  string priority    = 4;   // high | normal | low (empty = normal)
}
```

It is accepted on `CreateInferenceService`, on `UpdateInferenceServiceTopology` (omitted = keep the stored shape) and projected on every `InferenceServiceSummary`, so every topology — single, multinode, PD — can set it and every reader sees it.

`inference_services` gains `gpu_per_pod`, `cpu_milli`, `memory_mib`, `priority`.

**AD1 — Milli-cores, not floats, in the database.** The wire carries cores as a double because that is what a form has; the row stores milli-cores so `0.5` cores round-trips exactly through the form, the API, the database and the pod spec (`500m`). A float column would eventually disagree with itself.

**AD2 — 0 means "not requested".** A zero CPU or memory value leaves the field off the pod entirely, which is the historical shape: an installation that never sets the new fields deploys exactly what it deployed before. The console defaults to 4 cores and 16 GiB because a serving engine does need them, but the API does not invent a request.

**AD3 — Requests, not limits.** CPU and memory are requests. A limit would cap a process that legitimately bursts while loading weights and turning a load spike into throttling or an OOM kill; the request is what the scheduler needs in order to place the pod where it fits.

**AD4 — `gpu_per_pod` divides the replica's devices.** A replica shards across `tensor_parallel_size × pipeline_parallel_size` devices, and a multi-pod replica splits them into pods of `gpu_per_pod` devices: 8 devices with 4 per pod is two pods. The split must be exact — an uneven one would give the engine a world size its pods cannot see, which vLLM refuses at startup — so the control plane refuses the combination (10319) and the console refuses it before sending. A single-node replica is one pod and takes whatever it is given.

## 3. What the controller writes

`podResources` turns the event into the container's resource requirements: the accelerator's extended resource (`nvidia.com/gpu`, `iluvatar.ai/vgpu`, `metax-tech.com/gpu`) with the requested count, plus `cpu` and `memory` requests when non-zero. `workerPods` divides a multi-pod role by `gpu_per_pod`, so `podGroupSize` stays "one pod per machine" as the rendezvous command expects (`--nnodes`).

Every role of a service gets the same shape: a prefill and a decode pod of one PD service differ in engine arguments, not in the hardware they were promised. Per-role engine parameters are a separate axis (see the topology document).

## 4. Volcano

**AD5 — One PodGroup per service, `minMember` = one replica.** Pods join a group through the `scheduling.k8s.io/group-name` annotation; Volcano binds none of them until `minMember` fit. The group's `minMember` is the number of pods *one replica* spans (one role for single/multinode, prefill + decode pod groups for PD), not the whole service: gang-scheduling every replica at once would deadlock any scale-up, because a new replica could not start until capacity for all of them appeared. Gang per replica keeps the property that matters — a replica never half-starts — while a scale-up proceeds one replica at a time.

**AD6 — `minResources` is the replica's reservation.** The group carries the sum of one replica's requests (cards, CPU, memory), which is what Volcano uses for queue accounting and for the preemption decision. An understated value would make the gang look cheaper than it is.

**AD7 — Off unless configured.** `controller.scheduling.schedulerName` is empty by default; only `volcano` turns the path on (no PodGroup is written, no annotation is added, the pod keeps the cluster's default scheduler). A PodGroup nobody reads is noise, and an installation without Volcano must not annotate pods for a scheduler that is not there. The shipped chart sets `volcano` plus the three priority classes, because the platform requires gang scheduling.

**AD8 — Priority is a PriorityClass.** `high` / `normal` / `low` map to `controller.scheduling.priorityClassHigh|Normal|Low` (the cluster's `high-priority` / `normal-priority` / `low-priority`). An unmapped priority leaves the pod with the cluster default class rather than naming a class that may not exist — a pod whose `priorityClassName` does not exist is rejected at admission, which would be a worse failure than a missing hint. The same value goes on the PodGroup, which is what Volcano preempts on: a high-priority gang may evict a low-priority one instead of waiting for capacity, and the evicted service's pods come back when capacity frees.

Verified on the live cluster: the service's pod carried `schedulerName: volcano`, `priorityClassName: high-priority`, the `scheduling.k8s.io/group-name` annotation and `requests: cpu 2, memory 8Gi, nvidia.com/gpu 1`; its PodGroup reported `minMember: 1` and `Inqueue / NotEnoughResources` while no GPU was free, then `Running` once one was, and its pod was bound by Volcano.

## 5. Console

**Deploy form.** A *Per-pod resources and priority* section (cards per pod, CPU cores, memory GiB, priority) sits under the topology block, followed by the parameter tabs.

**AD9 — Role parameters are tabs, not a stack.** A PD service has three parameter sets (shared, prefill, decode); three open forms at once push the submit button off screen, and the operator loses track of which section they are editing. A tab strip switches between them and keeps one form on screen. A dot marks a tab that carries values, so an override hidden behind an inactive tab is still visible as "something is set here"; switching layout drops the tab of a role that no longer exists and falls back to the shared tab.

**Service detail.** The topology card shows the per-pod shape (`{gpus} cards · {cpu} CPU · {memory} GiB`) and priority, and the layout dialog edits both — a redeploy is exactly when an operator realizes a pod needs more memory or a higher priority.

## 6. Configuration

```yaml
controller:
  scheduling:
    schedulerName: volcano            # "" = cluster default, no gang
    priorityClassHigh: high-priority
    priorityClassNormal: normal-priority
    priorityClassLow: low-priority
    queue: ""                         # Volcano queue, default if empty
```

The controller's ClusterRole gains `scheduling.volcano.sh/podgroups` (get/list/watch/create/update/patch/delete).

## 7. Errors

| Code | Name | When |
| --- | --- | --- |
| 10323 | `INFER_SCHEDULING_INVALID` | `gpu_per_pod` outside 1–8, negative or > 128 cores, memory over 1 TiB, priority outside high/normal/low |
| 10319 | `INFER_TOPOLOGY_INVALID` | `gpu_per_pod` does not divide `tensor_parallel_size × pipeline_parallel_size`, or exceeds it |

## 8. Acceptance criteria

1. A service created without `scheduling` deploys exactly as before: one device per pod, no CPU/memory request, no priority class.
2. With `gpu_per_pod: 1, cpu_cores: 2, memory_mib: 8192, priority: high` the pod requests `nvidia.com/gpu: 1`, `cpu: 2`, `memory: 8Gi` and carries the high PriorityClass.
3. Cards, CPU and memory are requests only; no CPU or memory limit is set.
4. A multi-machine replica with TP×PP = 8 and `gpu_per_pod: 2` runs 4 pods; a `gpu_per_pod` that does not divide it is refused with 10319.
5. With Volcano configured, the pods carry `schedulerName: volcano` and the group annotation, and a PodGroup with `minMember` = one replica's pods exists in the inference namespace.
6. Without Volcano configured, no PodGroup is written and no group annotation is added.
7. A PD replica's gang counts prefill *and* decode pods.
8. Deleting the service deletes its PodGroup.
9. The deploy form's parameter tabs switch between the shared set and each role, and the service detail dialog round-trips the per-pod shape.
