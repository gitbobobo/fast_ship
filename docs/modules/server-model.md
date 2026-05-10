# Server Model

数据模型定义层。定义所有 GORM 模型和数据库表结构，包含 11 个核心模型。

## Public API

| Model | Key Fields | Description |
|---|---|---|
| `User` | ID, Username, PasswordHash, Email, Nickname, AvatarURL | 用户账户 |
| `Project` | ID, UserID, Name, RepoOwner, RepoName, GitHubToken, BranchesJSON | GitHub 项目 |
| `Version` | ID, ProjectID, Name, Notes, Status (draft/published/shipped) | 发布版本 |
| `Issue` | ID, ProjectID, GitHubID, Title, Body, State, LabelsJSON, InternalMeta | Issue（GitHub 同步或内部创建） |
| `Artifact` | ID, VersionID, FileName, FilePath, FileSize, ContentType | 构建产物 |
| `ApiKey` | ID, UserID, Name, KeyHash, KeyPrefix, Permissions | API 访问密钥 |
| `RefreshToken` | ID, UserID, Token, ExpiresAt | JWT 续期 Token |
| `JwtBlacklist` | ID, Token, ExpiresAt | JWT 失效黑名单 |
| `UserAiSetting` | ID, UserID, Provider, APIKey, Model | AI 服务配置 |
| `IssueAsset` | ID, IssueID, FileName, FilePath, FileSize | Issue 关联资产 |
| `IssueDraftAsset` | ID, UserID, FileName, FilePath, FileSize | Issue 创建时的临时资产 |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/model/user.go` | User 模型 |
| `server/internal/model/project.go` | Project 模型（含 GitHub 仓库信息） |
| `server/internal/model/version.go` | Version 模型（状态机：draft → published → shipped） |
| `server/internal/model/issue.go` | Issue 模型（支持 GitHub Issue 和内部 Issue） |
| `server/internal/model/artifact.go` | Artifact 构建产物模型 |
| `server/internal/model/api_key.go` | API Key 模型（含前缀和哈希） |
| `server/internal/model/refresh_token.go` | Refresh Token 模型 |
| `server/internal/model/jwt_blacklist.go` | JWT 黑名单模型 |
| `server/internal/model/user_ai_setting.go` | AI 设置模型 |
| `server/internal/model/issue_asset.go` | Issue 资产模型 |
| `server/internal/model/issue_draft_asset.go` | Issue 草稿资产模型 |

## Dependencies

| Depends on | Why |
|---|---|
| `gorm.io/gorm` | GORM 模型定义和钩子 |

## Dependents

| Used by | How |
|---|---|
| server-repository | 定义数据库操作的数据结构 |
| server-service | 业务逻辑中的数据传递 |

## Implementation Notes

- Issue 模型通过 `GitHubID` 字段区分：`GitHubID > 0` 为 GitHub Issue，`GitHubID = 0` 为内部 Issue
- `LabelsJSON` 存储 JSON 数组格式的标签名称，响应时解析为带颜色的标签详情
- `InternalMeta` 字段用于内部 Issue 的扩展元数据（如工作流状态、Checklist）
- Version 有三种状态：`draft`（草稿）、`published`（已发布）、`shipped`（已发货）
- 所有模型使用 `gorm.Model` 自动包含 ID/CreatedAt/UpdatedAt/DeletedAt 字段
