# 外部服务集成（INT-119）

「集成」是**用户级**配置：每个登录用户在「设置 → 集成」配置自己的 GitHub App
（App ID、私钥 PEM、webhook 验签密钥），只影响自己的自动化与 PR 评审链路，
用户之间互不可见、互不覆盖。保存即生效，无需重启。

## 归属与来源优先级

- `integrations` 表以 `(user_id, provider)` 唯一，一行属于一个用户；本期只有
  `provider = 'github_app'`。
- 读侧唯一入口是 `IntegrationService.resolveConfig(userID)`：
  1. 该用户的数据库行 → `source = database`
  2. `config.yaml` 的 `github_app`（`app_id` 与 `private_key_path` 都非空才生效）→ `source = config`
  3. 都没有 → `source = none`，该用户的 PR 评审不可用

不变量：

- 该用户的数据库行存在时永远以它为准，包括 `webhook_secret` 为空（→ webhook 503），**不回落** config。
- 数据库行密文解密失败 → 50000 明确报错，不回落 config（与 `github-credentials.md` 的凭证规则一致：
  不静默换另一把凭证）。
- `config.yaml` 的 `github_app` 是**部署级兜底**，所有尚未保存集成配置的用户共用它——
  保留既有单用户部署的升级路径，也方便本地自测；用户一旦保存，就只看自己的行。
- config 只配了 `webhook_secret`（没有 App 身份）时该用户 `source = none`，但 webhook 仍可用它验签
  （验签不依赖 App 身份）。
- `github_app.fake` 只允许在 `server.mode=debug` 打开，仍是 config 自测路径（身份与 webhook 密钥都不读库）。

## 存储

`model.Integration`：`user_id`（外键 `ON DELETE CASCADE`）、`provider`、`app_id`（明文，
App ID 不是密钥，用于 webhook 定位与页面展示）、`config_encrypted`（整组
`{app_id, private_key_pem, webhook_secret}` JSON 的 AES-256-GCM 密文，密钥 `encryption.key`）、
检测三列与时间戳；`(user_id, provider)` 唯一，`(provider, app_id)` 也唯一——一个 App ID
在库里最多属于一个用户（重复保存返回 40929「该 App ID 已被其他用户配置」）。
加解密只发生在 service 层。

PUT 是整组保存：`app_id` 必填且纯数字；`private_key_pem` 省略/null 保留已存私钥；
`webhook_secret` 省略/null 保留，空串清空。该用户首次保存（还没有数据库行）时，省略的私钥
与密钥会把 config 的对应现值收编进行内——行一旦存在就不再读 config，不收编会写出一个看似成功、
实际静默弄坏可用配置的行；两边都没有私钥时返回 40001「首次配置必须提供 App 私钥」。
私钥与 webhook 密钥只写不读：响应只回 `private_key_set` / `webhook_secret_set`，不写日志。

「读旧行 → 合并省略字段 → 加密 → 写新行」在**一个短事务**内完成（单连接池下闭包内只用 tx 句柄，
见 `database.md`），检测放在提交之后：否则并发保存会把刚轮换的私钥或刚清空的密钥写回旧值。

`configured` 表示该用户已有 App 身份可用（`source != none`）；只配了 config webhook 密钥的部署
`configured = false`，但 webhook 验签仍生效。`private_key_set` 表示私钥当前真的可读
（config 来源时文件读不到就是 false），`webhook_secret_set` 表示密钥非空。

## 保存即生效与身份定位

- `githubapp.Clients` 按 `userID` 解析当前身份（`ClientFor(ctx, userID)`）：按 AppID + PEM 的
  sha256 指纹为每个用户缓存 `InstallationTokenStore + REST client`，配置变化后下一次调用自动重建。
  未配置时返回 50000「GitHub App 未配置」（沿用 `automation_agent_pr.go` 的文案与错误码）。
  `ClientFor(ctx, "")` 解析到部署 config 兜底（config 身份的评审轮次用它）。
  每次调用现查一次该用户的数据库行（config 来源时还含一次 PEM 文件读取）——本地单连接库下为
  微秒级，换远程库前应先给它加短 TTL 缓存。
