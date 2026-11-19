# Local Accounts: Registration, Email Codes and Sign-In

| Attribute | Content |
| --- | --- |
| Feature point | Self-service registration of a local username/password account, with an email verification code and an immediate signed-in session |
| Owning modules | `services/auth` (`CreateUser`, `Login`, `SendEmailCode`, `issueSession`), `web/src/pages/RegisterPage.tsx` |
| API | `POST /api/v1/auth/users`, `POST /api/v1/auth/email-code`, `POST /api/v1/auth/login` |
| Status | Implemented |

---

## 1. Verification codes are single-use and purpose-bound

`SendEmailCode` stores a six-digit code in Redis under
`auth:email-code:<email>:<purpose>`, where the purpose is `register`,
`login`, `change_email` or `reset_password`. A code has a five-minute TTL
and a resend cooldown, and `verifyEmailCode` deletes the key on a match, so
one code authorises exactly one attempt of exactly one purpose. A
registration code cannot authenticate a login, and a consumed code cannot be
replayed.

## 2. Registration signs the account in

`CreateUser`:

1. validates the username and the password policy;
2. rejects a malformed email;
3. when email verification is enabled, requires the email and a `register`
   code and consumes the code;
4. hashes the password with Argon2id and inserts the user;
5. mints a session with the same helper `Login` uses, records `auth.login`,
   and returns `session_token`, `expires_at` and `realm`.

The response therefore carries everything the console needs to enter the
application. The console must **not** call `POST /auth/login` afterwards: the
registration code has just been consumed and carries the `register` purpose,
so that call would fail with 10002 and report "code invalid or expired"
while the account already exists. The console stores the returned
`session_token` under the realm key and routes by `realm`, exactly as it does
after a normal login.

If minting the session fails, the just-created row is deleted on a best-effort
basis and the caller receives the error, so a failed registration does not
leave a half-created account behind.

## 3. API contract

| RPC | Request fields | Response fields |
| --- | --- | --- |
| `CreateUser` | `username`, `password`, `email`, `emailCode` (required when email verification is enabled) | `response`, `userId`, `sessionToken`, `expiresAt`, `realm` |
| `SendEmailCode` | `email`, `purpose` (`register` / `login` / `change_email` / `reset_password`) | `response`, `resendAfterSeconds` |
| `Login` | `username`, `password`, `emailCode` (required when email verification is enabled and the account is not a platform administrator), `captchaId`/`captchaCode` for platform administrators | `response`, `sessionToken`, `expiresAt`, `realm` |

`realm` is always derived from the account's roles, never from a request
field. A freshly registered account has no organization role, so it enters
the user realm.

## 4. Error mapping

| Code | Meaning |
| --- | --- |
| 10002 | Invalid credentials, unknown account, or email code missing / wrong / expired |
| 10003 | User not found (including a missing username or email when verification is on) |
| 10004 | Username or email already exists |
| 10014 | Password policy not met |
| 10037 | Email code requested again inside the resend cooldown |
| 10044 | Malformed email address |
| 10045 | Verification mail could not be sent |

## 5. Tests

- `services/auth/local_account_test.go`: `TestServiceCreateUserIssuesSession`
  asserts the returned session is stored with the new user's id and realm.
- `services/auth/email_code_test.go`: `TestCreateUserRequiresEmailCode` and
  `TestLoginRequiresEmailCode` cover the purpose separation and single use.
- `test/fvt/email_code_fvt_test.go`: `TestFVTEmailCodeRegisterAndLogin`
  registers with a code, uses the returned `sessionToken` to read
  `/api/v1/auth/profile`, and still covers the separate login-code flow.
