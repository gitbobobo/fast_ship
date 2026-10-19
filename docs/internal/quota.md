# 订阅套餐额度（INT-109/INT-117）

用户在页面粘贴各家 AI 编码服务的凭据，服务端定时抓取订阅额度并只读展示。
覆盖 8 家供应商：Command Code、OpenCode Go、智谱 GLM Coding Plan、Grok、
Codex、Devin、Factory Droid、OpenDesign（Design Plan）。取数与解析细节以
CodexBar（MIT）Swift 源码为准；OpenDesign 部分另见下方专节。

## 模块边界

- `internal/pkg/quota`（取数包）：只管「用一组凭据向某供应商拉一次额度并解析成
  统一 `Snapshot`」。`Registry(baseURLs)` 返回固定顺序的 `Provider` 列表（元数据 +
  `Fetch`），`FetchError` 区分 `credential_invalid`（401/403，提示换凭据）与
  `provider_error`（网络/5xx/解析失败）。加密、落库、调度、HTTP 都不在这一层。
  支持浏览器设备授权的供应商在这里实现自己的 `DeviceAuth` 协议适配器
  （`Provider.DeviceAuth` 非空即代表有该能力；OpenDesign 的在
  `deviceauth_opendesign.go`），上游路径、字段名与状态词表不出这个包。
- `service/quota.go`（`QuotaService`）：凭据校验与加密、CRUD、`Probe`
  （`POST /api/quota/test`，不落库）、`Refresh`（手动刷新，冷却内 42900）、
  抓取结果落库与退避计算。`NewQuotaService(repo, cfg, logger, registry)` 的
  `registry` 是取数注入点——nil 时用 `quota.Registry(cfg.Quota.BaseURLs)`，
  测试注入假 registry。同一账号的并发抓取（手动 vs 调度）由 `fetchLocks`
  按账号 id 串行化。设备授权会话编排与其三个方法在 `service/quota_device.go`
  （INT-117），会话表只在内存。
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
| `snapshot_json` | 最近一次成功抓取的 `quota.Snapshot` JSON 原文（windows/balance/plan_name/note/fetched_at） |
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

调度器睡下后，HTTP 侧的写入仍可能把某个账号的 `next_retry_at` 改早（新建账号
同步抓取失败 → 2 分钟后重试、换凭据、手动刷新）。`QuotaService` 在每次抓取
结果落库后调 `SetKick` 注入的 `QuotaScheduler.Kick()`（缓冲 1，合并），
循环收到后丢弃旧定时器重算等待——否则要等满原来的 interval 才会重试。

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
  `POST /api/quota/accounts/{id}/refresh`、`POST /api/quota/test`、
  `POST/GET/DELETE /api/quota/device-authorizations[/{id}]`（INT-117 设备授权，见下节）。
- JWT + API Key（只读）：`GET /api/quota/accounts`——即「机器人读取额度接口」，
  API Key 不写。响应不含凭据（只有打码后的 `credential_hint`）。

错误码：40030 供应商未知；40031 凭据字段缺失或非法（含供应商 `Validate` 钩子的
结构校验，如 OpenDesign 的模型面密钥）；40032 该供应商不支持设备授权；
40430 账号不存在（含他人账号）；40431 授权会话不存在或已过期；42900 手动刷新
过于频繁（HTTP 429，由 handler 显式映射——`HandleAppError` 的区间表不覆盖
429xx）；50204 额度供应商授权服务不可用（HTTP 502）。

## 前端展示（INT-130）

页面只做「剩余」视角，取数口径集中在 `web/src/lib/quota.ts` 的纯函数里
（组件不认识供应商）：

- **有效剩余** = `100 − 该池内各窗口 used_percent 的最大值`；取到最大值的那
  个窗口叫「受限窗口」，它的 `reset_at` 就是该窗口的重置时间。全部窗口都算不
  出百分比（`used_percent` 为 null）时剩余为 null，卡片不显示大号数字。
