# 成本分析仪表盘 — 架构与详细设计

| 属性 | 内容 |
| --- | --- |
| 功能点 | 成本分析仪表盘 — 按组织 / 模型 / 密钥的成本归因，带趋势、每 token 成本，以及按维度的时间范围成本分解（backlog 第 29 行） |
| 文档范围 | 功能 29 的架构与详细设计：`billing` 模块中的只读成本分析（通过共享数据库只读读取 `metering` 的 `usage_records`）；`BillingService` 上的 `GetCostAnalyticsOverview`（admin 舰队）与 `GetCostAnalytics`（双绑定）RPC；admin 成本页面（`/admin/cost`、`/admin/cost/:dimension/:value`）与 end-user 成本页面（`/cost`、`/cost/:dimension/:value`）；以及错误处理、配置、安全、上线与逐层函数级设计 |
| 拥有模块 | `billing`（对 `charge_records` 的只读聚合以进行成本归因，以及两个 RPC）、`metering`（只读：通过共享数据库从 `usage_records` 获取 token 总计）、`model`（只读：`model_name` 解析）、`auth`（只读：`api_key_name` 解析、会话领域、会话活跃组织）、`tenancy`（RoleGuard，只读）、`pkg/server` 网关（admin 前缀与 user 前缀绑定）、控制台 Web 应用（admin `CostAnalyticsPage`/`CostDetailPage`，end-user `UserCostAnalyticsPage`/`UserCostDetailPage`） |
| 相关文档 | [需求分析与 UI/UX 设计](../design/cost-analytics-dashboard.md) · [架构设计](../design/architecture.md) §2.6（`billing`）· [用量仪表盘与按请求成本归因](./usage-dashboard.md)（同类只读仪表盘及其内联 SVG 图表、新鲜度、范围与分组约定）· [API 密钥用量分析](./api-key-usage-analytics.md)（同类按密钥分析及其 top 密钥排名）· [模型 × 卡类型价格矩阵与分层定价](./pricing.md)（每个成本数字背后的计费公式与生效日期价格）· [控制台表面分离](./console-surface-separation.md)（本功能跨越的两个表面、`UserShell`/`AdminShell` 约定、掩码投影规则） |
| 状态 | 架构完成，已移交给 Developer 智能体 |

---

## 1. 概述与目标

go-taas 将已结算用量转为带金额的 `charge_records`（按密钥 × 模型 × 卡 × 小时，功能 #5），用量仪表盘（功能 #9）与 API 密钥用量分析（功能 #28）按密钥归因成本。控制台仍无法回答的是操作员与租户的问题：*我的钱花到哪里去了，成本如何趋势？* 用量仪表盘以分组切换展示成本，但不以按维度成本归因开头；API 密钥分析页面按成本对密钥排名，但不按模型或组织分解成本；两者都不展示每 token 成本或成本趋势图。没有表面按维度（组织、模型、密钥）归因成本、展示成本随时间趋势、并报告每 token 成本。

本功能新增**成本分析仪表盘**：按组织 / 模型 / 密钥的成本归因，带趋势、每 token 成本，以及按维度的时间范围成本分解。它是**只读**聚合层 — 推理、计量或计费管线均无变化。它是 Phase 4 生产化路线图条目中最小且有独立价值的增量：它将"成本很高"变成"本月成本 60% 是模型 X、40% 是组织 Y，且每 token 成本在上升"。

**目标**：`GetCostAnalyticsOverview` RPC（admin 舰队：卡片 + 维度分解 + 成本趋势 + 每 token 成本）与 `GetCostAnalytics` RPC（admin 按维度钻取与 end-user 按维度视图：单维度值卡片 + 趋势），两者都在 `BillingService` 上；admin 成本页面（`/admin/cost`）与按维度钻取（`/admin/cost/:dimension/:value`），以及 end-user 成本页面（`/cost`）与钻取（`/cost/:dimension/:value`）；新错误码 11301 `CodeCostDimensionInvalid` 与 11302 `CodeCostDimensionValueNotFound`；页面 → 路由 → API 前缀表，含精确前缀；各页面交互状态，包括空、错误与权限拒绝；以及可在 compose 栈上以 Nightwatch 测试的编号验收标准。

**非目标**（延后，设计 §8）：实时流式指标（charge-record 节奏不变）；预测（AWS 的 18 个月预测不在范围内）；异常检测或阈值告警（功能 #26 在控制台内消费可观测性事件 — 此处不在范围内）；保存的自定义视图或仪表盘；在图表中并排比较维度（v1 展示维度分解与单维度趋势；比较图表是未来工作）；向租户暴露服务 ID、副本数或其他操作员编排内部信息（D8）；对推理、计量或计费管线的任何更改（只读功能）；新审计事件（D10）。

### 1.1 本文档的阅读顺序

第 2 节记录架构决策（AD1–AD10，对应设计的 D1–D10）。第 3–5 节是组件视图、数据模型与 API 设计。第 6 节是前端架构（两个控制台的页面 → 路由 → API 前缀表，各表面的认证守卫）。第 7–8 节是时序流与错误处理。第 9–11 节是配置、安全与上线。第 12–14 节是验收标准追溯、函数级详细设计与有序实现任务清单。

---

## 2. 架构决策

