# Server Handler

HTTP 请求处理器层。接收 HTTP 请求，解析参数，调用 Service 层处理业务逻辑，返回 JSON 响应。按资源划分为 8 个 Handler。

## Public API

| Handler | Key Methods |
|---|---|
| `AuthHandler` | Register, Login, Logout, Refresh, GetMe, UpdateMe, UpdatePassword |
| `ProjectHandler` | Create, Update, Delete, List, Get, GetBranches |
| `VersionHandler` | Create, Delete, List, Get, Update, ShipCheck, Ship |
| `IssueHandler` | Create, Update, List, Get, Sync, FilterOptions, RepoLabels, UploadDraftAsset, UploadAsset, AssetContent, ListComments, CreateComment, ListTimeline, UpdateInternalMeta, ReplaceChecklist |
| `ArtifactHandler` | Upload, Delete, Download |
| `AIHandler` | GetSettings, UpdateSettings, SuggestIssueChecklist |
| `ApiKeyHandler` | List, Create, Delete |
| `GitHubMediaProxyHandler` | Proxy |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/handler/auth.go` | 认证相关：注册、登录、登出、Token 刷新、用户信息管理 |
| `server/internal/handler/project.go` | 项目 CRUD 和分支查询 |
| `server/internal/handler/version.go` | 版本管理、Ship 检查和发布 |
| `server/internal/handler/issue.go` | Issue 完整生命周期：创建/更新/同步/评论/资产/时间线/标签 |
| `server/internal/handler/artifact.go` | 构建产物上传/删除/下载 |
| `server/internal/handler/ai.go` | AI 设置和 Checklist 建议 |
| `server/internal/handler/api_key.go` | API Key 管理 |
| `server/internal/handler/github_media_proxy.go` | GitHub 媒体资源代理 |

## Dependencies

| Depends on | Why |
|---|---|
| server-service | 调用业务逻辑 |
| server-pkg/response | 格式化 HTTP 响应 |

## Dependents

| Used by | How |
|---|---|
| server-router | 注入 Handler 并绑定路由 |

## Implementation Notes

- Handler 不包含业务逻辑，只负责参数解析、调用 Service、返回响应
- 认证信息通过 Gin Context 获取（由 middleware 注入 user_id）
- 文件上传使用 multipart form，支持进度追踪
- `GitHubMediaProxyHandler` 从目标 URL 获取内容并流式转发，同时缓存到本地
