<!-- 由 server/cmd/apidocgen 从 server/api/openapi.yaml 生成，请勿手改 -->

# Fast Ship API

Fast Ship 项目管理工具的 HTTP API。覆盖 `server/internal/router/router.go` 注册的全部 /api 路由；
SPA 静态资源与 NoRoute 回退（`web.go`）不在本文档范围。

约定：
- 所有 JSON 响应统一信封 `{code, message, data}`：`code=0` 成功；非 0 为业务错误码，`data` 为 null。
- 认证方式为 `Authorization: Bearer <token>`；token 为 JWT（登录/注册签发）或 `fsk_` 开头的 API Key，服务端按前缀区分。
- 部分二进制资源端点（头像、Issue 图片、安装包下载、GitHub 媒体代理）额外接受 `?token=<token>` query 凭证。
- 除特别注明外，字段级权限写在各字段 description 中：「仅 JWT」表示 API Key 携带该字段会返回 403（40301）；「仅 API Key」表示 JWT 调用返回 403（40303）。

各端点小节只展开 `data` 的形状，不再重复 `code` 与 `message`。

## auth

注册、登录、Token 与当前用户

### POST `/api/auth/register`

<!-- operationId: register -->

注册新用户

**鉴权**：公开

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `username` | string（长度 2..50） | 是 | 用户名，2-50 字符，全局唯一 |
| `email` | string（email） | 是 | 邮箱，全局唯一 |
| `password` | string（长度 ≥8） | 是 | 密码，≥8 字符 |

**成功响应**

**200** 注册成功，返回 token 与用户信息

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 是 | JWT access token（HS256，有效期见服务端 jwt.expire_hours，默认 24h） |
| `refresh_token` | string | 是 | fsr_ 开头，有效期默认 7 天 |
| `user` | object | 是 |  |
| `user.id` | string | 是 |  |
| `user.username` | string | 是 |  |
| `user.email` | string | 是 |  |
| `user.avatar_url` | string | 是 | 形如 /api/avatars/:uid/:filename，未设置为空串 |
| `user.created_at` | string（date-time） | 是 |  |
| `user.updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 409 | 业务冲突（40900-40999） |
| 500 | 服务器内部错误（50000） |

### POST `/api/auth/login`

<!-- operationId: login -->

登录（用户名或邮箱）

**鉴权**：公开

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `login` | string | 是 | 用户名或邮箱 |
| `password` | string | 是 |  |

**成功响应**

**200** 登录成功，返回 token 与用户信息

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 是 | JWT access token（HS256，有效期见服务端 jwt.expire_hours，默认 24h） |
| `refresh_token` | string | 是 | fsr_ 开头，有效期默认 7 天 |
| `user` | object | 是 |  |
| `user.id` | string | 是 |  |
| `user.username` | string | 是 |  |
| `user.email` | string | 是 |  |
| `user.avatar_url` | string | 是 | 形如 /api/avatars/:uid/:filename，未设置为空串 |
| `user.created_at` | string（date-time） | 是 |  |
| `user.updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 用户名/邮箱或密码错误（40103） |
| 500 | 服务器内部错误（50000） |

### POST `/api/auth/refresh`

<!-- operationId: refreshToken -->

刷新 access token（轮换 refresh token）

**鉴权**：公开

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `refresh_token` | string | 是 | fsr_ 开头的 refresh token |

**成功响应**

**200** 返回新的 token 与新的 refresh token（旧 refresh token 被轮换）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 是 |  |
| `refresh_token` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | Refresh Token 无效或已过期（40105） |
| 500 | 服务器内部错误（50000） |

### POST `/api/auth/logout`

<!-- operationId: logout -->

登出（将当前 JWT 加入黑名单，可选同时吊销 refresh token）

**鉴权**：JWT+API Key

**请求体**

`application/json`，可选。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `refresh_token` | string | 否 | 可选，fsr_ 开头的 refresh token，传入则一并吊销 |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### GET `/api/auth/me`

<!-- operationId: getMe -->

获取当前用户信息

**鉴权**：JWT+API Key

**成功响应**

**200** 当前用户信息

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `username` | string | 是 |  |
| `email` | string | 是 |  |
| `avatar_url` | string | 是 | 形如 /api/avatars/:uid/:filename，未设置为空串 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/auth/me`

<!-- operationId: updateMe -->

更新当前用户资料（用户名/邮箱）

**鉴权**：JWT+API Key

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `username` | string（长度 2..50） | 否 | 新用户名；与现值相同或省略则不变 |
| `email` | string（email） | 否 | 新邮箱 |

**成功响应**

**200** 更新后的用户信息

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `username` | string | 是 |  |
| `email` | string | 是 |  |
| `avatar_url` | string | 是 | 形如 /api/avatars/:uid/:filename，未设置为空串 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 409 | 业务冲突（40900-40999） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/auth/password`

<!-- operationId: updatePassword -->

修改密码

**鉴权**：JWT+API Key

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `old_password` | string | 是 |  |
| `new_password` | string（长度 ≥8） | 是 |  |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 旧密码错误（40103）或凭证无效 |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/auth/avatar`

<!-- operationId: uploadAvatar -->

上传头像（multipart/form-data，字段名 file；≤5MB，jpg/png/gif/webp，会嗅探内容必须为图片）

**鉴权**：JWT+API Key

**请求体**

`multipart/form-data`，必填。表单字段，不是 JSON。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | binary | 是 | 图片文件，≤5MB，扩展名限 jpg/jpeg/png/gif/webp 且内容须为图片 |

**成功响应**

**200** 更新后的用户信息（avatar_url 指向 /api/avatars/:uid/:filename）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `username` | string | 是 |  |
| `email` | string | 是 |  |
| `avatar_url` | string | 是 | 形如 /api/avatars/:uid/:filename，未设置为空串 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

## users

用户资源（头像等）

### GET `/api/avatars/{uid}/{filename}`

<!-- operationId: getAvatar -->

获取用户头像图片

返回头像二进制内容（image/*），带 `Cache-Control: private, max-age=3600`。支持 Authorization 头或 `?token=` query 凭证。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `uid` | string | 是 | 用户 ID（不得包含 `..` 或 `/`） |
| `filename` | string | 是 | 头像文件名（不得包含 `..` 或 `/`） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 否 | 可选 query 凭证（JWT 或 `fsk_` API Key），供无法携带 Authorization 头的场景（`<img>`、浏览器直链下载）。与 Authorization 头二选一，头优先。 |

**成功响应**

**200** 头像图片二进制内容

二进制内容（application/octet-stream、image/gif、image/jpeg、image/png、image/webp），无 JSON 信封。

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

## ai

AI 设置、标题/清单生成与全局提示词

### GET `/api/ai/settings`

<!-- operationId: getAISettings -->

获取 AI 设置

**鉴权**：仅 JWT

**成功响应**

**200** AI 设置（api_key 永不回显，configured 表示是否已配置）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `api_host` | string | 是 | 默认 https://api.minimaxi.com |
| `model` | string | 是 | 默认 MiniMax-M2.5 |
| `configured` | boolean | 是 | 是否已配置 API Key（Key 本身永不回显） |
| `updated_at` | string（date-time） | 否 | omitempty，未配置时缺省 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/ai/settings`

<!-- operationId: updateAISettings -->

更新 AI 设置

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `api_host` | string | 否 | 缺省回退默认 host；须为合法 URL |
| `api_key` | string | 否 | 明文 Key（加密存储，不回显）；首次配置必填，已有配置时空串保留旧值 |
| `model` | string | 否 | 缺省回退默认模型 |

**成功响应**

**200** 更新后的 AI 设置

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `api_host` | string | 是 | 默认 https://api.minimaxi.com |
| `model` | string | 是 | 默认 MiniMax-M2.5 |
| `configured` | boolean | 是 | 是否已配置 API Key（Key 本身永不回显） |
| `updated_at` | string（date-time） | 否 | omitempty，未配置时缺省 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### POST `/api/ai/generate-title`

<!-- operationId: generateIssueTitle -->

根据正文生成 3 个候选标题

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `body` | string | 是 | 正文内容；trim 后须 ≥10 个字符（rune），服务端截断至 10000 rune |

**成功响应**

**200** 候选标题列表

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `titles` | string[] | 是 | 候选标题（通常 3 个） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 未配置 AI 设置（40407） |
| 502 | AI 服务调用失败（50201） |

### GET `/api/issue-prompts`

<!-- operationId: getIssuePrompts -->

获取全局问题提示词配置

**鉴权**：仅 JWT

**成功响应**

**200** 提示词列表；未配置时 prompts 为 null（前端兜底默认提示词）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `prompts` | object[]（可空） | 是 | 未配置时为 null，前端兜底默认提示词 |
| `prompts[].id` | string | 是 | trim 后非空 |
| `prompts[].name` | string | 是 | trim 后非空 |
| `prompts[].content` | string | 是 | 提示词正文；trim 后非空 |
| `prompts[].supports_batch` | boolean | 否 | 是否支持批量场景 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/issue-prompts`

<!-- operationId: updateIssuePrompts -->

更新全局问题提示词配置（整体覆盖）

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `prompts` | object[]（≥1 项） | 是 |  |
| `prompts[].id` | string | 是 | trim 后非空 |
| `prompts[].name` | string | 是 | trim 后非空 |
| `prompts[].content` | string | 是 | 提示词正文；trim 后非空 |
| `prompts[].supports_batch` | boolean | 否 | 是否支持批量场景 |

**成功响应**

**200** 更新后的提示词列表

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `prompts` | object[]（可空） | 是 | 未配置时为 null，前端兜底默认提示词 |
| `prompts[].id` | string | 是 | trim 后非空 |
| `prompts[].name` | string | 是 | trim 后非空 |
| `prompts[].content` | string | 是 | 提示词正文；trim 后非空 |
| `prompts[].supports_batch` | boolean | 否 | 是否支持批量场景 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### POST `/api/issues/{iid}/checklist-suggestions` —— 见 `issues` 章

## api-keys

API Key 管理（仅 JWT）

### GET `/api/api-keys`

<!-- operationId: listApiKeys -->

列出当前用户的 API Key

**鉴权**：仅 JWT

**成功响应**

**200** API Key 列表（key 字段不回显）

`data` 为数组。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `user_id` | string | 是 |  |
| `name` | string | 是 |  |
| `key_prefix` | string | 是 | Key 原文前 8 字符（不含 fsk_ 前缀），用于列表辨识 |
| `key_hash` | string | 是 | Key 的 SHA-256 哈希 |
| `last_used_at` | string（date-time，可空） | 是 |  |
| `created_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### POST `/api/api-keys`

<!-- operationId: createApiKey -->

创建 API Key

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string（长度 1..100） | 是 |  |

**成功响应**

**200** 创建成功；完整 API Key（fsk_ 前缀）仅在本次响应的 key 字段中出现一次

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `user_id` | string | 是 |  |
| `name` | string | 是 |  |
| `key_prefix` | string | 是 | Key 原文前 8 字符（不含 fsk_ 前缀），用于列表辨识 |
| `key_hash` | string | 是 | Key 的 SHA-256 哈希 |
| `last_used_at` | string（date-time，可空） | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `key` | string | 否 | 完整 API Key（fsk_ 前缀），仅创建响应中出现一次，服务端不存原文 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/api-keys/{id}`

<!-- operationId: deleteApiKey -->

删除 API Key

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

## projects

项目 CRUD 与 GitHub 分支

### GET `/api/projects`

<!-- operationId: listProjects -->

分页列出当前用户的项目

**鉴权**：JWT+API Key

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `page` | integer（≥1，默认 1） | 否 | 页码，<1 按 1 处理 |
| `page_size` | integer（1..100，默认 20） | 否 | 每页条数；越界（<1 或 >100）回落为 20 |

**成功响应**

**200** 项目分页列表

`data`：

分页响应 data 形状；与 Envelope allOf 组合后 items 由兄弟 schema 收窄为具体类型

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].name` | string | 是 |  |
| `items[].description` | string | 是 |  |
| `items[].github_owner` | string | 是 |  |
| `items[].github_repo` | string | 是 |  |
| `items[].latest_version` | object | 否 | omitempty，仅在列表项中出现 |
| `items[].latest_version.id` | string | 是 |  |
| `items[].latest_version.version_number` | string | 是 |  |
| `items[].latest_version.status` | string（enum: pending \| shipped） | 是 |  |
| `items[].latest_version.created_at` | string（date-time） | 是 |  |
| `items[].issue_sync` | object | 否 | omitempty，项目 GitHub 同步状态 |
| `items[].issue_sync.status` | string（enum: idle \| running \| failed \| completed） | 是 |  |
| `items[].issue_sync.last_issue_updated_at` | string（date-time） | 否 | omitempty |
| `items[].issue_sync.last_synced_at` | string（date-time） | 否 | omitempty |
| `items[].issue_sync.last_successful_sync_at` | string（date-time） | 否 | omitempty |
| `items[].issue_sync.last_error` | string | 是 |  |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |
| `total` | integer（int64） | 是 | 符合条件的总条数 |
| `page` | integer | 是 |  |
| `page_size` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### POST `/api/projects`

<!-- operationId: createProject -->

创建项目（仅 JWT）

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string（长度 1..100） | 是 | 项目名，当前用户下唯一 |
| `description` | string | 否 |  |
| `repository_url` | string | 否 | GitHub 仓库链接（owner/repo 或完整 URL）；设置时须同时提供 github_token 或 source_project_id |
| `github_token` | string | 否 | GitHub PAT（加密存储，不回显） |
| `source_project_id` | string | 否 | 复用另一项目的 GitHub Token；优先于 github_token |

**成功响应**

**200** 创建的项目

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `name` | string | 是 |  |
| `description` | string | 是 |  |
| `github_owner` | string | 是 |  |
| `github_repo` | string | 是 |  |
| `latest_version` | object | 否 | omitempty，仅在列表项中出现 |
| `latest_version.id` | string | 是 |  |
| `latest_version.version_number` | string | 是 |  |
| `latest_version.status` | string（enum: pending \| shipped） | 是 |  |
| `latest_version.created_at` | string（date-time） | 是 |  |
| `issue_sync` | object | 否 | omitempty，项目 GitHub 同步状态 |
| `issue_sync.status` | string（enum: idle \| running \| failed \| completed） | 是 |  |
| `issue_sync.last_issue_updated_at` | string（date-time） | 否 | omitempty |
| `issue_sync.last_synced_at` | string（date-time） | 否 | omitempty |
| `issue_sync.last_successful_sync_at` | string（date-time） | 否 | omitempty |
| `issue_sync.last_error` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 业务冲突（40900-40999） |
| 500 | 服务器内部错误（50000） |

### GET `/api/projects/{id}`

<!-- operationId: getProject -->

