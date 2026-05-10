# Fast Ship

Fast Ship 是一个面向开发团队的**版本发布与 Issue 管理平台**，支持将 GitHub Issues 同步到本地管理，关联版本和构建产物，并提供一键 Ship（发布）流程。项目采用前后端分离架构，后端提供 RESTful API，前端为 SPA 应用。

## Tech Stack

| Layer | Technology |
|---|---|
| Backend Language | Go 1.25 |
| Backend Framework | Gin |
| Database | SQLite (GORM) |
| Frontend Language | TypeScript |
| Frontend Framework | React 19 + Vite 8 |
| State Management | Zustand (client) + TanStack Query (server) |
| UI Library | shadcn/ui + Tailwind CSS 4 |
| HTTP Client | ky |
| Testing (Server) | Go testing + testify assertions |
| Testing (Web) | Vitest + Testing Library |
| Auth | JWT + Refresh Token + API Key |
| Package Manager | pnpm (web) / Go Modules (server) |

## Project Structure

```
fast_ship/
├── server/                          # Go 后端
│   ├── cmd/server/                  # 应用入口 main.go
│   ├── internal/
│   │   ├── config/                  # Viper 配置加载
│   │   ├── handler/                 # HTTP 请求处理器 (auth/project/issue/version/artifact/ai/api_key)
│   │   ├── service/                 # 业务逻辑层 (含 Ship 发货流程)
│   │   ├── repository/              # GORM 数据访问层
│   │   ├── model/                   # 数据模型定义 (11 个 GORM 模型)
│   │   ├── middleware/              # JWT/API Key 认证 + CORS 中间件
│   │   ├── router/                  # API 路由注册 + SPA 静态服务
│   │   └── pkg/                     # 共享工具包
│   │       ├── crypto/              # AES 加解密
│   │       ├── errs/                # 统一错误码
│   │       ├── github/              # GitHub API 客户端
│   │       ├── githubmedia/         # GitHub 媒体代理
│   │       ├── response/            # HTTP 响应格式化
│   │       └── storage/             # 文件存储抽象
│   ├── configs/config.yaml          # 默认配置文件
│   └── data/                        # SQLite 数据库 + 上传目录
├── web/                             # React 前端
│   ├── src/
│   │   ├── main.tsx                 # 应用入口
│   │   ├── App.tsx                  # 路由定义 (lazy loading)
│   │   ├── routes/                  # 页面组件
│   │   │   ├── projects/            # 项目列表/新建/详情/编辑
│   │   │   ├── versions/            # 版本列表
│   │   │   ├── issues/              # Issue 列表
│   │   │   ├── settings/            # 用户设置 (profile/password/ai/api-keys)
│   │   │   ├── login.tsx            # 登录页
│   │   │   └── register.tsx         # 注册页
│   │   ├── components/
│   │   │   ├── ui/                  # shadcn/ui 基础组件 (20 个)
│   │   │   ├── layout/              # Header + Sidebar
│   │   │   ├── issues/              # Issue 表单组件
│   │   │   └── projects/            # GitHub Token 帮助对话框
│   │   └── lib/
│   │       ├── api/                 # ky HTTP 客户端 + 8 个 API 模块
│   │       ├── hooks/               # TanStack Query hooks (7 个)
│   │       ├── store/               # Zustand 状态 (auth/theme)
│   │       └── utils/               # 工具函数、验证器、格式化
│   └── package.json
├── scripts/dev.sh                   # 开发启动脚本
├── Makefile                         # 统一开发命令入口
├── Dockerfile                       # 多阶段构建 (Go + Node → Alpine)
├── .github/workflows/               # CI (lint + typecheck) + Docker 发布
└── docs/                            # Codedocs 文档输出
```

## Architecture

Fast Ship 采用经典的三层架构（Handler → Service → Repository），后端通过 Gin 框架暴露 RESTful API，前端通过 ky HTTP 客户端和 TanStack Query 进行数据获取和缓存管理。

