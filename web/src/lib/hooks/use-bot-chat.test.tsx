import type { ReactNode } from "react";
import { StrictMode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useBotChat, foldStreamEvent } from "./use-bot-chat";
import { botKeys } from "./use-bots";
import type { BotCall, BotMessage, BotRun } from "@/lib/api/bots";
import { signInAs, teardownAuth } from "@/test/auth";

const api = vi.hoisted(() => ({
  getChatSnapshot: vi.fn(),
  messages: vi.fn(),
  send: vi.fn(),
  stopRun: vi.fn(),
  decide: vi.fn(),
  cancelCall: vi.fn(),
}));

vi.mock("@/lib/api/bots", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/bots")>()),
  botApi: api,
}));

const stream = vi.hoisted(() => ({
  subscribe: vi.fn(),
  close: vi.fn(),
  resume: vi.fn(),
  captured: null as null | {
    afterSeq: number;
    onEvent: (event: { event: string; data: string; id: number | null }) => void;
    onStatus: (status: string, detail?: unknown) => void;
  },
}));

vi.mock("@/lib/bot-stream", () => ({
  subscribeBotStream: (options: never) => {
    stream.captured = options as (typeof stream)["captured"];
    stream.subscribe(options);
    return { close: stream.close, resume: stream.resume, lastSeq: () => 0 };
  },
}));

const bot = {
  id: "b1",
  name: "盯板机器人",
  persona_prompt: "",
  base_url: "https://api.example.com/v1",
  model: "gpt-test",
  api_key_last4: "1234",
  next_message_seq: 5,
  next_event_seq: 10,
  created_at: "2026-10-15T00:00:00Z",
  updated_at: "2026-10-15T00:00:00Z",
};

const committedMessage: BotMessage = {
  id: "m-a",
  bot_id: "b1",
  seq: 11,
  kind: "assistant",
  content: { text: "已收到，我来处理。" },
  created_at: "2026-10-15T00:01:00Z",
};

// 快照基础形状：与 GET /api/bots/{id}/chat 响应 data 一致，按用例覆盖。
const snapshotBase = {
  bot,
  messages: [] as BotMessage[],
  calls: [] as BotCall[],
  runs: [] as BotRun[],
  next_event_seq: 10,
};

function snapshotWith(overrides: Partial<typeof snapshotBase> = {}) {
  return { data: { ...snapshotBase, ...overrides } };
}

const activeRun: BotRun = {
  id: "r1",
  bot_id: "b1",
  trigger_source: "user_message",
  status: "running",
  stop_requested: false,
  created_at: "2026-10-15T00:00:30Z",
};

const queuedCall: BotCall = {
  id: "c1",
  bot_id: "b1",
  tool_name: "fs_issue",
  action: "update",
  arguments: {},
  execution_mode: "async",
  requires_confirmation: true,
  status: "queued",
  created_at: "2026-10-15T00:00:40Z",
};

function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, wrapper };
}

function emit(event: string, data: unknown, id: number) {
  act(() => {
    stream.captured?.onEvent({ event, data: JSON.stringify(data), id });
  });
}

function hook() {
  const { client, wrapper } = setup();
  const render = renderHook(() => useBotChat("b1"), { wrapper });
  return { client, result: render.result, rerender: render.rerender, unmount: render.unmount };
}

beforeEach(() => {
  signInAs("A");
  api.getChatSnapshot.mockResolvedValue(snapshotWith());
  api.messages.mockResolvedValue({ data: { items: [] } });
});

afterEach(() => {
  teardownAuth();
  vi.clearAllMocks();
});

