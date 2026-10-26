import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { BotUsageOverview } from "@/lib/api/bots";
import { useBotUsage, useBotUsageInsights } from "@/lib/hooks/use-bots";
import { renderWithRoute } from "@/test/render";
import {
  BotUsageOverviewPanel,
  buildDailyCostPoints,
  formatCacheHitRate,
  formatUsageCost,
  taskTotalCost,
  unpricedShare,
  usageTaskName,
} from "./bot-usage-overview";

vi.mock("@/lib/hooks/use-bots", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/hooks/use-bots")>()),
  useBotUsage: vi.fn(),
  useBotUsageInsights: vi.fn(),
}));

const pricedOverview: BotUsageOverview = {
  range: "7d",
  timezone: "Asia/Shanghai",
  start: "2026-10-11T16:00:00Z",
  end: "2026-10-18T16:00:00Z",
  totals: {
    cost: 1.23,
    daily_average: 0.1757,
    total_requests: 10,
    priced_requests: 8,
    unpriced_requests: 2,
  },
  days: [
    {
      date: "2026-10-17",
      cost: 0.5,
      bots: [{ bot_id: "b1", cost: 0.5 }],
    },
    {
      date: "2026-10-18",
      cost: 0.73,
      bots: [
        { bot_id: "b1", cost: 0.4 },
        { bot_id: "b2", cost: 0.33 },
      ],
    },
  ],
  bots: [
    {
      bot_id: "b1",
      name: "发布助手",
      cost: 0.9,
      cost_main: 0.5,
      cost_schedule: 0.3,
      cost_schedule_linked_approx: 0.1,
      cost_maintenance: 0.1,
      runs: 3,
      avg_run_cost: 0.3,
      max_run_cost: 0.5,
      p90_run_cost: 0.45,
      cache_hit_rate: 0.8,
      unpriced_requests: 1,
    },
    {
      bot_id: "b2",
      name: "巡检",
      cost: 0.33,
      cost_main: 0.33,
      cost_schedule: null,
      cost_schedule_linked_approx: null,
      cost_maintenance: null,
      runs: 2,
      avg_run_cost: 0.165,
      max_run_cost: 0.2,
      p90_run_cost: 0.2,
      cache_hit_rate: null,
      unpriced_requests: 1,
    },
  ],
  schedule_tasks: [
    {
      task_id: "t1",
      bot_id: "b1",
      bot_name: "发布助手",
      name: "每日巡检",
      deleted: false,
      enabled: false,
      cron_expression: "0 9 * * *",
      timezone: "Asia/Shanghai",
      runs: 3,
      failed: 1,
      skipped: 2,
      cost: 0.3,
      avg_run_cost: 0.1,
      max_run_cost: 0.15,
      p90_run_cost: 0.15,
      linked_main_cost: 0.1,
      estimated_monthly_cost: 3.0,
    },
    {
      task_id: "t2",
      bot_id: "b1",
      bot_name: "发布助手",
      name: "",
      deleted: true,
      enabled: false,
      cron_expression: "",
      timezone: "",
      runs: 1,
      failed: 0,
      skipped: 0,
      cost: null,
      avg_run_cost: null,
      max_run_cost: null,
      p90_run_cost: null,
      linked_main_cost: null,
      estimated_monthly_cost: null,
    },
  ],
};

const unpricedOverview: BotUsageOverview = {
  ...pricedOverview,
  totals: {
    cost: null,
    daily_average: null,
    total_requests: 3,
    priced_requests: 0,
    unpriced_requests: 3,
  },
  days: [
    { date: "2026-10-18", cost: 0, bots: [{ bot_id: "b1", cost: 0 }] },
  ],
  bots: [
    {
      ...pricedOverview.bots[0],
      cost: null,
      cost_main: null,
      cost_schedule: null,
      cost_maintenance: null,
      avg_run_cost: null,
      max_run_cost: null,
      p90_run_cost: null,
      unpriced_requests: 3,
    },
  ],
  schedule_tasks: [],
};

