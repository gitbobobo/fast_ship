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

## github-tokens

用户级 GitHub Token 库（仅 JWT）

### GET `/api/github-tokens`

<!-- operationId: listGithubTokens -->

列出当前用户的 GitHub Token

不回显明文；附带每条的引用项目与检测摘要。列表不触发任何 GitHub 请求。

**鉴权**：仅 JWT

**成功响应**

**200** Token 列表

`data` 为数组。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `title` | string | 是 |  |
| `token_prefix` | string | 是 | Token 明文前 8 个字符（如 ghp_Ab12），用于列表辨识；其余不存 |
| `detection_status` | string（enum: unchecked \| ok \| invalid \| failed） | 是 | unchecked=未检测；ok=认证成功；invalid=GitHub 拒绝凭证（401）；failed=检测失败（网络/GitHub 5xx，不是凭证无效） |
| `detection_token_type` | string（enum: classic \| fine_grained \| unknown） | 是 | classic=有 OAuth scopes 的 Classic PAT；fine_grained=github_pat_ 前缀；unknown=无法判断（未检测时恒为 unknown） |
| `detection_scopes` | string[] | 是 | Classic PAT 的 OAuth scopes；fine-grained 恒为空数组 |
| `detection_login` | string | 是 | 认证成功时的 GitHub login，否则为空串 |
| `detection_detail` | string | 是 | 给人看的补充说明（fine-grained 的「无法自动获取完整授权」、失败原因等），不含 token |
| `detected_at` | string（date-time，可空） | 否 | 最近一次检测完成时间；未检测为 null |
| `referenced_projects` | object[] | 是 | 两个用途中任一引用了该 Token 的项目 |
| `referenced_projects[].id` | string | 是 |  |
| `referenced_projects[].name` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### POST `/api/github-tokens`

<!-- operationId: createGithubToken -->

添加 GitHub Token

同一用户下明文完全相同（SHA-256 指纹一致）的 Token 复用已有记录：返回已有记录，
不修改其标题。新建成功后服务端立即做一次只读检测（GET /user）；检测失败
（网络 / GitHub 5xx）或凭证无效（401）仍然保存，响应中 detection_status
为 failed / invalid，HTTP 状态仍是 200。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `title` | string（长度 1..100） | 是 | Token 标题（如「主仓库发布 Token」） |
| `token` | string（长度 ≥1） | 是 | GitHub PAT 明文（加密存储，任何响应都不回显） |

**成功响应**

**200** 保存成功（明文重复时返回已有记录）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `title` | string | 是 |  |
| `token_prefix` | string | 是 | Token 明文前 8 个字符（如 ghp_Ab12），用于列表辨识；其余不存 |
| `detection_status` | string（enum: unchecked \| ok \| invalid \| failed） | 是 | unchecked=未检测；ok=认证成功；invalid=GitHub 拒绝凭证（401）；failed=检测失败（网络/GitHub 5xx，不是凭证无效） |
| `detection_token_type` | string（enum: classic \| fine_grained \| unknown） | 是 | classic=有 OAuth scopes 的 Classic PAT；fine_grained=github_pat_ 前缀；unknown=无法判断（未检测时恒为 unknown） |
| `detection_scopes` | string[] | 是 | Classic PAT 的 OAuth scopes；fine-grained 恒为空数组 |
| `detection_login` | string | 是 | 认证成功时的 GitHub login，否则为空串 |
| `detection_detail` | string | 是 | 给人看的补充说明（fine-grained 的「无法自动获取完整授权」、失败原因等），不含 token |
| `detected_at` | string（date-time，可空） | 否 | 最近一次检测完成时间；未检测为 null |
| `referenced_projects` | object[] | 是 | 两个用途中任一引用了该 Token 的项目 |
| `referenced_projects[].id` | string | 是 |  |
| `referenced_projects[].name` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/github-tokens/{id}`

<!-- operationId: updateGithubToken -->

修改标题或替换密钥

改标题不重新检测。替换密钥即对所有引用该 Token 的项目一起生效；
新明文与同用户另一条记录指纹重复时返回 40914（消息指向已有记录），
不做隐式合并。替换按加载时的指纹条件写入，并发替换返回 40915。
替换后服务端重新检测。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `title` | string（长度 1..100） | 否 | 非空时替换标题；改标题不重新检测 |
| `token` | string（长度 ≥1） | 否 | 非空时替换密钥，对所有引用项目立即生效并重新检测；与同用户另一条记录明文重复时返回 40914；并发替换时密钥已变返回 40915 |

**成功响应**

**200** 更新后的 Token 记录

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `title` | string | 是 |  |
| `token_prefix` | string | 是 | Token 明文前 8 个字符（如 ghp_Ab12），用于列表辨识；其余不存 |
| `detection_status` | string（enum: unchecked \| ok \| invalid \| failed） | 是 | unchecked=未检测；ok=认证成功；invalid=GitHub 拒绝凭证（401）；failed=检测失败（网络/GitHub 5xx，不是凭证无效） |
| `detection_token_type` | string（enum: classic \| fine_grained \| unknown） | 是 | classic=有 OAuth scopes 的 Classic PAT；fine_grained=github_pat_ 前缀；unknown=无法判断（未检测时恒为 unknown） |
| `detection_scopes` | string[] | 是 | Classic PAT 的 OAuth scopes；fine-grained 恒为空数组 |
| `detection_login` | string | 是 | 认证成功时的 GitHub login，否则为空串 |
| `detection_detail` | string | 是 | 给人看的补充说明（fine-grained 的「无法自动获取完整授权」、失败原因等），不含 token |
| `detected_at` | string（date-time，可空） | 否 | 最近一次检测完成时间；未检测为 null |
| `referenced_projects` | object[] | 是 | 两个用途中任一引用了该 Token 的项目 |
| `referenced_projects[].id` | string | 是 |  |
| `referenced_projects[].name` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 新明文与同用户另一条记录重复（40914）；并发替换时密钥已变（40915） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/github-tokens/{id}`

<!-- operationId: deleteGithubToken -->

删除 GitHub Token

仍被任何项目（任一用途）引用时返回 40913，需先解除或更换引用；删除项目不影响 Token 库。

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
| 409 | Token 仍被项目引用（40913） |
| 500 | 服务器内部错误（50000） |

### POST `/api/github-tokens/{id}/detect`

<!-- operationId: detectGithubToken -->

手动刷新检测

对该 Token 做一次只读检测（GET https://api.github.com/user），刷新检测状态与权限摘要；不创建任何 GitHub 资源。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**成功响应**

**200** 检测完成后的 Token 记录（检测失败时 detection_status=failed，仍是 200）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `title` | string | 是 |  |
| `token_prefix` | string | 是 | Token 明文前 8 个字符（如 ghp_Ab12），用于列表辨识；其余不存 |
| `detection_status` | string（enum: unchecked \| ok \| invalid \| failed） | 是 | unchecked=未检测；ok=认证成功；invalid=GitHub 拒绝凭证（401）；failed=检测失败（网络/GitHub 5xx，不是凭证无效） |
| `detection_token_type` | string（enum: classic \| fine_grained \| unknown） | 是 | classic=有 OAuth scopes 的 Classic PAT；fine_grained=github_pat_ 前缀；unknown=无法判断（未检测时恒为 unknown） |
| `detection_scopes` | string[] | 是 | Classic PAT 的 OAuth scopes；fine-grained 恒为空数组 |
| `detection_login` | string | 是 | 认证成功时的 GitHub login，否则为空串 |
| `detection_detail` | string | 是 | 给人看的补充说明（fine-grained 的「无法自动获取完整授权」、失败原因等），不含 token |
| `detected_at` | string（date-time，可空） | 否 | 最近一次检测完成时间；未检测为 null |
| `referenced_projects` | object[] | 是 | 两个用途中任一引用了该 Token 的项目 |
| `referenced_projects[].id` | string | 是 |  |
| `referenced_projects[].name` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

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
| `items[].public_github_token_id` | string | 是 | 公开仓库 Token 引用；未配置为空串 |
| `items[].code_github_token_id` | string | 是 | 代码仓库 Token 引用；未配置为空串 |
| `items[].latest_version` | object | 否 | omitempty，仅在列表项中出现 |
| `items[].latest_version.id` | string | 是 |  |
| `items[].latest_version.version_number` | string | 是 |  |
| `items[].latest_version.status` | string（enum: pending \| shipped） | 是 |  |
| `items[].latest_version.created_at` | string（date-time） | 是 |  |
| `items[].issue_count` | integer | 否 | omitempty，仅在列表项中出现；项目 Issue 总数（不区分状态） |
| `items[].issue_workflow_counts` | object | 否 | omitempty，仅在列表项中出现；仅统计 state=open 的 Issue，按内部状态分组 |
| `items[].issue_workflow_counts.unset` | integer | 是 | 未设置内部状态的 Issue 数（含无 internal meta 行的 Issue） |
| `items[].issue_workflow_counts.todo` | integer | 是 |  |
| `items[].issue_workflow_counts.in_progress` | integer | 是 |  |
| `items[].issue_workflow_counts.done` | integer | 是 |  |
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
| `repository_url` | string | 否 | GitHub 仓库链接（owner/repo 或完整 URL）；不再要求同时提供 Token |
| `public_github_token_id` | string | 否 | 公开仓库 Token（Token 库记录 id）引用；用于 Issue 同步与 GitHub 写、分支查询、发货。可空；必须属于当前用户，否则 40420 |
| `code_github_token_id` | string | 否 | 代码仓库 Token（Token 库记录 id）引用；仅用于读取关联 PR（含其他仓库）。可空；必须属于当前用户，否则 40420 |

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
| `public_github_token_id` | string | 是 | 公开仓库 Token 引用；未配置为空串 |
| `code_github_token_id` | string | 是 | 代码仓库 Token 引用；未配置为空串 |
| `latest_version` | object | 否 | omitempty，仅在列表项中出现 |
| `latest_version.id` | string | 是 |  |
| `latest_version.version_number` | string | 是 |  |
| `latest_version.status` | string（enum: pending \| shipped） | 是 |  |
| `latest_version.created_at` | string（date-time） | 是 |  |
| `issue_count` | integer | 否 | omitempty，仅在列表项中出现；项目 Issue 总数（不区分状态） |
| `issue_workflow_counts` | object | 否 | omitempty，仅在列表项中出现；仅统计 state=open 的 Issue，按内部状态分组 |
| `issue_workflow_counts.unset` | integer | 是 | 未设置内部状态的 Issue 数（含无 internal meta 行的 Issue） |
| `issue_workflow_counts.todo` | integer | 是 |  |
| `issue_workflow_counts.in_progress` | integer | 是 |  |
| `issue_workflow_counts.done` | integer | 是 |  |
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
| `public_github_token_id` | string | 是 | 公开仓库 Token 引用；未配置为空串 |
| `code_github_token_id` | string | 是 | 代码仓库 Token 引用；未配置为空串 |
| `latest_version` | object | 否 | omitempty，仅在列表项中出现 |
| `latest_version.id` | string | 是 |  |
| `latest_version.version_number` | string | 是 |  |
| `latest_version.status` | string（enum: pending \| shipped） | 是 |  |
| `latest_version.created_at` | string（date-time） | 是 |  |
| `issue_count` | integer | 否 | omitempty，仅在列表项中出现；项目 Issue 总数（不区分状态） |
| `issue_workflow_counts` | object | 否 | omitempty，仅在列表项中出现；仅统计 state=open 的 Issue，按内部状态分组 |
| `issue_workflow_counts.unset` | integer | 是 | 未设置内部状态的 Issue 数（含无 internal meta 行的 Issue） |
| `issue_workflow_counts.todo` | integer | 是 |  |
| `issue_workflow_counts.in_progress` | integer | 是 |  |
| `issue_workflow_counts.done` | integer | 是 |  |
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
| `description` | string | 否 | 缺省或 null 保留现值；显式空串清空；非空替换 |
| `repository_url` | string | 否 | 同创建；不再要求项目已配 Token |
| `public_github_token_id` | string（可空） | 否 | 三态——key 缺省保留现值；显式 null 或空串清空该用途引用；非空替换（须属于当前用户，否则 40420） |
| `code_github_token_id` | string（可空） | 否 | 三态——key 缺省保留现值；显式 null 或空串清空该用途引用；非空替换（须属于当前用户，否则 40420） |

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
| `public_github_token_id` | string | 是 | 公开仓库 Token 引用；未配置为空串 |
| `code_github_token_id` | string | 是 | 代码仓库 Token 引用；未配置为空串 |
| `latest_version` | object | 否 | omitempty，仅在列表项中出现 |
| `latest_version.id` | string | 是 |  |
| `latest_version.version_number` | string | 是 |  |
| `latest_version.status` | string（enum: pending \| shipped） | 是 |  |
| `latest_version.created_at` | string（date-time） | 是 |  |
| `issue_count` | integer | 否 | omitempty，仅在列表项中出现；项目 Issue 总数（不区分状态） |
| `issue_workflow_counts` | object | 否 | omitempty，仅在列表项中出现；仅统计 state=open 的 Issue，按内部状态分组 |
| `issue_workflow_counts.unset` | integer | 是 | 未设置内部状态的 Issue 数（含无 internal meta 行的 Issue） |
| `issue_workflow_counts.todo` | integer | 是 |  |
| `issue_workflow_counts.in_progress` | integer | 是 |  |
| `issue_workflow_counts.done` | integer | 是 |  |
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
| 400 | 项目未关联 GitHub 仓库（40003）或已关联仓库但未配置公开仓库 Token（40006） |
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

