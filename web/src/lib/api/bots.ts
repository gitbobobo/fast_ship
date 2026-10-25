import type { components } from "@/types/api.gen";
import { api, decodeApiError } from "./client";

// 内置机器人（INT-101，INT-133 起地址/密钥改由统一 AI 提供商派生）：
// 与契约形状等价的类型引用 api.gen.ts；明文与密文形式的 API Key 任何响应
// 都不回传，仅返回末四位。
//
// 消息/调用/run 这几组保留手写，契约形状不等价（细节见各自注释）：
// - 契约把 message.content / call.arguments 建模为无属性对象（生成
//   Record<string, never>），前端需要按 kind 的字段文档化访问；
// - 契约缺 run.error_action（SSE run_error 事件携带、REST 快照缺省）；
// - 前端维护 status/kind 的字面量联合供穷尽 switch 的标签映射使用。
type Schemas = components["schemas"];

/** 最近一条主线消息预览：契约 kind 为 string；前端按 user/assistant 区分前缀。 */
export type BotLastMessage = Schemas["BotLastMessage"];

export type Bot = Schemas["Bot"];

/** 契约名为 CreateBotRequest。 */
export type CreateBotInput = Schemas["CreateBotRequest"];

/** 契约名为 UpdateBotRequest（指针语义：出现才更新）。 */
export type UpdateBotInput = Schemas["UpdateBotRequest"];

/** 检查结果 checks 数组的元素（契约内联在 BotConnectionCheck.checks 里，直接取内联类型）。 */
export type BotConnectionCheck = Schemas["BotConnectionCheck"]["checks"][number];

/** 契约名就叫 BotConnectionCheck（整体结果），与上面的条目类型重名故保留映射名。 */
export type BotConnectionTestResult = Schemas["BotConnectionCheck"];

/** 契约名为 TestBotConnectionRequest；地址与密钥一律取 provider。 */
export type BotConnectionTestInput = Schemas["TestBotConnectionRequest"];

export type BotMessageKind =
  | "user"
  | "assistant"
  | "tool_reply"
  | "background_result"
  | "approval_decision"
  | "lifecycle"
  | "schedule_event";

/** assistant content.tool_calls 的条目：与调用账本（calls）按 call_id 关联。 */
export interface BotAssistantToolCall {
  call_id: string;
  provider_tool_call_id: string;
  tool_name: string;
  action: string;
  /** 规范化参数的 JSON 字符串（协议原样，需要对象时自行 parse）。 */
  arguments: string;
}

/**
 * content 按 kind 变化的正文，字段与后端各 kind 的结构一一对应：
 * user/assistant 是 text（assistant 另带 tool_calls）；tool_reply 携带
 * call_id/tool_name/status 与协议回复字符串 content；background_result 携带
 * call_id/tool_name/action/status 与可选 summary、错误信息；approval_decision
 * 是 call_id + decision；lifecycle 是 event + 任意 detail；schedule_event
 * 携带 task_id/task_name/run_id/status 与可选 summary、错误信息。字段可空
 * 是因为具体形状取决于 kind。
 */
export interface BotMessageContent {
  text?: string;
  tool_calls?: BotAssistantToolCall[];
  call_id?: string;
  provider_tool_call_id?: string;
  tool_name?: string;
  action?: string;
  status?: string;
  /** tool_reply：发送给模型的协议回复字符串。 */
  content?: string;
  summary?: string;
  error_code?: string;
  error_message?: string;
  effect_may_have_occurred?: boolean;
  /** background_result：分级放行评估结论（INT-160），自动执行的调用据此在聊天流里显示徽标。 */
  policy?: BotCallPolicy;
  decision?: string;
  event?: string;
  detail?: unknown;
  /** schedule_event：定时任务运行事件的归属信息。 */
  task_id?: string;
  task_name?: string;
  run_id?: string;
}

export interface BotMessage {
  id: string;
  bot_id: string;
  seq: number;
  run_id?: string | null;
  request_id?: string | null;
  call_id?: string | null;
  kind: BotMessageKind;
  content: BotMessageContent;
  created_at: string;
}

export interface BotMessagesQuery {
  /** 只取 seq 大于该值的正序一页。 */
  after_seq?: number;
  /** 取 seq 小于该值的最后一页（「加载更早」）。 */
  before_seq?: number;
  limit?: number;
}