| # | 决策 | 理由 |
| --- | --- | --- |
| AD1 | **成本 RPC 位于 `BillingService`**，并**通过共享数据库直接读取 `metering` 的 `usage_records`** — 无跨服务 RPC | 两个模块解析同一 `server.Components` PostgreSQL 句柄（`s.components.DB().GormDB()`），因此直接 SQL 读取是既定模式（usage-dashboard AD1）。设计文档的"拥有模块：billing + metering"得到实现：billing 拥有聚合并通过共享数据库只读读取 metering 的 usage records（设计 D3） |
| AD2 | **成本错误块为 11301–11399，即 usage-keys 112xx 之后的下一个空闲块。** usage-keys 模块拥有 11201（功能 #28）。设计"usage-keys 块（112xx）之后的新块"的意图通过取 112xx 之后的下一个空闲块来实现 | 每个模块有自己的错误块（`pkg/errors/codes.go`）；usage-keys 占用 112xx，因此 cost 占用 113xx。这是与现有架构最一致的解读（设计 D9，已确认） |
| AD3 | **成本从 `charge_records` 派生**（它们携带按密钥 × 模型 × 卡 × 小时的金额，功能 #5），在服务端按维度聚合。指标族为**成本**（整数分）、**token**（来自 `usage_records`，输入 + 输出 + 缓存 + 推理）与**每 token 成本**（客户端派生为成本 ÷ token） | `charge_records` 携带权威按密钥成本（功能 #5）；`usage_records` 携带 token 总计（功能 #4）。服务端聚合保持负载小、客户端依赖轻（设计 D2） |
| AD4 | **每个表面形状一个 RPC**，而非扩展 `GetUsageDashboard`：`GetCostAnalyticsOverview`（admin 舰队：卡片 + 维度分解 + 成本趋势 + 每 token 成本）与 `GetCostAnalytics`（admin 按维度钻取与 end-user 按维度视图：单维度值卡片 + 趋势）。end-user 表面复用 `GetCostAnalytics`，限定到租户自己的组织 | 成本页面需要同时多个形状（头条卡片、维度分解、趋势）；把它们硬塞进 `GetUsageDashboard` 会破坏其既定的分组语义，而单独调用会重现 N+1 慢控制台。专用 RPC 对将成本关注点从用量仪表盘的表面分离（设计 D3） |
| AD5 | **`dimension` 是服务端的** — `organization`（仅 admin）、`model`、`api_key` — 切换它会重新获取。**指标切换（cost / tokens / cost-per-token）是客户端的**，因为每个桶携带全部三个指标 | 负载保持单维度宽；切换感觉即时（AWS/OpenAI 模式）。admin 表面支持 `organization` 维度；end-user 表面仅支持 `model` 与 `api_key`（租户只看到自己的组织）（设计 D4） |
| AD6 | **时间桶随范围自适应**：范围 ≤ 7 天为小时桶，否则为天桶。范围上限为 **92 天**，验证复用 **10404**（metering 范围契约） | 短范围的小时粒度展示日内成本尖峰；长范围的天粒度保持负载小。92 天上限与 10404 复用使范围契约与每个 metering 查询统一（usage-dashboard AD7）（设计 D5） |
| AD7 | **图表以内联 SVG 渲染** — 每桶一个条（或线），带指标切换器（cost / tokens / cost-per-token）— 无新图表依赖 | 控制台刻意依赖轻；小型可测试 SVG 组件匹配 usage-dashboard AD8 决策（设计 D6） |
| AD8 | **新鲜度是显式的**：每个响应携带 `data_through`（charge records 覆盖的最后一个完整桶），控制台在所选范围超出时显示"data through <time>"说明加待处理/部分标记 | 成本滞后混淆是最常记录到的陷阱（AWS 24 小时滞后、OpenAI 用量滞后）；标记以零新管线工作保持新鲜度故事诚实（设计 D7） |
| AD9 | **end-user 表面是租户范围且掩码的**：user 前缀上的 `GetCostAnalytics` 仅返回租户自己组织的成本，无服务 ID、无副本数、无其他租户数据 | 遵循功能 #17 的掩码投影规则与可观测性 AD4 模式：租户获得自己的成本归因，而非操作员内部信息（设计 D8） |
| AD10 | **admin 成本 RPC 默认全舰队，而非组织范围。** `GetCostAnalyticsOverview` 与 `GetCostAnalytics` 的 admin 绑定跨所有组织聚合，带从 `X-Organization-Id` 读取的可选 `organization_id` 过滤。它们由 `tenancy.RoleGuard`（admin 角色，10036）门控。`GetCostAnalytics` 的 user 绑定硬性限定到调用者组织（会话活跃组织权威，忽略 `X-Organization-Id`） | 操作员需要跨组织舰队视图来管理平台支出；租户只需要自己的成本归因。这是对"admin 查询解析组织"模式的刻意偏离 — 舰队视图是 admin 表面的要点，RoleGuard 使其保持 admin 专属（设计 D1、D8） |

---

## 3. 组件视图

### 3.1 归属

| 层 / 组件 | 拥有 | 本功能的变更 |
| --- | --- | --- |
| **控制网关（`grpc-gateway`）** | 成本 RPC 的 HTTP/JSON 门面；领域守卫（功能 #17）已以 10038 拒绝错误领域会话；将 `X-Organization-Id` 作为 gRPC 元数据传递 | 两个 HTTP 成本 RPC 的新绑定（第 5 节）；领域守卫无变更 |
| **`billing` 模块（`services/billing`）** | 对 `charge_records` 的只读成本聚合、两个 RPC、维度验证、范围验证、桶构建器、`data_through` 水位 | 现有 `BillingService` 上的新 RPC（AD1、AD4） |
| **`metering` 模块** | `usage_records`（每 token 成本的 token 总计） | 只读：billing 模块通过共享数据库读取 `usage_records`（AD1）；无代码变更 |
| **`model` 模块** | 模型元数据（`model_id` → `model_name`） | 只读：billing 模块进程内解析 `model_name`（AD3） |
| **`auth` 模块** | API 密钥身份（`api_key_id` → `api_key_name`）、会话领域、会话活跃组织 | 只读：billing 模块进程内解析 `api_key_name` 以及 user 绑定的会话活跃组织（AD9、AD10） |
| **`tenancy` 模块** | 组织、成员、角色、RoleGuard | 只读：`RoleGuard` 按调用者角色门控 admin 成本 RPC（10036） |
| **PostgreSQL** | `charge_records`、`usage_records`（现有） | 无新表；现有索引服务范围扫描（第 4 节） |
| **控制台** | admin 成本页面与 end-user 成本页面 | 两个表面上的四个新页面（第 6 节） |

