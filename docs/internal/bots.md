# 内置机器人（INT-101 + INT-102 + INT-103）

Fast Ship 内置「机器人（智能体）」：用户自带 OpenAI 兼容 API Key，机器人通过
白名单工具操作 Fast Ship 的项目、Issue、已连接 T3 环境与线程，并可绑定定时
任务。本文记录架构、边界与关键决策；实现以 `server/internal/service/bot*.go` 为准。

## 总体结构

```text
service/bot.go             管理 CRUD、密钥加密、测试连接、发消息入口
service/bot_runtime.go     唤醒、每机器人串行执行槽、run 生命周期、SSE 订阅注册表
service/bot_loop.go        核心工具循环（安全点 → 构建 → 流式 → 提交 → 执行/受理 → 收尾）
service/bot_context.go     账本 → Chat Completions 投影、前缀/工具哈希、运行时上下文
service/bot_prompt.go      固定平台规则与版本
service/bot_openai.go      官方 openai-go 适配（流式聚合、usage、连接探测）
service/bot_tools.go       固定工具注册表（v1：10 工具/29 action；v2：11/38；v3：11/44）、schema 生成、参数校验
service/bot_compaction.go  压缩计划、安全切点、折叠、滚动摘要、工具集激活
service/bot_memory.go      记忆路径白名单、注入截断、归档去重
service/bot_maintenance.go 维护模型请求、静默存档、夜间整理执行器
service/bot_schedule.go            定时任务：schedule 会话上下文、静默哨兵、事件、工具执行、用户侧只读服务
service/bot_schedule_scheduler.go  定时任务调度（10s ticker）、执行槽、schedule run 收尾
service/bot_tools_project.go / bot_tools_issue.go / bot_tools_thread.go   工具适配
service/bot_tool_runner.go assistant 回合提交、同步执行、后台 worker、取消与终态
service/bot_authorization.go 确认决定、结构化「始终允许」、撤销
service/bot_results.go     结果瘦身、截断、fs_result.read 续读、能力查询与帮助
service/bot_recovery.go    启动恢复
handler/bot.go             REST（全部仅 JWT）
handler/bot_stream.go      SSE 重放与订阅
repository/bot*.go         各表仓库；repository/bot_schema.go 建索引与守护
```

调度保持单进程：**数据库是事实来源**，内存结构只负责唤醒，丢失唤醒由轻量
扫描（5s）补回。主线每机器人一个执行槽（至多一个未结束主线 run 由部分唯一
索引兜底），schedule 会话按 run 各占一个槽、可与主线并行（INT-103）；后台
调用由 4 个 worker 并发执行；定时任务另有 10s 调度 ticker。

## 数据模型

十四条普通表（INT-101 的 `bots`、`bot_messages`、`bot_runs`、
`bot_model_requests`、`bot_tool_calls`、`bot_authorizations`、`bot_events`，
INT-102 新增 `bot_context_states`、`bot_compactions`、`bot_memory_files`、
`bot_memory_versions`、`bot_maintenance_runs`、`bot_maintenance_requests`，
INT-103 新增 `bot_schedule_tasks`）
加一张 FTS5 虚表 `bot_memory_fts`，全部 UUID 主键、UTC 时间、不嵌软删除；
实际外键一律 `OnDelete:CASCADE`，整机器人删除级联清理。

- `bot_messages` 是不可改写的对话账本：批准/拒绝/错误/恢复都追加新消息；
  SQLite 触发器 `trg_bot_messages_append_only` 拦截一切 UPDATE（级联 DELETE
  不受影响）。幂等与去重靠唯一索引：`(bot_id, client_message_id)`、
  `(bot_id, dedup_key)`（`receipt:<call_id>` / `terminal:<call_id>` /
  `decision:<call_id>` / `recovery:<分钟>`）。`session_kind` 列（默认 main）
  区分主线与定时任务会话，两套会话共用 seq 水位、读取面互相隔离。
- `bot_runs` 唯一 `(bot_id, trigger_key)` 挡重复触发；部分唯一
  `(bot_id) WHERE finished_at IS NULL AND session_kind='main'` 保证每机器人
  至多一个未结束主线 run（INT-103 起按会话判别收窄，schedule run 可与主线
  并行）；`schedule_task_id/scheduled_for/summary/silent` 只在 schedule 会话
  有值，`schedule_task_id` 不建外键——任务删除后运行记录保留供排查。
