# 自动化（INT-90）

用户可配置的自动化：按 cron 或内部 Issue 事件在指定 T3 环境开线程执行提示词，
Agent 用一次性运行令牌回报开始/结束，结束后可选自动 settle/archive 线程并写站内通知。

**边界**：Fast Ship 只负责触发、上下文、运行账本、状态回报与后置操作。
不做内置规则、不做规则树/表达式过滤、不绑定具体 issue、不改 INT-85 客户端语义。
GitHub PR/推送/CI 事件不在本期。单进程部署假设（无分布式队列）。

## 数据模型

四张表（`server/internal/model/automation.go`，ID=UUID，时间一律 UTC）：

| 表 | 关键列 | 说明 |
| --- | --- | --- |
| `automations` | `enabled`、`trigger_type(cron\|issue_created\|issue_status_changed\|issue_label_added)`、`cron_expression`、`timezone`、`next_run_at`、可空 `issue_project_id`、`event_filter_json`、`prompt_template`、可空 `model_selection_json`、`runtime_mode`、`workspace_json`、`timeout_seconds`、`post_action`、`post_on_timeout` | 触发配置与线程创建参数。时区是独立 IANA 字段（API 未传默认 UTC），**不**写进表达式。 |
| `automation_runs` | `trigger_key`（UNIQUE 于 automation）、`trigger_context_json`、`config_snapshot_json`、预分配 `environment_id/t3_project_id/thread_id/launch_command_id/initial_message_id`、`status(dispatched\|running\|completed\|failed\|timed_out，未确认派发时为空串)`、`deadline_at`、`post_state/pending` 系列 | 一次触发的完整账本。`dispatched_at` 仅在启动 RPC 结果确认（launch 成功或 agent 回报开始）时写入，为空 = 线程是否建成未确认。 |
| `automation_run_tokens` | `run_id` PK、`token_hash`（UNIQUE）、`expires_at`、`consumed_at` | 每次运行一个回报令牌，只存 SHA-256。 |
| `notifications` | `kind`、`dedup_key`（UNIQUE 于 user）、`state_fingerprint`、`read_at` | environment/thread/run 为弱引用（无外键），目标删除后通知保留。 |

外键 CASCADE：`automations.user_id→users / environment_id→t3_environments / issue_project_id→projects`；
`runs.automation_id→automations`（删自动化清账本）；`tokens.run_id→runs`；`notifications.user_id→users`。
T3 项目/线程/命令 ID 与 Issue ID 不建 FK（远端生命周期不由本库管理）。

关键索引（`cmd/server/main.go` 手工登记）：

- `automation_runs UNIQUE(automation_id, trigger_key)` —— 触发幂等；
- **部分唯一** `automation_runs(automation_id) WHERE finished_at IS NULL OR post_state='pending'`
  —— 同一自动化至多一个活动/待后置运行，是重入与删除约束的数据库兜底；
- `notifications UNIQUE(user_id, dedup_key)` + 未读部分索引。

**快照纪律**：`config_snapshot_json` 存创建运行时的配置（模板原文、模型、工作区、
后置），不存渲染后提示词、令牌明文、线程内容。Issue 正文在派发时从 Issue 库现读。
更新自动化只影响后续触发，进行中的运行全部读快照。

## 调度（`automation_scheduler.go`）

单进程一个每 10 秒的协调循环 + 4 个派发 worker，四步：

1. **到期 cron**：短事务内乐观核对（`enabled` + 旧 `next_run_at` 未变）并写运行行；
   **停机错过的触发合并为一次**，`trigger_key=cron:<原计划UTC>`；随后把
   `next_run_at` 推进到 now 之后的下一次。
2. **待认领运行**：全量扫描 `status=''` 的运行（含 Issue 事件在调用方事务里写的行）
   投给 worker；worker 条件 UPDATE 认领（`'' → dispatched`），天然幂等。
3. **超时**：活动运行过 `deadline_at` → `timed_out`（结束回报与超时是条件更新
   竞速，先到者定终态）；按 `post_on_timeout` 登记后置并发超时通知。
4. **待后置**：settle 前先读线程 shell 确认「无活动 run、无待输入/审批」才下发
   （等待也计入 5 分钟 post 窗口）；archive 可终止活动线程，不等待。

事务纪律：`MaxOpenConns=1`，**事务内绝不调 T3**、不复用根 DB 再开查询；所有网络
调用在事务外，DB 落库用条件 UPDATE（影响 0 行 = 状态已被并发改写）。

cron 解析用 `robfig/cron/v3` 自定义 parser（`automationCronParser`）：只接受标准
五字段（分 时 日 月 周）；秒字段、`@every` 描述符、表达式内 `TZ=`/`CRON_TZ=`
前缀都被显式拒绝（robfig 会无条件剥离 TZ 前缀，这里在解析前拦截）。

## Issue 事件（`automation_events.go`）

`IssueService.SetIssueEventSink` 注入事件出口；`EnqueueIssueEventTx(tx, event)` 在
**调用方事务内**匹配（user + trigger_type + 项目范围 + 可选过滤）并预写运行行：

