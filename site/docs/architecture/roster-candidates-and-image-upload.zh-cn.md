# 成员候选列表与镜像上传 —— 架构与详细设计

| 属性 | 内容 |
| --- | --- |
| 功能点 | 成员名单改为从「还能加入的账号」中挑选；镜像清单增加上传能力（把 `docker save` 的镜像包推送到 Harbor），并去掉注册；邀请页以组织显示名描述；集群页操作按钮成组 |
| 文档范围 | 两个成员候选接口及其资格规则、账号目录 seam、multipart 上传端点、镜像包处理与目标命名规则、控制台交互、错误码、配置与验收标准 |
| 所属模块 | `services/tenancy`（候选接口、成员过滤） · `services/auth`（`ListActiveUsers`） · `services/image`（`UploadImage`） · `pkg/registry`（镜像包解析、`skopeo copy docker-archive:`） · `pkg/server`（multipart 端点与上传 seam） · `web`（`MembersPage`、`ProjectMembersPage`、`InvitationsPage`、`ClustersPage`、`ImagesPage`、`UploadImageDialog`） |
| 关联文档 | [镜像管理](./image-management.zh-cn.md)（镜像清单与预热） · [StormService 服务拓扑](./stormservice-serving-topology.zh-cn.md)（仓库配置投影） |
| 状态 | 已实现 |

---

## 1. 成员候选列表

一个用户最多属于一个组织和一个项目（RBAC 增量的第 5 条需求），所以「我能添加谁」不是自由文本问题，而正好是「还没加入的账号」。

| 名单 | 候选集合 |
| --- | --- |
| 组织成员 | 不属于**任何**组织的活跃账号 |
| 项目成员 | 该项目**所属组织**中不属于**任何**项目的活跃账号 |

两个接口返回该集合：

```proto
rpc ListOrgMemberCandidates(...)     // GET /api/v1/admin/tenancy/organizations/{id}/member-candidates
rpc ListProjectMemberCandidates(...) // GET /api/v1/admin/tenancy/projects/{id}/member-candidates
```

`MemberCandidate { user_id, username, display_name, email }`。

**AD1 —— tenancy 负责过滤，auth 提供账号。** 成员表（`org_members`、`project_members`）属于 tenancy 模块，账号行属于 `auth`。因此 tenancy 服务接收一个 `CandidateUserDirectory` seam（`ListActiveUsers`），由 `auth.ListActiveUsers` 实现，减法在 tenancy 内完成：

- `AllOrgMemberUserIDs`：属于任意组织的全部用户；
- `OrgMemberUserIDs(orgID)`：某个组织的成员；
- `AllProjectMemberUserIDs`：属于任意项目的全部用户。

seam 未接线时返回空列表而不是错误结果，避免半初始化时泄露账号。

**AD2 —— 与名单本身同一道门禁。** `ListOrgMemberCandidates` 使用名单管理员门禁（`requireRosterAdmin`，平台管理员同样满足）；`ListProjectMemberCandidates` 使用项目管理员门禁（项目管理员，或所属组织的管理员/所有者）。越权调用返回 10007/10036，绝不会返回候选列表。

**AD3 —— 控制台以下拉列表呈现。** 成员弹窗与项目名单都渲染候选 `<select>`，标签优先显示名（与用户名不同时附用户名，见 `web/src/candidates.ts`），并附一行说明资格规则。候选为空时给出明确文案（`members.noCandidates`、`projectMembers.noCandidates`），而不是一个空下拉。

**AD4 —— 弹窗只有一个操作。** 成员弹窗此前把页面的「添加成员」标签重复用在提交按钮上；现在改为标准的 `取消 / 添加` 操作行，且未选择账号时提交按钮禁用。

## 2. 镜像上传

### 2.1 传输方式

接口是 gRPC-JSON 转码的，而转码无法承载二进制体：`docker save` 的镜像包动辄数百 MB，base64 还会再放大三倍。因此上传是一个普通的流式端点：

```
POST /api/v1/admin/images:upload      multipart/form-data
  file              镜像包（.tar、.tar.gz、.tgz）
  name, tag         可选；由镜像包（或引用）提供
  sourceReference   可选；操作者填写的镜像包内镜像地址
  accelerator, engine, description
```