export type BotCallStatus =
  | "pending_confirmation"
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "rejected"
  | "cancelled"
  | "not_executed";

export type BotCallDecision = "approve" | "reject" | "always_allow";

/**
 * 分级放行（INT-160）的评估结论：服务端按「动作+参数+目标线程状态」算档，
 * 前端只渲染不判断。auto 且 tier=tier1 时工具卡显示「自动执行」徽标；
 * downgrade_reason/expiry 只出现在降级确认卡上。
 */
export interface BotCallPolicy {
  tier: "tier1" | "tier2" | "tier3";
  rule_id?: string;
  auto: boolean;
  source?: string;
  facts?: Record<string, unknown>;
  downgrade_reason?: string;
  expiry?: string;
  approved_call_id?: string;
  schedule_denied?: boolean;
}

export interface BotCall {
  id: string;
  bot_id: string;
  tool_name: string;
  action: string;
  arguments: Record<string, unknown>;
  execution_mode: "sync" | "async";
  requires_confirmation: boolean;
  status: BotCallStatus;
  decision?: BotCallDecision | null;
  decided_at?: string | null;
  error_code?: string | null;
  error_message?: string | null;
  /** 服务端生成的执行身份与授权范围；fs_schedule 确认卡的预览在 execution_context.schedule。 */
  execution_context?: Record<string, unknown> | null;
  /** 分级放行评估结论（INT-160）；历史行为空即「策略上线前」。 */
  policy?: BotCallPolicy | null;
  /** 副作用可能已发生（取消竞速、重启恢复等）；如实展示，不声称已回滚。 */
  effect_may_have_occurred?: boolean;
  /** 进入模型上下文的受限结果预览；完整结果经 result 端点按需读取。 */
  result_preview?: unknown;
  truncated?: boolean;
  created_at: string;
  started_at?: string | null;
  finished_at?: string | null;
}

export type BotRunStatus =
  | "queued"
  | "running"
  | "completed"
  | "failed"
  | "stopped";

// generic run 端点按 run 原值返回：定时任务触发的 run 是 schedule，手动触发是 manual；
// schedule_event 是定时任务运行事件写入主线后的立即唤醒（INT-138）。
export type BotRunTrigger =
  | "user_message"
  | "tool_result"
  | "recovery"
  | "schedule"
  | "manual"
  | "schedule_event";

/** run 失败时的可操作动作（INT-137）：kind 由前端映射到具体页面。 */
export interface BotRunErrorAction {
  /** 当前取值 ai_providers_settings（AI 提供商设置页）。 */
  kind: string;
  label: string;
}

export interface BotRun {
  id: string;
  bot_id: string;
  trigger_source: BotRunTrigger;
  status: BotRunStatus;
  /** 服务端已受理停止请求（响应是布尔，不回传请求时刻）。 */
  stop_requested: boolean;
  /** usage 缺失必须与零区分：usage_complete=false 时计数不可信。 */
  prompt_tokens?: number | null;
  completion_tokens?: number | null;
  total_tokens?: number | null;
  cached_tokens?: number | null;
  usage_complete?: boolean;
  /** 已计价请求的成本累计（USD）；目录无价/缺 usage 时为 null，缺失 ≠ 0，整项隐藏（INT-139）。 */
  cost_estimate?: number | null;
  /** 计价来源（catalog）；cost_estimate 为 null 时同 null。 */
  cost_source?: string | null;
  /** 显示口径成本（INT-162）：本 run 已计价成本 + 关联维护已计价成本；都为 null 时为 null。 */
  display_cost_estimate?: number | null;
  error_code?: string | null;
  error_message?: string | null;
  /** 可操作失败的动作提示；仅 run_error 事件携带，REST 快照缺省。 */
  error_action?: BotRunErrorAction | null;
  created_at: string;
  started_at?: string | null;
  finished_at?: string | null;
}

export type BotAuthorization = Schemas["BotAuthorization"];

/** 分级放行 tier1 规则的开关与误判状态（INT-160）；行缺失按启用展示。 */
export type BotWritePolicyRule = Schemas["BotWritePolicyRule"];

/** 契约同名的指标口径；进行中或 usage 未收齐时 token 计数为 null。 */
export type BotRequestMetrics = Schemas["BotRequestMetrics"];

