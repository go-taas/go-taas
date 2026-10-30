# go-taas Helm Chart

将 Go TaaS 控制面部署到 Kubernetes，并把所有第三方依赖的连接方式变成 values 配置项：Chart 既可以自行置备单副本的 PostgreSQL / Redis / RabbitMQ / Keycloak，也可以让控制面连接运维托管的外部服务。

## Chart 部署的资源

| 资源 | 生成条件 | 说明 |
| --- | --- | --- |
| `Deployment/<release>-server` | 始终 | `taas-server`：gRPC（9090）、REST 网关与内置管理控制台（9091）、指标与健康检查（9092） |
| `Service/<release>-server` | 始终 | 默认 ClusterIP；同时发布 gRPC 与监控端口 |
| `ServiceAccount`、`ClusterRole`、`ClusterRoleBinding` | `controller.rbac.create` | 控制器调谐推理负载所需的权限 |
| `Deployment/<release>-controller` | 始终 | 消费期望状态消息流并驱动 Kubernetes |
| `Namespace/<推理命名空间>` | `controller.createNamespace` | 控制器管理的命名空间，取自 `controller.config.controller.namespace` |
| `Secret/<release>-secrets` | 未设置 `secrets.existingSecret` 时 | 依赖密码与静态加密主密钥 |
| `ConfigMap/<release>-config-overrides` | `configOverrides` | 在镜像内置配置之后合并的原始配置文件 |
| PostgreSQL / Redis / RabbitMQ / Keycloak 的 `Deployment` + `Service` + `PersistentVolumeClaim` | 各依赖 `mode: internal` 时 | 面向评估与小规模安装的单副本实例 |

Chart 不创建 Ingress。对外暴露方式由 `taasServer.service.type`（`ClusterIP`、`NodePort`、`LoadBalancer`）决定，或在网关端口前自行添加 Ingress。

## 推理网关插件（鉴权与计量）

`gatewayPlugin.enabled` 会给推理网关挂上一个 Wasm 过滤器，校验 API Key 并上报词元用量，使推理流量在数据面
即完成鉴权，而不是被无条件转发。它**默认关闭**：开启后网关会开始拒绝未验证的请求，属于现有安装应当主动选择
的行为变更。

```yaml
gatewayPlugin:
  enabled: true
  # 留空时 Chart 会把共享令牌生成到 Release Secret；填入则复用它。
  token: ""
  protectedPrefixes: ["/v1/"]
  cacheTTL: 30s          # 判定结果的复用时长（每个 Envoy worker）
  verifyTimeout: 500ms   # 超时即失败关闭（拒绝请求）
  failOpen: false        # 插件故障时阻断流量，而不是放行
  internalHostname: taas-internal.invalid   # 过滤器调用的路由；不进入 DNS
```

它会创建：一个专用的数据面 `Gateway`、一个针对数据面 `HTTPRoute` 的 `EnvoyExtensionPolicy`，以及给过滤器
自身内部调用用的 `HTTPRoute`。过滤器产物内置于服务端镜像并由其提供，因此一次发布只对应一个插件版本。更换产物
需要重启数据面 Envoy Deployment —— Envoy 按 worker 缓存 Wasm 虚拟机，重启也是重新拉取代码的过程。全新安装
时这一步自动完成。

## 数据面有独立的网关

`dataPlane.enabled` 还会创建一个名为 `taas-inference` 的 `Gateway`，使推理流量与管理流量不共用代理：

| | 控制面 | 数据面 |
| --- | --- | --- |
| Gateway | 平台自有的 | `taas-inference`（本 Chart） |
| 承载 | `taas.<域名>` 与平台其他站点 | 仅推理域名 |
| 地址 | 平台网关的地址 | 独立的 LoadBalancer 地址 |

它使用 `aibrix-eg` GatewayClass，从而继承面向推理的 Envoy 调优，并且**不会**在监听器上固定推理域名：域名是
控制台的设置项，由服务端写入路由，因此修改域名无需 `helm upgrade`。

运维需要提供两样东西：

1. **Release 命名空间中的 TLS Secret**，覆盖推理域名：

   ```bash
   kubectl -n <namespace> create secret tls taas-inference-tls \
     --cert=fullchain.pem --key=privkey.pem
   ```

   Chart 不管理它：证书的生命周期长于发布，通常由域名管理者轮换。可用
   `dataPlane.gateway.tlsSecretName` 改名。