### 3.2 运行时组件视图

```mermaid
flowchart TD
    subgraph browsers["浏览器"]
        UC["End-user 控制台<br/>/cost /cost/:dimension/:value<br/>key go-taas.user.session-token"]
        AC["Admin 控制台<br/>/admin/cost /admin/cost/:dimension/:value<br/>key go-taas.admin.session-token"]
    end

    subgraph gateway["taas-server HTTP（单端口）"]
        GUARD["RealmGuard<br/>路径前缀到领域<br/>不匹配时 10038 或 10027"]
        MUX["grpc-gateway mux<br/>google.api.http 绑定"]
        ERR["gatewayErrorHandler<br/>code 消息信封"]
    end

    subgraph services["gRPC 服务"]
        BIL["billing<br/>对 charge_records 的成本聚合"]
        MET["metering<br/>usage_records（只读）"]
        MOD["model<br/>model_name"]
        AUTH["auth<br/>api_key_name 会话"]
        TENA["tenancy<br/>组织 成员 RoleGuard"]
    end

    subgraph stores["状态"]
        REDIS[("Redis<br/>taas:auth:session:*<br/>+ 领域")]
        PG[("PostgreSQL<br/>charge_records usage_records")]
    end

    UC -->|"/api/v1/cost/*"| GUARD
    AC -->|"/api/v1/admin/cost/*"| GUARD
    GUARD --> MUX
    GUARD -.->|"领域查找"| REDIS
    MUX --> BIL
    MUX --> AUTH
    MUX --> TENA
    BIL --> PG
    BIL -.->|"只读 usage_records"| PG
    BIL -.->|"进程内 model_name"| MOD
    BIL -.->|"进程内 api_key_name"| AUTH
    AUTH --> REDIS
    AUTH --> PG
    TENA --> PG
    classDef edge fill:#007F86,stroke:#00565C,stroke-width:2px,color:#FFFFFF
    classDef svc fill:#CFE9EA,stroke:#4E9CA0,stroke-width:1.5px,color:#0B383D
    classDef store fill:#E7ECEE,stroke:#9AACB1,stroke-width:1.5px,color:#23343A
    class UC,AC consumer
    class GUARD edge
    class BIL,MET,MOD,AUTH,TENA svc
    class REDIS,PG store
```

### 3.3 请求身份链

成本 RPC 复用已建立的身份链（console-surface-separation §3.3），admin 舰队视图有一处刻意差异（AD10）：

1. `RealmGuard`（HTTP，`pkg/server/realm.go`）— 路径前缀决定期望领域。`/api/v1/admin/cost/*` 期望 `admin`；`/api/v1/cost/*` 期望 `user`。无 `Authorization` 头：放行（过渡，功能 #17 的 AD4）。有头：从 Redis 解析会话领域；不匹配 → 10038，未知/过期/无领域 → 10027。
2. grpc-gateway mux — 按注解路径路由并转发 `authorization` 与 `x-organization-id`。
3. 服务处理器 — **admin 绑定**：舰队视图默认跨组织；`organization_id` 是从 `X-Organization-Id`（或调用者想限定到自己组织时的会话活跃组织）读取的可选过滤。**user 绑定**：`SessionActiveOrg` 使会话活跃组织权威并忽略 `X-Organization-Id`；无会话时，`resolveOrganizationID` 读取过渡头，缺失或为空时返回 10001。
4. `tenancy.RoleGuard` — 按调用者在已解析组织上下文中的角色门控 admin 成本 RPC（10036）。end-user 成本 RPC 硬性限定到调用者组织，无需角色检查。

---

## 4. 数据模型

### 4.1 无新表

成本功能是对现有 `charge_records` 与 `usage_records` 表的纯只读聚合（AD1，设计 D10）。无新表、无新 MQ 主题、无新运行器、任何路径上无写入。消费的列：

| 表 | 列 | 用途 |
| --- | --- | --- |
| `charge_records` | `organization_id` | 组织限定（user 绑定）与可选 admin 组织过滤 |
| | `api_key_id` | `api_key` 维度 |
| | `model_id` | `model` 维度 |
| | `period_start` | 桶分配与 `data_through` 水位 |
| | `amount` | 成本（整数分） |
| `usage_records` | `api_key_id`、`period_start`、token 列 | 每 token 成本的 token 总计 |

### 4.2 索引

现有 `charge_records` 索引 `idx_charge_records_org_period (organization_id, period_start)` 服务组织范围范围扫描。admin 舰队视图（AD10）跨所有组织扫描，因此 `idx_charge_records_group_period (api_key_id, model_id, accelerator_type, period_start)` 唯一索引服务全舰队范围扫描（它是分组列上的前导列索引）。**无需新索引** — 现有索引覆盖成本范围扫描。

### 4.3 迁移说明

- 无新表、无新索引、无数据迁移、无 init-SQL 升级路径。该功能是对现有表的纯只读聚合（AD1，设计 D10）。

---

## 5. API 设计

所有成本 RPC 属于现有 **`taas.billing.v1.BillingService`**（`proto/taas/billing/v1/billing.proto`），通过控制网关以 HTTP 服务。`GetCostAnalyticsOverview` 与 `GetCostAnalytics` 均双绑定（admin + user）。表面由请求路径派生（第 3.3 节）。