- **恢复时间**（已用尽账号）= 池内**所有 `used_percent === 100` 的窗口**里最晚
  的那个 `reset_at`（只取受限窗口会报早：同池两个窗口都满时，先重置的那个不
  代表池恢复）；账号恢复时间再取各池中的最早值。相关窗口缺 `reset_at` 时该池
  不算出恢复时间，全部算不出则显示「恢复时间未知」。
- **额度池**：按 `QuotaWindow.pool` 归并窗口（缺省 = 单池，不出选择器）。有多
  个池时默认选中**第一个未用尽的池**（即正在消耗的那个），选择器只显示池名，
  用尽状态进 `title` / `aria-label`（不写状态文字是为了让它挤进剩余数字那一
  行，见下条）；池顺序按窗口首次出现保持。已知限界：这个顺序依赖适配器按窗口
  类型（5 小时 → 每周 → 每月）稳定发射窗口，某池缺靠前的窗口类型时它可能排到
  前面，默认选中池也随之改变——双池供应商目前三个窗口都齐全，不会触发。
- **卡片正文只有两行**：第一行是有效剩余大号数字 + `受「受限窗口」限制`（多池
  时右侧同一行放池选择器），第二行只有**受限窗口那一根**进度条。窗口数、池数、
  余额、备注都不影响正文结构，所以同行卡片底边齐平；双池卡片的池选择器压在同
  一行、不给它单独一行，否则会比单池卡片高出一行。
- **悬浮详情**：鼠标悬浮或键盘聚焦正文（可聚焦，`aria-label` 为「查看 X 的额度
  详情」）弹出 `PreviewCard`，列出账号**全部**窗口（多池按池分组、标出「受限」），
  以及 `used/limit` 明细、余额与备注——这些都不进正文，避免正文高度随数据变
  化。
- **三段分组**：`credential_invalid` / `provider_error` → 顶部「需处理」横幅
  （只给「重新连接」，不展示过期快照；账号操作进横幅行的 `···` 菜单）；所有池
  都用尽 → 「已用尽」单行（按恢复时间升序，显示「X 天后恢复」，**不展示**
  balance / note —— 单行的职责只是告知何时恢复）；其余 → 可用卡片，按有效剩余
  降序。顶部计数 chip 兼做筛选。
- **配色约定**：红色只属于需处理；已用尽/接近用尽的进度条用中性色（表达
  「等重置」而不是报错），因此 `quotaUsageTone` 的 `danger` 档在进度条里映射到
  实心 `muted-foreground` 而不是 `destructive`（半透明浅灰会被读成「几乎是空
  的」）。

## OpenDesign（Design Plan，INT-117）

### 两套密钥

OpenDesign 有两套互不通用密钥，凭据页给的是后者、最容易填错：

- **控制面密钥 `vela_ctrl_…`**：控制面 `https://amr-api.open-design.ai` 认它，
  能查 workspace、Design Plan 额度与钱包余额。Fast Ship 只保存与使用这一把。
- **模型面密钥 `sk-amr-…`**：模型面 `https://amr-link.open-design.ai/v1` 用它调模型；
  控制面拿它会 401。用户从网页「API Key」页复制到的就是这把——供应商元数据的
  `Validate` 钩子在 create/update/probe 时提前拦截（40031，说明「这是调模型用的
  密钥，查不了额度」），`warning` 字段也在表单上提示。

### 取数链路

`workspaces/current` 取 `activeWorkspaceId`（缺省回退 `workspaceId`）→
`workspaces/{id}/billing/preflight` 取 `codingPlan`（5 小时/每周窗口、
`usedCredits/limitCredits`、`resetsAt`）与 `balanceUsd`（钱包余额）→
`billing/summary` **尽力而为**地补套餐名（`membershipTier` → 「Design Plan Go」；
summary 挂了回退 `codingPlan.tier`，任何失败只影响套餐名展示，不影响主数据）。
`codingPlan.eligible=false` 时不产生窗口，`note` 写「无 Design Plan 额度，按钱包
计费」；有额度窗口且有钱包余额时 `note` 写「窗口用尽后扣钱包」。接口异常
（网络/5xx/解析失败）只影响该账号卡片的状态展示，不清空上次成功快照，
也不影响其他供应商。

