import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
import {
  botApi,
  type Bot,
  type BotCall,
  type BotCallDecision,
  type BotCallStatus,
  type BotMessage,
  type BotRun,
  type BotRunErrorAction,
  type BotRunStatus,
} from "@/lib/api/bots";
import {
  subscribeBotStream,
  type BotStreamEvent,
  type BotStreamEventData,
  type BotStreamStatus,
  type BotStreamSubscription,
} from "@/lib/bot-stream";
import { useAuthStore } from "@/lib/store/auth-store";
import { uuid } from "@/lib/uuid";
import { BOTS_ROOT, botKeys } from "./use-bots";

/** 主线历史每页条数；「加载更早」沿用同值判断是否还有更早消息。 */
export const BOT_HISTORY_PAGE = 50;

const TERMINAL_CALL_STATUSES: readonly BotCallStatus[] = [
  "succeeded",
  "failed",
  "rejected",
  "cancelled",
  "not_executed",
];

export function isTerminalCall(call: Pick<BotCall, "status">) {
  return TERMINAL_CALL_STATUSES.includes(call.status);
}

/** run 未完结（可停）：queued/running 且尚未写 finished_at。 */
export function isLiveRun(run: BotRun): boolean {
  return !run.finished_at && (run.status === "queued" || run.status === "running");
}

/** 严格活跃 run：进度锚定等只认未完结 run 的场景用；数据库保证至多一个。 */
function pickLiveRun(runs: BotRun[] | undefined): BotRun | null {
  if (!runs?.length) return null;
  const active = runs.filter(isLiveRun);
  return active.length > 0 ? active[active.length - 1] : null;
}

/**
 * 运行栏的 run：优先未完结的活跃 run；没有活跃 run 时取最新一条（按
 * created_at）——run 到终态后运行栏继续展示「本轮」成本与明细链接，直到
 * 新 run 开始或离开页面（INT-162）。
 */
export function pickActiveRun(runs: BotRun[] | undefined): BotRun | null {
  if (!runs?.length) return null;
  return (
    pickLiveRun(runs) ??
    runs.reduce((newest, run) =>
      compareRunCreatedAt(run, newest) >= 0 ? run : newest,
    )
  );
}

/**
 * 后台调用面板内容：异步且未终结的调用（含待确认）。
 * run 完成不清理这里的调用——后台结果可能晚于 run 完成。
 */
export function pickBackgroundCalls(calls: BotCall[]): BotCall[] {
  return calls.filter((c) => c.execution_mode === "async" && !isTerminalCall(c));
}

/** 错误条模型（INT-137）：最近一次失败 run 中用户可直接处理的那类失败。 */
export interface BotRunError {
  code: string;
  message: string;
  action: BotRunErrorAction;
}

/** 错误条只覆盖「用户能直接处理」的失败：认证失效与额度用完。 */
const ACTIONABLE_RUN_ERROR_CODES: ReadonlySet<string> = new Set([
  "provider_auth_failed",
  "provider_quota_exhausted",
]);

const RUN_ERROR_FALLBACKS: Record<string, string> = {
  provider_auth_failed:
    "AI 提供商认证失败：密钥无效或已失效，请到设置页更新密钥",
  provider_quota_exhausted:
    "AI 提供商额度已用完或余额不足，请充值或更换提供商",
};

const AI_PROVIDERS_ACTION: BotRunErrorAction = {
  kind: "ai_providers_settings",
  label: "去设置",
};

/**
 * 取最新一次 run（按 created_at，缓存里快照与 SSE 追加的次序不保证）：只有它
 * 本人才是错误条的候选。错误码可操作（auth/quota）时返回错误条模型，其余
 * （包括后续运行已成功的情况）返回 null——不把历史失败一直挂在聊天页顶上。
 * action 优先用服务端随 run_error 下发的值；快照没有该字段时按 code 推导。
 */
/**
 * 服务端 created_at 是 RFC3339Nano，小数位不固定（`.1Z` / `.11Z` / 无小数），
 * 字符串比较会排错，统一解析成毫秒时间戳再比。
 */
function runCreatedAtKey(run: BotRun): { ms: number; frac: string } | null {
  if (typeof run.created_at !== "string" || run.created_at === "") return null;
  const ms = Date.parse(run.created_at);
  if (Number.isNaN(ms)) return null;
  // Date.parse 只保留毫秒；RFC3339Nano 的小数位可以到纳秒，取出来做同毫秒
  // 的次级比较（.1239 晚于 .1234，字符串比较会判反）。
  const matched = /\.(\d+)/.exec(run.created_at);
  const frac = (matched?.[1] ?? "").padEnd(9, "0").slice(0, 9);
  return { ms, frac };
}

function compareRunCreatedAt(a: BotRun, b: BotRun): number {
  const left = runCreatedAtKey(a);
  const right = runCreatedAtKey(b);
  if (left && right) {
    if (left.ms !== right.ms) return left.ms - right.ms;
    return left.frac.localeCompare(right.frac);
  }
  if (left) return 1;
  if (right) return -1;
  return 0;
}

