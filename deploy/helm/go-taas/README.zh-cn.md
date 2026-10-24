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

项目链接：[官方网站](https://go-taas.com) ·
[文档](https://go-taas.com/docs.html) ·
[源码](https://github.com/go-taas/go-taas) ·
[社区支持](https://github.com/go-taas/go-taas/discussions)。

## SSO 与内置 Keycloak

Chart 默认内置一个 Keycloak IdP（`dependencies.keycloak.mode: internal`），让控制台开箱即用。首次启动时 `taas-server` 会 seed 出 `go-taas` realm 的客户端（`go-taas-console`）与 `admin`/`admin` 用户（带 `admin` realm 角色），auth 的 Migrate 钩子会插入对应的 SSO 提供商记录。用 `admin`/`admin` 登录控制台即可。

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

> **生产注意**：seed 的 `admin`/`admin` 与 `alice`/`alice-password` 仅用于评估。公开暴露控制台前请修改它们（或把 `auth.seedSso.enabled` 设为 `false`，通过管理 API 注册自己的 IdP）。

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

* Kubernetes 1.25+
* Helm 3.8+（模板使用了 `dig`）

## 安装

```bash
helm install taas deploy/helm/go-taas \
  --namespace taas --create-namespace
```

不做任何覆盖时，Release 会在集群内置备 PostgreSQL、Redis 与 RabbitMQ，把它们的凭据（以及两个加密主密钥）生成到 Chart Secret 中，并把控制面自动连上去。控制器管理的命名空间为 `taas-infer`。

```bash
kubectl --namespace taas port-forward svc/taas-server 9091:9091
# 控制台：http://127.0.0.1:9091/
```

服务端在依赖不可达时会快速失败退出，由 Kubernetes 重启；因此全新安装时，内置的 PostgreSQL、Redis、RabbitMQ 尚在启动期间服务端可能重启一两次，随后自动收敛。控制器不配置探针：它不提供 HTTP 服务，且镜像内没有 shell。

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
| `configOverrides` | `{}` | 最后合并的原始配置文件 |

`values.schema.json` 校验类型、`mode` 与 `service.type` 枚举以及端口范围；无法用 Schema 表达的跨字段规则（external 依赖必须有 host、external 数据库必须提供密码或设置 `secrets.existingSecret`）由模板给出明确报错。

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