### 设备授权（浏览器登录）

与 OpenDesign 官方 CLI `vela login` 同款流程，三条路由均仅 JWT。协议实现收在
适配器层 `internal/pkg/quota/deviceauth_opendesign.go`（上游
`POST /api/v1/cli/device-authorizations` 建 201 授权、
`POST …/{deviceId}/token` 代查，均无认证头；`activation_url` 的 scheme 限定
http/https），service 只编排会话与建号：

1. `POST /api/quota/device-authorizations`（provider+label，可选 `account_id`）：
   服务端向上游建授权，内存里保存设备 id/设备密钥/有效期/轮询节奏，返回
   pending 视图（`user_code`、`activation_url`、`poll_interval_seconds`）。
   带上 `account_id` 表示「为已有账号换凭据」：目标账号必须存在且属于当前用户
   （否则 40430），且必须是同一供应商（否则 40001，在校验阶段直接拒绝、不触
   上游），批准后整组替换该账号凭据、不再新建。设备密钥永不出现在
   任何响应与日志里。
2. `GET /api/quota/device-authorizations/{id}`：前端按 `poll_interval_seconds`
   轮询；服务端距上次代查超过该间隔且仍 pending 时向上游代查一次。**过期判定
   顺序是先上游后本地**：到了轮询节奏先问一次上游（含已过本地有效期的情形），
   上游给出终态就按终态；上游仍 pending（或无结论）且已过有效期才判
   expired——用户在批准页待久一点再切回来，批准结果不会被作废（前端配合
   后台标签页也轮询）。代查结论由适配器统一成五态：批准（返回凭据组）、
   `expired|canceled|cancelled` → expired、`denied|rejected` → denied、
   上游 401/404 → invalid、其余 2xx → pending（正常等待）；网络错误/5xx 保持
   pending 待下次重试。批准后自动落库：
   - 会话**预分配**账号 id 并缓存首次兑换到的凭据组，建号走
     `createWithID`（只校验+加密+插入，同 id 行已存在视为成功）——建号失败
     保持 pending（Warn 只带 id），重试只重跑建号、复用缓存凭据，**不再向上游
     换取**（批准后的 token 端点大概率一次性），也不产生重复账号；
   - rebind 场景只做「整组替换凭据」的 DB 写（复用 `UpdateCredentials`，
     状态打回 pending、清退避），同步重抓在写库提交之后、会话锁之外进行；
   - 取消与落库互斥（会话级 `commitMu`）：出表统一走
     `unlinkDeviceSessionLocked`——在锁内置位取消标志再摘除 map，取消、过期
     清理与会话级定时回收三处共用；进行中的 GET 在锁内复查该标志后才写库，
     写库完成前不发起任何上游请求（同步重抓在锁外）。DELETE 返回时要么写库
     已提交（取消晚于提交，语义明确），要么其后的写库一定在锁内看到取消而
     放弃——「检查之后、写库之前」没有竞态窗口。
   token 响应里的 `runtimeKey`（模型面密钥）读到即丢，不保存、不返回、不写日志。
3. `DELETE /api/quota/device-authorizations/{id}`：取消会话（关闭对话框时调用）。

**内存会话与已知取舍**：会话只存 `QuotaService` 的内存 map（`deviceMu` 保护；
同一会话的 GET 由会话自己的 `mu` 串行，避免并发 GET 各建一个账号；「取消」
与「提交写库」由会话的 `commitMu` 串行，锁顺序固定 `deviceMu → commitMu`，
会话锁不与 `deviceMu` 嵌套）。生命周期不变量集中在两处：

- **终态即释放敏感字段引用**：expired / denied / invalid / approved 的收尾
  统一走 `session.finish`——置位状态、清空设备密钥与凭据组（释放引用，非
  物理擦除，内存随会话对象交还 GC）、记状态日志。approved 在 `commitMu` 内
  推进、晚于写库用完凭据；兑换到凭据时设备密钥即刻释放（之后不再需要）。
  建号/换凭据失败保持 pending 的路径不走收尾，凭据保留供幂等重试——因此
  「凭据已到手但建号一直失败、拖过有效期」会在本地过期判定处按 expired
  收尾并丢弃凭据，用户需重新发起授权。
