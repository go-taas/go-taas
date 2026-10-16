# 错误分析 — 架构与详细设计

| 属性 | 内容 |
| --- | --- |
| 功能点 | 错误分析 — 随时间聚合错误码与错误率、top 错误原因、错误率趋势，以及按错误钻取（backlog 第 31 行） |
| 文档范围 | 功能 31 的架构与详细设计：`metering` 模块中的只读错误分析；`MeteringService` 上的 `GetErrorAnalysisOverview`（admin 舰队）与 `GetErrorAnalysis`（双绑定）RPC；admin 错误分析页面（`/admin/errors`、`/admin/errors/:errorCode`）与 end-user 错误分析页面（`/errors`、`/errors/:errorCode`）；以及错误处理、配置、安全、上线与逐层函数级设计 |
| 拥有模块 | `metering`（对 `request_logs` 的只读聚合以获取错误指标，以及两个 RPC）、`model`（只读：`model_name` 解析）、`auth`（只读：`api_key_name` 解析、会话领域、会话活跃组织）、`tenancy`（RoleGuard，只读）、`pkg/server` 网关（admin 前缀与 user 前缀绑定）、控制台 Web 应用（admin `ErrorAnalysisPage`/`ErrorDetailPage`，end-user `UserErrorAnalysisPage`/`UserErrorDetailPage`） |
| 相关文档 | [需求分析与 UI/UX 设计](../design/error-analysis.md) · [架构设计](../design/architecture.md) §2.5（`metering`）· [请求日志与 API 游乐场](./request-logs-playground.md)（`request_logs` 表及其本功能聚合的 `status`/`error` 字段）· [模型可观测性仪表盘](./model-observability.md)（对 `request_logs` 的同类只读聚合及其错误率指标）· [请求追踪与延迟分解](./request-tracing.md)（本功能以错误聚合补充的同类按请求钻取）· [控制台表面分离](./console-surface-separation.md)（本功能跨越的两个表面、`UserShell`/`AdminShell` 约定、掩码投影规则） |
| 状态 | 架构完成，已移交给 Developer 智能体 |

---

## 1. 概述与目标

go-taas 将每个推理请求的元数据 — 延迟、状态、错误与 token 计数 — 记录在 `request_logs`（功能 #12）中，模型可观测性仪表盘（功能 #24）按模型聚合错误率。控制台仍无法回答的是操作员与租户的问题：*什么在失败，为什么？* 可观测性仪表盘将错误率作为指标展示，但不按错误码或原因分解错误；请求日志（功能 #12）展示单个错误行但无聚合；请求追踪（功能 #27）钻取单个请求但不聚合错误。没有表面随时间聚合错误码与错误率、对 top 错误原因排名、展示错误率趋势并钻取单个错误。

本功能新增**错误分析**：随时间聚合错误码与错误率、top 错误原因、错误率趋势，以及按错误钻取。它是**只读**聚合层 — 推理、计量或计费管线均无变化。它是 Phase 4 生产化路线图条目中最小且有独立价值的增量：它将"错误很高"变成"错误率 5%，top 原因是 `rate_limit_exceeded`，占错误的 60%，且在上升"。

**目标**：`GetErrorAnalysisOverview` RPC（admin 舰队：卡片 + top 原因排名 + 错误率趋势）与 `GetErrorAnalysis` RPC（admin 按错误钻取与 end-user 按错误视图：单错误码卡片 + 趋势），两者都在 `MeteringService` 上；admin 错误分析页面（`/admin/errors`）与按错误钻取（`/admin/errors/:errorCode`），以及 end-user 错误分析页面（`/errors`）与钻取（`/errors/:errorCode`）；新错误码 11501 `CodeErrorCauseNotFound`；页面 → 路由 → API 前缀表，含精确前缀；各页面交互状态，包括空、错误与权限拒绝；以及可在 compose 栈上以 Nightwatch 测试的编号验收标准。

**非目标**（延后，设计 §8）：实时流式指标（请求日志节奏不变）；异常检测或阈值告警（功能 #26 在控制台内消费可观测性事件 — 此处不在范围内）；保存的自定义视图或仪表盘；模糊指纹分组（D2 — 按精确 `error` 码分组）；向租户暴露服务 ID、副本数或其他操作员编排内部信息（D7）；对推理、计量或计费管线的任何更改（只读功能）；新审计事件（D9）。

### 1.1 本文档的阅读顺序

第 2 节记录架构决策（AD1–AD9，对应设计的 D1–D9）。第 3–5 节是组件视图、数据模型与 API 设计。第 6 节是前端架构（两个控制台的页面 → 路由 → API 前缀表，各表面的认证守卫）。第 7–8 节是时序流与错误处理。第 9–11 节是配置、安全与上线。第 12–14 节是验收标准追溯、函数级详细设计与有序实现任务清单。

---

## 2. 架构决策

