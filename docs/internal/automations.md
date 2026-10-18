# 自动化（INT-90）

用户可配置的自动化：按多个触发器（cron / 内部 Issue 事件 / GitHub PR 事件，
INT-118 起可混搭至多 20 个）在指定 T3 环境开线程执行提示词，Agent 用一次性运行
令牌回报开始/结束，结束后可选自动 settle/archive 线程并写站内通知。

**边界**：Fast Ship 只负责触发、上下文、运行账本、状态回报与后置操作。
不做内置规则、不做规则树/表达式过滤、不绑定具体 issue、不改 INT-85 客户端语义。
GitHub PR 评审触发（`github_pr_opened` / `github_pr_synchronize`）见
[pr-review.md](pr-review.md)（INT-93）；推送/CI 等其余事件不在本期。
单进程部署假设（无分布式队列）。

界面：自动化在侧边栏一级入口 `/automations`（旧 `/settings/automations` 重定向过来）；
新建/编辑是独立页面 `/automations/new`、`/automations/:id`（INT-118，不再是弹窗），
页内分「设置」「运行记录」两个分页，触发器以卡片列表呈现并由「添加触发器」菜单追加；
站内通知在侧边栏 `/notifications`，未读数同时出现在该入口和顶栏铃铛。

## 数据模型

五张表（`server/internal/model/automation.go`，ID=UUID，时间一律 UTC）：

| 表 | 关键列 | 说明 |
| --- | --- | --- |
| `automations` | `enabled`、`prompt_template`、可空 `model_selection_json`（cron/issue 触发的单模型缺省）、`runtime_mode`、`workspace_json`、`timeout_seconds`、`post_action`、`post_on_timeout` | 自动化本体：环境、提示词与运行参数。INT-118 起触发器字段全部拆到下一张表。 |
| `automation_triggers` | `automation_id`、`trigger_type(cron\|issue_created\|issue_status_changed\|issue_label_added\|github_pr_opened\|github_pr_synchronize)`、`position`、cron 专属 `cron_expression/timezone/next_run_at`、可空 `issue_project_id`、`event_filter_json`、PR 专属 `model_selections_json/debounce_seconds`、`created_at/updated_at` | 一个触发器一行（INT-118）。一条自动化 0–20 个，可重复、可混搭；service 保证非本类型字段清空（非 cron 无 cron 字段、非 PR 无 PR 字段；cron 不带项目范围与事件过滤，后者携带即 400）。时区是独立 IANA 字段（API 未传默认 UTC），**不**写进表达式。 |
| `automation_runs` | 可空 `trigger_id`（弱引用，不建外键——触发器删除后历史保留）、冗余 `trigger_type`、`trigger_key`（UNIQUE 于 trigger）、`trigger_context_json`、`config_snapshot_json`、预分配 `environment_id/t3_project_id/thread_id/launch_command_id/initial_message_id`、`status(queued\|dispatched\|running\|completed\|failed\|timed_out\|cancelled，未确认派发时为空串)`、`deadline_at`、`post_state/pending` 系列、PR 专用列 `repo_full_name/pr_number/head_sha/round_number/installation_id/check_run_id/model_instance_id/model_name/stop_attempted_at` | 一次触发的完整账本。`dispatched_at` 仅在启动 RPC 结果确认（launch 成功或 agent 回报开始）时写入，为空 = 线程是否建成未确认。 |
| `automation_run_tokens` | `run_id` PK、`token_hash`（UNIQUE）、`expires_at`、`consumed_at` | 每次运行一个回报令牌，只存 SHA-256。 |
| `notifications` | `kind`、`dedup_key`（UNIQUE 于 user）、`state_fingerprint`、`read_at` | environment/thread/run 为弱引用（无外键），目标删除后通知保留。 |

外键 CASCADE：`automations.user_id→users / environment_id→t3_environments`；
`triggers.automation_id→automations`（删自动化带走全部触发器）；
`triggers.issue_project_id→projects`、`runs.automation_id→automations`（删自动化清账本）；
`tokens.run_id→runs`；`notifications.user_id→users`。
T3 项目/线程/命令 ID、Issue ID 与 `runs.trigger_id` 不建 FK（远端生命周期或历史
保留不由本库级联管理）。

关键索引（`cmd/server/main.go` 手工登记）：

