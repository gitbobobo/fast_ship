# 项目 GitHub 凭证

`server/internal/service/github_token.go` 集中负责读取并解密项目 GitHub Token（`projects.github_token_encrypted`）。service 层不再各自调 `crypto.Decrypt`——项目 Token 的解密只允许出现在这个文件里（`ai.go` 的用户 AI Key 是另一套凭证，不受此约束）。

## 两种凭证要求

- `requiredProjectGitHubToken(project, cfg, logger)`：项目必须已配置 GitHub。未配置返回 `ErrProjectGitHubNotConfigured`（40003）。
- `optionalProjectGitHubToken(project, cfg, logger)`：允许未配置，返回 `(nil, nil)` 由调用方走降级路径。目前唯一使用方是 PR 关联（经 `resolvePullRequestCredential` 间接调用）：未配置的项目以未认证客户端访问公共仓库（受 60/hr 限流）。

两者共享私有实现 `decryptProjectGitHubToken`；「未配置是否允许」由外层函数名表达，调用点一眼能看出凭证要求。

## PR 访问 Token（第二种项目凭证）

项目另有一个可选的独立凭证 `projects.github_pr_token_encrypted`，仅用于读取已关联 PR 的详情与状态（attach 与 sync）。动机：反馈仓库（需要 Issue 写权限）与代码仓库（只需要 PR 读权限）可以不是同一个仓库，一个 Token 未必同时满足两边。

凭证选择集中在 `resolvePullRequestCredential`，返回解密后的 token 与来源标记 `pullRequestCredentialKind`（`pr` / `project` / `anonymous`）：

- 已配 PR Token → 优先且**只**使用它；解密失败、过期、权限不足都直接报错，**不静默回退**项目 Token 或匿名。
- 未配 PR Token → 沿用 optional 项目 Token 语义（已配项目 Token 用之，未配置走匿名）。
- PR Token 的可用性不依赖项目是否配置了反馈仓库——internal 项目只配 PR Token 也生效。

PR Token 可以经 `pr_token_source_project_id` 从另一项目复用：与 `source_project_id` 一样直接复制源项目的密文 blob（同一加密密钥，不解密重加密），优先于同传的 `github_pr_token`。`pr_token_source_kind` 与 source 搭配选择复制的凭证种类：`access` 复制源项目的 GitHub Access Token，缺省 `pr` 复制源项目的 PR Token；kind 单独提供（不带 source）或取值非法返回 40001，源项目未配置所选凭证时同样返回 40001。`clear_github_pr_token` 与 `github_pr_token` 或 `pr_token_source_project_id` 同时显式提供（含 null）返回 40001。

来源标记供错误文案使用：attach 与 sync 的失败消息会写明「使用 PR 访问 Token」「使用项目 Token」或「匿名访问」。GitHub 对「PR 不存在」与「凭证无权访问」都返回 404，401/403/404 会追加「确认仓库访问范围与 Pull requests 读权限」的排查提示，文案不断言单一原因。

## 失败语义

- 解密失败（密文损坏或密钥不匹配）= `logger.Error` + `ErrInternal`（50000）。日志只带 `project_id` 与解密错误，绝不包含 token 明文或密文。
- 已配置但解密失败不静默退回匿名——optional 路径同样报 50000，防止凭证故障被误判成「未配置」。
- PR Token 解密失败同样报 50000 且不回退项目 Token：用户以为 PR Token 在用、实际走另一个凭证，比直接报错更难排查。
- `VersionService.ensureTargetBranchExists` 走 required 后，未配置 GitHub 的项目传 `target_commitish` 返回 40003 而非旧行为的 50000（INT-61 的有意语义修正）。

## client factory 仍然分家

本工具只管 token 读取与解密，不合并 GitHub client 构造。各 service 的 client factory 按能力划分、各自可注入替身：`IssueService.newClient`（issue/PR 读写）、`ShipService.newClient`（tag/release/asset）、`ProjectService` 与 `VersionService.newBranchClient`（分支列表）。
