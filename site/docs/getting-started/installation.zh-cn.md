# 在 Kubernetes 上安装 Go TaaS —— 安装与部署指南

| 属性 | 内容 |
| --- | --- |
| 功能点 | Go TaaS 平台的端到端安装：集群前置条件、第三方组件、Helm 发布、带域名与证书的控制台暴露、初始管理员密码，以及以「成功登录控制台」为终点的首次使用清单 |
| 文档范围 | 受支持的安装路径、第三方依赖及官方安装链接、Chart 无法代劳的集群准备步骤、生产 values 文件、控制台的三种暴露方式、数据面与推理域名、升级与卸载，以及实践中遇到的故障及其解决办法 |
| 所属模块 | `deploy/helm/go-taas`（Chart） · `apps/taas-server`、`apps/controller`（镜像） · `internal/controller`（推理资源） · `site/docs`（本文档） |
| 关联文档 | Chart README（完整 values 参考，仓库内 `deploy/helm/go-taas/README.zh-cn.md`） · [架构设计](../design/architecture.zh-cn.md)（控制面 / 数据面划分） · [StormService 服务拓扑](../architecture/stormservice-serving-topology.zh-cn.md)（控制器为一次部署创建什么） |
| 状态 | 已实现 —— 参考部署运行在单节点 RKE2 集群（Kubernetes v1.36、Helm 4.3、AIBrix 0.7、Volcano 1.15、Envoy Gateway 1.8、JuiceFS CSI 0.32）；本文档每个片段都已针对 Chart 校验（见第 12 节） |

---

## 0. 最终你会得到什么

| 你会得到 | 地址 |
| --- | --- |
| **管理控制台** | `https://<控制台域名>/admin`（或通过端口转发访问 `http://127.0.0.1:9091/admin`） |
| **用户控制台** | `https://<控制台域名>/` |
| **管理 API** | `https://<控制台域名>/api/v1/admin/...` |
| **用户 API** | `https://<控制台域名>/api/v1/...` |
| **推理 API**（OpenAI 兼容） | `https://<推理域名>/v1/chat/completions` |
| **首次登录** | 用户名 `admin`，密码来自 `auth.seedSso.adminLocalPassword`（默认 `Pass4admin`） |

平台由两个 Deployment 组成：`taas-server`（gRPC 9090，REST 网关与两套控制台在 9091，指标与 `/healthz` 在 9092）和 `taas-controller`（把推理工作负载同步到集群）。其余组件 —— PostgreSQL、Redis、RabbitMQ —— 要么由 Chart 在集群内置备，要么由你提供外部地址。

> **发布名与资源名。** 发布名 `taas` + Chart 名 `go-taas` 时，所有资源都带 `taas-go-taas-` 前缀：服务端 Service 是 `taas-go-taas-server`，数据库是 `taas-go-taas-postgresql`。下文示例均按该组合书写。如果你用 `helm install go-taas ...` 安装，资源名会变成 `go-taas-*` —— 不确定时直接查服务：
>
> ```bash
> kubectl -n taas get svc -l app.kubernetes.io/component=server
> ```

## 1. 开始之前

