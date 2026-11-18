# LLMInferenceService 服务拓扑 — 架构与详细设计

| 属性 | 内容 |
| --- | --- |
| 特性点 | 每个推理服务就是一份 KServe `LLMInferenceService`：单机、多机（每个 worker 一个 Pod）与 PD 分离（prefill + decode，各自独立调参）；自动扩缩容由 KServe autoscaling 承担，不再为每个服务建 HPA |
| 文档范围 | 架构与详细设计：服务拓扑的数据模型、创建/更新接口、控制器的期望状态、Pod 覆盖方式、路由、自动扩缩容、删除与本次实际执行的验证 |
| 归属模块 | `services/infer`（拓扑校验、创建/更新、变更事件） · `internal/controller`（`serving.go`、`routing.go`、`reconciler.go`、`podgroup.go`） · `services/image`（`GetImageRegistryConfig`） · `web` 管理控制台（`DeployForm`、`ServiceDetailPage`、`InferenceServicesPage`、`ImagesPage`）与用户控制台（`OrgIdentity`） · `pkg/errors`（10319） |
| 关联文档 | [服务运行时使用指南](../guides/serving-runtime.md)（实际创建了什么、一个请求怎么到达 Pod） · [模型目录与一键部署](./model-catalog-deployment.md)（部署表单与服务页面） · [推理自动扩缩容](./inference-autoscaling.md)（服务上的扩缩容策略） · [镜像管理](./image-management.md)（镜像清单与导入流程） |
| 状态 | 已实现 |

---

## 1. 概述

此前每个推理服务是一个 `apps/v1 Deployment` 加一个 ClusterIP `Service`，开启自动扩缩容时再加一个 `HorizontalPodAutoscaler`。Deployment 只能表达“N 个完全相同的 Pod”：它无法把一个副本的多个分片放到各自机器上，无法在一个服务下跑两种角色（prefill 与 decode），HPA 也无法把非内置工作负载作为扩缩目标。

因此平台对每个服务只调和**一个**对象：KServe 的 `LLMInferenceService`。对象里带着引擎的 Pod 模板，平台因此仍然完全掌控容器；围绕容器的一切交给 KServe：多 Pod 副本的 leader-worker set（LWS）、prefill/decode 成对、工作负载 `Deployment`、暴露它的 `Service`，以及自动扩缩器。

| 拓扑 | 对象声明的内容 | 每个副本的 Pod 数 |
| --- | --- | --- |
| `single` | `spec.replicas` 与一个 `main` 模板；Pod 独占 `TP × PP` 张卡 | 1 |
| `multinode` | 同一模板加 `spec.worker` 与 `spec.parallelism.data = TP × PP / gpu_per_pod` | TP × PP / gpu_per_pod |
| `pd` | `spec.prefill{replicas,template}` 加 `spec.template` 里的 decode 模板，两者 `parallelism.data` 相同 | prefill Pod 数 + decode Pod 数 |

## 2. 设计决策

**AD1 — 一个服务一个对象，Pod 由平台定义。** `spec.template` 就是平台的引擎 Pod，经 JSON 转成 unstructured 形状，让 Kubernetes 的 `omitempty` 把空默认值挡在 CRD 之外。工作负载、Pod 身份与滚动更新由 KServe 负责，平台从不为服务写 `Deployment`。

**AD2 — 容器是覆盖，不是第二个容器。** 预置模板把模型服务容器命名为 `main`，KServe 按**名字**把覆盖合并进容器。因此平台的引擎容器必须改名为 `main`；叫别的名字只会被当成新容器加进去，Pod 里就会同时跑两个模型服务。

**AD3 — 覆盖里不声明模型服务端口。** 端口归预置模板所有（PD 的 decode 还在预置端口上跑 KServe 的路由 sidecar），覆盖里再声明端口要么重复、要么让 Pod spec 非法。引擎自己的 `--port` 保留，decode 用 `8001`，因为 `8000` 已被路由 sidecar 占用。

**AD4 — 权重是挂载的，不是拉取的。** 平台挂载共享权重存储，并把 `spec.model.uri` 指向“存储 + 模型目录”。`spec.storageInitializer.enabled` 为 `false`：初始化器会把整个模型再拷一份，还让 Pod 一直等它。