- `bot_tool_calls` 的 ID 同时是执行幂等键；`provider_tool_call_id` 只做模型
  协议配对。状态机全部条件 UPDATE（CAS）：`pending_confirmation→queued`
  （决定）、`queued→running`（认领）、`running→终态`（完成/取消竞速只有
  一个赢家）。
- `bot_events` 是 SSE 的持久重放账本，不进模型上下文；联合主键
  `(bot_id, seq)`。
- `bot_context_states` 是每机器人一行的持久请求投影：generation 单调递增
  （守卫触发器禁止水位回退）、`summary_through_seq` 摘要水位、摘要正文、
  标识符附录、大结果折叠映射。`bot_compactions` 追加记录每次压缩（原因、
  seq 区间、估算、终态）。
- `bot_memory_files` 存三个文件形记忆的当前态（含删除墓碑），
  `(bot_id, path)` 唯一；`bot_memory_versions` 存全部历史版本（50 版滚动
  保留），`(file_id, operation_key)` 唯一提供幂等重放；FTS5 虚表经触发器
  与当前态同步（trigram tokenizer，机器人间隔离）。
- `bot_maintenance_runs` / `bot_maintenance_requests` 独立记账压缩、归档、
  夜间整理三类维护模型调用——与主线 `bot_model_requests` 分开计数。

迁移：`cmd/server/main.go` 的 AutoMigrate 后调用 `repository.EnsureBotSchema`
（部分索引、复合索引、守护触发器集中于此），任何一条 DDL 失败即启动退出。

## 关键机制

### 主线循环与安全点

一次模型调用完整结束后提交最终 assistant 消息与 usage；每次同步工具结束、
以及准备结束 run 前，再检查新输入（`user` / `background_result` 两类消息超过
`input_through_seq` 水位）。新输入不取消进行中的模型/工具调用；若有未开始
的调用，补 `not_executed / new_input_before_execution` 回复让模型重新决定。
最后一次检查与 `completed` 落定在同一短事务，检查后恰好到达的消息不会丢唤醒。

`bots.offered_input_seq` 记录已交给某次模型请求的输入水位：run 失败后同一批
输入不自动重试。

### 前缀稳定与 usage

逻辑分段顺序固定：系统提示 → 记忆快照（无驻留文件时不注入空段）→
工具定义 → 会话摘要（首期空）→ 最近消息 → 本轮运行时上下文。Chat
Completions 的工具定义实际在顶层 `tools`，不伪造 tools 消息；当前时间、待
处理输入等易变内容只出现在末尾运行时上下文。`prefix_hash`（去掉运行时
上下文的消息数组）、`tools_hash`（tools 数组）按实际字节计算 SHA-256，
测试断言前缀逐字节只追加、不改写。记忆快照按 `context_generation` 冻结
（`memSnaps` 缓存），只有压缩边界或重启才重新注入——普通请求的记忆段
逐字节不变，写入立即可经工具读回但模型视图保持冻结。

系统提示词在创建或用户明确编辑人设时重建并递增 `prompt_revision`；工具
schema 进程内生成一次（invopop/jsonschema，去掉 `$id/$schema/$defs`），不按
机器人权限删减排序。usage 启用 `stream_options.include_usage`，finish_reason
之后继续读 usage chunk；缺失与零严格区分（`usage_complete` 标记 + 可空计数），
不伪造统计。工具定义 token 占用按 chars/4 估算并记录估算方式。

SDK 重试显式关零（`option.WithMaxRetries(0)`）：连接测试与被中断的请求不产生
隐藏重复调用。base URL 允许 http/https、自托管与内网，不追加 `/v1`；key 全链
路脱敏——日志、错误、快照、响应只允许末四位。

### 工具与确认

工具集按 `bots.toolset_version` 列冻结并版本化：v1 = INT-101 的 10 工具 /
29 action（`fs_memory`/`fs_schedule` 占位），v2 起 `fs_memory` 开放
`list/read/history/create/replace/append/delete/restore`（读类同步、写类
同步立即落库），另加 `fs_memory_search`；v3（INT-103）起 `fs_schedule`
开放定时任务管理（见下节）。三个版本的注册表都是**独立冻结的字面量**——
新版本变化不得改写旧版本的字节（存量会话的 tools_hash 失稳即前缀失稳）。
版本只在压缩边界或进程重启后首次运行由 `activateToolsetTx` 推进（重建平台
规则并递增 prompt_revision）；告知由 `maybeTellToolset` 按 `last_told_version`
单独补发 lifecycle 系统事件。普通请求期间钉死；请求行记录实际使用的版本号，
`tools_hash` 取完整 tools 数组实际 wire 字节。模型误用
旧 action 名（如 `fs_memory/search`、v3 里的 `fs_schedule/status`）会命中
改名映射表返回结构化迁移指引——不静默代执行新动作；冻结工具集里仍存在的
同名动作不受映射影响。

