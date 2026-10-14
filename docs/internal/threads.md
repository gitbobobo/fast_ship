# T3 集成：环境连接与线程客户端（INT-82 / INT-85）

Fast Ship 以 T3 客户端身份接入 T3 Code 环境：配对一个 T3 服务端，聚合展示线程，
并支持发消息、停止、排队管理、审批/提问应答、密钥代答与图片附件。线程、队列与
请求状态由 T3 持有——Fast Ship 不落库、不建队列库、不做任务调度器、不做第二套
线程状态机；每次 API 调用实时转发上游（REST 表达用户操作，适配器转成 T3 一次性
WS RPC）。

边界补充（INT-90 之后仍成立）：本文描述的客户端语义没有队列镜像、没有第二套
线程状态机。INT-90 的「自动化」是**用户配置的触发调度与运行账本**（cron /
Issue 事件 → 开线程 → 令牌回报 → settle/archive 后置），不是线程系统的一部分：
它只在触发与收尾两个时刻调用本文的 launch/settle/archive 适配器，运行态始终以
T3 为准。详见 `docs/internal/automations.md`。

架构边界（INT-85 共识）：**复杂性留在适配器**。REST 外层 snake_case，T3 的
camelCase 留在适配器内；不暴露通用 /rpc 或任意 dispatchCommand 转发端点，
只暴露下文定案的 REST 表面。已否决的方向：Plan、流式、Issue 开线程、
Fast Ship 队列镜像、HTTP 写 RPC、浏览器直连 T3、内部 thread.stop /
secret_request.record、deliveryIntent=auto。

## 实现坐标

| 关注点 | 文件 |
| --- | --- |
| 环境模型（凭证加密落库） | `server/internal/model/t3_environment.go` |
| 环境仓库（按字段条件 UPDATE） | `server/internal/repository/t3_environment.go` |
| 配对链接解析 | `server/internal/service/t3_pairing.go` |
| T3 HTTP 客户端（换 token/探测/线程详情/字节转发） | `server/internal/service/t3_client.go` |
| T3 WS RPC 客户端（effect rpc JSON 帧） | `server/internal/service/t3_ws.go` |
| 会话能力（scopes/access/operate 守卫） | `server/internal/service/t3_access.go` |
| 环境配对/改名/删除/探测 | `server/internal/service/t3_environment_service.go` |
| 线程聚合与条目补读 | `server/internal/service/t3_threads_service.go` |
| T3 线上形状与归一化 | `server/internal/service/t3_wire.go` |
| 交互条目（审批/提问/密钥）归一化 | `server/internal/service/t3_interact.go` |
| 队列/活动派生（对齐 deriveThreadQueueWorkflowState） | `server/internal/service/t3_queue.go` |
| 写操作（launch/dispatch/stop/队列） | `server/internal/service/t3_write.go` |
| 应答与密钥安全路径 | `server/internal/service/t3_respond.go` |
| 图片附件流转发 | `server/internal/service/t3_attachments.go` |
| 路由（仅 JWT） | `server/internal/router/router.go`（`t3` 组） |

## 协议来源

以下全部核对自 T3 真实源码（本机 `/Users/godbobo/work/tools/t3code`，2026-10），
未凭猜测写契约。写入协议的真相源文件：

- `packages/contracts/src/orchestrationV2.ts`（命令联合 `OrchestrationV2Command`、
  `OrchestrationV2ThreadLaunchInput`、TurnItem/RuntimeRequest/ThreadShell/投影形状）
- `packages/contracts/src/rpc.ts`（WS 方法名与输入输出 schema）
- `packages/contracts/src/auth.ts`（scope 词表、token 兑换、scope 授予规则）
- `packages/contracts/src/chatAttachment.ts`（图片附件边界：10MiB、四种 MIME）
- `packages/contracts/src/secretRequest.ts`（`secrets.answerRequest` 输入与错误枚举）
- `apps/server/src/auth/RpcAuthorization.ts`（每个 RPC 的 scope 要求：全部写方法
  要求 `orchestration:operate`，读方法 `orchestration:read`）
- `packages/client-runtime/src/operations/commands.ts`（官方客户端怎么发这些命令：
  commandId 分配、Stop 的 run 选择、launchThread 的字段组合）
