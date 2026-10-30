# Installing Go TaaS on Kubernetes — Installation and Deployment Guide

| Attribute | Content |
| --- | --- |
| Feature point | End-to-end installation of the Go TaaS platform: cluster prerequisites, third-party components, the Helm release, console exposure with DNS and TLS, the initial administrator password, and the first-run checklist that ends with a working console login |
| Document scope | The supported installation path, the third-party dependencies and their official installation links, the cluster preparation steps the chart cannot do for you, the production values file, three ways to expose the console, the data plane and inference hostname, upgrade/uninstall, and the failure modes seen in practice with their fixes |
| Owning modules | `deploy/helm/go-taas` (chart) · `apps/taas-server`, `apps/controller` (images) · `internal/controller` (inference resources) · `site/docs` (this guide) |
| Related documents | Chart README (full values reference, in the repository at `deploy/helm/go-taas/README.md`) · [Architecture Design](../design/architecture.md) (control plane / data plane split) · [StormService Serving Topology](../architecture/stormservice-serving-topology.md) (what the controller creates for a deployment) |
| Status | Implemented — the reference deployment runs on a single-node RKE2 cluster (Kubernetes v1.36, Helm 4.3, AIBrix 0.7, Volcano 1.15, Envoy Gateway 1.8, JuiceFS CSI 0.32); every snippet here is validated against the chart (see section 13) |

---

## 0. What you end up with

| You get | Where |
| --- | --- |
| **Admin console** | `https://<your-console-host>/admin` (or `http://127.0.0.1:9091/admin` through a port-forward) |
| **End-user console** | `https://<your-console-host>/` |
| **Management API** | `https://<your-console-host>/api/v1/admin/...` |
| **End-user API** | `https://<your-console-host>/api/v1/...` |
| **Inference API** (OpenAI-compatible) | `https://<your-inference-host>/v1/chat/completions` |
| **First sign-in** | user `admin`, password from `auth.seedSso.adminLocalPassword` (default `Pass4admin`) |

The platform is two deployments: `taas-server` (gRPC 9090, REST gateway + both consoles on 9091, metrics and `/healthz` on 9092) and `taas-controller` (reconciles inference workloads into the cluster). Everything else — PostgreSQL, Redis, RabbitMQ — is either provisioned by the chart or an endpoint you supply.

> **Release names and resource names.** With the release name `taas` and the chart name `go-taas`, every resource is prefixed `taas-go-taas-`: the server Service is `taas-go-taas-server`, the database `taas-go-taas-postgresql`. The examples below use that pairing. If you install as `helm install go-taas ...`, the names are `go-taas-*` instead — when in doubt, discover the Service:
>
> ```bash
> kubectl -n taas get svc -l app.kubernetes.io/component=server
> ```

## 1. Before you start