获取项目详情

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 项目详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `name` | string | 是 |  |
| `description` | string | 是 |  |
| `github_owner` | string | 是 |  |
| `github_repo` | string | 是 |  |
| `latest_version` | object | 否 | omitempty，仅在列表项中出现 |
| `latest_version.id` | string | 是 |  |
| `latest_version.version_number` | string | 是 |  |
| `latest_version.status` | string（enum: pending \| shipped） | 是 |  |
| `latest_version.created_at` | string（date-time） | 是 |  |
| `issue_sync` | object | 否 | omitempty，项目 GitHub 同步状态 |
| `issue_sync.status` | string（enum: idle \| running \| failed \| completed） | 是 |  |
| `issue_sync.last_issue_updated_at` | string（date-time） | 否 | omitempty |
| `issue_sync.last_synced_at` | string（date-time） | 否 | omitempty |
| `issue_sync.last_successful_sync_at` | string（date-time） | 否 | omitempty |
| `issue_sync.last_error` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/projects/{id}`

<!-- operationId: updateProject -->

更新项目（仅 JWT）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string（长度 1..100） | 否 |  |
| `description` | string | 否 |  |
| `repository_url` | string | 否 | 同创建；变更仓库且项目无 token 时须提供 token |
| `github_token` | string | 否 |  |
| `source_project_id` | string | 否 |  |

**成功响应**

**200** 更新后的项目

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `name` | string | 是 |  |
| `description` | string | 是 |  |
| `github_owner` | string | 是 |  |
| `github_repo` | string | 是 |  |
| `latest_version` | object | 否 | omitempty，仅在列表项中出现 |
| `latest_version.id` | string | 是 |  |
| `latest_version.version_number` | string | 是 |  |
| `latest_version.status` | string（enum: pending \| shipped） | 是 |  |
| `latest_version.created_at` | string（date-time） | 是 |  |
| `issue_sync` | object | 否 | omitempty，项目 GitHub 同步状态 |
| `issue_sync.status` | string（enum: idle \| running \| failed \| completed） | 是 |  |
| `issue_sync.last_issue_updated_at` | string（date-time） | 否 | omitempty |
| `issue_sync.last_synced_at` | string（date-time） | 否 | omitempty |
| `issue_sync.last_successful_sync_at` | string（date-time） | 否 | omitempty |
| `issue_sync.last_error` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 业务冲突（40900-40999） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/projects/{id}`

<!-- operationId: deleteProject -->

删除项目（仅 JWT；级联删除版本、Issue、文档、日志等子资源）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/projects/{id}/branches`

<!-- operationId: getProjectBranches -->

列出项目关联 GitHub 仓库的分支

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 分支列表与默认分支

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `branches` | object[] | 是 |  |
| `branches[].name` | string | 是 |  |
| `branches[].sha` | string | 是 |  |
| `branches[].default` | boolean | 是 |  |
| `default_branch` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 项目未关联 GitHub 仓库（40003） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 502 | GitHub API 调用失败（50200） |

## versions

版本 CRUD、前置校验与发货

### GET `/api/projects/{id}/versions`

<!-- operationId: listVersions -->

分页列出项目版本

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `status` | string（enum: pending \| shipped） | 否 | 按版本状态过滤 |
| `page` | integer（≥1，默认 1） | 否 | 页码，<1 按 1 处理 |
| `page_size` | integer（1..100，默认 20） | 否 | 每页条数；越界（<1 或 >100）回落为 20 |

**成功响应**

**200** 版本分页列表

`data`：

分页响应 data 形状；与 Envelope allOf 组合后 items 由兄弟 schema 收窄为具体类型

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].version_number` | string | 是 |  |
| `items[].status` | string（enum: pending \| shipped） | 是 |  |
| `items[].release_notes` | string | 是 |  |
| `items[].target_commitish` | string | 是 |  |
| `items[].github_release_url` | string | 是 | 发货成功后写入 |
| `items[].error_log` | string | 是 |  |
| `items[].ship_status` | string（enum: "" \| in_progress \| failed \| completed） | 是 | 空串表示尚未发货（idle） |
| `items[].ship_stage` | string（enum: "" \| precheck \| create_tag \| create_release \| upload_assets \| finalize） | 是 | 空串表示未进入发货流程 |
| `items[].ship_message` | string | 是 |  |
| `items[].ship_hooks_status` | string（enum: pending \| completed \| failed \| incomplete） | 是 | 该次发货触发的 issue 钩子执行汇总状态 |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].shipped_at` | string（date-time，可空） | 是 |  |
| `items[].artifacts` | object[] | 否 | omitempty，无安装包时缺省 |
| `items[].artifacts[].id` | string | 是 |  |
| `items[].artifacts[].version_id` | string | 是 |  |
| `items[].artifacts[].file_name` | string | 是 |  |
| `items[].artifacts[].file_size` | integer（int64） | 是 |  |
| `items[].artifacts[].platform` | string | 是 |  |
| `items[].artifacts[].uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `items[].artifacts[].uploaded_at` | string（date-time） | 是 |  |
| `total` | integer（int64） | 是 | 符合条件的总条数 |
| `page` | integer | 是 |  |
| `page_size` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/projects/{id}/versions`

<!-- operationId: createVersion -->

创建版本（仅 JWT）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `version_number` | string | 是 | 版本号（同 tag 名），项目内唯一 |
| `release_notes` | string | 否 | Release 说明；发货前必填 |
| `target_commitish` | string | 否 | 目标分支；非空时校验为仓库已存在分支 |

**成功响应**

**200** 创建的版本

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `version_number` | string | 是 |  |
| `status` | string（enum: pending \| shipped） | 是 |  |
| `release_notes` | string | 是 |  |
| `target_commitish` | string | 是 |  |
| `github_release_url` | string | 是 | 发货成功后写入 |
| `error_log` | string | 是 |  |
| `ship_status` | string（enum: "" \| in_progress \| failed \| completed） | 是 | 空串表示尚未发货（idle） |
| `ship_stage` | string（enum: "" \| precheck \| create_tag \| create_release \| upload_assets \| finalize） | 是 | 空串表示未进入发货流程 |
| `ship_message` | string | 是 |  |
| `ship_hooks_status` | string（enum: pending \| completed \| failed \| incomplete） | 是 | 该次发货触发的 issue 钩子执行汇总状态 |
| `created_at` | string（date-time） | 是 |  |
| `shipped_at` | string（date-time，可空） | 是 |  |
| `artifacts` | object[] | 否 | omitempty，无安装包时缺省 |
| `artifacts[].id` | string | 是 |  |
| `artifacts[].version_id` | string | 是 |  |
| `artifacts[].file_name` | string | 是 |  |
| `artifacts[].file_size` | integer（int64） | 是 |  |
| `artifacts[].platform` | string | 是 |  |
| `artifacts[].uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `artifacts[].uploaded_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001）或目标分支不存在（40002） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 版本号已存在（40903） |
| 502 | GitHub API 调用失败（50200） |

### GET `/api/versions/{vid}`

<!-- operationId: getVersion -->

获取版本详情

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `vid` | string | 是 | 版本 ID（UUID） |

**成功响应**

**200** 版本详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `version_number` | string | 是 |  |
| `status` | string（enum: pending \| shipped） | 是 |  |
| `release_notes` | string | 是 |  |
| `target_commitish` | string | 是 |  |
| `github_release_url` | string | 是 | 发货成功后写入 |
| `error_log` | string | 是 |  |
| `ship_status` | string（enum: "" \| in_progress \| failed \| completed） | 是 | 空串表示尚未发货（idle） |
| `ship_stage` | string（enum: "" \| precheck \| create_tag \| create_release \| upload_assets \| finalize） | 是 | 空串表示未进入发货流程 |
| `ship_message` | string | 是 |  |
| `ship_hooks_status` | string（enum: pending \| completed \| failed \| incomplete） | 是 | 该次发货触发的 issue 钩子执行汇总状态 |
| `created_at` | string（date-time） | 是 |  |
| `shipped_at` | string（date-time，可空） | 是 |  |
| `artifacts` | object[] | 否 | omitempty，无安装包时缺省 |
| `artifacts[].id` | string | 是 |  |
| `artifacts[].version_id` | string | 是 |  |
| `artifacts[].file_name` | string | 是 |  |
| `artifacts[].file_size` | integer（int64） | 是 |  |
| `artifacts[].platform` | string | 是 |  |
| `artifacts[].uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `artifacts[].uploaded_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/versions/{vid}`

<!-- operationId: updateVersion -->

更新版本（仅 pending 状态可改）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `vid` | string | 是 | 版本 ID（UUID） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `version_number` | string | 否 | 仅 JWT 可改；API Key 携带返回 40301。非空且项目内唯一 |
| `release_notes` | string | 否 |  |
| `target_commitish` | string | 否 | 非空时校验为仓库已存在分支；空串清除 |

**成功响应**

**200** 更新后的版本

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `version_number` | string | 是 |  |
| `status` | string（enum: pending \| shipped） | 是 |  |
| `release_notes` | string | 是 |  |
| `target_commitish` | string | 是 |  |
| `github_release_url` | string | 是 | 发货成功后写入 |
| `error_log` | string | 是 |  |
| `ship_status` | string（enum: "" \| in_progress \| failed \| completed） | 是 | 空串表示尚未发货（idle） |
| `ship_stage` | string（enum: "" \| precheck \| create_tag \| create_release \| upload_assets \| finalize） | 是 | 空串表示未进入发货流程 |
| `ship_message` | string | 是 |  |
| `ship_hooks_status` | string（enum: pending \| completed \| failed \| incomplete） | 是 | 该次发货触发的 issue 钩子执行汇总状态 |
| `created_at` | string（date-time） | 是 |  |
| `shipped_at` | string（date-time，可空） | 是 |  |
| `artifacts` | object[] | 否 | omitempty，无安装包时缺省 |
| `artifacts[].id` | string | 是 |  |
| `artifacts[].version_id` | string | 是 |  |
| `artifacts[].file_name` | string | 是 |  |
| `artifacts[].file_size` | integer（int64） | 是 |  |
| `artifacts[].platform` | string | 是 |  |
| `artifacts[].uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `artifacts[].uploaded_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001）或目标分支不存在（40002） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | API Key 修改 version_number（40301）或非资源所有者（40302） |
| 404 | 资源不存在（40400-40499） |
| 409 | 版本非 pending 状态（40904）或版本号冲突（40903） |
| 502 | GitHub API 调用失败（50200） |

### DELETE `/api/versions/{vid}`

<!-- operationId: deleteVersion -->

删除版本（仅 JWT；仅 pending 状态可删）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `vid` | string | 是 | 版本 ID（UUID） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 版本非 pending 状态（40904） |
| 500 | 服务器内部错误（50000） |

### GET `/api/versions/{vid}/ship-check`

<!-- operationId: shipCheck -->

发货前置校验（仅 JWT）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `vid` | string | 是 | 版本 ID（UUID） |

**成功响应**

**200** 校验项结果与待执行 issue 钩子

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `can_ship` | boolean | 是 |  |
| `items` | object[] | 是 |  |
| `items[].key` | string | 是 | release_notes / artifacts / target_commitish / github_config |
| `items[].label` | string | 是 |  |
| `items[].ok` | boolean | 是 |  |
| `items[].detail` | string | 否 | omitempty |
| `pending_issue_hooks` | object[] | 是 |  |
| `pending_issue_hooks[].issue_id` | string | 是 |  |
| `pending_issue_hooks[].reference` | string | 是 | INT-n 或 GH-n |
| `pending_issue_hooks[].title` | string | 是 |  |
| `pending_issue_hooks[].comment` | boolean | 是 |  |
| `pending_issue_hooks[].close` | boolean | 是 |  |
| `pending_issue_hooks[].workflow_enabled` | boolean | 是 |  |
| `pending_issue_hooks[].workflow_status` | string | 是 | 目标工作流状态，可能为 ""（重置） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 版本非 pending 状态（40904） |
| 500 | 服务器内部错误（50000） |

### POST `/api/versions/{vid}/ship`

<!-- operationId: shipVersion -->

执行发货（仅 JWT；创建 tag、release、上传安装包并触发 issue 钩子）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `vid` | string | 是 | 版本 ID（UUID） |

**成功响应**

**200** 发货钩子执行汇总

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `hook_total` | integer | 是 |  |
| `hook_failed` | integer | 是 |  |
| `hook_status` | string | 是 | completed / failed / incomplete |
| `hook_error` | string | 否 | omitempty |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 版本非 pending 状态（40904）或前置校验未通过（40906） |
| 500 | 服务器内部错误（50000） |
| 502 | GitHub 操作失败（50200-50202） |

## issues

Issue CRUD、过滤、同步、清单、发货钩子与图片附件

### GET `/api/issues/assets/{aid}/content`

<!-- operationId: getIssueAssetContent -->

获取 Issue 图片附件内容

返回附件二进制内容（实际 mime type，带 `Cache-Control: private, max-age=300`）。草稿附件与已挂载附件均可。同一路径另注册 HEAD 变体，语义同 GET 但不返回 body。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `aid` | string | 是 | 附件 ID（IssueAsset 或 IssueDraftAsset 的 UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 否 | 可选 query 凭证（JWT 或 `fsk_` API Key），供无法携带 Authorization 头的场景（`<img>`、浏览器直链下载）。与 Authorization 头二选一，头优先。 |

**成功响应**

**200** 附件二进制内容

二进制内容（application/octet-stream），无 JSON 信封。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### HEAD `/api/issues/assets/{aid}/content`

<!-- operationId: getIssueAssetContentHead -->

获取 Issue 图片附件头信息（HEAD）

与 GET 语义相同但不返回 body。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `aid` | string | 是 |  |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 否 | 可选 query 凭证（JWT 或 `fsk_` API Key），供无法携带 Authorization 头的场景（`<img>`、浏览器直链下载）。与 Authorization 头二选一，头优先。 |

**成功响应**