所有业务写操作为 async + 确认/授权；同步读取也记调用账本。不做反射调用
service、不提供任意 RPC。参数用专用结构体，action 列 enum，服务端校验
必填/禁止/未知字段并规范化（重序列化后哈希）。

两层 ID 严格区分：`provider_tool_call_id` 配对协议回复（每个恰好一条）；
`call_id` 是平台持久身份与执行幂等键。异步受理立即追加唯一回执
`{accepted:true, call_id, status}`，回执与 SSE 事件提交后才唤醒 worker；
worker 完成时原子落终态、结果、`background_result` 消息与事件，然后唤醒主线
（现有 run 继续，或新建 `tool_result` run）。完成事件不复用
`provider_tool_call_id` 写第二条 tool 回复。

结果先投影成工具专用精简对象再序列化；超过约 16k 字符（≈4k token，估算）时
保存完整结果、回给模型结构化预览 `{call_id, truncated, preview, total_chars,
next_offset}`；`fs_result.read` 按 Unicode 字符续读，不切断 UTF-8。

### 上下文压缩（INT-102）

压缩只改 `bot_context_states` 持久投影——对话账本一字节不动，前缀重写只
发生在压缩边界的一次发布（generation CAS）。

- **触发**：W=模型窗口，T=floor(W×compression_ratio)（默认 75%），
  G=floor(T×target_ratio)（默认约 0.45T，即窗口的 ~33.75%）。在实际请求
  构建之后估算：基线条件全成立且当前协议前缀是上次投影的追加时，才用上次
  prompt_tokens（含合法零）叠加新增协议消息 UTF-8 字节差/3.5；否则整包
  messages+tools 字节/3.5。估算达到 T 进入压缩周期。上游返回明确的上下文
  上限错误时强制压缩并重试**恰好一次**（按逻辑模型步骤重置额度），普通
  400/429 不重试。人设/模型/地址/密钥在当前 run 冻结，压缩或进程重启边界
  才接收允许变化的字段。
- **第一阶段·折叠**：先模拟折叠再决定是否摘要。切点内超过 ceil(W×0.01)
  token 的大 tool 结果换成指向 `fs_result.read` 的单行引用（原文留在账本
  与调用结果里）；批量净清除量不足 ceil(W×0.02) token 时不折叠。强制路径
  同样保留门槛。确认回执（pending/queued/accepted）即使调用后来成功也不折。
- **第二阶段·摘要**：对安全切点前的段做滚动摘要。摘要正文预算 =
  G − 固定前缀 − 记忆注入 − 保留尾部 − 标识符附录（下限 256 token）。按辅
  助模型窗口与**实际请求总预算**分批（发送前按实际 JSON 序列化体积核验，
  转义膨胀超出时按实测序列化成本迭代收缩本片直到可容纳——每轮切分剥离
  连续分片标记后的正文并实测复核，正文严格减少；连单字符+分片标记的
  最小分片实测仍超窗，才判定固定包装+输出预算自身超窗并干净失败——
  余片按当轮预算重切），长消息内部可分片、协议块保持安全边界，
  正文完整覆盖，全部成功才推进水位。验收用边界后完整快照（含将激活
  的工具集、最新记忆注入与人设），after 与 F 出自同一次物化，F=同一投影
  仅清空摘要正文后的体积（摘要
  正文转义开销不计入）：原目标可达（F+最小摘要预算≤G）时要求 after≤G；
  否则有效目标上调为 F+最小摘要预算，同时要求 after≤有效目标、<T 且有净
  缩减；仅当确有摘要正文（F<after）且该有效目标也 ≥T 才记录目标不可达并
  走降级或 `context_uncompressible`（fold-only/no_gain 回退快照 F≡after，
  折叠已有净收益且低于 T 时照常发布，不判不可达）；超窗/无净缩减路径同样
  先算不可达，记录的原因
  不得被 `context_uncompressible` 覆盖。无净缩减保旧摘要与水位
  （折叠仍可单独发布）；fold-only 后仍超窗口则 `context_uncompressible`；
  强制路径失败保留压缩错误码，不回落成 `model_request_failed`。空输出/
  截断不发布新水位。仅成功推进 generation 时才接收压缩边界人设；失败、
  无进展与复用旧维护记录保留原 freeze。