/** 聊天页指标区的一致读取：窗口能力 + 最近一次主线模型请求口径。 */
export type BotMetrics = Schemas["BotMetrics"];

/**
 * 活动主线 run 最近一条维护进度（压缩阶段/失败原因）：与 SSE 的
 * kind=maintenance_progress delta 事件同源，快照随取随带，刷新后运行栏
 * 不丢反馈。无活动主线 run 或尚无进度事件时整体缺省。
 */
export type BotMaintenanceProgress = Schemas["BotMaintenanceProgress"];

/**
 * 聊天页一致快照：bot、消息尾部页、调用、run 与事件水位取自服务端
 * 同一读取点。订阅游标取 next_event_seq - 1，与快照内容严格衔接，
 * 既不重放快照已含的事件也不漏掉其后的事件。
 *
 * 保留手写：契约 BotChatSnapshot 内嵌 BotMessage/BotToolCall（形状不等价，
 * 见文件头说明），别名会把消息与调用的字段访问一起拖垮。
 */
export interface ChatSnapshot {
  bot: Bot;
  /** 消息最新一页（默认 50 条），seq 正序。 */
  messages: BotMessage[];
  calls: BotCall[];
  runs: BotRun[];
  /** 与快照同一读取点的事件账本水位（下一条的 seq）。 */
  next_event_seq: number;
  /** 指标区数据源；服务端未上线该字段时整体缺省。 */
  metrics?: BotMetrics;
  /** 活动 run 的维护进度；SSE 后续事件优先，done/error 清空后不再出现。 */
  maintenance_progress?: BotMaintenanceProgress;
}

/** 契约名为 SendBotMessageRequest。 */
export type SendBotMessageInput = Schemas["SendBotMessageRequest"];

/**
 * 契约类型 + message/run 局部覆盖为前端富类型（契约把 content 建模为无
 * 属性对象、缺 run.error_action，别名会把消费方的类型访问一起拖垮）。
 */
export type SendBotMessageResult = Omit<
  Schemas["SendBotMessageResult"],
  "message" | "run"
> & {
  message: BotMessage;
  /** 受理时已存在的 run；没有则由服务端随后启动。 */
  run: BotRun | null;
};

// ---- 定时任务（INT-103）----
// 字段引用生成类型；status/trigger_source 的字面量联合供穷尽 switch 用。

export type BotScheduleRunStatus =
  | "running"
  | "completed"
  | "failed"
  | "stopped"
  | "skipped";

export type BotScheduleTriggerSource = "cron" | "manual";

/** 契约类型 + status/trigger_source 字面量收窄。 */
export type BotScheduleRun = Omit<
  Schemas["BotScheduleRun"],
  "status" | "trigger_source"
> & {
  status: BotScheduleRunStatus;
  /** 触发来源：cron 到点触发，manual 为工具 fs_schedule.run 手动触发。 */
  trigger_source: BotScheduleTriggerSource;
};

/**
 * 定时任务只能由机器人经工具创建/修改/删除，前端只读 + 启停。
 * timezone 空串按 UTC；model 空串 = 用机器人自身模型。
 * state 用原始 JSON 文本而不是解好的值（INT-123），避免浏览器把大整数折成 double。
 */
export type BotScheduleTask = Omit<Schemas["BotScheduleTask"], "last_run"> & {
  last_run: BotScheduleRun | null;
};

export interface BotScheduleRunsQuery {
  limit?: number;
  offset?: number;
}

export function botScheduleRunStatusLabel(status: BotScheduleRunStatus): string {
  switch (status) {
    case "running":
      return "运行中";
    case "completed":
      return "已完成";
    case "failed":
      return "已失败";
    case "stopped":
      return "已停止";
    case "skipped":
      return "已跳过";
  }
  // 词表外的存量/未来状态兜底显示原词，不把 undefined 渲染进界面。
  return status;
}

// ---- 花费总览（INT-162）----
// 类型直接引用生成契约：金额一律是内置目录单价估算（USD），可空字段表示
// 「未计价」——缺失 ≠ 0，展示写「—」而不是 $0。

export type BotUsageRange = "today" | "7d" | "30d";

export type BotUsageOverview = Schemas["BotUsageOverview"];

export type BotUsageBot = Schemas["BotUsageBot"];

export type BotUsageDay = Schemas["BotUsageDay"];

export type BotUsageScheduleTask = Schemas["BotUsageScheduleTask"];

