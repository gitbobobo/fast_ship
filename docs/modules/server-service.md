# Server Service

业务逻辑层。协调 Repository 层完成复杂业务操作，包括认证、项目/版本管理、Issue 同步、Ship 发布流程、AI 功能等。

## Public API

| Service | Key Methods |
|---|---|
| `AuthService` | Register, Login, Logout, RefreshToken, GetUserByID, UpdateUser, UpdatePassword, ValidateJWT, ParseJWT |
| `ProjectService` | Create, Update, Delete, List, GetByID, GetBranches |
| `VersionService` | Create, Delete, List, GetByID, Update, ShipCheck, Ship |
| `IssueService` | Create, Update, List, GetByID, SyncFromGitHub, GetFilterOptions, GetRepoLabels, ManageAssets, Comments, Timeline, InternalMeta, Checklist |
| `ArtifactService` | Upload, Delete, GetByID, GetDownloadURL |
| `AIService` | GetSettings, UpdateSettings, SuggestChecklist |
| `ApiKeyService` | Create, Delete, List, Validate |
| `ShipService` | Execute (创建 GitHub Tag/Release, 上传产物, 同步 Issue 状态) |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/service/auth.go` | JWT 生成/验证、密码哈希、Refresh Token 管理 |
| `server/internal/service/project.go` | 项目 CRUD，含 GitHub Token 加密存储 |
| `server/internal/service/version.go` | 版本管理，含 ShipCheck 验证逻辑 |
| `server/internal/service/issue.go` | Issue 完整生命周期，GitHub 同步，内部 Issue 管理 |
| `server/internal/service/artifact.go` | 产物上传和下载 URL 生成 |
| `server/internal/service/ai.go` | AI 设置管理（存储 API Key 等配置），Checklist 建议生成 |
| `server/internal/service/api_key.go` | API Key 生成（前缀 + 随机字符串）、哈希存储、权限验证 |
| `server/internal/service/ship.go` | Ship 发布流程：GitHub Tag → Release → 产物上传 → Issue 状态同步 |

## Dependencies

| Depends on | Why |
|---|---|
| server-repository | 数据访问 |
| server-model | 数据结构定义 |
| server-pkg/github | GitHub API 调用（同步 Issue、创建 Tag/Release） |
| server-pkg/crypto | GitHub Token 加密存储 |
| server-pkg/storage | 产物文件存储 |
| server-pkg/errs | 业务错误定义 |

## Dependents

| Used by | How |
|---|---|
| server-handler | 调用 Service 方法处理业务逻辑 |
| server-middleware | AuthService 用于 JWT 验证 |

## Implementation Notes

- `ShipService.Execute` 是核心业务流程：验证版本状态 → 创建 GitHub Tag → 创建 Release → 上传产物 → 更新 Issue 状态到 GitHub
- `IssueService.SyncFromGitHub` 支持双向同步，将 GitHub Issues 拉取到本地，并缓存 Labels
- `AuthService` 使用 Refresh Token 机制支持 Token 自动续期，JWT 黑名单用于登出时失效 Token
- GitHub Token 在存储前通过 AES 加密，使用时解密
- API Key 以 SHA256 哈希存储，只创建时返回明文
