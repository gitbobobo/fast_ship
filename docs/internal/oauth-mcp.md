# OAuth 2.1 授权服务与 MCP 资源（INT-222）

Fast Ship 作为 OAuth 2.1 授权服务器，保护 MCP 资源 `{PublicURL}/mcp`。本篇记录
授权服务核心（数据模型、令牌族、发现端点、失效模型）、`/mcp` 协议端点与 Web
授权面。

实现分布：`server/internal/model/oauth.go`、`server/internal/repository/oauth.go`、
`server/internal/service/oauth.go`、`server/internal/handler/oauth.go`（含 `/mcp`
端点与工具宿主 `service/mcp.go`、鉴权中间件 `middleware/mcp_auth.go`）。路由装配在
`router.Setup`（协议端点挂根路径）与 `cmd/server/main.go`。浏览器授权页是 SPA
路由 `web/src/routes/oauth/authorize.tsx`（未登录经 `?next=` 回跳，登录后选择
项目与读写档）；设置页「已授权的应用」在 `web/src/routes/settings/oauth-apps.tsx`。

## 基址与资源标识

- 所有对外 URL（issuer、发现文档、端点地址）一律基于部署公开基址
  `DeploymentSettingsService.PublicURL(ctx)`（INT-129：数据库非空值 > config.yaml
  回落 > 空串），去尾斜杠后拼装。**不新增任何 config 字段**；未配置 public_url
  时发现端点返回 503。
- 资源标识：`{base}/mcp`（`service.OAuthResourcePath`）。
- issuer：`{base}` 本身；`/.well-known/oauth-authorization-server` 挂在根路径下
  （不在 /api 下），issuer 根路径即可发现。

## 发现端点（公开，不进 openapi.yaml）

| 端点 | 规范 | 内容 |
| --- | --- | --- |
| `GET /.well-known/oauth-protected-resource` | RFC 9728 | `resource`、`authorization_servers:[{base}]`、`scopes_supported` |
| `GET /.well-known/oauth-authorization-server` | RFC 8414 | `issuer`、`authorization_endpoint:{base}/oauth/authorize`、`token_endpoint`、`registration_endpoint`、`response_types:["code"]`、`grant_types:["authorization_code","refresh_token"]`、`code_challenge_methods:["S256"]`、`token_endpoint_auth_methods:["none"]`、`scopes_supported` |

`authorization_endpoint` 指向 `{base}/oauth/authorize`——这是后续 Web 授权页的
浏览器地址；当前已实现的 JSON 预取/提交数据面在 `/api/oauth/authorize`
（JWT，走 Fast Ship envelope，进 openapi.yaml）。

`scopes_supported` 是编码模式提示（`project:<uuid>:read`、`project:<uuid>:write`），
不是可枚举清单——具体 scope 取值依赖用户项目集合，无法预先列出。

## 动态客户端注册（RFC 7591）

`POST /oauth/register`，无认证。仅接受公开客户端：

- `redirect_uris` 非空，每条必须是 https，或 http 回环地址
  （127.0.0.1 / localhost / [::1]，任意端口，RFC 8252），不得带 fragment。
- `token_endpoint_auth_method` 只接受 `"none"`（或缺省）。
- 其余字段（software_id 等）忽略；不存 client_secret。
- 返回 201 + `client_id`（Base62 随机串，即主键）、`client_id_issued_at`、
  `redirect_uris`、`token_endpoint_auth_method:"none"`。
- 错误按 RFC 7591：400 `{"error":"invalid_redirect_uri"|"invalid_client_metadata"}`。

## Scope 编码与权限模型

- `project:<uuid>:read` 与 `project:<uuid>:write`，write 隐含 read
  （`service.ScopeHasRead/ScopeHasWrite`；write 条目只落 write，不重复落 read）。
- 授权页提交选中的项目集合 + 全局读写档（`write` bool），服务端
  `BuildOAuthScopes` 排序去重后算出空格分隔的 scope 串。
- `project_ids` 必须全部属于当前用户且非空（40013/40014），权限不得超出授权集合。
- 重授权（同 user+client 再次授权）覆盖 scope 并复活吊销标记：授权总账一行
  `(user, client)`，`oauth_authorizations` 上有唯一索引；`created_at` 同步刷新为
  最近一次授权时间（「已授权的应用」展示语义）。scope 集合发生变化（含缩权）时，
  同一事务内吊销该授权名下全部旧 access/refresh 令牌并作废未消费旧授权码——
  否则旧令牌仍持变更前 scope，权限超出本次授权档。

## 授权码流程

