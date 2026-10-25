import { screen, within } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import type { BotUsageInsights } from "@/lib/api/bots";
import { useBotUsageInsights } from "@/lib/hooks/use-bots";
import { renderWithRoute } from "@/test/render";
import {
  BotUsageInsightsPanel,
  ContextGrowthChart,
  errorCodeLabel,
  selectXLabelIndexes,
  zonedFormatter,
} from "./bot-usage-insights";

vi.mock("@/lib/hooks/use-bots", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/hooks/use-bots")>()),
  useBotUsageInsights: vi.fn(),
}));

const insights: BotUsageInsights = {
  range: "7d",
  timezone: "Asia/Shanghai",
  start: "2026-10-11T16:00:00Z",
  end: "2026-10-18T16:00:00Z",
  cache_hit_by_day: [
    { date: "2026-10-14", rate: 0 },
    { date: "2026-10-15", rate: 0.004 },
    { date: "2026-10-16", rate: 0.4 },
    { date: "2026-10-17", rate: 0.8 },
    { date: "2026-10-18", rate: null },
  ],
  bots: [
    {
      bot_id: "b1",
      name: "发布助手",
      cache_hit_rate: 0.75,
      tools: {
        prompt_tokens: 100000,
        tools_tokens_estimate: 15000,
        share: 0.15,
      },
      maintenance: {
        cost: 0.3,
        share: 0.12,
        by_purpose: [
          { purpose: "summary", cost: 0.2 },
          { purpose: "archive", cost: 0.1 },
        ],
      },
      failures: {
        failed_requests: 3,
        failed_requests_cost: 0.12,
        failed_runs: 1,
        failed_runs_cost: 0.3,
        retried_requests: 2,
        by_error: [
          { error_code: "timeout", requests: 2, cost: 0.08 },
          { error_code: "rate_limit", requests: 1, cost: 0.06 },
          { error_code: "flaky_upstream_9x", requests: 1, cost: 0.01 },
          { error_code: "", requests: 1, cost: 0.02 },
          { error_code: null, requests: 1, cost: 0.04 },
        ],
      },
      context_growth: [
        {
          run_id: "r1",
          at: "2026-10-16T01:00:00Z",
          input_tokens: 8000,
          estimated: false,
          session_kind: "main",
        },
        {
          run_id: "r2",
          at: "2026-10-17T01:00:00Z",
          input_tokens: 12000,
          estimated: true,
          session_kind: "main",
        },
        {
          run_id: "r3",
          at: "2026-10-18T01:00:00Z",
          input_tokens: 15000,
          estimated: false,
          session_kind: "schedule",
        },
      ],
      context_growth_truncated: true,
      model_alternatives: [
        {
          catalog_id: "anthropic",
          model: "claude-opus",
          requests: 10,
          actual_cost: 1.2,
          candidates: [
            { model: "claude-sonnet", cost: 0.3, saving: 0.9 },
            { model: "claude-haiku", cost: 1.5, saving: -0.3 },
          ],
        },
      ],
    },
    {
      bot_id: "b2",
      name: "巡检",
      cache_hit_rate: null,
      tools: { prompt_tokens: 0, tools_tokens_estimate: 0, share: null },
      maintenance: { cost: null, share: null, by_purpose: [] },
      failures: {
        failed_requests: 0,
        failed_requests_cost: null,
        failed_runs: 0,
        failed_runs_cost: null,
        retried_requests: 0,
        by_error: [],
      },
      context_growth: [],
      context_growth_truncated: false,
      model_alternatives: [],
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
      evaluated_runs: 8,
      idle_runs: 3,
      idle_rate: 0.375,
      idle_cost: 0.09,
      silent_runs: 2,
      no_tool_call_runs: 1,
      no_write_call_runs: 3,
      cache_hit_rate: 0.6,
    },
    {
      task_id: "t2",
      bot_id: "b1",
      bot_name: "发布助手",
      name: "",
      deleted: true,
      enabled: false,
      evaluated_runs: 0,
      idle_runs: 0,
      idle_rate: null,
      idle_cost: null,
      silent_runs: 0,
      no_tool_call_runs: 0,
      no_write_call_runs: 0,
      cache_hit_rate: null,
    },
  ],
};

function mockInsights(data: BotUsageInsights | undefined) {
  vi.mocked(useBotUsageInsights).mockReturnValue({
    data,
    isLoading: false,
    isError: false,
    error: null,
    refetch: vi.fn(),
  } as never);
}

function renderPanel(
  props: Parameters<typeof BotUsageInsightsPanel>[0] = { range: "7d" },
) {
  return renderWithRoute(<BotUsageInsightsPanel {...props} />, {
    path: "/bots/usage",
    initialEntry: "/bots/usage",
  });
}

