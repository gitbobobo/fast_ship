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
service/bot_window.go      模型窗口能力：/models 探测、缓存、manual>detected>fallback 解析
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

十五条普通表（INT-101 的 `bots`、`bot_messages`、`bot_runs`、
`bot_model_requests`、`bot_tool_calls`、`bot_authorizations`、`bot_events`，
INT-102 新增 `bot_context_states`、`bot_compactions`、`bot_memory_files`、
`bot_memory_versions`、`bot_maintenance_runs`、`bot_maintenance_requests`，
INT-103 新增 `bot_schedule_tasks`，INT-115 新增 `bot_model_windows`）
加一张 FTS5 虚表 `bot_memory_fts`，全部 UUID 主键、UTC 时间、不嵌软删除；
实际外键一律 `OnDelete:CASCADE`，整机器人删除级联清理（`bot_model_windows`
是探测缓存、无外键，见 database.md）。

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
开放定时任务管理（见下节）；v4（INT-110）修正 action 枚举并新增线程/Issue
字段；v5（INT-120）让 `fs_thread list` 真正支持 `env_id`/`query` 过滤并
修正这两个参数的 schema 描述。已发布版本（v1–v4）的注册表都是**独立冻结的
字面量**——新版本变化不得改写旧版本的字节（存量会话的 tools_hash 失稳即
前缀失稳）。
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

- **触发**：W=inputBudgetFor（模型窗口能力解析，见下节；缺输出上限时即窗口
  原值），T=floor(W×compression_ratio)（默认 75%），
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
  ≥128 字符条目 shingle 相似度去重），失败不阻塞压缩。归档按归档模型
  （`ArchiveModel`，空则主模型）的能力预算执行：输出意向 1024 向探测上限
  封顶，输入按「窗口−包装−输出」的预算裁剪（只丢尾部，`source_seq` 白名单
  校验不受影响）；装不下时记 `over_window` 正常返回——归档尽力而为，
  不阻塞压缩主线。
- **记账**：压缩、归档、夜间整理各走 `bot_maintenance_runs` +
  `bot_maintenance_requests` 独立账本；不伪造主线 usage。

### 模型窗口能力与压缩可达性（INT-115）

线上事故（v0.1.56，DeepSeek 官方 API 实际窗口 1M，部署用默认 32K）推动了
两个改动：按「API 地址 + 模型名」自动识别模型窗口；压缩在昂贵请求之前先判
可达，失败后不自动重跑同一批输入。

- **窗口能力解析**（`bot_window.go`）：解析顺序 manual（部署
  `model_windows` 覆盖）> detected（`bot_model_windows` 缓存，来自
  `GET {base_url}/models` 探测）> fallback（默认窗口）。manual 是字段级
  覆盖：只覆盖窗口，已探测到的输出上限仍从缓存行读取（缓存无行或行内缺失
  则为 0）——否则输入预算不再预留输出空间、辅助请求失去封顶。
  `inputBudgetFor` 在窗口之上再扣输出空间（maxOutput≤0 或 ≥window 时不
  保留），只用于**主模型**的压缩触发与计划；摘要、归档、夜间整理按**完整
  窗口**定价（`botAuxBudgets`）并各自封顶输出，且 `auxModelRequest` 在发送
  边界做最终约束：输出 ≤ 探测上限、序列化输入+输出 ≤ 窗口（窗口已知时），
  违反即干净失败、不发注定非法的请求——即使调用方漏算也不出网。缺失字段
  保持 0，绝不当成 0 上限参与预算；`context_source` 如实区分
  manual/detected/fallback，展示层不得把回退值说成已检测。缓存键的地址做
  TrimSpace + 去尾部 `/` 规范化，同名模型在不同服务之间不串用。