**200** 仅响应头

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/projects/{id}/issues`

<!-- operationId: listIssues -->

分页列出项目 Issue（服务端内存全量过滤后分页）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `state` | string（enum: open \| closed） | 否 | 按 open/closed 过滤 |
| `q` | string | 否 | 关键词过滤（内存全量匹配）：匹配标题、正文、作者 login、`INT-123`/`GH-123` 编号，纯数字时还会匹配 sequence_number 或 GitHub number |
| `label` | string | 否 | 按标签名过滤（不区分大小写；GitHub 标签与 internal-meta 标签均匹配） |
| `source` | string（enum: github \| internal） | 否 | 按来源过滤 |
| `assignee` | string | 否 | 按 GitHub 指派人 login 过滤（不区分大小写） |
| `milestone` | string | 否 | 按 GitHub 里程碑标题过滤（不区分大小写） |
| `workflow_status` | string（enum: todo \| in_progress \| done \| unset） | 否 | 按工作流状态过滤；`unset` 表示未设置（无 meta 或为空串） |
| `sort` | string（enum: updated_desc \| updated_asc \| created_desc \| created_asc \| comments_desc \| comments_asc，默认 updated_desc） | 否 | 排序；未知值按 updated_desc 处理 |
| `page` | integer（≥1，默认 1） | 否 | 页码，<1 按 1 处理 |
| `page_size` | integer（1..100，默认 20） | 否 | 每页条数；越界（<1 或 >100）回落为 20 |

**成功响应**

**200** Issue 分页列表（列表项不含 collab / pull_requests 字段；带 pull_request_summary 聚合计数）

`data`：

分页响应 data 形状；与 Envelope allOf 组合后 items 由兄弟 schema 收窄为具体类型

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string（uuid） | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].source` | string（enum: github \| internal） | 是 |  |
| `items[].sequence_number` | integer | 是 | 项目内自增序号（reference 的 INT-n 来源） |
| `items[].reference` | string | 是 | 短编号；github 为 GH-<number>，internal 为 INT-<sequence_number> |
| `items[].state` | string（enum: open \| closed） | 是 |  |
| `items[].state_reason` | string | 是 | completed / not_planned / reopened 或空串 |
| `items[].title` | string | 是 |  |
| `items[].body` | string | 是 |  |
| `items[].body_html` | string | 是 | github 来源为远端渲染 HTML（媒体链接经代理改写）；internal 为空串 |
| `items[].author` | object | 是 |  |
| `items[].author.login` | string | 是 |  |
| `items[].author.avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |
| `items[].closed_at` | string（date-time，可空） | 是 |  |
| `items[].unread_comments_count` | integer | 是 | 未读评论数；仅列表响应填充（internal issue 恒为 0），详情响应恒为 0 |
| `items[].internal_meta` | object | 否 | omitempty；无 meta 且无 checklist 时整个字段缺省 |
| `items[].internal_meta.workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `items[].internal_meta.progress_percent` | integer（可空） | 是 | checklist 完成度 0-100；无 checklist 时为 null |
| `items[].internal_meta.checklist_total` | integer | 是 |  |
| `items[].internal_meta.checklist_done` | integer | 是 |  |
| `items[].internal_meta.started_at` | string（date-time） | 否 | omitempty；首次进入 in_progress/done 时写入 |
| `items[].internal_meta.completed_at` | string（date-time） | 否 | omitempty；首次进入 done 时写入 |
| `items[].internal_meta.checklist_updated_at` | string（date-time） | 否 | omitempty；checklist 非空时的最近替换时间 |
| `items[].internal_meta.updated_at` | string（date-time） | 否 | omitempty |
| `items[].internal_meta.checklist` | object[] | 否 | omitempty；仅 Issue 详情/PUT checklist 响应携带 |
| `items[].internal_meta.checklist[].id` | string | 是 |  |
| `items[].internal_meta.checklist[].title` | string | 是 |  |
| `items[].internal_meta.checklist[].is_completed` | boolean | 是 |  |
| `items[].internal_meta.checklist[].sort_order` | integer | 是 |  |
| `items[].internal_meta.labels` | object[] | 否 | omitempty；internal issue 的标签 |
| `items[].internal_meta.labels[].name` | string | 是 |  |
| `items[].internal_meta.labels[].color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `items[].internal_meta.labels[].description` | string | 是 |  |
| `items[].ship_hook` | object | 否 | omitempty；仅 Issue 详情/列表项中出现，未预约时缺省 |
| `items[].ship_hook.status` | string（enum: pending \| running \| fired） | 是 |  |
| `items[].ship_hook.comment_enabled` | boolean | 是 |  |
| `items[].ship_hook.comment_body` | string | 否 | omitempty；fired 后为渲染后正文 |
| `items[].ship_hook.close_enabled` | boolean | 是 |  |
| `items[].ship_hook.workflow_enabled` | boolean | 是 |  |
| `items[].ship_hook.workflow_status` | string | 是 | 目标工作流状态；workflow_enabled=true 且为 "" 表示重置 |
| `items[].ship_hook.version_id` | string | 否 | omitempty；fired 时的触发版本 |
| `items[].ship_hook.version_number` | string | 否 | omitempty |
| `items[].ship_hook.release_url` | string | 否 | omitempty |
| `items[].ship_hook.fired_at` | string（date-time） | 否 | omitempty |
| `items[].ship_hook.results` | object | 否 | omitempty；fired 后按启用的动作出现 |
| `items[].ship_hook.results.comment` | object | 否 |  |
| `items[].ship_hook.results.comment.ok` | boolean | 是 |  |
| `items[].ship_hook.results.comment.skipped` | boolean | 否 | omitempty |
| `items[].ship_hook.results.comment.error` | string | 否 | omitempty |
| `items[].ship_hook.results.close` | object | 否 |  |
| `items[].ship_hook.results.close.ok` | boolean | 是 |  |
| `items[].ship_hook.results.close.skipped` | boolean | 否 | omitempty |
| `items[].ship_hook.results.close.error` | string | 否 | omitempty |
| `items[].ship_hook.results.workflow_status` | object | 否 |  |
| `items[].ship_hook.results.workflow_status.ok` | boolean | 是 |  |
| `items[].ship_hook.results.workflow_status.skipped` | boolean | 否 | omitempty |
| `items[].ship_hook.results.workflow_status.error` | string | 否 | omitempty |
| `items[].github` | object | 否 | omitempty，仅 github 来源 Issue 出现 |
| `items[].github.github_issue_id` | integer（int64） | 是 |  |
| `items[].github.github_node_id` | string | 是 |  |
| `items[].github.number` | integer | 是 | GitHub issue 编号（reference 的 GH-n 来源） |
| `items[].github.html_url` | string | 是 |  |
| `items[].github.author_association` | string | 是 |  |
| `items[].github.assignees` | object[] | 是 |  |
| `items[].github.assignees[].login` | string | 是 |  |
| `items[].github.assignees[].avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `items[].github.labels` | object[] | 是 |  |
| `items[].github.labels[].name` | string | 是 |  |
| `items[].github.labels[].color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `items[].github.labels[].description` | string | 是 |  |
| `items[].github.milestone` | object | 否 |  |
| `items[].github.milestone.number` | integer | 是 |  |
| `items[].github.milestone.title` | string | 是 |  |
| `items[].github.milestone.state` | string | 是 |  |
| `items[].github.milestone.description` | string | 是 |  |
| `items[].github.reactions` | object | 是 |  |
| `items[].github.reactions.total_count` | integer | 是 |  |
| `items[].github.reactions.+1` | integer | 是 |  |
| `items[].github.reactions.-1` | integer | 是 |  |
| `items[].github.reactions.laugh` | integer | 是 |  |
| `items[].github.reactions.hooray` | integer | 是 |  |
| `items[].github.reactions.confused` | integer | 是 |  |
| `items[].github.reactions.heart` | integer | 是 |  |
| `items[].github.reactions.rocket` | integer | 是 |  |
| `items[].github.reactions.eyes` | integer | 是 |  |
| `items[].github.comments_count` | integer | 是 |  |
| `items[].github.locked` | boolean | 是 |  |
| `items[].github.active_lock_reason` | string | 是 |  |
| `items[].github.synced_at` | string（date-time） | 是 |  |
| `items[].collab` | object | 否 | omitempty；仅 Issue 详情响应携带，与 GET /collab 的 data 同形；列表项不出现 |
| `items[].collab.consensus` | object（可空） | 是 | 未产出为 null |
| `items[].collab.consensus.issue_id` | string | 是 |  |
| `items[].collab.consensus.body` | string | 是 |  |
| `items[].collab.consensus.author` | object | 是 |  |
| `items[].collab.consensus.author.kind` | string（enum: user \| agent） | 是 |  |
| `items[].collab.consensus.author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `items[].collab.consensus.author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `items[].collab.consensus.created_at` | string（date-time） | 是 |  |
| `items[].collab.consensus.updated_at` | string（date-time） | 是 |  |
| `items[].collab.summary` | object（可空） | 是 | 未产出为 null |
| `items[].collab.summary.issue_id` | string | 是 |  |
| `items[].collab.summary.body` | string | 是 |  |
| `items[].collab.summary.author` | object | 是 |  |
| `items[].collab.summary.author.kind` | string（enum: user \| agent） | 是 |  |
| `items[].collab.summary.author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `items[].collab.summary.author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `items[].collab.summary.created_at` | string（date-time） | 是 |  |
| `items[].collab.summary.updated_at` | string（date-time） | 是 |  |
| `items[].pull_requests` | object[] | 否 | omitempty；仅 Issue 详情响应携带（读取失败或无关联时缺省）；列表项不出现 |
| `items[].pull_requests[].id` | string（uuid） | 是 |  |
| `items[].pull_requests[].issue_id` | string | 是 |  |
| `items[].pull_requests[].provider` | string | 是 | 当前恒为 github；字段预留 gitlab |
| `items[].pull_requests[].repo_full_name` | string | 是 | owner/repo；允许与项目配置的仓库不同（跨仓库 attach） |
| `items[].pull_requests[].number` | integer | 是 |  |
| `items[].pull_requests[].html_url` | string | 是 |  |
| `items[].pull_requests[].title` | string | 是 |  |
| `items[].pull_requests[].state` | string（enum: open \| closed \| merged） | 是 | merged = GitHub 上 closed 且 merged_at 非空 |
| `items[].pull_requests[].is_draft` | boolean | 是 |  |
| `items[].pull_requests[].author_login` | string | 是 |  |
| `items[].pull_requests[].head_ref` | string | 是 |  |
| `items[].pull_requests[].base_ref` | string | 是 |  |
| `items[].pull_requests[].merged_at` | string（date-time，可空） | 是 | state=merged 时有值 |
| `items[].pull_requests[].closed_at` | string（date-time，可空） | 是 |  |
| `items[].pull_requests[].link_origin` | string（enum: manual \| synced） | 是 | manual=用户显式 attach；synced=远端同步投影产生（清理逻辑只作用于 synced） |
| `items[].pull_requests[].synced_at` | string（date-time） | 是 | 最近一次从 GitHub 刷新成功的时间 |
| `items[].pull_requests[].created_at` | string（date-time） | 是 |  |
| `items[].pull_requests[].updated_at` | string（date-time） | 是 |  |
| `items[].pull_request_summary` | object | 否 | omitempty；仅 Issue 列表项携带的 PR 聚合计数；详情项不出现。Issue 关联 PR 的聚合计数；closed 计数 = total - open - merged |
| `items[].pull_request_summary.total` | integer | 是 |  |
| `items[].pull_request_summary.open` | integer | 是 | state=open 计数 |
| `items[].pull_request_summary.merged` | integer | 是 | state=merged 计数 |
| `total` | integer（int64） | 是 | 符合条件的总条数 |
| `page` | integer | 是 |  |
| `page_size` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/projects/{id}/issues`

<!-- operationId: createIssue -->

创建 Issue

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `title` | string | 是 | trim 后须非空 |
| `body` | string | 否 | Markdown；引用 /api/issues/assets/{aid}/content 链接会在创建时挂载草稿附件 |
| `workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 否 | 空串表示未设置（重置语义） |
| `source` | string（enum: github \| internal） | 否 | 默认 internal；github 仅 JWT 可用，API Key 传 github 返回 40301；其他值按 internal 处理 |

**成功响应**

**200** 创建的 Issue

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string（uuid） | 是 |  |
| `project_id` | string | 是 |  |
| `source` | string（enum: github \| internal） | 是 |  |
| `sequence_number` | integer | 是 | 项目内自增序号（reference 的 INT-n 来源） |
| `reference` | string | 是 | 短编号；github 为 GH-<number>，internal 为 INT-<sequence_number> |
| `state` | string（enum: open \| closed） | 是 |  |
| `state_reason` | string | 是 | completed / not_planned / reopened 或空串 |
| `title` | string | 是 |  |
| `body` | string | 是 |  |
| `body_html` | string | 是 | github 来源为远端渲染 HTML（媒体链接经代理改写）；internal 为空串 |
| `author` | object | 是 |  |
| `author.login` | string | 是 |  |
| `author.avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `closed_at` | string（date-time，可空） | 是 |  |
| `unread_comments_count` | integer | 是 | 未读评论数；仅列表响应填充（internal issue 恒为 0），详情响应恒为 0 |
| `internal_meta` | object | 否 | omitempty；无 meta 且无 checklist 时整个字段缺省 |
| `internal_meta.workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `internal_meta.progress_percent` | integer（可空） | 是 | checklist 完成度 0-100；无 checklist 时为 null |
| `internal_meta.checklist_total` | integer | 是 |  |
| `internal_meta.checklist_done` | integer | 是 |  |
| `internal_meta.started_at` | string（date-time） | 否 | omitempty；首次进入 in_progress/done 时写入 |
| `internal_meta.completed_at` | string（date-time） | 否 | omitempty；首次进入 done 时写入 |
| `internal_meta.checklist_updated_at` | string（date-time） | 否 | omitempty；checklist 非空时的最近替换时间 |
| `internal_meta.updated_at` | string（date-time） | 否 | omitempty |
| `internal_meta.checklist` | object[] | 否 | omitempty；仅 Issue 详情/PUT checklist 响应携带 |
| `internal_meta.checklist[].id` | string | 是 |  |
| `internal_meta.checklist[].title` | string | 是 |  |
| `internal_meta.checklist[].is_completed` | boolean | 是 |  |
| `internal_meta.checklist[].sort_order` | integer | 是 |  |
| `internal_meta.labels` | object[] | 否 | omitempty；internal issue 的标签 |
| `internal_meta.labels[].name` | string | 是 |  |
| `internal_meta.labels[].color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `internal_meta.labels[].description` | string | 是 |  |
| `ship_hook` | object | 否 | omitempty；仅 Issue 详情/列表项中出现，未预约时缺省 |
| `ship_hook.status` | string（enum: pending \| running \| fired） | 是 |  |
| `ship_hook.comment_enabled` | boolean | 是 |  |
| `ship_hook.comment_body` | string | 否 | omitempty；fired 后为渲染后正文 |
| `ship_hook.close_enabled` | boolean | 是 |  |
| `ship_hook.workflow_enabled` | boolean | 是 |  |
| `ship_hook.workflow_status` | string | 是 | 目标工作流状态；workflow_enabled=true 且为 "" 表示重置 |
| `ship_hook.version_id` | string | 否 | omitempty；fired 时的触发版本 |
| `ship_hook.version_number` | string | 否 | omitempty |
| `ship_hook.release_url` | string | 否 | omitempty |
| `ship_hook.fired_at` | string（date-time） | 否 | omitempty |
| `ship_hook.results` | object | 否 | omitempty；fired 后按启用的动作出现 |
| `ship_hook.results.comment` | object | 否 |  |
| `ship_hook.results.comment.ok` | boolean | 是 |  |
| `ship_hook.results.comment.skipped` | boolean | 否 | omitempty |
| `ship_hook.results.comment.error` | string | 否 | omitempty |
| `ship_hook.results.close` | object | 否 |  |
| `ship_hook.results.close.ok` | boolean | 是 |  |
| `ship_hook.results.close.skipped` | boolean | 否 | omitempty |
| `ship_hook.results.close.error` | string | 否 | omitempty |
| `ship_hook.results.workflow_status` | object | 否 |  |
| `ship_hook.results.workflow_status.ok` | boolean | 是 |  |
| `ship_hook.results.workflow_status.skipped` | boolean | 否 | omitempty |
| `ship_hook.results.workflow_status.error` | string | 否 | omitempty |
| `github` | object | 否 | omitempty，仅 github 来源 Issue 出现 |
| `github.github_issue_id` | integer（int64） | 是 |  |
| `github.github_node_id` | string | 是 |  |
| `github.number` | integer | 是 | GitHub issue 编号（reference 的 GH-n 来源） |
| `github.html_url` | string | 是 |  |
| `github.author_association` | string | 是 |  |
| `github.assignees` | object[] | 是 |  |
| `github.assignees[].login` | string | 是 |  |
| `github.assignees[].avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `github.labels` | object[] | 是 |  |
| `github.labels[].name` | string | 是 |  |
| `github.labels[].color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `github.labels[].description` | string | 是 |  |
| `github.milestone` | object | 否 |  |
| `github.milestone.number` | integer | 是 |  |
| `github.milestone.title` | string | 是 |  |
| `github.milestone.state` | string | 是 |  |
| `github.milestone.description` | string | 是 |  |
| `github.reactions` | object | 是 |  |
| `github.reactions.total_count` | integer | 是 |  |
| `github.reactions.+1` | integer | 是 |  |
| `github.reactions.-1` | integer | 是 |  |
| `github.reactions.laugh` | integer | 是 |  |
| `github.reactions.hooray` | integer | 是 |  |
| `github.reactions.confused` | integer | 是 |  |
| `github.reactions.heart` | integer | 是 |  |
| `github.reactions.rocket` | integer | 是 |  |
| `github.reactions.eyes` | integer | 是 |  |
| `github.comments_count` | integer | 是 |  |
| `github.locked` | boolean | 是 |  |
| `github.active_lock_reason` | string | 是 |  |
| `github.synced_at` | string（date-time） | 是 |  |
| `collab` | object | 否 | omitempty；仅 Issue 详情响应携带，与 GET /collab 的 data 同形；列表项不出现 |
| `collab.consensus` | object（可空） | 是 | 未产出为 null |
| `collab.consensus.issue_id` | string | 是 |  |
| `collab.consensus.body` | string | 是 |  |
| `collab.consensus.author` | object | 是 |  |
| `collab.consensus.author.kind` | string（enum: user \| agent） | 是 |  |
| `collab.consensus.author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `collab.consensus.author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `collab.consensus.created_at` | string（date-time） | 是 |  |
| `collab.consensus.updated_at` | string（date-time） | 是 |  |
| `collab.summary` | object（可空） | 是 | 未产出为 null |
| `collab.summary.issue_id` | string | 是 |  |
| `collab.summary.body` | string | 是 |  |
| `collab.summary.author` | object | 是 |  |
| `collab.summary.author.kind` | string（enum: user \| agent） | 是 |  |
| `collab.summary.author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `collab.summary.author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `collab.summary.created_at` | string（date-time） | 是 |  |
| `collab.summary.updated_at` | string（date-time） | 是 |  |
| `pull_requests` | object[] | 否 | omitempty；仅 Issue 详情响应携带（读取失败或无关联时缺省）；列表项不出现 |
| `pull_requests[].id` | string（uuid） | 是 |  |
| `pull_requests[].issue_id` | string | 是 |  |
| `pull_requests[].provider` | string | 是 | 当前恒为 github；字段预留 gitlab |
| `pull_requests[].repo_full_name` | string | 是 | owner/repo；允许与项目配置的仓库不同（跨仓库 attach） |
| `pull_requests[].number` | integer | 是 |  |
| `pull_requests[].html_url` | string | 是 |  |
| `pull_requests[].title` | string | 是 |  |
| `pull_requests[].state` | string（enum: open \| closed \| merged） | 是 | merged = GitHub 上 closed 且 merged_at 非空 |
| `pull_requests[].is_draft` | boolean | 是 |  |
| `pull_requests[].author_login` | string | 是 |  |
| `pull_requests[].head_ref` | string | 是 |  |
| `pull_requests[].base_ref` | string | 是 |  |
| `pull_requests[].merged_at` | string（date-time，可空） | 是 | state=merged 时有值 |
| `pull_requests[].closed_at` | string（date-time，可空） | 是 |  |
| `pull_requests[].link_origin` | string（enum: manual \| synced） | 是 | manual=用户显式 attach；synced=远端同步投影产生（清理逻辑只作用于 synced） |
| `pull_requests[].synced_at` | string（date-time） | 是 | 最近一次从 GitHub 刷新成功的时间 |
| `pull_requests[].created_at` | string（date-time） | 是 |  |
| `pull_requests[].updated_at` | string（date-time） | 是 |  |
| `pull_request_summary` | object | 否 | omitempty；仅 Issue 列表项携带的 PR 聚合计数；详情项不出现。Issue 关联 PR 的聚合计数；closed 计数 = total - open - merged |
| `pull_request_summary.total` | integer | 是 |  |
| `pull_request_summary.open` | integer | 是 | state=open 计数 |
| `pull_request_summary.merged` | integer | 是 | state=merged 计数 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | API Key 创建 github 来源 Issue（40301）或项目未配 GitHub（40003） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |
| 502 | GitHub API 调用失败（50200） |

