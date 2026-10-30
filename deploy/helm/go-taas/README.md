# go-taas Helm chart

Deploys the Go TaaS control plane into Kubernetes and makes every
third-party dependency connection a value: the chart either provisions a
single-replica PostgreSQL / Redis / RabbitMQ / Keycloak instance itself, or
connects the control plane to an operator-managed service.

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
| PostgreSQL / Redis / RabbitMQ / Keycloak `Deployment` + `Service` + `PersistentVolumeClaim` | per dependency `mode: internal` | single-replica instances for evaluation and small installs |

The chart creates no Ingress. Choose the exposure mechanism with
`taasServer.service.type` (`ClusterIP`, `NodePort`, `LoadBalancer`) or add
your own Ingress in front of the gateway port.

## Inference gateway plugin (authentication and metering)

`gatewayPlugin.enabled` adds a Wasm filter to the inference gateway that
verifies API keys and reports token usage, so inference traffic is
authenticated at the data plane instead of being forwarded unverified.
It is **off by default**: enabling it makes the gateway start rejecting
unverified requests, which is a behaviour change an existing install should
choose deliberately.

```yaml
gatewayPlugin:
  enabled: true
  # The chart generates a shared token into the release Secret when this is
  # empty; set it to reuse a token you manage.
  token: ""
  protectedPrefixes: ["/v1/"]
  cacheTTL: 30s          # how long a verdict is reused (per Envoy worker)
  verifyTimeout: 500ms   # exceeding it fails closed
  failOpen: false        # a broken plugin blocks traffic rather than admitting it
  internalHostname: taas-internal.invalid   # route the filter calls; not in DNS
```

What it deploys: a dedicated data-plane `Gateway`, an `EnvoyExtensionPolicy`
targeting the data-plane `HTTPRoute`, and an `HTTPRoute` for the plugin's own
internal calls. The filter artifact is built into the server image and served
from it, so one release produces one plugin version. Changing the artifact
requires a restart of the data-plane Envoy deployment — Envoy caches the Wasm
VM per worker, and a restart is also what re-fetches the code. On a fresh
install this is automatic.

## The data plane has its own gateway

`dataPlane.enabled` also creates a `Gateway` named `taas-inference`, so
inference and management traffic do not share a proxy:

| | Control plane | Data plane |
| --- | --- | --- |
| Gateway | the platform's | `taas-inference` (this chart) |
| Serves | `taas.<domain>` and the platform's other sites | only the inference hostname |
| Address | the platform gateway's | its own LoadBalancer address |

It uses the `aibrix-eg` GatewayClass so it inherits the inference-oriented
Envoy tuning, and it does **not** pin the inference hostname on its listener:
the hostname is a console setting the server applies to the route, so changing
it does not need a Helm upgrade.

Two things an operator must provide:

1. **A TLS Secret in the release namespace** covering the inference hostname:

   ```bash
   kubectl -n <namespace> create secret tls taas-inference-tls \
     --cert=fullchain.pem --key=privkey.pem
   ```

   The chart does not manage it because certificates outlive releases and are
   usually rotated by whoever owns DNS. Override the name with
   `dataPlane.gateway.tlsSecretName`.

2. **A DNS record for the inference hostname pointing at the data-plane
   Gateway's address**, which differs from the console's:

   ```bash
   kubectl -n <namespace> get gateway taas-inference \
     -o jsonpath='{.status.addresses[0].value}'
   ```

   Pointing it at the console's gateway returns 404: inference has no route
   there any more.

If `gatewayPlugin.pluginSha256` is set, the policy refuses an artifact whose
digest differs. Leave it empty when the URL points at this release's own
server: Helm applies the policy before the new pods serve the new artifact,
so a pinned digest fails the first reconcile until the rollout finishes.

An install with the plugin **disabled** is unchanged: the gateway forwards
requests, with or without a bearer token, exactly as before.