- **探测**：独立 `http.Client`，10s 超时，Bearer 鉴权；响应宽容解析——顶层
  `{"data":[...]}`（OpenAI/DeepSeek 约定）、`{"models":[...]}` 或裸数组，
  模型名取 id/model/name，窗口字段按 context_window/context_length/
  max_context_tokens/max_input_tokens、输出按 max_output_tokens/
  max_completion_tokens/max_tokens/output_token_limit 顺序取第一个 >0 的值
  （数字或数字字符串都接受）；精确匹配优先，其次大小写不敏感。探测失败不
  阻塞每轮对话，只写日志并落一行短 TTL 失败标记（见下）。探测与持久化拆成
  `detectModelWindow` + `persistModelWindows` 两步：`TestConnection` 探测一次
  后持久化同一份结果（响应与缓存同源，不做第二次 /models 请求）；持久化按
  列表落**全部模型**——同一事务为列表内每个 `ContextWindow>0` 的条目 upsert
  一行（地址相同、模型名各自），配置成列表内其他模型的 SummaryModel/
  ArchiveModel/NightlyModel 同样能命中缓存拿到输出上限，缺失/空字段的条目
  不落 0 行。
- **失败负缓存**：探测失败（上游不提供窗口字段、网络失败、模型不在列表）也
  落一行 `ContextWindow=0` 的 `detect_failed` 标记（解析层只认 >0，天然跳
  过），TTL 10 分钟——对「本来就不返回窗口字段」的上游不再每轮白探，过期后
  允许重试。成功行 TTL 24 小时。失败标记走 `UpsertFailure` 原子条件写
  （`ON CONFLICT(base_url, model) DO UPDATE … WHERE context_window = 0`）：
  无行则插入，有行仅当当前是失败/空标记才覆盖——「失败不能覆盖成功」由
  数据库条件保证，并发下不存在先查再写的竞争窗口，有效成功行永不覆盖。
  成功行也通过 `MarkProbeFailed` 记录失败退避时间（`failed_at`，只碰失败
  时间、不动窗口/输出/`fetched_at`）：数据过期后探测失败时，10 分钟内不重复
  同步探测，过期后允许重试；成功探测写入清除该时间。
- **窗口新鲜度收敛到 `ensureModelWindow`**（run 循环里、算出输入估算之后、
  `inputBudgetFor` 之前调用）：缓存新鲜（按行类型取 TTL）→ 直接返回；缺失/
  过期且输入估算低于回退触发阈值 → 后台异步刷新、本轮不等网络；缺失/过期
  且估算已达回退触发阈值（升级后的存量机器人马上要按默认小窗口做压缩判断）
  → 5s 有界同步探测，成功落缓存、本轮即用探测值，失败继续用回退窗口、不冒
  泡为错误——网络故障不阻塞对话，也不能因探测失败把输入误消费。Create/
  Update 之后仍是后台 best-effort 刷新。
- **压缩前置可达性**：需要摘要的周期在归档/摘要之前，用「摘要正文清空」的
  同一次投影估算固定前缀 F_pre（折叠映射用本周期完整的 `plan.Folded`——切点
  之后的保留区同样可能有本周期新增折叠，用旧折叠集合会把 F_pre 算大、把可达
  的压缩误判为不可达）；F_pre≥窗口 → `context_uncompressible`、F_pre+最小
  摘要预算≥触发阈值 → `target_unreachable`、F_pre≥压缩前体积 →
  `no_reduction`。命中即快速失败——不再发出注定无效的归档/摘要请求（线上
  事故：14 批串行摘要 ≈495s 后才得到同样结论）。失败以带 `Code`/`Message`
  的错误类型随错误一路带到 run 收尾（不在返回边界降级成粗粒度哨兵，
  `errors.Is` 对既有哨兵的判定不变），并经 delta 事件
  `kind=maintenance_progress`（前端未知 kind 忽略，向后兼容）与一条去重的
  `compaction_failed` lifecycle 消息反馈到聊天页；lifecycle detail 的
  `note` 是渲染器直接显示的中文原因，`code` 保留供排查。
- **失败不重试**：压缩失败的 run 收尾（阈值触发与强制两条路径）先推进
  `offered_input_seq` 再落终态——这批输入已被「尝试处理过」，扫描器不再自动
  拉起同一输入重复一轮无效压缩（线上事故：失败 5s 后原样重跑 ≈471s，界面
  长期「正在处理中」）。用户再发新消息会推进水位并触发新一轮判断；若目标仍
  不可达，由前置检查快速给结果。阈值压缩**软失败**（未发布且估算仍低于窗口
  而放行主请求）后，同一 run 内不再重试阈值压缩——每轮工具迭代都会重新越过
  触发阈值，不设防会把确定性失败放大成每轮一次归档+摘要；上下文极限后的
  强制压缩不受此守卫影响（那是唯一合法的重试）。摘要请求的
  `max_completion_tokens` 向探测到的输出上限封顶，避免上游校验上限时的
  确定性 400。
