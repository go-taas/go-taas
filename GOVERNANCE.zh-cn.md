# Go TaaS 治理规则

本文规定 Go TaaS 如何授予社区角色、每个角色可以做什么、决策如何做出，以及
规则本身如何修改。本文具有约束力：当本文与非正式惯例冲突时，以本文为准。

- 英文版：[GOVERNANCE.md](./GOVERNANCE.md)
- 当前角色持有者：[MAINTAINERS.zh-cn.md](./MAINTAINERS.zh-cn.md)
- 贡献流程：[CONTRIBUTING.zh-cn.md](./CONTRIBUTING.zh-cn.md)
- 行为规范：[CODE_OF_CONDUCT.md](./CODE_OF_CONDUCT.md)

> **范围。** 本文治理的是开源项目本身 —— GitHub 组织、仓库权限、评审、发布与
> 社区角色。它**不**描述产品内的租户 RBAC（`owner`、`admin`、`member`、
> `viewer`），后者属于 Go TaaS 平台功能，见
> [组织成员、角色与邀请](./site/docs/design/org-members-rbac.zh-cn.md)。

## 1. 原则

1. **以证据论功。** 权限来自可见的贡献以及已承担责任者的判断。单纯的数量永远
   不构成功劳。
2. **最小权限，逐步授予。** 每个角色都映射到不超出其所需范围的 GitHub 权限。
3. **双通道。** 代码工作与社区工作（分诊、文档、设计、测试、布道）在晋升中
   权重相同。
4. **路径所有权。** 只要可行，责任按领域划分，而不是一次性授予全仓库权限。
5. **默认公开。** 讨论、证据与投票公开进行，除非行为准则案件或安全报告需要
   保密。
6. **人类问责。** 每一项被合并的变更都由一个人负责，而不是由工具负责。

## 2. 角色

角色是封闭集合，不存在自由文本角色。

| 角色 | GitHub 权限 | 范围 |
| --- | --- | --- |
| Contributor | Read | 全项目 |
| Triager | Triage | 全仓库或指定领域 |
| Reviewer | Write + 领域 Team | 指定领域 |
| Maintainer | Maintain + `CODEOWNERS` | 指定领域或全仓库 |
| Steering Committee | Admin（共享） | 全组织 |
| Emeritus | 除公开读之外无权限 | 仅荣誉 |

### 2.1 Contributor（贡献者）

任何参与项目的人：提 Issue、发 PR、参与讨论、以社区成员身份评审、报告缺陷、
改进文档或回答问题。无需申请。

**可以做：** 公开 GitHub 用户在公开仓库上能做的一切。
**不可以做：** 打标签、分配、批准、合并或修改仓库设置。

### 2.2 Triager（分诊者）

Triager 保证 backlog 可用。这个角色的存在，是为了让新人不必写代码也能积累信任。

**可以做：**

- 添加与移除标签，设置里程碑与负责人；
- 关闭重复 Issue、过时 Issue 与已在别处回答的问题；
- 复现与分类报告，索取缺失信息；
- 管理 Discussions（分类、置顶、锁定），把报告转给正确的领域；
- 被指派 Issue 与 Pull Request。

**不可以做：**

- 批准或合并 Pull Request；
- 修改仓库设置、Team 或工作流。

### 2.3 Reviewer（评审者）

Reviewer 是所属领域的质量关口。

**可以做：**

- 批准所属领域的 Pull Request，或要求修改；
- 通过 `CODEOWNERS` 成为必需评审人；
- 提名 Triager 与 Reviewer 候选人；
- 参与设计评审，并在治理投票中被征询。

**不可以做：**

- 单独合并高风险路径（见 §6）；
- 发布 Release、修改分支保护或管理密钥。

### 2.4 Maintainer（维护者）

Maintainer 承担一个领域的责任：方向、质量与发布。

**可以做：**

- 合并所属领域的 Pull Request（受 §6 约束）；
- 负责领域路线图、里程碑与标签；
- 提名 Triager、Reviewer 与 Maintainer 候选人；
- 对治理事项与晋升投票；
- 被指定时担任 release manager；
- 提议新的领域与领域 Team。

**不可以做：** 在没有第二名复核者的情况下修改密钥、分支保护、`GOVERNANCE.md`、
`CODEOWNERS` 或许可证（见 §6）。

### 2.5 Steering Committee（指导委员会）

Steering Committee 是最终上诉机构，也是本文的保管人。设 3–5 席，同一雇主最多
2 名成员。

**职责：**

- 解决 Maintainer 共识无法解决的僵局；
- 审阅并批准对本文的修订；
- 与 moderation 小组共同升级处理行为准则案件；
- 掌管组织级设置：Team、仓库设置、密钥、分支保护与集成；
- 处理发布与安全 escalation。

**约束：**

- 不得绕过行为准则流程；
- 不得单方面修改本文（见 §8）；
- 涉及本人或雇主利益时必须回避。