| RPC | HTTP（admin） | HTTP（user） | 状态 | 用途 |
| --- | --- | --- | --- | --- |
| `GetCostAnalyticsOverview` | `GET /api/v1/admin/cost` | `GET /api/v1/cost` | **新** | 卡片 + 维度分解 + 成本趋势 + 每 token 成本（admin：舰队；user：租户范围） |
| `GetCostAnalytics` | `GET /api/v1/admin/cost/{dimension}/{value}` | `GET /api/v1/cost/{dimension}/{value}` | **新** | 单维度值卡片 + 成本趋势（admin：任意维度值；user：租户范围） |

### 5.1 Proto 契约

```protobuf
syntax = "proto3";

package taas.billing.v1;

import "google/api/annotations.proto";
import "taas/common/v1/common.proto";

// （对现有 BillingService 的增量。）

// GetCostAnalyticsOverview 返回全舰队成本分析：摘要卡片、维度分解、成本
// 趋势与每 token 成本。Admin 表面 API：在 /api/v1/admin 下服务。
rpc GetCostAnalyticsOverview(GetCostAnalyticsOverviewRequest) returns (GetCostAnalyticsOverviewResponse) {
  option (google.api.http) = {get: "/api/v1/admin/cost"};
}

// GetCostAnalytics 返回单维度值成本分析：摘要卡片与成本趋势。admin 绑定
// 覆盖任意维度值；user 绑定是租户范围。
rpc GetCostAnalytics(GetCostAnalyticsRequest) returns (GetCostAnalyticsResponse) {
  option (google.api.http) = {
    get: "/api/v1/admin/cost/{dimension}/{value}"
    additional_bindings: {get: "/api/v1/cost/{dimension}/{value}"}
  };
}

message GetCostAnalyticsOverviewRequest {
  // organization_id 是可选舰队过滤。在 admin 表面从 X-Organization-Id
  // （或会话活跃组织）读取；缺失表示全舰队（AD10）。
  string organization_id = 1;
  // since/until 界定范围，unix 秒。默认：until = now，since = until - 24h。
  // 范围 > 92 天被拒绝（10404）。
  int64 since = 2;
  int64 until = 3;
  // dimension 是 organization / model / api_key 之一（默认 organization）。
  // 不支持的值返回 11301（AD2）。
  string dimension = 4;
}

message GetCostAnalyticsOverviewResponse {
  taas.common.v1.Response response = 1;
  CostAnalyticsCard cards = 2;
  repeated CostBreakdownRow breakdown = 3;
  repeated CostSeriesPoint series = 4;
}

message GetCostAnalyticsRequest {
  // organization_id 从会话活跃组织（user 绑定）或 X-Organization-Id
  // （admin 绑定）派生；该字段为 gRPC 直连调用者存在。
  string organization_id = 1;
  // dimension 是 organization / model / api_key（admin）或 model / api_key
  // （user）之一。不支持的值返回 11301（AD2）。
  string dimension = 2;
  // value 是维度值（如 model_id 或 api_key_id）。
  string value = 3;
  // since/until 界定范围，unix 秒。默认：until = now，since = until - 24h。
  // 范围 > 92 天被拒绝（10404）。
  int64 since = 4;
  int64 until = 5;
}

message GetCostAnalyticsResponse {
  taas.common.v1.Response response = 1;
  CostAnalyticsCard cards = 2;
  repeated CostSeriesPoint series = 3;
}

// CostAnalyticsCard 是范围的头条摘要。cost_per_token 与
// top_dimension_share_pct 在客户端派生；线上仅携带整数分与整数 token。
message CostAnalyticsCard {
  int64 total_cost_cents = 1;
  int64 total_tokens = 2;
  // top_dimension_value 是按成本最高的维度值。
  string top_dimension_value = 3;
  // top_dimension_share_pct 在客户端派生为 top 值成本 / 总成本。
  int64 top_dimension_share_pct = 4;
  // data_through 是 charge records 覆盖的最后一个完整桶（AD8）；其后的
  // 桶待处理。
  int64 data_through = 5;
}

// CostBreakdownRow 是分解中的一个维度值聚合。
message CostBreakdownRow {
  string dimension_value = 1;
  string dimension_name = 2;
  int64 total_cost_cents = 3;
  int64 total_tokens = 4;
  // cost_per_token 与 share_pct 在客户端派生。
  int64 cost_per_token = 5;
  int64 share_pct = 6;
}

// CostSeriesPoint 是序列的一个时间桶。
message CostSeriesPoint {
  // bucket 是桶开始，unix 秒（范围 <= 7 天为小时，否则为天，AD6）。
  int64 bucket = 1;
  int64 total_cost_cents = 2;
  int64 total_tokens = 3;
  // cost_per_token 在客户端派生为成本 / token。
  int64 cost_per_token = 4;
}
```

### 5.2 契约说明（为 Developer 智能体固定）

1. `GetCostAnalyticsOverview` 验证 `since`/`until`（int64 unix 秒；默认 `until = now`，`since = until − 24h`）；`since > until` 或范围 > 92 天返回 10404（AD6）。`dimension` 是 `organization` / `model` / `api_key` 之一（默认 `organization`）；不支持的值返回 11301 `CodeCostDimensionInvalid`（AD2）。`organization_id` 是可选过滤。
2. `GetCostAnalytics` 验证同一范围契约；不支持的 `dimension` 返回 11301，未知维度值返回 11302 `CodeCostDimensionValueNotFound`（AD2）。在 user 前缀上限定到调用者组织（AD9），且不暴露服务 ID 或操作员内部信息。
3. 桶在范围 ≤ 7 天为小时，否则为天（AD6）；每个桶携带 `bucket`、`total_cost_cents`、`total_tokens`、`cost_per_token`。`cost_per_token` 与 `share_pct` 在客户端派生；线上仅携带整数分与整数 token（AD3）。
4. 每个响应携带 `data_through`（charge records 覆盖的最后一个完整桶）用于新鲜度标记（AD8）。
5. 聚合读取 `charge_records`（功能 #5）— `organization_id`、`api_key_id`、`model_id`、`period_start`、`amount` — 以及 `usage_records`（功能 #4）获取 token 总计；它不写入任何内容（AD1，设计 D10）。
6. 线上约定不变：列表适用点分分页，成功 HTTP 200，业务错误为 `{"code": <int>, "message": "..."}`，int64 字段序列化为 JSON 字符串。