- **运行指标**：聊天快照 `metrics` 始终产出——窗口能力 + 最近一次主线模型
  请求的单次口径（usage、输入估算、`generation_ms`）；能力解析的模型与地址
  成对取 run 的配置快照（切换服务地址后，旧请求的窗口按它发起时的地址解析，
  不拿当前地址的窗口除旧请求的估算），没有主线请求时 `request` 缺省、窗口
  能力仍然可见。`bot_model_requests.first_delta_at` 在首个非空正文流式片段
  时记录；`generation_ms` = finished − first_delta，两者都有效且为正才给出，
  否则 0（纯工具调用、失败请求视为不可用）。历史累计仍在 run 上，指标区只
  用单次口径。辅助模型（摘要/归档）的输出预算沿用「目标 − 前缀 − 附录 −
  保留尾部」与 `MaxCompletionTokens` 传递，不因省 token 硬砍到截断。

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
为已消费。超窗时按完整辅助请求预算（`botAuxBudgets`：输出意向 4096 先向
窗口余量、再向探测上限封顶，低于 JSON 契约下限 256 或包装自身超窗即按
`over_window` 干净失败；输入预算 = 窗口−包装−输出，含当前常驻正文与每文件
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
  清空（不补跑过期计划）。每机器人任务数上限 200，与列表读取上限一致——建得比
  看得见还多的话，用户唯一的任务管理面（列表 + 开关）就管不到那些任务；计数与
  插入在同一事务内（任务允许并行运行，分开读会让并发创建都通过检查）。
- **会话隔离**：schedule run 复用 `bot_runs`/`bot_messages`/`bot_events`，
  以 `session_kind='schedule'` 判别；每次运行是独立会话，只带同一份系统提示
  + 冻结记忆注入（run 开始现读一次、run 内冻结）+ 冻结工具集 + 只含本 run
  的消息 + 末尾运行时上下文（任务名/指令/cron/静默约定）。主线一切读取面
  （`FindActiveByBot`、/runs、`ListMainline`、聊天快照、SSE 重放、
  `HasModelInputSince`）按 `session_kind='main'` 收窄；`offered_input_seq`
  只由主线推进。run 的模型/地址取配置快照（任务可指定模型覆盖）；密钥密文
  （`bot_runs.api_key_encrypted_snapshot`，不进任何 API 投影）与 `base_url`
  在派发时成对冻结——轮换密钥又换地址时，不能把新密钥发往旧地址。schedule
  会话的「新输入」判定按 `run_id` 收窄，同一机器人另一个 schedule run 的
  后台结果不算自己的输入。
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
  的运行（`scheduled_for=原计划时间`，summary 写明跳过），不执行不补跑。单轮
  tick 按键集分页**排空**到期集合（上限 1000 条/tick，约 9000 条同刻到期的
  容量）——只读一批的话，超过一批的同刻到期任务会被拖过宽限、误记成错过；
  触上限会告警，不静默丢弃。
  重启不重复触发（trigger_key 唯一 + 不在启动时补跑），未完成的 schedule
  run 由启动恢复统一置 failed（`server_restarted`）并照常追加主线事件。
  同一计划时间已有运行记录时仍推进 `next_run_at`（否则任务会永远停在到期集合里
  空转）。cron 解析复用 automation 的五字段 parser 与时区校验
  （`parseAutomationCron` 同一函数），解析失败暂停该任务（`next_run_at` 置空且
  `enabled=false`，不留「开着但不再跑」）。对已启用、且这次计划还在宽限内等着
  派发的任务再点「启用」不会重算计划（不吞掉这次触发）；只有原本停用或没有下次
  计划时才重算。
