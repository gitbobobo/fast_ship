# Authentication

Fast Ship 支持三种认证方式：JWT Token（常规用户会话）、Refresh Token（自动续期）和 API Key（程序化访问）。认证逻辑贯穿前后端多个模块。

## Where It Appears

- **server-middleware** — 三种认证中间件：RequireJWT、RequireAuth（JWT 或 API Key）、RequireAuthWithQueryToken
- **server-handler** — AuthHandler 处理登录/注册/登出/Token 刷新
- **server-service** — AuthService 实现 JWT 生成/验证、密码哈希、Refresh Token 管理
- **server-repository** — RefreshTokenRepository、JwtBlacklistRepository、ApiKeyRepository
- **server-model** — RefreshToken、JwtBlacklist、ApiKey 数据模型
- **web-api** — client.ts 自动注入 Token header，401 时自动刷新
- **web-state** — auth-store 管理 Token 持久化和用户状态

## Convention

### 后端

1. **JWT 生成**：使用 `golang-jwt/jwt/v5`，payload 包含 `user_id`，支持配置过期时间（默认通过 `jwt.expire_hours`）
2. **密码存储**：使用 `golang.org/x/crypto/bcrypt` 哈希存储，永不存储明文
3. **API Key 格式**：`fs_` 前缀 + 随机字符串，存储 SHA256 哈希，创建时返回完整 Key（仅一次）
4. **Token 刷新**：客户端发送 `POST /api/auth/refresh`，使用 Refresh Token 换取新的 JWT
5. **登出**：JWT 加入黑名单 + 删除 Refresh Token
6. **路由认证分组**：在 router.go 中按中间件分组（公开/JWT 必选/JWT+API Key）

### 前端

1. **Token 存储**：Zustand auth-store + localStorage 持久化
2. **请求认证**：ky hook 在每个请求前从 auth-store 读取 Token 注入 header
3. **自动刷新**：收到 401 时，使用 Refresh Token 尝试续期，成功后重试原请求
4. **登出清理**：清除 auth-store 中的 Token 和用户信息，跳转登录页

## Examples

后端认证中间件使用（`server/internal/router/router.go`）：
```go
// JWT 必选
ai := api.Group("/ai", middleware.RequireJWT(cfg, authService))

// JWT 或 API Key 均可
projectRead := api.Group("/projects", middleware.RequireAuth(cfg, apiKeyRepo, authService))

// Query Token（浏览器下载场景）
api.GET("/artifacts/:aid/download",
    middleware.RequireAuthWithQueryToken(cfg, apiKeyRepo, authService, "token"),
    artifactHandler.Download)
```

前端 Token 注入（`web/src/lib/api/client.ts`）：
```typescript
hooks: {
  beforeRequest: [
    (request) => {
      const token = useAuthStore.getState().token
      if (token) request.headers.set('Authorization', `Bearer ${token}`)
    }
  ]
}
```

## Adding to This Pattern

添加需要认证的新端点时：
1. 在 `server/internal/router/router.go` 中选择合适的认证中间件
2. 大多数写操作用 `RequireJWT`，读操作用 `RequireAuth`
3. 文件下载/图片场景用 `RequireAuthWithQueryToken`
4. 前端在 `web/src/lib/api/` 中添加对应 API 函数，Token 会自动注入