describe("useBotChat", () => {
  it("snapshot 安装后订阅从水位续读", async () => {
    api.getChatSnapshot.mockResolvedValue(snapshotWith({ next_event_seq: 42 }));
    const { result } = hook();

    await waitFor(() => expect(result.current.loading).toBe(false));
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalled());
    // 订阅游标 = 快照 next_event_seq - 1（同一读取点，既不漏也不重放）。
    expect(stream.captured?.afterSeq).toBe(41);
    expect(result.current.entries).toEqual([]);
    expect(result.current.activeRun).toBeNull();
  });

  it("旧快照不覆盖 SSE 已合并的调用状态", async () => {
    // 首载快照无 c1；随后到达的快照响应模拟 in-flight 旧数据（c1 仍 queued）。
    api.getChatSnapshot
      .mockResolvedValueOnce(snapshotWith())
      .mockResolvedValueOnce(snapshotWith({ calls: [queuedCall] }));
    const { client, result } = hook();
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalled());

    emit(
      "tool_result",
      { call_id: "c1", phase: "terminal", payload: { id: "c1", status: "succeeded" } },
      20,
    );
    await waitFor(() => expect(result.current.calls[0]?.status).toBe("succeeded"));

    await act(() =>
      client.refetchQueries({ queryKey: botKeys.snapshot("A", "b1") }),
    );
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.calls[0]?.status).toBe("succeeded");
  });

  it("run_started 到达即折叠出活动 run（运行栏实时出现）", async () => {
    const { result } = hook();
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalled());

    emit("delta", { kind: "run_started", run_id: "r-1", payload: { trigger: "user_message" } }, 10);
    await waitFor(() => expect(result.current.activeRun).not.toBeNull());
    expect(result.current.activeRun?.id).toBe("r-1");
    expect(result.current.activeRun?.status).toBe("running");
  });

  it("accumulates delta drafts and replaces them on message_commit", async () => {
    const { result } = hook();
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalled());

    emit("delta", { message_id: "m-a", payload: { text: "你好" } }, 10);
    emit("delta", { message_id: "m-a", payload: { text: "，正在处理" } }, 11);
    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    expect(result.current.entries[0].draft).toBe(true);
    expect(result.current.entries[0].message.content.text).toBe("你好，正在处理");

    emit(
      "delta",
      { kind: "message_commit", message_id: "m-a", payload: committedMessage },
      12,
    );
    await waitFor(() =>
      expect(result.current.entries[0].message.id).toBe("m-a"),
    );
    expect(result.current.entries).toHaveLength(1);
    expect(result.current.entries[0].draft).toBeUndefined();
    expect(result.current.entries[0].message.content.text).toBe("已收到，我来处理。");
  });

  it("folds tool events into the call ledger and keeps receipts out of the timeline", async () => {
    api.getChatSnapshot.mockResolvedValue(
      snapshotWith({
        messages: [
          {
            id: "m-1",
            bot_id: "b1",
            seq: 10,
            kind: "assistant",
            content: {
              text: "",
              tool_calls: [
                {
                  call_id: "c1",
                  provider_tool_call_id: "call-1",
                  tool_name: "fs_issue",
                  action: "update",
                  arguments: "{}",
                },
              ],
            },
            created_at: "2026-10-15T00:00:40Z",
          },
          {
            id: "m-2",
            bot_id: "b1",
            seq: 11,
            kind: "tool_reply",
            content: {
              call_id: "c1",
              provider_tool_call_id: "call-1",
              tool_name: "fs_issue",
              status: "succeeded",
              content: "已更新",
            },
            created_at: "2026-10-15T00:00:41Z",
          },
          {
            id: "m-3",
            bot_id: "b1",
            seq: 12,
            kind: "tool_reply",
            content: {
              call_id: "c404",
              provider_tool_call_id: "call-404",
              tool_name: "fs_issue",
              status: "succeeded",
              content: "孤儿回执",
            },
            created_at: "2026-10-15T00:00:42Z",
          },
        ],
      }),
    );
    const { result } = hook();
    await waitFor(() => expect(result.current.loading).toBe(false));

    // 回执被工具卡吸收；关联不到调用的回执保留为提示行。
    expect(
      result.current.entries.map((e) => e.message.id),
    ).toEqual(["m-1", "m-3"]);

    emit(
      "tool_call",
      {
        call_id: "c1",
        phase: "awaiting_confirmation",
        payload: {
          id: "c1",
          tool_name: "fs_issue",
          action: "update",
          execution_mode: "async",
          status: "pending_confirmation",
          requires_confirmation: true,
        },
      },
      20,
    );
    await waitFor(() =>
      expect(result.current.calls[0]?.status).toBe("pending_confirmation"),
    );
    await waitFor(() =>
      expect(result.current.backgroundCalls.map((c) => c.id)).toEqual(["c1"]),
    );

    emit(
      "tool_result",
      { call_id: "c1", phase: "terminal", payload: { id: "c1", status: "succeeded" } },
      21,
    );
    await waitFor(() =>
      expect(result.current.calls[0]?.status).toBe("succeeded"),
    );
    await waitFor(() => expect(result.current.backgroundCalls).toEqual([]));
  });

  it("keeps earlier call cards when a second call streams in", async () => {
    const { result } = hook();
    await waitFor(() => expect(result.current.loading).toBe(false));

    emit(
      "tool_call",
      {
        call_id: "c1",
        phase: "awaiting_confirmation",
        payload: {
          id: "c1",
          tool_name: "fs_issue",
          action: "update",
          execution_mode: "async",
          status: "pending_confirmation",
          requires_confirmation: true,
        },
      },
      20,
    );
    emit(
      "tool_call",
      {
        call_id: "c2",
        phase: "awaiting_confirmation",
        payload: {
          id: "c2",
          tool_name: "fs_issue",
          action: "comment",
          execution_mode: "async",
          status: "pending_confirmation",
          requires_confirmation: true,
        },
      },
      21,
    );

    // 第二张确认卡到达不得把第一张挤掉，否则第一张无法批准/取消。
    await waitFor(() =>
      expect(result.current.calls.map((c) => c.id)).toEqual(["c1", "c2"]),
    );

    emit(
      "tool_result",
      { call_id: "c1", phase: "terminal", payload: { id: "c1", status: "succeeded" } },
      22,
    );
    await waitFor(() =>
      expect(result.current.calls.map((c) => `${c.id}:${c.status}`)).toEqual([
        "c1:succeeded",
        "c2:pending_confirmation",
      ]),
    );
  });

  it("keeps background calls visible after the run finishes", async () => {
    // 首载给活动 run；done 触发的快照重读返回完成态（服务端权威记录）。
    const completedRun: BotRun = {
      ...activeRun,
      status: "completed",
      finished_at: "2026-10-15T00:05:00Z",
    };
    const backgroundCall: BotCall = {
      ...queuedCall,
      tool_name: "fs_issue",
      action: "comment",
      requires_confirmation: false,
    };
    api.getChatSnapshot
      .mockResolvedValueOnce(snapshotWith({ runs: [activeRun], calls: [backgroundCall] }))
      .mockResolvedValue(snapshotWith({ runs: [completedRun], calls: [backgroundCall] }));
    const { result } = hook();
    await waitFor(() => expect(result.current.loading).toBe(false));
    await waitFor(() => expect(result.current.runActive).toBe(true));
    await waitFor(() => expect(result.current.backgroundCalls).toHaveLength(1));

    emit("done", { run_id: "r1", payload: { status: "completed" } }, 30);
    await waitFor(() => expect(result.current.activeRun).toBeNull());
    // run 完成不隐藏后台调用状态。
    expect(result.current.backgroundCalls).toHaveLength(1);
  });

  it("sends with an idempotency key and reuses it across retries", async () => {
    const { result } = hook();
    await waitFor(() => expect(result.current.loading).toBe(false));

    api.send.mockRejectedValueOnce(new Error("网络错误"));
    const failed = await act(() => result.current.sendMessage("帮我看看问题"));
    expect(failed).toBe(false);
    expect(result.current.sendError).toBeInstanceOf(Error);

    api.send.mockResolvedValueOnce({
      message: {
        id: "m-u",
        bot_id: "b1",
        seq: 20,
        kind: "user",
        content: { text: "帮我看看问题" },
        created_at: "2026-10-15T00:02:00Z",
      },
      run: null,
      queued: false,
    });
    const ok = await act(() => result.current.sendMessage("帮我看看问题"));
    expect(ok).toBe(true);

    expect(api.send).toHaveBeenCalledTimes(2);
    const first = api.send.mock.calls[0][1].client_message_id;
    const second = api.send.mock.calls[1][1].client_message_id;
    expect(first).toBe(second);
    // 成功后消息进入主线；幂等键清空，下一条消息换新键。
    await waitFor(() =>
      expect(result.current.entries.map((e) => e.message.id)).toContain("m-u"),
    );
    api.send.mockResolvedValueOnce({
      message: { ...committedMessage, seq: 21 },
      run: null,
      queued: false,
    });
    await act(() => result.current.sendMessage("第二条"));
    expect(api.send.mock.calls[2][1].client_message_id).not.toBe(first);
  });

  it("stops the main run without touching background calls", async () => {
    api.getChatSnapshot.mockResolvedValue(snapshotWith({ runs: [activeRun] }));
    const { result } = hook();
    await waitFor(() => expect(result.current.loading).toBe(false));

    api.stopRun.mockResolvedValue({
      ...activeRun,
      status: "running",
      stop_requested: true,
    });
    await act(() => result.current.stopRun());
    expect(api.stopRun).toHaveBeenCalledWith("b1", "r1");
    await waitFor(() => expect(result.current.stopRequested).toBe(true));
  });

  it("decides and cancels calls through the ledger endpoints", async () => {
    const pending: BotCall = {
      id: "c1",
      bot_id: "b1",
      tool_name: "fs_project",
      action: "update",
      arguments: { project_id: "p1" },
      execution_mode: "async",
      requires_confirmation: true,
      status: "pending_confirmation",
      created_at: "2026-10-15T00:00:40Z",
    };
    api.getChatSnapshot.mockResolvedValue(snapshotWith({ calls: [pending] }));
    const { result } = hook();
    await waitFor(() => expect(result.current.loading).toBe(false));

    api.decide.mockResolvedValue({ ...pending, status: "queued", decision: "approve" });
    await act(() => result.current.decide("c1", "approve"));
    expect(api.decide).toHaveBeenCalledWith("b1", "c1", "approve");
    await waitFor(() =>
      expect(result.current.calls[0]?.status).toBe("queued"),
    );

    api.cancelCall.mockResolvedValue({ ...pending, status: "cancelled" });
    await act(() => result.current.cancelCall("c1"));
    await waitFor(() =>
      expect(result.current.calls[0]?.status).toBe("cancelled"),
    );
    await waitFor(() => expect(result.current.backgroundCalls).toEqual([]));
  });

  it("surfaces stream errors and resumes manually", async () => {
    const { result } = hook();
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalled());

    act(() => {
      stream.captured?.onStatus("error", new Error("连接中断"));
    });
    expect(result.current.streamStatus).toBe("error");
    expect(result.current.streamError).toBe("连接中断");

    await act(() => result.current.resumeStream());
    expect(stream.resume).toHaveBeenCalled();
  });

  it("resubscribes from the last seen seq instead of replaying", async () => {
    const { client } = hook();
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalledTimes(1));
    emit("delta", { message_id: "d1", payload: { text: "A" } }, 10);

    // 快照重读后水位前进，订阅重建：从 max(游标, 已见 seq) 续读，不重放。
    api.getChatSnapshot.mockResolvedValue(snapshotWith({ next_event_seq: 12 }));
    await act(() =>
      client.refetchQueries({ queryKey: botKeys.snapshot("A", "b1") }),
    );
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalledTimes(2));
    expect(stream.close).toHaveBeenCalledTimes(1);
    expect(stream.captured?.afterSeq).toBe(11);
  });

  it("StrictMode 双挂载不产生重复消息", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const { result } = renderHook(() => useBotChat("b1"), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <StrictMode>
          <QueryClientProvider client={client}>{children}</QueryClientProvider>
        </StrictMode>
      ),
    });
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalled());
    const subscribeCalls = stream.subscribe.mock.calls.length;
    emit("delta", { message_id: "d1", payload: { text: "草稿" } }, 10);
    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    // 双挂载各自建立了订阅，随后第一份被 close 清理。
    expect(stream.close).toHaveBeenCalledTimes(subscribeCalls - 1);
  });
});