- **执行槽**：`botRuntimeState.active` 的 key 泛化为会话槽——主线 `botID`、
  schedule `"schedule:"+runID`。schedule run 与主线可同时运行、互不让位；
  同一 schedule run 不重复执行（槽占用即放弃）。`scanOnce` 每 5s 按键集分页重扫
  **全部**未完成的 schedule run 补回丢失的唤醒（`wakeScheduleRun` 按槽占用与终态
  幂等）；只扫最早一页的话，最早的若干条都占着槽时较新的丢失唤醒会被饿死。否则
  一次瞬态失败会让 run 卡在 running，并挡住机器人删除。
- **结算后才收尾**：schedule run 自己的异步写全部落终态前不完成——等待在
  构建请求之前阻塞轮询（不发模型请求空转），`tryFinishScheduleRun` 在终态
  事务内再核对一次。调用终态与后台结果同一事务落库，所以计数归零即结果可见，
  重建上下文后模型据实汇报成败；等待上限是单次异步执行超时加 5 分钟，超时按
  `async_calls_unsettled` 显式失败，不谎报成功。失败终态被停止请求抢先后回落
  `stopped`（与主线 `finishFailed` 同型兜底）。
- **默认放行**：schedule 会话里需确认的写调用直接排队执行，**不写**
  `bot_authorizations`、不产生 `pending_confirmation`（回执即 queued）；范围
  仍要求可解析，目标不明的调用按失败回复模型。`fs_stop` 停当前 run、
  `fs_cancel` 取消自己的后台调用，被取消的 schedule run 落 `stopped`。
- **事件进主线**：与 run 终态同一事务追加消息 kind `schedule_event`
  （`session_kind='main'`、`run_id=NULL`，正文 JSON 含 task_id/task_name/
  run_id/status/summary/error_code/error_message）。时机：**failed 必报**；
  `completed && needs_report && !silent` 才报；成功静默、成功且不需汇报、
  stopped 不追加；`needs_report` 的任务被跳过时追加一条 `status=skipped` 的事件
  （跳过也是一次「没跑」，不能零信号；跳过摘要写明原定计划时间）。任务在运行期间
  被删除时仍按派发时冻结的
  `needs_report` 与任务名判定并追加事件（失败必报；含启动恢复路径）——读不到
  任务行时用占位名「已删除的定时任务」，否则这次运行的结果既报不出去、用户也
  无从经任务查询看到。`schedule_event` 是主线模型输入（计入
  `HasModelInputSince`），主线循环自动被唤醒判断是否通知用户。
- **静默约定**：运行时上下文明确要求「无话可说只回复一行 `[[SILENT]]`」。
  收尾取本 run 最后一条 assistant 正文，去尾部空白与句号后等于哨兵即
  `silent=true`，summary 记空串；否则 summary 取正文并截断到 2000 字符。
- **工具**：`fs_schedule`（v3）动作固定 7 个：`list/create/update/delete/
  runs/enable/disable`，全部同步执行、立即生效。create/update 校验 cron 五
  字段与时区，结果带 `next_run_at` 与 cron/时区原文，让模型能在回复里用中文
  说明执行时间。update 省略字段即保留；`model` 用指针区分「未提供」与「空串」
  ——传空串明确清除模型覆盖、回到机器人当前模型。`list` 支持 `limit`/`offset`
  分页（默认且最多 200），返回真实 `total` 与 `has_more`：把截断结果当总量会让
  旧任务不可达、也就无从管理。旧占位动作 `fs_schedule/status` 走改名映射回结构化提示
  （冻结在 v1/v2 的会话仍可用 status 查询可用性）。
- **REST（仅 JWT，接口形状已与前端冻结）**：
  `GET /api/bots/{id}/schedule-tasks`、`GET .../{taskId}`、
  `GET .../{taskId}/runs?limit=&offset=`、
  `PUT .../{taskId}/enabled`（body 只接受 `{"enabled":bool}`，其它字段 400）。
  没有任何创建/编辑/删除端点。`BotScheduleTask` 含 `last_run`；
  `BotScheduleRun` 含 `status/summary/silent/scheduled_for/duration_ms/usage`。
- **删除任务**：同一事务里给该任务未完成的运行置 `stop_requested_at` 再删任务
  ——运行记录只能按任务查询，任务一删这些 run 就既查不到也停不了，却仍会挡住
  机器人删除；已跑了很久的运行不受影响，会在下一个安全点收尾为 `stopped`。