**200** Issue 分页列表（列表项不含 collab / pull_requests / attachments 字段；带 pull_request_summary 聚合计数）

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
| `items[].attachments` | object[] | 否 | omitempty；仅 Issue 详情响应携带（无附件时缺省）；列表项不出现 |
| `items[].attachments[].id` | string | 是 |  |
| `items[].attachments[].file_name` | string | 是 |  |
| `items[].attachments[].file_size` | integer（int64） | 是 |  |
| `items[].attachments[].mime_type` | string | 是 | 内容嗅探结果（http.DetectContentType），供 UI 图标使用 |
| `items[].attachments[].uploader` | string | 是 | 用户名或 "API Key: <name>" |
| `items[].attachments[].download_url` | string | 是 | /api/attachments/{id}/download |
| `items[].attachments[].created_at` | string（date-time） | 是 |  |
| `items[].screenshot_annotations` | object[] | 否 | omitempty；仅 Issue 详情响应携带（无关联标注时缺省）；列表项不出现 |
| `items[].screenshot_annotations[].id` | string | 是 |  |
| `items[].screenshot_annotations[].project_id` | string | 是 |  |
| `items[].screenshot_annotations[].screen_id` | string | 是 | 所属界面 ID（由版本冗余带出，便于过滤） |
| `items[].screenshot_annotations[].version_id` | string | 是 | 标注挂在该版本上；版本删除时标注级联删除 |
| `items[].screenshot_annotations[].issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `items[].screenshot_annotations[].issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `items[].screenshot_annotations[].issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `items[].screenshot_annotations[].screen_key` | string | 是 |  |
| `items[].screenshot_annotations[].screen_title` | string | 是 | 界面显示名；空串时客户端回退展示 screen_key |
| `items[].screenshot_annotations[].screen_group` | string | 是 | 界面分组；空串表示未分组 |
| `items[].screenshot_annotations[].image_url` | string | 是 | 标注所在版本的原图地址（版本 content_url）；追加 ?token= 供 <img> |
| `items[].screenshot_annotations[].image_width` | integer | 是 | 原图宽像素；0 表示未知（存量版本未记录尺寸） |
| `items[].screenshot_annotations[].image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `items[].screenshot_annotations[].is_latest_version` | boolean | 是 | 标注是否挂在该界面当前最新版本上（画布只展示最新版本，旧版本标注经预览弹窗查看） |
| `items[].screenshot_annotations[].x` | number | 是 | 框选矩形左上角横坐标 / 图宽（0~1） |
| `items[].screenshot_annotations[].y` | number | 是 | 框选矩形左上角纵坐标 / 图高（0~1） |
| `items[].screenshot_annotations[].width` | number | 是 | 框选矩形宽 / 图宽（0~1） |
| `items[].screenshot_annotations[].height` | number | 是 | 框选矩形高 / 图高（0~1） |
| `items[].screenshot_annotations[].pixel_rect` | object（可空） | 是 | 由比例坐标 × 原图尺寸换算的像素矩形；原图尺寸未知时为 null。像素矩形（原图坐标系） |
| `items[].screenshot_annotations[].pixel_rect.x` | integer | 是 |  |
| `items[].screenshot_annotations[].pixel_rect.y` | integer | 是 |  |
| `items[].screenshot_annotations[].pixel_rect.width` | integer | 是 |  |
| `items[].screenshot_annotations[].pixel_rect.height` | integer | 是 |  |
| `items[].screenshot_annotations[].body` | string | 是 | 标注文字 |
| `items[].screenshot_annotations[].status` | string（enum: open \| resolved） | 是 | open=未解决，resolved=已解决 |
| `items[].screenshot_annotations[].crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop；统一输出 PNG，前端自行追加 ?token= |
| `items[].screenshot_annotations[].created_by` | string | 是 | 用户名或 "API Key: <name>" |
| `items[].screenshot_annotations[].created_at` | string（date-time） | 是 |  |
| `items[].screenshot_annotations[].updated_at` | string（date-time） | 是 |  |
| `items[].screenshot_annotations[].resolved_at` | string（date-time，可空） | 是 | 最近一次标为已解决的时间；open 状态为 null |
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
| `attachments` | object[] | 否 | omitempty；仅 Issue 详情响应携带（无附件时缺省）；列表项不出现 |
| `attachments[].id` | string | 是 |  |
| `attachments[].file_name` | string | 是 |  |
| `attachments[].file_size` | integer（int64） | 是 |  |
| `attachments[].mime_type` | string | 是 | 内容嗅探结果（http.DetectContentType），供 UI 图标使用 |
| `attachments[].uploader` | string | 是 | 用户名或 "API Key: <name>" |
| `attachments[].download_url` | string | 是 | /api/attachments/{id}/download |
| `attachments[].created_at` | string（date-time） | 是 |  |
| `screenshot_annotations` | object[] | 否 | omitempty；仅 Issue 详情响应携带（无关联标注时缺省）；列表项不出现 |
| `screenshot_annotations[].id` | string | 是 |  |
| `screenshot_annotations[].project_id` | string | 是 |  |
| `screenshot_annotations[].screen_id` | string | 是 | 所属界面 ID（由版本冗余带出，便于过滤） |
| `screenshot_annotations[].version_id` | string | 是 | 标注挂在该版本上；版本删除时标注级联删除 |
| `screenshot_annotations[].issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `screenshot_annotations[].issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `screenshot_annotations[].issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `screenshot_annotations[].screen_key` | string | 是 |  |
| `screenshot_annotations[].screen_title` | string | 是 | 界面显示名；空串时客户端回退展示 screen_key |
| `screenshot_annotations[].screen_group` | string | 是 | 界面分组；空串表示未分组 |
| `screenshot_annotations[].image_url` | string | 是 | 标注所在版本的原图地址（版本 content_url）；追加 ?token= 供 <img> |
| `screenshot_annotations[].image_width` | integer | 是 | 原图宽像素；0 表示未知（存量版本未记录尺寸） |
| `screenshot_annotations[].image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `screenshot_annotations[].is_latest_version` | boolean | 是 | 标注是否挂在该界面当前最新版本上（画布只展示最新版本，旧版本标注经预览弹窗查看） |
| `screenshot_annotations[].x` | number | 是 | 框选矩形左上角横坐标 / 图宽（0~1） |
| `screenshot_annotations[].y` | number | 是 | 框选矩形左上角纵坐标 / 图高（0~1） |
| `screenshot_annotations[].width` | number | 是 | 框选矩形宽 / 图宽（0~1） |
| `screenshot_annotations[].height` | number | 是 | 框选矩形高 / 图高（0~1） |
| `screenshot_annotations[].pixel_rect` | object（可空） | 是 | 由比例坐标 × 原图尺寸换算的像素矩形；原图尺寸未知时为 null。像素矩形（原图坐标系） |
| `screenshot_annotations[].pixel_rect.x` | integer | 是 |  |
| `screenshot_annotations[].pixel_rect.y` | integer | 是 |  |
| `screenshot_annotations[].pixel_rect.width` | integer | 是 |  |
| `screenshot_annotations[].pixel_rect.height` | integer | 是 |  |
| `screenshot_annotations[].body` | string | 是 | 标注文字 |
| `screenshot_annotations[].status` | string（enum: open \| resolved） | 是 | open=未解决，resolved=已解决 |
| `screenshot_annotations[].crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop；统一输出 PNG，前端自行追加 ?token= |
| `screenshot_annotations[].created_by` | string | 是 | 用户名或 "API Key: <name>" |
| `screenshot_annotations[].created_at` | string（date-time） | 是 |  |
| `screenshot_annotations[].updated_at` | string（date-time） | 是 |  |
| `screenshot_annotations[].resolved_at` | string（date-time，可空） | 是 | 最近一次标为已解决的时间；open 状态为 null |
| `pull_request_summary` | object | 否 | omitempty；仅 Issue 列表项携带的 PR 聚合计数；详情项不出现。Issue 关联 PR 的聚合计数；closed 计数 = total - open - merged |
| `pull_request_summary.total` | integer | 是 |  |
| `pull_request_summary.open` | integer | 是 | state=open 计数 |
| `pull_request_summary.merged` | integer | 是 | state=merged 计数 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | API Key 创建 github 来源 Issue（40301）或项目未配公开仓库 Token（40003/40006） |
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
| 400 | 项目未关联 GitHub 仓库（40003）或已关联仓库但未配置公开仓库 Token（40006） |
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
| 400 | 项目未关联 GitHub 仓库（40003）或已关联仓库但未配置公开仓库 Token（40006） |
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

