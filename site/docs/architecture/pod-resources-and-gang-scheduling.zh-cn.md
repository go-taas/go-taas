# 每 Pod 资源、优先级与 Volcano Gang 调度 —— 架构与详细设计

| 属性 | 内容 |
| --- | --- |
| 功能点 | 所有推理拓扑都可以指定每个 Pod 的资源规格（加速卡数、CPU、内存）与优先级（高/中/低）；平台把一个副本的 Pod 作为 Volcano gang 整体调度，并允许高优先级抢占低优先级。角色参数改用 Tab 切换而非纵向堆叠 |
| 文档范围 | 接口与存储字段、校验规则、控制器写入的 Pod 规格、Volcano PodGroup、优先级类、抢占、控制台表单与 Tab、配置、错误码与验收标准 |
| 所属模块 | `services/infer`（`scheduling.go`、`service.go`、`change_publisher.go`） · `internal/controller`（`podgroup.go`、`reconciler.go`、`stormservice.go`） · `pkg/config`（`controller.scheduling`） · `web`（`DeployForm`、`ServiceDetailPage`） · helm（`rbac-controller`、values） |
| 关联文档 | [StormService 服务拓扑](./stormservice-serving-topology.zh-cn.md)（一个副本由哪些角色组成） · [加速卡清单](./accelerator-inventory.zh-cn.md)（请求所指的设备） · [推理引擎参数模板](./engine-parameter-templates.zh-cn.md)（部署表单的另一类输入） |
| 状态 | 已实现 |

---

## 1. 为什么需要

此前一个推理 Pod 只申请一张卡，其他一概不申请。集群调度器随后把 Pod 放到任何有空闲 GPU 的节点上，对 CPU 和内存不作任何承诺，也没有任何方式表达「哪个负载更重要」。单租户、单模型的集群没问题；共享集群上则会出现两类故障：

- **Pod 装不下自己的模型。** 一个 7B 模型需要权重加 KV cache 的内存；没有内存请求的 Pod 可能落到空闲内存小于引擎实际分配量的节点，引擎在加载数分钟后被 OOM 杀掉。
- **半个副本长期悬挂。** Kubernetes 逐个调度 Pod。PD 分离需要两个角色同时就绪才能服务，多机推理需要所有分片到齐后引擎才能启动（它们要会合）。默认调度器在集群资源紧张时会痛快地绑定半个 gang：已绑定的那一半占着 GPU 却无法服务，pending 的那一半则在等待被它自己阻塞的容量。没有人手动删除就无法恢复。

`PodScheduling` 就是缺失的输入 —— 一个 Pod 占几张卡、需要多少 CPU 与内存、优先级如何 —— 而 Volcano 负责把「这些 Pod 属于同一个整体」与「这个更重要」变成真正的调度决策。

## 2. 接口与存储字段

```proto
message PodScheduling {
  int32  gpu_per_pod = 1;   // 每 Pod 卡数（0 表示 1）
  double cpu_cores   = 2;   // 0 表示不申请
  int64  memory_mib  = 3;   // 0 表示不申请
  string priority    = 4;   // high | normal | low（空 = normal）
}
```

`CreateInferenceService` 与 `UpdateInferenceServiceTopology` 都接受它（拓扑更新时省略表示保留原值），并在每个 `InferenceServiceSummary` 上投影，因此单机、多机、PD 分离三种拓扑都能设置，读取方也都能看到。

`inference_services` 新增 `gpu_per_pod`、`cpu_milli`、`memory_mib`、`priority`。

**AD1 —— 数据库存毫核而不是浮点。** 接口用 double 表达核数，因为表单里就是这样的输入；记录里存毫核，使 `0.5` 核能在表单、接口、数据库与 Pod 规格（`500m`）之间精确往返。用浮点列迟早会与自己不一致。

**AD2 —— 0 表示「不申请」。** CPU 或内存为 0 时该字段完全不写入 Pod，这正是历史形态：从不设置新字段的安装，部署结果与以前完全一致。控制台默认填 4 核 / 16 GiB，因为推理引擎确实需要，但接口不会凭空造出一个请求。

**AD3 —— 只设 request，不设 limit。** CPU 与内存都是 request。limit 会限制加载权重时合理突发的进程，把一次负载尖峰变成限流或 OOM kill；而 request 才是调度器判断「Pod 放得下」的依据。

**AD4 —— `gpu_per_pod` 切分副本的总卡数。** 一个副本的张量×流水线并行度就是它跨的卡数，多 Pod 副本按每 Pod `gpu_per_pod` 张切分：8 张卡、每 Pod 4 张 → 2 个 Pod。切分必须整除 —— 不整除会给引擎一个它看不见的 world size，vLLM 启动时直接拒绝 —— 因此控制面返回 10319 拒绝，控制台也会在提交前拦下。单机副本永远是一个 Pod，申请多少就给它多少。

## 3. 控制器写出的内容

`podResources` 把事件转换成容器资源：按请求数量申请加速卡的扩展资源（`nvidia.com/gpu`、`iluvatar.ai/vgpu`、`metax-tech.com/gpu`），CPU 与内存非 0 时追加 request。`workerPods` 用 `gpu_per_pod` 切分多 Pod 角色，使 `podGroupSize` 仍然是「每台机器一个 Pod」，与会合命令的 `--nnodes` 一致。

一个服务的所有角色共享同一资源规格：PD 服务里的 prefill 与 decode Pod 差异在引擎参数，而不在被承诺的硬件上。按角色的引擎参数是另一维度（见拓扑文档）。