### 5.3 错误码

错误码（cost 块 11301–11399，`pkg/errors/codes.go`）：

| 条件 | 码 | 常量 | 说明 |
| --- | --- | --- | --- |
| 不支持的 `dimension` | 11301 | `CodeCostDimensionInvalid` | **新**（AD2） |
| 未知维度值 | 11302 | `CodeCostDimensionValueNotFound` | **新**（AD2） |
| 格式错误或超长范围 | 10404 | `CodeMeteringRangeInvalid` | 复用（AD6）— metering 范围契约 |
| 数据库 / 基础设施失败 | 500 | `CodeInternal` | 经错误归一化 |

---

## 6. 前端架构

### 6.1 页面 → 路由 → API 前缀表

| 页面 / 组件 | 表面 | Web 路由 | API 前缀 | 认证守卫 |
| --- | --- | --- | --- | --- |
| **成本分析页面** | admin | `/admin/cost` | `/api/v1/admin/cost` | admin 会话；RoleGuard（admin 角色） |
| **成本详情页面** | admin | `/admin/cost/:dimension/:value` | `/api/v1/admin/cost/{dimension}/{value}` | admin 会话；RoleGuard（admin 角色） |
| **成本分析页面** | end-user | `/cost` | `/api/v1/cost` | user 会话；硬性限定到调用者组织 |
| **成本详情页面** | end-user | `/cost/:dimension/:value` | `/api/v1/cost/{dimension}/{value}` | user 会话；硬性限定到调用者组织 |

> admin 成本页面仅调用 `/api/v1/admin/cost/*`；end-user 成本页面仅调用 `/api/v1/cost/*`。两个表面绝不共享会话 token（功能 #17）。

### 6.2 导航位置

- **Admin 控制台**：admin 导航中新增 **Cost** 项（`/admin/cost`，testid `nav-cost`），位于操作组，与 Usage、Usage Keys 和 Observability 并列。
- **End-user 控制台**：user 导航中新增 **Cost** 项（`/cost`，testid `user-nav-cost`），与 Usage 和 Usage Keys 并列。

### 6.3 复用共享组件与状态

- **API 客户端**（`web/src/api.ts`）：领域范围客户端（`createApi(realm)`）已发送 `Authorization: Bearer <realm>.session-token` 与 `X-Organization-Id`（仅当领域 token 键为空时）。成本页面原样复用；不新增客户端。
- **内联 SVG 图表**：新的共享 `CostChart.tsx` 组件（每桶一个条/线，带指标切换器），遵循 usage-dashboard `UsageChart.tsx` 模式（AD7）。指标切换器在客户端切换 cost / tokens / cost-per-token，无需重新获取。
- **时间范围过滤**：预设控件（24 h / 7 d / 30 d / 自定义日期时间选择器）与 Usage、Request Logs 和 Observability 页面共享。
- **摘要卡片**：新的共享 `CostCards.tsx` 组件渲染卡片行，带 "data through <time>" 新鲜度说明（AD8）。
- **维度分解**：新的共享 `CostBreakdownTable.tsx` 组件渲染带份额条的维度值。
- **状态 / 新鲜度徽章**：`data_through` 之后的待处理/部分标记复用 usage-dashboard 待处理徽章样式。
- **空状态 / 数据新鲜度说明**：复用 Request Logs 页面模式（说明指标在摄取窗口内出现）。

### 6.4 各表面认证守卫

- **Admin 成本页面**（`/admin/cost`、`/admin/cost/:dimension/:value`）：在 `AdminShell` 内渲染，当 `go-taas.admin.session-token` 非空时运行 admin 会话守卫；缺失/过期会话重定向到 `/admin/login?reason=expired`；错误领域会话被网关以 10038 拒绝。页面 API 调用发往 `/api/v1/admin/cost/*`。无所需角色的会话收到 10036，页面显示标准权限拒绝状态（功能 #17）。
- **End-user 成本页面**（`/cost`、`/cost/:dimension/:value`）：在 `UserShell` 内渲染，当 `go-taas.user.session-token` 非空时运行 user 会话守卫；缺失/过期会话重定向到 `/login?reason=expired`。页面 API 调用发往 `/api/v1/cost/*`。页面不暴露服务 ID 或操作员内部信息（AD9）。
- **未认证访客**：任一页面的未认证访客由 shell 守卫重定向到正确的登录页（`/admin/login` 对 `/login`）。

### 6.5 控制台契约（为 Developer 智能体固定）