- **已知限制**：schedule 会话不做压缩与摘要（超长会话靠确定性折叠与上游
  上下文限制显式失败）；错过不补跑；无用户侧任务编辑。**同一任务在一次运行
  还没结束时仍会按 cron 再次触发**（多个 run 并行、各自默认放行写操作）——
  这是产品决定：不限制任务运行的并发，与「运行内不限制工具调用次数/并发/费用」
  一致；运行之间靠 `session_kind` + `run_id` 隔离，互不看见对方的输入。另外：
  重启或 `async_calls_unsettled`
  失败后，该 run 已受理的排队异步写仍会执行，结果落在已终态的 schedule 会话
  里，主线不可见也不上报；`schedule_event` 唤醒主线靠 5s 扫描（非即时）；
  同一机器人的多个定时任务同刻到时会并行起多个 run，模型调用并发无上限
  （仅 4 个异步 worker 兜底）。

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
一次（流式片段不改消息）。压缩进度走 delta 事件的 `kind=maintenance_progress`
（INT-115；旧前端对未知 kind 忽略，不新增第六种事件类型）。

## 读取完整性、写入保护与停止语义（INT-110 / INT-120）

工具面按「读得完整、写得安全」收敛，与共识「优先读取不丢失、详情可读、
覆盖范围准确，再完善新输入与写入保护」一致。

- **Schema 准确**：多取值 `action` 枚举此前只会生成第一个取值（invopop
  `enum=a,b` 语法），v4 起改用 `enum=a,enum=b` 逐项声明；v1/v2/v3 的旧字节由
  `bot_args_frozen.go` 的冻结结构体逐字节保留（`botToolsetV1FrozenHash` /
  `V2FrozenHash` / `V3FrozenHash` 钉死；v3 与 v4 工具/动作/描述相同，仅参数
  结构体不同）。各动作必填字段写进 `action` 字段的 schema 描述
  （`botActionRules` 同时是服务端校验的唯一事实源）；不用 `if/then` 是因为
  多数 OpenAI 兼容后端不校验 JSON Schema，条件分支只增加每请求约 700 token
  的固定开销而无强制力。
- **列表筛选与分页**：`fs_thread list` 默认只返回未归档线程，支持
  `env_id`（只保留该环境的线程；不在已连接环境里就报错，不静默返回空）/
  `project_id` / `query`（线程 id/标题/项目名/项目 id/环境名，不区分大小写
  包含）/
  `settled` / `status` / `updated_after` 过滤与 `page` / `page_size`（默认
  50、上限 100）；结果带 `total_items` / `returned_items` / `offset`（实际
  切片起点）/ `has_more` 与各环境的读取覆盖（`environments`，未完整读取时
  给 `incomplete_environments` 与说明；`env_id` 收窄时只报该环境）。某环境
  读不到不谎报为「不存在」，也不宣称全部正常。
- **详情可读**：`fs_issue get` 返回正文、清单、标签、`updated_at` /
  `checklist_updated_at`（`labels` / `checklist` 恒在，没有时为空数组，
  让调用方能区分「没有」与「字段未实现」；列表项保持精简）；`fs_thread get`
  返回线程状态、
  最近条目（含条目 id、短摘要与 `detail_available`）以及完整历史的翻页入口
  （`item_offset` / `next_item_offset`），条目正文用 `fs_thread item` 按 id
  补读。
- **结果续读无损**：`fs_result.read` 把整个读取体控制在预算内（必要时收缩
  `content` 并迭代收敛），保证不会被通用截断再切一次——否则按 `next_offset`
  续读会跳过一段谁也没看过的内容。返回体带 `offset` / `end` / `next_offset`
  / `has_more` 与说明。截断预览只给开头约 500 字符（`botResultPreviewChars`），
  避免「看到预览」被当成「看到完整结果」。
- **新输入只暂停写操作**：安全点发现新输入时（每个调用前重查，前一个长读
  不会把新输入盖过去），尚未开始的写操作补 `not_executed`：同步写走协议回复
  （`error_code=not_executed`，不再复用参数校验的 40001），已受理但尚未被
  worker 认领的异步写走后台结果（CAS `queued→not_executed`，认领竞速失败即
  跳过）；读类调用（`botReadOnlyActions` 判定）允许执行完，已有结果不丢。
