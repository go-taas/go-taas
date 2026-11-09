# 推理服务使用 RDMA 设备 —— 架构与详细设计

| 属性 | 内容 |
| --- | --- |
| 功能点 | 推理服务可以为自己的 Pod 申请 RDMA 网卡，让分布式与 PD 服务通过 RoCE/InfiniBand 而不是 TCP 交换张量与 KV 缓存。设备通过 Kubernetes DRA 分配；Helm Chart 创建 `DeviceClass`，控制器为每个服务写一个 `ResourceClaimTemplate` |
| 文档范围 | 问题背景、DRA 模型、服务契约、校验、控制器的 Pod 装配、清理与同名复用、Chart 资源、控制台界面、错误码与验收标准 |
| 归属模块 | `services/infer`（`rdma.go`、`infer_model.go`、`service.go`、`change_publisher.go`）· `internal/controller`（`rdma.go`、`reconciler.go`）· `proto/taas/infer/v1` · `deploy/helm/go-taas` · `web`（`DeployForm`、`ServiceDetailPage`） |
| 相关文档 | [StormService 服务拓扑](./stormservice-serving-topology.zh-cn.md)（多个角色共用同一个 Pod 模板，因此共用同一个 claim 模板）· [模型目录与部署](./model-catalog-deployment.zh-cn.md)（本文扩展的部署契约） |
| 状态 | 已实现 |

---

## 1. 为什么需要 RDMA

单机服务不需要：模型在一个 Pod 内，张量并行的工作进程通过共享内存通信。但一旦平台把副本跨机器铺开（`multinode`），或把引擎拆成 `prefill` 与 `decode` 角色（`pd`），每个 token 都要过网络：前向传播时的激活值，以及分离式预填充时的 KV 缓存。

走 TCP 时这条链路就是瓶颈。一个 FP8 的 27B 模型，一次长请求的 KV 缓存有数百 MB 需要在 prefill 与 decode Pod 之间搬运；无损 RDMA 网络的传输速度快一个数量级，CPU 占用只是零头。因此面向推理的 GPU 节点都会配一或多张 RDMA 网卡，主流引擎（vLLM、SGLang）在容器能看到设备时就会通过 NCCL 使用它。

平台要做的事情很窄但关键：**让设备出现在有它的节点上的 Pod 里**，每个 Pod 一份，而不是把主机上所有网卡都交给工作负载。

## 2. 为什么用 DRA 而不是扩展资源

传统做法是设备插件发布一个扩展资源（类似 `nvidia.com/gpu` 的 `rdma/rdma_shared_device_a`）。它能用，但无法表达运维真正需要的选择条件：

| 问题 | 扩展资源 | DRA |
| --- | --- | --- |
| 链路是否为 up？ | 只有一个不透明的数量 | `device.attributes["dra.net"].state == "up"` |
| 哪张卡 / 哪个 PCI 地址？ | 无法表达 | `dra.net/pciAddress`、`dra.net/pciDevice` |
| 哪个 NUMA 节点？ | 无法表达 | `dra.net/numaNode` |
| 一个 Pod 要多个设备 | 只能写数量，不保证互不相同 | 单个请求上 `exactly.count: N` |
| 分配归属 | 容器资源列表的一部分 | 独立的 `ResourceClaim` 对象，可审计 |

现网已经在运行 DRA 网络驱动（`dra.net`，NVIDIA DRA network driver），因此平台直接使用它。这样设备申请就是**声明式的数据**：写在服务行上，由控制器渲染，平台不需要再维护自己的设备插件。

**AD1 —— `DeviceClass` 属于 Chart 基础设施，claim 模板属于工作负载产物。** 集群提供哪些设备是集群事实，与单个服务无关，因此由 Chart 创建一次。每个 Pod 用多少设备**是**服务属性，因此控制器按服务写 `ResourceClaimTemplate`，并随服务一起删除。若只在 Chart 里放一个共享模板，所有服务就只能用同一个设备数量。

**AD2 —— 每个服务一个模板，而不是每个服务一个 claim。** `ResourceClaim` 是单次分配：多个 Pod 引用同一个 claim 会争抢同一组设备。`ResourceClaimTemplate` 正是 DRA 对此的答案——每个由它创建的 Pod 都会得到自己的 claim，这正是多副本 StormService 需要的（N 个副本 × 每个副本 M 个 Pod，各自独立）。

## 3. 契约

```proto
message RdmaConfig {
  bool enabled = 1;          // 只有为 true 才申请
  int32 devices = 2;         // 每个 Pod 的设备数；0 视为 1，上限 8
  string device_class = 3;   // DRA DeviceClass；空 = 平台默认
}
```

| RPC | 字段 | 行为 |
| --- | --- | --- |
| `CreateInferenceService` | `rdma` | 任何写入前完成校验，随后落库 |
| `GetInferenceService` / 列表摘要 | `rdma` | 始终存在；未申请时 `enabled=false` |
| `UpdateInferenceServiceTopology` | `rdma` | 传了则替换；**不传则保留原值**，改布局不会静默丢掉设备 |