- `packages/client-runtime/src/state/threadWorkflows.ts`（`deriveThreadQueueWorkflowState`
  ——Fast Ship 详情的 queue/activity 与它同口径）
- `apps/server/src/assets/AttachmentUpload.ts` + `apps/server/src/http.ts`
  （签名上传 URL 与 `POST /api/attachments/upload/*` 路由：Content-Length 必须与
  声明一致，成功 204）
- `apps/server/src/secrets/SecretRequests.ts`（密钥代答：值只进 secret store，
  orchestration 只记录状态）

### 配对与会话（读 + 写的凭证基础）

- **OAuth token 兑换**：`POST /oauth/token`，form-urlencoded（auth.ts
  `AuthTokenExchangeRequest`）。字段：`grant_type=urn:ietf:params:oauth:grant-type:token-exchange`、
  `subject_token=<配对码>`、`subject_token_type=urn:t3:params:oauth:token-type:environment-bootstrap`、
  `requested_token_type=urn:ietf:params:oauth:token-type:access_token`、
  `client_label=Fast Ship`、`client_device_type=bot`。**不携带 `scope`**——auth.ts 中
  scope 为 optional，`EnvironmentAuth.exchangeBootstrapCredentialForAccessToken` 在
  缺省时授予整份 grant.scopes（官方配对链接缺省 = `AuthStandardClientScopes`，
  含 `orchestration:read` + `orchestration:operate`）。授予的 scope 原样落库
  （`BearerScope` 列），响应以 `scopes`/`access` 带回。`token_type` 非 `Bearer`
  （如 DPoP）拒绝。
- **会话能力判定**：`access` 唯一看精确持有 `orchestration:operate`
  （RpcAuthorization 的全部写方法都要求它；T3 的 legacy 宽 scope 集不含它，
  无需父级推导）。`read-only` 环境调用任何写入口统一 40300
  「该环境为只读会话，重新配对后可操作」。**旧环境不自动升级**：INT-82 时代
  配对的只读会话读路径照常，重新配对后 `access` 变 `operate`。
- **WS RPC 线路协议**：`GET /ws?orchestrationProtocol=2`（ws.ts）+ effect rpc
  JSON 帧（Request/Chunk/Exit/Ack/Interrupt）。每个写请求一条 WS 会话：
  探测（`GET /api/auth/session` 分类凭证）→ 拨号 → 一次或数次一次性 RPC → 断开。
  `orchestration.dispatchCommand` 的 payload **就是** OrchestrationV2Command 对象
  （无额外 command 包装）。
- **线程详情走 HTTP 而非 WS**：`GET /api/orchestration/threads/:threadId`（带
  `x-t3-orchestration-protocol: 2`）返回完整投影；WS 侧同名读取是分页受限窗口。
  投影的 `thread` 是 AppThread（无运行态字段），运行态另取 WS 快照面的可信
  ThreadShell（见下）。写操作的成员校验（requestId/itemId 属于本线程）也读这份
  HTTP 投影，提前于 dispatch 给出稳定的 Fast Ship 错误码。

### 写命令映射（REST → dispatchCommand / launchThread）

| REST | T3 |
| --- | --- |
| POST E/threads | `orchestration.launchThread`（`OrchestrationV2ThreadLaunchInput`） |
| POST T/messages | dispatchCommand `message.dispatch`，`dispatchMode={type:"queue_after_active"}`；attachments 必须发数组（无附件发 `[]`）；**禁止 `deliveryIntent`** |
| POST T/stop | dispatchCommand `run.interrupt`，`holdQueue=true`；无 run 可停时按 T3 口径无操作（`stopped=false`），不杜撰内部 thread.stop |
| POST T/queue/resume | dispatchCommand `queue.resume` |
| POST T/queue/:runId/steer | dispatchCommand `queued-message.promote-to-steer`（queuedRunId + targetRunId，服务端解析活动 run） |
| POST T/queue/:runId/cancel | dispatchCommand `queued-run.cancel` |
| PUT T/queue/:runId | dispatchCommand `queued-run.edit`；attachments 三态：缺省=保留（payload 不带字段）、`[]`=清空、非空=整组替换 |
| POST T/queue/:runId/reorder | dispatchCommand `queued-run.reorder`；`beforeRunId` 显式 null = 移到队尾 |
| POST T/requests/:requestId/respond | dispatchCommand `runtime-request.respond`；联合 `{type:approval,decision}` 或 `{type:user_input,answers,attachmentsByQuestionId?}` |
| POST T/requests/:requestId/dismiss | dispatchCommand `thread.user-input.dismiss` |
| POST T/items/:itemId/secret-answer | RPC `secrets.answerRequest`（`{threadId,turnItemId,answer:{type:save,secret}|{type:decline}}`） |
| POST E/attachments | RPC `attachments.createUploadUrl` + 同 origin 签名 POST 字节 |
| GET E/attachments/:id/content | RPC `assets.createUrl`（resource=`{_tag:"attachment"}`，只需 read scope）+ 代理字节 |
| DELETE E/attachments/:id | RPC `attachments.delete` |