export function pickRunError(runs: BotRun[] | undefined): BotRunError | null {
  if (!runs?.length) return null;
  const latest = runs.reduce((newest, run) => {
    // 缺时间戳的 run（旧服务端来的 run_started 没有 created_at）只在双方都
    // 缺时按追加次序胜出；一方带时间戳就以时间戳为准——否则永远赢过缓存里
    // 已存在的 run，旧失败会盖住后续成功一直挂在顶上。
    return compareRunCreatedAt(run, newest) >= 0 ? run : newest;
  });
  if (latest.status !== "failed") return null;
  const code = latest.error_code ?? "";
  if (!ACTIONABLE_RUN_ERROR_CODES.has(code)) return null;
  const action =
    latest.error_action && latest.error_action.kind
      ? {
          kind: latest.error_action.kind,
          label: latest.error_action.label || AI_PROVIDERS_ACTION.label,
        }
      : AI_PROVIDERS_ACTION;
  return {
    code,
    message: latest.error_message || RUN_ERROR_FALLBACKS[code] || "模型调用失败",
    action,
  };
}

// ---- 合并纯函数：SSE 事件与 REST 响应折叠进 react-query 缓存 ----

/** 按 id 去重合并消息，页内保持 seq 正序；同 id 新值覆盖旧值。 */
export function mergeMessages(
  previous: BotMessage[],
  incoming: BotMessage[],
): BotMessage[] {
  const byId = new Map(previous.map((m) => [m.id, m]));
  for (const m of incoming) byId.set(m.id, m);
  return [...byId.values()].sort((a, b) => a.seq - b.seq);
}

/** SSE 只携带调用快照的部分字段：与已存记录按字段合并，undefined 不覆盖。 */
export function mergeCall(
  previous: BotCall | undefined,
  patch: Partial<BotCall> & { id: string },
): BotCall {
  const base: BotCall = previous ?? {
    id: patch.id,
    bot_id: "",
    tool_name: "",
    action: "",
    arguments: {},
    execution_mode: "async",
    requires_confirmation: true,
    status: "queued",
    created_at: "",
  };
  const merged: BotCall = { ...base };
  for (const [key, value] of Object.entries(patch)) {
    if (value !== undefined) {
      (merged as unknown as Record<string, unknown>)[key] = value;
    }
  }
  return merged;
}

export function mergeCalls(
  previous: BotCall[],
  patch: Partial<BotCall> & { id: string },
): BotCall[] {
  const index = previous.findIndex((c) => c.id === patch.id);
  if (index === -1) {
    return [...previous, mergeCall(undefined, patch)];
  }
  const next = [...previous];
  next[index] = mergeCall(previous[index], patch);
  return next;
}

export function mergeRun(
  previous: BotRun[],
  patch: Partial<BotRun> & { id: string },
): BotRun[] {
  const index = previous.findIndex((r) => r.id === patch.id);
  if (index === -1) return [...previous, patch as BotRun];
  const next = [...previous];
  const merged: BotRun = { ...previous[index] };
  for (const [key, value] of Object.entries(patch)) {
    if (value !== undefined) {
      (merged as unknown as Record<string, unknown>)[key] = value;
    }
  }
  next[index] = merged;
  return next;
}

// ---- 快照安装合并：防止迟到的旧快照覆盖 SSE 已折叠的新状态 ----

/** 状态推进序：更靠后的状态胜出；同分保留已缓存值。
 *  批准转移是 pending_confirmation → queued，序必须顺着转移方向，
 *  否则权威快照里的 queued 会输给缓存里的旧待确认卡。 */
const CALL_STATUS_RANK: Record<BotCallStatus, number> = {
  pending_confirmation: 0,
  queued: 1,
  running: 2,
  succeeded: 3,
  failed: 3,
  rejected: 3,
  cancelled: 3,
  not_executed: 3,
};

/** run 状态推进序：终态 > running > queued；同分保留已缓存值。 */
const RUN_STATUS_RANK: Record<BotRunStatus, number> = {
  queued: 0,
  running: 1,
  completed: 2,
  failed: 2,
  stopped: 2,
};

export function mergeCallSnapshots(
  previous: BotCall[],
  incoming: BotCall[],
): BotCall[] {
  const merged = new Map(previous.map((c) => [c.id, c]));
  for (const call of incoming) {
    const cached = merged.get(call.id);
    if (!cached || CALL_STATUS_RANK[call.status] > CALL_STATUS_RANK[cached.status]) {
      merged.set(call.id, call);
    } else if (CALL_STATUS_RANK[call.status] === CALL_STATUS_RANK[cached.status]) {
      // 同分也合并：缓存里的终态可能缺 error/result 元数据（只收到过
      // 状态级 tool_result 事件的标签页），权威快照字段更全就补齐。
      merged.set(call.id, { ...cached, ...call });
    }
  }
  return [...merged.values()];
}

export function mergeRunSnapshots(
  previous: BotRun[],
  incoming: BotRun[],
): BotRun[] {
  const merged = new Map(previous.map((r) => [r.id, r]));
  for (const run of incoming) {
    const cached = merged.get(run.id);
    if (
      !cached ||
      RUN_STATUS_RANK[run.status] > RUN_STATUS_RANK[cached.status]
    ) {
      merged.set(run.id, run);
    } else if (RUN_STATUS_RANK[run.status] === RUN_STATUS_RANK[cached.status]) {
      // 同分也合并：快照是权威，error_*/stop_requested 等可变字段以快照为准
      // 补齐（SSE 折叠的记录常缺这些字段）；created_at 只在缓存缺省时回填，
      // SSE run_started 已写入的创建时刻不回拨。
      merged.set(run.id, {
        ...cached,
        ...run,
        created_at: cached.created_at || run.created_at,
        // 停止受理是单调事实：迟到的旧快照可能还带着 stop_requested=false，
        // 不能把已受理的停止态回退（其他标签页/重连场景会丢失「停止中」）。
        stop_requested: Boolean(cached.stop_requested) || Boolean(run.stop_requested),
      });
    }
  }
  return [...merged.values()];
}

