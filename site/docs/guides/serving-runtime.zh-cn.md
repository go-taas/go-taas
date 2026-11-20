# 服务运行时

| 属性 | 内容 |
| --- | --- |
| 特性点 | 每个推理服务就是一份 KServe `LLMInferenceService`；Pod 由平台定义，工作负载与它对应的 Service 由 KServe 负责 |
| 文档范围 | 运行时为一个服务创建什么、请求怎么到达某个副本、运维要装什么、改一个服务会发生什么，以及服务不服务任何东西时先看哪里 |
| 归属模块 | `internal/controller`（`serving.go`、`routing.go`、`reconcile.go`） · `pkg/k8s`（数据面路由） · 数据面 Gateway 与模型路由 |
| 关联文档 | [LLMInferenceService 服务拓扑](../architecture/llminferenceservice-serving-topology.zh-cn.md) · [推理自动扩缩容](../architecture/inference-autoscaling.zh-cn.md) · [安装](../getting-started/installation.zh-cn.md) |
| 状态 | 已实现 |

# 服务运行时

平台启动的每个推理服务都是一份 KServe `LLMInferenceService`。本页说明这对运维意味着什么：会创建什么、请求怎么到达模型，以及它不工作时先看哪里。

## 为什么用这个运行时

平台的模型既有单卡 Pod，也有跨多台机器的 prefill/decode 副本对。必须用一个对象同时表达这些形态，因为平台里的其他部分——配额、控制台、日志查看器、计量——都按同一个形状写：

- **一个服务对象。** 一个服务就是一份 `LLMInferenceService`。从单卡 Pod 换成 PD 分离，改的是这份对象上的字段，而不是平台要跟踪的对象种类。
- **路由归服务层。** 平台只发一条路由并把它转给模型路由，模型路由再把请求送给模型自己的工作负载 Service。从那里到某个副本的路径是运行时自己的事：引擎的副本都挂在这一个 Service 后面，平台不必知道有几个 Pod 在答。
- **扩缩容来自同一个对象。** 服务的副本上下界和扩缩信号都跟着工作负载走，所以一个自己会扩缩的服务不需要平台再同步第二个控制器。

## 会创建什么

对推理命名空间（默认 `taas-infer`）里名为 `demo` 的服务，控制器写入：

| 对象 | 归属 | 作用 |
| --- | --- | --- |
| `LLMInferenceService demo` | 平台控制器 | 服务对象本身：模型、副本、Pod 模板 |
| `Deployment demo-kserve` 与 `Service demo-kserve-workload-svc` | KServe | 引擎副本及它们在集群内的地址（端口 8000） |
| `PodGroup demo-<role>` | 平台控制器 | 多 Pod 副本作为一整组被放置（Volcano） |

`PodGroup` 由平台控制器写；工作负载与它的 Service 由 KServe 按对象里的模板写。平台**不**向运行时索取它自己的模型路由（`spec.router`）：那条路由的后端是 `InferencePool`，没有 Gateway API Inference Extension 的 Gateway 会拒掉它，于是模型明明在服务，对象却被报成未就绪。真正对外服务的是平台这条路由，模型路由直接寻址模型的工作负载 Service。

## 请求怎么到达模型

```
客户端 ──TLS──▶ 平台 Gateway（https 监听器）
                   │  Wasm：API Key、余额、用量计量
                   ▼
              模型路由 ──▶ 模型的工作负载 Service ──▶ 某个副本
```

1. 公开主机名由数据面 Gateway 的 `https` 监听器服务。路由前的 Wasm 插件校验 API Key、检查余额并记录用量；下游不再校验一次 Key。
2. 这条路由唯一的后端是模型路由。只有模型路由知道一个模型是本地服务还是外部供应商卖的，所以路由没法只凭路径选后端。
3. 本地服务的模型，模型路由向平台问“哪个服务答这个模型名”，然后把请求原样转给它，OpenAI 路径（`/v1/chat/completions`）不变。调用方只需要提供模型名——映射由模型路由自己维护，控制台不必为每个服务暴露一条路径。
4. 本地服务的模型落在它自己的工作负载 Service 上：`<服务>-kserve-workload-svc.<推理命名空间>.svc:8000`。每个被服务的模型都有这么一个 Service，它带着引擎的端口，前面没有任何东西需要做决定，所以在任何 Gateway 上请求都能到达某个副本。同一个服务的引擎副本之间通过这一个 Service 自己分担负载。

