import type { ReactNode } from "react";
import { StrictMode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useBotChat, foldStreamEvent, mergeCallSnapshots } from "./use-bot-chat";
import { botKeys } from "./use-bots";
import type { BotCall, BotMessage, BotRun, ChatSnapshot } from "@/lib/api/bots";
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
const snapshotBase: ChatSnapshot = {
  bot,
  messages: [] as BotMessage[],
  calls: [] as BotCall[],
  runs: [] as BotRun[],
  next_event_seq: 10,
};

function snapshotWith(overrides: Partial<ChatSnapshot> = {}) {
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

  it("维护进度事件出现在 hook 返回值，done 后消失", async () => {
    const { result } = hook();
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalled());

    emit(
      "delta",
      {
        kind: "maintenance_progress",
        run_id: "r-1",
        payload: { phase: "summary", message: "正在生成上下文摘要（第 3 批）…" },
      },
      10,
    );
    await waitFor(() => expect(result.current.maintenanceProgress).not.toBeNull());
    expect(result.current.maintenanceProgress).toMatchObject({
      runId: "r-1",
      phase: "summary",
      message: "正在生成上下文摘要（第 3 批）…",
    });

    emit("done", { run_id: "r-1", payload: { status: "completed" } }, 11);
    await waitFor(() => expect(result.current.maintenanceProgress).toBeNull());
  });

  // ---- 快照水合：进度事件在快照游标之前不重放，刷新后由快照回填 ----

  it("快照携带 maintenance_progress 时水合进 hook 返回值", async () => {
    api.getChatSnapshot.mockResolvedValue(
      snapshotWith({
        maintenance_progress: {
          run_id: "r-1",
          phase: "archive",
          message: "正在归档上下文…",
        },
      }),
    );
    const { result } = hook();
    await waitFor(() => expect(result.current.loading).toBe(false));
    await waitFor(() => expect(result.current.maintenanceProgress).not.toBeNull());
    expect(result.current.maintenanceProgress).toEqual({
      runId: "r-1",
      phase: "archive",
      message: "正在归档上下文…",
    });
  });

  it("快照无维护进度时维持 null", async () => {
    const { result } = hook();
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.maintenanceProgress).toBeNull();
  });

  it("SSE 进度优先于快照旧值：迟到快照不把进度拨回去", async () => {
    api.getChatSnapshot.mockResolvedValue(
      snapshotWith({
        maintenance_progress: {
          run_id: "r-1",
          phase: "archive",
          message: "正在归档上下文…",
        },
      }),
    );
    const { client, result } = hook();
    await waitFor(() =>
      expect(result.current.maintenanceProgress?.phase).toBe("archive"),
    );

    emit(
      "delta",
      {
        kind: "maintenance_progress",
        run_id: "r-1",
        payload: { phase: "summary", message: "正在生成上下文摘要（第 2 批）…" },
      },
      10,
    );
    await waitFor(() =>
      expect(result.current.maintenanceProgress?.phase).toBe("summary"),
    );

    // in-flight 旧快照（仍带 archive）重读安装后，本地已折叠的新阶段胜出。
    await act(() =>
      client.refetchQueries({ queryKey: botKeys.snapshot("A", "b1") }),
    );
    expect(result.current.maintenanceProgress?.phase).toBe("summary");
  });

  it("done 清空后快照重读（无活动 run、不带进度）不写回", async () => {
    api.getChatSnapshot
      .mockResolvedValueOnce(
        snapshotWith({
          runs: [activeRun],
          maintenance_progress: {
            run_id: "r1",
            phase: "summary",
            message: "正在生成上下文摘要（第 1 批）…",
          },
        }),
      )
      .mockResolvedValue(
        snapshotWith({
          runs: [
            { ...activeRun, status: "completed", finished_at: "2026-10-15T00:05:00Z" },
          ],
        }),
      );
    const { client, result } = hook();
    await waitFor(() =>
      expect(result.current.maintenanceProgress?.phase).toBe("summary"),
    );

    emit("done", { run_id: "r1", payload: { status: "completed" } }, 30);
    await waitFor(() => expect(result.current.maintenanceProgress).toBeNull());

    await act(() =>
      client.refetchQueries({ queryKey: botKeys.snapshot("A", "b1") }),
    );
    expect(result.current.maintenanceProgress).toBeNull();
  });

  it("message_discard 到达即丢弃挂出的草稿", async () => {
    const { result } = hook();
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalled());
    emit("delta", { message_id: "m-d", payload: { text: "写到一半" } }, 10);
    await waitFor(() => expect(result.current.entries).toHaveLength(1));
    emit("delta", { kind: "message_discard", message_id: "m-d" }, 11);
    await waitFor(() => expect(result.current.entries).toHaveLength(0));
  });
  it("accumulates delta drafts and replaces them on message_commit", async () => {    const { result } = hook();
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

  it("sends a stop request without touching background calls", async () => {
    api.getChatSnapshot.mockResolvedValue(snapshotWith({ runs: [activeRun] }));
    const { result } = hook();
    await waitFor(() => expect(result.current.loading).toBe(false));

    api.stopRun.mockResolvedValue({ ...activeRun, status: "running" });
    await act(() => result.current.stopRun());
    expect(api.stopRun).toHaveBeenCalledWith("b1", "r1");
    // 停止等价于一条用户消息，服务端不再硬取消：本地不自造 stop_requested，
    // 保持服务器权威值。
    expect(result.current.stopRequested).toBe(false);
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

  // ---- H2：指标随新模型请求刷新 ----

  it("新 message_id 的首个 text_delta 触发一次快照重读，同一 id 不重复", async () => {
    hook();
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalled());
    const before = api.getChatSnapshot.mock.calls.length;

    // 首个 text_delta 到达即重读快照：服务端对进行中的请求已返回
    // running + input_tokens_estimate，指标随新请求切换。
    emit("delta", { kind: "text_delta", message_id: "m-x", payload: { text: "第一段" } }, 10);
    await waitFor(() =>
      expect(api.getChatSnapshot.mock.calls.length).toBe(before + 1),
    );

    // 同一 message_id 的后续 delta 与坏帧都不再触发。
    emit("delta", { kind: "text_delta", message_id: "m-x", payload: { text: "第二段" } }, 11);
    act(() => {
      stream.captured?.onEvent({ event: "delta", data: "{broken", id: 12 });
    });
    await act(async () => {});
    expect(api.getChatSnapshot.mock.calls.length).toBe(before + 1);

    // 新一轮模型请求（新 message_id）再触发一次。
    emit("delta", { kind: "text_delta", message_id: "m-y", payload: { text: "新一轮" } }, 13);
    await waitFor(() =>
      expect(api.getChatSnapshot.mock.calls.length).toBe(before + 2),
    );
  });

  it("tool-only 模型请求（新 call_id 的 tool_call）也触发一次快照重读", async () => {
    hook();
    await waitFor(() => expect(stream.subscribe).toHaveBeenCalled());
    const before = api.getChatSnapshot.mock.calls.length;

    // 纯工具调用请求没有 text_delta：首个 tool_call 到达即重读快照，
    // 指标随这个进行中的请求切换。
    emit(
      "tool_call",
      { call_id: "c-1", phase: "proposed", payload: { id: "c-1" } },
      20,
    );
    await waitFor(() =>
      expect(api.getChatSnapshot.mock.calls.length).toBe(before + 1),
    );

    // 同一 call_id 的后续阶段不重复。
    emit(
      "tool_call",
      { call_id: "c-1", phase: "running", payload: { id: "c-1" } },
      21,
    );
    await act(async () => {});
    expect(api.getChatSnapshot.mock.calls.length).toBe(before + 1);

    // 下一个调用的新 call_id 再触发一次。
    emit(
      "tool_call",
      { call_id: "c-2", phase: "proposed", payload: { id: "c-2" } },
      22,
    );
    await waitFor(() =>
      expect(api.getChatSnapshot.mock.calls.length).toBe(before + 2),
    );
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
      maintenanceProgress: null as null | {
        runId: string | null;
        phase: string;
        message: string;
      },
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
        setMaintenanceProgress: (
          progress: typeof state.maintenanceProgress,
        ) => {
          state.maintenanceProgress = progress;
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
  it("run_error 用 payload 的 code/message 字段填充失败原因", () => {
    const { state, sinks: sinksObj } = sinks();
    foldStreamEvent(
      {
        event: "error",
        data: JSON.stringify({
          run_id: "r-err",
          payload: { code: "model_request_failed", message: "上游 502" },
        }),
        id: 10,
      },
      sinksObj,
    );
    expect(state.runs.map((r) => `${r.id}:${r.status}:${r.error_code}:${r.error_message}`)).toEqual([
      "r-err:failed:model_request_failed:上游 502",
    ]);
  });
  it("同分快照也合并：权威快照补齐缓存缺失的终态字段", () => {
    const previous: BotCall[] = [{
      id: "call-1", bot_id: "bot-1", tool_name: "fs_issue", action: "create",
      status: "failed", created_at: "2026-01-01T00:00:00Z",
    } as unknown as BotCall];
    const authoritative: BotCall = {
      id: "call-1", bot_id: "bot-1", tool_name: "fs_issue", action: "create",
      status: "failed", error_code: "github_unauthorized", error_message: "402",
      created_at: "2026-01-01T00:00:00Z",
    } as unknown as BotCall;
    const merged = mergeCallSnapshots(previous, [authoritative]);
    expect(merged[0].error_code).toBe("github_unauthorized");
    // 反向（快照更旧更空）也不丢缓存里已有的字段。
    const back = mergeCallSnapshots([authoritative], [{
      id: "call-1", bot_id: "bot-1", tool_name: "fs_issue", action: "create",
      status: "failed", created_at: "2026-01-01T00:00:00Z",
    } as unknown as BotCall]);
    expect(back[0].error_code).toBe("github_unauthorized");
  });
  it("快照合并顺着批准转移方向：queued 胜过缓存的 pending_confirmation", () => {
    const { state, sinks: sinksObj } = sinks();
    state.calls = [{
      id: "call-1", bot_id: "bot-1", tool_name: "fs_issue", action: "create",
      status: "pending_confirmation", created_at: "2026-01-01T00:00:00Z",
    } as unknown as BotCall];
    foldStreamEvent(
      {
        event: "tool_call",
        data: JSON.stringify({
          call_id: "call-1",
          payload: { id: "call-1", status: "queued" },
        }),
        id: 12,
      },
      sinksObj,
    );
    expect(state.calls[0].status).toBe("queued");
  });
  it("异步终态 tool_result 携带完整后台结果消息并入时间线", () => {
    const { state, sinks: sinksObj } = sinks();
    const backgroundMessage: BotMessage = {
      id: "m-bg",
      bot_id: "bot-1",
      seq: 7,
      kind: "background_result",
      content: { summary: "issue 已创建", status: "succeeded" },
      created_at: "2026-01-01T00:00:00Z",
    };
    foldStreamEvent(
      {
        event: "tool_result",
        data: JSON.stringify({
          call_id: "call-1",
          payload: {
            id: "call-1",
            status: "succeeded",
            message_id: "m-bg",
            message: backgroundMessage,
          },
        }),
        id: 11,
      },
      sinksObj,
    );
    expect(state.messages.map((m) => m.id)).toEqual(["m-bg"]);
    expect(state.calls.map((c) => `${c.id}:${c.status}`)).toEqual(["call-1:succeeded"]);
  });

  it("schedule_event delta 折叠为完整消息，与刷新后的账本一致", () => {
    const { state, sinks: sinksObj } = sinks();
    const scheduleEventMessage: BotMessage = {
      id: "m-se",
      bot_id: "b1",
      seq: 9,
      kind: "schedule_event",
      content: {
        task_id: "t1",
        task_name: "每日汇总",
        run_id: "r-s1",
        status: "completed",
        summary: "已汇总 3 条变化",
      },
      created_at: "2026-10-17T01:02:10Z",
    };
    const event = {
      event: "delta",
      data: JSON.stringify({
        kind: "schedule_event",
        message_id: "m-se",
        payload: scheduleEventMessage,
      }),
      id: 13,
    };
    foldStreamEvent(event, sinksObj);
    // 重复投递幂等；payload 无 text，不得落入草稿分支产生空块。
    foldStreamEvent(event, sinksObj);
    expect(
      state.messages.map((m) => `${m.id}:${m.kind}:${m.content.task_name}`),
    ).toEqual(["m-se:schedule_event:每日汇总"]);
    expect(state.drafts.size).toBe(0);
  });

  // ---- F3：维护进度（压缩阶段/失败原因）----

  it("maintenance_progress 折叠为进度状态，不落入草稿分支", () => {
    const { state, sinks: sinksObj } = sinks();
    foldStreamEvent(
      {
        event: "delta",
        data: JSON.stringify({
          kind: "maintenance_progress",
          run_id: "r-1",
          payload: { phase: "archive", message: "正在归档上下文…" },
        }),
        id: 20,
      },
      sinksObj,
    );
    expect(state.maintenanceProgress).toEqual({
      runId: "r-1",
      phase: "archive",
      message: "正在归档上下文…",
    });
    expect(state.drafts.size).toBe(0);
    expect(state.messages).toEqual([]);

    // 最新一条替换旧阶段：摘要批次推进。
    foldStreamEvent(
      {
        event: "delta",
        data: JSON.stringify({
          kind: "maintenance_progress",
          run_id: "r-1",
          payload: { phase: "summary", message: "正在生成上下文摘要（第 2 批）…" },
        }),
        id: 21,
      },
      sinksObj,
    );
    expect(state.maintenanceProgress).toMatchObject({
      phase: "summary",
      message: "正在生成上下文摘要（第 2 批）…",
    });
  });

  it("done 清掉该 run 的维护进度，不残留假运行状态", () => {
    const { state, sinks: sinksObj } = sinks();
    foldStreamEvent(
      {
        event: "delta",
        data: JSON.stringify({
          kind: "maintenance_progress",
          run_id: "r-1",
          payload: { phase: "summary", message: "正在生成上下文摘要（第 1 批）…" },
        }),
        id: 20,
      },
      sinksObj,
    );
    foldStreamEvent(
      { event: "done", data: JSON.stringify({ run_id: "r-1", payload: { status: "completed" } }), id: 21 },
      sinksObj,
    );
    expect(state.maintenanceProgress).toBeNull();
  });

  it("run_error 同样清掉维护进度", () => {
    const { state, sinks: sinksObj } = sinks();
    foldStreamEvent(
      {
        event: "delta",
        data: JSON.stringify({
          kind: "maintenance_progress",
          run_id: "r-1",
          payload: { phase: "failed", message: "压缩目标不可达" },
        }),
        id: 20,
      },
      sinksObj,
    );
    foldStreamEvent(
      {
        event: "error",
        data: JSON.stringify({
          run_id: "r-1",
          payload: { code: "target_unreachable", message: "压缩目标不可达" },
        }),
        id: 21,
      },
      sinksObj,
    );
    expect(state.maintenanceProgress).toBeNull();
    expect(state.runs[0]?.error_code).toBe("target_unreachable");
  });

  it("新 run_started 清掉上一段维护进度", () => {
    const { state, sinks: sinksObj } = sinks();
    foldStreamEvent(
      {
        event: "delta",
        data: JSON.stringify({
          kind: "maintenance_progress",
          run_id: "r-0",
          payload: { phase: "failed", message: "压缩目标不可达" },
        }),
        id: 20,
      },
      sinksObj,
    );
    foldStreamEvent(
      {
        event: "delta",
        data: JSON.stringify({ kind: "run_started", run_id: "r-1" }),
        id: 21,
      },
      sinksObj,
    );
    expect(state.maintenanceProgress).toBeNull();
    expect(state.runs.map((r) => r.id)).toEqual(["r-1"]);
  });
});