| # | 决策 | 理由 |
| --- | --- | --- |
| AD1 | **错误分析 RPC 位于 `MeteringService`** — 拥有 `request_logs`（功能 #12）的模块 | 错误指标从 `request_logs` 派生；metering 拥有该表及其查询表面，因此聚合属于那里（设计 D3） |
| AD2 | **错误分析错误块为 11501–11599，即 status 114xx 之后的下一个空闲块。** status 模块拥有 11401（功能 #30）。设计"status 块（114xx）之后的新块"的意图通过取 114xx 之后的下一个空闲块来实现 | 每个模块有自己的错误块（`pkg/errors/codes.go`）；status 占用 114xx，因此 error-analysis 占用 115xx。这是与现有架构最一致的解读（设计 D8，已确认） |
| AD3 | **错误从 `request_logs` 派生**（它们携带每个请求的 `status` 与 `error`，功能 #12），在服务端按错误码聚合。指标族为**错误计数**（`status = error` 行数）、**错误率**（错误请求 ÷ 总请求）与**top 原因**（按计数排名的 `error` 码） | `request_logs` 已捕获所需一切，并在同一幂等处理器中随凭证一起写入（功能 #12）；服务端聚合保持负载小、客户端依赖轻（设计 D2） |
| AD4 | **每个表面形状一个 RPC**，而非扩展 `ListRequestLogs`：`GetErrorAnalysisOverview`（admin 舰队：卡片 + top 原因排名 + 错误率趋势）与 `GetErrorAnalysis`（admin 按错误钻取与 end-user 按错误视图：单错误码卡片 + 趋势）。end-user 表面复用 `GetErrorAnalysis`，限定到租户自己的错误 | 错误页面需要同时多个形状（头条卡片、top 原因排名、趋势）；把它们硬塞进 `ListRequestLogs` 会破坏其既定的行语义，而单独调用会重现 N+1 慢控制台。专用 RPC 对将错误关注点从原始日志表面分离（设计 D3） |
| AD5 | **时间桶随范围自适应**：范围 ≤ 7 天为小时桶，否则为天桶。范围上限为 **92 天**，验证复用 **10404**（metering 范围契约） | 短范围的小时粒度展示日内错误尖峰；长范围的天粒度保持负载小。92 天上限与 10404 复用使范围契约与每个 metering 查询统一（usage-dashboard AD7）（设计 D4） |
| AD6 | **图表以内联 SVG 渲染** — 每桶一个条（或线），带指标切换器（error count / error rate）— 无新图表依赖 | 控制台刻意依赖轻；小型可测试 SVG 组件匹配 usage-dashboard AD8 决策（设计 D5） |
| AD7 | **新鲜度是显式的**：每个响应携带 `data_through`（请求日志覆盖的最后一个完整桶），控制台在所选范围超出时显示"data through <time>"说明加待处理/部分标记 | 错误滞后混淆是记录到的陷阱；标记以零新管线工作保持新鲜度故事诚实（设计 D6） |
| AD8 | **end-user 表面是租户范围且掩码的**：user 前缀上的 `GetErrorAnalysis` 仅返回租户自己的错误，无服务 ID、无副本数、无其他租户数据 | 遵循功能 #17 的掩码投影规则与可观测性 AD4 模式：租户获得自己的错误聚合，而非操作员内部信息（设计 D7） |
| AD9 | **admin 错误分析 RPC 默认全舰队，而非组织范围。** `GetErrorAnalysisOverview` 与 `GetErrorAnalysis` 的 admin 绑定跨所有组织聚合，带从 `X-Organization-Id` 读取的可选 `organization_id` 过滤。它们由 `tenancy.RoleGuard`（admin 角色，10036）门控。`GetErrorAnalysis` 的 user 绑定硬性限定到调用者组织（会话活跃组织权威，忽略 `X-Organization-Id`） | 操作员需要跨组织舰队视图来发现平台级失败；租户只需要自己的错误聚合。这是对"admin 查询解析组织"模式的刻意偏离 — 舰队视图是 admin 表面的要点，RoleGuard 使其保持 admin 专属（设计 D1、D7） |

---

## 3. 组件视图

### 3.1 归属

| 层 / 组件 | 拥有 | 本功能的变更 |
| --- | --- | --- |
| **控制网关（`grpc-gateway`）** | 错误分析 RPC 的 HTTP/JSON 门面；领域守卫（功能 #17）已以 10038 拒绝错误领域会话；将 `X-Organization-Id` 作为 gRPC 元数据传递 | 两个 HTTP 错误分析 RPC 的新绑定（第 5 节）；领域守卫无变更 |
| **`metering` 模块（`services/metering`）** | 对 `request_logs` 的只读错误聚合、两个 RPC、范围验证、桶构建器、`data_through` 水位 | 现有 `MeteringService` 上的新 RPC（AD1、AD4） |
| **`model` 模块** | 模型元数据（`model_id` → `model_name`） | 只读：metering 模块进程内解析 `model_name`（AD3） |
| **`auth` 模块** | API 密钥身份（`api_key_id` → `api_key_name`）、会话领域、会话活跃组织 | 只读：metering 模块进程内解析 `api_key_name` 以及 user 绑定的会话活跃组织（AD8、AD9） |
| **`tenancy` 模块** | 组织、成员、角色、RoleGuard | 只读：`RoleGuard` 按调用者角色门控 admin 错误分析 RPC（10036） |
| **PostgreSQL** | `request_logs`（现有） | 无新表；现有索引服务范围扫描（第 4 节） |
| **控制台** | admin 错误分析页面与 end-user 错误分析页面 | 两个表面上的四个新页面（第 6 节） |

### 3.2 运行时组件视图