- **保护**：最近至少 3 个真实 user 轮，以及最近 3 个主模型 assistant 回合
  及其工具回复块（取更早起点）；另保护所有未被主模型消费的输入。切点按
  (request_id, provider_tool_call_id) 配对，不得把调用-结果块切散。
- **静默归档**：每轮压缩前把本轮要点追加进当日 daily 笔记（段落哈希 +
  ≥128 字符条目 shingle 相似度去重），失败不阻塞压缩。
- **记账**：压缩、归档、夜间整理各走 `bot_maintenance_runs` +
  `bot_maintenance_requests` 独立账本；不伪造主线 usage。

### 长期记忆（INT-102）

文件形记忆：`USER.md`（用户画像，≤1500 字符注入）、`MEMORY.md`（长期
事实，≤8000）、`daily/YYYY-MM-DD.md`（每日笔记）。路径白名单拒绝绝对
路径、点段、反斜杠与控制字符；daily 校验真实公历日期。

写操作立即落库并可经 `fs_memory` 读回：每写一版走 `(bot_id,path)` +
expected_version CAS + operation_key 幂等，历史保 50 版，`restore` 回滚；
注入正文按配置上限截断（截断带标记，全文仍在库里）。`fs_memory_search`
走 SQLite FTS5 trigram——≥3 字符查询用 MATCH（含引号短语），1–2 字符退
LIKE；搜索按 `bot_id` 隔离。

夜间整理是内置协调器（非 AutomationScheduler）：按配置时区在当地 03:00
触发，`trigger_key = nightly:<当地日期>` 唯一挡重复，用可配的更便宜模型
合并 daily 笔记到 USER.md / MEMORY.md。输出必须是完整 JSON（含正文与来源
path+version）；只有 sources 无正文、截断或 `{}` 不算成功，不把 daily 标
为已消费。超窗时按完整辅助请求预算（含 4096 输出、当前常驻正文与每文件
标题行）逐批动态分批，单文件按 UTF-8 字节分片并保持字符完整，逐批校验
本批来源/版本，且本批输入的每个来源都必须被本批声明处理（同一 daily 的
分片跨批时须逐片覆盖，全部处理完才消费该版本）。
已消费水位聚合历次成功运行的最高
path→version，无新笔记的空成功不会让次日重跑全部 daily。恢复中断的压缩
周期会补一条失败的最终压缩记录。

### 定时任务（INT-103）

机器人可绑定多个定时任务，与主对话线相互独立。任务只由机器人经 `fs_schedule`
创建/修改/删除；用户只能查看（任务、指令、最近运行）并通过 REST 开启/关闭。

- **数据模型**：`bot_schedule_tasks`（bot、cron 五字段、时区 IANA 空串=UTC、
  指令、可选模型、needs_report、enabled、next_run_at）。调度事实来源是
  `next_run_at`（UTC 存储）；启用/重新启用时重算为 now 之后的下一次，停用
  清空（不补跑过期计划）。
- **会话隔离**：schedule run 复用 `bot_runs`/`bot_messages`/`bot_events`，
  以 `session_kind='schedule'` 判别；每次运行是独立会话，只带同一份系统提示
  + 冻结记忆注入（run 开始现读一次、run 内冻结）+ 冻结工具集 + 只含本 run
  的消息 + 末尾运行时上下文（任务名/指令/cron/静默约定）。主线一切读取面
  （`FindActiveByBot`、/runs、`ListMainline`、聊天快照、SSE 重放、
  `HasModelInputSince`）按 `session_kind='main'` 收窄；`offered_input_seq`
  只由主线推进。run 的模型/地址取配置快照（任务可指定模型覆盖）。