**AD5 — 多卡副本有自己的共享内存。** `TP × PP > 1` 时在 `/dev/shm` 挂 2 GiB 的内存盘，因为 Kubernetes 默认的 64 MiB 会让引擎直接报 "Insufficient space in /dev/shm"。

**AD6 — 多 Pod 副本沿用引擎自己的 rendezvous。** rank 取 Pod 序数，master 取同一组里序数为 0 的 Pod，两者都在容器启动时解析，因为同一角色的 Pod 共用一个模板，放不下每 Pod 不同的参数。引擎参数以位置参数传递，像 `kv-transfer-config` 这样的 JSON 值不会被 shell 再解析一遍。

**AD7 — 对象只索取工作负载，不索取路由。** 数据面路由由平台自己写：一条兜底路由（`/` → 模型路由的 Service）把所有请求交给模型路由，模型路由再把这个模型的请求转给服务栈为它创建的工作负载 `Service`。因此对象里没有 `spec.router`：这一段会让运行时额外写一条按模型的路由，而那条路由的后端是 `InferencePool`，没有 Gateway API Inference Extension 的 Gateway 会拒掉它——模型明明在正常服务，对象却被报成未就绪。工作负载 `Service` 无论如何都会写出，模型路由寻址的正是它。

**AD8 — 路由按模型解析服务，路径里带服务名。** 控制台给每个服务发布的入口是 `<host>/<service>/v1`，调用方因此有一条稳定的地址；模型名仍然写在请求体里。模型路由把第一段（服务名）去掉，再把这个服务的请求转给它的工作负载，工作负载答的是标准的 OpenAI 路径。模型路由不认识的模型（平台没有发布的）退回数据面内部监听器。整条链路没有任何按模型的路由对象。

**AD9 — 自动扩缩容是对象自己的，不是第二个对象。** 策略变成 `spec.scaling` 的上下界，关闭自动扩缩容时控制器用 merge patch 钉住 `spec.replicas`，免得残留的扩缩器跟手动改副本数打架。

**AD10 — 队伍来自标签，gang 来自调度器。** 只有多 Pod 副本、且 Volcano gang 调度开启、且不是 HAMi 切片时才写 PodGroup 和它的 Volcano 注解；HAMi 切片由 HAMi 自己的调度器放置，Volcano 管不到。

## 3. 数据模型

`inference_services.topology` 存 `single` / `multinode` / `pd`，旁边是 `tensor_parallel_size`、`pipeline_parallel_size`、`prefill_replicas`、`decode_replicas`。读取时空值一律归一为 `single`（列存在之前写入的行就是这个意思），线上契约用同一套小写名字。

| 列 | 类型 | 含义 |
| --- | --- | --- |
| `topology` | `text` 默认 `single` | 服务布局 |
| `tensor_parallel_size` / `pipeline_parallel_size` | `int` 默认 `1` | 一个副本的并行度 |
| `gpu_per_pod` | `int` 默认 `1` | 一个 Pod 独占几张卡；0 读作 1 |
| `prefill_replicas` / `decode_replicas` | `int` 默认 `0` | PD 两个角色的副本数 |

## 4. 接口

`CreateInferenceServiceRequest` 带上 `topology`、`tensor_parallel_size`、`pipeline_parallel_size`、`prefill_replicas`、`decode_replicas`；`InferenceServiceSummary` 原样回传，列表页与详情页才画得出布局。

```proto
rpc UpdateInferenceServiceTopology(UpdateInferenceServiceTopologyRequest)
    returns (UpdateInferenceServiceTopologyResponse) {
  option (google.api.http) = {
    post: "/api/v1/admin/inference-services/{service_id}:update-topology"
    body: "*"
  };
}
```

更新时的校验顺序：服务存在（10301）→ 未终止（10303）→ 镜像可读 → 布局合法（10319）→ 副本数在范围内（10305）。变更事件里带着布局，控制器因此不需要回读数据库。

`ImageService.GetImageRegistryConfig`（`GET /api/v1/admin/image-registry-config`）投影已配置的 Harbor：`configured`、`hostname`、`project`、`example_reference`。凭据从不外泄；未配置时 `configured=false`，控制台把导入动作置灰。

## 5. 控制器映射

`buildLLMInferenceService` 组装出一个对象：