```mermaid
flowchart TD
    subgraph browsers["浏览器"]
        UC["End-user 控制台<br/>/errors /errors/:errorCode<br/>key go-taas.user.session-token"]
        AC["Admin 控制台<br/>/admin/errors /admin/errors/:errorCode<br/>key go-taas.admin.session-token"]
    end

    subgraph gateway["taas-server HTTP（单端口）"]
        GUARD["RealmGuard<br/>路径前缀到领域<br/>不匹配时 10038 或 10027"]
        MUX["grpc-gateway mux<br/>google.api.http 绑定"]
        ERR["gatewayErrorHandler<br/>code 消息信封"]
    end

    subgraph services["gRPC 服务"]
        MET["metering<br/>对 request_logs 的错误聚合"]
        MOD["model<br/>model_name"]
        AUTH["auth<br/>api_key_name 会话"]
        TENA["tenancy<br/>组织 成员 RoleGuard"]
    end

    subgraph stores["状态"]
        REDIS[("Redis<br/>taas:auth:session:*<br/>+ 领域")]
        PG[("PostgreSQL<br/>request_logs（现有）")]
    end

    UC -->|"/api/v1/errors/*"| GUARD
    AC -->|"/api/v1/admin/errors/*"| GUARD
    GUARD --> MUX
    GUARD -.->|"领域查找"| REDIS
    MUX --> MET
    MUX --> AUTH
    MUX --> TENA
    MET --> PG
    MET -.->|"进程内 model_name"| MOD
    MET -.->|"进程内 api_key_name"| AUTH
    AUTH --> REDIS
    AUTH --> PG
    TENA --> PG
    classDef edge fill:#007F86,stroke:#00565C,stroke-width:2px,color:#FFFFFF
    classDef svc fill:#CFE9EA,stroke:#4E9CA0,stroke-width:1.5px,color:#0B383D
    classDef store fill:#E7ECEE,stroke:#9AACB1,stroke-width:1.5px,color:#23343A
    class UC,AC consumer
    class GUARD edge
    class MET,MOD,AUTH,TENA svc
    class REDIS,PG store
```

### 3.3 请求身份链

错误分析 RPC 复用已建立的身份链（console-surface-separation §3.3），admin 舰队视图有一处刻意差异（AD9）：

1. `RealmGuard`（HTTP，`pkg/server/realm.go`）— 路径前缀决定期望领域。`/api/v1/admin/errors/*` 期望 `admin`；`/api/v1/errors/*` 期望 `user`。无 `Authorization` 头：放行（过渡，功能 #17 的 AD4）。有头：从 Redis 解析会话领域；不匹配 → 10038，未知/过期/无领域 → 10027。
2. grpc-gateway mux — 按注解路径路由并转发 `authorization` 与 `x-organization-id`。
3. 服务处理器 — **admin 绑定**：舰队视图默认跨组织；`organization_id` 是从 `X-Organization-Id`（或调用者想限定到自己组织时的会话活跃组织）读取的可选过滤。**user 绑定**：`SessionActiveOrg` 使会话活跃组织权威并忽略 `X-Organization-Id`；无会话时，`resolveOrganizationID` 读取过渡头，缺失或为空时返回 10001。
4. `tenancy.RoleGuard` — 按调用者在已解析组织上下文中的角色门控 admin 错误分析 RPC（10036）。end-user 错误分析 RPC 硬性限定到调用者组织，无需角色检查。

---

## 4. 数据模型

### 4.1 无新表

错误分析功能是对现有 `request_logs` 表的纯只读聚合（AD1，设计 D9）。无新表、无新 MQ 主题、无新运行器、任何路径上无写入。消费的列：

| 列 | 用途 |
| --- | --- |
| `organization_id` | 组织限定（user 绑定）与可选 admin 组织过滤 |
| `api_key_id` | 可选 API 密钥过滤 |
| `model_id` | 可选模型过滤 |
| `status` | 错误计数（status = `error`）与总请求计数 |
| `error` | 错误码分组（top 原因） |
| `created_at` | 桶分配与 `data_through` 水位 |

### 4.2 索引

现有 `request_logs` 索引 `idx_request_logs_org_created (organization_id, created_at)` 与 `idx_request_logs_key_created (api_key_id, created_at)` 服务组织范围与密钥范围的范围扫描。admin 舰队视图（AD9）跨所有组织扫描，因此可观测性功能（功能 #24）添加的 `idx_request_logs_created (created_at)` 索引服务全舰队范围扫描。**无需新索引** — 现有索引覆盖错误分析范围扫描。

### 4.3 迁移说明

- 无新表、无新索引、无数据迁移、无 init-SQL 升级路径。该功能是对现有表的纯只读聚合（AD1，设计 D9）。

---

## 5. API 设计

所有错误分析 RPC 属于现有 **`taas.metering.v1.MeteringService`**（`proto/taas/metering/v1/metering.proto`），通过控制网关以 HTTP 服务。`GetErrorAnalysisOverview` 与 `GetErrorAnalysis` 均双绑定（admin + user）。表面由请求路径派生（第 3.3 节）。

| RPC | HTTP（admin） | HTTP（user） | 状态 | 用途 |
| --- | --- | --- | --- | --- |
| `GetErrorAnalysisOverview` | `GET /api/v1/admin/errors` | `GET /api/v1/errors` | **新** | 卡片 + top 原因排名 + 错误率趋势（admin：舰队；user：租户范围） |
| `GetErrorAnalysis` | `GET /api/v1/admin/errors/{error_code}` | `GET /api/v1/errors/{error_code}` | **新** | 单错误码卡片 + 错误率趋势（admin：任意错误；user：租户范围） |

