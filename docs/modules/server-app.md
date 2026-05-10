# Server App

应用入口和配置管理。`main.go` 负责初始化所有依赖并启动 HTTP 服务；`config.go` 通过 Viper 加载 YAML 配置并支持环境变量覆盖。

## Public API

| Export | Type | Description |
|---|---|---|
| `main()` | func | 应用入口：初始化配置 → 数据库 → 服务 → Handler → 路由 → 启动 HTTP 服务 |
| `config.Load(path)` | func | 加载配置文件，返回 `*Config` |
| `config.Config` | struct | 配置结构体，包含 Server/Database/JWT/Upload/Encryption/Issues 六个子配置 |

## Internal Structure

| File | Purpose |
|---|---|
| `server/cmd/server/main.go` | 应用入口，依赖注入和启动流程 |
| `server/internal/config/config.go` | 配置结构定义和 Viper 加载逻辑 |

## Dependencies

| Depends on | Why |
|---|---|
| server-model | AutoMigrate 数据库迁移 |
| server-repository | 初始化所有 Repository |
| server-service | 初始化所有 Service |
| server-handler | 初始化所有 Handler |
| server-router | 注册路由 |
| server-middleware | 提供 CORS 中间件 |
| `spf13/viper` | YAML 配置解析和环境变量绑定 |

## Implementation Notes

- `main.go` 在启动时开启两个后台 goroutine：JWT 黑名单定时清理和 Issue 自动同步
- 配置支持 `FAST_SHIP_` 前缀的环境变量覆盖，`JWT_SECRET` 和 `ENCRYPTION_KEY` 有独立的环境变量名
- 数据库使用 GORM AutoMigrate，schema 变更通过代码自动应用，无需手动迁移文件