/** bot 只做整体替换，水位字段单调取大（旧快照不得回拨订阅游标）。 */
export function mergeBotSnapshot(previous: Bot | undefined, incoming: Bot): Bot {
  if (!previous) return incoming;
  return {
    ...incoming,
    next_event_seq: Math.max(incoming.next_event_seq, previous.next_event_seq ?? 1),
  };
}

/**
 * 预览列与主线同步：消息提交时让机器人列表失效重取，不在客户端就地拼
 * last_message。列表端点是预览的唯一事实来源：重取拿到的就是提交后的值，
 * 客户端不必自己判断新旧，也不存在「列表还没加载时写到哪」的问题——
 * 那些都得靠客户端维护一份影子状态，代价高于一次沿索引命中的列表请求。
 * 唯一要额外处理的是重取时机：先取消在飞请求（见下），否则首载会复用它。
 * 「哪些消息算预览」的资格判断只在服务端：客户端不复制正文规则——
 * Go 的 unicode.IsSpace 与 JS trim 的空白字符集不一致（如 U+FEFF），
 * 复制一份会漏刷新。这里只按角色（user/assistant）筛选。
 */
async function refreshListPreview(
  queryClient: QueryClient,
  userId: string | null,
  message: BotMessage,
) {
  if (message.kind !== "user" && message.kind !== "assistant") return;
  const key = botKeys.list(userId);
  // 先取消在飞的列表请求再失效：Query.fetch 只在已有 data 时才按
  // cancelRefetch 取消当前请求，首次加载（还没有 data）会直接复用旧
  // promise，提交前发出的旧快照照样安装，预览停在旧消息且不会自愈。
  await queryClient.cancelQueries({ queryKey: key });
  await queryClient.invalidateQueries({ queryKey: key });
}

// ---- 主线时间线装配 ----

export interface BotChatEntry {
  message: BotMessage;
  /** true = 流式草稿（全文提交事件尚未到达）。 */
  draft?: boolean;
}

/**
 * 主线时间线：同步/异步回执消息被对应工具卡吸收（卡片状态由调用账本
 * 派生，不重复渲染协议回执），关联不到任何工具调用的回执退化为普通
 * 提示行；后台结果、批准决定与生命周期事件原样展示。流式草稿排末尾。
 */
export function buildChatEntries(
  messages: BotMessage[],
  drafts: ReadonlyMap<string, string>,
): BotChatEntry[] {
  const absorbedCallIds = new Set<string>();
  for (const m of messages) {
    if (m.kind === "assistant") {
      for (const tc of m.content.tool_calls ?? []) {
        absorbedCallIds.add(tc.call_id);
      }
    }
  }
  const entries: BotChatEntry[] = [];
  for (const m of [...messages].sort((a, b) => a.seq - b.seq)) {
    if (
      m.kind === "tool_reply" &&
      m.content.call_id &&
      absorbedCallIds.has(m.content.call_id)
    ) {
      continue;
    }
    entries.push({ message: m });
  }
  for (const [messageId, text] of drafts) {
    entries.push({
      message: {
        id: messageId,
        bot_id: "",
        seq: Number.MAX_SAFE_INTEGER,
        kind: "assistant",
        content: { text },
        created_at: "",
      },
      draft: true,
    });
  }
  return entries;
}

function streamErrorText(detail: unknown): string {
  if (detail instanceof Error && detail.message) return detail.message;
  if (typeof detail === "string" && detail) return detail;
  return "实时连接不可用";
}

/**
 * 维护进度（压缩阶段/失败原因，INT-115）：服务端以 delta +
 * kind=maintenance_progress（payload {phase,message}，无 message_id）推送，
 * 运行栏展示。按 run 归属；run 结束或新 run 开始时清掉，不残留成假运行状态。
 */
export interface BotMaintenanceProgress {
  /** 进度所属 run；服务端无 run 场景（不会出现在主线压缩）为 null。 */
  runId: string | null;
  phase: string;
  message: string;
}

/**
 * 把一条 SSE 事件折叠进缓存与草稿。事件先落库再由服务端发送（重复投递
 * 已在订阅层按 seq 去重），这里的合并都按 id 幂等，重复折叠无副作用。
 */
