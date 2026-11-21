# 多存储后端、异步模型任务与模型预热调度

| 属性 | 内容 |
| --- | --- |
| 特性点 | 可插拔模型权重存储、异步搬运/解压 Job、节点级模型预热与调度 |
| 文档范围 | 存储抽象、交付模式、无状态搬运 Job、预热索引与调度、API、数据模型、Helm 配置、控制台页面、失败处理与验收标准 |
| 归属模块 | `model`（目录、下载、上传、搬运、缓存记录）、`controller`（Kubernetes Job 与 KServe 缓存资源）、`storage` / `transfer` / `warmup` 包 |
| 相关文档 | [模型目录与一键部署](./model-catalog-deployment.zh-cn.md)、[架构设计](./architecture.zh-cn.md) |
| 状态 | 已实现 |

---

## 1. 背景

控制面此前只假设一种存储形态：一个挂载在 `weightsDir` 的 ReadWriteMany PVC，由进程内下载协程写入、所有推理 Pod 读取。这一假设阻碍了三类真实部署：

1. **对象存储是事实来源。** S3/MinIO/OSS/COS 更便宜、更持久，但推理 Pod 无法直接 `mmap` 对象，必须先把字节物化到可读位置。
2. **多种存储类并存。** 平台可能用高性能 JuiceFS 承载热模型、用 NFS/CephFS 归档冷模型、用 S3 保存导入的检查点；一个模型版本必须声明它所在的存储后端。
3. **大文件搬运不属于 API Server。** 在 server 进程里下载 100 GiB 检查点或跨桶复制，会让控制面有状态、吃内存且无法水平扩展。

因此本特性把“字节放在哪”与“Pod 怎么读”解耦，把所有大流量操作搬进专用 Kubernetes Job，并增加节点本地缓存，让已预热的模型调度到持有权重的节点。

## 2. 目标与非目标

目标：

- 通过 Helm 声明任意数量的文件系统或对象存储后端。
- 目录版本携带存储后端与交付模式（`mount`、`download`、`cache`），不改变部署 API 形状。
- Hub 下载、跨存储复制、删除全部异步、可观测、可取消。
- `taas-server` 保持无状态：搬运不依赖 weights 挂载、不跑搬运协程、不持有 Job 的 Kubernetes 客户端。
- 从 KServe `LocalModelNode` 构建“模型 → 已预热节点”索引，并向 `LLMInferenceService` 注入 `nodeAffinity`，默认 `preferred`，运维可要求 `required`。
- 向后兼容：未声明 `storage` 的安装保持旧的单 PVC 行为。

非目标：

- 租户级存储隔离。存储后端是平台级能力，只有平台管理员可以注册、下载、搬运、预热模型。
- 通用桶间数据搬运器。Job 只在声明的后端之间搬运模型目录前缀。
- 替代 KServe 的缓存 agent：平台创建 `LocalModelNodeGroup`/`LocalModelCache` 并消费 `LocalModelNode` 状态，节点拷贝由 KServe `localmodelnode` agent 完成。
- 本地归档的异步上传。现有管理员上传接口保持有界、流式的同步解压并写入 weights 文件系统；搬运 Job 覆盖 hub 下载、跨后端复制与删除。把解压迁移到 Job 需要暂存卷与新的上传会话 API，作为独立后续项。

## 3. 存储模型

### 3.1 后端类型

| 类型 | 字段 | 适用 |
| --- | --- | --- |
| `filesystem` | `existingClaim`、`provision{enabled,storageClass,accessMode,size}`、`mountPath` | JuiceFS、NFS、CephFS、EFS、Lustre、任意 RWX/RWO PVC |
| `objectstore` | `provider`、`endpoint`、`region`、`bucket`、`prefix`、`forcePathStyle`、`tls`、`credentials` | S3、MinIO、OSS、COS、GCS S3 互操作 |

对象存储凭据不内联：`credentials.existingSecret` 指向 Secret，`accessKeyKey` / `secretKeyKey` 指定键名。搬运 Job 会把它投射到 transfer 命名空间，服务 Pod 会把它投射到推理命名空间，因此同一个 Secret 也必须存在于 `inference.namespace`；chart 不会跨命名空间复制 Secret 数据，需要运维或外部 secret controller 完成。服务端本地上传直接用对象存储写入时，chart 会把每个对象存储后端投射为 `TAAS_STORAGE_<BACKEND>_ACCESS_KEY` 与 `TAAS_STORAGE_<BACKEND>_SECRET_KEY`（`<BACKEND>` 为大写后端名，非 `A-Z0-9` 字符替换为 `_`）；服务端优先读取这两个变量，未配置时回退到 `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`，因此多个对象存储后端不会互相覆盖。