| 步骤 | 是否必需 | 耗时 | 何时可以跳过 |
| --- | --- | --- | --- |
| [1. Kubernetes](#2-步骤-1--kubernetes可选) | 仅在没有集群时需要 | 10–20 分钟 | 已有集群，且 `kubectl` 已指向它 |
| [2. 第三方组件](#3-步骤-2--第三方组件) | 推理需要 Gateway API + Envoy Gateway 与 AIBrix；模型权重需要 JuiceFS | 30–60 分钟 | 只想先把控制台跑起来（做完步骤 3–5 即可） |
| [3. 集群准备](#4-步骤-3--集群准备) | 必需 | 10 分钟 | 不可跳过 —— 这些是 Chart 无法猜测的输入 |
| [4. 安装平台](#5-步骤-4--安装平台) | 必需 | 5 分钟 | 不可跳过 |
| [5. 暴露控制台](#6-步骤-5--暴露控制台) | 必需，三选一 | 5 分钟 | 不可跳过 —— 你需要一个地址来登录 |
| [6. 登录](#7-步骤-6--登录) | 必需 | 1 分钟 | 不可跳过 |

在执行 `helm` 的机器上需要：

- `kubectl`，且其上下文具备创建集群级对象的权限（Chart 会创建 ClusterRole 与 ClusterRoleBinding）；
- Helm 3.8+ 或 Helm 4.x；
- 创建命名空间的权限（或已存在的 `taas` 命名空间）；
- 一个**默认 StorageClass**：集群内的 PostgreSQL、Redis 与 RabbitMQ 各自需要一个
  PersistentVolumeClaim。没有默认存储类时它们的 Pod 会一直 `Pending` —— 请安装存储插件
  （longhorn、local-path 等），或把 `dependencies.*.internal.persistence.storageClass` 指向已有的存储类。

## 2. 步骤 1 —— Kubernetes（可选）

如果已有集群，跳过本步骤。参考部署使用 **[RKE2](https://docs.rke2.io/install/quickstart)** —— 一个符合 CNCF 规范、单二进制的 Kubernetes 发行版，自带 GPU 节点所需的 CRI、CNI 与 CSI 组件：

```bash
# 在每个节点（server 或 agent）上以 root 执行
curl -sfL https://get.rke2.io | sudo sh -
sudo systemctl enable --now rke2-server        # 控制面节点
# 其他工作节点：
#   curl -sfL https://get.rke2.io | INSTALL_RKE2_TYPE="agent" sudo sh -
#   sudo systemctl enable --now rke2-agent
```

让 `kubectl` 指向它（kubeconfig 由安装脚本写入）：

```bash
export KUBECONFIG=/etc/rancher/rke2/rke2.yaml
kubectl get nodes
```

其他发行版同样可用（k3s、kind、EKS/GKE/AKS）；平台只要求 Kubernetes 1.25+ 且存在默认 StorageClass。

**GPU 节点。** 安装 NVIDIA GPU Operator（或厂商 device plugin），使 `nvidia.com/gpu` 成为可分配资源，并由 GPU Feature Discovery 为节点打标签：

```bash
kubectl get node <gpu-node> -o jsonpath='{.status.allocatable.nvidia\.com/gpu}{"\n"}'
kubectl get node <gpu-node> --show-labels | tr ',' '\n' | grep -E 'gpu-vendor|gpu.product'
```

纯 CPU 的体验环境无需 GPU。

## 3. 步骤 2 —— 第三方组件

Go TaaS 不内置这些组件。请使用它们各自的 Chart 安装，并遵循其官方文档 —— 下表链接即为上游安装指南，不是本文档的副本。

| 组件 | 平台为何需要 | 是否必需 | 官方安装文档 |
| --- | --- | --- | --- |
| **Gateway API CRD** + **Envoy Gateway** | 提供 `Gateway`/`HTTPRoute` API，以及控制台路由与数据面路由挂载所需的 GatewayClass | 通过 Gateway 暴露控制台（步骤 5 方案 B）时必需 | [Gateway API](https://gateway-api.sigs.k8s.io/guides/) · [Envoy Gateway（Helm）](https://gateway.envoyproxy.io/docs/install/install-helm/) |
| **AIBrix** | 推理编排：`StormService`（角色/Pod）、`ModelClaim` 与 `PodAutoscaler` CRD，以及为模型提供前置代理的 `aibrix-eg` GatewayClass | 推理必需（控制台不需要） | [AIBrix 安装](https://aibrix.readthedocs.io/latest/getting_started/installation/installation.html) |
| **Volcano** | Gang 调度（一个副本的 Pod 整体放置）与优先级抢占；提供控制器所指向的调度器 | 推荐；仅当设置 `controller.scheduling.schedulerName: volcano` 时需要 | [Volcano 安装](https://volcano.sh/en/docs/installation/) |
| **JuiceFS CSI**（或任意 ReadWriteMany 存储） | 控制面（下载/上传）与推理 Pod（只读挂载）共享的同一套模型权重文件系统 | 模型下载/上传必需；任何 RWX 存储类均可 | [JuiceFS CSI 驱动](https://juicefs.com/docs/csi/getting_started/) |
| **Harbor**（或其他镜像仓库） | 平台内部镜像仓库：镜像导入/上传流程把引擎镜像推到这里并注册本地引用 | 可选；未设置时镜像导入与上传功能关闭 | [Harbor 安装](https://goharbor.io/docs/2.11.0/install-config/) |
| **kube-prometheus-stack** | 指标页面读取 Prometheus；没有它时相关页面回退到控制器自身采集的样本 | 可选 | [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack) |
| **HAMi** | vGPU 切分（共享 GPU） | 可选 | [HAMi（Helm）](https://project-hami.io/docs/get-started/deploy-with-helm) |
| **cert-manager** | 为控制台与推理域名签发证书 | 可选（自带证书时不需要） | [cert-manager（Helm）](https://cert-manager.io/docs/installation/helm/) |

### 3.1 Gateway API 与 Envoy Gateway

```bash
# Gateway API CRD（standard 通道；v1.5.1 为参考部署使用的版本）
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.1/standard-install.yaml

# Envoy Gateway（参考部署为 v1.8.1；上游当前版本为 v1.9.x）
helm install envoy-gateway oci://docker.io/envoyproxy/gateway-helm \
  --version v1.8.1 --namespace envoy-gateway-system --create-namespace
kubectl wait --timeout=5m -n envoy-gateway-system \
  deployment/envoy-gateway --for=condition=Available
```

然后创建为控制台域名终止 TLS 的平台 Gateway（步骤 5 的 `HTTPRoute` 挂载到它）。一个 Gateway 可以服务多个域名：

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
            name: platform-tls        # 在步骤 4.3 创建
```

```bash
kubectl apply -f platform-gateway.yaml
kubectl -n envoy-gateway-system get gateway envoy-gateway   # ADDRESS 即入口地址
```

### 3.2 AIBrix

AIBrix 以发布清单形式分发；请按其 README 的顺序安装（先依赖，再 CRD，最后组件）：

```bash
export AIBRIX_VERSION=v0.7.0     # 参考部署使用 0.7.0；更高版本同样可用

# 依赖（Envoy Gateway、KubeRay 等）
kubectl apply -f "https://github.com/vllm-project/aibrix/releases/download/${AIBRIX_VERSION}/aibrix-dependency-${AIBRIX_VERSION}.yaml" --server-side
# CRD 单独发布，这样卸载 operator 不会删除用户资源
kubectl apply -f "https://github.com/vllm-project/aibrix/releases/download/${AIBRIX_VERSION}/aibrix-core-crds-${AIBRIX_VERSION}.yaml" --server-side
# operator 及其数据面
kubectl apply -f "https://github.com/vllm-project/aibrix/releases/download/${AIBRIX_VERSION}/aibrix-core-${AIBRIX_VERSION}.yaml"

kubectl -n aibrix-system get pods
kubectl get gatewayclass                      # 应看到 aibrix-eg，Accepted=True
```

AIBrix 同时提供 Helm Chart；
[官方安装文档](https://aibrix.readthedocs.io/latest/getting_started/installation/installation.html)对两种方式都有说明。

AIBrix 会自带 Envoy 数据面；它创建的 Service 就是平台转发推理流量指向的目标（步骤 9 会说明如何确定其名称）。

### 3.3 Volcano

```bash
helm repo add volcano-sh https://volcano-sh.github.io/helm-charts
helm repo update
helm install volcano volcano-sh/volcano --namespace volcano-system --create-namespace
kubectl -n volcano-system get pods
```

控制器使用调度器名 `volcano` 与三个 PriorityClass。**Chart 不会创建 PriorityClass** —— 请在集群中创建一次（或把 values 指向你已有的类）：

```bash
cat <<'EOF' | kubectl apply -f -
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: high-priority
value: 1000000
globalDefault: false
preemptionPolicy: PreemptLowerPriority
description: "生产推理；可抢占低优先级工作负载"
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: normal-priority
value: 500000
globalDefault: true
preemptionPolicy: PreemptLowerPriority
description: "默认推理优先级"
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: low-priority
value: 100000
globalDefault: false
preemptionPolicy: PreemptLowerPriority
description: "批处理与实验性推理"
EOF
kubectl get priorityclass | grep -E "high-priority|normal-priority|low-priority"
```

如果不安装 Volcano，请把 `controller.config.controller.scheduling` 留空：推理 Pod 将使用集群默认调度器逐个放置，优先级只作为 Pod 上的标签存在。

### 3.4 共享模型权重（JuiceFS CSI）

控制面写入模型权重（Hub 下载或压缩包上传），推理 Pod 读取它们。两者必须看到**同一个**文件系统，因此 StorageClass 必须是 ReadWriteMany 且后端为共享文件系统。

```bash
helm repo add juicefs https://juicedata.github.io/charts/
helm repo update
helm install juicefs-csi-driver juicefs/juicefs-csi-driver \
  --namespace kube-system \
  --set mountRoot=/var/lib/juicefs/volume \
  --set kubeletDir=/var/lib/kubelet
```

按上面链接的官方指南创建卷（format + StorageClass）。参考部署使用所有租户共享的 `juicefs-platform-shared`；任何 RWX 存储类都可用，`juicefs-*` 是经过验证的方案。

> **重要：** 服务端挂载的是**它自己命名空间**里的 PVC，控制器创建的 PVC 在**推理命名空间**里。它们能共享数据，唯一原因是 StorageClass 指向同一个 JuiceFS 卷 —— 两个 `local-path` 或 `longhorn` PVC 会是两块不同的磁盘，推理将因「weights not found」失败。

### 3.5 Harbor（可选）

创建名为 `taas` 的项目（或使用你自己的名称并设置 `harbor.project`）、一个可推送的账号，以及平台读取的两个 Secret：

```bash
# 镜像存放在自有仓库时，TaaS Pod 的拉取凭据
kubectl -n taas create secret docker-registry harbor-pull \
  --docker-server=hub.example.com \
  --docker-username='robot$taas+puller' \
  --docker-password='<token>'

# 镜像导入 / 上传使用的推送凭据
kubectl -n taas create secret generic taas-harbor-push \
  --from-literal=harbor-password='<push-password>'
```

## 4. 步骤 3 —— 集群准备

这些是 Chart 无法推断的输入。

### 4.1 加速卡节点标签

推理 Pod 通过节点标签固定加速卡厂商（以及在指定时固定卡型）。控制器接受 `go-taas.com/accelerator`，并会回退到 GPU Feature Discovery 设置的 `gpu-vendor`：

```bash
# 推荐：显式打标签（值为平台使用的厂商标识）
kubectl label node <gpu-node> go-taas.com/accelerator=nvidia --overwrite
# 卡型来自 device plugin 自身的标签，例如
kubectl get node <gpu-node> -o jsonpath='{.metadata.labels.nvidia\.com/gpu\.product}{"\n"}'
```

可用的厂商标识为 `nvidia`、`iluvatar`、`metax`，以及用于无加速卡镜像的 `cpu`。控制台展示的卡型来自实时清单，因此标签就绪后无需额外配置。

### 4.2 命名空间

```bash
kubectl create namespace taas
```

`helm install --create-namespace` 会替你创建；先创建的好处是能提前把拉取凭据与 TLS Secret 放进去。

### 4.3 TLS 证书

需要两个证书：控制台域名与推理域名。可以用 cert-manager 的 `Certificate` 资源签发，也可以导入自有证书：

```bash
# 控制台域名 —— Gateway 终止 TLS 时放在 Gateway 所在命名空间
kubectl -n envoy-gateway-system create secret tls platform-tls \
  --cert=fullchain.pem --key=privkey.pem

# 推理域名 —— 始终放在 TaaS 命名空间（Chart 创建的 Gateway 在这里读取）
kubectl -n taas create secret tls taas-inference-tls \
  --cert=fullchain.pem --key=privkey.pem
```

通配符证书（`*.example.com`）可同时覆盖两个域名，是最常见的选择。Chart 有意不创建这些 Secret：证书的生命周期长于 Helm 发布，且通常由域名管理者轮换。

### 4.4 服务端使用的共享权重 PVC

服务端需要把同一套权重文件系统挂载到 `/data/weights`：

```yaml
# weights-pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: taas-model-weights
  namespace: taas
spec:
  accessModes: [ReadWriteMany]
  storageClassName: juicefs-platform-shared   # 你的 RWX 存储类
  resources:
    requests:
      storage: 100Gi                        # 共享文件系统不受该值限制
```

```bash
kubectl apply -f weights-pvc.yaml
```

## 5. 步骤 4 —— 安装平台

### 5.1 values 文件

保存为 `taas-values.yaml`，替换所有 `<...>` 占位符。这是参考部署的形态：集群内 PostgreSQL/Redis/RabbitMQ、本地管理员账号、Volcano 调度、Harbor 镜像目录，以及两个网关集成。

```yaml
# ---- 镜像 -------------------------------------------------------------------
image:
  registry: ghcr.io
  repository: go-taas/go-taas
  tag: ""                       # 留空 = v<Chart appVersion>；生产环境请固定版本
imagePullSecrets: []            # 镜像在自有仓库时，例如 [{name: harbor-pull}]

# ---- 首次登录 ---------------------------------------------------------------
auth:
  seedSso:
    # 初始化的平台管理员 `admin` 的初始密码。
    # 策略：>= 8 个字符，且大写/小写/数字/符号中至少三类。
    adminLocalPassword: "<YourStr0ng!Pass>"

# ---- 依赖（体验环境用集群内实例即可） ---------------------------------------
dependencies:
  postgresql:
    mode: internal              # 生产环境改为 external 并填写 external 块
    internal:
      persistence:
        enabled: true
        size: 20Gi
  redis:
    mode: internal
  rabbitmq:
    mode: internal
  keycloak:
    mode: disabled              # 仅使用本地账号；SSO 见步骤 7

# ---- 控制面 -----------------------------------------------------------------
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

# ---- 镜像仓库（可选：启用镜像导入 / 上传） ---------------------------------
harbor:                         # 也可写作 taasServer.harbor
  hostname: hub.example.com
  project: taas
  username: admin
  existingSecret: taas-harbor-push
  existingSecretKey: harbor-password

# ---- 控制器 -----------------------------------------------------------------
controller:
  config:
    controller:
      namespace: taas-infer
      weights:
        pvcName: model-weights
        mountPath: /data/weights
        storageClass: juicefs-platform-shared   # 步骤 3.4 中的 RWX 存储类
      # Gang 调度与优先级抢占（步骤 3.3）。未安装 Volcano 时整块删除。
      scheduling:
        schedulerName: volcano
        priorityClassHigh: high-priority
        priorityClassNormal: normal-priority
        priorityClassLow: low-priority

# ---- 监控（可选） -----------------------------------------------------------
monitoring:
  prometheusURL: ""             # 例如 http://kube-prometheus-stack-prometheus.monitoring.svc:9090
```

> `taasServer.harbor` 与顶层 `harbor` 是同一组配置：Chart 优先读取顶层 `harbor`，同时兼容旧文档中的 `taasServer.harbor`，因此两种写法都生效。

### 5.2 安装

```bash
helm install taas deploy/helm/go-taas \
  --namespace taas --create-namespace \
  --values taas-values.yaml
```

从仓库克隆后安装时，把最后一段换成 Chart 目录：

```bash
git clone https://github.com/go-taas/go-taas.git
cd go-taas
helm install taas deploy/helm/go-taas -n taas --create-namespace -f taas-values.yaml
```

### 5.3 等待滚动更新完成

```bash
kubectl -n taas rollout status deploy/taas-go-taas-server --timeout=10m
kubectl -n taas rollout status deploy/taas-go-taas-controller --timeout=5m
kubectl -n taas get pods
```

全新安装时**服务端常会重启 1～2 次**：PostgreSQL 尚未接受连接时它会快速失败，由 Kubernetes 重启。`RESTARTS=1`/`2` 之后进入 `Running` 属正常；若持续 CrashLoop 则不正常（见第 11 节故障排查）。

### 5.4 验证 API 可用

```bash
kubectl -n taas port-forward svc/taas-go-taas-server 9091:9091 &
curl -s http://127.0.0.1:9091/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<YourStr0ng!Pass>"}'
# {"response":{"code":0,"message":"OK"},"sessionToken":"...","realm":"admin"}
```

能拿到 `sessionToken` 说明数据库、初始化管理员与认证链路都正常 —— 控制台可以登录了。

> `/healthz` 与 `/metrics` 在**监控端口 9092** 上，不在控制台端口：9091 上控制台路由会把包括 `/healthz` 在内的
> 所有路径都返回 SPA 页面。检查健康状态请用 `kubectl -n taas port-forward svc/taas-go-taas-server 9092:9092`
> 后 `curl -s http://127.0.0.1:9092/healthz`，或直接依赖 Chart 的就绪探针（它做的就是这件事）。

## 6. 步骤 5 —— 暴露控制台

三选一。参考部署使用方案 B。

### 方案 A —— 端口转发（最快，仅体验）

```bash
kubectl -n taas port-forward svc/taas-go-taas-server 9091:9091
# 控制台：http://127.0.0.1:9091/   管理控制台：http://127.0.0.1:9091/admin
```

无需其他配置，但该地址只在你的机器上存在。

### 方案 B —— Gateway API HTTPRoute + 真实域名与证书

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
      name: envoy-gateway            # 步骤 3.1 创建的平台 Gateway
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
        request: 900s                # 模型与镜像上传都是长时间传输
```

```bash
kubectl apply -f console-route.yaml
kubectl -n envoy-gateway-system get gateway envoy-gateway \
  -o jsonpath='{.status.addresses[0].value}{"\n"}'   # DNS 指向该地址
```

`timeouts.request: 900s` 在实践中不是可选项：模型压缩包上传或镜像推送会在同一个请求上持续数分钟，默认超时会直接中断。

然后创建 DNS 记录 `taas.example.com → <Gateway 地址>` 并确认：

```bash
curl -sI https://taas.example.com/ | head -1        # HTTP/2 200
```

**不要**把控制台路由挂到推理 Gateway（`taas-inference`）上 —— 那个 Gateway 由 Chart 管理、只承载推理流量，没有控制台后端。

### 方案 C —— LoadBalancer Service

```bash
helm upgrade taas deploy/helm/go-taas -n taas --reuse-values \
  --set taasServer.service.type=LoadBalancer
kubectl -n taas get svc taas-go-taas-server -w    # 等待 EXTERNAL-IP 出现
```

然后把 DNS 指向该外部地址并在其前面终止 TLS（或使用云厂商的 `LoadBalancer` 注解托管证书）。该路径下控制台为纯 HTTP，除非你额外加 TLS。

## 7. 步骤 6 —— 登录

1. 打开 `https://<控制台域名>/admin`（或 `http://127.0.0.1:9091/admin`）。
2. 控制台会跳转到登录页；使用用户名 **`admin`** 与你在 `auth.seedSso.adminLocalPassword` 中设置的密码登录。
3. 管理控制台默认打开用户目录；用户控制台位于 `/`。

初始化账号被标记为平台管理员，因此不需要所属组织，也豁免登录邮箱验证码。若登录被拒绝：

```bash
# 该发布是否应用了初始化配置？
kubectl -n taas exec deploy/taas-go-taas-server -- env | grep SEEDSSO
kubectl -n taas logs deploy/taas-go-taas-server | grep -i "seed"
```

**请在首次登录后立即修改密码**（账号菜单 → 个人资料），或在安装前把强密码写入 values 文件。

### 可选：用单点登录替代本地账号

默认使用平台自有账号体系，无需任何外部组件。若要用 OIDC IdP，可设置 `dependencies.keycloak.mode: internal`（Chart 会置备 Keycloak 并 seed 出 `go-taas` realm 与体验用的 `admin`/`admin`），或设为 `mode: external` 并提供 issuer、client id、secret 与 redirect URI：

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

redirect URI 必须与步骤 5 暴露的控制台域名一致。

## 8. 步骤 7 —— 首次使用清单

以下操作都在管理控制台完成。

| # | 任务 | 位置 | 推理是否必需 |
| --- | --- | --- | --- |
| 1 | 导入或上传推理引擎镜像 | 推理 → 镜像 | 是 |
| 2 | 注册、下载或上传模型 | 推理 → 模型 | 是 |
| 3 | 创建推理服务 | 推理 → 推理服务 → 创建推理服务 | 是 |
| 4 | 创建组织，并把用户加入该组织 | 访问 → 组织；再 访问 → 用户 → 加入组织 | 面向终端用户时 |
| 5 | 签发 API Key | （用户控制台）API → API 密钥 | 通过 API 调用时 |
| 6 | 在试用页试跑 | （用户控制台）API → 试用 | 否 |
| 7 | 设置平台偏好 | 平台 → 系统偏好设置（时区、语言、货币） | 否 |
| 8 | 设置推理域名 | 平台 → 网关设置 | 通过 API 调用时 |

项目由**组织**管理员创建，而不是平台管理员：组织管理员在 `/admin` 拥有自己的管理域（组织与访问：项目、
成员、邀请、项目成员配额），平台管理域则管理用户、组织、模型与镜像目录，以及全部推理服务。

### 8.1 镜像

配置 `harbor` 后，**镜像 → 导入镜像** 会把上游引用复制到你的 Harbor 项目并注册本地引用：

```text
docker.io/vllm/vllm-openai:v0.6.3   →   hub.example.com/taas/vllm-openai:v0.6.3
```

没有 Harbor 时，仍可注册集群内已可直接拉取的镜像（公共仓库或镜像站）。

### 8.2 模型

**模型 → 模型下载** 从 ModelScope/HuggingFace 拉取到共享权重目录；**本地上传** 接收模型目录的 `.tar`/`.tar.gz`/`.tgz`/`.zip` 并解压到同一位置。两者都会把模型注册进目录，且都依赖步骤 3.4 的共享权重文件系统（`model.weightsDir` 未挂载时下载会立即失败）。

### 8.3 推理服务

**推理服务 → 创建推理服务** 选择模型、镜像与加速卡。表单至少需要一个声明了所选加速卡的节点（`nvidia.com/gpu` 可分配）—— 否则 Pod 会停留在 `Pending` 并报 `Insufficient nvidia.com/gpu`。

服务状态机为 `queued → starting → running`；`running` 表示引擎自身健康检查已通过，加载权重期间可能需要数分钟。

## 9. 步骤 8 —— 推理入口（数据面）

API 调用方需要一个能到达引擎的域名。支持两种配置；参考部署使用第二种。

### 9.1 仅通过 AIBrix 网关

直接调用 AIBrix Envoy 地址（其 Service 为 `LoadBalancer`）：

```bash
kubectl -n aibrix-system get svc \
  -l gateway.envoyproxy.io/owning-gateway-name=aibrix-eg
curl -s http://<address>/v1/models
```

该路径没有 API Key 校验，也没有计量 —— 任何调用方都是匿名的。

### 9.2 通过平台数据面（API Key + 计量）

在 Chart 中启用数据面。它会在 TaaS 命名空间创建名为 `taas-inference` 的 Gateway（class `aibrix-eg`），用 `taas-inference-tls` 终止 TLS，并由服务端在其上发布 `taas-data-plane` 路由；前置的 Wasm 插件负责校验 API Key 并上报词元用量。

```yaml
# 继续写在 taas-values.yaml 中
dataPlane:
  enabled: true
  hostname: llm.example.com        # 用户调用的域名
  gateway:
    name: taas-inference
    className: aibrix-eg
    tlsSecretName: taas-inference-tls
  parentRef:
    name: taas-inference           # Chart 创建的 Gateway
    namespace: ""                  # 留空 = 发布的命名空间
    sectionName: https
  backend:
    # 路由转发到的 AIBrix Envoy Service，先查询其名称：
    #   kubectl -n aibrix-system get svc \
    #     -l gateway.envoyproxy.io/owning-gateway-name=aibrix-eg \
    #     -o jsonpath='{.items[0].metadata.name}{"\n"}'
    name: envoy-aibrix-system-aibrix-eg-903790dc
    namespace: aibrix-system
    port: 80

gatewayPlugin:
  enabled: true                    # 在网关处做 API Key 校验与计量
  failOpen: false
```

```bash
helm upgrade taas deploy/helm/go-taas -n taas --reuse-values -f taas-values.yaml
kubectl -n taas get gateway taas-inference -o wide      # 等待 PROGRAMMED=True
kubectl -n taas get httproute                           # 出现 taas-data-plane
```

随后：

1. 把 `llm.example.com` 指向 `taas-inference` Gateway 的地址
   （`kubectl -n taas get gateway taas-inference -o jsonpath='{.status.addresses[0].value}'`）。
   该地址与**控制台地址不同**；把推理域名指到控制台 Gateway 会返回 404。
2. 在控制台打开 **平台 → 网关设置**，设置同一个域名（路由域名是控制台设置项，不是 Chart 值），并填写服务端调用的集群内网关地址（上述 backend Service 的 `ClusterIP`，例如 `http://10.43.253.124`）。
3. 在用户控制台创建 API Key 并调用：

```bash
curl -s https://llm.example.com/v1/chat/completions \
  -H "Authorization: Bearer sk-..." \
  -H 'Content-Type: application/json' \
  -d '{"model":"<目录中的模型名>","messages":[{"role":"user","content":"hello"}],"max_tokens":16}'
```

无效 Key 返回 `401`；有效 Key 会被计量，并在一小时内出现在用量页面。

## 10. 运维操作

### 升级

```bash
helm upgrade taas deploy/helm/go-taas -n taas -f taas-values.yaml
kubectl -n taas rollout status deploy/taas-go-taas-server
```

Chart 只生成一次依赖密码并复用，发布 Secret 带 `helm.sh/resource-policy: keep`，因此升级或 `helm rollback` 不会轮换正在运行的组件已在使用的凭据。

### 卸载

```bash
helm uninstall taas -n taas
```

发布 Secret 与推理命名空间（`taas-infer`，带 `keep` 注解）会保留，因此正在运行的负载继续服务。不再需要时请显式删除。

## 11. 故障排查

| 现象 | 原因 | 解决办法 |
| --- | --- | --- |
| `helm install` 报找不到 `Gateway`/`HTTPRoute`，或 `no matches for kind "EnvoyExtensionPolicy"` | 开启了 `dataPlane.enabled`/`gatewayPlugin.enabled`，但缺少 Gateway API 或 Envoy Gateway CRD | 先执行步骤 3.1，或把两者设为 `false` 只先跑控制台 |
| `kubectl -n taas get pods` 中 PostgreSQL/Redis/RabbitMQ 处于 `Pending`，`describe pod` 提示 `no persistent volumes available for this claim` | 集群没有默认 StorageClass（或所请求的存储类不存在） | 安装存储插件，或把 `dependencies.*.internal.persistence.storageClass` 设为已有存储类；`persistence.enabled: false`（emptyDir）仅用于开发，Pod 重新调度会丢数据 |
| 服务端 Pod `CrashLoopBackOff`，日志 `init db failed ... dial tcp ...: connection refused` | 首次启动时 PostgreSQL 尚未就绪 | 全新安装出现一两次属正常；若持续，请检查 `kubectl -n taas get pods -l app.kubernetes.io/component=postgresql` 与发布 Secret |
| Pod `ImagePullBackOff`，`failed to resolve reference ... not found` | 标签在仓库中不存在，或缺少拉取凭据 | 核对标签（`helm -n taas get values taas \| grep tag`）并确认 `imagePullSecrets` 指向存在的 Secret |
| 推送同标签新镜像后 Pod 仍 `ImagePullBackOff` 或运行旧代码 | `imagePullPolicy: IfNotPresent`，节点已缓存该标签 | 推新标签（推荐），或删除 Pod 让 kubelet 重新解析 |
| 控制台返回 "The admin console bundle is not built in this environment" | 服务端镜像未包含 `web/dist`（源码构建时未执行 `npm run build`） | 使用官方镜像，或在编译前先构建控制台 |
| `curl https://taas.example.com` 返回 404 | HTTPRoute 挂错 Gateway，或 DNS 指向了推理 Gateway | 把控制台路由挂到平台 Gateway（步骤 5 方案 B），并检查 `kubectl -n taas get httproute` 的 `Accepted`/`ResolvedRefs` |
| 用初始化密码登录被拒 | 初始化只会填充「缺失」的密码，之前的安装可能已设置过 | `exec` 进服务端查看 `CONFIG_AUTH_SEEDSSO_ADMINLOCALPASSWORD`，或通过另一位平台管理员重置 |
| 推理 Pod `Pending`，`0/N nodes are unavailable: Insufficient nvidia.com/gpu` | 没有空闲 GPU，或节点缺少所选卡型 | 释放设备或改选其他加速卡；`kubectl get node -o jsonpath='{.status.allocatable}'` |
| 推理 Pod 已运行但引擎日志报 "weights not found" | 服务端与推理命名空间未共享权重文件系统 | 两个 PVC 必须来自同一个 RWX/JuiceFS 卷（步骤 3.4），且 `model.weightsDir` 必须为 `/data/weights` |
| 在 AIBrix 地址上 `/v1/models` 正常，但推理域名返回 404 | 数据面路由域名与 DNS 记录不一致 | 在 平台 → 网关设置 与 DNS 中设为同一域名 |
| 用控制台可用的 Key 调用推理域名返回 `401 invalid api_key` | 插件查询的是另一套 Key 存储，或 Key 已吊销/过期 | 为正确的组织签发新 Key；插件会缓存判定 `gatewayPlugin.cacheTTL`（默认 30s） |
| 网关设置页提示域名「未由任何路由发布」 | 数据面路由尚未收敛，或 `dataPlane.enabled` 为 false | 启用数据面，等待 `PROGRAMMED=True` 后重新保存设置 |
| 模型下载立即失败 | `model.weightsDir` 未挂载，或 Hub 不可达 | 挂载共享 PVC（步骤 4.4）并设置 `taasServer.config.model.weightsDir` |

常用诊断命令：

```bash
kubectl -n taas logs deploy/taas-go-taas-server --tail=100
kubectl -n taas logs deploy/taas-go-taas-controller --tail=100
kubectl -n taas get events --sort-by=.lastTimestamp | tail -20
kubectl -n taas-infer get pods,stormservices,podgroups
```

## 12. 本文档的验证方式

| 结论 | 验证方式 |
| --- | --- |
| 5.1 的 values 文件（以及 9.2 的数据面片段）合法且能生效 | 从本文档原样抽取后用 `helm template taas deploy/helm/go-taas -f …` 渲染：得到 5 个 Deployment、ConfigMap/Secret 路径，以及预期的 `CONFIG_IMAGE_HARBOR_URL`、`CONFIG_CONTROLLER_SCHEDULING_SCHEDULERNAME`、`CONFIG_AUTH_SEEDSSO_ADMINLOCALPASSWORD`、`CONFIG_MODEL_WEIGHTSDIR` 取值 |
| 默认安装不需要任何第三方组件 | 默认渲染结果只有 Deployment、Service、PVC、RBAC、Namespace 与 Secret —— 没有 Gateway API 或 AIBrix 对象 |
| PriorityClass 片段能被 API 接受 | 对参考集群执行 `kubectl apply --dry-run=client` |
| 控制台导航路径、`/healthz` 端口与登录响应准确 | 对照运行中的参考部署（控制台快照、`POST /api/v1/auth/login`、Chart 在 9092 上的探针） |
| 默认镜像标签存在 | `ghcr.io/go-taas/go-taas/taas-server:v<Chart appVersion>` 在仓库中可解析 |
| 文档中的配置项通过 `helm lint --strict` | 以 CI 方式对 Chart 执行 |

## 13. 其他配置在哪里看

| 主题 | 文档 |
| --- | --- |
| 全部 Chart values、外接 PostgreSQL/Redis/RabbitMQ、已有 Secret、标签 | Chart README —— 仓库内 `deploy/helm/go-taas/README.zh-cn.md` |
| 控制面 / 数据面划分与模块职责 | [架构设计](../design/architecture.zh-cn.md) |
| 控制器为一次部署创建什么（角色、Pod、gang 组） | [StormService 服务拓扑](../architecture/stormservice-serving-topology.zh-cn.md) |
| 引擎参数模板与按角色 Tab | [推理引擎参数模板](../architecture/engine-parameter-templates.zh-cn.md) |
| 本地模型上传 | [本地模型上传](../architecture/local-model-upload.zh-cn.md) |
| 显示偏好（时区、语言、货币） | [平台显示偏好](../architecture/platform-display-preferences.zh-cn.md) |