与列表项结构相同，另带可选 `collab` 字段（与 GET /collab 的 data 同形；仅详情填充，列表不返回）、`pull_requests` 数组（关联 PR 全量；读取失败或无关联时缺省）与 `attachments` 数组（文件附件全量；无附件时缺省）。

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
| `attachments` | object[] | 否 | omitempty；仅 Issue 详情响应携带（无附件时缺省）；列表项不出现 |
| `attachments[].id` | string | 是 |  |
| `attachments[].file_name` | string | 是 |  |
| `attachments[].file_size` | integer（int64） | 是 |  |
| `attachments[].mime_type` | string | 是 | 内容嗅探结果（http.DetectContentType），供 UI 图标使用 |
| `attachments[].uploader` | string | 是 | 用户名或 "API Key: <name>" |
| `attachments[].download_url` | string | 是 | /api/attachments/{id}/download |
| `attachments[].created_at` | string（date-time） | 是 |  |
| `screenshot_annotations` | object[] | 否 | omitempty；仅 Issue 详情响应携带（无关联标注时缺省）；列表项不出现 |
| `screenshot_annotations[].id` | string | 是 |  |
| `screenshot_annotations[].project_id` | string | 是 |  |
| `screenshot_annotations[].screen_id` | string | 是 | 所属界面 ID（由版本冗余带出，便于过滤） |
| `screenshot_annotations[].version_id` | string | 是 | 标注挂在该版本上；版本删除时标注级联删除 |
| `screenshot_annotations[].issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `screenshot_annotations[].issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `screenshot_annotations[].issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `screenshot_annotations[].screen_key` | string | 是 |  |
| `screenshot_annotations[].screen_title` | string | 是 | 界面显示名；空串时客户端回退展示 screen_key |
| `screenshot_annotations[].screen_group` | string | 是 | 界面分组；空串表示未分组 |
| `screenshot_annotations[].image_url` | string | 是 | 标注所在版本的原图地址（版本 content_url）；追加 ?token= 供 <img> |
| `screenshot_annotations[].image_width` | integer | 是 | 原图宽像素；0 表示未知（存量版本未记录尺寸） |
| `screenshot_annotations[].image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `screenshot_annotations[].is_latest_version` | boolean | 是 | 标注是否挂在该界面当前最新版本上（画布只展示最新版本，旧版本标注经预览弹窗查看） |
| `screenshot_annotations[].x` | number | 是 | 框选矩形左上角横坐标 / 图宽（0~1） |
| `screenshot_annotations[].y` | number | 是 | 框选矩形左上角纵坐标 / 图高（0~1） |
| `screenshot_annotations[].width` | number | 是 | 框选矩形宽 / 图宽（0~1） |
| `screenshot_annotations[].height` | number | 是 | 框选矩形高 / 图高（0~1） |
| `screenshot_annotations[].pixel_rect` | object（可空） | 是 | 由比例坐标 × 原图尺寸换算的像素矩形；原图尺寸未知时为 null。像素矩形（原图坐标系） |
| `screenshot_annotations[].pixel_rect.x` | integer | 是 |  |
| `screenshot_annotations[].pixel_rect.y` | integer | 是 |  |
| `screenshot_annotations[].pixel_rect.width` | integer | 是 |  |
| `screenshot_annotations[].pixel_rect.height` | integer | 是 |  |
| `screenshot_annotations[].body` | string | 是 | 标注文字 |
| `screenshot_annotations[].status` | string（enum: open \| resolved） | 是 | open=未解决，resolved=已解决 |
| `screenshot_annotations[].crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop；统一输出 PNG，前端自行追加 ?token= |
| `screenshot_annotations[].created_by` | string | 是 | 用户名或 "API Key: <name>" |
| `screenshot_annotations[].created_at` | string（date-time） | 是 |  |
| `screenshot_annotations[].updated_at` | string（date-time） | 是 |  |
| `screenshot_annotations[].resolved_at` | string（date-time，可空） | 是 | 最近一次标为已解决的时间；open 状态为 null |
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
| `attachments` | object[] | 否 | omitempty；仅 Issue 详情响应携带（无附件时缺省）；列表项不出现 |
| `attachments[].id` | string | 是 |  |
| `attachments[].file_name` | string | 是 |  |
| `attachments[].file_size` | integer（int64） | 是 |  |
| `attachments[].mime_type` | string | 是 | 内容嗅探结果（http.DetectContentType），供 UI 图标使用 |
| `attachments[].uploader` | string | 是 | 用户名或 "API Key: <name>" |
| `attachments[].download_url` | string | 是 | /api/attachments/{id}/download |
| `attachments[].created_at` | string（date-time） | 是 |  |
| `screenshot_annotations` | object[] | 否 | omitempty；仅 Issue 详情响应携带（无关联标注时缺省）；列表项不出现 |
| `screenshot_annotations[].id` | string | 是 |  |
| `screenshot_annotations[].project_id` | string | 是 |  |
| `screenshot_annotations[].screen_id` | string | 是 | 所属界面 ID（由版本冗余带出，便于过滤） |
| `screenshot_annotations[].version_id` | string | 是 | 标注挂在该版本上；版本删除时标注级联删除 |
| `screenshot_annotations[].issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `screenshot_annotations[].issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `screenshot_annotations[].issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `screenshot_annotations[].screen_key` | string | 是 |  |
| `screenshot_annotations[].screen_title` | string | 是 | 界面显示名；空串时客户端回退展示 screen_key |
| `screenshot_annotations[].screen_group` | string | 是 | 界面分组；空串表示未分组 |
| `screenshot_annotations[].image_url` | string | 是 | 标注所在版本的原图地址（版本 content_url）；追加 ?token= 供 <img> |
| `screenshot_annotations[].image_width` | integer | 是 | 原图宽像素；0 表示未知（存量版本未记录尺寸） |
| `screenshot_annotations[].image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `screenshot_annotations[].is_latest_version` | boolean | 是 | 标注是否挂在该界面当前最新版本上（画布只展示最新版本，旧版本标注经预览弹窗查看） |
| `screenshot_annotations[].x` | number | 是 | 框选矩形左上角横坐标 / 图宽（0~1） |
| `screenshot_annotations[].y` | number | 是 | 框选矩形左上角纵坐标 / 图高（0~1） |
| `screenshot_annotations[].width` | number | 是 | 框选矩形宽 / 图宽（0~1） |
| `screenshot_annotations[].height` | number | 是 | 框选矩形高 / 图高（0~1） |
| `screenshot_annotations[].pixel_rect` | object（可空） | 是 | 由比例坐标 × 原图尺寸换算的像素矩形；原图尺寸未知时为 null。像素矩形（原图坐标系） |
| `screenshot_annotations[].pixel_rect.x` | integer | 是 |  |
| `screenshot_annotations[].pixel_rect.y` | integer | 是 |  |
| `screenshot_annotations[].pixel_rect.width` | integer | 是 |  |
| `screenshot_annotations[].pixel_rect.height` | integer | 是 |  |
| `screenshot_annotations[].body` | string | 是 | 标注文字 |
| `screenshot_annotations[].status` | string（enum: open \| resolved） | 是 | open=未解决，resolved=已解决 |
| `screenshot_annotations[].crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop；统一输出 PNG，前端自行追加 ?token= |
| `screenshot_annotations[].created_by` | string | 是 | 用户名或 "API Key: <name>" |
| `screenshot_annotations[].created_at` | string（date-time） | 是 |  |
| `screenshot_annotations[].updated_at` | string（date-time） | 是 |  |
| `screenshot_annotations[].resolved_at` | string（date-time，可空） | 是 | 最近一次标为已解决的时间；open 状态为 null |
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

## attachments

Issue 文件附件（独立于正文图片 assets）上传、下载与删除

### POST `/api/issues/{iid}/attachments`

<!-- operationId: uploadIssueAttachment -->

上传 Issue 文件附件（multipart/form-data，字段名 file）

仅 internal 来源 Issue（github 源返回 40908）。不限文件类型，`mime_type` 按内容嗅探存储供 UI 图标使用；大小受服务端 upload.max_file_size 限制（默认 500MB）；同名不去重，每次上传生成新记录。附件与正文图片 assets 相互独立，不随正文引用变化而回收；正文中引用 download_url 不被追踪，删除后成为 404 死链。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `iid` | string | 是 | Issue ID（UUID，非 INT-123/GH-123 短编号） |

**请求体**

`multipart/form-data`，必填。表单字段，不是 JSON。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `file` | binary | 是 | 附件文件，不限类型 |

**成功响应**

**200** 上传的附件记录

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `file_name` | string | 是 |  |
| `file_size` | integer（int64） | 是 |  |
| `mime_type` | string | 是 | 内容嗅探结果（http.DetectContentType），供 UI 图标使用 |
| `uploader` | string | 是 | 用户名或 "API Key: <name>" |
| `download_url` | string | 是 | /api/attachments/{id}/download |
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

### DELETE `/api/attachments/{aid}`

<!-- operationId: deleteIssueAttachment -->

删除 Issue 附件

校验项目归属（同 artifacts，不限上传者本人）。删除后 download_url 失效（404），正文中已引用的链接成为死链。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `aid` | string | 是 | 附件 ID（UUID） |

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

### GET `/api/attachments/{aid}/download`

<!-- operationId: downloadIssueAttachment -->

下载 Issue 附件

返回 `application/octet-stream` 与 `Content-Disposition: attachment`。支持 Authorization 头或 `?token=` query 凭证（供浏览器直链下载）。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `aid` | string | 是 | 附件 ID（UUID） |

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
| `items[].latest_version.width` | integer | 是 | 原图宽像素；0 表示未知（历史存量版本未记录尺寸） |
| `items[].latest_version.height` | integer | 是 | 原图高像素；0 表示未知 |
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
| `group` | string（长度 ≤100） | 否 | 分组名（≤100 字符）；字段出现即更新（空串置为未分组），不出现则保持原值 |
| `title` | string（长度 ≤200） | 否 | 界面标题（≤200 字符）；非空才更新 |
| `note` | string（长度 ≤1000） | 否 | 本次上传版本的备注（≤1000 字符） |

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
| `version.width` | integer | 是 | 原图宽像素；0 表示未知（历史存量版本未记录尺寸） |
| `version.height` | integer | 是 | 原图高像素；0 表示未知 |
| `version.content_url` | string | 是 | /api/screenshot-versions/{vid}/content；前端自行追加 ?token= |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 413 | 请求体超过大小限制（裸 413，无 JSON body） |
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
| `versions[].width` | integer | 是 | 原图宽像素；0 表示未知（历史存量版本未记录尺寸） |
| `versions[].height` | integer | 是 | 原图高像素；0 表示未知 |
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
| `group` | string（长度 ≤100） | 否 | 分组名（≤100 字符）；允许空串置为未分组 |
| `title` | string（长度 ≤200） | 否 | 界面标题（≤200 字符）；空串回退显示 screen_key |

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
| `versions[].width` | integer | 是 | 原图宽像素；0 表示未知（历史存量版本未记录尺寸） |
| `versions[].height` | integer | 是 | 原图高像素；0 表示未知 |
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

### GET `/api/projects/{id}/screenshot-annotations`

<!-- operationId: listScreenshotAnnotations -->

列出项目全部截图标注（可按状态 / Issue / 界面过滤）

返回项目下全部截图标注（含挂在旧版本上的），按创建先后升序。
可选 query 过滤：`status`（open/resolved，缺省返回全部；非法值 40001）、`issue_id`、`screen_id`（精确匹配，可组合）。
每条标注返回：原图地址（image_url，即所在版本的 content_url）、比例坐标（x/y/width/height，0~1）、
像素坐标（pixel_rect；原图尺寸未知的存量版本为 null）、文字、所属画面（screen_key/screen_title/screen_group）、
关联 Issue（issue_id/issue_reference/issue_title，未关联均为 null）、是否挂在界面最新版本（is_latest_version）
与裁剪图地址（crop_url）。删除界面/版本会级联删除其上标注；删除 Issue 只解除关联不删标注。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `status` | string（enum: open \| resolved） | 否 | 按状态过滤；缺省返回全部状态。open=未解决，resolved=已解决 |
| `issue_id` | string | 否 | 只返回关联该 Issue 的标注（UUID） |
| `screen_id` | string | 否 | 只返回该界面（含全部历史版本）上的标注 |

**成功响应**

