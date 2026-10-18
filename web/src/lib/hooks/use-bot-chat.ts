import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  botApi,
  type Bot,
  type BotCall,
  type BotCallDecision,
  type BotCallStatus,
  type BotMessage,
  type BotRun,
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
import { botKeys } from "./use-bots";

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

/** 活动主线 run：数据库保证至多一个未完结 run；列表兜底取最新一条。 */
export function pickActiveRun(runs: BotRun[] | undefined): BotRun | null {
  if (!runs?.length) return null;
  const active = runs.filter(
    (r) => !r.finished_at && (r.status === "queued" || r.status === "running"),
  );
  return active.length > 0 ? active[active.length - 1] : null;
}

/**
 * 后台调用面板内容：异步且未终结的调用（含待确认）。
 * run 完成不清理这里的调用——后台结果可能晚于 run 完成。
 */
export function pickBackgroundCalls(calls: BotCall[]): BotCall[] {
  return calls.filter((c) => c.execution_mode === "async" && !isTerminalCall(c));
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
    next_message_seq: Math.max(
      incoming.next_message_seq ?? 1,
      previous.next_message_seq ?? 1,
    ),
  };
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
        sinks.setMaintenanceProgress(null);
        sinks.setRuns(
          mergeRun(sinks.getRuns(), { id: data.run_id, status: "running" }),
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
      // run_error 载荷字段是 code/message（服务端 finishFailed 的形状）。
      const payload = (data.payload ?? {}) as { code?: string; message?: string };
      sinks.setMaintenanceProgress(null);
      sinks.setRuns(
        mergeRun(sinks.getRuns(), {
          id: data.run_id,
          status: "failed",
          error_code: payload.code,
          error_message: payload.message,
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
  const [busyCallId, setBusyCallId] = useState<string | null>(null);
  const [callActionError, setCallActionError] = useState<unknown>(null);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [olderError, setOlderError] = useState<unknown>(null);
  const [hasMoreHistory, setHasMoreHistory] = useState(true);

  // 发送失败保留同一幂等键重试：同键同正文幂等返回，换键会被判为不同正文。
  const pendingMessageIdRef = useRef<string | null>(null);
  const subscriptionRef = useRef<BotStreamSubscription | null>(null);
  const lastSeenSeqRef = useRef(0);

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

  useEffect(() => {
    if (!userId || !botId || cursor === null) return;
    const snapshotKey = botKeys.snapshot(userId, botId);
    const messagesKey = botKeys.messages(userId, botId);
    const callsKey = botKeys.calls(userId, botId);
    const runsKey = botKeys.runs(userId, botId);

    const appendDraft = (messageId: string, text: string) => {
      setDrafts((previous) => {
        const next = new Map(previous);
        next.set(messageId, (next.get(messageId) ?? "") + text);
        return next;
      });
    };
    const clearDraft = (messageId: string) => {
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
        });
        if (event.event === "done") {
          // 机器人经工具增改定时任务不进聊天事件：主线跑完就刷新任务区，
          // 用户不用手动重载才能看到刚创建/修改的计划。
          void queryClient.invalidateQueries({
            queryKey: botKeys.scheduleTasks(userId, botId),
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
    };
  }, [botId, cursor, queryClient, userId]);

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

  const activeRun = useMemo(
    () => pickActiveRun(runsQuery.data ?? []),
    [runsQuery.data],
  );

  const stopRun = useCallback(async () => {
    if (!activeRun || stopping || !userId) return;
    setStopError(null);
    setStopping(true);
    try {
      // 「停止」等价于向机器人发一条「停止」消息：服务端不再硬取消当前回复，
      // 是否结束由机器人决定。这里不回填 stop_requested，改为重读 run 收敛。
      await botApi.stopRun(botId, activeRun.id);
      await queryClient.invalidateQueries({ queryKey: botKeys.runs(userId, botId) });
    } catch (err) {
      setStopError(err);
    } finally {
      setStopping(false);
    }
  }, [activeRun, botId, queryClient, stopping, userId]);

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
    activeRun,
    runActive: Boolean(activeRun),
    stopRequested: Boolean(activeRun?.stop_requested),
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

export type UseBotChatResult = ReturnType<typeof useBotChat>;