### 3.2 交付模式

`delivery.default` 决定版本的默认交付方式，`delivery.allowed` 限定请求可选项。

| 模式 | Pod 如何读取 | 前提 |
| --- | --- | --- |
| `mount` | 文件系统挂载到 `mountPath`，或对象存储经 CSI 挂载（`delivery.mount.storageClass`） | 可挂载后端 |
| `download` | KServe storage-initializer 拉取模型 URI | `delivery.download.enabled` 且对象存储可达 |
| `cache` | 权重复制到节点本地，Pod 绑定已预热节点 | `warmup.enabled`、节点组、KServe local-model agent |

文件系统后端声明 `provision.enabled` 时由 chart 创建 PVC，否则必须给出 `existingClaim`；两者皆无时在渲染期拒绝。版本逻辑 URI 对文件系统为 `pvc://<claim>/<weightPath>`，对对象存储为 `s3://<bucket>/<prefix>/<weightPath>`。`cache` 交付会把 KServe 源改为缓存 URI 并记录源，以便缓存被驱逐后重建。

**服务侧交付。** 控制面把每个版本的存储后端与交付模式解析进变更事件，Controller 据此构建 Pod：

- `mount` 挂载后端 claim 到 `filesystem.mountPath`；任何无法应用的交付都回退到它。
- `download` 增加一个 `storage-initializer` init 容器，把对象存储 URI 拉到共享 `emptyDir` 的 `/models`。容器获得后端的 endpoint/region/path-style 配置；当 `objectStore.credentials.existingSecret` 存在时，从该 Secret 注入 `AWS_ACCESS_KEY_ID` 与 `AWS_SECRET_ACCESS_KEY`。事件中只传名称，凭据值不出 Secret。
- `cache` 以只读方式挂载预热节点组的 `hostPath`，并让引擎指向 `<hostPath>/models/<storageKey>`，即种子 Job 与 KServe local-model agent 写入的路径；已有的 warm-aware 亲和性把 Pod 约束到已预热节点。

引擎容器丢弃全部 Linux capability，因此 root 也无法绕过文件权限。每个推理 Pod 都以附加组加入权重组（gid 65532），从而能穿过上传时创建的 `0750` 目录，同时不必在每次启动时递归 chown 一个数 GB 的 PVC。

版本在注册、上传或下载时记录 `storage_backend`，这三种请求都显式指定后端（留空仍表示平台默认后端）；下载任务行也保存所选后端，恢复或重试时落回原位置。字段出现前的旧行按默认后端解析。预热不依赖 KServe agent 自带的对象存储凭据：Controller 的种子 Job 同时覆盖 `pvc://` 与已配置后端的对象存储 URI，并从推理命名空间投射后端 Secret。

## 4. 无状态搬运 Job

### 4.1 流程

```text
控制台 / API
   |  POST /api/v1/admin/model-transfers
   v
model 服务 ── 行: model_transfers(state=queued) ──> MQ model.transfers
   |                                                  |
   |                      controller（taas-controller）<+
   |                              | 构建 Job（spec JSON）
   |                              v
   |                     Job/taas-transfer-<id>（transfer.namespace）
   |                       | 挂载文件系统 PVC / 投射对象存储环境变量
   |                       | 进度 -> ConfigMap taas-transfer-progress-<id>
   |                       v
   |                     MQ model.transfers.status
   v                              |
model 服务 <----------------------+
   | 终态：更新行、注册版本、审计
   v
控制台轮询 GET /api/v1/admin/model-transfers/{id}
```

服务端只写意图行与目录注册，Controller 拥有 Job 生命周期并回传观测状态。已终态或不存在的行收到状态消息时直接忽略，重复投递安全。

### 4.2 Job 描述与幂等

描述包含搬运 id、操作、源/目标后端、解析后的 URI 与容器内路径、对象存储引用、可选 hub 下载身份、逻辑权重路径，整体序列化为 `TRANSFER_SPEC_JSON`；Job 名由搬运 id 派生，同一消息重复应用是 no-op。凭据以 `TRANSFER_SOURCE_*` / `TRANSFER_TARGET_*` 环境变量从 Secret 投射，绝不写入描述。