- metadata：名字就是服务名，命名空间是推理命名空间，带平台资源标签、路由标签与注解，注解至少是空 map；
- `spec.replicas` 取角色的副本数（`clampReplicas(evt.Replicas)`）；
- `spec.model{name, uri}`：`name` 是对客户端可见的模型名，`uri` 是 `pvc://<pvc>/<权重路径>`；
- `spec.storageInitializer.enabled: false`（AD4）；
- `spec.router` 不写：路由由平台负责，运行时只负责跑工作负载（AD7）；
- `spec.template` 是引擎 Pod 的覆盖；一个副本跨多个 Pod 时补 `spec.worker` 与 `spec.parallelism.data`（AD6）；
- 拓扑是 `pd` 时补 `spec.prefill{replicas,template}`；
- 服务自动扩缩时补 `spec.scaling{minReplicas, maxReplicas}`（允许缩到零时下界是 `0`）— 见 §7。

`applyLLMInferenceService` 带界限重试地创建或更新（KServe 控制器会同时写 status 与默认值，陈旧的 resourceVersion 会让第一次之后的每次 apply 都失败）。`deleteLLMInferenceService` 忽略 not-found；`pinLLMInferenceReplicas` 在自动扩缩关闭时对 `spec.replicas` 做 merge patch。

## 6. 路由

平台发布的入口是 `/<service>/v1`：`endpointFor(name)` 返回 `<endpoint base>/<name>/v1`，主机是数据面的主机名。模型路由把这个第一段去掉，再把请求转给服务栈为该服务创建的工作负载 `Service`（`<service>-kserve-workload-svc.<推理命名空间>.svc:8000`），由它来答标准的 OpenAI 路径。这么设计带来三件必须写清楚的事：

- 没有按模型的 `HTTPRoute`，也不需要为模型的路由写 `ReferenceGrant`：平台只写一条兜底路由（`/` → 模型路由的 Service），路由按模型解析出服务，再去寻址那个服务的工作负载；
- 让这件事成立的是“模型到服务”的那张表：控制面公布每个服务答哪些模型，所以调用方可以只在请求体里写模型名；
- 平台没有发布的模型没有已知的工作负载，于是退回数据面内部监听器，而不是一条服务栈从未写过的路由。

## 7. 自动扩缩容

服务上的策略变成对象上的上下界 `spec.scaling{minReplicas,maxReplicas}`；允许缩到零的服务其下界是 `0`。工作负载按什么信号扩缩是运行时自己的事，平台不写触发器：写死的查询会在运行时改指标名的那一刻过期。关闭自动扩缩时，控制器在每次 apply 之后把 `spec.replicas` 钉住，手动改的副本数就是对象最终生效的值。扩缩器是运行时自己的，不是另建的 HPA。

## 8. 错误

| 码 | 名称 | 何时出现 |
| --- | --- | --- |
| 10319 | `INFER_TOPOLOGY_INVALID` | 布局不认识、并行度越界、`multinode` 实际只有一个 Pod、不支持 PD 的引擎被要求 PD、角色副本数越界 |
| 10318 | `INFER_ENGINE_ARGS_INVALID` | 角色名不认识、同一角色给两次、角色覆盖里的参数名或值非法 |
| 10305 | `INFER_REPLICAS_INVALID` | 更新时 `replicas` 不在 `1..100` |
| 10303 | `INFER_SERVICE_STATE_INVALID` | 更新一个已终止的服务 |
| 10301 | `INFER_SERVICE_NOT_FOUND` | 更新一个不存在的服务 |

## 9. 验证

这次实际跑了什么：`go build ./...`、`go vet ./...`、`go test ./...`（控制器单测覆盖了期望对象：模型名、URI 与关闭的存储初始化器、单机服务一个副本、没有 `spec.router`、单机副本没有 leader-worker set、容器覆盖合并进 `main` 且不声明端口、以及同一对象的创建后再更新）与 `deploy/helm/go-taas` 在打开数据面、网关插件与模型路由时的 `helm template` / `helm lint`。

也在一套真实集群上用真实的模型和它自己的镜像观测过：对象变成 `Ready=True`，工作负载 `Deployment` 与它的 `Service` 都在，只有一个引擎 Pod 在跑（没有 router-scheduler Pod），走公开主机名的一次补全能返回生成的文本。服务上报告的副本数是“就绪的引擎 Pod 数”，所以还在加载的 Pod 不会被算成在服务的副本。
