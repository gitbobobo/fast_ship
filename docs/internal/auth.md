# 认证与会话

两种凭证：JWT（Web 会话，access + refresh 双令牌）和 API Key（`fsk_`，自动化与脚本）。路由层由 `middleware.RequireAuth`（两者皆可）与 `middleware.RequireJWT`（仅 JWT）分流，按 `fsk_` 前缀判断凭证类型。

## 会话失效模型

JWT 校验每个请求做两次查库：

1. `jwt_blacklist`（按 jti）：登出时吊销单个 access token。
2. `users.token_version`（按用户）：改密递增后，此前签发的所有 access token 立即失效。

token_version 在签发时写进 JWT 的 `ver` claim；无 `ver` 的历史 token 按版本 0 处理，机制上线不会造成全员掉线。校验查用户不存在也按失效处理——已删除用户的存量 token 随之失效。

## 改密 = 全量吊销 + 当前会话续签

`PUT /api/auth/password` 成功时在**同一事务**里（`UserRepository.UpdatePasswordAndRotateSessions`）：更新密码哈希、`token_version + 1`、吊销该用户全部未吊销的 refresh token，并写入为当前会话新签发的 refresh token。响应返回新 token 对（`RefreshResponse` 形状），前端必须立即换存——旧凭证此后一律 401。API Key 不受改密影响。

## 签发栅栏

改密不是唯一的写方：并发的登录/刷新可能在旧密码、旧 refresh token 尚有效时通过校验，而在改密事务提交后才把新凭证落库——漏出扫描的 refresh token 仍能换出活 token。两处签发因此都有栅栏：

- `RefreshTokenRepository.CreateIfTokenVersion`：登录/注册写入 refresh token 前，在事务内核对 `token_version` 仍等于验证时读到的版本，变了就放弃写入。
- `RefreshTokenRepository.Rotate`：刷新轮换先条件吊销旧行（`RowsAffected=1` 才继续）再补发后继；旧行若已被改密扫描吊销，旋转直接中止，不产生新行。
- `UpdatePasswordAndRotateSessions` 以验证过的旧密码哈希为写谓词（`WHERE id AND password_hash`）：旧密码 bcrypt 校验在事务外，两次并发改密中后提交者持有的已是作废证明，谓词不命中即 `ErrPasswordChanged` 整体回滚，不得推进版本、吊销会话或补发凭证。
- `UpdatePasswordAndRotateSessions` 的 `token_version` 在事务内自增（`token_version + 1`）并回读返回，服务层用回读值签 JWT。不能用调用方预读的 nextVersion 写回——两次并发改密会停在同一版本，先提交者换出的 `ver` token 将保持有效。
- 普通用户写路径只走 `UserRepository.UpdateFields` 按列更新；禁止整行 `Save` 回写——调用方持有的 user 副本可能早于一次已提交的改密，全量回写会把 `token_version`/`password_hash` 覆盖回旧值，复活已吊销会话。

Web 侧镜像约束：改密响应落地前旧凭证已死、新凭证未写回，期间并发请求的旧 token 401 不能走 refresh→logout。`withCredentialRotation` 包住改密调用，`resolveSessionToken` 是 ky 钩子与 XHR 上传共用的恢复出口：窗口内等待轮换，再以 store 当前 token 直接重试。改密请求自身携带 `x-fast-ship-rotation-owner` 标记跳过等待——否则 401 时它等待的正是自己。凭证写方是 CAS：先于窗口启动的 refresh 若迟到落地，`tryRefreshToken` 比对 store 中 refresh token 仍是发起时那个才写入（否则丢弃响应、取回当前 token）；刷新失败时 `resolveSessionToken` 重查 store 再决定是否放行 logout。

## 事务边界

改密事务内创建新 refresh token 是刻意的：先改密后补发凭证会留下「密码已换但无会话可用」的中间态。单连接池规则（见 database.md）要求闭包内只用 `tx` 句柄，该方法的所有读写都走 `tx`。

## 角色与邀请注册（INT-129）

`users.role` 只有两个取值：`admin` / `member`（`model.UserRoleAdmin` / `UserRoleMember`，列默认 `member`）。
它**不写进 JWT**：`/api/auth/me` 返回 `role` 供前端隐藏入口，服务端每次请求现查数据库
（`middleware.RequireAdmin` 挂在 `RequireJWT` 之后，用 `AuthService.RoleOf` 查一次，非 admin 返回
40305）。把角色放进令牌会让「刚被降级的管理员」在令牌过期前继续有效。

首次开通：

- 全新库：第一个注册成功的用户 `role = admin`（注册事务内 `count(users) == 0` 判定）。
- 存量库升级：`cmd/server/migrate_user_roles.go` 在 AutoMigrate 之后跑一次，没有任何 admin
  时把 `created_at` 最早的用户提为 admin；幂等，重复启动无副作用。

### 关闭公开注册，改为仅管理员邀请

库里已有用户后，`POST /api/auth/register` 必须携带有效的 `invite_token`：

- 邀请只有 `MemberService.CreateInvite` 的返回值里出现一次明文，库里存 SHA-256（`token_hash` 唯一）。
- 有效期缺省 72 小时，可传 1..720（越界 40001）。管理员可随时撤销。
- 角色与邀请消费在**同一事务**内完成：`resolveInviteRole` 校验（不存在/过期/撤销统一 40432，
  不泄漏状态；已被使用 40932），再用 `ConsumeTx`（`WHERE used_at IS NULL AND revoked_at IS NULL`）
  条件置为已使用。条件更新未命中说明并发注册抢先消费了同一邀请，返回 40932 并整体回滚。
- 没有令牌或没有可用邀请 → 40306「公开注册已关闭，请联系管理员获取邀请链接」。
- 被邀请人固定 `role = member`。
- `POST /api/auth/invites/validate`（体 `{token}`）供注册页提前显示邀请状态（有效 → 200；无效/过期/撤销/已用 → 40432）。
  token 走请求体而不是 URL 路径：请求日志会原样记录 `URL.Path`，而邀请明文是无前缀随机串，
  凭证脱敏规则匹配不到，放路径里等于把可用邀请写进日志库。

### 成员管理

`GET /api/members`、`PUT /api/members/{uid}/role`、`GET|POST /api/invites`、`DELETE /api/invites/{id}`
全部要求管理员（`RequireJWT` + `RequireAdmin`）。改角色在事务内判定「最后一个管理员不能降级」
（`count(role='admin') == 1` 且目标当前是 admin → 40931）：系统里永远至少留一个能管理部署配置的人。

管理员的边界：成员管理、部署设置（`public_url`）、GitHub App 的保存/测试/manifest 创建。
读侧（`GET /api/deployment/settings`、`GET /api/deployment/github-app`）对全部登录用户开放，
成员需要看到 App 状态与安装链接。

