# T3 集成：环境连接与线程客户端（INT-82 / INT-85 / INT-116）

Fast Ship 以 T3 客户端身份接入 T3 Code 环境：配对一个 T3 服务端，聚合展示线程，
并支持发消息、停止、排队管理、审批/提问应答、密钥代答与图片附件。线程、队列与
请求状态由 T3 持有——Fast Ship 不落库、不建队列库、不做任务调度器、不做第二套
线程状态机。读取走按环境常驻的 shell 订阅（INT-116：活跃/归档各一条长驻 WS +
增量推送，内存组装回答；INT-132 起订阅跟环境走、不跟读请求走，变更经 SSE 推给
浏览器，前端不再定时轮询）；写操作仍是 REST 表达用户操作、适配器转成 T3 一次性
WS RPC。

边界补充：本文描述的客户端语义没有队列镜像、没有第二套线程状态机。INT-135
的「PR 审查」是**项目级触发的运行账本**（webhook → 防抖/排队 → 开线程 →
运行令牌回报 → 工作区回收），不是线程系统的一部分：它只在派发与回收两个
时刻调用本文的 launch/archive 适配器，运行态始终以 T3 为准。详见
`docs/internal/reviews.md`。

架构边界（INT-85 共识）：**复杂性留在适配器**。REST 外层 snake_case，T3 的
camelCase 留在适配器内；不暴露通用 /rpc 或任意 dispatchCommand 转发端点，
只暴露下文定案的 REST 表面。已否决的方向：Plan、流式、
Fast Ship 队列镜像、HTTP 写 RPC、浏览器直连 T3、内部 thread.stop /
secret_request.record、deliveryIntent=auto。

「Issue 开线程」原在否决名单上（INT-85），INT-89 推翻了它，但范围严格限定：
**前端复用现有新建线程接口发起线程，不保存 Issue ↔ 线程关联**——Fast Ship 侧
不存在「该 Issue 由哪些线程实现」的记录，启动后的线程就是普通线程，状态推进
由 Agent 通过 fast-ship 技能回写 workflow_status，线程创建动作本身不改 Issue。
另一条配套例外：绑定与 T3 项目发现端点（见 `t3-bindings.md`）是仅有的对
API Key 开放的 T3 只读面（不含凭证、不含线程内容）；线程读写、新建线程、
附件、配对仍仅 JWT。

## 实现坐标

| 关注点 | 文件 |
| --- | --- |
| 环境模型（凭证加密落库） | `server/internal/model/t3_environment.go` |
| 环境仓库（按字段条件 UPDATE） | `server/internal/repository/t3_environment.go` |
| 配对链接解析 | `server/internal/service/t3_pairing.go` |
| T3 HTTP 客户端（换 token/探测/线程详情/项目表/字节转发） | `server/internal/service/t3_client.go` |
| T3 WS RPC 客户端（effect rpc JSON 帧） | `server/internal/service/t3_ws.go` |
| shell 订阅纯状态（活跃/归档两面 reducer） | `server/internal/service/t3_shell_state.go` |
| shell 订阅 hub（每环境两条常驻 WS + 增量 + 变更通知） | `server/internal/service/t3_shell_hub.go` |
| shell 变更事件与按用户订阅 | `server/internal/service/t3_shell_events.go` |
| 浏览器 SSE 订阅与查询失效 | `server/internal/handler/t3_environment.go`（`/api/threads/stream`）、`web/src/lib/thread-stream.ts`、`web/src/lib/hooks/use-threads.ts` |
| 会话能力（scopes/access/operate 守卫） | `server/internal/service/t3_access.go` |
| 环境配对/改名/删除/探测（变更清 hub entry） | `server/internal/service/t3_environment_service.go` |
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
  `OrchestrationV2ThreadLaunchInput`、TurnItem/RuntimeRequest/ThreadShell/投影形状、
  shell 订阅流项 `OrchestrationV2ShellStreamItem`）
- `packages/contracts/src/rpc.ts`（WS 方法名与输入输出 schema）
- `apps/server/src/ws.ts` + `apps/server/src/orchestration-v2/ShellStream.ts`
  （订阅流语义：权威快照 vs enrichment 帧、`synchronized` 标记位置、活跃流只产生
  `location:"active"` 增量、归档订阅无 afterSequence、afterSequence 失效改发全量）
