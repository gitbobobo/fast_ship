# Web API

HTTP 客户端和 API 模块。基于 ky 封装统一的 HTTP 客户端，按资源划分 7 个 API 模块。

## Public API

### Client (`client.ts`)

| Export | Description |
|---|---|
| `api` | ky 实例，配置 baseURL、JSON 序列化、认证 header 注入和 401 自动刷新 |

### API Modules

| Module | File | Key Functions |
|---|---|---|
| auth | `auth.ts` | login, register, logout, getMe, updateMe, updatePassword, refreshToken |
| projects | `projects.ts` | listProjects, getProject, createProject, updateProject, deleteProject, getBranches |
| versions | `versions.ts` | listVersions, getVersion, createVersion, updateVersion, deleteVersion, shipCheck, ship |
| issues | `issues.ts` | listIssues, getIssue, createIssue, updateIssue, syncIssues, getFilterOptions, getRepoLabels, uploadDraftAsset, uploadAsset, listComments, createComment, listTimeline, updateInternalMeta, replaceChecklist |
| artifacts | `artifacts.ts` | uploadArtifact, deleteArtifact, getDownloadUrl |
| ai | `ai.ts` | getAISettings, updateAISettings, suggestChecklist |
| api-keys | `api-keys.ts` | listApiKeys, createApiKey, deleteApiKey |

## Internal Structure

| File | Purpose |
|---|---|
| `web/src/lib/api/client.ts` | ky 实例创建，请求/响应拦截，Token 自动刷新逻辑 |
| `web/src/lib/api/auth.ts` | 认证 API |
| `web/src/lib/api/projects.ts` | 项目 API |
| `web/src/lib/api/versions.ts` | 版本 API |
| `web/src/lib/api/issues.ts` | Issue API |
| `web/src/lib/api/artifacts.ts` | 产物 API |
| `web/src/lib/api/ai.ts` | AI API |
| `web/src/lib/api/api-keys.ts` | API Key 管理 |

## Dependencies

| Depends on | Why |
|---|---|
| web-state | auth-store（获取/存储 Token） |
| `ky` | HTTP 客户端 |

## Dependents

| Used by | How |
|---|---|
| web-hooks | 所有 hooks 调用 API 模块获取数据 |

## Implementation Notes

- `client.ts` 在请求前自动注入 `Authorization: Bearer <token>` header
- 401 响应时自动尝试 Refresh Token 续期，成功后重试原请求，失败则跳转登录页
- 文件上传使用 FormData，支持进度回调
- 下载 URL 使用 Query Token 方式（`?token=xxx`），用于浏览器直接下载