1. `GET /api/oauth/authorize`（JWT）：校验客户端存在、redirect_uri 与注册精确
   匹配、PKCE 仅 S256、challenge 合法 base64url（43–128 字符）；返回
   `{client_name, projects:[{id,name}]}` 供授权页勾选。
2. `POST /api/oauth/authorize`（JWT）：同一事务里 upsert 授权 + 写授权码
   （10 分钟有效、单次使用、只存 SHA-256 哈希），返回
   `{redirect: "<redirect_uri>?code=..&state=.."}`（state 原样带回）。
   事务闭包只用 tx（单连接池规则，见 auth.md）。
3. `POST /oauth/token`，`grant_type=authorization_code`：client_id 取 form 或
   Basic；校验 PKCE（`BASE64URL(SHA256(verifier))` 与 challenge 常数时间比较）、
   redirect_uri 一致、码未过期未使用；消费码是原子条件更新
   （`used_at IS NULL` 才置位，重放 RowsAffected=0 直接拒绝）且授权未被吊销；
   整个兑换在同一事务内。

## 令牌族

| 令牌 | 前缀 | 有效期 | 存储 |
| --- | --- | --- | --- |
| MCP access token | `fsm_` | 1 小时 | SHA-256（哈希去掉前缀的原文），唯一索引 |
| OAuth refresh token | `fsn_` | 30 天 | 同上 |
| 授权码 | 无前缀 | 10 分钟 | 同上，单次使用 |

- 与既有 `fsk_`（API Key）、`fsr_`（审查 Agent 运行令牌）互不重叠。
- access 行记 `token_version`（签发时用户 token_version 快照）与
  `authorization_id`（吊销联动键）；refresh 行同构。
- token 响应按 RFC 6749 §5.1：`access_token`、`token_type:"Bearer"`、
  `expires_in:3600`、`refresh_token`、`scope`，带 `Cache-Control: no-store`。

## refresh 轮换与栅栏

- `grant_type=refresh_token`：校验未吊销未过期、client 匹配、请求 scope 不超出
  原授权（`invalid_scope`；write 隐含 read，持 `:write` 请求只读属降权不算越界）、
  用户 `token_version` 未变（改密即拒，双保险覆盖
  吊销扫描窗口）。轮换 = 条件吊销旧行（`RowsAffected=1` 才继续，与
  `RefreshTokenRepository.Rotate` 同型栅栏）+ 同事务签发新对；并发轮换/改密
  抢先吊销旧行后整体失败，不产生新行。请求带 scope 时新令牌按请求收窄
  （RFC 6749 §6），不带则沿用原授权 scope。

## 访问令牌校验（资源服务器）

`OAuthService.ValidateAccessToken(ctx, rawToken)`：哈希命中（行查询已过滤
revoked/expired）→ `AuthService.IsTokenVersionCurrent` 通过。返回
`{user_id, client_id, authorization_id, scopes, token_version, expires_at}`，
供后续 `/mcp` 中间件做 scope 级鉴权（write 隐含 read）。

## 失效模型

- **改密联动**：`AuthService.UpdatePassword` → `UpdatePasswordAndRotateSessions`
  的同一事务内（经 `SetOAuthService` 注入的钩子，闭包只用 tx）吊销该用户全部
  OAuth 授权、access 与 refresh 令牌。access 的版本自增机制本身已使快照比对
  失效，refresh 靠显式吊销 + 轮换时的版本比对双保险。未注入 OAuthService 时
  改密不触碰 OAuth 表（既有测试与构造路径不受影响）。
- **吊销授权**：`DELETE /api/oauth/authorizations/:id`（仅本人）：同一事务置
  授权 `revoked_at` 并吊销该 `authorization_id` 下全部令牌，即时生效；清单
  只显示未吊销授权。
- **授权码重放**：原子消费栅栏拒绝，`invalid_grant`。
- 明文令牌/授权码不落日志：日志只记 client_id、user_id、authorization_id 等
  元信息。

## 响应格式约定

- 协议端点（`/oauth/register`、`/oauth/token`、`/.well-known/*`）按 RFC 原生
  格式返回，**不套** Fast Ship envelope，也**不进** openapi.yaml。
- 管理 API（`/api/oauth/authorize` GET/POST、`/api/oauth/authorizations`
  GET/DELETE-by-id）按 Fast Ship envelope，进 openapi.yaml（仅 JWT）。

## /mcp 协议端点（本任务续写）