**认证流程**：系统支持三种认证方式 — JWT Token（常规用户）、Refresh Token（自动续期）和 API Key（程序化访问）。后端中间件根据请求路径决定使用哪种认证方式，部分读操作（如获取项目列表）同时支持 JWT 和 API Key。

**Ship 发布流程**：核心业务流程 — 创建版本后，关联 Issue 和构建产物（Artifact），执行 Ship Check 验证，然后一键发布到 GitHub（创建 Tag/Release、上传产物、同步 Issue 状态到 GitHub）。

**Issue 同步**：支持双向同步 — 可从 GitHub 仓库导入 Issues，也可创建内部 Issue（不关联 GitHub）。内部 Issue 支持工作流状态管理和 Checklist。

**媒体代理**：通过后端代理访问 GitHub 媒体资源（Issue 中的图片等），解决私有仓库资源无法直接在浏览器中展示的问题。

## Entry Points

| Entry Point | Purpose |
|---|---|
| `server/cmd/server/main.go` | Go 后端入口：初始化配置、数据库、服务、路由，启动 HTTP 服务 |
| `web/src/main.tsx` | React 前端入口：挂载根组件，配置 ThemeProvider 和 QueryClient |
| `web/src/App.tsx` | 路由定义：lazy loading 页面组件，认证布局守卫 |
| `scripts/dev.sh` | 开发脚本：同时启动前后端开发服务 |

## Module Map

| Module | Path | Description | Files |
|---|---|---|---|
| server-app | `server/cmd/` + `server/internal/config/` | 应用入口和配置管理 | 2 |
| server-router | `server/internal/router/` | API 路由注册和 SPA 静态文件服务 | 2 |
| server-handler | `server/internal/handler/` | HTTP 请求处理器层 | 8 |
| server-service | `server/internal/service/` | 业务逻辑层 | 8 |
| server-repository | `server/internal/repository/` | GORM 数据访问层 | 11 |
| server-model | `server/internal/model/` | 数据模型和 GORM 定义 | 11 |
| server-middleware | `server/internal/middleware/` | 认证和 CORS 中间件 | 2 |
| server-pkg | `server/internal/pkg/` | 共享工具包 | 7 |
| web-app | `web/src/` (根) | 应用入口、根组件、全局样式 | 3 |
| web-routes | `web/src/routes/` | 页面组件和路由定义 | 22 |
| web-components | `web/src/components/` | UI 组件库 | 27 |
| web-api | `web/src/lib/api/` | HTTP 客户端和 API 模块 | 8 |
| web-hooks | `web/src/lib/hooks/` | TanStack Query hooks | 7 |
| web-state | `web/src/lib/store/` + context | Zustand 状态管理 | 4 |
| web-utils | `web/src/lib/utils/` | 工具函数、验证器 | 4 |

> Sub-modules: web-components 拆分为 `web-components/ui` 和 `web-components/features`

## Cross-cutting Patterns

| Pattern | Doc | Modules Affected |
|---|---|---|
| Authentication | `patterns/authentication.md` | server-middleware, server-handler, server-service, server-repository, web-state, web-api |
| API Conventions | `patterns/api-conventions.md` | server-handler, server-pkg, web-api, web-hooks |
| Testing Strategy | `patterns/testing-strategy.md` | server (全部), web (全部) |

## Key Concepts

- **Ship**：版本发布操作。创建 GitHub Tag/Release、上传构建产物、同步 Issue 状态。
- **Artifact**：构建产物（如 APK、IPA、ZIP 等文件），关联到具体版本。
- **Issue Sync**：从 GitHub 仓库导入 Issues 到本地，支持自动定时同步。
- **Internal Issue**：不关联 GitHub 的内部 Issue，支持独立的工作流状态管理。
- **API Key**：用于程序化访问的密钥，支持权限范围控制。
- **GitHub Media Proxy**：代理访问私有仓库中的媒体资源，通过后端中转鉴权。

## Getting Started

See `GETTING_STARTED.md` for the full development guide.

## Documentation Coverage

Generated by codedocs. Coverage: 97% (126/130 source files).
Last updated: 2026-05-10 at commit `000a45c`.