### 5.1 Proto 契约

```protobuf
syntax = "proto3";

package taas.metering.v1;

import "google/api/annotations.proto";
import "taas/common/v1/common.proto";

// （对现有 MeteringService 的增量。）

// GetErrorAnalysisOverview 返回全舰队错误分析：摘要卡片、top 原因排名与
// 错误率趋势。Admin 表面 API：在 /api/v1/admin 下服务。
rpc GetErrorAnalysisOverview(GetErrorAnalysisOverviewRequest) returns (GetErrorAnalysisOverviewResponse) {
  option (google.api.http) = {get: "/api/v1/admin/errors"};
}

// GetErrorAnalysis 返回单错误码分析：摘要卡片与错误率趋势。admin 绑定覆盖
// 任意错误；user 绑定是租户范围。
rpc GetErrorAnalysis(GetErrorAnalysisRequest) returns (GetErrorAnalysisResponse) {
  option (google.api.http) = {
    get: "/api/v1/admin/errors/{error_code}"
    additional_bindings: {get: "/api/v1/errors/{error_code}"}
  };
}

message GetErrorAnalysisOverviewRequest {
  // organization_id 是可选舰队过滤。在 admin 表面从 X-Organization-Id
  // （或会话活跃组织）读取；缺失表示全舰队（AD9）。
  string organization_id = 1;
  // since/until 界定范围，unix 秒。默认：until = now，since = until - 24h。
  // 范围 > 92 天被拒绝（10404）。
  int64 since = 2;
  int64 until = 3;
  // model_id 可选地将卡片、原因与序列过滤到一个模型。
  string model_id = 4;
}

message GetErrorAnalysisOverviewResponse {
  taas.common.v1.Response response = 1;
  ErrorAnalysisCard cards = 2;
  repeated ErrorCauseRow causes = 3;
  repeated ErrorSeriesPoint series = 4;
}

message GetErrorAnalysisRequest {
  // organization_id 从会话活跃组织（user 绑定）或 X-Organization-Id
  // （admin 绑定）派生；该字段为 gRPC 直连调用者存在。
  string organization_id = 1;
  // error_code 是路径参数。
  string error_code = 2;
  // since/until 界定范围，unix 秒。默认：until = now，since = until - 24h。
  // 范围 > 92 天被拒绝（10404）。
  int64 since = 3;
  int64 until = 4;
}

message GetErrorAnalysisResponse {
  taas.common.v1.Response response = 1;
  ErrorAnalysisCard cards = 2;
  repeated ErrorSeriesPoint series = 3;
}

// ErrorAnalysisCard 是范围的头条摘要。error_rate 与 top_cause_share_pct
// 在客户端派生；线上仅携带整数计数。
message ErrorAnalysisCard {
  int64 error_count = 1;
  int64 request_count = 2;
  // top_cause 是按计数最高的错误码。
  string top_cause = 3;
  // top_cause_share_pct 在客户端派生为 top 原因计数 / 总错误计数。
  int64 top_cause_share_pct = 4;
  // data_through 是请求日志覆盖的最后一个完整桶（AD7）；其后的桶待处理。
  int64 data_through = 5;
}

// ErrorCauseRow 是 top 原因表中的一个错误码聚合。
message ErrorCauseRow {
  string error_code = 1;
  string error_message = 2;
  int64 error_count = 3;
  // error_rate 与 share_pct 在客户端派生。
  int64 error_rate = 4;
  int64 share_pct = 5;
}

// ErrorSeriesPoint 是序列的一个时间桶。
message ErrorSeriesPoint {
  // bucket 是桶开始，unix 秒（范围 <= 7 天为小时，否则为天，AD5）。
  int64 bucket = 1;
  int64 error_count = 2;
  int64 request_count = 3;
  // error_rate 在客户端派生为 error_count / request_count。
  int64 error_rate = 4;
}
```

### 5.2 契约说明（为 Developer 智能体固定）

1. `GetErrorAnalysisOverview` 验证 `since`/`until`（int64 unix 秒；默认 `until = now`，`since = until − 24h`）；`since > until` 或范围 > 92 天返回 10404（AD5）。`model_id` 与 `organization_id` 是可选过滤。
2. `GetErrorAnalysis` 验证同一范围契约；未知 `error_code` 返回 11501 `CodeErrorCauseNotFound`（AD2）。在 user 前缀上限定到调用者组织（AD8），且不暴露服务 ID 或操作员内部信息。
3. 桶在范围 ≤ 7 天为小时，否则为天（AD5）；每个桶携带 `bucket`、`error_count`、`request_count`、`error_rate`。`error_rate` 与 `share_pct` 在客户端派生；线上仅携带整数计数（AD3）。
4. 每个响应携带 `data_through`（请求日志覆盖的最后一个完整桶）用于新鲜度标记（AD7）。
5. 聚合读取 `request_logs`（功能 #12）— 每个请求的 `status`、`error` 与四个 token 计数；它不写入任何内容（AD1，设计 D9）。
6. 线上约定不变：列表适用点分分页，成功 HTTP 200，业务错误为 `{"code": <int>, "message": "..."}`，int64 字段序列化为 JSON 字符串。

### 5.3 错误码