- `packages/contracts/src/environmentHttp.ts` + `apps/server/src/project/http.ts`
  （`GET /api/projects` 项目表：无需 orchestration protocol 头、
  要求 `orchestration:read`、返回 `{projects, updatedAt}`）
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
的 cwd 从上游项目表解析（`GET /api/projects` 的 `projects[].workspaceRoot`，零
shell 订阅）。`existing_worktree` 的 `worktreePath` 原样转给 T3（UI 从
workspace-options 选），由 T3 校验路径；Fast Ship 不另做项目目录白名单。
**新 worktree 的 base_ref 默认主检出当前分支**（共识，经 `vcs.refreshStatus`
解析；不是 T3 UI 的仓库默认分支）；`start_from_origin` 缺省取 T3 设置
`newWorktreesStartFromOrigin`。

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

读取顺序恢复基线（F6）：**先读 HTTP 投影**——线程真的不存在回到 40418
（投影 404 → `errT3ThreadNotFound`；凭证被拒/不可达按既有分类），再取 hub
**可信活跃面**（`Ready`；未就绪/陈旧 50202，不基于陈旧快照下「无事可做」的
结论；Ready 但 shell 无此线程也是 50202「上游快照面不一致」）。run 选择以
**新鲜 shell** 为主：`shell.activeRunId`（上游按可中断活动 run 派生）优先——
投影是等待之前读的，冷启动等首次同步期间新起的 run 不在里面；
`shell.activeRunId` 为空时（典型是 waiting）才回读一次投影，保留
「waiting/后台工作也算可停」的既有口径；再否则 shell 的 `latestRunId` 带着非空
`pendingBackgroundTasks`（后台工作仍会唤醒代理）→ 中断最近 run；都没有 →
`stopped=false`（T3 此场景只做 PR unwatch，Fast Ship 不维护 PR watch，不杜撰
内部命令）。

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

### shell 订阅 hub（INT-116，列表/详情/Stop 的运行态来源）

每个「用户 × 环境」在 hub（`t3_shell_hub.go`）里持**两条长驻 WS 连接**：活跃面
订阅 `orchestration.subscribeShell`（恢复时带 `afterSequence` 游标 +
`requestCompletionMarker`，能力由 `server.getConfig.shellResumeCompletionMarker`
决定），归档面订阅 `orchestration.subscribeArchivedShell`（无 afterSequence，重连
即重拉全量）。两条而非一条多路复用：会话客户端是串行的，复用需自建 requestId
派发；两条单流连接读循环无需派发，取消/重订阅互不影响。数据进纯内存 reducer
（`t3_shell_state.go`）：活跃/归档两面独立维护，权威快照整体替换并把游标重置为
`snapshotSequence`，增量（thread/project updated/removed）按 `sequence` 推进游标；
带 `resolvedRepositoryIdentityRoots` 字段的帧是 enrichment/元数据帧，整体忽略。

- **可信度分别标注**：活跃面（`ActiveReady`/`ActiveSynced`）与归档面
  （`ArchiveReady`）独立判定。归档未同步完成与归档读取失败同等对待——
  `ArchiveErr != nil`（含 `errT3ShellArchiveNotReady`），**不得据此断言线程
  不存在**（G1），也不得声称列表完整。
- **归档可信度随连接生灭**：归档面只维护线程（项目表见下），且归档连接的
  **任何终止**——读错误、Ack/Ping 写失败、正常 Exit、取消，乃至探测/拨号
  失败——都统一撤销归档可信度（清空归档线程表 + `ArchiveErr`），直到下一次
  归档权威快照到达才恢复；凭健康连接之外的缓存宣称归档命中可信会违反 G1。
  活跃面独立，不受归档终止影响。
- **项目表只有活跃面一份权威来源**：归档订阅只在初始快照时携带项目，常驻
  订阅后旧名字会一直覆盖活跃流的项目更新/删除且永不自愈，因此归档快照的
  `Projects` 一律忽略；归档线程的项目名从活跃项目表取，项目已删除时显示
  无名（与「项目已删除」一致）。