- **PR 协调账按验签身份分区**：`github_pr_reviews` 主键 `(repo_full_name, pr_number, app_user_id)`、
  `github_pr_seen_shas` 主键含 `app_user_id`、`github_pr_debounces` / `automation_runs` 都有
  `app_user_id` 列（空串 = config 身份）。开轮时把验签身份写进运行行，之后 agent、同步、取消、
  置 pending 全部读这一列，不再从评审账或规则所有者反推；同步的运行查询按 `app_user_id` 过滤。
  同一仓库+PR 可被不同身份各自开轮：评审账、评论（隐藏 marker 带身份，config 沿用旧 marker）、
  check 都按身份分开，互不串扰。
- **升级迁移**：INT-93 已发布的 `github_pr_reviews` / `github_pr_seen_shas` 主键不含身份列，
  SQLite 改不了主键，所以 `cmd/server/migrate_pr_identity.go` 在 AutoMigrate 之前备份式重建
  两张表（旧行回填 `''` = 当时的全局 config 身份），重复启动幂等；`automation_runs` /
  `github_pr_debounces` 的新列带默认值，由 AutoMigrate 补列。
- webhook 只有一个入口，验签用**候选列表**：
  - 按 `X-GitHub-Hook-Installation-Target-ID`（GitHub App webhook 携带的 App ID）取出该 App 的
    数据库行（`(provider, app_id)` 唯一，最多一行）+ config 同名身份作为**候选**，逐个用
    `X-Hub-Signature-256` 验签；命中候选的身份就是后续处理的授权身份。用户接管 config 同名 App
    （从部署配置迁到个人配置的正常路径）时用户行在后选首位，正是期望结果；
  - 未知 App → 401「未知的 GitHub App」；缺少该头时，只有全库 + config 恰好一份 App 身份才接受
    （兼容 `scripts/simulate-github-pr-webhook.sh`），多个不同 App ID → 401；
  - 定位不到身份但部署只在 config 里配了 webhook 密钥（INT-93 旧部署）→ 仍用 config 密钥验签；
  - 候选全部没有密钥，或命中身份密钥为空 → 503；有可用密钥但签名不匹配 → 401。
  
  验签规则仍是 HMAC-SHA256 常量时间比较（`pkg/githubapp/webhook.go`，空请求体一律拒绝）。

## 检测

`githubapp.Verify` 用 App 私钥签 RS256 JWT 调 `GET {api}/app`（只读，不创建任何 GitHub 资源）：
200 → `ok`（detail 带 App 名称）；401 → `invalid`；其他状态码/网络错误 → `failed`。
detail 用固定文案，不拼底层 `err.Error()`，不含任何密钥。

保存与「测试连接」都同步跑一次检测：检测失败或凭证无效**仍然保存**（HTTP 200，状态落库），
和 GitHub Token 库的检测语义一致，避免用户因为 GitHub 一时不可用而丢配置。
检测结果回写按保存时的密文做条件更新，慢检测不会覆盖后来保存的新配置；检测只写检测三列，
不改 `updated_at`（它表示配置文件最近保存时间）。`source = config` 时没有数据库行可落库，
检测结果只出现在本次响应里（GET 仍为 `unchecked`）。

## 接口与前端

- `GET /api/integrations/github-app`：当前用户的生效配置（`source`、`webhook_url` = `server.public_url`
  + `/api/github/webhooks`，本部署统一入口）
- `PUT /api/integrations/github-app`：整组保存到当前用户名下并检测
- `POST /api/integrations/github-app/test`：对当前用户生效配置再检测一次；未配置 → 40006
- 页面：设置 → 集成（`web/src/routes/settings/integrations.tsx`），展示来源、状态徽章、
  Webhook URL（可复制）、三字段表单与接入步骤；密钥输入框恒不回填。

新增一类外部服务的做法：加 provider 行 + `resolveConfig` 分支 + 一个 `/api/integrations/<provider>`
子资源 + 页面上一张卡片；不做通用 provider 框架，避免为单个实现先造抽象。
