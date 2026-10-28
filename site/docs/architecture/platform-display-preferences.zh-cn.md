# 平台显示偏好与控制台调整 —— 架构与详细设计

| 属性 | 内容 |
| --- | --- |
| 功能点 | 为整个平台设置一个系统时区、默认语言与货币单位；模型下载并入模型页面；外部模型不可部署；多机推理与按角色的引擎参数；控制台自动扩缩容文案 |
| 文档范围 | `platform_settings` 的显示列与投影、双端可读的查询接口、控制台如何消费这些偏好，以及随附的控制台调整 |
| 所属模块 | `services/runtime`（设置模型、校验、投影、接口） · `services/infer`（外部模型拦截、按角色引擎参数） · `internal/controller`（多机角色、stateful storm、会合参数、按角色参数） · `web`（`platform.tsx`、`display.ts`、`GatewaySettingsPage`、`ModelsPage`、`ModelDownloadsPanel`、`DeployForm`、文案） |
| 关联文档 | [StormService 服务拓扑](./stormservice-serving-topology.zh-cn.md)（形态与按角色调参） · [控制台分端](./console-surface-separation.zh-cn.md) |
| 状态 | 已实现 |

---

## 1. 显示偏好

平台管理员在 `/admin/display-settings`（独立页面，紧跟「网关设置」）设置三个全平台生效的值：

| 设置 | 可选值 | 效果 |
| --- | --- | --- |
| 系统时区 | IANA 名称，或留空 | 所有控制台按此时区显示时间；留空表示各人使用本机时区 |
| 默认语言 | `zh` / `en` | 首次访问者看到的语言；用户在语言切换器中显式选择后以用户选择为准 |
| 货币单位 | `CNY` / `USD` / `EUR` / `HKD` / `JPY` | 金额、价格与成本数字按此货币显示 |

**AD1 —— 与平台设置同一条记录。** `platform_settings` 新增 `timezone`、`default_language`、`currency`（AutoMigrate）。它们与网关主机名一样属于安装级属性，因此存在同一条单例记录里。

**AD1b —— 独立接口保存。** `UpdatePlatformDisplaySettings`（`PUT /api/v1/admin/platform-display-settings`）只写这三个显示字段。此前显示偏好由网关页面的 `UpdatePlatformSettings` 保存，而那是整行覆盖：一旦拆成两个页面，在一个从不展示主机名的页面上保存显示偏好就会把主机名清空。仓储层只写这三列（首次安装时创建记录），不触碰网关字段。

**AD2 —— 使用封闭集合校验。** `normaliseTimezone` 要求 `time.LoadLocation` 能解析（留空合法），`normaliseLanguage` 接受 `en`/`zh`，`normaliseCurrency` 接受上述五种货币。非法值返回 **12905 RUNTIME_SETTING_INVALID**，且不保存任何内容。

**AD3 —— 双端可读。** `GetPlatformDisplaySettings` 同时绑定在 `/api/v1/admin/platform-display-settings` 与 `/api/v1/platform-display-settings`：用户端显示的金额必须与管理端使用同一货币。该接口只投影三个显示字段，绝不暴露网关凭据或主机名。

**AD4 —— 只有一个应用点。** `web/src/display.ts` 以无依赖模块保存当前时区与货币，`formatTime`（api.ts）与 `formatMoney`/`formatAmount` 从中读取。`PlatformDisplayProvider`（挂在两端之上）每次启动读取一次并写入，然后通过 `setLangIfUnset` 应用默认语言（不会覆盖用户已保存的选择）。该 Provider 位于 surface 上下文之上，因此自行发起请求，并按令牌所属 realm 选择路径（会话只在其所属前缀上有效）。