- **停止按钮 = 用户消息**：`POST /runs/:id/stop` 追加一条「停止」用户消息并
  唤醒（`client_message_id=stop:<runID>` 幂等），不再硬取消当前回复、后台
  调用或远端线程；是否结束由机器人结合上下文决定，真正要停自己时仍可用
  `fs_stop`。停止后的迟到结果不复活旧 run。
- **Issue 写入保护**：`fs_issue update` / `replace_checklist` 支持
  `expected_updated_at` / `expected_checklist_updated_at`（先 `get` 读回）。
  不符时不写入，回 `conflict=true`、原因与目标最新内容，让机器人重新读取、
  尽量保留双方修改后再提交。检查与写入之间仍有极小竞态窗口（服务端尚未做
  事务级 CAS），待后续收敛。

## 对话页呈现与工具本地化（INT-111）

- **Markdown 渲染范围**：assistant 正文经 `web/src/components/bots/bot-markdown.tsx`
  按 GFM 渲染（标题/列表/表格/行内与围栏代码/引用/链接/分隔线），复用全局
  `github-markdown-css` 的 `.markdown-body`。边界与 `ThreadMarkdown` 一致：不解析
  原始 HTML（react-markdown 默认转义，无 `rehype-raw`）、不渲染任何图片
  （`disallowedElements=["img"]`，Markdown 与 HTML 图片都不出图）、`javascript:`
  等危险链接目标被丢弃、链接一律新窗口。围栏代码块带语言标签与复制按钮，
  **不做语法高亮**（与仓库其它 Markdown 面一致，不引入高亮依赖）。
  user 消息保持纯文本不解析。
- **流式围栏降级**：`web/src/lib/bots/markdown-stream.ts` 的
  `splitIncompleteFence` 按 CommonMark 围栏规则（``` 与 ~~~、长度 ≥3、
  行首缩进 ≤3 空格、闭围栏长度 ≥ 开围栏、反引号开围栏信息串不得含反引号）
  找出最后一个未闭合围栏；流式渲染时围栏起始行之后的内容按纯文本展示
  （标记原样可见），闭合后自动升级为代码块。非流式正文不走该降级——
  CommonMark 里未闭合围栏本来就是合法的延续到文末的代码块。扫描器识别
  行首围栏与 blockquote 容器内的围栏（含嵌套）：逐行剥掉容器标记后按同一
  规则判定，开/闭围栏需容器深度相同（顶层围栏不会被内容里的 `> ``` ` 行
  误闭合），切分偏移仍取原始行起点。列表项缩进内的围栏不在扫描范围——
  其内容列取决于列表标记宽度，逐行扫描给不出完整 CommonMark 语义，
  部分实现反而会引入新的误判，保持已知边界。
- **工具调用本地化**：工具卡与确认卡的主标题、只读/写入徽标查
  `web/src/lib/bots/tool-labels.ts` 的映射表，不以 `fs_issue.create` 这类
  原始标识当标题。工具卡把原始 `tool.action` 与完整参数 JSON 折叠进
  「查看原始调用」；确认卡是刻意例外——「始终允许」按精确 action 授权，
  因此常驻原始 `tool.action` 小字、完整参数 JSON 默认展开不折叠。参数
  摘要成 chips：`*_id` 显示前 8 位短标识（完整值进 title），
  `issue_id+project_id` 链接到 `/projects/{pid}/issues/{iid}`、
  `thread_id+env_id` 链接到 `/threads/{env}/{tid}`，其余 ID 没有对应路由
  时只显示短标识不造坏链；分页与 `expected_*` 等次要参数不进 chips。
- **映射表维护约定**：`tool-labels.ts` 是前端侧副本，唯一事实源是
  `server/internal/service/bot_tools.go` 的 `botToolRegistryV4()`（工具与
  action 清单）和 `botReadOnlyActions`（只读集合）。effect 不能靠
  `requires_confirmation` 推断——`fs_schedule` 的写操作同步执行、不需要
  确认但仍是写入。后端新增/改名工具或 action 时必须同步更新此表；
  表外条目按「未知」回退：标签显示原始 `tool.action`、effect 保守按
  写入、chips 走通用字段集，不抛错（账本可能先于界面升级）。

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