| 操作 | 行为 |
| --- | --- |
| `move` | 把模型前缀复制到其他后端后删除源前缀；整项搬运成功后才把版本的 `storage_backend` 改为目标后端 |
| `copy` | 在两个后端间复制模型前缀并保留源；对象到对象且 endpoint/bucket/prefix 不同时经临时暂存 |
| `download` | 拉取 hub 模型到目标后端，终态成功后注册版本 |
| `delete` | 目录行删除后清理后端中的模型前缀 |
| `cancel` | Controller 以 background 传播删除 Job，行转 `cancelled` |

控制台新建传输只提供 `move`。源后端固定为版本记录的存储后端，调用方只能选择其他目标后端。服务端拒绝与版本身份不同的源后端、缺失的目标后端，以及解析到同一后端或同一 URI 的目标。Job 先复制，复制成功后才删除源，因此失败或中断的搬运不会丢掉唯一副本。终态成功后版本归属改为目标后端，推理读取新后端；失败则版本仍留在源后端。

`copy` 与 `delete` 仍被 API 接受，用于兼容旧客户端和既有自动化；控制台不再为新建传输提供这两个操作。

进度是 Job 写入、Controller 读取的小 JSON（`state`、`bytes`、`totalBytes`、`message`），仅作参考；成败以 Job 终态条件为准。Hub 下载在搬运前上报模型在 Hub 上的总大小，并在运行期间采样目标目录，因此进度是百分比而不是只有结束时的一次采样；model 服务把每个非终态上报同步到关联的 `model_downloads` 行，下载因此离开 `queued` 并展示同一组字节数。Job TTL 清理 Job，进度 ConfigMap 体量小，可按同一 TTL 或运维策略回收。

Job 以 root 运行，才能读取属于其他 uid 的 hub 缓存和上传权重。文件系统复制或下载成功后，Job 把目标目录交给服务端 uid/gid（默认 `65532`，可用 `TRANSFER_TARGET_UID` / `TRANSFER_TARGET_GID` 覆盖），目录行删除时无需特权助手即可清理权重。对象存储目标没有本地目录，不做此处理。

### 4.3 并发与失败处理

- `transfer.concurrency` 限制活跃 Job 数，默认 4，避免同时拉取大量模型压垮集群。
- Job 的 backoff、active deadline、TTL 可按 release 配置；超时以 `failed` 和 Job condition 消息呈现。
- 缺少镜像或 ServiceAccount 属于配置错误；仅当 `transfer.mode=inline` 或未配置 `taas-transfer` 镜像时回退旧的进程内路径，保证离线安装可用。
- 源与目标对象存储 endpoint/bucket/prefix 不同时，先在目标存储的临时前缀落地，成功后清理。

## 5. 模型预热与感知调度

### 5.1 资源

| 资源 | 归属 | 作用 |
| --- | --- | --- |
| `LocalModelNodeGroup` | Controller，来自 `warmup_node_groups` 行 | 可缓存权重的节点池，含容量与 hostPath/PVC |
| `LocalModelCache` | Controller，来自 `WarmModel` 请求 | 一个待复制到节点组的模型 |
| `LocalModelNode` | KServe localmodel controller（节点状态由 agent 维护） | 每节点已下载模型状态 |
| `LLMInferenceService` | Controller | 接收注入的 `nodeAffinity` |

预热节点组**不是 Helm 配置**。平台管理员在“运维 > 预热节点组”中创建、编辑和删除，模型服务校验后写入 `warmup_node_groups`，Controller 监听 `model.warmups.groups` 创建、更新或删除对应的 KServe `LocalModelNodeGroup`。服务端每 30 秒发布一次完整期望集合，Controller 应用全部节点组并删除集合中已不存在的受管节点组，因此重启后无需数据库即可收敛，停机期间删除的节点组也会被清理。仍被排队、预热中或已就绪任务引用的节点组拒绝删除。

KServe 的 local-model 节点 agent 是 DaemonSet，只调度到带
`kserve/localmodel=worker` 标签的节点。节点组的 PV 节点亲和只决定哪些节点属于该池，
不会调度 agent，因此 Controller 会给选择器匹配到的每个节点补上该标签，并在删除节点组时移除；
若节点仍被其他受管节点组选中则保留。缺少该标签的节点没有 agent，其
`LocalModelNode` 永远不会报告 `ModelDownloaded`，该池上的所有预热都会一直停在
`warming`。

