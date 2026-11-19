# 致谢与开源项目

| 属性 | 内容 |
| --- | --- |
| 功能点 | Go TaaS 所依赖的开源项目清单：每个项目的官方主页，以及平台具体使用它的部分 |
| 文档范围 | 平台基础、推理引擎、模型与存储、状态与消息、Go 依赖、Web 控制台、可观测性与可选身份；每个条目的核对依据 |
| 所属模块 | `site/docs`（本文档） · `go.mod` · `web/package.json` · `deploy/` · 平台架构文档 |
| 关联文档 | [架构设计](../design/architecture.zh-cn.md)（系统上下文与组件边界） · [在 Kubernetes 上安装 Go TaaS](../getting-started/installation.zh-cn.md)（第三方组件及官方安装链接） · [离线 / 气隙部署](../deployment/offline-air-gap.zh-cn.md)（这些项目制品的镜像方式） |
| 状态 | 已实现 —— 下文每个项目都能在 `go.mod`、`web/package.json`、部署清单或文档集中找到明确引用 |

---

Go TaaS 是一个集成型项目：它把一组成熟的开源组件组合成多租户的 Token 工厂。没有下列社区，
就没有这个平台。本页是完整清单，仓库 [README](../../../README.zh-cn.md) 保留简短版本。

## 1. 平台基础

| 项目 | 官方主页 | Go TaaS 的用法 |
| --- | --- | --- |
| **Kubernetes** | <https://kubernetes.io/> | 运行时底座：Deployment、Service、PVC、RBAC、CRD、DRA 设备类，以及控制器所调谐的 API |
| **KServe** | <https://kserve.github.io/kserve/> | 推理控制面与 `LLMInferenceService` CRD（model、template、replicas、worker、prefill、parallelism、router、scaling）；控制器为分布式与单机服务写入 KServe 资源 |
| **Envoy Gateway** | <https://gateway.envoyproxy.io/> | 南北向数据面：发布控制台与 OpenAI 兼容推理端点的 Gateway 与 HTTPRoute |
| **Gateway API** | <https://gateway-api.sigs.k8s.io/> | 控制台与推理入口共同使用的 `Gateway`、`HTTPRoute` 及相关 CRD |
| **Volcano** | <https://volcano.sh/> | 可选的推理副本组调度（gang scheduling）与优先级抢占；控制器写入 PodGroup，并在 Pod 模板中设置调度器名 |
| **HAMi** | <https://github.com/Project-HAMi/HAMi> | 加速卡层的 GPU 切分与 vGPU 分区；不使用 HAMi 的异构设备管理与调度器 |
| **cert-manager** | <https://cert-manager.io/> | 可选的证书签发，用于控制台与推理域名；Chart 本身不创建 TLS Secret |

## 2. 推理引擎

| 项目 | 官方主页 | Go TaaS 的用法 |
| --- | --- | --- |
| **vLLM** | <https://github.com/vllm-project/vllm> | 主要的 OpenAI 兼容推理引擎，也是 KServe 预设所面向的引擎容器；平台镜像目录登记 vLLM 引擎镜像并调优其启动参数 |
| **SGLang** | <https://github.com/sgl-project/sglang> | 与 vLLM 并列的另一种推理引擎，支持分布式与 prefill/decode 分离拓扑 |

## 3. 模型与存储

| 项目 | 官方主页 | Go TaaS 的用法 |
| --- | --- | --- |
| **Hugging Face Hub** | <https://huggingface.co/> | 模型目录中模型中心下载的来源之一，包括模型元数据与文件布局 |
| **ModelScope** | <https://modelscope.cn/> | 第二个模型中心来源；下载器同时支持 `huggingface` 与 `modelscope` 两种目录来源 |
| **JuiceFS** | <https://juicefs.com/> | 支撑 `ReadWriteMany` 的 StorageClass，在控制面与推理 Pod 之间共享模型权重；参考部署的权重文件系统 |
| **MinIO** | <https://min.io/> | `deploy/compose` 中开发用 JuiceFS 卷的本地对象存储；可替换为任意 S3 兼容存储 |
| **Harbor** | <https://goharbor.io/> | 集群内部容器镜像仓库：镜像导入/上传流程把引擎镜像推入 Harbor 项目并登记本地引用 |

## 4. 状态与消息