放在 `UpdateInferenceServiceTopology` 里，是因为它和 `scheduling` 一样属于 Pod 属性：运维调大 `gpu_per_pod` 的同一个对话框，就是他们补充互联配置的地方。

## 4. 存储

`inference_services`

| 列 | 类型 | 含义 |
| --- | --- | --- |
| `rdma_enabled` | bool，非空，默认 `false` | 服务是否申请设备 |
| `rdma_devices` | int，非空，默认 `0` | 每个 Pod 的设备数（关闭时为 0） |
| `rdma_device_class` | varchar(253)，非空，默认 `''` | 申请的类；空 = 平台默认 |

关闭时存 0 而不是保留旧数量，这样之后重新开启且不显式指定数量时，拿到的是文档化默认值，而不是复活一个旧值。

## 5. 校验

`NormalizeRdma` 在任何行或事件产生之前，以 **10324 `INFER_RDMA_INVALID`** 拒绝：

- `devices` 超出 `1..8`（每 GPU 一张网卡的全节点是最宽的实际情况；上限用于在调度器看到之前拦住笔误）；
- `device_class` 超过 253 字符，或不是 DNS 子域（`DeviceClass` 名称的合法形状）；
- 关闭的申请既不保留数量，也不保留类名。

控制器补充一项 API 无法完成的运行时校验：应用部署时 `DeviceClass` 必须存在。类名写错会以 “RDMA device class \"x\" does not exist” 让部署失败，而不是留下一堆永远无法调度的 Pod。

## 6. 控制器行为

每个 `upsert`/`start`/`update_*` 事件都会执行 [`applyRdmaClaims`](../../../internal/controller/rdma.go)：

1. **开启** —— 先确认 `DeviceClass` 存在；创建或更新 `<service>-rdma`（`ResourceClaimTemplate`），只含一个设备请求：`allocationMode: ExactCount`、`count: devices`，带 `go-taas.com/service-id` 标签，并在 `spec.metadata` 下写同样标签，使 DRA 生成的 claim 也带上它们。随后每个角色的 Pod 模板获得
   `spec.resourceClaims[{name: rdma, resourceClaimTemplateName: <service>-rdma}]`，引擎容器获得 `resources.claims[{name: rdma}]`。
2. **关闭** —— 删除模板，避免关闭 RDMA 后留下无用的设备申请。

claim 引用加在 Storm 构建函数产出的 Pod 模板上，因此**所有拓扑、所有角色**都会带上：single、multinode、pd 都由同一个函数构建 Pod。

清理（`stop`/`delete`）按名删除模板（带归属校验），按 service-id 标签清扫 `resourceclaimtemplates` **与** `resourceclaims`，并在同名创建前回收旧模板。因此旧服务的迟到删除事件不会拆掉新服务的设备。

**AD3 —— 模板必须先于工作负载存在。** 引用不存在模板的 Pod 会在准入阶段被拒绝；先建工作负载再建模板，会把一个清晰的控制器错误变成卡死的部署。

**AD4 —— 回收保证同名复用安全。** 服务名平台唯一且删除后可复用。模板带 `go-taas.com/service-id`，归属判断（`ownedByOtherService`）能区分“我们的旧对象”和“新所有者的对象”。

**AD5 —— 没有 DRA 驱动时明确失败，不做静默降级。** 读取 `DeviceClass` 返回 `meta.IsNoMatchError` 时，直接报告集群不支持 DRA。服务会以可操作的错误失败，绝不会在没有设备的情况下启动。

## 7. Chart 资源

`templates/deviceclass-rdma.yaml` 渲染：

```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: taas-rdma
spec:
  selectors:
    - cel:
        expression: 'device.driver == "dra.net" && device.attributes["dra.net"].rdma == true && device.attributes["dra.net"].state == "up"'
```

| 值 | 默认 | 含义 |
| --- | --- | --- |
| `rdma.enabled` | `true` | 是否渲染 `DeviceClass` |
| `rdma.deviceClass` | `taas-rdma` | 服务继承的类名 |
| `rdma.driver` | `dra.net` | 该类接受的 DRA 驱动 |
| `rdma.requireLinkUp` | `true` | 只接受节点报告为 up 的链路 |
| `rdma.selector` | `""` | 完整 CEL 覆盖（固定网卡、PCI 地址、NUMA） |
| `controller.config.controller.rdma.deviceClass` | `taas-rdma` | `CONFIG_CONTROLLER_RDMA_DEVICECLASS`，必须一致 |

控制器 `ClusterRole` 增加 `deviceclasses` 的 `get/list/watch`，以及 `resourceclaimtemplates`/`resourceclaims` 的完整生命周期权限。