export type BotUsageRunDetail = Schemas["BotUsageRunDetail"];

/**
 * 花费优化参考（INT-163）：与总览同窗口的只读聚合。子类型一并引出，
 * 组件层按需解构（BotUsageContextPoint / BotUsageModelGroup 等）。
 */
export type BotUsageInsights = Schemas["BotUsageInsights"];

export type BotUsageInsightsBot = Schemas["BotUsageInsightsBot"];

export type BotUsageInsightsTask = Schemas["BotUsageInsightsTask"];

export type BotUsageContextPoint = Schemas["BotUsageContextPoint"];

export type BotUsageRun = Schemas["BotUsageRun"];

export type BotUsageRequest = Schemas["BotUsageRequest"];

export type BotUsageMaintenanceRun = Schemas["BotUsageMaintenanceRun"];

export type BotUsageMaintenanceRequest =
  Schemas["BotUsageMaintenanceRequest"];

// ---- 周期预算（INT-164）----
// thresholds 是相对金额的比例（0.8 = 80%、1 = 100%，可超 1），服务端
// 规范化去重升序；spend_usd 只统计已计价花费，预算越线只告警不停止运行。

export type BotBudget = Schemas["BotBudget"];

export type BotBudgetPeriod = BotBudget["period"];

/** 契约名为 BotBudgetUpsertRequest。 */
export type BotBudgetUpsertInput = Schemas["BotBudgetUpsertRequest"];

export const BOT_REQUEST_TIMEOUT_MS = 35_000;

function options(signal?: AbortSignal) {
  return { signal, timeout: BOT_REQUEST_TIMEOUT_MS, retry: 0 } as const;
}

/** 写接口：同样 35 秒超时、不重试，错误在边界解码成 ApiRequestError。 */
function write<T>(promise: Promise<ApiResponse<T>>) {
  return decodeApiError(promise).then((res) => res.data);
}

const botPath = (botId: string) => `bots/${encodeURIComponent(botId)}`;
const callPath = (botId: string, callId: string) =>
  `${botPath(botId)}/calls/${encodeURIComponent(callId)}`;
const scheduleTaskPath = (botId: string, taskId: string) =>
  `${botPath(botId)}/schedule-tasks/${encodeURIComponent(taskId)}`;

/** searchParams 只收已定义字段，避免 undefined 被序列化成字符串。 */
function definedParams(params: Record<string, string | number | undefined>) {
  const searchParams: Record<string, string> = {};
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined) searchParams[key] = String(value);
  }
  return Object.keys(searchParams).length > 0 ? searchParams : undefined;
}