| 项目 | 官方主页 | Go TaaS 的用法 |
| --- | --- | --- |
| **PostgreSQL** | <https://www.postgresql.org/> | 系统记录库：租户、模型目录、部署、计量、计费、审计等关系型状态 |
| **Redis** | <https://redis.io/> | 需要低延迟共享状态的缓存与协调；Chart 可在集群内制备，也可指向外部实例 |
| **RabbitMQ** | <https://www.rabbitmq.com/> | 解耦异步工作的消息队列：期望状态变更、计量写入、预热与下载状态、各类 runner |
| **Keycloak** | <https://www.keycloak.org/> | 可选的外部或由 Chart 制备的 OIDC 身份提供方；默认安装使用平台自有账号体系与种子本地管理员 |

## 5. Go 平台依赖

| 项目 | 官方主页 | Go TaaS 的用法 |
| --- | --- | --- |
| **gRPC** | <https://grpc.io/> | 服务间契约；`proto/taas/**` 生成的每个平台服务都是 gRPC 服务 |
| **grpc-gateway** | <https://github.com/grpc-ecosystem/grpc-gateway> | 由同一份 Protobuf 生成的 HTTP/JSON 接口与 OpenAPI 文档 |
| **GORM** | <https://gorm.io/> | PostgreSQL 上的 ORM 与迁移层（测试中使用 SQLite），各服务共用的仓储模式 |
| **Prometheus client for Go** | <https://github.com/prometheus/client_golang> | 服务端监控端口与 gRPC 服务端指标暴露 |
| **Kubernetes client-go** | <https://github.com/kubernetes/client-go> | 控制器对 Kubernetes API 的类型化访问，以及服务端的只读集群发现 |
| **Zap** | <https://github.com/uber-go/zap> | 服务端、控制器与 runner 的结构化日志 |

完整依赖集见 [`go.mod`](../../../go.mod)；上表列出读者最可能关心的项目，而不是全部传递依赖。

## 6. Web 控制台

| 项目 | 官方主页 | Go TaaS 的用法 |
| --- | --- | --- |
| **React** | <https://react.dev/> | 同时承载用户端与管理端两套界面的单页应用 |
| **Vite** | <https://vite.dev/> | 控制台的开发服务器、构建管线与生产包 |
| **Phosphor Icons** | <https://phosphoricons.com/> | 控制台使用的图标集 |
| **TypeScript** | <https://www.typescriptlang.org/> | 控制台的类型系统，生产构建由 `tsc -b` 编译 |

具体版本固定在 [`web/package.json`](../../../web/package.json)。

## 7. 可观测性

| 项目 | 官方主页 | Go TaaS 的用法 |
| --- | --- | --- |
| **Prometheus** | <https://prometheus.io/> | 指标抓取（Chart 的 PodMonitor 与服务端监控端口）；配置 `monitoring.prometheusURL` 后，资源指标与可观测页面以它为数据源 |
| **OpenTelemetry** | <https://opentelemetry.io/> | 架构文档定义的可观测栈：与 Prometheus、日志聚合并列的 OTel 兼容追踪与指标 |
| **Grafana** | <https://grafana.com/> | 运维方安装 `kube-prometheus-stack` 时的可选仪表盘；平台不内嵌 Grafana，自身控制台渲染自己的面板 |

请求追踪由平台自身存储（`traces` 与 `trace_spans`，与请求日志一同写入），不集成 Jaeger，
也不集成托管追踪后端。

## 8. 清单范围

只有满足以下任一条件的项目才会被列出：

- 在 `go.mod` 中作为直接或间接依赖出现；
- 在 `web/package.json` 中出现；
- 被 `deploy/helm/go-taas`、`deploy/compose` 或安装指南引用；
- 属于设计文档描述、且仓库实际依赖的架构组成部分。

仅在设计文档中作为对比对象出现的项目（例如平台明确不采用的托管云服务或评测工具）不会列入。
Knative 不是平台的依赖：分布式推理设计明确拒绝 Knative/Istio 的额外足迹，参考拓扑使用
KServe 的 `LLMInferenceService` 与 Envoy Gateway。

## 9. 回馈社区

如果你基于 Go TaaS 构建产品，对这些项目最有用的贡献是精确的缺陷报告或文档修复，并附上不依赖
私有部署的复现步骤。在本仓库内，[贡献指南](../../../CONTRIBUTING.zh-cn.md) 与
[行为准则](../../../CODE_OF_CONDUCT.md) 说明了如何修改此处列出的集成。