commandId/messageId/threadId 由 Fast Ship 生成 UUID（T3 的 ID 是 branded
trimmed string）；threadId 稳定生成使重试幂等（launchThread 会复用同 ID 线程，
`resumed=true`）。

### launchThread 字段（对齐 OrchestrationV2ThreadLaunchInput）

`commandId`、`creationSource:"web"`、`threadId`（稳定 UUID）、`projectId`、
`title`（必填；缺省时从首条消息生成种子——正文压缩空白、空则用首个附件名、
兜底 "New thread"，与 T3 `deriveThreadTitleSeed` 同主干）、`generateTitle`
（未显式给 title 时 true）、`modelSelection {instanceId,model,options?}`（缺省回落
项目 `defaultModelSelection`）、`runtimeMode`（缺省 full-access）、
`interactionMode` 固定 default、`workspaceStrategy`、`initialMessage
{messageId,text,attachments[]}`。

workspace：`root`（Current checkout，不传 branch）/ `worktree{baseRef,branch?,
startFromOrigin?}` / `existing_worktree{worktreePath,branch?}`。`root` / `worktree`
的 cwd 从上游 shell 快照解析（`projects[].workspaceRoot`）。`existing_worktree` 的
`worktreePath` 原样转给 T3（UI 从 workspace-options 选），由 T3 校验路径；Fast Ship
不另做项目目录白名单。**新 worktree 的 base_ref 默认主检出当前分支**（共识，经
`vcs.refreshStatus` 解析；不是 T3 UI 的仓库默认分支）；`start_from_origin` 缺省取
T3 设置 `newWorktreesStartFromOrigin`。

### 队列与活动（详情的 queue/activity）

口径对齐 T3 `deriveThreadQueueWorkflowState`（threadWorkflows.ts）：

- `activeRun` = 投影 runs 中状态 ∈ {preparing,starting,running,waiting} 的最近一个
- `items` = status=="queued" 且其 userMessage 不是自动完成/通知消息
  （`delegatedCompletion`/`notification` 不在用户队列），按 `(queuePosition ??
  ordinal, ordinal)` 升序；`text` 取投影消息（缺省 "Queued message"）
- `held` = 任一排队 run `queueHeld===true`（Stop/重启恢复挂起）
- `canReorder` = 会话 `capabilities.turns.supportsQueuedMessages === true`
- `canSteer` = 活动 run 有进行中的 provider 回合，且会话声明
  `supportsActiveSteering` 或 `supportsSteeringByInterruptRestart`
- 会话解析同 `resolveThreadProviderSession`：活动 run 的 providerThread →
  线程 activeProviderThread → 最近一个未停止会话
- `activity.stopping` = 活动 run 的最近一条 `run_interrupt_request` 条目晚于同
  run 的最近一条 `run_interrupt_result`（Stop 已请求尚未落定；这两类结构性
  条目不进 visibleTurnItems，从投影 `turnItems` 全量读）
- 排队消息开始前没有 turn item——只出现在 `queue.items`
- `activity.hasPendingRequests` = shell 的 `pendingRuntimeRequest` 非空；
  `pendingBackgroundTaskCount` 按 command 不占位的口径计数

### Stop 的 run 选择（对齐官方 stopThread）

活动 run（同 activeRun 口径）优先；否则 shell 的 `latestRunId` 带着非空
`pendingBackgroundTasks`（后台工作仍会唤醒代理）→ 中断最近 run；都没有 →
`stopped=false`（T3 此场景只做 PR unwatch，Fast Ship 不维护 PR watch，
不杜撰内部命令）。