**200** 标注列表（data 为 {items}，非分页信封）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].screen_id` | string | 是 | 所属界面 ID（由版本冗余带出，便于过滤） |
| `items[].version_id` | string | 是 | 标注挂在该版本上；版本删除时标注级联删除 |
| `items[].issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `items[].issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `items[].issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `items[].screen_key` | string | 是 |  |
| `items[].screen_title` | string | 是 | 界面显示名；空串时客户端回退展示 screen_key |
| `items[].screen_group` | string | 是 | 界面分组；空串表示未分组 |
| `items[].image_url` | string | 是 | 标注所在版本的原图地址（版本 content_url）；追加 ?token= 供 <img> |
| `items[].image_width` | integer | 是 | 原图宽像素；0 表示未知（存量版本未记录尺寸） |
| `items[].image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `items[].is_latest_version` | boolean | 是 | 标注是否挂在该界面当前最新版本上（画布只展示最新版本，旧版本标注经预览弹窗查看） |
| `items[].x` | number | 是 | 框选矩形左上角横坐标 / 图宽（0~1） |
| `items[].y` | number | 是 | 框选矩形左上角纵坐标 / 图高（0~1） |
| `items[].width` | number | 是 | 框选矩形宽 / 图宽（0~1） |
| `items[].height` | number | 是 | 框选矩形高 / 图高（0~1） |
| `items[].pixel_rect` | object（可空） | 是 | 由比例坐标 × 原图尺寸换算的像素矩形；原图尺寸未知时为 null。像素矩形（原图坐标系） |
| `items[].pixel_rect.x` | integer | 是 |  |
| `items[].pixel_rect.y` | integer | 是 |  |
| `items[].pixel_rect.width` | integer | 是 |  |
| `items[].pixel_rect.height` | integer | 是 |  |
| `items[].body` | string | 是 | 标注文字 |
| `items[].status` | string（enum: open \| resolved） | 是 | open=未解决，resolved=已解决 |
| `items[].crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop；统一输出 PNG，前端自行追加 ?token= |
| `items[].created_by` | string | 是 | 用户名或 "API Key: <name>" |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |
| `items[].resolved_at` | string（date-time，可空） | 是 | 最近一次标为已解决的时间；open 状态为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/screenshot-versions/{vid}/annotations`

<!-- operationId: createScreenshotAnnotation -->

在截图版本上新建矩形标注（仅 JWT）

在指定版本图片上创建「矩形框 + 文字」标注。坐标为图片宽高比例（0~1）：
x/y 为左上角，width/height 为尺寸；须满足 0 ≤ x,y、0 < width,height、x+width ≤ 1、y+height ≤ 1，否则 40001。
`issue_id` 可选：缺省或空串表示不关联；提供时须为同一项目内的 Issue（不存在或跨项目返回 40001）。
API Key 调用返回 403（40301）。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `vid` | string | 是 | 截图版本 ID（UUID） |

**请求体**

`application/json`，必填。

x/y/width/height 必填；字段缺省或传 null 一律 40001（nullable 仅为让服务端能区分缺省与显式 0）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `x` | number（0..1，可空） | 是 |  |
| `y` | number（0..1，可空） | 是 |  |
| `width` | number（0..1，可空） | 是 |  |
| `height` | number（0..1，可空） | 是 | x+width ≤ 1 且 y+height ≤ 1，否则 40001 |
| `body` | string（长度 1..1000） | 是 | 标注文字（1~1000 字符） |
| `issue_id` | string | 否 | 可选；关联本项目内 Issue，缺省或空串 = 不关联 |

**成功响应**

**200** 新建成功的标注

`data`：

挂在某个截图版本上的「矩形框 + 文字」标注；坐标以图片宽高比例（0~1）存储

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `screen_id` | string | 是 | 所属界面 ID（由版本冗余带出，便于过滤） |
| `version_id` | string | 是 | 标注挂在该版本上；版本删除时标注级联删除 |
| `issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `screen_key` | string | 是 |  |
| `screen_title` | string | 是 | 界面显示名；空串时客户端回退展示 screen_key |
| `screen_group` | string | 是 | 界面分组；空串表示未分组 |
| `image_url` | string | 是 | 标注所在版本的原图地址（版本 content_url）；追加 ?token= 供 <img> |
| `image_width` | integer | 是 | 原图宽像素；0 表示未知（存量版本未记录尺寸） |
| `image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `is_latest_version` | boolean | 是 | 标注是否挂在该界面当前最新版本上（画布只展示最新版本，旧版本标注经预览弹窗查看） |
| `x` | number | 是 | 框选矩形左上角横坐标 / 图宽（0~1） |
| `y` | number | 是 | 框选矩形左上角纵坐标 / 图高（0~1） |
| `width` | number | 是 | 框选矩形宽 / 图宽（0~1） |
| `height` | number | 是 | 框选矩形高 / 图高（0~1） |
| `pixel_rect` | object（可空） | 是 | 由比例坐标 × 原图尺寸换算的像素矩形；原图尺寸未知时为 null。像素矩形（原图坐标系） |
| `pixel_rect.x` | integer | 是 |  |
| `pixel_rect.y` | integer | 是 |  |
| `pixel_rect.width` | integer | 是 |  |
| `pixel_rect.height` | integer | 是 |  |
| `body` | string | 是 | 标注文字 |
| `status` | string（enum: open \| resolved） | 是 | open=未解决，resolved=已解决 |
| `crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop；统一输出 PNG，前端自行追加 ?token= |
| `created_by` | string | 是 | 用户名或 "API Key: <name>" |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `resolved_at` | string（date-time，可空） | 是 | 最近一次标为已解决的时间；open 状态为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/screenshot-annotations/{aid}`

<!-- operationId: updateScreenshotAnnotation -->

更新标注文字 / 状态 / 关联 Issue

指针语义：字段出现才更新，未出现保持原值；三个字段都不传返回 40001。
`issue_id` 传空串表示解除关联，非空须为同一项目内的 Issue（否则 40001）；JSON null 与未传同义（保持）。
权限分工：JWT 可更新全部字段；API Key 只允许传 `status: "resolved"`（把标注标为已解决），
携带 body/issue_id 或传 `status:"open"` 重开均返回 403（40301）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `aid` | string | 是 | 截图标注 ID（UUID） |

**请求体**

`application/json`，必填。

指针语义：字段出现才更新，都不传返回 40001。
JWT 可更新全部字段；API Key 只允许传 `{"status":"resolved"}`，其余字段或 status=open 返回 40301。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `body` | string（长度 1..1000） | 否 | 标注文字（1~1000 字符） |
| `status` | string（enum: open \| resolved） | 否 | open=重开（仅 JWT），resolved=标为已解决。open=未解决，resolved=已解决 |
| `issue_id` | string | 否 | 空串=解除关联；非空须为本项目内 Issue（否则 40001）；不传或传 null = 保持不变 |

**成功响应**

**200** 更新后的标注

`data`：

挂在某个截图版本上的「矩形框 + 文字」标注；坐标以图片宽高比例（0~1）存储

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `screen_id` | string | 是 | 所属界面 ID（由版本冗余带出，便于过滤） |
| `version_id` | string | 是 | 标注挂在该版本上；版本删除时标注级联删除 |
| `issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `screen_key` | string | 是 |  |
| `screen_title` | string | 是 | 界面显示名；空串时客户端回退展示 screen_key |
| `screen_group` | string | 是 | 界面分组；空串表示未分组 |
| `image_url` | string | 是 | 标注所在版本的原图地址（版本 content_url）；追加 ?token= 供 <img> |
| `image_width` | integer | 是 | 原图宽像素；0 表示未知（存量版本未记录尺寸） |
| `image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `is_latest_version` | boolean | 是 | 标注是否挂在该界面当前最新版本上（画布只展示最新版本，旧版本标注经预览弹窗查看） |
| `x` | number | 是 | 框选矩形左上角横坐标 / 图宽（0~1） |
| `y` | number | 是 | 框选矩形左上角纵坐标 / 图高（0~1） |
| `width` | number | 是 | 框选矩形宽 / 图宽（0~1） |
| `height` | number | 是 | 框选矩形高 / 图高（0~1） |
| `pixel_rect` | object（可空） | 是 | 由比例坐标 × 原图尺寸换算的像素矩形；原图尺寸未知时为 null。像素矩形（原图坐标系） |
| `pixel_rect.x` | integer | 是 |  |
| `pixel_rect.y` | integer | 是 |  |
| `pixel_rect.width` | integer | 是 |  |
| `pixel_rect.height` | integer | 是 |  |
| `body` | string | 是 | 标注文字 |
| `status` | string（enum: open \| resolved） | 是 | open=未解决，resolved=已解决 |
| `crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop；统一输出 PNG，前端自行追加 ?token= |
| `created_by` | string | 是 | 用户名或 "API Key: <name>" |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `resolved_at` | string（date-time，可空） | 是 | 最近一次标为已解决的时间；open 状态为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/screenshot-annotations/{aid}`

<!-- operationId: deleteScreenshotAnnotation -->

删除标注（仅 JWT）

重复删除返回 404（40416），非幂等。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `aid` | string | 是 | 截图标注 ID（UUID） |

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

### GET `/api/screenshot-annotations/{aid}/crop`

<!-- operationId: getScreenshotAnnotationCrop -->

获取标注框选区域裁剪图（统一 PNG）

按标注比例坐标换算像素矩形，向外扩约矩形短边 15% 的外边距（最小 8px，不超出图片边界），统一输出 PNG。
源图为 WebP 时服务端解码；GIF 取第一帧。典型用途：AI 拉取裁剪图比对修改，或前端缩略展示。
支持 Authorization 头或 `?token=` query 凭证（供 `<img>` 直链）。同一路径另注册 HEAD 变体。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `aid` | string | 是 | 截图标注 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 否 | 可选 query 凭证（JWT 或 `fsk_` API Key），供无法携带 Authorization 头的场景（`<img>`、浏览器直链下载）。与 Authorization 头二选一，头优先。 |

**成功响应**

**200** PNG 图片二进制内容

二进制内容（image/png），无 JSON 信封。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### HEAD `/api/screenshot-annotations/{aid}/crop`

<!-- operationId: getScreenshotAnnotationCropHead -->

获取标注裁剪图头信息（HEAD）

与 GET 语义相同但不返回 body。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `aid` | string | 是 | 截图标注 ID（UUID） |

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

## environments

T3 环境连接与 T3 客户端操作（配对、线程读取/发消息/停止/队列、审批与提问应答、图片附件；仅 JWT，INT-82/INT-85）

### GET `/api/environments`

<!-- operationId: listT3Environments -->

列出已配对的 T3 环境并探测状态

有界并行逐个环境探测（`GET {address}/api/auth/session`，单请求同时回答可达性与凭证有效性，
单环境独立预算，坏环境不拖垮其它环境）：
`ok`=Bearer 会话有效；`unreachable`=网络不可达/超时/5xx；`repair`=可达但凭证失效（需重新配对）；
`degraded`=可达且凭证有效，但部分数据读取失败（如归档快照），其余数据仍返回。
响应不包含任何凭证字段。

**鉴权**：公开

**成功响应**

**200** 环境列表（含探测状态）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].name` | string | 是 |  |
| `items[].address` | string | 是 | 从配对链接解析出的 T3 服务端基址（规范化 origin，如 https://host） |
| `items[].status` | string（enum: ok \| unreachable \| repair \| degraded） | 是 | 探测状态：ok=Bearer 会话有效；unreachable=网络不可达/超时/5xx； repair=可达但凭证失效（Bearer 过期或被吊销），需重新配对； degraded=可达且凭证有效，但部分数据读取失败（如归档线程快照）， 其余数据仍返回 |
| `items[].scopes` | string[] | 是 | 该环境会话实际授予的 T3 scope 列表（配对时服务端授予，原样落库） |
| `items[].access` | string（enum: read-only \| operate） | 是 | 会话能力：仅当精确持有 `orchestration:operate` scope 时为 `operate`， 否则 `read-only`。唯一判定依据是精确 token 的 scope 列； `read-only` 环境调用任何写入口返回 40300（重新配对后可操作） |
| `items[].error` | string | 否 | 最近一次探测失败的说明；成功时省略 |
| `items[].updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |

### POST `/api/environments`

<!-- operationId: createT3Environment -->

通过配对链接新增 T3 环境

解析配对链接（支持 `t3 pair` 直接链接 `https://host/#token=...` 与 hosted 包装链接
`https://relay/?host=<backend>&token=...`），用一次性配对码向 T3 服务端
`POST /oauth/token` 换取 Bearer 会话并加密落库。请求不携带 `scope`：
T3 服务端按配对链接的授权授予完整 scope（标准客户端含 `orchestration:read`
与 `orchestration:operate`），授予的 scope 原样落库并在响应 `scopes`/`access`
中带回。配对码一次性，不落库。旧环境（只读会话）不自动升级，重新配对后
`access` 变为 `operate`。

**鉴权**：公开

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string（长度 ≥1） | 是 | 环境显示名；同一用户下唯一（重复返回 40912） |
| `pairing_url` | string（长度 ≥1） | 是 | 配对链接：`t3 pair` 直接链接（`https://host/#token=code`，hash 优先， 兼容 `?token=`）或 hosted 包装链接（`https://relay/?host=<backend>&token=code`）。 协议支持 http/https/ws/wss；缺省协议时 IP 字面量补 http、其余补 https。 |

**成功响应**

**200** 创建成功，返回带探测状态的环境

`data`：

已配对的 T3 环境连接；不泄漏任何凭证字段

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `name` | string | 是 |  |
| `address` | string | 是 | 从配对链接解析出的 T3 服务端基址（规范化 origin，如 https://host） |
| `status` | string（enum: ok \| unreachable \| repair \| degraded） | 是 | 探测状态：ok=Bearer 会话有效；unreachable=网络不可达/超时/5xx； repair=可达但凭证失效（Bearer 过期或被吊销），需重新配对； degraded=可达且凭证有效，但部分数据读取失败（如归档线程快照）， 其余数据仍返回 |
| `scopes` | string[] | 是 | 该环境会话实际授予的 T3 scope 列表（配对时服务端授予，原样落库） |
| `access` | string（enum: read-only \| operate） | 是 | 会话能力：仅当精确持有 `orchestration:operate` scope 时为 `operate`， 否则 `read-only`。唯一判定依据是精确 token 的 scope 列； `read-only` 环境调用任何写入口返回 40300（重新配对后可操作） |
| `error` | string | 否 | 最近一次探测失败的说明；成功时省略 |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 配对链接无效或缺少配对码（40005） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 409 | 环境名称已存在（40912） |
| 502 | 配对失败（上游拒绝/不可达/scope 超宽）（50202） |

