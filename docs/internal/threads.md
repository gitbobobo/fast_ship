# T3 线程只读集成（INT-82）

Fast Ship 以「远程只读观察者」身份接入 T3 Code 环境：配对一个 T3 服务端，聚合展示该环境
上的线程列表与单个线程的完整时间线。线程与事件**不落库、不订阅实时流**——每次 API 调用
实时转发上游数据。

第一版只接 T3 单一适配器，严格只读：无消息发送、无运行/停止、无审批、无归档、无终端、
无浏览器、无 diff。任何写入型 RPC/HTTP 端点都不在本适配器的调用面上。

## 实现坐标

| 关注点 | 文件 |
| --- | --- |
| 环境模型（凭证加密落库） | `server/internal/model/t3_environment.go` |
| 环境仓库（按字段条件 UPDATE） | `server/internal/repository/t3_environment.go` |
| 配对链接解析 | `server/internal/service/t3_pairing.go` |
| T3 HTTP 客户端（换 token/探测/线程详情） | `server/internal/service/t3_client.go` |
| T3 WS RPC 客户端（effect rpc JSON 帧） | `server/internal/service/t3_ws.go` |
| 环境配对/改名/删除/探测 | `server/internal/service/t3_environment_service.go` |
| 线程聚合与条目补读 | `server/internal/service/t3_threads_service.go` |
| T3 线上形状与归一化 | `server/internal/service/t3_wire.go` |
| 路由（仅 JWT） | `server/internal/router/router.go`（`t3` 组） |

## 协议来源

以下全部核对自 T3 真实源码（本机 `/Users/godbobo/work/tools/t3code`，2026-10），
未凭猜测写契约：

- **OAuth token 兑换**：`packages/contracts/src/auth.ts`（`AuthTokenExchangeRequest` /
  `AuthAccessTokenResult`）+ `packages/contracts/src/environmentHttp.ts`
  （`POST /oauth/token`）。请求为 `application/x-www-form-urlencoded`：
  `grant_type=urn:ietf:params:oauth:grant-type:token-exchange`、
  `subject_token=<配对码>`、`subject_token_type=urn:t3:params:oauth:token-type:environment-bootstrap`、
  `requested_token_type=urn:ietf:params:oauth:token-type:access_token`、`scope=orchestration:read`。
- **WS RPC 线路协议**：`apps/server/src/ws.ts`（`GET /ws?orchestrationProtocol=2`，
  认证顺序 wsTicket → Bearer/cookie）+ effect@4.0.2 `rpc/RpcSerialization.ts`
  （每帧一个 JSON）+ `rpc/RpcMessage.ts`（Request/Chunk/Exit/Ack/Interrupt 帧形状）。
  Interrupt 只取消单个订阅请求，连接可继续承载后续一次性 RPC。
- **读方法与 scope**：`apps/server/src/auth/RpcAuthorization.ts` 的
  `RPC_REQUIRED_SCOPES`——`orchestration.subscribeShell`、
  `orchestration.getArchivedShellSnapshot`、`orchestration.getTurnItem` 均为
  `orchestration:read`。
- **归档线程是独立 RPC**：真实 T3 的活跃 shell 快照（`buildActiveShellSnapshot`）
  把 `archivedThreads` 写死为 `[]`；归档数据只由一次性 RPC
  `orchestration.getArchivedShellSnapshot` 提供，且字段名是 `threads`
  （`OrchestrationV2ArchivedShellSnapshot`）。测试必须按此形状模拟，
  不能把归档塞进 subscribeShell。
- **子代理排除**：T3 自身的 `listProjectThreads` 用
  `lineage.relationshipToParent !== "subagent"` 过滤（app 侧 delegate_task
  子线程与 provider 原生子代理都排除）。Fast Ship 列表同口径；详情仍可通过
  父条目的 `child_thread_id` 访问子线程。