错误码（error-analysis 块 11501–11599，`pkg/errors/codes.go`）：

| 条件 | 码 | 常量 | 说明 |
| --- | --- | --- | --- |
| 未知 `error_code` | 11501 | `CodeErrorCauseNotFound` | **新**（AD2） |
| 格式错误或超长范围 | 10404 | `CodeMeteringRangeInvalid` | 复用（AD5）— metering 范围契约 |
| 数据库 / 基础设施失败 | 500 | `CodeInternal` | 经错误归一化 |

---

## 6. 前端架构

### 6.1 页面 → 路由 → API 前缀表

| 页面 / 组件 | 表面 | Web 路由 | API 前缀 | 认证守卫 |
| --- | --- | --- | --- | --- |
| **错误分析页面** | admin | `/admin/errors` | `/api/v1/admin/errors` | admin 会话；RoleGuard（admin 角色） |
| **错误详情页面** | admin | `/admin/errors/:errorCode` | `/api/v1/admin/errors/{error_code}` | admin 会话；RoleGuard（admin 角色） |
| **错误分析页面** | end-user | `/errors` | `/api/v1/errors` | user 会话；硬性限定到调用者组织 |
| **错误详情页面** | end-user | `/errors/:errorCode` | `/api/v1/errors/{error_code}` | user 会话；硬性限定到调用者组织 |

> admin 错误分析页面仅调用 `/api/v1/admin/errors/*`；end-user 错误分析页面仅调用 `/api/v1/errors/*`。两个表面绝不共享会话 token（功能 #17）。

### 6.2 导航位置

- **Admin 控制台**：admin 导航中新增 **Errors** 项（`/admin/errors`，testid `nav-errors`），位于操作组，与 Inference Services、Observability 和 Traces 并列。
- **End-user 控制台**：user 导航中新增 **Errors** 项（`/errors`，testid `user-nav-errors`），与 Usage、Cost 和 Traces 并列。

### 6.3 复用共享组件与状态

- **API 客户端**（`web/src/api.ts`）：领域范围客户端（`createApi(realm)`）已发送 `Authorization: Bearer <realm>.session-token` 与 `X-Organization-Id`（仅当领域 token 键为空时）。错误分析页面原样复用；不新增客户端。
- **内联 SVG 图表**：新的共享 `ErrorChart.tsx` 组件（每桶一个条/线，带指标切换器），遵循 usage-dashboard `UsageChart.tsx` 模式（AD6）。指标切换器在客户端切换 error count / error rate，无需重新获取。
- **时间范围过滤**：预设控件（24 h / 7 d / 30 d / 自定义日期时间选择器）与 Usage、Request Logs 和 Observability 页面共享。
- **摘要卡片**：新的共享 `ErrorCards.tsx` 组件渲染卡片行，带 "data through <time>" 新鲜度说明（AD7）。
- **top 原因表**：新的共享 `ErrorCausesTable.tsx` 组件渲染带份额条的错误码。
- **状态 / 新鲜度徽章**：`data_through` 之后的待处理/部分标记复用 usage-dashboard 待处理徽章样式。
- **空状态 / 数据新鲜度说明**：复用 Request Logs 页面模式（说明指标在摄取窗口内出现）。

### 6.4 各表面认证守卫

- **Admin 错误分析页面**（`/admin/errors`、`/admin/errors/:errorCode`）：在 `AdminShell` 内渲染，当 `go-taas.admin.session-token` 非空时运行 admin 会话守卫；缺失/过期会话重定向到 `/admin/login?reason=expired`；错误领域会话被网关以 10038 拒绝。页面 API 调用发往 `/api/v1/admin/errors/*`。无所需角色的会话收到 10036，页面显示标准权限拒绝状态（功能 #17）。
- **End-user 错误分析页面**（`/errors`、`/errors/:errorCode`）：在 `UserShell` 内渲染，当 `go-taas.user.session-token` 非空时运行 user 会话守卫；缺失/过期会话重定向到 `/login?reason=expired`。页面 API 调用发往 `/api/v1/errors/*`。页面不暴露服务 ID 或操作员内部信息（AD8）。
- **未认证访客**：任一页面的未认证访客由 shell 守卫重定向到正确的登录页（`/admin/login` 对 `/login`）。

### 6.5 控制台契约（为 Developer 智能体固定）

**错误分析页面**（`/admin/errors`）：页面头部（"Errors"，副标题 "Error codes and rates over time"）带 **Refresh** 动作（`errors-refresh`）。下方：**过滤栏** — **时间范围**控件（`errors-filter-range`，预设 24 h / 7 d / 30 d / 自定义）与**模型**过滤（`errors-filter-model`，下拉，"All models" 默认）；一行**摘要卡片**（`errors-cards`）：Error rate、Error count、Request count、Top cause（带份额的 top 错误码），各带 "data through <time>" 说明；内联 SVG **错误率趋势图**（`errors-chart`）带指标切换器（`errors-metric-toggle`）；以及 **top 原因表**（`errors-causes`，`errors-cause-{error_code}`）列：Error code（链接到钻取）、Error message、Error count、Error rate、Share。可按 Error count、Error rate 与 Share 排序；可按 Model 下拉过滤；分页。空状态："No error data in this range."，提示扩大范围。错误状态：错误横幅带 Retry 按钮与 "Showing stale data" 横幅。权限拒绝：标准状态，带返回 admin 首页的链接。

