# 离线 / 气隙部署

| 属性 | 内容 |
| --- | --- |
| 功能点 | 在完全无外网访问的环境中安装、升级与运行 Go TaaS：把容器镜像镜像到私有仓库、预置 Helm Chart、从本地归档或内部模型中心导入模型权重 |
| 文档范围 | 离线物料清单、镜像的构建/导出/推送与拉取密钥、Helm Chart 与第三方 Chart 的镜像、共享权重目录与两种模型导入路径、通过策略避免公网模型下载、离线 values 文件、安装、验证与故障处理 |
| 所属模块 | `deploy/helm/go-taas`（Chart） · `deploy/compose`（开发栈） · `build/docker/Dockerfile`（镜像） · `pkg/modelhub`、`services/model`（权重导入与下载） · `site/docs`（本文档） |
| 关联文档 | [在 Kubernetes 上安装 Go TaaS](../getting-started/installation.zh-cn.md)（联网安装路径、第三方组件与控制台暴露） · [本地模型上传](../architecture/local-model-upload.zh-cn.md)（归档格式与解压安全规则） · [镜像管理](../architecture/image-management.zh-cn.md)（镜像目录、导入与预热） · Chart README（完整 values 参考，仓库内 `deploy/helm/go-taas/README.zh-cn.md`） |
| 状态 | 已实现 —— 本地模型上传、Harbor 镜像导入、控制器预热（预拉取）以及下文引用的 Chart values 均已随仓库提供；本文档把它们组织为离线操作流程 |

---

## 0. 这里的「离线」指什么

控制面、控制台、控制器与所有推理 Pod 都在无外网的环境中运行，请求链路不调用任何公网服务。所有外部物料都在联网的中转主机上准备一次，再以文件形式送入气隙环境。

| 阶段 | 网络 | 做什么 |
| --- | --- | --- |
| 构建 / 镜像 | 联网的中转主机 | 构建或拉取平台镜像与推理引擎镜像，镜像第三方 Chart 及其镜像，下载模型权重 |
| 传输 | 移动介质、单向导入设备或受控内网 | 把 OCI 仓库导出、Chart 目录与模型归档送入气隙环境 |
| 安装 | 仅气隙集群 | 用本地 Chart 向私有仓库、私有 Harbor 项目与共享权重文件系统执行 `helm install` |
| 运行 | 仅气隙集群 | 控制台、API、调度、计量与推理数据面全部本地运行；模型权重通过上传或直接写入共享文件系统到达 |

三条导入通道覆盖全部外部依赖：

1. **容器镜像** —— 镜像到集群自己的仓库；参考部署使用 `hub.sudoinfotech.com/taas`。
2. **Helm Chart 与清单** —— Go TaaS Chart，以及 KServe、Envoy Gateway、Volcano、JuiceFS CSI、cert-manager、kube-prometheus-stack 等第三方 Chart，统一下载为 `.tgz` 或 OCI 制品。
3. **模型权重** —— 通过控制台/API 从本地归档导入，或从内部模型中心镜像复制到共享权重目录后按权重路径注册。

## 1. 准备离线物料

在联网的中转主机上建立一个发布物料目录，并把它作为唯一可信来源：

| 物料 | 来源 | 离线落点 | 校验 |
| --- | --- | --- | --- |
| 平台镜像 | 由 `build/docker/Dockerfile` 构建，或从已发布仓库拉取 `taas-server`、`taas-controller`、`taas-console` | 私有仓库 `hub.sudoinfotech.com/taas` | `docker manifest inspect hub.sudoinfotech.com/taas/taas-server:<tag>` |
| 推理引擎镜像 | 模型目录中登记的引擎镜像，例如 `vllm` / `sglang` | 同一私有仓库，或 Harbor 的 `taas` 项目 | 目录中记录的镜像 digest |
| Go TaaS Chart | `deploy/helm/go-taas` | `offline-bundle/charts/go-taas-<version>.tgz` | `helm show chart` |
| 第三方 Chart | KServe、Envoy Gateway、Volcano、JuiceFS CSI、cert-manager、kube-prometheus-stack | `offline-bundle/charts/` | 用离线 values 执行 `helm template` |
| 第三方容器镜像 | 上述 Chart 引用的镜像 | 私有仓库，并把 Chart 的镜像地址改为镜像后的引用 | 安装后 `kubectl get events` |
| 模型权重 | 模型中心、内部模型仓库或微调检查点 | JuiceFS 支撑的共享权重文件系统（`/data/weights`） | 目录条目可解析，且推理 Pod 能读取 |
| Gateway API CRD | Gateway API 发布清单 | `offline-bundle/manifests/` | `kubectl apply --dry-run=server` |
| 证书与密钥 | 组织 PKI | `taas` 命名空间中的 Kubernetes Secret | 控制台与推理域名可完成 TLS 握手 |

