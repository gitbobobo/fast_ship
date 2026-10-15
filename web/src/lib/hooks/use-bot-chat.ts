import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  botApi,
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
import { botKeys, useBot } from "./use-bots";

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
      } else if (data.kind === "run_started" && data.run_id) {
        // 运行栏依赖 activeRun：run_started 到达即把 run 折叠进缓存。
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
      const previous = sinks.getCalls().find((c) => c.id === id);
      sinks.setCalls(mergeCalls(previous ? [previous] : [], { ...patch, id }));
      break;
    }
    case "error": {
      if (!data.run_id) break;
      const payload = (data.payload ?? {}) as Partial<BotRun>;
      sinks.setRuns(
        mergeRun(sinks.getRuns(), {
          id: data.run_id,
          status: "failed",
          error_code: payload.error_code,
          error_message: payload.error_message,
        }),
      );
      break;
    }
    case "done": {
      if (!data.run_id) break;
      const payload = (data.payload ?? {}) as Partial<BotRun>;
      const status: BotRunStatus = payload.status ?? "completed";
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

  const botQuery = useBot(botId);
  const messagesQuery = useQuery({
    queryKey: botKeys.messages(userId, botId),
    queryFn: async ({ signal }) =>
      (await botApi.messages(botId, { limit: BOT_HISTORY_PAGE }, signal)).data.items,
    enabled: Boolean(userId && botId),
    refetchOnMount: "always",
  });
  const callsQuery = useQuery({
    queryKey: botKeys.calls(userId, botId),
    queryFn: async ({ signal }) => (await botApi.calls(botId, signal)).data.items,
    enabled: Boolean(userId && botId),
    refetchOnMount: "always",
  });
  const runsQuery = useQuery({
    queryKey: botKeys.runs(userId, botId),
    queryFn: async ({ signal }) => (await botApi.runs(botId, signal)).data.items,
    enabled: Boolean(userId && botId),
    refetchOnMount: "always",
  });

  // 订阅游标：详情的 next_event_seq - 1（事件先落库再发送，全量补读也幂等）。
  const cursor = botQuery.data
    ? Math.max(0, (botQuery.data.next_event_seq ?? 1) - 1)
    : null;

  useEffect(() => {
    if (!userId || !botId || cursor === null) return;
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
          refreshRuns: () =>
            void queryClient.invalidateQueries({ queryKey: runsKey }),
          appendDraft,
          clearDraft,
        });
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
    async (text: string) => {
      const trimmed = text.trim();
      if (!trimmed || !userId || sending) return;
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
      } catch (err) {
        setSendError(err);
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
      const updated = await botApi.stopRun(botId, activeRun.id);
      queryClient.setQueryData<BotRun[]>(
        botKeys.runs(userId, botId),
        (previous) =>
          mergeRun(previous ?? [], {
            id: updated.id || activeRun.id,
            // 受理成功即置位，UI 立即进入「停止中」；权威状态经 runs 重读收敛。
            stop_requested: true,
          }),
      );
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

  /** 首屏加载失败后的手动重试：让三份主线数据重新读取。 */
  const retry = useCallback(() => {
    if (!userId || !botId) return;
    void queryClient.invalidateQueries({
      queryKey: botKeys.messages(userId, botId),
    });
    void queryClient.invalidateQueries({
      queryKey: botKeys.calls(userId, botId),
    });
    void queryClient.invalidateQueries({
      queryKey: botKeys.runs(userId, botId),
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
    loading:
      Boolean(userId) &&
      (messagesQuery.isPending || callsQuery.isPending || runsQuery.isPending),
    loadError: messagesQuery.error ?? callsQuery.error ?? runsQuery.error,
    entries,
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
