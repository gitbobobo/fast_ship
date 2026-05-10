# Server Repository

GORM 数据访问层。封装所有数据库操作，每个模型对应一个 Repository。使用 SQLite 数据库，GORM AutoMigrate 管理 schema。

## Public API

| Repository | Key Operations |
|---|---|
| `UserRepository` | CRUD、按用户名查找、更新密码 |
| `ProjectRepository` | CRUD、按用户过滤列表、含关联预加载 |
| `VersionRepository` | CRUD、按项目过滤、含关联预加载 |
| `IssueRepository` | CRUD、复杂过滤查询（状态/标签/工作流）、分页、评论管理、时间线 |
| `ArtifactRepository` | CRUD、按版本过滤 |
| `ApiKeyRepository` | 创建（哈希存储）、删除、按用户列表、按哈希查找 |
| `RefreshTokenRepository` | 创建、按 Token 查找、删除（登出清理） |
| `JwtBlacklistRepository` | 添加到黑名单、检查是否在黑名单、定时清理过期记录 |
| `UserAiSettingRepository` | 按用户获取/更新 AI 配置 |
| `IssueAssetRepository` | Issue 关联资产管理 |
| `IssueDraftAssetRepository` | Issue 草稿资产管理（创建时临时上传） |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/repository/user.go` | 用户数据访问 |
| `server/internal/repository/project.go` | 项目数据访问，含 GitHub Token 存储 |
| `server/internal/repository/version.go` | 版本数据访问 |
| `server/internal/repository/issue.go` | Issue 数据访问，含复杂过滤和分页 |
| `server/internal/repository/artifact.go` | 构建产物数据访问 |
| `server/internal/repository/api_key.go` | API Key 数据访问（SHA256 哈希存储） |
| `server/internal/repository/refresh_token.go` | Refresh Token 存储（JWT 续期） |
| `server/internal/repository/jwt_blacklist.go` | JWT 黑名单（登出失效） |
| `server/internal/repository/user_ai_setting.go` | AI 配置数据访问 |
| `server/internal/repository/issue_asset.go` | Issue 关联资产 |
| `server/internal/repository/issue_draft_asset.go` | Issue 草稿资产（创建时临时文件） |

## Dependencies

| Depends on | Why |
|---|---|
| server-model | GORM 模型定义 |
| `gorm.io/gorm` | ORM 操作 |

## Dependents

| Used by | How |
|---|---|
| server-service | 所有业务逻辑通过 Repository 访问数据库 |

## Implementation Notes

- 每个 Repository 接收 `*gorm.DB` 实例，在 `main.go` 中统一注入
- Issue 查询支持多维度过滤：状态、标签、工作流状态、关键词搜索，使用动态条件构建
- API Key 只存储 SHA256 哈希，验证时对传入 Key 做哈希后比对
- JWT 黑名单使用 `ExpiresAt` 字段，配合后台 goroutine 定时清理过期记录