> **参考仓库。** Chart 默认使用 `ghcr.io/go-taas/go-taas`；参考部署把同样三个镜像镜像到 `hub.sudoinfotech.com/taas`，渲染后的镜像地址为 `hub.sudoinfotech.com/taas/taas-server`、`.../taas-controller`、`.../taas-console`。相应设置 `image.registry` 与 `image.repository`，Chart 会自动追加组件名。

## 2. 容器镜像

### 2.1 构建并推送平台镜像

在联网的中转主机上，以私有仓库作为镜像仓库构建全部目标并推送：

```bash
# 生成 hub.sudoinfotech.com/taas/{taas-server,taas-controller,taas-console}:<tag>
make docker-build docker-push \
  IMAGE_REPO=hub.sudoinfotech.com/taas \
  IMAGE_TAG=v1.120.0

# 构建网络受限时，把构建期依赖指向内部镜像源
make docker-build \
  IMAGE_REPO=hub.sudoinfotech.com/taas \
  IMAGE_TAG=v1.120.0 \
  GOPROXY=https://goproxy.internal.example.com,direct \
  NPM_REGISTRY=https://npm.internal.example.com \
  APT_MIRROR=http://apt.internal.example.com
```

`IMAGE_TARGETS` 已包含 `taas-server`、`taas-controller` 与 `taas-console`；控制台是独立的 nginx 镜像，不嵌入服务端。若镜像在其他环境构建，可用 `docker save` / `docker load` 或 `skopeo copy` 迁移并重新打标签到私有仓库。

集群通过拉取密钥访问私有仓库：

```bash
kubectl -n taas create secret docker-registry harbor-pull \
  --docker-server=hub.sudoinfotech.com \
  --docker-username='robot$taas+puller' \
  --docker-password='<pull-token>'
```

在发布中引用它（`imagePullSecrets: [{name: harbor-pull}]`），并把 Harbor 推送密码放在独立的 `taas-harbor-push` Secret 中，做法与安装指南一致。

### 2.2 推理引擎镜像与预热

登记推理 Pod 将运行的引擎镜像。配置 Harbor 后，控制台的 **镜像 → 导入镜像** 会把上游引用复制到 `taas` 项目并登记本地引用；没有 Harbor 时，登记一个已经存在于私有仓库的镜像引用即可。该镜像在模型目录中按 `taas` 作用域管理，创建推理服务时由控制器解析。

平台可以在首次部署前把镜像预拉到计算节点：

- 控制台：**镜像** → 目标镜像的 **预热** 操作。
- API：`POST /api/v1/admin/images/{imageId}:warmup`，随后轮询 `GET /api/v1/admin/images/{imageId}/warmup-tasks` 或 `GET /api/v1/admin/warmup-tasks/{taskId}`。

预热是异步的控制器任务，并按节点返回结果。对离线物料中的每个引擎镜像执行预热，避免第一个推理服务卡在多 GB 的镜像拉取上。在宣布物料完整之前，确认相关节点已报告镜像存在。

### 2.3 第三方组件镜像

平台不内置 KServe、Envoy Gateway、Volcano、JuiceFS CSI、cert-manager 或可选监控栈。把这些 Chart 引用的镜像同步到私有仓库，并把 Chart 的镜像地址与标签改为镜像后的引用。把每个 digest 写入物料清单：气隙环境中，一个标签在同步后解析到不同 digest 等于一次无声的供应链变更。