- **线程详情走 HTTP 而非 WS**：T3 对此有官方分流——
  `apps/server/src/orchestration-v2/http.ts` 注释明确 HTTP 快照端点用于「把大投影从
  socket 上卸下来」；WS 的 `orchestration.getThreadProjection` 在服务端被
  `THREAD_HISTORY_SNAPSHOT_ROW_LIMIT` 截成有界窗口。只有
  `GET /api/orchestration/threads/:threadId` 返回完整投影。该端点同样只要
  `orchestration:read` + `x-t3-orchestration-protocol: 2` 头。
  注意：HTTP 投影仍经 `projectThreadProjectionForWire` 裁剪——命令输出被移除
  （留 `outputOmitted`）、成功文件变更的 diff 被移除、subagent prompt/result 在
  wire 层限长（32KB）。「完整」指完整历史窗口，不是未裁剪正文。
  **投影的 thread 是 AppThread 不是 shell**（contracts `orchestrationV2.ts`:
  `OrchestrationV2ThreadProjection.thread = OrchestrationV2AppThread`，约 :363）：
  只有 id/projectId/title/provider/model/branch/lineage/archivedAt 等线程事实，
  **没有** status/activityRunStatus/pendingRuntimeRequest——那些只属于
  ThreadShell（约 :1927，服务端由 `threadShellFromProjection` 从投影派生）。
  详情的运行态因此不能读投影 thread，必须另取可信 shell（见下）。
- **详情运行态取同一 WS 快照面的可信 shell**：详情读取 = HTTP 投影（时间线）
  + WS `subscribeShell` 活跃快照 + `orchestration.getArchivedShellSnapshot`
  （归档），在该线程 ID 对应的 shell 上取运行态与元数据（title/provider/model/
  branch/PR 数/项目名）。快照两侧都含子代理线程，详情不过滤 lineage——列表
  排除子代理、详情仍可经 `child_thread_id` 进入。快照里找不到该线程（如刚被
  删除）时明确报错，不假 idle。T3 没有 `orchestration.getThreadShell` 这样的
  单线程 shell RPC（`ORCHESTRATION_V2_WS_METHODS` 只有
  subscribeShell/subscribeArchivedShell/getArchivedShellSnapshot/
  getThreadProjection/getTurnItem 等），两份快照就是可信 shell 的全部来源。
- **可见时间线是 visibleTurnItems**：T3 UI 按 `ProjectionStore.buildVisibleTurnItems`
  渲染——fork 线程的继承前缀（`visibility: "inherited"`，`sourceThreadId` 指向
  来源线程）只存在于 `visibleTurnItems`；回滚（`rolled_back` run）/取消的排队消息/
  被取代的中断结果也由上游按 `createOrchestrationV2TurnItemVisibility` 排除。
  Fast Ship 按行顺序归一化 `visibleTurnItems[].item`，不在 Fast Ship 侧重做
  继承/回滚规则。
- **条目正文补读**：`orchestration.getTurnItem`（输入
  `{threadId, itemId, revision?}`，输出 `{item: TurnItem | null}`）按需返回完整
  input/output，单值上游限界 256KB（`MAX_ON_DEMAND_BYTES`），截断后缀
  `… output truncated for transport` 原样透传。文件变更的 detail 读路径
  （`projectTurnItemForDetail`）也不恢复 diff——diff 有专用读路径，不属于本集成。
- **配对链接语义**：`packages/shared/src/remote.ts`（`resolveRemotePairingTarget`）、
  `apps/mobile/src/features/connection/pairing.ts`（`buildPairingUrl`）、
  `apps/server/src/cli/pair.ts` 与 `startupAccess.ts`（`t3 pair` 输出
  `https://host/pair#token=...`）。直接链接解析只保留 origin（`/pair` 路径清除、
  origin 不变），hash 里带 token（兼容 query）；hosted 包装链接
  `?host=<backend>&token=` 的 host 参数才是真正后端；协议支持 http/https/ws/wss；
  缺省协议时 IP 字面量补 http、其余补 https。
- **服务身份探测**：`GET /.well-known/t3/environment`（`ExecutionEnvironmentDescriptor`）
  是 T3 自己的探测路径（`t3 pair` 的 `probeEnvironmentDescriptor`）。

## 配对与会话生命周期

