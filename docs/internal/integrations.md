# GitHub App 身份（INT-93 → INT-119 → INT-129）

Fast Ship 用 GitHub App 身份代发 PR 审查评论与行内 review、写 commit status，
并接收 App 的 webhook 投递。INT-119 曾把身份做成**用户级**（每个用户自己建 App），INT-129 收回
**部署级**：一套部署一个 App，管理员在网页上配置，成员只看状态。

部署地址（`public_url`）与 manifest 一键创建的完整流程见
`docs/internal/deployment-settings.md`；本文只描述身份本身、检测与 webhook。

## 解析优先级

读侧唯一入口 `IntegrationService.resolveConfig()`（不接收 userID）：

1. `deployment_settings` 有可用 App 身份（`app_id` 非空）→ `source = database`。私钥不可解析（或为空）时仍算 `database` 来源但 `usable = false`：身份来源与「能不能用」是两个问题，页面要能打开让管理员改存新配置。
2. `config.yaml` 的 `github_app`（`app_id` 与 `private_key_path` 都非空）→ `source = config`
3. 都没有 → `source = none`，PR 评审与 PR 代发端点不可用

不变量：

- 数据库身份存在时永远以它为准，包括 `webhook_secret` 为空（→ webhook 503），**不回落** config。
- 密文解密失败明确 50000，不静默换另一把凭证（与 `github-credentials.md` 同一规则）。
- `github_app` 是**部署级兜底**，也是本地自测路径：`github_app.fake` 只在 `server.mode=debug`
  允许 true，此时注入内存假实现，身份与 webhook 密钥都只走 config（不读数据库）。

## 存储

`model.DeploymentSettings` 的 App 列（单行表，主键 `default`）：`app_id`、`app_slug`
（明文，都不是密钥，用于展示与生成 `https://github.com/apps/{slug}/installations/new`）、
`config_encrypted`（`{private_key_pem, webhook_secret}` 的 AES-256-GCM 密文，密钥
`encryption.key`）、检测三列与时间戳。加解密只发生在 service 层。

PUT 是整组保存：`app_id` 必填且纯数字；`private_key_pem` 省略/null 保留已存私钥；
`webhook_secret` 省略/null 保留，空串清空。「读旧值 → 合并省略字段 → 加密 → 写库」在
**一个短事务**内完成（单连接池下闭包内只用 tx 句柄，见 `database.md`），检测放在提交之后
并按密文版本做条件更新——慢检测覆盖不了后来保存的新配置。私钥与 webhook 密钥只写不读：
响应只回 `private_key_set` / `webhook_secret_set`。

`configured` 表示已有可用 App 身份（`source != none`）；只配了 config webhook 密钥的部署
`configured = false`，但 webhook 验签仍生效。`private_key_set` 表示私钥当前真的可读
（config 来源时文件读不到就是 false）。

## 保存即生效

`githubapp.Clients.ClientFor(ctx)` 是 PR 审查链路取客户端的唯一入口：按
`app_id + PEM` 的 sha256 指纹缓存**一份** `InstallationTokenStore + REST client`，
配置变化后下一次调用自动重建。未配置时返回 50000「GitHub App 未配置」。
`StaticClients` 恒返回同一 client，供 fake 自测与测试注入。

INT-119 的「按用户缓存、按用户解析」在 INT-129 全部删除：一套部署只有一份身份。

## webhook 验签

`POST /api/github/webhooks` 是公开端点，只认 `X-Hub-Signature-256`
（HMAC-SHA256 常量时间比较，见 `pkg/githubapp/webhook.go`，空请求体一律拒绝）：

- 当前生效密钥为空 → 503（50300，不接受未签名请求）
- 签名缺失/不匹配 → 401（40107）
- 通过 → 202，事件交给 `ReviewService.HandleGitHubWebhook`

`X-GitHub-Hook-Installation-Target-ID` 不再参与身份归属（只有一份身份），
只当无意义的头忽略。INT-119 的多候选验签、同 App 用户行接管 config、多命中拒绝
（「无法确定集成身份」）随用户级身份一起删除。INT-135 的新审查表
（`review_runs` 等）直接不带身份分区列——一套部署只有一份身份。

## 检测

`githubapp.Verify` 用 App 私钥签 RS256 JWT 调 `GET {api}/app`（只读，不创建任何 GitHub 资源）：
200 → `ok`（detail 带 App 名称，并把响应里的 `slug` 记进数据库——手填（PUT）或 config 配置的
App 因此也能生成安装链接；config 来源没有数据库 App 身份时只补写 `app_slug` 一列，`app_id`
仍为空，来源仍是 `config`）；401 与私钥解析失败 → `invalid`；其他状态码/网络错误 → `failed`。
detail 用固定文案，不拼底层 `err.Error()`，不含任何密钥。

保存、「测试连接」与 manifest 完成后都同步跑一次检测：失败或无效**仍然保存**
（HTTP 200，状态落库），和 GitHub Token 库的检测语义一致，避免用户因为 GitHub
一时不可用而丢配置。`source = config` 时没有数据库行可落库，检测结果只出现在本次响应里。

`github_app.api_base_url`（`FAST_SHIP_GITHUB_APP_API_BASE_URL`）把检测、安装令牌换新与
manifest conversions 的 API base 指到别处；默认空 = `https://api.github.com`。

## 权限

manifest 一键创建申请的权限：`pull_requests: write`（行内 review）、
`issues: write`（顶层评论）、`statuses: write`（commit status——
`代码审查`/`视觉审查` 两个 context 由它支撑，**不是** checks）、
`contents: read`、`metadata: read`。GitHub 对权限变更是显式确认制：
新装安装或在 manifest 之后升级过权限的 App，已有安装需要在
GitHub 上「Review request / 重新接受权限」后新权限才生效。

## 接口

| 端点 | 鉴权 | 说明 |
| --- | --- | --- |
| `GET /api/deployment/github-app` | 登录用户 | 生效身份投影（含 `install_url`、`webhook_url`），不打 GitHub |
| `PUT /api/deployment/github-app` | 管理员 | 整组保存后检测一次 |
| `POST /api/deployment/github-app/test` | 管理员 | 对当前生效配置再检测一次；未配置 → 40006 |
| `POST /api/deployment/github-app/manifest` | 管理员 | 生成 manifest 表单参数，见 `deployment-settings.md` |
| `POST /api/deployment/github-app/manifest/complete` | 管理员 | 用 `code` 换凭证并保存 |

旧的 `/api/integrations/github-app*` 三条在 INT-129 删除（前端 `lib/api/integrations.ts`
同步改为 `lib/api/deployment.ts`）。

## 页面

- 设置 → 集成（`web/src/routes/settings/integrations.tsx`）：
  管理员 = 「在 GitHub 上创建 App」主按钮 + 组织名输入，原三字段表单收进
  「手动配置（已有 App）」折叠区；成员 = 状态徽章、检测详情与「安装到仓库」（`install_url`）。
- 设置 → 部署（`web/src/routes/settings/deployment.tsx`）：`public_url`，管理员。
- `/settings/integrations/github/callback`：manifest 回跳页，管理员。

新增一类外部服务的做法：加一条部署设置投影 + 一个 `/api/deployment/<provider>` 子资源 +
页面上一张卡片；不做通用 provider 框架，避免为单个实现先造抽象。