- **不做压缩**：schedule 会话不读写 `bot_context_states`/`bot_compactions`、
  不跑压缩周期、不发工具集告知。折叠是**无状态、确定性**的：tool_reply 正文
  超过 6000 字符即替换为一行 `fs_result.read` 引用——同一 run 内任何一次投影
  结果一致（前缀稳定），原文仍在账本与调用结果里。这与主线「压缩驱动折叠」
  （大结果换引用、写入持久投影、按窗口与门槛裁剪）不同：schedule 会话生命
  只有一次运行，没有跨请求的水位需要维护，确定性规则即可保证不漂移。
- **调度**：`bot_schedule_scheduler.go` 单进程 10s ticker（与 Automation
  同纪律：事务内绝不发网络调用）。每 tick 短事务逐任务核对 `enabled` + 旧
  `next_run_at` 未变，建 run 并在同一事务内 CAS 推进 `next_run_at` 到严格晚
  于 now 的下一次（0 行 = 被并发抢走）。触发幂等靠 `UNIQUE(bot_id,
  trigger_key)`，`trigger_key = schedule:<task_id>:<计划时间 UTC RFC3339>`。
  计划时间已过 **90s 宽限**（≥tick 间隔）即视为错过：记一条 `status=skipped`
  的运行（`scheduled_for=原计划时间`，summary 写明跳过），不执行不补跑；
  重启不重复触发（trigger_key 唯一 + 不在启动时补跑），未完成的 schedule
  run 由启动恢复统一置 failed（`server_restarted`）并照常追加主线事件。
  cron 解析复用 automation 的五字段 parser 与时区校验（`parseAutomationCron`
  同一函数），解析失败暂停该任务（next_run_at 置空）。
- **执行槽**：`botRuntimeState.active` 的 key 泛化为会话槽——主线 `botID`、
  schedule `"schedule:"+runID`。schedule run 与主线可同时运行、互不让位；
  同一 schedule run 不重复执行（槽占用即放弃），run 终态后到达的后台结果
  保留在会话里不再续跑（已知限制，见下）。
- **默认放行**：schedule 会话里需确认的写调用直接排队执行，**不写**
  `bot_authorizations`、不产生 `pending_confirmation`（回执即 queued）；范围
  仍要求可解析，目标不明的调用按失败回复模型。`fs_stop` 停当前 run、
  `fs_cancel` 取消自己的后台调用，被取消的 schedule run 落 `stopped`。
- **事件进主线**：与 run 终态同一事务追加消息 kind `schedule_event`
  （`session_kind='main'`、`run_id=NULL`，正文 JSON 含 task_id/task_name/
  run_id/status/summary/error_code/error_message）。时机：**failed 必报**；
  `completed && needs_report && !silent` 才报；成功静默、成功且不需汇报、
  skipped、stopped 都不追加。`schedule_event` 是主线模型输入（计入
  `HasModelInputSince`），主线循环自动被唤醒判断是否通知用户。
- **静默约定**：运行时上下文明确要求「无话可说只回复一行 `[[SILENT]]`」。
  收尾取本 run 最后一条 assistant 正文，去尾部空白与句号后等于哨兵即
  `silent=true`，summary 记空串；否则 summary 取正文并截断到 2000 字符。
- **工具**：`fs_schedule`（v3）动作固定 7 个：`list/create/update/delete/
  runs/enable/disable`，全部同步执行、立即生效。create/update 校验 cron 五
  字段与时区，结果带 `next_run_at` 与 cron/时区原文，让模型能在回复里用中文
  说明执行时间。旧占位动作 `fs_schedule/status` 走改名映射回结构化提示
  （冻结在 v1/v2 的会话仍可用 status 查询可用性）。
- **REST（仅 JWT，接口形状已与前端冻结）**：
  `GET /api/bots/{id}/schedule-tasks`、`GET .../{taskId}`、
  `GET .../{taskId}/runs?limit=&offset=`、
  `PUT .../{taskId}/enabled`（body 只接受 `{"enabled":bool}`，其它字段 400）。
  没有任何创建/编辑/删除端点。`BotScheduleTask` 含 `last_run`；
  `BotScheduleRun` 含 `status/summary/silent/scheduled_for/duration_ms/usage`。
- **已知限制**：schedule 会话不做压缩与摘要（超长会话靠确定性折叠与上游
  上下文限制显式失败）；错过不补跑；run 终态后晚到的后台结果不再驱动续跑
  （记录保留在会话中）；无用户侧任务编辑。

### 确认与授权

