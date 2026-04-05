# Fast Ship — 技术设计文档

## 1. 技术选型

### 1.1 后端语言：Go

选择 Go 而非 TypeScript 的理由：

| 维度 | Go | TypeScript (Node.js) |
|------|-----|---------------------|
| 大文件上传（≤500MB） | 原生流式处理，内存占用可控 | 需要额外配置流式处理，内存压力较大 |
| 并发模型 | Goroutine 轻量级并发，发货流程天然适合 | 单线程事件循环，CPU 密集场景受限 |
| 部署 | 单二进制文件，无运行时依赖 | 需要 Node.js 运行时 + node_modules |
| GitHub API 集成 | `go-github` 官方库成熟稳定 | `octokit` 同样成熟 |
| 类型安全 | 编译期强类型 | 编译期类型检查，但运行时无保障 |
| 性能 | 编译型语言，I/O 与 CPU 表现均优 | I/O 密集场景尚可，CPU 密集场景弱 |

发货流程涉及 Git Tag 创建 → Release 创建 → 多安装包上传的顺序操作，Go 的并发原语（Goroutine + Channel）可以在安装包上传阶段实现并行上传，显著提升发货效率。同时单二进制部署大幅简化运维。

### 1.2 技术栈总览

| 层级 | 技术 | 说明 |
|------|------|------|
| HTTP 框架 | [Gin](https://github.com/gin-gonic/gin) | 高性能、社区活跃、中间件丰富 |
| ORM | [GORM](https://gorm.io/) | Go 生态最主流 ORM，支持迁移、关联、Hook |
| 数据库 | SQLite | 零依赖嵌入式数据库，单文件部署，简化开发运维 |
| 文件存储 | 本地磁盘（初期）/ S3 兼容存储（后期） | 安装包文件存储，后续可切换至 MinIO 或 AWS S3 |
| GitHub 集成 | [go-github](https://github.com/google/go-github) | Google 官方维护的 GitHub API v3 Go 客户端 |
| 认证 | JWT（[golang-jwt](https://github.com/golang-jwt/jwt)） | 用户会话管理 |
| 密码哈希 | bcrypt（`golang.org/x/crypto/bcrypt`） | 安全密码存储 |
| 加密 | AES-256-GCM | GitHub Token 加密存储 |
| 配置管理 | [Viper](https://github.com/spf13/viper) | 支持多格式配置文件 + 环境变量 |
| 日志 | [Zap](https://github.com/uber-go/zap) | 高性能结构化日志 |
| 数据校验 | [validator](https://github.com/go-playground/validator) | 请求参数校验 |
| API 文档 | [swag](https://github.com/swaggo/swag) | Swagger/OpenAPI 文档自动生成 |
| 容器化 | Docker + Docker Compose | 开发及部署环境 |

## 2. 系统架构

### 2.1 架构风格

采用**分层架构**，简洁务实：

```
┌─────────────────────────────────────────────┐
│                  HTTP Layer                  │
│          (Gin Router + Middleware)           │
├─────────────────────────────────────────────┤
│                Handler Layer                 │
│       (请求解析、参数校验、响应封装)            │
├─────────────────────────────────────────────┤
│                Service Layer                 │
│          (业务逻辑、权限控制、流程编排)         │
├─────────────────────────────────────────────┤
│              Repository Layer                │
│            (数据访问、GORM 操作)              │
├─────────────────────────────────────────────┤
│              Infrastructure                  │
│      (DB / FileStorage / GitHub API)         │
└─────────────────────────────────────────────┘
```

### 2.2 项目目录结构

```
fast_ship/
├── cmd/
│   └── server/
│       └── main.go              # 程序入口
├── internal/
│   ├── config/
│   │   └── config.go            # 配置加载
│   ├── middleware/
│   │   ├── auth.go              # JWT + API Key 鉴权中间件
│   │   └── cors.go              # 跨域配置
│   ├── model/
│   │   ├── user.go              # User 模型
│   │   ├── api_key.go           # ApiKey 模型
│   │   ├── project.go           # Project 模型
│   │   ├── version.go           # Version 模型
│   │   └── artifact.go          # Artifact 模型
│   ├── handler/
│   │   ├── auth.go              # 认证相关 Handler
│   │   ├── api_key.go           # API Key Handler
│   │   ├── project.go           # 项目 Handler
│   │   ├── version.go           # 版本 Handler
│   │   └── artifact.go          # 安装包 Handler
│   ├── service/
│   │   ├── auth.go              # 认证服务
│   │   ├── api_key.go           # API Key 服务
│   │   ├── project.go           # 项目服务
│   │   ├── version.go           # 版本服务
│   │   ├── artifact.go          # 安装包服务
│   │   └── ship.go              # 发货服务（GitHub 集成）
│   ├── repository/
│   │   ├── user.go              # 用户数据访问
│   │   ├── api_key.go           # API Key 数据访问
│   │   ├── project.go           # 项目数据访问
│   │   ├── version.go           # 版本数据访问
│   │   └── artifact.go          # 安装包数据访问
│   ├── pkg/
│   │   ├── crypto/
│   │   │   └── aes.go           # AES 加解密工具
│   │   ├── github/
│   │   │   └── client.go        # GitHub API 封装
│   │   ├── storage/
│   │   │   ├── storage.go       # 存储接口定义
│   │   │   ├── local.go         # 本地文件存储实现
│   │   │   └── s3.go            # S3 存储实现（预留）
│   │   └── response/
│   │       └── response.go      # 统一响应格式
│   └── router/
│       └── router.go            # 路由注册
├── migrations/                   # 数据库迁移脚本
├── configs/
│   └── config.yaml              # 配置文件模板
├── docs/                         # Swagger 文档（自动生成）
├── Dockerfile
├── docker-compose.yml
├── go.mod
├── go.sum
└── Makefile
```

## 3. 数据库设计

### 3.1 ER 关系

```
User 1──N ApiKey
User 1──N Project
Project 1──N Version
Version 1──N Artifact
```

### 3.2 表结构

SQLite 通过 GORM AutoMigrate 自动建表，以下为逻辑结构说明。

> **注意**：SQLite 需开启 `PRAGMA foreign_keys = ON` 以启用外键约束，GORM SQLite 驱动需在连接参数中配置 `_pragma=foreign_keys(1)`。

#### users

```sql
CREATE TABLE users (
    id            TEXT PRIMARY KEY,  -- UUID 由应用层生成
    username      TEXT NOT NULL UNIQUE,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);
```

#### api_keys

```sql
CREATE TABLE api_keys (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    key_prefix   TEXT NOT NULL,
    key_hash     TEXT NOT NULL,
    last_used_at DATETIME,
    created_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_api_keys_user_id ON api_keys(user_id);
CREATE INDEX idx_api_keys_key_hash ON api_keys(key_hash);
```

#### projects

```sql
CREATE TABLE projects (
    id                     TEXT PRIMARY KEY,
    user_id                TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                   TEXT NOT NULL,
    description            TEXT,
    github_owner           TEXT NOT NULL,
    github_repo            TEXT NOT NULL,
    github_token_encrypted BLOB NOT NULL,
    created_at             DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at             DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(user_id, name)
);

CREATE INDEX idx_projects_user_id ON projects(user_id);
```

#### versions

```sql
CREATE TABLE versions (
    id                 TEXT PRIMARY KEY,
    project_id         TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    version_number     TEXT NOT NULL,
    status             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'shipped')),
    release_notes      TEXT,
    target_commitish   TEXT,
    github_release_url TEXT,
    error_log          TEXT,
    created_at         DATETIME NOT NULL DEFAULT (datetime('now')),
    shipped_at         DATETIME,
    UNIQUE(project_id, version_number)
);

CREATE INDEX idx_versions_project_id ON versions(project_id);
CREATE INDEX idx_versions_status ON versions(status);
```

#### artifacts

```sql
CREATE TABLE artifacts (
    id          TEXT PRIMARY KEY,
    version_id  TEXT NOT NULL REFERENCES versions(id) ON DELETE CASCADE,
    file_name   TEXT NOT NULL,
    file_size   INTEGER NOT NULL,
    file_path   TEXT NOT NULL,
    platform    TEXT,
    uploaded_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_artifacts_version_id ON artifacts(version_id);
```

### 3.3 JWT 黑名单（数据库表）

使用 SQLite 表替代 Redis 实现登出 Token 黑名单：

```sql
CREATE TABLE jwt_blacklist (
    jti        TEXT PRIMARY KEY,
    expired_at DATETIME NOT NULL
);
```

应用层定期清理已过期的记录（通过后台 Goroutine 定时执行 `DELETE FROM jwt_blacklist WHERE expired_at < datetime('now')`）。

## 4. 认证与鉴权

### 4.1 双认证通道

系统支持两种认证方式，通过统一中间件处理：

```
请求到达
  │
  ▼
解析 Authorization Header
  │
  ├── Bearer <JWT Token>  → JWT 校验 → 完整权限
  │
  └── Bearer <API Key>    → Key Hash 匹配 → 受限权限
```

**区分策略**：JWT Token 为标准 JWT 格式（三段式 `xxxxx.yyyyy.zzzzz`），API Key 使用自定义前缀格式 `fsk_`，中间件根据格式自动路由到对应的验证逻辑。

### 4.2 JWT Token 策略

| 配置项 | 值 |
|--------|------|
| 签名算法 | HS256 |
| 有效期 | 24 小时 |
| Payload | `sub` (用户ID), `jti` (Token唯一ID), `exp`, `iat` |
| 刷新策略 | 前端定期调用刷新接口获取新 Token（预留） |

### 4.3 API Key 生成与校验

**生成流程**：
1. 生成 32 字节安全随机数
2. Base62 编码得到 Key 原文
3. 添加前缀 `fsk_`，最终格式如：`fsk_a1b2c3d4e5f6...`
4. 保存 `key_prefix`（前 8 位）和 `key_hash`（SHA-256 哈希）到数据库
5. 将完整 Key 返回给用户（仅此一次展示明文）

**校验流程**：
1. 提取 `fsk_` 后的 Key 原文
2. 计算 SHA-256 哈希
3. 在 `api_keys` 表中查找匹配的 `key_hash`
4. 匹配成功则获取关联用户，更新 `last_used_at`
5. 在请求上下文中标记认证方式为 `api_key`，后续中间件据此判断权限

### 4.4 权限控制中间件

```go
// 伪代码
func RequireJWT() gin.HandlerFunc {
    // 仅允许 JWT 认证通过的请求
    // 用于：创建项目、创建版本、删除版本、执行发货、删除项目
}

func RequireAuth() gin.HandlerFunc {
    // JWT 或 API Key 均可
    // 用于：查看信息、上传安装包、更新版本说明等
}

func RequireOwner(resourceType string) gin.HandlerFunc {
    // 校验当前用户是否为资源所有者
}
```

## 5. 核心业务流程

### 5.1 发货流程

```
用户点击「发货」
      │
      ▼
┌──────────────┐
│   前置校验    │ ← Release 说明 / 安装包 / Target Commitish / GitHub 配置
│  (Service层)  │
└──────┬───────┘
       │ 校验通过
       ▼
┌──────────────┐
│ 解密 GitHub   │
│   Token      │
└──────┬───────┘
       │
       ▼
┌──────────────┐     失败
│ 创建 Git Tag │ ──────────────┐
│  (GitHub API) │               │
└──────┬───────┘               │
       │ 成功                   │
       ▼                       │
┌──────────────┐     失败      │
│ 创建 Release │ ──────────┐   │
│  (GitHub API) │           │   │
└──────┬───────┘           │   │
       │ 成功               ▼   ▼
       ▼              ┌──────────────┐
┌──────────────┐      │  记录错误日志  │
│ 并行上传安装包│      │  状态保持     │
│  (GitHub API) │      │   Pending    │
└──────┬───────┘      │  返回错误信息  │
       │ 全部成功      └──────────────┘
       ▼                    ▲
┌──────────────┐     失败   │
│ 更新状态为    │ ──────────┘
│   Shipped    │
└──────────────┘
```

**幂等处理**：
- 创建 Tag 前，先查询该 Tag 是否已存在，若存在则跳过
- 创建 Release 前，先按 Tag 查询 Release 是否已存在，若存在则复用
- 上传安装包前，检查 Release Assets 中是否已有同名文件，若有则先删后传

### 5.2 文件上传流程

```
客户端上传 (multipart/form-data)
      │
      ▼
┌──────────────────┐
│ Gin 接收文件流    │  ← 限制单文件 500MB (可配置)
│ 不全量加载到内存   │
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│ 校验版本状态      │  ← 必须为 Pending
│ 校验文件类型/大小  │
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│ 流式写入存储      │  ← 存储路径: uploads/{project_id}/{version_id}/{filename}
└──────┬───────────┘
       │
       ▼
┌──────────────────┐
│ 写入 artifact 记录│
└──────────────────┘
```

## 6. 安全设计

### 6.1 敏感数据保护

| 数据 | 保护方式 |
|------|----------|
| 用户密码 | bcrypt 哈希 (cost=10) |
| GitHub Token | AES-256-GCM 加密存储，密钥通过环境变量注入 |
| API Key | SHA-256 哈希存储，仅创建时返回明文 |
| JWT Token | HS256 签名，Secret 通过环境变量注入 |

### 6.2 接口安全

- 所有接口强制鉴权（注册/登录除外）
- API Key 权限严格限制，服务层通过上下文中的认证方式标记进行判断
- 用户只能访问自己的数据，Repository 层所有查询附带 `user_id` 条件
- 文件上传大小限制通过 Gin 中间件配置
- 文件下载通过服务端代理，不暴露存储路径
- 请求速率限制（基于内存令牌桶，单实例部署足够）

### 6.3 输入校验

使用 `validator` 库对所有请求参数进行校验：

- 用户名：2-50 字符，字母数字下划线
- 邮箱：标准邮箱格式
- 密码：最少 8 位，需包含大小写字母和数字
- 版本号：符合 `v` + semver 格式（如 `v1.0.0`）
- 平台标识：枚举值 `android / ios / windows / macos / linux`

## 7. 接口设计规范

### 7.1 统一响应格式

**成功响应**：

```json
{
    "code": 0,
    "message": "success",
    "data": { ... }
}
```

**错误响应**：

```json
{
    "code": 40001,
    "message": "版本号在该项目下已存在",
    "data": null
}
```

**分页响应**：

```json
{
    "code": 0,
    "message": "success",
    "data": {
        "items": [ ... ],
        "total": 100,
        "page": 1,
        "page_size": 20
    }
}
```

### 7.2 错误码规划

| 范围 | 说明 |
|------|------|
| 0 | 成功 |
| 40001-40099 | 参数校验错误 |
| 40100-40199 | 认证错误（Token 无效/过期、API Key 无效） |
| 40300-40399 | 权限错误（API Key 越权、非资源所有者） |
| 40400-40499 | 资源不存在 |
| 40900-40999 | 业务冲突（重复数据、状态不允许） |
| 50000-50099 | 服务器内部错误 |
| 50200-50299 | GitHub API 调用错误 |

### 7.3 路由分组

```go
api := r.Group("/api")
{
    // 公开接口
    auth := api.Group("/auth")
    {
        auth.POST("/register", h.Register)
        auth.POST("/login", h.Login)
    }

    // JWT 必须
    authed := api.Group("", middleware.RequireAuth())
    {
        authed.POST("/auth/logout", h.Logout)
        authed.GET("/auth/me", h.GetMe)
        authed.PUT("/auth/me", h.UpdateMe)
        authed.PUT("/auth/password", h.UpdatePassword)
    }

    // JWT 必须 — API Key 管理
    apiKeys := api.Group("/api-keys", middleware.RequireJWT())
    {
        apiKeys.GET("", h.ListApiKeys)
        apiKeys.POST("", h.CreateApiKey)
        apiKeys.DELETE("/:id", h.DeleteApiKey)
    }

    // JWT 必须 — 项目写操作
    projectWrite := api.Group("/projects", middleware.RequireJWT())
    {
        projectWrite.POST("", h.CreateProject)
        projectWrite.PUT("/:id", h.UpdateProject)
        projectWrite.DELETE("/:id", h.DeleteProject)
    }

    // JWT / API Key 均可 — 项目读操作
    projectRead := api.Group("/projects", middleware.RequireAuth())
    {
        projectRead.GET("", h.ListProjects)
        projectRead.GET("/:id", h.GetProject)
    }

    // JWT 必须 — 版本写操作
    versionWrite := api.Group("/projects/:id/versions", middleware.RequireJWT())
    {
        versionWrite.POST("", h.CreateVersion)
    }
    api.DELETE("/versions/:vid", middleware.RequireJWT(), h.DeleteVersion)
    api.POST("/versions/:vid/ship", middleware.RequireJWT(), h.ShipVersion)

    // JWT / API Key 均可 — 版本读写
    api.GET("/projects/:id/versions", middleware.RequireAuth(), h.ListVersions)
    api.GET("/versions/:vid", middleware.RequireAuth(), h.GetVersion)
    api.PUT("/versions/:vid", middleware.RequireAuth(), h.UpdateVersion)

    // JWT / API Key 均可 — 安装包操作
    api.POST("/versions/:vid/artifacts", middleware.RequireAuth(), h.UploadArtifact)
    api.DELETE("/artifacts/:aid", middleware.RequireAuth(), h.DeleteArtifact)
    api.GET("/artifacts/:aid/download", middleware.RequireAuth(), h.DownloadArtifact)
}
```

## 8. 部署方案

### 8.1 Docker Compose（开发/单机部署）

```yaml
services:
  app:
    build: .
    ports:
      - "8080:8080"
    environment:
      - ENCRYPTION_KEY=${ENCRYPTION_KEY}
      - JWT_SECRET=${JWT_SECRET}
    volumes:
      - app-data:/app/data  # SQLite 数据库文件 + 上传文件

volumes:
  app-data:
```

### 8.2 Dockerfile

```dockerfile
# 构建阶段
FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o fast_ship ./cmd/server

# 运行阶段
FROM alpine:3.19
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
RUN mkdir -p /app/data
COPY --from=builder /build/fast_ship .
COPY --from=builder /build/configs ./configs
EXPOSE 8080
CMD ["./fast_ship"]
```

### 8.3 配置文件模板

```yaml
server:
  port: 8080
  mode: release  # debug / release

database:
  path: ./data/fast_ship.db   # SQLite 数据库文件路径

jwt:
  secret: ""         # 生产环境用环境变量覆盖
  expire_hours: 24

upload:
  max_file_size: 524288000  # 500MB
  storage_path: ./data/uploads

encryption:
  key: ""            # 32 字节 AES 密钥，生产环境用环境变量覆盖
```

## 9. 开发规范

### 9.1 错误处理

- Service 层返回自定义业务错误类型，包含错误码和用户友好的消息
- Handler 层统一捕获并转换为 HTTP 响应
- 不在 Handler 层暴露内部错误细节

### 9.2 数据库事务

以下操作需使用数据库事务：
- 删除项目（级联删除版本和安装包记录 + 清理文件）
- 删除版本（删除记录 + 清理安装包文件）
- 发货成功后更新版本状态 + 写入 GitHub Release URL

### 9.3 日志规范

- 使用结构化日志，关键字段：`request_id`, `user_id`, `action`
- 敏感数据（密码、Token）不写入日志
- 发货流程每一步记录详细日志，便于排查问题

### 9.4 测试策略

| 层级 | 策略 |
|------|------|
| Repository | 使用内存 SQLite (`:memory:`) 进行集成测试，无需外部依赖 |
| Service | Mock Repository 接口进行单元测试 |
| Handler | 使用 httptest 进行 HTTP 接口测试 |
| 发货流程 | Mock GitHub API 进行端到端流程测试 |