**成本分析页面**（`/admin/cost`）：页面头部（"Cost"，副标题 "Cost attribution by dimension over time"）带 **Refresh** 动作（`cost-refresh`）。下方：**过滤栏** — **时间范围**控件（`cost-filter-range`，预设 24 h / 7 d / 30 d / 自定义）、**维度**控件（`cost-filter-dimension`，下拉：Organization / Model / API key；默认 Organization）与**模型**过滤（`cost-filter-model`，下拉，"All models" 默认，维度非 Model 时显示）；一行**摘要卡片**（`cost-cards`）：Total cost、Total tokens、Cost per token、Top dimension（带份额的 top 维度值），各带 "data through <time>" 说明；内联 SVG **成本趋势图**（`cost-chart`）带指标切换器（`cost-metric-toggle`）；以及**维度分解**表（`cost-breakdown`，`cost-breakdown-row-{dimension_value}`）列：Dimension value（链接到钻取）、Cost、Tokens、Cost per token、Share。可按 Cost、Tokens、Cost per token 与 Share 排序；可按 Model 下拉过滤（适用时）；分页。空状态："No cost data in this range."，提示扩大范围。错误状态：错误横幅带 Retry 按钮与 "Showing stale data" 横幅。权限拒绝：标准状态，带返回 admin 首页的链接。

**成本详情页面**（`/admin/cost/:dimension/:value`）：返回概览的链接、带维度名称与值的头部、过滤栏（时间范围）、摘要卡片与带指标切换器的内联 SVG 成本趋势图。空文案："No cost data for this dimension value in this range." 未找到状态（11302）显示标准未找到状态，带返回概览的链接。

**End-user 成本分析页面**（`/cost`）：与 admin 页面相同，限定到租户自己的用量。**维度**控件仅提供 Model / API key（默认 Model）。空文案 "No cost data in this range." 与租户自己的权限拒绝文案（10005 组织消失 / 10017 组织禁用，来自功能 #17 §8.2）。

**End-user 成本详情页面**（`/cost/:dimension/:value`）：与 admin 详情相同，限定到租户自己对维度值的用量。未知维度值（11302）的未找到文案与租户自己的权限拒绝文案（10005/10017）。

---

## 7. 时序流

### 7.1 Admin 全舰队成本概览加载

```mermaid
sequenceDiagram
    autonumber
    actor Op as 平台操作员
    participant UI as Admin 控制台
    participant CGW as 控制网关
    participant BIL as billing 模块
    participant CR as charge_records + usage_records

    Op->>UI: 打开 /admin/cost
    UI->>CGW: GET /api/v1/admin/cost
    CGW->>BIL: GetCostAnalyticsOverview
    BIL->>CR: 按桶与维度聚合 charge_records，读取 usage_records 获取 token
    CR-->>BIL: 桶与维度行
    BIL-->>UI: 卡片 + 分解 + 序列
    UI-->>Op: 摘要卡片 + 成本趋势图 + 维度分解
    Op->>UI: 选择一个维度值并点击 View
    UI->>CGW: GET /api/v1/admin/cost/{dimension}/{value}
    CGW->>BIL: GetCostAnalytics
    BIL->>CR: 按桶聚合该维度值的 charge_records
    CR-->>BIL: 该维度值的桶
    BIL-->>UI: 卡片 + 序列
    UI-->>Op: 卡片 + 成本趋势图
```

### 7.2 End-user 租户范围成本视图加载

```mermaid
sequenceDiagram
    autonumber
    actor T as 租户开发者 / 智能体
    participant UI as End-user 控制台
    participant CGW as 控制网关
    participant BIL as billing 模块
    participant CR as charge_records + usage_records

    T->>UI: 打开 /cost/:dimension/:value
    UI->>CGW: GET /api/v1/cost/{dimension}/{value}
    CGW->>BIL: GetCostAnalytics（user 绑定）
    BIL->>BIL: 从会话活跃组织解析调用者组织
    BIL->>CR: 按桶聚合调用者组织的该维度值 charge_records
    CR-->>BIL: 该维度值的桶（组织范围）
    BIL-->>UI: 卡片 + 序列（租户范围，无内部信息）
    UI-->>T: 卡片 + 成本趋势图
```

### 7.3 聚合查询

```mermaid
sequenceDiagram
    autonumber
    participant BIL as billing 模块
    participant REPO as billing 仓库
    participant DB as PostgreSQL

    BIL->>REPO: AggregateCost(ctx, orgFilter, dimension, value, since, until, bucketSize)
    REPO->>DB: SELECT bucket, sum(amount)*100, sum(tokens)<br/>FROM charge_records LEFT JOIN usage_records<br/>WHERE <org/dimension/range> GROUP BY bucket, dimension_value
    DB-->>REPO: 桶与维度行
    REPO->>DB: SELECT max(period_start) FROM charge_records WHERE <org/range>
    DB-->>REPO: data_through 水位
    REPO-->>BIL: 桶 + 维度行 + 水位
    BIL-->>BIL: 构建卡片、分解与序列
```

---

## 8. 错误处理

所有错误都是统一信封中的 `pkg/errors` 业务码（第 5.3 节）。成本模块只读，因此无运行器侧失败、无写入可失败。数据库失败归一化为 500 `CodeInternal`。

控制台按 body `code` 分支（`web/src/api.ts` 已如此），绝不按 HTTP 状态。每个新码渲染特定内联消息：11301 "invalid dimension"、11302 "dimension value not found"、10404 "invalid range"（metering 范围契约）。admin 页面将 10036 映射到标准权限拒绝状态；end-user 页面将 10005/10017 映射到租户的权限拒绝文案（功能 #17 §8.2）。

---

## 9. 配置新增

无。成本功能是对现有表的纯只读聚合，复用 metering 范围契约（10404）与现有 `billing` 模块范围上限。不新增配置键、运行器或 MQ 主题（AD1，设计 D10）。

---

## 10. 安全考量