无授权的写操作保持 `pending_confirmation`（不阻塞主线继续对话）。approve：
CAS 为 queued；reject：置 rejected 并落唯一终态；always_allow：同一事务建立
与本次调用相同 action/范围/字段约束的精确授权并批准当前调用（例如允许改正文
不能顺带允许关闭 Issue）。授权规则按 `(tool, action, scope, 排序字段集)`
规范化哈希，活跃授权 `(bot_id, rule_hash)` 部分唯一；因此字段子集语义是
「授权 {body} 只匹配恰好写 body 的调用」，更宽的调用需要重新授权。worker
认领前重新核对归属与授权；撤销只写 `revoked_at` 并让尚未认领的调用回到待
确认。重复相同决定幂等，冲突决定 40926。

### 停止、取消与恢复

停止主线：置 `stop_requested` + 取消执行 context，禁止再启动后续步骤，安全点
收尾为 stopped；缺协议回复的调用补「已被中断」。后台取消：待确认/queued 直接
CAS 终结；running 记录取消并取消 context，worker 落唯一终态且
`effect_may_have_occurred=true`——不声称回滚。取消与迟到完成竞速只有一个
终态（CAS 保证）。

启动恢复（worker 启动前）：未完成模型请求与活动 run 置 failed
（`server_restarted`）；running 调用置 failed，异步调用标记效果可能发生；
缺回复的补中断回复、已有回执的只补后台终态；`pending_confirmation` 保留、
`queued` 保留（认领前重查授权）；恢复通知（lifecycle，幂等）触发 recovery run
通知机器人。执行中崩溃一律失败、不自动重放副作用。

### SSE

`GET /api/bots/:id/events?after_seq=N`（仅 JWT）。五种事件固定
`delta/tool_call/tool_result/done/error`，data 形如
`{bot_id, run_id?, message_id?, call_id?, kind, phase?, payload}`；每帧
`id: <event_seq>`；心跳是 `: ping` 注释；run 失败顺序固定 error → done；done
是某个 run 完成，不是事件流关闭。事件先落库再发送；订阅先注册再补读，避免
补读与订阅之间漏事件；断开只结束订阅、不取消 run；慢客户端断开后按 seq 重放。
事件行带 `session_kind`，重放只返回 main——定时任务的流式事件不进聊天页。
正文流式片段约 100ms 一批落事件，完整 assistant 消息只在模型调用结束时追加
一次（流式片段不改消息）。

## 边界与决策

- **身份**：机器人继承归属人身份（userID 恒取机器人归属人），全部端点仅
  JWT；不降格为 API Key 身份，也不接受请求体指定 user_id。
- **绕过 HTTP 不等于绕过校验**：handler 层的分页/输入校验在工具适配层补齐
  （`botValidatePage` 等）；T3 的 scope/operate 守卫继续由既有 service 执行。
- **T3 幂等身份**：command/thread/message ID 受理时由服务端生成存
  `execution_context`，不让模型指定。`fs_thread.launch` 复用
  `CreateThreadForAutomation` 的 launchSent 语义——结果只表示启动 RPC 受理，
  远端是否完成用 get/item 查询。
- **Issue 写路径 context 化**：`CreateGitHubIssueContext` /
  `UpdateInternalIssueContext` / `CreateInternalCommentIdempotentContext` 为
  新增 Context 版本，旧方法委托，网络调用透传 worker context；短本地事务一旦
  完成仍不回滚。
- **既有自动化边界**：`AutomationService` 是调度与账本，不是机器人的执行器；
  机器人调用 Issue service 时既有 Issue 事件自动化仍可能正常触发（确认说明
  应反映这一点）。
- **仍不做**：环境配对/删除/密钥代答；不设人为 token/费用/步数上限，上游
  上下文不足时明确失败并记录错误。记忆与摘要/自动压缩已于 INT-102 落地
  （见上两节）；定时任务已于 INT-103 落地（见上节）——任务由机器人工具面
  维护，用户侧只有查看与开关，不新增任务编辑 REST。记忆仍只经模型工具面
  访问。
- **未来扩展面**：`Wake(botID, source, triggerKey, payload)` 已以 `botWake`
  形态存在；schedule 触发源已随 INT-103 落地，后续触发类型（如事件驱动）
  只需新增触发源与会话判别。
- 契约真相源是 `server/api/openapi.yaml`；`make api-types` 生成
  `internal/api/types.gen.go`，`make api-docs` 生成
  `skills/fast-ship/references/api.md`。