它通过 `HandlePath` 挂在网关 mux 上，因此 realm 守卫、控制台处理器与管理端前缀都按普通管理端请求对待。

**AD5 —— 流式处理，绝不整包缓存。** `pkg/server` 用 `r.MultipartReader()` 读取请求，把文件部分直接复制到临时文件，表单字段读入 map（每项 1 MiB）。不解析进内存，也从不解包，因此恶意镜像包不可能写到该临时文件之外。`http.MaxBytesReader` 在读取任何字节前就把请求限制在 `image.upload.maxArchiveBytes`（默认 8 GiB）。

**AD6 —— 身份规则与 gRPC 一致。** 处理器把 `Authorization` 与 `X-Organization-Id` 转发进 gRPC 元数据，并把会话角色解析器挂到 context 上（与一元拦截器完全相同），然后在进程内调用镜像服务。平台管理员门禁看到的调用者与转码调用一致。审计所需的操作者由接线适配层解析（`auth.SessionUserID`）。

**AD7 —— 目标命名规则唯一。** 上传与仓库导入落到同一处：`image.harbor.hostname` + `image.harbor.project` + 镜像**自身**的名称。源仓库与其命名空间被丢弃，`docker.io/library/a/b/hello:1.0.0` 变成 `<harbor>/<project>/hello:1.0.0`。

**AD8 —— 镜像包提供名称，表单是回退。** `pkg/registry.ReadArchiveReference` 从（可能 gzip 压缩的）tar 中读取 `manifest.json`，返回第一个 `RepoTags`。这里有两种用途，绝不能混用：

- **选择器**：只有镜像包本身携带的引用才能选中包内成员（`docker-archive:<path>:<reference>`）；无标签的包整包复制，由目标名命名；
- **名称/标签**：依次取镜像包引用、表单 `sourceReference`、显式 `name`/`tag` 字段。

把表单引用当作选择器会失败并报 `Tag "…" not found` —— 这正是代码中显式区分两者并由测试覆盖的原因。

**AD9 —— 控制台不再提供注册。** 镜像页只保留「导入镜像」（来自其它仓库）与「上传镜像」（来自镜像包）。`RegisterImage` RPC 仍作为清单的底层接口保留（FVT 与 CLI 使用），但控制台不再有入口，相关废弃文案键已删除。

## 3. 控制台细节

- **上传弹窗**（`components/UploadImageDialog.tsx`）：先展示解析出的目标地址（`<host>/<project>/<name>`），然后是加速卡（集群厂商 + CPU）、引擎、描述。上传使用 `XMLHttpRequest` —— 它是唯一能报告上传进度的浏览器 API；`progress-fill` 进度条显示已发送字节，字节传完后切换为「正在推送到 Harbor」等待服务端执行 `skopeo copy`。关闭弹窗会中止请求。
- **邀请页**以组织显示名描述（`organizationLabel`，控制台 scope 的名称作为回退），不再打印 UUID。
- **集群页**把 *发现当前集群 / 刷新 / 注册集群* 收进一个 `.toolbar`，因为页面头部用 `space-between` 分配直接子元素 —— 三个平级按钮会被推到两端。

## 3b. 加入即迁移

成员候选列表建立在一个前提上：「一个账号最多属于一个组织和一个项目」。此前这只是一句文档描述：`AddOrgMember` 插入新行后保留旧行，而目录解析的是**最早**的一条成员记录，于是管理员把用户移到另一个组织后，界面仍显示原来的组织 —— 看起来像什么都没发生。

`MoveUserToOrg`（`AddOrgMember`）与 `MoveUserToProject`（`AddProjectMember`）在写入时把它变成事实，并与随后的插入处于同一个事务：

