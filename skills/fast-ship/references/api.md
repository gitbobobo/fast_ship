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

注册新用户（首个用户成为管理员；之后需有效邀请）

INT-129 起公开注册按部署状态区分：
- 库里还没有任何用户：本次注册创建**第一个用户**，`role=admin`，`invite_token` 即使携带也被忽略；
- 库里已有用户：必须携带有效 `invite_token`（未使用、未撤销、未过期），新用户固定 `role=member`，
  邀请在注册同一事务内置为已使用（并发重复使用会整体失败，40932）。
无邀请（或邀请无效/已过期/已撤销）时返回 40306 / 40432。注册页可先调
`POST /api/auth/invites/validate` 校验（token 在请求体，不进 URL）。

**鉴权**：公开

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `username` | string（长度 2..50） | 是 | 用户名，2-50 字符，全局唯一 |
| `email` | string（email） | 是 | 邮箱，全局唯一 |
| `password` | string（长度 ≥8） | 是 | 密码，≥8 字符 |
| `invite_token` | string | 否 | 邀请明文 token（/register?invite= 携带）；部署还没有用户时被忽略（首个用户直接成为 admin），已有用户时必须有效 |

**成功响应**

**200** 注册成功，返回 token 与用户信息（首个用户 role=admin，其余 role=member）

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
| `user.role` | string（enum: admin \| member） | 是 | 用户角色（INT-129）：admin 可管理成员、邀请与部署设置；member 只能使用自己的资源。角色只存库，服务端每请求查库判定，不写进 JWT |
| `user.timezone` | string | 是 | 用户 IANA 时区（INT-138）；空串表示未声明，机器人按部署配置回退 |
| `user.effective_timezone` | string | 否 | 生效时区（INT-138）：timezone 非空时为它，否则为部署配置 bots.memory.timezone，再否则 UTC。/auth/me 会返回；成员列表不返回（省略） |
| `user.created_at` | string（date-time） | 是 |  |
| `user.updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 403 | 已有用户且未携带邀请，公开注册关闭（40306） |
| 404 | 邀请无效或已过期（40432；不存在/已过期/已撤销统一此码） |
| 409 | 用户名已存在（40900）/ 邮箱已存在（40901）/ 邀请已被使用（40932） |
| 500 | 服务器内部错误（50000） |

### POST `/api/auth/invites/validate`

<!-- operationId: validateInvite -->

校验注册邀请是否可用（公开，注册页提前显示状态）

供 `/register?invite=<token>` 页面在渲染表单前调用。token 走请求体、
不落 URL 路径，避免明文被请求日志（selflog 记录 path）记下后被读回复用。
有效返回过期时间；不存在、已过期、已撤销、已被使用统一返回 40432，
不区分具体原因。

**鉴权**：公开

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 是 | 邀请链接中的明文 token；走请求体，不进 URL 路径 |

**成功响应**

**200** 邀请可用

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `expires_at` | string（date-time） | 是 | 邀说过期时间 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 404 | 邀请无效或已过期（40432；不存在/已过期/已撤销/已使用/请求体缺 token 统一此码） |
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
| `user.role` | string（enum: admin \| member） | 是 | 用户角色（INT-129）：admin 可管理成员、邀请与部署设置；member 只能使用自己的资源。角色只存库，服务端每请求查库判定，不写进 JWT |
| `user.timezone` | string | 是 | 用户 IANA 时区（INT-138）；空串表示未声明，机器人按部署配置回退 |
| `user.effective_timezone` | string | 否 | 生效时区（INT-138）：timezone 非空时为它，否则为部署配置 bots.memory.timezone，再否则 UTC。/auth/me 会返回；成员列表不返回（省略） |
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
| `role` | string（enum: admin \| member） | 是 | 用户角色（INT-129）：admin 可管理成员、邀请与部署设置；member 只能使用自己的资源。角色只存库，服务端每请求查库判定，不写进 JWT |
| `timezone` | string | 是 | 用户 IANA 时区（INT-138）；空串表示未声明，机器人按部署配置回退 |
| `effective_timezone` | string | 否 | 生效时区（INT-138）：timezone 非空时为它，否则为部署配置 bots.memory.timezone，再否则 UTC。/auth/me 会返回；成员列表不返回（省略） |
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
| `timezone` | string（长度 ≤64） | 否 | 用户 IANA 时区（INT-138）；空串清空，非法名称拒绝 |

**成功响应**

**200** 更新后的用户信息

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `username` | string | 是 |  |
| `email` | string | 是 |  |
| `avatar_url` | string | 是 | 形如 /api/avatars/:uid/:filename，未设置为空串 |
| `role` | string（enum: admin \| member） | 是 | 用户角色（INT-129）：admin 可管理成员、邀请与部署设置；member 只能使用自己的资源。角色只存库，服务端每请求查库判定，不写进 JWT |
| `timezone` | string | 是 | 用户 IANA 时区（INT-138）；空串表示未声明，机器人按部署配置回退 |
| `effective_timezone` | string | 否 | 生效时区（INT-138）：timezone 非空时为它，否则为部署配置 bots.memory.timezone，再否则 UTC。/auth/me 会返回；成员列表不返回（省略） |
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

修改密码（吊销其他全部会话；返回为当前会话新签发的 token 与 refresh token）

**鉴权**：JWT+API Key

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `old_password` | string | 是 |  |
| `new_password` | string（长度 ≥8） | 是 |  |

**成功响应**

**200** 改密成功；该用户其余 refresh token 已吊销、改密前签发的 access token 已失效，data 为当前会话的新 token 对

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `token` | string | 是 |  |
| `refresh_token` | string | 是 |  |

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
| `role` | string（enum: admin \| member） | 是 | 用户角色（INT-129）：admin 可管理成员、邀请与部署设置；member 只能使用自己的资源。角色只存库，服务端每请求查库判定，不写进 JWT |
| `timezone` | string | 是 | 用户 IANA 时区（INT-138）；空串表示未声明，机器人按部署配置回退 |
| `effective_timezone` | string | 否 | 生效时区（INT-138）：timezone 非空时为它，否则为部署配置 bots.memory.timezone，再否则 UTC。/auth/me 会返回；成员列表不返回（省略） |
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
| `prompts[].preferences` | object | 否 | T3 启动偏好（绑定在提示词条目上的默认值；对象缺省即各字段取默认）。 写入时校验：environment 只能是 "auto" 或已存在的 T3 环境 ID， workspace 只能是 "worktree"\|"root"，model_selection 的字段类型； 非法值返回 40001 |
| `prompts[].preferences.environment` | string | 否 | "auto" 或已存在的 T3 环境 ID；缺省 auto |
| `prompts[].preferences.workspace` | string | 否 | "worktree" 或 "root"；缺省 worktree |
| `prompts[].preferences.model_selection` | object | 否 | T3 模型选择（instanceId 来自 composer-options 的 providers；缺省用项目默认） |
| `prompts[].preferences.model_selection.instance_id` | string | 是 | T3 providerInstanceId |
| `prompts[].preferences.model_selection.model` | string | 是 |  |
| `prompts[].preferences.model_selection.options` | object[] | 否 | provider 实例声明的模型选项，canonical 数组 `[{id,value}]` （与 T3 ModelSelection.options 一致）；缺省省略 |
| `prompts[].preferences.model_selection.options[].id` | string | 是 |  |
| `prompts[].preferences.model_selection.options[].value` | string | 是 | 字符串或布尔（T3 ProviderOptionSelectionValue）。JSON 原样透传， 布尔选项发 JSON boolean 而非字符串。 |

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
| `prompts[].preferences` | object | 否 | T3 启动偏好（绑定在提示词条目上的默认值；对象缺省即各字段取默认）。 写入时校验：environment 只能是 "auto" 或已存在的 T3 环境 ID， workspace 只能是 "worktree"\|"root"，model_selection 的字段类型； 非法值返回 40001 |
| `prompts[].preferences.environment` | string | 否 | "auto" 或已存在的 T3 环境 ID；缺省 auto |
| `prompts[].preferences.workspace` | string | 否 | "worktree" 或 "root"；缺省 worktree |
| `prompts[].preferences.model_selection` | object | 否 | T3 模型选择（instanceId 来自 composer-options 的 providers；缺省用项目默认） |
| `prompts[].preferences.model_selection.instance_id` | string | 是 | T3 providerInstanceId |
| `prompts[].preferences.model_selection.model` | string | 是 |  |
| `prompts[].preferences.model_selection.options` | object[] | 否 | provider 实例声明的模型选项，canonical 数组 `[{id,value}]` （与 T3 ModelSelection.options 一致）；缺省省略 |
| `prompts[].preferences.model_selection.options[].id` | string | 是 |  |
| `prompts[].preferences.model_selection.options[].value` | string | 是 | 字符串或布尔（T3 ProviderOptionSelectionValue）。JSON 原样透传， 布尔选项发 JSON boolean 而非字符串。 |

**成功响应**

**200** 更新后的提示词列表

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `prompts` | object[]（可空） | 是 | 未配置时为 null，前端兜底默认提示词 |
| `prompts[].id` | string | 是 | trim 后非空 |
| `prompts[].name` | string | 是 | trim 后非空 |
| `prompts[].content` | string | 是 | 提示词正文；trim 后非空 |
| `prompts[].preferences` | object | 否 | T3 启动偏好（绑定在提示词条目上的默认值；对象缺省即各字段取默认）。 写入时校验：environment 只能是 "auto" 或已存在的 T3 环境 ID， workspace 只能是 "worktree"\|"root"，model_selection 的字段类型； 非法值返回 40001 |
| `prompts[].preferences.environment` | string | 否 | "auto" 或已存在的 T3 环境 ID；缺省 auto |
| `prompts[].preferences.workspace` | string | 否 | "worktree" 或 "root"；缺省 worktree |
| `prompts[].preferences.model_selection` | object | 否 | T3 模型选择（instanceId 来自 composer-options 的 providers；缺省用项目默认） |
| `prompts[].preferences.model_selection.instance_id` | string | 是 | T3 providerInstanceId |
| `prompts[].preferences.model_selection.model` | string | 是 |  |
| `prompts[].preferences.model_selection.options` | object[] | 否 | provider 实例声明的模型选项，canonical 数组 `[{id,value}]` （与 T3 ModelSelection.options 一致）；缺省省略 |
| `prompts[].preferences.model_selection.options[].id` | string | 是 |  |
| `prompts[].preferences.model_selection.options[].value` | string | 是 | 字符串或布尔（T3 ProviderOptionSelectionValue）。JSON 原样透传， 布尔选项发 JSON boolean 而非字符串。 |

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
| `key` | string | 是 | 完整 API Key（fsk_ 前缀），仅创建响应中出现一次，服务端不存原文 |

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
| `items[].code_github_owner` | string | 是 | 代码仓库（PR 审查用）owner；未单独配置为空串，表示与公开仓库相同 |
| `items[].code_github_repo` | string | 是 | 代码仓库（PR 审查用）repo 名；未单独配置为空串，表示与公开仓库相同 |
| `items[].public_github_token_id` | string | 是 | 公开仓库 Token 引用；未配置为空串 |
| `items[].code_github_token_id` | string | 是 | 代码仓库 Token 引用；未配置为空串 |
| `items[].code_integration_id` | string（可空） | 是 | 代码仓集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `items[].public_integration_id` | string（可空） | 是 | 公开仓库集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `items[].code_repo_url` | string（可空） | 是 | 代码仓页面地址（INT-166）。无代码仓（owner/repo 缺一）为 null；内置 github 集成为 https://github.com/{owner}/{repo}；forgejo 集成为 {base_url}/{owner}/{repo}；绑定的集成行已删除时为 null（不回落 github.com，避免私有仓库地址外发） |
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
| `items[].shelved_at` | string（date-time，可空） | 是 | 搁置开始时间；null 表示未搁置（INT-165） |
| `items[].shelved_until` | string（date-time，可空） | 是 | 搁置截止时间；null 表示无限期或未搁置（INT-165） |
| `items[].is_shelved` | boolean | 是 | 响应生成时刻是否处于搁置中；到期自动恢复，无后台任务（INT-165） |
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
| `code_repository_url` | string | 否 | 代码仓库链接（owner/repo 或完整 URL），供 PR 审查使用；空 = 与公开仓库相同 |
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
| `code_github_owner` | string | 是 | 代码仓库（PR 审查用）owner；未单独配置为空串，表示与公开仓库相同 |
| `code_github_repo` | string | 是 | 代码仓库（PR 审查用）repo 名；未单独配置为空串，表示与公开仓库相同 |
| `public_github_token_id` | string | 是 | 公开仓库 Token 引用；未配置为空串 |
| `code_github_token_id` | string | 是 | 代码仓库 Token 引用；未配置为空串 |
| `code_integration_id` | string（可空） | 是 | 代码仓集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `public_integration_id` | string（可空） | 是 | 公开仓库集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `code_repo_url` | string（可空） | 是 | 代码仓页面地址（INT-166）。无代码仓（owner/repo 缺一）为 null；内置 github 集成为 https://github.com/{owner}/{repo}；forgejo 集成为 {base_url}/{owner}/{repo}；绑定的集成行已删除时为 null（不回落 github.com，避免私有仓库地址外发） |
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
| `shelved_at` | string（date-time，可空） | 是 | 搁置开始时间；null 表示未搁置（INT-165） |
| `shelved_until` | string（date-time，可空） | 是 | 搁置截止时间；null 表示无限期或未搁置（INT-165） |
| `is_shelved` | boolean | 是 | 响应生成时刻是否处于搁置中；到期自动恢复，无后台任务（INT-165） |
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
| `code_github_owner` | string | 是 | 代码仓库（PR 审查用）owner；未单独配置为空串，表示与公开仓库相同 |
| `code_github_repo` | string | 是 | 代码仓库（PR 审查用）repo 名；未单独配置为空串，表示与公开仓库相同 |
| `public_github_token_id` | string | 是 | 公开仓库 Token 引用；未配置为空串 |
| `code_github_token_id` | string | 是 | 代码仓库 Token 引用；未配置为空串 |
| `code_integration_id` | string（可空） | 是 | 代码仓集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `public_integration_id` | string（可空） | 是 | 公开仓库集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `code_repo_url` | string（可空） | 是 | 代码仓页面地址（INT-166）。无代码仓（owner/repo 缺一）为 null；内置 github 集成为 https://github.com/{owner}/{repo}；forgejo 集成为 {base_url}/{owner}/{repo}；绑定的集成行已删除时为 null（不回落 github.com，避免私有仓库地址外发） |
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
| `shelved_at` | string（date-time，可空） | 是 | 搁置开始时间；null 表示未搁置（INT-165） |
| `shelved_until` | string（date-time，可空） | 是 | 搁置截止时间；null 表示无限期或未搁置（INT-165） |
| `is_shelved` | boolean | 是 | 响应生成时刻是否处于搁置中；到期自动恢复，无后台任务（INT-165） |
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
| `code_repository_url` | string（可空） | 否 | 三态——key 缺省保留现值；显式 null 或空串清空（回落公开仓库）；非空替换（owner/repo 或完整 URL；与公开仓库相同则归一为空） |
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
| `code_github_owner` | string | 是 | 代码仓库（PR 审查用）owner；未单独配置为空串，表示与公开仓库相同 |
| `code_github_repo` | string | 是 | 代码仓库（PR 审查用）repo 名；未单独配置为空串，表示与公开仓库相同 |
| `public_github_token_id` | string | 是 | 公开仓库 Token 引用；未配置为空串 |
| `code_github_token_id` | string | 是 | 代码仓库 Token 引用；未配置为空串 |
| `code_integration_id` | string（可空） | 是 | 代码仓集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `public_integration_id` | string（可空） | 是 | 公开仓库集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `code_repo_url` | string（可空） | 是 | 代码仓页面地址（INT-166）。无代码仓（owner/repo 缺一）为 null；内置 github 集成为 https://github.com/{owner}/{repo}；forgejo 集成为 {base_url}/{owner}/{repo}；绑定的集成行已删除时为 null（不回落 github.com，避免私有仓库地址外发） |
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
| `shelved_at` | string（date-time，可空） | 是 | 搁置开始时间；null 表示未搁置（INT-165） |
| `shelved_until` | string（date-time，可空） | 是 | 搁置截止时间；null 表示无限期或未搁置（INT-165） |
| `is_shelved` | boolean | 是 | 响应生成时刻是否处于搁置中；到期自动恢复，无后台任务（INT-165） |
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

### POST `/api/projects/{id}/shelve`

<!-- operationId: shelveProject -->

搁置项目（仅 JWT）。body 可省略或为空对象表示无限期；until 提供时必须晚于当前时间（INT-165）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`application/json`，可选。

搁置项目请求体；整包可省略或为空对象，表示无限期搁置（INT-165）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `until` | string（date-time） | 否 | 搁置截止时间（RFC3339）；提供时必须晚于当前时间，缺省表示无限期 |

**成功响应**

**200** 搁置后的项目

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `name` | string | 是 |  |
| `description` | string | 是 |  |
| `github_owner` | string | 是 |  |
| `github_repo` | string | 是 |  |
| `code_github_owner` | string | 是 | 代码仓库（PR 审查用）owner；未单独配置为空串，表示与公开仓库相同 |
| `code_github_repo` | string | 是 | 代码仓库（PR 审查用）repo 名；未单独配置为空串，表示与公开仓库相同 |
| `public_github_token_id` | string | 是 | 公开仓库 Token 引用；未配置为空串 |
| `code_github_token_id` | string | 是 | 代码仓库 Token 引用；未配置为空串 |
| `code_integration_id` | string（可空） | 是 | 代码仓集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `public_integration_id` | string（可空） | 是 | 公开仓库集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `code_repo_url` | string（可空） | 是 | 代码仓页面地址（INT-166）。无代码仓（owner/repo 缺一）为 null；内置 github 集成为 https://github.com/{owner}/{repo}；forgejo 集成为 {base_url}/{owner}/{repo}；绑定的集成行已删除时为 null（不回落 github.com，避免私有仓库地址外发） |
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
| `shelved_at` | string（date-time，可空） | 是 | 搁置开始时间；null 表示未搁置（INT-165） |
| `shelved_until` | string（date-time，可空） | 是 | 搁置截止时间；null 表示无限期或未搁置（INT-165） |
| `is_shelved` | boolean | 是 | 响应生成时刻是否处于搁置中；到期自动恢复，无后台任务（INT-165） |
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

### DELETE `/api/projects/{id}/shelve`

<!-- operationId: unshelveProject -->

取消项目搁置（仅 JWT）；幂等，未搁置的项目也返回 200（INT-165）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 取消搁置后的项目

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `name` | string | 是 |  |
| `description` | string | 是 |  |
| `github_owner` | string | 是 |  |
| `github_repo` | string | 是 |  |
| `code_github_owner` | string | 是 | 代码仓库（PR 审查用）owner；未单独配置为空串，表示与公开仓库相同 |
| `code_github_repo` | string | 是 | 代码仓库（PR 审查用）repo 名；未单独配置为空串，表示与公开仓库相同 |
| `public_github_token_id` | string | 是 | 公开仓库 Token 引用；未配置为空串 |
| `code_github_token_id` | string | 是 | 代码仓库 Token 引用；未配置为空串 |
| `code_integration_id` | string（可空） | 是 | 代码仓集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `public_integration_id` | string（可空） | 是 | 公开仓库集成引用（INT-166）；null 或空串 = 内置 github-default。写入接线由后续任务完成，本期只随响应带出 |
| `code_repo_url` | string（可空） | 是 | 代码仓页面地址（INT-166）。无代码仓（owner/repo 缺一）为 null；内置 github 集成为 https://github.com/{owner}/{repo}；forgejo 集成为 {base_url}/{owner}/{repo}；绑定的集成行已删除时为 null（不回落 github.com，避免私有仓库地址外发） |
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
| `shelved_at` | string（date-time，可空） | 是 | 搁置开始时间；null 表示未搁置（INT-165） |
| `shelved_until` | string（date-time，可空） | 是 | 搁置截止时间；null 表示无限期或未搁置（INT-165） |
| `is_shelved` | boolean | 是 | 响应生成时刻是否处于搁置中；到期自动恢复，无后台任务（INT-165） |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

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
| `items[].screenshot_annotations[].screen_id` | string | 是 | 所属截图 ID |
| `items[].screenshot_annotations[].issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `items[].screenshot_annotations[].issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `items[].screenshot_annotations[].issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `items[].screenshot_annotations[].screen_key` | string | 是 |  |
| `items[].screenshot_annotations[].screen_title` | string | 是 | 截图显示名；空串时客户端回退展示 screen_key |
| `items[].screenshot_annotations[].screen_platform` | string（可空） | 是 | 所属截图的平台；null 表示未标注 |
| `items[].screenshot_annotations[].screen_locale` | string（可空） | 是 | 所属截图的语言；null 表示未标注 |
| `items[].screenshot_annotations[].screen_theme` | string（可空） | 是 | 所属截图的亮度主题；null 表示未标注 |
| `items[].screenshot_annotations[].image_url` | string | 是 | 所在截图的原图地址（content_url，带 ?v= 修订参数）；追加 &token= 供 <img> |
| `items[].screenshot_annotations[].image_width` | integer | 是 | 原图宽像素；0 表示未知 |
| `items[].screenshot_annotations[].image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `items[].screenshot_annotations[].outdated` | boolean | 是 | 截图被覆盖更新后仍未「确认保留」；resolved 标注恒为 false |
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
| `items[].screenshot_annotations[].crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop?v=<所属截图 uploaded_at 纳秒值>；统一输出 PNG，v 随覆盖上传变化（浏览器缓存按完整 URL 记键），前端自行追加 &token= |
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
| `screenshot_annotations[].screen_id` | string | 是 | 所属截图 ID |
| `screenshot_annotations[].issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `screenshot_annotations[].issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `screenshot_annotations[].issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `screenshot_annotations[].screen_key` | string | 是 |  |
| `screenshot_annotations[].screen_title` | string | 是 | 截图显示名；空串时客户端回退展示 screen_key |
| `screenshot_annotations[].screen_platform` | string（可空） | 是 | 所属截图的平台；null 表示未标注 |
| `screenshot_annotations[].screen_locale` | string（可空） | 是 | 所属截图的语言；null 表示未标注 |
| `screenshot_annotations[].screen_theme` | string（可空） | 是 | 所属截图的亮度主题；null 表示未标注 |
| `screenshot_annotations[].image_url` | string | 是 | 所在截图的原图地址（content_url，带 ?v= 修订参数）；追加 &token= 供 <img> |
| `screenshot_annotations[].image_width` | integer | 是 | 原图宽像素；0 表示未知 |
| `screenshot_annotations[].image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `screenshot_annotations[].outdated` | boolean | 是 | 截图被覆盖更新后仍未「确认保留」；resolved 标注恒为 false |
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
| `screenshot_annotations[].crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop?v=<所属截图 uploaded_at 纳秒值>；统一输出 PNG，v 随覆盖上传变化（浏览器缓存按完整 URL 记键），前端自行追加 &token= |
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
| `screenshot_annotations[].screen_id` | string | 是 | 所属截图 ID |
| `screenshot_annotations[].issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `screenshot_annotations[].issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `screenshot_annotations[].issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `screenshot_annotations[].screen_key` | string | 是 |  |
| `screenshot_annotations[].screen_title` | string | 是 | 截图显示名；空串时客户端回退展示 screen_key |
| `screenshot_annotations[].screen_platform` | string（可空） | 是 | 所属截图的平台；null 表示未标注 |
| `screenshot_annotations[].screen_locale` | string（可空） | 是 | 所属截图的语言；null 表示未标注 |
| `screenshot_annotations[].screen_theme` | string（可空） | 是 | 所属截图的亮度主题；null 表示未标注 |
| `screenshot_annotations[].image_url` | string | 是 | 所在截图的原图地址（content_url，带 ?v= 修订参数）；追加 &token= 供 <img> |
| `screenshot_annotations[].image_width` | integer | 是 | 原图宽像素；0 表示未知 |
| `screenshot_annotations[].image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `screenshot_annotations[].outdated` | boolean | 是 | 截图被覆盖更新后仍未「确认保留」；resolved 标注恒为 false |
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
| `screenshot_annotations[].crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop?v=<所属截图 uploaded_at 纳秒值>；统一输出 PNG，v 随覆盖上传变化（浏览器缓存按完整 URL 记键），前端自行追加 &token= |
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
| `screenshot_annotations[].screen_id` | string | 是 | 所属截图 ID |
| `screenshot_annotations[].issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `screenshot_annotations[].issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `screenshot_annotations[].issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `screenshot_annotations[].screen_key` | string | 是 |  |
| `screenshot_annotations[].screen_title` | string | 是 | 截图显示名；空串时客户端回退展示 screen_key |
| `screenshot_annotations[].screen_platform` | string（可空） | 是 | 所属截图的平台；null 表示未标注 |
| `screenshot_annotations[].screen_locale` | string（可空） | 是 | 所属截图的语言；null 表示未标注 |
| `screenshot_annotations[].screen_theme` | string（可空） | 是 | 所属截图的亮度主题；null 表示未标注 |
| `screenshot_annotations[].image_url` | string | 是 | 所在截图的原图地址（content_url，带 ?v= 修订参数）；追加 &token= 供 <img> |
| `screenshot_annotations[].image_width` | integer | 是 | 原图宽像素；0 表示未知 |
| `screenshot_annotations[].image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `screenshot_annotations[].outdated` | boolean | 是 | 截图被覆盖更新后仍未「确认保留」；resolved 标注恒为 false |
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
| `screenshot_annotations[].crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop?v=<所属截图 uploaded_at 纳秒值>；统一输出 PNG，v 随覆盖上传变化（浏览器缓存按完整 URL 记键），前端自行追加 &token= |
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

项目截图库（每变体一张图，覆盖上传即替换文件，只保留最新版）

### GET `/api/projects/{id}/screenshots`

<!-- operationId: listScreenshotScreens -->

列出项目全部截图（uploaded_at 倒序）

可选 query 过滤：`platform`/`locale`/`theme`（逗号分隔多值，OR 语义；值 `__none__` 表示「未标注」桶即 NULL，可与其他值混用，可组合）。
每项为截图字段 + 文件信息 + content_url。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `platform` | string | 否 | 逗号分隔多值；`__none__` 表示未标注（NULL） |
| `locale` | string | 否 | 逗号分隔多值；`__none__` 表示未标注（NULL） |
| `theme` | string | 否 | 逗号分隔多值（light/dark）；`__none__` 表示未标注（NULL） |

**成功响应**

**200** 截图列表（data 为 {items}，非分页信封）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].screen_key` | string | 是 | 服务端规范化（ToLower+TrimSpace）后的界面标识 |
| `items[].title` | string | 是 | 可为空串，客户端回退显示 screen_key |
| `items[].platform` | string（可空） | 是 | 平台（开放词表，trim+小写）；null 表示未标注 |
| `items[].locale` | string（可空） | 是 | 语言（BCP 47 规范化大小写）；null 表示未标注 |
| `items[].theme` | string（enum: light \| dark，可空） | 是 | 亮度主题；null 表示未标注 |
| `items[].mime_type` | string | 是 | 上传时内容嗅探得到的类型（image/png、image/jpeg、image/webp、image/gif 之一） |
| `items[].file_size` | integer（int64） | 是 | 文件字节数 |
| `items[].width` | integer | 是 | 原图宽像素；0 表示未知 |
| `items[].height` | integer | 是 | 原图高像素；0 表示未知 |
| `items[].uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `items[].uploaded_at` | string（date-time） | 是 | 当前文件的上传时间；覆盖上传即更新，列表按它倒序 |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].content_url` | string | 是 | /api/screenshot-screens/{sid}/content?v=<uploaded_at 纳秒值>；v 随文件变化（覆盖上传即换 URL，浏览器缓存按完整 URL 记键），前端自行追加 &token= |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/projects/{id}/screenshots`

<!-- operationId: uploadScreenshot -->

上传界面截图（multipart/form-data，字段名 file + screen_key）

multipart 字段：`file`（必填；内容嗅探须为 image/png、image/jpeg、image/webp、image/gif，大小受服务端 upload.max_file_size 限制）、
`screen_key`（必填；服务端按 ToLower+TrimSpace 规范化，规范化后须 1~100 字符，否则 40001）、
`title`/`platform`/`locale`/`theme`（可选）。
(project_id, screen_key, platform, locale, theme) 定位一张截图（NULL 与 NULL 视为相等）：
已存在即覆盖——替换文件、更新宽高/大小/上传时间/上传者，不新增行；不存在则新建。
表单字段存在性语义：`platform`（trim+小写化，开放词表；空串=清除为未标注）、`locale`（BCP 47 规范化，非法值 40001；空串=清除）、
`theme`（严格 light|dark，其他值 40001；空串=清除）字段出现才生效，不出现保持原值——按同 screen_key 既有截图继承缺席维度；
已给出的维度能唯一命中才继承，命中多个变体（省略无法定位唯一目标）返回 40001，零个命中按缺席维度新建；`title` 非空才更新。
覆盖后该截图上全部 open 标注置为 outdated（待确认）；「确认保留」清零。
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
| `title` | string（长度 ≤200） | 否 | 界面标题（≤200 字符）；非空才更新 |
| `platform` | string（长度 ≤100） | 否 | 平台（开放词表，trim+小写，≤100 字符）；字段出现即生效（空串清除为未标注） |
| `locale` | string | 否 | 语言（BCP 47，服务端规范化大小写，非法值 40001）；字段出现即生效（空串清除） |
| `theme` | string（enum: light \| dark） | 否 | 亮度主题（严格 light\|dark，其他值 40001）；字段出现即生效（空串清除） |

**成功响应**

**200** 上传结果（screen 为新建或覆盖后的截图）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `screen` | object | 是 | 截图库中的一张截图：(project_id, screen_key, platform, locale, theme) 唯一，再上传即覆盖文件 |
| `screen.id` | string | 是 |  |
| `screen.project_id` | string | 是 |  |
| `screen.screen_key` | string | 是 | 服务端规范化（ToLower+TrimSpace）后的界面标识 |
| `screen.title` | string | 是 | 可为空串，客户端回退显示 screen_key |
| `screen.platform` | string（可空） | 是 | 平台（开放词表，trim+小写）；null 表示未标注 |
| `screen.locale` | string（可空） | 是 | 语言（BCP 47 规范化大小写）；null 表示未标注 |
| `screen.theme` | string（enum: light \| dark，可空） | 是 | 亮度主题；null 表示未标注 |
| `screen.mime_type` | string | 是 | 上传时内容嗅探得到的类型（image/png、image/jpeg、image/webp、image/gif 之一） |
| `screen.file_size` | integer（int64） | 是 | 文件字节数 |
| `screen.width` | integer | 是 | 原图宽像素；0 表示未知 |
| `screen.height` | integer | 是 | 原图高像素；0 表示未知 |
| `screen.uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `screen.uploaded_at` | string（date-time） | 是 | 当前文件的上传时间；覆盖上传即更新，列表按它倒序 |
| `screen.created_at` | string（date-time） | 是 |  |
| `screen.content_url` | string | 是 | /api/screenshot-screens/{sid}/content?v=<uploaded_at 纳秒值>；v 随文件变化（覆盖上传即换 URL，浏览器缓存按完整 URL 记键），前端自行追加 &token= |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 413 | 请求体超过大小限制（裸 413，无 JSON body） |
| 500 | 服务器内部错误（50000） |

### PATCH `/api/screenshot-screens/{sid}`

<!-- operationId: updateScreenshotScreen -->

更新截图标题/平台/语言/亮度（仅 JWT）

四字段均为可选指针字段：字段出现才更新（三个维度允许空串清除为未标注），都不传返回 40001。
platform 非法长度、locale 非法 BCP 47、theme 非 light|dark 返回 40001；
改出的 (screen_key, platform, locale, theme) 组合与项目内已有截图撞唯一约束返回 409（40921）。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sid` | string | 是 | 截图 ID（UUID） |

**请求体**

`application/json`，必填。

四字段均可选，出现才更新（三个维度允许空串清除为未标注）；都不传返回 40001；维度组合撞唯一约束返回 409（40921）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `title` | string（长度 ≤200） | 否 | 界面标题（≤200 字符）；空串回退显示 screen_key |
| `platform` | string（长度 ≤100） | 否 | 平台（开放词表，trim+小写，≤100 字符）；空串清除为未标注 |
| `locale` | string | 否 | 语言（BCP 47，服务端规范化大小写，非法值 40001）；空串清除为未标注 |
| `theme` | string（enum: light \| dark \| ""） | 否 | 亮度主题（严格 light\|dark，其他值 40001）；空串清除为未标注 |

**成功响应**

**200** 更新后的截图

`data`：

截图库中的一张截图：(project_id, screen_key, platform, locale, theme) 唯一，再上传即覆盖文件

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `screen_key` | string | 是 | 服务端规范化（ToLower+TrimSpace）后的界面标识 |
| `title` | string | 是 | 可为空串，客户端回退显示 screen_key |
| `platform` | string（可空） | 是 | 平台（开放词表，trim+小写）；null 表示未标注 |
| `locale` | string（可空） | 是 | 语言（BCP 47 规范化大小写）；null 表示未标注 |
| `theme` | string（enum: light \| dark，可空） | 是 | 亮度主题；null 表示未标注 |
| `mime_type` | string | 是 | 上传时内容嗅探得到的类型（image/png、image/jpeg、image/webp、image/gif 之一） |
| `file_size` | integer（int64） | 是 | 文件字节数 |
| `width` | integer | 是 | 原图宽像素；0 表示未知 |
| `height` | integer | 是 | 原图高像素；0 表示未知 |
| `uploaded_by` | string | 是 | 用户名或 "API Key: <name>" |
| `uploaded_at` | string（date-time） | 是 | 当前文件的上传时间；覆盖上传即更新，列表按它倒序 |
| `created_at` | string（date-time） | 是 |  |
| `content_url` | string | 是 | /api/screenshot-screens/{sid}/content?v=<uploaded_at 纳秒值>；v 随文件变化（覆盖上传即换 URL，浏览器缓存按完整 URL 记键），前端自行追加 &token= |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 409 | 业务冲突（40900-40999） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/screenshot-screens/{sid}`

<!-- operationId: deleteScreenshotScreen -->

删除截图及其文件与全部标注

支持 Authorization 头携带 JWT 或 API Key。删除为硬删除：级联删除该截图上的全部标注（FK CASCADE），并在事务提交后清理磁盘文件；截图不存在或属于其他用户时一律按不存在处理（40413）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sid` | string | 是 | 截图 ID（UUID） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/screenshot-screens/{sid}/content`

<!-- operationId: getScreenshotScreenContent -->

获取截图图片内容

返回图片二进制内容（存储时嗅探的 mime type，`Content-Disposition: inline`，`Cache-Control: private, max-age=300`——缓存键是含 `?v=` 修订参数的完整 URL，覆盖上传推进 v 即自然失效）。支持 Authorization 头或 `?token=` query 凭证（供 `<img>` 直链）。同一路径另注册 HEAD 变体，语义同 GET 但不返回 body。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sid` | string | 是 | 截图 ID（UUID） |

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

### HEAD `/api/screenshot-screens/{sid}/content`

<!-- operationId: getScreenshotScreenContentHead -->

获取截图图片头信息（HEAD）

与 GET 语义相同但不返回 body。

**鉴权**：JWT+API Key，支持 ?token=

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sid` | string | 是 | 截图 ID（UUID） |

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

列出项目全部截图标注（可按状态 / 过期 / 维度 / Issue / 截图过滤）

返回项目下截图标注，按创建先后升序。
可选 query 过滤：`status`（open/resolved，缺省返回全部；非法值 40001）、`outdated`（true/false）、
`issue_id`、`screen_id`（精确匹配）、`platform`/`locale`/`theme`（逗号分隔多值，OR 语义；
值 `__none__` 表示「未标注」桶即截图维度为 NULL，可与其他值混用），全部可组合。
每条标注返回：原图地址（image_url，即所在截图的 content_url）、比例坐标（x/y/width/height，0~1）、
像素坐标（pixel_rect；原图尺寸未知时为 null）、文字、状态、是否过期（outdated，截图被覆盖更新后未确认）、
所属截图（screen_key/screen_title/screen_platform/screen_locale/screen_theme）、
关联 Issue（issue_id/issue_reference/issue_title，未关联均为 null）与裁剪图地址（crop_url）。
删除截图会级联删除其上标注；删除 Issue 只解除关联不删标注。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `status` | string（enum: open \| resolved） | 否 | 按状态过滤；缺省返回全部状态。open=未解决，resolved=已解决 |
| `outdated` | boolean | 否 | 按过期标记过滤；true=仅过期（待确认），false=仅未过期 |
| `issue_id` | string | 否 | 只返回关联该 Issue 的标注（UUID） |
| `screen_id` | string | 否 | 只返回该截图上的标注 |
| `platform` | string | 否 | 逗号分隔多值；`__none__` 表示未标注（NULL） |
| `locale` | string | 否 | 逗号分隔多值；`__none__` 表示未标注（NULL） |
| `theme` | string | 否 | 逗号分隔多值（light/dark）；`__none__` 表示未标注（NULL） |

**成功响应**

**200** 标注列表（data 为 {items}，非分页信封）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].screen_id` | string | 是 | 所属截图 ID |
| `items[].issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `items[].issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `items[].issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `items[].screen_key` | string | 是 |  |
| `items[].screen_title` | string | 是 | 截图显示名；空串时客户端回退展示 screen_key |
| `items[].screen_platform` | string（可空） | 是 | 所属截图的平台；null 表示未标注 |
| `items[].screen_locale` | string（可空） | 是 | 所属截图的语言；null 表示未标注 |
| `items[].screen_theme` | string（可空） | 是 | 所属截图的亮度主题；null 表示未标注 |
| `items[].image_url` | string | 是 | 所在截图的原图地址（content_url，带 ?v= 修订参数）；追加 &token= 供 <img> |
| `items[].image_width` | integer | 是 | 原图宽像素；0 表示未知 |
| `items[].image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `items[].outdated` | boolean | 是 | 截图被覆盖更新后仍未「确认保留」；resolved 标注恒为 false |
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
| `items[].crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop?v=<所属截图 uploaded_at 纳秒值>；统一输出 PNG，v 随覆盖上传变化（浏览器缓存按完整 URL 记键），前端自行追加 &token= |
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

### POST `/api/projects/{id}/screenshot-annotations/batch`

<!-- operationId: batchScreenshotAnnotations -->

批量处理当前过滤范围内的标注（仅 JWT）

对 `filter` 圈定的标注集执行同一动作。`action=confirm`（确认保留：清除 outdated，
仅作用于 open 标注）或 `action=resolve`（标记已解决：置 status=resolved 并记录 resolved_at，
仅作用于 open 标注）。filter 字段均可选，缺省即不限制；返回实际更新的标注数。
API Key 调用返回 403（40301）——API Key 只能逐条把标注置 resolved，没有批量能力。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`application/json`，必填。

对 filter 圈定的标注集执行批量动作；filter 缺省字段即不限制

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `action` | string（enum: confirm \| resolve） | 是 | confirm=确认保留（清除 outdated）；resolve=标记已解决；两者都只作用于 open 标注 |
| `filter` | object | 否 |  |
| `filter.platform` | string[] | 否 | 平台多值；含 `__none__` 表示「未标注」（NULL） |
| `filter.locale` | string[] | 否 | 语言多值；含 `__none__` 表示「未标注」（NULL） |
| `filter.theme` | string[]（enum: light \| dark \| __none__） | 否 | 亮度多值；`__none__` 表示「未标注」（NULL） |
| `filter.status` | string（enum: open \| resolved） | 否 | open=未解决，resolved=已解决 |
| `filter.outdated` | boolean（可空） | 否 | 按过期标记过滤；缺省不限制 |
| `filter.screen_id` | string | 否 | 只作用于该截图上的标注 |

**成功响应**

**200** 批量处理结果

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `updated` | integer | 是 | 实际更新的标注数 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/screenshot-screens/{sid}/annotations`

<!-- operationId: createScreenshotAnnotation -->

在截图上新建矩形标注（仅 JWT）

在指定截图上创建「矩形框 + 文字」标注。坐标为图片宽高比例（0~1）：
x/y 为左上角，width/height 为尺寸；须满足 0 ≤ x,y、0 < width,height、x+width ≤ 1、y+height ≤ 1，否则 40001。
`issue_id` 可选：缺省或空串表示不关联；提供时须为同一项目内的 Issue（不存在或跨项目返回 40001）。
API Key 调用返回 403（40301）。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `sid` | string | 是 | 截图 ID（UUID） |

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

挂在某张截图上的「矩形框 + 文字」标注；坐标以图片宽高比例（0~1）存储

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `screen_id` | string | 是 | 所属截图 ID |
| `issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `screen_key` | string | 是 |  |
| `screen_title` | string | 是 | 截图显示名；空串时客户端回退展示 screen_key |
| `screen_platform` | string（可空） | 是 | 所属截图的平台；null 表示未标注 |
| `screen_locale` | string（可空） | 是 | 所属截图的语言；null 表示未标注 |
| `screen_theme` | string（可空） | 是 | 所属截图的亮度主题；null 表示未标注 |
| `image_url` | string | 是 | 所在截图的原图地址（content_url，带 ?v= 修订参数）；追加 &token= 供 <img> |
| `image_width` | integer | 是 | 原图宽像素；0 表示未知 |
| `image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `outdated` | boolean | 是 | 截图被覆盖更新后仍未「确认保留」；resolved 标注恒为 false |
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
| `crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop?v=<所属截图 uploaded_at 纳秒值>；统一输出 PNG，v 随覆盖上传变化（浏览器缓存按完整 URL 记键），前端自行追加 &token= |
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

挂在某张截图上的「矩形框 + 文字」标注；坐标以图片宽高比例（0~1）存储

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `project_id` | string | 是 |  |
| `screen_id` | string | 是 | 所属截图 ID |
| `issue_id` | string（可空） | 是 | 关联的本项目 Issue ID；未关联为 null。删除 Issue 只解除关联不删标注 |
| `issue_reference` | string（可空） | 是 | 关联 Issue 的短编号（INT-n / GH-n）；未关联为 null |
| `issue_title` | string（可空） | 是 | 关联 Issue 的标题；未关联为 null |
| `screen_key` | string | 是 |  |
| `screen_title` | string | 是 | 截图显示名；空串时客户端回退展示 screen_key |
| `screen_platform` | string（可空） | 是 | 所属截图的平台；null 表示未标注 |
| `screen_locale` | string（可空） | 是 | 所属截图的语言；null 表示未标注 |
| `screen_theme` | string（可空） | 是 | 所属截图的亮度主题；null 表示未标注 |
| `image_url` | string | 是 | 所在截图的原图地址（content_url，带 ?v= 修订参数）；追加 &token= 供 <img> |
| `image_width` | integer | 是 | 原图宽像素；0 表示未知 |
| `image_height` | integer | 是 | 原图高像素；0 表示未知 |
| `outdated` | boolean | 是 | 截图被覆盖更新后仍未「确认保留」；resolved 标注恒为 false |
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
| `crop_url` | string | 是 | /api/screenshot-annotations/{aid}/crop?v=<所属截图 uploaded_at 纳秒值>；统一输出 PNG，v 随覆盖上传变化（浏览器缓存按完整 URL 记键），前端自行追加 &token= |
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

T3 环境连接与 T3 客户端操作（配对、线程读取/发消息/停止/队列、审批与提问应答、图片附件；仅 JWT，INT-82/INT-85）。例外：t3-projects 发现与项目 t3-bindings 对 API Key 开放（INT-89）

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

### GET `/api/environments/t3-projects`

<!-- operationId: listT3EnvironmentProjects -->

列出各环境的 T3 项目（绑定用只读发现）

逐环境实时拉取 T3 项目表（WS shell 快照 projects，活跃 + 归档去重合并）：
多环境有界并行、各自超时预算；单环境失败只标在该环境的
`status`/`error` 上，不影响其他环境的结果。响应不含凭证字段。
与项目的 `t3-bindings` 读写端点一起，是仅有的对 API Key 开放的
T3 面（INT-89）；线程读写、配对、附件等其余 T3 端点仍仅 JWT。

**鉴权**：JWT+API Key

**成功响应**

**200** 各环境及其 T3 项目列表

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].environment_id` | string | 是 |  |
| `items[].name` | string | 是 | 环境显示名 |
| `items[].status` | string（enum: ok \| unreachable \| repair \| degraded） | 是 | 探测状态：ok=Bearer 会话有效；unreachable=网络不可达/超时/5xx； repair=可达但凭证失效（Bearer 过期或被吊销），需重新配对； degraded=可达且凭证有效，但部分数据读取失败（如归档线程快照）， 其余数据仍返回 |
| `items[].access` | string（enum: read-only \| operate） | 是 | 会话能力：仅当精确持有 `orchestration:operate` scope 时为 `operate`， 否则 `read-only`。唯一判定依据是精确 token 的 scope 列； `read-only` 环境调用任何写入口返回 40300（重新配对后可操作） |
| `items[].error` | string | 否 | 该环境发现失败的说明；成功时省略 |
| `items[].projects` | object[] | 是 |  |
| `items[].projects[].id` | string | 是 | T3 projectId |
| `items[].projects[].title` | string | 是 |  |
| `items[].projects[].workspace_root` | string | 否 | 项目主检出根目录；上游缺省时省略（此类项目不可绑定） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 内部错误（50000） |

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
| `thread.pull_request_count` | integer | 是 | 关联 PR 数：shell.pullRequests 字段存在时（含空数组与全墓碑）恒为 pull_requests 过滤后的可见条数（已排除墓碑条目），两侧同源；仅字段缺失（旧服务端）时按 linkedPullRequest/branchPullRequest 计 |
| `thread.run_status` | string | 是 | T3 原始线程状态（shell.status）原样透传，不做归一化。与归一化后的 status 互补：completed/failed/cancelled/interrupted 等终态在 status 里都会折叠成 idle，这里保留上游原文，供区分「正常结束」「失败」「被取消」。 注意 status 不是 idle 时线程还没结束（后台 run/后台工作仍在），此时 run_status 只是最近一次根 run 的状态；只有 status=idle 时它才代表最终结局 |
| `thread.pull_requests` | object[] | 是 | 线程关联的 PR 可见明细（来自 shell.pullRequests，已排除 source=stack-dismissed 的墓碑条目——用户取消关联的栈成员，上游仅为同步去重保留）；无可见 PR 时为空数组。旧服务端只有 linkedPullRequest/branchPullRequest 而无明细，此时也是空数组（不合成缺 state 的条目），条目数看 pull_request_count |
| `thread.pull_requests[].repo` | string | 是 | 仓库标识（host + repository，如 github.com/owner/repo） |
| `thread.pull_requests[].number` | integer | 是 | PR 号 |
| `thread.pull_requests[].url` | string | 是 | PR 页面链接 |
| `thread.pull_requests[].watching` | boolean | 是 | 该 PR 是否挂着 T3 监听（有监听时服务端会在检查结束、他人评论或分支冲突时唤醒线程代理） |
| `thread.pull_requests[].state` | string | 否 | PR 状态（open/closed/merged）；snapshot 未同步时省略 |
| `thread.pull_requests[].merged_at` | string | 否 | 合并时间（ISO-8601）；未合并或 snapshot 未同步时省略 |
| `thread.pull_requests[].is_draft` | boolean | 否 | 是否草稿 PR；snapshot 未同步时省略 |
| `thread.pull_requests[].checks_state` | string | 否 | 检查状态（passing/failing/pending）；无检查数据或 snapshot 未同步时省略 |
| `thread.pull_requests[].mergeability` | string | 否 | 可合并性（mergeable/conflicting/unknown）；snapshot 未同步时省略 |
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
| `items[].pull_request_count` | integer | 是 | 关联 PR 数：shell.pullRequests 字段存在时（含空数组与全墓碑）恒为 pull_requests 过滤后的可见条数（已排除墓碑条目），两侧同源；仅字段缺失（旧服务端）时按 linkedPullRequest/branchPullRequest 计 |
| `items[].run_status` | string | 是 | T3 原始线程状态（shell.status）原样透传，不做归一化。与归一化后的 status 互补：completed/failed/cancelled/interrupted 等终态在 status 里都会折叠成 idle，这里保留上游原文，供区分「正常结束」「失败」「被取消」。 注意 status 不是 idle 时线程还没结束（后台 run/后台工作仍在），此时 run_status 只是最近一次根 run 的状态；只有 status=idle 时它才代表最终结局 |
| `items[].pull_requests` | object[] | 是 | 线程关联的 PR 可见明细（来自 shell.pullRequests，已排除 source=stack-dismissed 的墓碑条目——用户取消关联的栈成员，上游仅为同步去重保留）；无可见 PR 时为空数组。旧服务端只有 linkedPullRequest/branchPullRequest 而无明细，此时也是空数组（不合成缺 state 的条目），条目数看 pull_request_count |
| `items[].pull_requests[].repo` | string | 是 | 仓库标识（host + repository，如 github.com/owner/repo） |
| `items[].pull_requests[].number` | integer | 是 | PR 号 |
| `items[].pull_requests[].url` | string | 是 | PR 页面链接 |
| `items[].pull_requests[].watching` | boolean | 是 | 该 PR 是否挂着 T3 监听（有监听时服务端会在检查结束、他人评论或分支冲突时唤醒线程代理） |
| `items[].pull_requests[].state` | string | 否 | PR 状态（open/closed/merged）；snapshot 未同步时省略 |
| `items[].pull_requests[].merged_at` | string | 否 | 合并时间（ISO-8601）；未合并或 snapshot 未同步时省略 |
| `items[].pull_requests[].is_draft` | boolean | 否 | 是否草稿 PR；snapshot 未同步时省略 |
| `items[].pull_requests[].checks_state` | string | 否 | 检查状态（passing/failing/pending）；无检查数据或 snapshot 未同步时省略 |
| `items[].pull_requests[].mergeability` | string | 否 | 可合并性（mergeable/conflicting/unknown）；snapshot 未同步时省略 |
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
| `runtime_mode` | string（enum: approval-required \| auto-accept-edits \| auto \| full-access，默认 full-access） | 否 |  |
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

### GET `/api/projects/{id}/t3-bindings`

<!-- operationId: listProjectT3Bindings -->

列出项目的 T3 绑定（附环境名与探测状态）

返回该 Fast Ship 项目在各 T3 环境下的 T3 项目绑定快照；
`environment_name`/`environment_status` 取当前环境记录与实时探测结果
（与 GET /api/environments 同口径）。快照字段与环境当前事实不一致
即失效绑定，需重新绑定。JWT 与 API Key 均可访问（INT-89）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 项目的绑定列表

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].environment_id` | string | 是 |  |
| `items[].environment_name` | string | 是 | 环境显示名（当前记录值） |
| `items[].environment_status` | string（enum: ok \| unreachable \| repair \| degraded） | 是 | 探测状态：ok=Bearer 会话有效；unreachable=网络不可达/超时/5xx； repair=可达但凭证失效（Bearer 过期或被吊销），需重新配对； degraded=可达且凭证有效，但部分数据读取失败（如归档线程快照）， 其余数据仍返回 |
| `items[].t3_project_id` | string | 是 | 该环境中的 T3 项目 ID |
| `items[].t3_project_title` | string | 是 | 绑定时快照 |
| `items[].workspace_root` | string | 是 | 绑定时快照；启动线程据此解析 cwd |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 项目不存在（40401） |
| 500 | 内部错误（50000） |

### PUT `/api/projects/{id}/t3-bindings/{environmentId}`

<!-- operationId: upsertProjectT3Binding -->

绑定 T3 项目（实时校验后写入快照）

实时到目标环境确认 `t3_project_id` 存在后才写入绑定快照
（`t3_project_title`/`workspace_root` 取绑定成功那一刻的上游值），
不保存未校验的绑定：T3 项目不在该环境返回 40401，环境不存在 40417，
环境不可达/凭证失效/上游错误返回 50202。同一 `(project_id,
environment_id)` 重复 PUT 覆盖快照。JWT 与 API Key 均可访问（INT-89）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |
| `environmentId` | string | 是 | T3 环境 ID（UUID，归属当前用户；跨用户一律 40417） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `t3_project_id` | string（长度 ≥1） | 是 | 该环境中的 T3 项目 ID（来自 GET /api/environments/t3-projects 的 projects[].id） |

**成功响应**

**200** 绑定成功，返回写入的快照

`data`：

Fast Ship 项目在某 T3 环境绑定的 T3 项目（INT-89）。t3_project_title /
workspace_root 是绑定成功那一刻的快照：环境不可达时仍可展示；
快照与最新发现不一致即失效绑定，需重新绑定

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `environment_id` | string | 是 |  |
| `environment_name` | string | 是 | 环境显示名（当前记录值） |
| `environment_status` | string（enum: ok \| unreachable \| repair \| degraded） | 是 | 探测状态：ok=Bearer 会话有效；unreachable=网络不可达/超时/5xx； repair=可达但凭证失效（Bearer 过期或被吊销），需重新配对； degraded=可达且凭证有效，但部分数据读取失败（如归档线程快照）， 其余数据仍返回 |
| `t3_project_id` | string | 是 | 该环境中的 T3 项目 ID |
| `t3_project_title` | string | 是 | 绑定时快照 |
| `workspace_root` | string | 是 | 绑定时快照；启动线程据此解析 cwd |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数校验失败（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 项目不存在（40401）/ 环境不存在（40417）/ T3 项目不在该环境（40401） |
| 502 | 环境不可达、凭证失效或 T3 上游失败（50202） |

### DELETE `/api/projects/{id}/t3-bindings/{environmentId}`

<!-- operationId: deleteProjectT3Binding -->

解绑 T3 项目

删除该环境下的绑定；已解绑时重复 DELETE 返回 200。JWT 与 API Key 均可访问（INT-89）。

**鉴权**：JWT+API Key

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |
| `environmentId` | string | 是 | T3 环境 ID（UUID，归属当前用户；跨用户一律 40417） |

**成功响应**

**200** 删除成功

`data`：any

响应负载；形状随端点而变，由 allOf 组合的兄弟 schema 收窄

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 项目不存在（40401） |

## reviews

PR 审查（INT-135，仅 JWT）：项目级审查设置、审查总览与审查记录查询

### GET `/api/reviews`

<!-- operationId: listReviewProjects -->

列出当前用户的审查总览

返回当前用户所有已关联 GitHub 仓库的项目。同仓库已被其他项目开启审查时，
该行 `taken_by` 返回占用者登录名且 `can_manage=false`。

**鉴权**：仅 JWT

**成功响应**

**200** 审查总览

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].project_name` | string | 是 |  |
| `items[].github_owner` | string | 是 | 实际审查的代码仓库 owner（未单独配置代码仓库时即公开仓库） |
| `items[].github_repo` | string | 是 | 实际审查的代码仓库 repo 名（未单独配置代码仓库时即公开仓库） |
| `items[].enabled` | boolean | 是 |  |
| `items[].code_combination_count` | integer | 是 |  |
| `items[].visual_combination_count` | integer | 是 |  |
| `items[].code_status` | string（可空） | 否 | 最近一次代码审查结论（success/failure/error/pending） |
| `items[].visual_status` | string（可空） | 否 |  |
| `items[].last_review_at` | string（可空） | 否 |  |
| `items[].taken_by` | string（可空） | 否 |  |
| `items[].can_manage` | boolean | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### GET `/api/reviews/pull-requests`

<!-- operationId: listReviewPullRequests -->

跨项目 PR 审查动态

以当前用户已开启审查的项目（`enabled=true`）的总账行为范围，服务端算好
分组（进行中 / 需要处理 / 最近通过）、阶段进度、发现计数与待处理原因。
`counts`/`total` 是过滤范围内全量 PR 的统计，`items` 按 `offset`/`limit`
切片。动作未勾选导致新 head 没有任何运行的 PR 不返回；通过且最后一轮
完成时间早于 7 天前的 PR 不返回。

**鉴权**：仅 JWT

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `project_id` | string | 否 | 项目 ID（UUID，可选；必须属于当前用户，否则 40401） |
| `limit` | integer（1..200，默认 50） | 否 | 返回的 PR 数量上限 |
| `offset` | integer（≥0，默认 0） | 否 | 分页偏移 |

**成功响应**

**200** PR 审查动态

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].project_id` | string | 是 |  |
| `items[].project_name` | string | 是 |  |
| `items[].repo_full_name` | string | 是 |  |
| `items[].pr_number` | integer | 是 |  |
| `items[].pr_title` | string | 是 |  |
| `items[].pr_url` | string | 是 |  |
| `items[].group` | string（enum: in_progress \| needs_action \| recently_passed） | 是 |  |
| `items[].stage` | string（enum: debouncing \| queued \| reviewing，可空） | 否 | 仅 group=in_progress 时有值 |
| `items[].debounce_fire_at` | string（可空） | 否 | 防抖到点时刻（仅 stage=debouncing 时有值） |
| `items[].round_number` | integer | 是 |  |
| `items[].head_sha` | string | 是 |  |
| `items[].updated_at` | string | 是 |  |
| `items[].started_at` | string | 是 | 本轮开始时刻（防抖中为防抖行更新时刻） |
| `items[].stages` | object[] | 是 | 两段进度（代码/视觉）；防抖中为空数组 |
| `items[].stages[].kind` | string（enum: code \| visual） | 是 |  |
| `items[].stages[].total` | integer | 是 | 该段组合总数（无运行时为设置行配置数） |
| `items[].stages[].completed` | integer | 是 | 已完成的组合数 |
| `items[].stages[].status` | string（enum: pending \| queued \| running \| completed \| failed \| skipped） | 是 | skipped=该段未启动（无 UI 改动/截图未变化/代码发现问题） |
| `items[].findings` | object | 是 | 发现计数（缺省 0；代码 P0/P1/P2，视觉 blocker/worth_fixing） |
| `items[].findings.P0` | integer | 是 |  |
| `items[].findings.P1` | integer | 是 |  |
| `items[].findings.P2` | integer | 是 |  |
| `items[].findings.blocker` | integer | 是 |  |
| `items[].findings.worth_fixing` | integer | 是 |  |
| `items[].issues` | object[] | 是 | 需要人处理的问题（失败的运行、无法解析的结论、账本兜底） |
| `items[].issues[].kind` | string（enum: code \| visual） | 是 |  |
| `items[].issues[].combination` | string（可空） | 否 | <model> · <instance_id>；账本兜底的 issue 无组合 |
| `items[].issues[].reason` | string | 是 | 短原因（派发失败/超时/审查失败/审查未通过/审查未完成/结论无法解析） |
| `items[].issues[].message` | string | 是 | 面向用户的说明 |
| `items[].can_rerun` | boolean | 是 |  |
| `counts` | object | 是 | 过滤范围内全部 PR 的分组计数（与分页无关） |
| `counts.in_progress` | integer | 是 |  |
| `counts.needs_action` | integer | 是 |  |
| `counts.recently_passed` | integer | 是 |  |
| `total` | integer | 是 | 过滤范围内全部 PR 数（分页前的总数） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/reviews/pull-requests/{prNumber}`

<!-- operationId: getReviewPullRequestDetail -->

单个 PR 的审查详情（轮次 → 各组合运行 → 发现列表）

与项目审查记录同结构，另带每轮 `trigger_action` 与每运行的完整
`findings`/`unmeasurable`。项目必须属于当前用户（40401）；项目有设置行
且按设置行仓库匹配的账本行存在，否则 40434。不要求项目已开启审查
（停用项目仍可查看历史详情）。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `prNumber` | integer（≥1） | 是 | PR 编号 |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `project_id` | string | 是 | 项目 ID（UUID，必填） |

**成功响应**

**200** PR 审查详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `repo_full_name` | string | 是 |  |
| `pr_number` | integer | 是 |  |
| `pr_title` | string | 是 |  |
| `pr_url` | string | 是 |  |
| `head_sha` | string | 是 |  |
| `can_rerun` | boolean | 是 | 该记录是否属于项目当前仓库；跨仓库的历史记录只能查看，不能重跑。 |
| `code_status` | string（可空） | 否 |  |
| `visual_status` | string（可空） | 否 |  |
| `updated_at` | string | 是 |  |
| `rounds` | object[] | 是 |  |
| `rounds[].round_number` | integer | 是 |  |
| `rounds[].head_sha` | string | 是 |  |
| `rounds[].trigger_action` | string（enum: opened \| synchronize \| ready_for_review \| reopened \| manual \| visual \| unknown） | 是 | 触发方式（该轮第一条代码 run 的 trigger_action；没有代码 run 时为 unknown） |
| `rounds[].created_at` | string | 是 |  |
| `rounds[].code_runs` | object[] | 是 |  |
| `rounds[].code_runs[].id` | string | 是 |  |
| `rounds[].code_runs[].kind` | string（enum: code \| visual） | 是 |  |
| `rounds[].code_runs[].environment_id` | string | 是 |  |
| `rounds[].code_runs[].environment_name` | string | 否 |  |
| `rounds[].code_runs[].instance_id` | string | 是 |  |
| `rounds[].code_runs[].model` | string | 是 |  |
| `rounds[].code_runs[].options` | object[] | 否 |  |
| `rounds[].code_runs[].options[].id` | string | 是 |  |
| `rounds[].code_runs[].options[].value` | any | 是 | 选项值（字符串或布尔） |
| `rounds[].code_runs[].status` | string（enum: queued \| dispatched \| running \| completed \| failed \| timed_out \| cancelled） | 是 |  |
| `rounds[].code_runs[].thread_id` | string | 是 |  |
| `rounds[].code_runs[].summary` | string | 否 |  |
| `rounds[].code_runs[].error_code` | string | 否 |  |
| `rounds[].code_runs[].error_message` | string | 否 |  |
| `rounds[].code_runs[].findings_count` | integer | 否 | P0/P1/P2（代码）或 blocker/worth fixing（视觉）的条目数 |
| `rounds[].code_runs[].findings` | object[] | 否 | 完整发现列表（result_json 可解析时才有）；代码含 Note，视觉为 severity→level |
| `rounds[].code_runs[].findings[].level` | string | 是 | 代码为 P0/P1/P2/Note；视觉为 blocker/worth_fixing/minor |
| `rounds[].code_runs[].findings[].body` | string | 是 |  |
| `rounds[].code_runs[].findings[].file` | string | 否 | 代码发现涉及的文件（文件:行号 展示） |
| `rounds[].code_runs[].findings[].line` | integer（≥1） | 否 | 当前 HEAD 右侧代码行号 |
| `rounds[].code_runs[].findings[].dimension` | string | 否 | 视觉发现的设计维度 |
| `rounds[].code_runs[].findings[].fix` | string | 否 | 视觉发现的修复建议 |
| `rounds[].code_runs[].findings[].screenshot` | string | 否 | 视觉发现涉及的截图标签 |
| `rounds[].code_runs[].unmeasurable` | object[] | 否 | 视觉审查「无法判断」的截图项（仅视觉运行） |
| `rounds[].code_runs[].unmeasurable[].screenshot` | string | 是 |  |
| `rounds[].code_runs[].unmeasurable[].reason` | string | 是 |  |
| `rounds[].code_runs[].ui_changed` | boolean | 否 |  |
| `rounds[].code_runs[].worktree_state` | string（可空） | 否 |  |
| `rounds[].code_runs[].started_at` | string（可空） | 否 |  |
| `rounds[].code_runs[].finished_at` | string（可空） | 否 |  |
| `rounds[].visual_runs` | object[] | 是 |  |
| `rounds[].visual_runs[].id` | string | 是 |  |
| `rounds[].visual_runs[].kind` | string（enum: code \| visual） | 是 |  |
| `rounds[].visual_runs[].environment_id` | string | 是 |  |
| `rounds[].visual_runs[].environment_name` | string | 否 |  |
| `rounds[].visual_runs[].instance_id` | string | 是 |  |
| `rounds[].visual_runs[].model` | string | 是 |  |
| `rounds[].visual_runs[].options` | object[] | 否 |  |
| `rounds[].visual_runs[].options[].id` | string | 是 |  |
| `rounds[].visual_runs[].options[].value` | any | 是 | 选项值（字符串或布尔） |
| `rounds[].visual_runs[].status` | string（enum: queued \| dispatched \| running \| completed \| failed \| timed_out \| cancelled） | 是 |  |
| `rounds[].visual_runs[].thread_id` | string | 是 |  |
| `rounds[].visual_runs[].summary` | string | 否 |  |
| `rounds[].visual_runs[].error_code` | string | 否 |  |
| `rounds[].visual_runs[].error_message` | string | 否 |  |
| `rounds[].visual_runs[].findings_count` | integer | 否 | P0/P1/P2（代码）或 blocker/worth fixing（视觉）的条目数 |
| `rounds[].visual_runs[].findings` | object[] | 否 | 完整发现列表（result_json 可解析时才有）；代码含 Note，视觉为 severity→level |
| `rounds[].visual_runs[].findings[].level` | string | 是 | 代码为 P0/P1/P2/Note；视觉为 blocker/worth_fixing/minor |
| `rounds[].visual_runs[].findings[].body` | string | 是 |  |
| `rounds[].visual_runs[].findings[].file` | string | 否 | 代码发现涉及的文件（文件:行号 展示） |
| `rounds[].visual_runs[].findings[].line` | integer（≥1） | 否 | 当前 HEAD 右侧代码行号 |
| `rounds[].visual_runs[].findings[].dimension` | string | 否 | 视觉发现的设计维度 |
| `rounds[].visual_runs[].findings[].fix` | string | 否 | 视觉发现的修复建议 |
| `rounds[].visual_runs[].findings[].screenshot` | string | 否 | 视觉发现涉及的截图标签 |
| `rounds[].visual_runs[].unmeasurable` | object[] | 否 | 视觉审查「无法判断」的截图项（仅视觉运行） |
| `rounds[].visual_runs[].unmeasurable[].screenshot` | string | 是 |  |
| `rounds[].visual_runs[].unmeasurable[].reason` | string | 是 |  |
| `rounds[].visual_runs[].ui_changed` | boolean | 否 |  |
| `rounds[].visual_runs[].worktree_state` | string（可空） | 否 |  |
| `rounds[].visual_runs[].started_at` | string（可空） | 否 |  |
| `rounds[].visual_runs[].finished_at` | string（可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 项目不存在（40401）或审查 PR 记录不存在（40434） |
| 500 | 服务器内部错误（50000） |

### GET `/api/projects/{id}/review-settings`

<!-- operationId: getProjectReviewSettings -->

读取项目审查设置

`prerequisites` 逐条列出开启审查的前置条件，`missing=true` 的项不允许开启。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**成功响应**

**200** 审查设置

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `project_id` | string | 是 |  |
| `repo_full_name` | string | 是 | 该审查实际使用的代码仓库（owner/repo，原样大小写；未单独配置代码仓库时即公开仓库） |
| `enabled` | boolean | 是 |  |
| `trigger_opened` | boolean | 是 |  |
| `trigger_synchronize` | boolean | 是 |  |
| `trigger_ready_for_review` | boolean | 是 |  |
| `trigger_reopened` | boolean | 是 |  |
| `code_combinations` | object[]（≤5 项） | 是 |  |
| `code_combinations[].environment_id` | string | 是 |  |
| `code_combinations[].instance_id` | string | 是 | T3 provider 实例 id（harness 维度） |
| `code_combinations[].model` | string | 是 |  |
| `code_combinations[].options` | object[] | 否 | 模型选项（如思考强度），canonical `[{id,value}]` 形式，原样交给 T3 |
| `code_combinations[].options[].id` | string | 是 |  |
| `code_combinations[].options[].value` | any | 是 | 选项值（字符串或布尔） |
| `code_combinations[].environment_name` | string | 否 | 仅响应；环境当前名称 |
| `code_combinations[].t3_project_id` | string | 否 | 仅响应；绑定的 T3 项目 id（保存时快照） |
| `visual_combinations` | object[]（≤3 项） | 是 |  |
| `visual_combinations[].environment_id` | string | 是 |  |
| `visual_combinations[].instance_id` | string | 是 | T3 provider 实例 id（harness 维度） |
| `visual_combinations[].model` | string | 是 |  |
| `visual_combinations[].options` | object[] | 否 | 模型选项（如思考强度），canonical `[{id,value}]` 形式，原样交给 T3 |
| `visual_combinations[].options[].id` | string | 是 |  |
| `visual_combinations[].options[].value` | any | 是 | 选项值（字符串或布尔） |
| `visual_combinations[].environment_name` | string | 否 | 仅响应；环境当前名称 |
| `visual_combinations[].t3_project_id` | string | 否 | 仅响应；绑定的 T3 项目 id（保存时快照） |
| `prerequisites` | object[] | 是 |  |
| `prerequisites[].code` | string（enum: github_app \| github_app_installation \| public_url \| github_token \| environment_binding） | 是 |  |
| `prerequisites[].label` | string | 是 |  |
| `prerequisites[].missing` | boolean | 是 |  |
| `prerequisites[].detail` | string | 否 |  |
| `taken_by` | string（可空） | 否 | 同仓库已开启审查的其他项目占用者登录名（只读） |
| `can_manage` | boolean | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/projects/{id}/review-settings`

<!-- operationId: updateProjectReviewSettings -->

保存项目审查设置（全量替换）

启用时必须满足全部前置条件、绑定 Token、所选环境属于当前用户且已绑定该项目，
代码审查组合 1–5 个、视觉审查组合 0–3 个，同一组合不得重复。
同一仓库已被其他项目开启时返回 40922。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `enabled` | boolean | 是 |  |
| `trigger_opened` | boolean | 是 |  |
| `trigger_synchronize` | boolean | 是 |  |
| `trigger_ready_for_review` | boolean | 是 |  |
| `trigger_reopened` | boolean | 是 |  |
| `code_combinations` | object[]（≤5 项） | 是 |  |
| `code_combinations[].environment_id` | string | 是 |  |
| `code_combinations[].instance_id` | string | 是 | T3 provider 实例 id（harness 维度） |
| `code_combinations[].model` | string | 是 |  |
| `code_combinations[].options` | object[] | 否 | 模型选项（如思考强度），canonical `[{id,value}]` 形式，原样交给 T3 |
| `code_combinations[].options[].id` | string | 是 |  |
| `code_combinations[].options[].value` | any | 是 | 选项值（字符串或布尔） |
| `code_combinations[].environment_name` | string | 否 | 仅响应；环境当前名称 |
| `code_combinations[].t3_project_id` | string | 否 | 仅响应；绑定的 T3 项目 id（保存时快照） |
| `visual_combinations` | object[]（≤3 项） | 是 |  |
| `visual_combinations[].environment_id` | string | 是 |  |
| `visual_combinations[].instance_id` | string | 是 | T3 provider 实例 id（harness 维度） |
| `visual_combinations[].model` | string | 是 |  |
| `visual_combinations[].options` | object[] | 否 | 模型选项（如思考强度），canonical `[{id,value}]` 形式，原样交给 T3 |
| `visual_combinations[].options[].id` | string | 是 |  |
| `visual_combinations[].options[].value` | any | 是 | 选项值（字符串或布尔） |
| `visual_combinations[].environment_name` | string | 否 | 仅响应；环境当前名称 |
| `visual_combinations[].t3_project_id` | string | 否 | 仅响应；绑定的 T3 项目 id（保存时快照） |

**成功响应**

**200** 保存后的设置

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `project_id` | string | 是 |  |
| `repo_full_name` | string | 是 | 该审查实际使用的代码仓库（owner/repo，原样大小写；未单独配置代码仓库时即公开仓库） |
| `enabled` | boolean | 是 |  |
| `trigger_opened` | boolean | 是 |  |
| `trigger_synchronize` | boolean | 是 |  |
| `trigger_ready_for_review` | boolean | 是 |  |
| `trigger_reopened` | boolean | 是 |  |
| `code_combinations` | object[]（≤5 项） | 是 |  |
| `code_combinations[].environment_id` | string | 是 |  |
| `code_combinations[].instance_id` | string | 是 | T3 provider 实例 id（harness 维度） |
| `code_combinations[].model` | string | 是 |  |
| `code_combinations[].options` | object[] | 否 | 模型选项（如思考强度），canonical `[{id,value}]` 形式，原样交给 T3 |
| `code_combinations[].options[].id` | string | 是 |  |
| `code_combinations[].options[].value` | any | 是 | 选项值（字符串或布尔） |
| `code_combinations[].environment_name` | string | 否 | 仅响应；环境当前名称 |
| `code_combinations[].t3_project_id` | string | 否 | 仅响应；绑定的 T3 项目 id（保存时快照） |
| `visual_combinations` | object[]（≤3 项） | 是 |  |
| `visual_combinations[].environment_id` | string | 是 |  |
| `visual_combinations[].instance_id` | string | 是 | T3 provider 实例 id（harness 维度） |
| `visual_combinations[].model` | string | 是 |  |
| `visual_combinations[].options` | object[] | 否 | 模型选项（如思考强度），canonical `[{id,value}]` 形式，原样交给 T3 |
| `visual_combinations[].options[].id` | string | 是 |  |
| `visual_combinations[].options[].value` | any | 是 | 选项值（字符串或布尔） |
| `visual_combinations[].environment_name` | string | 否 | 仅响应；环境当前名称 |
| `visual_combinations[].t3_project_id` | string | 否 | 仅响应；绑定的 T3 项目 id（保存时快照） |
| `prerequisites` | object[] | 是 |  |
| `prerequisites[].code` | string（enum: github_app \| github_app_installation \| public_url \| github_token \| environment_binding） | 是 |  |
| `prerequisites[].label` | string | 是 |  |
| `prerequisites[].missing` | boolean | 是 |  |
| `prerequisites[].detail` | string | 否 |  |
| `taken_by` | string（可空） | 否 | 同仓库已开启审查的其他项目占用者登录名（只读） |
| `can_manage` | boolean | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 409 | 同一仓库已被其他项目开启（40922） |
| 412 | 前置条件缺失（41202），message 内含逐条缺项 |
| 500 | 服务器内部错误（50000） |

### GET `/api/projects/{id}/review-records`

<!-- operationId: listProjectReviewRecords -->

列出项目审查记录

按最近更新时间倒序列出 PR，每个 PR 内嵌轮次与各组合运行（可跳 T3 线程）。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `limit` | integer（1..100，默认 20） | 否 | 返回的 PR 数量上限 |

**成功响应**

**200** 审查记录

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].repo_full_name` | string | 是 |  |
| `items[].pr_number` | integer | 是 |  |
| `items[].pr_title` | string | 是 |  |
| `items[].pr_url` | string | 是 |  |
| `items[].head_sha` | string | 是 |  |
| `items[].can_rerun` | boolean | 是 | 该记录是否属于项目当前仓库；跨仓库的历史记录只能查看，不能重跑。 |
| `items[].code_status` | string（可空） | 否 |  |
| `items[].visual_status` | string（可空） | 否 |  |
| `items[].updated_at` | string | 是 |  |
| `items[].rounds` | object[] | 是 |  |
| `items[].rounds[].round_number` | integer | 是 |  |
| `items[].rounds[].head_sha` | string | 是 |  |
| `items[].rounds[].trigger_action` | string（enum: opened \| synchronize \| ready_for_review \| reopened \| manual \| visual \| unknown） | 是 | 触发方式（该轮第一条代码 run 的 trigger_action；没有代码 run 时为 unknown） |
| `items[].rounds[].created_at` | string | 是 |  |
| `items[].rounds[].code_runs` | object[] | 是 |  |
| `items[].rounds[].code_runs[].id` | string | 是 |  |
| `items[].rounds[].code_runs[].kind` | string（enum: code \| visual） | 是 |  |
| `items[].rounds[].code_runs[].environment_id` | string | 是 |  |
| `items[].rounds[].code_runs[].environment_name` | string | 否 |  |
| `items[].rounds[].code_runs[].instance_id` | string | 是 |  |
| `items[].rounds[].code_runs[].model` | string | 是 |  |
| `items[].rounds[].code_runs[].options` | object[] | 否 |  |
| `items[].rounds[].code_runs[].options[].id` | string | 是 |  |
| `items[].rounds[].code_runs[].options[].value` | any | 是 | 选项值（字符串或布尔） |
| `items[].rounds[].code_runs[].status` | string（enum: queued \| dispatched \| running \| completed \| failed \| timed_out \| cancelled） | 是 |  |
| `items[].rounds[].code_runs[].thread_id` | string | 是 |  |
| `items[].rounds[].code_runs[].summary` | string | 否 |  |
| `items[].rounds[].code_runs[].error_code` | string | 否 |  |
| `items[].rounds[].code_runs[].error_message` | string | 否 |  |
| `items[].rounds[].code_runs[].findings_count` | integer | 否 | P0/P1/P2（代码）或 blocker/worth fixing（视觉）的条目数 |
| `items[].rounds[].code_runs[].findings` | object[] | 否 | 完整发现列表（result_json 可解析时才有）；代码含 Note，视觉为 severity→level |
| `items[].rounds[].code_runs[].findings[].level` | string | 是 | 代码为 P0/P1/P2/Note；视觉为 blocker/worth_fixing/minor |
| `items[].rounds[].code_runs[].findings[].body` | string | 是 |  |
| `items[].rounds[].code_runs[].findings[].file` | string | 否 | 代码发现涉及的文件（文件:行号 展示） |
| `items[].rounds[].code_runs[].findings[].line` | integer（≥1） | 否 | 当前 HEAD 右侧代码行号 |
| `items[].rounds[].code_runs[].findings[].dimension` | string | 否 | 视觉发现的设计维度 |
| `items[].rounds[].code_runs[].findings[].fix` | string | 否 | 视觉发现的修复建议 |
| `items[].rounds[].code_runs[].findings[].screenshot` | string | 否 | 视觉发现涉及的截图标签 |
| `items[].rounds[].code_runs[].unmeasurable` | object[] | 否 | 视觉审查「无法判断」的截图项（仅视觉运行） |
| `items[].rounds[].code_runs[].unmeasurable[].screenshot` | string | 是 |  |
| `items[].rounds[].code_runs[].unmeasurable[].reason` | string | 是 |  |
| `items[].rounds[].code_runs[].ui_changed` | boolean | 否 |  |
| `items[].rounds[].code_runs[].worktree_state` | string（可空） | 否 |  |
| `items[].rounds[].code_runs[].started_at` | string（可空） | 否 |  |
| `items[].rounds[].code_runs[].finished_at` | string（可空） | 否 |  |
| `items[].rounds[].visual_runs` | object[] | 是 |  |
| `items[].rounds[].visual_runs[].id` | string | 是 |  |
| `items[].rounds[].visual_runs[].kind` | string（enum: code \| visual） | 是 |  |
| `items[].rounds[].visual_runs[].environment_id` | string | 是 |  |
| `items[].rounds[].visual_runs[].environment_name` | string | 否 |  |
| `items[].rounds[].visual_runs[].instance_id` | string | 是 |  |
| `items[].rounds[].visual_runs[].model` | string | 是 |  |
| `items[].rounds[].visual_runs[].options` | object[] | 否 |  |
| `items[].rounds[].visual_runs[].options[].id` | string | 是 |  |
| `items[].rounds[].visual_runs[].options[].value` | any | 是 | 选项值（字符串或布尔） |
| `items[].rounds[].visual_runs[].status` | string（enum: queued \| dispatched \| running \| completed \| failed \| timed_out \| cancelled） | 是 |  |
| `items[].rounds[].visual_runs[].thread_id` | string | 是 |  |
| `items[].rounds[].visual_runs[].summary` | string | 否 |  |
| `items[].rounds[].visual_runs[].error_code` | string | 否 |  |
| `items[].rounds[].visual_runs[].error_message` | string | 否 |  |
| `items[].rounds[].visual_runs[].findings_count` | integer | 否 | P0/P1/P2（代码）或 blocker/worth fixing（视觉）的条目数 |
| `items[].rounds[].visual_runs[].findings` | object[] | 否 | 完整发现列表（result_json 可解析时才有）；代码含 Note，视觉为 severity→level |
| `items[].rounds[].visual_runs[].findings[].level` | string | 是 | 代码为 P0/P1/P2/Note；视觉为 blocker/worth_fixing/minor |
| `items[].rounds[].visual_runs[].findings[].body` | string | 是 |  |
| `items[].rounds[].visual_runs[].findings[].file` | string | 否 | 代码发现涉及的文件（文件:行号 展示） |
| `items[].rounds[].visual_runs[].findings[].line` | integer（≥1） | 否 | 当前 HEAD 右侧代码行号 |
| `items[].rounds[].visual_runs[].findings[].dimension` | string | 否 | 视觉发现的设计维度 |
| `items[].rounds[].visual_runs[].findings[].fix` | string | 否 | 视觉发现的修复建议 |
| `items[].rounds[].visual_runs[].findings[].screenshot` | string | 否 | 视觉发现涉及的截图标签 |
| `items[].rounds[].visual_runs[].unmeasurable` | object[] | 否 | 视觉审查「无法判断」的截图项（仅视觉运行） |
| `items[].rounds[].visual_runs[].unmeasurable[].screenshot` | string | 是 |  |
| `items[].rounds[].visual_runs[].unmeasurable[].reason` | string | 是 |  |
| `items[].rounds[].visual_runs[].ui_changed` | boolean | 否 |  |
| `items[].rounds[].visual_runs[].worktree_state` | string（可空） | 否 |  |
| `items[].rounds[].visual_runs[].started_at` | string（可空） | 否 |  |
| `items[].rounds[].visual_runs[].finished_at` | string（可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/projects/{id}/reviews/rerun`

<!-- operationId: rerunProjectReview -->

对 PR 的实时 HEAD 重跑代码审查

用 GitHub App 令牌实时取 PR 当前 head；旧 SHA 未终态运行一律取消。
项目未开启审查时返回 40923。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 项目 ID（UUID） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `pr_number` | integer（≥1） | 是 |  |
| `repo_full_name` | string | 否 | 记录所属仓库（审查记录里回传）。项目仓库改过之后，历史记录会属于 旧仓库；带上它可避免把同号的无关 PR 当成本项目的 PR 重跑。 与项目当前仓库不一致时返回 400。 |

**成功响应**

**200** 已受理

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `accepted` | boolean | 是 |  |
| `head_sha` | string | 是 |  |
| `round_number` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 409 | 项目未开启审查（40923） |
| 500 | 服务器内部错误（50000） |

## notifications

站内通知（只用于审查系统故障：派发失败、超时、工作区回收失败；仅 JWT）

### GET `/api/notifications`

<!-- operationId: listNotifications -->

列出当前用户的通知

**鉴权**：仅 JWT

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `limit` | integer（1..100，默认 50） | 否 |  |
| `unread_only` | boolean（默认 false） | 否 |  |

**成功响应**

**200** 通知列表（按更新时间倒序）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].kind` | string（enum: review_dispatch_failed \| review_run_timeout \| review_cleanup_failed \| review_redelivery_failed） | 是 |  |
| `items[].title` | string | 是 |  |
| `items[].body` | string | 是 |  |
| `items[].environment_id` | string（可空） | 否 |  |
| `items[].thread_id` | string（可空） | 否 |  |
| `items[].run_id` | string（可空） | 否 |  |
| `items[].created_at` | string | 是 |  |
| `items[].updated_at` | string | 是 |  |
| `items[].last_seen_at` | string | 是 |  |
| `items[].read_at` | string（可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### GET `/api/notifications/unread-count`

<!-- operationId: getUnreadNotificationCount -->

未读通知数

**鉴权**：仅 JWT

**成功响应**

**200** 未读数量

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `count` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### POST `/api/notifications/{id}/read`

<!-- operationId: markNotificationRead -->

标记单条通知已读

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 通知 ID（UUID，归属当前用户，跨用户一律 404） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/notifications/read-all`

<!-- operationId: markAllNotificationsRead -->

全部通知标记已读

**鉴权**：仅 JWT

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

## review-agent

审查运行令牌专用的 Agent 读取与结论提交端点（仅 runTokenAuth，INT-135）

### POST `/api/github/webhooks`

<!-- operationId: receiveGitHubWebhook -->

接收 GitHub App webhook（pull_request opened/synchronize/ready_for_review/reopened）

公开端点（无 JWT），以 `X-Hub-Signature-256`（HMAC-SHA256）按部署唯一的
webhook 密钥验签。验签密钥取部署级 GitHub App 身份（INT-129：每套部署
一个 App，没有用户级候选集）——以 `deployment_settings` 行内的密钥为准，
行为空即视为未配置、不回落 config；仅当部署没有数据库 App 身份时用
`github_app.webhook_secret` 兜底。密钥为空 → 503（50300）拒收；签名缺失
或不匹配 → 401（40107）。只处理 pull_request 的 opened/synchronize/ready_for_review/reopened；
draft 与 fork 一律 202 忽略。仓库必须已与本部署的 Fast Ship 项目关联
才会进入防抖/开轮。同一 `X-GitHub-Delivery` 幂等（重复投递返回 202 且
不再开轮）。

**鉴权**：公开

**Header 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `X-GitHub-Event` | string | 是 |  |
| `X-GitHub-Delivery` | string | 是 |  |
| `X-Hub-Signature-256` | string | 是 |  |

**请求体**

`application/json`，必填。

GitHub 官方 pull_request 事件 payload（原样验签，服务端只取 owner/repo、PR 号、head SHA、draft/fork 标记与 installation id）

**成功响应**

**202** 已受理（含幂等重复、draft/fork、未关联仓库等忽略场景）

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 签名缺失或不匹配（40107） |
| 500 | 服务器内部错误（50000） |
| 503 | 部署未配置 webhook 验签密钥（50300），拒绝接受请求 |

### GET `/api/review-agent/pull-request`

<!-- operationId: reviewAgentPullRequest -->

本运行审查的 PR 元数据

只接受运行令牌；PR 身份全部取自运行行，不接受请求参数。

**鉴权**：runTokenAuth

**成功响应**

**200** PR 元数据

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `repo_full_name` | string | 是 |  |
| `pr_number` | integer | 是 |  |
| `head_sha` | string | 是 |  |
| `title` | string | 是 |  |
| `body` | string | 是 |  |
| `html_url` | string | 是 |  |
| `base_ref` | string | 是 |  |
| `head_ref` | string | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### GET `/api/review-agent/pull-request/diff`

<!-- operationId: reviewAgentPullRequestDiff -->

本运行的 PR diff

超过 1MB 截断并标记 truncated。

**鉴权**：runTokenAuth

**成功响应**

**200** diff

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `content` | string | 是 |  |
| `truncated` | boolean | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### GET `/api/review-agent/history`

<!-- operationId: reviewAgentHistory -->

历史处置上下文（系统生成 Markdown）

代码审查：上一轮各组合的结构化发现 + 上一轮结论之后的新评论/行内回复；首次审查给全部讨论。
视觉审查：上一轮视觉发现与 unmeasurable。只识别本系统自己发的结论。

**鉴权**：runTokenAuth

**成功响应**

**200** 历史上下文

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `content` | string | 是 | Markdown 文本；没有历史时为空串 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### GET `/api/review-agent/screenshots`

<!-- operationId: reviewAgentScreenshots -->

视觉审查截图清单

只有视觉审查运行可调用；图片本体走下面的图片端点。

**鉴权**：runTokenAuth

**成功响应**

**200** 截图清单

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].label` | string | 是 |  |
| `items[].url` | string | 是 | 指向本端点的图片路径（相对 server.public_url）；下载失败时为空串 |
| `items[].error` | string（可空） | 否 | 下载/不可用原因；非空时 url 为空，审查员应记入 unmeasurable |
| `changed` | boolean | 是 | 截图集合相对上一轮视觉审查是否有变化 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### GET `/api/review-agent/screenshots/{assetId}`

<!-- operationId: reviewAgentScreenshotImage -->

截图图片本体

按运行所属 PR/SHA 校验；返回原始字节与 Content-Type。

**鉴权**：runTokenAuth

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `assetId` | string | 是 |  |

**成功响应**

**200** 图片字节

二进制内容（image/gif、image/jpeg、image/png、image/webp），无 JSON 信封。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 资源不存在（40400-40499） |

### POST `/api/review-agent/result`

<!-- operationId: reviewAgentSubmitResult -->

提交结构化审查结论

请求体按运行类型二选一：代码审查 `ReviewCodeResult`、视觉审查 `ReviewVisualResult`（字段校验失败返回 400，
提示修正重交；PR HEAD 与运行绑定 SHA 不一致返回 40920 并作废本轮）。

**鉴权**：runTokenAuth

**请求体**

`application/json`，必填。

二选一，各分支结构如下：

方案 1

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `summary` | string | 是 |  |
| `findings` | object[] | 是 |  |
| `findings[].level` | string（enum: P0 \| P1 \| P2 \| Note） | 是 |  |
| `findings[].file` | string | 是 |  |
| `findings[].line` | integer（≥1，可空） | 否 | 当前 HEAD 右侧代码行号；可为空 |
| `findings[].body` | string | 是 |  |
| `prior` | object[] | 是 |  |
| `prior[].level` | string | 否 | 原发现的分级（P0/P1/P2/Note 或 blocker/worth_fixing/minor） |
| `prior[].file` | string（可空） | 否 |  |
| `prior[].line` | integer（可空） | 否 |  |
| `prior[].body` | string | 是 | 原发现的正文摘要 |
| `prior[].status` | string（enum: fixed \| rejected \| open） | 是 |  |
| `prior[].reason` | string | 否 | rejected 时必须写明理由 |
| `ui_changed` | boolean | 是 |  |
| `ui_changed_since_last_visual` | boolean | 是 |  |

方案 2

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `summary` | string | 是 |  |
| `findings` | object[] | 是 |  |
| `findings[].severity` | string（enum: blocker \| worth_fixing \| minor） | 是 |  |
| `findings[].screenshot` | string | 是 | 截图标签（来自 screenshots 端点的 label） |
| `findings[].dimension` | string（enum: hierarchy \| spacing \| typography \| color \| interaction \| copy \| accessibility） | 是 |  |
| `findings[].body` | string | 是 |  |
| `findings[].fix` | string | 是 |  |
| `prior` | object[] | 是 |  |
| `prior[].level` | string | 否 | 原发现的分级（P0/P1/P2/Note 或 blocker/worth_fixing/minor） |
| `prior[].file` | string（可空） | 否 |  |
| `prior[].line` | integer（可空） | 否 |  |
| `prior[].body` | string | 是 | 原发现的正文摘要 |
| `prior[].status` | string（enum: fixed \| rejected \| open） | 是 |  |
| `prior[].reason` | string | 否 | rejected 时必须写明理由 |
| `unmeasurable` | object[] | 是 |  |
| `unmeasurable[].screenshot` | string | 是 |  |
| `unmeasurable[].reason` | string | 是 |  |

**成功响应**

**200** 已受理

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `accepted` | boolean | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 409 | PR HEAD 已变化（40920），本轮作废 |
| 500 | 服务器内部错误（50000） |

## self-logs

Fast Ship 自身日志查询（只读，INT-94）：slog 结构化采集、独立 SQLite 日志库存储，JWT 与 fsk_ API Key 可查本人归属及显式系统行（无主行默认不可见）；供代理排查 Fast Ship 自身问题

### GET `/api/self-logs`

<!-- operationId: listSelfLogs -->

分页查询 Fast Ship 自身日志（slog 采集，独立日志库）

只读查询 Fast Ship 服务端自身的结构化日志（INT-94，与 /projects/:id/logs 的业务日志无关）。
可见范围：当前用户归属的日志（user_id 等于调用者），以及显式标记的系统行（无业务关联列的进程级启动日志）。
无归属且未标系统的行（解析失败、键冲突、漏标）对任何认证用户均不可见。请求路径日志均带 user_id。
传入他人 user_id 过滤只会得到空结果。游标为键集分页（时间+ID），插入不影响翻页稳定性；
next_cursor 为 null 表示没有更多。

**鉴权**：JWT+API Key

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `start_time` | string（date-time） | 否 | 起始时间（含），RFC3339 |
| `end_time` | string（date-time） | 否 | 结束时间（含），RFC3339 |
| `level` | string（enum: DEBUG \| INFO \| WARN \| ERROR） | 否 | 精确级别过滤 |
| `min_level` | string（enum: DEBUG \| INFO \| WARN \| ERROR） | 否 | 不低于该级别的过滤（与 level 互斥，同时提供时以 level 为准） |
| `modules` | string | 否 | 模块过滤，逗号分隔多值（如 `http,review`）；模块取值见条目 module 字段 |
| `request_id` | string | 否 | 按请求 ID 过滤（响应头 X-Request-Id 的值） |
| `user_id` | string | 否 | 按归属用户过滤；仅限本人，他人 ID 只会得到空结果 |
| `project_id` | string | 否 | 按关联项目 ID 过滤 |
| `issue_id` | string | 否 | 按关联 Issue ID 过滤 |
| `run_id` | string | 否 | 按关联审查运行 ID 过滤（review_runs.id） |
| `thread_id` | string | 否 | 按关联 T3 线程 ID 过滤 |
| `extra_eq` | string | 否 | 附加字段等值过滤，可重复，格式 `key=value`（json_extract 后 CAST 为文本再比较，数字值如 status=200 也可匹配） |
| `keyword` | string | 否 | 关键词包含过滤（匹配 message 与 extra） |
| `exclude_keyword` | string | 否 | 关键词排除过滤（同 keyword 口径） |
| `cursor` | string | 否 | 分页游标；取上一页响应的 next_cursor |
| `limit` | integer（1..500，默认 100） | 否 | 单页条数；越界（<1 或 >500）回落为 100 |
| `order` | string（enum: asc \| desc，默认 desc） | 否 | 按时间排序方向；游标内含方向，翻页时不允许改变 |
| `fields` | string | 否 | 返回字段选择，逗号分隔（如 `module,message,extra`）；id/time/level/message 始终返回，其余未选字段在条目中省略。未知字段名返回 40001 |

**成功响应**

**200** 日志分页列表（含下一页游标提示）

`data`：

自身日志分页（键集游标；next_cursor 为 null 表示没有更多）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | integer（int64） | 是 | 存储行 ID；与时间联合构成游标定位 |
| `items[].time` | string（date-time） | 是 |  |
| `items[].level` | string（enum: DEBUG \| INFO \| WARN \| ERROR） | 是 |  |
| `items[].module` | string | 否 | 产生日志的模块（http / review / t3 / github / auth / api_key 等） |
| `items[].message` | string | 是 |  |
| `items[].request_id` | string | 否 |  |
| `items[].user_id` | string | 否 |  |
| `items[].project_id` | string | 否 |  |
| `items[].issue_id` | string | 否 |  |
| `items[].run_id` | string | 否 |  |
| `items[].thread_id` | string | 否 |  |
| `items[].extra` | object | 否 | 其余结构化字段（JSON 对象）；未选入 fields 时省略 |
| `next_cursor` | string（可空） | 是 | 下一页游标；没有更多时为 null |
| `has_more` | boolean | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 凭证类型不允许（40301：fsr_ 运行令牌/刷新令牌无权查询自身日志） |
| 500 | 服务器内部错误（50000） |

### GET `/api/self-logs/aggregate`

<!-- operationId: aggregateSelfLogs -->

按模块/级别/时间桶聚合统计自身日志条数

过滤条件与 GET /api/self-logs 完全一致，对命中条数按 `by` 维度组合分组计数；
`by` 可多选（逗号分隔）；含 time 时 bucket 缺省为 minute（minute/hour）。
可见范围与列表端点相同（本人归属 + 显式系统行；无主行不可见）。按时间桶聚合时保留最近 2000 个桶（超出 truncated=true）。

**鉴权**：JWT+API Key

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `by` | string | 是 | 聚合维度，逗号分隔组合，取值 module / level / time（如 `by=module,level`） |
| `bucket` | string（enum: minute \| hour，默认 minute） | 否 | 时间桶粒度；仅 by 含 time 时生效 |
| `start_time` | string（date-time） | 否 | 起始时间（含），RFC3339 |
| `end_time` | string（date-time） | 否 | 结束时间（含），RFC3339 |
| `level` | string（enum: DEBUG \| INFO \| WARN \| ERROR） | 否 | 精确级别过滤 |
| `min_level` | string（enum: DEBUG \| INFO \| WARN \| ERROR） | 否 | 不低于该级别的过滤（与 level 互斥，同时提供时以 level 为准） |
| `modules` | string | 否 | 模块过滤，逗号分隔多值（如 `http,review`）；模块取值见条目 module 字段 |
| `request_id` | string | 否 | 按请求 ID 过滤（响应头 X-Request-Id 的值） |
| `user_id` | string | 否 | 按归属用户过滤；仅限本人，他人 ID 只会得到空结果 |
| `project_id` | string | 否 | 按关联项目 ID 过滤 |
| `issue_id` | string | 否 | 按关联 Issue ID 过滤 |
| `run_id` | string | 否 | 按关联审查运行 ID 过滤（review_runs.id） |
| `thread_id` | string | 否 | 按关联 T3 线程 ID 过滤 |
| `extra_eq` | string | 否 | 附加字段等值过滤，可重复，格式 `key=value`（json_extract 后 CAST 为文本再比较，数字值如 status=200 也可匹配） |
| `keyword` | string | 否 | 关键词包含过滤（匹配 message 与 extra） |
| `exclude_keyword` | string | 否 | 关键词排除过滤（同 keyword 口径） |

**成功响应**

**200** 聚合行（行数上限 2000；按桶时间倒序截断，truncated=true 表示还有更早的桶）

`data`：

自身日志聚合结果

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `rows` | object[] | 是 |  |
| `rows[].count` | integer | 是 |  |
| `rows[].module` | string | 否 |  |
| `rows[].level` | string | 否 |  |
| `rows[].time_bucket` | string（date-time） | 否 | 时间桶起点（对齐分钟/小时），RFC3339 |
| `truncated` | boolean | 是 | 超过单次聚合上限（2000 行）被截断；按时间桶时保留的是最近的桶 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 凭证类型不允许（40301） |
| 500 | 服务器内部错误（50000） |

### GET `/api/self-logs/trace`

<!-- operationId: traceSelfLogs -->

按请求 ID 或 run ID 查询整条链路日志

返回该请求/运行从开始到结束的全部日志（时间正序）。可见范围与列表端点相同（本人归属 + 显式系统行；无主行不可见）。
request_id 与 run_id 必须二选一；同请求的响应头 X-Request-Id 即查询键，
失败请求（panic/5xx）的条目携带堆栈。上限 1000 条；超出时保留最新 1000 条（时间正序），truncated=true。

**鉴权**：JWT+API Key

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `request_id` | string | 否 | 按请求 ID 过滤（响应头 X-Request-Id 的值） |
| `run_id` | string | 否 | 按关联审查运行 ID 过滤（review_runs.id） |
| `fields` | string | 否 | 返回字段选择，逗号分隔（如 `module,message,extra`）；id/time/level/message 始终返回，其余未选字段在条目中省略。未知字段名返回 40001 |

**成功响应**

**200** 链路日志（时间正序；截断时 truncated=true）

`data`：

请求/运行链路日志（时间正序）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | integer（int64） | 是 | 存储行 ID；与时间联合构成游标定位 |
| `items[].time` | string（date-time） | 是 |  |
| `items[].level` | string（enum: DEBUG \| INFO \| WARN \| ERROR） | 是 |  |
| `items[].module` | string | 否 | 产生日志的模块（http / review / t3 / github / auth / api_key 等） |
| `items[].message` | string | 是 |  |
| `items[].request_id` | string | 否 |  |
| `items[].user_id` | string | 否 |  |
| `items[].project_id` | string | 否 |  |
| `items[].issue_id` | string | 否 |  |
| `items[].run_id` | string | 否 |  |
| `items[].thread_id` | string | 否 |  |
| `items[].extra` | object | 否 | 其余结构化字段（JSON 对象）；未选入 fields 时省略 |
| `truncated` | boolean | 是 | 超过单次链路上限（1000 条）被截断；保留的是最新 1000 条 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 凭证类型不允许（40301） |
| 500 | 服务器内部错误（50000） |

## bots

内置机器人（智能体，INT-101，仅 JWT）：用户自带 OpenAI 兼容 key，机器人经白名单工具操作项目/Issue/已连接环境与线程。管理、主线消息、运行、调用、确认授权与 SSE 全部仅 JWT；key 全链路脱敏只回末四位。INT-133 起地址与密钥改存到 AI 提供商，bot 只存引用

### GET `/api/bots`

<!-- operationId: listBots -->

列出当前用户的机器人

**鉴权**：仅 JWT

**成功响应**

**200** 机器人列表

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].name` | string | 是 |  |
| `items[].persona_prompt` | string | 是 | 人设原文；平台规则在服务端拼接，不在此回显 |
| `items[].prompt_revision` | integer（int64） | 是 | 人设/平台规则版本；用户明确编辑人设时递增 |
| `items[].platform_prompt_version` | integer（int64） | 是 |  |
| `items[].toolset_version` | integer（int64） | 是 |  |
| `items[].provider_id` | string | 是 | 所属 AI 提供商（INT-133）；地址与密钥都在提供商上 |
| `items[].provider_name` | string | 是 | 提供商名称（展示用，派生自 provider） |
| `items[].catalog_id` | string | 是 | 提供商的目录 id（models.dev id 或 custom，派生自 provider） |
| `items[].base_url` | string | 是 | OpenAI 兼容 API 基址；INT-133 起为 provider 的派生值（提供商轮换后下一次读取生效） |
| `items[].model` | string | 是 |  |
| `items[].api_key_last4` | string | 是 | API Key 末四位（provider 的派生值）；明文任何端点都不回显 |
| `items[].context_window` | integer（int64） | 是 | 模型上下文窗口（未扣输出空间）。解析顺序 manual > detected > catalog > fallback；fallback 为部署默认窗口 |
| `items[].max_output_tokens` | integer（int64） | 是 | 模型输出上限；detected（探测缓存）与 catalog（内置目录）来源可给出，未知为 0 |
| `items[].context_source` | string | 是 | 窗口来源：manual（部署覆盖）/ detected（GET /models 探测缓存）/ catalog（内置 models.dev 目录，INT-133）/ fallback（默认窗口）； 展示层不得把回退值说成已检测 |
| `items[].context_detail` | string | 否 | 解析口径说明（探测方式或回退原因）；可省略 |
| `items[].next_event_seq` | integer（int64） | 是 | 事件账本水位（下一条的 seq）；详情订阅以此-1 为 afterSeq， 与详情快照衔接，不从 0 重放历史 |
| `items[].last_message` | object（可空） | 是 | 主线最近一条正文 trim 后非空的 user/assistant 消息预览（按 seq 最大取）； 正文纯空白或 content 坏 JSON 的消息跳过继续往前找，定时任务会话不参与。 没有符合条件消息时为 null。仅 GET /api/bots 列表端点返回真实值； 详情与聊天快照等其它端点恒为 null。机器人主线最近一条可见消息的预览（INT-125），机器人列表端点专用 |
| `items[].last_message.kind` | string | 是 | 消息角色 user / assistant |
| `items[].last_message.text` | string | 是 | 完整正文；服务端不截断，截断展示由前端负责 |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### POST `/api/bots`

<!-- operationId: createBot -->

创建机器人

provider_id 必填且属于当前用户（INT-133）：地址与密钥都在 AI 提供商上，
bot 只存引用，同一 provider 可被多个机器人共用。人设与平台规则在创建时
拼成系统提示词并生成 prompt_revision=1。同名机器人不做唯一约束。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string（长度 ≥1） | 是 |  |
| `persona_prompt` | string | 否 |  |
| `provider_id` | string | 是 | 必填且属于当前用户（40433）；地址与密钥都在提供商上 |
| `model` | string | 是 | 模型名，不带 provider 前缀 |

**成功响应**

**200** 创建成功

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `name` | string | 是 |  |
| `persona_prompt` | string | 是 | 人设原文；平台规则在服务端拼接，不在此回显 |
| `prompt_revision` | integer（int64） | 是 | 人设/平台规则版本；用户明确编辑人设时递增 |
| `platform_prompt_version` | integer（int64） | 是 |  |
| `toolset_version` | integer（int64） | 是 |  |
| `provider_id` | string | 是 | 所属 AI 提供商（INT-133）；地址与密钥都在提供商上 |
| `provider_name` | string | 是 | 提供商名称（展示用，派生自 provider） |
| `catalog_id` | string | 是 | 提供商的目录 id（models.dev id 或 custom，派生自 provider） |
| `base_url` | string | 是 | OpenAI 兼容 API 基址；INT-133 起为 provider 的派生值（提供商轮换后下一次读取生效） |
| `model` | string | 是 |  |
| `api_key_last4` | string | 是 | API Key 末四位（provider 的派生值）；明文任何端点都不回显 |
| `context_window` | integer（int64） | 是 | 模型上下文窗口（未扣输出空间）。解析顺序 manual > detected > catalog > fallback；fallback 为部署默认窗口 |
| `max_output_tokens` | integer（int64） | 是 | 模型输出上限；detected（探测缓存）与 catalog（内置目录）来源可给出，未知为 0 |
| `context_source` | string | 是 | 窗口来源：manual（部署覆盖）/ detected（GET /models 探测缓存）/ catalog（内置 models.dev 目录，INT-133）/ fallback（默认窗口）； 展示层不得把回退值说成已检测 |
| `context_detail` | string | 否 | 解析口径说明（探测方式或回退原因）；可省略 |
| `next_event_seq` | integer（int64） | 是 | 事件账本水位（下一条的 seq）；详情订阅以此-1 为 afterSeq， 与详情快照衔接，不从 0 重放历史 |
| `last_message` | object（可空） | 是 | 主线最近一条正文 trim 后非空的 user/assistant 消息预览（按 seq 最大取）； 正文纯空白或 content 坏 JSON 的消息跳过继续往前找，定时任务会话不参与。 没有符合条件消息时为 null。仅 GET /api/bots 列表端点返回真实值； 详情与聊天快照等其它端点恒为 null。机器人主线最近一条可见消息的预览（INT-125），机器人列表端点专用 |
| `last_message.kind` | string | 是 | 消息角色 user / assistant |
| `last_message.text` | string | 是 | 完整正文；服务端不截断，截断展示由前端负责 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### POST `/api/bots/test-connection`

<!-- operationId: testBotConnection -->

测试模型连接（流式与工具调用兼容性）

只做最小模型探测，不写入主线。地址与密钥一律取 provider_id 对应的
AI 提供商（INT-133），不接受请求体里的 base_url/api_key；model 显式
提供时优先，否则沿用 bot_id 对应机器人的已保存模型（两者都没有时 400）。
streaming 与 tool_call 两项检查分别返回；HTTP 200 且 ok=false 表示
配置可达性/兼容性问题。另同步探测 GET /models 的模型窗口元数据
（INT-115）：成功写入缓存并把 detected 值并入响应；失败回退当前解析值
（manual/catalog/默认窗口），不影响 ok。SDK 重试显式关零，不产生隐藏
重复调用。测试保存中的新密钥请用 POST /api/ai-providers/test。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

地址与密钥一律取 provider（INT-133）；model 显式提供时优先，否则沿用 bot_id 的已保存模型（两者都没有时 40001）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `provider_id` | string | 是 |  |
| `bot_id` | string | 否 | 可选；给出时 model 缺省沿用该机器人的已保存模型 |
| `model` | string | 否 |  |

**成功响应**

**200** 检查结果（ok=false 时检查项中带失败详情）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `ok` | boolean | 是 | 流式检查通过即 true |
| `checks` | object[] | 是 |  |
| `checks[].name` | string | 是 | streaming / tool_call |
| `checks[].passed` | boolean | 是 |  |
| `checks[].detail` | string | 否 | 失败原因（截断；不含密钥） |
| `context_window` | integer（int64） | 是 | 模型上下文窗口：GET /models 同步探测成功时为探测值并写入缓存； 失败回退到当前解析值（manual / catalog / 默认窗口） |
| `max_output_tokens` | integer（int64） | 是 | 模型输出上限；仅探测到时非 0 |
| `context_source` | string | 是 | 窗口来源 manual / detected / catalog / fallback（探测失败时如实标注） |
| `context_detail` | string | 否 | 解析口径说明；可省略 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}`

<!-- operationId: getBot -->

读取机器人配置

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**成功响应**

**200** 机器人配置

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `name` | string | 是 |  |
| `persona_prompt` | string | 是 | 人设原文；平台规则在服务端拼接，不在此回显 |
| `prompt_revision` | integer（int64） | 是 | 人设/平台规则版本；用户明确编辑人设时递增 |
| `platform_prompt_version` | integer（int64） | 是 |  |
| `toolset_version` | integer（int64） | 是 |  |
| `provider_id` | string | 是 | 所属 AI 提供商（INT-133）；地址与密钥都在提供商上 |
| `provider_name` | string | 是 | 提供商名称（展示用，派生自 provider） |
| `catalog_id` | string | 是 | 提供商的目录 id（models.dev id 或 custom，派生自 provider） |
| `base_url` | string | 是 | OpenAI 兼容 API 基址；INT-133 起为 provider 的派生值（提供商轮换后下一次读取生效） |
| `model` | string | 是 |  |
| `api_key_last4` | string | 是 | API Key 末四位（provider 的派生值）；明文任何端点都不回显 |
| `context_window` | integer（int64） | 是 | 模型上下文窗口（未扣输出空间）。解析顺序 manual > detected > catalog > fallback；fallback 为部署默认窗口 |
| `max_output_tokens` | integer（int64） | 是 | 模型输出上限；detected（探测缓存）与 catalog（内置目录）来源可给出，未知为 0 |
| `context_source` | string | 是 | 窗口来源：manual（部署覆盖）/ detected（GET /models 探测缓存）/ catalog（内置 models.dev 目录，INT-133）/ fallback（默认窗口）； 展示层不得把回退值说成已检测 |
| `context_detail` | string | 否 | 解析口径说明（探测方式或回退原因）；可省略 |
| `next_event_seq` | integer（int64） | 是 | 事件账本水位（下一条的 seq）；详情订阅以此-1 为 afterSeq， 与详情快照衔接，不从 0 重放历史 |
| `last_message` | object（可空） | 是 | 主线最近一条正文 trim 后非空的 user/assistant 消息预览（按 seq 最大取）； 正文纯空白或 content 坏 JSON 的消息跳过继续往前找，定时任务会话不参与。 没有符合条件消息时为 null。仅 GET /api/bots 列表端点返回真实值； 详情与聊天快照等其它端点恒为 null。机器人主线最近一条可见消息的预览（INT-125），机器人列表端点专用 |
| `last_message.kind` | string | 是 | 消息角色 user / assistant |
| `last_message.text` | string | 是 | 完整正文；服务端不截断，截断展示由前端负责 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/bots/{id}`

<!-- operationId: updateBot -->

更新机器人配置

指针字段表达「未提供保留」。provider_id 变更时新提供商必须属于当前用户，
否则 40433；地址与密钥随提供商（INT-133），轮换密钥请更新 provider。
人设变化重建系统提示词并递增 prompt_revision。配置变化对下个 run 生效，
当前 run 使用开始时的快照与已创建的客户端。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**请求体**

`application/json`，必填。

指针字段表达「未提供保留」；INT-133 起不再接受 base_url / api_key，密钥轮换经 AI 提供商端点

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string | 否 |  |
| `persona_prompt` | string | 否 |  |
| `provider_id` | string | 否 | 更换所属提供商；必须属于当前用户（40433） |
| `model` | string | 否 |  |

**成功响应**

**200** 更新后的机器人配置

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `name` | string | 是 |  |
| `persona_prompt` | string | 是 | 人设原文；平台规则在服务端拼接，不在此回显 |
| `prompt_revision` | integer（int64） | 是 | 人设/平台规则版本；用户明确编辑人设时递增 |
| `platform_prompt_version` | integer（int64） | 是 |  |
| `toolset_version` | integer（int64） | 是 |  |
| `provider_id` | string | 是 | 所属 AI 提供商（INT-133）；地址与密钥都在提供商上 |
| `provider_name` | string | 是 | 提供商名称（展示用，派生自 provider） |
| `catalog_id` | string | 是 | 提供商的目录 id（models.dev id 或 custom，派生自 provider） |
| `base_url` | string | 是 | OpenAI 兼容 API 基址；INT-133 起为 provider 的派生值（提供商轮换后下一次读取生效） |
| `model` | string | 是 |  |
| `api_key_last4` | string | 是 | API Key 末四位（provider 的派生值）；明文任何端点都不回显 |
| `context_window` | integer（int64） | 是 | 模型上下文窗口（未扣输出空间）。解析顺序 manual > detected > catalog > fallback；fallback 为部署默认窗口 |
| `max_output_tokens` | integer（int64） | 是 | 模型输出上限；detected（探测缓存）与 catalog（内置目录）来源可给出，未知为 0 |
| `context_source` | string | 是 | 窗口来源：manual（部署覆盖）/ detected（GET /models 探测缓存）/ catalog（内置 models.dev 目录，INT-133）/ fallback（默认窗口）； 展示层不得把回退值说成已检测 |
| `context_detail` | string | 否 | 解析口径说明（探测方式或回退原因）；可省略 |
| `next_event_seq` | integer（int64） | 是 | 事件账本水位（下一条的 seq）；详情订阅以此-1 为 afterSeq， 与详情快照衔接，不从 0 重放历史 |
| `last_message` | object（可空） | 是 | 主线最近一条正文 trim 后非空的 user/assistant 消息预览（按 seq 最大取）； 正文纯空白或 content 坏 JSON 的消息跳过继续往前找，定时任务会话不参与。 没有符合条件消息时为 null。仅 GET /api/bots 列表端点返回真实值； 详情与聊天快照等其它端点恒为 null。机器人主线最近一条可见消息的预览（INT-125），机器人列表端点专用 |
| `last_message.kind` | string | 是 | 消息角色 user / assistant |
| `last_message.text` | string | 是 | 完整正文；服务端不截断，截断展示由前端负责 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/bots/{id}`

<!-- operationId: deleteBot -->

删除机器人（连同对话账本级联删除）

存在活动 run 或未终结调用（pending_confirmation/queued/running）时返回
40927：先显式停止/取消，避免静默丢任务。

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
| 404 | 资源不存在（40400-40499） |
| 409 | 有活动 run 或未终结调用（40927） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/messages`

<!-- operationId: listBotMessages -->

主线消息分页

before_seq/after_seq 都是排他 seq 边界，页内按 seq 正序；不使用 offset 分页。
两个边界都省略时返回最新的 limit 条（聊天首屏加载尾部）；只给 before_seq
返回该边界之前最近的 limit 条（「加载更早」）；只给 after_seq 返回该边界
之后最早的 limit 条（断线补读）。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `before_seq` | integer（int64） | 否 | 只返回 seq 小于该值的消息；取距该边界最近的 limit 条（「加载更早」） |
| `after_seq` | integer（int64） | 否 | 只返回 seq 大于该值的消息；取距该边界最近的 limit 条（补读） |
| `limit` | integer（≤200，默认 100） | 否 |  |

**成功响应**

**200** 消息列表（含 total=本次返回条数）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].bot_id` | string | 是 |  |
| `items[].seq` | integer（int64） | 是 |  |
| `items[].run_id` | string | 否 |  |
| `items[].request_id` | string | 否 |  |
| `items[].call_id` | string | 否 |  |
| `items[].kind` | string | 是 | user / assistant / tool_reply / background_result / approval_decision / lifecycle |
| `items[].content` | object | 是 | 按 kind 变化的正文；assistant 含 text 与 tool_calls 数组；tool_reply 含协议回复字符串 |
| `items[].created_at` | string（date-time） | 是 |  |
| `calls` | object[] | 是 | 页内消息引用到的调用记录（assistant 嵌套 tool_calls 与非 assistant 消息顶层 call_id），保证旧消息渲染确认/警示卡不缺数据 |
| `calls[].id` | string | 是 | 平台调用 ID，同时是执行幂等键 |
| `calls[].bot_id` | string | 是 |  |
| `calls[].request_id` | string | 是 |  |
| `calls[].provider_tool_call_id` | string | 是 | 模型协议配对 ID |
| `calls[].tool_name` | string | 是 |  |
| `calls[].action` | string | 是 |  |
| `calls[].arguments` | object | 是 | 规范化后的参数 |
| `calls[].execution_mode` | string | 是 | sync / async |
| `calls[].requires_confirmation` | boolean | 是 |  |
| `calls[].status` | string | 是 | pending_confirmation / queued / running / succeeded / failed / rejected / cancelled / not_executed |
| `calls[].decision` | string（可空） | 否 | approve / reject / always_allow |
| `calls[].decided_at` | string（date-time，可空） | 否 |  |
| `calls[].authorization_id` | string（可空） | 否 |  |
| `calls[].execution_context` | object | 是 | 服务端生成的执行身份（T3 command/thread/message ID 等）与授权范围 |
| `calls[].policy` | object | 否 | 分级放行评估结论（INT-160）：{tier, rule_id?, auto, source, facts?, downgrade_reason?, expiry?, schedule_denied?}。凡经过策略评估的写 调用都有：自动执行（auto=true）、确认卡（含降级原因与过期时间）、 定时任务会话 tier3 同步拒绝三类；历史行为空即「策略上线前」 |
| `calls[].cancel_requested` | boolean | 否 |  |
| `calls[].result_preview` | object | 否 | 进入模型上下文的受限结果（完整结果走 result 端点） |
| `calls[].result_chars` | integer（int64） | 是 |  |
| `calls[].truncated` | boolean | 是 |  |
| `calls[].error_code` | string（可空） | 否 |  |
| `calls[].error_message` | string（可空） | 否 |  |
| `calls[].effect_may_have_occurred` | boolean | 是 | 副作用可能已发生（取消/中断/重启）；不声称回滚 |
| `calls[].created_at` | string（date-time） | 是 |  |
| `calls[].started_at` | string（date-time，可空） | 否 |  |
| `calls[].finished_at` | string（date-time，可空） | 否 |  |
| `total` | integer | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/bots/{id}/messages`

<!-- operationId: sendBotMessage -->

向主线发送用户消息

client_message_id 必填（幂等键）：同键同正文返回已有消息；同键不同正文
返回 40925。消息落库后由 runtime 唤醒/延续主线 run；响应返回已落库
消息、当前活动 run（可能尚未包含本消息）与 queued 标记。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `text` | string（长度 ≥1） | 是 |  |
| `client_message_id` | string（长度 ≥1） | 是 | 幂等键；同键同正文幂等返回，同键不同正文 40925 |

**成功响应**

**200** 已受理

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `message` | object | 是 | 不可改写的主线账本；批准/拒绝/错误/恢复都是追加新消息 |
| `message.id` | string | 是 |  |
| `message.bot_id` | string | 是 |  |
| `message.seq` | integer（int64） | 是 |  |
| `message.run_id` | string | 否 |  |
| `message.request_id` | string | 否 |  |
| `message.call_id` | string | 否 |  |
| `message.kind` | string | 是 | user / assistant / tool_reply / background_result / approval_decision / lifecycle |
| `message.content` | object | 是 | 按 kind 变化的正文；assistant 含 text 与 tool_calls 数组；tool_reply 含协议回复字符串 |
| `message.created_at` | string（date-time） | 是 |  |
| `run` | object（可空） | 是 | 当前活动 run（可能尚未包含本消息）；没有时为 null，本次发送会触发新 run |
| `run.id` | string | 是 |  |
| `run.bot_id` | string | 是 |  |
| `run.trigger_source` | string | 是 | user_message / tool_result / recovery / schedule / manual / schedule_event（定时任务运行事件写入主线后的立即唤醒，INT-138） |
| `run.trigger_key` | string | 是 |  |
| `run.status` | string | 是 | queued / running / completed / failed / stopped |
| `run.config_snapshot` | string | 是 | 本次模型/地址/提示词版本快照 JSON；不含明文 key |
| `run.input_through_seq` | integer（int64） | 是 |  |
| `run.stop_requested` | boolean | 是 |  |
| `run.prompt_tokens` | integer（int64，可空） | 否 |  |
| `run.completion_tokens` | integer（int64，可空） | 否 |  |
| `run.total_tokens` | integer（int64，可空） | 否 |  |
| `run.cached_tokens` | integer（int64，可空） | 否 |  |
| `run.usage_complete` | boolean | 是 | false 表示上游未报 usage（缺失与零严格区分） |
| `run.cost_estimate` | number（double，可空） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价/全缺 usage 时为 null（缺失 ≠ 0，INT-139） |
| `run.cost_source` | string（可空） | 否 | 计价来源（catalog）；cost_estimate 为 null 时同 null |
| `run.error_code` | string（可空） | 否 |  |
| `run.error_message` | string（可空） | 否 |  |
| `run.created_at` | string（date-time） | 是 |  |
| `run.started_at` | string（date-time，可空） | 否 |  |
| `run.finished_at` | string（date-time，可空） | 否 |  |
| `queued` | boolean | 是 | 恒为 true；执行交给 runtime |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 409 | 同 client_message_id 已用于不同正文（40925） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/chat`

<!-- operationId: getBotChatSnapshot -->

聊天页一致快照

消息尾部页、调用、run 与事件水位（next_event_seq）取自同一读取点。
详情页先装快照再订阅：SSE 游标从 next_event_seq - 1 起读，与快照内容
严格衔接，既不重放快照已含的事件也不漏掉快照之后的事件。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `message_limit` | integer（≤200，默认 50） | 否 | 消息尾部页大小 |

**成功响应**

**200** 快照

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `bot` | object | 是 |  |
| `bot.id` | string | 是 |  |
| `bot.name` | string | 是 |  |
| `bot.persona_prompt` | string | 是 | 人设原文；平台规则在服务端拼接，不在此回显 |
| `bot.prompt_revision` | integer（int64） | 是 | 人设/平台规则版本；用户明确编辑人设时递增 |
| `bot.platform_prompt_version` | integer（int64） | 是 |  |
| `bot.toolset_version` | integer（int64） | 是 |  |
| `bot.provider_id` | string | 是 | 所属 AI 提供商（INT-133）；地址与密钥都在提供商上 |
| `bot.provider_name` | string | 是 | 提供商名称（展示用，派生自 provider） |
| `bot.catalog_id` | string | 是 | 提供商的目录 id（models.dev id 或 custom，派生自 provider） |
| `bot.base_url` | string | 是 | OpenAI 兼容 API 基址；INT-133 起为 provider 的派生值（提供商轮换后下一次读取生效） |
| `bot.model` | string | 是 |  |
| `bot.api_key_last4` | string | 是 | API Key 末四位（provider 的派生值）；明文任何端点都不回显 |
| `bot.context_window` | integer（int64） | 是 | 模型上下文窗口（未扣输出空间）。解析顺序 manual > detected > catalog > fallback；fallback 为部署默认窗口 |
| `bot.max_output_tokens` | integer（int64） | 是 | 模型输出上限；detected（探测缓存）与 catalog（内置目录）来源可给出，未知为 0 |
| `bot.context_source` | string | 是 | 窗口来源：manual（部署覆盖）/ detected（GET /models 探测缓存）/ catalog（内置 models.dev 目录，INT-133）/ fallback（默认窗口）； 展示层不得把回退值说成已检测 |
| `bot.context_detail` | string | 否 | 解析口径说明（探测方式或回退原因）；可省略 |
| `bot.next_event_seq` | integer（int64） | 是 | 事件账本水位（下一条的 seq）；详情订阅以此-1 为 afterSeq， 与详情快照衔接，不从 0 重放历史 |
| `bot.last_message` | object（可空） | 是 | 主线最近一条正文 trim 后非空的 user/assistant 消息预览（按 seq 最大取）； 正文纯空白或 content 坏 JSON 的消息跳过继续往前找，定时任务会话不参与。 没有符合条件消息时为 null。仅 GET /api/bots 列表端点返回真实值； 详情与聊天快照等其它端点恒为 null。机器人主线最近一条可见消息的预览（INT-125），机器人列表端点专用 |
| `bot.last_message.kind` | string | 是 | 消息角色 user / assistant |
| `bot.last_message.text` | string | 是 | 完整正文；服务端不截断，截断展示由前端负责 |
| `bot.created_at` | string（date-time） | 是 |  |
| `bot.updated_at` | string（date-time） | 是 |  |
| `messages` | object[] | 是 |  |
| `messages[].id` | string | 是 |  |
| `messages[].bot_id` | string | 是 |  |
| `messages[].seq` | integer（int64） | 是 |  |
| `messages[].run_id` | string | 否 |  |
| `messages[].request_id` | string | 否 |  |
| `messages[].call_id` | string | 否 |  |
| `messages[].kind` | string | 是 | user / assistant / tool_reply / background_result / approval_decision / lifecycle |
| `messages[].content` | object | 是 | 按 kind 变化的正文；assistant 含 text 与 tool_calls 数组；tool_reply 含协议回复字符串 |
| `messages[].created_at` | string（date-time） | 是 |  |
| `calls` | object[] | 是 |  |
| `calls[].id` | string | 是 | 平台调用 ID，同时是执行幂等键 |
| `calls[].bot_id` | string | 是 |  |
| `calls[].request_id` | string | 是 |  |
| `calls[].provider_tool_call_id` | string | 是 | 模型协议配对 ID |
| `calls[].tool_name` | string | 是 |  |
| `calls[].action` | string | 是 |  |
| `calls[].arguments` | object | 是 | 规范化后的参数 |
| `calls[].execution_mode` | string | 是 | sync / async |
| `calls[].requires_confirmation` | boolean | 是 |  |
| `calls[].status` | string | 是 | pending_confirmation / queued / running / succeeded / failed / rejected / cancelled / not_executed |
| `calls[].decision` | string（可空） | 否 | approve / reject / always_allow |
| `calls[].decided_at` | string（date-time，可空） | 否 |  |
| `calls[].authorization_id` | string（可空） | 否 |  |
| `calls[].execution_context` | object | 是 | 服务端生成的执行身份（T3 command/thread/message ID 等）与授权范围 |
| `calls[].policy` | object | 否 | 分级放行评估结论（INT-160）：{tier, rule_id?, auto, source, facts?, downgrade_reason?, expiry?, schedule_denied?}。凡经过策略评估的写 调用都有：自动执行（auto=true）、确认卡（含降级原因与过期时间）、 定时任务会话 tier3 同步拒绝三类；历史行为空即「策略上线前」 |
| `calls[].cancel_requested` | boolean | 否 |  |
| `calls[].result_preview` | object | 否 | 进入模型上下文的受限结果（完整结果走 result 端点） |
| `calls[].result_chars` | integer（int64） | 是 |  |
| `calls[].truncated` | boolean | 是 |  |
| `calls[].error_code` | string（可空） | 否 |  |
| `calls[].error_message` | string（可空） | 否 |  |
| `calls[].effect_may_have_occurred` | boolean | 是 | 副作用可能已发生（取消/中断/重启）；不声称回滚 |
| `calls[].created_at` | string（date-time） | 是 |  |
| `calls[].started_at` | string（date-time，可空） | 否 |  |
| `calls[].finished_at` | string（date-time，可空） | 否 |  |
| `runs` | object[] | 是 |  |
| `runs[].id` | string | 是 |  |
| `runs[].bot_id` | string | 是 |  |
| `runs[].trigger_source` | string | 是 | user_message / tool_result / recovery / schedule / manual / schedule_event（定时任务运行事件写入主线后的立即唤醒，INT-138） |
| `runs[].trigger_key` | string | 是 |  |
| `runs[].status` | string | 是 | queued / running / completed / failed / stopped |
| `runs[].config_snapshot` | string | 是 | 本次模型/地址/提示词版本快照 JSON；不含明文 key |
| `runs[].input_through_seq` | integer（int64） | 是 |  |
| `runs[].stop_requested` | boolean | 是 |  |
| `runs[].prompt_tokens` | integer（int64，可空） | 否 |  |
| `runs[].completion_tokens` | integer（int64，可空） | 否 |  |
| `runs[].total_tokens` | integer（int64，可空） | 否 |  |
| `runs[].cached_tokens` | integer（int64，可空） | 否 |  |
| `runs[].usage_complete` | boolean | 是 | false 表示上游未报 usage（缺失与零严格区分） |
| `runs[].cost_estimate` | number（double，可空） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价/全缺 usage 时为 null（缺失 ≠ 0，INT-139） |
| `runs[].cost_source` | string（可空） | 否 | 计价来源（catalog）；cost_estimate 为 null 时同 null |
| `runs[].error_code` | string（可空） | 否 |  |
| `runs[].error_message` | string（可空） | 否 |  |
| `runs[].created_at` | string（date-time） | 是 |  |
| `runs[].started_at` | string（date-time，可空） | 否 |  |
| `runs[].finished_at` | string（date-time，可空） | 否 |  |
| `next_event_seq` | integer（int64） | 是 | 与快照同一读取点的事件账本水位（下一条的 seq） |
| `metrics` | object | 否 | 运行指标（INT-115）：窗口能力 + 最近一次主线模型请求口径； 没有主线请求时 request 缺省、窗口能力仍然可见；字段缺省（omitempty） |
| `metrics.context_window` | integer（int64） | 是 | 模型上下文窗口（未扣输出空间）。解析顺序 manual > detected > catalog > fallback；fallback 为部署默认窗口 |
| `metrics.max_output_tokens` | integer（int64） | 是 | 模型输出上限；detected（探测缓存）与 catalog（内置目录）来源可给出，未知为 0 |
| `metrics.context_source` | string | 是 | 窗口来源：manual（部署覆盖）/ detected（GET /models 探测缓存）/ catalog（内置 models.dev 目录，INT-133）/ fallback（默认窗口） |
| `metrics.context_detail` | string | 否 | 解析口径说明；可省略 |
| `metrics.request` | object | 否 | 最近一次主线模型请求（进行中或已结束）；没有主线请求时省略。单次主线模型请求口径；历史累计仍在 run 上，指标区不用累计值 |
| `metrics.request.request_id` | string | 是 |  |
| `metrics.request.run_id` | string | 是 |  |
| `metrics.request.ordinal` | integer（int64） | 是 |  |
| `metrics.request.status` | string | 是 | running / succeeded / failed |
| `metrics.request.prompt_tokens` | integer（int64，可空） | 否 |  |
| `metrics.request.completion_tokens` | integer（int64，可空） | 否 |  |
| `metrics.request.cached_tokens` | integer（int64，可空） | 否 |  |
| `metrics.request.usage_complete` | boolean | 是 | false 表示上游未报 usage（缺失与零严格区分）；false 时计数视为不可用 |
| `metrics.request.cost_estimate` | number（double，可空） | 否 | 按 run 冻结目录单价估算的本次请求成本（USD）；目录无价/缺 usage 时为 null（缺失 ≠ 0，INT-139） |
| `metrics.request.cost_source` | string（可空） | 否 | 计价来源（catalog）；cost_estimate 为 null 时同 null |
| `metrics.request.input_tokens_estimate` | integer（int64） | 是 | 发送前的上下文 token 估算值（口径见 input_estimator；上下文占比用估算值） |
| `metrics.request.input_estimator` | string | 是 | 估算方式（usage_delta/3.5 或 utf8_bytes/3.5） |
| `metrics.request.started_at` | string（date-time） | 是 |  |
| `metrics.request.finished_at` | string（date-time，可空） | 否 |  |
| `metrics.request.generation_ms` | integer（int64） | 是 | 首个流式正文片段到请求结束的耗时；0 表示不可用（纯工具 调用、失败请求或时长非正）。Token 输出速度 = completion_tokens / (generation_ms/1000)，仅在两者都有且 >0 时给出 |
| `maintenance_progress` | object | 否 | 当前活动主线 run 最近一条维护进度（压缩阶段/失败原因）； 无活动主线 run 或尚无进度事件时字段缺省——已结束 run 的进度不再返回。活动主线 run 的最近一条维护进度；与 SSE 的 kind=maintenance_progress delta 事件同源，供刷新后运行栏水合 |
| `maintenance_progress.run_id` | string | 否 | 进度所属主线 run；服务端无 run 场景省略 |
| `maintenance_progress.phase` | string | 是 | 压缩阶段（archive / summary / failed 等） |
| `maintenance_progress.message` | string | 是 | 人类可读的进度或失败说明 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/events`

<!-- operationId: streamBotEvents -->

SSE 事件流（持久重放账本）

`text/event-stream`。五种事件固定：`delta`（text_delta 增量、
message_commit 完整消息提交、run_started、user_message、lifecycle）、
`tool_call`（phase=proposed/awaiting_confirmation/queued/running）、
`tool_result`（phase=receipt/decision/terminal）、`done`（run 完成，
不是整个事件流关闭）、`error`（run 失败；顺序固定 error → done）。
每帧 `id: <event_seq>`；断线后用 `after_seq=N` 重放补齐。心跳是
`: ping` 注释。SSE 断开只结束订阅，不取消 run。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `after_seq` | integer（int64，默认 0） | 否 | 从该事件 seq 之后开始重放 |

**成功响应**

**200** 事件流

`text/event-stream`：object

SSE 帧 data 的统一形状：bot_id 必带；kind 区分 delta 下的
text_delta / message_commit / run_started / user_message / lifecycle，
tool_call 的 phase 区分 proposed / awaiting_confirmation / queued /
running，tool_result 的 phase 区分 receipt / decision / terminal，
done 携带 run 终态，error 携带 code/message（顺序固定 error → done）。

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |

### GET `/api/bots/{id}/runs`

<!-- operationId: listBotRuns -->

运行记录列表

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `limit` | integer（≤100，默认 50） | 否 |  |

**成功响应**

**200** run 列表（新→旧）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].bot_id` | string | 是 |  |
| `items[].trigger_source` | string | 是 | user_message / tool_result / recovery / schedule / manual / schedule_event（定时任务运行事件写入主线后的立即唤醒，INT-138） |
| `items[].trigger_key` | string | 是 |  |
| `items[].status` | string | 是 | queued / running / completed / failed / stopped |
| `items[].config_snapshot` | string | 是 | 本次模型/地址/提示词版本快照 JSON；不含明文 key |
| `items[].input_through_seq` | integer（int64） | 是 |  |
| `items[].stop_requested` | boolean | 是 |  |
| `items[].prompt_tokens` | integer（int64，可空） | 否 |  |
| `items[].completion_tokens` | integer（int64，可空） | 否 |  |
| `items[].total_tokens` | integer（int64，可空） | 否 |  |
| `items[].cached_tokens` | integer（int64，可空） | 否 |  |
| `items[].usage_complete` | boolean | 是 | false 表示上游未报 usage（缺失与零严格区分） |
| `items[].cost_estimate` | number（double，可空） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价/全缺 usage 时为 null（缺失 ≠ 0，INT-139） |
| `items[].cost_source` | string（可空） | 否 | 计价来源（catalog）；cost_estimate 为 null 时同 null |
| `items[].error_code` | string（可空） | 否 |  |
| `items[].error_message` | string（可空） | 否 |  |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].started_at` | string（date-time，可空） | 否 |  |
| `items[].finished_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/runs/{runId}`

<!-- operationId: getBotRun -->

读取运行记录

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `runId` | string | 是 |  |

**成功响应**

**200** run 详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `bot_id` | string | 是 |  |
| `trigger_source` | string | 是 | user_message / tool_result / recovery / schedule / manual / schedule_event（定时任务运行事件写入主线后的立即唤醒，INT-138） |
| `trigger_key` | string | 是 |  |
| `status` | string | 是 | queued / running / completed / failed / stopped |
| `config_snapshot` | string | 是 | 本次模型/地址/提示词版本快照 JSON；不含明文 key |
| `input_through_seq` | integer（int64） | 是 |  |
| `stop_requested` | boolean | 是 |  |
| `prompt_tokens` | integer（int64，可空） | 否 |  |
| `completion_tokens` | integer（int64，可空） | 否 |  |
| `total_tokens` | integer（int64，可空） | 否 |  |
| `cached_tokens` | integer（int64，可空） | 否 |  |
| `usage_complete` | boolean | 是 | false 表示上游未报 usage（缺失与零严格区分） |
| `cost_estimate` | number（double，可空） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价/全缺 usage 时为 null（缺失 ≠ 0，INT-139） |
| `cost_source` | string（可空） | 否 | 计价来源（catalog）；cost_estimate 为 null 时同 null |
| `error_code` | string（可空） | 否 |  |
| `error_message` | string（可空） | 否 |  |
| `created_at` | string（date-time） | 是 |  |
| `started_at` | string（date-time，可空） | 否 |  |
| `finished_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/bots/{id}/runs/{runId}/stop`

<!-- operationId: stopBotRun -->

停止主线 run

首次调用受理停止：追加一条「停止」用户消息（client_message_id=
stop:<runId> 幂等）并唤醒机器人，由它结合上下文决定后续动作；宽限期
内 run 未自然到安全点时按受理时的请求序号判断——之后已发起过新请求则
不硬取消（避免丢掉停止之后的新输入），否则硬取消执行槽 context，run
在安全点收尾为 stopped。已受理过停止再调用（重复点击 / 强制停止）跳过
宽限与进展判断，立即硬取消。run 已结束返回 40928。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `runId` | string | 是 |  |

**成功响应**

**200** 停止已请求（run 状态以响应为准）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `bot_id` | string | 是 |  |
| `trigger_source` | string | 是 | user_message / tool_result / recovery / schedule / manual / schedule_event（定时任务运行事件写入主线后的立即唤醒，INT-138） |
| `trigger_key` | string | 是 |  |
| `status` | string | 是 | queued / running / completed / failed / stopped |
| `config_snapshot` | string | 是 | 本次模型/地址/提示词版本快照 JSON；不含明文 key |
| `input_through_seq` | integer（int64） | 是 |  |
| `stop_requested` | boolean | 是 |  |
| `prompt_tokens` | integer（int64，可空） | 否 |  |
| `completion_tokens` | integer（int64，可空） | 否 |  |
| `total_tokens` | integer（int64，可空） | 否 |  |
| `cached_tokens` | integer（int64，可空） | 否 |  |
| `usage_complete` | boolean | 是 | false 表示上游未报 usage（缺失与零严格区分） |
| `cost_estimate` | number（double，可空） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价/全缺 usage 时为 null（缺失 ≠ 0，INT-139） |
| `cost_source` | string（可空） | 否 | 计价来源（catalog）；cost_estimate 为 null 时同 null |
| `error_code` | string（可空） | 否 |  |
| `error_message` | string（可空） | 否 |  |
| `created_at` | string（date-time） | 是 |  |
| `started_at` | string（date-time，可空） | 否 |  |
| `finished_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 409 | 运行状态已变化（40928） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/schedule-tasks`

<!-- operationId: listBotScheduleTasks -->

定时任务列表

机器人定时任务（INT-103）用户侧只读；任务的创建/修改/删除只经机器人 fs_schedule 工具。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**成功响应**

**200** 任务列表（新→旧，含最近一次运行）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].bot_id` | string | 是 |  |
| `items[].name` | string | 是 |  |
| `items[].cron_expression` | string | 是 | 标准五字段 cron（分 时 日 月 周） |
| `items[].timezone` | string | 是 | IANA 时区名称；空串 = UTC |
| `items[].instruction` | string | 是 | 每次运行交给机器人的任务指令 |
| `items[].model` | string | 是 | 运行使用的模型；空串用机器人当前模型 |
| `items[].needs_report` | boolean | 是 | true 时成功且非静默的运行也追加事件进主线 |
| `items[].enabled` | boolean | 是 |  |
| `items[].next_run_at` | string（date-time，可空） | 是 | 下一次计划触发时间（UTC）；停用或调度暂停时为 null |
| `items[].state` | string（可空） | 是 | 任务小状态的原始 JSON 文本（INT-123，无损；无状态为 null）。写入只经机器人 fs_schedule 的 state_set，用户只能经 DELETE .../state 清空 |
| `items[].state_updated_at` | string（date-time，可空） | 是 | 状态最近一次写入时间（UTC）；无状态为 null |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |
| `items[].last_run` | object（可空） | 是 |  |
| `items[].last_run.id` | string | 是 |  |
| `items[].last_run.task_id` | string（可空） | 是 |  |
| `items[].last_run.status` | string | 是 | running / completed / failed / stopped / skipped |
| `items[].last_run.summary` | string | 是 | 结果摘要（截断到 2000 字符）；skipped 记跳过原因，静默运行记空串 |
| `items[].last_run.silent` | boolean | 是 | true 表示运行以 [[SILENT]] 哨兵结束，无事可汇报 |
| `items[].last_run.trigger_source` | string | 是 | 触发来源：cron=按计划自动触发，manual=机器人经 fs_schedule run 手动立即触发（INT-121） |
| `items[].last_run.error_code` | string | 否 |  |
| `items[].last_run.error_message` | string | 否 |  |
| `items[].last_run.scheduled_for` | string（date-time，可空） | 是 | 本 run 对应的计划时间（UTC） |
| `items[].last_run.started_at` | string（date-time，可空） | 是 |  |
| `items[].last_run.finished_at` | string（date-time，可空） | 是 |  |
| `items[].last_run.duration_ms` | integer（int64，可空） | 是 |  |
| `items[].last_run.prompt_tokens` | integer（int64） | 否 |  |
| `items[].last_run.completion_tokens` | integer（int64） | 否 |  |
| `items[].last_run.total_tokens` | integer（int64） | 否 |  |
| `items[].last_run.cost_estimate` | number（double） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价时缺省（缺失 ≠ 0，INT-139） |
| `items[].last_run.cost_source` | string | 否 | 计价来源（catalog） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/schedule-tasks/{taskId}`

<!-- operationId: getBotScheduleTask -->

读取定时任务

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `taskId` | string | 是 |  |

**成功响应**

**200** 任务详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `bot_id` | string | 是 |  |
| `name` | string | 是 |  |
| `cron_expression` | string | 是 | 标准五字段 cron（分 时 日 月 周） |
| `timezone` | string | 是 | IANA 时区名称；空串 = UTC |
| `instruction` | string | 是 | 每次运行交给机器人的任务指令 |
| `model` | string | 是 | 运行使用的模型；空串用机器人当前模型 |
| `needs_report` | boolean | 是 | true 时成功且非静默的运行也追加事件进主线 |
| `enabled` | boolean | 是 |  |
| `next_run_at` | string（date-time，可空） | 是 | 下一次计划触发时间（UTC）；停用或调度暂停时为 null |
| `state` | string（可空） | 是 | 任务小状态的原始 JSON 文本（INT-123，无损；无状态为 null）。写入只经机器人 fs_schedule 的 state_set，用户只能经 DELETE .../state 清空 |
| `state_updated_at` | string（date-time，可空） | 是 | 状态最近一次写入时间（UTC）；无状态为 null |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `last_run` | object（可空） | 是 |  |
| `last_run.id` | string | 是 |  |
| `last_run.task_id` | string（可空） | 是 |  |
| `last_run.status` | string | 是 | running / completed / failed / stopped / skipped |
| `last_run.summary` | string | 是 | 结果摘要（截断到 2000 字符）；skipped 记跳过原因，静默运行记空串 |
| `last_run.silent` | boolean | 是 | true 表示运行以 [[SILENT]] 哨兵结束，无事可汇报 |
| `last_run.trigger_source` | string | 是 | 触发来源：cron=按计划自动触发，manual=机器人经 fs_schedule run 手动立即触发（INT-121） |
| `last_run.error_code` | string | 否 |  |
| `last_run.error_message` | string | 否 |  |
| `last_run.scheduled_for` | string（date-time，可空） | 是 | 本 run 对应的计划时间（UTC） |
| `last_run.started_at` | string（date-time，可空） | 是 |  |
| `last_run.finished_at` | string（date-time，可空） | 是 |  |
| `last_run.duration_ms` | integer（int64，可空） | 是 |  |
| `last_run.prompt_tokens` | integer（int64） | 否 |  |
| `last_run.completion_tokens` | integer（int64） | 否 |  |
| `last_run.total_tokens` | integer（int64） | 否 |  |
| `last_run.cost_estimate` | number（double） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价时缺省（缺失 ≠ 0，INT-139） |
| `last_run.cost_source` | string | 否 | 计价来源（catalog） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/schedule-tasks/{taskId}/runs`

<!-- operationId: listBotScheduleTaskRuns -->

定时任务运行记录列表

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `taskId` | string | 是 |  |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `limit` | integer（≤100，默认 20） | 否 |  |
| `offset` | integer（默认 0） | 否 |  |

**成功响应**

**200** 运行记录列表（新→旧）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].task_id` | string（可空） | 是 |  |
| `items[].status` | string | 是 | running / completed / failed / stopped / skipped |
| `items[].summary` | string | 是 | 结果摘要（截断到 2000 字符）；skipped 记跳过原因，静默运行记空串 |
| `items[].silent` | boolean | 是 | true 表示运行以 [[SILENT]] 哨兵结束，无事可汇报 |
| `items[].trigger_source` | string | 是 | 触发来源：cron=按计划自动触发，manual=机器人经 fs_schedule run 手动立即触发（INT-121） |
| `items[].error_code` | string | 否 |  |
| `items[].error_message` | string | 否 |  |
| `items[].scheduled_for` | string（date-time，可空） | 是 | 本 run 对应的计划时间（UTC） |
| `items[].started_at` | string（date-time，可空） | 是 |  |
| `items[].finished_at` | string（date-time，可空） | 是 |  |
| `items[].duration_ms` | integer（int64，可空） | 是 |  |
| `items[].prompt_tokens` | integer（int64） | 否 |  |
| `items[].completion_tokens` | integer（int64） | 否 |  |
| `items[].total_tokens` | integer（int64） | 否 |  |
| `items[].cost_estimate` | number（double） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价时缺省（缺失 ≠ 0，INT-139） |
| `items[].cost_source` | string | 否 | 计价来源（catalog） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/bots/{id}/schedule-tasks/{taskId}/runs/{runId}/stop`

<!-- operationId: stopBotScheduleTaskRun -->

停止定时任务运行

置 stop_requested 并取消该 run 的执行槽 context，循环在安全点收尾为
stopped，不追加主线事件（用户主动停止不是需要汇报的运行结果）。没有
执行槽的运行由补唤醒或 5s 扫描进入收尾。run 已终态返回 40928；run 不
属于该机器人或该任务返回 404。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `taskId` | string | 是 |  |
| `runId` | string | 是 |  |

**成功响应**

**200** 停止已请求（run 状态以响应为准）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `bot_id` | string | 是 |  |
| `trigger_source` | string | 是 | user_message / tool_result / recovery / schedule / manual / schedule_event（定时任务运行事件写入主线后的立即唤醒，INT-138） |
| `trigger_key` | string | 是 |  |
| `status` | string | 是 | queued / running / completed / failed / stopped |
| `config_snapshot` | string | 是 | 本次模型/地址/提示词版本快照 JSON；不含明文 key |
| `input_through_seq` | integer（int64） | 是 |  |
| `stop_requested` | boolean | 是 |  |
| `prompt_tokens` | integer（int64，可空） | 否 |  |
| `completion_tokens` | integer（int64，可空） | 否 |  |
| `total_tokens` | integer（int64，可空） | 否 |  |
| `cached_tokens` | integer（int64，可空） | 否 |  |
| `usage_complete` | boolean | 是 | false 表示上游未报 usage（缺失与零严格区分） |
| `cost_estimate` | number（double，可空） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价/全缺 usage 时为 null（缺失 ≠ 0，INT-139） |
| `cost_source` | string（可空） | 否 | 计价来源（catalog）；cost_estimate 为 null 时同 null |
| `error_code` | string（可空） | 否 |  |
| `error_message` | string（可空） | 否 |  |
| `created_at` | string（date-time） | 是 |  |
| `started_at` | string（date-time，可空） | 否 |  |
| `finished_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 409 | 运行状态已变化（40928） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/bots/{id}/schedule-tasks/{taskId}/enabled`

<!-- operationId: setBotScheduleTaskEnabled -->

开启/关闭定时任务

用户对任务唯一的写操作。body 只接受 {"enabled":bool}；出现其它字段返回
40001。重新开启时 next_run_at 重算为当前时间之后的下一次，不补跑过期计划。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `taskId` | string | 是 |  |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `enabled` | boolean | 是 |  |

**成功响应**

**200** 任务现状（含重算后的 next_run_at 与最近一次运行）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `bot_id` | string | 是 |  |
| `name` | string | 是 |  |
| `cron_expression` | string | 是 | 标准五字段 cron（分 时 日 月 周） |
| `timezone` | string | 是 | IANA 时区名称；空串 = UTC |
| `instruction` | string | 是 | 每次运行交给机器人的任务指令 |
| `model` | string | 是 | 运行使用的模型；空串用机器人当前模型 |
| `needs_report` | boolean | 是 | true 时成功且非静默的运行也追加事件进主线 |
| `enabled` | boolean | 是 |  |
| `next_run_at` | string（date-time，可空） | 是 | 下一次计划触发时间（UTC）；停用或调度暂停时为 null |
| `state` | string（可空） | 是 | 任务小状态的原始 JSON 文本（INT-123，无损；无状态为 null）。写入只经机器人 fs_schedule 的 state_set，用户只能经 DELETE .../state 清空 |
| `state_updated_at` | string（date-time，可空） | 是 | 状态最近一次写入时间（UTC）；无状态为 null |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `last_run` | object（可空） | 是 |  |
| `last_run.id` | string | 是 |  |
| `last_run.task_id` | string（可空） | 是 |  |
| `last_run.status` | string | 是 | running / completed / failed / stopped / skipped |
| `last_run.summary` | string | 是 | 结果摘要（截断到 2000 字符）；skipped 记跳过原因，静默运行记空串 |
| `last_run.silent` | boolean | 是 | true 表示运行以 [[SILENT]] 哨兵结束，无事可汇报 |
| `last_run.trigger_source` | string | 是 | 触发来源：cron=按计划自动触发，manual=机器人经 fs_schedule run 手动立即触发（INT-121） |
| `last_run.error_code` | string | 否 |  |
| `last_run.error_message` | string | 否 |  |
| `last_run.scheduled_for` | string（date-time，可空） | 是 | 本 run 对应的计划时间（UTC） |
| `last_run.started_at` | string（date-time，可空） | 是 |  |
| `last_run.finished_at` | string（date-time，可空） | 是 |  |
| `last_run.duration_ms` | integer（int64，可空） | 是 |  |
| `last_run.prompt_tokens` | integer（int64） | 否 |  |
| `last_run.completion_tokens` | integer（int64） | 否 |  |
| `last_run.total_tokens` | integer（int64） | 否 |  |
| `last_run.cost_estimate` | number（double） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价时缺省（缺失 ≠ 0，INT-139） |
| `last_run.cost_source` | string | 否 | 计价来源（catalog） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求体携带未知字段或缺 enabled（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/bots/{id}/schedule-tasks/{taskId}/state`

<!-- operationId: clearBotScheduleTaskState -->

清空定时任务状态

清空任务跨运行保留的小状态（INT-123）。幂等：无状态时同样返回 200。
状态的写入只经机器人 fs_schedule 的 state_set，这是用户对该状态唯一的
操作；随任务删除一并清除。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `taskId` | string | 是 |  |

**成功响应**

**200** 清空后的任务详情（state 与 state_updated_at 为 null）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `bot_id` | string | 是 |  |
| `name` | string | 是 |  |
| `cron_expression` | string | 是 | 标准五字段 cron（分 时 日 月 周） |
| `timezone` | string | 是 | IANA 时区名称；空串 = UTC |
| `instruction` | string | 是 | 每次运行交给机器人的任务指令 |
| `model` | string | 是 | 运行使用的模型；空串用机器人当前模型 |
| `needs_report` | boolean | 是 | true 时成功且非静默的运行也追加事件进主线 |
| `enabled` | boolean | 是 |  |
| `next_run_at` | string（date-time，可空） | 是 | 下一次计划触发时间（UTC）；停用或调度暂停时为 null |
| `state` | string（可空） | 是 | 任务小状态的原始 JSON 文本（INT-123，无损；无状态为 null）。写入只经机器人 fs_schedule 的 state_set，用户只能经 DELETE .../state 清空 |
| `state_updated_at` | string（date-time，可空） | 是 | 状态最近一次写入时间（UTC）；无状态为 null |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |
| `last_run` | object（可空） | 是 |  |
| `last_run.id` | string | 是 |  |
| `last_run.task_id` | string（可空） | 是 |  |
| `last_run.status` | string | 是 | running / completed / failed / stopped / skipped |
| `last_run.summary` | string | 是 | 结果摘要（截断到 2000 字符）；skipped 记跳过原因，静默运行记空串 |
| `last_run.silent` | boolean | 是 | true 表示运行以 [[SILENT]] 哨兵结束，无事可汇报 |
| `last_run.trigger_source` | string | 是 | 触发来源：cron=按计划自动触发，manual=机器人经 fs_schedule run 手动立即触发（INT-121） |
| `last_run.error_code` | string | 否 |  |
| `last_run.error_message` | string | 否 |  |
| `last_run.scheduled_for` | string（date-time，可空） | 是 | 本 run 对应的计划时间（UTC） |
| `last_run.started_at` | string（date-time，可空） | 是 |  |
| `last_run.finished_at` | string（date-time，可空） | 是 |  |
| `last_run.duration_ms` | integer（int64，可空） | 是 |  |
| `last_run.prompt_tokens` | integer（int64） | 否 |  |
| `last_run.completion_tokens` | integer（int64） | 否 |  |
| `last_run.total_tokens` | integer（int64） | 否 |  |
| `last_run.cost_estimate` | number（double） | 否 | 本 run 已计价请求的成本累计（USD，含送过 usage 的失败请求）；目录无价时缺省（缺失 ≠ 0，INT-139） |
| `last_run.cost_source` | string | 否 | 计价来源（catalog） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/calls`

<!-- operationId: listBotCalls -->

调用列表（支持按状态过滤）

status 逗号分隔；常用 pending_confirmation 查待确认卡片、queued/running 查后台执行。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `status` | string | 否 | 逗号分隔的状态过滤 |
| `limit` | integer（≤200，默认 100） | 否 |  |

**成功响应**

**200** 调用列表（新→旧）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 | 平台调用 ID，同时是执行幂等键 |
| `items[].bot_id` | string | 是 |  |
| `items[].request_id` | string | 是 |  |
| `items[].provider_tool_call_id` | string | 是 | 模型协议配对 ID |
| `items[].tool_name` | string | 是 |  |
| `items[].action` | string | 是 |  |
| `items[].arguments` | object | 是 | 规范化后的参数 |
| `items[].execution_mode` | string | 是 | sync / async |
| `items[].requires_confirmation` | boolean | 是 |  |
| `items[].status` | string | 是 | pending_confirmation / queued / running / succeeded / failed / rejected / cancelled / not_executed |
| `items[].decision` | string（可空） | 否 | approve / reject / always_allow |
| `items[].decided_at` | string（date-time，可空） | 否 |  |
| `items[].authorization_id` | string（可空） | 否 |  |
| `items[].execution_context` | object | 是 | 服务端生成的执行身份（T3 command/thread/message ID 等）与授权范围 |
| `items[].policy` | object | 否 | 分级放行评估结论（INT-160）：{tier, rule_id?, auto, source, facts?, downgrade_reason?, expiry?, schedule_denied?}。凡经过策略评估的写 调用都有：自动执行（auto=true）、确认卡（含降级原因与过期时间）、 定时任务会话 tier3 同步拒绝三类；历史行为空即「策略上线前」 |
| `items[].cancel_requested` | boolean | 否 |  |
| `items[].result_preview` | object | 否 | 进入模型上下文的受限结果（完整结果走 result 端点） |
| `items[].result_chars` | integer（int64） | 是 |  |
| `items[].truncated` | boolean | 是 |  |
| `items[].error_code` | string（可空） | 否 |  |
| `items[].error_message` | string（可空） | 否 |  |
| `items[].effect_may_have_occurred` | boolean | 是 | 副作用可能已发生（取消/中断/重启）；不声称回滚 |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].started_at` | string（date-time，可空） | 否 |  |
| `items[].finished_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/calls/{callId}`

<!-- operationId: getBotCall -->

读取调用详情（卡片当前状态从调用账本派生）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `callId` | string | 是 |  |

**成功响应**

**200** 调用详情

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 平台调用 ID，同时是执行幂等键 |
| `bot_id` | string | 是 |  |
| `request_id` | string | 是 |  |
| `provider_tool_call_id` | string | 是 | 模型协议配对 ID |
| `tool_name` | string | 是 |  |
| `action` | string | 是 |  |
| `arguments` | object | 是 | 规范化后的参数 |
| `execution_mode` | string | 是 | sync / async |
| `requires_confirmation` | boolean | 是 |  |
| `status` | string | 是 | pending_confirmation / queued / running / succeeded / failed / rejected / cancelled / not_executed |
| `decision` | string（可空） | 否 | approve / reject / always_allow |
| `decided_at` | string（date-time，可空） | 否 |  |
| `authorization_id` | string（可空） | 否 |  |
| `execution_context` | object | 是 | 服务端生成的执行身份（T3 command/thread/message ID 等）与授权范围 |
| `policy` | object | 否 | 分级放行评估结论（INT-160）：{tier, rule_id?, auto, source, facts?, downgrade_reason?, expiry?, schedule_denied?}。凡经过策略评估的写 调用都有：自动执行（auto=true）、确认卡（含降级原因与过期时间）、 定时任务会话 tier3 同步拒绝三类；历史行为空即「策略上线前」 |
| `cancel_requested` | boolean | 否 |  |
| `result_preview` | object | 否 | 进入模型上下文的受限结果（完整结果走 result 端点） |
| `result_chars` | integer（int64） | 是 |  |
| `truncated` | boolean | 是 |  |
| `error_code` | string（可空） | 否 |  |
| `error_message` | string（可空） | 否 |  |
| `effect_may_have_occurred` | boolean | 是 | 副作用可能已发生（取消/中断/重启）；不声称回滚 |
| `created_at` | string（date-time） | 是 |  |
| `started_at` | string（date-time，可空） | 否 |  |
| `finished_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/calls/{callId}/result`

<!-- operationId: getBotCallResult -->

读取调用的完整瘦身结果（UI 查看）

模型侧的分页续读走 fs_result.read 工具；本端点返回完整结果原文。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `callId` | string | 是 |  |

**成功响应**

**200** 完整结果

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `call_id` | string | 是 |  |
| `status` | string | 是 |  |
| `result` | object | 否 | 完整瘦身结果原文 |
| `truncated` | boolean | 是 |  |
| `result_chars` | integer（int64） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### POST `/api/bots/{id}/calls/{callId}/decision`

<!-- operationId: decideBotCall -->

确认卡片决定（approve/reject/always_allow）

只对保存好的调用参数决策，不能同时修改参数。approve：CAS 为 queued 并
唤醒后台 worker；reject：置 rejected 并落唯一终态结果；always_allow：
同一事务建立与本次调用相同 action/范围/字段约束的授权并批准当前调用。
重复相同决定幂等返回；相互冲突或状态已变化返回 40926。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `callId` | string | 是 |  |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `decision` | string | 是 | approve / reject / always_allow |

**成功响应**

**200** 决定已生效（返回最新调用状态）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 平台调用 ID，同时是执行幂等键 |
| `bot_id` | string | 是 |  |
| `request_id` | string | 是 |  |
| `provider_tool_call_id` | string | 是 | 模型协议配对 ID |
| `tool_name` | string | 是 |  |
| `action` | string | 是 |  |
| `arguments` | object | 是 | 规范化后的参数 |
| `execution_mode` | string | 是 | sync / async |
| `requires_confirmation` | boolean | 是 |  |
| `status` | string | 是 | pending_confirmation / queued / running / succeeded / failed / rejected / cancelled / not_executed |
| `decision` | string（可空） | 否 | approve / reject / always_allow |
| `decided_at` | string（date-time，可空） | 否 |  |
| `authorization_id` | string（可空） | 否 |  |
| `execution_context` | object | 是 | 服务端生成的执行身份（T3 command/thread/message ID 等）与授权范围 |
| `policy` | object | 否 | 分级放行评估结论（INT-160）：{tier, rule_id?, auto, source, facts?, downgrade_reason?, expiry?, schedule_denied?}。凡经过策略评估的写 调用都有：自动执行（auto=true）、确认卡（含降级原因与过期时间）、 定时任务会话 tier3 同步拒绝三类；历史行为空即「策略上线前」 |
| `cancel_requested` | boolean | 否 |  |
| `result_preview` | object | 否 | 进入模型上下文的受限结果（完整结果走 result 端点） |
| `result_chars` | integer（int64） | 是 |  |
| `truncated` | boolean | 是 |  |
| `error_code` | string（可空） | 否 |  |
| `error_message` | string（可空） | 否 |  |
| `effect_may_have_occurred` | boolean | 是 | 副作用可能已发生（取消/中断/重启）；不声称回滚 |
| `created_at` | string（date-time） | 是 |  |
| `started_at` | string（date-time，可空） | 否 |  |
| `finished_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 409 | 调用状态已变化或决定冲突（40926） |
| 500 | 服务器内部错误（50000） |

### POST `/api/bots/{id}/calls/{callId}/cancel`

<!-- operationId: cancelBotCall -->

取消后台调用

待确认/queued 直接 CAS 终结（未执行，effect_may_have_occurred=false）；
running 记录取消并取消其 context，worker 落唯一终态且标记
effect_may_have_occurred=true（不声称回滚）。已终结调用幂等返回现状。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `callId` | string | 是 |  |

**成功响应**

**200** 取消结果（返回最新调用状态）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 平台调用 ID，同时是执行幂等键 |
| `bot_id` | string | 是 |  |
| `request_id` | string | 是 |  |
| `provider_tool_call_id` | string | 是 | 模型协议配对 ID |
| `tool_name` | string | 是 |  |
| `action` | string | 是 |  |
| `arguments` | object | 是 | 规范化后的参数 |
| `execution_mode` | string | 是 | sync / async |
| `requires_confirmation` | boolean | 是 |  |
| `status` | string | 是 | pending_confirmation / queued / running / succeeded / failed / rejected / cancelled / not_executed |
| `decision` | string（可空） | 否 | approve / reject / always_allow |
| `decided_at` | string（date-time，可空） | 否 |  |
| `authorization_id` | string（可空） | 否 |  |
| `execution_context` | object | 是 | 服务端生成的执行身份（T3 command/thread/message ID 等）与授权范围 |
| `policy` | object | 否 | 分级放行评估结论（INT-160）：{tier, rule_id?, auto, source, facts?, downgrade_reason?, expiry?, schedule_denied?}。凡经过策略评估的写 调用都有：自动执行（auto=true）、确认卡（含降级原因与过期时间）、 定时任务会话 tier3 同步拒绝三类；历史行为空即「策略上线前」 |
| `cancel_requested` | boolean | 否 |  |
| `result_preview` | object | 否 | 进入模型上下文的受限结果（完整结果走 result 端点） |
| `result_chars` | integer（int64） | 是 |  |
| `truncated` | boolean | 是 |  |
| `error_code` | string（可空） | 否 |  |
| `error_message` | string（可空） | 否 |  |
| `effect_may_have_occurred` | boolean | 是 | 副作用可能已发生（取消/中断/重启）；不声称回滚 |
| `created_at` | string（date-time） | 是 |  |
| `started_at` | string（date-time，可空） | 否 |  |
| `finished_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/authorizations`

<!-- operationId: listBotAuthorizations -->

授权列表

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**Query 参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `active` | boolean（默认 false） | 否 | true 时只返回未撤销授权 |

**成功响应**

**200** 授权列表（新→旧）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].tool_name` | string | 是 |  |
| `items[].action` | string | 是 |  |
| `items[].scope_kind` | string | 是 | project / project_collection / issue_project / thread_launch / thread / thread_stop |
| `items[].scope_id` | string | 是 | 范围主体 ID（项目/Issue 所属项目/T3 项目/线程；project_collection 时为用户 ID） |
| `items[].environment_id` | string（可空） | 否 |  |
| `items[].constraints` | object | 是 | 规范化约束（如允许写入的字段集合 fields） |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].expires_at` | string（date-time，可空） | 否 | 到期时间（INT-160）：新建「始终允许」默认创建后 30 天；到期后不再命中（视为已失效，惰性补 revoked_at）。历史行可能为 null |
| `items[].revoked_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/bots/{id}/authorizations/{authorizationId}`

<!-- operationId: revokeBotAuthorization -->

撤销授权（实际撤销，不物理删除）

撤销只写 revoked_at，保留审计关联。撤销对尚未认领的授权调用生效
（回到待确认）；已经开始的操作不回滚。重复撤销幂等。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `authorizationId` | string | 是 |  |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### GET `/api/bots/{id}/policy-rules`

<!-- operationId: listBotWritePolicyRules -->

分级放行规则清单（tier1 规则开关与误判状态，INT-160）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**成功响应**

**200** 规则清单（静态目录全量，行缺失按启用）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].rule_id` | string | 是 | 规则 ID（thread_send_continue / thread_settle_idle / thread_unsettle） |
| `items[].name` | string | 是 |  |
| `items[].description` | string | 是 |  |
| `items[].enabled` | boolean | 是 | 行不存在即启用；误判自动降级或手动关闭后为 false |
| `items[].false_positive_count_24h` | integer | 是 | 24 小时窗口内的误判次数（用户反向操作按事实计数） |
| `items[].auto_disabled_at` | string（date-time，可空） | 否 | 误判自动降级的落库时间；手动重新启用后清空 |
| `items[].updated_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/bots/{id}/policy-rules/{ruleId}`

<!-- operationId: setBotWritePolicyRuleEnabled -->

开关一条分级放行规则（INT-160）

开关 tier1 规则；行不存在则落行。手动重新启用会清空自动降级时间戳
与误判计数（窗口重新开始）。误判自动降级（24 小时内 2 次用户反向
操作）也会写本开关并给主线追加生命周期消息。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |
| `ruleId` | string | 是 |  |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `enabled` | boolean | 是 |  |

**成功响应**

**200** 开关后的规则状态

`data`：

分级放行 tier1 规则的独立开关与误判状态（INT-160）。分档与前置条件由服务端按「动作+参数+目标线程内存状态」计算，本端点只暴露开关

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `rule_id` | string | 是 | 规则 ID（thread_send_continue / thread_settle_idle / thread_unsettle） |
| `name` | string | 是 |  |
| `description` | string | 是 |  |
| `enabled` | boolean | 是 | 行不存在即启用；误判自动降级或手动关闭后为 false |
| `false_positive_count_24h` | integer | 是 | 24 小时窗口内的误判次数（用户反向操作按事实计数） |
| `auto_disabled_at` | string（date-time，可空） | 否 | 误判自动降级的落库时间；手动重新启用后清空 |
| `updated_at` | string（date-time，可空） | 否 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

## ai-providers

AI 提供商（INT-133，仅 JWT）：按用户隔离的「地址 + 密钥」配置，可被多个机器人引用；内置 models.dev 目录（OpenAI 兼容 + 有 api 地址 + 模型支持 tool_call 的子集，随发版刷新）用于选择提供商与模型。key 全链路脱敏只回末四位

### GET `/api/ai-providers`

<!-- operationId: listAIProviders -->

列出当前用户的 AI 提供商（含被引用计数）

**鉴权**：仅 JWT

**成功响应**

**200** 提供商列表

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].catalog_id` | string | 是 | models.dev 提供商 id，或 custom |
| `items[].name` | string | 是 |  |
| `items[].base_url` | string | 是 | 规范化存储（TrimSpace + 去尾部 /）；目录提供商由目录决定，custom 由用户提供 |
| `items[].api_key_last4` | string | 是 | API Key 末四位；明文任何端点都不回显 |
| `items[].bot_count` | integer | 是 | 当前引用该提供商的机器人数量 |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### POST `/api/ai-providers`

<!-- operationId: createAIProvider -->

新增 AI 提供商

catalog_id 为目录 id 或 `custom`。目录提供商的 base_url 由目录决定
（请求里的 base_url 忽略）；custom 必须给 http/https 地址，存储前
规范化（TrimSpace + 去尾部 /）。name 缺省用目录名（custom 用「自定义」）。
api_key 加密落库（AES-GCM），响应只回末四位。同一 catalog 允许配多份
不同 key。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `catalog_id` | string | 是 | models.dev 提供商 id，或 custom |
| `name` | string | 否 | 缺省用目录名（custom 用「自定义」） |
| `base_url` | string | 否 | 仅 custom 接受；必须是 http/https，存储前规范化。目录提供商的地址由目录决定，此字段忽略 |
| `api_key` | string | 是 | 加密落库；空串/空白拒绝（40001） |

**成功响应**

**200** 创建成功

`data`：

AI 提供商（INT-133）：按用户隔离的地址 + 密钥配置，key 只回末四位

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `catalog_id` | string | 是 | models.dev 提供商 id，或 custom |
| `name` | string | 是 |  |
| `base_url` | string | 是 | 规范化存储（TrimSpace + 去尾部 /）；目录提供商由目录决定，custom 由用户提供 |
| `api_key_last4` | string | 是 | API Key 末四位；明文任何端点都不回显 |
| `bot_count` | integer | 是 | 当前引用该提供商的机器人数量 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### POST `/api/ai-providers/test`

<!-- operationId: testAIProvider -->

测试提供商连接（保存前/保存后均可）

与机器人 test-connection 同一检查项（streaming / tool_call），HTTP 200
且 ok=false 表示可达性/兼容性问题。目录提供商的地址由目录决定；
custom 必须给 base_url 与 model；目录提供商缺省用目录里第一个模型。
models 为 /models 列表尽力拉取的模型 id（拉取失败不填充，不影响 ok）。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `catalog_id` | string | 否 | 缺省按 custom 处理 |
| `base_url` | string | 否 | custom / 缺省 catalog_id 时必填 |
| `api_key` | string | 是 |  |
| `model` | string | 否 | 缺省用目录里第一个模型；custom 必须提供 |
| `provider_id` | string | 否 | 已存提供商 id（可选）：测试通过时服务端清除该提供商的定时任务熔断 |

**成功响应**

**200** 检查结果（ok=false 时检查项中带失败详情）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `ok` | boolean | 是 | streaming 与 tool_call 都通过才 true |
| `checks` | object[] | 是 |  |
| `checks[].name` | string | 是 | streaming / tool_call |
| `checks[].passed` | boolean | 是 |  |
| `checks[].detail` | string | 否 | 失败原因（截断；不含密钥） |
| `models` | string[] | 否 | /models 列表尽力拉取的模型 id；拉取失败省略（不影响 ok） |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | provider_id 不存在或不属于当前用户（40433） |
| 500 | 服务器内部错误（50000） |

### GET `/api/ai-providers/catalog`

<!-- operationId: listAIProviderCatalog -->

读取内置 models.dev 目录（OpenAI 兼容子集）

随二进制发布的静态目录（运行时不联网，发版时刷新）：OpenAI 兼容协议、
提供 api 地址且模型支持 tool_call 的提供商子集。模型窗口/输出上限缺失
时为 0（未知）。

**鉴权**：仅 JWT

**成功响应**

**200** 目录提供商列表（按 id 排序）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].name` | string | 是 |  |
| `items[].api` | string | 是 | 官方 OpenAI 兼容端点 |
| `items[].models` | object[] | 是 |  |
| `items[].models[].id` | string | 是 |  |
| `items[].models[].name` | string | 是 |  |
| `items[].models[].context_window` | integer（int64） | 是 |  |
| `items[].models[].max_output_tokens` | integer（int64） | 是 |  |
| `items[].models[].reasoning` | boolean | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### GET `/api/ai-providers/{id}`

<!-- operationId: getAIProvider -->

读取 AI 提供商

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**成功响应**

**200** 提供商配置

`data`：

AI 提供商（INT-133）：按用户隔离的地址 + 密钥配置，key 只回末四位

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `catalog_id` | string | 是 | models.dev 提供商 id，或 custom |
| `name` | string | 是 |  |
| `base_url` | string | 是 | 规范化存储（TrimSpace + 去尾部 /）；目录提供商由目录决定，custom 由用户提供 |
| `api_key_last4` | string | 是 | API Key 末四位；明文任何端点都不回显 |
| `bot_count` | integer | 是 | 当前引用该提供商的机器人数量 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/ai-providers/{id}`

<!-- operationId: updateAIProvider -->

更新 AI 提供商（轮换密钥 / 改名 / custom 改地址）

指针字段表达「未提供保留」；api_key 缺省保留原 key，空串拒绝（40001）。
catalog_id 不可改（更换目录归属等于换提供商，应新建一条）；目录提供商
的 base_url 由目录决定，请求里的 base_url 忽略。更新后引用它的机器人在
下一个 run 生效。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**请求体**

`application/json`，必填。

指针字段表达「未提供保留」；catalog_id 不可改；目录提供商的 base_url 由目录决定（请求忽略）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string | 否 |  |
| `base_url` | string | 否 | 仅 custom 生效 |
| `api_key` | string | 否 | 空串拒绝（40001）；缺省保留原 key |

**成功响应**

**200** 更新成功

`data`：

AI 提供商（INT-133）：按用户隔离的地址 + 密钥配置，key 只回末四位

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `catalog_id` | string | 是 | models.dev 提供商 id，或 custom |
| `name` | string | 是 |  |
| `base_url` | string | 是 | 规范化存储（TrimSpace + 去尾部 /）；目录提供商由目录决定，custom 由用户提供 |
| `api_key_last4` | string | 是 | API Key 末四位；明文任何端点都不回显 |
| `bot_count` | integer | 是 | 当前引用该提供商的机器人数量 |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 请求参数无效（40001-40099） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/ai-providers/{id}`

<!-- operationId: deleteAIProvider -->

删除 AI 提供商

仍被机器人引用时返回 409（40933），先更换或删除相关机器人。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | API Key ID |

**成功响应**

**200** 删除成功

`data`：any

响应负载；形状随端点而变，由 allOf 组合的兄弟 schema 收窄

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 404 | 资源不存在（40400-40499） |
| 409 | 仍被机器人引用（40933） |
| 500 | 服务器内部错误（50000） |

## quota

订阅套餐额度（INT-109）：粘贴凭据后服务端定时抓取各 AI 编码服务订阅额度并只读展示。管理端点仅 JWT；GET /api/quota/accounts 对 JWT 与 API Key 均开放（只读，机器人取数通道）

### GET `/api/quota/providers`

<!-- operationId: listQuotaProviders -->

列出支持的额度供应商

返回各供应商的凭据字段元数据与「凭据从哪里获取」的分步说明；前端按它动态渲染添加表单。不触发任何抓取。

**鉴权**：仅 JWT

**成功响应**

**200** 供应商列表（固定顺序）

`data` 为数组。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 供应商标识（commandcode / opencodego / zai / grok / codex / devin / factory / opendesign） |
| `name` | string | 是 |  |
| `plan_hint` | string | 是 | 可选，仅供空态提示 |
| `credential_fields` | object[] | 是 |  |
| `credential_fields[].key` | string | 是 | 凭据字段标识（api_key / token / access_token / org_id / account_id），提交时以它为 credentials 的 map key |
| `credential_fields[].label` | string | 是 |  |
| `credential_fields[].secret` | boolean | 是 | 密文字段前端用密码框展示；任何响应不回显 |
| `credential_fields[].required` | boolean | 是 |  |
| `credential_fields[].placeholder` | string | 是 |  |
| `credential_fields[].help` | string | 是 |  |
| `instructions` | string[] | 是 | 「凭据从哪里获取」分步说明，逐条一句话 |
| `warning` | string | 是 | 会话令牌类供应商必填的过期提示；其余为空串 |
| `device_authorization` | boolean | 是 | 该供应商是否支持浏览器设备授权获取凭据；true 时前端显示「使用浏览器授权」 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### GET `/api/quota/accounts`

<!-- operationId: listQuotaAccounts -->

列出当前用户的额度账号

回显最近一次成功抓取的快照（窗口、余额、套餐名）与状态；凭据不回显，credential_hint 只含末 4 位。JWT 与 API Key 均可（API Key 是机器人取数通道，只读）。

**鉴权**：JWT+API Key

**成功响应**

**200** 额度账号列表

`data` 为数组。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `provider` | string | 是 |  |
| `provider_name` | string | 是 | 供应商显示名（随供应商元数据，非用户可改） |
| `label` | string | 是 |  |
| `credential_hint` | string | 是 | 凭据尾号（形如 …ab12）；绝不回显明文 |
| `plan_name` | string | 是 | 最近一次成功快照的套餐名；未抓取过为空串 |
| `note` | string | 是 | 供应商特有的补充说明（如「无 Design Plan 额度，按钱包计费」）；为空串表示无 |
| `status` | string（enum: ok \| credential_invalid \| provider_error \| pending） | 是 | ok=正常；credential_invalid=凭据失效（401/403）；provider_error=接口异常；pending=暂未抓取 |
| `last_error` | string | 是 | 最近一次失败原因（脱敏后的用户可读文本） |
| `fetched_at` | string（date-time，可空） | 是 | 最近一次成功抓取时间（= 快照数据时间）；未成功过为 null |
| `last_success_at` | string（date-time，可空） | 是 | 同 fetched_at（保留字段名便于前端区分） |
| `windows` | object[] | 是 | 最近一次成功快照的窗口；失败时保留旧数据 |
| `windows[].kind` | string（enum: five_hour \| daily \| weekly \| monthly \| other） | 是 |  |
| `windows[].label` | string | 是 | 面向用户的窗口名；other 类给准确名字（如 MCP 次数） |
| `windows[].used_percent` | number（double，可空） | 是 | 0..100，四舍五入到 1 位小数；算不出为 null |
| `windows[].used` | number（double，可空） | 否 | 单位由 unit 决定 |
| `windows[].limit` | number（double，可空） | 否 |  |
| `windows[].unit` | string（enum: percent \| usd \| count） | 是 |  |
| `windows[].reset_at` | string（date-time，可空） | 否 | UTC；窗口重置时间 |
| `windows[].pool` | string | 否 | 窗口所属额度池的展示名；只有同账号存在多个并行额度池时才出现，如 Factory Droid 的「标准」/「Core」 |
| `balance` | object（可空） | 是 |  |
| `balance.label` | string | 是 |  |
| `balance.amount` | number（double） | 是 | 金额；显式 double，不降 float32 |
| `balance.currency` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 500 | 服务器内部错误（50000） |

### POST `/api/quota/accounts`

<!-- operationId: createQuotaAccount -->

添加额度账号

整组凭据按供应商元数据校验后 AES-256-GCM 加密落库；保存后服务端同步抓取一次，
抓取失败仍然保存（HTTP 200，status 为 credential_invalid / provider_error），
未抓取过时 status 为 pending。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `provider` | string（长度 ≥1） | 是 | 供应商 id（GET /api/quota/providers 返回的 id）；未知返回 40030 |
| `label` | string（长度 1..100） | 是 | 用户给账号起的名字（如「我的 Pro」） |
| `credentials` | object | 是 | 按供应商 credential_fields 的 key 组织的明文凭据（加密落库，任何响应不回显）；整组替换语义 |

**成功响应**

**200** 保存成功（含同步抓取结果）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `provider` | string | 是 |  |
| `provider_name` | string | 是 | 供应商显示名（随供应商元数据，非用户可改） |
| `label` | string | 是 |  |
| `credential_hint` | string | 是 | 凭据尾号（形如 …ab12）；绝不回显明文 |
| `plan_name` | string | 是 | 最近一次成功快照的套餐名；未抓取过为空串 |
| `note` | string | 是 | 供应商特有的补充说明（如「无 Design Plan 额度，按钱包计费」）；为空串表示无 |
| `status` | string（enum: ok \| credential_invalid \| provider_error \| pending） | 是 | ok=正常；credential_invalid=凭据失效（401/403）；provider_error=接口异常；pending=暂未抓取 |
| `last_error` | string | 是 | 最近一次失败原因（脱敏后的用户可读文本） |
| `fetched_at` | string（date-time，可空） | 是 | 最近一次成功抓取时间（= 快照数据时间）；未成功过为 null |
| `last_success_at` | string（date-time，可空） | 是 | 同 fetched_at（保留字段名便于前端区分） |
| `windows` | object[] | 是 | 最近一次成功快照的窗口；失败时保留旧数据 |
| `windows[].kind` | string（enum: five_hour \| daily \| weekly \| monthly \| other） | 是 |  |
| `windows[].label` | string | 是 | 面向用户的窗口名；other 类给准确名字（如 MCP 次数） |
| `windows[].used_percent` | number（double，可空） | 是 | 0..100，四舍五入到 1 位小数；算不出为 null |
| `windows[].used` | number（double，可空） | 否 | 单位由 unit 决定 |
| `windows[].limit` | number（double，可空） | 否 |  |
| `windows[].unit` | string（enum: percent \| usd \| count） | 是 |  |
| `windows[].reset_at` | string（date-time，可空） | 否 | UTC；窗口重置时间 |
| `windows[].pool` | string | 否 | 窗口所属额度池的展示名；只有同账号存在多个并行额度池时才出现，如 Factory Droid 的「标准」/「Core」 |
| `balance` | object（可空） | 是 |  |
| `balance.label` | string | 是 |  |
| `balance.amount` | number（double） | 是 | 金额；显式 double，不降 float32 |
| `balance.currency` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001）；供应商未知（40030）；必填凭据字段缺失或为空（40031） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/quota/accounts/{id}`

<!-- operationId: updateQuotaAccount -->

修改标签或整组替换凭据

省略 credentials 表示只改标签（不触发抓取）；提供 credentials 则整组替换凭据，
抓取状态打回 pending 并同步重抓一次，旧快照保留到新快照写入。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 额度账号 ID（UUID） |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `label` | string（长度 1..100） | 否 | 非空时替换标签；改标签不触发抓取 |
| `credentials` | object | 否 | 按供应商 credential_fields 的 key 组织的明文凭据（加密落库，任何响应不回显）；整组替换语义 |

**成功响应**

**200** 更新后的额度账号

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `provider` | string | 是 |  |
| `provider_name` | string | 是 | 供应商显示名（随供应商元数据，非用户可改） |
| `label` | string | 是 |  |
| `credential_hint` | string | 是 | 凭据尾号（形如 …ab12）；绝不回显明文 |
| `plan_name` | string | 是 | 最近一次成功快照的套餐名；未抓取过为空串 |
| `note` | string | 是 | 供应商特有的补充说明（如「无 Design Plan 额度，按钱包计费」）；为空串表示无 |
| `status` | string（enum: ok \| credential_invalid \| provider_error \| pending） | 是 | ok=正常；credential_invalid=凭据失效（401/403）；provider_error=接口异常；pending=暂未抓取 |
| `last_error` | string | 是 | 最近一次失败原因（脱敏后的用户可读文本） |
| `fetched_at` | string（date-time，可空） | 是 | 最近一次成功抓取时间（= 快照数据时间）；未成功过为 null |
| `last_success_at` | string（date-time，可空） | 是 | 同 fetched_at（保留字段名便于前端区分） |
| `windows` | object[] | 是 | 最近一次成功快照的窗口；失败时保留旧数据 |
| `windows[].kind` | string（enum: five_hour \| daily \| weekly \| monthly \| other） | 是 |  |
| `windows[].label` | string | 是 | 面向用户的窗口名；other 类给准确名字（如 MCP 次数） |
| `windows[].used_percent` | number（double，可空） | 是 | 0..100，四舍五入到 1 位小数；算不出为 null |
| `windows[].used` | number（double，可空） | 否 | 单位由 unit 决定 |
| `windows[].limit` | number（double，可空） | 否 |  |
| `windows[].unit` | string（enum: percent \| usd \| count） | 是 |  |
| `windows[].reset_at` | string（date-time，可空） | 否 | UTC；窗口重置时间 |
| `windows[].pool` | string | 否 | 窗口所属额度池的展示名；只有同账号存在多个并行额度池时才出现，如 Factory Droid 的「标准」/「Core」 |
| `balance` | object（可空） | 是 |  |
| `balance.label` | string | 是 |  |
| `balance.amount` | number（double） | 是 | 金额；显式 double，不降 float32 |
| `balance.currency` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001）；凭据字段缺失或为空（40031） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 账号不存在或不属于当前用户（40430） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/quota/accounts/{id}`

<!-- operationId: deleteQuotaAccount -->

删除额度账号

删除该行（连带凭据密文与快照）；他人的账号一律 40430，不泄露存在性。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 额度账号 ID（UUID） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 账号不存在或不属于当前用户（40430） |
| 500 | 服务器内部错误（50000） |

### POST `/api/quota/accounts/{id}/refresh`

<!-- operationId: refreshQuotaAccount -->

立即抓取一次

同步抓取并落库后返回账号；抓取结果（成功或失败）落在 status 字段，HTTP 仍为 200。
同一账号距上次成功抓取过近时返回 42900，message 写明需等待的秒数。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 额度账号 ID（UUID） |

**成功响应**

**200** 抓取完成后的额度账号

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `provider` | string | 是 |  |
| `provider_name` | string | 是 | 供应商显示名（随供应商元数据，非用户可改） |
| `label` | string | 是 |  |
| `credential_hint` | string | 是 | 凭据尾号（形如 …ab12）；绝不回显明文 |
| `plan_name` | string | 是 | 最近一次成功快照的套餐名；未抓取过为空串 |
| `note` | string | 是 | 供应商特有的补充说明（如「无 Design Plan 额度，按钱包计费」）；为空串表示无 |
| `status` | string（enum: ok \| credential_invalid \| provider_error \| pending） | 是 | ok=正常；credential_invalid=凭据失效（401/403）；provider_error=接口异常；pending=暂未抓取 |
| `last_error` | string | 是 | 最近一次失败原因（脱敏后的用户可读文本） |
| `fetched_at` | string（date-time，可空） | 是 | 最近一次成功抓取时间（= 快照数据时间）；未成功过为 null |
| `last_success_at` | string（date-time，可空） | 是 | 同 fetched_at（保留字段名便于前端区分） |
| `windows` | object[] | 是 | 最近一次成功快照的窗口；失败时保留旧数据 |
| `windows[].kind` | string（enum: five_hour \| daily \| weekly \| monthly \| other） | 是 |  |
| `windows[].label` | string | 是 | 面向用户的窗口名；other 类给准确名字（如 MCP 次数） |
| `windows[].used_percent` | number（double，可空） | 是 | 0..100，四舍五入到 1 位小数；算不出为 null |
| `windows[].used` | number（double，可空） | 否 | 单位由 unit 决定 |
| `windows[].limit` | number（double，可空） | 否 |  |
| `windows[].unit` | string（enum: percent \| usd \| count） | 是 |  |
| `windows[].reset_at` | string（date-time，可空） | 否 | UTC；窗口重置时间 |
| `windows[].pool` | string | 否 | 窗口所属额度池的展示名；只有同账号存在多个并行额度池时才出现，如 Factory Droid 的「标准」/「Core」 |
| `balance` | object（可空） | 是 |  |
| `balance.label` | string | 是 |  |
| `balance.amount` | number（double） | 是 | 金额；显式 double，不降 float32 |
| `balance.currency` | string | 是 |  |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 账号不存在或不属于当前用户（40430） |
| 429 | 手动刷新过于频繁（42900），message 含 Refresh-After 秒数 |
| 500 | 服务器内部错误（50000） |

### POST `/api/quota/test`

<!-- operationId: testQuotaCredentials -->

测试一组凭据

不落库、不改任何账号状态；结果落在 data.status，HTTP 恒为 200（参数校验错误除外）。供「添加账号」对话框的测试按钮。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `provider` | string（长度 ≥1） | 是 | 供应商 id；未知返回 40030 |
| `credentials` | object | 是 | 按供应商 credential_fields 的 key 组织的明文凭据（加密落库，任何响应不回显）；整组替换语义 |

**成功响应**

**200** 探测结果（status != ok 时 error 给用户可读原因，detail 为可选诊断）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `status` | string（enum: ok \| credential_invalid \| provider_error） | 是 |  |
| `plan_name` | string | 是 |  |
| `note` | string | 是 | 供应商特有的补充说明（如「无 Design Plan 额度，按钱包计费」）；为空串表示无 |
| `windows` | object[] | 是 |  |
| `windows[].kind` | string（enum: five_hour \| daily \| weekly \| monthly \| other） | 是 |  |
| `windows[].label` | string | 是 | 面向用户的窗口名；other 类给准确名字（如 MCP 次数） |
| `windows[].used_percent` | number（double，可空） | 是 | 0..100，四舍五入到 1 位小数；算不出为 null |
| `windows[].used` | number（double，可空） | 否 | 单位由 unit 决定 |
| `windows[].limit` | number（double，可空） | 否 |  |
| `windows[].unit` | string（enum: percent \| usd \| count） | 是 |  |
| `windows[].reset_at` | string（date-time，可空） | 否 | UTC；窗口重置时间 |
| `windows[].pool` | string | 否 | 窗口所属额度池的展示名；只有同账号存在多个并行额度池时才出现，如 Factory Droid 的「标准」/「Core」 |
| `balance` | object（可空） | 是 |  |
| `balance.label` | string | 是 |  |
| `balance.amount` | number（double） | 是 | 金额；显式 double，不降 float32 |
| `balance.currency` | string | 是 |  |
| `error` | string | 是 | status != ok 时给用户可读原因（已脱敏） |
| `detail` | string | 是 | 可选；更细的诊断（HTTP 状态码等），可为空串 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001）；供应商未知（40030）；凭据字段缺失或为空（40031） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### POST `/api/quota/device-authorizations`

<!-- operationId: startQuotaDeviceAuthorization -->

发起浏览器设备授权

供支持设备授权的供应商（provider.device_authorization = true，目前只有 opendesign），
在服务端向上游发起设备授权并建立内存会话，返回 pending 视图；前端拿 user_code 与
activation_url 引导用户在浏览器批准，再按 poll_interval_seconds 轮询 GET。
不带 account_id 时批准后自动新建额度账号；带上 account_id 则为该已有账号整组换
凭据（账号不存在或属于他人返回 40430）。
会话只存内存，进程重启即丢，之后 GET 返回 40431。deviceSecret 永不出现在任何响应里。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `provider` | string（长度 ≥1） | 是 | 供应商 id（GET /api/quota/providers 返回的 id）；未知返回 40030，不支持设备授权返回 40032 |
| `label` | string（长度 1..100） | 是 | 批准后新建额度账号的名字 |
| `account_id` | string | 否 | 可选；带上时批准后不再新建账号，而是整组替换该已有账号的凭据（为已有账号 换凭据，编辑场景使用）。账号不存在或属于他人返回 40430；目标账号必须与 provider 是同一供应商，否则返回 40001（校验在建会话之前，不触上游）。 |

**成功响应**

**200** 已发起（pending）；同时表示上游会话建立成功

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 会话 ID，用于后续 GET 轮询与 DELETE 取消 |
| `provider` | string | 是 |  |
| `status` | string（enum: pending \| approved \| expired \| denied \| invalid） | 是 | pending 等待用户批准；approved 已拿到控制面密钥并创建账号； expired 验证码过期或本地超时；denied 用户拒绝/取消； invalid 设备密钥失效（上游 401/404） |
| `user_code` | string | 是 | 用户在批准页面输入的验证码 |
| `activation_url` | string | 是 | 浏览器批准页面地址（前端 target=_blank 打开，不在服务端跳转） |
| `expires_at` | string（date-time） | 是 | UTC；超过后前端可直接提示过期（服务端 GET 也会判本地超时） |
| `poll_interval_seconds` | integer | 是 | 前端轮询节奏（上游给的值，缺省 2） |
| `account_id` | string | 是 | approved 时新建账号的 id；其余空串 |
| `error` | string | 是 | status 为 expired/denied/invalid 时的用户可读原因；其余空串 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数无效（40001，含空白标签）；供应商未知（40030）；该供应商不支持设备授权（40032） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 要换凭据的目标账号不存在或属于其他用户（40430） |
| 500 | 服务器内部错误（50000） |
| 502 | 额度供应商授权服务不可用或响应缺关键字段（50204） |

### GET `/api/quota/device-authorizations/{id}`

<!-- operationId: getQuotaDeviceAuthorization -->

查询设备授权会话状态

按会话 ID 查询授权进度；距上次轮询超过 poll_interval_seconds 且仍 pending 时，
服务端代为向上游轮询一次。approved 表示已自动创建额度账号（account_id 为新账号 ID）；
expired/denied/invalid 为终态，error 给用户可读原因，前端提示重新发起。
会话是本人资源，他人的会话与不存在的会话一样返回 40431。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 设备授权会话 ID（POST 返回的 id） |

**成功响应**

**200** 会话当前状态（网络抖动或上游 5xx 时保持 pending，前端按节奏再轮询即可）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 会话 ID，用于后续 GET 轮询与 DELETE 取消 |
| `provider` | string | 是 |  |
| `status` | string（enum: pending \| approved \| expired \| denied \| invalid） | 是 | pending 等待用户批准；approved 已拿到控制面密钥并创建账号； expired 验证码过期或本地超时；denied 用户拒绝/取消； invalid 设备密钥失效（上游 401/404） |
| `user_code` | string | 是 | 用户在批准页面输入的验证码 |
| `activation_url` | string | 是 | 浏览器批准页面地址（前端 target=_blank 打开，不在服务端跳转） |
| `expires_at` | string（date-time） | 是 | UTC；超过后前端可直接提示过期（服务端 GET 也会判本地超时） |
| `poll_interval_seconds` | integer | 是 | 前端轮询节奏（上游给的值，缺省 2） |
| `account_id` | string | 是 | approved 时新建账号的 id；其余空串 |
| `error` | string | 是 | status 为 expired/denied/invalid 时的用户可读原因；其余空串 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 会话不存在、已过期或属于其他用户（40431） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/quota/device-authorizations/{id}`

<!-- operationId: cancelQuotaDeviceAuthorization -->

取消设备授权会话

删除内存会话（前端关闭对话框 / 用户取消时调用）；不存在的会话返回 40431。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 设备授权会话 ID（POST 返回的 id） |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 404 | 会话不存在、已过期或属于其他用户（40431） |
| 500 | 服务器内部错误（50000） |

## members

成员与邀请管理（INT-129，仅管理员 = JWT + RequireAdmin，40305）：成员列表与改角色、邀请的创建/列表/撤销；公开侧的邀请校验在 auth 组

### GET `/api/members`

<!-- operationId: listMembers -->

列出全部成员

仅管理员（JWT + RequireAdmin，非管理员 40305；API Key 一律 40301）。
返回全部用户，按 `created_at` 升序（首个用户=最早的 admin 排最前）。
形状与 `User` 一致，含 `role`。

**鉴权**：仅 JWT

**成功响应**

**200** 成员列表（created_at 升序）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].username` | string | 是 |  |
| `items[].email` | string | 是 |  |
| `items[].avatar_url` | string | 是 | 形如 /api/avatars/:uid/:filename，未设置为空串 |
| `items[].role` | string（enum: admin \| member） | 是 | 用户角色（INT-129）：admin 可管理成员、邀请与部署设置；member 只能使用自己的资源。角色只存库，服务端每请求查库判定，不写进 JWT |
| `items[].timezone` | string | 是 | 用户 IANA 时区（INT-138）；空串表示未声明，机器人按部署配置回退 |
| `items[].effective_timezone` | string | 否 | 生效时区（INT-138）：timezone 非空时为它，否则为部署配置 bots.memory.timezone，再否则 UTC。/auth/me 会返回；成员列表不返回（省略） |
| `items[].created_at` | string（date-time） | 是 |  |
| `items[].updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/members/{uid}/role`

<!-- operationId: updateMemberRole -->

修改成员角色

仅管理员。`role` 只接受 `admin` / `member`（其他值 40001）。
把最后一个管理员降级为 member 返回 40931（判定在事务内：
`count(role='admin')=1` 且目标当前是 admin）。目标不存在 40400。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `uid` | string | 是 | 目标用户 ID |

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `role` | string（enum: admin \| member） | 是 | 用户角色（INT-129）：admin 可管理成员、邀请与部署设置；member 只能使用自己的资源。角色只存库，服务端每请求查库判定，不写进 JWT |

**成功响应**

**200** 修改后的成员

`data`：

成员（/api/members 的元素），形状与 User 完全一致

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `username` | string | 是 |  |
| `email` | string | 是 |  |
| `avatar_url` | string | 是 | 形如 /api/avatars/:uid/:filename，未设置为空串 |
| `role` | string（enum: admin \| member） | 是 | 用户角色（INT-129）：admin 可管理成员、邀请与部署设置；member 只能使用自己的资源。角色只存库，服务端每请求查库判定，不写进 JWT |
| `timezone` | string | 是 | 用户 IANA 时区（INT-138）；空串表示未声明，机器人按部署配置回退 |
| `effective_timezone` | string | 否 | 生效时区（INT-138）：timezone 非空时为它，否则为部署配置 bots.memory.timezone，再否则 UTC。/auth/me 会返回；成员列表不返回（省略） |
| `created_at` | string（date-time） | 是 |  |
| `updated_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | role 取值非法（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 404 | 用户不存在（40400） |
| 409 | 不能降级最后一个管理员（40931） |
| 500 | 服务器内部错误（50000） |

### GET `/api/invites`

<!-- operationId: listInvites -->

列出全部邀请

仅管理员。返回全部邀请，按 `created_at` 倒序；不返回 token 明文，
只回状态（撤销/使用时间）。明文 token 只在创建响应里出现一次。

**鉴权**：仅 JWT

**成功响应**

**200** 邀请列表（created_at 倒序，不含 token）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `items` | object[] | 是 |  |
| `items[].id` | string | 是 |  |
| `items[].created_by` | string | 是 | 签发邀请的管理员用户 ID |
| `items[].expires_at` | string（date-time） | 是 | 过期时间 |
| `items[].revoked_at` | string（date-time，可空） | 是 | 撤销时间；未撤销为 null |
| `items[].used_at` | string（date-time，可空） | 是 | 被使用（注册消费）时间；未使用为 null |
| `items[].used_by` | string（可空） | 是 | 使用该邀请注册的用户 ID；未使用为 null |
| `items[].created_at` | string（date-time） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 500 | 服务器内部错误（50000） |

### POST `/api/invites`

<!-- operationId: createInvite -->

创建注册邀请

仅管理员。签发一次性邀请：明文 token（`GenerateApiKeyRaw` 随机串）只在
本次响应返回一次，库里只存 SHA-256；`url` 由前端用 public_url 或
`window.location.origin` 自行拼接（`/register?invite=<token>`），服务端不回 url。
`expires_in_hours` 省略时默认 72，允许 1..720，越界 40001。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `expires_in_hours` | integer（1..720） | 否 | 有效期（小时），省略默认 72；越界 40001 |

**成功响应**

**200** 创建成功，返回邀请与一次性 token 明文

`data`：

新建邀请（Invite 全部字段 + 一次性 token 明文）。邀请投影，不含 token 明文（明文只在创建响应里出现一次）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 |  |
| `created_by` | string | 是 | 签发邀请的管理员用户 ID |
| `expires_at` | string（date-time） | 是 | 过期时间 |
| `revoked_at` | string（date-time，可空） | 是 | 撤销时间；未撤销为 null |
| `used_at` | string（date-time，可空） | 是 | 被使用（注册消费）时间；未使用为 null |
| `used_by` | string（可空） | 是 | 使用该邀请注册的用户 ID；未使用为 null |
| `created_at` | string（date-time） | 是 |  |
| `token` | string | 是 | 邀请明文 token，只在本次响应出现一次；前端拼 /register?invite=<token> |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | expires_in_hours 越界（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/invites/{id}`

<!-- operationId: revokeInvite -->

撤销邀请

仅管理员。置 `revoked_at`；幂等——已撤销的邀请再次撤销仍返回 200。
邀请 ID 不存在返回 40432。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 邀请 ID |

**成功响应**

**200** 操作成功（data 为 null）

`data` 为 null。

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 404 | 邀请不存在（40432） |
| 500 | 服务器内部错误（50000） |

## deployment

部署设置（INT-129）：public_url 网页配置（GET 仅 JWT，PUT 仅管理员）与部署级 GitHub App（B 轮实现：每套部署一个 App、Manifest 一键创建；GET 所有登录用户，其余仅管理员）

### GET `/api/deployment/settings`

<!-- operationId: getDeploymentSettings -->

读取部署设置（当前生效的 public_url 及来源）

仅 JWT（所有登录用户可读）。`public_url` 是当前生效值（trim 且去尾部 `/`）；
`public_url_source` 说明它的来源：`database`=部署设置里保存的值（优先），
`config`=config.yaml 的 `server.public_url` 兜底（数据库值为空串时），
`none`=两处都没有。

**鉴权**：仅 JWT

**成功响应**

**200** 部署设置

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `public_url` | string | 是 | 当前生效的对外访问基址（trim 且去尾部 /）；两处都没有时为空串 |
| `public_url_source` | string（enum: database \| config \| none） | 是 | public_url 来源：database=部署设置保存的值（优先）；config=config.yaml 的 server.public_url 兜底；none=未配置 |
| `updated_at` | string（date-time，可空） | 是 | 部署设置最近保存时间；数据库从未保存过该行为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/deployment/settings`

<!-- operationId: updateDeploymentSettings -->

保存部署设置（public_url）

仅管理员。校验规则：空串表示清空数据库值（回落 config）；
其余必须是 http(s) 绝对 URL、host 非空、不能带 query 或 fragment，
末尾 `/` 会被去掉；不合法返回 40001。写入数据库后回读返回。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `public_url` | string | 是 | 对外访问基址；空串=清空数据库值回落 config；其余必须是 http(s) 绝对 URL、host 非空、不带 query/fragment，末尾 / 自动去掉 |

**成功响应**

**200** 保存后的部署设置（回读）

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `public_url` | string | 是 | 当前生效的对外访问基址（trim 且去尾部 /）；两处都没有时为空串 |
| `public_url_source` | string（enum: database \| config \| none） | 是 | public_url 来源：database=部署设置保存的值（优先）；config=config.yaml 的 server.public_url 兜底；none=未配置 |
| `updated_at` | string（date-time，可空） | 是 | 部署设置最近保存时间；数据库从未保存过该行为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | public_url 不合法（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 500 | 服务器内部错误（50000） |

### GET `/api/deployment/github-app`

<!-- operationId: getGitHubAppDeployment -->

读取部署级 GitHub App 配置（所有登录用户）

INT-129 B 轮实现。返回本部署当前生效的 GitHub App 身份投影：
是否已配置、来源（数据库 > config > 未配置）、webhook URL 与安装链接等；
私钥与 webhook 密钥本体永不回显，只回「是否已设置」。

**鉴权**：仅 JWT

**成功响应**

**200** 部署级 GitHub App 配置

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `configured` | boolean | 是 | 本部署是否有 App 身份配置来源（部署设置行或 config 兜底）；私钥是否真的可读看 private_key_set |
| `source` | string（enum: database \| config \| none） | 是 | 当前生效来源；database 优先，config 是 config.yaml 兜底，none 为未配置 |
| `app_id` | string | 是 | GitHub App ID（未配置时为空串） |
| `app_slug` | string | 是 | GitHub App slug（拼 install_url 用；未配置时为空串） |
| `private_key_set` | boolean | 是 | 私钥是否已配置（本体永不回显） |
| `webhook_secret_set` | boolean | 是 | webhook 验签密钥是否已配置（本体永不回显） |
| `webhook_url` | string | 是 | 填进 GitHub App 的 Webhook URL（{public_url}/api/github/webhooks）；public_url 未配置时为空串 |
| `install_url` | string | 是 | 安装到仓库入口（https://github.com/apps/{slug}/installations/new）；slug 为空时为空串 |
| `detection_status` | string（enum: unchecked \| ok \| invalid \| failed） | 是 | unchecked=未检测；ok=App 身份认证成功；invalid=GitHub 拒绝凭证（401）；failed=检测失败（网络/限流/5xx，不是凭证无效） |
| `detection_detail` | string | 是 | 给人看的补充说明（App 名称、失败原因等），不含任何密钥 |
| `detected_at` | string（date-time，可空） | 是 | 最近一次检测完成时间；未检测为 null |
| `updated_at` | string（date-time，可空） | 是 | 数据库配置最近保存时间；source 为 config/none 时为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 已认证但无权限（40300-40399；40301=API Key 越权，40303=仅限 API Key） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/deployment/github-app`

<!-- operationId: updateGitHubAppDeployment -->

手动配置部署级 GitHub App

仅管理员（B 轮实现）：「手动配置（已有 App）」折叠区的保存入口。
app_id 必填；private_key_pem / webhook_secret 省略表示保留已有值，
首次配置必须提供私钥。凭证加密落库；保存后立即生效，无需重启。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `app_id` | string | 是 | GitHub App ID（纯数字） |
| `private_key_pem` | string（可空） | 否 | App 私钥 PEM（RSA）；省略或 null 表示保留已有私钥，首次配置必填 |
| `webhook_secret` | string（可空） | 否 | webhook 验签密钥；省略或 null 表示保留已有值，空串表示清空 |

**成功响应**

**200** 保存后的配置

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `configured` | boolean | 是 | 本部署是否有 App 身份配置来源（部署设置行或 config 兜底）；私钥是否真的可读看 private_key_set |
| `source` | string（enum: database \| config \| none） | 是 | 当前生效来源；database 优先，config 是 config.yaml 兜底，none 为未配置 |
| `app_id` | string | 是 | GitHub App ID（未配置时为空串） |
| `app_slug` | string | 是 | GitHub App slug（拼 install_url 用；未配置时为空串） |
| `private_key_set` | boolean | 是 | 私钥是否已配置（本体永不回显） |
| `webhook_secret_set` | boolean | 是 | webhook 验签密钥是否已配置（本体永不回显） |
| `webhook_url` | string | 是 | 填进 GitHub App 的 Webhook URL（{public_url}/api/github/webhooks）；public_url 未配置时为空串 |
| `install_url` | string | 是 | 安装到仓库入口（https://github.com/apps/{slug}/installations/new）；slug 为空时为空串 |
| `detection_status` | string（enum: unchecked \| ok \| invalid \| failed） | 是 | unchecked=未检测；ok=App 身份认证成功；invalid=GitHub 拒绝凭证（401）；failed=检测失败（网络/限流/5xx，不是凭证无效） |
| `detection_detail` | string | 是 | 给人看的补充说明（App 名称、失败原因等），不含任何密钥 |
| `detected_at` | string（date-time，可空） | 是 | 最近一次检测完成时间；未检测为 null |
| `updated_at` | string（date-time，可空） | 是 | 数据库配置最近保存时间；source 为 config/none 时为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | App ID 缺失或非数字（40001）、私钥不是有效 RSA PEM（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 500 | 服务器内部错误（50000） |

### POST `/api/deployment/github-app/test`

<!-- operationId: testGitHubAppDeployment -->

测试部署级 GitHub App 连接（仅管理员）

B 轮实现。对本部署生效的 App 身份做一次只读检测（用 App 私钥签 JWT 调
`GET /app`）：`ok`=认证成功，`invalid`=GitHub 拒绝该身份（401），
`failed`=网络/限流/GitHub 5xx。未配置时返回 40006。

**鉴权**：仅 JWT

**成功响应**

**200** 检测后的配置

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `configured` | boolean | 是 | 本部署是否有 App 身份配置来源（部署设置行或 config 兜底）；私钥是否真的可读看 private_key_set |
| `source` | string（enum: database \| config \| none） | 是 | 当前生效来源；database 优先，config 是 config.yaml 兜底，none 为未配置 |
| `app_id` | string | 是 | GitHub App ID（未配置时为空串） |
| `app_slug` | string | 是 | GitHub App slug（拼 install_url 用；未配置时为空串） |
| `private_key_set` | boolean | 是 | 私钥是否已配置（本体永不回显） |
| `webhook_secret_set` | boolean | 是 | webhook 验签密钥是否已配置（本体永不回显） |
| `webhook_url` | string | 是 | 填进 GitHub App 的 Webhook URL（{public_url}/api/github/webhooks）；public_url 未配置时为空串 |
| `install_url` | string | 是 | 安装到仓库入口（https://github.com/apps/{slug}/installations/new）；slug 为空时为空串 |
| `detection_status` | string（enum: unchecked \| ok \| invalid \| failed） | 是 | unchecked=未检测；ok=App 身份认证成功；invalid=GitHub 拒绝凭证（401）；failed=检测失败（网络/限流/5xx，不是凭证无效） |
| `detection_detail` | string | 是 | 给人看的补充说明（App 名称、失败原因等），不含任何密钥 |
| `detected_at` | string（date-time，可空） | 是 | 最近一次检测完成时间；未检测为 null |
| `updated_at` | string（date-time，可空） | 是 | 数据库配置最近保存时间；source 为 config/none 时为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 尚未配置 GitHub App（40006） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 500 | 服务器内部错误（50000） |

### POST `/api/deployment/github-app/manifest`

<!-- operationId: createGitHubAppManifest -->

生成 GitHub App Manifest（仅管理员）

B 轮实现。「在 GitHub 上创建 App」主按钮的第一步：服务端按本部署的
public_url 组装 manifest（原样 POST 给 GitHub 的形状），返回 GitHub
创建页地址、防 CSRF 的 state 与 manifest 本体；前端以表单 POST 跳转
`action_url`。填了 `org` 时创建页指向该组织的 Apps 设置。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `org` | string | 否 | 目标组织；填了则创建页指向该组织的 Apps 设置，省略为个人 App |

**成功响应**

**200** Manifest 与跳转信息

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `action_url` | string | 是 | GitHub 创建页；默认 https://github.com/settings/apps/new，填了 org 时 https://github.com/organizations/{org}/settings/apps/new |
| `state` | string | 是 | 防 CSRF 的一次性 state，GitHub 回跳时原样带回 |
| `manifest` | object | 是 | 原样 POST 给 GitHub 的 App Manifest（形状由 GitHub Manifest 流程约定） |
| `manifest.name` | string | 是 | App 名称（取自部署，如 Fast Ship） |
| `manifest.url` | string | 是 | 主页地址 = public_url |
| `manifest.hook_attributes` | object | 是 |  |
| `manifest.hook_attributes.url` | string | 是 | Webhook URL = {public_url}/api/github/webhooks |
| `manifest.hook_attributes.active` | boolean | 是 | 恒为 true（不用 enum[true]：oapi-codegen 会生成与 api.True 撞名的常量） |
| `manifest.redirect_url` | string | 是 | 创建完成回跳地址 = {public_url}/settings/integrations/github/callback |
| `manifest.setup_url` | string | 是 | 安装后跳转地址 = {public_url}/settings/integrations |
| `manifest.public` | boolean | 是 | 恒为 false（非公开 App；同上不用 enum[false] 避免生成撞名常量） |
| `manifest.default_events` | string[]（enum: pull_request） | 是 |  |
| `manifest.default_permissions` | object | 是 | PR 审查所需的最小权限集（结论用 commit status，不用 Checks API） |
| `manifest.default_permissions.pull_requests` | string（enum: write） | 是 |  |
| `manifest.default_permissions.issues` | string（enum: write） | 是 |  |
| `manifest.default_permissions.statuses` | string（enum: write） | 是 |  |
| `manifest.default_permissions.contents` | string（enum: read） | 是 |  |
| `manifest.default_permissions.metadata` | string（enum: read） | 是 |  |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 412 | 尚未配置 public_url，无法组装 Manifest（41202） |
| 500 | 服务器内部错误（50000） |

### POST `/api/deployment/github-app/manifest/complete`

<!-- operationId: completeGitHubAppManifest -->

完成 Manifest 创建（GitHub 回跳后换取凭证，仅管理员）

B 轮实现。GitHub 创建 App 后回跳 `/settings/integrations/github/callback?code&state`，
前端带 JWT 把 code/state 交给本端点：服务端用 code 向 GitHub 换 App 凭证，
加密落库为部署级身份并返回配置投影。失败时前端显示明确文案与「重新创建」。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `code` | string | 是 | GitHub 回跳携带的一次性 code，用于换取 App 凭证 |
| `state` | string | 是 | 发起创建时签发的 state，服务端核对一致才继续 |

**成功响应**

**200** 完成后的部署级 GitHub App 配置

`data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `configured` | boolean | 是 | 本部署是否有 App 身份配置来源（部署设置行或 config 兜底）；私钥是否真的可读看 private_key_set |
| `source` | string（enum: database \| config \| none） | 是 | 当前生效来源；database 优先，config 是 config.yaml 兜底，none 为未配置 |
| `app_id` | string | 是 | GitHub App ID（未配置时为空串） |
| `app_slug` | string | 是 | GitHub App slug（拼 install_url 用；未配置时为空串） |
| `private_key_set` | boolean | 是 | 私钥是否已配置（本体永不回显） |
| `webhook_secret_set` | boolean | 是 | webhook 验签密钥是否已配置（本体永不回显） |
| `webhook_url` | string | 是 | 填进 GitHub App 的 Webhook URL（{public_url}/api/github/webhooks）；public_url 未配置时为空串 |
| `install_url` | string | 是 | 安装到仓库入口（https://github.com/apps/{slug}/installations/new）；slug 为空时为空串 |
| `detection_status` | string（enum: unchecked \| ok \| invalid \| failed） | 是 | unchecked=未检测；ok=App 身份认证成功；invalid=GitHub 拒绝凭证（401）；failed=检测失败（网络/限流/5xx，不是凭证无效） |
| `detection_detail` | string | 是 | 给人看的补充说明（App 名称、失败原因等），不含任何密钥 |
| `detected_at` | string（date-time，可空） | 是 | 最近一次检测完成时间；未检测为 null |
| `updated_at` | string（date-time，可空） | 是 | 数据库配置最近保存时间；source 为 config/none 时为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 创建流程已过期或已失效（40009，state 不存在/过期/重放/换管理员）；GitHub 授权码已过期或已被使用（40010） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 500 | 服务器内部错误（50000） |
| 502 | GitHub App Manifest 转换失败（50205） |

### GET `/api/deployment/integrations`

<!-- operationId: listForgeIntegrations -->

列出部署级代码仓集成（仅管理员）

INT-166 实现。首条恒为内置 `github-default`（只读映射到部署级 GitHub App
配置，builtin=true），后接 forgejo 集成行。token 与 webhook_secret 本体
永不回显，只回「是否已设置」标记。

**鉴权**：仅 JWT

**成功响应**

**200** 集成列表（首条 github-default）

`data` 为数组。

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 内置集成固定为 github-default；forgejo 行为 forge_integrations 行 ID |
| `kind` | string（enum: github \| forgejo） | 是 | github=内置 GitHub App（只读映射）；forgejo=机器人账号 + 仓库范围 PAT |
| `name` | string | 是 | 显示名 |
| `base_url` | string | 是 | Forgejo 实例基址（内置集成为 https://github.com） |
| `api_base_url` | string | 是 | API 基址（forgejo 保存时已推导展开；内置集成为部署 GitHub App 的 api_base_url） |
| `api_base_url_derived` | boolean | 是 | true=api_base_url 是按 {base_url}/api/v1 推导的展开值（改 base_url 时跟随 重新推导）；false=管理员显式钉死的端点（不随 base_url 变化）。内置集成恒 false。 |
| `token_set` | boolean | 是 | 凭证是否已配置（内置=GitHub App 身份可用；本体永不回显） |
| `webhook_secret_set` | boolean | 是 | webhook 验签密钥是否已配置（本体永不回显） |
| `has_ca_cert` | boolean | 是 | 是否配置了自定义 CA（本体永不回显） |
| `allow_private_network` | boolean | 是 | 是否放行本集成配置主机解析到私网地址（http 同样仅在开启时可用） |
| `builtin` | boolean | 是 | 内置集成恒为 true，不可修改、不可删除、不可走 test 端点 |
| `unavailable` | boolean | 否 | omitempty；仅内置 github-default 可能出现：true 表示内置投影读取 失败（如 App 密文解不开），该条目仅作占位展示、凭证状态不可信， 请检查部署级 GitHub App 配置。forgejo 行恒无此字段。 |
| `created_at` | string（date-time，可空） | 是 | 创建时间；内置集成无落库行，为 null |
| `updated_at` | string（date-time，可空） | 是 | 最近保存时间；内置集成为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 500 | 服务器内部错误（50000） |

### POST `/api/deployment/integrations`

<!-- operationId: createForgeIntegration -->

新建 forgejo 集成（仅管理员）

仅管理员。本期只接受 forgejo 种类（内置 GitHub App 走 deployment/github-app
端点，请求不设 kind 字段）。base_url 必填并做 SSRF 校验（含保存时 DNS
解析校验）；api_base_url 省略时按 `{base_url}/api/v1` 推导；token 必填
（仓库范围 PAT）；webhook_secret / ca_cert_pem 可选；token 与
webhook_secret 加密落库、永不回显。allow_private_network=true 放行该
集成配置主机解析到私网地址（http 基址同样只在开启时可用）。

**鉴权**：仅 JWT

**请求体**

`application/json`，必填。

新建 forgejo 集成请求；本期不接受 github 种类（内置 App 走 deployment/github-app）

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string | 是 | 显示名（必填） |
| `base_url` | string | 是 | Forgejo 实例基址；必须 https（开启 allow_private_network 后可用 http）且解析不到内网地址 |
| `api_base_url` | string（可空） | 否 | API 基址；省略或 null 按 {base_url}/api/v1 推导 |
| `token` | string | 是 | 仓库范围 PAT（必填）；加密落库、只写不回显 |
| `webhook_secret` | string（可空） | 否 | webhook 验签密钥（可选）；加密落库、只写不回显 |
| `ca_cert_pem` | string（可空） | 否 | 自定义 CA（PEM，可选）；追加系统根证书池 |
| `allow_private_network` | boolean | 否 | 放行该集成配置主机解析到私网地址；缺省 false |

**成功响应**

**200** 创建后的集成投影

`data`：

部署级代码仓集成投影（INT-166）；内置 github-default 与 forgejo 行同形

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 内置集成固定为 github-default；forgejo 行为 forge_integrations 行 ID |
| `kind` | string（enum: github \| forgejo） | 是 | github=内置 GitHub App（只读映射）；forgejo=机器人账号 + 仓库范围 PAT |
| `name` | string | 是 | 显示名 |
| `base_url` | string | 是 | Forgejo 实例基址（内置集成为 https://github.com） |
| `api_base_url` | string | 是 | API 基址（forgejo 保存时已推导展开；内置集成为部署 GitHub App 的 api_base_url） |
| `api_base_url_derived` | boolean | 是 | true=api_base_url 是按 {base_url}/api/v1 推导的展开值（改 base_url 时跟随 重新推导）；false=管理员显式钉死的端点（不随 base_url 变化）。内置集成恒 false。 |
| `token_set` | boolean | 是 | 凭证是否已配置（内置=GitHub App 身份可用；本体永不回显） |
| `webhook_secret_set` | boolean | 是 | webhook 验签密钥是否已配置（本体永不回显） |
| `has_ca_cert` | boolean | 是 | 是否配置了自定义 CA（本体永不回显） |
| `allow_private_network` | boolean | 是 | 是否放行本集成配置主机解析到私网地址（http 同样仅在开启时可用） |
| `builtin` | boolean | 是 | 内置集成恒为 true，不可修改、不可删除、不可走 test 端点 |
| `unavailable` | boolean | 否 | omitempty；仅内置 github-default 可能出现：true 表示内置投影读取 失败（如 App 密文解不开），该条目仅作占位展示、凭证状态不可信， 请检查部署级 GitHub App 配置。forgejo 行恒无此字段。 |
| `created_at` | string（date-time，可空） | 是 | 创建时间；内置集成无落库行，为 null |
| `updated_at` | string（date-time，可空） | 是 | 最近保存时间；内置集成为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数缺失或非法（40001）：name/token 缺失、基址不是合法 https 绝对 URL、基址解析到内网地址、CA PEM 不可解析等 |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 500 | 服务器内部错误（50000） |

### GET `/api/deployment/integrations/{id}`

<!-- operationId: getForgeIntegration -->

读取单个集成投影（仅管理员）

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 代码仓集成 ID（内置 github-default 或 forge_integrations 行 ID） |

**成功响应**

**200** 集成投影

`data`：

部署级代码仓集成投影（INT-166）；内置 github-default 与 forgejo 行同形

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 内置集成固定为 github-default；forgejo 行为 forge_integrations 行 ID |
| `kind` | string（enum: github \| forgejo） | 是 | github=内置 GitHub App（只读映射）；forgejo=机器人账号 + 仓库范围 PAT |
| `name` | string | 是 | 显示名 |
| `base_url` | string | 是 | Forgejo 实例基址（内置集成为 https://github.com） |
| `api_base_url` | string | 是 | API 基址（forgejo 保存时已推导展开；内置集成为部署 GitHub App 的 api_base_url） |
| `api_base_url_derived` | boolean | 是 | true=api_base_url 是按 {base_url}/api/v1 推导的展开值（改 base_url 时跟随 重新推导）；false=管理员显式钉死的端点（不随 base_url 变化）。内置集成恒 false。 |
| `token_set` | boolean | 是 | 凭证是否已配置（内置=GitHub App 身份可用；本体永不回显） |
| `webhook_secret_set` | boolean | 是 | webhook 验签密钥是否已配置（本体永不回显） |
| `has_ca_cert` | boolean | 是 | 是否配置了自定义 CA（本体永不回显） |
| `allow_private_network` | boolean | 是 | 是否放行本集成配置主机解析到私网地址（http 同样仅在开启时可用） |
| `builtin` | boolean | 是 | 内置集成恒为 true，不可修改、不可删除、不可走 test 端点 |
| `unavailable` | boolean | 否 | omitempty；仅内置 github-default 可能出现：true 表示内置投影读取 失败（如 App 密文解不开），该条目仅作占位展示、凭证状态不可信， 请检查部署级 GitHub App 配置。forgejo 行恒无此字段。 |
| `created_at` | string（date-time，可空） | 是 | 创建时间；内置集成无落库行，为 null |
| `updated_at` | string（date-time，可空） | 是 | 最近保存时间；内置集成为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 404 | 集成不存在（40435） |
| 500 | 服务器内部错误（50000） |

### PUT `/api/deployment/integrations/{id}`

<!-- operationId: updateForgeIntegration -->

部分更新集成（仅管理员）

仅管理员。部分更新：省略的字段保持不变。base_url / api_base_url 变更时
重新做 SSRF 校验（含 DNS）；allow_private_network 关闭会按公网口径
复核现有基址。token / webhook_secret 省略、null 或空串 = 保留旧值；
ca_cert_pem 省略 = 保留、空串 = 清空；api_base_url 空串 = 重置为
`{base_url}/api/v1`。内置 github-default 不可通过本端点修改（40001）。
base_url 归一主机变更等同迁移全部项目绑定：仍有项目引用时拒绝
（40935，消息列引用项目名），须先解除项目绑定或走「新建集成 +
项目逐个改绑」的显式迁移路径。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 代码仓集成 ID（内置 github-default 或 forge_integrations 行 ID） |

**请求体**

`application/json`，必填。

部分更新请求；省略的字段保持不变

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string（可空） | 否 | 显示名；null 或空串拒绝（name 不允许为空） |
| `base_url` | string（可空） | 否 | 基址变更时重新校验（含 DNS）；null 保留 |
| `api_base_url` | string（可空） | 否 | null 保留（存量恰为旧 base_url 推导值时随新 base_url 重新推导）；空串重置为 {base_url}/api/v1；其余重新校验 |
| `token` | string（可空） | 否 | 新 PAT；留空（省略、null 或空串）= 保留现值；不支持单独清空，需删除集成重建 |
| `webhook_secret` | string（可空） | 否 | 新验签密钥；省略或 null = 保留现值；空串 = 清空已存密钥；其余替换并加密落库 |
| `ca_cert_pem` | string（可空） | 否 | null = 保留；空串 = 清空；其余替换并校验 PEM |
| `allow_private_network` | boolean（可空） | 否 | null = 保留；变更后按新开关复核现有基址 |

**成功响应**

**200** 更新后的集成投影

`data`：

部署级代码仓集成投影（INT-166）；内置 github-default 与 forgejo 行同形

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 内置集成固定为 github-default；forgejo 行为 forge_integrations 行 ID |
| `kind` | string（enum: github \| forgejo） | 是 | github=内置 GitHub App（只读映射）；forgejo=机器人账号 + 仓库范围 PAT |
| `name` | string | 是 | 显示名 |
| `base_url` | string | 是 | Forgejo 实例基址（内置集成为 https://github.com） |
| `api_base_url` | string | 是 | API 基址（forgejo 保存时已推导展开；内置集成为部署 GitHub App 的 api_base_url） |
| `api_base_url_derived` | boolean | 是 | true=api_base_url 是按 {base_url}/api/v1 推导的展开值（改 base_url 时跟随 重新推导）；false=管理员显式钉死的端点（不随 base_url 变化）。内置集成恒 false。 |
| `token_set` | boolean | 是 | 凭证是否已配置（内置=GitHub App 身份可用；本体永不回显） |
| `webhook_secret_set` | boolean | 是 | webhook 验签密钥是否已配置（本体永不回显） |
| `has_ca_cert` | boolean | 是 | 是否配置了自定义 CA（本体永不回显） |
| `allow_private_network` | boolean | 是 | 是否放行本集成配置主机解析到私网地址（http 同样仅在开启时可用） |
| `builtin` | boolean | 是 | 内置集成恒为 true，不可修改、不可删除、不可走 test 端点 |
| `unavailable` | boolean | 否 | omitempty；仅内置 github-default 可能出现：true 表示内置投影读取 失败（如 App 密文解不开），该条目仅作占位展示、凭证状态不可信， 请检查部署级 GitHub App 配置。forgejo 行恒无此字段。 |
| `created_at` | string（date-time，可空） | 是 | 创建时间；内置集成无落库行，为 null |
| `updated_at` | string（date-time，可空） | 是 | 最近保存时间；内置集成为 null |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 参数非法（40001）：内置集成不可修改、基址校验失败、CA PEM 不可解析等 |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 404 | 集成不存在（40435） |
| 500 | 服务器内部错误（50000） |

### DELETE `/api/deployment/integrations/{id}`

<!-- operationId: deleteForgeIntegration -->

删除集成（仅管理员）

仅管理员。内置 github-default 不可删除（40001）；仍被项目
（code_integration_id / public_integration_id 任一）引用时拒绝（40935），
消息列出引用项目名。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 代码仓集成 ID（内置 github-default 或 forge_integrations 行 ID） |

**成功响应**

**200** 删除成功（data 为 null）

`data`：any

响应负载；形状随端点而变，由 allOf 组合的兄弟 schema 收窄

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 内置集成不可删除（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 404 | 集成不存在（40435） |
| 409 | 仍被项目引用（40935，消息含项目名） |
| 500 | 服务器内部错误（50000） |

### POST `/api/deployment/integrations/{id}/test`

<!-- operationId: testForgeIntegration -->

连接检测（仅管理员）

仅管理员。用配置的 token 调 Forgejo `GET {api_base_url}/version` 与
`GET {api_base_url}/user`，返回实例版本与机器人账号名。检测结论随响应
返回、不落库；token 被拒绝时 status=invalid，网络/5xx 时 status=failed，
都不是 500。内置 github-default 走本端点报错（40001），引导用已有的
`POST /api/deployment/github-app/test`。

**鉴权**：仅 JWT

**路径参数**

| 名称 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 代码仓集成 ID（内置 github-default 或 forge_integrations 行 ID） |

**成功响应**

**200** 检测结论（status 含失败口径，非 500）

`data`：

Forgejo 连接检测结论；不落库，只随本次响应返回

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `status` | string（enum: unchecked \| ok \| invalid \| failed） | 是 | ok=version+user 均成功；invalid=凭证被拒（401/403）；failed=网络/5xx 等检测失败。unchecked 仅为口径对齐，本端点不会返回 |
| `version` | string | 是 | Forgejo 实例版本（检测成功时非空） |
| `username` | string | 是 | 机器人账号名（检测成功时非空） |
| `detail` | string | 是 | 给人看的补充说明（失败原因等），不含任何密钥 |

**错误**

| HTTP | 说明 |
| --- | --- |
| 400 | 内置集成不支持本端点（40001） |
| 401 | 未提供或提供无效凭证（40100-40199） |
| 403 | 非管理员（40305） |
| 404 | 集成不存在（40435） |
| 500 | 服务器内部错误（50000） |