## 配对与会话生命周期

1. 用户提交 `pairing_url`（POST /api/environments，或 PUT 携带 `pairing_url` 重配）。
2. 解析出 `{http_base, ws_base, credential}`（`t3_pairing.go`）。
3. `POST {http_base}/oauth/token` 用一次性配对码换取 Bearer（不申请 scope），
   校验 `token_type=Bearer`；授予的 scope 原样落库。
4. access_token 用 AES-256-GCM 加密落库（`pkg/crypto`，密钥 `encryption.key`），
   与 AI Key / GitHub Token 同一路径；配对码一次性、不落库。
5. Bearer 到期（`expires_in`）后**无法续期**：原配对码已消费。会话失效的环境在
   探测中进入 `repair`，用户重新配对即修复（PUT 带 `pairing_url`，同时把只读
   旧会话升级为可操作）。删除环境为本地删除。

环境状态（列表逐个探测 `GET /api/auth/session`，单请求同时回答可达性与凭证有效性）：

| 状态 | 判定 |
| --- | --- |
| `ok` | 200 且 `authenticated=true` |
| `repair` | 401/403（服务端应答了但拒绝凭证）或凭证解密失败 |
| `unreachable` | 网络错误、超时、其余状态码/5xx |

## 读路径与预算

- **线程列表**（GET /api/threads）：多环境**有界并行**（并行度 4），不落库。
  每环境在单环境预算内：① 探测会话；② WS RPC `orchestration.subscribeShell`
  取首个 Chunk 的活跃 `snapshot`（projects + threads），Ack + Interrupt（连接保留）；
  ③ 同一连接一次性 RPC `orchestration.getArchivedShellSnapshot` 取归档线程；
  ④ 断开。单环境失败或超时不阻塞其它环境，状态与错误保留在 `environments[]`。
  **归档 RPC 失败是部分失败**：活跃线程照常返回，环境标 `degraded`，由 problem
  banner 提示；只有活跃面读取失败才让该环境整体不可达。列表排除
  `lineage.relationshipToParent=subagent` 的子代理线程（与 T3 自身列表一致）。
- **线程详情**（GET /api/environments/:id/threads/:threadId）：两段读取共享一个
  整体 deadline（25s < 前端 35s），**先读运行态、后读时间线**。运行态取 WS 快照
  面的可信 shell（活跃 + 归档，按线程 ID 找出，详情不过滤子代理）；时间线按
  HTTP 投影的 `visibleTurnItems` 归一化；审批/提问条目按投影 `runtimeRequests`
  补齐 `request_status`/`response_capability`/`decision`；另派生 `queue`/`activity`；
  `access` 取该环境落库的 `orchestration:operate`（与环境列表同口径，不另探测）。
  上游 404 → 40418；凭证被拒 → 50202（带「请重新配对」指引）；shell 快照无此
  线程 → 50202（明确失败，不假 idle）。**归档面不可信时不下存在性结论**（G1）。
- **条目正文**（GET .../items/:itemId）：先取可见时间线验证成员（40419），再从
  可信行派生 `getTurnItem` 参数（不接受前端指定来源或 RPC 方法）。
- **预算**（`t3RemoteBudgets`）：整轮聚合 20s、单环境 6s、线程详情 25s（写操作
  复用同一预算位）、条目补读 25s；WS 单帧读 15s、写 10s；HTTP 响应体与 WS 帧
  上限 64MB，读到上限 +1 字节即明确拒绝。

### 状态映射（列表与详情共用）

判定顺序对齐 T3 Sidebar：`archivedAt` 非空 → `archived`；其次
`pendingRuntimeRequest` 非空 → `waiting`；再看 `activityRunStatus`：
preparing/starting/running → `running`、waiting → `waiting`；然后
`pendingBackgroundTasks`（kind=command 不占完成位，其余一切 kind 含未知都占，
kind 缺省按 background_task 加载）→ 存在占位条目即 `running`；最后 `shell.status`
兜底。**详情不扫描历史 runs**，与列表共用同一映射、读同一份可信 shell 快照。

