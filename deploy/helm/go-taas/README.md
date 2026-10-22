# go-taas Helm chart

Deploys the Go TaaS control plane into Kubernetes and makes every
third-party dependency connection a value: the chart either provisions a
single-replica PostgreSQL / Redis / RabbitMQ instance itself, or connects the
control plane to an operator-managed service.

## What the chart deploys

| Resource | Source | Notes |
| --- | --- | --- |
| `Deployment/<release>-server` | always | `taas-server`: gRPC (9090), REST gateway + embedded admin console (9091), metrics/health (9092) |
| `Service/<release>-server` | always | ClusterIP by default; the gRPC and monitor ports are published too |
| `ServiceAccount`, `ClusterRole`, `ClusterRoleBinding` | `controller.rbac.create` | the permissions the controller needs to reconcile inference workloads |
| `Deployment/<release>-controller` | always | consumes the desired-state stream and drives Kubernetes |
| `Namespace/<inference namespace>` | `controller.createNamespace` | the namespace the controller manages, taken from `controller.config.controller.namespace` |
| `Secret/<release>-secrets` | unless `secrets.existingSecret` | dependency passwords and the at-rest encryption keys |
| `ConfigMap/<release>-config-overrides` | `configOverrides` | raw configuration file merged after the image defaults |
| PostgreSQL / Redis / RabbitMQ `Deployment` + `Service` + `PersistentVolumeClaim` | per dependency `mode: internal` | single-replica instances for evaluation and small installs |

The chart creates no Ingress. Choose the exposure mechanism with
`taasServer.service.type` (`ClusterIP`, `NodePort`, `LoadBalancer`) or add
your own Ingress in front of the gateway port.

