# 项目 GitHub 凭证

`server/internal/service/github_token.go` 集中负责读取并解密项目 GitHub Token（`projects.github_token_encrypted`）。service 层不再各自调 `crypto.Decrypt`——项目 Token 的解密只允许出现在这个文件里（`ai.go` 的用户 AI Key 是另一套凭证，不受此约束）。

## 两种凭证要求

- `requiredProjectGitHubToken(project, cfg, logger)`：项目必须已配置 GitHub。未配置返回 `ErrProjectGitHubNotConfigured`（40003）。
- `optionalProjectGitHubToken(project, cfg, logger)`：允许未配置，返回 `(nil, nil)` 由调用方走降级路径。目前唯一使用方是 PR 关联（`IssueService.pullRequestClient`）：未配置的项目以未认证客户端访问公共仓库（受 60/hr 限流）。

两者共享私有实现 `decryptProjectGitHubToken`；「未配置是否允许」由外层函数名表达，调用点一眼能看出凭证要求。

## 失败语义

- 解密失败（密文损坏或密钥不匹配）= `logger.Error` + `ErrInternal`（50000）。日志只带 `project_id` 与解密错误，绝不包含 token 明文或密文。
- 已配置但解密失败不静默退回匿名——optional 路径同样报 50000，防止凭证故障被误判成「未配置」。
- `VersionService.ensureTargetBranchExists` 走 required 后，未配置 GitHub 的项目传 `target_commitish` 返回 40003 而非旧行为的 50000（INT-61 的有意语义修正）。

## client factory 仍然分家

本工具只管 token 读取与解密，不合并 GitHub client 构造。各 service 的 client factory 按能力划分、各自可注入替身：`IssueService.newClient`（issue/PR 读写）、`ShipService.newClient`（tag/release/asset）、`ProjectService` 与 `VersionService.newBranchClient`（分支列表）。
