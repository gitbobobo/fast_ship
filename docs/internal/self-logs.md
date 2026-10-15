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

### 请求链路贯穿范围

已贯穿（业务日志带同一 `request_id`，可按响应头查 `/trace`）：

- HTTP 中间件请求日志器（鉴权成功后补 `user_id`；panic 路径同样读 `GetUserID`）
- `AuthService`（注册 / 登录 / 改密）与 `ApiKeyService`（创建 / 删除）：从 `context` 取请求 logger

未贯穿（后台/zap 路径没有 HTTP 请求，不把 request_id 硬塞进调度循环）：

- 自动化协调循环、Issue 后台同步、GitHub / T3 / AI 等 zap 服务
- 这些路径在能拿到归属时写 `user_id`（automation 失败分支、issue 同步），查询靠 `user_id` / `run_id` / `project_id`，不靠 request 链路

`user_id=""` 只留给真正的进程级日志（启动、清理失败等）。请求路径与已归属的后台任务不得再写空归属。

## 存储

独立文件 `logging.self_log_path`（默认 `./data/self_logs.db`），不进业务库 AutoMigrate，无外键。

保留：默认 7 天 + 最大 100 万条，清理周期默认 1 小时，均可在 `server/configs/config.yaml` 的 `logging` 段配置。
清理按主键分批删除（每批独立事务），结束后 `wal_checkpoint(TRUNCATE)`。

## 查询

`GET /api/self-logs`（列表，键集游标）、`/aggregate`（按 module/level/time 桶；时间桶保留最近 2000，`truncated` 标记截断）、`/trace`（`request_id` 或 `run_id` 二选一；超出保留最新 1000 条）。

JWT 与 `fsk_` API Key 可查本人归属及无归属的进程级日志；`fsr_` 运行/刷新令牌返回 40301。契约在 `server/api/openapi.yaml`。