/** 已删除机器人（INT-164）：明细已级联清理，name 是删除时快照。 */
const deletedBot: BotUsageOverview["bots"][number] = {
  bot_id: "b9",
  name: "退役机器人",
  deleted: true,
  cost: 0.2,
  cost_main: null,
  cost_schedule: 0.2,
  cost_schedule_linked_approx: null,
  cost_maintenance: null,
  runs: 1,
  avg_run_cost: 0.2,
  max_run_cost: 0.2,
  p90_run_cost: 0.2,
  cache_hit_rate: null,
  unpriced_requests: 0,
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

/** 优化参考区由独立查询驱动：总览测试里固定回空，聚焦总览自身的断言。 */
function mockEmptyInsights() {
  vi.mocked(useBotUsageInsights).mockReturnValue({
    data: undefined,
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  } as never);
}

describe("usage 格式化助手", () => {
  it("可空金额是「—」、显式 0 是真实 $0", () => {
    expect(formatUsageCost(null)).toBe("—");
    expect(formatUsageCost(undefined)).toBe("—");
    expect(formatUsageCost(0)).toBe("$0");
    expect(formatUsageCost(1.234)).toBe("$1.23");
  });

  it("缓存命中率：0–1 转整百分数，null → 「—」，极小非零不显示 0%", () => {
    expect(formatCacheHitRate(0.8)).toBe("80%");
    // 真零仍是 0%，与「四舍五入变 0%」区分开。
    expect(formatCacheHitRate(0)).toBe("0%");
    expect(formatCacheHitRate(null)).toBe("—");
    // 0 < rate < 0.005 显示「<1%」；边界 0.005 照常进位成 1%。
    expect(formatCacheHitRate(0.004)).toBe("<1%");
    expect(formatCacheHitRate(0.0049)).toBe("<1%");
    expect(formatCacheHitRate(0.005)).toBe("1%");
  });

  it("任务名：deleted 或空名落「已删除的任务」，有快照名仍显示原名", () => {
    const task = pricedOverview.schedule_tasks[0];
    expect(usageTaskName(task)).toBe("每日巡检");
    expect(usageTaskName({ ...task, deleted: true })).toBe("每日巡检");
    expect(usageTaskName({ ...task, name: "", deleted: true })).toBe(
      "已删除的任务",
    );
  });

  it("任务总花费 = 自身已计价 + 连带主线，两者都未计价为 null", () => {
    const task = pricedOverview.schedule_tasks[0];
    expect(taskTotalCost(task)).toBeCloseTo(0.4);
    expect(taskTotalCost({ ...task, cost: null })).toBeCloseTo(0.1);
    expect(taskTotalCost({ ...task, linked_main_cost: null })).toBeCloseTo(0.3);
    expect(
      taskTotalCost({ ...task, cost: null, linked_main_cost: null }),
    ).toBeNull();
  });

  it("未计价占比：无请求返回 null", () => {
    expect(unpricedShare(pricedOverview.totals)).toEqual({
      unpriced: 2,
      total: 10,
      percent: 20,
    });
    expect(
      unpricedShare({
        cost: null,
        daily_average: null,
        total_requests: 0,
        priced_requests: 0,
        unpriced_requests: 0,
      }),
    ).toBeNull();
  });

  it("按天堆叠点：每个机器人一个系列，缺名退化为 bot_id", () => {
    const points = buildDailyCostPoints(pricedOverview.days, [
      pricedOverview.bots[0],
    ]);
    expect(points).toHaveLength(2);
    expect(points[0]).toMatchObject({ label: "2026-10-17", shortLabel: "10-17" });
    expect(points[0].series).toEqual([
      { id: "b1", name: "发布助手", value: 0.5 },
    ]);
    // b2 不在 bots 表（极端情况）：id 兜底当名字。
    expect(points[1].series).toEqual([
      { id: "b1", name: "发布助手", value: 0.4 },
      { id: "b2", name: "b2", value: 0.33 },
    ]);
  });
});

describe("BotUsageOverviewPanel", () => {
  beforeEach(() => {
    mockEmptyInsights();
  });

  it("默认 7 天范围：总计卡、按天柱状图、按机器人与按任务表", () => {
    mockUsage(pricedOverview);
    renderWithRoute(<BotUsageOverviewPanel />, {
      path: "/bots/usage",
      initialEntry: "/bots/usage",
    });

    // 范围切换默认「近 7 天」，请求带该 range。
    expect(screen.getByTestId("usage-range-7d")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(useBotUsage).toHaveBeenCalledWith("7d", undefined);

    // 「总花费」出现在总计卡与任务表头两处。
    expect(screen.getAllByText("总花费")).toHaveLength(2);
    expect(screen.getByText("$1.23")).toBeInTheDocument();
    // 未计价占比 + 原因提示（「未计价」出现在总计卡与按机器人表头两处）。
    expect(screen.getAllByText("未计价")).toHaveLength(2);
    expect(screen.getByText("2 次")).toBeInTheDocument();
    expect(screen.getByText(/占 20%/)).toBeInTheDocument();
    expect(screen.getByText(/目录无价/)).toBeInTheDocument();

    // 按天柱状图（columns 变体）。
    expect(
      screen.getByRole("img", { name: "stacked column chart" }),
    ).toBeInTheDocument();

    // 按机器人表：行可点回详情，未计价列直接计数。
    const botRows = screen.getAllByTestId("usage-bot-row");
    expect(botRows).toHaveLength(2);
    expect(
      within(botRows[0]).getByRole("link", { name: "发布助手" }),
    ).toHaveAttribute("href", "/bots/b1");
    expect(within(botRows[0]).getByText("80%")).toBeInTheDocument();
    // P90 列（INT-163）：与平均/最高并列，两张表各有一个口径表头。
    expect(screen.getAllByTitle(/P90（nearest-rank）/)).toHaveLength(2);
    expect(within(botRows[0]).getByText("$0.45")).toBeInTheDocument();
    // b2 定时/维护/缓存命中率均为 null → 三个「—」，不是 0。
    expect(within(botRows[1]).getAllByText("—")).toHaveLength(3);

    // 按任务表：停用徽标、已删除名、「近似」口径与推算月花费。
    const taskRows = screen.getAllByTestId("usage-task-row");
    expect(taskRows).toHaveLength(2);
    expect(within(taskRows[0]).getByText("已停用")).toBeInTheDocument();
    expect(within(taskRows[1]).getByText("已删除的任务")).toBeInTheDocument();
    expect(screen.getByText("连带主线（估）")).toBeInTheDocument();
    expect(within(taskRows[0]).getByText("$3.00")).toBeInTheDocument();
    // 「总花费」含连带主线（与排序口径一致）：0.3 + 0.1 = $0.4；
    // 连带部分仍在单独列（连带主线≈ $0.1）。P90 列插在平均与最高之间。
    const t1Cells = within(taskRows[0]).getAllByRole("cell");
    expect(t1Cells[5]).toHaveTextContent("$0.4");
    expect(t1Cells[7]).toHaveTextContent("$0.15");
    expect(t1Cells[9]).toHaveTextContent("$0.1");
    // 两者都未计价时「总花费」显示「—」。
    const t2Cells = within(taskRows[1]).getAllByRole("cell");
    expect(t2Cells[5]).toHaveTextContent("—");
    expect(t2Cells[7]).toHaveTextContent("—");
  });

  it("全未计价：总计显示「—」与提示，不显示 $0", () => {
    mockUsage(unpricedOverview);
    renderWithRoute(<BotUsageOverviewPanel />, {
      path: "/bots/usage",
      initialEntry: "/bots/usage",
    });

    expect(screen.getAllByText("总花费").length).toBeGreaterThan(0);
    // 总花费与日均都是「—」；页面不得冒出 $0。
    const dashes = screen.getAllByText("—");
    expect(dashes.length).toBeGreaterThanOrEqual(2);
    expect(document.body.textContent).not.toContain("$0");
    expect(screen.getByText(/未计价不计入金额/)).toBeInTheDocument();
    // 全零天柱状图回落到空态说明而不是一根隐形柱。
    expect(screen.getByText("窗口内没有已计价花费")).toBeInTheDocument();
  });

  it("切换范围触发同 hook 的不同 range 请求", async () => {
    mockUsage(pricedOverview);
    const user = userEvent.setup();
    renderWithRoute(<BotUsageOverviewPanel />, {
      path: "/bots/usage",
      initialEntry: "/bots/usage",
    });

    await user.click(screen.getByTestId("usage-range-today"));
    expect(screen.getByTestId("usage-range-today")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(useBotUsage).toHaveBeenLastCalledWith("today", undefined);
  });

  it("embedded 单机器人：成本构成紧凑清单，请求带 bot_id", () => {
    mockUsage({
      ...pricedOverview,
      bots: [pricedOverview.bots[0]],
    });
    renderWithRoute(<BotUsageOverviewPanel botId="b1" embedded />, {
      path: "/bots/b1",
      initialEntry: "/bots/b1",
    });

    expect(useBotUsage).toHaveBeenCalledWith("7d", "b1");
    expect(screen.getByText("成本构成")).toBeInTheDocument();
    // 紧凑清单不含宽表头。
    expect(screen.queryByText("缓存命中率")).toBeNull();
    expect(screen.getByText(/平均 \$0\.3/)).toBeInTheDocument();
    // 摘要行的 P90 与紧凑任务行的 P90（INT-163）。
    expect(screen.getByText(/P90 \$0\.45/)).toBeInTheDocument();
    expect(screen.getByText(/P90 \$0\.15/)).toBeInTheDocument();
    // 紧凑任务清单的「总花费」同样含连带主线（0.3 + 0.1）。
    expect(screen.getByText(/总花费 \$0\.4/)).toBeInTheDocument();
  });

  it("已删除机器人行：无链接、「已删除」徽记、名字弱化且无预算入口（INT-164）", () => {
    mockUsage({
      ...pricedOverview,
      bots: [...pricedOverview.bots, deletedBot],
      // t9 挂在已删除机器人 b9 上；另留一条 b1 的存活任务对照。
      schedule_tasks: [
        pricedOverview.schedule_tasks[0],
        {
          ...pricedOverview.schedule_tasks[0],
          task_id: "t9",
          bot_id: "b9",
          bot_name: "退役机器人",
        },
      ],
    });
    renderWithRoute(<BotUsageOverviewPanel />, {
      path: "/bots/usage",
      initialEntry: "/bots/usage",
    });

    const botRows = screen.getAllByTestId("usage-bot-row");
    expect(botRows).toHaveLength(3);
    const deletedRow = botRows[2];
    const deletedName = within(deletedRow).getByText("退役机器人");
    expect(deletedName).not.toHaveAttribute("href");
    expect(deletedName).toHaveClass("text-muted-foreground");
    expect(within(deletedRow).getByText("已删除")).toBeInTheDocument();
    expect(within(deletedRow).queryByRole("button")).toBeNull();

    // 存活机器人行不受影响：详情链接与「预算」入口都在。
    const liveRow = botRows[0];
    expect(
      within(liveRow).getByRole("link", { name: "发布助手" }),
    ).toHaveAttribute("href", "/bots/b1");
    expect(
      within(liveRow).getByRole("button", { name: "设置 发布助手 的预算" }),
    ).toBeInTheDocument();

    // 任务表的「机器人」列同口径：已删除机器人的快照名不链接、弱化，
    // 行内没有预算入口（预算挂在 bot/task 目标上，删除后无从设置）。
    const taskRows = screen.getAllByTestId("usage-task-row");
    expect(taskRows).toHaveLength(2);
    const orphanTaskRow = taskRows[1];
    const orphanBotName = within(orphanTaskRow).getByText("退役机器人");
    expect(orphanBotName.closest("a")).toBeNull();
    expect(orphanBotName).toHaveClass("text-muted-foreground");
    expect(within(orphanTaskRow).queryByRole("button")).toBeNull();
    expect(
      within(taskRows[0]).getByRole("button", {
        name: "设置 每日巡检 的预算",
      }),
    ).toBeInTheDocument();
  });

  it("embedded 已删除机器人：显示快照名与「已删除」徽记", () => {
    mockUsage({
      ...pricedOverview,
      bots: [{ ...pricedOverview.bots[0], deleted: true }],
    });
    renderWithRoute(<BotUsageOverviewPanel botId="b1" embedded />, {
      path: "/bots/b1",
      initialEntry: "/bots/b1",
    });

    // 图例里也有机器人名，断言限定在紧凑拆分区内。
    const section = screen.getByTestId("usage-bots-section");
    expect(within(section).getByText("发布助手")).toHaveClass(
      "text-muted-foreground",
    );
    expect(within(section).getByText("已删除")).toBeInTheDocument();
  });
});
