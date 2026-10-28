# StormService 服务拓扑 —— 架构与详细设计

| 属性 | 内容 |
| --- | --- |
| 功能点 | 所有推理任务通过 AIBrix `StormService` 承载：单机推理、分布式推理（张量/流水并行跨 Pod 组）、PD 分离推理（Prefill + Decode 双角色）；扩缩容只用 AIBrix `PodAutoscaler`，不再使用 HPA；镜像页展示当前配置的 Harbor 域名与项目；用户端左下角的下拉框改为组织 + 项目的只读展示 |
| 文档范围 | 架构与详细设计：服务拓扑的数据模型、创建/更新接口、控制器侧的 StormService 期望状态、自动扩缩容目标切换、Harbor 配置投影，以及两端控制台的交互 |
| 所属模块 | `services/infer`（拓扑校验、创建/更新、变更事件） · `internal/controller`（`stormservice.go`、`reconciler.go`、`modelclaim.go`、`engine_ready.go`） · `services/image`（`GetImageRegistryConfig`） · `web` 管理端（`DeployForm`、`ServiceDetailPage`、`InferenceServicesPage`、`ImagesPage`）与用户端（`OrgIdentity`） · `pkg/errors`（10319） |
| 关联文档 | [分布式推理运行时（AIBrix 集成）](./distributed-inference-runtime.zh-cn.md)（本文复用的 `ModelClaim` 路径） · [模型目录与一键部署](./model-catalog-deployment.zh-cn.md)（部署表单与服务页面） · [推理自动扩缩容](./inference-autoscaling.zh-cn.md)（被本文取代的 HPA 路径） · [镜像管理](./image-management.zh-cn.md)（镜像清单与导入流程） |
| 状态 | 已实现 |

---

## 1. 概述

改造前，控制器为每个推理服务维护一个普通 `apps/v1 Deployment` 加一个 ClusterIP `Service`；开启自动扩缩容时再创建一个 Kubernetes `HorizontalPodAutoscaler`。Deployment 只能表达“N 个相同的 Pod”：它无法把一个副本的多个分片作为一个整体调度（gang scheduling），无法在一个服务下运行两种不同角色（Prefill 与 Decode），HPA 也无法伸缩非内置工作负载。

AIBrix 恰好提供了缺失的这套原语：**`StormService`**（`orchestration.aibrix.ai/v1alpha1`）。一个 storm 拥有一个*角色集合*；每个角色运行 `replicas` 个 Pod 组、每组 `podGroupSize` 个 Pod，`spec.replicas` 是角色组副本数。一个对象即可表达平台支持的三种拓扑：

| 拓扑 | Storm 形态 | 每个副本的 Pod 数 |
| --- | --- | --- |
| `single` | 单角色 `worker`，`podGroupSize = 1` | 1 |
| `distributed` | 单角色 `worker`；Pod 内把模型分片到 TP × PP 张卡 | 1（申请 TP × PP 张卡） |
| `pd` | `prefill` 与 `decode` 两个角色，各自副本数与 TP × PP 张卡 | prefill + decode |

现在推理服务**始终**是一个 StormService。Deployment 构建逻辑仅保留给没有 dynamic client 的控制器（单元测试、未安装 CRD 的环境），以及用于删除旧控制器遗留的 Deployment。

## 2. 架构决策

**AD1 —— 每个服务一个承载对象。** storm 的 `spec.replicas` 就是服务的 `replicas`；PD 的一个 storm 副本等于一对 prefill + decode。

**AD1b —— 并行副本拥有独立的共享内存。** 引擎的并行 worker 通过 `/dev/shm` 交换张量；Kubernetes 默认只给 64 MiB，引擎会以 “Insufficient space in /dev/shm” 退出（已在真实集群复现）。因此 `TP × PP > 1` 的副本会挂载一个 2 GiB 的内存型 `emptyDir` 到 `/dev/shm`。

**AD1a —— 并行发生在 Pod 内部，而不是跨 Pod。** 张量/流水并行的副本以**一个** Pod 运行，并按分片数申请设备（`TP × PP` 张卡），`podGroupSize` 保持 1。跨 Pod 的分片组需要每个 rank 的会合信息（world size、node rank、master address），StormService 控制器不会注入这些信息；而 vLLM 在 world size 大于 Pod 可见设备数时会拒绝启动——这在真实集群上已复现（按 Pod 分片时报错 “World size (2) is larger than the number of available GPUs (1)”）。因此多节点推理需要用户自行提供 rank 配置，不在本次范围内。`Service`（`<name>-svc`）、`ModelClaim` 的 Pod 选择器与状态跟随器仍然按共享资源标签（`go-taas.com/service-id`、`app`、`app.kubernetes.io/managed-by`）选中 Pod，角色模板会带上这些标签，因此 Pod 下游的链路完全不变。