5. 请求不离开集群：它以明文 HTTP 写进模型的工作负载 Service，那是个只在内网可解析的 ClusterIP 名字。

第二跳不再校验、也不再次计量。这是有意的：第一跳已经做过两件事，再查一次就需要一个平台并不持有的可信令牌旁路。


## 平台仍然拥有的部分

服务栈不知道的部分仍由控制器写：`PodGroup`（多 Pod 副本是一整个 gang，半个副本不会占着卡空等）、权重卷，以及加速器请求。回收、拆除与清扫都按服务的归属标签找同一批对象，所以删掉一个服务会连它的 gang 与 claim 一起带走。

## 安装前提

| 组件 | 为什么需要 |
| --- | --- |
| 带 LLM 服务运行时与预置的 KServe | 写工作负载与它对应的 Service，以及引擎运行的 Pod |
| Gateway API 与一个 Gateway 实现 | 提供下面两个监听器 |
| Volcano | 多 Pod 副本的 gang 放置 |
| 默认 `StorageClass`（或预先建好权重 PVC） | 权重卷 |

chart 的数据面假定 Gateway 的 class 名为 `eg`。平台不会向服务栈索取要它自己的模型路由：那条路由的后端是 `InferencePool`，不支持 Gateway API Inference Extension 的 Gateway 会拒掉它，于是模型明明在服务，对象却被报成未就绪。对外服务的是平台这一条路由，模型路由直接寻址模型的工作负载 Service——任何 Gateway 都通得过，也不需要任何扩展。

两个监听器不能互换：

- `https`（443）是公开的那个。它为推理主机名终止 TLS，只收自己命名空间里的路由。
- `internal`（80）供不能公开、但必须能从集群内到达的路由使用，例如服务器自己的内部 API。它不带证书，主机名也不在任何 DNS 区里，所以绝不能经负载均衡器可达：把它公开出去，调用方直接打它就能绕过 Key 校验。让 Gateway 的 Service 保持集群内（`ClusterIP` 的负载均衡类，或用防火墙规则拒掉它）。

Envoy Service 的名字被钉成 Gateway 名（`taas-inference`），因为 `Gateway.status.addresses` 报的是地址而不是 Service 名，而运行时视图与内部路由必须用一个能在滚动之后仍然成立的名字去寻址它。

## 改一个服务

| 改动 | 效果 |
| --- | --- |
| 影响 Pod 模板的镜像或命令 | 对象原地被替换：旧副本先排水，新副本才被调度 |
| 副本数或扩缩策略 | 工作负载原地改大小；策略决定副本数，因为集群不装扩缩器 |
| 拓扑（单机 → 多机或 PD） | 服务被重写；工作负载被替换 |
| 删除服务 | 服务、工作负载、gang 与 claim 一起删掉 |

Pod 在整个生命周期里占着它的设备，所以模型是被替换而不是被“多起一个”。服务的对象里就写着 `maxSurge: 0` 与 `maxUnavailable: 1`：多起的那一个副本会一直等在旧副本占着的设备上，在只有一块卡的节点上就是永远滚不完。代价是更新期间有一段没有任何副本在服务的窗口；要不丢这条窗口里的在途请求，就跑多个副本。

对象变更时正在生成的副本由服务栈排水，不由平台排水，所以长生成允许跑完。

## 服务不服务任何东西时

| 现象 | 先看什么 |
| --- | --- |
| 公开主机名上 404 或 503 | 模型路由能把这个模型名解析到一个服务（陌生的模型名它不会答），且它点名的工作负载 Service 有就绪的 endpoint |
| 还没到 Pod 就 401 | Key 不在平台里或余额用完了；Wasm 的判定会缓存 `gatewayPlugin.cacheTTL` |
| 请求没到任何副本 | 模型的工作负载 Service 有就绪 endpoint，且它后面的引擎 Pod 通过了就绪探针 |
| Pod 一直 `Pending` | 多 Pod 副本在等整组：看 `PodGroup` 和集群剩余加速器容量；整卡请求旁边还有在跑的副本时，要靠“替换”策略腾卡，而不是等第二块卡 |
| 模型一直不 ready | 权重卷挂上了，且服务模型 URI 里的路径在卷里存在 |

管理端运行时 API 会报告服务组件与模型的条件；先看条件，再看 Pod。