`WarmModel` 校验模型版本与解析后的源 URI，支持可选的 `node_groups`（为空表示全部托管节点组），写入 `model_cache_entries`（`queued`）并发布携带完整节点组配置的 `model.warmups`。Controller 确保所选节点组与缓存存在，随后回报 `queued` → `warming` → `ready`（或 `failed`/`cancelled`），携带已预热节点、节点总数与字节数。

KServe storage-initializer 不支持 `pvc://`，local-model agent 也未必携带平台的对象存储凭据。因此 Controller 对能解析到已配置后端的缓存都在节点上自行预热：每轮扫描缓存时找到对应的 `LocalModelNode`，从实时 `LocalModelNodeGroup` 解析节点组 hostPath，在目标节点运行一次性 `taas-transfer` Job（该 hostPath 挂载到 `/target`）。`cache` 交付的推理 Pod 也挂载同一 hostPath，因此修改节点组路径无需发布 Helm 即同时影响两条链路。文件系统源把 PVC 只读挂载到 `/source`；对象存储源把后端 Secret 投射为 `WARMUP_SOURCE_ACCESS_KEY` / `WARMUP_SOURCE_SECRET_KEY`，由 transfer 拉取前缀。两者都复制到 `/target/models/<storageKey>`，只有 Job 成功后才把节点的 `status.modelStatus[cacheName]` 标记为 `ModelDownloaded`。storageKey 与 KServe 一致，取源 URI SHA-256 的前 16 个十六进制字符。种子 Job 以 root 运行，因为上传的权重可能属于任意 uid 且目录为 0750；复制出的文件可被任意用户读取，供非 root 推理进程使用。没有匹配后端的 HTTP 源继续走 KServe 原生路径。

预热列表中的字节数是真实权重大小，而非 KServe 占位值。`WarmModel` 创建缓存时按所选后端解析（文件系统后端遍历目录），失败则回退到该版本最近一次成功的下载或传输记录；列表会修复仍带占位值的旧记录，无法解析时显示为未知而不是 `1 B`。

### 5.2 调度

创建或更新 `LLMInferenceService` 前，Controller 列举 `LocalModelNode` 构建“模型 → 已下载节点”索引。模型存在已预热节点时，注入基于 `kubernetes.io/hostname` 的节点亲和：

- `scheduling.mode: preferred`（默认）使用 `preferredDuringSchedulingIgnoredDuringExecution` 与 `scheduling.weight`（1–100），冷节点仍可运行。
- `scheduling.mode: required` 使用 `requiredDuringSchedulingIgnoredDuringExecution`，只在已预热节点可调度；适合延迟敏感模型，但节点排空时存在容量风险。
- `scheduling.mode: disabled`（或 `warmup.enabled: false`）交由集群调度。

亲和合并进已有 affinity，不覆盖运维自定义规则。

### 5.3 驱逐与自动预热

`warmup.eviction` 配置缓存保留策略（默认 `lru`）与单节点最小剩余空间；每个预热节点组的容量在控制台的节点组中编辑，不再由 Helm 设置。`warmup.auto` 记录“模型就绪或相关服务部署时自动预热”的意图；面向运维的入口仍是 `WarmModel`，未配置预热的部署行为不变。

## 6. 数据模型

| 表 | 关键列 | 说明 |
| --- | --- | --- |
| `model_transfers` | `id`、`model_id`、`version`、`operation`、`source_backend`、`target_backend`、`source_uri`、`target_uri`、`state`、`message`、`bytes`、`total_bytes`、`job_name`、`download_id` | 一次搬运一行；`download_id` 关联异步下载要完成的下载行 |
| `model_cache_entries` | `id`、`model_id`、`model_name`、`version`、`backend`、`source_uri`、`weight_path`、`state`、`message`、`size_bytes`、`warmed_nodes`、`node_groups` | 一个 `LocalModelCache` 一行 |
| `warmup_node_groups` | `name`、`node_selector_json`、`storage_limit`、`host_path`、`storage_class`、时间戳 | 平台管理的预热节点池，是 KServe `LocalModelNodeGroup` 的事实来源 |
| `warmup_nodes` | `name`、`node_groups_json`、`models_json`、`model_count`、`downloaded_count`、`observed_at` | 最近一次完整 `LocalModelNode` 快照；原子替换，被删除节点会消失 |
| `model_versions` | `storage_backend` | 版本所在后端，空表示默认后端 |
| `model_downloads` | `storage_backend` | 下载任务写入的后端，空表示默认后端 |