- **新鲜度**：`Ready`=活跃面权威且已追平；`Syncing`=还没有权威快照；
  `Stale`=曾有数据但连接断开/重连中/追平中（`SyncedAgo` 标注距最近一次确认
  可信的时间）；`Failed`=从未拿到数据且首连失败（错误分类含凭证被拒）。
  **陈旧命中不当可信运行态**：详情/Stop/收尾判定只接受 `Ready`，否则 50202——
  陈旧快照里的 idle 会把旧数据当成可信运行态、让 Stop 无事可做。
- **保活与恢复**：距最近完整帧超 25s 发应用层 Ping（Effect RPC `{"_tag":"Ping"}`
  → `{"_tag":"Pong"}`，非 WS 控制帧）；单帧读 75s 到点即判死重连（x/net 超时后
  不可续读）。重连退避 1s→2s→…→30s ±25% jitter，稳定 60s 后重置；凭证被拒走
  5min 长退避。恢复订阅带 `afterSequence` 续传（序号失效/缺口过大时上游直接改发
  权威快照，客户端不要求序号连续）。
- **完成标记兼容**：只有**带游标的 resume** 才等 `synchronized` 并受 10s 预算
  约束；预算内没等到就**不把未追平的缓存提升为 Ready**，而是置强制全量重同步
  并结束本连接，下次重连不带 `afterSequence`，由上游权威全量快照重新建立追平
  （期间保持 Stale/Syncing）。冷启动与强制全量重同步**不计时**，完成标志就是
  权威全量快照本身——否则首帧比预算慢的慢环境会被反复截断、永远 Ready 不了。
  **上游不支持 marker 时不带游标恢复**（退回权威全量）：没有完成证明就不能
  宣称追平，否则「断线前 idle、断线期间线程已开始运行」时旧缓存会被当成可信
  运行态；官方客户端的新 session 也是先加载权威快照、失败才退回全量，而不是
  沿用上一会话的缓存。权威帧到达前视图保持 Stale/Syncing。
- **生命周期跟环境走（INT-132）**：服务启动预热时为每个已配对环境建立 entry
  （`PrewarmShellSubscriptions` → `hub.Ensure`），环境创建/重新配对后重建，
  删除即关闭（`Drop` 传 `math.MaxInt64` tombstone：淘汰恒成立、迟到请求恒被拒）。
  不再有基于 View 的空闲断开或空闲回收；`View` 只取用已有 entry 并等待首次同步，
  读请求不决定连接生灭。连接结束时会汇合本次连接的取消监听与 reader；
  `Close()` 返回后本 hub 启动的 goroutine 均已退出。会话层 `context.AfterFunc`
  的取消回调（`t3_ws.go` 的 `closeOnCancel`，其 stop 按 Go 语义不等待已启动的
  回调）不在等待范围内：它只做一次 `conn.Close()`，可能在 `Close()` 返回后的
  极短时间内完成，不构成泄漏。
- **版本守卫**：配置世代是 `t3_environments.config_revision`——只在地址/凭证
  变更时由仓库侧 SQL 自增（`config_revision + 1`），改名不算；`updated_at` 是
  UPDATE 前生成的墙钟时间、没有提交顺序保证（并发改名会让版本号回退），只做
  展示，不能当世代用。entry 记建立时的世代与凭证指纹（规范化 address + token）。
  凭证/地址变更且世代更新 → 停旧建新；否则拒绝（`errT3ShellConfigStale`），
  调用方按可重试错误处理（「环境配置已更新，请重试」——不做内部重读重试，
  避免同一操作前半程旧配置、后半程新配置的拼接；用户/前端重试会重新加载
  配置，整个操作用同一份配置完成）。环境 Update（重配）落库成功后重读该行拿
  新世代（自增发生在 SQL 里，Go 侧算不出并发安全的新值；重读失败退回
  `旧世代+1` 这个最小可能值）调 `shellHub.Drop`；Delete 的 cutoff 用
  `math.MaxInt64` 哨兵：淘汰条件 `entry.revision < cutoff` 恒真，entry 无条件
  关闭；floor 同为哨兵，任何迟到请求（世代必然有限）都被拒。不用「删除前
  revision + 1」——并发重配先把世代升到 r+1 再被删除时，旧 cutoff=r+1 会保留
  同代 entry 并放行迟到的 Ensure。环境 ID 不可复用，永久拒绝正是该语义；floor
  按记录时刻 1 小时后清理也无碍（环境行已删，预热与读写路径都不会再为它建
  entry，能撞上 floor 的只剩分钟级在途请求）。准入语义是「**请求 revision <
  cutoff 即拒**」：
  携带新世代的请求（revision == cutoff）可以重建，变更前的请求一律被拒——
  包括并发改名后仍带着旧凭证的行（改名不递增 revision，世代与变更前相同），
  以及「迟到请求复活已删环境」这条路径。Drop 只淘汰旧代 entry（revision 小于
  cutoff）：若新请求已用新配置重建 entry，迟到的或幂等的重复 Drop 不会把它
  关掉。floors 表按**记录时刻**保留 1 小时后清理（在途请求只有几十秒量级；
  revision 值本身没有时间含义，清理只能以记录时刻为准）。