- **出表即撤销提交资格**：取消、过期清理、定时回收三处统一走
  `unlinkDeviceSessionLocked`（先在 `commitMu` 内置位取消标志、再摘除
  map），出表的会话永不提交——GET 通过存活复核后才被出表的会话，其提交段
  也会在 `commitMu` 内看到取消而放弃。出表不等待会话锁；进行中的 GET 在
  提交段复查到取消时，就地释放敏感字段引用。

GET **先清理过期会话、再按 id 查找**：刚过保留期的会话直接 40431，不会被
取出后又被清理摘走、继续代查甚至建号；拿到会话锁后复核取消标志（出表必先
置位），已出表同样 40431。进程重启即丢，之后 GET 返回 40431，前端提示
重新发起——刻意不落库：设备密钥是敏感凭据，落库要引入新的一组加密与清理
语义，而授权流程生命周期只有几分钟，重启后重新点一次「使用浏览器授权」
成本更低。会话有效期取 `min(上游值, now+30min)`，避免上游给出极远时间让含
设备密钥的会话长期驻留内存。过期会话的回收**不依赖后续设备授权流量**：每个
会话建好时注册会话级定时器（`time.AfterFunc`），到 `expiresAt + 1h` 以指针
相等确认该 id 仍是自己后统一出表——被放弃的 pending 会话最长驻留
`expiresAt + 1h`；POST/GET 时的顺手清理保留（成本低、测试假时钟语义不变），
保留期内终态会话仍可被 GET 读到。日志只记
session id / provider / status / account id。

**真实上游样本**：2026-10-18 用真实控制面实测抓取并脱敏，放在
`internal/pkg/quota/testdata/opendesign_device_*.json`（起始 201、pending、
expired、401、404 五份），适配器测试直接用它们驱动状态映射。

### note 字段

`quota.Snapshot.Note` 是供应商特有的补充说明（目前只有 OpenDesign 会填），
随 `QuotaAccount.note` 与 `QuotaProbe.note` 必填字段下发（空串表示无），
前端在卡片窗口/余额下方与探测面板展示。图标不在这层：来源由前端组件内注明
（`web/src/components/quota/provider-icons.tsx` 与 `OPENDESIGN-BRAND.md`）。

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
- **Factory core 池「有数据」按键存在判定**：CodexBar 的 `hasUsageData` 用
  `usedPercent > 0`，因为它的 `usedPercent` 是非可选字段（缺省即 0）、分不清
  「没报」与「报了 0」。这里从原始 JSON 取值，能分辨两者，所以按「键存在」判定
  （与 `secondsRemaining` 一致）——只报 `usedPercent: 0` 的核心池会以 0% 窗口
  展示，而不是被当成无数据整池丢掉。
- **窗口 `pool` 字段与 Factory 双池**：`quota.Window.Pool`（JSON `pool`，可选）
  是窗口所属额度池的展示名，只有同一账号存在多个并行额度池的供应商才填，前端
  据此分组展示；单池供应商不填，omitempty 使字段在 JSON 里整个缺省。目前只有
  Factory Droid 是双池：新计费接口（`/api/billing/limits`）把 standard/core
  两个池的窗口平铺在同一 `windows` 数组里，标准池填「标准」、core 池填「Core」，
  两池窗口同名，core 的 label 不再带「Core 」前缀（池名已由 `pool` 表达）；
  旧版 usage 接口的窗口名自含池信息（标准额度/高级额度），不填该字段。
- **数值类型一律 float64**：契约里 `used_percent`/`used`/`limit`/`amount`
  在 openapi 显式标 `format: double`（生成 `float64`/`*float64`）。金额与
  百分比不降 float32 是显式决定——`10.1` 降精度会变成 `10.100000381469727`。