export function foldStreamEvent(
  event: BotStreamEvent,
  sinks: {
    getMessages: () => BotMessage[];
    setMessages: (messages: BotMessage[]) => void;
    getCalls: () => BotCall[];
    setCalls: (calls: BotCall[]) => void;
    getRuns: () => BotRun[];
    setRuns: (runs: BotRun[]) => void;
    refreshRuns: () => void;
    appendDraft: (messageId: string, text: string) => void;
    clearDraft: (messageId: string) => void;
    /** 维护进度的唯一写入口；null = 清除。旧服务端不发该 kind，天然兼容。 */
    setMaintenanceProgress: (progress: BotMaintenanceProgress | null) => void;
    /** 完整消息提交进账本时刷新左栏预览（让列表重取，不就地拼）。 */
    syncListPreview: (message: BotMessage) => void;
  },
) {
  let data: BotStreamEventData;
  try {
    data = JSON.parse(event.data) as BotStreamEventData;
  } catch {
    // 坏帧忽略；seq 已推进，不影响后续事件。
    return;
  }
  switch (event.event) {
    case "delta": {
      if (data.kind === "message_commit") {
        const message = data.payload as BotMessage | undefined;
        if (message?.id) {
          sinks.setMessages(mergeMessages(sinks.getMessages(), [message]));
          sinks.syncListPreview(message);
        }
        const draftId = data.message_id ?? message?.id;
        if (draftId) sinks.clearDraft(draftId);
      } else if (data.kind === "message_discard") {
        // 模型请求失败/停止打断：草稿等不来 message_commit，直接丢弃。
        if (data.message_id) sinks.clearDraft(data.message_id);
      } else if (data.kind === "user_message") {
        // 完整 user 消息投影：其他标签页不经过自己的 REST 发送也能入时间线。
        const message = data.payload as BotMessage | undefined;
        if (message?.id) {
          sinks.setMessages(mergeMessages(sinks.getMessages(), [message]));
          sinks.syncListPreview(message);
        }
      } else if (data.kind === "schedule_event") {
        // 完整 schedule_event 投影：payload 是消息本体，直接并入时间线，
        // 保证实时折叠与刷新后的账本一致（payload 无 text，不产生草稿）。
        const message = data.payload as BotMessage | undefined;
        if (message?.id) {
          sinks.setMessages(mergeMessages(sinks.getMessages(), [message]));
        }
      } else if (data.kind === "maintenance_progress") {
        // 压缩进度（INT-115）：{phase,message} 直接替换——最新一条即当前
        // 阶段；无 message_id，不得落入下方草稿分支产生空块。
        const payload = (data.payload ?? {}) as { phase?: unknown; message?: unknown };
        if (typeof payload.phase === "string" || typeof payload.message === "string") {
          sinks.setMaintenanceProgress({
            runId: data.run_id ?? null,
            phase: typeof payload.phase === "string" ? payload.phase : "",
            message: typeof payload.message === "string" ? payload.message : "",
          });
        }
      } else if (data.kind === "run_started" && data.run_id) {
        // 运行栏依赖 activeRun：run_started 到达即把 run 折叠进缓存；
        // 新 run 开始意味着上一段维护进度已过时，一并清掉。
        // payload 携带服务端落库时刻 created_at（RFC3339）：写进缓存让
        // 「最新 run」判定按时间比较，不再依赖数组追加次序。
        sinks.setMaintenanceProgress(null);
        const payload = (data.payload ?? {}) as { created_at?: unknown };
        sinks.setRuns(
          mergeRun(sinks.getRuns(), {
            id: data.run_id,
            status: "running",
            created_at:
              typeof payload.created_at === "string"
                ? payload.created_at
                : undefined,
          }),
        );
      } else if (data.message_id) {
        const text = (data.payload as { text?: string } | undefined)?.text;
        if (typeof text === "string" && text.length > 0) {
          sinks.appendDraft(data.message_id, text);
        }
      }
      break;
    }
    case "tool_call":
    case "tool_result": {
      const patch = (data.payload ?? {}) as Partial<BotCall>;
      const id = patch.id ?? data.call_id;
      if (!id) break;
      // 全量列表进 mergeCalls：只传匹配项会把缓存整表替换成一个调用，
      // 第二张确认卡一出现就把第一张挤掉。
      sinks.setCalls(mergeCalls(sinks.getCalls(), { ...patch, id }));
      // 异步终态事件的平台后台结果带完整消息投影：不等快照重读直接入时间线。
      const resultPayload = data.payload as
        | { message?: BotMessage | undefined }
        | undefined;
      if (resultPayload?.message?.id) {
        sinks.setMessages(
          mergeMessages(sinks.getMessages(), [resultPayload.message]),
        );
      }
      break;
    }
    case "error": {
      if (!data.run_id) break;
      // run_error 载荷字段是 code/message（服务端 finishFailed 的形状）；
      // 可操作失败（auth/quota）另带 action 按钮描述。
      const payload = (data.payload ?? {}) as {
        code?: string;
        message?: string;
        action?: BotRun["error_action"];
      };
      sinks.setMaintenanceProgress(null);
      sinks.setRuns(
        mergeRun(sinks.getRuns(), {
          id: data.run_id,
          status: "failed",
          error_code: payload.code,
          error_message: payload.message,
          error_action: payload.action,
        }),
      );
      break;
    }
    case "done": {
      if (!data.run_id) break;
      const payload = (data.payload ?? {}) as Partial<BotRun>;
      const status: BotRunStatus = payload.status ?? "completed";
      sinks.setMaintenanceProgress(null);
      sinks.setRuns(
        mergeRun(sinks.getRuns(), {
          id: data.run_id,
          status,
          finished_at: payload.finished_at ?? new Date().toISOString(),
          error_code: payload.error_code,
          error_message: payload.error_message,
          error_action: payload.error_action,
        }),
      );
      // done 只代表本 run 结束；后台结果仍可能继续到达，调用状态不动。
      sinks.refreshRuns();
      break;
    }
    default:
      break;
  }
}