| Step | Required? | Time | You can skip it when |
| --- | --- | --- | --- |
| [1. Kubernetes](#2-step-1--kubernetes-optional) | only if you have no cluster | 10–20 min | you already have a cluster and a `kubectl` context pointing at it |
| [2. Third-party components](#3-step-2--third-party-components) | Gateway API + Envoy Gateway and AIBrix for inference; JuiceFS for model weights | 30–60 min | you only want the console up (steps 3–5 are enough) |
| [3. Cluster preparation](#4-step-3--cluster-preparation) | yes | 10 min | never — these are the inputs the chart cannot guess |
| [4. Install the platform](#5-step-4--install-the-platform) | yes | 5 min | never |
| [5. Expose the console](#6-step-5--expose-the-console) | yes, one of the three options | 5 min | never — you need an address to log in to |
| [6. Sign in](#7-step-6--sign-in) | yes | 1 min | never |

You need, on the machine you run `helm` from:

- `kubectl` with a context that can create cluster-scoped objects (the chart installs ClusterRoles and a ClusterRoleBinding);
- Helm 3.8+ or Helm 4.x;
- permission to create a namespace (or an existing `taas` namespace);
- a **default StorageClass**: the in-cluster PostgreSQL, Redis and RabbitMQ each request a
  PersistentVolumeClaim. Without one their pods stay `Pending` — either install a provisioner
  (longhorn, local-path, …) or point
  `dependencies.*.internal.persistence.storageClass` at a class you have.

## 2. Step 1 — Kubernetes (optional)

If you already run a cluster, skip this step. If not, the reference deployment uses **[RKE2](https://docs.rke2.io/install/quickstart)**, a CNCF-conformant, single-binary Kubernetes distribution that ships the CRI, CNI and CSI plumbing a GPU node needs:

```bash
# on every node (server or agent), as root
curl -sfL https://get.rke2.io | sudo sh -
sudo systemctl enable --now rke2-server        # control-plane node
# on additional worker nodes:
#   curl -sfL https://get.rke2.io | INSTALL_RKE2_TYPE="agent" sudo sh -
#   sudo systemctl enable --now rke2-agent
```

Point `kubectl` at it (the kubeconfig is written by the installer):

```bash
export KUBECONFIG=/etc/rancher/rke2/rke2.yaml
kubectl get nodes
```

Other distributions work as well (k3s, kind, EKS/GKE/AKS); the platform only needs Kubernetes 1.25+ with a default StorageClass.

**GPU nodes.** Install the NVIDIA GPU Operator (or the vendor's device plugin) so that `nvidia.com/gpu` is an allocatable resource and GPU Feature Discovery labels the node:

```bash
kubectl get node <gpu-node> -o jsonpath='{.status.allocatable.nvidia\.com/gpu}{"\n"}'
kubectl get node <gpu-node> --show-labels | tr ',' '\n' | grep -E 'gpu-vendor|gpu.product'
```

A CPU-only evaluation install needs neither.

## 3. Step 2 — Third-party components

Go TaaS does not vendor these. Install them with their own charts and follow their own documentation — the links below are the upstream install guides, not copies.

| Component | Why the platform needs it | Required | Official installation guide |
| --- | --- | --- | --- |
| **Gateway API CRDs** + **Envoy Gateway** | Provides the `Gateway`/`HTTPRoute` API and the GatewayClass the console route and the data-plane route attach to | required to expose the console through a Gateway (option B in step 5) | [Gateway API](https://gateway-api.sigs.k8s.io/guides/) · [Envoy Gateway (Helm)](https://gateway.envoyproxy.io/docs/install/install-helm/) |
| **AIBrix** | Inference orchestration: the `StormService` (roles/pods), `ModelClaim` and `PodAutoscaler` CRDs, plus the `aibrix-eg` GatewayClass that fronts the models | required for inference (not for the console) | [AIBrix installation](https://aibrix.readthedocs.io/latest/getting_started/installation/installation.html) |
| **Volcano** | Gang scheduling (one replica's pods are placed together) and priority preemption; supplies the scheduler the controller targets | recommended; needed only if you set `controller.scheduling.schedulerName: volcano` | [Volcano installation](https://volcano.sh/en/docs/installation/) |
| **JuiceFS CSI** (or any ReadWriteMany storage) | One model-weights filesystem shared by the control plane (downloads/uploads) and the inference pods (read-only mounts) | required for model download/upload; any RWX class works | [JuiceFS CSI driver](https://juicefs.com/docs/csi/getting_started/) |
| **Harbor** (or another registry) | The platform's internal image registry: the image import/upload flow pushes engine images here and registers the local reference | optional; image import and upload are disabled while unset | [Harbor installation](https://goharbor.io/docs/2.11.0/install-config/) |
| **kube-prometheus-stack** | Metrics pages read Prometheus; without it those pages fall back to the controller's own samples | optional | [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack) |
| **HAMi** | vGPU slicing (shared GPUs) | optional | [HAMi (Helm)](https://project-hami.io/docs/get-started/deploy-with-helm) |
| **cert-manager** | Issues the TLS certificates for the console and inference hostnames | optional if you bring your own certificate | [cert-manager (Helm)](https://cert-manager.io/docs/installation/helm/) |

### 3.1 Gateway API and Envoy Gateway

```bash
# Gateway API CRDs (standard channel)
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml

# Envoy Gateway
helm install envoy-gateway oci://docker.io/envoyproxy/gateway-helm \
  --version v1.2.1 --namespace envoy-gateway-system --create-namespace
kubectl wait --timeout=5m -n envoy-gateway-system \
  deployment/envoy-gateway --for=condition=Available
```

Then create the platform Gateway that terminates TLS for your console hostname (this is the Gateway the console `HTTPRoute` in step 5 attaches to). One Gateway can serve several hostnames:

```yaml
# platform-gateway.yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: envoy-gateway
  namespace: envoy-gateway-system
spec:
  gatewayClassName: envoy-gateway-class
  listeners:
    - name: http
      protocol: HTTP
      port: 80
    - name: https
      protocol: HTTPS
      port: 443
      tls:
        mode: Terminate
        certificateRefs:
          - kind: Secret
            name: platform-tls        # created in step 4.3
```

```bash
kubectl apply -f platform-gateway.yaml
kubectl -n envoy-gateway-system get gateway envoy-gateway   # ADDRESS is the entry point
```

### 3.2 AIBrix

```bash
export AIBRIX_VERSION=v0.7.0     # the reference deployment runs 0.7.0; newer releases work
kubectl create namespace aibrix-system
helm install aibrix oci://ghcr.io/vllm-project/aibrix/charts/aibrix \
  --version ${AIBRIX_VERSION#v} --namespace aibrix-system
# CRDs ship as their own release so uninstalling the operator keeps user resources
kubectl apply -f https://github.com/vllm-project/aibrix/releases/download/${AIBRIX_VERSION}/aibrix-core.crds.yaml

kubectl -n aibrix-system get pods
kubectl get gatewayclass                      # expect aibrix-eg, Accepted=True
```

AIBrix installs its own Envoy data plane; the Service it creates is what the platform routes inference traffic to (step 9 discovers its exact name).

### 3.3 Volcano

```bash
helm repo add volcano-sh https://volcano-sh.github.io/helm-charts
helm repo update
helm install volcano volcano-sh/volcano --namespace volcano-system --create-namespace
kubectl -n volcano-system get pods
```

The controller targets the scheduler name `volcano` and three PriorityClasses. **The chart does not create PriorityClasses** — create them once per cluster (or point the values at classes you already have):

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: high-priority
value: 1000000
globalDefault: false
preemptionPolicy: PreemptLowerPriority
description: "Production inference; may preempt lower priority workloads"
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: normal-priority
value: 500000
globalDefault: true
preemptionPolicy: PreemptLowerPriority
description: "Default inference priority"
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: low-priority
value: 100000
globalDefault: false
preemptionPolicy: PreemptLowerPriority
description: "Batch and experimental inference"
EOF
kubectl get priorityclass | grep -E "high-priority|normal-priority|low-priority"
```

If you skip Volcano, leave `controller.config.controller.scheduling` empty: inference pods then use the cluster default scheduler, one pod at a time, and the priority is only carried as a label on the pod.

### 3.4 Shared model weights (JuiceFS CSI)

The control plane writes model weights (hub download or archive upload); inference pods read them. Both must see the **same** filesystem, so the StorageClass has to be ReadWriteMany and backed by a shared filesystem.

```bash
helm repo add juicefs https://juicedata.github.io/charts/
helm repo update
helm install juicefs-csi-driver juicefs/juicefs-csi-driver \
  --namespace kube-system \
  --set mountRoot=/var/lib/juicefs/volume \
  --set kubeletDir=/var/lib/kubelet
```

Create a volume (format + a StorageClass) following the driver's guide linked above. The reference deployment uses a `juicefs-platform-shared` class shared by every tenant; any RWX class works, `juicefs-*` is what was tested.

> **Important:** the server mounts a PVC **in its own namespace** and the controller creates one **in the inference namespace**. They only share data because the StorageClass points at the same JuiceFS volume — two `local-path` or `longhorn` PVCs would be two different disks, and inference would fail with "weights not found".

### 3.5 Harbor (optional)

Create a project named `taas` (or use your own name and set `harbor.project`), an account that can push to it, and the two Secrets the platform reads:

```bash
# pull secret for the TaaS pods, when the images live in your own registry
kubectl -n taas create secret docker-registry harbor-pull \
  --docker-server=hub.example.com \
  --docker-username='robot$taas+puller' \
  --docker-password='<token>'

# push credential used by image import / upload
kubectl -n taas create secret generic taas-harbor-push \
  --from-literal=harbor-password='<push-password>'
```

## 4. Step 3 — Cluster preparation

These are the inputs the chart cannot infer.

### 4.1 Accelerator node labels

Inference pods pin the accelerator vendor (and the card type when one is requested) through node labels. The controller accepts `go-taas.com/accelerator` and falls back to the `gpu-vendor` label that GPU Feature Discovery sets:

```bash
# preferred: label explicitly (value = vendor token used by the platform)
kubectl label node <gpu-node> go-taas.com/accelerator=nvidia --overwrite
# the card type comes from the device plugin's own label, e.g.
kubectl get node <gpu-node> -o jsonpath='{.metadata.labels.nvidia\.com/gpu\.product}{"\n"}'
```

Accepted vendor tokens are `nvidia`, `iluvatar`, `metax`, and `cpu` for accelerator-free images. The card types the console offers are read from the live inventory, so nothing else is needed once the labels exist.

### 4.2 Namespace

```bash
kubectl create namespace taas
```

`helm install --create-namespace` does this for you; creating it first lets you put the pull secret and the TLS secret in it beforehand.

### 4.3 TLS certificates

Two hostnames need certificates: the console host and the inference host. Either issue them with cert-manager and a `Certificate` resource, or import your own:

```bash
# console host — in the Gateway's namespace when the Gateway terminates TLS
kubectl -n envoy-gateway-system create secret tls platform-tls \
  --cert=fullchain.pem --key=privkey.pem

# inference host — always in the TaaS namespace (the chart-owned Gateway reads it there)
kubectl -n taas create secret tls taas-inference-tls \
  --cert=fullchain.pem --key=privkey.pem
```

A wildcard certificate (`*.example.com`) covers both hostnames and is the usual choice. The chart does not create these Secrets on purpose: certificates outlive Helm releases and are rotated by whoever owns DNS.

### 4.4 Shared weights PVC for the server

The server needs the same weights filesystem mounted at `/data/weights`:

```yaml
# weights-pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: taas-model-weights
  namespace: taas
spec:
  accessModes: [ReadWriteMany]
  storageClassName: juicefs-platform-shared   # your RWX class
  resources:
    requests:
      storage: 100Gi                        # the shared filesystem is not capped by this
```

```bash
kubectl apply -f weights-pvc.yaml
```

## 5. Step 4 — Install the platform

### 5.1 The values file

Save this as `taas-values.yaml` and replace every `<...>` placeholder. It is the reference deployment's shape: in-cluster PostgreSQL/Redis/RabbitMQ, the local admin account, a Volcano-scheduled fleet, a Harbor-backed image catalog, and the two gateway integrations.

```yaml
# ---- images -----------------------------------------------------------------
image:
  registry: ghcr.io
  repository: go-taas/go-taas
  tag: ""                       # empty = v<chart appVersion>; pin a version in production
imagePullSecrets: []            # e.g. [{name: harbor-pull}] when the images live in your registry

# ---- first sign-in ----------------------------------------------------------
auth:
  seedSso:
    # Initial password of the seeded platform administrator `admin`.
    # Policy: >= 8 characters, at least three of upper/lower/digit/symbol.
    adminLocalPassword: "<YourStr0ng!Pass>"

# ---- dependencies (in-cluster is fine for evaluation) -----------------------
dependencies:
  postgresql:
    mode: internal              # external + the external block for production
    internal:
      persistence:
        enabled: true
        size: 20Gi
  redis:
    mode: internal
  rabbitmq:
    mode: internal
  keycloak:
    mode: disabled              # local accounts only; see step 7 for SSO

# ---- control plane ----------------------------------------------------------
taasServer:
  config:
    model:
      weightsDir: /data/weights
  extraVolumes:
    - name: model-weights
      persistentVolumeClaim:
        claimName: taas-model-weights
  extraVolumeMounts:
    - name: model-weights
      mountPath: /data/weights

# ---- registry (optional: enables image import / upload) ---------------------
harbor:                         # also accepted as taasServer.harbor
  hostname: hub.example.com
  project: taas
  username: admin
  existingSecret: taas-harbor-push
  existingSecretKey: harbor-password

# ---- controller -------------------------------------------------------------
controller:
  config:
    controller:
      namespace: taas-infer
      weights:
        pvcName: model-weights
        mountPath: /data/weights
        storageClass: juicefs-platform-shared   # the RWX class from step 3.4
      # Gang scheduling and priority preemption (step 3.3). Drop this block
      # when Volcano is not installed.
      scheduling:
        schedulerName: volcano
        priorityClassHigh: high-priority
        priorityClassNormal: normal-priority
        priorityClassLow: low-priority

# ---- monitoring (optional) --------------------------------------------------
monitoring:
  prometheusURL: ""             # e.g. http://kube-prometheus-stack-prometheus.monitoring.svc:9090
```

### 5.2 Install

```bash
helm install taas deploy/helm/go-taas \
  --namespace taas --create-namespace \
  --values taas-values.yaml
```

From a clone of the repository, point at the chart directory instead:

```bash
git clone https://github.com/go-taas/go-taas.git
cd go-taas
helm install taas deploy/helm/go-taas -n taas --create-namespace -f taas-values.yaml
```

### 5.3 Wait for the rollout

```bash
kubectl -n taas rollout status deploy/taas-go-taas-server --timeout=10m
kubectl -n taas rollout status deploy/taas-go-taas-controller --timeout=5m
kubectl -n taas get pods
```

A fresh install commonly shows **one or two restarts of the server**: it fails fast when PostgreSQL is not accepting connections yet, and Kubernetes restarts it. `RESTARTS=1`/`2` followed by `Running` is expected; a crash loop is not (see the troubleshooting table in section 11).

### 5.4 Verify the API is up

```bash
kubectl -n taas port-forward svc/taas-go-taas-server 9091:9091 &
curl -s http://127.0.0.1:9091/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<YourStr0ng!Pass>"}'
# {"response":{"code":0,"message":"OK"},"sessionToken":"...","realm":"admin"}
```

Getting a `sessionToken` back means the database, the seeded administrator and the auth path all work — the console will log in.

> `/healthz` and `/metrics` live on the **monitor port 9092**, not on the console port: on 9091 the console route answers every path (including `/healthz`) with the SPA. Probe health with `kubectl -n taas port-forward svc/taas-go-taas-server 9092:9092` and `curl -s http://127.0.0.1:9092/healthz`, or rely on the chart's readiness probe, which does exactly that.

## 6. Step 5 — Expose the console

Pick one. Option B is what the reference deployment uses.

### Option A — port-forward (fastest, evaluation only)

```bash
kubectl -n taas port-forward svc/taas-go-taas-server 9091:9091
# console: http://127.0.0.1:9091/   admin console: http://127.0.0.1:9091/admin
```

Nothing else to configure, but the URL only exists on your machine.

### Option B — Gateway API HTTPRoute with real DNS and TLS

```yaml
# console-route.yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: taas-console
  namespace: taas
spec:
  parentRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: envoy-gateway            # the platform Gateway from step 3.1
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
        request: 900s                # model uploads and image uploads are long transfers
```

```bash
kubectl apply -f console-route.yaml
kubectl -n envoy-gateway-system get gateway envoy-gateway \
  -o jsonpath='{.status.addresses[0].value}{"\n"}'   # point DNS here
```

`timeouts.request: 900s` is not optional in practice: a model archive upload or an image push stays on one request for minutes, and the default timeout severs it.

Then create the DNS record `taas.example.com → <gateway address>` and confirm:

```bash
curl -sI https://taas.example.com/ | head -1        # HTTP/2 200
```

**Do not** attach the console route to the inference Gateway (`taas-inference`) — that one is managed by this chart for inference traffic only, and it has no console backend.

### Option C — LoadBalancer Service

```bash
helm upgrade taas deploy/helm/go-taas -n taas --reuse-values \
  --set taasServer.service.type=LoadBalancer
kubectl -n taas get svc taas-go-taas-server -w    # EXTERNAL-IP appears
```

Then point DNS at the external address and terminate TLS in front of it (or use `LoadBalancer` annotations for your cloud's certificate handling). The console is plain HTTP on this path unless you add TLS.

## 7. Step 6 — Sign in

1. Open `https://<your-console-host>/admin` (or `http://127.0.0.1:9091/admin`).
2. The console redirects to the login page; sign in with user **`admin`** and the password you set in `auth.seedSso.adminLocalPassword`.
3. The admin console opens on the user directory. The end-user console is at `/`.

The seeded account is flagged as the platform administrator, so it needs no organization and is exempt from login email verification. If the login is rejected:

```bash
# was the seed applied to this release?
kubectl -n taas exec deploy/taas-go-taas-server -- env | grep SEEDSSO
kubectl -n taas logs deploy/taas-go-taas-server | grep -i "seed"
```

**Change the password** right after the first login (account menu → profile), or set a strong value in the values file before installing.

### Optional: single sign-on instead of local accounts

The local account store is the default and needs nothing external. To use an OIDC IdP, either set `dependencies.keycloak.mode: internal` (the chart provisions Keycloak and seeds a `go-taas` realm with `admin`/`admin` for evaluation) or `mode: external` with your issuer, client id, secret and redirect URI:

```yaml
dependencies:
  keycloak:
    mode: external
    external:
      issuer: https://idp.example.com/realms/go-taas
      clientId: go-taas-console
      clientSecret: "<secret>"
      redirectUri: https://taas.example.com/api/v1/auth/sso/*
```

The redirect URI must match the console host you exposed in step 5.

## 8. Step 7 — First-run checklist

Everything below is done in the admin console.

The navigation paths are given as `中文 → 中文 (English)`; the console follows the language you pick in
the header, and the two trees are identical.

| # | Task | Where | Required for inference? |
| --- | --- | --- | --- |
| 1 | Import or upload an engine image | 推理 → 镜像 (Inference → Images) | yes |
| 2 | Register, download or upload a model | 推理 → 模型 (Inference → Models) | yes |
| 3 | Create an inference service | 推理 → 推理服务 → 创建推理服务 (Inference → Inference services → Create) | yes |
| 4 | Create an organization, then place the person into it | 访问 → 组织 (Access → Organizations), then 访问 → 用户 → 加入组织 (Access → Users → Join organization) | for end users |
| 5 | Issue an API key | end-user console: API → API 密钥 (API → API Keys) | for API calls |
| 6 | Try it in the playground | end-user console: API → 试用 (API → Playground) | no |
| 7 | Set the platform preferences | 平台 → 系统偏好设置 (Platform → System preferences) | no |
| 8 | Set the inference hostname | 平台 → 网关设置 (Platform → Gateway settings) | for API calls |

Projects are created by the **organization** administrator, not by the platform administrator: an
organization administrator gets their own console scope at `/admin` (组织与访问: projects, roster,
invitations, project member quotas), while the platform scope manages users, organizations, the model
and image catalogs, and every inference service.

### 8.1 Image

With `harbor` configured, **镜像 → 导入镜像** copies an upstream reference into your Harbor project and registers the local one:

```text
docker.io/vllm/vllm-openai:v0.6.3   →   hub.example.com/taas/vllm-openai:v0.6.3
```

Without Harbor you can still register an image that is already reachable from the cluster (public registry or a mirrored one).

### 8.2 Model

**模型 → 模型下载** pulls from ModelScope/HuggingFace into the shared weights directory; **本地上传** takes a `.tar`/`.tar.gz`/`.tgz`/`.zip` of a model directory and extracts it there. Both register the model in the catalog, and both need the shared weights filesystem from step 3.4 (a download fails immediately if `model.weightsDir` is not mounted).

### 8.3 Inference service

**推理服务 → 创建推理服务** picks the model, the image and the accelerator. The form needs at least one node that advertises the accelerator you select (`nvidia.com/gpu` allocatable) — otherwise the pod stays `Pending` with `Insufficient nvidia.com/gpu`.

The service state machine is `queued → starting → running`; `running` means the engine answered its own health endpoint, which can take minutes while weights load.

## 9. Step 8 — Inference endpoint (data plane)

An API caller needs a hostname that reaches the engine. Two configurations are supported; the second is what the reference deployment uses.

### 9.1 Through the AIBrix gateway only

Call the AIBrix Envoy address directly (its Service is a `LoadBalancer`):

```bash
kubectl -n aibrix-system get svc \
  -l gateway.envoyproxy.io/owning-gateway-name=aibrix-eg
curl -s http://<address>/v1/models
```

No API-key enforcement and no metering on this path — every caller is anonymous.

### 9.2 Through the platform data plane (API keys + metering)

Enable the data plane in the chart. It creates a Gateway named `taas-inference` (class `aibrix-eg`) in the TaaS namespace, terminates TLS with `taas-inference-tls`, and the server publishes the `taas-data-plane` route on it; the Wasm plugin in front enforces API keys and reports token usage.

```yaml
# continue in taas-values.yaml
dataPlane:
  enabled: true
  hostname: llm.example.com        # what users call
  gateway:
    name: taas-inference
    className: aibrix-eg
    tlsSecretName: taas-inference-tls
  parentRef:
    name: taas-inference           # the Gateway this chart creates
    namespace: ""                  # empty = the release namespace
    sectionName: https
  backend:
    # The AIBrix Envoy Service the route forwards to. Discover it first:
    #   kubectl -n aibrix-system get svc \
    #     -l gateway.envoyproxy.io/owning-gateway-name=aibrix-eg \
    #     -o jsonpath='{.items[0].metadata.name}{"\n"}'
    name: envoy-aibrix-system-aibrix-eg-903790dc
    namespace: aibrix-system
    port: 80

gatewayPlugin:
  enabled: true                    # API-key auth + metering at the gateway
  failOpen: false
```

```bash
helm upgrade taas deploy/helm/go-taas -n taas --reuse-values -f taas-values.yaml
kubectl -n taas get gateway taas-inference -o wide      # wait for PROGRAMMED=True
kubectl -n taas get httproute                           # taas-data-plane appears
```

Then:

1. Point `llm.example.com` at the `taas-inference` Gateway's address
   (`kubectl -n taas get gateway taas-inference -o jsonpath='{.status.addresses[0].value}'`).
   This address is **different** from the console's; pointing the inference host at the console Gateway returns 404.
2. In the console open **平台 → 网关设置** and set the same hostname (the route's hostname is a console setting, not a chart value), plus the in-cluster gateway URL the server calls (the backend Service's `ClusterIP`, e.g. `http://10.43.253.124`).
3. Create an API key in the end-user console and call the endpoint:

```bash
curl -s https://llm.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-..." \
  -H 'Content-Type: application/json' \
  -d '{"model":"<catalog model name>","messages":[{"role":"user","content":"hello"}],"max_tokens":16}'
```

An invalid key is rejected with `401`; a valid one is metered and appears on the usage pages within the hour.

## 10. Operations

### Upgrade

```bash
helm upgrade taas deploy/helm/go-taas -n taas -f taas-values.yaml
kubectl -n taas rollout status deploy/taas-go-taas-server
```

The chart generates dependency passwords once and reuses them, and the release Secret carries `helm.sh/resource-policy: keep`, so an upgrade or `helm rollback` never rotates a credential the running services already use.

### Uninstall

```bash
helm uninstall taas -n taas
```

The release Secret and the inference namespace (`taas-infer`, annotated `keep`) survive, so running workloads keep serving. Delete them explicitly when they are no longer needed.

## 11. Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `helm install` fails: `Gateway`/`HTTPRoute` not found, or `no matches for kind "EnvoyExtensionPolicy"` | Gateway API CRDs or Envoy Gateway CRDs are missing while `dataPlane.enabled`/`gatewayPlugin.enabled` is on | install step 3.1 first, or set both to `false` to bring up the console only |
| `kubectl -n taas get pods` shows PostgreSQL/Redis/RabbitMQ `Pending`, and `kubectl -n taas describe pod` says `no persistent volumes available for this claim` | the cluster has no default StorageClass (or the requested one does not exist) | install a provisioner, or set `dependencies.*.internal.persistence.storageClass` to an existing class; `persistence.enabled: false` (emptyDir) is development-only and loses data on reschedule |
| Server pod `CrashLoopBackOff` with `init db failed ... dial tcp ...: connect: connection refused` | PostgreSQL was not ready yet at first start | expected once or twice on a fresh install; if it persists, check `kubectl -n taas get pods -l app.kubernetes.io/component=postgresql` and the release Secret |
| Pod `ImagePullBackOff`, `failed to resolve reference ... not found` | the tag does not exist in the registry, or the pull secret is missing | verify the tag (`helm -n taas get values taas \| grep tag`) and that `imagePullSecrets` names an existing Secret |
| Pod `ImagePullBackOff` after pushing a new image with the same tag | `imagePullPolicy: IfNotPresent` and the tag was already cached on the node | push a new tag (recommended) or delete the pod so the kubelet re-resolves it |
| Console returns "The admin console bundle is not built in this environment" | the server image was built without `web/dist` (a from-source build without `npm run build`) | use the published image, or build the console before the binary |
| `curl https://taas.example.com` → 404 | the HTTPRoute is attached to the wrong Gateway, or DNS points at the inference Gateway | attach the console route to the platform Gateway (step 5B) and check `kubectl -n taas get httproute` `Accepted`/`ResolvedRefs` |
| Login rejected with the seeded password | the seed only fills a missing password; a previous install may have set one | `exec` into the server and check `CONFIG_AUTH_SEEDSSO_ADMINLOCALPASSWORD`, or reset through the profile of another platform administrator |
| Inference pod `Pending`, `0/N nodes are unavailable: Insufficient nvidia.com/gpu` | no free GPU, or the node lacks the requested card | free a device, or pick another accelerator; `kubectl get node -o jsonpath='{.status.allocatable}'` |
| Inference pod running but the engine logs "weights not found" | the server and the inference namespace do not share the weights filesystem | both PVCs must be created from the same RWX/JuiceFS volume (step 3.4), and `model.weightsDir` must be `/data/weights` |
| Inference host returns 404 while `/v1/models` works on the AIBrix address | the data-plane route's hostname differs from the DNS record | set the same hostname in 平台 → 网关设置 and in DNS |
| Requests to the inference host return `401 invalid api_key` with a key that works in the console | the plugin checked a different key store, or the key was revoked/expired | issue a fresh key for the right organization; the plugin caches verdicts for `gatewayPlugin.cacheTTL` (30s) |
| Gateway settings page warns the hostname "is not published by any route" | the data-plane route has not converged, or `dataPlane.enabled` is false | enable the data plane, wait for `PROGRAMMED=True`, then re-save the setting |
| Model download fails immediately | `model.weightsDir` is not mounted, or the hub is unreachable | mount the shared PVC (step 4.4) and set `taasServer.config.model.weightsDir` |

Useful diagnostics:

```bash
kubectl -n taas logs deploy/taas-go-taas-server --tail=100
kubectl -n taas logs deploy/taas-go-taas-controller --tail=100
kubectl -n taas get events --sort-by=.lastTimestamp | tail -20
kubectl -n taas-infer get pods,stormservices,podgroups
```

## 12. How this guide is verified

| Claim | How it was checked |
| --- | --- |
| The values file in 5.1 (and the data-plane block in 9.2) is valid and reaches the containers | extracted from this document verbatim and rendered with `helm template taas deploy/helm/go-taas -f …`: 5 Deployments, both ConfigMap/Secret paths, and the expected `CONFIG_IMAGE_HARBOR_URL`, `CONFIG_CONTROLLER_SCHEDULING_SCHEDULERNAME`, `CONFIG_AUTH_SEEDSSO_ADMINLOCALPASSWORD`, `CONFIG_MODEL_WEIGHTSDIR` values |
| Default install needs no third-party component | the default render contains only Deployments, Services, PVCs, RBAC, a Namespace and a Secret — no Gateway API or AIBrix objects |
| The PriorityClass snippet is accepted by the API | `kubectl apply --dry-run=client` against the reference cluster |
| The catalog/console navigation paths, `/healthz` port and login response are accurate | checked against the running reference deployment (console snapshots, `POST /api/v1/auth/login`, chart probes on 9092) |
| The default image tag exists | `ghcr.io/go-taas/go-taas/taas-server:v<chart appVersion>` resolves in the registry |
| `helm lint --strict` passes with the documented keys | run in CI-like fashion on the chart |

## 13. Where the rest of the configuration lives

| Topic | Document |
| --- | --- |
| Every chart value, external PostgreSQL/Redis/RabbitMQ, existing Secrets, labels | Chart README — `deploy/helm/go-taas/README.md` in the repository |
| Control plane / data plane split, module responsibilities | [Architecture Design](../design/architecture.md) |
| What the controller creates for a deployment (roles, pods, gang groups) | [StormService Serving Topology](../architecture/stormservice-serving-topology.md) |
| Engine parameter templates, per-role tabs | [Engine Parameter Templates](../architecture/engine-parameter-templates.md) |
| Local model upload | [Local Model Upload](../architecture/local-model-upload.md) |
| Display preferences (time zone, language, currency) | [Platform Display Preferences](../architecture/platform-display-preferences.md) |