**错误详情页面**（`/admin/errors/:errorCode`）：返回概览的链接、带错误码的头部、过滤栏（时间范围）、摘要卡片与带指标切换器的内联 SVG 错误率趋势图。空文案："No error data for this cause in this range." 未找到状态（11501）显示标准未找到状态，带返回概览的链接。

**End-user 错误分析页面**（`/errors`）：与 admin 页面相同，限定到租户自己的错误。空文案 "No error data in this range." 与租户自己的权限拒绝文案（10005 组织消失 / 10017 组织禁用，来自功能 #17 §8.2）。

**End-user 错误详情页面**（`/errors/:errorCode`）：与 admin 详情相同，限定到租户自己对错误码的用量。未知 `error_code`（11501）的未找到文案与租户自己的权限拒绝文案（10005/10017）。

---

## 7. 时序流

### 7.1 Admin 全舰队错误概览加载

```mermaid
sequenceDiagram
    autonumber
    actor Op as 平台操作员
    participant UI as Admin 控制台
    participant CGW as 控制网关
    participant MET as metering 模块
    participant RL as request_logs

    Op->>UI: 打开 /admin/errors
    UI->>CGW: GET /api/v1/admin/errors
    CGW->>MET: GetErrorAnalysisOverview
    MET->>RL: 按桶与错误码聚合 request_logs
    RL-->>MET: 桶与原因行
    MET-->>UI: 卡片 + 原因 + 序列
    UI-->>Op: 摘要卡片 + 错误率趋势图 + top 原因表
    Op->>UI: 选择一个错误码并点击 View
    UI->>CGW: GET /api/v1/admin/errors/{error_code}
    CGW->>MET: GetErrorAnalysis
    MET->>RL: 按桶聚合该错误码的 request_logs
    RL-->>MET: 该错误码的桶
    MET-->>UI: 卡片 + 序列
    UI-->>Op: 卡片 + 错误率趋势图
```

### 7.2 End-user 租户范围错误视图加载

```mermaid
sequenceDiagram
    autonumber
    actor T as 租户开发者 / 智能体
    participant UI as End-user 控制台
    participant CGW as 控制网关
    participant MET as metering 模块
    participant RL as request_logs

    T->>UI: 打开 /errors/:errorCode
    UI->>CGW: GET /api/v1/errors/{error_code}
    CGW->>MET: GetErrorAnalysis（user 绑定）
    MET->>MET: 从会话活跃组织解析调用者组织
    MET->>RL: 按桶聚合调用者组织的该错误码 request_logs
    RL-->>MET: 该错误码的桶（组织范围）
    MET-->>UI: 卡片 + 序列（租户范围，无内部信息）
    UI-->>T: 卡片 + 错误率趋势图
```

### 7.3 聚合查询

```mermaid
sequenceDiagram
    autonumber
    participant MET as metering 模块
    participant REPO as metering 仓库
    participant DB as PostgreSQL

    MET->>REPO: AggregateErrors(ctx, orgFilter, modelFilter, since, until, bucketSize)
    REPO->>DB: SELECT bucket, count, sum(CASE WHEN status = 'error' THEN 1 ELSE 0 END)<br/>FROM request_logs WHERE <org/model/range> GROUP BY bucket, error
    DB-->>REPO: 桶与原因行
    REPO->>DB: SELECT max(created_at) FROM request_logs WHERE <org/model/range>
    DB-->>REPO: data_through 水位
    REPO-->>MET: 桶 + 原因行 + 水位
    MET-->>MET: 构建卡片、原因与序列
```

---

## 8. 错误处理

所有错误都是统一信封中的 `pkg/errors` 业务码（第 5.3 节）。错误分析模块只读，因此无运行器侧失败、无写入可失败。数据库失败归一化为 500 `CodeInternal`。

控制台按 body `code` 分支（`web/src/api.ts` 已如此），绝不按 HTTP 状态。每个新码渲染特定内联消息：11501 "error cause not found"、10404 "invalid range"（metering 范围契约）。admin 页面将 10036 映射到标准权限拒绝状态；end-user 页面将 10005/10017 映射到租户的权限拒绝文案（功能 #17 §8.2）。

---

## 9. 配置新增

无。错误分析功能是对现有表的纯只读聚合，复用 metering 范围契约（10404）与现有 `metering.maxRangeSeconds` 风格上限。不新增配置键、运行器或 MQ 主题（AD1，设计 D9）。

---

## 10. 安全考量

- **表面分离**：admin 错误分析页面仅调用 `/api/v1/admin/errors/*`；end-user 错误分析页面仅调用 `/api/v1/errors/*`。领域守卫在任何处理器运行前以 10038 拒绝错误领域会话（功能 #17）。
- **admin 舰队视图按角色门控**：admin 错误分析 RPC 默认全舰队（AD9），由 `tenancy.RoleGuard` 门控 — 只有具备所需 admin 角色的调用者能看跨组织舰队视图；不可访问组织返回 10036。
- **end-user 硬性限定**：`GetErrorAnalysis` 的 user 绑定硬性限定到调用者组织（会话活跃组织权威，忽略 `X-Organization-Id`）；调用者绝不可能看到另一租户的错误聚合。
- **掩码投影**：end-user 表面不暴露服务 ID、副本数或其他操作员编排内部信息（AD8）。admin 表面是操作员范围。
- **构造上只读**：错误分析模块仅发出 `SELECT`；任何路径上无写入（AD1，设计 D9）。无需新审计事件 — 底层请求日志写入已被审计（功能 #15）。