### 2.6 Emeritus（荣休）

已退出日常工作的前角色持有者。Emeritus 是荣誉而非降级：保留头衔、名册条目与
署名，同时释放权限。

- 连续 6 个月无实质贡献，或本人申请后授予；
- 由 Maintainer 确认，并记录在 `MAINTAINERS.zh-cn.md`；
- 本人可申请恢复，经 Maintainer 共识确认；
- 因违反行为准则被移除者不具备 Emeritus 资格。

## 3. 权限矩阵

| 操作 | Contributor | Triager | Reviewer | Maintainer | Steering |
| --- | --- | --- | --- | --- | --- |
| 提 Issue、Pull Request、评论 | ✅ | ✅ | ✅ | ✅ | ✅ |
| 打标签、分配、关闭重复 Issue | ❌ | ✅ | ✅ | ✅ | ✅ |
| 管理 Discussions | ❌ | ✅ | ✅ | ✅ | ✅ |
| 批准所属领域的 Pull Request | ❌ | ❌ | ✅ | ✅ | ✅ |
| 合并所属领域的 Pull Request | ❌ | ❌ | ❌ | ✅ | ✅ |
| 合并跨领域或高风险 Pull Request | ❌ | ❌ | ❌ | 2 名批准，其一为非作者 Maintainer | ✅ |
| 创建 Release 或 tag | ❌ | ❌ | ❌ | ✅（release manager） | ✅ |
| 修改 CI/CD 或发布工作流 | ❌ | ❌ | ❌ | 提案 + 另 1 名 Maintainer | ✅ |
| 修改 `GOVERNANCE.md`、`CODEOWNERS`、分支保护 | ❌ | ❌ | ❌ | 提案 + 公开评论期 | ✅（2/3 投票） |
| 管理 Team、仓库设置、密钥 | ❌ | ❌ | ❌ | ❌ | ✅（双人规则） |
| 提名晋升 | ❌ | ❌ | ✅ | ✅ | ✅ |
| 治理投票 | ❌ | ❌ | 被征询 | ✅ | ✅ |
| 处理行为准则报告 | 仅可报告 | 仅可报告 | 仅可报告 | 报告/回避 | ✅（与 moderation 小组） |

## 4. 晋升

### 4.1 流程

1. **提名。** 持有目标角色或更高角色者，使用
   [角色申请模板](./.github/ISSUE_TEMPLATE/role_request.yml) 提交 role request
   Issue，写明候选人、角色、领域与证据。
2. **证据。** 申请中链接满足 §4.2 的具体贡献：Pull Request、评审、分诊记录、
   文档、设计或测试工作。允许自荐，但需一名赞助人联署。
3. **评论期。** 申请至少开放 7 天，任何社区成员都可以公开提出异议。
4. **确认。** Maintainer 以共识确认；共识失败时由活跃 Maintainer 简单多数投票
   决定。若存在关于行为或质量的持续反对，晋升暂停直至问题解决。
5. **授予。** 由 Steering 成员（bootstrap 模式下为临时 Steering 成员）应用
   GitHub 权限或 Team 成员关系，更新 `MAINTAINERS.zh-cn.md`，并在 Discussions
   公告。

### 4.2 标准

**Triager** —— 至少分类、复现或去重 10 个 Issue 或 Pull Request；1 名 Maintainer
提名；一份描述领域与所用标签的简短交接说明。不要求代码贡献。

**Reviewer** —— 领域内至少 3 个已合并 Pull Request，或等价的设计、文档、测试
贡献；至少 5 次有实质内容的评审；2 名 Reviewer 或 Maintainer 提名；Maintainer
共识。

**Maintainer** —— 作为 Reviewer 的持续记录（至少 10 次实质评审或合并）；在领域
架构、发布与兼容性上展现判断力；2 名 Maintainer 提名；活跃 Maintainer 简单多数；
Steering 确认不存在治理或行为阻碍。

**Steering Committee** —— 至少一个 Maintainer 任期；由 Maintainer 或 Reviewer
提名，由 Maintainer 与 Reviewer 选举；3–5 席，同一雇主最多 2 席。

标准可以由等价工作满足：持续评审 10 个文档 PR 的文档负责人，与代码评审者以同样
方式满足 Reviewer 门槛。

### 4.3 志愿工作不是义务

角色是信任的授予，不是合同。任何人都可以随时退出且无需解释；项目鼓励转入
Emeritus，而不是无声消失。

## 5. 决策

### 5.1 默认：lazy consensus

日常决策 —— 合并 PR、接受设计、关闭 Issue —— 通过 lazy consensus 推进。没有
带理由的反对时决策成立。重大 RFC 与治理变更需要至少 72 小时的公开评论期。

### 5.2 何时需要投票

- 角色持有者的晋升、恢复或移除；
- 对本文的修订；
- 因技术或质量原因阻断发布；
- 领域 Maintainer 无法解决的僵局。