1. 用户提交 `pairing_url`（POST /api/environments，或 PUT 携带 `pairing_url` 重配）。
2. 解析出 `{http_base, ws_base, credential}`（`t3_pairing.go`）。
3. `POST {http_base}/oauth/token` 用一次性配对码换取 Bearer，**只请求
   `orchestration:read`**，并校验返回的 `scope` 恰好等于请求值——服务端授予任何更宽
   的 scope 都拒绝建立会话（防宽权限会话）；`token_type` 非 `Bearer`（如 DPoP）同样拒绝。
4. access_token 用 AES-256-GCM 加密落库（`pkg/crypto`，密钥 `encryption.key`），
   与 AI Key / GitHub Token 同一路径；配对码一次性、不落库。
5. Bearer 到期（`expires_in`）后**无法续期**：原配对码已消费。会话失效的环境在探测中
   进入 `repair`，用户重新配对即修复（PUT 带 `pairing_url`）。删除环境为本地删除。

环境状态（列表逐个探测 `GET /api/auth/session`，单请求同时回答可达性与凭证有效性）：

| 状态 | 判定 |
| --- | --- |
| `ok` | 200 且 `authenticated=true` |
| `repair` | 401/403（服务端应答了但拒绝凭证）或凭证解密失败 |
| `unreachable` | 网络错误、超时、其余状态码/5xx |

## 并发与写入约定

- **远程兑换不持锁**：配对码换 Bearer 的网络往返不持有任何数据库行状态；落库前
  检查 ctx（请求取消后不落库，避免客户端已报失败而配对码被消费、记录却写入的错位）。
- **一次条件 UPDATE 落库全部请求字段，不用 Save/upsert**：Update 先完成全部参数
  校验与远程配对兑换，再构造仅含请求字段的更新集合（改名写 `name` 列、重配只写
  配对产物列，互不覆盖），一次 `WHERE id + user_id` 的条件 UPDATE 落库——中途失败
  （坏配对链接、兑换被拒、取消）不产生部分写入。影响 0 行即视为环境已被并发删除，
  返回 40417，**绝不复活记录**（GORM Save 在 UPDATE 0 行时会退化 INSERT）。
  唯一索引冲突（并发重名：Create 与改名竞态同源）映射 40912。
- **所有 service 方法接受请求 ctx**：配对/探测/列表全程受请求上下文与独立预算约束，
  取消即中止。

## 读路径与预算

- **线程列表**（GET /api/threads）：多环境**有界并行**（并行度 4），不落库。
  每环境在单环境预算内：① 探测会话；② WS RPC `orchestration.subscribeShell`
  取首个 Chunk 的活跃 `snapshot`（projects + threads），Ack + Interrupt（连接保留）；
  ③ 同一连接一次性 RPC `orchestration.getArchivedShellSnapshot` 取归档线程；
  ④ 断开。单环境失败或超时不阻塞其它环境，状态与错误保留在 `environments[]`，
  健康环境的已读结果照常返回。**归档 RPC 失败是部分失败**：活跃快照已到手时
  归档读取失败不推翻该环境——活跃线程照常返回，环境标 `unreachable`
  （error=`归档线程暂不可读`），由 problem banner 提示降级，而不是把健康线程
  整批丢成空；只有活跃面读取失败（探测/握手/快照）才让该环境整体不可达。
- **线程详情**（GET /api/environments/:id/threads/:threadId）：两段读取共享
  一个整体 deadline（`ThreadDetail` 预算，25s < 前端 35s），**先读运行态、后读
  时间线**——① 同环境 WS 快照面取该线程的可信 shell（活跃 + 归档，见上）供
  运行态与元数据（含项目名）；② HTTP `GET {address}/api/orchestration/threads/`
  `{threadId}`，按 `visibleTurnItems` 归一化为 `items[]`。顺序保证返回正文不早于
  运行态：两读之间运行结束时拼出「idle 运行态 + 更新的时间线」，前端停止轮询
  也不漏最终消息（反序会留下「旧时间线 + idle」的错位）。上游 404 → 40418，
  凭证被拒 → 50202，shell 快照无此线程 → 50202（明确失败）。**归档面不可信时
  不下存在性结论**：活跃面没有该线程且归档读取失败 → 直接 50202（归档线程
  会在此被谎报 404），只有归档读取成功、两份快照都没有时才读 HTTP 投影分
  40418（投影 404）/50202（投影在、shell 无）。