## 3. Helm Chart 与依赖镜像

### 3.1 打包并发布 Go TaaS Chart

Chart 没有 `dependencies:` 段，`helm package` 的产物就是完整制品：

```bash
helm package deploy/helm/go-taas --destination offline-bundle/charts
helm push offline-bundle/charts/go-taas-*.tgz oci://hub.sudoinfotech.com/taas/charts
```

在气隙环境从本地文件安装：

```bash
helm install taas ./offline-bundle/charts/go-taas-<version>.tgz \
  --namespace taas --create-namespace \
  -f taas-offline-values.yaml
```

### 3.2 镜像第三方 Chart

在联网主机上按安装指南锁定的版本解析并下载全部 Chart，再整体搬运 `.tgz`：

```bash
helm repo add kserve https://kserve.github.io/archive
helm repo add envoy-gateway https://gateway.envoyproxy.io/charts
helm repo add volcano https://volcano-sh.github.io/helm-charts
helm repo add juicefs https://juicedata.github.io/charts/
helm repo add jetstack https://charts.jetstack.io
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

helm pull kserve/kserve --version <version> --destination offline-bundle/charts
helm pull envoy-gateway/gateway-helm --version <version> --destination offline-bundle/charts
helm pull volcano/volcano --version <version> --destination offline-bundle/charts
helm pull juicefs/juicefs-csi-driver --version <version> --destination offline-bundle/charts
helm pull jetstack/cert-manager --version <version> --destination offline-bundle/charts
helm pull prometheus-community/kube-prometheus-stack --version <version> --destination offline-bundle/charts
```

安装 Go TaaS 之前，逐个从本地 `.tgz`（或 OCI 镜像仓库）安装。Gateway API CRD 是普通 YAML，放入同一物料目录；它是集群前置条件，不是 Chart 依赖。

### 3.3 构建期的语言依赖镜像

Go module proxy 与 npm registry 只在构建镜像时需要，运行时不会访问。`make docker-build` 会把 `GOPROXY`、`NPM_REGISTRY` 与 `APT_MIRROR` 作为构建参数传入，因此受限网络中的构建主机可以使用组织镜像或 vendored 源码。把镜像地址与解析后的模块校验和写入物料清单。

## 4. 模型权重

权重是唯一必须在推理服务启动前进入集群的物料。它存放在控制面与推理 Pod 共同挂载的 `ReadWriteMany` 文件系统上；参考部署使用 JuiceFS 支撑的 StorageClass。控制器创建推理侧的 PVC，服务端把同一文件系统挂载到 `model.weightsDir`（例如 `/data/weights`）；两个 PVC 之所以共享数据，是因为 StorageClass 指向同一个 JuiceFS 卷。

### 4.1 目录布局

```text
<weightsDir>/<clean name>/<version>/
```

模型目录中的 `weight_path` 就是该相对目录加尾部斜杠。上传导入的模型与从模型中心下载的模型没有区别：相同布局、相同部署路径、相同删除清理。

### 4.2 从本地归档导入

受支持的离线路径是平台自带的上传，不需要外网：

- 控制台：**模型 → 本地上传**。
- API：`POST /api/v1/admin/models:upload`（`multipart/form-data`），字段为 `file`、`name` 与必填的 `hubModelId`（`<vendor>/<model>`）；`version` 省略时默认为 `upload-<时间戳>`，`description` 可选。

接受的归档为模型目录的 `.tar`、`.tar.gz`、`.tgz` 或 `.zip`。解压器会拒绝路径穿越、限制展开体积、拒绝空归档，并保留文件可读权限，确保推理 Pod 能读取。上传大小受 `model.upload.maxArchiveBytes` 限制（默认 200 GiB），且要求已挂载 `model.weightsDir`；未挂载权重目录的安装会拒绝上传，而不是接收无法保存的字节。

模型特别大时，从中转主机直接写入共享文件系统再登记版本，效果相同且没有 HTTP 开销：

```bash
# 中转主机：/srv/models/Qwen2.5-7B-Instruct/{config.json,*.safetensors,...}
rsync -a --info=progress2 /srv/models/Qwen2.5-7B-Instruct/ \
  /mnt/juicefs/taas-weights/Qwen2.5-7B-Instruct/v1/
```

