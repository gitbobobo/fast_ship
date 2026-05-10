# Server Pkg / GitHub

GitHub API 客户端封装。基于 `google/go-github` 库，封装项目需要的 GitHub 操作。

## Public API

| Export | Type | Description |
|---|---|---|
| `Client` | struct | GitHub API 客户端 |
| `NewClient(token)` | func | 创建 GitHub 客户端（OAuth2 认证） |
| `ListIssues(ctx, owner, repo, opts)` | func | 列出仓库 Issues |
| `GetIssue(ctx, owner, repo, number)` | func | 获取单个 Issue |
| `CreateIssue(ctx, owner, repo, issue)` | func | 创建 Issue |
| `UpdateIssue(ctx, owner, repo, number, issue)` | func | 更新 Issue |
| `ListRepoLabels(ctx, owner, repo)` | func | 获取仓库标签列表 |
| `CreateTag(ctx, owner, repo, tag, sha)` | func | 创建 Git Tag |
| `CreateRelease(ctx, owner, repo, params)` | func | 创建 Release |
| `UploadReleaseAsset(ctx, owner, repo, id, file)` | func | 上传 Release 产物 |
| `ListBranches(ctx, owner, repo)` | func | 列出分支 |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/pkg/github/client.go` | GitHub API 客户端，封装所有 GitHub 操作 |

## Dependencies

| Depends on | Why |
|---|---|
| `google/go-github/v62` | GitHub REST API v3 客户端 |
| `golang.org/x/oauth2` | OAuth2 Token 认证 |

## Dependents

| Used by | How |
|---|---|
| server-service | Issue 同步、Ship 发布、分支查询 |

## Implementation Notes

- Token 通过 OAuth2 StaticTokenSource 传入，每次请求使用解密后的 Token 创建新客户端
- Ship 流程中使用：创建 Tag → 创建 Release → 上传产物
- Issue 同步使用：拉取仓库 Issues 和 Labels