**AD2 —— 拓扑是数据，不是代码。** `inference_services.topology` 存 `single` / `distributed` / `pd`，同时存 `tensor_parallel_size`、`pipeline_parallel_size`、`prefill_replicas`、`decode_replicas`。读取时空值归一化为 `single`（即历史数据行的语义）。接口契约使用同一套小写名称，控制台与后端使用同一词汇。

**AD3 —— 任何写入之前先校验。** `NormalizeTopology` 拒绝未知形态、越界的并行度（`1..100`，且 `TP × PP ≤ 100`）、实际只有一个 Pod 的“分布式”请求（`TP × PP < 2`）、不支持分离的引擎上的 PD（只允许 vLLM/SGLang）、以及越界的角色副本数；错误码 **10319 INFER_TOPOLOGY_INVALID**。校验失败不会发布任何期望状态。

**AD4 —— 只下发引擎无法自行推导的参数。** 当用户未显式设置时，控制器追加 `--tensor-parallel-size` / `--pipeline-parallel-size`；PD 场景追加角色标记：vLLM 使用 `--kv-transfer-config`（prefill 为 `kv_producer`，decode 为 `kv_consumer`，默认连接器 `PyNcclConnector`），SGLang 使用 `--disaggregation-mode prefill|decode`。用户显式给出的引擎参数始终优先，因此集群若使用其它 KV 连接器，可通过现有的高级参数表单配置。

**AD5 —— 自动扩缩容只用 AIBrix PodAutoscaler。** `PodAutoscaler` 的目标是 **StormService**（`scaleTargetRef.kind: StormService`），CRD 接受该目标且 AIBrix 控制器可正常驱动（已在真实集群验证）。每次协调都会清理旧控制器遗留的 HPA；PodAutoscaler 写入失败按部署失败上报，而不是静默固定副本数——控制台不会把“没有任何组件在扩缩容”的服务显示为已自动扩缩容。角色级扩缩容（`subTargetSelector`）被放弃：准入 Webhook 禁止与 `HPA` 策略同时使用，而其它策略需要本部署未定义的额外基础设施。

**AD6 —— 更新是规格变更。** `UpdateInferenceServiceTopology`（`POST /api/v1/admin/inference-services/{id}:update-topology`）重写拓扑、把服务置回 `pending` 并重新发布期望状态；storm 会以新角色滚动更新，服务 ID 与访问地址保持不变。`replicas = 0` 表示保留当前副本数。

**AD7 —— 迁移是隐式的。** 每次 upsert 时控制器都会先删除同名遗留 Deployment，避免其 Pod 与 storm 的 Pod 同时被 Service 选中；`applyDelete` 会同时清理两者。运行中的服务无需手工迁移步骤。

**AD8 —— 就绪判定面向整个 Pod 组。** 分布式或 PD storm 只有一个 Pod 暴露 API（rank 0 / decode 角色），因此就绪跟随器在任意 Pod 被调度时报 `starting`，在任意 Pod 响应引擎健康检查时报 `running`；任一 Pod 永久无法启动则整个部署失败。

## 3. 数据模型

`inference_services` 新增五列（GORM AutoMigrate，无手写 DDL）：

| 列 | 类型 | 含义 |
| --- | --- | --- |
| `topology` | `varchar(16)`，默认 `single` | 形态名称 |
| `tensor_parallel_size` | `int`，默认 `1` | 单个副本的张量并行度 |
| `pipeline_parallel_size` | `int`，默认 `1` | 单个副本的流水并行度 |
| `prefill_replicas` | `int`，默认 `0` | PD 的 Prefill 副本数 |
| `decode_replicas` | `int`，默认 `0` | PD 的 Decode 副本数 |

历史数据读作 `single` + `1/1`，无需回填。

## 4. 接口

`CreateInferenceServiceRequest` 新增 `topology`、`tensor_parallel_size`、`pipeline_parallel_size`、`prefill_replicas`、`decode_replicas`；`InferenceServiceSummary` 同步返回这些字段，供列表与详情展示。

```proto
rpc UpdateInferenceServiceTopology(UpdateInferenceServiceTopologyRequest)
    returns (UpdateInferenceServiceTopologyResponse) {
  option (google.api.http) = {
    post: "/api/v1/admin/inference-services/{service_id}:update-topology"
    body: "*"
  };
}
```

更新校验顺序：服务存在（10301）→ 未终止（10303）→ 可读取镜像 → 拓扑合法（10319）→ 副本数在范围内（10305）。变更事件携带拓扑信息，控制器无需回查数据库。

`ImageService.GetImageRegistryConfig`（`GET /api/v1/admin/image-registry-config`）投影已配置的 Harbor 仓库：`configured`、`hostname`、`project`、`example_reference`。不暴露任何凭据；未配置时返回 `configured=false`，控制台据此禁用导入操作。

## 5. 控制器映射

`buildStormService` 组装：

