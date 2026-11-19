# 本地账号：注册、邮箱验证码与登录

| 属性 | 内容 |
| --- | --- |
| 特性 | 本地用户名/密码账号的自助注册：邮箱验证码校验后直接签发会话 |
| 所属模块 | `services/auth`（`CreateUser`、`Login`、`SendEmailCode`、`issueSession`）、`web/src/pages/RegisterPage.tsx` |
| API | `POST /api/v1/auth/users`、`POST /api/v1/auth/email-code`、`POST /api/v1/auth/login` |
| 状态 | 已实现 |

---

## 1. 验证码一次性且绑定用途

`SendEmailCode` 把六位验证码写入 Redis 的
`auth:email-code:<email>:<purpose>`，用途取 `register`、`login`、
`change_email`、`reset_password`。验证码 5 分钟过期，并有重发冷却；
`verifyEmailCode` 在校验通过后删除该键，因此一个验证码只能用于一次、
且只能用于它签发时的用途。注册验证码不能用于登录，已消费的验证码也不能重放。

## 2. 注册即登录

`CreateUser` 依次：

1. 校验用户名和密码策略；
2. 拒绝格式非法的邮箱；
3. 开启邮箱验证时，要求邮箱和 `register` 验证码，并消费该验证码；
4. 用 Argon2id 哈希密码并写入用户；
5. 调用与 `Login` 相同的会话签发函数，记录 `auth.login`，返回
   `session_token`、`expires_at` 与 `realm`。

因此响应已经包含进入控制台所需的全部信息。前端**不得**在注册成功后再调用
`POST /auth/login`：注册验证码刚刚被消费，且用途是 `register`，这次登录会
返回 10002「验证码错误或已过期」，而账号其实已经创建成功。前端应把响应中的
`session_token` 写入对应 realm 的存储，并按 `realm` 跳转，与正常登录完全一致。

如果会话签发失败，服务端会尽力删除刚创建的用户记录并把错误返回给调用方，
避免留下半注册状态。

## 3. API 契约

| RPC | 请求字段 | 响应字段 |
| --- | --- | --- |
| `CreateUser` | `username`、`password`、`email`、`emailCode`（开启邮箱验证时必填） | `response`、`userId`、`sessionToken`、`expiresAt`、`realm` |
| `SendEmailCode` | `email`、`purpose`（`register` / `login` / `change_email` / `reset_password`） | `response`、`resendAfterSeconds` |
| `Login` | `username`、`password`、`emailCode`（开启邮箱验证且账号不是平台管理员时必填）、平台管理员的 `captchaId`/`captchaCode` | `response`、`sessionToken`、`expiresAt`、`realm` |

`realm` 始终由账号角色推导，不接受请求字段指定。刚注册的账号没有组织角色，因此进入用户端。

## 4. 错误码

| 错误码 | 含义 |
| --- | --- |
| 10002 | 凭据无效、账号不存在，或邮箱验证码缺失/错误/过期 |
| 10003 | 用户不存在（开启验证时也包括缺少用户名或邮箱） |
| 10004 | 用户名或邮箱已存在 |
| 10014 | 密码不满足策略 |
| 10037 | 在重发冷却期内再次请求验证码 |
| 10044 | 邮箱格式非法 |
| 10045 | 验证邮件发送失败 |

## 5. 测试

- `services/auth/local_account_test.go`：`TestServiceCreateUserIssuesSession`
  断言注册返回的会话以新用户 id 和 user realm 写入会话存储。
- `services/auth/email_code_test.go`：`TestCreateUserRequiresEmailCode` 与
  `TestLoginRequiresEmailCode` 覆盖用途隔离与一次性消费。
- `test/fvt/email_code_fvt_test.go`：`TestFVTEmailCodeRegisterAndLogin`
  用验证码完成注册后，直接用返回的 `sessionToken` 读取
  `/api/v1/auth/profile`，同时保留独立的登录验证码流程。