**AD6 —— `requireLinkUp` 默认开启。** 把 down 的端口交给 Pod，会得到一个能启动却做不了 RDMA 的服务。若驱动版本不发布 `dra.net/state`，需要设置 `rdma.requireLinkUp: false`（已在 values 注释中说明）；相反默认则会静默分配死链路。

## 8. 控制台界面

| 界面 | 控件 |
| --- | --- |
| 部署表单 | “RDMA 设备”开关、“每个 Pod 的设备数”（1–8）、“设备类”（空 = 平台默认） |
| 服务详情 → 拓扑卡片 | `RDMA：2 个设备（来自 taas-rdma）` 或 `未申请` |
| 服务详情 → 拓扑对话框 | 同样三个控件，因为该申请随 Pod 形态一起变更 |

## 9. 错误码

| 错误码 | 含义 |
| --- | --- |
| 10324 | `INFER_RDMA_INVALID` —— 设备数量或类名不可用 |

运行时失败（类不存在、集群没有 DRA API）通过控制器上报的 `failure_reason` 呈现。

## 10. 验收标准

| ID | 标准 |
| --- | --- |
| AC1 | 以 `rdma.enabled=true, devices=2` 创建的服务会存储两者，`GetInferenceService` 与列表摘要都会返回，变更事件也会携带 |
| AC2 | 关闭或省略申请时不存数量，且 Pod 模板完全不变 |
| AC3 | single、multinode、pd 三种拓扑下，每个角色的 Pod 模板都引用 `<service>-rdma`，引擎容器都声明该 claim |
| AC4 | `ResourceClaimTemplate` 从指定类申请 `devices` 个不同设备（`ExactCount`），未指定类时使用平台默认 |
| AC5 | `DeviceClass` 不存在时部署失败并指出类名，且不创建工作负载 |
| AC6 | 关闭 RDMA 会删除模板；stop/delete 会删除模板；同名复用时回收旧模板而不是冲突 |
| AC7 | `devices` 超出 `1..8` 或类名非法时，在任何写入前返回 10324 |
| AC8 | `UpdateInferenceServiceTopology` 不传 `rdma` 时保留原申请；传了则替换 |

## 11. 引擎是怎么知道设备名的

平台**不会**把设备名传给 vLLM。Pod spec 里的 `rdma` 是**claim 在 Pod 内的名字**，
不是网卡名；`devices: 2` 只是数量。真正确定可用范围的是：驱动把被授权设备的字符设备
节点注入容器，剩下的由引擎自己发现。

1. DRA 驱动（这里是 `dra.net`）分配例如 PCI `0000:0f:00.0`、`0000:0f:00.1`，然后把
   对应的 `/dev/infiniband/uverbsN` 与配套网卡注入容器的网络命名空间。
2. 容器内 `/sys/class/infiniband` 是**宿主**的 sysfs，节点上的 6 个设备都可见；但只有
   被授权的那些才有 `/dev/infiniband/uverbsN` 节点。
3. `libibverbs` 逐个尝试打开 `uverbsN`，打不开的直接跳过。因此 NCCL 与
   `PyNcclConnector` 只会看到被授权的设备，名字沿用宿主驱动枚举出来的 `mlx5_N`。

在一台有 6 张网卡的节点上申请 2 个设备的实测结果：`/dev/infiniband` 里是
`uverbs4`/`uverbs5`，`/sys/class/infiniband` 列出 `mlx5_0`…`mlx5_5`，而
`ibv_get_device_list` 只返回 **`mlx5_4` 与 `mlx5_5`**，且都可打开。同一节点上另一个
申请 2 个设备的 Pod 拿到的是 `uverbs2`/`uverbs3`。可见索引取决于驱动当时空闲的设备与
宿主枚举顺序，**跨 Pod、跨节点都不固定**，绝不能写死在模板里。

**指定某张网卡。** 一个 Pod 拥有多个设备时 NCCL 会全部使用，这正是 rail 优化想要的；
若要限制，通过服务的 `env` 设置变量即可（无需改代码）：

| 变量 | 作用 |
| --- | --- |
| `NCCL_IB_HCA=mlx5_4` | 只用该设备（`mlx5_4:1` 指定端口） |
| `NCCL_DEBUG=INFO` | 日志 `NET/IB : Using [0]mlx5_4:1/IB` 会写明实际选用的设备 |
| `NCCL_IB_DISABLE=1` | 完全关闭 IB：引擎会静默回退到 TCP，因此 RDMA 服务上绝不能设置 |

**实践提醒。** 只申请一个设备的 Pod，在多功能网卡节点上仍会在
`/sys/class/infiniband` 看到其他网卡；它们打不开所以无害，但绕过 libibverbs 直接读
sysfs 的工具（部分 NCCL 分支、`ibv_devinfo` 风格脚本）可能会把它们列出来。工作负载
必须唯一确定设备时，设置 `NCCL_IB_HCA`。
