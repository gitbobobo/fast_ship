# Server Router

API 路由注册和前端 SPA 静态文件服务。`router.go` 定义所有 API 端点及其认证方式；`web.go` 处理 SPA 路由的 fallback 和静态资源服务。

## Public API

| Export | Type | Description |
|---|---|---|
| `Setup(r, cfg, handlers...)` | func | 注册所有 API 路由和中间件 |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/router/router.go` | API 路由定义，按认证方式分组（公开/JWT 必选/JWT 或 API Key） |
| `server/internal/router/web.go` | SPA 静态文件服务，处理 NoRoute fallback 到 index.html |

## API Route Map

### 公开接口（无需认证）
- `POST /api/auth/register` — 注册
- `POST /api/auth/login` — 登录
- `POST /api/auth/refresh` — 刷新 Token

### JWT 认证接口
- `POST /api/auth/logout` — 登出
- `GET/PUT /api/auth/me` — 用户信息
- `PUT /api/auth/password` — 修改密码
- `GET/PUT /api/ai/settings` — AI 设置
- `GET/POST/DELETE /api/api-keys` — API Key 管理
- `POST /api/projects` — 创建项目
- `PUT/DELETE /api/projects/:id` — 修改/删除项目
- `POST /api/projects/:id/versions` — 创建版本
- `DELETE /api/versions/:vid` — 删除版本
- `GET /api/versions/:vid/ship-check` — Ship 前检查
- `POST /api/versions/:vid/ship` — 执行 Ship
- `POST /api/projects/:id/issues/assets` — 上传 Issue 草稿资产
- `POST /api/projects/:id/issues/sync` — 同步 Issue
- `POST /api/projects/:id/issues/batch-close` — 批量关闭已完成 Issue
- `POST /api/issues/:iid/comments` — 创建 Issue 评论

### JWT 或 API Key 认证接口
- `GET /api/projects` — 项目列表
- `GET /api/projects/:id` — 项目详情
- `GET /api/projects/:id/branches` — 分支列表
- `GET /api/projects/:id/versions` — 版本列表
- `GET/PUT /api/versions/:vid` — 版本详情/更新
- `POST /api/projects/:id/issues` — 创建 Issue
- `PUT /api/issues/:iid` — 更新 Issue
- `POST /api/issues/:iid/assets` — 上传 Issue 资产
- `PUT /api/issues/:iid/internal-meta` — 更新 Issue 内部元数据
- `PUT /api/issues/:iid/checklist` — 替换 Issue 清单
- `POST /api/issues/:iid/checklist-suggestions` — 生成清单建议（Agent）
- `GET /api/projects/:id/issues` — Issue 列表
- `GET /api/projects/:id/issues/count` — Issue 数量
- `GET /api/projects/:id/issues/filter-options` — Issue 筛选选项
- `GET /api/projects/:id/issues/repo-labels` — 仓库标签
- `GET /api/issues/:iid` — Issue 详情
- `GET /api/issues/:iid/comments` — Issue 评论列表
- `GET /api/issues/:iid/timeline` — Issue 时间线
- Artifact 操作（上传/删除/下载）

### Query Token 认证（用于浏览器下载）
- `GET /api/github/media-proxy` — GitHub 媒体代理
- `GET /api/artifacts/:aid/download` — 产物下载
- `GET /api/issues/assets/:aid/content` — Issue 资产内容

## Dependencies

| Depends on | Why |
|---|---|
| server-handler | 注入所有 HTTP Handler |
| server-middleware | 认证中间件 (RequireAuth, RequireJWT, RequireAuthWithQueryToken) |
| server-service | AuthService 用于认证中间件 |
| server-config | 读取配置 |

## Implementation Notes

- 路由按认证方式分组：公开、仅 JWT、JWT 或 API Key、Query Token（URL 参数传递 token，用于浏览器直接下载）
- `web.go` 在生产模式下启用，当 `WebDistDir` 配置指向有效目录时，未匹配的 GET 请求 fallback 到 `index.html`（SPA 路由）
- 所有路径遍历攻击已在 `resolveWebPath` 中防御