- **生命周期**：`Close()` 等 hub 级生命周期 WaitGroup 排空——覆盖已被
  Drop/替换/淘汰摘除的 entry 与各订阅 reader，返回后没有本 hub 启动的
  goroutine 仍在运行。

### 变更推送到浏览器（SSE，INT-132）

hub 在数据路径上发布轻量事件（`t3_shell_events.go`），消费方按用户订阅：

- 事件形状 `{environment_id, thread_ids[], full, state}`：`thread.updated/removed`
  带线程 ID；权威快照、项目增删、归档面失效与合并超限（>256 个线程 ID）置
  `full`（整环境重拉）；`state`（syncing/ready/stale/failed）表示连接状态翻转，
  stale/failed 下失败原因文案变化同样发布。环境创建/改名/重配/删除成功后再发
  一条**无 environment_id 的 `full`**（用户级全局刷新）：这些操作不产生线程
  增量，删除时 entry 若正在重连退避更不会发出任何连接事件，其它标签页否则会
  一直显示已删除的环境。
- 发布只做内存合并（同环境：`full` 逻辑或、线程 ID 并集、state 取最新）与非阻塞
  唤醒，绝不写网络、不持 entry/hub 锁；订阅注册表用独立的 `subsMu`，锁序
  `subsMu → 订阅锁`，不与 `hub.mu → entry.mu` 交叉。慢客户端不会反压订阅流：
  信号 channel 容量 1，合并缓冲由消费方一次 `Drain` 取空。
- 端点 `GET /api/threads/stream`（仅 JWT）：SSE，帧名 `shell`，连接建立先发一帧
  全局 `{"full":true}`（无 environment_id）表示「全量刷新一次」；15s 心跳注释
  `: ping`。handler 单 goroutine 写全部帧并检查写错误，断开即退订。
- 前端 `web/src/lib/thread-stream.ts` 用 fetch + ReadableStream 订阅（EventSource
  不能带 Bearer 头），指数退避无限重连；`applyThreadStreamEvent` 把事件映射成
  TanStack Query 失效：无 environment_id 失效整个线程子树；有环境时列表必失效，
  `full` 失效该环境全部详情，否则只失效命中的线程详情。`useThreads` /
  `useThreadDetail` 因此删除固定间隔轮询（保留挂载/聚焦/写操作后的重读）。
- 浏览器 SSE 通道是 `hub.Subscribe(userID)` 的唯一消费者：同一个 hub 事件流
  按用户订阅，不需要各自去问 T3；跨用户隔离由按用户分组的注册表保证。

### 各读取路径

- **线程列表**（GET /api/threads）：多环境**有界并行**（并行度 4），不落库。
  每环境 `shellHub.View`（等待预算 `ShellFirstSync` 15s），从内存组装回答：
  活跃+归档合并（活跃优先），排除 `lineage.relationshipToParent=subagent`。
  状态映射：`Ready` → `ok`；`Syncing` → `degraded`「正在同步 T3 线程快照，请稍后
  刷新」；`Stale` → `degraded`「与 T3 的连接已断开（最近确认于 X 前），正在重连」
  （线程表仍如实展示）；`Failed` → 凭证被拒 `repair`、其余 `unreachable`。
  归档不可信是部分失败（G1）：活跃线程照常返回，环境标 `degraded`「归档线程
  暂不可读」；其它状态优先自己的文案。列表请求不做每请求会话探测——凭证失效
  由 hub 的重连探测异步发现。前端列表由 SSE 事件驱动失效（见上），不再按固定
  间隔轮询 degraded 环境。