beforeEach(() => {
  mockInsights(insights);
});

describe("BotUsageInsightsPanel", () => {
  it("range/botId 透传给查询", () => {
    renderPanel({ range: "30d", botId: "b1" });
    expect(useBotUsageInsights).toHaveBeenCalledWith("30d", "b1");
  });

  it("七个分区齐全，数值与契约字段一一对应", () => {
    renderPanel();

    for (const testId of [
      "usage-insights-context",
      "usage-insights-cache",
      "usage-insights-tools",
      "usage-insights-maintenance",
      "usage-insights-idle",
      "usage-insights-failures",
      "usage-insights-models",
    ]) {
      expect(screen.getByTestId(testId)).toBeInTheDocument();
    }

    // 工具定义占比：15%（≈15000 / 100000），分母 0 的巡检显示「—」。
    const tools = within(screen.getByTestId("usage-insights-tools"));
    expect(tools.getByText("15%")).toBeInTheDocument();
    expect(tools.getByText("≈15,000 / 100,000")).toBeInTheDocument();
    const toolsRows = tools.getAllByTestId("usage-insights-tools-row");
    expect(within(toolsRows[1]).getAllByText("—")).toHaveLength(2);

    // 维护占比：花费、占比与按用途拆分；全未计价的巡检为「—」。
    const maintenance = within(screen.getByTestId("usage-insights-maintenance"));
    const maintenanceRows = maintenance.getAllByTestId(
      "usage-insights-maintenance-row",
    );
    expect(maintenanceRows[0]).toHaveTextContent("$0.3");
    expect(maintenanceRows[0]).toHaveTextContent("12%");
    expect(maintenanceRows[0]).toHaveTextContent("摘要 $0.2 · 归档 $0.1");
    expect(maintenanceRows[1]).toHaveTextContent("—");

    // 失败与重试浪费：已计价失败浪费、失败运行、重试计数。
    const failures = within(screen.getByTestId("usage-insights-failures"));
    const failureRows = failures.getAllByTestId("usage-insights-failures-row");
    expect(failureRows[0]).toHaveTextContent("$0.12");
    expect(failureRows[0]).toHaveTextContent("2");
    expect(failureRows[1]).toHaveTextContent("—");

    // 按错误码分组：无错误码的失败归「无错误码」一组；旧数据的空串码同样
    // 落占位文案，不渲染成空白单元格。已知码附中文释义（含 title 悬浮），
    // 未知码只显原码、不崩。
    const errorRows = failures.getAllByTestId("usage-insights-error-row");
    expect(errorRows).toHaveLength(5);
    expect(errorRows[0]).toHaveTextContent("timeout");
    expect(errorRows[0]).toHaveTextContent("超时");
    expect(errorRows[1]).toHaveTextContent("rate_limit");
    expect(errorRows[1]).toHaveTextContent("限流");
    expect(
      within(errorRows[1]).getByTitle("rate_limit：限流"),
    ).toBeInTheDocument();
    expect(errorRows[2]).toHaveTextContent("flaky_upstream_9x");
    expect(errorRows[2]).not.toHaveTextContent("限流");
    expect(errorRows[3]).toHaveTextContent("无错误码");
    expect(errorRows[4]).toHaveTextContent("无错误码");
  });

  it("分区说明中文为主、原字段名以括注保留", () => {
    renderPanel();

    expect(
      screen.getByText(/缓存命中 token \/ 输入 token（cached\/prompt），按天/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/按非「跳过」（skipped）终态运行评估/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/重试按重复请求指纹（request_hash）计数/),
    ).toBeInTheDocument();
  });

  it("定时任务空转：口径标题、比率与三条件计数，分母 0 为「—」", () => {
    renderPanel();

    const idle = within(screen.getByTestId("usage-insights-idle"));
    // 口径写进表头 title：非「跳过」（skipped）终态运行。
    expect(idle.getByTitle(/非「跳过」（skipped）的终态运行/)).toBeInTheDocument();
    const rows = idle.getAllByTestId("usage-insights-idle-row");
    expect(rows[0]).toHaveTextContent("3/8");
    expect(within(rows[0]).getByText("38%")).toBeInTheDocument();
    expect(rows[0]).toHaveTextContent("$0.09");
    expect(within(rows[0]).getByText("60%")).toBeInTheDocument();
    expect(within(rows[0]).getByText("2")).toBeInTheDocument();
    // 已删除任务：空转率与花费为「—」（缺失 ≠ 0），名字落「已删除的任务」。
    expect(rows[1]).toHaveTextContent("已删除的任务");
    expect(within(rows[1]).getAllByText("—").length).toBeGreaterThanOrEqual(2);
  });

  it("缓存命中率按天条形：条长按 0–100% 比例画满轨道，缺量天保留行标缺口", () => {
    renderPanel();

    const cache = within(screen.getByTestId("usage-insights-cache"));
    // 右值与 formatCacheHitRate 同口径：真零「0%」、极小非零「<1%」（0.4%
    // 四舍五入会是 0%）、常规「40%」「80%」。
    expect(cache.getByText("0%")).toBeInTheDocument();
    expect(cache.getByText("<1%")).toBeInTheDocument();
    expect(cache.getByText("40%")).toBeInTheDocument();
    expect(cache.getByText("80%")).toBeInTheDocument();

    // 条长 = 命中率 × 轨道宽：80% 与 40% 的条宽成 2:1，与窗口内最大值无关。
    const rows = cache.getAllByTestId("usage-insights-cache-row");
    expect(rows).toHaveLength(5);
    const barWidth = (row: HTMLElement) => {
      const bar = within(row).queryByTestId("usage-insights-cache-bar");
      return bar ? Number(bar.style.width.replace("%", "")) : 0;
    };
    expect(barWidth(rows[2])).toBeCloseTo(40);
    expect(barWidth(rows[3])).toBeCloseTo(80);
    expect(barWidth(rows[3]) / barWidth(rows[2])).toBeCloseTo(2);
    // 极小非零画 0.4% 细条而不是空轨道；真零只留空轨道。
    expect(barWidth(rows[1])).toBeCloseTo(0.4);
    expect(barWidth(rows[0])).toBe(0);

    // 缺 usage 的天：行保留、条不画，轨道内浅灰「无完整数据」、右值「—」。
    expect(within(rows[4]).queryByTestId("usage-insights-cache-bar")).toBeNull();
    expect(within(rows[4]).getByText("无完整数据")).toBeInTheDocument();
    expect(within(rows[4]).getByText("—")).toBeInTheDocument();
    // 脚注仍计数。
    expect(
      cache.getByText("其中 1 天无完整用量数据，未画条形。"),
    ).toBeInTheDocument();
  });

  it("上下文增长：点数与数据一致、估算点有标注、点链接到运行明细", () => {
    renderPanel();

    const points = screen.getAllByTestId("usage-context-point");
    expect(points).toHaveLength(3);
    // 估算点有 data-estimated 标记，实测点没有。
    expect(points[1]).toHaveAttribute("data-estimated", "true");
    expect(points[0]).not.toHaveAttribute("data-estimated");
    expect(points[2]).not.toHaveAttribute("data-estimated");
    // 点可打开对应运行明细。
    expect(points[0]).toHaveAttribute("href", "/bots/usage/runs/r1");
    expect(points[2]).toHaveAttribute("href", "/bots/usage/runs/r3");
    // 图例说明估算值；截断标注可见。
    expect(screen.getByText(/估算值（请求缺 usage/)).toBeInTheDocument();
    expect(screen.getByText("仅最近 200 次")).toBeInTheDocument();
  });

  it("上下文增长图：中文可访问名经 aria-labelledby，链接点带 ≥20px 命中区", () => {
    renderPanel();

    const svg = screen
      .getByTestId("usage-insights-context")
      .querySelector("svg");
    // 整图不再声明 role="img"（会把内部可聚焦的运行明细链接从无障碍树剪掉），
    // 可访问名改由 aria-labelledby 指向图内中文标题。
    expect(svg).not.toHaveAttribute("role");
    const titleId = svg!.getAttribute("aria-labelledby");
    expect(titleId).toBeTruthy();
    expect(document.getElementById(titleId!)).toHaveTextContent("上下文增长");

    // 链接点在可视圆点（r=4）之外有透明命中圆，点击目标 ≥20px。
    const points = screen.getAllByTestId("usage-context-point");
    const hit = points[0].querySelector("circle");
    expect(hit).not.toBeNull();
    expect(parseFloat(hit!.getAttribute("r")!)).toBeGreaterThanOrEqual(10);
  });

  it("上下文增长横轴与点悬浮按服务端时区（Asia/Shanghai）渲染日期", () => {
    renderPanel();

    // r1 = 2026-10-16T01:00:00Z：上海 10/16 09:00；浏览器时区（如纽约）会给
    // 出 10/15 21:00——这里断言的是显式时区结果，与按天缓存柱的日期归属一致。
    const points = screen.getAllByTestId("usage-context-point");
    expect(points[0].getAttribute("aria-label")).toContain("2026/10/16 09:00");
    // 横轴同口径：三天的点在 Shanghai 落在 10/16–10/18。
    const labels = screen
      .getAllByTestId("usage-context-x-label")
      .map((el) => el.textContent);
    expect(labels).toEqual(["10/16", "10/17", "10/18"]);
  });

  it("换模型差价：「不代表效果一样」醒目提示 + 省/贵着色", () => {
    renderPanel();

    const section = screen.getByTestId("usage-insights-models");
    const warning = within(section).getByTestId(
      "usage-insights-models-warning",
    );
    expect(warning).toHaveTextContent("只按内置目录价格重算，不代表效果一样");

    const candidates = screen.getAllByTestId("usage-insights-model-candidate");
    expect(candidates).toHaveLength(2);
    expect(candidates[0]).toHaveTextContent("claude-sonnet$0.3省 $0.9");
    // 差价色在差额 span 上：省钱绿、更贵红。
    expect(
      within(candidates[0]).getByText("省 $0.9").className,
    ).toContain("emerald");
    expect(candidates[1]).toHaveTextContent("claude-haiku$1.50贵 $0.3");
    expect(
      within(candidates[1]).getByText("贵 $0.3").className,
    ).toContain("destructive");
    // 组头：当前模型实际花费与参与请求数。
    expect(screen.getByTestId("usage-insights-model-group")).toHaveTextContent(
      "10 次请求",
    );
  });

  it("embedded 窄栏：宽表换成紧凑清单", () => {
    renderPanel({ range: "7d", botId: "b1", embedded: true });

    // 不渲染任何宽表头。
    expect(screen.queryAllByRole("columnheader")).toHaveLength(0);
    // 工具定义占比紧凑行：名字 + 占比 + 分子/分母。
    const tools = within(screen.getByTestId("usage-insights-tools"));
    expect(tools.getByText("15%")).toBeInTheDocument();
    // share 为 null 的机器人显文字空态，不并排印 ≈0 / 0 的假分子分母。
    expect(tools.getByText("巡检").closest("div")).toHaveTextContent(
      "无完整数据",
    );
    expect(tools.queryByText("≈0 / 0")).toBeNull();
    // 空转紧凑清单：口径 title 与条件计数在同一行说明里。
    const idle = within(screen.getByTestId("usage-insights-idle"));
    expect(idle.getByText(/空转 3\/8/)).toBeInTheDocument();
    expect(idle.getByText(/缓存命中率 60%/)).toBeInTheDocument();
  });

  it("全空数据：各分区显示空态而不是崩", () => {
    mockInsights({
      ...insights,
      cache_hit_by_day: [],
      bots: [
        {
          ...insights.bots[1],
          model_alternatives: [],
        },
      ],
      schedule_tasks: [],
    });
    renderPanel();

    expect(screen.getByText("窗口内没有运行记录")).toBeInTheDocument();
    expect(screen.getByText("窗口内没有完整用量数据")).toBeInTheDocument();
    expect(
      screen.getByText("窗口内没有定时任务运行"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("窗口内没有已计价请求，无法比较模型差价"),
    ).toBeInTheDocument();
  });

  it("缓存命中率全缺：收成单条空态，不逐行铺占位", () => {
    mockInsights({
      ...insights,
      cache_hit_by_day: [
        { date: "2026-10-17", rate: null },
        { date: "2026-10-18", rate: null },
      ],
    });
    renderPanel();

    const cache = within(screen.getByTestId("usage-insights-cache"));
    expect(cache.getByText("窗口内没有完整用量数据")).toBeInTheDocument();
    expect(cache.queryAllByTestId("usage-insights-cache-row")).toHaveLength(0);
    expect(cache.queryByText(/其中 .* 天无完整用量数据/)).toBeNull();
  });
});

describe("errorCodeLabel", () => {
  it("已知码给中文释义（覆盖服务端分类码与通用码），未知码返回 undefined", () => {
    // 服务端分类层（bot_model_errors.go）的产出码。
    expect(errorCodeLabel("provider_rate_limited")).toBe("限流");
    expect(errorCodeLabel("provider_quota_exhausted")).toBe("额度不足");
    expect(errorCodeLabel("provider_auth_failed")).toBe("鉴权失败");
    expect(errorCodeLabel("provider_unavailable")).toBe("服务端错误");
    expect(errorCodeLabel("model_not_found")).toBe("模型不存在");
    expect(errorCodeLabel("model_stream_timeout")).toBe("超时");
    expect(errorCodeLabel("model_request_failed")).toBe("请求失败");
    // 历史/通用码。
    expect(errorCodeLabel("timeout")).toBe("超时");
    expect(errorCodeLabel("github_unauthorized")).toBe("鉴权失败");
    expect(errorCodeLabel("http_5xx")).toBe("服务端错误");
    expect(errorCodeLabel("context_length")).toBe("上下文超限");
    expect(errorCodeLabel("context_uncompressible")).toBe("上下文超限");
    expect(errorCodeLabel("network_error")).toBe("网络错误");
    expect(errorCodeLabel("server_restarted")).toBe("服务重启中断");
    // 未知码：不强行释义，只显原码。
    expect(errorCodeLabel("e40001")).toBeUndefined();
    expect(errorCodeLabel("flaky_upstream_9x")).toBeUndefined();
  });
});

describe("上下文增长图助手", () => {
  it("zonedFormatter：同一时刻按时区归属不同日期，非法时区回退浏览器", () => {
    const at = Date.parse("2026-10-16T01:00:00Z");
    const day = (tz: string) =>
      zonedFormatter(tz, { month: "2-digit", day: "2-digit" });
    // 用户时区 ≠ 浏览器时区的场景：同一时刻上海是 10/16、纽约是 10/15。
    expect(day("Asia/Shanghai")(at)).toBe("10/16");
    expect(day("America/New_York")(at)).toBe("10/15");
    const full = zonedFormatter("America/New_York", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
    });
    expect(full(at)).toBe("2026/10/15 21:00");
    // 时区串非法：回退浏览器时区，不抛异常。
    expect(() => day("Not/AZone")(at)).not.toThrow();
  });

  it("selectXLabelIndexes：按像素间距贪心，末点必保留，同刻只留 1 个", () => {
    // 时间集中：前 8 个点挤在 14px 内。首点标签强制保留——它与最近
    // 已选点（cx=78）间距不足 48px，顶掉那个近邻而不叠字。
    expect(selectXLabelIndexes([64, 66, 68, 70, 72, 74, 76, 78, 696])).toEqual([
      0, 8,
    ]);
    // 间距充足时全部保留。
    expect(selectXLabelIndexes([0, 100, 200, 300])).toEqual([0, 1, 2, 3]);
    // 全部点跨度不足 minGap：唯一标签保留末点（最新端日期优先）。
    expect(selectXLabelIndexes([100, 100, 100])).toEqual([2]);
    expect(selectXLabelIndexes([64, 68, 72])).toEqual([2]);
    expect(selectXLabelIndexes([64])).toEqual([0]);
    expect(selectXLabelIndexes([])).toEqual([]);
  });

  it("横轴标签按像素抽稀渲染：时间集中的点不堆叠，末点标签在末点正下方", () => {
    const base = Date.parse("2026-10-16T00:00:00Z");
    const minute = 60_000;
    // 8 个点挤在头 20 分钟内，最后一个点隔 3 天：按下标抽稀会把 8 个标签
    // 叠进同一片像素，按像素抽稀只保留间距 ≥48px 的。
    const points = [
      ...Array.from({ length: 8 }, (_, i) => ({
        x: base + i * minute,
        y: 1000 + i * 100,
        estimated: false,
        label: `p${i}`,
      })),
      { x: base + 3 * 24 * 60 * minute, y: 9000, estimated: false, label: "p8" },
    ];
    renderWithRoute(
      <ContextGrowthChart
        points={points}
        formatXLabel={(ms) => String(ms)}
        chartTitle="上下文增长"
        titleId="label-thinning-title"
        emptyMessage="无数据"
      />,
    );

    const xs = screen
      .getAllByTestId("usage-context-x-label")
      .map((el) => Number(el.getAttribute("x")));
    expect(xs.length).toBeGreaterThanOrEqual(2);
    for (let i = 1; i < xs.length; i++) {
      expect(xs[i] - xs[i - 1]).toBeGreaterThanOrEqual(48);
    }
    // 末点标签落在末点正下方（chartWidth=720：pad.left+8+usable=696）。
    expect(xs[xs.length - 1]).toBeCloseTo(696, 0);
  });

  it("全部点同刻（xSpan=0）只画 1 个横轴标签", () => {
    const at = Date.parse("2026-10-16T01:00:00Z");
    renderWithRoute(
      <ContextGrowthChart
        points={[
          { x: at, y: 100, estimated: false, label: "a" },
          { x: at, y: 200, estimated: false, label: "b" },
        ]}
        formatXLabel={() => "10/16"}
        chartTitle="上下文增长"
        titleId="same-instant-title"
        emptyMessage="无数据"
      />,
    );
    expect(screen.getAllByTestId("usage-context-x-label")).toHaveLength(1);
  });
});
