# Go TaaS 架构设计文档

| 属性 | 内容 |
| --- | --- |
| 项目名称 | Go TaaS（Token as a Service） |
| 整体定位 | 可私有化部署的开源 **Token 工厂**：把异构、碎片的加速卡算力变成可计量、可结算、带 SLA 的 Token。它不是又一个 MaaS，而是运营供给侧——部署、调度、计量、计费、路由与治理 |
| 文档范围 | 定义系统拓扑、26 个微服务模块的职责边界、控制面/数据面划分、集群网络与协作流程；数据模型、存储参数、运行时配置等实现细节由各模块的详细设计文档另行承载 |
| 判定它是 TaaS 的标准 | 按 Token 分类型计量（输入 / 输出 / 缓存 / 推理）；多租户在密钥、配额与数据边界上隔离；配额、余额、账单与结算构成闭环；SLA 与模型、卡型、上下文长度绑定；核心 KPI 是单位 Token 成本而不是调用量 |
| 核心架构原则 | 控制面与数据面网关分离；26 个微服务 gRPC server 合并部署并通过消息队列与 Controller 解耦；推理服务与控制面部署于同一 Kubernetes 集群；准入控制在网关与控制面创建路径上完成，而非独立配额服务 |

---

## 1. 架构总览与拓扑设计

```mermaid
flowchart TB
subgraph L1["LAYER 01 · 接入层"]
    direction LR
    A1["REST API<br/>/api/v1 控制面 · /v1 数据面 · OpenAPI 3.1"]:::cell
    A2["Console<br/>Web 控制台 · OIDC 登录 · 引导式部署"]:::cell
    A3["SDK<br/>Python / Go / Node · SSE 流式 · 自动重试"]:::cell
    A4["CLI / Skill<br/>taas CLI · Agent Skill · GitOps"]:::cell
    A1 ~~~ A2 ~~~ A3 ~~~ A4
end

subgraph L2["LAYER 02 · 网关层"]
    direction LR
    G1["TaaS Gateway（控制面）<br/>TLS · 认证 · RBAC · 限流 · 审计 · 请求校验 · 路由到 TaaS Server"]:::cp
    G2["Inference Gateway + TaaS Plugin（数据面）<br/>OpenAI API · 模型路由 · Token 感知负载均衡 · 同模型 failover · 计量 · Trace"]:::dp
    G1 ~~~ G2
end

subgraph L3["LAYER 03 · K8s 集群"]
    direction TB
    subgraph L3A["① 控制面 + 中间件"]
        direction TB
        subgraph L3A1["控制面"]
            direction LR
            S1["TaaS Server<br/>无状态业务服务：租户、模型、部署、密钥、定价、计量、账单、配额、审计"]:::cp
            S2["TaaS Controller<br/>K8s Operator：CRD Reconcile、工作负载编排、状态与回滚"]:::cp
            S3["TaaS Console<br/>控制台服务：静态资源、BFF 聚合、引导式流程（不直连数据库）"]:::cp
            S1 ~~~ S2 ~~~ S3
        end
        subgraph L3A2["中间件"]
            direction LR
            M1["PostgreSQL（CNPG）<br/>业务真源：租户、模型、部署、价格、用量账本、审计"]:::mw
            M2["Redis<br/>缓存、限流计数、配额预留、分布式锁、路由表"]:::mw
            M3["RabbitMQ<br/>部署任务、计量摄取、结算、批处理、通知；重试 + DLQ"]:::mw
            M1 ~~~ M2 ~~~ M3
        end
        L3A1 ~~~ L3A2
    end
    subgraph L3B["② 第三方依赖 · Helm Chart"]
        direction LR
        B1["Aibrix<br/>分布式运行时、KV Cache"]:::dp
        B2["HAMI<br/>异构卡纳管、设备插件、调度"]:::dp
        B3["Volcano<br/>Gang Scheduling、队列与公平共享"]:::dp
        B4["GPU / NPU Operator<br/>驱动、设备插件、拓扑与健康"]:::cell
        B5["可观测栈<br/>Prometheus / OTel / Loki"]:::cell
        B1 ~~~ B2 ~~~ B3 ~~~ B4 ~~~ B5
    end
    subgraph L3C["③ 计算节点 · 推理引擎"]
        direction LR
        C1["vLLM<br/>PagedAttention · Continuous Batching"]:::cp
        C2["SGLang<br/>RadixAttention · 结构化输出 · 并行"]:::cp
        C3["TensorRT-LLM / MindIE<br/>NVIDIA / 昇腾原生推理栈"]:::cp
        C4["自研 / 其他引擎<br/>按镜像接入，统一 OpenAI 协议"]:::cp
        C1 ~~~ C2 ~~~ C3 ~~~ C4
    end
    L3A ~~~ L3B ~~~ L3C
end

L1 -->|"HTTPS · SSE · OIDC · API Key<br/>按流量类型分流"| L2
L2 -->|"控制流：REST / gRPC<br/>数据流：OpenAI /v1 · SSE"| L3

classDef cell fill:#ffffff,stroke:#c7ccd6,color:#1b2530
classDef cp fill:#f4f3ff,stroke:#a9a4ea,color:#1f2340
classDef dp fill:#eefaf3,stroke:#8fd6ae,color:#123a25
classDef mw fill:#fff7ec,stroke:#eec796,color:#4a3410
```

### 1.1 控制面与数据面分离

- **控制面（Control Plane）**：承载平台自身的 API——SSO 联邦登录、用户与组织、API Key、模型与镜像管理、推理服务编排、定价与账单。入口是**控制面 Gateway**（HTTP），由 `grpc-gateway` 从 Protobuf 契约自动生成，背后是统一的 gRPC Server。
- **数据面（Data Plane）**：承载推理服务的 API——OpenAI 兼容的 `/v1/chat/completions`、`/v1/embeddings` 等端点。入口是**推理 Gateway**，基于 **Envoy** 实现，面向**智能体 / SDK** 等程序化调用方，鉴权、计量与路由均由 **Wasm 插件**在网关侧完成。
- **独立部署**：两个网关各自作为独立工作负载部署。控制面 API 与推理 API 在扩缩容、升级节奏与故障域上完全隔离——推理 Gateway 过载或升级不会影响平台管理能力，反之亦然。

#### 1.1.1 分离是如何落地的

两个网关是独立的工作负载、独立的地址，Chart 会同时发布两者：

| 平面 | 工作负载 | 入口 | 承载 |
| --- | --- | --- | --- |
| 控制面 | `taasServer`（Deployment） | 平台 Gateway 上控制台主机名的路由（如 `taas.example.com`） | `/api/v1/*`、`/api/v1/admin/*`、控制台 |
| 数据面 | AIBrix Envoy 网关（`aibrix-eg` Gateway → Envoy 代理 Service） | 平台 Gateway 上推理主机名的路由（如 `api.example.com`） | `/v1/chat/completions`、`/v1/embeddings` 等 |