- **线程详情**（GET /api/environments/:id/threads/:threadId）：两段读取共享一个
  整体 deadline（25s < 前端 35s），**先读运行态、后读时间线**。运行态只取 hub
  **可信活跃面**（`Ready`；未就绪/陈旧一律 50202「线程运行状态暂时不可读
  （正在同步），请重试」，凭证被拒仍给「请重新配对」）；时间线按 HTTP 投影的
  `visibleTurnItems` 归一化；审批/提问条目按投影 `runtimeRequests` 补齐；
  另派生 `queue`/`activity`；`access` 取该环境落库的 `orchestration:operate`。
  shell 无此线程时以投影归类：投影 404 → 40418，投影在、shell 无 → 50202；
  归档不可信时不下存在性结论（G1）。前端详情只在收到本线程（或本环境 full）
  的 SSE 事件后重读，不再按运行态每 3s 轮询。
- **条目正文**（GET .../items/:itemId）：不读 shell。先取可见时间线验证成员
  （40419），再从可信行派生 `getTurnItem` 参数（不接受前端指定来源或 RPC 方法）。
- **启动器/工作区选项/新建线程/项目发现/绑定校验**：项目表走 HTTP
  `GET /api/projects`（`fetchProjectSnapshot`，过滤软删项），**零 shell 订阅**；
  401/403 分类为凭证被拒（50202「请重新配对」），404 是端点缺失/版本不兼容，
  按上游错误处理、绝不映射成 40418。项目不在该环境/缺 workspaceRoot 仍 40401。
  不做项目表缓存——绑定校验必须实时。
- **新建线程**：`launchThread` 受理即返回；线程经订阅流进入内存后，下一次列表
  刷新即可见（后台同步，无需额外拉取）。
- **预算**（`t3RemoteBudgets`）：整轮聚合 20s、单环境 HTTP（探测/项目表）6s、
  线程详情 25s（写操作复用同一预算位）、条目补读 25s、**等 hub 首次同步 15s**
  （`ShellFirstSync`，只用于等待首次同步，不是每请求预算的放大——entry 建立后
  列表从内存回答）；WS 单帧读 15s（一次性会话）/75s（订阅 reader）、写 10s；
  HTTP 响应体与 WS 帧上限 64MB，读到上限 +1 字节即明确拒绝。

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

## 线程列表页（前端）

- `/threads` 是主从两栏（`routes/threads/layout.tsx`）：左栏 320px
  （`lg:w-80`）= 顶栏（移动端导航/刷新/新建）+ 环境单选列表 + 搜索行
  （搜索框 + 「筛选线程」按钮，项目/状态/完结三个下拉收在按钮后的面板里，
  有筛选时按钮上留一个点）+ 可独立滚动的线程列表（`components/threads/
  thread-list.tsx`，用 `usePersistedScroll` 记住滚动位置）；右栏经 `<Outlet/>` 渲染子路由——`/threads` 是「从
  左侧选择一个线程查看详情」空态（`routes/threads/index.tsx`），
  `/threads/:environmentId/:threadId` 是详情页。点左栏条目只换右栏内容，
  列表不重挂载、滚动不丢。`/threads/new` 是独立平级路由，不套两栏。
- 窄于 `lg` 时只显示一栏：由子路由是否有 `threadId` 决定（`useParams`）——
  开着线程就只显详情，否则只显左栏；不引入 JS 断点 hook。