随后在控制台或通过模型 API 以权重路径 `Qwen2.5-7B-Instruct/v1/` 注册模型（控制台不询问版本，服务器会记录内部版本）。文件权限设为 `0644`、目录权限设为 `0750`，与上传器保持一致。

### 4.3 从内部模型中心镜像导入

如果组织已经运行内部模型中心（镜像的 Hugging Face / ModelScope 仓库、内部 S3/MinIO 桶或制品服务），可以把它作为唯一来源，把权重物化到共享文件系统：

1. 在内部模型中心按目录记录的同一 repository id 与 revision 镜像模型。
2. 把快照复制到共享权重目录：可从中转主机复制，也可使用存储侧同步工具（`juicefs sync`、`mc mirror` 或对象存储批量任务）。
3. 登记前用校验和或文件清单与模型中心逐项核对。
4. 以相对权重路径注册目录模型。

内置下载器固定访问**公网**模型中心基址（`https://modelscope.cn`、`https://huggingface.co`），没有暴露可替换基址的 Chart 配置。若内部镜像完整实现了模型中心 API，可通过 DNS 与 HTTPS 反向代理把上述域名指到内部服务；采用前必须验证其列表与下载路径与公网一致。对严格气隙环境，不要依赖这层代理：可靠路径始终是本地归档上传或直接写入共享权重目录。

### 4.4 避免公网模型下载

模型中心下载没有独立的「在线/离线」开关。部署通过不使用 **模型下载** 动作并在网络层强制边界来避免公网下载：

- 在 `taas` 与推理命名空间的出方向拒绝 `huggingface.co`、`modelscope.cn` 及其 CDN / 对象存储域名；只放行私有仓库、内部模型中心、数据库/MQ 端点与集群自身服务。
- 保持 `model.weightsDir` 已挂载，以便本地上传与共享文件导入仍然可用；将其留空会整体关闭模型功能，包括上传。
- 从模型目录移除外部模型提供商，或保持未配置；它们是出站集成，在气隙环境没有位置。
- 引擎镜像与模型快照优先使用固定 digest 与校验和；气隙环境无法重新拉取浮动标签或浮动 revision。

## 5. 离线 values 文件

一个把所有运行时依赖留在集群内的最小 values 文件：

```yaml
# ---- 镜像：只用私有仓库，绝不回退到 ghcr.io --------------------------------
image:
  registry: hub.sudoinfotech.com
  repository: taas
  tag: "v1.120.0"                # 固定版本，不要依赖 :latest
imagePullSecrets:
  - name: harbor-pull

# ---- 首次登录 --------------------------------------------------------------
auth:
  seedSso:
    adminLocalPassword: "<YourStr0ng!Pass>"

# ---- 内部依赖（或内网可达的外部端点） --------------------------------------
dependencies:
  postgresql:
    mode: internal
    internal:
      persistence:
        enabled: true
        size: 20Gi
  redis:
    mode: internal
  rabbitmq:
    mode: internal
  keycloak:
    mode: disabled               # 仅本地账号，不需要外部 IdP

# ---- 共享模型权重 ----------------------------------------------------------
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

# ---- 镜像导入 / 上传使用的私有仓库 -----------------------------------------
harbor:
  hostname: hub.sudoinfotech.com
  project: taas
  username: admin
  existingSecret: taas-harbor-push
  existingSecretKey: harbor-password

# ---- 控制器：推理命名空间中的同一权重文件系统 ------------------------------
controller:
  config:
    controller:
      namespace: taas-infer
      weights:
        pvcName: model-weights
        mountPath: /data/weights
        storageClass: juicefs-platform-shared

# ---- 可选可观测性：未安装 Prometheus 时留空 --------------------------------
monitoring:
  prometheusURL: ""
```

Harbor 推送密码放在 `taas-harbor-push` Secret 中，不要写进 values 文件。集群内 PostgreSQL、Redis 与 RabbitMQ 是最简路径；如果组织已有外部实例，把 `dependencies.*.mode` 设为 `external` 并指向内网端点，凭据放入现有 Secret。