export const botApi = {
  list: (signal?: AbortSignal) =>
    decodeApiError(api.get("bots", options(signal)).json<ApiResponse<{ items: Bot[] }>>()),

  create: (input: CreateBotInput) =>
    write(api.post("bots", { json: input, ...options() }).json<ApiResponse<Bot>>()),

  /** 测试连接可针对未保存配置，也可基于已存机器人覆盖表单值。 */
  testConnection: (input: BotConnectionTestInput) =>
    write(
      api
        .post("bots/test-connection", { json: input, ...options() })
        .json<ApiResponse<BotConnectionTestResult>>(),
    ),

  get: (botId: string, signal?: AbortSignal) =>
    decodeApiError(
      api.get(botPath(botId), options(signal)).json<ApiResponse<Bot>>(),
    ),

  /** message_limit 用服务端默认（50，与 BOT_HISTORY_PAGE 一致），不额外传参。 */
  getChatSnapshot: (botId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(`${botPath(botId)}/chat`, options(signal))
        .json<ApiResponse<ChatSnapshot>>(),
    ),

  update: (botId: string, input: UpdateBotInput) =>
    write(
      api.put(botPath(botId), { json: input, ...options() }).json<ApiResponse<Bot>>(),
    ),

  /** 存在活动 run 或未终结调用时服务端返回冲突，需先显式处理。 */
  delete: (botId: string) =>
    write(api.delete(botPath(botId), options()).json<ApiResponse<null>>()),

  /**
   * 主线消息：页内按 seq 正序；省略边界返回最新的 limit 条（首屏加载尾部），
   * before_seq 取该边界之前最近一页（「加载更早」），after_seq 向后补读一页。
   */
  messages: (botId: string, query: BotMessagesQuery = {}, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(`${botPath(botId)}/messages`, {
          ...options(signal),
          searchParams: definedParams({
            after_seq: query.after_seq,
            before_seq: query.before_seq,
            limit: query.limit,
          }),
        })
        .json<ApiResponse<{ items: BotMessage[]; calls?: BotCall[] }>>(),
    ),

  /** client_message_id 必填；同键同正文幂等返回，同键不同正文返回冲突。 */
  send: (botId: string, input: SendBotMessageInput) =>
    write(
      api
        .post(`${botPath(botId)}/messages`, { json: input, ...options() })
        .json<ApiResponse<SendBotMessageResult>>(),
    ),

  runs: (botId: string, signal?: AbortSignal) =>
    decodeApiError(
      api.get(`${botPath(botId)}/runs`, options(signal)).json<ApiResponse<{ items: BotRun[] }>>(),
    ),

  stopRun: (botId: string, runId: string) =>
    write(
      api
        .post(`${botPath(botId)}/runs/${encodeURIComponent(runId)}/stop`, options())
        .json<ApiResponse<BotRun>>(),
    ),

  calls: (botId: string, signal?: AbortSignal) =>
    decodeApiError(
      api.get(`${botPath(botId)}/calls`, options(signal)).json<ApiResponse<{ items: BotCall[] }>>(),
    ),

  callResult: (botId: string, callId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(`${callPath(botId, callId)}/result`, options(signal))
        .json<ApiResponse<unknown>>(),
    ),

  /** 只对已保存的调用参数决策；重复相同决定幂等，冲突决定返回 409。 */
  decide: (botId: string, callId: string, decision: BotCallDecision) =>
    write(
      api
        .post(`${callPath(botId, callId)}/decision`, {
          json: { decision },
          ...options(),
        })
        .json<ApiResponse<BotCall>>(),
    ),

  /** 待确认/queued 直接终结；running 先记录取消再取消 context。 */
  cancelCall: (botId: string, callId: string) =>
    write(
      api
        .post(`${callPath(botId, callId)}/cancel`, options())
        .json<ApiResponse<BotCall>>(),
    ),

  authorizations: (botId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(`${botPath(botId)}/authorizations`, options(signal))
        .json<ApiResponse<{ items: BotAuthorization[] }>>(),
    ),

  /** 实际撤销（写 revoked_at），不物理删除。 */
  revokeAuthorization: (botId: string, authorizationId: string) =>
    write(
      api
        .delete(
          `${botPath(botId)}/authorizations/${encodeURIComponent(authorizationId)}`,
          options(),
        )
        .json<ApiResponse<null>>(),
    ),

  /** 分级放行规则清单（tier1 规则开关与误判状态，INT-160）。 */
  policyRules: (botId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(`${botPath(botId)}/policy-rules`, options(signal))
        .json<ApiResponse<{ items: BotWritePolicyRule[] }>>(),
    ),

  /** 开关一条分级放行规则；手动重开会清自动降级痕迹与误判计数。 */
  setPolicyRuleEnabled: (botId: string, ruleId: string, enabled: boolean) =>
    write(
      api
        .put(`${botPath(botId)}/policy-rules/${encodeURIComponent(ruleId)}`, {
          json: { enabled },
          ...options(),
        })
        .json<ApiResponse<BotWritePolicyRule>>(),
    ),

  scheduleTasks: (botId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(`${botPath(botId)}/schedule-tasks`, options(signal))
        .json<ApiResponse<{ items: BotScheduleTask[] }>>(),
    ),

  scheduleTask: (botId: string, taskId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(scheduleTaskPath(botId, taskId), options(signal))
        .json<ApiResponse<BotScheduleTask>>(),
    ),

  scheduleTaskRuns: (
    botId: string,
    taskId: string,
    query: BotScheduleRunsQuery = {},
    signal?: AbortSignal,
  ) =>
    decodeApiError(
      api
        .get(`${scheduleTaskPath(botId, taskId)}/runs`, {
          ...options(signal),
          searchParams: definedParams({
            limit: query.limit,
            offset: query.offset,
          }),
        })
        .json<ApiResponse<{ items: BotScheduleRun[] }>>(),
    ),

  /** 停止一次定时任务运行（INT-137）；成功语义是运行落 stopped。 */
  stopScheduleRun: (botId: string, taskId: string, runId: string) =>
    write(
      api
        .post(
          `${scheduleTaskPath(botId, taskId)}/runs/${encodeURIComponent(runId)}/stop`,
          options(),
        )
        .json<ApiResponse<BotScheduleRun>>(),
    ),

  /**
   * 清空任务的持久小状态；幂等，返回清空后的任务。
   * 与启停一样是用户侧仅有的写操作：任务的增删改由机器人经工具完成。
   */
  clearScheduleTaskState: (botId: string, taskId: string) =>
    write(
      api
        .delete(`${scheduleTaskPath(botId, taskId)}/state`, options())
        .json<ApiResponse<BotScheduleTask>>(),
    ),

  /** 启停任务；enabled 与 last_run/next_run_at 在响应里一并返回。 */
  setScheduleTaskEnabled: (botId: string, taskId: string, enabled: boolean) =>
    write(
      api
        .put(`${scheduleTaskPath(botId, taskId)}/enabled`, {
          json: { enabled },
          ...options(),
        })
        .json<ApiResponse<BotScheduleTask>>(),
    ),

  /** 花费总览（INT-162）：bot_id 缺省为全部机器人；窗口按服务端生效时区切日。 */
  usage: (
    query: { range?: BotUsageRange; botId?: string } = {},
    signal?: AbortSignal,
  ) =>
    decodeApiError(
      api
        .get("bots/usage", {
          ...options(signal),
          searchParams: definedParams({
            range: query.range,
            bot_id: query.botId,
          }),
        })
        .json<ApiResponse<BotUsageOverview>>(),
    ),

  /** 单次运行花费明细：自身每次模型请求 + 关联维护运行。 */
  usageRun: (runId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(`bots/usage/runs/${encodeURIComponent(runId)}`, options(signal))
        .json<ApiResponse<BotUsageRunDetail>>(),
    ),

  /** 花费优化参考（INT-163）：与 usage 同参（range / bot_id），同窗口口径。 */
  usageInsights: (
    query: { range?: BotUsageRange; botId?: string } = {},
    signal?: AbortSignal,
  ) =>
    decodeApiError(
      api
        .get("bots/usage/insights", {
          ...options(signal),
          searchParams: definedParams({
            range: query.range,
            bot_id: query.botId,
          }),
        })
        .json<ApiResponse<BotUsageInsights>>(),
    ),

  /** 机器人周期预算（INT-164）：每日/每月各至多一条，带当前周期已计价花费。 */
  budgets: (botId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(`${botPath(botId)}/budgets`, options(signal))
        .json<ApiResponse<{ items: BotBudget[] }>>(),
    ),

  /** 覆盖某周期的预算；阈值比例由服务端规范化（去重、升序）。 */
  setBudget: (
    botId: string,
    period: BotBudgetPeriod,
    input: BotBudgetUpsertInput,
  ) =>
    write(
      api
        .put(`${botPath(botId)}/budgets/${period}`, { json: input, ...options() })
        .json<ApiResponse<BotBudget>>(),
    ),

  /** 删除某周期的预算；不存在也算成功（幂等）。 */
  deleteBudget: (botId: string, period: BotBudgetPeriod) =>
    write(
      api
        .delete(`${botPath(botId)}/budgets/${period}`, options())
        .json<ApiResponse<null>>(),
    ),

  /** 定时任务周期预算（INT-164）：花费只算任务口径（含连带主线与关联维护）。 */
  taskBudgets: (botId: string, taskId: string, signal?: AbortSignal) =>
    decodeApiError(
      api
        .get(`${scheduleTaskPath(botId, taskId)}/budgets`, options(signal))
        .json<ApiResponse<{ items: BotBudget[] }>>(),
    ),

  setTaskBudget: (
    botId: string,
    taskId: string,
    period: BotBudgetPeriod,
    input: BotBudgetUpsertInput,
  ) =>
    write(
      api
        .put(`${scheduleTaskPath(botId, taskId)}/budgets/${period}`, {
          json: input,
          ...options(),
        })
        .json<ApiResponse<BotBudget>>(),
    ),

  deleteTaskBudget: (botId: string, taskId: string, period: BotBudgetPeriod) =>
    write(
      api
        .delete(`${scheduleTaskPath(botId, taskId)}/budgets/${period}`, options())
        .json<ApiResponse<null>>(),
    ),
};