---

## 11. 上线 / 升级说明

- **无模式变更**：该功能仅读取现有表；单独部署 `taas-server`。无新表、无新索引、无数据迁移、无 init-SQL 升级路径。
- **proto 变更是增量的**：现有 `MeteringService` 上两个新 RPC；无现有 RPC 或消息变更。网关 mux 增加新绑定；领域守卫不变。
- **控制台**：四个新页面加入现有 bundle；admin 导航增加 Errors，end-user 导航增加 Errors。无现有路由变更。
- **向后兼容**：过渡（无会话、`X-Organization-Id`）路径为 CLI、FVT 与 e2e 套件保留；领域守卫放行无 `Authorization` 头的请求（功能 #17 AD4）。
- **数据存在前为空**：错误分析 RPC 在请求日志存在前返回空卡片/原因/序列；页面渲染空状态，提示扩大范围。

---

## 12. 验收标准追溯

| # | 标准 | 覆盖位置 |
| --- | --- | --- |
| AC1 | `GetErrorAnalysisOverview` 带有效范围返回摘要卡片、top 原因排名与错误率趋势；范围 > 92 天或 `since > until` 返回 10404 | §5.1、§5.2、§5.3 |
| AC2 | `GetErrorAnalysisOverview` 返回按错误计数降序排序的 `causes[]`，带客户端派生的 `share_pct` | §5.1、§7.3 |
| AC3 | `GetErrorAnalysis`（admin）返回单错误码卡片与错误率趋势；未知 `error_code` 返回 11501 | §5.1、§5.2、§5.3 |
| AC4 | `GetErrorAnalysis`（user）仅返回调用者组织的错误，无服务 ID 或操作员内部信息 | §3.3、§6.4、§10 |
| AC5 | 桶在范围 ≤ 7 天为小时，范围 > 7 天为天；每个响应携带 `data_through` | §5.2、§7.3 |
| AC6 | `/admin/errors` 页面从首次成功加载渲染过滤栏、摘要卡片、错误率趋势图与 top 原因表，带最后更新时间戳 | §6.5 |
| AC7 | 更改时间范围或模型过滤会重新获取并重新渲染卡片、图表与表；指标切换器切换图表指标 | §6.3、§6.5 |
| AC8 | 无数据匹配时渲染空状态（"No error data in this range."）；失败加载保留最后好数据，带 "Showing stale data" 横幅与 Retry 动作 | §6.5 |
| AC9 | `/admin/errors/:errorCode` 页面渲染错误码的卡片与错误率趋势图；未知错误码显示未找到状态 | §6.5 |
| AC10 | `/errors` 与 `/errors/:errorCode` 页面渲染租户自己的卡片、排名与趋势，无服务 ID 或操作员内部信息可见 | §6.5、§10 |
| AC11 | admin 错误页面仅在 admin 表面可达：路由 `/admin/errors` 与 `/admin/errors/:errorCode`，每个 API 调用使用 `/api/v1/admin/errors/*` 前缀且无 `/api/v1/errors/*` 字符串 | §6.1、§6.4、§10 |
| AC12 | end-user 错误页面仅在 end-user 表面可达：路由 `/errors` 与 `/errors/:errorCode`，每个 API 调用使用 `/api/v1/errors/*` 前缀且无 `/api/v1/admin/*` 字符串 | §6.1、§6.4、§10 |
| AC13 | 无所需角色的会话在 admin 错误页面收到 10036，页面显示标准权限拒绝状态 | §3.3、§6.4、§10 |

---

## 13. 详细设计（逐层函数级职责）

### 13.1 Proto → 服务 → 仓库 → 控制器