另外两个用户维度字段原样透传（列表/详情共用）：`settled`（上游
`settledOverride=="settled"`，用户在 T3 把线程收进历史区；与 status/archived
互不蕴含，筛选与呈现由前端决定）；`run_started_at`（上游
`latestRunStartedAt`，卡片「运行中 N 分钟」的计时起点）。

### Turn item 归一化映射

T3 `OrchestrationV2TurnItem` → Fast Ship 11 种 `ThreadItem`。条目 ID 由来源身份
合成：`sourceThreadId + "::" + T3 原始条目 id`。正文不额外截断；summary 是
rune 安全的单行短摘要；上游 `updatedAt` 以 `updated_at` 原样透传。

| T3 类型 | ThreadItem.type | 关键字段 |
| --- | --- | --- |
| `user_message` | `user_message` | text 全量；`attachments[]` 图片元数据 |
| `assistant_message` / `proposed_plan` | `assistant_message` | text（plan 用 markdown），summary 首行 |
| `reasoning` | `reasoning` | text 全量 |
| `command_execution` | `command_execution` | text=命令；detail_available=true（输出经补读） |
| `dynamic_tool` / `file_search` / `web_search` / `todo_list` | `dynamic_tool` | 负载 JSON；仅 dynamic_tool 可补读 |
| `file_change` | `file_change` | 真实元数据（文件名/±行数/子操作）；无 diff 读路径 |
| `subagent` | `subagent` | prompt + `child_thread_id`；结果经补读 |
| `error` | `error` | text=failure.message，summary=failure.class |
| `approval_request` | `approval_request` | `request_id`/`request_kind`/`prompt`/`app_name`/`options[]`（请求自带，缺省请求才是 Approve/Decline/Always allow this session/Cancel，服务端不自增 acceptAlways）+ enrich 的 `request_status`/`response_capability`/`decision` |
| `user_input_request` | `user_input_request` | `request_id`/`questions[]`/`response_mode` + enrich 的状态；`answers` 为已提交值 |
| `secret_request` | `secret_request` | `label`/`reason`/`placeholder`/`secret_status`；**绝不携带 secret/SecretRef** |

丢弃（结构性事件）：`notification`、`system_notice`、`run_interrupt_request/result`、
`checkpoint`、`compaction`、`handoff`、`fork`、`thread_created`。未知类型前向
兼容地跳过——与 T3 客户端自身的解码策略一致。工具/推理条目由前端默认折叠。

## 错误码

| 码 | 含义 |
| --- | --- |
| 40001 | 参数/图片/工作区组合无效 |
| 40005 | 配对链接无效或缺少配对码 |
| 40300 | 该环境为只读会话（写入口缺 `orchestration:operate`），重新配对后可操作 |
| 40401 | T3 项目不在该环境（workspace-options / launch 的项目解析失败） |
| 40417 | T3 环境不存在 |
| 40418 | T3 线程不存在（含附件不存在/已过期） |
| 40419 | 线程条目/运行时请求不是该线程的成员（含继承行的密钥请求） |
| 40912 | 环境名称在当前用户下已存在 |
| 40916 | T3 状态冲突——仅在能明确识别时返回：请求已回答/已决、排队消息已开始、命令 ID 冲突、无活动 run 可转向 |
| 50202 | T3 环境调用失败（上游/网络/凭证失效/provider 不支持排队或转向等；不支持 queued messages 时明确报告，不自动改 Steer） |

冲突识别依据是上游 Fail 错误的 `_tag` 与固定 cause 文案
（`OrchestratorCommandIdConflictError`、"is not queued"、"Runtime request …
was not found/is <status>"）——都是 Orchestrator 判定分支的稳定输出；识别不了
的一律 50202，不猜测。

## 图片附件