- **表面分离**：admin 成本页面仅调用 `/api/v1/admin/cost/*`；end-user 成本页面仅调用 `/api/v1/cost/*`。领域守卫在任何处理器运行前以 10038 拒绝错误领域会话（功能 #17）。
- **admin 舰队视图按角色门控**：admin 成本 RPC 默认全舰队（AD10），由 `tenancy.RoleGuard` 门控 — 只有具备所需 admin 角色的调用者能看跨组织舰队视图；不可访问组织返回 10036。
- **end-user 硬性限定**：`GetCostAnalytics` 的 user 绑定硬性限定到调用者组织（会话活跃组织权威，忽略 `X-Organization-Id`）；调用者绝不可能看到另一租户的成本。
- **掩码投影**：end-user 表面不暴露服务 ID、副本数或其他操作员编排内部信息（AD9）。admin 表面是操作员范围。
- **构造上只读**：成本模块仅发出 `SELECT`；任何路径上无写入（AD1，设计 D10）。无需新审计事件 — 底层 charge-record 写入已被审计（功能 #15）。

---

## 11. 上线 / 升级说明

- **无模式变更**：该功能仅读取现有表；单独部署 `taas-server`。无新表、无新索引、无数据迁移、无 init-SQL 升级路径。
- **proto 变更是增量的**：现有 `BillingService` 上两个新 RPC；无现有 RPC 或消息变更。网关 mux 增加新绑定；领域守卫不变。
- **控制台**：四个新页面加入现有 bundle；admin 导航增加 Cost，end-user 导航增加 Cost。无现有路由变更。
- **向后兼容**：过渡（无会话、`X-Organization-Id`）路径为 CLI、FVT 与 e2e 套件保留；领域守卫放行无 `Authorization` 头的请求（功能 #17 AD4）。
- **数据存在前为空**：成本 RPC 在 charge records 存在前返回空卡片/分解/序列；页面渲染空状态，提示扩大范围。

---

## 12. 验收标准追溯

| # | 标准 | 覆盖位置 |
| --- | --- | --- |
| AC1 | `GetCostAnalyticsOverview` 带有效范围返回摘要卡片、维度分解、成本趋势与每 token 成本；范围 > 92 天或 `since > until` 返回 10404 | §5.1、§5.2、§5.3 |
| AC2 | `GetCostAnalyticsOverview` 带不支持的 `dimension` 返回 11301 | §5.1、§5.2、§5.3 |
| AC3 | `GetCostAnalytics`（admin）返回单维度值卡片与成本趋势；不支持的 `dimension` 返回 11301，未知维度值返回 11302 | §5.1、§5.2、§5.3 |
| AC4 | `GetCostAnalytics`（user）仅返回调用者组织的成本，无服务 ID 或操作员内部信息 | §3.3、§6.4、§10 |
| AC5 | 桶在范围 ≤ 7 天为小时，范围 > 7 天为天；每个响应携带 `data_through` | §5.2、§7.3 |
| AC6 | `/admin/cost` 页面从首次成功加载渲染过滤栏、摘要卡片、成本趋势图与维度分解，带最后更新时间戳 | §6.5 |
| AC7 | 更改时间范围、维度或模型过滤会重新获取并重新渲染卡片、图表与分解；指标切换器切换图表指标 | §6.3、§6.5 |
| AC8 | 无数据匹配时渲染空状态（"No cost data in this range."）；失败加载保留最后好数据，带 "Showing stale data" 横幅与 Retry 动作 | §6.5 |
| AC9 | `/admin/cost/:dimension/:value` 页面渲染维度值的卡片与成本趋势图；未知维度值显示未找到状态 | §6.5 |
| AC10 | `/cost` 与 `/cost/:dimension/:value` 页面渲染租户自己的卡片、分解与趋势，无服务 ID 或操作员内部信息可见 | §6.5、§10 |
| AC11 | admin 成本页面仅在 admin 表面可达：路由 `/admin/cost` 与 `/admin/cost/:dimension/:value`，每个 API 调用使用 `/api/v1/admin/cost/*` 前缀且无 `/api/v1/cost/*` 字符串 | §6.1、§6.4、§10 |
| AC12 | end-user 成本页面仅在 end-user 表面可达：路由 `/cost` 与 `/cost/:dimension/:value`，每个 API 调用使用 `/api/v1/cost/*` 前缀且无 `/api/v1/admin/*` 字符串 | §6.1、§6.4、§10 |
| AC13 | 无所需角色的会话在 admin 成本页面收到 10036，页面显示标准权限拒绝状态 | §3.3、§6.4、§10 |

---

## 13. 详细设计（逐层函数级职责）

### 13.1 Proto → 服务 → 仓库 → 控制器