2. **指向数据面 Gateway 地址的 DNS 记录**，该地址与控制台的不同：

   ```bash
   kubectl -n <namespace> get gateway taas-inference \
     -o jsonpath='{.status.addresses[0].value}'
   ```

   指到控制台网关会返回 404：推理在那里没有任何路由。

若设置了 `gatewayPlugin.pluginSha256`，摘要不一致的产物会被拒绝。当 URL 指向本 Release 自己的服务端时请
留空：Helm 会先应用策略、后滚动新 Pod，固定摘要会一直失败到滚动结束。

插件**关闭**时安装行为不变：网关照常转发请求，无论是否带 Bearer 令牌。

项目链接：[官方网站](https://go-taas.com) ·
[文档](https://go-taas.com/docs.html) ·
[源码](https://github.com/go-taas/go-taas) ·
[社区支持](https://github.com/go-taas/go-taas/discussions)。

## SSO 与内置 Keycloak

`dependencies.keycloak.mode` 有三个取值，只有后两个会安装任何东西：

| 取值 | 行为 |
| --- | --- |
| `disabled`（**默认**） | 不部署也不 seed 任何 IdP；平台自有账号体系是唯一登录方式，靠 seed 的 `admin` 账号登录 |
| `internal` | Chart 置备单副本 Keycloak；首次启动时 `taas-server` 会 seed 出 `go-taas` realm 的客户端（`go-taas-console`）与 `admin`/`admin` 用户（带 `admin` realm 角色），auth 的 Migrate 钩子插入对应的 SSO 提供商记录 |
| `external` | 不部署 Keycloak；把 `dependencies.keycloak.external` 中的 issuer / client / redirect URI seed 进提供商记录，控制台对着你的 IdP 登录 |

seed 的**本地**账号（`admin` + `auth.seedSso.adminLocalPassword`）在任何模式下都会创建，因为平台自有账号体系必须能在没有外部 IdP 时工作。`internal` 模式下的 `admin`/`admin` 是 *Keycloak* 的账号，本地账号仍然使用本地密码。

若要通过网关或 Ingress 暴露控制台，请把 redirect URI 与 web origin 设置为公网地址：

```yaml
dependencies:
  keycloak:
    internal:
      redirectUri: "https://taas.example.com/api/v1/auth/sso/*"
      webOrigin: "https://taas.example.com"
```

内置 Keycloak 以 uid 1000 运行；Chart 会设置 `fsGroup: 1000` 使数据 PVC 可写，并在管理端口（`dependencies.keycloak.internal.managementPort`，默认 9000）暴露健康检查端点，供就绪/存活探针使用。

若要使用已有 IdP 而非内置实例，把 `dependencies.keycloak.mode` 设为 `external`，并提供 issuer、client id、client secret 与 redirect URI。此时 Chart 不创建 Keycloak 的 Deployment/Service/PVC，并把 SSO seed 指向外部 IdP。

> **生产注意**：seed 出来的凭据仅用于评估 —— 本地管理员的 `auth.seedSso.adminLocalPassword`，以及
> `mode: internal` 时 Keycloak 的 `admin`/`admin`。公开暴露控制台前请两者都修改：本地密码在账号的
> 个人资料页修改（或安装前写入 values 文件），Keycloak 的密码在 Keycloak 管理控制台修改。完全不用 IdP 时，
> 保持 `dependencies.keycloak.mode: disabled`（默认值）即可。

### 内置的本地 admin 账号

首次启动时平台会在**自己的账号系统**中 seed 一个 `admin` 账号（不依赖外部 IdP）：初始密码以 Argon2id 哈希写入 `users` 表。通过一级配置项 `auth.seedSso.adminLocalPassword` 设置（默认 `Pass4admin`）：

```bash
helm upgrade --install taas deploy/helm/go-taas --namespace taas \
  --set auth.seedSso.adminLocalPassword='YourStr0ng!Pass'
```

密码需满足平台策略：至少 8 位，且至少包含大写字母、小写字母、数字、特殊符号中的三类。seed 是幂等的——只在密码缺失时写入，不会覆盖运维已修改的密码。平台管理员登录时免邮箱验证码，保证引导管理员始终可登录。

## 资源标签

Chart 创建的每个资源都带 Kubernetes 推荐标签：

| 标签 | 取值 |
| --- | --- |
| `helm.sh/chart` | `<chart>-<version>` |
| `app.kubernetes.io/name` | Chart 名称（`go-taas`，可用 `nameOverride` 覆盖） |
| `app.kubernetes.io/instance` | Release 名称 |
| `app.kubernetes.io/version` | Chart 的 `appVersion` |
| `app.kubernetes.io/managed-by` | Chart 资源为 `Helm`，Controller 创建的资源为 `taas-controller` |
| `app.kubernetes.io/part-of` | `go-taas` |
| `app.kubernetes.io/component` | `server`、`controller`、`postgresql`、`redis`、`rabbitmq` 或 `keycloak` |

平台自有键统一使用项目自己的 DNS 前缀 `go-taas.com/`（取自官方网站）。不要使用项目并不拥有的域名（已弃用的 `taas.go-taas.github.io/` 与 `go-taas.io/`）：标签前缀应当能被使用方解析。

| 键 | 设置位置 | 含义 |
| --- | --- | --- |
| `go-taas.com/service-id` | 推理 Deployment、Service、Pod、ModelClaim | 所属推理服务 |
| `go-taas.com/replica-index` | 推理 Pod | 掩码后的副本序号 |
| `go-taas.com/accelerator`、`go-taas.com/accelerator-type` | 推理 Pod（node selector）、节点 | 请求的加速卡厂商与卡型 |
| `go-taas.com/driver-version`、`go-taas.com/device-plugin` | 节点 | 加速卡驱动与 device-plugin 健康信号 |
| `go-taas.com/model`、`go-taas.com/model-id`、`go-taas.com/origin` | ModelClaim | 模型名、目录 ID，以及 Claim 来自服务还是注册 |
| `go-taas.com/task-type`、`go-taas.com/task-id` | 镜像预热 Job | 预热任务标识 |
| `go-taas.com/role` | compose 集群资源 | 存储卷、Secret 或命名空间的静态角色 |
| `go-taas.com/vgpu-pool` | 节点 | vGPU 池成员（`vgpu.hami.poolLabelPrefix`，可配置） |

出现在 selector 中的键属于 API 面：改动会滚动受影响的工作负载，因此按迁移处理，而不是改名。`taasServer.podLabels`、`controller.podLabels` 与各依赖的 `podLabels` 用于追加标签，不会覆盖上表。

## 环境要求

* Kubernetes 1.25+，且存在默认 StorageClass
* Helm 3.8+（模板使用了 `dig`）或 Helm 4.x
* 创建集群级对象的权限（Chart 会创建 ClusterRole 与 ClusterRoleBinding）

### 第三方组件

Chart 部署平台本身，不部署其基础设施。以下组件需用各自的 Chart 独立安装，并遵循其官方文档：

| 组件 | 用途 | 相关 values | 官方安装文档 |
| --- | --- | --- | --- |
| Gateway API CRD + Envoy Gateway | 通过 Gateway 暴露控制台；数据面网关插件 | `dataPlane.enabled`、`gatewayPlugin.enabled` | [Gateway API](https://gateway-api.sigs.k8s.io/guides/) · [Envoy Gateway](https://gateway.envoyproxy.io/docs/install/install-helm/) |
| AIBrix | 推理服务（`StormService`、`ModelClaim`、`PodAutoscaler`、`aibrix-eg` 类） | `dataPlane.backend`、`controller.config.runtime.aibrix.*` | [AIBrix](https://aibrix.readthedocs.io/latest/getting_started/installation/installation.html) |
| Volcano | Gang 调度与优先级抢占；values 中引用的 PriorityClass | `controller.config.controller.scheduling` | [Volcano](https://volcano.sh/en/docs/installation/) |
| JuiceFS CSI（或任意 ReadWriteMany 存储类） | 服务端与推理 Pod 共享的同一套模型权重 | `controller.config.controller.weights.storageClass`、`taasServer.extraVolumes` | [JuiceFS CSI](https://juicefs.com/docs/csi/getting_started/) |
| Harbor（可选） | 镜像导入 / 上传到自有仓库 | `harbor`（或 `taasServer.harbor`） | [Harbor](https://goharbor.io/docs/2.11.0/install-config/) |
| kube-prometheus-stack（可选） | 指标与延迟页面 | `monitoring.prometheusURL` | [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack) |
| HAMi（可选） | vGPU 切分 | `vgpu.hami.*`（配置文件） | [HAMi](https://project-hami.io/docs/get-started/deploy-with-helm) |
| cert-manager（可选） | 签发控制台与推理域名证书 | — | [cert-manager](https://cert-manager.io/docs/installation/helm/) |

默认值让必需项降为零：`dataPlane.enabled`、`gatewayPlugin.enabled` 为关闭，`harbor.hostname` 为空，
`controller.config.controller.scheduling` 为空时推理 Pod 使用集群默认调度器。包含 Chart 无法代劳的集群
准备的逐步路径见[安装与部署指南](../../../site/docs/getting-started/installation.zh-cn.md)。

## 安装

```bash
helm install taas deploy/helm/go-taas \
  --namespace taas --create-namespace \
  --set auth.seedSso.adminLocalPassword='<YourStr0ng!Pass>'
```

在不做其他覆盖时，Release 会在集群内置备 PostgreSQL、Redis 与 RabbitMQ，把它们的凭据（以及两个加密主密钥）
生成到 Chart Secret 中，并把控制面自动连上去。默认**不部署** Keycloak（`dependencies.keycloak.mode:
disabled`）：平台在自己的账号体系中 seed 管理员 `admin`，密码取自 `auth.seedSso.adminLocalPassword`，
因此不需要任何 IdP 即可登录。密码需满足策略（至少 8 位，且大写/小写/数字/符号中至少三类）。控制器管理的
命名空间为 `taas-infer`。

资源名由发布名与 Chart 名拼接：发布名 `taas` + Chart `go-taas` 得到 `taas-go-taas-server`、
`taas-go-taas-controller`、`taas-go-taas-postgresql` 等。不确定时直接查服务：

```bash
kubectl --namespace taas get svc -l app.kubernetes.io/component=server
kubectl --namespace taas port-forward svc/taas-go-taas-server 9091:9091
# 控制台：http://127.0.0.1:9091/ · 管理控制台：http://127.0.0.1:9091/admin
# 用 "admin" 与 auth.seedSso.adminLocalPassword 登录
```

服务端在依赖不可达时会快速失败退出，由 Kubernetes 重启；因此全新安装时，内置的 PostgreSQL、Redis、
RabbitMQ 尚在启动期间服务端可能重启一两次，随后自动收敛。控制器不配置探针：它不提供 HTTP 服务，
且镜像内没有 shell。

### 暴露控制台

Chart 不为控制台创建 Ingress 或 HTTPRoute。请把它挂载到为你的域名终止 TLS 的 Gateway 上（参见
[Gateway API 与 Envoy Gateway](../../../site/docs/getting-started/installation.zh-cn.md#31-gateway-api-与-envoy-gateway)）：

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: taas-console
  namespace: taas
spec:
  parentRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: envoy-gateway
      namespace: envoy-gateway-system
      sectionName: https
  hostnames:
    - taas.example.com
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /
      backendRefs:
        - kind: Service
          name: taas-go-taas-server
          port: 9091
      timeouts:
        request: 900s      # 模型与镜像上传都是长时间的单次请求
```

也可以把 `taasServer.service.type` 设为 `NodePort` 或 `LoadBalancer`，并在前面放自己的代理；无论哪种方式，
都应在控制台之前终止 TLS。

## 连接外部依赖

把某个依赖的 `mode` 设为 `external`，并在其 `external` 段描述连接信息。模式按依赖独立生效，因此可以是「托管数据库 + 集群内缓存」的组合：

```yaml
dependencies:
  postgresql:
    mode: external
    maxOpenConns: 64
    external:
      host: taas-pg.example.rds.amazonaws.com
      port: 5432
      database: taas
      username: taas
      password: ""              # 建议改用 secrets.existingSecret
      sslMode: require

  redis:
    mode: external
    database: 1                 # 逻辑库序号
    external:
      host: taas-valkey.example.cache.amazonaws.com
      port: 6379
      password: ""

  rabbitmq:
    mode: external
    namespace: taas-prod        # 按环境隔离主题/主题空间
    external:
      url: amqp://guest:guest@rabbitmq.example.internal:5672/
```

此时 Chart 不再渲染这些依赖的任何资源，而是通过 `CONFIG_*` 环境变量把控制面指向配置的地址（`CONFIG_DB_MASTER_HOST`、`CONFIG_REDIS_HOST`、`CONFIG_MQ_URL` 等）。

### 使用已有的 Secret 提供凭据

设置 `secrets.existingSecret` 即可让凭据不落在 values 文件中（External Secrets、Sealed Secrets、Vault 等）。此时 Chart 不创建 Secret，而是从该 Secret 读取以下键：

| 键 | 用途 |
| --- | --- |
| `postgresql-password` | `CONFIG_DB_MASTER_PASSWORD` |
| `redis-password` | `CONFIG_REDIS_PASSWORD`（实例未启用认证时为 `""`） |
| `webhook-secret-encryption-key` | `CONFIG_WEBHOOK_SECRETENCRYPTIONKEY`（可选；仅在通过其他方式配置该密钥时可省略） |
| `provider-secret-encryption-key` | `CONFIG_PROVIDER_SECRETENCRYPTIONKEY`（可选，规则同上） |

```yaml
secrets:
  existingSecret: taas-credentials
```

由 Chart 自行管理 Secret 时，依赖密码只在首次生成，后续升级会通过 lookup 复用，因此 `helm upgrade` 与 `helm rollback` 不会轮换正在使用的密码。该 Secret 带 `helm.sh/resource-policy: keep`，`helm uninstall` 后仍保留，便于之后重新安装时继续使用。

## 配置方式

按优先级从高到低有三种：

1. **`taasServer.config` / `taasServer.secretConfig`**（`controller` 下同名）——覆盖镜像内置的 `configs/*.yaml`，渲染为 `CONFIG_*` 环境变量。键与配置文件路径一一对应，叶子节点必须是标量：

   | Chart 路径 | 环境变量 |
   | --- | --- |
   | `taasServer.config.infer.endpointBaseURL` | `CONFIG_INFER_ENDPOINTBASEURL` |
   | `taasServer.config.model.weightsDir` | `CONFIG_MODEL_WEIGHTSDIR` |
   | `taasServer.secretConfig.image.harbor.password` | `CONFIG_IMAGE_HARBOR_PASSWORD`（来自 Secret） |
   | `controller.config.controller.namespace` | `CONFIG_CONTROLLER_NAMESPACE` |
   | `controller.config.controller.weights.storageClass` | `CONFIG_CONTROLLER_WEIGHTS_STORAGECLASS` |
   | `controller.config.runtime.aibrix.namespace` | `CONFIG_RUNTIME_AIBRIX_NAMESPACE` |

   在这些键下使用列表或字典会渲染失败，并提示改用 `configOverrides`。

2. **`configOverrides`**——挂载进两个工作负载的原始配置文件，在镜像内置的 `configs/*.yaml` 之后合并（`/etc/taas/configs/zz-go-taas-overrides.yaml`）。用于列表与字典，例如镜像目录种子、`auth.adminRoles`、`optimization.peakHours`。

3. **镜像内置的 `configs/*.yaml` 默认值**。

### 主要 values

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `image.registry` / `image.repository` | `ghcr.io` / `go-taas/go-taas` | 镜像仓库与仓库路径，组件名自动拼接在后 |
| `image.tag` | `""` | 为空时使用 `v<Chart.appVersion>` |
| `dependencies.postgresql.mode` | `internal` | `internal` 或 `external` |
| `dependencies.postgresql.maxOpenConns` / `maxIdleConns` / `connMaxLifetime` / `debug` | `32` / `8` / `30m` / `false` | 连接池参数，两种模式均生效 |
| `dependencies.postgresql.external.*` | — | `host`、`port`、`database`、`username`、`password`、`sslMode` |
| `dependencies.postgresql.internal.*` | — | `image`、`auth`、`persistence`、调度与资源 |
| `dependencies.redis.mode` | `internal` | `internal` 或 `external` |
| `dependencies.redis.database` | `0` | 逻辑库序号 |
| `dependencies.redis.maxIdle` / `maxActive` / `idleTimeout` | `8` / `64` / `5m` | 连接池参数，两种模式均生效 |
| `dependencies.redis.external.*` | — | `host`、`port`、`password` |
| `dependencies.redis.internal.auth.enabled` | `true` | 置备的实例是否要求密码 |
| `dependencies.rabbitmq.mode` | `internal` | `internal` 或 `external` |
| `dependencies.rabbitmq.driver` / `namespace` | `rabbitmq` / `taas` | 消息队列驱动与主题命名空间 |
| `dependencies.rabbitmq.external.url` | — | 完整连接 URL（可内含凭据） |
| `dependencies.rabbitmq.internal.port` | `5672` | AMQP 客户端端口 |
| `dependencies.*.internal.persistence.enabled` / `size` / `storageClass` | `true` / `10Gi`-`8Gi` / `""` | 置备实例的存储配置 |
| `secrets.existingSecret` | `""` | 保存上述凭据的已有 Secret |
| `secrets.webhookSecretEncryptionKey` / `providerSecretEncryptionKey` | `""` | 为空时随机生成 |
| `taasServer.enabled` / `replicaCount` | `true` / `1` | 是否部署控制面服务端及副本数 |
| `taasServer.service.type` | `ClusterIP` | `ClusterIP`、`NodePort` 或 `LoadBalancer` |
| `taasServer.service.gateway.port` | `9091` | REST 网关与控制台 |
| `taasServer.service.grpc.enabled` / `port` | `true` / `9090` | gRPC API |
| `taasServer.service.monitor.enabled` / `port` | `true` / `9092` | Prometheus 指标与 `/healthz` |
| `taasServer.args` | `[]` | 额外容器参数 |
| `taasServer.config` / `secretConfig` | `{}` | `configs/*.yaml` 覆盖项 |
| `taasServer.extraEnv` / `extraVolumes` / `extraVolumeMounts` | `[]` | 逃生舱：共享权重文件系统、`secretKeyRef` 等 |
| `taasServer.livenessProbe` / `readinessProbe` | 启用 | 基于监控端口的 HTTP 探针 |
| `controller.enabled` / `replicaCount` | `true` / `1` | 是否部署控制器及副本数 |
| `controller.rbac.create` | `true` | 是否创建 ClusterRole 及其绑定 |
| `controller.rbac.existingClusterRole` | `""` | 改为绑定已有的 ClusterRole |
| `controller.createNamespace` | `true` | 是否创建受管推理命名空间 |
| `controller.config.controller.namespace` | `taas-infer` | 控制器管理的命名空间 |
| `controller.config.controller.weights.*` | `model-weights` / `/data/weights` / `""` | 共享权重 PVC 名称、挂载路径、StorageClass |
| `controller.config.controller.scheduling.schedulerName` | `""` | 设为 `volcano` 时启用 gang 调度（控制器写入 PodGroup）；为空则使用集群默认调度器 |
| `controller.config.controller.scheduling.priorityClassHigh` / `Normal` / `Low` | `""` | 服务优先级映射到的 PriorityClass；未映射时 Pod 使用集群默认类 |
| `dependencies.keycloak.mode` | `disabled` | `disabled`、`internal`（Chart 置备 Keycloak）或 `external` |
| `dependencies.keycloak.external.*` | — | `issuer`、`clientId`、`clientSecret`、`redirectUri`（seed 进 SSO 提供商记录） |
| `dependencies.keycloak.internal.redirectUri` / `webOrigin` | 本机地址 | `mode: internal` 时控制台的公网地址 |
| `auth.seedSso.adminLocalPassword` | `Pass4admin` | seed 的平台管理员 `admin` 的初始密码 |
| `harbor.hostname` / `project` / `username` | `""` / `taas` / `""` | 内部镜像仓库；`hostname` 为空时镜像导入与上传关闭（`taasServer.harbor.*` 为兼容别名） |
| `harbor.password` / `existingSecret` / `existingSecretKey` | `""` / `""` / `harbor-password` | 推送凭据，可用内联值或已有 Secret |
| `harbor.uploadMaxArchiveBytes` | `0`（8 GiB） | 上传端点接受的镜像包大小上限 |
| `dataPlane.enabled` / `hostname` | `false` / `""` | 通过 Chart 创建的 Gateway 以某个域名发布推理网关 |
| `dataPlane.parentRef.name` / `namespace` / `sectionName` | `taas-inference` / 发布命名空间 / `https` | 为推理域名终止 TLS 的 Gateway |
| `dataPlane.backend.name` / `namespace` / `port` | AIBrix Envoy Service / `yunzhi-system` / `80` | 数据面路由转发到的 Service |
| `gatewayPlugin.enabled` / `failOpen` / `cacheTTL` / `verifyTimeout` | `false` / `false` / `30s` / `500ms` | 校验 API Key 并上报计量的 Wasm 过滤器 |
| `monitoring.prometheusURL` | `""` | Prometheus 地址；为空时回退到控制器自身采集的样本 |
| `monitoring.inferencePodMonitor.*` | 启用 | 抓取引擎自身指标的 PodMonitor（需要 Prometheus Operator CRD） |
| `configOverrides` | `{}` | 最后合并的原始配置文件 |

`values.schema.json` 校验类型、`mode` 与 `service.type` 枚举以及端口范围；无法用 Schema 表达的跨字段规则（external 依赖必须有 host、external 数据库必须提供密码或设置 `secrets.existingSecret`）由模板给出明确报错。

## 镜像仓库（Harbor）

镜像目录的导入与上传流程会把引擎镜像推送到集群自有仓库。运维只需设置两个直观的值：

```yaml
# 规范位置
harbor:
  hostname: hub.example.com        # 为空则关闭导入与上传
  project: taas                    # 导入镜像落在该 Harbor 项目
  username: admin
  existingSecret: taas-harbor-push # 保存密码的 Secret
  existingSecretKey: harbor-password
  # password: ""                   # 内联写法，不建议；优先 existingSecret
  uploadMaxArchiveBytes: 0         # 0 表示默认 8 GiB
```

`taasServer.harbor` 是旧文档写法的兼容别名；两条路径渲染出同样的 `CONFIG_IMAGE_HARBOR_*` 环境变量，
两者同时设置时以顶层 `harbor` 为准。`hostname` 为空时，控制台会禁用导入/上传并说明原因，而不是在传输
途中失败。

## 共享模型权重

推理 Pod 与控制面必须读取同一份模型权重。控制器会在其管理的命名空间内创建 PVC（`controller.config.controller.weights.pvcName`），从 `controller.config.controller.weights.storageClass` 置备——参考部署中是支持 `ReadWriteMany` 的 JuiceFS StorageClass。该值为空时使用集群默认 StorageClass，而默认 StorageClass 很少支持 `ReadWriteMany`：除冒烟测试外都应显式设置，并通过 `taasServer.extraVolumes` / `taasServer.extraVolumeMounts` 将同一文件系统挂载进服务端，同时配合 `taasServer.config.model.weightsDir`。

## 生产环境检查清单

* PostgreSQL、Redis、RabbitMQ 使用 `dependencies.*.mode: external`（或由 Chart 之外的 Operator 管理）；内置实例是单副本且以 `Recreate` 策略重启。
* 设置 `secrets.existingSecret`，并为其提供强随机的 `webhook-secret-encryption-key` 与 `provider-secret-encryption-key`——留空会让平台退回到其文档中说明的开发密钥。
* 远程数据库设置 `dependencies.postgresql.external.sslMode` 为 `require` 或 `verify-full`。
* 当 Redis/RabbitMQ 可被集群外部访问时，保持 `dependencies.redis.internal.auth.enabled: true`，或使用 `dependencies.rabbitmq.mode: external`（亦或带认证的内置实例）。
* 将 `controller.config.controller.weights.storageClass` 设置为支持 `ReadWriteMany` 的 StorageClass。
* 为两个组件以及每个内置依赖设置 `resources` 的 requests/limits。
* 控制台需要从集群外访问时，使用 `taasServer.service.type: LoadBalancer`（或 Ingress）并在网关前启用 TLS。

## 卸载

```bash
helm uninstall taas --namespace taas
```

Release 的 Secret（生成的凭据）与推理命名空间（带 `helm.sh/resource-policy: keep`）会保留，因此运行中的推理负载与后续重新安装都不受影响。确认不再需要时可显式删除。