行只由服务端写入，Controller 仅发布状态；重连无需数据库句柄，也不会与目录分叉。

## 7. API（仅平台管理员）

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/v1/admin/storage-backends` | 列出后端、类型、交付模式与能力 |
| POST | `/api/v1/admin/model-transfers` | 排队把模型移动到其他后端（`copy`/`delete` 仍兼容保留）；模型下载走下载接口 |
| GET | `/api/v1/admin/model-transfers` | 分页列出搬运，最新在前 |
| GET | `/api/v1/admin/model-transfers/{transfer_id}` | 单个搬运及进度 |
| POST | `/api/v1/admin/model-transfers/{transfer_id}:cancel` | 取消排队/运行中的搬运 |
| POST | `/api/v1/admin/model-warmups` | 在指定（或全部）预热节点组预热模型版本 |
| GET | `/api/v1/admin/model-warmups` | 以 `warmups` 字段分页列出预热任务，已就绪在前、其余最新在前 |
| GET | `/api/v1/admin/model-cache-entries` | 同一列表的旧路径别名，字段名为 `caches` |
| POST | `/api/v1/admin/model-warmups/{cache_id}:cancel` | 移除模型预热 |
| GET | `/api/v1/admin/model-warmup-node-groups` | 列出平台管理的预热节点组及就绪/总节点数 |
| POST | `/api/v1/admin/model-warmup-node-groups` | 创建节点组并让 Controller 落地 |
| PUT | `/api/v1/admin/model-warmup-node-groups/{name}` | 编辑节点组，名称不可变 |
| DELETE | `/api/v1/admin/model-warmup-node-groups/{name}` | 无活跃预热引用时删除节点组 |
| GET | `/api/v1/admin/model-warmup-nodes` | 列出每个节点的最近一次 `LocalModelNode` 观测 |

既有 `StartModelDownload` 请求不变；配置搬运镜像后立即返回排队下载，搬运行携带同一 `download_id`，轮询下载接口的客户端可看到状态迁移，而不是长连接等待。

## 8. 控制台

平台管理员控制台按运维任务重新组织：

- **运维 > 存储后端**：只读展示后端类型、endpoint/claim、挂载路径、交付模式与是否允许节点缓存。列表上方用设计过的提示卡解释 `mount`、`download`、`cache`，并用一条提示说明：节点缓存同时需要“允许缓存交付的后端”和“覆盖目标节点的预热节点组”。
- **运维 > 预热节点组**：创建、编辑、删除平台管理的节点组（选择器、存储上限、宿主机路径、存储类），并展示就绪/总节点数；提示卡说明节点组是运行时配置而非 Helm 值。
- **模型**：单页三个标签：**模型目录**、**预热**、**模型传输**。每个标签的内容与标签栏留出上间距，标签内的操作按钮靠右。目录保留注册、上传、下载与部署，每个导入表单都指定存储后端（默认后端排最前），列表与详情展示最新版本的存储后端；模型目录的行操作提供**预热**与**传输模型**：前者点击后切到预热标签并打开预热弹框，后者切到模型传输标签并打开新建传输弹框，均自动选中该模型。预热包含原模型缓存列表和新增的**节点**标签，展示每个 `LocalModelNode` 的节点组与模型副本；模型传输包含异步移动/下载与历史记录，并支持取消；下载任务由下载入口创建并同样列在这里。新建传输**只能选择移动**：源后端只读展示所选模型的当前后端，目标后端只列出其他已配置后端；移动成功后源数据被删除，模型详情中的存储后端同步变为目标后端。任务的失败原因显示为状态后的警告图标，悬停或聚焦时用提示框展示消息，避免长错误撑高表格。旧路由 `/admin/model-transfers`、`/admin/model-cache-entries` 会重定向到对应标签，书签继续可用。

所有视图遵循共享表格、空态、错误与分页约定（`PAGE_SIZE = 20`），文案全部来自 API 数据而非硬编码样例。

## 9. Helm 配置

```yaml
storage:
  defaultBackend: shared
  backends:
    shared:
      type: filesystem
      filesystem:
        existingClaim: model-weights
        mountPath: /data/weights
      delivery:
        default: mount
        allowed: [mount, cache]
    archive:
      type: objectstore
      objectStore:
        provider: s3
        endpoint: https://s3.example.com
        bucket: taas-models
        prefix: weights
        forcePathStyle: false
        credentials:
          existingSecret: archive-keys
      delivery:
        default: cache
        allowed: [cache, download]

