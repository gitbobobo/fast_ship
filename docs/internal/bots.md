# 内置机器人（INT-101）

Fast Ship 内置「机器人（智能体）」：用户自带 OpenAI 兼容 API Key，机器人通过
白名单工具操作 Fast Ship 的项目、Issue、已连接 T3 环境与线程。本文记录架构、
边界与关键决策；实现以 `server/internal/service/bot*.go` 为准。

## 总体结构

```text
service/bot.go             管理 CRUD、密钥加密、测试连接、发消息入口
service/bot_runtime.go     唤醒、每机器人串行执行槽、run 生命周期、SSE 订阅注册表
service/bot_loop.go        核心工具循环（安全点 → 构建 → 流式 → 提交 → 执行/受理 → 收尾）
service/bot_context.go     账本 → Chat Completions 投影、前缀/工具哈希、运行时上下文
service/bot_prompt.go      固定平台规则与版本
service/bot_openai.go      官方 openai-go 适配（流式聚合、usage、连接探测）
service/bot_tools.go       固定 10 工具/29 action 注册表、schema 生成、参数校验
service/bot_tools_project.go / bot_tools_issue.go / bot_tools_thread.go   工具适配
service/bot_tool_runner.go assistant 回合提交、同步执行、后台 worker、取消与终态
service/bot_authorization.go 确认决定、结构化「始终允许」、撤销
service/bot_results.go     结果瘦身、截断、fs_result.read 续读、能力查询与帮助
service/bot_recovery.go    启动恢复
handler/bot.go             REST（全部仅 JWT）
handler/bot_stream.go      SSE 重放与订阅
repository/bot*.go         七张表的仓库；repository/bot_schema.go 建索引与守护
```

调度保持单进程：**数据库是事实来源**，内存结构只负责唤醒，丢失唤醒由轻量
扫描（5s）补回。主线每机器人一个执行槽（至多一个未结束 run 由部分唯一索引
兜底）；后台调用由 4 个 worker 并发执行。

## 数据模型

七张表（`bots`、`bot_messages`、`bot_runs`、`bot_model_requests`、
`bot_tool_calls`、`bot_authorizations`、`bot_events`），全部 UUID 主键、UTC
时间、不嵌软删除；实际外键一律 `OnDelete:CASCADE`，整机器人删除级联清理。

- `bot_messages` 是不可改写的对话账本：批准/拒绝/错误/恢复都追加新消息；
  SQLite 触发器 `trg_bot_messages_append_only` 拦截一切 UPDATE（级联 DELETE
  不受影响）。幂等与去重靠唯一索引：`(bot_id, client_message_id)`、
  `(bot_id, dedup_key)`（`receipt:<call_id>` / `terminal:<call_id>` /
  `decision:<call_id>` / `recovery:<分钟>`）。
- `bot_runs` 唯一 `(bot_id, trigger_key)` 挡重复触发；部分唯一
  `(bot_id) WHERE finished_at IS NULL` 保证每机器人至多一个主线 run。
- `bot_tool_calls` 的 ID 同时是执行幂等键；`provider_tool_call_id` 只做模型
  协议配对。状态机全部条件 UPDATE（CAS）：`pending_confirmation→queued`
  （决定）、`queued→running`（认领）、`running→终态`（完成/取消竞速只有
  一个赢家）。
- `bot_events` 是 SSE 的持久重放账本，不进模型上下文；联合主键
  `(bot_id, seq)`。

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

逻辑分段顺序固定：系统提示 → 记忆快照（首期空）→ 工具定义 → 会话摘要
（首期空）→ 最近消息 → 本轮运行时上下文。Chat Completions 的工具定义实际在
顶层 `tools`，不伪造 tools 消息；当前时间、待处理输入等易变内容只出现在末尾
运行时上下文。`prefix_hash`（去掉运行时上下文的消息数组）、`tools_hash`
（tools 数组）按实际字节计算 SHA-256，测试断言前缀逐字节只追加、不改写。

系统提示词在创建或用户明确编辑人设时重建并递增 `prompt_revision`；工具
schema 进程内生成一次（invopop/jsonschema，去掉 `$id/$schema/$defs`），不按
机器人权限删减排序。usage 启用 `stream_options.include_usage`，finish_reason
之后继续读 usage chunk；缺失与零严格区分（`usage_complete` 标记 + 可空计数），
不伪造统计。工具定义 token 占用按 chars/4 估算并记录估算方式。

SDK 重试显式关零（`option.WithMaxRetries(0)`）：连接测试与被中断的请求不产生
隐藏重复调用。base URL 允许 http/https、自托管与内网，不追加 `/v1`；key 全链
路脱敏——日志、错误、快照、响应只允许末四位。

### 工具与确认

固定 10 工具 / 29 action：`fs_project`(4)、`fs_issue`(9)、`fs_thread`(8)、
`fs_environment`(2)、`fs_schedule`/`fs_memory`（各 1，`available:false`）、
`fs_help.read`、`fs_result.read`、`fs_stop.stop`、`fs_cancel.cancel`。所有业务
写操作为 async + 确认/授权；同步读取也记调用账本。不做反射调用 service、不
提供任意 RPC。参数用专用结构体，action 列 enum，服务端校验必填/禁止/未知
字段并规范化（重序列化后哈希）。

两层 ID 严格区分：`provider_tool_call_id` 配对协议回复（每个恰好一条）；
`call_id` 是平台持久身份与执行幂等键。异步受理立即追加唯一回执
`{accepted:true, call_id, status}`，回执与 SSE 事件提交后才唤醒 worker；
worker 完成时原子落终态、结果、`background_result` 消息与事件，然后唤醒主线
（现有 run 继续，或新建 `tool_result` run）。完成事件不复用
`provider_tool_call_id` 写第二条 tool 回复。

结果先投影成工具专用精简对象再序列化；超过约 16k 字符（≈4k token，估算）时
保存完整结果、回给模型结构化预览 `{call_id, truncated, preview, total_chars,
next_offset}`；`fs_result.read` 按 Unicode 字符续读，不切断 UTF-8。

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
- **首期不做**：调度与记忆（占位工具返回 `available:false`）、环境配对/删除/
  密钥代答、摘要与自动压缩（预留 `botMemorySnapshot` /
  `botConversationSummary` 两个接缝）；不设人为 token/费用/步数上限，上游
  上下文不足时明确失败并记录错误。
- **未来扩展面**：`Wake(botID, source, triggerKey, payload)` 已以 `botWake`
  形态存在，定时触发（schedule）将来只需新增触发源并提升 toolset_version。
- 契约真相源是 `server/api/openapi.yaml`；`make api-types` 生成
  `internal/api/types.gen.go`，`make api-docs` 生成
  `skills/fast-ship/references/api.md`。