| 层 | 文件 | 职责 |
| --- | --- | --- |
| `proto/taas/metering/v1` | `metering.proto` | 增量：`GetErrorAnalysisOverview`/`GetErrorAnalysis` RPC + `GetErrorAnalysisOverviewRequest/Response`、`GetErrorAnalysisRequest/Response`、`ErrorAnalysisCard`、`ErrorCauseRow`、`ErrorSeriesPoint` 消息（第 5.1 节）。经 `buf generate` 重新生成 `metering.pb.go`/`metering_grpc.pb.go`/`metering.pb.gw.go` |
| `services/metering` | `error_analysis_model.go` | 聚合行结构体（`ErrorBucketRow`、`ErrorCauseRow`）与 `bucketSizeForRange` 辅助函数（范围 ≤ 7 天为小时，否则为天，AD5） |
| | `error_analysis_repository.go` | `AggregateErrors(ctx, orgFilter, modelFilter, since, until, bucketSize)` — 对 `request_logs` 的舰队聚合（卡片 + 原因行 + 序列）（AD3）；`AggregateError(ctx, orgID, errorCode, since, until, bucketSize)` — 单错误码聚合（卡片 + 序列）；`DataThrough(ctx, orgFilter, modelFilter, since, until)` — `max(created_at)` 水位（AD7） |
| | `service.go` | 新 RPC `GetErrorAnalysisOverview`、`GetErrorAnalysis`；范围验证（10404，AD5）；admin 舰队范围对 user 硬性限定解析（AD9）；`SessionActiveOrg`/`resolveOrganizationID` 接缝；admin 组织限定的 `RoleGuard` 接缝；进程内 `model_name`/`api_key_name` 解析（AD3） |
| `services/model` | `service.go` | 只读：为 metering 模块暴露 `ModelName(ctx, modelID) (string, error)` 接缝（或复用 `GetModel`）以进程内解析 `model_name`（AD3） |
| `services/auth` | `service.go` | 只读：为 metering 模块暴露 `APIKeyName(ctx, apiKeyID) (string, error)` 接缝以进程内解析 `api_key_name`（AD3） |
| `pkg/errors` | `codes.go`/`messages.go` | `CodeErrorCauseNotFound`（11501）常量 + 规范消息 "error cause not found"（AD2） |
| `apps/taas-server` | `main.go` | 无变更 — 错误分析 RPC 搭乘现有 metering 服务注册；将 `model`/`auth` 名称解析接缝与 `tenancy` RoleGuard 接入 metering 服务 |
| `web/src` | `pages/ErrorAnalysisPage.tsx`、`pages/ErrorDetailPage.tsx`、`pages/user/UserErrorAnalysisPage.tsx`、`pages/user/UserErrorDetailPage.tsx`、`components/ErrorChart.tsx`、`components/ErrorCards.tsx`、`components/ErrorCausesTable.tsx`、`App.tsx`、`api.ts`、`shells/AdminShell.tsx`、`shells/UserShell.tsx` | 路由 `/admin/errors`、`/admin/errors/:errorCode`、`/errors`、`/errors/:errorCode`；`GetErrorAnalysisOverview`/`GetErrorAnalysis` API 类型与调用；导航项（第 6.5 节） |
| `test` | `fvt/error_analysis_fvt_test.go`、`e2e/tests/errorAnalysis.js` | 第 14 节 |

### 13.2 哪个 React 页面/模块实现每个屏幕

| 屏幕 | React 模块 | 路由 | API 调用 |
| --- | --- | --- | --- |
| 错误分析页面（admin） | `web/src/pages/ErrorAnalysisPage.tsx` | `/admin/errors` | `GetErrorAnalysisOverview` |
| 错误详情页面（admin） | `web/src/pages/ErrorDetailPage.tsx` | `/admin/errors/:errorCode` | `GetErrorAnalysis` |
| 错误分析页面（end-user） | `web/src/pages/user/UserErrorAnalysisPage.tsx` | `/errors` | `GetErrorAnalysisOverview` |
| 错误详情页面（end-user） | `web/src/pages/user/UserErrorDetailPage.tsx` | `/errors/:errorCode` | `GetErrorAnalysis` |
| 内联 SVG 图表 | `web/src/components/ErrorChart.tsx`（共享） | （四个页面） | （客户端；指标切换器，无需重新获取） |
| 摘要卡片 | `web/src/components/ErrorCards.tsx`（共享） | （四个页面） | （客户端；渲染返回的卡片） |
| top 原因表 | `web/src/components/ErrorCausesTable.tsx`（共享） | （两个概览页面） | （客户端；渲染返回的原因） |

---

## 14. 测试策略

- **单元**（`services/metering`，sqlite 内存）：`error_analysis_repository_test.go` — `AggregateErrors` 为种子 `request_logs` 集返回正确卡片/原因行/序列（AC1、AC2），`AggregateError` 返回单错误码卡片/序列（AC3），`DataThrough` 返回最后一个完整桶（AC5），桶大小在 7 天边界切换（AC5）。`service_test.go` — 范围验证对 `since > until` 与范围 > 92 天返回 10404（AC1）；未知 `error_code` 返回 11501（AC3）；user 绑定硬性限定到调用者组织且不暴露服务 ID（AC4）；admin 组织限定返回 10036（AC13）。新文件覆盖率 ≥ 80%。
- **FVT**（`test/fvt/error_analysis_fvt_test.go`，metering FVT 模式：文件支持 sqlite + `MigrateSchemaForFVT` + `NewForFVT` + 带生产拦截器的 gRPC 服务器 + 带 `FVTHeaderMatcher` 的网关 mux）：跨组织/模型/密钥种子带错误码的 `request_logs` 行，然后断言 `GetErrorAnalysisOverview` 卡片/原因/序列（AC1）、`causes[]` 错误计数降序排序与 `share_pct`（AC2）、`GetErrorAnalysis` admin 单错误码卡片/序列与 11501（AC3）、user 绑定仅返回调用者组织的行且无内部信息（AC4）、小时对天桶与 `data_through`（AC5）、以及内联 10404（AC1）。
- **E2E**（`test/e2e/tests/errorAnalysis.js`，`usageDashboard.js` 模式）：针对 compose 栈 — admin `/admin/errors` 页面从首次成功加载渲染 `errors-cards`、`errors-chart` 与 `errors-causes`（AC6）；更改时间范围或模型过滤会重新获取且指标切换器切换图表指标（AC7）；空状态与陈旧数据横幅渲染（AC8）；admin 钻取渲染错误码的卡片与趋势及未找到状态（AC9）；end-user `/errors` 与 `/errors/:errorCode` 页面渲染租户自己的卡片/排名/趋势且无服务 ID（AC10）；每个页面仅调用自己的前缀且未认证访客被重定向到正确登录页（AC11/AC12）；无所需角色的会话收到 10036 并显示权限拒绝状态（AC13）。