**AD5 —— 所有金额都按平台显示货币呈现。** 控制台只有一个货币单位；在金额后面拼接记录中的币种代码（`{amount} {record.currency}`）正是「配置为 CNY 却仍显示 USD」的原因。因此所有金额渲染统一走 `formatMoney`（最小单位）、`formatMajor`（已是主单位的金额，如账单合计）或 `formatAmount`（指定小数位），都不再拼接存储的币种代码。定价表格仍展示价格自身的币种，因为那里的币种是被编辑的数据而不是展示单位。

**AD6 —— 结算币种与显示币种并排展示。** 金额以服务端配置的 `billing.currency` 存储、以显示货币呈现；没有汇率换算时两者必须是同一个值。因此该页面展示配置的结算币种，不一致时给出告警；以人民币计价的部署应在 `configOverrides` 中设置 `billing.currency: CNY`。静默的不一致会给美元金额贴上 ¥ 符号，比原来的问题更糟。

## 2. 模型下载并入模型页面

下载流程不再是导航项：`ModelDownloadsPage` 改造为 `components/ModelDownloadsPanel`，由模型页面以「模型下载」开关展开，导入表单默认折叠。`/admin/model-downloads` 重定向到 `/admin/models`（`MOVED_ADMIN_ROUTES`），旧书签仍能落到工作位置。

## 3. 外部模型不可部署

`CreateInferenceService` 对 `external` 模型返回 **10106 MODEL_EXTERNAL**：这类模型由提供方通过自己的 API 提供，平台没有权重可调度。部署表单也会把外部模型从选择列表中过滤掉，从源头避免误选。

## 4. 多机推理与按角色引擎参数

原 `distributed` 形态更名为**多机推理**：一个副本每个 worker 一个 Pod，各占一张卡、各在一台机器上，worker 之间通过 stateful storm 提供的 headless Service 会合（见拓扑文档 AD1a–AD1c）。`distributed` 作为旧名称仍然接受，控制台对两种写法都做归一化。

`role_engine_args` 让每种角色单独调参：角色显式设置的参数覆盖共享值，其余继承，额外参数追加。部署表单为每个角色渲染一个参数区块（`prefill`/`decode` 或 `worker`）；`:update-topology` 可接收新的覆盖列表，未提供时保留已存值。

## 5. 控制台文案

自动扩缩相关标签与按钮统一为**自动扩缩容**与**扩缩容**；事件日志保留「最近扩缩事件」（那是一次扩缩事件，而非功能名）。

## 6. 错误码

| 错误码 | 名称 | 触发场景 |
| --- | --- | --- |
| 12905 | `RUNTIME_SETTING_INVALID` | 未知时区、语言不在 en/zh、不支持的货币 |
| 10106 | `MODEL_EXTERNAL` | 部署外部（提供方托管）模型 |
| 10318 | `INFER_ENGINE_ARGS_INVALID` | 角色名未知、角色重复、角色覆盖中的参数非法 |

## 7. 验收标准

- **AC1** `/admin/display-settings` 可编辑时区、默认语言与货币；非法值返回 12905 且不改变任何内容。
- **AC1b** 保存显示偏好不会影响网关主机名与集群内地址。
- **AC2** 保存后的值通过 `platform-display-settings` 在两端可见，管理端设置投影同样携带。
- **AC3** 时间按配置时区显示；时区留空则各人使用本机时区。
- **AC4** 控制台各处的金额（用量、账单、账户、发票、批量成本、预测、优化节省、余额）都使用配置的货币符号，且不再出现存储的币种代码。
- **AC5** 首次访问者获得默认语言；用户显式选择不会被覆盖。
- **AC6** 模型页面承载下载面板；导航不再有下载入口；旧地址自动重定向。
- **AC7** 部署选择列表不含外部模型，接口对之返回 10106。
- **AC8** 多机推理以 stateful storm 为每个 worker 创建 Pod 并带上会合参数；按角色的引擎参数只覆盖对应角色。
- **AC9** 自动扩缩相关文案显示为「自动扩缩容 / 扩缩容」。
