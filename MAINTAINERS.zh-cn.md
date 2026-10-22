# 维护者名册

本文件是 Go TaaS 社区角色的权威名册，记录每个人所属的领域，以及支撑
[GOVERNANCE.zh-cn.md](./GOVERNANCE.zh-cn.md) 规则的仓库设置。治理规则本身在
`GOVERNANCE.zh-cn.md`；本文件只记录当前由谁持有哪个角色。

- 英文版：[MAINTAINERS.md](./MAINTAINERS.md)
- 角色定义与晋升标准：[GOVERNANCE.zh-cn.md](./GOVERNANCE.zh-cn.md#4-晋升)
- 如何申请角色：
  [角色申请模板](./.github/ISSUE_TEMPLATE/role_request.yml)

> **状态：bootstrap 模式。** 项目 Maintainer 少于 3 人，因此由创始人与现有
> Maintainer 组成临时 Steering Committee，晋升确认只需活跃 Maintainer 简单多数
> （见 `GOVERNANCE.zh-cn.md` §7）。第三名 Maintainer 确认后该模式自动结束。

## 1. Steering Committee（临时）

| GitHub | 姓名 | 起始时间 | 范围 | 备注 |
| --- | --- | --- | --- | --- |
| [@sanmuny](https://github.com/sanmuny) | Sen Wang | 2026-10-22 | 全组织 | 创始人；首次常规选举前担任临时主席 |

席位：3–5 席中已填 1 席。第三名 Maintainer 确认后 30 天内公告首次常规选举。

## 2. Maintainer（维护者）

| GitHub | 姓名 | 起始时间 | 领域 |
| --- | --- | --- | --- |
| [@sanmuny](https://github.com/sanmuny) | Sen Wang | 2026-10-22 | 全仓库（bootstrap） |

## 3. Reviewer（评审者）

尚无已确认的 Reviewer。候选人应阅读 `GOVERNANCE.zh-cn.md` §4.2 的 Reviewer 标准，
在某个领域积累 3 个已合并贡献与 5 次实质评审后提交角色申请。

## 4. Triager（分诊者）

尚无已确认的 Triager。分诊是新贡献者预期的第一步：不要求代码，只需分类、复现或
去重报告的历史记录。

## 5. Emeritus（荣休）

暂无 Emeritus 成员。Emeritus 条目永久保留，不会从本文件中删除。

## 6. 领域与所有权

领域是路径所有权的基本单位。每个领域对应一个 GitHub Team 与一条
`.github/CODEOWNERS` 记录。Team 创建前，由 bootstrap 所有者负责该领域的评审。

| 领域 | 路径 | GitHub Team | bootstrap 所有者 | 状态 |
| --- | --- | --- | --- | --- |
| 核心服务 | `/services/` | `@go-taas/area-core` | [@sanmuny](https://github.com/sanmuny) | 待创建 Team |
| 共享包 | `/pkg/` | `@go-taas/area-core` | [@sanmuny](https://github.com/sanmuny) | 待创建 Team |
| 控制器 | `/internal/` | `@go-taas/area-core` | [@sanmuny](https://github.com/sanmuny) | 待创建 Team |
| API 契约 | `/proto/` | `@go-taas/area-api` | [@sanmuny](https://github.com/sanmuny) | 待创建 Team |
| Web 控制台 | `/web/` | `@go-taas/area-web` | [@sanmuny](https://github.com/sanmuny) | 待创建 Team |
| 部署与 CI | `/deploy/`、`/build/`、`/.github/workflows/` | `@go-taas/maintainers`、`@go-taas/steering` | [@sanmuny](https://github.com/sanmuny) | 高风险；双人规则 |
| 测试 | `/test/` | `@go-taas/area-test` | [@sanmuny](https://github.com/sanmuny) | 待创建 Team |
| 文档与站点 | `/site/docs/`、根目录 `*.md` | `@go-taas/area-docs` | [@sanmuny](https://github.com/sanmuny) | 待创建 Team |

当某个领域出现第二名稳定 Maintainer 时，按 `GOVERNANCE.zh-cn.md` §12 升级为独立
领域 Team。

## 7. 待创建的 Team

组织目前没有 Team。在依赖 `CODEOWNERS` 之前先创建：

| Team | 成员 | 仓库权限 | 用途 |
| --- | --- | --- | --- |
| `maintainers` | 全部 Maintainer | Maintain | 默认 `CODEOWNERS` 所有者；合并所属领域 |
| `steering` | Steering Committee | Admin | 组织设置、发布、高风险批准 |
| `area-core` | `services/`、`pkg/`、`internal/` 的 Maintainer | Write | 核心服务评审 |
| `area-api` | `proto/` 的 Maintainer | Write | API 契约评审 |
| `area-web` | `web/` 的 Maintainer | Write | 控制台评审 |
| `area-test` | `test/` 的 Maintainer | Write | 测试与 e2e 评审 |
| `area-docs` | `site/docs/` 的 Maintainer | Write | 文档评审 |
| `triagers` | 全部 Triager | Triage | backlog 管理 |

Reviewer 加入所属领域 Team。Triager 只加入 `triagers`。

## 8. 评审服务等级

以下是目标而非合同。它们让贡献者知道可以期待什么，也让维护者及时发现评审停滞。

| 事项 | 目标 | 升级路径 |
| --- | --- | --- |
| 新 Issue 首次响应 | 3 个工作日 | Triager，然后 Maintainer |
| 新 Pull Request 首次评审 | 5 个工作日 | 领域 Maintainer，然后 `@go-taas/maintainers` |
| 修改后再次评审 | 3 个工作日 | 领域 Maintainer |
| 安全报告确认 | 2 个工作日 | Steering Committee（私下） |
| 阻断发布的回归 | 1 个工作日 | Release manager，然后 Steering |

如果同一领域连续两次未达目标，Steering Committee 询问领域所有者是否需要增加一名
Maintainer。这是容量信号，不是惩罚。

## 9. 支撑规则的仓库设置

在 GitHub 设置中（或经确认后用 `gh api`）应用以下配置。它们记录在此，因为
`GOVERNANCE.zh-cn.md` 依赖它们生效。

- [ ] 创建 §7 中的 Team 并授予所列权限。
- [ ] 为 `main` 启用分支保护：
  - [ ] 合并前必须提交 Pull Request。
  - [ ] 至少需要 1 个批准。
  - [ ] 必须获得 Code Owner 评审。
  - [ ] 推送新提交时驳回过期批准。
  - [ ] 要求批准最近一次可评审的推送。
  - [ ] 要求线性历史。
  - [ ] 禁止强制推送与删除。
  - [ ] 不允许绕过上述设置（包括管理员）。
- [ ] 要求组织成员启用双因素认证。
- [ ] 仅允许 `maintainers` Team 推送到 `main`。
- [ ] 仅允许 `maintainers` 与 `steering` 创建 Release 与 tag。
- [ ] 限制 Actions 只能运行 `.github/workflows/` 中的工作流，并要求首次贡献者
      的工作流需批准。
- [ ] 启用 secret scanning 与 Dependabot 告警。
- [ ] 确认 Discussions 分类与 §6 的领域一致。
- [ ] 保留 `good first issue` 与 `help wanted` 标签用于新人引导。

## 10. 修改本文件

`MAINTAINERS.md` 属于 `GOVERNANCE.zh-cn.md` §6 定义的高风险路径：变更需要两名
批准，其中一人不是作者。晋升时应在同一变更集中更新名册与 GitHub Team 成员关系。