- **条目正文**（GET /api/environments/:id/threads/:threadId/items/:itemId）：
  先取指定线程的可见时间线验证 `itemId` 是其成员（40419 = 不是成员），再从可信行
  自身派生 `getTurnItem` 参数（threadId/itemId 取行内条目的持久身份；不接受前端
  指定来源或 RPC 方法），返回完整归一化 ThreadItem。
- **预算**（`t3RemoteBudgets`，默认值）：整轮聚合 20s（< 前端 API 超时 35s）、
  单环境列表 6s（探测 + WS 握手 + 两次 RPC 共享）、线程详情 25s（HTTP 投影 +
  WS shell 快照共享）、条目补读 25s（HTTP 快照 + WS getTurnItem）。WS 握手由
  调用方 ctx 约束（挂起的服务端不能绕过读超时），单帧读 15s、写 10s 各自独立；
  建连后 ctx 取消经关闭连接立即打断阻塞中的读写（x/net 不感知 ctx），取消后的
  调用在写出前即失败，整轮预算因此是硬边界。

### 状态映射（列表与详情共用）

判定顺序对齐 T3 Sidebar（`resolveSidebarThreadStatus`）与
`threadShellFromProjection`：`archivedAt` 非空 → `archived`；其次
`pendingRuntimeRequest` 非空（等用户审批/输入/代答 secret）→ `waiting`；再看
`activityRunStatus`（后台 run 未停时 shell.status 可能已是 completed/idle）：
preparing/starting/running → `running`、waiting → `waiting`；然后是
`pendingBackgroundTasks`（根 run 结束后仍在跑的后台工作，上游契约
`OrchestrationV2PendingBackgroundTask`：kind ∈ subagent/command/monitor/
background_task，未知 kind 回退 background_task、kind 缺省按 background_task
加载）：按上游 `backgroundWorkHoldsCompletion` 的口径，kind=command 不占完成位
（dev server 之类可长跑数小时），其余一切 kind 含未知都占——存在占位条目 →
`running`（线程还有会唤醒代理继续的工作，详情保持轮询；不是「等待输入或
审批」）；最后 `shell.status` 兜底（preparing/queued/starting/running →
`running`、waiting → `waiting`、其余 → `idle`）。**详情不扫描历史 runs**：
投影里的 `thread` 是 AppThread（无运行态字段），详情与列表共用同一映射、
读同一份可信 shell 快照（`subscribeShell` 活跃 + `getArchivedShellSnapshot`
归档，按线程 ID 找出，不过滤子代理）。

### Turn item 归一化映射

T3 `OrchestrationV2TurnItem`（约 24 种类型）→ Fast Ship 8 种 `ThreadItem`。
条目 ID 由来源身份合成：`sourceThreadId + "::" + T3 原始条目 id`（继承行与本地行
的原始 id 可能重复，靠来源线程区分）。正文不额外截断（上游裁剪如实保留）；
summary 是 rune 安全的单行短摘要。条目自带版本：上游条目的 `updatedAt` 以
`updated_at` 原样透传（RFC3339；上游省略即缺省），补读响应同样携带。

| T3 类型 | ThreadItem.type | text（快照） | summary | detail_available |
| --- | --- | --- | --- | --- |
| `user_message` | `user_message` | 全量文本 | — | false |
| `assistant_message` | `assistant_message` | 全量文本 | 首行 | false |
| `proposed_plan` | `assistant_message` | markdown | 首行 | false |
| `reasoning` | `reasoning` | 全量文本 | 首行 | false |
| `command_execution` | `command_execution` | 命令 | 命令首行 | **true**（输出经补读） |
| `dynamic_tool` | `dynamic_tool` | 负载 JSON（tool_name/input/output） | 工具名 | **true** |
| `file_search` / `web_search` / `todo_list` | `dynamic_tool` | 负载 JSON | 类型名 | false（快照即全部） |
| `file_change` | `file_change` | 真实元数据（文件名/±行数/子操作；失败态含上游截断 diff） | `文件名 (+a/-d)` | false（上游 detail 无 diff，不调用 diff 读路径） |
| `subagent` | `subagent` | 任务 prompt | 任务首行；`child_thread_id` 带出 | **true**（结果/进度经补读） |
| `error` | `error` | failure.message | failure.class | false |