- 环境筛选为**单选**：`ThreadFilters.environmentId`（空串 = 全部环境），替代早期
  的多选数组。侧栏环境是两列卡片（「全部环境」+ 每个环境一格，窄栏里
  一行两张）：卡片第一行健康点 + 名称，第二行一排「彩点 + 数量」pill，
  只显示计数非 0 的状态（固定 running/waiting/idle/archived 顺序）。计数来自分面计数 `countThreadsByEnvironment`：忽略
  environmentId 这一项，搜索/项目/状态/完结照常叠加，每个环境（与「全部
  环境」汇总行）返回按状态分桶的 `ThreadStatusCounts`（含 total）；
  status 为空时归档不计入，数字与命中条数（含收进折叠区的已完结线程）一致。
- 已完结折叠区：仅当 `filters.settled === ""` 且 `filters.status === ""`
  （默认视图，搜索/环境/项目筛选不影响分组）时，列表底部渲染
  「已完结 · N」折叠条，默认收起（`thread-list-store` 的 `settledOpen`，
  离开页面保持、刷新复位）；展开后已完结行接在未完结行之后，同一个
  `thread-grid` 内。选了完结或状态筛选时是平铺列表、无折叠条。分页的
  `limit` 只作用于未完结行（「加载更多（还有 N 条）」不含已完结数），
  展开后已完结行一次性全部渲染；只有已完结线程时列表区只有折叠条，
  不显示「暂无线程」空态。
- 列表条目是三行式（`components/threads/thread-row.tsx`，`data-testid="thread-card"`）：
  第一行项目徽章+名称与状态（运行中带时长），第二行 `line-clamp-2` 标题，
  第三行分支 + PR 数/相对时间；右栏正在显示的线程整行 `bg-accent` 高亮并带
  `aria-current="page"`；等待输入用左侧 2px 琥珀竖条，已完结整行
  opacity-60（hover/focus 恢复）。hover/focus 由 `ui/preview-card.tsx`
  （Base UI PreviewCard，portal 定位）展开 `ThreadPreviewContent`，承载完整
  标题、环境、模型、绝对时间、PR 数等；卡片在列表**右侧**展开
  （`side="right"`，`sideOffset` 10，不遮挡列表；窄屏无右侧空间时由
  floating-ui 自动翻转），按可用宽高收缩（`--available-width/height`），
  超高时内部滚动。
- 每页 60 条、筛选状态存内存 store（`lib/store/thread-list-store.ts`），刷新页面
  回默认视图；筛选控件由 `ThreadSidebar`/`ThreadFilterControls` 承载
  （`components/threads/thread-sidebar.tsx`）。
- 列表在常驻 layout 里不会随导航重挂载，所以新鲜度靠两处补：切线程时 layout
  重读一次列表；详情每次重读（挂载、聚焦、SSE 失效、写操作结算）把最新
  `ThreadSummary` 回写列表缓存
  （`mergeThreadSummary`），避免「右栏已结束、左栏还显示运行中」以及排序/计数
  滞后。
- 详情页信息卡（`ThreadInfoCard`）按**右栏容器宽度**而非视口显隐：右栏是
  `@container`，卡片 `@4xl` 起出现、`@5xl` 加宽，中等视口下不再把聊天区
  挤窄；顶栏的父线程入口同步用 `@4xl` 断点与信息卡互补。
- `ProjectBadge` 已独立到 `components/threads/project-badge.tsx`，旧
  `thread-card.tsx`（大卡片）已删除。

## 已知限制

- 列表/运行态来自常驻订阅 + 增量推送（近实时）；详情的 queue/activity 取自
  读取瞬间，返回后的变化要刷新才可见。
- 线程列表为全量轻量快照，筛选/分页由前端处理。
- 凭证吊销不再逐请求探测：hub 连接存活期间吊销要等下一次重连（探测 401）才
  暴露为 `repair`；在此之前列表仍给出内存中的线程并如实标注连接状态。
- 归档面失败/未就绪期间列表不完整（只有活跃线程），由 `degraded` banner 说明。
- 补读正文受上游 `getTurnItem` 的 256KB 限界，截断后缀原样显示、不掩盖。
- 文件变更永远没有 diff：上游 detail 读路径也不恢复 diff。
- DPoP 会话不支持：只接受 `token_type=Bearer`。
- 写操作不做乐观并发控制：commandId 每次新生成，重试可能产生重复消息
  （T3 的 commandId 冲突检测只对同 ID 重放生效）；队列类操作靠 40916 反馈。