### PUT `/api/environments/{id}`

<!-- operationId: updateT3Environment -->

改名或重新配对

指针语义：字段出现才更新。`pairing_url` 出现时重新走配对流程，
覆盖地址与 Bearer（用于修复 `repair` 状态，同时把只读旧会话升级为
可操作会话）；两个都不传返回 40001。
改名与重配按字段条件更新：并发改名/重配互不覆盖；环境被并发删除时
返回 40417 且不复活记录；并发改名触发唯一索引冲突时返回 40912。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |

**请求体**

`application/json`，必填。

指针语义：字段出现才更新，都不传返回 40001

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string（长度 ≥1） | 否 |  |
| `pairing_url` | string | 否 | 出现时重新配对（覆盖地址与 Bearer），用于修复 repair 状态 |

**成功响应**

**200** 更新成功，返回带探测状态的环境

`data`：

已配对的 T3 环境连接；不泄漏任何凭证字段

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `name` | string | 是 |  |
| `address` | string | 是 | 从配对链接解析出的 T3 服务端基址（规范化 origin，如 https://host） |
| `status` | string（enum: ok \| unreachable \| repair \| degraded） | 是 | 探测状态：ok=Bearer 会话有效；unreachable=网络不可达/超时/5xx； repair=可达但凭证失效（Bearer 过期或被吊销），需重新配对； degraded=可达且凭证有效，但部分数据读取失败（如归档线程快照）， 其余数据仍返回 |
| `scopes` | string[] | 是 | 该环境会话实际授予的 T3 scope 列表（配对时服务端授予，原样落库） |
| `access` | string（enum: read-only \| operate） | 是 | 会话能力：仅当精确持有 `orchestration:operate` scope 时为 `operate`， 否则 `read-only`。唯一判定依据是精确 token 的 scope 列； `read-only` 环境调用任何写入口返回 40300（重新配对后可操作） |
| `error` | string | 否 | 最近一次探测失败的说明；成功时省略 |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 环境不存在（40417） |
| 409 | 环境名称已存在（40912） |
| 502 | 重新配对失败（50202） |

### DELETE `/api/environments/{id}`

<!-- operationId: deleteT3Environment -->

删除环境

删除本地记录。注意：T3 协议的会话自吊销端点（`/api/auth/clients/revoke`）
需要 `access:write` scope，Fast Ship 的会话没有该 scope，协议上不存在
自吊销路径；Bearer 由 T3 服务端按 `expires_in` 自行过期；如需立即吊销
请在 T3 侧的访问设置中操作。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |

**成功响应**

**200** 删除成功

`data`：any

响应负载；形状随端点而变，由 allOf 组合的兄弟 schema 收窄

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 环境不存在（40417） |

### GET `/api/environments/{id}/threads/{threadId}`

<!-- operationId: getT3Thread -->

读取单个线程的完整归一化时间线（含队列与活动状态）

通过 T3 的 `GET /api/orchestration/threads/{threadId}` HTTP 快照端点取完整投影
（WS 侧同名为分页受限窗口，不满足「完整历史」要求），按投影的
`visibleTurnItems`（含 fork 继承前缀、已排除回滚/被取代条目）归一化为
ThreadItem 列表；条目 ID 由 `sourceThreadId + "::" + T3 原始条目 id` 合成。
长消息不截断；命令输出/工具输出/subagent 结果在 wire 投影中被上游裁剪，
`detail_available=true` 的条目经 items/{itemId} 端点补读。
结构性事件（checkpoint 等）被丢弃。审批/提问/secret 请求条目经投影
`runtimeRequests` 补齐状态与应答能力。响应另含 `queue`（排队消息工作流，
口径对齐 T3 `deriveThreadQueueWorkflowState`）与 `activity`（活动 run、
停止中、待处理请求）。排队消息开始前没有 turn item，出现在 `queue.items`。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |

**成功响应**

**200** 线程详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `thread` | object | 是 |  |
| `thread.id` | string | 是 | T3 threadId |
| `thread.environment_id` | string | 是 |  |
| `thread.environment_name` | string | 是 |  |
| `thread.project_id` | string | 是 |  |
| `thread.project_name` | string | 是 | 来自 WS shell 快照项目表，列表和详情均使用该来源 |
| `thread.title` | string | 是 |  |
| `thread.status` | string（enum: running \| waiting \| idle \| archived） | 是 | 由 T3 归一化（对齐 T3 Sidebar 的判定顺序）： archivedAt 非空 → archived；其次 pendingRuntimeRequest 非空（等用户审批/输入）→ waiting； 再看 activityRunStatus（后台 run 未停时 shell.status 可能已是 completed/idle）： preparing/starting/running → running、waiting → waiting； 最后 shell.status 兜底（preparing/queued/starting/running → running、 waiting → waiting、其余 → idle） |
| `thread.branch` | string | 是 | 工作分支；T3 侧为 null 时为空串 |
| `thread.provider` | string | 是 | T3 providerInstanceId |
| `thread.model` | string | 是 |  |
| `thread.updated_at` | string | 是 | T3 原样返回的 ISO-8601 时间 |
| `thread.settled` | boolean | 是 | 上游 settledOverride=="settled" 原样透传：用户在 T3 把线程收进了历史区。归档判定看 status=archived，与 settled 互不蕴含 |
| `thread.run_started_at` | string | 否 | 最近 run 的 startedAt（上游 latestRunStartedAt）原样透传，供「运行中 N 分钟」计时；无线程运行记录时缺省 |
| `thread.pull_request_count` | integer | 是 | 关联 PR 数（pullRequests 数组长度；缺失时按 linkedPullRequest/branchPullRequest 计） |
| `thread.parent_thread_id` | string | 否 | 仅详情且该线程为子代理线程时给出（lineage.relationshipToParent=subagent 的 parentThreadId）；列表与父线程缺省 |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 | Fast Ship 条目 ID（sourceThreadId + "::" + T3 原始条目 id；继承行与本地行的原始 id 可能重复，靠来源线程区分） |
| `items[].inherited` | boolean | 否 | 是否为 fork 继承前缀行（visibility=inherited）；继承的审批/提问/密钥只展示，不能在本线程作答 |
| `items[].source_thread_id` | string | 否 | 该条目所在来源线程；继承行指向来源，本地行等于当前线程 |
| `items[].type` | string（enum: user_message \| assistant_message \| reasoning \| command_execution \| dynamic_tool \| file_change \| subagent \| error \| approval_request \| user_input_request \| secret_request） | 是 |  |
| `items[].text` | string | 是 | 正文，不额外截断（命令=命令文本；文件=元数据；审批=缺省时为空；展开后的完整正文经 items/{itemId} 获取） |
| `items[].summary` | string | 是 | 单行概要（折叠态显示，rune 安全截断）；命令/子代理=首行、文件=文件名 ±行数、工具=工具名、审批/提问=提示或问题首行 |
| `items[].status` | string | 否 | T3 侧条目状态（idle/pending/running/waiting/completed/failed/cancelled/interrupted）；可能省略 |
| `items[].updated_at` | string | 否 | 上游条目自带的 updatedAt（RFC3339）原样透传，作为条目版本；上游省略时本字段缺省 |
| `items[].child_thread_id` | string | 否 | 仅 subagent；子线程 ID |
| `items[].detail_available` | boolean | 否 | 条目在快照之外还有正文，可经 items/{itemId} 补读： 快照里 command_execution/dynamic_tool/subagent 为 true（wire 投影裁掉 输出/结果，file_change 上游 detail 也无 diff 故为 false）；补读响应恒为 false。缺省即 false。 |
| `items[].attachments` | object[] | 否 | 仅 user_message；随消息发送的图片附件元数据 |
| `items[].attachments[].type` | string（enum: image） | 是 |  |
| `items[].attachments[].id` | string | 是 | T3 附件 ID（createUploadUrl 产出的 attachmentId） |
| `items[].attachments[].name` | string（长度 ≤255） | 是 |  |
| `items[].attachments[].mime_type` | string | 是 | 仅支持 image/png、image/jpeg、image/gif、image/webp |
| `items[].attachments[].size_bytes` | integer | 是 | 字节数；单图上限 10MiB |
| `items[].request_id` | string | 否 | 仅 approval_request/user_input_request；T3 运行时请求 ID（respond/dismiss 端点使用） |
| `items[].request_kind` | string | 否 | 仅 approval_request；请求类型（command/file-read/file-change/mcp-elicitation/permission） |
| `items[].prompt` | string | 否 | 仅 approval_request；审批提示（上游缺省时为空） |
| `items[].app_name` | string | 否 | 仅 approval_request；发起审批的应用（mcp-elicitation 等）；上游缺省时缺省 |
| `items[].options` | object[] | 否 | 仅 approval_request；审批可选项（上游自带，按序展示） |
| `items[].options[].decision` | string | 是 | T3 ProviderApprovalDecision（accept/acceptForSession/acceptAlways/decline/cancel） |
| `items[].options[].label` | string | 是 |  |
| `items[].options[].warning` | string | 否 | 提供方给出的警示（如提示注入警告）；上游缺省时缺省 |
| `items[].decision` | string | 否 | 仅 approval_request；已做出的决定（accept/acceptForSession/acceptAlways/decline/cancel），来自运行时请求记录 |
| `items[].questions` | object[] | 否 | 仅 user_input_request；问题列表 |
| `items[].questions[].id` | string | 是 |  |
| `items[].questions[].header` | string | 是 |  |
| `items[].questions[].question` | string | 是 |  |
| `items[].questions[].options` | object[] | 是 |  |
| `items[].questions[].options[].label` | string | 是 |  |
| `items[].questions[].options[].description` | string | 是 |  |
| `items[].questions[].options[].value` | string | 否 | 上游缺省时缺省（提交 answers 时以 label 为值） |
| `items[].questions[].multi_select` | boolean | 否 | 上游缺省时缺省（视为单选） |
| `items[].questions[].allow_custom_answer` | boolean | 否 | 上游缺省时缺省 |
| `items[].questions[].initial_answer` | string | 否 | 可编辑的初始文本；上游缺省时缺省 |
| `items[].questions[].required` | boolean | 否 | 上游缺省时缺省 |
| `items[].answers` | object | 否 | 仅 user_input_request；已提交的答案（questionId → 答案值，原样透传） |
| `items[].response_mode` | string | 否 | 仅 user_input_request；应答通道（message=经消息应答） |
| `items[].response_capability` | string | 否 | 仅 approval_request/user_input_request；该请求当前的应答通道（live/message/not_resumable，运行时请求记录） |
| `items[].request_status` | string | 否 | 仅 approval_request/user_input_request；运行时请求状态（pending/resolved/expired/cancelled；pending 才可应答） |
| `items[].label` | string | 否 | 仅 secret_request；请求的密钥名称 |
| `items[].reason` | string | 否 | 仅 secret_request；为什么需要该密钥 |
| `items[].placeholder` | string | 否 | 仅 secret_request；输入框占位文本；上游缺省时缺省 |
| `items[].secret_status` | string | 否 | 仅 secret_request；密钥请求状态（pending/saved/declined/cancelled；绝不携带密钥值本身） |
| `queue` | object | 是 | 线程排队消息工作流，口径对齐 T3 `deriveThreadQueueWorkflowState`： items 为用户消息产生的排队 run（自动完成/通知 run 不在内），按 queuePosition（缺省 ordinal）升序。 |
| `queue.held` | boolean | 是 | 队列被挂起（Stop 或重启恢复后等待用户手动恢复） |
| `queue.can_reorder` | boolean | 是 | provider 会话能力支持排队消息（排序/编辑/取消可用） |
| `queue.can_steer` | boolean | 是 | provider 会话能力支持把排队消息上浮为活动运行的转向输入 |
| `queue.items` | object[] | 是 |  |
| `queue.items[].run_id` | string | 是 |  |
| `queue.items[].message_id` | string | 是 |  |
| `queue.items[].text` | string | 是 | 消息正文；上游投影缺消息时为「Queued message」 |
| `queue.items[].attachments` | object[] | 否 | 随消息的图片附件元数据；上游缺省时为空数组 |
| `queue.items[].attachments[].type` | string（enum: image） | 是 |  |
| `queue.items[].attachments[].id` | string | 是 | T3 附件 ID（createUploadUrl 产出的 attachmentId） |
| `queue.items[].attachments[].name` | string（长度 ≤255） | 是 |  |
| `queue.items[].attachments[].mime_type` | string | 是 | 仅支持 image/png、image/jpeg、image/gif、image/webp |
| `queue.items[].attachments[].size_bytes` | integer | 是 | 字节数；单图上限 10MiB |
| `activity` | object | 是 | 线程当前活动摘要（活动 run 来自投影 runs，与 queue 同一来源） |
| `activity.active_run_id` | string | 否 | 活动 run（preparing/starting/running/waiting 的最近一个）；静止时缺省 |
| `activity.active_run_status` | string | 否 | 活动 run 状态；静止时缺省 |
| `activity.stopping` | boolean | 是 | 停止请求已下发且尚未落定：活动 run 的最近一条 run_interrupt_request 条目晚于同 run 的最近一条 run_interrupt_result |
| `activity.has_pending_requests` | boolean | 是 | 有等待用户处理的运行时请求（审批/提问/代答 secret） |
| `activity.pending_background_task_count` | integer | 是 | 根 run 结束后仍在跑的后台工作条数（command 类不占完成位；上游缺省时为 0） |
| `access` | string（enum: read-only \| operate） | 是 | 会话能力：仅当精确持有 `orchestration:operate` scope 时为 `operate`， 否则 `read-only`。唯一判定依据是精确 token 的 scope 列； `read-only` 环境调用任何写入口返回 40300（重新配对后可操作） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 环境不存在（40417）或线程不存在（40418） |
| 502 | T3 环境调用失败（50202，含凭证失效提示） |