| 层 | 文件 | 职责 |
| --- | --- | --- |
| `proto/taas/billing/v1` | `billing.proto` | 增量：`GetCostAnalyticsOverview`/`GetCostAnalytics` RPC + `GetCostAnalyticsOverviewRequest/Response`、`GetCostAnalyticsRequest/Response`、`CostAnalyticsCard`、`CostBreakdownRow`、`CostSeriesPoint` 消息（第 5.1 节）。经 `buf generate` 重新生成 `billing.pb.go`/`billing_grpc.pb.go`/`billing.pb.gw.go` |
| `services/billing` | `cost_analytics_model.go` | 聚合行结构体（`CostBucketRow`、`CostBreakdownRow`）与 `bucketSizeForRange` 辅助函数（范围 ≤ 7 天为小时，否则为天，AD6） |
| | `cost_analytics_repository.go` | `AggregateCost(ctx, orgFilter, dimension, value, since, until, bucketSize)` — 对 `charge_records` LEFT JOIN `usage_records` 的聚合（卡片 + 分解行 + 序列）（AD3）；`DataThrough(ctx, orgFilter, since, until)` — `max(period_start)` 水位（AD8） |
| | `service.go` | 新 RPC `GetCostAnalyticsOverview`、`GetCostAnalytics`；维度验证（11301，AD2）；维度值未找到（11302，AD2）；范围验证（10404，AD6）；admin 舰队范围对 user 硬性限定解析（AD10）；`SessionActiveOrg`/`resolveOrganizationID` 接缝；admin 组织限定的 `RoleGuard` 接缝；进程内 `model_name`/`api_key_name` 解析（AD3） |
| `services/metering` | `service.go` | **无变更** — 其 `usage_records` 由 billing 模块通过共享数据库只读读取（AD1） |
| `services/model` | `service.go` | 只读：为 billing 模块暴露 `ModelName(ctx, modelID) (string, error)` 接缝（或复用 `GetModel`）以进程内解析 `model_name`（AD3） |
| `services/auth` | `service.go` | 只读：为 billing 模块暴露 `APIKeyName(ctx, apiKeyID) (string, error)` 接缝以进程内解析 `api_key_name`（AD3） |
| `pkg/errors` | `codes.go`/`messages.go` | `CodeCostDimensionInvalid`（11301）+ `CodeCostDimensionValueNotFound`（11302）常量 + 规范消息 "invalid cost dimension" / "cost dimension value not found"（AD2） |
| `apps/taas-server` | `main.go` | 无变更 — 成本 RPC 搭乘现有 billing 服务注册；将 `model`/`auth` 名称解析接缝与 `tenancy` RoleGuard 接入 billing 服务 |
| `web/src` | `pages/CostAnalyticsPage.tsx`、`pages/CostDetailPage.tsx`、`pages/user/UserCostAnalyticsPage.tsx`、`pages/user/UserCostDetailPage.tsx`、`components/CostChart.tsx`、`components/CostCards.tsx`、`components/CostBreakdownTable.tsx`、`App.tsx`、`api.ts`、`shells/AdminShell.tsx`、`shells/UserShell.tsx` | 路由 `/admin/cost`、`/admin/cost/:dimension/:value`、`/cost`、`/cost/:dimension/:value`；`GetCostAnalyticsOverview`/`GetCostAnalytics` API 类型与调用；导航项（第 6.5 节） |
| `test` | `fvt/cost_analytics_fvt_test.go`、`e2e/tests/costAnalytics.js` | 第 14 节 |

### 13.2 哪个 React 页面/模块实现每个屏幕

| 屏幕 | React 模块 | 路由 | API 调用 |
| --- | --- | --- | --- |
| 成本分析页面（admin） | `web/src/pages/CostAnalyticsPage.tsx` | `/admin/cost` | `GetCostAnalyticsOverview` |
| 成本详情页面（admin） | `web/src/pages/CostDetailPage.tsx` | `/admin/cost/:dimension/:value` | `GetCostAnalytics` |
| 成本分析页面（end-user） | `web/src/pages/user/UserCostAnalyticsPage.tsx` | `/cost` | `GetCostAnalyticsOverview` |
| 成本详情页面（end-user） | `web/src/pages/user/UserCostDetailPage.tsx` | `/cost/:dimension/:value` | `GetCostAnalytics` |
| 内联 SVG 图表 | `web/src/components/CostChart.tsx`（共享） | （四个页面） | （客户端；指标切换器，无需重新获取） |
| 摘要卡片 | `web/src/components/CostCards.tsx`（共享） | （四个页面） | （客户端；渲染返回的卡片） |
| 维度分解 | `web/src/components/CostBreakdownTable.tsx`（共享） | （两个概览页面） | （客户端；渲染返回的分解） |

---

## 14. 测试策略

- **单元**（`services/billing`，sqlite 内存）：`cost_analytics_repository_test.go` — `AggregateCost` 为种子 `charge_records` 集返回正确卡片/分解行/序列（AC1、AC3），`DataThrough` 返回最后一个完整桶（AC5），桶大小在 7 天边界切换（AC5）。`service_test.go` — 范围验证对 `since > until` 与范围 > 92 天返回 10404（AC1）；不支持的 `dimension` 返回 11301（AC2、AC3）；未知维度值返回 11302（AC3）；user 绑定硬性限定到调用者组织且不暴露服务 ID（AC4）；admin 组织限定返回 10036（AC13）。新文件覆盖率 ≥ 80%。
- **FVT**（`test/fvt/cost_analytics_fvt_test.go`，billing FVT 模式：文件支持 sqlite + `MigrateSchemaForFVT` + `NewForFVT` + 带生产拦截器的 gRPC 服务器 + 带 `FVTHeaderMatcher` 的网关 mux）：跨组织/模型/密钥种子 `charge_records` 与 `usage_records` 行，然后断言 `GetCostAnalyticsOverview` 卡片/分解/序列（AC1）、11301 无效维度（AC2）、`GetCostAnalytics` admin 单维度值卡片/序列与 11301/11302（AC3）、user 绑定仅返回调用者组织的行且无内部信息（AC4）、小时对天桶与 `data_through`（AC5）、以及内联 10404（AC1）。
- **E2E**（`test/e2e/tests/costAnalytics.js`，`usageDashboard.js` 模式）：针对 compose 栈 — admin `/admin/cost` 页面从首次成功加载渲染 `cost-cards`、`cost-chart` 与 `cost-breakdown`（AC6）；更改时间范围、维度或模型过滤会重新获取且指标切换器切换图表指标（AC7）；空状态与陈旧数据横幅渲染（AC8）；admin 钻取渲染维度值的卡片与趋势及未找到状态（AC9）；end-user `/cost` 与 `/cost/:dimension/:value` 页面渲染租户自己的卡片/分解/趋势且无服务 ID（AC10）；每个页面仅调用自己的前缀且未认证访客被重定向到正确登录页（AC11/AC12）；无所需角色的会话收到 10036 并显示权限拒绝状态（AC13）。