打开 `dataPlane.enabled` 并设置 `dataPlane.hostname` 后，会由**服务端**创建一条 `HTTPRoute`，
把推理主机名直接转发到推理网关的 Service，并创建跨命名空间后端所需的 `ReferenceGrant`。
Chart 刻意不再渲染这条路由：此前 Chart 与服务端同时写 `spec.hostnames`，每次 Helm 升级都会
与控制台保存互相冲突，最终哪个主机名生效取决于谁后写。改为服务端唯一所有者后，这类问题不再出现。
因此推理流量从不经过控制面进程，控制面滚动发布也不会打断正在进行的流式响应。

推理主机名是部署事实而非编译期常量，所以它同时是一个运维设置项：管理端**网关设置**页
（`/admin/settings`，`GET|PUT /api/v1/admin/platform-settings`）保存该值，
`GetInferenceEndpoint` 把 `<scheme>://<hostname>/v1` 报告给快速开始页、API 文档以及所有
查询该接口的客户端。优先级为：控制台设置 → `infer.endpointBaseURL` → 集群内自动发现的网关。

保存主机名不只是写数据库：服务端会调谐数据面的 `HTTPRoute`，使其恰好发布该主机名。
写入发生在**落库之前**；无法发布时（例如未启用发布、未配置父 Gateway 或后端）更新会以
`12909` 失败——否则控制台会对外公布一个返回 404 的端点。周期性重同步还会重新发布路由，
因此被误删的路由会自愈，全新安装也会自动发布配置中的初始主机名，无需有人打开控制台。清空主机名是空操作，避免误操作把数据面下线。路由的父 Gateway、
后端 Service 与超时仍由 Chart 持有：它们是安装期事实，而主机名是运维在运行期唯一要改的部分。
设置响应里的 `served_hostnames` 报告路由实际发布的主机名，控制台据此提示设置是否生效。

存储值刻意分成两种形态：给用户的是**公网**地址，而平台自身的调用（控制台试用）优先使用
**集群内**地址——公网主机名在 Pod 内不一定可解析，且把推理流量绕回公网入口会多一跳，
正是这次拆分要避免的。集群内无法解析公网名称时，请设置 `inference_gateway_internal_url`。

### 1.2 服务形态与通信机制

- **统一 gRPC 服务进程**：**全部 26 个微服务**（`auth`、`tenancy`、`account`、`model`、`image`、`infer`、`runtime`、`routing`、`provider`、`metering`、`billing`、`batch`、`audit`、`webhook`、`notification`、`observability`、`tracing`、`resourcemetrics`、`accelerator`、`cluster`、`optimization`、`prompt`、`evaluation`、`fault`、`vgpu`、`docs`）的 gRPC server 合并部署于同一个 Deployment，模块间在进程内直接调用，无需为每个模块单独部署进程；
- **消息队列解耦 gRPC server 与 Controller**：消息队列（Kafka / RabbitMQ）承担**微服务 gRPC server 与 Controller 之间的消息传递**。gRPC server 只负责对外接口与期望状态写入并投递消息，Controller 消费消息后驱动 Kubernetes 资源，二者可独立扩缩容与重启；
- **RESTful API 自动生成**：控制面 HTTP/JSON 接口与 OpenAPI 文档由 `grpc-gateway` 从 Protobuf 契约自动生成；
- **数据面不经过业务进程**：推理请求由 Envoy 直接代理至推理实例，鉴权与计量在 Wasm 插件内完成，避免业务进程成为转发瓶颈，也避免流式响应的二次拷贝。

### 1.3 部署与资源形态

- **单一集群**：推理服务与控制面部署在**同一个 Kubernetes 集群**内，共享一套控制面、消息队列与可观测性设施；
- **集群网络**：集群内运行 CNI Pod 网络，并通过 ClusterIP Service 与 CoreDNS 提供东西向服务发现；南北向流量经 Envoy Gateway + TLS 终结进入；NetworkPolicy 对控制面、数据面与租户命名空间做隔离；推理数据链路可选走 RDMA 网络（RoCE / InfiniBand），控制面保持常规以太网（详见 §5.2）；
- **配额与准入控制**：平台不引入独立的配额微服务，准入控制落在流量所在的位置——按 API Key 的请求限流与按组织的服务配额（最大并发推理请求数、推理服务数、每日批量作业数、单请求最大输出 token 数）在推理 Gateway 与控制面创建路径上强制，组织支出上限由计费链路强制。在上述约束内，管理员按**集群可用资源**创建推理服务，集群容量由 Kubernetes 调度器与节点资源自然约束；
- **负载隔离**：推理工作负载与控制面工作负载通过 Namespace、节点标签与污点/容忍度隔离，可在同一集群内稳定共存。
- **内置依赖**：Helm Chart 默认在集群内置备单副本的 PostgreSQL、Redis 与 RabbitMQ（每个依赖均可通过 `dependencies.<name>.mode` 切换为外部实例）。**Keycloak 不属于默认安装的一部分**（`dependencies.keycloak.mode: disabled`）：平台在自有账号体系中 seed 管理员 `admin`（密码取自 `auth.seedSso.adminLocalPassword`），因此不需要任何 IdP 即可使用控制台。设为 `internal` 时会置备单副本 Keycloak，并 seed 出 `go-taas` realm 的客户端以及 Keycloak 侧的 `admin`/`admin` 用户；设为 `external` 时则对着你自己的 IdP seed 提供商记录。两种情况下 auth 的 Migrate 钩子都会按 `auth.seedSso` 配置（issuer/redirectUri 指向 IdP 与暴露地址）写入对应的 SSO 提供商记录。
- **资源标签**：Chart 与 Controller 创建的每个资源都带标准 `app.kubernetes.io/*` 标签（`name`、`instance`、`version`、`managed-by`、`part-of: go-taas`，并按组件补充 `component`：`server`、`controller`、`postgresql`、`redis`、`rabbitmq`、`keycloak`）。平台自有键统一使用项目自己的 DNS 前缀 `go-taas.com/`（官方网站），不使用项目并不拥有的域名：`go-taas.com/service-id`、`go-taas.com/replica-index`、`go-taas.com/origin`、`go-taas.com/model`、`go-taas.com/model-id`、`go-taas.com/task-type`、`go-taas.com/task-id`、`go-taas.com/role`、`go-taas.com/accelerator`、`go-taas.com/accelerator-type`、`go-taas.com/driver-version`、`go-taas.com/device-plugin` 与 `go-taas.com/vgpu-pool`。出现在 selector 中的键属于 API 面：改名会触发受影响工作负载的滚动更新，不是纯展示性修改。

---

## 2. 组件职责定义

各组件间的依赖关系与职责边界如下：

