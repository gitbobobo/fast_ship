import { screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { BotUsageRunDetail } from "@/lib/api/bots";
import { useBotUsageRun } from "@/lib/hooks/use-bots";
import { renderWithRoute } from "@/test/render";
import BotUsageRunPage from "@/routes/bots/usage/runs/$runId";

vi.mock("@/lib/hooks/use-bots", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/hooks/use-bots")>()),
  useBotUsageRun: vi.fn(),
}));

const detail: BotUsageRunDetail = {
  run: {
    id: "run-1",
    bot_id: "b1",
    bot_name: "发布助手",
    session_kind: "schedule",
    schedule_task_id: "t1",
    schedule_task_name: "每日巡检",
    trigger_source: "schedule",
    trigger_key: "t1",
    status: "completed",
    summary: "巡检完成",
    silent: false,
    prompt_tokens: 12000,
    completion_tokens: 800,
    total_tokens: 12800,
    cached_tokens: 6000,
    usage_complete: true,
    cost_estimate: 0.4,
    cost_source: "catalog",
    maintenance_cost: 0.05,
    display_cost: 0.45,
    created_at: "2026-10-18T01:00:00Z",
    started_at: "2026-10-18T01:00:01Z",
    finished_at: "2026-10-18T01:00:41Z",
  },
  requests: [
    {
      id: "req-1",
      ordinal: 1,
      status: "failed",
      is_retry: false,
      input_through_seq: 5,
      input_tokens_estimate: 11000,
      input_estimator: "usage_delta/3.5",
      prompt_tokens: null,
      cached_tokens: null,
      completion_tokens: null,
      total_tokens: null,
      tools_tokens_estimate: 400,
      usage_complete: false,
      cost_estimate: null,
      cost_source: null,
      started_at: "2026-10-18T01:00:01Z",
      finished_at: "2026-10-18T01:00:05Z",
      error_code: "timeout",
      error_message: "上游超时",
      tool_calls: [],
    },
    {
      id: "req-2",
      ordinal: 2,
      status: "succeeded",
      is_retry: true,
      input_through_seq: 5,
      input_tokens_estimate: 11000,
      input_estimator: "usage_delta/3.5",
      prompt_tokens: 12000,
      cached_tokens: 6000,
      completion_tokens: 800,
      total_tokens: 12800,
      tools_tokens_estimate: 400,
      usage_complete: true,
      cost_estimate: 0.4,
      cost_source: "catalog",
      started_at: "2026-10-18T01:00:06Z",
      finished_at: "2026-10-18T01:00:30Z",
      tool_calls: [
        {
          id: "call-1",
          tool_name: "fs_issue",
          action: "update",
          status: "succeeded",
          created_at: "2026-10-18T01:00:10Z",
          finished_at: "2026-10-18T01:00:12Z",
        },
      ],
    },
  ],
  requests_truncated: false,
  maintenance_runs: [
    {
      id: "m-1",
      kind: "compaction",
      status: "succeeded",
      created_at: "2026-10-18T01:00:31Z",
      started_at: "2026-10-18T01:00:31Z",
      finished_at: "2026-10-18T01:00:38Z",
      cost_estimate: 0.05,
      requests: [
        {
          id: "mreq-1",
          ordinal: 1,
          purpose: "summary",
          model: "gpt-test",
          status: "succeeded",
          usage_complete: true,
          prompt_tokens: 9000,
          cached_tokens: 0,
          completion_tokens: 300,
          total_tokens: 9300,
          cost_estimate: 0.05,
          cost_source: "catalog",
          started_at: "2026-10-18T01:00:31Z",
          finished_at: "2026-10-18T01:00:38Z",
        },
      ],
    },
  ],
};

function mockRun(data: BotUsageRunDetail | undefined) {
  vi.mocked(useBotUsageRun).mockReturnValue({
    data,
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  } as never);
}

function renderPage() {
  return renderWithRoute(<BotUsageRunPage />, {
    path: "/bots/usage/runs/:runId",
    initialEntry: "/bots/usage/runs/run-1",
  });
}