### GET `/api/environments/{id}/threads/{threadId}/items/{itemId}`

<!-- operationId: getT3ThreadItem -->

读取单个线程条目的完整正文（按需补读）

先取指定线程的可见时间线验证 `itemId` 是其成员（40419 = 不是成员），
再从可信行自身派生 T3 `orchestration.getTurnItem` 参数（threadId/itemId
取行内条目的持久身份；不接受前端指定来源或 RPC 方法），返回完整归一化
ThreadItem：text 含命令 input+output / subagent prompt+result+progress /
工具全量 input+output，summary 为单行短摘要，`detail_available=false`
（已无可再取的正文）。上游对单条正文有 256KB 限界，截断后缀原样保留。
`detail_available=false` 的条目直接返回快照形态，不发起补读。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |
| `itemId` | string | 是 | Fast Ship 条目 ID（sourceThreadId + "::" + T3 原始条目 id，来自线程详情的 items[].id） |

**成功响应**

**200** 完整条目

`data`：

归一化线程条目。映射规则：user_message（含 attachments 图片元数据）/
assistant_message/reasoning 原样带快照文本；proposed_plan →
assistant_message；command_execution（text=命令，summary=命令首行，
输出经 items/{itemId} 补读）；dynamic_tool/file_search/web_search/todo_list →
dynamic_tool；file_change（真实元数据：文件名/增删行数/子操作，summary=文件名 ±行数，
上游 detail 无 diff，不调用 diff 读路径）；subagent（child_thread_id 带出，
结果经 items/{itemId} 补读）；error（text=失败信息，summary=失败类别）。
交互类条目：approval_request（审批，request_id 关联运行时请求）/
user_input_request（提问）/ secret_request（密钥，绝不携带密钥值）。
结构性事件（checkpoint、handoff、notification、system_notice 等）丢弃；
未知类型前向兼容地跳过。正文不额外截断。
继承行（fork 前缀，visibility=inherited）只展示历史：inherited=true，
不能在本线程原地应答；source_thread_id 指向来源线程。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | Fast Ship 条目 ID（sourceThreadId + "::" + T3 原始条目 id；继承行与本地行的原始 id 可能重复，靠来源线程区分） |
| `inherited` | boolean | 否 | 是否为 fork 继承前缀行（visibility=inherited）；继承的审批/提问/密钥只展示，不能在本线程作答 |
| `source_thread_id` | string | 否 | 该条目所在来源线程；继承行指向来源，本地行等于当前线程 |
| `type` | string（enum: user_message \| assistant_message \| reasoning \| command_execution \| dynamic_tool \| file_change \| subagent \| error \| approval_request \| user_input_request \| secret_request） | 是 |  |
| `text` | string | 是 | 正文，不额外截断（命令=命令文本；文件=元数据；审批=缺省时为空；展开后的完整正文经 items/{itemId} 获取） |
| `summary` | string | 是 | 单行概要（折叠态显示，rune 安全截断）；命令/子代理=首行、文件=文件名 ±行数、工具=工具名、审批/提问=提示或问题首行 |
| `status` | string | 否 | T3 侧条目状态（idle/pending/running/waiting/completed/failed/cancelled/interrupted）；可能省略 |
| `updated_at` | string | 否 | 上游条目自带的 updatedAt（RFC3339）原样透传，作为条目版本；上游省略时本字段缺省 |
| `child_thread_id` | string | 否 | 仅 subagent；子线程 ID |
| `detail_available` | boolean | 否 | 条目在快照之外还有正文，可经 items/{itemId} 补读： 快照里 command_execution/dynamic_tool/subagent 为 true（wire 投影裁掉 输出/结果，file_change 上游 detail 也无 diff 故为 false）；补读响应恒为 false。缺省即 false。 |
| `attachments` | object[] | 否 | 仅 user_message；随消息发送的图片附件元数据 |
| `attachments[].type` | string（enum: image） | 是 |  |
| `attachments[].id` | string | 是 | T3 附件 ID（createUploadUrl 产出的 attachmentId） |
| `attachments[].name` | string（长度 ≤255） | 是 |  |
| `attachments[].mime_type` | string | 是 | 仅支持 image/png、image/jpeg、image/gif、image/webp |
| `attachments[].size_bytes` | integer | 是 | 字节数；单图上限 10MiB |
| `request_id` | string | 否 | 仅 approval_request/user_input_request；T3 运行时请求 ID（respond/dismiss 端点使用） |
| `request_kind` | string | 否 | 仅 approval_request；请求类型（command/file-read/file-change/mcp-elicitation/permission） |
| `prompt` | string | 否 | 仅 approval_request；审批提示（上游缺省时为空） |
| `app_name` | string | 否 | 仅 approval_request；发起审批的应用（mcp-elicitation 等）；上游缺省时缺省 |
| `options` | object[] | 否 | 仅 approval_request；审批可选项（上游自带，按序展示） |
| `options[].decision` | string | 是 | T3 ProviderApprovalDecision（accept/acceptForSession/acceptAlways/decline/cancel） |
| `options[].label` | string | 是 |  |
| `options[].warning` | string | 否 | 提供方给出的警示（如提示注入警告）；上游缺省时缺省 |
| `decision` | string | 否 | 仅 approval_request；已做出的决定（accept/acceptForSession/acceptAlways/decline/cancel），来自运行时请求记录 |
| `questions` | object[] | 否 | 仅 user_input_request；问题列表 |
| `questions[].id` | string | 是 |  |
| `questions[].header` | string | 是 |  |
| `questions[].question` | string | 是 |  |
| `questions[].options` | object[] | 是 |  |
| `questions[].options[].label` | string | 是 |  |
| `questions[].options[].description` | string | 是 |  |
| `questions[].options[].value` | string | 否 | 上游缺省时缺省（提交 answers 时以 label 为值） |
| `questions[].multi_select` | boolean | 否 | 上游缺省时缺省（视为单选） |
| `questions[].allow_custom_answer` | boolean | 否 | 上游缺省时缺省 |
| `questions[].initial_answer` | string | 否 | 可编辑的初始文本；上游缺省时缺省 |
| `questions[].required` | boolean | 否 | 上游缺省时缺省 |
| `answers` | object | 否 | 仅 user_input_request；已提交的答案（questionId → 答案值，原样透传） |
| `response_mode` | string | 否 | 仅 user_input_request；应答通道（message=经消息应答） |
| `response_capability` | string | 否 | 仅 approval_request/user_input_request；该请求当前的应答通道（live/message/not_resumable，运行时请求记录） |
| `request_status` | string | 否 | 仅 approval_request/user_input_request；运行时请求状态（pending/resolved/expired/cancelled；pending 才可应答） |
| `label` | string | 否 | 仅 secret_request；请求的密钥名称 |
| `reason` | string | 否 | 仅 secret_request；为什么需要该密钥 |
| `placeholder` | string | 否 | 仅 secret_request；输入框占位文本；上游缺省时缺省 |
| `secret_status` | string | 否 | 仅 secret_request；密钥请求状态（pending/saved/declined/cancelled；绝不携带密钥值本身） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 环境不存在（40417）、线程不存在（40418）或条目不属于该线程（40419） |
| 502 | T3 环境调用失败（50202，含凭证失效提示） |

### GET `/api/threads`

<!-- operationId: listT3Threads -->

聚合当前用户全部 T3 环境的线程（实时转发，不落库）

多环境有界并行读取（并行度 4），不落库。每环境在独立预算内：
先 `GET /api/auth/session` 分类凭证状态，再经 WS RPC
`orchestration.subscribeShell` 取活跃快照、一次性 RPC
`orchestration.getArchivedShellSnapshot` 取归档线程，随后立即断开；
整轮有总预算（小于前端超时）。单环境失败或超时不阻塞其它环境，
错误保留在 `environments` 数组的 `status/error` 中，健康环境的
已读结果照常返回。列表排除 `lineage.relationshipToParent=subagent` 的
子代理线程（与 T3 自身列表一致）。第一版返回完整轻量列表，
筛选与分页由前端处理。

**鉴权**：公开

**成功响应**

**200** 线程聚合与环境状态

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 | T3 threadId |
| `items[].environment_id` | string | 是 |  |
| `items[].environment_name` | string | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].project_name` | string | 是 | 来自 WS shell 快照项目表，列表和详情均使用该来源 |
| `items[].title` | string | 是 |  |
| `items[].status` | string（enum: running \| waiting \| idle \| archived） | 是 | 由 T3 归一化（对齐 T3 Sidebar 的判定顺序）： archivedAt 非空 → archived；其次 pendingRuntimeRequest 非空（等用户审批/输入）→ waiting； 再看 activityRunStatus（后台 run 未停时 shell.status 可能已是 completed/idle）： preparing/starting/running → running、waiting → waiting； 最后 shell.status 兜底（preparing/queued/starting/running → running、 waiting → waiting、其余 → idle） |
| `items[].branch` | string | 是 | 工作分支；T3 侧为 null 时为空串 |
| `items[].provider` | string | 是 | T3 providerInstanceId |
| `items[].model` | string | 是 |  |
| `items[].updated_at` | string | 是 | T3 原样返回的 ISO-8601 时间 |
| `items[].settled` | boolean | 是 | 上游 settledOverride=="settled" 原样透传：用户在 T3 把线程收进了历史区。归档判定看 status=archived，与 settled 互不蕴含 |
| `items[].run_started_at` | string | 否 | 最近 run 的 startedAt（上游 latestRunStartedAt）原样透传，供「运行中 N 分钟」计时；无线程运行记录时缺省 |
| `items[].pull_request_count` | integer | 是 | 关联 PR 数（pullRequests 数组长度；缺失时按 linkedPullRequest/branchPullRequest 计） |
| `items[].parent_thread_id` | string | 否 | 仅详情且该线程为子代理线程时给出（lineage.relationshipToParent=subagent 的 parentThreadId）；列表与父线程缺省 |
| `environments` | object[] | 是 |  |
| `environments[].id` | string | 是 |  |
| `environments[].name` | string | 是 |  |
| `environments[].address` | string | 是 | 从配对链接解析出的 T3 服务端基址（规范化 origin，如 https://host） |
| `environments[].status` | string（enum: ok \| unreachable \| repair \| degraded） | 是 | 探测状态：ok=Bearer 会话有效；unreachable=网络不可达/超时/5xx； repair=可达但凭证失效（Bearer 过期或被吊销），需重新配对； degraded=可达且凭证有效，但部分数据读取失败（如归档线程快照）， 其余数据仍返回 |
| `environments[].scopes` | string[] | 是 | 该环境会话实际授予的 T3 scope 列表（配对时服务端授予，原样落库） |
| `environments[].access` | string（enum: read-only \| operate） | 是 | 会话能力：仅当精确持有 `orchestration:operate` scope 时为 `operate`， 否则 `read-only`。唯一判定依据是精确 token 的 scope 列； `read-only` 环境调用任何写入口返回 40300（重新配对后可操作） |
| `environments[].error` | string | 否 | 最近一次探测失败的说明；成功时省略 |
| `environments[].updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |

### GET `/api/environments/{id}/composer-options`

<!-- operationId: getT3ComposerOptions -->

读取线程启动器选项（项目表 + 服务器配置 + 设置）

一次 WS 会话内取 T3 `orchestration.subscribeShell` 首快照（项目表）+
`server.getConfig` + `server.getSettings`。`projects` 为归一化项目表
（snake_case，含 workspace_root 供 workspace-options 解析 cwd）；
`config`/`settings` 为上游 `server.getConfig`/`server.getSettings`
响应原样透传（camelCase，字段随 T3 版本演进，客户端按需读取）。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |

**成功响应**

**200** 启动器选项

`data`：

线程启动器选项（projects 归一化；config/settings 为上游原样透传）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `projects` | object[] | 是 |  |
| `projects[].id` | string | 是 |  |
| `projects[].title` | string | 是 |  |
| `projects[].workspace_root` | string | 否 | 项目主检出根目录（workspace-options 据此解析 cwd）；上游缺省时缺省 |
| `projects[].default_model_selection` | object | 否 | T3 模型选择（instanceId 来自 composer-options 的 providers；缺省用项目默认） |
| `projects[].default_model_selection.instance_id` | string | 是 | T3 providerInstanceId |
| `projects[].default_model_selection.model` | string | 是 |  |
| `projects[].default_model_selection.options` | object[] | 否 | provider 实例声明的模型选项，canonical 数组 `[{id,value}]` （与 T3 ModelSelection.options 一致）；缺省省略 |
| `projects[].default_model_selection.options[].id` | string | 是 |  |
| `projects[].default_model_selection.options[].value` | string | 是 | 字符串或布尔（T3 ProviderOptionSelectionValue）。JSON 原样透传， 布尔选项发 JSON boolean 而非字符串。 |
| `config` | object | 是 | T3 `server.getConfig` 响应原样透传（camelCase，含 providers 等启动器所需的全部配置） |
| `settings` | object | 是 | T3 `server.getSettings` 响应原样透传（camelCase，含 newWorktreesStartFromOrigin 等设置） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 环境不存在（40417） |
| 502 | T3 环境调用失败（50202，含凭证失效提示） |

### GET `/api/environments/{id}/projects/{projectId}/workspace-options`

<!-- operationId: getT3WorkspaceOptions -->

读取线程工作区选项（当前分支 + 分支/引用列表）

项目与 cwd 从上游解析：先经 shell 快照找到该项目的 `workspaceRoot`，
再在 T3 上执行 `vcs.refreshStatus`（当前分支、工作区状态）与
`vcs.listRefs`（分支/引用分页列表，`query` 过滤、`cursor` 翻页）。
不接受前端任意 cwd。新建 worktree 线程的 `base_ref` 默认取这里的
`ref_name`（主检出当前分支）。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `projectId` | string | 是 | T3 projectId（来自 composer-options 的 projects[].id） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `query` | string（长度 ≤256） | 否 | 引用名过滤前缀（原样传给 vcs.listRefs） |
| `cursor` | integer（≥0） | 否 | 分页游标（来自上一页响应的 next_cursor） |

**成功响应**

**200** 工作区选项

`data`：

线程工作区选项（cwd 来自上游项目 workspaceRoot，不接受前端任意路径）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `project_id` | string | 是 |  |
| `cwd` | string | 是 | 项目主检出根目录 |
| `is_repo` | boolean | 否 | vcs.refreshStatus 报告该目录是否为 git 仓库 |
| `ref_name` | string | 否 | 主检出当前分支；新建 worktree 线程的 base_ref 缺省取此值 |
| `has_working_tree_changes` | boolean | 否 | 工作区有未提交变更 |
| `has_primary_remote` | boolean | 否 |  |
| `refs` | object[] | 是 |  |
| `refs[].name` | string | 是 |  |
| `refs[].kind` | string | 是 | local / remote（T3 refKind） |
| `refs[].is_default` | boolean | 否 | 上游标注的默认分支；上游缺省时缺省 |
| `refs[].worktree_path` | string | 否 | 该引用对应的既有工作树路径（若有）；上游缺省时缺省 |
| `next_cursor` | integer（可空） | 否 | vcs.listRefs 分页游标；null 表示没有更多 |
| `total_count` | integer | 否 | 过滤后的引用总数 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001，如 cursor 非法） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 环境不存在（40417）或项目不在该环境（40401） |
| 502 | T3 环境调用失败（50202） |

### POST `/api/environments/{id}/threads`

<!-- operationId: createT3Thread -->

在 T3 环境上启动新线程

经 WS RPC `orchestration.launchThread` 启动线程。thread_id 由 Fast Ship
生成（稳定 UUID，重试幂等）；title 必填，缺省时从首条消息生成种子标题，
并让 T3 生成正式标题（generateTitle=true）。runtime_mode 缺省
full-access；interaction_mode 固定 default。workspace 省略时为项目主检出
（root 不带 branch）；`worktree` 的 `base_ref` 缺省为主检出当前分支
（经 vcs.refreshStatus 解析），`start_from_origin` 缺省取 T3 设置
`newWorktreesStartFromOrigin`；`existing_worktree` 需给出已在 T3 侧
注册的 worktree_path。项目必须属于该环境（project_id 来自
composer-options），不接受前端任意 cwd。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `command_id` | string | 否 | 稳定命令 ID；缺省由服务端生成。同一操作重试必须带同一 ID |
| `thread_id` | string | 否 | 稳定线程 ID；缺省由服务端生成。同一操作重试必须带同一 ID（T3 据此幂等） |
| `project_id` | string | 是 | T3 projectId（来自 composer-options 的 projects[].id） |
| `message` | object | 是 | 首条消息（必填；title 缺省时据此生成种子标题）。 text 可为空，但 text 与 attachments 不能同时空。 |
| `message.message_id` | string | 否 | 稳定消息 ID；缺省由服务端生成 |
| `message.text` | string | 是 | 正文；仅图片消息时为空串 |
| `message.attachments` | object[] | 否 | 图片附件元数据（来自附件上传端点）；缺省按 [] |
| `message.attachments[].type` | string（enum: image） | 是 |  |
| `message.attachments[].id` | string | 是 | T3 附件 ID（createUploadUrl 产出的 attachmentId） |
| `message.attachments[].name` | string（长度 ≤255） | 是 |  |
| `message.attachments[].mime_type` | string | 是 | 仅支持 image/png、image/jpeg、image/gif、image/webp |
| `message.attachments[].size_bytes` | integer | 是 | 字节数；单图上限 10MiB |
| `title` | string | 否 | 线程标题；缺省时从首条消息生成种子标题并让 T3 生成正式标题 |
| `model_selection` | object | 否 | T3 模型选择（instanceId 来自 composer-options 的 providers；缺省用项目默认） |
| `model_selection.instance_id` | string | 是 | T3 providerInstanceId |
| `model_selection.model` | string | 是 |  |
| `model_selection.options` | object[] | 否 | provider 实例声明的模型选项，canonical 数组 `[{id,value}]` （与 T3 ModelSelection.options 一致）；缺省省略 |
| `model_selection.options[].id` | string | 是 |  |
| `model_selection.options[].value` | string | 是 | 字符串或布尔（T3 ProviderOptionSelectionValue）。JSON 原样透传， 布尔选项发 JSON boolean 而非字符串。 |
| `runtime_mode` | string（enum: approval-required \| auto-accept-edits \| auto \| full-access，默认 full-access） | 否 | 缺省 full-access |
| `workspace` | object | 否 | 新线程工作区（对齐 T3 OrchestrationV2ThreadLaunchWorkspaceStrategy）。 缺省为项目主检出（root 不带 branch）。`worktree` 的 `base_ref` 缺省为 主检出当前分支；`start_from_origin` 缺省取 T3 设置 `newWorktreesStartFromOrigin`。`existing_worktree` 的 worktree_path 必填。 |
| `workspace.type` | string（enum: root \| worktree \| existing_worktree） | 是 |  |
| `workspace.branch` | string | 否 | 新检出的分支名；缺省由 T3 决定（root 的 Current checkout 不传 branch） |
| `workspace.base_ref` | string | 否 | 仅 worktree；新建 worktree 的基线引用；缺省为主检出当前分支 |
| `workspace.start_from_origin` | boolean | 否 | 仅 worktree；缺省取 T3 设置 newWorktreesStartFromOrigin |
| `workspace.worktree_path` | string | 否 | 仅 existing_worktree；T3 侧已注册的工作树路径（来自 workspace-options 或线程详情） |

**成功响应**

**200** 线程已启动

`data`：

launchThread 结果归一化（thread 详情经 GET threads/{threadId} 读取）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `thread_id` | string | 是 |  |
| `resumed` | boolean | 是 | 复用了同 ID 的既有线程（幂等重试）时为 true |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001，如消息为空、工作区组合非法、图片不合规） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300，重新配对后可操作） |
| 404 | 环境不存在（40417） |
| 502 | T3 环境调用失败（50202） |

### POST `/api/environments/{id}/threads/{threadId}/messages`

<!-- operationId: sendT3ThreadMessage -->

向线程发送消息（活动运行时排队）

经 WS RPC `orchestration.dispatchCommand` 下发 `message.dispatch`，
dispatchMode 固定 `{type:"queue_after_active"}`：线程空闲立即开始，
活动中进入队列（线程详情的 queue 可见、可编辑/取消/上浮）。
attachments 缺省按 `[]` 发送（上游要求字段为数组）。
不支持 queued messages 的 provider 会话返回 50202（明确报告，
不自动改用 steer）。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `command_id` | string | 否 | 稳定命令 ID；缺省由服务端生成。同一操作重试必须带同一 ID |
| `message_id` | string | 否 | 稳定消息 ID；缺省由服务端生成 |
| `text` | string | 是 | 正文；仅图片消息时为空串。text 与 attachments 不能同时空 |
| `attachments` | object[] | 否 | 图片附件元数据；缺省按 [] 发送（上游要求字段为数组） |
| `attachments[].type` | string（enum: image） | 是 |  |
| `attachments[].id` | string | 是 | T3 附件 ID（createUploadUrl 产出的 attachmentId） |
| `attachments[].name` | string（长度 ≤255） | 是 |  |
| `attachments[].mime_type` | string | 是 | 仅支持 image/png、image/jpeg、image/gif、image/webp |
| `attachments[].size_bytes` | integer | 是 | 字节数；单图上限 10MiB |

**成功响应**

**200** 消息已受理（排队或立即开始）

`data`：

dispatchCommand 受理结果（sequence 为 T3 投影事件序号）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sequence` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001，如正文为空、附件超限） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417）或线程不存在（40418） |
| 502 | T3 环境调用失败（50202，含 provider 不支持排队） |

### POST `/api/environments/{id}/threads/{threadId}/stop`

<!-- operationId: stopT3Thread -->

停止线程当前活动（并挂起队列）

经 WS RPC `orchestration.dispatchCommand` 下发 `run.interrupt`
（holdQueue=true，T3 Stop 按钮口径：同时挂起队列）。目标 run 由
服务端选择：活动 run（preparing/starting/running/waiting 的最近一个），
否则仍有后台工作的最近 run（对齐 T3 客户端 stopThread 的选择逻辑）。
没有可停止的 run 时按 T3 口径视为无操作，返回 stopped=false，
不杜撰内部 thread.stop 命令。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |

**成功响应**

**200** 停止请求已下发（或无可停止运行）

`data`：

停止结果；无可停止运行时按 T3 口径视为无操作（stopped=false）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `stopped` | boolean | 是 |  |
| `run_id` | string | 否 | 被中断的 run；无可停止运行时缺省 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417）或线程不存在（40418） |
| 502 | T3 环境调用失败（50202） |

### POST `/api/environments/{id}/threads/{threadId}/queue/resume`

<!-- operationId: resumeT3ThreadQueue -->

恢复被挂起的排队队列

经 WS RPC 下发 `queue.resume`（Stop 挂起或重启恢复后的手动恢复）。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |

**成功响应**

**200** 队列已恢复

`data`：

dispatchCommand 受理结果（sequence 为 T3 投影事件序号）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sequence` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417）或线程不存在（40418） |
| 502 | T3 环境调用失败（50202） |