### GET `/api/projects/{id}/issues/count`

<!-- operationId: countIssues -->

按过滤条件统计 Issue 数

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `state` | string（enum: open \| closed） | 否 |  |
| `q` | string | 否 | 同 listIssues 的 q：标题/正文/作者/编号/纯数字序号 |
| `label` | string | 否 |  |
| `source` | string（enum: github \| internal） | 否 |  |
| `assignee` | string | 否 |  |
| `milestone` | string | 否 |  |
| `workflow_status` | string（enum: todo \| in_progress \| done \| unset） | 否 |  |
| `sort` | string | 否 | 接受但忽略（计数不排序） |

**成功响应**

**200** 匹配数量

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `count` | integer（int64） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/projects/{id}/issues/filter-options`

<!-- operationId: issueFilterOptions -->

获取 Issue 过滤器可选值（标签/指派人/里程碑）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 可过滤的取值集合

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `labels` | string[] | 是 |  |
| `assignees` | string[] | 是 |  |
| `milestones` | string[] | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/projects/{id}/issues/repo-labels`

<!-- operationId: issueRepoLabels -->

获取 GitHub 仓库标签（带本地缓存，失败时回落到 GitHub API 并回写缓存）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 仓库标签列表

`data` 为数组。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string | 是 |  |
| `color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `description` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 项目未关联 GitHub 仓库（40003） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |
| 502 | GitHub API 调用失败（50200） |

### POST `/api/projects/{id}/issues/assets`

<!-- operationId: uploadDraftIssueAsset -->

上传 Issue 草稿图片（仅 JWT；multipart/form-data，字段名 file）

上传后得到附件 ID 与 `/api/issues/assets/{aid}/content` 链接，写正文时以该链接引用，创建 Issue 时服务端自动挂载并清理草稿。仅接受 image/*（内容嗅探）；大小受服务端 upload.max_file_size 限制（默认 500MB）。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`multipart/form-data`，必填。表单字段，不是 JSON。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | binary | 是 | 图片文件；内容须为 image/* |

**成功响应**

**200** 上传的草稿附件（issue_id 为空）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `issue_id` | string | 是 | 草稿附件为空串 |
| `file_name` | string | 是 |  |
| `mime_type` | string | 是 |  |
| `file_size` | integer（int64） | 是 |  |
| `content_url` | string | 是 | /api/issues/assets/{aid}/content |
| `markdown` | string | 是 | 可直接粘贴的 ![alt](content_url) 片段 |
| `created_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/projects/{id}/issues/sync`

<!-- operationId: syncIssues -->

手动触发 GitHub Issue 同步（仅 JWT；项目须已配 GitHub）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 本次同步统计

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `project_id` | string | 是 |  |
| `synced_issue_count` | integer | 是 |  |
| `synced_comment_count` | integer | 是 |  |
| `synced_timeline_count` | integer | 是 |  |
| `started_at` | string（date-time） | 是 |  |
| `completed_at` | string（date-time） | 是 |  |
| `last_issue_updated_at` | string（date-time） | 否 | omitempty；本次增量水位 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 项目未关联 GitHub 仓库（40003） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 同步正在进行中（40907） |
| 500 | 服务器内部错误（50000） |
| 502 | GitHub API 调用失败（50200） |

### POST `/api/projects/{id}/issues/batch-close`

<!-- operationId: batchCloseDoneIssues -->

批量关闭 workflow_status=done 的 open Issue（仅 JWT；超过 200 条返回 41201）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`application/json`，可选。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `source` | string（enum: github \| internal） | 否 | 可选，限定仅关闭 internal 或 github 来源；缺省全部 |

**成功响应**

**200** 批量关闭结果

`data`：

批量操作结果，与 service.BatchCloseDoneIssuesResponse 同形；batch-close 与批量 internal-meta 共用

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `total` | integer（int64） | 是 |  |
| `succeeded` | integer | 是 |  |
| `failed` | integer | 是 |  |
| `failures` | object[] | 是 | 最多返回前 50 条失败明细 |
| `failures[].id` | string | 是 | Issue ID |
| `failures[].reference` | string | 否 | omitempty；INT-n 或 GH-n |
| `failures[].error` | string | 是 |  |
| `elapsed_ms` | integer（int64） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 412 | 匹配问题数量超过 200（41201） |
| 500 | 服务器内部错误（50000） |

### GET `/api/issues/{iid}`

<!-- operationId: getIssue -->

获取 Issue 详情

与列表项结构相同，另带可选 `collab` 字段（与 GET /collab 的 data 同形；仅详情填充，列表不返回）与 `pull_requests` 数组（关联 PR 全量；读取失败或无关联时缺省）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** Issue 详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string（uuid） | 是 |  |
| `project_id` | string | 是 |  |
| `source` | string（enum: github \| internal） | 是 |  |
| `sequence_number` | integer | 是 | 项目内自增序号（reference 的 INT-n 来源） |
| `reference` | string | 是 | 短编号；github 为 GH-<number>，internal 为 INT-<sequence_number> |
| `state` | string（enum: open \| closed） | 是 |  |
| `state_reason` | string | 是 | completed / not_planned / reopened 或空串 |
| `title` | string | 是 |  |
| `body` | string | 是 |  |
| `body_html` | string | 是 | github 来源为远端渲染 HTML（媒体链接经代理改写）；internal 为空串 |
| `author` | object | 是 |  |
| `author.login` | string | 是 |  |
| `author.avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `closed_at` | string（date-time，可空） | 是 |  |
| `unread_comments_count` | integer | 是 | 未读评论数；仅列表响应填充（internal issue 恒为 0），详情响应恒为 0 |
| `internal_meta` | object | 否 | omitempty；无 meta 且无 checklist 时整个字段缺省 |
| `internal_meta.workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `internal_meta.progress_percent` | integer（可空） | 是 | checklist 完成度 0-100；无 checklist 时为 null |
| `internal_meta.checklist_total` | integer | 是 |  |
| `internal_meta.checklist_done` | integer | 是 |  |
| `internal_meta.started_at` | string（date-time） | 否 | omitempty；首次进入 in_progress/done 时写入 |
| `internal_meta.completed_at` | string（date-time） | 否 | omitempty；首次进入 done 时写入 |
| `internal_meta.checklist_updated_at` | string（date-time） | 否 | omitempty；checklist 非空时的最近替换时间 |
| `internal_meta.updated_at` | string（date-time） | 否 | omitempty |
| `internal_meta.checklist` | object[] | 否 | omitempty；仅 Issue 详情/PUT checklist 响应携带 |
| `internal_meta.checklist[].id` | string | 是 |  |
| `internal_meta.checklist[].title` | string | 是 |  |
| `internal_meta.checklist[].is_completed` | boolean | 是 |  |
| `internal_meta.checklist[].sort_order` | integer | 是 |  |
| `internal_meta.labels` | object[] | 否 | omitempty；internal issue 的标签 |
| `internal_meta.labels[].name` | string | 是 |  |
| `internal_meta.labels[].color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `internal_meta.labels[].description` | string | 是 |  |
| `ship_hook` | object | 否 | omitempty；仅 Issue 详情/列表项中出现，未预约时缺省 |
| `ship_hook.status` | string（enum: pending \| running \| fired） | 是 |  |
| `ship_hook.comment_enabled` | boolean | 是 |  |
| `ship_hook.comment_body` | string | 否 | omitempty；fired 后为渲染后正文 |
| `ship_hook.close_enabled` | boolean | 是 |  |
| `ship_hook.workflow_enabled` | boolean | 是 |  |
| `ship_hook.workflow_status` | string | 是 | 目标工作流状态；workflow_enabled=true 且为 "" 表示重置 |
| `ship_hook.version_id` | string | 否 | omitempty；fired 时的触发版本 |
| `ship_hook.version_number` | string | 否 | omitempty |
| `ship_hook.release_url` | string | 否 | omitempty |
| `ship_hook.fired_at` | string（date-time） | 否 | omitempty |
| `ship_hook.results` | object | 否 | omitempty；fired 后按启用的动作出现 |
| `ship_hook.results.comment` | object | 否 |  |
| `ship_hook.results.comment.ok` | boolean | 是 |  |
| `ship_hook.results.comment.skipped` | boolean | 否 | omitempty |
| `ship_hook.results.comment.error` | string | 否 | omitempty |
| `ship_hook.results.close` | object | 否 |  |
| `ship_hook.results.close.ok` | boolean | 是 |  |
| `ship_hook.results.close.skipped` | boolean | 否 | omitempty |
| `ship_hook.results.close.error` | string | 否 | omitempty |
| `ship_hook.results.workflow_status` | object | 否 |  |
| `ship_hook.results.workflow_status.ok` | boolean | 是 |  |
| `ship_hook.results.workflow_status.skipped` | boolean | 否 | omitempty |
| `ship_hook.results.workflow_status.error` | string | 否 | omitempty |
| `github` | object | 否 | omitempty，仅 github 来源 Issue 出现 |
| `github.github_issue_id` | integer（int64） | 是 |  |
| `github.github_node_id` | string | 是 |  |
| `github.number` | integer | 是 | GitHub issue 编号（reference 的 GH-n 来源） |
| `github.html_url` | string | 是 |  |
| `github.author_association` | string | 是 |  |
| `github.assignees` | object[] | 是 |  |
| `github.assignees[].login` | string | 是 |  |
| `github.assignees[].avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `github.labels` | object[] | 是 |  |
| `github.labels[].name` | string | 是 |  |
| `github.labels[].color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `github.labels[].description` | string | 是 |  |
| `github.milestone` | object | 否 |  |
| `github.milestone.number` | integer | 是 |  |
| `github.milestone.title` | string | 是 |  |
| `github.milestone.state` | string | 是 |  |
| `github.milestone.description` | string | 是 |  |
| `github.reactions` | object | 是 |  |
| `github.reactions.total_count` | integer | 是 |  |
| `github.reactions.+1` | integer | 是 |  |
| `github.reactions.-1` | integer | 是 |  |
| `github.reactions.laugh` | integer | 是 |  |
| `github.reactions.hooray` | integer | 是 |  |
| `github.reactions.confused` | integer | 是 |  |
| `github.reactions.heart` | integer | 是 |  |
| `github.reactions.rocket` | integer | 是 |  |
| `github.reactions.eyes` | integer | 是 |  |
| `github.comments_count` | integer | 是 |  |
| `github.locked` | boolean | 是 |  |
| `github.active_lock_reason` | string | 是 |  |
| `github.synced_at` | string（date-time） | 是 |  |
| `collab` | object | 否 | omitempty；仅 Issue 详情响应携带，与 GET /collab 的 data 同形；列表项不出现 |
| `collab.consensus` | object（可空） | 是 | 未产出为 null |
| `collab.consensus.issue_id` | string | 是 |  |
| `collab.consensus.body` | string | 是 |  |
| `collab.consensus.author` | object | 是 |  |
| `collab.consensus.author.kind` | string（enum: user \| agent） | 是 |  |
| `collab.consensus.author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `collab.consensus.author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `collab.consensus.created_at` | string（date-time） | 是 |  |
| `collab.consensus.updated_at` | string（date-time） | 是 |  |
| `collab.summary` | object（可空） | 是 | 未产出为 null |
| `collab.summary.issue_id` | string | 是 |  |
| `collab.summary.body` | string | 是 |  |
| `collab.summary.author` | object | 是 |  |
| `collab.summary.author.kind` | string（enum: user \| agent） | 是 |  |
| `collab.summary.author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `collab.summary.author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `collab.summary.created_at` | string（date-time） | 是 |  |
| `collab.summary.updated_at` | string（date-time） | 是 |  |
| `pull_requests` | object[] | 否 | omitempty；仅 Issue 详情响应携带（读取失败或无关联时缺省）；列表项不出现 |
| `pull_requests[].id` | string（uuid） | 是 |  |
| `pull_requests[].issue_id` | string | 是 |  |
| `pull_requests[].provider` | string | 是 | 当前恒为 github；字段预留 gitlab |
| `pull_requests[].repo_full_name` | string | 是 | owner/repo；允许与项目配置的仓库不同（跨仓库 attach） |
| `pull_requests[].number` | integer | 是 |  |
| `pull_requests[].html_url` | string | 是 |  |
| `pull_requests[].title` | string | 是 |  |
| `pull_requests[].state` | string（enum: open \| closed \| merged） | 是 | merged = GitHub 上 closed 且 merged_at 非空 |
| `pull_requests[].is_draft` | boolean | 是 |  |
| `pull_requests[].author_login` | string | 是 |  |
| `pull_requests[].head_ref` | string | 是 |  |
| `pull_requests[].base_ref` | string | 是 |  |
| `pull_requests[].merged_at` | string（date-time，可空） | 是 | state=merged 时有值 |
| `pull_requests[].closed_at` | string（date-time，可空） | 是 |  |
| `pull_requests[].link_origin` | string（enum: manual \| synced） | 是 | manual=用户显式 attach；synced=远端同步投影产生（清理逻辑只作用于 synced） |
| `pull_requests[].synced_at` | string（date-time） | 是 | 最近一次从 GitHub 刷新成功的时间 |
| `pull_requests[].created_at` | string（date-time） | 是 |  |
| `pull_requests[].updated_at` | string（date-time） | 是 |  |
| `pull_request_summary` | object | 否 | omitempty；仅 Issue 列表项携带的 PR 聚合计数；详情项不出现。Issue 关联 PR 的聚合计数；closed 计数 = total - open - merged |
| `pull_request_summary.total` | integer | 是 |  |
| `pull_request_summary.open` | integer | 是 | state=open 计数 |
| `pull_request_summary.merged` | integer | 是 | state=merged 计数 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/issues/{iid}`

<!-- operationId: updateIssue -->

更新 Issue（至少传一个字段）

更新标题、正文、状态或标签。`state`/`state_reason` 仅 JWT：API Key 携带任一即返回 403（40301）。
`state_reason` 须随 `state` 同传且值与状态匹配（closed→completed|not_planned，open→reopened；open 且 reason 为空即清空）。
`labels`：internal issue 须解析为已存在的仓库标签名，否则 40001；github issue 仅做非空/去重校验。
github 来源 Issue 的更改写回 GitHub 远端，internal 仅写本地；`state=closed` 会写入 closed_at，`open` 则清空。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`application/json`，必填。

至少传一个字段；state_reason 必须随 state 同传

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `title` | string | 否 | trim 后须非空 |
| `body` | string | 否 |  |
| `state` | string（enum: open \| closed） | 否 | 仅 JWT；API Key 携带返回 40301 |
| `state_reason` | string（enum: completed \| not_planned \| reopened） | 否 | 仅 JWT；closed→completed\|not_planned，open→reopened；置 open 且 reason 为空会清空 |
| `labels` | string[] | 否 | 标签名数组；internal issue 须为已存在仓库标签，GitHub issue 仅做非空/去重校验 |

**成功响应**

**200** 更新后的 Issue

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string（uuid） | 是 |  |
| `project_id` | string | 是 |  |
| `source` | string（enum: github \| internal） | 是 |  |
| `sequence_number` | integer | 是 | 项目内自增序号（reference 的 INT-n 来源） |
| `reference` | string | 是 | 短编号；github 为 GH-<number>，internal 为 INT-<sequence_number> |
| `state` | string（enum: open \| closed） | 是 |  |
| `state_reason` | string | 是 | completed / not_planned / reopened 或空串 |
| `title` | string | 是 |  |
| `body` | string | 是 |  |
| `body_html` | string | 是 | github 来源为远端渲染 HTML（媒体链接经代理改写）；internal 为空串 |
| `author` | object | 是 |  |
| `author.login` | string | 是 |  |
| `author.avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `closed_at` | string（date-time，可空） | 是 |  |
| `unread_comments_count` | integer | 是 | 未读评论数；仅列表响应填充（internal issue 恒为 0），详情响应恒为 0 |
| `internal_meta` | object | 否 | omitempty；无 meta 且无 checklist 时整个字段缺省 |
| `internal_meta.workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `internal_meta.progress_percent` | integer（可空） | 是 | checklist 完成度 0-100；无 checklist 时为 null |
| `internal_meta.checklist_total` | integer | 是 |  |
| `internal_meta.checklist_done` | integer | 是 |  |
| `internal_meta.started_at` | string（date-time） | 否 | omitempty；首次进入 in_progress/done 时写入 |
| `internal_meta.completed_at` | string（date-time） | 否 | omitempty；首次进入 done 时写入 |
| `internal_meta.checklist_updated_at` | string（date-time） | 否 | omitempty；checklist 非空时的最近替换时间 |
| `internal_meta.updated_at` | string（date-time） | 否 | omitempty |
| `internal_meta.checklist` | object[] | 否 | omitempty；仅 Issue 详情/PUT checklist 响应携带 |
| `internal_meta.checklist[].id` | string | 是 |  |
| `internal_meta.checklist[].title` | string | 是 |  |
| `internal_meta.checklist[].is_completed` | boolean | 是 |  |
| `internal_meta.checklist[].sort_order` | integer | 是 |  |
| `internal_meta.labels` | object[] | 否 | omitempty；internal issue 的标签 |
| `internal_meta.labels[].name` | string | 是 |  |
| `internal_meta.labels[].color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `internal_meta.labels[].description` | string | 是 |  |
| `ship_hook` | object | 否 | omitempty；仅 Issue 详情/列表项中出现，未预约时缺省 |
| `ship_hook.status` | string（enum: pending \| running \| fired） | 是 |  |
| `ship_hook.comment_enabled` | boolean | 是 |  |
| `ship_hook.comment_body` | string | 否 | omitempty；fired 后为渲染后正文 |
| `ship_hook.close_enabled` | boolean | 是 |  |
| `ship_hook.workflow_enabled` | boolean | 是 |  |
| `ship_hook.workflow_status` | string | 是 | 目标工作流状态；workflow_enabled=true 且为 "" 表示重置 |
| `ship_hook.version_id` | string | 否 | omitempty；fired 时的触发版本 |
| `ship_hook.version_number` | string | 否 | omitempty |
| `ship_hook.release_url` | string | 否 | omitempty |
| `ship_hook.fired_at` | string（date-time） | 否 | omitempty |
| `ship_hook.results` | object | 否 | omitempty；fired 后按启用的动作出现 |
| `ship_hook.results.comment` | object | 否 |  |
| `ship_hook.results.comment.ok` | boolean | 是 |  |
| `ship_hook.results.comment.skipped` | boolean | 否 | omitempty |
| `ship_hook.results.comment.error` | string | 否 | omitempty |
| `ship_hook.results.close` | object | 否 |  |
| `ship_hook.results.close.ok` | boolean | 是 |  |
| `ship_hook.results.close.skipped` | boolean | 否 | omitempty |
| `ship_hook.results.close.error` | string | 否 | omitempty |
| `ship_hook.results.workflow_status` | object | 否 |  |
| `ship_hook.results.workflow_status.ok` | boolean | 是 |  |
| `ship_hook.results.workflow_status.skipped` | boolean | 否 | omitempty |
| `ship_hook.results.workflow_status.error` | string | 否 | omitempty |
| `github` | object | 否 | omitempty，仅 github 来源 Issue 出现 |
| `github.github_issue_id` | integer（int64） | 是 |  |
| `github.github_node_id` | string | 是 |  |
| `github.number` | integer | 是 | GitHub issue 编号（reference 的 GH-n 来源） |
| `github.html_url` | string | 是 |  |
| `github.author_association` | string | 是 |  |
| `github.assignees` | object[] | 是 |  |
| `github.assignees[].login` | string | 是 |  |
| `github.assignees[].avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `github.labels` | object[] | 是 |  |
| `github.labels[].name` | string | 是 |  |
| `github.labels[].color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `github.labels[].description` | string | 是 |  |
| `github.milestone` | object | 否 |  |
| `github.milestone.number` | integer | 是 |  |
| `github.milestone.title` | string | 是 |  |
| `github.milestone.state` | string | 是 |  |
| `github.milestone.description` | string | 是 |  |
| `github.reactions` | object | 是 |  |
| `github.reactions.total_count` | integer | 是 |  |
| `github.reactions.+1` | integer | 是 |  |
| `github.reactions.-1` | integer | 是 |  |
| `github.reactions.laugh` | integer | 是 |  |
| `github.reactions.hooray` | integer | 是 |  |
| `github.reactions.confused` | integer | 是 |  |
| `github.reactions.heart` | integer | 是 |  |
| `github.reactions.rocket` | integer | 是 |  |
| `github.reactions.eyes` | integer | 是 |  |
| `github.comments_count` | integer | 是 |  |
| `github.locked` | boolean | 是 |  |
| `github.active_lock_reason` | string | 是 |  |
| `github.synced_at` | string（date-time） | 是 |  |
| `collab` | object | 否 | omitempty；仅 Issue 详情响应携带，与 GET /collab 的 data 同形；列表项不出现 |
| `collab.consensus` | object（可空） | 是 | 未产出为 null |
| `collab.consensus.issue_id` | string | 是 |  |
| `collab.consensus.body` | string | 是 |  |
| `collab.consensus.author` | object | 是 |  |
| `collab.consensus.author.kind` | string（enum: user \| agent） | 是 |  |
| `collab.consensus.author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `collab.consensus.author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `collab.consensus.created_at` | string（date-time） | 是 |  |
| `collab.consensus.updated_at` | string（date-time） | 是 |  |
| `collab.summary` | object（可空） | 是 | 未产出为 null |
| `collab.summary.issue_id` | string | 是 |  |
| `collab.summary.body` | string | 是 |  |
| `collab.summary.author` | object | 是 |  |
| `collab.summary.author.kind` | string（enum: user \| agent） | 是 |  |
| `collab.summary.author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `collab.summary.author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `collab.summary.created_at` | string（date-time） | 是 |  |
| `collab.summary.updated_at` | string（date-time） | 是 |  |
| `pull_requests` | object[] | 否 | omitempty；仅 Issue 详情响应携带（读取失败或无关联时缺省）；列表项不出现 |
| `pull_requests[].id` | string（uuid） | 是 |  |
| `pull_requests[].issue_id` | string | 是 |  |
| `pull_requests[].provider` | string | 是 | 当前恒为 github；字段预留 gitlab |
| `pull_requests[].repo_full_name` | string | 是 | owner/repo；允许与项目配置的仓库不同（跨仓库 attach） |
| `pull_requests[].number` | integer | 是 |  |
| `pull_requests[].html_url` | string | 是 |  |
| `pull_requests[].title` | string | 是 |  |
| `pull_requests[].state` | string（enum: open \| closed \| merged） | 是 | merged = GitHub 上 closed 且 merged_at 非空 |
| `pull_requests[].is_draft` | boolean | 是 |  |
| `pull_requests[].author_login` | string | 是 |  |
| `pull_requests[].head_ref` | string | 是 |  |
| `pull_requests[].base_ref` | string | 是 |  |
| `pull_requests[].merged_at` | string（date-time，可空） | 是 | state=merged 时有值 |
| `pull_requests[].closed_at` | string（date-time，可空） | 是 |  |
| `pull_requests[].link_origin` | string（enum: manual \| synced） | 是 | manual=用户显式 attach；synced=远端同步投影产生（清理逻辑只作用于 synced） |
| `pull_requests[].synced_at` | string（date-time） | 是 | 最近一次从 GitHub 刷新成功的时间 |
| `pull_requests[].created_at` | string（date-time） | 是 |  |
| `pull_requests[].updated_at` | string（date-time） | 是 |  |
| `pull_request_summary` | object | 否 | omitempty；仅 Issue 列表项携带的 PR 聚合计数；详情项不出现。Issue 关联 PR 的聚合计数；closed 计数 = total - open - merged |
| `pull_request_summary.total` | integer | 是 |  |
| `pull_request_summary.open` | integer | 是 | state=open 计数 |
| `pull_request_summary.merged` | integer | 是 | state=merged 计数 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | API Key 传 state/state_reason（40301）或非资源所有者（40302） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |
| 502 | GitHub API 调用失败（50200；github 来源 Issue 的更改写回远端时） |