- `automation_triggers(trigger_type, next_run_at)` —— 到期 cron 扫描；
- `automation_triggers(trigger_type, issue_project_id)` —— 事件/PR 匹配；
- `automation_triggers(automation_id, position)` —— 编辑页/响应数组排序；
- `automation_runs UNIQUE(trigger_id, trigger_key)` —— 触发幂等（INT-118 从
  automation 维度收窄到触发器维度）；
- **部分唯一** `automation_runs(trigger_id) WHERE trigger_type IN
  (cron/issue 三类) AND (finished_at IS NULL OR post_state='pending')`
  —— cron/issue 同一**触发器**至多一个活动/待后置运行，是重入约束的数据库
  兜底；同一自动化的不同触发器可并行运行。PR 触发（INT-93）不受此约束
  （一轮多模型 + 排队）。删除守卫仍按自动化（`HasActiveRunTx`）；
- `automation_runs(repo_full_name, pr_number, head_sha)` —— PR 审查按轮聚合；
- `notifications UNIQUE(user_id, dedup_key)` + 未读部分索引。

**快照纪律**：`config_snapshot_json` 存创建运行时的配置（模板原文、模型、工作区、
后置、当时的 trigger_id/trigger_type），不存渲染后提示词、令牌明文、线程内容。
Issue 正文在派发时从 Issue 库现读。更新自动化只影响后续触发，进行中的运行全部读
快照。

**升级路径（INT-118）**：`cmd/server/migrate_automation_triggers.go`。旧
automations 上的 8 个触发器列迁入 `automation_triggers`（每条自动化一行、
position=0、时间取父行），`automation_runs.trigger_id` 回填为该触发器，旧主键的
`github_pr_debounces` 整表丢弃（秒级瞬态账本），AutoMigrate 按
`(trigger_id, repo, pr)` 新主键重建。旧列清理用**整表重建**（改名旧表 → 按模型
DDL 建新表 → 拷贝共有列 → 删旧表，单事务完成，失败即启动失败）：逐列
`DROP COLUMN` 会在被 `fk_automations_issue_project` 外键引用的 `issue_project_id`
上失败，残留的 CASCADE 外键会让删项目误删整条自动化。重建必须在专用连接上以
「`foreign_keys OFF` + `legacy_alter_table ON`」进行——foreign_keys 开着时 RENAME
会把子表（triggers/runs）外键改写成旧表名、DROP 旧表时又沿级联清空触发器与
运行行，而 foreign_keys 不能在事务内切换，所以用手工 BEGIN/COMMIT，结束跑
`PRAGMA foreign_key_check` 断言无悬空引用。幂等闸门是「automations 是否残留
任一旧触发器列」（放宽到任一列：逐列 DROP 时代卡在外键上的库也要重进迁移）；
重跑按「已有触发器行即复用」跳过，不重复回填、不换新 trigger_id，旧列 SELECT
按实际残留列动态拼装。

**编辑语义**：`triggers` 是必填数组——缺失或 `null` 在 service 边界直接 400
（生成类型没有 required 校验，nil 会被当成「清空全部」），清空必须显式传
`[]`（停用时允许零触发器）。全量替换里按 id 更新会核对影响行数：命中 0 行 =
触发器已被并发删除，整事务回滚并报 400（「触发器已被并发修改，请刷新后重试」），
不会提交出「启用但零触发器」的父配置。

## 调度（`automation_scheduler.go`）

单进程一个每 10 秒的协调循环 + 4 个派发 worker，四步：

1. **到期 cron 触发器**：到期查询即过滤父 automation `enabled`（停用的触发器
   不推进 `next_run_at`，不过滤会把最老优先的批次永久占满、饿死启用的触发器）；
   短事务内乐观核对（父 automation `enabled` 兜并发停用 + 触发器旧 `next_run_at`
   未变）并写运行行（trigger_id/trigger_type 取触发器）；
   **停机错过的触发合并为一次**，`trigger_key=cron:<原计划UTC>`；随后把触发器的
   `next_run_at` 推进到 now 之后的下一次（表达式损坏时只停该触发器并置空）。
   忙碌判定按触发器：同一自动化的其它触发器可并行。
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
前缀都被显式拒绝（robfig 会无条件剥离 TZ 前缀，这里在解析前拦截）。每次保存
（含原样再存）都从当时时间重算 `next_run_at`。

## 前端可视化调度（INT-100，`web/src/lib/`、`routes/automations/cron-editor.tsx`）