```mermaid
flowchart TD
    subgraph edge["入口"]
        direction LR
        CGW["控制面 Gateway（HTTP）<br/>grpc-gateway · /api/v1/* · /api/v1/admin/*"]
        DGW["推理 Gateway<br/>Envoy + Wasm（鉴权 · 计量 · 路由）"]
    end

    subgraph mods["微服务 —— 单个 Deployment（26 个模块）"]
        direction TB
        subgraph m_acc["接入与租户"]
            direction LR
            AUTH["auth"]
            TENANCY["tenancy"]
            ACCOUNT["account"]
        end
        subgraph m_mid["模型与推理"]
            direction LR
            MODEL["model"]
            IMAGE["image"]
            INFER["infer"]
            RUNTIME["runtime"]
            ROUTING["routing"]
            PROVIDER["provider"]
            ACCELERATOR["accelerator"]
            CLUSTER["cluster"]
            BATCH["batch"]
        end
        subgraph m_bill["计量、计费与成本"]
            direction LR
            METERING["metering"]
            BILLING["billing"]
            OPTIMIZATION["optimization"]
        end
        subgraph m_obs["可观测与自动化"]
            direction LR
            OBSERVABILITY["observability"]
            TRACING["tracing"]
            RESOURCEMETRICS["resourcemetrics"]
            AUDIT["audit"]
            WEBHOOK["webhook"]
            NOTIFICATION["notification"]
        end
        subgraph m_prompt["提示词与文档"]
            direction LR
            PROMPT["prompt"]
            EVALUATION["evaluation"]
            DOCS["docs"]
        end
    end

    CGW --> mods
    DGW --> RT["推理服务"]
    DGW -.->|gRPC 校验 API Key| AUTH
    DGW -.->|计量事件| MQ["消息队列（Kafka / RabbitMQ）"]

    AUTH --> PG[(PostgreSQL)]
    AUTH --> RD[(Redis)]
    AUTH --> IDP["外部身份提供方<br/>LDAP · OIDC · SAML · 微信"]
    TENANCY --> PG
    ACCOUNT --> PG
    MODEL --> PG
    MODEL --> JFS[(JuiceFS)]
    IMAGE --> PG
    IMAGE --> REG[("镜像仓库 / MinIO")]
    IMAGE --> MQ
    INFER --> PG
    INFER --> MQ
    RUNTIME --> MQ
    ROUTING --> PG
    PROVIDER --> PG
    PROVIDER --> EXT["外部大模型提供方<br/>DeepSeek · OpenAI 兼容"]
    BATCH --> PG
    BATCH --> FSTORE[("批量文件存储<br/>输入 / 结果 / 错误 JSONL")]
    METERING --> PG
    METERING --> RD
    METERING --> MQ
    BILLING --> PG
    BILLING --> MQ
    OPTIMIZATION --> PG
    OBSERVABILITY --> PG
    TRACING --> PG
    RESOURCEMETRICS --> PG
    AUDIT --> PG
    WEBHOOK --> HOOK["客户 HTTPS 端点"]
    NOTIFICATION --> PG
    PROMPT --> PG
    EVALUATION --> PG
    EVALUATION --> MQ
    DOCS --> PG

    MQ --> CTRL["Controller<br/>K8s 资源调谐"]
    MQ --> ACCELERATOR
    MQ --> CLUSTER
    CTRL --> K8S["Kubernetes API<br/>推理负载 · 镜像预热 · 资源采样"]
    CTRL --> RESOURCEMETRICS
    RT -.-> JFS
    classDef edge fill:#007F86,stroke:#00565C,stroke-width:2px,color:#FFFFFF
    classDef svc fill:#CFE9EA,stroke:#4E9CA0,stroke-width:1.5px,color:#0B383D
    classDef core fill:#0B4F55,stroke:#083B40,stroke-width:2px,color:#FFFFFF
    classDef store fill:#E7ECEE,stroke:#9AACB1,stroke-width:1.5px,color:#23343A
    classDef ext fill:#FBE7CF,stroke:#D08A3C,stroke-width:2px,color:#6B3B08
    classDef gpu fill:#FADDD1,stroke:#D2704E,stroke-width:2px,color:#71260E
    class CGW,DGW edge
    class MQ,CTRL core
    class AUTH,TENANCY,ACCOUNT,MODEL,IMAGE,INFER,RUNTIME,ROUTING,PROVIDER,ACCELERATOR,CLUSTER,BATCH,METERING,BILLING,OPTIMIZATION,OBSERVABILITY,TRACING,RESOURCEMETRICS,AUDIT,WEBHOOK,NOTIFICATION,PROMPT,EVALUATION,DOCS svc
    class PG,RD,JFS,REG,FSTORE store
    class IDP,EXT,HOOK ext
    class K8S,RT gpu
    style edge fill:#EFF7F7,stroke:#9CC7C9,stroke-width:1.5px,color:#2A6A6F
    style mods fill:#F5F9F9,stroke:#B4C7CA,stroke-width:1.5px,color:#2F5B60
    style m_acc fill:#EAF4F5,stroke:#A9CDCF,stroke-width:1px,color:#2A6A6F
    style m_mid fill:#EAF4F5,stroke:#A9CDCF,stroke-width:1px,color:#2A6A6F
    style m_bill fill:#EAF4F5,stroke:#A9CDCF,stroke-width:1px,color:#2A6A6F
    style m_obs fill:#EAF4F5,stroke:#A9CDCF,stroke-width:1px,color:#2A6A6F
    style m_prompt fill:#EAF4F5,stroke:#A9CDCF,stroke-width:1px,color:#2A6A6F
```

### 微服务清单

单个 Deployment 承载 26 个微服务模块，逐项列明职责边界与对应特性点（backlog 编号）：

| # | 模块 | 核心职责 | 特性点 |
| --- | --- | --- | --- |
| 1 | `auth` | 本地与 SSO 联邦认证（LDAP / OIDC / SAML / 微信）、账号绑定与 JIT 预置、会话与令牌、API Key 全生命周期与校验、按 Key 限流、按 Key 模型范围限定、基于角色的控制台路由 | #1、#7、#11、#22、#46 |
| 2 | `tenancy` | 组织 / 项目两级隔离、成员/角色/邀请（RBAC）、控制台面分离、按组织的服务配额 | #6、#10、#17、#51 |
| 3 | `account` | 账户信息与符合 GDPR 的账户数据导出包 | #41 |
| 4 | `model` | 模型目录、版本管理与回滚、权重路径与模型仓库下载、租户级模型授权 | #2、#13、#32 |
| 5 | `image` | 推理引擎镜像的登记/版本/导入、节点级预热任务、模型 × 引擎 × 卡型兼容性矩阵 | #3、#19 |
| 6 | `infer` | 推理服务生命周期（部署、扩缩容、升级、回滚）、弹性伸缩、路由策略、playground（含多模型对比）、SDK 快速开始、服务日志、压测 | #2、#12、#16、#20、#21、#33、#35、#45 |
| 7 | `runtime` | 平台服务形态（普通 Deployment 与 AIBrix）、运行时组件健康与被纳管模型投影 | #47 |
| 8 | `routing` | 路由组（虚拟模型）与真实目录模型候选：权重、成本/时延策略、意图规则、决策轨迹 | #49 |
| 9 | `provider` | 外部提供方注册表、加密只写密钥、上游模型治理与发布入目录、连通性探测 | #48 |
| 10 | `metering` | Token 计量与防篡改凭单、用量记录、请求日志、用量看板、按 Key 用量分析、错误分析 | #4、#9、#12、#28、#31 |
| 11 | `billing` | 「模型 × 卡型」价格矩阵、余额/额度账户、扣费与结算、支付/发票/自动充值、组织支出上限、成本分析、用量成本预测、账单报表 | #5、#8、#11、#14、#25、#29、#36 |
| 12 | `batch` | 批量推理作业：JSONL 提交与校验、异步 worker、进度跟踪、结果/错误下载 | #42 |
| 13 | `audit` | 控制面与鉴权事件的审计日志与活动导出、部署历史审计 | #15、#34 |
| 14 | `webhook` | 出站 Webhook 端点、事件订阅、HMAC 签名、重试策略与投递日志 | #23 |
| 15 | `notification` | 控制台内通知中心、按用户偏好、阈值告警与已读/未读状态 | #26 |
| 16 | `observability` | 模型性能聚合（时延 / 吞吐 / 错误率 / token 吞吐）与平台组件健康、状态页 | #24、#30 |
| 17 | `tracing` | 单条请求链路与分阶段时延（TTFT / 生成 / 总时延）、按 request-id 检索 | #27 |
| 18 | `resourcemetrics` | Controller 采样的按服务 CPU / 内存 / GPU 利用率时序 | #37 |
| 19 | `accelerator` | 加速器清单与健康：每节点厂商、卡型、驱动与 Device Plugin 状态、已分配/空闲数量、就绪度 | #18 |
| 20 | `cluster` | 多集群注册、集群健康与工作负载分布、按集群的部署路由 | #40 |
| 21 | `optimization` | 成本优化建议：规则化、可量化、可忽略的节省建议（仅建议，不自动执行） | #50 |
| 22 | `prompt` | 提示词库：创建、版本化、归类、搜索、每提示词用量统计、跳转 playground | #43 |
| 23 | `evaluation` | 提示词评测套件：基于变量的用例、可评分运行、运行对比 | #44 |
| 24 | `fault` | 故障管理：故障策略、故障检测、根因分析与部署关联 | — |
| 25 | `vgpu` | 基于 HAMi 的 vGPU 切分池与切分模板；部署链路消费其切分账目 | #52 |
| 26 | `docs` | 面向租户面的 API 目录，供控制台内 API 文档浏览器使用 | #38 |