### POST `/api/issues/{iid}/assets`

<!-- operationId: uploadIssueAsset -->

上传 Issue 图片附件（multipart/form-data，字段名 file）

仅 internal 来源 Issue；仅接受 image/*（内容嗅探）；大小受服务端 upload.max_file_size 限制（默认 500MB）。返回附件 ID 与 content_url/markdown，把 markdown 或链接写进正文即完成挂载。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`multipart/form-data`，必填。表单字段，不是 JSON。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | binary | 是 | 图片文件；内容须为 image/* |

**成功响应**

**200** 上传的附件（status 为 pending，正文引用后转 attached）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `issue_id` | string | 是 | 草稿附件为空串 |
| `file_name` | string | 是 |  |
| `mime_type` | string | 是 |  |
| `file_size` | integer（int64） | 是 |  |
| `content_url` | string | 是 | /api/issues/assets/{aid}/content |
| `markdown` | string | 是 | 可直接粘贴的 ![alt](content_url) 片段 |
| `created_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 非 internal 来源 Issue 不可上传（40908） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/issues/{iid}/internal-meta`

<!-- operationId: updateIssueInternalMeta -->

更新 Issue 内部工作流状态

workflow_status 必填；`""` 表示重置为未设置。置为 in_progress/done 会顺带删除该 Issue 的推荐（如有）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 否 | 服务端按必填校验；缺省或 null 返回 400。空串表示未设置（重置语义） |

**成功响应**

**200** 更新后的 internal meta

`data`：

omitempty；无 meta 且无 checklist 时整个字段缺省

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `progress_percent` | integer（可空） | 是 | checklist 完成度 0-100；无 checklist 时为 null |
| `checklist_total` | integer | 是 |  |
| `checklist_done` | integer | 是 |  |
| `started_at` | string（date-time） | 否 | omitempty；首次进入 in_progress/done 时写入 |
| `completed_at` | string（date-time） | 否 | omitempty；首次进入 done 时写入 |
| `checklist_updated_at` | string（date-time） | 否 | omitempty；checklist 非空时的最近替换时间 |
| `updated_at` | string（date-time） | 否 | omitempty |
| `checklist` | object[] | 否 | omitempty；仅 Issue 详情/PUT checklist 响应携带 |
| `checklist[].id` | string | 是 |  |
| `checklist[].title` | string | 是 |  |
| `checklist[].is_completed` | boolean | 是 |  |
| `checklist[].sort_order` | integer | 是 |  |
| `labels` | object[] | 否 | omitempty；internal issue 的标签 |
| `labels[].name` | string | 是 |  |
| `labels[].color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `labels[].description` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/issues/internal-meta`

<!-- operationId: batchUpdateIssueInternalMeta -->

批量更新 Issue 内部工作流状态

批量把多个 Issue 的 workflow_status 设为指定值；逐项处理，单项失败计入 failures 不中断整批。items 为空或 >200 整体 400；某项 issue_id 为空整体 400；某项 workflow_status 为 null 计入该项失败。JWT 与 API Key 均可调用。

**鉴权**：JWT+API Key

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[]（1..200 项） | 是 |  |
| `items[].issue_id` | string（uuid） | 是 | Issue ID（UUID） |
| `items[].workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 否 | 缺省或为 null 时该项计入 failures。空串表示未设置（重置语义） |

**成功响应**

**200** 批量更新结果

`data`：

批量操作结果，与 service.BatchCloseDoneIssuesResponse 同形；batch-close 与批量 internal-meta 共用

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `total` | integer（int64） | 是 |  |
| `succeeded` | integer | 是 |  |
| `failed` | integer | 是 |  |
| `failures` | object[] | 是 | 最多返回前 50 条失败明细 |
| `failures[].id` | string | 是 | Issue ID |
| `failures[].reference` | string | 否 | omitempty；INT-n 或 GH-n |
| `failures[].error` | string | 是 |  |
| `elapsed_ms` | integer（int64） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/issues/{iid}/checklist`

<!-- operationId: replaceIssueChecklist -->

整组替换 Issue checklist

全量覆盖语义：提交的 items 即最终清单，未包含的旧条目会被删除。item.id 缺省时服务端生成；title trim 后须非空。items=[] 合法，清空清单并把 progress_percent、checklist_updated_at 重置为 null。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 否 | 缺省时服务端生成；传旧 id 复用条目 |
| `items[].title` | string | 是 | trim 后须非空 |
| `items[].is_completed` | boolean | 否 |  |

**成功响应**

**200** 更新后的 internal meta（含 checklist 与进度快照）

`data`：

omitempty；无 meta 且无 checklist 时整个字段缺省

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `progress_percent` | integer（可空） | 是 | checklist 完成度 0-100；无 checklist 时为 null |
| `checklist_total` | integer | 是 |  |
| `checklist_done` | integer | 是 |  |
| `started_at` | string（date-time） | 否 | omitempty；首次进入 in_progress/done 时写入 |
| `completed_at` | string（date-time） | 否 | omitempty；首次进入 done 时写入 |
| `checklist_updated_at` | string（date-time） | 否 | omitempty；checklist 非空时的最近替换时间 |
| `updated_at` | string（date-time） | 否 | omitempty |
| `checklist` | object[] | 否 | omitempty；仅 Issue 详情/PUT checklist 响应携带 |
| `checklist[].id` | string | 是 |  |
| `checklist[].title` | string | 是 |  |
| `checklist[].is_completed` | boolean | 是 |  |
| `checklist[].sort_order` | integer | 是 |  |
| `labels` | object[] | 否 | omitempty；internal issue 的标签 |
| `labels[].name` | string | 是 |  |
| `labels[].color` | string | 是 | 十六进制颜色（不带 # 前缀） |
| `labels[].description` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/issues/{iid}/ship-hook`

<!-- operationId: upsertShipHook -->

预约发货后钩子（仅 JWT；整体覆盖）

配置该 Issue 所属项目下一次任意版本成功发货后执行的一次性动作；三类动作至少启用一个。comment_body 中 `{version}`、`{release_url}` 占位符在发货时替换。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`application/json`，必填。

三类动作至少启用一个，否则 40001

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `comment_body` | string | 否 | 传入即启用评论动作；trim 后 1-4000 rune；支持 {version}、{release_url} 占位符 |
| `close` | boolean | 否 | true 时发货后关闭 Issue（state_reason=completed） |
| `workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 否 | 传入即启用工作流动作（含 "" 重置）。空串表示未设置（重置语义） |

**成功响应**

**200** 更新后的钩子

`data`：

omitempty；仅 Issue 详情/列表项中出现，未预约时缺省

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `status` | string（enum: pending \| running \| fired） | 是 |  |
| `comment_enabled` | boolean | 是 |  |
| `comment_body` | string | 否 | omitempty；fired 后为渲染后正文 |
| `close_enabled` | boolean | 是 |  |
| `workflow_enabled` | boolean | 是 |  |
| `workflow_status` | string | 是 | 目标工作流状态；workflow_enabled=true 且为 "" 表示重置 |
| `version_id` | string | 否 | omitempty；fired 时的触发版本 |
| `version_number` | string | 否 | omitempty |
| `release_url` | string | 否 | omitempty |
| `fired_at` | string（date-time） | 否 | omitempty |
| `results` | object | 否 | omitempty；fired 后按启用的动作出现 |
| `results.comment` | object | 否 |  |
| `results.comment.ok` | boolean | 是 |  |
| `results.comment.skipped` | boolean | 否 | omitempty |
| `results.comment.error` | string | 否 | omitempty |
| `results.close` | object | 否 |  |
| `results.close.ok` | boolean | 是 |  |
| `results.close.skipped` | boolean | 否 | omitempty |
| `results.close.error` | string | 否 | omitempty |
| `results.workflow_status` | object | 否 |  |
| `results.workflow_status.ok` | boolean | 是 |  |
| `results.workflow_status.skipped` | boolean | 否 | omitempty |
| `results.workflow_status.error` | string | 否 | omitempty |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/issues/{iid}/ship-hook`

<!-- operationId: deleteShipHook -->

删除发货后钩子（仅 JWT）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/issues/{iid}/comments`

<!-- operationId: listIssueComments -->

分页列出 Issue 评论

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `page` | integer（≥1，默认 1） | 否 | 页码，<1 按 1 处理 |
| `page_size` | integer（1..100，默认 50） | 否 | 每页条数；越界（<1 或 >100）回落为 50 |

**成功响应**

**200** 评论分页列表

`data`：

分页响应 data 形状；与 Envelope allOf 组合后 items 由兄弟 schema 收窄为具体类型

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].issue_id` | string | 是 |  |
| `items[].source` | string（enum: github \| internal） | 是 |  |
| `items[].github_comment_id` | integer（int64） | 是 | internal 评论为本地合成的负值序号 |
| `items[].github_node_id` | string | 是 |  |
| `items[].body` | string | 是 |  |
| `items[].body_html` | string | 是 |  |
| `items[].html_url` | string | 是 |  |
| `items[].author` | object | 是 |  |
| `items[].author.login` | string | 是 |  |
| `items[].author.avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `items[].author_association` | string | 是 |  |
| `items[].reactions` | object | 是 |  |
| `items[].reactions.total_count` | integer | 是 |  |
| `items[].reactions.+1` | integer | 是 |  |
| `items[].reactions.-1` | integer | 是 |  |
| `items[].reactions.laugh` | integer | 是 |  |
| `items[].reactions.hooray` | integer | 是 |  |
| `items[].reactions.confused` | integer | 是 |  |
| `items[].reactions.heart` | integer | 是 |  |
| `items[].reactions.rocket` | integer | 是 |  |
| `items[].reactions.eyes` | integer | 是 |  |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |
| `total` | integer（int64） | 是 | 符合条件的总条数 |
| `page` | integer | 是 |  |
| `page_size` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/issues/{iid}/comments`

<!-- operationId: createIssueComment -->

发表评论（仅 JWT；GitHub issue 同步发到远端，internal issue 存本地）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `body` | string | 是 | 评论正文；trim 后须非空 |

**成功响应**

**200** 创建的评论

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `issue_id` | string | 是 |  |
| `source` | string（enum: github \| internal） | 是 |  |
| `github_comment_id` | integer（int64） | 是 | internal 评论为本地合成的负值序号 |
| `github_node_id` | string | 是 |  |
| `body` | string | 是 |  |
| `body_html` | string | 是 |  |
| `html_url` | string | 是 |  |
| `author` | object | 是 |  |
| `author.login` | string | 是 |  |
| `author.avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `author_association` | string | 是 |  |
| `reactions` | object | 是 |  |
| `reactions.total_count` | integer | 是 |  |
| `reactions.+1` | integer | 是 |  |
| `reactions.-1` | integer | 是 |  |
| `reactions.laugh` | integer | 是 |  |
| `reactions.hooray` | integer | 是 |  |
| `reactions.confused` | integer | 是 |  |
| `reactions.heart` | integer | 是 |  |
| `reactions.rocket` | integer | 是 |  |
| `reactions.eyes` | integer | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |
| 502 | GitHub API 调用失败（50200） |

### POST `/api/issues/{iid}/read`

<!-- operationId: markIssueRead -->

标记 Issue 评论已读（仅 JWT；internal Issue 无未读概念，恒成功）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/issues/{iid}/timeline`

<!-- operationId: listIssueTimeline -->

分页列出 Issue 动态（仅 GitHub 来源有数据，internal 返回空）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `page` | integer（≥1，默认 1） | 否 | 页码，<1 按 1 处理 |
| `page_size` | integer（1..100，默认 50） | 否 | 每页条数；越界（<1 或 >100）回落为 50 |

**成功响应**

**200** 动态分页列表

`data`：

分页响应 data 形状；与 Envelope allOf 组合后 items 由兄弟 schema 收窄为具体类型

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].issue_id` | string | 是 |  |
| `items[].event_key` | string | 是 |  |
| `items[].event_type` | string | 是 | GitHub 事件类型（labeled/assigned/closed/...） |
| `items[].github_event_id` | integer（int64） | 是 |  |
| `items[].actor` | object | 是 |  |
| `items[].actor.login` | string | 是 |  |
| `items[].actor.avatar_url` | string | 是 | GitHub 头像经媒体代理改写；internal 用户为空串 |
| `items[].body` | string | 是 |  |
| `items[].summary` | string | 是 | 中文化摘要 |
| `items[].payload` | object | 是 | 原始事件 JSON |
| `items[].created_at` | string（date-time） | 是 |  |
| `total` | integer（int64） | 是 | 符合条件的总条数 |
| `page` | integer | 是 |  |
| `page_size` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/issues/{iid}/checklist-suggestions`

<!-- operationId: suggestIssueChecklist -->

AI 生成 checklist 建议（需已配置 AI 设置；JWT 与 API Key 均可）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** 建议事项列表（≤12 条）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[]（≤12 项） | 是 |  |
| `items[].title` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | Issue 不存在（40405）、项目不存在（40401）或未配置 AI（40407） |
| 500 | 服务器内部错误（50000） |
| 502 | AI 服务调用失败（50201） |

### POST `/api/issues/{iid}/pull-requests`

<!-- operationId: attachIssuePullRequest -->

关联一个 GitHub PR 到 Issue（幂等）

请求体只传 PR URL（`https://github.com/<owner>/<repo>/pull/<number>`），服务端解析 owner/repo/number 后立即从 GitHub 拉取 title/state/is_draft/author/head/base/merged_at 等字段入库；拉取失败返回错误，不落记录。
允许跨仓库：URL 可以是任意 GitHub 仓库的 PR，不要求等于项目配置的仓库，不做跨仓库鉴权。项目未配 GitHub token 时以未认证方式拉取，仅支持公共仓库。
重复 attach 同一 PR（唯一键 issue_id+provider+repo_full_name+number）幂等返回既有记录并顺带刷新状态。attach 产生的行 `link_origin=manual`；PR 状态与 Issue workflow_status 不联动。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `url` | string | 是 | GitHub PR 链接，形如 https://github.com/<owner>/<repo>/pull/<number>（允许 http、www 前缀与尾部 /files、query、fragment 等）；其他字段由服务端从 GitHub 拉取 |

**成功响应**

**200** 关联记录（重复 attach 返回既有行并刷新状态）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string（uuid） | 是 |  |
| `issue_id` | string | 是 |  |
| `provider` | string | 是 | 当前恒为 github；字段预留 gitlab |
| `repo_full_name` | string | 是 | owner/repo；允许与项目配置的仓库不同（跨仓库 attach） |
| `number` | integer | 是 |  |
| `html_url` | string | 是 |  |
| `title` | string | 是 |  |
| `state` | string（enum: open \| closed \| merged） | 是 | merged = GitHub 上 closed 且 merged_at 非空 |
| `is_draft` | boolean | 是 |  |
| `author_login` | string | 是 |  |
| `head_ref` | string | 是 |  |
| `base_ref` | string | 是 |  |
| `merged_at` | string（date-time，可空） | 是 | state=merged 时有值 |
| `closed_at` | string（date-time，可空） | 是 |  |
| `link_origin` | string（enum: manual \| synced） | 是 | manual=用户显式 attach；synced=远端同步投影产生（清理逻辑只作用于 synced） |
| `synced_at` | string（date-time） | 是 | 最近一次从 GitHub 刷新成功的时间 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 非 GitHub 或无法解析的 PR URL（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |
| 502 | GitHub API 调用失败（50200；含 PR 不存在或无权访问） |

### POST `/api/issues/{iid}/pull-requests/sync`

<!-- operationId: syncIssuePullRequests -->

刷新该 Issue 下全部已关联 PR 的状态

逐条重新拉取 GitHub PR 信息并更新 state/merged_at/closed_at 等字段。只更新既有行，不新增、不删除；每条关联是独立失败域——单行拉取或保存失败记入 failures 不中断其余行（失败行保留旧数据），整体恒返回 200。仅 Issue/项目不存在返回 404，读取关联列表失败返回 500。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** 刷新结果：items 为刷新成功的关联行，failures 为逐行失败明细

`data`：

syncIssuePullRequests 的响应形状，与批量 internal-meta 的 {items, failures} 约定一致

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 | 本轮刷新成功的关联行（无关联或无成功时为空数组） |
| `items[].id` | string（uuid） | 是 |  |
| `items[].issue_id` | string | 是 |  |
| `items[].provider` | string | 是 | 当前恒为 github；字段预留 gitlab |
| `items[].repo_full_name` | string | 是 | owner/repo；允许与项目配置的仓库不同（跨仓库 attach） |
| `items[].number` | integer | 是 |  |
| `items[].html_url` | string | 是 |  |
| `items[].title` | string | 是 |  |
| `items[].state` | string（enum: open \| closed \| merged） | 是 | merged = GitHub 上 closed 且 merged_at 非空 |
| `items[].is_draft` | boolean | 是 |  |
| `items[].author_login` | string | 是 |  |
| `items[].head_ref` | string | 是 |  |
| `items[].base_ref` | string | 是 |  |
| `items[].merged_at` | string（date-time，可空） | 是 | state=merged 时有值 |
| `items[].closed_at` | string（date-time，可空） | 是 |  |
| `items[].link_origin` | string（enum: manual \| synced） | 是 | manual=用户显式 attach；synced=远端同步投影产生（清理逻辑只作用于 synced） |
| `items[].synced_at` | string（date-time） | 是 | 最近一次从 GitHub 刷新成功的时间 |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |
| `failures` | object[] | 是 | 逐行失败明细 |
| `failures[].id` | string | 是 | 关联行 id（issue_pull_requests.id，detach 时用此 id） |
| `failures[].error` | string | 是 | 失败原因，前缀带 repo_full_name#number 便于定位 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/issues/{iid}/pull-requests/{id}`

<!-- operationId: detachIssuePullRequest -->

解除 Issue 与一个 PR 的关联（按关联行 id）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |
| `id` | string | 是 | PR 关联行 ID（UUID） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | Issue 不存在（40405）或 PR 关联不存在（40412） |
| 500 | 服务器内部错误（50000） |

## collab

Issue 人机协作区（consensus / summary）

### GET `/api/issues/{iid}/collab`

<!-- operationId: getIssueCollabArea -->

获取人机协作区（consensus 与 summary 两块，未产出为 null）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** 协作区内容

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `consensus` | object（可空） | 是 | 未产出为 null |
| `consensus.issue_id` | string | 是 |  |
| `consensus.body` | string | 是 |  |
| `consensus.author` | object | 是 |  |
| `consensus.author.kind` | string（enum: user \| agent） | 是 |  |
| `consensus.author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `consensus.author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `consensus.created_at` | string（date-time） | 是 |  |
| `consensus.updated_at` | string（date-time） | 是 |  |
| `summary` | object（可空） | 是 | 未产出为 null |
| `summary.issue_id` | string | 是 |  |
| `summary.body` | string | 是 |  |
| `summary.author` | object | 是 |  |
| `summary.author.kind` | string（enum: user \| agent） | 是 |  |
| `summary.author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `summary.author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `summary.created_at` | string（date-time） | 是 |  |
| `summary.updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/issues/{iid}/collab`

<!-- operationId: clearIssueCollabArea -->

清空协作区两块内容（幂等）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/issues/{iid}/collab/consensus`

<!-- operationId: upsertCollabConsensus -->

写入/覆盖共识（仅 API Key；JWT 返回 40303）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `body` | string | 是 | Markdown；trim 后 1-8000 rune；重复 PUT 覆盖 |

**成功响应**

**200** 写入后的共识文档

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `issue_id` | string | 是 |  |
| `body` | string | 是 |  |
| `author` | object | 是 |  |
| `author.kind` | string（enum: user \| agent） | 是 |  |
| `author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | JWT 调用仅限 API Key 的端点（40303） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/issues/{iid}/collab/consensus`

<!-- operationId: deleteCollabConsensus -->

删除共识（幂等，不存在也返回 200）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/issues/{iid}/collab/summary`

<!-- operationId: upsertCollabSummary -->

写入/覆盖完成总结（仅 API Key；JWT 返回 40303）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `body` | string | 是 | Markdown；trim 后 1-8000 rune；重复 PUT 覆盖 |

**成功响应**

**200** 写入后的总结文档

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `issue_id` | string | 是 |  |
| `body` | string | 是 |  |
| `author` | object | 是 |  |
| `author.kind` | string（enum: user \| agent） | 是 |  |
| `author.login` | string | 是 | agent 时为 "代理"；用户不存在时为 "未知用户" |
| `author.avatar_url` | string | 是 | 仅 user 作者可能有值，否则空串 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | JWT 调用仅限 API Key 的端点（40303） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/issues/{iid}/collab/summary`

<!-- operationId: deleteCollabSummary -->

删除完成总结（幂等，不存在也返回 200）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

## recommendations

Agent 推荐任务队列

### GET `/api/recommendations`

<!-- operationId: listIssueRecommendations -->

推荐任务列表（一次全量，无分页；active 在前 deferred 在后，组内按 priority 降序）

**鉴权**：JWT+API Key

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `project_id` | string | 否 | 项目 ID；缺省返回当前用户全部项目的推荐 |

**成功响应**

**200** 推荐列表

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].issue` | object | 是 |  |
| `items[].issue.id` | string | 是 |  |
| `items[].issue.project_id` | string | 是 |  |
| `items[].issue.project_name` | string | 是 |  |
| `items[].issue.source` | string（enum: github \| internal） | 是 |  |
| `items[].issue.sequence_number` | integer | 是 |  |
| `items[].issue.reference` | string | 是 |  |
| `items[].issue.title` | string | 是 |  |
| `items[].issue.state` | string（enum: open \| closed） | 是 |  |
| `items[].issue.workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `items[].reason` | string | 是 |  |
| `items[].priority` | string（enum: high \| medium \| low） | 是 |  |
| `items[].created_by` | string | 是 | 提交者 API Key 名称 |
| `items[].status` | string（enum: active \| deferred） | 是 | deferred 表示被用户延后；延后项对 Agent 不可再推荐（PUT 返回 40911） |
| `items[].deferred_at` | string（date-time，可空） | 是 | 延后时间；status=active 时为 null |
| `items[].defer_note` | string（可空） | 是 | 延后备注；无备注时为 null |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |
| `items[].dependencies` | object[] | 是 |  |
| `items[].dependencies[].issue_id` | string | 是 |  |
| `items[].dependencies[].title` | string | 是 |  |
| `items[].dependencies[].state` | string（enum: open \| closed） | 是 |  |
| `items[].dependencies[].workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `items[].dependencies[].project_id` | string | 是 |  |
| `items[].dependencies[].sequence_number` | integer | 是 |  |
| `items[].dependencies[].reference` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/issues/{iid}/recommendation`

<!-- operationId: upsertIssueRecommendation -->

写入/覆盖推荐（仅 API Key；JWT 返回 40303）

覆盖 upsert：同一 Issue 重复 PUT 整体替换 reason/priority/dependencies/created_by，保留原 created_at。目标须 state=open 且 workflow_status 为未设置或 todo，否则 40910。dependencies 整组替换，≤20 个 Issue UUID，不含自身；依赖须属当前用户的项目，不存在或越权统一 40405。已被用户延后的推荐不可再写入（40911）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `reason` | string | 是 | 推荐理由，trim 后 1-500 rune |
| `priority` | string（enum: high \| medium \| low） | 否 |  |
| `dependencies` | string[]（uuid，≤20 项） | 否 | 前置依赖 Issue UUID 数组，整组替换，不含自身；重复自动去重（保序） |

**成功响应**

**200** 写入后的推荐

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `issue` | object | 是 |  |
| `issue.id` | string | 是 |  |
| `issue.project_id` | string | 是 |  |
| `issue.project_name` | string | 是 |  |
| `issue.source` | string（enum: github \| internal） | 是 |  |
| `issue.sequence_number` | integer | 是 |  |
| `issue.reference` | string | 是 |  |
| `issue.title` | string | 是 |  |
| `issue.state` | string（enum: open \| closed） | 是 |  |
| `issue.workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `reason` | string | 是 |  |
| `priority` | string（enum: high \| medium \| low） | 是 |  |
| `created_by` | string | 是 | 提交者 API Key 名称 |
| `status` | string（enum: active \| deferred） | 是 | deferred 表示被用户延后；延后项对 Agent 不可再推荐（PUT 返回 40911） |
| `deferred_at` | string（date-time，可空） | 是 | 延后时间；status=active 时为 null |
| `defer_note` | string（可空） | 是 | 延后备注；无备注时为 null |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `dependencies` | object[] | 是 |  |
| `dependencies[].issue_id` | string | 是 |  |
| `dependencies[].title` | string | 是 |  |
| `dependencies[].state` | string（enum: open \| closed） | 是 |  |
| `dependencies[].workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `dependencies[].project_id` | string | 是 |  |
| `dependencies[].sequence_number` | integer | 是 |  |
| `dependencies[].reference` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | JWT 调用仅限 API Key 的端点（40303） |
| 404 | 目标或依赖 Issue 不存在（40405）、项目不存在（40401） |
| 409 | Issue 当前状态不可被推荐（40910）或推荐已被延后（40911） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/issues/{iid}/recommendation`

<!-- operationId: deleteIssueRecommendation -->

移除推荐（JWT 与 API Key 均可；删除=遗忘可再推荐，延后项仅 JWT 可删）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 推荐不存在（40411）或项目不存在（40401） |
| 409 | API Key 尝试删除延后态推荐（40911）；延后项须 JWT 删除 |
| 500 | 服务器内部错误（50000） |

### PUT `/api/issues/{iid}/recommendation/defer`

<!-- operationId: deferIssueRecommendation -->

延后推荐（仅 JWT）

冻结整条推荐（reason/priority/dependencies/created_by 原样保留），从推荐列表的 active 组消失且 Agent 不可再推荐（PUT recommendation 返回 40911）。仅作用于已存在的推荐行；重复调用覆盖 note 并刷新 deferred_at。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`application/json`，可选。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `note` | string（长度 ≤500） | 否 | 延后备注，trim 后 ≤500 rune；缺省或空串表示无备注 |

**成功响应**

**200** 延后后的推荐

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `issue` | object | 是 |  |
| `issue.id` | string | 是 |  |
| `issue.project_id` | string | 是 |  |
| `issue.project_name` | string | 是 |  |
| `issue.source` | string（enum: github \| internal） | 是 |  |
| `issue.sequence_number` | integer | 是 |  |
| `issue.reference` | string | 是 |  |
| `issue.title` | string | 是 |  |
| `issue.state` | string（enum: open \| closed） | 是 |  |
| `issue.workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `reason` | string | 是 |  |
| `priority` | string（enum: high \| medium \| low） | 是 |  |
| `created_by` | string | 是 | 提交者 API Key 名称 |
| `status` | string（enum: active \| deferred） | 是 | deferred 表示被用户延后；延后项对 Agent 不可再推荐（PUT 返回 40911） |
| `deferred_at` | string（date-time，可空） | 是 | 延后时间；status=active 时为 null |
| `defer_note` | string（可空） | 是 | 延后备注；无备注时为 null |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `dependencies` | object[] | 是 |  |
| `dependencies[].issue_id` | string | 是 |  |
| `dependencies[].title` | string | 是 |  |
| `dependencies[].state` | string（enum: open \| closed） | 是 |  |
| `dependencies[].workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `dependencies[].project_id` | string | 是 |  |
| `dependencies[].sequence_number` | integer | 是 |  |
| `dependencies[].reference` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | note 超长（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | API Key 调用仅限 JWT 的端点（40301） |
| 404 | 推荐不存在（40411）或项目不存在（40401） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/issues/{iid}/recommendation/defer`

<!-- operationId: restoreIssueRecommendation -->

恢复已延后的推荐（仅 JWT）

清空 deferred_at/defer_note 并把推荐移回 active 组，updated_at 刷新；未延后时调用为幂等成功。Issue 非 open 时返回 40910。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**成功响应**

**200** 恢复后的推荐

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `issue` | object | 是 |  |
| `issue.id` | string | 是 |  |
| `issue.project_id` | string | 是 |  |
| `issue.project_name` | string | 是 |  |
| `issue.source` | string（enum: github \| internal） | 是 |  |
| `issue.sequence_number` | integer | 是 |  |
| `issue.reference` | string | 是 |  |
| `issue.title` | string | 是 |  |
| `issue.state` | string（enum: open \| closed） | 是 |  |
| `issue.workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `reason` | string | 是 |  |
| `priority` | string（enum: high \| medium \| low） | 是 |  |
| `created_by` | string | 是 | 提交者 API Key 名称 |
| `status` | string（enum: active \| deferred） | 是 | deferred 表示被用户延后；延后项对 Agent 不可再推荐（PUT 返回 40911） |
| `deferred_at` | string（date-time，可空） | 是 | 延后时间；status=active 时为 null |
| `defer_note` | string（可空） | 是 | 延后备注；无备注时为 null |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `dependencies` | object[] | 是 |  |
| `dependencies[].issue_id` | string | 是 |  |
| `dependencies[].title` | string | 是 |  |
| `dependencies[].state` | string（enum: open \| closed） | 是 |  |
| `dependencies[].workflow_status` | string（enum: "" \| todo \| in_progress \| done） | 是 | 空串表示未设置（重置语义） |
| `dependencies[].project_id` | string | 是 |  |
| `dependencies[].sequence_number` | integer | 是 |  |
| `dependencies[].reference` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | API Key 调用仅限 JWT 的端点（40301） |
| 404 | 推荐不存在（40411）或项目不存在（40401） |
| 409 | Issue 当前状态不可被推荐（40910，如已关闭） |
| 500 | 服务器内部错误（50000） |

## artifacts

版本安装包上传、下载与删除

### POST `/api/versions/{vid}/artifacts`

<!-- operationId: uploadArtifact -->

上传版本安装包（multipart/form-data，字段名 file；同名覆盖）

仅 pending 状态版本可上传。`platform` 为可选表单字段（如 darwin-arm64）；`uploaded_by` 由服务端按凭证自动记录。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `vid` | string | 是 | 版本 ID（UUID） |

**请求体**

`multipart/form-data`，必填。表单字段，不是 JSON。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | binary | 是 | 安装包文件 |
| `platform` | string | 否 | 可选平台标识，如 darwin-arm64、linux-amd64 |

**成功响应**

**200** 上传的安装包记录

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `version_id` | string | 是 |  |
| `file_name` | string | 是 |  |
| `file_size` | integer（int64） | 是 |  |
| `platform` | string | 是 |  |
| `uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `uploaded_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 版本非 pending 状态（40904） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/artifacts/{aid}`

<!-- operationId: deleteArtifact -->

删除安装包（所属版本须 pending）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `aid` | string | 是 | 安装包 ID（UUID） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 版本非 pending 状态（40904） |
| 500 | 服务器内部错误（50000） |

### GET `/api/artifacts/{aid}/download`

<!-- operationId: downloadArtifact -->

下载安装包

返回 `application/octet-stream` 与 `Content-Disposition: attachment`。支持 Authorization 头或 `?token=` query 凭证（供浏览器直链下载）。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `aid` | string | 是 | 安装包 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 否 | 可选 query 凭证（JWT 或 `fsk_` API Key），供无法携带 Authorization 头的场景（`<img>`、浏览器直链下载）。与 Authorization 头二选一，头优先。 |

**成功响应**

**200** 安装包二进制内容

二进制内容（application/octet-stream），无 JSON 信封。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

## logs

日志分片上传与查询

### GET `/api/projects/{id}/logs`

<!-- operationId: listLogEntries -->

分页查询日志条目

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `run_id` | string | 否 | 按运行 ID 过滤 |
| `level` | string（enum: debug \| info \| warn \| error \| fatal） | 否 | 按日志级别过滤 |
| `entry_source` | string | 否 | 按条目 source 过滤 |
| `q` | string | 否 | 按 message 内容过滤 |
| `from` | string（date-time） | 否 | 起始时间（RFC3339），按条目 timestamp 过滤 |
| `to` | string（date-time） | 否 | 结束时间（RFC3339） |
| `sort` | string（enum: timestamp_desc \| timestamp_asc，默认 timestamp_desc） | 否 | 排序；非法值返回 40001 |
| `page` | integer（≥1，默认 1） | 否 | 页码，<1 按 1 处理 |
| `page_size` | integer（1..100，默认 50） | 否 | 每页条数；越界（<1 或 >100）回落为 50 |

**成功响应**

**200** 日志条目分页列表

`data`：

分页响应 data 形状；与 Envelope allOf 组合后 items 由兄弟 schema 收窄为具体类型

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].run_id` | string | 是 |  |
| `items[].timestamp` | string（date-time） | 是 |  |
| `items[].level` | string（enum: debug \| info \| warn \| error \| fatal） | 是 |  |
| `items[].source` | string | 是 |  |
| `items[].message` | string | 是 |  |
| `items[].metadata` | string | 否 | omitempty；JSON 文本（非对象） |
| `items[].created_at` | string（date-time） | 是 |  |
| `total` | integer（int64） | 是 | 符合条件的总条数 |
| `page` | integer | 是 |  |
| `page_size` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/projects/{id}/logs`

<!-- operationId: uploadLogs -->

上传日志分片（仅 API Key；JWT 返回 40303）

同一项目同 `run_id` 合并为一次运行；`(run_id, chunk_id)` 幂等，重复 chunk 返回 200 且 `duplicate: true`、`accepted_count: 0`，不插入。
`source`/`description` 仅创建该运行时写入（先到先得）。body 总大小 ≤4MB（超出裸 413）；一次运行累计 ≤50000 条，超出整包拒绝（40909）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `run_id` | string（pattern: ^[A-Za-z0-9_-]{1,128}$） | 是 | 客户端生成的运行 ID，项目内唯一 |
| `chunk_id` | string（pattern: ^[A-Za-z0-9_-]{1,128}$） | 是 | 分片 ID；同 (run_id, chunk_id) 重试幂等，不要复用 chunk_id 传不同内容 |
| `source` | string（长度 ≤128） | 否 | ≤128 字节；仅首次创建运行时写入 |
| `description` | string（长度 ≤500） | 否 | ≤500 字节；仅首次创建运行时写入 |
| `entries` | object[]（1..500 项） | 是 |  |
| `entries[].timestamp` | string（date-time） | 是 | 必填，不能为零值时间 |
| `entries[].level` | string（enum: debug \| info \| warn \| error \| fatal） | 是 |  |
| `entries[].source` | string（长度 ≤128） | 否 | ≤128 字节 |
| `entries[].message` | string | 是 | 1-4000 字节 |
| `entries[].metadata` | object | 否 | 可选 JSON 对象，序列化后 ≤4KB |

**成功响应**

**200** 上传结果（含 accepted_count 与 duplicate）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `run_id` | string | 是 |  |
| `source` | string | 是 |  |
| `description` | string | 是 |  |
| `entry_count` | integer | 是 | 该运行累计条目数 |
| `first_entry_at` | string（date-time，可空） | 是 |  |
| `last_entry_at` | string（date-time，可空） | 是 |  |
| `accepted_count` | integer | 是 | 本次实际写入条数；重复 chunk 为 0 |
| `duplicate` | boolean | 是 | (run_id, chunk_id) 重复时为 true，本次不插入 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | JWT 调用仅限 API Key 的端点（40303） |
| 404 | 资源不存在（40400-40499） |
| 409 | 该运行日志条数已达上限（40909） |
| 413 | 请求体超过大小限制（裸 413，无 JSON body） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/projects/{id}/logs`

<!-- operationId: deleteProjectLogs -->

清空项目全部日志

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/projects/{id}/log-runs`

<!-- operationId: listLogRuns -->

分页查询日志运行列表

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `run_id` | string | 否 | 按运行 ID 过滤 |
| `source` | string | 否 | 按运行 source 过滤 |
| `from` | string（date-time） | 否 | 起始时间（RFC3339），按运行创建时间过滤 |
| `to` | string（date-time） | 否 | 结束时间（RFC3339） |
| `page` | integer（≥1，默认 1） | 否 | 页码，<1 按 1 处理 |
| `page_size` | integer（1..100，默认 50） | 否 | 每页条数；越界（<1 或 >100）回落为 50 |

**成功响应**

**200** 运行分页列表

`data`：

分页响应 data 形状；与 Envelope allOf 组合后 items 由兄弟 schema 收窄为具体类型

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].run_id` | string | 是 |  |
| `items[].source` | string | 是 |  |
| `items[].description` | string | 是 |  |
| `items[].entry_count` | integer | 是 |  |
| `items[].first_entry_at` | string（date-time，可空） | 是 |  |
| `items[].last_entry_at` | string（date-time，可空） | 是 |  |
| `items[].uploader_api_key_id` | string | 否 | omitempty |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |
| `total` | integer（int64） | 是 | 符合条件的总条数 |
| `page` | integer | 是 |  |
| `page_size` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/projects/{id}/log-runs/{run_id}`

<!-- operationId: getLogRun -->

获取单次运行详情

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |
| `run_id` | string | 是 | 日志运行 ID（客户端生成，项目内唯一） |

**成功响应**

**200** 运行详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `project_id` | string | 是 |  |
| `run_id` | string | 是 |  |
| `source` | string | 是 |  |
| `description` | string | 是 |  |
| `entry_count` | integer | 是 |  |
| `first_entry_at` | string（date-time，可空） | 是 |  |
| `last_entry_at` | string（date-time，可空） | 是 |  |
| `uploader_api_key_id` | string | 否 | omitempty |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 运行不存在或项目不存在（40409） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/projects/{id}/log-runs/{run_id}`

<!-- operationId: deleteLogRun -->

删除单次运行及其条目

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |
| `run_id` | string | 是 | 日志运行 ID（客户端生成，项目内唯一） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 运行不存在或项目不存在（40409） |
| 500 | 服务器内部错误（50000） |

## documents

项目文档

### GET `/api/projects/{id}/documents`

<!-- operationId: listDocuments -->

列出项目全部文档（不分页）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 文档列表（data 为 {items, total}，非分页信封）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].parent_id` | string（可空） | 是 |  |
| `items[].title` | string | 是 |  |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |
| `total` | integer（int64） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/projects/{id}/documents`

<!-- operationId: createDocument -->

创建文档（body 上限 200000 rune；请求体 ≤1MB，超出裸 413）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `title` | string | 是 | trim 后 1-200 rune |
| `body` | string | 否 | ≤200000 rune |
| `parent_id` | string | 否 | 父文档 ID；须同项目，深度 ≤64 |

**成功响应**

**200** 创建的文档

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `parent_id` | string（可空） | 是 |  |
| `title` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `body` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 413 | 请求体超过大小限制（裸 413，无 JSON body） |
| 500 | 服务器内部错误（50000） |

### GET `/api/documents/{doc_id}`

<!-- operationId: getDocument -->

获取文档详情

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `doc_id` | string | 是 | 文档 ID（UUID） |

**成功响应**

**200** 文档详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `parent_id` | string（可空） | 是 |  |
| `title` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `body` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/documents/{doc_id}`

<!-- operationId: updateDocument -->

更新文档（至少传一个字段；请求体 ≤1MB，超出裸 413）

parent_id 三态：不传=不变，null=清除父级，字符串=设为父文档（须同项目且不形成环，深度 ≤64）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `doc_id` | string | 是 | 文档 ID（UUID） |

**请求体**

`application/json`，必填。

title/body/parent_id 至少传一个

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `title` | string | 否 | trim 后 1-200 rune |
| `body` | string | 否 | ≤200000 rune |
| `parent_id` | string（可空） | 否 | 三态：不传=不变；null=清除父级；字符串=设为父文档（须同项目且无环） |

**成功响应**

**200** 更新后的文档

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `parent_id` | string（可空） | 是 |  |
| `title` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `body` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 413 | 请求体超过大小限制（裸 413，无 JSON body） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/documents/{doc_id}`

<!-- operationId: deleteDocument -->

删除文档

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `doc_id` | string | 是 | 文档 ID（UUID） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

## dashboard

看板概览

### GET `/api/dashboard/overview`

<!-- operationId: dashboardOverview -->

看板概览（各项目 open issue 数 + 近 30 天每日解决数）

**鉴权**：JWT+API Key

**成功响应**

**200** 概览数据

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `open_issues_by_project` | object[] | 是 |  |
| `open_issues_by_project[].project_id` | string | 是 |  |
| `open_issues_by_project[].project_name` | string | 是 |  |
| `open_issues_by_project[].open_issue_count` | integer | 是 |  |
| `daily_resolved` | object[] | 是 | 固定近 30 天（含今天），逐日排列 |
| `daily_resolved[].date` | string | 是 | YYYY-MM-DD |
| `daily_resolved[].resolved_count` | integer | 是 |  |
| `daily_resolved[].projects` | object[] | 是 | 当日无数据的项目不出现（可为 null/空） |
| `daily_resolved[].projects[].project_id` | string | 是 |  |
| `daily_resolved[].projects[].project_name` | string | 是 |  |
| `daily_resolved[].projects[].count` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

## media

GitHub 媒体代理

### GET `/api/github/media-proxy`

<!-- operationId: githubMediaProxy -->

GitHub 媒体代理（图片/视频）

代理拉取并缓存 GitHub 附件/图片等媒体 URL，支持 `Range` 头与缓存命中直出。
错误响应为 `text/plain`（非 JSON 信封）：400 invalid url、415 unsupported media type、
其他上游错误按状态码透传或 502。同一路径另注册 HEAD 变体，语义同 GET 但不返回 body。

**鉴权**：JWT+API Key，支持 ?token=

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `url` | string | 是 | 要代理的媒体 URL（会做 HTML unescape 与多至两次 URL decode） |
| `token` | string | 否 | 可选 query 凭证（JWT 或 `fsk_` API Key），供无法携带 Authorization 头的场景（`<img>`、浏览器直链下载）。与 Authorization 头二选一，头优先。 |

**成功响应**

**200** 上游媒体内容（透传 Content-Type 与缓存控制头）

二进制内容（application/octet-stream），无 JSON 信封。

**206** Range 请求的部分内容

二进制内容（application/octet-stream），无 JSON 信封。

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 无效的媒体 URL（text/plain） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 缓存文件不存在（text/plain） |
| 413 | 上游资源超过缓存大小上限时改为透传（text/plain） |
| 415 | 不支持的媒体类型（text/plain） |
| 502 | 拉取上游媒体失败（text/plain） |

### HEAD `/api/github/media-proxy`

<!-- operationId: githubMediaProxyHead -->

GitHub 媒体代理（HEAD，仅头信息）

与 GET 语义相同但不返回 body，用于探测 Content-Length/Type。

**鉴权**：JWT+API Key，支持 ?token=

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `url` | string | 是 | 要代理的媒体 URL |
| `token` | string | 否 | 可选 query 凭证（JWT 或 `fsk_` API Key），供无法携带 Authorization 头的场景（`<img>`、浏览器直链下载）。与 Authorization 头二选一，头优先。 |

**成功响应**

**200** 仅响应头

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 无效的媒体 URL（text/plain） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 502 | 拉取上游媒体失败（text/plain） |

## screenshots

项目截图库（按界面聚合，版本全量保留）

### GET `/api/projects/{id}/screenshots`

<!-- operationId: listScreenshotScreens -->

列出项目全部界面（last_uploaded_at 倒序）

每项为界面字段 + version_count + latest_version（最新一个版本，无版本时为 null，实际不会发生）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 界面列表（data 为 {items}，非分页信封）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].screen_key` | string | 是 | 服务端规范化（ToLower+TrimSpace）后的界面标识 |
| `items[].title` | string | 是 | 可为空串，客户端回退显示 screen_key |
| `items[].group` | string | 是 | 自由文本分组；空串表示未分组 |
| `items[].version_count` | integer | 是 | 该界面累计保留的版本数 |
| `items[].last_uploaded_at` | string（date-time） | 是 | 最近一次上传时间，列表按它倒序 |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].latest_version` | object（可空） | 是 | 最新一个版本；无版本时为 null |
| `items[].latest_version.id` | string | 是 |  |
| `items[].latest_version.screen_id` | string | 是 |  |
| `items[].latest_version.note` | string | 是 |  |
| `items[].latest_version.file_name` | string | 是 |  |
| `items[].latest_version.file_size` | integer（int64） | 是 |  |
| `items[].latest_version.mime_type` | string | 是 | 上传时内容嗅探得到的类型（image/png、image/jpeg、image/webp、image/gif 之一） |
| `items[].latest_version.uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `items[].latest_version.uploaded_at` | string（date-time） | 是 |  |
| `items[].latest_version.content_url` | string | 是 | /api/screenshot-versions/{vid}/content；前端自行追加 ?token= |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/projects/{id}/screenshots`

<!-- operationId: uploadScreenshot -->

上传界面截图（multipart/form-data，字段名 file + screen_key）

multipart 字段：`file`（必填；内容嗅探须为 image/png、image/jpeg、image/webp、image/gif，大小受服务端 upload.max_file_size 限制）、
`screen_key`（必填；服务端按 ToLower+TrimSpace 规范化，规范化后须 1~100 字符，否则 40001）、
`group`/`title`/`note`（可选）。
同 project 下 screen_key 唯一对应一个界面：首次出现自动建界面，重复上传归并为新版本（版本全量保留，不去重、不清理）。
表单含 `group` 字段（允许空串=未分组）即更新界面分组；`title` 非空即更新界面标题；`note` 记在该版本上。
`uploaded_by` 由服务端按凭证自动记录（用户名，或 "API Key: <name>"）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`multipart/form-data`，必填。表单字段，不是 JSON。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | binary | 是 | 截图文件；内容须为 image/png、image/jpeg、image/webp、image/gif |
| `screen_key` | string | 是 | 界面标识；规范化后 1~100 字符 |
| `group` | string | 否 | 分组名；字段出现即更新（空串置为未分组），不出现则保持原值 |
| `title` | string | 否 | 界面标题；非空才更新 |
| `note` | string | 否 | 本次上传版本的备注 |

**成功响应**

**200** 上传结果（screen 为归并后的界面，version 为新建版本）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `screen` | object | 是 |  |
| `screen.id` | string | 是 |  |
| `screen.project_id` | string | 是 |  |
| `screen.screen_key` | string | 是 | 服务端规范化（ToLower+TrimSpace）后的界面标识 |
| `screen.title` | string | 是 | 可为空串，客户端回退显示 screen_key |
| `screen.group` | string | 是 | 自由文本分组；空串表示未分组 |
| `screen.version_count` | integer | 是 | 该界面累计保留的版本数 |
| `screen.last_uploaded_at` | string（date-time） | 是 | 最近一次上传时间，列表按它倒序 |
| `screen.created_at` | string（date-time） | 是 |  |
| `version` | object | 是 |  |
| `version.id` | string | 是 |  |
| `version.screen_id` | string | 是 |  |
| `version.note` | string | 是 |  |
| `version.file_name` | string | 是 |  |
| `version.file_size` | integer（int64） | 是 |  |
| `version.mime_type` | string | 是 | 上传时内容嗅探得到的类型（image/png、image/jpeg、image/webp、image/gif 之一） |
| `version.uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `version.uploaded_at` | string（date-time） | 是 |  |
| `version.content_url` | string | 是 | /api/screenshot-versions/{vid}/content；前端自行追加 ?token= |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/screenshot-screens/{sid}`

<!-- operationId: getScreenshotScreen -->

获取界面详情（含全部版本，uploaded_at 倒序）

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sid` | string | 是 | 截图界面 ID（UUID） |

**成功响应**

**200** 界面详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `screen_key` | string | 是 | 服务端规范化（ToLower+TrimSpace）后的界面标识 |
| `title` | string | 是 | 可为空串，客户端回退显示 screen_key |
| `group` | string | 是 | 自由文本分组；空串表示未分组 |
| `version_count` | integer | 是 | 该界面累计保留的版本数 |
| `last_uploaded_at` | string（date-time） | 是 | 最近一次上传时间，列表按它倒序 |
| `created_at` | string（date-time） | 是 |  |
| `versions` | object[] | 是 | 全部历史版本，uploaded_at 倒序 |
| `versions[].id` | string | 是 |  |
| `versions[].screen_id` | string | 是 |  |
| `versions[].note` | string | 是 |  |
| `versions[].file_name` | string | 是 |  |
| `versions[].file_size` | integer（int64） | 是 |  |
| `versions[].mime_type` | string | 是 | 上传时内容嗅探得到的类型（image/png、image/jpeg、image/webp、image/gif 之一） |
| `versions[].uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `versions[].uploaded_at` | string（date-time） | 是 |  |
| `versions[].content_url` | string | 是 | /api/screenshot-versions/{vid}/content；前端自行追加 ?token= |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PATCH `/api/screenshot-screens/{sid}`

<!-- operationId: updateScreenshotScreen -->

更新界面分组/标题（仅 JWT）

group 与 title 均为可选指针字段：字段出现才更新（group 允许空串置为未分组），都不传返回 40001。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sid` | string | 是 | 截图界面 ID（UUID） |

**请求体**

`application/json`，必填。

两字段均可选，出现才更新；都不传返回 40001

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `group` | string | 否 | 分组名；允许空串置为未分组 |
| `title` | string | 否 | 界面标题 |

**成功响应**

**200** 更新后的界面详情（含全部版本）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `screen_key` | string | 是 | 服务端规范化（ToLower+TrimSpace）后的界面标识 |
| `title` | string | 是 | 可为空串，客户端回退显示 screen_key |
| `group` | string | 是 | 自由文本分组；空串表示未分组 |
| `version_count` | integer | 是 | 该界面累计保留的版本数 |
| `last_uploaded_at` | string（date-time） | 是 | 最近一次上传时间，列表按它倒序 |
| `created_at` | string（date-time） | 是 |  |
| `versions` | object[] | 是 | 全部历史版本，uploaded_at 倒序 |
| `versions[].id` | string | 是 |  |
| `versions[].screen_id` | string | 是 |  |
| `versions[].note` | string | 是 |  |
| `versions[].file_name` | string | 是 |  |
| `versions[].file_size` | integer（int64） | 是 |  |
| `versions[].mime_type` | string | 是 | 上传时内容嗅探得到的类型（image/png、image/jpeg、image/webp、image/gif 之一） |
| `versions[].uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `versions[].uploaded_at` | string（date-time） | 是 |  |
| `versions[].content_url` | string | 是 | /api/screenshot-versions/{vid}/content；前端自行追加 ?token= |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/screenshot-screens/{sid}`

<!-- operationId: deleteScreenshotScreen -->

删除界面及其全部版本与磁盘文件（仅 JWT）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sid` | string | 是 | 截图界面 ID（UUID） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/screenshot-versions/{vid}`

<!-- operationId: deleteScreenshotVersion -->

删除单个截图版本与文件（仅 JWT）

若删除的是该界面最后一个版本，连带删除界面本身。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `vid` | string | 是 | 截图版本 ID（UUID） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/screenshot-versions/{vid}/content`

<!-- operationId: getScreenshotVersionContent -->

获取截图版本图片内容

返回图片二进制内容（存储时嗅探的 mime type，`Content-Disposition: inline`，`Cache-Control: private, max-age=300`）。支持 Authorization 头或 `?token=` query 凭证（供 `<img>` 直链）。同一路径另注册 HEAD 变体，语义同 GET 但不返回 body。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `vid` | string | 是 | 截图版本 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 否 | 可选 query 凭证（JWT 或 `fsk_` API Key），供无法携带 Authorization 头的场景（`<img>`、浏览器直链下载）。与 Authorization 头二选一，头优先。 |

**成功响应**

**200** 图片二进制内容

二进制内容（application/octet-stream），无 JSON 信封。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### HEAD `/api/screenshot-versions/{vid}/content`

<!-- operationId: getScreenshotVersionContentHead -->

获取截图版本图片头信息（HEAD）

与 GET 语义相同但不返回 body。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `vid` | string | 是 | 截图版本 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 否 | 可选 query 凭证（JWT 或 `fsk_` API Key），供无法携带 Authorization 头的场景（`<img>`、浏览器直链下载）。与 Authorization 头二选一，头优先。 |

**成功响应**

**200** 仅响应头

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |
