# 部署设置与部署级 GitHub App（INT-129）

Fast Ship 是自部署软件，没有厂商共用的 GitHub App，也没有厂商域名。INT-129
把「外部服务身份」和「部署地址」都收拢成**部署级**配置：一套部署一个 GitHub App、
一份 `public_url`，由管理员在网页上配置，成员不填任何东西。

## 两张表

### `deployment_settings`（单行）

主键固定为 `default`（`model.DeploymentSettingsID`）。单行表而不是 KV 表：
这里每一项都是「这套部署」的唯一取值，多行没有语义。

| 列 | 用途 |
| --- | --- |
| `public_url` | 网页配置的对外基址；空串表示「以 `config.yaml` 为准」 |
| `app_id` / `app_slug` | 部署级 GitHub App 的数字 ID 与 slug（slug 用于安装链接） |
| `config_encrypted` | `{private_key_pem, webhook_secret}` 的 AES-256-GCM 密文，密钥 `encryption.key` |
| `detection_status` / `detection_detail` / `detected_at` | 最近一次连接检测结论 |

App 的四个列与 `public_url` 同表：它们都是「这套部署」的身份，分开建表只会
多一层 join。`app_id` 与 `slug` 是明文（都不是密钥，且要用来生成链接）。

### `invites`

一次性邀请凭证：`token_hash`（SHA-256，明文只在创建响应里出现一次）、
`created_by`、`expires_at`、`revoked_at`、`used_by`、`used_at`。
`users.role` 与邀请的语义见 `auth.md`。

`integrations` 表（INT-119 的用户级 App 身份）在 INT-129 删除：本仓库的部署从未有人
配置过每用户 App，启动迁移直接 `DROP TABLE`（`cmd/server/migrate_drop_integrations.go`）。
表里还有行时该迁移**拒绝启动**：删表会不可逆地丢掉别人的加密私钥与 webhook 密钥，
而「多个用户各配一个 App 时该用谁」属于产品决策，不在升级里替用户选。运维备份数据库、
确认改由「设置 → 集成」重新配置后，设 `FAST_SHIP_DROP_USER_INTEGRATIONS=1` 重启即可。
`automation_runs.app_user_id` 等列按原样保留、
一律写空串（`docs/internal/pr-review.md`），避免再做一次 SQLite 重建主键的迁移。

## public_url：唯一读取入口

`service.DeploymentSettingsService.PublicURL(ctx)` 是读 `public_url` 的唯一入口，
按 **数据库非空值 > `server.public_url`（config.yaml / `FAST_SHIP_SERVER_PUBLIC_URL`）> 空串**
解析，读侧统一 trim 并去掉末尾 `/`。

三个消费点都经注入的 `PublicURLSource` 接口取值，不直接读 `cfg`：

| 消费点 | 用途 |
| --- | --- |
| `AutomationService.PublicURLOK` / `requirePublicURL` | 启用自动化的前置条件（41202） |
| `AutomationScheduler.launchThreadForRun` | 自动化运行提示词里的回报端点 |
| `IntegrationService.project` | `webhook_url` 与 manifest 里的各个 URL |

未注入时各 service 用默认实现（只读 config），既有测试与构造调用因此不需要改。

数据库读失败**静默回落** config：取不到网页配置不该让派发整体失败。

### 写入校验

`PUT /api/deployment/settings` 仅管理员。请求值必须能解析为 `http` / `https`
绝对 URL、host 非空、不带 query 与 fragment；末尾 `/` 自动去掉；空串合法，
表示「清空数据库值、回落 config」。不合法 40001。

`GET` 对全部登录用户开放（成员也要能看到这份部署的对外地址），返回当前生效值
与来源（`database` / `config` / `none`），页面据此标注「改动可能被 config 覆盖」。

## 部署级 GitHub App

### 解析优先级

`IntegrationService.resolveConfig()`（不再接收 userID）：
`deployment_settings` 有 App 身份（`app_id` 非空且私钥可解析）→ `database`；
否则 `config.yaml` 的 `github_app`（`app_id` 与 `private_key_path` 都非空）→ `config`；
都没有 → `none`。

不变量：

- 数据库身份存在时永远以它为准，包括 `webhook_secret` 为空（→ webhook 503），**不回落** config。
- 密文解密失败明确 50000，不静默换另一把凭证。
- `github_app.fake` 仍只允许 `server.mode=debug`，此时身份与 webhook 密钥都只走 config（不读数据库）。

### 一键创建（App Manifest）

`POST /api/deployment/github-app/manifest`（管理员）在服务端拼好 manifest 并返回
三样东西：表单提交地址 `action_url`、一次性 `state`、manifest 本体。前端生成一个
隐藏的 POST 表单直接提交给 GitHub（`manifest` 字段是 JSON 字符串，`state` 单独一个字段）。

- `public_url` 未配置 → 41202（manifest 里的每个 URL 都依赖它）。
- GitHub 创建完 App 后回跳 `{public_url}/settings/integrations/github/callback?code=...&state=...`，
  SPA 路由 `/settings/integrations/github/callback` 带 JWT 调
  `POST /api/deployment/github-app/manifest/complete`。
- 服务端校验 `state`（进程内一次性记录，10 分钟过期，记录发起的管理员 ID——
  换管理员来提交同样拒绝），再调 `POST {api}/app-manifests/{code}/conversions`，
  只保存 `id`、`slug`、`pem`、`webhook_secret`（丢弃 `client_id` / `client_secret`），
  紧接一次连接检测，返回部署级投影。前端拿 `install_url` 跳安装页。
- 失败分型：`state` 无效/过期 40009、GitHub 授权码 404/422 → 40010、其余上游失败 50205。
  页面文案要能让用户知道「重新创建」是可行的恢复动作。

manifest 内容（`name` 里的 host 取自 `public_url`）：`hook_attributes.url = {public_url}/api/github/webhooks`、
`redirect_url = {public_url}/settings/integrations/github/callback`、
`setup_url = {public_url}/settings/integrations`、`public: false`、
`default_events: [pull_request]`、权限 `pull_requests/issues/checks: write` 与
`contents/metadata: read`。

### 读取与保存的权限分层

| 端点 | 谁可以 |
| --- | --- |
| `GET /api/deployment/github-app` | 全部登录用户（成员只看状态，用来显示「安装到仓库」） |
| `PUT /api/deployment/github-app` | 管理员 |
| `POST /api/deployment/github-app/test` | 管理员 |
| `POST /api/deployment/github-app/manifest` | 管理员 |
| `POST /api/deployment/github-app/manifest/complete` | 管理员 |

GET 不触发任何 GitHub 请求。页面按角色分叉：管理员看到「在 GitHub 上创建 App」
（主路径）与「手动配置（已有 App）」折叠区；成员只看到状态与「安装到仓库」。

### 单身份带来的简化

INT-119 为「每个用户一份 App」付出的复杂度在 INT-129 全部删掉：
webhook 多候选验签、用户行与 config 的裁决、`(provider, app_id)` 唯一约束与 40929、
`githubapp.Clients` 的按用户缓存。现在：

- `githubapp.Clients.ClientFor(ctx)` 只缓存**一份**身份，按 `app_id + PEM` 指纹失效重建。
- webhook 只有一把密钥：空 → 503（50300，不接受未签名请求），签名不匹配 → 401（40107）。
- 手填或 config 配置的 App 也能生成安装链接：连接检测（`GET /app`）顺带记下 `slug`。
  手填的 App 身份有自己的行，slug 直接写在行上；纯 config 来源（行里没有 App 身份）只补写
  `app_slug` 一列，`app_id` 仍为空、来源仍是 `config`，投影仍从行里读 slug——成员不触发检测
  也能看到「安装到仓库」。

`Config` 的 `github_app.api_base_url`（`FAST_SHIP_GITHUB_APP_API_BASE_URL`）把
`Verify`、安装令牌换新与 manifest conversions 的 API base 指到别处——本地端到端
用 mock GitHub 走通创建流程靠它，默认空 = `https://api.github.com`。

## 前端

| 路由 | 可见性 | 内容 |
| --- | --- | --- |
| `/settings/deployment` | 管理员 | `public_url` 表单、来源徽章、「使用当前地址」 |
| `/settings/members` | 管理员 | 成员角色升降级、邀请创建/复制/撤销 |
| `/settings/integrations` | 全部登录用户 | 管理员：创建 App + 手动配置；成员：状态 + 安装按钮 |
| `/settings/integrations/github/callback` | 管理员 | 用 `code`+`state` 完成创建并跳安装页 |

管理员专属的路由不给非管理员渲染内容，而是显示「需要管理员权限」的空状态——
直接访问 URL 也不该白屏。设置页侧栏同样按 `user.role` 过滤这两项。

## 已知限制

- `state` 存进程内：多副本部署不适用（本项目一直是单进程，见 `pr-review.md` 已知限制）。
- manifest 流程依赖 GitHub 真实页面，无头环境只能截到「创建中 / 失败」与 mock 下的成功路径，
  真实创建无法自动化验证。