transfer:
  enabled: true
  mode: job
  namespace: taas-jobs
  concurrency: 4
  image:
    repository: taas-transfer
    tag: ""
  serviceAccount:
    create: true

warmup:
  enabled: true
  # 预热节点组不是 Helm 值。在“运维 > 预热节点组”运行时创建；模型服务写入
  # PostgreSQL，Controller 创建对应的 KServe LocalModelNodeGroup。
  scheduling:
    mode: preferred
    weight: 100
```

校验规则：

- `filesystem` 必须声明 `existingClaim` 或 `provision.enabled`。
- `delivery.default` 必须属于 `delivery.allowed`；`cache` 需要 `warmup.enabled`。
- `transfer.serviceAccount.create` 为真时 chart 创建命名空间、ServiceAccount 与仅能读写 progress ConfigMap 的 Role。
- Controller ClusterRole 仅在相应特性启用时增加 `batch/jobs`、`configmaps` 与 `serving.kserve.io` 本地模型权限。
- `warmup.nodeGroups` 已从 `values.yaml` 与 `values.schema.json` 移除；旧值会被忽略。升级后必须在“运维 > 预热节点组”至少创建一个节点组，否则首次预热会返回清晰的 `no warmup node group is configured` 错误。

## 10. 兼容与迁移

- 未配置 `storage.backends` 时，chart 用 `controller.weights.pvcName`、`storageClass`、`mountPath` 合成一个文件系统后端，既有 release 升级无需改值。
- `model_versions.storage_backend` 为新增列，空值在读取时解析为默认后端。
- `model_transfers`、`model_cache_entries` 为新增表，`model_versions.storage_backend` 为新增列；它们都是 GORM 模型，由 model 服务启动时 `Migrate`（`db.AutoMigrate`）创建/变更。本仓库没有手写 init-SQL 脚本；以外部方式初始化 schema 的安装必须在启动新 server 前执行同一份 `AutoMigrate`（或等价的增量 DDL）。
- 搬运默认 `job`；`mode: inline` 保留旧进程内行为，便于尚未推送 `taas-transfer` 镜像的集群，服务日志说明所走路径。

## 11. 安全

- 所有新 RPC 要求平台管理员角色，不提供租户级存储/搬运访问。
- 对象存储凭据只从 Secret 读取并投射给 Job，绝不出现在 API 响应、日志或 Job 描述中。
- 上传解压拒绝绝对路径、`..` 穿越、逃逸目标的符号链接，并限制文件数与总字节。
- 搬运 Job 使用专用 ServiceAccount 与仅限 transfer 命名空间 progress ConfigMap 的 Role。

## 12. 可观测性与运维

- 搬运行携带字节/总量与可读消息，控制台与 API 均可见。
- progress ConfigMap 以搬运 id 命名；Job 未写进度只表示没有进度，不会被误判成功。
- Controller 以 info 记录 Job 创建、缓存协调与亲和注入；缺少 LocalModelNode CRD 是 warning，不使协调失败。
- 运维可检查 transfer 命名空间中的 Job 与推理命名空间中的 KServe 本地模型资源。

## 13. 测试与验收

单元测试覆盖存储归一化/校验、搬运状态机、上传解压、预热投影、Job 构造与取消、预热资源创建与亲和注入，以及控制台构建/类型检查；FVT 覆盖新 REST 端点与分页。验收要求：

1. 模型版本可在文件系统与对象存储之间往返复制，server 进程不参与字节搬运。
2. 配置搬运镜像的下载只在 Job 成功后注册版本，Job 失败不留下已注册版本。
3. 在控制台创建预热节点组后，Controller 创建对应 KServe `LocalModelNodeGroup`；选择该组的 `WarmModel` 创建缓存，`ready` 携带已预热节点，该模型的部署获得配置的亲和。
4. 未配置 `storage`/`transfer`/`warmup` 的安装升级后行为不变。
5. 模型页的目录、预热（模型与节点）和传输标签渲染真实 API 数据、分页，并可取消非终态任务；两个运维页面展示存储交付提示并提供节点组 CRUD。