### 2.1 `auth` —— 认证、SSO 联邦与多租户管理

**核心职责：**

- **本地账号认证**：用户注册、登录与密码策略，凭据仅以加盐哈希落库；
- **第三方 SSO 联邦认证**：内置**插件化身份提供方（IdP）框架**，可直接对接企业既有账号体系，无需为每种协议重复实现登录链路（OIDC 与内置本地账号体系已实现，其余企业级协议建设中）：
  - **OIDC / OAuth 2.0**：GitHub、Google、GitLab 以及企业自建 IdP；
  - **SAML 2.0**（建设中）：企业级单点登录；
  - **LDAP / LDAPS**（建设中）：对接企业目录服务（Active Directory、OpenLDAP 等），支持基于用户 DN 绑定与按组过滤；
  - **微信**（建设中）：微信开放平台与企业微信扫码登录；
- **多 IdP 并存与可插拔配置**：管理员可同时启用多个 IdP，并分别配置协议参数、可见范围、默认组织与是否允许自动注册；新增提供方以独立插件接入，不侵入核心登录逻辑；
- **账号绑定与 JIT 预置**：以 `issuer + subject`（OIDC / SAML）或 `DN`（LDAP）等稳定标识作为外部身份主键，与平台账号绑定；首次登录可自动创建账号（Just-In-Time Provisioning），亦可关闭自动创建，改由管理员预先分配；
- **属性与角色映射**（建设中）：把 IdP 返回的用户名、邮箱、组与角色声明映射为平台的组织、项目与角色，实现基于企业组织架构的权限下发；
- **会话与令牌**：SSO 登录成功后签发平台会话与访问令牌，支持令牌轮换与统一登出（Single Logout）；本地密码登录可独立开关，与 SSO 并存或完全禁用；
- 组织 / 项目两级多租户体系的管理与隔离；
- API Key 全生命周期管理（创建、鉴权、吊销、过期控制），明文仅在创建时展示一次，系统只保存加盐哈希摘要；
- **为控制面 Gateway 与数据面推理 Gateway 提供 API Key 校验能力**：对外暴露鉴权 gRPC 接口，供推理 Gateway 的 Wasm 插件逐请求调用，并以 Redis 正缓存支撑高并发低时延校验；

**外部依赖：** PostgreSQL（账户、租户与身份绑定数据）、Redis（鉴权缓存与会话）、外部身份提供方（LDAP / OIDC / SAML / 微信）

### 2.2 `model` —— 模型资产管理

**核心职责：**

- 开源大模型（Qwen、DeepSeek、LLaMA 等）的元数据注册与版本管理；
- 模型权重在对象存储中的路径维护，供推理服务通过共享文件系统直接挂载；
- 租户级模型授权管理，决定各租户可消费的模型范围。

**外部依赖：** PostgreSQL（模型元数据）、MinIO 与 JuiceFS（权重存储）

### 2.3 `image` —— 推理镜像管理

**核心职责：**

- 统一管理推理引擎容器镜像：各主流引擎族（vLLM / SGLang / TensorRT-LLM）在多种异构加速卡上的镜像登记、版本与适配关系；
- 维护镜像与卡型、引擎、模型格式的适配矩阵，为推理服务编排提供可选的镜像清单；
- 驱动镜像预热与缓存：向消息队列投递预热任务，由 Controller 在各算力节点提前拉取镜像，缩短推理副本冷启动时间；
- 校验镜像来源与完整性，避免不可信镜像进入推理链路。

**外部依赖：** 镜像仓库 / MinIO（镜像与元数据存储）、消息队列（预热任务投递）

### 2.4 `infer` —— 推理服务管理

**核心职责：**

- 异构算力纳管：通过厂商无关的节点标签与扩展资源模型，统一纳管**多种异构加速卡**，新增卡型只需为节点打标，无需改动控制面；
- 推理服务的生命周期管理：部署、扩缩容、升级与下线的期望状态维护与变更投递；
- 推理引擎适配：对外统一呈现 OpenAI 兼容能力，屏蔽各引擎差异；
- 推理实例的健康状态与可用端点登记，供数据面 Gateway 路由使用；
- 配额感知的准入：创建推理服务时按管理员指定的副本数与卡型使用资源，同时受组织服务配额（特性 #51）与集群可用资源约束。

**外部依赖：** 消息队列（变更投递）、MinIO 与 JuiceFS（权重与镜像共享挂载）

### 2.5 `metering` —— Token 计量

**核心职责：**

- 消费数据面 Gateway 的 Wasm 插件投递的计量事件，提取 Token 用量（Prompt / Completion / Cached / Reasoning）；
- 生成防篡改的计量凭单；
- 通过消息队列异步投递给计费链路，解耦推理链路与账务链路。

**外部依赖：** Redis（计量缓冲）、消息队列（Kafka / RabbitMQ）

### 2.6 `billing` —— 计费与账务

**核心职责：**

- 维护「模型 × 卡型」价格矩阵，支持阶梯价与套餐；
- 请求准入时的资金预占与请求完成后的实际结算；
- 余额（预付费）与额度（后付费）两种账户模式的扣费管理；
- 账单生成、用量统计与对账。

**外部依赖：** PostgreSQL（账务数据）、消息队列（计量事件消费）

### 2.7 Controller —— 资源调谐

**核心职责：**