/**
 * 机器人唯一主线：历史消息、调用账本与 run 状态经 react-query 加载，
 * SSE 事件折叠进同一份缓存；发送、停止、批准/取消是对应的 REST 动作。
 * 断线只重连 GET 流（按 seq 补读），绝不重发消息或批准请求。
 */
export function useBotChat(botId: string) {
  const queryClient = useQueryClient();
  const userId = useAuthStore((state) => state.user?.id ?? null);

  const [drafts, setDrafts] = useState<Map<string, string>>(new Map());
  const [maintenanceProgress, setMaintenanceProgress] =
    useState<BotMaintenanceProgress | null>(null);
  const [streamStatus, setStreamStatus] = useState<BotStreamStatus>("connecting");
  const [streamError, setStreamError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<unknown>(null);
  const [stopping, setStopping] = useState(false);
  const [stopError, setStopError] = useState<unknown>(null);
  // 本次 run 已受理软停止：服务端首次受理只追加「停止」消息，
  // stop_requested 要等宽限兜底才落库；本地记住受理态让按钮立刻切到
  // 「强制停止」。按 run id 匹配——run 结束或换 run 后旧 id 自然失效。
  const [softStopRunId, setSoftStopRunId] = useState<string | null>(null);
  const [busyCallId, setBusyCallId] = useState<string | null>(null);
  const [callActionError, setCallActionError] = useState<unknown>(null);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [olderError, setOlderError] = useState<unknown>(null);
  const [hasMoreHistory, setHasMoreHistory] = useState(true);

  // 发送失败保留同一幂等键重试：同键同正文幂等返回，换键会被判为不同正文。
  const pendingMessageIdRef = useRef<string | null>(null);
  const subscriptionRef = useRef<BotStreamSubscription | null>(null);
  const lastSeenSeqRef = useRef(0);
  // 已触发过指标刷新的 assistant message_id：每个模型请求对应新 id，首个
  // text_delta 刷新一次快照；同一 id 的后续 delta 不重复（H2）。
  const lastDeltaMessageIdRef = useRef<string | null>(null);
  // 纯工具调用请求没有 text_delta，用首个 tool_call（新 call_id）触发同样的
  // 指标刷新，避免指标一直停在上一个请求。
  const lastToolCallIdRef = useRef<string | null>(null);
  // 当前活动主线 run 的 id：进度快照只能回填到同一个 run，避免「旧 run 的快照
  // 晚到」把旧压缩阶段挂到新 run 的运行栏。
  const activeRunIdRef = useRef<string | null>(null);
  // 按帧合并草稿增量（INT-176）：服务端约 100ms 一批 text_delta，突发/重连
  // 补读时多批会挤进同一帧，逐批 setState 意味着一帧内多次整表重渲染。
  // 增量先累积进 pending，排帧后一帧至多 flush 一次；flushFrameRef 存已
  // 排帧的取消句柄（rAF 与 setTimeout 二选一），null 表示当前没有待 flush 帧。
  const pendingDraftsRef = useRef<Map<string, string>>(new Map());
  const flushFrameRef = useRef<(() => void) | null>(null);

  // 单一水合来源：bot 详情、消息、调用与 run 全部来自同一读取点的快照，
  // 消除分离请求的竞态窗口（快照与水位同点，SSE 从水位续读不漏不重）。
  // 快照安装进各自缓存时用合并语义，迟到的旧快照覆盖不掉 SSE 已折叠状态。
  const snapshotQuery = useQuery({
    queryKey: botKeys.snapshot(userId, botId),
    queryFn: async ({ signal }) => {
      const snapshot = (await botApi.getChatSnapshot(botId, signal)).data;
      const messagesKey = botKeys.messages(userId, botId);
      const callsKey = botKeys.calls(userId, botId);
      const runsKey = botKeys.runs(userId, botId);
      queryClient.setQueryData<BotMessage[]>(messagesKey, (previous) =>
        mergeMessages(previous ?? [], snapshot.messages),
      );
      queryClient.setQueryData<BotCall[]>(callsKey, (previous) =>
        mergeCallSnapshots(previous ?? [], snapshot.calls),
      );
      queryClient.setQueryData<BotRun[]>(runsKey, (previous) =>
        mergeRunSnapshots(previous ?? [], snapshot.runs),
      );
      queryClient.setQueryData<Bot>(botKeys.detail(userId, botId), (previous) =>
        mergeBotSnapshot(previous, snapshot.bot),
      );
      // 维护进度水合（INT-115）：进度事件在订阅游标之前不会经 SSE 重放，
      // 刷新/重开聊天后由快照回填。只回填当前活动 run 的进度：快照可能是在
      // 旧 run 期间取得、晚于新 run 的 run_started 才返回，按 run_id 拦下，
      // 不把旧阶段挂到新 run。SSE 后续事件照常覆盖；done/error 清空后服务端
      // 已无活动 run、不再返回该字段。
      setMaintenanceProgress((previous) => {
        const progress = snapshot.maintenance_progress;
        if (previous !== null || !progress) return previous;
        if (activeRunIdRef.current === null) {
          // 本地还没有更新的 run 事件：快照是权威。进度存在意味着服务端有活动
          // run，即使快照的 runs 列表被截断也能用它锚定。只认未完结 run——
          // pickActiveRun 会退回最新终态，不能拿来锚定活动进度。
          activeRunIdRef.current =
            pickLiveRun(snapshot.runs)?.id ?? progress.run_id ?? null;
        } else if (progress.run_id && progress.run_id !== activeRunIdRef.current) {
          // 旧 run 期间的快照晚到：不得把旧阶段挂到当前 run。
          return previous;
        }
        return {
          runId: progress.run_id ?? activeRunIdRef.current ?? null,
          phase: progress.phase,
          message: progress.message,
        };
      });
      return snapshot;
    },
    enabled: Boolean(userId && botId),
    refetchOnMount: "always",
  });

  // 三份主线缓存由快照查询经 setQueryData 水合；这里的 useQuery 只订阅
  // 缓存供渲染响应（enabled 恒为 false，queryFn 不会被调用）。
  const messagesQuery = useQuery({
    queryKey: botKeys.messages(userId, botId),
    queryFn: () => [] as BotMessage[],
    enabled: false,
  });
  const callsQuery = useQuery({
    queryKey: botKeys.calls(userId, botId),
    queryFn: () => [] as BotCall[],
    enabled: false,
  });
  const runsQuery = useQuery({
    queryKey: botKeys.runs(userId, botId),
    queryFn: () => [] as BotRun[],
    enabled: false,
  });

  // 订阅游标：快照的 next_event_seq - 1（快照与水位同读取点，竞态窗口消失）。
  const cursor = snapshotQuery.data
    ? Math.max(0, (snapshotQuery.data.next_event_seq ?? 1) - 1)
    : null;

  // 把 pending 里的草稿增量合并进 drafts 并清空 pending（一次 setState）。
  // 幂等：先取消已排帧（自然触发时句柄已被置空，这里是兑底），pending
  // 为空时直接返回，不产生无谓的 setState。
  const flushDrafts = useCallback(() => {
    if (flushFrameRef.current) {
      flushFrameRef.current();
      flushFrameRef.current = null;
    }
    const pending = pendingDraftsRef.current;
    if (pending.size === 0) return;
    pendingDraftsRef.current = new Map();
    setDrafts((previous) => {
      const next = new Map(previous);
      for (const [id, text] of pending) {
        next.set(id, (next.get(id) ?? "") + text);
      }
      return next;
    });
  }, []);

  useEffect(() => {
    if (!userId || !botId || cursor === null) return;
    const snapshotKey = botKeys.snapshot(userId, botId);
    const messagesKey = botKeys.messages(userId, botId);
    const callsKey = botKeys.calls(userId, botId);
    const runsKey = botKeys.runs(userId, botId);

    // 排帧：一帧内多批增量只 flush 一次。环境没有 rAF（非浏览器）时退回
    // setTimeout，两者都缺失则同步 flush。
    const scheduleDraftFlush = () => {
      if (flushFrameRef.current) return;
      if (typeof requestAnimationFrame === "function") {
        const frame = requestAnimationFrame(() => {
          flushFrameRef.current = null;
          flushDrafts();
        });
        flushFrameRef.current = () => cancelAnimationFrame(frame);
      } else if (typeof setTimeout === "function") {
        const timer = setTimeout(() => {
          flushFrameRef.current = null;
          flushDrafts();
        }, 0);
        flushFrameRef.current = () => clearTimeout(timer);
      } else {
        flushDrafts();
      }
    };

    const appendDraft = (messageId: string, text: string) => {
      const pending = pendingDraftsRef.current;
      pending.set(messageId, (pending.get(messageId) ?? "") + text);
      scheduleDraftFlush();
    };
    const clearDraft = (messageId: string) => {
      // commit 携带的正文已完整入账本、discard 宣告草稿作废：pending 里
      // 该 id 的增量必须先丢弃，否则排帧 flush 晚于事件到达时会把旧草稿
      // 写回「复活」；其余 pending 同步 flush 后再删掉草稿本体。
      pendingDraftsRef.current.delete(messageId);
      flushDrafts();
      setDrafts((previous) => {
        if (!previous.has(messageId)) return previous;
        const next = new Map(previous);
        next.delete(messageId);
        return next;
      });
    };

    const subscription = subscribeBotStream({
      botId,
      // 重连或 StrictMode 重建一律从已见最大 seq 续读，不重放旧事件。
      afterSeq: Math.max(cursor, lastSeenSeqRef.current),
      onEvent: (event) => {
        if (event.id !== null) lastSeenSeqRef.current = event.id;
        foldStreamEvent(event, {
          getMessages: () =>
            queryClient.getQueryData<BotMessage[]>(messagesKey) ?? [],
          setMessages: (messages) =>
            queryClient.setQueryData<BotMessage[]>(messagesKey, messages),
          getCalls: () => queryClient.getQueryData<BotCall[]>(callsKey) ?? [],
          setCalls: (calls) => queryClient.setQueryData<BotCall[]>(callsKey, calls),
          getRuns: () => queryClient.getQueryData<BotRun[]>(runsKey) ?? [],
          setRuns: (runs) => queryClient.setQueryData<BotRun[]>(runsKey, runs),
          // 权威重读走快照端点；安装的合并语义保护已折叠状态。
          refreshRuns: () =>
            void queryClient.invalidateQueries({ queryKey: snapshotKey }),
          appendDraft,
          clearDraft,
          setMaintenanceProgress,
          // 左栏预览与主线同步：让列表重取，服务端是唯一事实来源。
          syncListPreview: (message) =>
            void refreshListPreview(queryClient, userId, message),
        });
        // 新模型请求开始即刷新一次快照（H2）：metrics 只读快照，而服务端对
        // 进行中的请求已返回 status=running + input_tokens_estimate，刷新后
        // 指标随新请求切换，不必等 done。文本请求看新 message_id 的首个
        // text_delta；纯工具调用请求没有正文，看新 call_id 的首个 tool_call。
        // 解析失败忽略，不影响事件折叠。
        try {
          const data = JSON.parse(event.data) as {
            kind?: string;
            message_id?: string;
            call_id?: string;
            run_id?: string;
          };
          // 记住当前活动 run：run_started 置位，done/error 清空（不覆盖更新的 run）。
          if (event.event === "delta" && data.kind === "run_started" && data.run_id) {
            activeRunIdRef.current = data.run_id;
          }
          if (
            (event.event === "done" || event.event === "error") &&
            data.run_id &&
            activeRunIdRef.current === data.run_id
          ) {
            activeRunIdRef.current = null;
          }
          const newTextRequest =
            event.event === "delta" &&
            data.kind === "text_delta" &&
            !!data.message_id &&
            lastDeltaMessageIdRef.current !== data.message_id;
          const newToolOnlyRequest =
            event.event === "tool_call" &&
            !!data.call_id &&
            lastToolCallIdRef.current !== data.call_id;
          if (newTextRequest) lastDeltaMessageIdRef.current = data.message_id!;
          if (newToolOnlyRequest) lastToolCallIdRef.current = data.call_id!;
          if (newTextRequest || newToolOnlyRequest) {
            void queryClient.invalidateQueries({ queryKey: snapshotKey });
          }
        } catch {
          // 坏帧忽略；seq 已推进，不影响后续事件。
        }
        if (event.event === "done") {
          // 机器人经工具增改定时任务不进聊天事件：主线跑完就刷新任务区，
          // 用户不用手动重载才能看到刚创建/修改的计划。
          void queryClient.invalidateQueries({
            queryKey: botKeys.scheduleTasks(userId, botId),
          });
          // run 到终态才落定花费口径（INT-162）：usage 查询没有事件源，
          // 按前缀失效，「花费」标签 keepMounted 的旧数随重取收敛。
          void queryClient.invalidateQueries({
            queryKey: [BOTS_ROOT, userId, "usage"],
          });
        }
      },
      onStatus: (status, detail) => {
        setStreamStatus(status);
        setStreamError(status === "error" ? streamErrorText(detail) : null);
      },
    });
    subscriptionRef.current = subscription;
    return () => {
      subscription.close();
      if (subscriptionRef.current === subscription) subscriptionRef.current = null;
      // 订阅因快照推进 cursor 而重建时，pending 里可能还压着已消费、还没到
      // flush 帧的增量：新订阅从已推进的 seq 续读，这段事件不会重放，必须先
      // 把 pending 写回 drafts（flushDrafts 内部同时取消已排的帧），否则草稿
      // 永久少一段。卸载时这次 setState 是空操作（React 18+ 不再对已卸载组件
      // 告警，也不产生更新），不会造成「卸载后更新」。
      flushDrafts();
    };
  }, [botId, cursor, flushDrafts, queryClient, userId]);

  const applyCallUpdate = useCallback(
    (updated?: BotCall) => {
      if (!userId || !botId) return;
      const callsKey = botKeys.calls(userId, botId);
      if (!updated?.id) {
        void queryClient.invalidateQueries({ queryKey: callsKey });
        return;
      }
      queryClient.setQueryData<BotCall[]>(callsKey, (previous) =>
        mergeCalls(previous ?? [], updated),
      );
    },
    [botId, queryClient, userId],
  );

  const sendMessage = useCallback(
    async (text: string): Promise<boolean> => {
      const trimmed = text.trim();
      if (!trimmed || !userId || sending) return false;
      setSendError(null);
      setSending(true);
      if (!pendingMessageIdRef.current) pendingMessageIdRef.current = uuid();
      try {
        const result = await botApi.send(botId, {
          client_message_id: pendingMessageIdRef.current,
          text: trimmed,
        });
        pendingMessageIdRef.current = null;
        queryClient.setQueryData<BotMessage[]>(
          botKeys.messages(userId, botId),
          (previous) => mergeMessages(previous ?? [], [result.message]),
        );
        // REST 发送路径不碰预览：发送方也会从 SSE 收到自己的 user_message
        // （服务端在同一事务追加该事件，重连按 seq 补读），预览刷新只由
        // 提交点统一触发（取消并重取列表），这里不补第二个写点——模型回得
        // 快时晚到的 REST 响应会用更旧的 user 消息盖掉 assistant 预览。
        if (result.run) {
          queryClient.setQueryData<BotRun[]>(
            botKeys.runs(userId, botId),
            (previous) => mergeRun(previous ?? [], result.run as BotRun),
          );
        }
        return true;
      } catch (err) {
        setSendError(err);
        return false;
      } finally {
        setSending(false);
      }
    },
    [botId, queryClient, sending, userId],
  );

  // 运行栏的 run：最新一条（优先活跃），可能是终态——终态后栏仍展示
  // 「本轮」成本；runActive 只报告它未完结（运行中状态/停止按钮的门槛）。
  const activeRun = useMemo(
    () => pickActiveRun(runsQuery.data ?? []),
    [runsQuery.data],
  );
  const runActive = activeRun != null && isLiveRun(activeRun);
  // 内容区顶部的可操作错误条：最近一次失败 run 的 auth/quota 失败。
  const runError = useMemo(() => pickRunError(runsQuery.data), [runsQuery.data]);

  const stopRun = useCallback(async () => {
    // stopping 期间仍挡重复提交；已受理后再点（stopRequested=true）刻意
    // 放行——服务端对已有停止消息的同 client_message_id 请求会立即硬取消。
    // activeRun 可能是终态（终态后运行栏仍展示本轮成本）：只停未完结 run。
    if (!activeRun || !runActive || stopping || !userId) return;
    setStopError(null);
    setStopping(true);
    try {
      // 「停止」等价于向机器人发一条「停止」消息：服务端不再硬取消当前回复，
      // 是否结束由机器人决定。stop_requested 要等宽限兜底才落库，受理成功后
      // 本地先记下软停止态，让按钮立即进入「强制停止」语义。
      await botApi.stopRun(botId, activeRun.id);
      setSoftStopRunId(activeRun.id);
      await queryClient.invalidateQueries({ queryKey: botKeys.runs(userId, botId) });
    } catch (err) {
      setStopError(err);
    } finally {
      setStopping(false);
    }
  }, [activeRun, runActive, botId, queryClient, stopping, userId]);

  const decide = useCallback(
    async (callId: string, decision: BotCallDecision) => {
      if (busyCallId) return;
      setCallActionError(null);
      setBusyCallId(callId);
      try {
        applyCallUpdate(await botApi.decide(botId, callId, decision));
      } catch (err) {
        setCallActionError(err);
      } finally {
        setBusyCallId(null);
      }
    },
    [applyCallUpdate, botId, busyCallId],
  );

  const cancelCall = useCallback(
    async (callId: string) => {
      if (busyCallId) return;
      setCallActionError(null);
      setBusyCallId(callId);
      try {
        applyCallUpdate(await botApi.cancelCall(botId, callId));
      } catch (err) {
        setCallActionError(err);
      } finally {
        setBusyCallId(null);
      }
    },
    [applyCallUpdate, botId, busyCallId],
  );

  const loadOlder = useCallback(async () => {
    const current = messagesQuery.data ?? [];
    const first = current[0];
    if (!first || loadingOlder || !userId) return;
    setLoadingOlder(true);
    setOlderError(null);
    try {
      const page = await botApi.messages(botId, {
        before_seq: first.seq,
        limit: BOT_HISTORY_PAGE,
      });
      const items = page.data.items ?? [];
      setHasMoreHistory(items.length >= BOT_HISTORY_PAGE);
      queryClient.setQueryData<BotMessage[]>(
        botKeys.messages(userId, botId),
        (previous) => mergeMessages(previous ?? [], items),
      );
      // 翻页响应附带页内消息引用的调用：更早消息的警示卡/人话行
      // 按 call_id 命中 calls 缓存，缺失会让卡片整条消失（INT-157
      // 复审）。并入既有缓存，不覆盖较新状态。
      const pageCalls = page.data.calls ?? [];
      if (pageCalls.length > 0) {
        queryClient.setQueryData<BotCall[]>(
          botKeys.calls(userId, botId),
          (previous) => mergeCallSnapshots(previous ?? [], pageCalls),
        );
      }
    } catch (err) {
      setOlderError(err);
    } finally {
      setLoadingOlder(false);
    }
  }, [botId, loadingOlder, messagesQuery.data, queryClient, userId]);

  const resumeStream = useCallback(() => {
    subscriptionRef.current?.resume();
  }, []);

  /** 首屏加载失败后的手动重试：快照重读并重新水合三份主线缓存。 */
  const retry = useCallback(() => {
    if (!userId || !botId) return;
    void queryClient.invalidateQueries({
      queryKey: botKeys.snapshot(userId, botId),
    });
  }, [botId, queryClient, userId]);

  const entries = useMemo(
    () => buildChatEntries(messagesQuery.data ?? [], drafts),
    [drafts, messagesQuery.data],
  );
  const backgroundCalls = useMemo(
    () => pickBackgroundCalls(callsQuery.data ?? []),
    [callsQuery.data],
  );

  return {
    loading: Boolean(userId) && snapshotQuery.isPending,
    loadError: snapshotQuery.error,
    entries,
    /** 输入框旁指标区的数据源；快照随 run 结束重读时随之更新。 */
    metrics: snapshotQuery.data?.metrics,
    /** 运行栏的维护进度（压缩阶段/失败原因）；run 结束或新 run 开始时为 null。 */
    maintenanceProgress,
    calls: callsQuery.data ?? [],
    /** 运行栏展示的 run（最新一条，优先活跃）；可能已到终态。 */
    activeRun,
    /** 运行栏的 run 未完结：运行中状态、停止按钮与打字动画的门槛。 */
    runActive,
    stopRequested:
      runActive &&
      (Boolean(activeRun?.stop_requested) || softStopRunId === activeRun?.id),
    /** 可操作错误条（auth/quota 失败）；其它失败为 null，视图不渲染。 */
    runError,
    backgroundCalls,
    streamStatus,
    streamError,
    resumeStream,
    retry,
    sending,
    sendError,
    sendMessage,
    stopping,
    stopError,
    stopRun,
    busyCallId,
    callActionError,
    decide,
    cancelCall,
    loadingOlder,
    olderError,
    hasMoreHistory,
    loadOlder,
  };
}