| 规则 | 效果 |
| --- | --- |
| 清除其他组织 | `DELETE FROM org_members WHERE user_id = ? AND organization_id <> ? AND organization_id <> 'platform'` |
| 清除其他组织的项目 | 组织 B 的成员不能同时持有组织 A 的项目，这些 `project_members` 行一并删除 |
| 保留平台锚点 | `platform` 是平台自身的租户锚点，不是个人加入的组织 |
| 保留目标组织内的项目 | 用户在新组织内已经拥有的项目不会被移除 |
| 清除其他项目（项目加入） | 一个账号最多属于一个项目 |
| 重复加入先行拒绝 | 把已在该组织的成员再次加入时，在任何删除之前返回 10029，保证「返回错误的请求没有顺带迁移账号」 |

**组织所有者不可迁移。** 组织必须保留所有者，成员名单本来也拒绝移除所有者（10032）。迁移同样拒绝，而不是让平台用户目录成为绕过限制的通道：所有者必须先完成所有权交接。

控制台与此保持一致：用户页的弹窗默认选中账号当前所属组织，明确提示将离开哪个组织（以及该组织下的项目会一并移除）；当组织未改变时改用 `PATCH` 更新角色，而不是发送一次必然返回 10029 的重复「加入」。

## 4. 错误码

| 错误码 | 名称 | 触发场景 |
| --- | --- | --- |
| 10207 | `IMAGE_REFERENCE_INVALID` | 缺少文件、镜像包不可解析、名称无法推导、未配置 Harbor、未配置镜像包导入、`skopeo` 失败 |
| 10202 | `IMAGE_EXISTS` | （名称、标签、加速卡）三元组已注册 |
| 10203 | `IMAGE_INCOMPATIBLE` | 加速卡不是已知厂商或 `cpu` |
| 10007 / 10036 | 角色错误 | 调用者无权管理该名单 |
| 10006 | `PROJECT_NOT_FOUND` | 为不存在的项目请求候选列表 |
| 10029 | `MEMBER_EXISTS` | 加入账号已在该组织（控制台改为发送角色更新） |
| 10032 | `OWNER_PROTECTED` | 把账号移出其担任所有者的组织 |

## 5. 配置

```yaml
image:
  harbor:
    hostname: hub.example.com   # 为空则同时关闭导入与上传
    project: taas
  upload:
    maxArchiveBytes: 8589934592 # 默认 8 GiB
    tempDir: ""                 # 使用系统临时目录
```

Helm 暴露 `harbor.uploadMaxArchiveBytes`（为 0 时使用默认值）。控制台路由的请求超时必须大于大镜像包的传输时间；当前部署的路由使用 900 秒。

## 6. 验收标准

- **AC1** `GET …/organizations/{id}/member-candidates` 只返回未加入任何组织的活跃账号；非名单管理员被拒绝。
- **AC2** `GET …/projects/{id}/member-candidates` 只返回所属组织中未加入任何项目的成员；项目不存在返回 10006。
- **AC3** 成员弹窗以下拉列表提供候选，未选择时确认按钮禁用，且只有一个取消/确认操作行。
- **AC4** 项目名单以下拉列表提供所属组织的合格成员。
- **AC5** 上传 `docker save` 镜像包后，它被推送到 `<harbor>/<project>/<name>:<tag>` 并以该引用注册到清单；控制台先报上传进度，再报推送。
- **AC6** 无标签的镜像包也能上传成功（不会用表单引用去选中包内成员）。
- **AC7** `image.upload.maxArchiveBytes` 限制请求大小，且 Helm 可配置。
- **AC8** 镜像页无注册操作；邀请页展示组织名；集群页操作成组。
- **AC9** 把用户加入第二个组织会移除原组织成员身份，以及其在原组织项目的成员身份；目录随后报告新的组织。
- **AC10** 目标组织内已有的项目在迁移后保留。
- **AC11** 迁移组织所有者返回 10032，且不改变任何数据。

## 7. 验证

已在目标集群验证：候选接口在有/无合格账号时均正确返回（插入并删除了一个临时账号），成员弹窗渲染出候选下拉与操作行，邀请页显示组织名，集群按钮成组；用 `skopeo copy … docker-archive:` 生成的无 `RepoTags` 镜像包（24 MB）分别通过浏览器与 `curl` 上传成功，Harbor 新增 `<project>/curl-ui:1` 与 `<project>/curl:latest` 且清单同时列出两条。随后删除了测试记录与 Harbor 仓库。