- 消费消息队列中的变更消息，将其转化为 Kubernetes 资源的实际动作：创建/更新/删除推理工作负载、Service 与 Endpoint；
- 执行镜像预热：把 `image` 模块投递的预热任务落到具体节点；
- 持续调谐：观测实际状态与期望状态的差异并收敛，保证推理服务可用；
- 与 gRPC server 通过消息队列解耦，可独立扩缩容、重启与升级。

**外部依赖：** 消息队列（消息消费）、Kubernetes API、MinIO 与 JuiceFS（权重与镜像挂载）

---

## 3. 网关设计

### 3.1 控制面 Gateway

- **定位**：平台管理 API 的入口，只服务管理员与控制台；
- **实现**：`grpc-gateway` 从 Protobuf 契约自动生成 HTTP/JSON 代理与 OpenAPI 文档，业务逻辑完全在统一 gRPC Server 内；
- **承载能力**：SSO 联邦登录入口（授权发起与回调）、模型与镜像管理、推理服务编排、API Key 与组织管理、定价与账单查询等；
- **不承载推理流量**：与推理数据面完全分离，便于独立发布与故障隔离；
- **管理员/用户面分离**：普通用户控制台位于 `/`（API `/api/v1/*`），管理控制台位于 `/admin`（API `/api/v1/admin/*`），两者为独立面、独立会话与独立登录流程（特性 #17、#22）；携带错误面会话的请求会在进入任何 handler 之前被拒绝。用户账户类 API（登录、注册）位于 `/api/v1/auth/*`，管理控制台 Web 页面位于 `/admin` 路径前缀（`/admin/models`、`/admin/inference-services`、`/admin/api-keys` 等）。

### 3.2 数据面（推理）Gateway：Envoy + Wasm

推理 Gateway 基于 **Envoy** 构建，通过 **Wasm 插件**在网关侧实现以下能力，使推理流量不经过业务进程：

| 能力 | 说明 |
| --- | --- |
| **鉴权** | 解析 `Authorization` 头中的 API Key，**通过 gRPC 调用 `auth` 模块的鉴权接口**完成校验；非法或已吊销的 Key 在网关侧直接拒绝，不进入推理实例 |
| **计量** | 从流式（SSE）与非流式响应中解析 Token 用量，生成计量事件投递至消息队列，供 `metering` 与 `billing` 消费 |
| **路由** | 按模型选择健康推理实例、负载均衡与失败重试，屏蔽后端实例变化 |
| **流式透传** | 保持 SSE 逐事件 Flush 透传，避免缓冲导致的额外时延 |
| **可观测性** | 统一产出访问日志与指标，用于容量观测与问题定位 |

> 说明：由于鉴权与计量下沉到网关 Wasm 插件，平台不再需要独立的配额/限流服务。准入控制落在流量所在的位置：按 API Key 的请求限流与按组织的服务配额（最大并发请求数、推理服务数、每日批量作业数、单请求最大输出 token 数）在推理 Gateway 与控制面创建路径上强制，组织支出上限由计费链路强制；校验顺序为 限流 → 配额 → 资金。

#### 3.2.1 插件流水线的执行顺序

每个推理请求在 Envoy 内走同一条有序流水线。顺序是有意义的：鉴权失败必须比配额检查
更便宜，而计量绝不能拒绝请求。

| # | 步骤 | 做什么 |
| --- | --- | --- |
| 01 | **Auth 鉴权** | 解析 `Authorization: Bearer sk-…`，调用 `auth` 模块校验 Key（本地缓存 → gRPC），解析出租户、Key ID、作用域与模型白名单 |
| 02 | **Quota 配额** | 令牌桶限流 + 并发租约；Redis 原子扣减；默认 fail-closed，也可按策略降级 |
| 03 | **Route 路由** | `RoutingGroup` → 健康部署 → 按加权 / 延迟 / 成本选实例；只允许**同模型**部署之间 failover |
| 04 | **Meter 计量** | 以引擎返回的 `usage` 为准，网关计数兜底；按 `request_id` 生成事件并异步写入队列 |
| 05 | **Trace 追踪** | 注入 `traceparent`；记录排队、Prefill、Decode 与总耗时；日志中脱敏提示词 |
| 06 | **Guard 保护** | 请求体大小、`max_tokens`、超时与取消传播；异常上游快速摘除 |
| 07 | **Cache 缓存** | 可选的幂等结果缓存；前缀缓存亲和。命中缓存不改变计费口径 |
| 08 | **Degrade 降级** | Redis / 队列不可用时改用本地令牌桶与本地缓冲，恢复后补偿上报 |

两个取舍是刻意的：

- **计量失败不能阻断推理。** 计量组件故障就拒绝服务，会把故障半径从「账单不准」扩大到
  「业务全停」。正确做法是先放行、事件进补偿队列、事后按 `request_id` 对账并归因差异。
- **插件跑在 Envoy 进程内，而不是自己写 sidecar。** 它可以独立发布、灰度、回滚，不随
  Server 发版，也不增加一次网络跳转。流式推理关注的 P99 上，一次跳转就是几十毫秒。

> **硬约束。** 插件必须无状态、可热更新、可降级。任何计量失败都不能阻断推理，但必须进入
> 补偿队列，并且在对账报表里可见。看不见的失败，比失败本身更危险。

> **实现载体。** 上述插件契约是数据面必须满足的能力。Wasm 载体已经交付：外层推理网关上的
> proxy-wasm 过滤器执行第 01 步（Auth）与第 04 步（Meter），以及两项契约本就写明由网关执行
> 的限制（`rate_limit_rpm` 与 `max_tokens` 上限）。AIBrix 的 Go `ext_proc` 插件保留第 03 步
> （Route）—— 过滤器不选择后端。第 02 步（配额租约）、05–07 以及 08 尚未进入 Wasm 载体；已实现、
> 按 worker 近似的部分与延后项见[数据面网关插件](../architecture/data-plane-gateway-plugin.zh-cn.md)。


### 3.3 推理 Gateway 的 API Key 鉴权调用链

推理数据面的每一次请求都需要校验 API Key，而鉴权的权威判断属于 `auth` 模块。因此 Wasm 插件不自己做业务判断，而是**通过 gRPC 调用 `auth` 模块**完成校验：

```mermaid
sequenceDiagram
    autonumber
    participant Agent as 智能体 / SDK
    participant W as Wasm 插件（推理 Gateway）
    participant Cache as 本地缓存（Wasm 内存）
    participant Auth as auth 模块（gRPC）
    participant RD as Redis 缓存

    Agent->>W: POST /v1/chat/completions（Bearer sk-xxx）
    W->>Cache: 以 Key 摘要为键查本地缓存
    alt 缓存命中
        Cache-->>W: 返回组织 / KeyID / 角色
    else 缓存未命中
        W->>Auth: gRPC VerifyAPIKey（Key 摘要、模型、来源 IP）
        Auth->>RD: 查鉴权正缓存
        RD-->>Auth: 命中则直接返回
        Auth-->>W: 返回组织 / KeyID / 角色（或拒绝）
        W->>Cache: 写入带 TTL 的本地缓存
    end
    alt 校验通过
        W-->>Agent: 放行，转发至推理实例
    else 校验失败
        W-->>Agent: 401 / 403，不进入推理实例
    end
```