Project links: [website](https://go-taas.com) ·
[documentation](https://go-taas.com/docs.html) ·
[source](https://github.com/go-taas/go-taas) ·
[support](https://github.com/go-taas/go-taas/discussions).

## Resource labels

Every resource the chart creates carries the Kubernetes recommended labels:

| Label | Value |
| --- | --- |
| `helm.sh/chart` | `<chart>-<version>` |
| `app.kubernetes.io/name` | chart name (`go-taas`, overridable with `nameOverride`) |
| `app.kubernetes.io/instance` | release name |
| `app.kubernetes.io/version` | chart `appVersion` |
| `app.kubernetes.io/managed-by` | `Helm` for chart resources, `taas-controller` for resources the controller creates |
| `app.kubernetes.io/part-of` | `go-taas` |
| `app.kubernetes.io/component` | `server`, `controller`, `postgresql`, `redis`, or `rabbitmq` |

Platform-specific keys use the project's own DNS prefix, `go-taas.com/`, taken
from the official website. Never use a domain the project does not own (the
retired `taas.go-taas.github.io/` and `go-taas.io/` prefixes), because a label
prefix is expected to be resolvable by whoever consumes it.

| Key | Set on | Meaning |
| --- | --- | --- |
| `go-taas.com/service-id` | inference Deployment, Service, pods, ModelClaim | owning inference service |
| `go-taas.com/replica-index` | inference pods | masked replica index |
| `go-taas.com/accelerator`, `go-taas.com/accelerator-type` | inference pods (node selector), nodes | requested accelerator vendor and card type |
| `go-taas.com/driver-version`, `go-taas.com/device-plugin` | nodes | accelerator driver and device-plugin health signals |
| `go-taas.com/model`, `go-taas.com/model-id`, `go-taas.com/origin` | ModelClaim | model name, catalog id, and whether the claim came from a service or a registration |
| `go-taas.com/task-type`, `go-taas.com/task-id` | image warmup Job | warmup task identity |
| `go-taas.com/role` | compose cluster resources | static role of a volume, secret, or namespace |
| `go-taas.com/vgpu-pool` | nodes | vGPU pool membership (`vgpu.hami.poolLabelPrefix`, configurable) |

Keys used in selectors are an API surface: changing one rolls the affected
workloads, so it is treated as a migration, not a rename. `taasServer.podLabels`,
`controller.podLabels`, and the dependency `podLabels` values add labels without
overriding the ones above.

## Requirements

* Kubernetes 1.25+
* Helm 3.8+ (the templates use `dig`)

## Install

```bash
helm install taas deploy/helm/go-taas \
  --namespace taas --create-namespace
```

With no overrides the release provisions PostgreSQL, Redis and RabbitMQ in
cluster, generates their credentials (and the two encryption master keys)
into the chart Secret, and wires the control plane to them. The namespace
managed by the controller is `taas-infer`.

```bash
kubectl --namespace taas port-forward svc/taas-server 9091:9091
# console: http://127.0.0.1:9091/
```

The server fails fast when a dependency is unreachable and Kubernetes restarts
it, so on a fresh install it may restart once or twice while the in-cluster
PostgreSQL, Redis and RabbitMQ finish starting; the rollout converges without
intervention. The controller has no probes: it speaks no HTTP and its image
carries no shell.

## Connecting to external dependencies

Set a dependency's `mode` to `external` and describe the connection in its
`external` block. Modes are per dependency, so a managed database can sit
next to the in-cluster cache:

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
      password: ""              # prefer secrets.existingSecret
      sslMode: require

  redis:
    mode: external
    database: 1                 # logical database index
    external:
      host: taas-valkey.example.cache.amazonaws.com
      port: 6379
      password: ""

  rabbitmq:
    mode: external
    namespace: taas-prod        # isolates subjects/topics per environment
    external:
      url: amqp://guest:guest@rabbitmq.example.internal:5672/
```

The chart then renders nothing for those dependencies and points the control
plane at the configured endpoints through `CONFIG_*` environment variables
(`CONFIG_DB_MASTER_HOST`, `CONFIG_REDIS_HOST`, `CONFIG_MQ_URL`, ...).

### Credentials from an existing Secret

Set `secrets.existingSecret` to keep credentials out of the values file
(External Secrets, Sealed Secrets, Vault, ...). The chart then creates no
Secret and reads these keys from it:

| Key | Used for |
| --- | --- |
| `postgresql-password` | `CONFIG_DB_MASTER_PASSWORD` |
| `redis-password` | `CONFIG_REDIS_PASSWORD` (`""` when the instance has no auth) |
| `webhook-secret-encryption-key` | `CONFIG_WEBHOOK_SECRETENCRYPTIONKEY` (optional; omit only if you configure the key another way) |
| `provider-secret-encryption-key` | `CONFIG_PROVIDER_SECRETENCRYPTIONKEY` (optional, same rule) |

```yaml
secrets:
  existingSecret: taas-credentials
```

When the chart manages the Secret itself, the dependency passwords are
generated once and looked up on later upgrades, so a `helm upgrade` or
`helm rollback` never rotates a password the running services still use.
The Secret carries `helm.sh/resource-policy: keep`, so `helm uninstall`
leaves it for a later re-install.

## Configuration reference

Three mechanisms, highest precedence first:

1. **`taasServer.config` / `taasServer.secretConfig`** (and the same under
   `controller`) — overrides for the `configs/*.yaml` files baked into the
   image, rendered as `CONFIG_*` environment variables. Keys mirror the
   config-file paths and the leaves must be scalars:

   | Chart path | Environment variable |
   | --- | --- |
   | `taasServer.config.infer.endpointBaseURL` | `CONFIG_INFER_ENDPOINTBASEURL` |
   | `taasServer.config.model.weightsDir` | `CONFIG_MODEL_WEIGHTSDIR` |
   | `taasServer.secretConfig.image.harbor.password` | `CONFIG_IMAGE_HARBOR_PASSWORD` (from the Secret) |
   | `controller.config.controller.namespace` | `CONFIG_CONTROLLER_NAMESPACE` |
   | `controller.config.controller.weights.storageClass` | `CONFIG_CONTROLLER_WEIGHTS_STORAGECLASS` |
   | `controller.config.runtime.aibrix.namespace` | `CONFIG_RUNTIME_AIBRIX_NAMESPACE` |

   A list or map under these keys fails the render with a pointer to
   `configOverrides`.

2. **`configOverrides`** — a raw configuration file mounted into both
   workloads and merged after the shipped `configs/*.yaml`
   (`/etc/taas/configs/zz-go-taas-overrides.yaml`). Use it for lists and
   maps, e.g. the image catalog seed, `auth.adminRoles` or
   `optimization.peakHours`.

3. **The `configs/*.yaml` defaults** baked into the image.

### Values

| Key | Default | Description |
| --- | --- | --- |
| `image.registry` / `image.repository` | `ghcr.io` / `go-taas/go-taas` | registry and repository path; the component name is appended |
| `image.tag` | `""` | empty uses `v<Chart.appVersion>` |
| `dependencies.postgresql.mode` | `internal` | `internal` or `external` |
| `dependencies.postgresql.maxOpenConns` / `maxIdleConns` / `connMaxLifetime` / `debug` | `32` / `8` / `30m` / `false` | connection pool tuning, both modes |
| `dependencies.postgresql.external.*` | — | `host`, `port`, `database`, `username`, `password`, `sslMode` |
| `dependencies.postgresql.internal.*` | — | `image`, `auth`, `persistence`, scheduling, resources |
| `dependencies.redis.mode` | `internal` | `internal` or `external` |
| `dependencies.redis.database` | `0` | logical database index |
| `dependencies.redis.maxIdle` / `maxActive` / `idleTimeout` | `8` / `64` / `5m` | connection pool tuning, both modes |
| `dependencies.redis.external.*` | — | `host`, `port`, `password` |
| `dependencies.redis.internal.auth.enabled` | `true` | require a password on the provisioned instance |
| `dependencies.rabbitmq.mode` | `internal` | `internal` or `external` |
| `dependencies.rabbitmq.driver` / `namespace` | `rabbitmq` / `taas` | message-queue driver and subject namespace |
| `dependencies.rabbitmq.external.url` | — | full connection URL (may embed credentials) |
| `dependencies.rabbitmq.internal.port` | `5672` | AMQP client port |
| `dependencies.*.internal.persistence.enabled` / `size` / `storageClass` | `true` / `10Gi`-`8Gi` / `""` | storage of the provisioned instance |
| `secrets.existingSecret` | `""` | pre-existing Secret with the credentials above |
| `secrets.webhookSecretEncryptionKey` / `providerSecretEncryptionKey` | `""` | empty generates a random key |
| `taasServer.enabled` / `replicaCount` | `true` / `1` | deploy the control-plane server |
| `taasServer.service.type` | `ClusterIP` | `ClusterIP`, `NodePort` or `LoadBalancer` |
| `taasServer.service.gateway.port` | `9091` | REST gateway and console |
| `taasServer.service.grpc.enabled` / `port` | `true` / `9090` | gRPC API |
| `taasServer.service.monitor.enabled` / `port` | `true` / `9092` | Prometheus metrics and `/healthz` |
| `taasServer.args` | `[]` | extra container arguments |
| `taasServer.config` / `secretConfig` | `{}` | `configs/*.yaml` overrides |
| `taasServer.extraEnv` / `extraVolumes` / `extraVolumeMounts` | `[]` | escape hatches for a shared weights filesystem, a `secretKeyRef`, ... |
| `taasServer.livenessProbe` / `readinessProbe` | enabled | HTTP probes on the monitor port |
| `controller.enabled` / `replicaCount` | `true` / `1` | deploy the reconciler |
| `controller.rbac.create` | `true` | create the ClusterRole and its binding |
| `controller.rbac.existingClusterRole` | `""` | bind an existing ClusterRole instead |
| `controller.createNamespace` | `true` | create the managed inference namespace |
| `controller.config.controller.namespace` | `taas-infer` | namespace the controller manages |
| `controller.config.controller.weights.*` | `model-weights` / `/data/weights` / `""` | shared weights PVC name, mount path, StorageClass |
| `configOverrides` | `{}` | raw configuration file merged last |

`values.schema.json` validates the types, the `mode` and `service.type`
enumerations and the port ranges; cross-field rules (an external dependency
needs a host, an external database needs a password unless
`secrets.existingSecret` is set) are enforced by the templates with
actionable messages.

## Shared model weights

Inference pods and the control plane must read the same model weights. The
controller creates the PVC (`controller.config.controller.weights.pvcName`)
inside the namespace it manages, provisioning it from
`controller.config.controller.weights.storageClass` — a `ReadWriteMany`
capable, JuiceFS-backed StorageClass in the reference deployment. When the
value is empty the cluster default StorageClass is used, which is rarely
`ReadWriteMany`: set it explicitly for anything beyond a smoke test, and
mount the same filesystem into the server with
`taasServer.extraVolumes` / `taasServer.extraVolumeMounts` paired with
`taasServer.config.model.weightsDir`.

## Production checklist

* `dependencies.*.mode: external` (or a chart-external operator) for
  PostgreSQL, Redis and RabbitMQ; the provisioned instances are single-replica
  and restart with `Recreate`.
* `secrets.existingSecret` set, with strong values for
  `webhook-secret-encryption-key` and `provider-secret-encryption-key` —
  leaving them empty makes the platform fall back to its documented
  development key.
* `dependencies.postgresql.external.sslMode` set to `require` or
  `verify-full` for a remote database.
* `dependencies.redis.internal.auth.enabled: true` and
  `dependencies.rabbitmq.mode: external` (or an internal instance with
  authentication) when Redis/RabbitMQ are reachable beyond the cluster.
* `controller.config.controller.weights.storageClass` set to a
  `ReadWriteMany` StorageClass.
* `resources` requests/limits set for both components and for every
  provisioned dependency.
* `taasServer.service.type: LoadBalancer` (or an Ingress) plus TLS in front
  of the gateway when the console is reachable from outside the cluster.

## Uninstall

```bash
helm uninstall taas --namespace taas
```

The release Secret (generated credentials) and the inference namespace
(`helm.sh/resource-policy: keep`) survive, so running inference workloads
and a later re-install keep working. Delete them explicitly when they are no
longer needed.