## 4. Volcano

**AD5 —— 每个服务一个 PodGroup，`minMember` 为一个副本的 Pod 数。** Pod 通过 `scheduling.k8s.io/group-name` 注解加入组；Volcano 在 `minMember` 个 Pod 都能放下之前不绑定其中任何一个。`minMember` 取的是**一个副本**的 Pod 数（单机/多机为一个角色，PD 为 prefill 与 decode 的 Pod 组之和），而不是整个服务：一次性对全部副本做 gang 会让任何扩容死锁 —— 新副本必须等「所有副本」的容量才能启动。按副本 gang 保住了真正重要的性质（副本不会半启动），同时扩容可以逐副本推进。

**AD6 —— `minResources` 是一个副本的预留。** 组上携带一个副本的请求总和（卡、CPU、内存），Volcano 用它做队列核算与抢占判断。低估会让这个 gang 看起来比实际便宜。

**AD7 —— 不配置即关闭。** `controller.scheduling.schedulerName` 默认为空，只有设为 `volcano` 才会启用（不写 PodGroup、不加注解、Pod 使用集群默认调度器）。没人读取的 PodGroup 只是噪声，而没有 Volcano 的安装也不应该给一个不存在的调度器打注解。随附的 chart 设置为 `volcano` 并配好三个优先级类，因为本平台需要 gang 调度。

**AD8 —— 优先级即 PriorityClass。** `high` / `normal` / `low` 映射到 `controller.scheduling.priorityClassHigh|Normal|Low`（集群中的 `high-priority` / `normal-priority` / `low-priority`）。未映射的优先级会让 Pod 保持集群默认类，而不是指向一个可能不存在的类 —— `priorityClassName` 不存在会导致 Pod 在准入阶段被拒绝，那比缺少优先级提示更糟。同一个值也会写到 PodGroup 上，Volcano 正是据此抢占：高优先级 gang 可以驱逐低优先级 gang 而不必等容量，被驱逐服务的 Pod 会在容量释放后回来。

已在真实集群验证：服务的 Pod 带有 `schedulerName: volcano`、`priorityClassName: high-priority`、`scheduling.k8s.io/group-name` 注解以及 `requests: cpu 2, memory 8Gi, nvidia.com/gpu 1`；PodGroup 在没有空闲 GPU 时报告 `minMember: 1` 与 `Inqueue / NotEnoughResources`，空闲后转为 `Running`，Pod 由 Volcano 完成绑定。

## 5. 控制台

**部署表单。** 拓扑区下方是「每个 Pod 的资源与优先级」（每 Pod 卡数、CPU 核数、内存 GiB、优先级），其后是参数 Tab。

**AD9 —— 角色参数用 Tab，而不是层层堆叠。** PD 服务有三套参数（共用、prefill、decode）；三个同时展开的表单会把提交按钮挤出屏幕，运维人员也会搞不清自己在编辑哪一段。Tab 条在它们之间切换，屏幕上始终只有一个表单。带值的 Tab 会显示圆点，因此藏在非激活 Tab 里的覆盖值依然可见；切换拓扑时会丢弃已不存在角色的 Tab 并回落到共用 Tab。

**服务详情。** 拓扑卡片展示每 Pod 规格（`{gpus} 卡 · {cpu} CPU · {memory} GiB`）与优先级，布局弹窗可同时编辑两者 —— 重新部署正是运维人员意识到某个 Pod 需要更多内存或更高优先级的时刻。

## 6. 配置

```yaml
controller:
  scheduling:
    schedulerName: volcano            # 空 = 集群默认调度器，不启用 gang
    priorityClassHigh: high-priority
    priorityClassNormal: normal-priority
    priorityClassLow: low-priority
    queue: ""                         # Volcano 队列，空则用默认队列
```

控制器的 ClusterRole 增加 `scheduling.volcano.sh/podgroups`（get/list/watch/create/update/patch/delete）。

## 7. 错误

| 错误码 | 名称 | 触发条件 |
| --- | --- | --- |
| 10323 | `INFER_SCHEDULING_INVALID` | `gpu_per_pod` 不在 1–8、CPU 为负或超过 128 核、内存超过 1 TiB、优先级不在 high/normal/low |
| 10319 | `INFER_TOPOLOGY_INVALID` | `gpu_per_pod` 不能整除 `tensor_parallel_size × pipeline_parallel_size`，或大于该总数 |

## 8. 验收标准

1. 创建时不带 `scheduling` 的服务与以前完全一致：每 Pod 一卡、无 CPU/内存请求、无优先级类。
2. 带 `gpu_per_pod: 1, cpu_cores: 2, memory_mib: 8192, priority: high` 时，Pod 申请 `nvidia.com/gpu: 1`、`cpu: 2`、`memory: 8Gi`，并带有高优先级类。
3. 卡、CPU、内存都只是 request，不设置 CPU/内存 limit。
4. 多机副本 TP×PP = 8 且 `gpu_per_pod: 2` 时运行 4 个 Pod；不能整除时返回 10319。
5. 配置 Volcano 后，Pod 带 `schedulerName: volcano` 与分组注解，推理命名空间中出现 `minMember` 等于一个副本 Pod 数的 PodGroup。
6. 未配置 Volcano 时不写 PodGroup，也不加分组注解。
7. PD 副本的 gang 同时计入 prefill 与 decode 的 Pod。
8. 删除服务会同时删除其 PodGroup。
9. 部署表单的参数 Tab 可在共用参数与各角色之间切换，服务详情弹窗可往返保持每 Pod 规格。