浏览器 → Fast Ship → T3：`POST /api/environments/:id/attachments` 收原始图片
body（Content-Type 即 MIME，`X-File-Name` 为显示名）→ WS RPC
`attachments.createUploadUrl`（type=image，边界同上游：PNG/JPEG/GIF/WebP、
单图 10MiB）→ 把字节**流式** POST 到同 origin 签名 URL（Content-Length 必须
与声明一致，T3 校验，成功 204）→ 返回 ChatImageAttachment 元数据
`{type,id,name,mime_type,size_bytes}`，可直接放进 messages/threads 请求的
`attachments` 数组。字节不落 Fast Ship 盘、不建附件表、不复用 Issue 附件；
禁 multipart 自动落临时文件、禁 base64。读取经 `assets.createUrl`
（attachment 资源，只需 read scope）后由 Fast Ship 代理字节；删除经
`attachments.delete`（pending 附件）。客户端在消息请求仍飞行时不删已写入该
请求的 pending 附件；请求失败且输入会话已结束时再补删。T3 接收时会把 pending
附件复制成线程自己的一份再引用，删除接口只认 pending id，所以请求结算后再删
待用文件不会弄坏已发出的消息。

## 密钥安全（secrets.answerRequest）

验证条目是**本线程本地** pending 的 secret_request（合成 itemId 转回真实
turnItemId；继承/fork 前缀行拒绝，40419）→ WS RPC `secrets.answerRequest`
（`{threadId, turnItemId, answer:{type:save,secret}|{type:decline}}`）→ 成功返回
空 data。密钥值只在这一次请求中经过 Fast Ship：**不落库、不写日志、不出现在
错误信息、RPC cause、请求对象的任何回显中**。失败路径不复用
`t3RPCCauseError`（它会拼接上游 message）——专用 `callInspect` 拿到原始 cause
后只解析 `SecretRequestError.reason` 枚举，映射固定文案：
already_answered/agent_stopped → 40916，not_found → 40419，其余 → 50202
（固定文案，不带任何上游详情）。

## 权限与安全

- **仅 JWT**：全部端点挂在 `middleware.RequireJWT`，API Key（`fsk_`）在解析
  body、访问环境或发上游之前一律 403（40301）——与 AI 设置同构。
- **写入口 operate 守卫**：环境解析后立即校验精确 `orchestration:operate`，
  缺失统一 40300，不发上游。
- **环境按用户隔离**：所有查询带 `user_id` 条件，跨用户访问一律 404。
- **凭证存储**：Bearer 加密落库（AES-256-GCM）；API 响应结构体不含凭证字段
  （模型层 `json:"-"`）。
- **不跟随重定向**：上游 HTTP 客户端不跟随跳转——配对地址是用户输入的远端，
  跟随可能把 Bearer 带到非预期主机。
- **超时与尺寸**：三级预算 + 单帧读写上限 + 64MB 体上限（见上）。
- **允许本机/私网地址**：T3 环境本来就常跑在本机或 Tailscale 内网；这是产品的
  明确用途，不对目标网段做限制；防护重心在凭证不外泄。
- **吊销的协议限制**：T3 的会话吊销端点要求 `access:write`，Fast Ship 没有该
  scope，协议上不存在自吊销路径；删除环境只做本地删除，需要立即吊销时在 T3
  侧的访问设置操作。这是协议边界，不是实现遗漏。

## 并发与写入约定

- **远程兑换不持锁**：配对码换 Bearer 的网络往返不持有任何数据库行状态；落库前
  检查 ctx。
- **一次条件 UPDATE 落库全部请求字段**：Update 先完成全部校验与远程兑换，再构造
  仅含请求字段的更新集合，一次 `WHERE id + user_id` 条件 UPDATE——影响 0 行视为
  已被并发删除（40417，不复活记录）；唯一索引冲突映射 40912。
- **写请求每条一条 WS 会话**：无跨请求的连接状态；成员校验（投影读）与命令
  下发之间存在固有竞窗，上游的同构校验兜底（40916/40419 映射见错误码表）。

## 已知限制

- 不做实时推送：Fast Ship 侧每次调用现拉现转，T3 端新事件要刷新才可见。
- 线程列表为全量轻量快照，筛选/分页由前端处理。
- 补读正文受上游 `getTurnItem` 的 256KB 限界，截断后缀原样显示、不掩盖。
- 文件变更永远没有 diff：上游 detail 读路径也不恢复 diff。
- 详情的 queue/activity/运行态取自读取瞬间：返回后的变化要刷新才可见。
- DPoP 会话不支持：只接受 `token_type=Bearer`。
- 写操作不做乐观并发控制：commandId 每次新生成，重试可能产生重复消息
  （T3 的 commandId 冲突检测只对同 ID 重放生效）；队列类操作靠 40916 反馈。