- **调用方**：Envoy 的 Wasm 插件，在请求头阶段发起鉴权；实现上优先使用 Envoy 的 `ext_authz` gRPC 过滤器，或由 Wasm 插件直接发起 gRPC 调用；
- **调用目标**：`auth` 模块暴露的鉴权 gRPC 接口，入参为 API Key 摘要与请求上下文（模型、来源 IP），出参为组织、Key ID、角色与计量所需上下文；Wasm 插件**不直连数据库**；
- **缓存分层**：Wasm 插件内维护带 TTL 的本地缓存（建议 30s），以 Key 摘要为键；未命中时再回源 `auth`，`auth` 侧再以 Redis 作为正缓存，两级缓存把逐请求鉴权开销保持在亚毫秒级；
- **超时与失败策略**：鉴权调用设置毫秒级超时与重试，超时或 `auth` 不可用时按 **fail-closed** 策略拒绝请求，避免未授权流量进入推理实例；
- **吊销生效**：本地缓存 TTL 决定吊销的最长生效延迟，可在控制面提供缓存失效广播以满足更严格的即时吊销要求；
- **职责边界**：Wasm 插件只负责“调用、缓存与判定”，API Key 的哈希比对、组织归属、吊销与过期等权威判断始终由 `auth` 模块完成。

---

## 4. 模块协作流程

### 4.1 SSO 联邦登录与账号映射

```mermaid
sequenceDiagram
    autonumber
    actor User as 用户
    participant Console as 控制台 / CLI
    participant CGW as 控制面 Gateway
    participant Auth as auth 模块
    participant IdP as 外部身份提供方<br/>LDAP · OIDC · SAML · 微信

    User->>Console: 选择「使用 xx 登录」
    Console->>CGW: GET /api/v1/auth/sso/{provider}/authorize
    CGW->>Auth: 发起 SSO 授权
    Auth->>IdP: OIDC / SAML 重定向，或 LDAP 目录绑定查询
    IdP-->>Auth: 返回授权码、断言或目录条目
    Auth->>IdP: 校验换取用户标识与属性
    IdP-->>Auth: 返回 subject 与属性声明
    Auth->>Auth: 匹配已有绑定，或按策略 JIT 创建账号
    Auth->>Auth: 映射组织与角色，签发会话与访问令牌
    Auth-->>CGW: 登录成功
    CGW-->>Console: 下发会话令牌
    Console-->>User: 进入控制台
```

- **可插拔**：新增一种 SSO 提供方只需实现统一的 IdP 插件接口（授权、回调、标识与属性提取），不改动 `auth` 核心登录逻辑；
- **安全**：回调严格校验 `state` / `nonce` / 签名，避免请求伪造；外部身份仅作为绑定标识，平台始终以内部用户 ID 作为授权主体。

### 4.2 模型一键部署流程
两条链路是不对称的，而这种不对称正是设计：部署链路异步、每一步都可能失败，所以需要状态机、
重试与回滚；推理链路同步且必须快，因此计量之后的一切都是异步的，绝不阻塞响应。

| # | 部署（控制面，异步） |
| --- | --- |
| 1 | Console 或 API 提交模型、引擎、卡型、副本与推理参数 |
| 2 | 网关鉴权；Server 校验配额与参数 |
| 3 | Server 写入部署记录（`PENDING`）与 Outbox 事件，返回部署 ID |
| 4 | Outbox Publisher 把事件投递到队列 |
| 5 | Controller 调谐：创建工作负载对象，渲染 Deployment、Service 与弹性策略 |
| 6 | Kubernetes + HAMi + Volcano 分配设备；镜像预热；挂载模型缓存 |
| 7 | 健康检查与 warmup 通过；状态置 `READY`，注册端点 |
| 8 | 网关刷新路由；模型可调用；审计与通知完成 |


```mermaid
sequenceDiagram
    autonumber
    actor Admin as 平台管理员
    participant CGW as 控制面 Gateway
    participant GRPC as gRPC Server（infer / image）
    participant MQ as 消息队列
    participant CTRL as Controller
    participant K8s as Kubernetes

    Admin->>CGW: POST /api/v1/models/{id}/deploy（指定卡型、镜像与副本数）
    CGW->>GRPC: 校验参数、写入期望状态、登记部署任务
    GRPC->>MQ: 投递「部署推理服务」消息
    MQ->>CTRL: 消费部署消息
    CTRL->>K8s: 创建推理 Deployment 与 Service（挂载权重共享卷）
    K8s-->>CTRL: 推理 Pod 就绪
    CTRL->>K8s: 登记服务端点，标记推理服务可用
    CTRL-->>GRPC: 回报部署结果
    GRPC-->>CGW: 返回部署成功
    CGW-->>Admin: 200 OK
```

### 4.3 推理请求与计费闭环
| # | 推理（数据面，计量之后异步） |
| --- | --- |
| 1 | 客户端调用 `/v1/chat/completions`，携带 API Key 与 `stream=true` |
| 2 | 插件校验 API Key、租户/用户状态与模型作用域 |
| 3 | 配额：检查并发、速率与余额；Redis 原子扣减；失败返回 429/402 |
| 4 | 路由：RoutingGroup 选健康部署，再按加权、延迟、成本与前缀亲和选实例 |
| 5 | 转发到 vLLM / SGLang Pod，以 SSE 流式返回 |
| 6 | 计量读取上游 `usage`，生成按 `request_id` 幂等的用量事件 |
| 7 | 异步：事件 → 队列 → 消费者 → PostgreSQL 用量账本 |
| 8 | 结算：日/小时聚合 → 账单、余额、配额；看板与告警更新 |


```mermaid
sequenceDiagram
    autonumber
    actor Agent as 智能体 / SDK
    participant DGW as 推理 Gateway（Envoy + Wasm）
    participant Auth as auth 模块
    participant RT as 推理服务
    participant MQ as 消息队列
    participant Metering as metering 模块
    participant Billing as billing 模块

    Agent->>DGW: POST /v1/chat/completions（Bearer sk-xxx, stream=true）
    DGW->>Auth: gRPC VerifyAPIKey（Wasm 插件调用，命中本地缓存则不回源）
    Auth-->>DGW: 返回组织 / KeyID / 角色，鉴权通过
    DGW->>RT: 按模型路由并转发推理请求
    loop SSE 事件流实时透传
        RT-->>DGW: data: {"choices":[{"delta":{"content":"..."}}]}
        DGW-->>Agent: 实时 flush 转发
    end
    RT-->>DGW: 终态数据包（含 usage 统计）与 [DONE]
    DGW-->>Agent: 转发 [DONE] 并优雅断开
    DGW->>MQ: Wasm 插件投递 Token 用量计量事件
    MQ->>Metering: 消费计量事件，生成计量凭单
    Metering->>MQ: 发布结算事件
    MQ->>Billing: 完成实际扣费与账单落账
```

---

### 3.4 计量口径与账本规则

计量准确率决定下游的一切：定价、配额、账单、毛利。客户可以接受贵，不能接受算不清。

难点不在数 Token，而在**口径**。网关自己做 tokenizer 会受 chat template、工具调用格式、
多模态输入的影响，与引擎实际计算的长度不完全一致。因此以引擎返回的 `usage` 为准，
网关计数兜底，并按日对账。

