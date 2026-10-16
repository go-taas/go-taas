# go-taas Helm Chart

将 Go TaaS 控制面部署到 Kubernetes，并把所有第三方依赖的连接方式变成 values 配置项：Chart 既可以自行置备单副本的 PostgreSQL / Redis / NATS，也可以让控制面连接运维托管的外部服务。

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
| PostgreSQL / Redis / NATS 的 `Deployment` + `Service` + `PersistentVolumeClaim` | 各依赖 `mode: internal` 时 | 面向评估与小规模安装的单副本实例 |

Chart 不创建 Ingress。对外暴露方式由 `taasServer.service.type`（`ClusterIP`、`NodePort`、`LoadBalancer`）决定，或在网关端口前自行添加 Ingress。

## 环境要求

* Kubernetes 1.25+
* Helm 3.8+（模板使用了 `dig`）

## 安装

```bash
helm install taas deploy/helm/go-taas \
  --namespace taas --create-namespace
```

不做任何覆盖时，Release 会在集群内置备 PostgreSQL、Redis 与 NATS，把它们的凭据（以及两个加密主密钥）生成到 Chart Secret 中，并把控制面自动连上去。控制器管理的命名空间为 `taas-infer`。

```bash
kubectl --namespace taas port-forward svc/taas-server 9091:9091
# 控制台：http://127.0.0.1:9091/
```

服务端在依赖不可达时会快速失败退出，由 Kubernetes 重启；因此全新安装时，内置的 PostgreSQL、Redis、NATS 尚在启动期间服务端可能重启一两次，随后自动收敛。控制器不配置探针：它不提供 HTTP 服务，且镜像内没有 shell。

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

  nats:
    mode: external
    namespace: taas-prod        # 按环境隔离主题/主题空间
    external:
      url: nats://nats.example.internal:4222
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
| `dependencies.nats.mode` | `internal` | `internal` 或 `external` |
| `dependencies.nats.driver` / `namespace` | `nats` / `taas` | 消息队列驱动与主题命名空间 |
| `dependencies.nats.external.url` | — | 完整连接 URL（可内含凭据） |
| `dependencies.nats.internal.jetstream` | `true` | 计量与计费消息流依赖 JetStream |
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

* PostgreSQL、Redis、NATS 使用 `dependencies.*.mode: external`（或由 Chart 之外的 Operator 管理）；内置实例是单副本且以 `Recreate` 策略重启。
* 设置 `secrets.existingSecret`，并为其提供强随机的 `webhook-secret-encryption-key` 与 `provider-secret-encryption-key`——留空会让平台退回到其文档中说明的开发密钥。
* 远程数据库设置 `dependencies.postgresql.external.sslMode` 为 `require` 或 `verify-full`。
* 当 Redis/NATS 可被集群外部访问时，保持 `dependencies.redis.internal.auth.enabled: true`，或使用 `dependencies.nats.mode: external`（亦或带认证的内置实例）。
* 将 `controller.config.controller.weights.storageClass` 设置为支持 `ReadWriteMany` 的 StorageClass。
* 为两个组件以及每个内置依赖设置 `resources` 的 requests/limits。
* 控制台需要从集群外访问时，使用 `taasServer.service.type: LoadBalancer`（或 Ingress）并在网关前启用 TLS。

## 卸载

```bash
helm uninstall taas --namespace taas
```

Release 的 Secret（生成的凭据）与推理命名空间（带 `helm.sh/resource-policy: keep`）会保留，因此运行中的推理负载与后续重新安装都不受影响。确认不再需要时可显式删除。