- 接入点：`CreateInternalIssue`（只发 issue_created）、`UpdateInternalIssue`
  （比较前后 state/标签）、`UpdateInternalMeta`（workflow_status，批量走同一路径
  不重复发）、`issueMirror.upsertGitHubIssue`（镜像同步：issue / meta / 推荐删除
  / 事件预写运行行在同一事务）。
- 状态事件区分 `state` 与 `workflow_status` 两类 field；标签只对**新增**集合发
  一次事件；无变化不触发；**事务回滚事件即消失**。
- 事件身份是 UUID（`trigger_key=event:<uuid>`），不是「旧值→新值」哈希。
- 归属 = 项目 `user_id`（镜像路径经 `ProjectRepository.FindByIDGlobal` 解析）。
- 忙碌（已有活动运行或后置 pending）**不排队**：仍记一条
  `failed/automation_busy` 终态运行，并写 `automation_busy` 站内通知
  （dedup 按自动化身份，反复忙碌不刷屏）。
- 过滤结构：状态变化 `field/from/to`、加标签 `labels`（匹配任一）；
  没有过滤 = 该类型全部真实事件。

停用自动化不中断已有运行；有活动运行时删除返回 40919。

## 派发与运行令牌

派发（worker）：生成令牌 → 渲染提示词（用户模板占位符替换 + 服务端回报说明，
含 `public_url` 与令牌）→ `CreateThreadForAutomation` 开线程。标题固定
「自动化 · 名称」；command/thread/message ID 全部用运行行预分配值（重试幂等）。

`CreateThread` 的实现体抽成了 `createThread`，经 WS 会话的 `callTracked` 报告
**launch RPC 请求帧是否已写出**（`launchSent`）：

- 未发出（拨号/探测/校验失败）→ 运行 `failed/dispatch_failed` + 通知；
- 已发出但结果未确认 → 保留状态 `dispatched`、`dispatched_at` 留空，等 agent
  回报或 deadline，不自动重试；JWT REST 入口语义不变（INT-85）。

**运行令牌**：`fsr_` + 32 字节 base64url；哈希为完整令牌的 SHA-256（与 API Key
同口径）。`expires_at = deadline_at + 5min`（只给回报重放用）。

`POST /api/automation-agent/report`（`automation_agent.go`）：

- `started`：幂等；`dispatched/'' → running`，顺带确认派发（回报先到说明线程
  确实起来了）；结束后再 started → 40917。
- `finished`：同一事务内 **条件更新终态 + 消费令牌 + 登记后置**。允许结束先于
  开始；已超时的运行保持 `timed_out`（迟到结束不能改成 completed），只登记
  summary；携带 `error_code` 记 failed。之后仅**完全相同**的三元组
  （summary/error_code/error_message）可重放，其它 40917。
- 令牌消费后读线程/发通知立即 40106；Agent 复用 `T3ThreadsService` 的
  List/Detail/ItemDetail（不走 JWT handler），列表按 environment/project/status/
  settled 过滤，详情 items 分页（默认最后 50、上限 100、页内正序、
  `before_item_id` 上一页、边界消失 40918）。

Agent 通知 kind 固定 `agent`，不能冒充系统 kind（`automation_dispatch_failed` /
`automation_run_timeout` / `automation_post_failed` / `automation_busy`）。
请求必须带 `problem_key` / `state_key`（去重身份与状态指纹）。dedup 语义见
`NotificationSink`（同键同指纹只推 `last_seen_at`，状态变化更新并清已读）。

## 后置操作（`t3_automation.go`）

`thread.settle` / `thread.archive` 经 `orchestration.dispatchCommand` 下发
（协议形状核对自 T3 `orchestrationV2.ts`：settle 的 `settledAt` 不传）。
不公开 REST。`commandId` 按 `post_attempt` 派生：**网络失败重放同 commandId**
（commandId 冲突 = 此前已受理，按成功处理）；**上游明确拒绝才 attempt+1 换新 ID**。
后置失败不改 agent 回报的结果，只发通知。未确认建线程的运行不执行后置。

## 配置与安全

- `server.public_url`（env `FAST_SHIP_SERVER_PUBLIC_URL`）：提示词里回报端点的
  基址，**不从 Host 推断**；启用自动化前必须配置（41202）。
- 超时缺省 1h，可配 1min–24h，从 `triggered_at` 计；无 heartbeat。
- Agent 路由仅接受运行令牌（`middleware.RequireRunToken`，仅 Bearer 不支持
  query）；`fsk_` API Key 一律 40304，不因持有 API Key 获得任何 Agent 能力。
- 日志不回显令牌与渲染后提示词。

## 已知限制

- 单进程：协调循环与队列在同一进程内；多副本部署会重复派发（唯一索引只保证
  运行行唯一，认领是单 DB 条件更新，跨副本由同一 DB 收敛——但 worker 会重复
  消费队列消息，靠条件认领兜底）。
- 删除环境 CASCADE 删自动化但不终止远端线程。
- 未确认建线程的运行在超时后无法区分「线程其实起来了但没回报」——账本如实记
  timed_out，不回捞。
