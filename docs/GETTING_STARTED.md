# Getting Started

Fast Ship 本地开发完整指南。涵盖环境准备、安装、开发、测试、构建、格式检查等所有操作。

## Prerequisites

| Tool | Version | Install |
|---|---|---|
| Go | >= 1.25 | https://go.dev/dl/ |
| Node.js | >= 22 | https://nodejs.org/ |
| pnpm | >= 10 | `npm install -g pnpm` |
| Git | any | https://git-scm.com/ |

## Installation

```bash
# 克隆仓库
git clone <repo-url>
cd fast_ship

# 安装前端依赖
cd web
pnpm install
cd ..
```

后端依赖在首次构建时自动下载 (`go mod download`)。

## Environment Setup

后端默认配置位于 `server/configs/config.yaml`，开箱即用。默认配置包括：

- 服务端口：`8080`
- SQLite 数据库：`server/data/fast_ship.db`
- 上传目录：`server/data/uploads`

环境变量可覆盖配置（前缀 `FAST_SHIP_`）：

```bash
# 关键环境变量
FAST_SHIP_SERVER_MODE=release      # 运行模式 (debug/release)
FAST_SHIP_DATABASE_LOG_SQL=true    # 打印 SQL 日志
JWT_SECRET=your-secret-key         # JWT 签名密钥
ENCRYPTION_KEY=your-aes-key        # AES 加密密钥（用于 GitHub Token 加密）
FAST_SHIP_WEB_DIST_DIR=/app/web    # 前端静态资源目录（生产环境）
```

使用自定义配置文件：

```bash
CONFIG_PATH=/your/config.yaml make dev-server
```

## Development

```bash
# 同时启动前后端开发服务
make dev

# 或分别启动
make dev-server   # 后端 → http://localhost:8080
make dev-web      # 前端 → http://localhost:5173
```

前端通过 Vite 代理将 `/api` 请求转发到 `http://localhost:8080`。

按 `Ctrl+C` 停止所有服务。

## Testing

```bash
# 运行全部测试（后端 + 前端）
make test

# 只运行后端测试
make test-server
# 或
cd server && go test ./...

# 只运行前端测试
make test-web
# 或
cd web && pnpm test

# 运行特定后端测试
cd server && go test ./internal/service/...

# 运行特定前端测试文件
cd web && pnpm test -- src/routes/__tests__/auth-pages.test.tsx
```

## Building

```bash
# 构建后端和前端
make build

# 分别构建
make build-server   # 编译 Go 二进制 → server/server
make build-web      # 构建前端产物 → web/dist/
```

## Linting & Formatting

```bash
# 运行全部检查
make lint

# 分别检查
make lint-server   # gofmt + go vet
make lint-web      # ESLint + TypeScript 类型检查

# 前端单独类型检查
cd web && pnpm typecheck

# 前端单独 lint
cd web && pnpm lint
```

## Database

后端使用 SQLite，无需额外安装数据库服务。数据库文件在首次启动时自动创建。

- 数据库路径：`server/data/fast_ship.db`
- GORM AutoMigrate 在启动时自动执行 schema 迁移
- 无需手动运行迁移命令

## Common Workflows

### 添加新的 API 端点

1. 在 `server/internal/model/` 定义数据模型（如需要）
2. 在 `server/internal/repository/` 添加数据访问方法
3. 在 `server/internal/service/` 添加业务逻辑
4. 在 `server/internal/handler/` 添加 HTTP 处理器
5. 在 `server/internal/router/router.go` 注册路由
6. 在 `web/src/lib/api/` 添加 API 调用函数
7. 在 `web/src/lib/hooks/` 添加 React Query hook
8. 在 `web/src/routes/` 添加页面组件

### 添加新的前端页面

1. 在 `web/src/routes/` 创建页面组件
2. 在 `web/src/App.tsx` 中使用 `createLazyRoute` 注册路由
3. 在 sidebar/header 中添加导航链接

> 当前默认首页是 `/dashboard` 仪表盘；如果新增页面要参与主导航，请同步检查默认重定向和侧边栏顺序。

### 仪表盘统计口径

- **剩余开启问题**：汇总所有已配置项目中当前仍为开启状态的问题数量，并按项目展示。
- **近 30 天已解决问题**：按问题首次进入 `done`/“已完成”状态的时间聚合到每天；同一个问题即使重复回退再完成，也只统计一次。
- **空状态**：没有项目、没有开启问题或最近 30 天没有已解决问题时，仍会渲染 0 值图表并展示提示文案。

### 运行部分测试

```bash
# 后端：指定包路径
cd server && go test ./internal/service/

# 后端：运行特定测试函数
cd server && go test -run TestShipCheck ./internal/service/

# 前端：指定测试文件
cd web && pnpm test -- --reporter=verbose src/lib/api/client.test.ts
```

## CI/CD

### 本地复现 CI 检查

```bash
# CI 检查前端 lint + typecheck
cd web && pnpm check

# CI 完整流程
make lint && make test && make build
```

### Docker 镜像发布

```bash
# 创建版本标签触发自动构建
git tag v1.0.0
git push origin v1.0.0
```

Docker 镜像会自动构建并推送到 `ghcr.io/<owner>/<repo>`，同时带上版本 tag 和 `latest` 标签。

### Docker 本地构建

```bash
docker build -t fast_ship .
docker run -p 4888:4888 -v $(pwd)/server/data:/app/data fast_ship
```

生产环境使用端口 `4888`（开发环境使用 `8080`）。

## Dependency Management

```bash
# 整理后端 Go 依赖
make tidy

# 更新前端依赖
cd web && pnpm update
```