### 5.3 法定人数与门槛

| 决策 | 主体 | 门槛 |
| --- | --- | --- |
| 晋升 Triager | Maintainer | 共识；共识失败时多数 |
| 晋升 Reviewer | Maintainer | 共识；共识失败时多数 |
| 晋升 Maintainer | 活跃 Maintainer | 简单多数 |
| Steering 选举 | Maintainer + Reviewer | 相对多数，同一雇主最多 2 席 |
| 阻断发布 | 活跃 Maintainer | 简单多数 |
| 治理修订 | 活跃 Maintainer + Steering | 2/3 赞成 |
| 因故移除 | Steering + moderation 小组 | Steering 2/3 |

常规投票需 2/3 活跃 Maintainer 参与。未达法定人数的投票重新公告一次；再次未达
则由 Steering Committee 决定。

### 5.4 回避

任何在结果中存在个人、雇主、赞助或财务利益者必须回避并声明。Steering 成员同样
适用。

### 5.5 升级

任何社区成员都可以在仓库中开 Issue 并 @`go-taas/steering`，请求 Steering
Committee 复核某项决定。Steering 可以维持、推翻或附指导意见退回。它不得绕过
行为准则流程，也不会在未违反流程规则时复核普通 Pull Request 的技术优劣。

## 6. 高风险操作的双人规则

下列变更需要两名批准者，且第二名批准者不能是作者：

- 创建 Release 或推送版本 tag；
- 修改 `.github/workflows/`、`/build/` 或 `/deploy/` 下的任何内容；
- 修改 `GOVERNANCE.md`、`MAINTAINERS.md`、`CODEOWNERS`、`CODE_OF_CONDUCT.md`
  或许可证；
- 修改分支保护、组织 Team、密钥、部署密钥或仓库集成。

v1 中该规则以社会约束方式执行，并体现在 `.github/CODEOWNERS`。如果只有一名
Maintainer 可用却必须进行高风险变更，则先在 Discussions 公告、至少开放 24 小时，
并由 `MAINTAINERS.zh-cn.md` 中指定的备份复核者评审。

## 7. Bootstrap 模式

项目 Maintainer 少于 3 人时，适用以下调整，使规则在第一天即可运行：

- 创始人与现有 Maintainer 组成**临时 Steering Committee**；
- 晋升确认只需**活跃 Maintainer 简单多数**，而非 §5.3 的门槛；
- Steering 选举推迟至第三名 Maintainer 确认；
- 双人规则继续生效，并指定具名备份复核者。

第三名 Maintainer 确认后该模式自动结束。临时 Steering Committee 在 30 天内公告
首次常规 Steering 选举。

## 8. 修订

1. 提交修改本文的 Pull Request，并说明动机。
2. 至少开放 7 天公开评论。
3. 获得活跃 Maintainer 与 Steering Committee 的 2/3 批准，并适用 §6 的双人规则。
4. 在 Pull Request 中记录变更，并在 Discussions 公告。

编辑性修正（拼写、失效链接、格式）只需 1 名 Maintainer 批准，不受评论期约束。

## 9. 移除与冲突处理

- 角色持有者可能因持续不活跃（§2.6）、反复违反本文或行为准则决定而被移除。
- 因故移除需要 Steering Committee 2/3 投票，并向本人、必要时向社区说明书面理由。
- 行为案件由行为准则下的 moderation 小组处理；Steering Committee 只负责升级处理
  与权限回收。
- 关于本文的分歧由 Steering Committee 在公开讨论后裁定。

## 10. AI 辅助贡献

- 可以使用 AI 工具编写代码、测试与文档。人类提交者对正确性、许可证与测试覆盖
  负全部责任。
- Pull Request 说明使用了哪些 AI 工具，以及哪些部分由人工验证。
- AI 不能持有角色、赞助提名、投票或被列为维护者。
- AI 生成内容与人类内容采用同一评审标准。维护者可要求补充测试、解释或人工复核。

## 11. 认可

- 每位角色持有者都记录在 `MAINTAINERS.zh-cn.md`，包含领域与授予日期。
- 贡献者在 Release Notes 中署名；非代码贡献（分诊、文档、设计、测试、社区支持）
  与代码贡献以同样方式署名。
- Emeritus 成员的条目永久保留。
- 认可是声誉与治理权。项目不发放悬赏，任何角色都不代表雇佣关系。

## 12. 演进

当某个领域拥有两名以上稳定 Maintainer 时，该领域升级为领域 Team：

1. 创建 `<area>` Team 并加入 `CODEOWNERS`；
2. 把该领域的合并权从全局 Maintainer Team 转移到领域 Team；
3. 若评审量足以支撑，在该领域内把 Reviewer 拆分为 Reviewer 与 Approver；
4. 在 `MAINTAINERS.zh-cn.md` 记录变更并在 Discussions 公告。

随着项目成长，§2 的角色词汇保持不变。
