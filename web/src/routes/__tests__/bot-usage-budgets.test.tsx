import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  BotBudgetDialog,
  parseThresholdInput,
} from "@/components/bots/bot-budget-dialog";
import { BotUsageOverviewPanel } from "@/components/bots/bot-usage-overview";
import type { BotBudget, BotUsageOverview } from "@/lib/api/bots";
import { useBotUsage, useBotUsageInsights } from "@/lib/hooks/use-bots";
import { signInAs, teardownAuth } from "@/test/auth";
import { renderWithRoute, withTestQueryClient } from "@/test/render";

// 预算对话框走真实 hooks（botApi → ky → fetch stub），只把总览面板的
// 两个读查询 mock 掉，让入口集成测试不必组装 usage/insights 的大响应。
vi.mock("@/lib/hooks/use-bots", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/hooks/use-bots")>()),
  useBotUsage: vi.fn(),
  useBotUsageInsights: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

import { toast } from "sonner";

beforeEach(() => {
  vi.clearAllMocks();
  // 测试环境默认 fetch 快速失败（setup.ts），这里按用例覆盖并记账。
  vi.stubGlobal("fetch", vi.fn());
  signInAs("alice");
});

afterEach(() => {
  teardownAuth();
  vi.unstubAllGlobals();
});

function apiJson(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

function apiOk(data: unknown) {
  return apiJson({ code: 0, message: "", data });
}

/** fetchMock 记账的请求：ky 传 Request 对象，路径取 pathname、体懒解析。 */
type Recorded = {
  method: string;
  path: string;
  body: () => Promise<unknown>;
};

function recordedRequests(): Recorded[] {
  const fetchMock = vi.mocked(globalThis.fetch);
  return fetchMock.mock.calls.map((call) => {
    const input = call[0] as URL | RequestInfo;
    const request = input instanceof Request ? input : new Request(String(input));
    return {
      method: request.method,
      path: new URL(request.url).pathname,
      body: async () => {
        const text = await request.clone().text();
        return text ? JSON.parse(text) : undefined;
      },
    };
  });
}

function findRecorded(
  method: string,
  path: string,
): Recorded | undefined {
  return recordedRequests().find(
    (r) => r.method === method && r.path === path,
  );
}

function makeBudget(overrides: Partial<BotBudget> = {}): BotBudget {
  return {
    id: "budget-daily",
    period: "daily",
    amount_usd: 1,
    thresholds: [0.8, 1],
    spend_usd: 0.4,
    period_key: "2026-10-26",
    spend_window_start: "2026-10-25T16:00:00Z",
    spend_window_end: "2026-10-26T04:00:00Z",
    created_at: "2026-10-20T00:00:00Z",
    updated_at: "2026-10-20T00:00:00Z",
    ...overrides,
  };
}

/** 有状态的 budgets 内存端：PUT 覆盖、DELETE 移除，验证对话框的刷新链路。 */
function serveBudgets(prefix: string, initial: BotBudget[]) {
  let items = [...initial];
  const fetchMock = vi.mocked(globalThis.fetch);
  fetchMock.mockImplementation(async (input) => {
    const request = input instanceof Request ? input : new Request(String(input));
    const path = new URL(request.url).pathname;
    if (!path.startsWith(prefix)) {
      return apiJson({ code: 40400, message: "not found" }, 404);
    }
    if (request.method === "GET" && path === prefix) {
      return apiOk({ items });
    }
    const period = path.slice(prefix.length + 1);
    if (request.method === "PUT" && ["daily", "monthly"].includes(period)) {
      const input = (await request.clone().json()) as {
        amount_usd: number;
        thresholds: number[];
      };
      const saved = makeBudget({
        period: period as BotBudget["period"],
        id: `budget-${period}`,
        amount_usd: input.amount_usd,
        thresholds: input.thresholds,
      });
      items = [...items.filter((b) => b.period !== saved.period), saved];
      return apiOk(saved);
    }
    if (request.method === "DELETE" && ["daily", "monthly"].includes(period)) {
      items = items.filter((b) => b.period !== period);
      return apiOk(null);
    }
    return apiJson({ code: 40400, message: "not found" }, 404);
  });
}

async function flushRequest(method: string, path: string) {
  await waitFor(() => {
    expect(findRecorded(method, path)).toBeDefined();
  });
}

describe("parseThresholdInput", () => {
  it("「80%,100%」与「0.8,1」同样解析成比例数组并去重升序", () => {
    expect(parseThresholdInput("80%,100%")).toEqual({ ratios: [0.8, 1] });
    expect(parseThresholdInput("0.8,1")).toEqual({ ratios: [0.8, 1] });
    expect(parseThresholdInput("100，80")).toEqual({ ratios: [0.8, 1] });
    // 可超 100%（150% = 1.5）；重复档位去重。
    expect(parseThresholdInput("150%")).toEqual({ ratios: [1.5] });
    expect(parseThresholdInput("80% 80%")).toEqual({ ratios: [0.8] });
  });

  it("空输入、非法数字与越界档位报错", () => {
    expect(parseThresholdInput("")).toHaveProperty("error");
    expect(parseThresholdInput("abc")).toHaveProperty("error");
    expect(parseThresholdInput("0")).toHaveProperty("error");
    expect(parseThresholdInput("30000%")).toHaveProperty("error");
  });
});

describe("BotBudgetDialog", () => {
  it("机器人预算：GET 列表、进度展示、PUT 体与 DELETE 路径", async () => {
    const user = userEvent.setup();
    serveBudgets("/api/bots/b1/budgets", [makeBudget()]);
    render(
      withTestQueryClient(
        <BotBudgetDialog
          target={{ kind: "bot", botId: "b1" }}
          open
          onClose={vi.fn()}
        />,
      ),
    );

    // 打开即拉取列表；已设置周期渲染 spend/amount 进度。
    await flushRequest("GET", "/api/bots/b1/budgets");
    expect(
      await screen.findByText("已花费 $0.4 / $1.00（40%）"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("progressbar", { name: "每日预算使用进度" }),
    ).toHaveAttribute("aria-valuenow", "40");

    // 未设置的周期没有「清除」按钮。
    expect(screen.queryByTestId("budget-clear-monthly")).toBeNull();

    // 保存每日预算：金额直传，阈值文本解析成比例数组。
    const amount = screen.getByTestId("budget-amount-daily");
    await user.clear(amount);
    await user.type(amount, "10");
    const thresholds = screen.getByTestId("budget-thresholds-daily");
    await user.clear(thresholds);
    await user.type(thresholds, "80%,100%");
    await user.click(screen.getByTestId("budget-save-daily"));

    await waitFor(async () => {
      const put = findRecorded("PUT", "/api/bots/b1/budgets/daily");
      expect(put).toBeDefined();
      expect(await put!.body()).toEqual({
        amount_usd: 10,
        thresholds: [0.8, 1],
      });
    });
    await waitFor(() => {
      expect(toast.success).toHaveBeenCalledWith("每日预算已保存");
    });
    // 回显服务端规范化结果（响应即已存值）。
    expect(screen.getByTestId("budget-amount-daily")).toHaveValue("10");

    // 清除：DELETE 幂等路径，之后回落「未设置」态（两个周期都未设置）。
    await user.click(screen.getByTestId("budget-clear-daily"));
    await flushRequest("DELETE", "/api/bots/b1/budgets/daily");
    await waitFor(() => {
      expect(screen.queryByTestId("budget-clear-daily")).toBeNull();
    });
    expect(screen.getAllByText(/未设置/)).toHaveLength(2);
  });

  it("表单校验：金额非正或阈值非法时不发写请求", async () => {
    const user = userEvent.setup();
    serveBudgets("/api/bots/b1/budgets", []);
    render(
      withTestQueryClient(
        <BotBudgetDialog
          target={{ kind: "bot", botId: "b1" }}
          open
          onClose={vi.fn()}
        />,
      ),
    );
    await screen.findByTestId("budget-period-daily");

    // 空金额直接保存：行内报错，无 PUT。
    await user.click(screen.getByTestId("budget-save-daily"));
    expect(screen.getByRole("alert")).toHaveTextContent("金额需为大于 0");
    expect(findRecorded("PUT", "/api/bots/b1/budgets/daily")).toBeUndefined();

    // 阈值非法：同样拦截。
    await user.type(screen.getByTestId("budget-amount-daily"), "10");
    await user.type(screen.getByTestId("budget-thresholds-daily"), "abc");
    await user.click(screen.getByTestId("budget-save-daily"));
    expect(screen.getByRole("alert")).toHaveTextContent("阈值");
    expect(findRecorded("PUT", "/api/bots/b1/budgets/daily")).toBeUndefined();

    // 修正后可以提交成功。
    await user.clear(screen.getByTestId("budget-thresholds-daily"));
    await user.type(screen.getByTestId("budget-thresholds-daily"), "50%,100%");
    await user.click(screen.getByTestId("budget-save-daily"));
    await waitFor(async () => {
      const put = findRecorded("PUT", "/api/bots/b1/budgets/daily");
      expect(put).toBeDefined();
      expect(await put!.body()).toEqual({
        amount_usd: 10,
        thresholds: [0.5, 1],
      });
    });
    expect(toast.success).toHaveBeenCalledWith("每日预算已保存");
  });

  it("任务预算：GET/PUT 走 schedule-tasks 路径", async () => {
    const user = userEvent.setup();
    serveBudgets("/api/bots/b1/schedule-tasks/t1/budgets", []);
    render(
      withTestQueryClient(
        <BotBudgetDialog
          target={{ kind: "task", botId: "b1", taskId: "t1" }}
          open
          onClose={vi.fn()}
        />,
      ),
    );

    await flushRequest("GET", "/api/bots/b1/schedule-tasks/t1/budgets");
    await screen.findByTestId("budget-period-monthly");

    await user.type(screen.getByTestId("budget-amount-monthly"), "5");
    await user.type(screen.getByTestId("budget-thresholds-monthly"), "50%,100%");
    await user.click(screen.getByTestId("budget-save-monthly"));

    await waitFor(async () => {
      const put = findRecorded(
        "PUT",
        "/api/bots/b1/schedule-tasks/t1/budgets/monthly",
      );
      expect(put).toBeDefined();
      expect(await put!.body()).toEqual({
        amount_usd: 5,
        thresholds: [0.5, 1],
      });
    });
  });
});

describe("花费总览的预算入口", () => {
  const usage: BotUsageOverview = {
    range: "7d",
    timezone: "Asia/Shanghai",
    start: "2026-10-19T16:00:00Z",
    end: "2026-10-26T16:00:00Z",
    totals: {
      cost: 1,
      daily_average: 0.14,
      total_requests: 4,
      priced_requests: 4,
      unpriced_requests: 0,
    },
    days: [],
    bots: [
      {
        bot_id: "b1",
        name: "发布助手",
        cost: 1,
        cost_main: 1,
        cost_schedule: null,
        cost_schedule_linked_approx: null,
        cost_maintenance: null,
        runs: 2,
        avg_run_cost: 0.5,
        max_run_cost: 0.6,
        p90_run_cost: 0.6,
        cache_hit_rate: null,
        unpriced_requests: 0,
      },
    ],
    schedule_tasks: [
      {
        task_id: "t1",
        bot_id: "b1",
        bot_name: "发布助手",
        name: "每日巡检",
        deleted: false,
        enabled: true,
        cron_expression: "0 9 * * *",
        timezone: "Asia/Shanghai",
        runs: 2,
        failed: 0,
        skipped: 0,
        cost: 0.5,
        avg_run_cost: 0.25,
        max_run_cost: 0.3,
        p90_run_cost: 0.3,
        linked_main_cost: null,
        estimated_monthly_cost: 7.5,
      },
    ],
  };

  function mockUsage(data: BotUsageOverview | undefined) {
    vi.mocked(useBotUsage).mockReturnValue({
      data,
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    } as never);
  }

  function mockEmptyInsights() {
    vi.mocked(useBotUsageInsights).mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: false,
      error: null,
      refetch: vi.fn(),
    } as never);
  }

  it("机器人行「预算」打开 bot 目标对话框并拉取预算", async () => {
    mockEmptyInsights();
    mockUsage(usage);
    serveBudgets("/api/bots/b1/budgets", []);
    const user = userEvent.setup();
    renderWithRoute(<BotUsageOverviewPanel />, {
      path: "/bots/usage",
      initialEntry: "/bots/usage",
    });

    await user.click(
      await screen.findByRole("button", { name: "设置 发布助手 的预算" }),
    );
    expect(await screen.findByText("预算设置")).toBeInTheDocument();
    await flushRequest("GET", "/api/bots/b1/budgets");
  });

  it("任务行「预算」打开 task 目标对话框并拉取任务预算", async () => {
    mockEmptyInsights();
    mockUsage(usage);
    serveBudgets("/api/bots/b1/schedule-tasks/t1/budgets", []);
    const user = userEvent.setup();
    renderWithRoute(<BotUsageOverviewPanel />, {
      path: "/bots/usage",
      initialEntry: "/bots/usage",
    });

    await user.click(
      await screen.findByRole("button", { name: "设置 每日巡检 的预算" }),
    );
    expect(await screen.findByText("预算设置")).toBeInTheDocument();
    await flushRequest("GET", "/api/bots/b1/schedule-tasks/t1/budgets");
  });
});