编辑页（INT-118 起独立页面）内每个 cron 触发器卡片是「频率选择」（每 N 分钟/
每 N 小时/每天/每周多选/每月某日/工作日），自动生成五字段表达式；最后一个选项
「自定义」进入 cron 高级模式（实时校验、字段级错误、示例回填）。分钟步进只列
整除 60 的值（1/2/5/10/15/20/30），小时只列整除 24 的值——非整除间隔只能走
自定义，这是「可视化层能 1:1 还原」的前提。「月末」在五字段 cron 里不可表达
（L 被拒），每月只提供 1–31 日；29–31 日的描述会注明短月不运行。可视化层生成
不出表达式的状态（每周未选日、时间非法）会上报空表达式，由表单必填校验拦截保存。

- `cron-schedule.ts`：robfig `SpecSchedule.Next` 的 TypeScript 移植（`*`/`?`、
  `N/step→N-max`、dom/dow 星号决定 AND/OR、DST 校正、5 年上限），墙钟换算只
  依赖 `Intl`。**预览行为 = 服务端调度行为**，由 `cron-schedule.test.ts` 里的
  黄金向量保证（向量由 Go 侧同款 parser 离线生成，覆盖 UTC/上海/纽约/柏林/
  圣地亚哥/Apia/Lord Howe 的 DST 边界）。
  - 已知分叉：robfig 在「整日被跳过的时区」（Apia 2011-12-30 等）里 day 循环
    原地死循环；本实现检测到「时间不前进」跳到下一个真实午夜，等效该日不匹配，
    不挂起页面。
  - robfig 另一怪癖保持一致：午夜被跳过的日子里，小时循环不回看星期——
    `0 9 * * 6` 在 Santiago 2026-09-06（周日）也会触发 09:00。
  - 空字段集（`, * * * *` 这类逗号空段表达式）robfig 解析成功且永不命中；
    本实现在 Next 入口判空直接返回无运行，避免逐分钟扫 5 年拖死渲染线程。
- `cron-frequency.ts`：频率↔表达式双向转换与人话描述（结构化描述优先，
  还原不了时用 cronstrue）；界面语言跟随浏览器（zh* → 中文，其余英文）。
- 列表「触发」列展示触发器数量 + 首个摘要（如「定时 · 每天 09:00」，多个时
  加「等 N 个」）；「下次运行」取所有 cron 触发器里最早的非空 `next_run_at`。
- 服务端存储/API：`cron_expression` 五字段 + `timezone` IANA 名称挂在触发器上
  （INT-118），空时区 = UTC；新建触发器默认浏览器时区 + `0 9 * * *`（每天 09:00）。
- `Local` 时区（Go `time.LoadLocation` 合法值）：展示/描述/落库均保留原值；
  前端预览按浏览器本机时区尽力近似，与服务器机器时区可能不同。

## Issue 事件（`automation_events.go`）

`IssueService.SetIssueEventSink` 注入事件出口；`EnqueueIssueEventTx(tx, event)` 在
**调用方事务内**匹配触发器（父 automation 启用 + 触发器类型 + 项目范围 + 可选
过滤，INT-118 起匹配单位是触发器）并逐触发器预写运行行：

- 接入点：`CreateInternalIssue`（只发 issue_created）、`UpdateInternalIssue`
  （比较前后 state/标签）、`UpdateInternalMeta`（workflow_status，批量走同一路径
  不重复发）、`issueMirror.upsertGitHubIssue`（镜像同步：issue / meta / 推荐删除
  / 事件预写运行行在同一事务）。
- 状态事件区分 `state` 与 `workflow_status` 两类 field；标签只对**新增**集合发
  一次事件；无变化不触发；**事务回滚事件即消失**。
- 事件身份是 UUID（`trigger_key=event:<uuid>`），不是「旧值→新值」哈希。
- 归属 = 项目 `user_id`（镜像路径经 `ProjectRepository.FindByIDGlobal` 解析）。
- 忙碌按**触发器**判定（该触发器已有活动运行或后置 pending）**不排队**：仍记一条
  `failed/automation_busy` 终态运行，并写 `automation_busy` 站内通知
  （dedup 按自动化身份，反复忙碌不刷屏）；同一自动化的其它触发器照常触发。
- 过滤结构：状态变化 `field/from/to`、加标签 `labels`（匹配任一）；
  没有过滤 = 该类型全部真实事件。

停用自动化不中断已有运行；任一触发器有活动运行/后置 pending 时删除返回 40919
（删除守卫按自动化维度）。

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