用量还要按四类 Token 拆分——**prompt、completion、cached、reasoning**——因为它们的成本
结构不同，定价也应该不同。

流式请求中途断开是最典型的边界情况：引擎可能已经算了 Token 而网关没收到完整响应，
重试又可能重复计费。下面的规则是为了让这种情况从设计上不可能发生，而不是事后修补。

| 规则 | 原因 |
| --- | --- |
| 以引擎 `usage` 为准，网关计数兜底 | 只有引擎知道它实际算了多少 |
| 每个请求分配 `request_id`，计量事件按此幂等 | 断连与重试不得重复计费 |
| 账本**只追加**，按日分区，不原地修改 | 可审计的账单需要不可变的历史 |
| 余额由幂等的聚合任务算出，不在每次请求上更新 | 每请求更新余额扛不住推理峰值 |
| 每租户按日生成哈希链 | 无需区块链即可防篡改、可追溯 |
| 账单由账本聚合；日对账覆盖 ≥ 99.5%，差异 ≤ 0.1% | 对账是产品能力，不是财务杂务 |

这些规则听起来像财务要求，其实是产品要求：客户必须能从账单按 `request_id` 下钻到每一次
调用，出现差异必须 100% 归因。做不到这两点，SLA 就是一句空话。

## 5. 存储与网络

### 5.1 存储分层

| 存储 | 服务对象 | 承担职责 |
| --- | --- | --- |
| **Longhorn** | Kubernetes 集群自身 | 为控制面与集群内有状态组件提供持久卷（PVC）；通过 CSI 提供块存储与副本冗余 |
| **MinIO** | 推理服务 | S3 兼容对象存储，存放推理引擎镜像与模型权重原始件，并作为 JuiceFS 的后端 |
| **JuiceFS** | 推理服务 | 将对象存储挂载为 POSIX 文件系统，多副本共享同一份权重与镜像，实现秒级挂载与节点级缓存 |

- **职责分离**：Longhorn 只面向集群内部件（PostgreSQL、Redis 等），不参与推理数据链路；推理权重与镜像统一走 MinIO + JuiceFS；
- **共享加速**：推理多副本共享 JuiceFS 卷，配合节点本地缓存，避免每个副本重复拉取数十 GB 权重。

### 5.2 Kubernetes 集群内网络设计

集群拥有独立的网络平面，在拓扑图中以「**集群网络**」区域呈现，包含四类关注点：

| 平面 | 用途 | 设计 |
| --- | --- | --- |
| **Pod 网络（东西向）** | Pod 之间与 Pod 到 Service 的流量 | 由标准 CNI 插件提供扁平 Pod 网络与 IPAM；26 个微服务、Controller、两个 Gateway 与推理实例都运行在该网络之上 |
| **服务发现** | 为 26 个微服务模块与 Controller 提供稳定寻址 | 按暴露单元（gRPC Server、两个 Gateway、消息队列、Redis、PostgreSQL）各建一个 ClusterIP Service，经 CoreDNS 解析；模块间为进程内调用，服务发现只用于工作负载之间 |
| **南北向入口** | 集群外访问控制台与 API | 由 Envoy Gateway 终结 TLS，并把 `/`（普通用户控制台）、`/admin`（管理控制台）、`/v1/*`（推理 API）、`/api/v1/*` 路由到控制面 Gateway 或推理 Gateway |
| **隔离与策略** | 控制面、数据面与租户之间的爆炸半径收敛 | 命名空间隔离 + NetworkPolicy 只放行拓扑图中的流量（控制台 → Gateway、Gateway → gRPC Server/数据面、gRPC Server ↔ 消息队列、Controller → Kubernetes API、推理服务 → 权重存储）；`provider` 到外部大模型端点、`webhook` 到客户端的出向流量显式放行 |
| **传输分层** | 将高带宽推理流量与管理流量分离 | 控制面使用常规以太网；推理数据链路（请求/响应、KV Cache 传输、多卡通信）可选走 RDMA 网络（RoCE / InfiniBand） |

- **物理/逻辑分离**：推理的大流量与低时延诉求走 RDMA，控制面走常规网络，避免相互干扰；
- **按需启用**：RDMA 是推理网络的增强能力，未部署 RDMA 时推理流量可回落至常规网络；
- **故障收敛**：控制面 Gateway 与推理 Gateway 是相互独立的 Envoy Gateway 后端，拥有各自的 Service 与 Deployment，推理链路的网络或升级故障不会阻塞平台管理流量，反之亦然；
- **出向访问**：只有 `provider`（外部大模型提供方，只做 connect 式五态探测）、`webhook`（客户 HTTPS 端点）与 `model`（模型仓库下载）需要访问集群外网络，其余组件全部停留在集群网络内部。

---

## 6. 外部系统依赖边界

| 外部系统 | 使用组件 | 承担职责 |
| --- | --- | --- |
| PostgreSQL | auth · tenancy · account · model · image · infer · routing · provider · metering · billing · batch · audit · webhook · notification · observability · tracing · resourcemetrics · cluster · optimization · prompt · evaluation · docs | 业务数据持久化与事务账本 |
| Redis | auth · metering | 鉴权缓存（API Key 正缓存、会话）与计量缓冲 |
| Longhorn | 控制面有状态组件 | 集群自身持久化存储（PVC） |
| MinIO | model · image | 对象存储：模型权重与推理引擎镜像 |
| JuiceFS | model · infer · Controller | 权重与镜像的多副本共享挂载 |
| Kubernetes | Controller | 推理负载、镜像预热、加速器快照与资源指标采样的创建与调谐 |
| Kafka / RabbitMQ | 微服务 gRPC server · Controller | gRPC server 与 Controller 之间的消息传递（部署、镜像预热、运行时形态、加速器/集群快照、计量事件） |
| Envoy | 推理 Gateway（数据面） | 推理请求的鉴权、计量与代理转发 |
| Envoy Gateway / CNI / CoreDNS | 集群内全部工作负载 | 南北向入口与 TLS 终结、Pod 网络与服务发现（见 §5.2） |
| 镜像仓库（Harbor） | image | 推理引擎镜像导入的源与目标仓库 |
| 外部大模型提供方（DeepSeek / OpenAI 兼容） | provider · infer | 已发布外部模型的上游推理端点；连通性以五态结果探测 |
| 外部身份提供方（LDAP / OIDC / SAML / 微信） | auth | 第三方 SSO 联邦登录与用户属性来源 |
| 客户 Webhook 端点（HTTPS） | webhook | 带 HMAC 签名与重试策略的出站事件投递 |
| 模型仓库（ModelScope / HuggingFace） | model | 模型权重下载到共享 JuiceFS 卷 |
| **AIBrix** | Controller · infer · runtime | 分布式推理运行时与推理网关：模型部署对象（`ModelClaim`、`PodAutoscaler`）、KV Cache 感知路由，以及数据面背后的 Envoy 代理。以独立 Helm release 安装；Chart 的 `dataPlane` 值负责把它的网关发布到推理主机名上 |
| **HAMi** | Controller · vgpu | 异构加速卡虚拟化：设备插件、按 Pod 的切分调度，以及 Controller 为共享 GPU 服务设置的 placement-policy 注解 |
| **Volcano**（建设中） | Controller · batch | 批处理与多 Pod 推理角色的 Gang 调度、队列与公平共享。可选组件：未安装时使用原生 Kubernetes 调度 |
| **GPU / NPU Operator** | Controller · accelerator | 厂商驱动、设备插件、拓扑与健康状态；加速卡清单读取它们发布的节点标签 |
| **可观测栈**（Prometheus · OpenTelemetry · Loki） | observability · tracing · 全部工作负载 | 指标、追踪与日志聚合；控制台的可观测页面读取平台已存储的聚合结果 |
| **推理引擎**（vLLM · SGLang · TensorRT-LLM · MindIE · 自研） | Controller · infer | 以 OpenAI 兼容接口对外提供推理。以镜像方式注册，新增引擎是一条注册记录，而不是一次改代码 |