- metadata：名称为服务名，携带资源标签与网关路由标签/注解；
- `spec.replicas` = `clampReplicas(evt.Replicas)`，`stateful: false`，`updateStrategy: RollingUpdate(maxUnavailable: 1)`；
- `spec.selector.matchLabels` = 共享资源标签（不可变）；
- `spec.template.spec.roles[]`：每项包含 `name`、`replicas`、`podGroupSize` 和一个 `template`；`template.labels` 追加 `go-taas.com/role`，`template.spec` 是把引擎 Pod 规格经 JSON 转换得到的非结构化对象（利用 k8s 类型的 `omitempty`，避免把空默认值写进 CRD）。

`applyStormService` 创建或更新，并做有界冲突重试（AIBrix 控制器会并发写 status）。关闭自动扩缩容时，`pinStormServiceReplicas` 通过 merge patch 写 `spec.replicas`。`deleteStormService` 忽略 not-found。

## 6. 控制台

**创建**（`DeployForm`，内联面板）：新增「服务拓扑」区块：形态下拉、TP/PP 输入（分布式与 PD）、Prefill/Decode 副本数（PD），并给出一行汇总说明将要创建的 Pod 数（`replicas × 角色 × TP × PP`）。同样的规则在前端先校验一次，服务端仍是权威。

**更新**（`ServiceDetailPage`）：新增「服务拓扑」卡片，展示形态、并行度与 PD 角色副本数，并提供「调整规格」对话框调用 `:update-topology`。列表页新增「拓扑」列（`分布式 · TP 4 / PP 2`、`PD · P2/D3`）。

**镜像**（`ImagesPage`）：新增「导入仓库（Harbor）」卡片，展示配置的域名、项目与导入后的引用格式；导入对话框预览具体目标（`<host>/<project>/<image>`）。

**用户端**（`UserShell`）：组织下拉框替换为 `OrgIdentity` 只读区块，展示账号所属组织（显示名 + ID）与其项目。用户最多属于一个组织和一个项目，因此没有可切换的内容；加入组织/项目在申请页面完成。

## 7. 错误码

| 错误码 | 名称 | 触发场景 |
| --- | --- | --- |
| 10319 | `INFER_TOPOLOGY_INVALID` | 未知形态、并行度越界、“分布式”只有 1 个 Pod、非 vLLM/SGLang 引擎使用 PD、角色副本数越界 |
| 10305 | `INFER_REPLICAS_INVALID` | 更新时副本数不在 `1..100` |
| 10303 | `INFER_SERVICE_STATE_INVALID` | 更新已终止的服务 |
| 10301 | `INFER_SERVICE_NOT_FOUND` | 更新不存在的服务 |

## 8. 验证

已在目标集群（`sd4090`，AIBrix 位于 `aibrix-system`）验证：

- 用双角色且 `podGroupSize: 2` 的探针 storm 验证了 Pod 数恰为 `replicas × Σ(role.replicas × podGroupSize)`，确认了本文依赖的角色语义；
- 以 `StormService` 为目标的 `PodAutoscaler` 可被接受并正常协调（`actualScale`、`ReconcilingScaleDiff`）；仅当 `subTargetSelector` 与 `HPA` 策略同时使用时才被拒绝；
- 单机服务可正常部署、进入 `running` 并通过网关提供服务；
- 控制台的拓扑卡片、规格编辑、拓扑列、仓库卡片与身份区块均可正常渲染。

如实说明限制：PD 与多 Pod 分布式推理需要多张 GPU（或多个节点）才能真正启动；验证所用集群只有一张 GPU，因此这两种形态由单元测试/FVT 与 CRD 语义探针覆盖，而非真实的端到端生成。KV 传输连接器默认使用 vLLM 的 `PyNcclConnector`；若集群使用其它连接器，通过高级引擎参数传入 `kv-transfer-config`，显式参数始终优先。

## 9. 验收标准

- **AC1** 以 `topology=distributed, TP=4, PP=2` 创建服务后，拓扑被持久化并发布，storm 只有一个 `worker` 角色且 `podGroupSize: 1`，Pod 申请 8 张卡。
- **AC2** 以 `topology=distributed, TP=PP=1` 创建时返回 10319，且不发布任何状态。
- **AC3** 创建 PD 服务后生成 `prefill`、`decode` 两个角色，副本数符合请求，每个 Pod 申请 TP × PP 张卡，引擎命令行携带对应角色的 KV 传输模式。
- **AC4** `:update-topology` 使服务回到 `pending`、重新发布 upsert，并保持服务 ID 不变。
- **AC5** 开启自动扩缩容的服务获得 `scaleTargetRef.kind = StormService` 的 `PodAutoscaler`，且不创建 HPA；关闭后该对象被删除并固定 storm 副本数。
- **AC6** 删除服务后 storm、Service、`ModelClaim`、路由与扩缩容器一并清理。
- **AC7** `GET /api/v1/admin/image-registry-config` 返回配置的域名与项目；未配置时 `configured=false`。
- **AC8** 用户端侧边栏以文本形式展示组织与项目，不提供下拉选择。