### POST `/api/environments/{id}/threads/{threadId}/queue/{runId}/steer`

<!-- operationId: steerT3QueuedRun -->

把排队消息上浮为对活动运行的转向输入

经 WS RPC 下发 `queued-message.promote-to-steer`。target_run_id 由服务端
解析为当前活动 run；`queue.can_steer=false`（provider 不支持）或没有
活动 run 时返回 40916/50202，不自动降级重试。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |
| `runId` | string | 是 | T3 runId（排队消息行，来自线程详情 queue.items[].run_id） |

**请求体**

`application/json`，必填。

target_run_id 由服务端解析为当前活动 run，请求体保留字段以显式表达意图

**成功响应**

**200** 已上浮

`data`：

dispatchCommand 受理结果（sequence 为 T3 投影事件序号）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sequence` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417）或线程不存在（40418） |
| 409 | 状态冲突（40916，如无活动 run 可转向） |
| 502 | T3 环境调用失败（50202） |

### POST `/api/environments/{id}/threads/{threadId}/queue/{runId}/cancel`

<!-- operationId: cancelT3QueuedRun -->

取消一条排队消息

经 WS RPC 下发 `queued-run.cancel`。队列已开始消费该 run 时返回 40916。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |
| `runId` | string | 是 | T3 runId（排队消息行，来自线程详情 queue.items[].run_id） |

**成功响应**

**200** 已取消

`data`：

dispatchCommand 受理结果（sequence 为 T3 投影事件序号）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sequence` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417）或线程不存在（40418） |
| 409 | 状态冲突（40916，如该 run 已开始） |
| 502 | T3 环境调用失败（50202） |

### PUT `/api/environments/{id}/threads/{threadId}/queue/{runId}`

<!-- operationId: editT3QueuedRun -->

编辑一条排队消息的正文与附件

经 WS RPC 下发 `queued-run.edit`。`attachments` 三态：字段缺省=保留
原附件（与 T3 语义一致，不得把缺省反序列化成空数组）；`[]`=清空；
非空数组=整组替换。队列已开始消费该 run 时返回 40916。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |
| `runId` | string | 是 | T3 runId（排队消息行，来自线程详情 queue.items[].run_id） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `text` | string（长度 ≥1） | 是 |  |
| `attachments` | object[] | 否 | 三态：字段缺省=保留原附件（不得反序列化成空数组）；[]=清空； 非空数组=整组替换 |
| `attachments[].type` | string（enum: image） | 是 |  |
| `attachments[].id` | string | 是 | T3 附件 ID（createUploadUrl 产出的 attachmentId） |
| `attachments[].name` | string（长度 ≤255） | 是 |  |
| `attachments[].mime_type` | string | 是 | 仅支持 image/png、image/jpeg、image/gif、image/webp |
| `attachments[].size_bytes` | integer | 是 | 字节数；单图上限 10MiB |

**成功响应**

**200** 已编辑

`data`：

dispatchCommand 受理结果（sequence 为 T3 投影事件序号）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sequence` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001，如正文为空） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417）或线程不存在（40418） |
| 409 | 状态冲突（40916，如该 run 已开始） |
| 502 | T3 环境调用失败（50202） |

### POST `/api/environments/{id}/threads/{threadId}/queue/{runId}/reorder`

<!-- operationId: reorderT3QueuedRun -->

调整排队消息顺序

经 WS RPC 下发 `queued-run.reorder`。`before_run_id` 为目标位置后一条
排队消息的 run_id；传 null 或缺省表示移到队尾。该 run 不在队列中时
返回 40916。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |
| `runId` | string | 是 | T3 runId（排队消息行，来自线程详情 queue.items[].run_id） |

**请求体**

`application/json`，必填。

before_run_id 为 null 或缺省表示移到队尾

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `before_run_id` | string（可空） | 否 | 目标位置后一条排队消息的 run_id |

**成功响应**

**200** 已调整

`data`：

dispatchCommand 受理结果（sequence 为 T3 投影事件序号）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sequence` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417）或线程不存在（40418） |
| 409 | 状态冲突（40916，如该 run 已不在队列） |
| 502 | T3 环境调用失败（50202） |

### POST `/api/environments/{id}/threads/{threadId}/requests/{requestId}/respond`

<!-- operationId: respondT3ThreadRequest -->

应答审批或用户输入请求

经 WS RPC 下发 `runtime-request.respond`。`type=approval` 携带 decision
（approve 缺省时使用请求自带 options 的 decision，缺省选项才是
Approve/Decline/Always allow this session/Cancel，服务端不自增
acceptAlways）；`type=user_input` 携带 answers（questionId → 答案）与
可选 attachments_by_question_id。请求已回答/已过期返回 40916。
继承（fork 前缀）请求不能在本线程原位应答——requestId 必须属于
本线程的 runtimeRequests。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |
| `requestId` | string（长度 ≤256） | 是 | T3 运行时请求 ID（来自线程详情 items[].request_id） |

**请求体**

`application/json`，必填。

应答联合：approval 携带 decision；user_input 携带 answers

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `type` | string（enum: approval \| user_input） | 是 |  |
| `decision` | string | 否 | 仅 type=approval；T3 ProviderApprovalDecision（accept/acceptForSession/acceptAlways/decline/cancel），应为请求 options 中的 decision |
| `answers` | object | 否 | 仅 type=user_input；questionId → 答案值（原样透传给 T3） |
| `attachments_by_question_id` | object | 否 | 仅 type=user_input；按问题追加图片附件；缺省不发 |

**成功响应**

**200** 已应答

`data`：

dispatchCommand 受理结果（sequence 为 T3 投影事件序号）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sequence` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001，如 decision/answers 非法） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417）、线程不存在（40418）或请求不属于本线程（40419） |
| 409 | 状态冲突（40916，请求已回答或已过期） |
| 502 | T3 环境调用失败（50202） |

### POST `/api/environments/{id}/threads/{threadId}/requests/{requestId}/dismiss`

<!-- operationId: dismissT3ThreadRequest -->

关闭用户输入请求卡片

经 WS RPC 下发 `thread.user-input.dismiss`（不提供答案，直接收起卡片）。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |
| `requestId` | string（长度 ≤256） | 是 | T3 运行时请求 ID（来自线程详情 items[].request_id） |

**成功响应**

**200** 已关闭

`data`：

dispatchCommand 受理结果（sequence 为 T3 投影事件序号）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sequence` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417）、线程不存在（40418）或请求不属于本线程（40419） |
| 409 | 状态冲突（40916，请求已回答或已过期） |
| 502 | T3 环境调用失败（50202） |

### POST `/api/environments/{id}/threads/{threadId}/items/{itemId}/secret-answer`

<!-- operationId: answerT3ThreadSecretRequest -->

应答线程内的密钥请求（保存或拒绝）

验证 itemId 是本线程本地 pending 的 secret_request 条目（合成 ID 转回
T3 真实 turnItemId），再经 WS RPC `secrets.answerRequest` 提交。
成功返回空 data。密钥值只在这一次请求中经过 Fast Ship：不落库、
不写日志、不出现在错误信息或任何回显中；上游失败只返回固定文案
（按 reason 映射 40916/40419/50202，不透传上游 cause）。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `threadId` | string | 是 | T3 threadId |
| `itemId` | string | 是 | Fast Ship 条目 ID（sourceThreadId + "::" + T3 原始条目 id，来自线程详情的 items[].id） |

**请求体**

`application/json`，必填。

密钥应答联合；save 携带密钥值（仅此一次经过，不落库不回显）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `type` | string | 是 | save（携带 secret）或 decline |
| `secret` | string（长度 ≥1） | 否 | 仅 type=save；密钥值 |

**成功响应**

**200** 已应答（空 data）

`data`：any

响应负载；形状随端点而变，由 allOf 组合的兄弟 schema 收窄

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001，如 save 的 secret 为空） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417）、线程不存在（40418）或条目不是本线程的 secret_request（40419） |
| 409 | 状态冲突（40916，该请求已被回答或代理已停止） |
| 502 | T3 环境调用失败（50202，固定文案，不含上游详情） |

### POST `/api/environments/{id}/attachments`

<!-- operationId: uploadT3Attachment -->

上传图片附件到 T3 环境（流转发，不落 Fast Ship 盘）

浏览器 → Fast Ship → T3：先经 WS RPC `attachments.createUploadUrl`
取得同 origin 签名上传 URL，再把请求体原始字节流式 POST 到该 URL，
返回可直接放进 messages/threads 请求 `attachments` 数组的
ChatImageAttachment 元数据。字节不落 Fast Ship 盘、不 base64、
不接受 multipart。仅支持 PNG/JPEG/GIF/WebP，单图上限 10MiB
（超限 40001）。请求头 `Content-Type` 即图片 MIME，
`X-File-Name` 提供文件名（缺省 `image.<ext>`）。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |

**Header 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `X-File-Name` | string（长度 ≤255） | 否 | 文件显示名（进入 T3 后的附件 name） |

**请求体**

`image/gif`，必填。

`image/jpeg`，必填。

`image/png`，必填。

`image/webp`，必填。

**成功响应**

**200** 上传成功，返回附件元数据

`data`：

随消息发送的图片附件元数据（T3 ChatImageAttachment 归一化；上传经 environments/{id}/attachments）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `type` | string（enum: image） | 是 |  |
| `id` | string | 是 | T3 附件 ID（createUploadUrl 产出的 attachmentId） |
| `name` | string（长度 ≤255） | 是 |  |
| `mime_type` | string | 是 | 仅支持 image/png、image/jpeg、image/gif、image/webp |
| `size_bytes` | integer | 是 | 字节数；单图上限 10MiB |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001，MIME 不支持/超 10MiB/缺 Content-Length） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417） |
| 502 | T3 环境调用失败（50202） |

### GET `/api/environments/{id}/attachments/{attachmentId}/content`

<!-- operationId: getT3AttachmentContent -->

读取 T3 附件内容（Fast Ship 代理字节）

经 WS RPC `assets.createUrl`（resource=_tag:"attachment"）取得签名下载
URL，再由 Fast Ship 拉取字节并原样代理给浏览器（带上游 Content-Type）。
字节不落盘。附件不存在或上传已过期返回 40417 语义之外的 40418；
上游未就绪返回 50202。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `attachmentId` | string（长度 ≤256） | 是 | T3 附件 ID（来自附件上传响应的 id） |

**成功响应**

**200** 附件字节（Content-Type 为图片 MIME）

二进制内容（image/gif、image/jpeg、image/png、image/webp），无 JSON 信封。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 环境不存在（40417）或附件不存在/已过期（40418） |
| 502 | T3 环境调用失败（50202） |

### DELETE `/api/environments/{id}/attachments/{attachmentId}`

<!-- operationId: deleteT3Attachment -->

删除 T3 上的待用附件

经 WS RPC `attachments.delete` 删除尚未被消息引用的 pending 附件。

**鉴权**：公开

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | T3 环境 ID（UUID，归属当前用户，跨用户一律 40417） |
| `attachmentId` | string（长度 ≤256） | 是 | T3 附件 ID（来自附件上传响应的 id） |

**成功响应**

**200** 删除成功

`data`：any

响应负载；形状随端点而变，由 allOf 组合的兄弟 schema 收窄

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 该环境为只读会话（40300） |
| 404 | 环境不存在（40417） |
| 502 | T3 环境调用失败（50202） |