实现分布：`server/internal/handler/mcp.go`（HTTP 端点）、
`server/internal/service/mcp.go`（工具层与 scope 强制）、
`server/internal/middleware/mcp_auth.go`（Bearer 挑战中间件）。路由装配在
`router.Setup`：`r.Any("/mcp", RequireMCPToken, mcpHandler.Handle)`——静态路径，
与 Web 面板（NoRoute 回落）无冲突。

### SDK 与选型

- `github.com/modelcontextprotocol/go-sdk v1.8.0`（官方 Go SDK；proxy 可拉，
  API 面稳定，Streamable HTTP + 无状态模式原生支持）。
- `mcp.NewStreamableHTTPHandler` 挂 `/mcp`，**无状态模式**（`Stateless: true`）：
  不分配 `Mcp-Session-Id`，每个 POST 经 per-request factory（`svc.NewServer`）
  构造绑定当前用户 scope 的 server，令牌吊销/改密即时生效，也无跨用户
  session 劫持面；GET/DELETE 按 SEP-2567 返回 405。`JSONResponse: true` 让
  工具调用以 application/json 直返，不经 SSE。
- `DisableLocalhostProtection: true`：生产部署通常由同机反代经回环地址转发
  （Host 是公网域名），SDK 默认 DNS rebinding 防护会把这种部署整体 403。
  本端点强制 Bearer 令牌，无令牌请求只得到 401 挑战，浏览器侧 rebinding
  拿不到任何数据，防护收益为零。
- server 名 `fast-ship`（`service.MCPServerName`），版本 `1.0.0`
  （`service/mcp.go` 内独立维护，站内无全局版本常量）。

### 鉴权挑战（RFC 6750）

- `Authorization: Bearer <token>` 缺失或校验失败 → 401；`WWW-Authenticate:
  Bearer resource_metadata="{BaseURL}/.well-known/oauth-protected-resource"`，
  有令牌但无效时附加 `error="invalid_token"`；body 为简短文本
  （`Unauthorized`），**不套** Fast Ship envelope。
- 仅接受 `fsm_` 前缀；其他令牌（含 `fsk_`/JWT）直接按无效处理，不进哈希
  查询，不落日志明文。校验走 `OAuthService.ValidateAccessToken`（scope 行、
  过期、吊销、token_version），通过后把 `OAuthAccessTokenInfo` 注入请求
  context（`service.ContextWithMCPAuth`），工具层经 `MCPAuthFromContext` 取用。

### 第一期工具（`fastship_` 前缀）

命名选 `fastship_`：工具名字符集（字母/数字/`_-.`）允许下划线，与模块语义
同源可读。

| 工具 | 作用 | scope |
| --- | --- | --- |
| `fastship_list_issues` | 按 project 列 issue（state/q/label 过滤 + 分页） | `project:<id>:read` |
| `fastship_get_issue` | Issue 详情 | `project:<id>:read` |
| `fastship_create_issue` | 创建 internal issue（title/body/workflow_status） | `project:<id>:write` |
| `fastship_update_issue` | 更新标题/正文/标签（缺省字段不修改） | `project:<id>:write` |
| `fastship_close_issue` | 关闭（reason 可选 completed/not_planned） | `project:<id>:write` |
| `fastship_reopen_issue` | 重开 | `project:<id>:write` |
| `fastship_add_comment` | 发 issue 评论 | `project:<id>:write` |

- scope 判定：项目 id 从工具入参（list/create）或 issue 归属（其余，经
  `IssueService.Get` 顺带完成归属校验）解析；write 隐含 read。越权返回
  MCP 工具错误（`CallToolResult.IsError`，SDK ToolHandlerFor 惯例），文案
  不含令牌。
- 全部以授权用户身份走 IssueService（与 JWT 同一条链），不走 API Key 路径。
- **硬边界**：不登记进站内机器人动作注册表（INT-195/INT-202），不接入
  机器人工具循环（INT-101），不碰 `bot_*` 文件。

### 已知限制

- 令牌清理：`oauth_access_tokens`/`oauth_refresh_tokens` 吊销与过期行、
  `oauth_authorization_codes` 已用行尚无后台清扫（对齐
  `refreshTokenRepo.CleanExpired` 的做法，待后续任务）。
- 无状态模式不支持 server→client 请求（sampling/roots/elicitation）与独立
  SSE 流；第一期工具均为请求-响应型，不受影响。
- `fastship_list_issues` 的关键词搜索依赖 FTS 表，生产可用；无状态会话不
  保留游标，深翻页按 page/page_size 逐页拉。

## 后续候选（未排期）

- 令牌清理：`oauth_authorization_codes` 过期/已用行与吊销令行的后台清扫
  （对齐 `refreshTokenRepo.CleanExpired` 的做法）。