Project links: [website](https://go-taas.com) ·
[documentation](https://go-taas.com/docs.html) ·
[source](https://github.com/go-taas/go-taas) ·
[support](https://github.com/go-taas/go-taas/discussions).

## SSO and the bundled Keycloak

`dependencies.keycloak.mode` has three values, and only the last two install
anything:

| Mode | What happens |
| --- | --- |
| `disabled` (**default**) | no IdP is deployed or seeded; the platform's own account store is the only sign-in path, and the seeded `admin` account is how you log in |
| `internal` | the chart provisions a single-replica Keycloak; on first start `taas-server` seeds the `go-taas` realm's client (`go-taas-console`) and an `admin`/`admin` user (with the `admin` realm role), and the auth Migrate hook inserts the matching SSO provider row |
| `external` | no Keycloak is deployed; the issuer/client/redirect URI in `dependencies.keycloak.external` are seeded into the provider row so the console can sign in against your IdP |

The seeded **local** account (`admin` + `auth.seedSso.adminLocalPassword`) is
created in every mode, because the platform's own account store must work
without an external IdP. With `internal`, `admin`/`admin` is the *Keycloak*
account and the local password still applies to the local account.

To expose the console behind a gateway or Ingress, set the redirect URI and
web origin to the public URL:

```yaml
dependencies:
  keycloak:
    internal:
      redirectUri: "https://taas.example.com/api/v1/auth/sso/*"
      webOrigin: "https://taas.example.com"
```

The bundled Keycloak runs as uid 1000; the chart sets `fsGroup: 1000` so the
data PVC is writable, and exposes the health endpoints on the management port
(`dependencies.keycloak.internal.managementPort`, default 9000) which the
readiness/liveness probes use.

To use an existing IdP instead of the bundled one, set
`dependencies.keycloak.mode: external` and provide the issuer, client id,
client secret, and redirect URI. The chart then skips the Keycloak
Deployment/Service/PVC and points the SSO seed at the external IdP.

> **Production note**: the credentials created by the seed are for evaluation
> only — the local administrator's `auth.seedSso.adminLocalPassword` and, with
> `mode: internal`, Keycloak's `admin`/`admin`. Change both before exposing the
> console publicly: the local password from the account's profile page (or the
> values file before the first install), the Keycloak one in the Keycloak admin
> console. To run without the IdP entirely, leave `dependencies.keycloak.mode:
> disabled` (the default).

### Seeded local admin account

On first boot the platform seeds an `admin` account **in its own account
store** (no external IdP required): the initial password is Argon2id-hashed
into the `users` table. Set it with the first-class value
`auth.seedSso.adminLocalPassword` (default `Pass4admin`):

```bash
helm upgrade --install taas deploy/helm/go-taas --namespace taas \
  --set auth.seedSso.adminLocalPassword='YourStr0ng!Pass'
```

The password must satisfy the platform policy: at least 8 characters and at
least three of uppercase, lowercase, digit, and special character. The seed is
idempotent — it only fills a missing password, so an operator-changed password
is never overwritten. Platform administrators are exempt from the login email
verification code, so the bootstrap admin can always sign in.

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
| `app.kubernetes.io/component` | `server`, `controller`, `postgresql`, `redis`, `rabbitmq`, or `keycloak` |

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

* Kubernetes 1.25+ with a default StorageClass
* Helm 3.8+ (the templates use `dig`) or Helm 4.x
* permission to create cluster-scoped objects (the chart installs ClusterRoles
  and a ClusterRoleBinding)

### Third-party components

The chart deploys the platform, not its infrastructure. These are installed
separately, with their own charts and their own documentation:

| Component | Needed for | Chart values that depend on it | Official installation guide |
| --- | --- | --- | --- |
| Gateway API CRDs + Envoy Gateway | exposing the console through a Gateway; the data-plane gateway plugin | `dataPlane.enabled`, `gatewayPlugin.enabled` | [Gateway API](https://gateway-api.sigs.k8s.io/guides/) · [Envoy Gateway](https://gateway.envoyproxy.io/docs/install/install-helm/) |
| AIBrix | inference serving (`StormService`, `ModelClaim`, `PodAutoscaler`, the `aibrix-eg` class) | `dataPlane.backend`, `controller.config.runtime.aibrix.*` | [AIBrix](https://aibrix.readthedocs.io/latest/getting_started/installation/installation.html) |
| Volcano | gang scheduling and priority preemption; the PriorityClasses the values name | `controller.config.controller.scheduling` | [Volcano](https://volcano.sh/en/docs/installation/) |
| JuiceFS CSI (or any ReadWriteMany class) | one model-weights filesystem shared by the server and the inference pods | `controller.config.controller.weights.storageClass`, `taasServer.extraVolumes` | [JuiceFS CSI](https://juicefs.com/docs/csi/getting_started/) |
| Harbor (optional) | image import / upload into your own registry | `taasServer.harbor` | [Harbor](https://goharbor.io/docs/2.11.0/install-config/) |
| kube-prometheus-stack (optional) | the metrics and latency pages | `monitoring.prometheusURL` | [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack) |
| HAMi (optional) | vGPU slicing | `vgpu.hami.*` (config file) | [HAMi](https://project-hami.io/docs/get-started/deploy-with-helm) |
| cert-manager (optional) | issuing the console and inference certificates | — | [cert-manager](https://cert-manager.io/docs/installation/helm/) |

Defaults keep the requirements at zero: `dataPlane.enabled`, `gatewayPlugin.enabled`
and `taasServer.harbor.hostname` are off/empty, and an empty
`controller.config.controller.scheduling` leaves inference pods on the cluster's
default scheduler. The step-by-step path, including the cluster preparation the
chart cannot do for you, is the
[installation guide](../../../site/docs/getting-started/installation.md).

## Install

```bash
helm install taas deploy/helm/go-taas \
  --namespace taas --create-namespace \
  --set auth.seedSso.adminLocalPassword='<YourStr0ng!Pass>'
```

With no further overrides the release provisions PostgreSQL, Redis and RabbitMQ
in cluster, generates their credentials (and the two encryption master keys)
into the chart Secret, and wires the control plane to them. Keycloak is **not**
deployed by default (`dependencies.keycloak.mode: disabled`): the platform seeds
the administrator `admin` in its own account store with the password from
`auth.seedSso.adminLocalPassword`, so the console is usable without an IdP. Set
the password to satisfy the policy (>= 8 characters, at least three of
upper/lower/digit/symbol). The namespace managed by the controller is
`taas-infer`.

Resource names carry the release name and the chart name: release `taas` + chart
`go-taas` gives `taas-go-taas-server`, `taas-go-taas-controller`,
`taas-go-taas-postgresql`, and so on. Discover the server Service when in doubt:

```bash
kubectl --namespace taas get svc -l app.kubernetes.io/component=server
kubectl --namespace taas port-forward svc/taas-go-taas-server 9091:9091
# console: http://127.0.0.1:9091/ · admin console: http://127.0.0.1:9091/admin
# sign in as "admin" with auth.seedSso.adminLocalPassword
```

### Exposing the console

The chart creates no Ingress or HTTPRoute for the console. Attach it to the
Gateway that terminates TLS for your hostname (see
[Gateway API and Envoy Gateway](../../../site/docs/getting-started/installation.md#31-gateway-api-and-envoy-gateway)):

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
        request: 900s      # model and image uploads are long single requests
```

Alternatively set `taasServer.service.type: NodePort` or `LoadBalancer` and put
your own proxy in front of it; terminate TLS somewhere before the console.

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
| `controller.config.controller.scheduling.schedulerName` | `""` | `volcano` turns on gang scheduling with the PodGroups the controller writes; empty keeps the cluster default scheduler |
| `controller.config.controller.scheduling.priorityClassHigh` / `Normal` / `Low` | `""` | PriorityClasses a service priority maps onto; an unmapped priority leaves the pod with the cluster default class |
| `dependencies.keycloak.mode` | `disabled` | `disabled`, `internal` (chart-provisioned Keycloak) or `external` |
| `dependencies.keycloak.external.*` | — | `issuer`, `clientId`, `clientSecret`, `redirectUri` (seeded into the SSO provider row) |
| `dependencies.keycloak.internal.redirectUri` / `webOrigin` | localhost values | the console's public URL when `mode: internal` |
| `auth.seedSso.adminLocalPassword` | `Pass4admin` | initial password of the seeded platform administrator `admin` |
| `harbor.hostname` / `project` / `username` | `""` / `taas` / `""` | the internal registry: image import and upload are disabled while `hostname` is empty (`taasServer.harbor.*` is an accepted alias) |
| `harbor.password` / `existingSecret` / `existingSecretKey` | `""` / `""` / `harbor-password` | push credential, inline or from an existing Secret |
| `harbor.uploadMaxArchiveBytes` | `0` (8 GiB) | largest image archive the upload endpoint accepts |
| `dataPlane.enabled` / `hostname` | `false` / `""` | publish the inference gateway under a hostname through the chart-owned Gateway |
| `dataPlane.parentRef.name` / `namespace` / `sectionName` | `taas-inference` / release ns / `https` | the Gateway that terminates TLS for the inference host |
| `dataPlane.backend.name` / `namespace` / `port` | AIBrix Envoy Service / `yunzhi-system` / `80` | the Service the data-plane route forwards to |
| `gatewayPlugin.enabled` / `failOpen` / `cacheTTL` / `verifyTimeout` | `false` / `false` / `30s` / `500ms` | the Wasm filter that verifies API keys and reports metered usage |
| `monitoring.prometheusURL` | `""` | Prometheus base URL; empty falls back to the controller's own samples |
| `monitoring.inferencePodMonitor.*` | enabled | PodMonitor scraping the engine's own metrics (needs the Prometheus Operator CRDs) |
| `configOverrides` | `{}` | raw configuration file merged last |

`values.schema.json` validates the types, the `mode` and `service.type`
enumerations and the port ranges; cross-field rules (an external dependency
needs a host, an external database needs a password unless
`secrets.existingSecret` is set) are enforced by the templates with
actionable messages.

## Container registry (Harbor)

The image catalog's import and upload flows push engine images into the cluster's
own registry. Two obvious values are all an operator sets:

```yaml
# canonical location
harbor:
  hostname: hub.example.com        # empty disables import and upload
  project: taas                    # Harbor project imported images land in
  username: admin
  existingSecret: taas-harbor-push # a Secret holding the password
  existingSecretKey: harbor-password
  # password: ""                   # inline, discouraged; prefer existingSecret
  uploadMaxArchiveBytes: 0         # 0 = the 8 GiB default
```

`taasServer.harbor` is accepted as an alias for values files written against
earlier documentation; both paths render the same `CONFIG_IMAGE_HARBOR_*`
environment variables, and the top-level block wins when both are set. While
`hostname` is empty the console disables the import/upload actions and says why,
instead of failing halfway through a transfer.

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