补读响应（items/{itemId}）的 text 组合：命令 = `input + "\n\n--- 输出 ---\n\n" + output`；
subagent = prompt + `--- 结果 ---` + result + `--- 进度 ---` + progress；
dynamic_tool = 全量负载 JSON。补读响应的 `detail_available` 恒为 false
（已无可再取的正文）。

丢弃（结构性/交互性事件，不归入 8 类）：`notification`、`system_notice`、
`user_input_request`、`approval_request`、`run_interrupt_request/result`、`checkpoint`、
`compaction`、`handoff`、`fork`、`thread_created`、`secret_request`。
未知类型（前向兼容）同样跳过——与 T3 客户端自身的解码策略一致。
工具/推理条目由前端默认折叠（UI 侧约定）。

## 错误码

| 码 | 含义 |
| --- | --- |
| 40005 | 配对链接无效或缺少配对码 |
| 40417 | T3 环境不存在 |
| 40418 | T3 线程不存在（上游 `thread_not_found`） |
| 40419 | T3 线程条目不存在（itemId 不是该线程可见时间线的成员） |
| 40912 | 环境名称在当前用户下已存在 |
| 50202 | T3 环境调用失败（含配对失败、凭证失效提示） |

## 权限与安全

- **仅 JWT**：环境配对涉及用户私有凭证，七个端点全部挂在 `middleware.RequireJWT`，
  API Key（`fsk_`）一律 403（40301）——与 AI 设置同构。
- **环境按用户隔离**：所有查询带 `user_id` 条件，跨用户访问一律 404。
- **凭证存储**：Bearer 加密落库（AES-256-GCM）；API 响应结构体不含凭证字段
  （模型层 `json:"-"`）。列表/聚合响应只回 `address`（规范化 origin）。
- **不跟随重定向**：上游 HTTP 客户端 `CheckRedirect` 返回错误——配对地址是用户输入的
  远端，跟随跳转可能把 Bearer 带到非预期主机。
- **超时与尺寸**：整轮/单环境/补读三级预算（见上）；WS 握手随 ctx、单帧读 15s、
  写 10s；HTTP 响应体与 WS 帧上限 64MB，HTTP 侧读到上限 +1 字节即**明确拒绝**
  （截断的响应体不能当成功解析——截断点可能恰好还是合法 JSON）。
- **允许本机/私网地址**：T3 环境本来就常跑在本机或 Tailscale 内网（`t3 pair` 对
  loopback 会提示「仅本机可达」）。这是产品的明确用途，不对目标网段做限制；
  防护重心放在凭证不外泄（上两条）与固定 scope 上。
- **吊销的协议限制**：T3 的会话吊销端点（`/api/auth/clients/revoke`、
  `revoke-others`）要求 `access:write`，只读会话无法自吊销；因此删除环境只做本地
  删除，Bearer 由 T3 服务端按 `expires_in` 自行过期。需要立即吊销时在 T3 侧的
  访问设置（Pairing/Access UI）操作。这是协议边界，不是实现遗漏。

## 已知限制

- 不做实时推送：Fast Ship 侧每次调用现拉现转，T3 端新事件要刷新才可见（第一版约定）。
- 线程列表为全量轻量快照，筛选/60 条分页由前端处理（第一版约定）。
- 补读正文受上游 `getTurnItem` 的 256KB 限界，超长输出带
  `… output truncated for transport` 后缀，原样显示、不掩盖。
- 文件变更永远没有 diff：上游 detail 读路径也不恢复 diff（专用 diff 读路径
  `getFullThreadDiff` 不属于本只读集成），展开只显示真实元数据。
- 详情的运行态取自读取瞬间的 shell 快照：详情返回后线程状态再变化要刷新才可见
  （与列表同，不做实时推送）。
- fork 合成标记行（`turn-item:fork:*`，"Forked from conversation"）不在持久层，
  归一化按结构性事件丢弃；继承前缀本身通过 `visibleTurnItems` 完整呈现。
- DPoP 会话不支持：只接受 `token_type=Bearer`（DPoP 需要每请求签名，超出第一版范围）。
