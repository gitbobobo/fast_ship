# GitHub 凭证（Token 库与项目引用）

GitHub Token 不再存在项目行上。`user_github_tokens` 是用户级 Token 库（模型 `model.UserGitHubToken`，仓库 `repository.UserGitHubTokenRepository`），项目只在 `projects.public_github_token_id` / `projects.code_github_token_id` 两个引用列上指向库里的记录。同一 Token 可被多项目、两个用途同时引用；同一用户重复添加完全相同的明文时按指纹复用已有记录。删项目不删 Token，被引用的 Token 禁止删除（40913）。

两个用途属于项目引用，Token 本身没有角色：

- **公开仓库 Token**（原 Access Token）：Issue 同步与 GitHub Issue 写、分支查询、创建 Tag、发布 Release、上传安装包。
- **代码仓库 Token**（原 PR 访问 Token）：仅用于读取关联 PR（含其他仓库）。

两个用途**互不兜底**：未配公开仓库 Token 不做 Issue 同步（后台跳过、手动同步与 GitHub 写返回 40006），不能借代码仓库 Token；未配代码仓库 Token 时匿名读公共 PR，不借公开仓库 Token。凭证解密失败/引用行缺失一律 50000 明确报错，不换另一把、不降级匿名。

## 模块划分

- `service/project_credentials.go`（`ProjectCredentials`）：项目凭证读侧——按用途选 Token 并解密。`requiredGitHubToken`（公开仓库用途）与 `pullRequestCredential`（代码仓库用途/匿名）是仅有的两个入口；加解密只允许出现在凭证模块（`ai.go` 的用户 AI Key 是另一套凭证）。`CheckTokenReference` 校验项目写入的引用属于当前用户（40420）。
- `service/github_token.go`（`GitHubTokenService`）：Token 库 CRUD、指纹去重与检测编排。密文用 AES-256-GCM（`pkg/crypto`）加密；`token_fingerprint` 是明文的 SHA-256 hex（AES-GCM 密文每次不同，绝不能按密文去重），`token_prefix` 存明文前 8 个可见字符用于列表辨识。
- `pkg/github/detector.go`（`TokenDetector`）：只读检测。`GET https://api.github.com/user`；401 → `invalid`，超时/网络错误/限流/5xx → `failed`（网络失败不能判成凭证无效）；200 → `ok`，`X-OAuth-Scopes` 非空 → `classic` + scopes，空 scopes 且前缀 `github_pat_` → `fine_grained`（detail 写明「无法自动获取完整授权」），否则 `unknown`。`detection_detail` 使用固定文案，不拼接底层 `err.Error()`。不创建任何 GitHub 资源、不用写操作试探权限。测试经 service 的 `detect` 字段注入 fake。
- REST API：`/api/github-tokens`（仅 JWT），见 openapi `github-tokens` tag。创建/替换后服务端同步检测一次，检测失败仍 200 保存（状态落在 `detection_status`）；列表不触发 GitHub 请求。

项目凭证的写侧（create/update 引用解析）归 `ProjectService`：create 直接校验引用归属，update 用 `TokenRefUpdate` 三态（key 缺省保留、显式 null/空串清空、非空替换）。

## 读侧语义

- `requiredGitHubToken(project)`：无 owner/repo → 40003「尚未关联仓库」；有仓库但 `PublicGitHubTokenID == ""` → 40006「该项目未配置公开仓库 Token」；有引用则加载行并解密，行缺失或解密失败 → 50000，文案点名「公开仓库 Token」。
- `pullRequestCredential(project)`：有 `CodeGitHubTokenID` 只用它（解密失败 50000 点名「代码仓库 Token」，不回退公开/匿名）；否则匿名（`kind=anonymous`），即使公开仓库 Token 已配。`pullRequestCredentialKind` 只剩 `code`/`anonymous` 两个值，`usage()` 文案「使用代码仓库 Token」/「匿名访问」供 attach/sync 错误消息标注来源。
- `IsGitHubConfigured()` 现在只判 owner+repo（仓库已关联）；公开仓库 Token 是否存在用 `PublicGitHubTokenID != ""` 单独判断。同步/写/分支/发货在有仓库但无公开 Token 时返回 40006 而非「尚未关联仓库」。
- 同步：`SyncAll` 跳过无仓库或无公开 Token 的项目（后台不刷错误）；手动 `SyncProjectIssues` 有仓库无 Token 时经 `requiredGitHubToken` 返回 40006 并记录进 sync state。

## 失败语义

- 解密失败（密文损坏或密钥不匹配）与引用行缺失 = `logger.Error` + 50000。日志只带 `project_id`/`token_id` 与底层错误，绝不包含 token 明文或密文。
- 已配置但解密失败不静默退回匿名——用户以为某把 Token 在用、实际走了另一条路径比报错更难排查。
- Token 库 API 的重复语义：create 指纹撞已有记录 → 返回已有记录（不改其标题）；update 替换撞同用户另一条 → 40914 并点名已有 title，不隐式合并。替换按加载时指纹条件写入，并发替换 → 40915。被引用（任一用途）的 Token 删除 → 40913。改标题只写 title，不覆盖密文。

## 历史迁移（`cmd/server/migrate_project_tokens.go`）

`migrateProjectGitHubTokens` 在 AutoMigrate 之后执行，把项目上的旧密文列（`github_token_encrypted` / `github_pr_token_encrypted`）迁入 Token 库并切换到引用列：

- 按 `user_id` 分组，全部待迁移用户放进同一个事务；任一旧密文解密失败即整库回滚并返回错误（启动失败，宁可起不来），不跳过该项目。只按实际存在的旧列读写，清理中断后只剩一列也能继续。
- 同一用户内按明文指纹去重复用；标题按产生顺序「迁移 Token 1」起；旧密文 blob 直接复用（同一密钥，不解密重加密）。
- 原 Access → 公开仓库引用；独立 PR Token → 代码仓库引用；只有 Access 的项目把 Access 显式引用到两个位置（保留旧兜底行为）。
- 检测状态一律 `unchecked`、`detected_at` NULL，不依赖 GitHub 在线。
- 成功后清空旧列并尝试 `DROP COLUMN`（失败仅告警，留空列同样幂等）；幂等性靠「只选旧列非 NULL 的行 + 事务原子性」保证，全新库直接跳过。

## client factory 仍然分家

凭证模块只管引用解析与解密，不合并 GitHub client 构造。各 service 的 client factory 按能力划分、各自可注入替身：`IssueService.newClient`（issue/PR 读写）、`ShipService.newClient`（tag/release/asset）、`ProjectService` 与 `VersionService.newBranchClient`（分支列表）。