describe("foldStreamEvent", () => {
  const sinks = () => {
    const state = {
      messages: [] as BotMessage[],
      calls: [] as BotCall[],
      runs: [] as BotRun[],
      drafts: new Map<string, string>(),
    };
    return {
      state,
      sinks: {
        getMessages: () => state.messages,
        setMessages: (m: BotMessage[]) => {
          state.messages = m;
        },
        getCalls: () => state.calls,
        setCalls: (calls: BotCall[]) => {
          state.calls = calls;
        },
        getRuns: () => state.runs,
        setRuns: (runs: BotRun[]) => {
          state.runs = runs;
        },
        refreshRuns: () => undefined,
        appendDraft: (id: string, text: string) => {
          state.drafts.set(id, (state.drafts.get(id) ?? "") + text);
        },
        clearDraft: (id: string) => {
          state.drafts.delete(id);
        },
      },
    };
  };

  it("重复折叠同一条提交不产生重复消息", () => {
    const { state, sinks: sinksObj } = sinks();
    const event = {
      event: "delta",
      data: JSON.stringify({
        kind: "message_commit",
        message_id: "m-a",
        payload: committedMessage,
      }),
      id: 5,
    };
    foldStreamEvent(event, sinksObj);
    foldStreamEvent(event, sinksObj);
    expect(state.messages).toHaveLength(1);
    expect(state.drafts.size).toBe(0);
  });

  it("坏帧被忽略且不影响后续事件", () => {
    const { state, sinks: sinksObj } = sinks();
    foldStreamEvent({ event: "delta", data: "{broken", id: 6 }, sinksObj);
    foldStreamEvent(
      { event: "delta", data: JSON.stringify({ text: "hi" }), id: 7 },
      sinksObj,
    );
    expect(state.messages).toEqual([]);
  });
});
