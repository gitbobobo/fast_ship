# 订阅套餐额度（INT-109）

用户在页面粘贴各家 AI 编码服务的凭据，服务端定时抓取订阅额度并只读展示。
覆盖 7 家供应商：Command Code、OpenCode Go、智谱 GLM Coding Plan、Grok、
Codex、Devin、Factory Droid。取数与解析细节以 CodexBar（MIT）Swift 源码为准。

## 模块边界

- `internal/pkg/quota`（取数包）：只管「用一组凭据向某供应商拉一次额度并解析成
  统一 `Snapshot`」。`Registry(baseURLs)` 返回固定顺序的 `Provider` 列表（元数据 +
  `Fetch`），`FetchError` 区分 `credential_invalid`（401/403，提示换凭据）与
  `provider_error`（网络/5xx/解析失败）。加密、落库、调度、HTTP 都不在这一层。
- `service/quota.go`（`QuotaService`）：凭据校验与加密、CRUD、`Probe`
  （`POST /api/quota/test`，不落库）、`Refresh`（手动刷新，冷却内 42900）、
  抓取结果落库与退避计算。`NewQuotaService(repo, cfg, logger, registry)` 的
  `registry` 是取数注入点——nil 时用 `quota.Registry(cfg.Quota.BaseURLs)`，
  测试注入假 registry。同一账号的并发抓取（手动 vs 调度）由 `fetchLocks`
  按账号 id 串行化。
- `repository/quota_account.go`（`QuotaAccountRepository`）：行级读写。
  单账号操作一律带 `user_id` 条件；`ListDue` / `EarliestNextRetryAt` /
  `FindByIDAny` 跨用户，仅供调度器使用。
- `service/quota_scheduler.go`（`QuotaScheduler`）：后台循环。`Start`/`Stop`
  与 `automationScheduler` 同款；`main.go` 在 `quota.enabled` 为真时启动。

## 存储与加密

表 `quota_accounts`（模型 `model.QuotaAccount`，已入 `autoMigrateModels`）：

| 列 | 说明 |
|---|---|
| `id` / `user_id` | TEXT 主键；user_id 索引 + FK users cascade |
| `provider` / `label` | 供应商 id；用户起的账号名 |
| `credentials_encrypted` | BLOB。整组凭据 JSON `{"key":"value"}` 的 AES-256-GCM 密文（`internal/pkg/crypto`）；**不存明文指纹**——允许同用户重复添加相同凭据 |
| `credential_hint` | 形如 `…ab12`，只存首个必填密文字段的末 4 位；不足 8 位的凭据整段打码（`…••••`）——短凭据的「末 4 位」等于全文或大半个文，而该字段会随账号列表返回 |
| `snapshot_json` | 最近一次成功抓取的 `quota.Snapshot` JSON 原文（windows/balance/plan_name/fetched_at） |
| `plan_name` / `status` / `last_error` | 冗余出快照便于查询；status ∈ ok / credential_invalid / provider_error / pending |
| `last_fetched_at` / `last_success_at` | 最近一次**成功**抓取时间（= 快照数据时间）；失败不触碰，两者等值 |
| `failure_count` / `next_retry_at` | 连续失败数；下一次计划抓取的绝对时间。NULL 表示从未抓取过，或凭据刚被替换而清空退避（两种情形 ListDue 都视为立即到期，各自的自愈路径依赖这一点） |

凭据明文只存在于请求体与解密后的内存；响应、日志、`last_error` 中不出现
明文（fetcher 的 `FetchError.Message` 按契约已脱敏）。

## 状态与退避

- 创建：校验（未知供应商 40030、必填凭据缺失/空白 40031）→ 落库 pending →
  同步抓一次 → 响应带最新状态。PUT 只改 label 不抓取；给 credentials 则整组
  替换（状态打回 pending、清退避）并同步重抓。
- 成功：写 `snapshot_json`/`plan_name`/`status=ok`/`last_error=""`/
  `last_fetched_at`/`last_success_at`（取 `snapshot.fetched_at`，缺省或
  未来值都回退当前时间——未来值会把手动刷新冷却锁死），`failure_count=0`、
  `next_retry_at = min(now + interval, 开窗 reset_at + 30s)`——成功后始终落
  绝对时间，NULL 只保留给从未抓取过的账号；否则 `ListDue` 把 NULL 当立即
  到期，任何一个失败账号的退避唤醒都会连带重抓全部健康账号。
- 失败：**只写** `status`（credential_invalid/provider_error）/`last_error`/
  `failure_count+1`/`next_retry_at = now + min(2^n 分钟, 30 分钟)`——保留上一次
  成功快照与数据时间，列表仍展示旧额度数据并标出错误。
- 手动 `POST /accounts/{id}/refresh`：立即抓并落库；距上次成功抓取 <
  `manual_refresh_cooldown_seconds`（默认 30s）→ 42900（HTTP 429，message 含
  等待秒数）。失败不更新 `last_fetched_at`，修凭据后可立即重试。
- 用户隔离：他人的账号在改/删/刷新上一律 40430，不泄露存在性。

## 调度

默认每 10 分钟一轮（`quota.interval_minutes`），`run_on_startup` 为真时启动
先跑一轮。每轮抓 `next_retry_at` 为空或到期的账号，并发上限
`quota.max_concurrency`（默认 3），单次请求超时 `quota.request_timeout_seconds`
（默认 20s）；单账号失败互不影响。