> 鉴权调用链：推理 Gateway 的 Wasm 插件在放行前通过 **gRPC 调用 `auth` 模块**校验 API Key；未命中 Wasm 本地缓存时回源 `auth`，`auth` 再以 Redis 正缓存先承接，两级缓存保证高并发下的低时延，并在超时或不可用时按 fail-closed 拒绝。

## 7. 容量、SLO 与平台级设计决策

### 7.1 容量估算

```
副本数 = ceil(峰值 QPS × 平均 Token 数 ÷ (单副本 tokens/s × 目标利用率))
```

示例：`100 × 1200 ÷ (8000 × 0.7) ≈ 3` 副本。

两个常见错误：单副本吞吐必须**实测**，不能用标称峰值——标称峰值与实测聚合吞吐之间，往往
差着一整条批处理流水线；平均 Token 数要按业务分布算，长尾请求会显著拉高驻留时间。

并发由吞吐推出，而不是反过来：一个 8 卡实例聚合输出 2,000 tokens/s、单请求约 50 tokens/s，
对应约 40 个并发流，而不是 2,000 个。可以用 Little's Law 校验
（`到达率 × 驻留时间 ≈ 并发数`）。

### 7.2 SLO 目标

| 指标 | 目标 | 说明 |
| --- | --- | --- |
| 网关可用性 | ≥ 99.95% | 数据面是所有流量的入口，目标刻意高于推理本身 |
| 推理可用性 | ≥ 99.9% | 按模型、按卡型分别统计，不混算 |
| TTFT P95 | < 1.5s | 基线为 7B 模型、1K prompt，需按实际部署校准 |
| TPOT P95 | < 50ms | 每 Token 延迟，直接决定流式体验 |
| 计量准确率 | ≥ 99.5% | 与引擎 `usage` 对账，差异需归因 |
| 控制面 API P95 | < 300ms | 不包含异步部署耗时 |

这些都是需要按真实基线校准的目标值，不是合同数字。SLA 必须绑定模型、卡型、上下文长度与
并发，并对外给出「基线 + 降级策略」而不是单一数字：同样叫 99.9%，7B 模型 1K 上下文与
70B 模型 128K 上下文完全是两件事，不写清条件的 SLA 签的时候容易，赔的时候一定吵架。

### 7.3 平台级设计决策

| 决策 | 理由 |
| --- | --- |
| **CRD 是期望状态的唯一来源。** Server 写 CR，Controller 调谐。 | 漂移检测与回滚需要一个集群自己拥有的对象，而不是一次性命令。见下方现状说明。 |
| **Outbox + 幂等消费。** 业务事务与消息投递一起提交，投递与提交解耦。 | 按 `request_id` / `deployment_id` 幂等，才让 at-least-once 投递是安全的 |
| **计量以引擎 `usage` 为准。** | 网关 tokenizer 在 chat template 下会与引擎漂移；引擎为准，网关计数兜底 |
| **热路径不写数据库。** 网关只读写本地缓存与队列；PostgreSQL 只由 Server 与异步消费者写入。 | 推理峰值可能是管理流量的几万倍，一次促销不能把平台打死 |
| **第一天就支持多集群。** P0 可以只部署一个集群，但数据模型里必须有 `cluster_id` 与 `placement_policy`。 | 到 P1 再补字段，就要同时改表、改 API、改权限，而且一定会漏 |
| **单元化（Cell）架构。** 每个 Cell 约 100–500 卡，自带网关、Controller 与 Redis。 | 控制爆炸半径：一个 Cell 出问题不能把全国租户一起带走。代价是运维复杂度，收益是故障隔离与容量弹性 |
| **API-first，SDK 生成。** OpenAPI 是契约，Console、SDK、CLI 共用同一套接口。 | 否则各接入形态之间必然行为漂移 |
| **选择无聊的技术。** Go + PostgreSQL + Redis + RabbitMQ + Kubernetes。 | 创新留给推理优化与验证矩阵，而不是底座本身 |

> **现状说明。** CRD 作为唯一真源是目标设计。仓库目前通过消息队列发布期望状态**变更事件**，
> Controller 直接写 Deployment、Service、弹性策略与服务网关路由对象；其中 `ModelClaim`
> 是以 AIBrix CR 形式写入的。把工作负载对象本身也收敛到 CRD，是这条决策尚未完成的一步，
> 按「差距」记录，而不是当作已完成来描述。

## 8. 仓库结构

仓库按职责边界划分，而不是按技术层划分。

| 路径 | 内容 | 说明 |
| --- | --- | --- |
| `services/` | 26 个业务服务目录 | 每个领域一个包：`auth`、`tenancy`、`account`、`model`、`image`、`infer`、`runtime`、`routing`、`provider`、`metering`、`billing`、`batch`、`audit`、`webhook`、`notification`、`observability`、`tracing`、`resourcemetrics`、`accelerator`、`cluster`、`optimization`、`prompt`、`evaluation`、`fault`、`vgpu`、`docs` |
| `apps/` | 可执行入口 | `taas-server` 与 `taas-controller` 两个二进制 |
| `internal/controller` | Kubernetes Operator | 调谐、工作负载编排、状态机与回滚 |
| `pkg/` | 共享基础库 | `config`、`database`、`grpcmiddleware`、`k8s`、`mq`、`redisx`、`registry`、`modelhub`、`logger`、`errors`、`server` |
| `proto/` | 接口契约 | Protobuf 定义；由 `buf` 生成 gRPC stub、HTTP 网关与 OpenAPI 文档 |
| `web/` | 前端 SPA | React 18 + TypeScript + Vite；同一份构建同时服务用户控制台与管理员控制台 |
| `deploy/` | 部署 | `helm/go-taas`（生产 chart）与 `compose`（本地开发） |
| `site/` | 文档站 | 中英双语的架构与设计文档，每个功能点一份 |
| `test/` | 测试 | `fvt`（进程内全栈验证）与 `e2e`（浏览器端到端） |

26 个服务跑在**同一个 Deployment** 里，服务之间是进程内调用。这是早期阶段刻意避免
分布式复杂度，而不是疏忽。真正有价值的异步边界只有一处：业务事务通过消息队列把期望状态
交给 Controller，于是 API 响应时间与集群收敛时间解耦，部署失败也不会把请求线程一起拖住。