## 6. 安装与验证

```bash
kubectl create namespace taas
kubectl apply -f offline-bundle/manifests/gateway-api-crds.yaml
# 先从 offline-bundle/charts 安装第三方 Chart
helm install taas ./offline-bundle/charts/go-taas-<version>.tgz \
  --namespace taas \
  -f taas-offline-values.yaml
```

按顺序验证：

```bash
kubectl -n taas get pods -o wide
kubectl -n taas get events --sort-by=.lastTimestamp
kubectl -n taas get pvc
kubectl -n taas port-forward svc/taas-go-taas-server 9091:9091
```

| 检查项 | 方法 | 预期 |
| --- | --- | --- |
| 镜像只来自私有仓库 | `kubectl -n taas get pods -o jsonpath='{..image}'` | 每个镜像都以 `hub.sudoinfotech.com/taas/` 开头 |
| 无 `ImagePullBackOff` | `kubectl -n taas describe pod <pod>` | 拉取密钥被接受，digest 与物料一致 |
| 两侧都能看到权重 | 上传一个小模型并部署 | 服务进入 `running`；引擎日志显示权重路径而非下载 |
| 禁止出网时控制台与 API 仍可用 | 打开控制台、创建模型、启动推理服务 | 不需要任何出站调用；服务从共享权重启动 |
| 可观测性在本地 | 如启用，查询集群内 Prometheus | 指标只从内部端点抓取 |
| 不触发公网模型下载 | 在禁止出网时尝试下载动作 | 快速失败或入口不可用；上传路径仍然可用 |

每次升级后重复同样的检查。引入新镜像、新 Chart 或新引擎默认值的升级是一份新的离线物料，而不是只改 values。

## 7. 升级与物料刷新

1. 在联网主机上为新 Chart 版本、新镜像 digest 与新引擎镜像准备物料。
2. 把镜像与 Chart 推送到私有仓库。
3. 使用新的本地 Chart 与同一份离线 values 执行 `helm upgrade`。
4. 在滚动第一个服务前，用预热任务预拉新引擎镜像。
5. 在新版本完成一次真实推理请求之前保留旧 Chart 与镜像 digest；气隙环境的回滚只能依赖保留的物料。

## 8. 故障处理

| 现象 | 常见原因 | 处理 |
| --- | --- | --- |
| `ImagePullBackOff` / `failed to resolve reference` | 镜像标签未推送，或拉取密钥缺失/作用域错误 | 在 Harbor 核对标签，确认 `imagePullSecrets` 指向 `taas` 中的 Secret |
| `helm install` 报缺少依赖 | 第三方 Chart 或 Gateway API CRD 未放入物料 | 把 `.tgz` 与 CRD 清单加入物料并先安装 |
| 模型上传被拒绝 | `model.weightsDir` 未挂载，或归档超过限制 | 在 `/data/weights` 挂载共享 PVC，检查 `model.upload.maxArchiveBytes` |
| 推理服务一直 `Pending` | 加速卡标签/设备插件缺失，或权重 PVC 未绑定 | 给 GPU 节点打标签，确认 `nvidia.com/gpu` 可分配，检查 PVC |
| 推理启动但读不到权重 | 服务端与推理命名空间挂载了不同卷 | 两侧必须使用同一个 JuiceFS 支撑的 `ReadWriteMany` StorageClass，不能是两个本地卷 |
| 出现下载尝试并失败 | 在禁止出网时使用了 **模型下载** | 改用 **本地上传**，或把快照复制进共享权重目录 |
| 可观测页面为空 | `monitoring.prometheusURL` 指向不可达的公网端点 | 指向集群内 Prometheus，或留空 |

## 9. 不在范围内

平台不提供通用制品镜像、包代理或模型中心复制服务。这些属于组织基础设施：内部镜像仓库（Harbor）、内部 Chart 仓库与内部模型存储是本文档假定的输入。平台的职责是在不访问公网的前提下消费它们，这由上文中的镜像仓库、Chart values、共享权重文件系统与本地模型上传共同保证。