下一次唤醒不是固定 10 分钟边界，而是取 `min(now + interval, 最早的未来
next_retry_at)`：失败退避到期与「窗口预抓」都按自己的时刻唤醒。
窗口预抓（契约 §7）：成功快照里任一窗口 `reset_at` 落在「现在 + interval」
之内时，把该账号 `next_retry_at` 提前到 `reset_at + 30s`（取 min），让额度
进度条在重置后很快刷出满额；无临近重置时 `next_retry_at` 也落
`now + interval` 的绝对时间（不是 NULL），保证各账号按自己的节奏到期。

`quota.enabled=false` 时接口仍可用（手动刷新正常），只是没有后台轮。

## 配置（`server/configs/config.yaml` + `FAST_SHIP_QUOTA_*`）

```yaml
quota:
  enabled: true                      # FAST_SHIP_QUOTA_ENABLED
  interval_minutes: 10               # FAST_SHIP_QUOTA_INTERVAL_MINUTES
  run_on_startup: true               # FAST_SHIP_QUOTA_RUN_ON_STARTUP
  max_concurrency: 3                 # FAST_SHIP_QUOTA_MAX_CONCURRENCY
  request_timeout_seconds: 20        # FAST_SHIP_QUOTA_REQUEST_TIMEOUT_SECONDS
  manual_refresh_cooldown_seconds: 30 # FAST_SHIP_QUOTA_MANUAL_REFRESH_COOLDOWN_SECONDS
  base_urls: {}                      # {"<provider id>": "http://..."} 仅部署/测试用，无环境变量绑定
```

数值项均有默认回退（`QuotaConfig` 上的方法），`base_urls` 只经配置文件覆盖。
`enabled` 与 `run_on_startup` 用 `viper.SetDefault` 设为 `true`：老部署的配置文件
里没有 `quota` 段，只看结构体零值会把它们反序列化成 `false`，等于升级后静默
停掉后台抓取；显式写 `false` 仍能关闭（有 `config_test.go` 用例钉住）。

## API 与用户隔离 / API Key 边界

路由见 `router.go` 与 openapi `quota` tag：

- 仅 JWT：`GET /api/quota/providers`、`POST/PUT/DELETE /api/quota/accounts[/{id}]`、
  `POST /api/quota/accounts/{id}/refresh`、`POST /api/quota/test`。
- JWT + API Key（只读）：`GET /api/quota/accounts`——即「机器人读取额度接口」，
  API Key 不写。响应不含凭据（只有打码后的 `credential_hint`）。

错误码：40030 供应商未知；40031 凭据字段缺失或非法；40430 账号不存在
（含他人账号）；42900 手动刷新过于频繁（HTTP 429，由 handler 显式映射——
`HandleAppError` 的区间表不覆盖 429xx）。

## 明确不做与已知取舍

- **Codex 服务端 OAuth 刷新**：Codex 的 access_token 是用户本机 Codex CLI 的
  网页会话令牌，服务端代刷可能使其 refresh token 失效且无法在线验证。首期
  只读展示：供应商元数据 `warning` 已提示「过期后重新粘贴」，前端表单回显
  该警告。后续若要支持，需先验证与用户本机 CLI 的令牌轮换兼容性。
- 不做购买/续费/改套餐；不新增机器人工具（API Key 读 `GET /accounts` 即为
  机器人通道）。
- **Codex `additional_rate_limits` 未移植**：CodexBar 把 wham/usage 响应里
  模型级限额（如 Codex Spark 的 5 小时/每周窗口）映射成额外窗口；首版只解析
  primary/secondary 窗口与余额池，模型级窗口不展示。
- **zai `unit=5` 含义未确认**：官方样例确认的单位只有 1=天（30 天按月度
  窗口）、3=小时、6=周；`TIME_LIMIT`（MCP 次数）条目出现的 `unit=5` 等单位
  含义不明，取数按 type 识别窗口、不依赖未确认单位（未知单位的
  TOKENS_LIMIT/CREDIT_LIMIT 一律归 `other`）。
- **Command Code 套餐记忆缓存未移植**：CodexBar 的 `CommandCodePlanCache`
  会在订阅接口失败时沿用上次确认的套餐额度（最长 24h）；首版每次抓取都
  现场解析，订阅接口失败时对应窗口直接缺失，不做跨次记忆。
- **Devin 顶层百分比 `<=1` 缩放**：`daily_percentage`/`weekly_percentage`
  等字段取值 `<=1` 时按 `×100` 处理、否则按已是百分数处理——刻意与
  CodexBar `DevinUsageSnapshot.percent(from:)` 的判定对齐（同一字段两种
  量纲都出现过）。
- **Codex `remaining_balance` 富化未移植**：CodexBar 在 `credits.has_credits`
  为真、非不限量且内联 `credits.balance` 缺失时，会再请求 `remaining_balance`
  端点补工作区余额（该请求还需要 `account_id`）。首版只展示响应内联的
  `credits.balance`，缺失时余额为空。
- **Devin 嵌套配额的「剩余」不当总量**：CodexBar 的通用字典回退把 `available`
  同时当 limit 候选，`{used:3, available:7}` 会算成 42.9%；这里把 `available`/
  `remaining` 只当剩余，没有显式上限时用「已用 + 剩余」推总量（得 30%），
  只有剩余而无总量时不给百分比。这是相对 CodexBar 的刻意偏离：静默偏低的
  百分比会让用户以为还有余量。
- **数值类型一律 float64**：契约里 `used_percent`/`used`/`limit`/`amount`
  在 openapi 显式标 `format: double`（生成 `float64`/`*float64`）。金额与
  百分比不降 float32 是显式决定——`10.1` 降精度会变成 `10.100000381469727`。
