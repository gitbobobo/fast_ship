# 认证与会话

两种凭证：JWT（Web 会话，access + refresh 双令牌）和 API Key（`fsk_`，自动化与脚本）。路由层由 `middleware.RequireAuth`（两者皆可）与 `middleware.RequireJWT`（仅 JWT）分流，按 `fsk_` 前缀判断凭证类型。

## 会话失效模型

JWT 校验每个请求做两次查库：

1. `jwt_blacklist`（按 jti）：登出时吊销单个 access token。
2. `users.token_version`（按用户）：改密递增后，此前签发的所有 access token 立即失效。

token_version 在签发时写进 JWT 的 `ver` claim；无 `ver` 的历史 token 按版本 0 处理，机制上线不会造成全员掉线。校验查用户不存在也按失效处理——已删除用户的存量 token 随之失效。

## 改密 = 全量吊销 + 当前会话续签

`PUT /api/auth/password` 成功时在**同一事务**里（`UserRepository.UpdatePasswordAndRotateSessions`）：更新密码哈希、`token_version + 1`、吊销该用户全部未吊销的 refresh token，并写入为当前会话新签发的 refresh token。响应返回新 token 对（`RefreshResponse` 形状），前端必须立即换存——旧凭证此后一律 401。API Key 不受改密影响。

## 事务边界

改密事务内创建新 refresh token 是刻意的：先改密后补发凭证会留下「密码已换但无会话可用」的中间态。单连接池规则（见 database.md）要求闭包内只用 `tx` 句柄，该方法的三个写操作都走 `tx`。
