# 为 Go TaaS 贡献

感谢你的关注！本文介绍开发环境、编码约定，以及提交 Issue 与 Pull Request 的流程。

## 目录

- [快速开始](#快速开始)
- [开发流程](#开发流程)
- [提交规范](#提交规范)
- [Pull Request 指引](#pull-request-指引)
- [Issue 报告](#issue-报告)
- [设计变更](#设计变更)
- [社区角色与成长](#社区角色与成长)
- [评审 Pull Request](#评审-pull-request)
- [AI 辅助贡献](#ai-辅助贡献)
- [许可证](#许可证)

## 快速开始

### 前置条件

- Go 1.25+
- golangci-lint v2
- [buf](https://buf.build/)（用于 Protobuf 代码生成）
- Git

### 从源码构建

```bash
git clone https://github.com/go-taas/go-taas.git
cd go-taas

make pbgen   # 生成 Protobuf 代码（gRPC stub + grpc-gateway + OpenAPI）
make deps    # 更新依赖
make lint    # 静态检查
make ut      # 单元测试
make build   # 构建二进制
```

## 开发流程

1. Fork 仓库并从 `main` 创建分支：

   ```bash
   git checkout -b feat/my-feature
   ```

2. 修改代码。保持提交小而聚焦；每个提交都应通过 `make lint` 与 `make ut`。

3. 提交 Pull Request 前确认完整检查通过：

   ```bash
   make lint
   make ut
   ```

4. 推送分支并向 `main` 提交 Pull Request。

### 代码风格

- 所有代码必须通过 `golangci-lint v2`；修复告警而不是抑制它。
- 遵循标准 Go 约定（[Effective Go](https://go.dev/doc/effective_go)、`gofmt`/`goimports`）。
- 新代码必须有单元测试。关键模块（计费、协议转换）必须覆盖边界情况；项目目标为至少 80% 单元测试覆盖率。
- Protobuf 契约是 API 的事实来源：先改 `.proto` 文件，再用 `make pbgen` 生成。生成代码（`*.pb.go`、`*.pb.gw.go`、`site/docs/api/`）**不入库** —— 只跟踪 `*.proto` 文件。

### 文档

- 面向用户与设计的文档是双语的：英文文档位于 `site/docs/`，中文版本使用 `.zh-cn.md` 后缀。修改术语或架构图时，两侧都要更新，包括 Mermaid 节点标签。

## 提交规范

提交必须遵循 [Conventional Commits](https://www.conventionalcommits.org/zh-hans/)，由 commitlint 校验：

```
<type>(<可选 scope>): <subject>

<可选 body>
<可选 footer>
```

常用类型：`feat`、`fix`、`docs`、`style`、`refactor`、`test`、`chore`、`perf`。

示例：

```
feat(infer): publish deployment changes to the message queue
```

## Pull Request 指引

- 一个 PR 只做一件逻辑上的事。不要把重构与功能混在一起。
- 填写 Pull Request 模板，包含动机与变更摘要。
- 新功能与缺陷修复应附带能证明修复或行为的测试。
- 如果 PR 影响架构或公共 API，请链接相关设计文档并说明影响。
- 不允许直接推送到 `main`；所有变更都通过 Pull Request 评审。

## Issue 报告

- **缺陷报告**：使用缺陷模板，包含复现步骤、期望行为与实际行为。
- **功能请求**：使用功能请求模板，描述使用场景，而不仅是方案。
- 提交前先搜索已有 Issue；遇到同样问题时在已有 Issue 下评论。

## 设计变更

Go TaaS 处于设计与早期开发阶段，涉及设计的变更（新模块、职责边界调整、协议变更）应先开 Issue 或 Discussion 再实现。大改动以设计文档形式评审，远比一大段代码 diff 更容易被接受。

## 社区角色与成长

贡献不只是代码。分诊、文档、设计、测试与回答问题都算贡献，也都可以通向角色。规则见 [GOVERNANCE.zh-cn.md](./GOVERNANCE.zh-cn.md)；当前角色持有者见 [MAINTAINERS.zh-cn.md](./MAINTAINERS.zh-cn.md)。

| 角色 | 获得什么 | 如何达到 |
| --- | --- | --- |
| Contributor（贡献者） | 参与权 | 提 Issue 或 Pull Request —— 无需申请 |
| Triager（分诊者） | Triage 权限：标签、分配、关闭重复、Discussions 管理 | 分类或复现 10 个报告，1 名 Maintainer 提名，一份简短交接说明 |
| Reviewer（评审者） | 某个领域的写权限，可批准该领域 PR | 领域内 3 个已合并贡献、5 次实质评审、2 个提名 |
| Maintainer（维护者） | 领域合并权、路线图、release manager、治理投票 | 持续的 Reviewer 记录、2 名 Maintainer 提名、多数确认 |
| Steering Committee（指导委员会） | 最终上诉、治理修订、高风险设置 | 由 Maintainer 与 Reviewer 从在任 Maintainer 中选举 |

如果你想从有边界的任务开始，挑一个 `good first issue`，在下面评论，维护者会指派
并解答疑问。不确定从哪里开始时，开一个 Discussion 直接提问。

认可方式是声誉与治理权，不是报酬：角色持有者记录在 `MAINTAINERS.zh-cn.md`，
贡献者在 Release Notes 中署名，非代码工作与代码工作同样署名。退出后可获得
Emeritus 荣休身份，保留署名。

## 评审 Pull Request

评审是积累信任的主要方式，而评审不只是点批准。评审时：

- 关注行为、错误路径与测试，而不只是风格 —— 风格由 linter 负责；
- 说明你验证了什么、没有验证什么；
- 区分阻塞性意见与建议；
- 对 `.github/workflows/`、`/deploy/`、`/build/` 或治理文件的变更，注意
  `GOVERNANCE.zh-cn.md` §6 的双人规则。

## AI 辅助贡献

可以使用 AI 工具编写代码、测试或文档。你对结果负全部责任：正确性、许可证与
测试覆盖都是你的责任。在 Pull Request 中说明使用了哪些工具、哪些部分由人工验证。
AI 不能持有社区角色、不能赞助提名、不能投票；AI 生成内容与其他贡献采用同一评审
标准。

## 许可证

向 Go TaaS 贡献即表示你同意你的贡献按 [Apache License 2.0](./LICENSE) 授权。