describe("BotUsageRunPage", () => {
  it("run 摘要：display_cost 与自身/维护拆分、任务与触发", () => {
    mockRun(detail);
    renderPage();

    expect(useBotUsageRun).toHaveBeenCalledWith("run-1");
    const summary = screen.getByTestId("usage-run-summary");
    expect(
      within(summary).getByRole("link", { name: "发布助手" }),
    ).toHaveAttribute("href", "/bots/b1");
    expect(summary).toHaveTextContent("定时任务");
    expect(summary).toHaveTextContent("每日巡检");
    expect(summary).toHaveTextContent("$0.45");
    // 拆分口径：自身 + 维护（formatCostEstimate 分位段剥尾零：$0.4）。
    expect(summary).toHaveTextContent("自身 $0.4 + 维护 $0.05");
  });

  it("请求列表：失败重试都列出，重试打标、错误可见、工具调用挂在对应请求下", () => {
    mockRun(detail);
    renderPage();

    const rows = screen.getAllByTestId("usage-request-row");
    expect(rows).toHaveLength(2);
    // 失败的第一次请求：usage 未收齐、未计价「—」、错误码与消息。
    expect(within(rows[0]).getByText("已失败")).toBeInTheDocument();
    expect(within(rows[0]).getByText(/上游超时/)).toBeInTheDocument();
    expect(within(rows[0]).getByText(/usage 未收齐/)).toBeInTheDocument();
    // 第二次是同输入的重试。
    expect(within(rows[1]).getByText("重试")).toBeInTheDocument();
    expect(within(rows[1]).getByText("$0.4")).toBeInTheDocument();
    // 工具调用显示的是本地化动作标签（title 保留原始 tool:action 便于排查）。
    const toolLabel = within(rows[1]).getByText("更新问题");
    expect(toolLabel).toHaveAttribute("title", "fs_issue:update");
  });

  it("关联维护：上下文压缩运行与其请求（purpose/model/成本）", () => {
    mockRun(detail);
    renderPage();

    const run = screen.getByTestId("usage-maintenance-run");
    expect(run).toHaveTextContent("上下文压缩");
    expect(run).toHaveTextContent("$0.05");
    expect(run).toHaveTextContent("摘要");
    expect(run).toHaveTextContent("gpt-test");
  });

  it("未计价 run：display_cost 与请求成本都显示「—」", () => {
    mockRun({
      ...detail,
      run: {
        ...detail.run,
        usage_complete: false,
        cost_estimate: null,
        maintenance_cost: null,
        display_cost: null,
      },
      requests: [
        { ...detail.requests[1], cost_estimate: null, is_retry: false },
      ],
      maintenance_runs: [],
    });
    renderPage();

    const summary = screen.getByTestId("usage-run-summary");
    // 「—」出现在 display_cost 与拆分处；不能出现 $0。
    expect(summary.textContent).not.toContain("$0");
    expect(screen.queryByTestId("usage-maintenance-run")).toBeNull();
  });

  it("输入上下文增长：点数=请求数，缺 usage 的请求标估算", () => {
    mockRun(detail);
    renderPage();

    const section = screen.getByTestId("usage-run-context-growth");
    const points = within(section).getAllByTestId("usage-context-point");
    expect(points).toHaveLength(2);
    // #1 缺 usage（prompt_tokens=null）→ 取估算输入并标估算；#2 实测。
    expect(points[0]).toHaveAttribute("data-estimated", "true");
    expect(points[1]).not.toHaveAttribute("data-estimated");
    expect(
      within(section).getByText(/空心点 = 估算值/),
    ).toBeInTheDocument();
    // 无链接点声明成图片并带可访问名（此前只有悬浮 <title>）。
    expect(points[0]).toHaveAttribute("role", "img");
    expect(points[0].getAttribute("aria-label")).toMatch(/^#1 · 11,000 输入/);
  });

  it("输入上下文增长图：中文可访问名经 aria-labelledby，svg 无 role=img", () => {
    mockRun(detail);
    renderPage();

    const svg = screen
      .getByTestId("usage-run-context-growth")
      .querySelector("svg");
    expect(svg).not.toHaveAttribute("role");
    const titleId = svg!.getAttribute("aria-labelledby");
    expect(titleId).toBeTruthy();
    expect(document.getElementById(titleId!)).toHaveTextContent(
      "输入上下文增长",
    );
  });

  it("请求截断时上下文图标注仅最新 500 条", () => {
    mockRun({ ...detail, requests_truncated: true });
    renderPage();

    expect(screen.getByTestId("usage-run-context-growth")).toHaveTextContent(
      "仅最新 500 条",
    );
  });

  it("供应商返回金额：provider_cost 有值时打徽记并替代估算", () => {
    mockRun({
      ...detail,
      requests: [
        {
          ...detail.requests[1],
          provider_cost: 0.0134,
          cost_estimate: 0.4,
        },
      ],
      maintenance_runs: [
        {
          ...detail.maintenance_runs[0],
          requests: [
            {
              ...detail.maintenance_runs[0].requests[0],
              provider_cost: 0.05,
              cost_estimate: 0.06,
            },
          ],
        },
      ],
    });
    renderPage();

    // 主线请求行：徽记 + 实际金额，估算 0.4 不再出现。
    const rows = screen.getAllByTestId("usage-request-row");
    const cost = within(rows[0]).getByTestId("usage-provider-cost");
    expect(within(cost).getByText("供应商返回")).toBeInTheDocument();
    expect(cost).toHaveTextContent("$0.0134");
    expect(rows[0].textContent).not.toContain("$0.4");
    // 维护请求行同口径。
    const maintRun = screen.getByTestId("usage-maintenance-run");
    expect(maintRun).toHaveTextContent("供应商返回");
    expect(maintRun).toHaveTextContent("$0.05");
  });

  it("推理 token：有值显示在输出括注里，缺失显示「—」", () => {
    mockRun({
      ...detail,
      requests: [
        { ...detail.requests[0], usage_complete: true, prompt_tokens: 1000, cached_tokens: 0, completion_tokens: 200, total_tokens: 1200 },
        { ...detail.requests[1], reasoning_tokens: 512 },
      ],
      maintenance_runs: [
        {
          ...detail.maintenance_runs[0],
          requests: [
            { ...detail.maintenance_runs[0].requests[0], reasoning_tokens: null },
          ],
        },
      ],
    });
    renderPage();

    const rows = screen.getAllByTestId("usage-request-row");
    // 缺失（null）→「—」，不是 0。
    expect(rows[0]).toHaveTextContent("输出 200（含推理 —）");
    // 有值 → 数字。
    expect(rows[1]).toHaveTextContent("输出 800（含推理 512）");
    // 维护请求行缺失同样显示「—」。
    const maintRun = screen.getByTestId("usage-maintenance-run");
    expect(maintRun).toHaveTextContent("输出 300（含推理 —）");
  });
});
