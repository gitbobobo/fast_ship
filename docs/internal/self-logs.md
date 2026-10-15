# 自身日志（INT-94）

Fast Ship 服务端自己的结构化日志：采集、独立存储、只读查询。
与项目运行日志（`/api/projects/:id/logs`）无关，也没有 Web 界面。

## 采集

统一走 `log/slog`。`cmd/server/main.go` 启动独立 SQLite 管道（`internal/pkg/selflog`）：

- slog（鉴权、API Key、HTTP 请求中间件）→ `Pipeline.Handler()`：镜像 stderr，并按 `logging.min_level` 入库。
- 既有 zap 服务（GitHub / T3 / 自动化 / AI）保持 `*zap.Logger`，用 `zapcore.NewTee` 复制一份到 `Pipeline.IngestHandler()`（zap 自己已经打过控制台，不再镜像）。
- HTTP 中间件 `RequestLog` 生成或透传 `X-Request-Id`（仅 UUID/字母数字/横线，最长 128；非法则丢弃并改生成），记录方法、实际路径、路由模板、状态码、耗时；panic 与 5xx 附堆栈。不记录请求体。
- 静态资源/SPA 的 2xx 成功请求不入库。
- 入库前：所有持久化字符串（含关联列）做形状脱敏；附加字段按确切敏感 key 递归整值打码（`password` / `token` / `api_key` / `secret` / `authorization` 等，`token_id` / `api_key_id` 不打码）。写库失败只计数丢弃，不反压业务。

关联字段进固定列：`module`、`request_id`、`user_id`、`project_id`、`issue_id`、`automation_id`、`run_id`、`thread_id`；其余进 `extra` JSON。

`system_scope` 是写入侧的显式系统行标记（slog 属性 `system_scope`，bool）。仅进程级启动日志使用 `slog.Bool(selflog.AttrSystemScope, true)`；不要把该属性挂到交给 auth/api_key 的 base logger。管道自身的缓冲满/写库失败只写 stderr，不标系统。调度器无关联列的批次失败也不标系统——漏标的后果是 API 里看不到，stderr 仍有。

行带 `user_id` 或任一业务关联列（`project_id` / `issue_id` / `automation_id` / `run_id` / `thread_id`）时，`system_scope` 强制为 0，拒绝「误标系统行公开业务日志」。

### 请求链路贯穿范围

已贯穿（业务日志带同一 `request_id`，可按响应头查 `/trace`）：

- HTTP 中间件请求日志器（鉴权成功后补 `user_id`；panic 路径同样读 `GetUserID`）
- `AuthService`（注册 / 登录 / 改密）与 `ApiKeyService`（创建 / 删除）：从 `context` 取请求 logger

未贯穿（后台/zap 路径没有 HTTP 请求，不把 request_id 硬塞进调度循环）：

- 自动化协调循环、Issue 后台同步、GitHub / T3 / AI 等 zap 服务
- 这些路径在能拿到归属时写 `user_id`；漏标时由归属解析器按关联列补全，补不上则该行对 API 隐藏（失败方向是不可见，不是公开）

### 归属解析器

`Handle` 只入队。刷盘前（`batchLoop`）按关联列批量解析空 `user_id`：

- `run_id` → `automation_runs.user_id`
- `automation_id` → `automations.user_id`
- `issue_id` → `issues.project_id` → `projects.user_id`
- `project_id` → `projects.user_id`
- `thread_id` → `automation_runs.thread_id` → `user_id`

规则：只处理 `user_id` 为空、带关联列、未标系统的行；显式 `user_id` 永不覆盖；同一行多个键解析到不同用户 → 保持无主；超时/出错/查无 → 无主。批内按列去重后 `IN` 查询，禁止逐行。查无的键负缓存 1 分钟。解析器不打 zap/slog。

业务库 repository 构造完成后再 `SetOwnerResolver`。管道先于业务库启动期间的日志只能是启动类（走 `system_scope`）。

## 存储

独立文件 `logging.self_log_path`（默认 `./data/self_logs.db`），不进业务库 AutoMigrate，无外键。表含 `system_scope INTEGER NOT NULL DEFAULT 0`；存量空归属行不回填，保留期内从 API 消失（含旧「系统启动」行）。

保留：默认 7 天 + 最大 100 万条，清理周期默认 1 小时，均可在 `server/configs/config.yaml` 的 `logging` 段配置。
清理按主键分批删除（每批独立事务），结束后 `wal_checkpoint(TRUNCATE)`。

## 查询

`GET /api/self-logs`（列表，键集游标）、`/aggregate`（按 module/level/time 桶；时间桶保留最近 2000，`truncated` 标记截断）、`/trace`（`request_id` 或 `run_id` 二选一；超出保留最新 1000 条）。三条路径共用可见性谓词：

`user_id = ? OR (system_scope = 1 AND user_id = '' AND project_id = '' AND issue_id = '' AND automation_id = '' AND run_id = '' AND thread_id = '')`

JWT 与 `fsk_` API Key 可查本人归属及显式系统行；无主行（解析失败、键冲突、漏标）对任何认证用户均不可见。`fsr_` 运行/刷新令牌返回 40301。公开查询入口拒绝空 requester。契约形状不变，见 `server/api/openapi.yaml`。
