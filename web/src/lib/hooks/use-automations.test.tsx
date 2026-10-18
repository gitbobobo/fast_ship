import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { type ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  automationApi,
  type Automation,
  type AutomationTrigger,
} from "@/lib/api/automations";
import { useToggleAutomation } from "./use-automations";

// 真实 hook + mock api：验证启停不是拿列表行的旧快照组请求体，而是先 GET 最新
// 详情再全量 PUT（服务端 PUT 是触发器全量替换语义）。
vi.mock("@/lib/api/automations", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/automations")>();
  return {
    ...actual,
    automationApi: { ...actual.automationApi, get: vi.fn(), update: vi.fn() },
  };
});

const getMock = vi.mocked(automationApi.get);
const updateMock = vi.mocked(automationApi.update);

const createdAt = "2026-10-14T00:00:00Z";

const cronTrigger: AutomationTrigger = {
  id: "t-1",
  trigger_type: "cron",
  cron_expression: "0 9 * * *",
  timezone: "Asia/Shanghai",
  next_run_at: null,
  issue_project_id: null,
  event_filter: null,
  model_selections: [],
  debounce_seconds: 0,
  created_at: createdAt,
};

// 列表加载之后在编辑页新增的触发器：列表快照里没有它。
const issueTrigger: AutomationTrigger = {
  id: "t-2",
  trigger_type: "issue_created",
  cron_expression: null,
  timezone: null,
  next_run_at: null,
  issue_project_id: null,
  event_filter: null,
  model_selections: [],
  debounce_seconds: 0,
  created_at: createdAt,
};

function record(over: Partial<Automation> = {}): Automation {
  return {
    id: "a-1",
    name: "每日令牌回报",
    enabled: false,
    environment_id: "env-1",
    t3_project_id: "proj-1",
    triggers: [cronTrigger],
    prompt_template: "回报令牌",
    model_selection: null,
    runtime_mode: "full-access",
    workspace: null,
    timeout_seconds: 3600,
    post_action: "settle",
    post_on_timeout: false,
    created_at: createdAt,
    updated_at: createdAt,
    ...over,
  };
}

const response = <T,>(data: T) =>
  ({ code: 0, message: "success", data }) as unknown as Awaited<
    ReturnType<typeof automationApi.get>
  >;

function setup() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return renderHook(() => useToggleAutomation(), { wrapper });
}

describe("useToggleAutomation", () => {
  beforeEach(() => {
    getMock.mockReset();
    updateMock.mockReset();
  });

  it("只传 id：先 GET 最新详情，再用最新触发器组全量 PUT 并翻转 enabled", async () => {
    // 详情里多出 issue 触发器（列表快照里只有 cron）。
    getMock.mockResolvedValue(
      response(record({ triggers: [cronTrigger, issueTrigger] })),
    );
    updateMock.mockResolvedValue(
      response(record({ enabled: true, triggers: [cronTrigger, issueTrigger] })),
    );

    const { result } = setup();
    await act(async () => {
      await result.current.mutateAsync("a-1");
    });

    expect(getMock).toHaveBeenCalledWith("a-1");
    expect(updateMock).toHaveBeenCalledTimes(1);
    // 先读后写：请求体必须来自 GET 的最新响应。
    expect(getMock.mock.invocationCallOrder[0]).toBeLessThan(
      updateMock.mock.invocationCallOrder[0],
    );

    const [id, body] = updateMock.mock.calls[0];
    expect(id).toBe("a-1");
    expect(body.enabled).toBe(true);
    expect(body.triggers.map((t) => t.id)).toEqual(["t-1", "t-2"]);
    expect(body.prompt_template).toBe("回报令牌");
  });

  it("已启用的详情翻转成停用", async () => {
    getMock.mockResolvedValue(response(record({ enabled: true })));
    updateMock.mockResolvedValue(response(record({ enabled: false })));

    const { result } = setup();
    await act(async () => {
      await result.current.mutateAsync("a-1");
    });

    expect(updateMock.mock.calls[0][1].enabled).toBe(false);
  });
});
