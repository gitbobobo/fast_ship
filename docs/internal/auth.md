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
- `UpdatePasswordAndRotateSessions` 的 `token_version` 在事务内自增（`token_version + 1`）并回读返回，服务层用回读值签 JWT。不能用调用方预读的 nextVersion 写回——两次并发改密会停在同一版本，先提交者换出的 `ver` token 将保持有效。
- 普通用户写路径只走 `UserRepository.UpdateFields` 按列更新；禁止整行 `Save` 回写——调用方持有的 user 副本可能早于一次已提交的改密，全量回写会把 `token_version`/`password_hash` 覆盖回旧值，复活已吊销会话。

Web 侧镜像约束：改密响应落地前旧凭证已死、新凭证未写回，期间并发请求的旧 token 401 不能走 refresh→logout。`withCredentialRotation` 包住改密调用，`resolveSessionToken` 是 ky 钩子与 XHR 上传共用的恢复出口：窗口内等待轮换，再以 store 当前 token 直接重试。改密请求自身携带 `x-fast-ship-rotation-owner` 标记跳过等待——否则 401 时它等待的正是自己。

## 事务边界

改密事务内创建新 refresh token 是刻意的：先改密后补发凭证会留下「密码已换但无会话可用」的中间态。单连接池规则（见 database.md）要求闭包内只用 `tx` 句柄，该方法的所有读写都走 `tx`。
