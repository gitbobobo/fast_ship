# Server Middleware

HTTP 中间件层。提供 JWT/API Key 认证和 CORS 处理。

## Public API

| Export | Type | Description |
|---|---|---|
| `RequireJWT(cfg, authService)` | func | 要求 JWT 认证，从 Authorization header 解析 Token |
| `RequireAuth(cfg, apiKeyRepo, authService)` | func | 支持 JWT 或 API Key 认证（两者均可） |
| `RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, param)` | func | 从 URL query 参数读取 Token（用于浏览器下载场景） |
| `CORSConfig()` | func | 返回 Gin CORS 中间件配置 |

## Internal Structure

| File | Purpose |
|---|---|
| `server/internal/middleware/auth.go` | 三种认证中间件：JWT 必选、JWT/API Key 均可、Query Token |
| `server/internal/middleware/cors.go` | CORS 跨域配置 |

## Dependencies

| Depends on | Why |
|---|---|
| server-config | JWT Secret 配置 |
| server-service | AuthService 解析和验证 JWT |
| server-repository | ApiKeyRepository 验证 API Key |
| `golang-jwt/jwt/v5` | JWT 解析和验证 |

## Dependents

| Used by | How |
|---|---|
| server-router | 在路由注册时应用中间件 |

## Implementation Notes

- `RequireAuth` 同时检查 `Authorization: Bearer <token>` header 和 `X-API-Key` header，优先 JWT
- `RequireAuthWithQueryToken` 从 URL query 参数（如 `?token=xxx`）读取 Token，用于浏览器无法设置 header 的场景（如 `<img>` 标签、直接下载链接）
- 认证成功后将 `user_id` 注入 Gin Context，后续 Handler 通过 `c.Get("user_id")` 获取
- JWT 验证包含黑名单检查，已登出的 Token 即使未过期也会被拒绝
