import { api, decodeApiError } from "./client";

// 内置机器人（INT-101）：类型与端点按设计第 4 节冻结的形状手写，
// 明文与密文形式的 API Key 任何响应都不回传，仅返回末四位。

export interface Bot {
  id: string;
  name: string;
  persona_prompt: string;
  base_url: string;
  model: string;
  /** 已配置密钥的末四位；空串表示尚未配置。 */
  api_key_last4: string;
  /** 消息/事件账本水位（下一条的 seq）；订阅游标取 next_event_seq - 1。 */
  next_message_seq?: number;
  next_event_seq: number;
  created_at: string;
  updated_at: string;
}

export interface CreateBotInput {
  name: string;
  persona_prompt: string;
  base_url: string;
  model: string;
  api_key: string;
}

/** 更新为指针语义：出现才更新；api_key 省略保留已存值，空串会被服务端拒绝。 */
export interface UpdateBotInput {
  name?: string;
  persona_prompt?: string;
  base_url?: string;
  model?: string;
  api_key?: string;
}

export interface BotConnectionCheck {
  name: string;
  passed: boolean;
  /** 失败原因（截断；不含密钥），通过时服务端省略。 */
  detail?: string;
}

export interface BotConnectionTestResult {
  ok: boolean;
  /** 流式、工具调用兼容性等逐项检查结果。 */
  checks: BotConnectionCheck[];
}

/** 可传未保存配置直接测试；或传 bot_id 复用已存 key，表单字段可覆盖地址/模型。 */
export interface BotConnectionTestInput {
  bot_id?: string;
  base_url?: string;
  model?: string;
  api_key?: string;
}

export type BotMessageKind =
  | "user"
  | "assistant"
  | "tool_reply"
  | "background_result"
  | "approval_decision"
  | "lifecycle";

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
 * 是 call_id + decision；lifecycle 是 event + 任意 detail。字段可空是因为
 * 具体形状取决于 kind。
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
  decision?: string;
  event?: string;
  detail?: unknown;
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

export type BotRunTrigger = "user_message" | "tool_result" | "recovery";

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
  error_code?: string | null;
  error_message?: string | null;
  created_at: string;
  started_at?: string | null;
  finished_at?: string | null;
}

export interface BotAuthorization {
  id: string;
  tool_name: string;
  action: string;
  scope_kind: string;
  scope_id: string;
  environment_id?: string | null;
  /** 规范化约束（写入字段集合等），键为字段名。 */
  constraints?: Record<string, unknown>;
  created_at: string;
  /** 撤销只写 revoked_at，记录保留用于审计。 */
  revoked_at?: string | null;
}

/**
 * 聊天页一致快照：bot、消息尾部页、调用、run 与事件水位取自服务端
 * 同一读取点。订阅游标取 next_event_seq - 1，与快照内容严格衔接，
 * 既不重放快照已含的事件也不漏掉其后的事件。
 */
export interface ChatSnapshot {
  bot: Bot;
  /** 消息最新一页（默认 50 条），seq 正序。 */
  messages: BotMessage[];
  calls: BotCall[];
  runs: BotRun[];
  /** 与快照同一读取点的事件账本水位（下一条的 seq）。 */
  next_event_seq: number;
}

export interface SendBotMessageInput {
  client_message_id: string;
  text: string;
}

export interface SendBotMessageResult {
  message: BotMessage;
  /** 受理时已存在的 run；没有则由服务端随后启动。 */
  run: BotRun | null;
  /** true = 消息排队等待现有 run 在安全点处理，而不是立即触发新 run。 */
  queued: boolean;
}

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

/** searchParams 只收已定义字段，避免 undefined 被序列化成字符串。 */
function definedParams(params: BotMessagesQuery) {
  const searchParams: Record<string, string> = {};
  if (params.after_seq !== undefined) searchParams.after_seq = String(params.after_seq);
  if (params.before_seq !== undefined) searchParams.before_seq = String(params.before_seq);
  if (params.limit !== undefined) searchParams.limit = String(params.limit);
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
          searchParams: definedParams(query),
        })
        .json<ApiResponse<{ items: BotMessage[] }>>(),
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
};
