# fast_ship

`fast_ship` 是一个前后端分离项目：

- `server/`：Go + Gin 后端
- `web/`：React + Vite 前端

现在推荐直接通过根目录统一命令启动和管理开发流程。

## 环境要求

- Go `1.25.x`
- Node.js
- `pnpm`

首次启动前请先安装前端依赖：

```bash
cd web
pnpm install
```

## 快速开始

在仓库根目录执行：

```bash
make dev
```

这会同时启动：

- 后端开发服务：`http://localhost:8080`
- 前端开发服务：`http://localhost:5173`

开发时前端通过 Vite 代理将 `/api` 请求转发到 `http://localhost:8080`。

按 `Ctrl+C` 会同时停止前后端进程。

## 常用命令

```bash
make
make help
make dev
make dev-server
make dev-web
make build
make test
make lint
make tidy
make clean
```

命令说明：

- `make dev`：同时启动前后端开发服务
- `make dev-server`：只启动后端
- `make dev-web`：只启动前端
- `make build`：构建后端二进制和前端产物
- `make test`：运行后端和前端测试
- `make lint`：运行 Go 格式/静态检查与前端 ESLint + TypeScript 校验
- `make tidy`：整理后端 Go 依赖
- `make clean`：清理后端构建产物和前端 `dist`

## 配置说明

后端默认配置文件是 [server/configs/config.yaml](/Users/godbobo/work/projects/fast_ship/server/configs/config.yaml)。

默认配置包括：

- 服务端口：`8080`
- SQLite 数据库：`server/data/fast_ship.db`
- 上传目录：`server/data/uploads`

如果需要使用其他配置文件，可以在启动后端前设置：

```bash
CONFIG_PATH=/your/config.yaml make dev-server
```

## 项目结构

```text
fast_ship/
├── server/     # Go 后端
├── web/        # React 前端
├── scripts/    # 根目录脚本
└── Makefile    # 根目录统一入口
```

## 补充说明

- 根目录命令只是统一入口，不会替代子项目自己的原生命令。
- 后端仍可在 `server/` 下使用 `make dev`、`make build` 等命令。
- 前端仍可在 `web/` 下使用 `pnpm dev`、`pnpm lint`、`pnpm typecheck`、`pnpm check`、`pnpm test` 等命令。
- 需求和设计文档位于 [docs](/Users/godbobo/work/projects/fast_ship/docs)。

## Docker 镜像发布

- 当前仓库的整站 Docker 镜像构建使用 [Dockerfile](/Users/godbobo/work/projects/fast_ship/Dockerfile)。
- 镜像会同时构建 `web/` 前端并编译 `server/` 后端，启动后由 Go 服务直接托管前端静态资源。
- 推送形如 `v1.0.0` 的 Git tag 后，GitHub Actions 会自动构建并推送镜像到 `ghcr.io/<owner>/<repo>`。
- 推送后的镜像会同时带上对应 tag 和 `latest` 标签。

示例：

```bash
git tag v1.0.0
git push origin v1.0.0
```
