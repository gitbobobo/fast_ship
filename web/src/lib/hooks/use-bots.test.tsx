import type { ReactNode } from "react";
import { act, renderHook, waitFor } from "@testing-library/react";
import {
  QueryClient,
  QueryClientProvider,
  focusManager,
} from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  BOT_USAGE_RUN_GRACE_MS,
  BOT_USAGE_RUN_POLL_MS,
  useBotUsage,
  useBotUsageRun,
} from "./use-bots";
import type { BotUsageOverview, BotUsageRunDetail } from "@/lib/api/bots";
import { signInAs, teardownAuth } from "@/test/auth";

const api = vi.hoisted(() => ({
  usage: vi.fn(),
  usageRun: vi.fn(),
}));

vi.mock("@/lib/api/bots", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/bots")>()),
  botApi: api,
}));

const overview: BotUsageOverview = {
  range: "7d",
  timezone: "Asia/Shanghai",
  start: "2026-10-11T16:00:00Z",
  end: "2026-10-18T16:00:00Z",
  totals: {
    cost: null,
    daily_average: null,
    total_requests: 0,
    priced_requests: 0,
    unpriced_requests: 0,
  },
  days: [],
  bots: [],
  schedule_tasks: [],
};

const runDetail = (status: string): { data: BotUsageRunDetail } => ({
  data: {
    run: {
      id: "r1",
      bot_id: "b1",
      bot_name: "发布助手",
      session_kind: "main",
      trigger_source: "user_message",
      trigger_key: "",
      status,
      summary: "",
      silent: false,
      usage_complete: false,
      created_at: "2026-10-18T01:00:00Z",
      cost_estimate: null,
      cost_source: null,
      maintenance_cost: null,
      display_cost: null,
    },
    requests: [],
    requests_truncated: false,
    maintenance_runs: [],
  },
});

function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, wrapper };
}

beforeEach(() => {
  signInAs("A");
});

afterEach(() => {
  teardownAuth();
  vi.clearAllMocks();
  vi.useRealTimers();
  focusManager.setFocused(undefined);
});

describe("usage 查询（INT-162 F11）", () => {
  it("总览查询在窗口聚焦时重取", async () => {
    api.usage.mockResolvedValue({ data: overview });
    const { wrapper } = setup();
    renderHook(() => useBotUsage("7d"), { wrapper });
    await waitFor(() => expect(api.usage).toHaveBeenCalledTimes(1));

    act(() => focusManager.setFocused(false));
    act(() => focusManager.setFocused(true));

    await waitFor(() => expect(api.usage).toHaveBeenCalledTimes(2));
  });

  it("运行明细在窗口聚焦时重取", async () => {
    api.usageRun.mockResolvedValue(runDetail("completed"));
    const { wrapper } = setup();
    renderHook(() => useBotUsageRun("r1"), { wrapper });
    await waitFor(() => expect(api.usageRun).toHaveBeenCalledTimes(1));

    act(() => focusManager.setFocused(false));
    act(() => focusManager.setFocused(true));

    await waitFor(() => expect(api.usageRun).toHaveBeenCalledTimes(2));
  });

  it("run 未终态时按 5s 轮询，拿到终态后停", async () => {
    vi.useFakeTimers();
    api.usageRun.mockResolvedValue(runDetail("running"));
    const { wrapper } = setup();
    renderHook(() => useBotUsageRun("r1"), { wrapper });
    // act 的 await 刷新微任务：首轮请求落地后才会按 run.status 排定轮询。
    await act(async () => {});
    expect(api.usageRun).toHaveBeenCalledTimes(1);

    await act(async () => {
      vi.advanceTimersByTime(BOT_USAGE_RUN_POLL_MS);
    });
    expect(api.usageRun).toHaveBeenCalledTimes(2);

    // 下一轮返回终态（finished_at 远在宽限期前）：轮询就此停止。
    const done = runDetail("completed");
    done.data.run.finished_at = new Date(
      Date.now() - BOT_USAGE_RUN_GRACE_MS * 2,
    ).toISOString();
    api.usageRun.mockResolvedValue(done);
    await act(async () => {
      vi.advanceTimersByTime(BOT_USAGE_RUN_POLL_MS);
    });
    expect(api.usageRun).toHaveBeenCalledTimes(3);
    await act(async () => {
      vi.advanceTimersByTime(BOT_USAGE_RUN_POLL_MS * 4);
    });
    expect(api.usageRun).toHaveBeenCalledTimes(3);
  });

  it("终态后宽限期内仍轮询，宽限期过后停", async () => {
    vi.useFakeTimers();
    const detail = runDetail("completed");
    detail.data.run.finished_at = new Date().toISOString();
    api.usageRun.mockResolvedValue(detail);
    const { wrapper } = setup();
    renderHook(() => useBotUsageRun("r1"), { wrapper });
    await act(async () => {});
    expect(api.usageRun).toHaveBeenCalledTimes(1);

    // 宽限期内：迟到入账的关联维护还有机会进 detail。
    await act(async () => {
      vi.advanceTimersByTime(BOT_USAGE_RUN_POLL_MS);
    });
    expect(api.usageRun).toHaveBeenCalledTimes(2);
    await act(async () => {
      vi.advanceTimersByTime(BOT_USAGE_RUN_POLL_MS);
    });
    expect(api.usageRun).toHaveBeenCalledTimes(3);

    // 过了宽限期即停。
    await act(async () => {
      vi.advanceTimersByTime(BOT_USAGE_RUN_GRACE_MS);
    });
    const calls = api.usageRun.mock.calls.length;
    await act(async () => {
      vi.advanceTimersByTime(BOT_USAGE_RUN_POLL_MS * 3);
    });
    expect(api.usageRun).toHaveBeenCalledTimes(calls);
  });

  it("run 一开始就是终态且早已结束时不轮询", async () => {
    vi.useFakeTimers();
    const done = runDetail("completed");
    done.data.run.finished_at = new Date(
      Date.now() - BOT_USAGE_RUN_GRACE_MS * 2,
    ).toISOString();
    api.usageRun.mockResolvedValue(done);
    const { wrapper } = setup();
    renderHook(() => useBotUsageRun("r1"), { wrapper });
    await act(async () => {});
    expect(api.usageRun).toHaveBeenCalledTimes(1);

    await act(async () => {
      vi.advanceTimersByTime(BOT_USAGE_RUN_POLL_MS * 3);
    });
    expect(api.usageRun).toHaveBeenCalledTimes(1);
  });
});
