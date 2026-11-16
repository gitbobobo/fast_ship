import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { FlowCard } from "@/components/dashboard/flow-card";
import type { MetricChartSeriesPoint } from "@/components/dashboard/metric-chart";

const dailyCreated = Array.from({ length: 30 }, (_, index) => ({
  date: `2026-05-${String(index + 1).padStart(2, "0")}`,
  created_count: index === 29 ? 4 : 0,
}));

const flowPoints: MetricChartSeriesPoint[] = dailyCreated.map((point, index) => {
  const resolved = index === 28 ? 1 : 0;
  return {
    label: point.date,
    shortLabel: point.date.slice(5),
    value: point.created_count + resolved,
    series: [
      { id: "created", name: "新建", value: point.created_count },
      { id: "resolved", name: "已解决", value: resolved },
    ],
  };
});

const completeFlow = {
  todo_count: 3,
  in_progress_count: 2,
  daily_created: dailyCreated,
  cycle_time_by_project: [
    {
      project_id: "p1",
      project_name: "Alpha App",
      median_days: 2.5,
      completed_count: 3,
    },
  ],
  backlog_age: { under_7d: 2, d7_to_30d: 2, over_30d: 1 },
};

describe("FlowCard", () => {
  it("renders meta counts, cycle list, series legend and backlog bar", () => {
    render(
      <MemoryRouter>
        <FlowCard flow={completeFlow} points={flowPoints} />
      </MemoryRouter>,
    );

    expect(screen.getByTestId("flow-card")).toBeInTheDocument();
    // 恒等式：3+2 == Σopen_issue_count（5）；灰底药丸改纯文本元信息（W5）
    expect(screen.getByText("待办 3 · 进行中 2")).toBeInTheDocument();
    // 中位补名词「个问题」（W3）；≥1 天保持一位小数
    expect(screen.getByText("中位 2.5 天 · 3 个问题")).toBeInTheDocument();
    // 双序列图例（W1）：新建/已解决 色点与面积图两条序列同色
    expect(screen.getByText("新建")).toBeInTheDocument();
    expect(screen.getByText("已解决")).toBeInTheDocument();
    expect(screen.getByTestId("backlog-age-bar")).toBeInTheDocument();
    expect(screen.getByText("7 天内 2")).toBeInTheDocument();
    expect(screen.getByText("30 天以上 1")).toBeInTheDocument();
  });

  it("mirrors the tier colors between bar segments and label dots (W4)", () => {
    render(
      <MemoryRouter>
        <FlowCard
          flow={{
            ...completeFlow,
            backlog_age: { under_7d: 3, d7_to_30d: 0, over_30d: 0 },
          }}
          points={flowPoints}
        />
      </MemoryRouter>,
    );

    const bar = screen.getByTestId("backlog-age-bar");
    // 只有 7 天内有值：条内 1 段 + 标签前 1 个同色圆点（emerald）
    expect(bar.querySelectorAll('[role="progressbar"]')).toHaveLength(1);
    expect(bar.querySelectorAll('[class*="bg-emerald-500"]')).toHaveLength(2);
    // 无值档的标签色点保留，充当图例（amber / red 各 1 个圆点）
    expect(bar.querySelectorAll('[class*="bg-amber-500"]')).toHaveLength(1);
    expect(bar.querySelectorAll('[class*="bg-red-500"]')).toHaveLength(1);
    expect(screen.getByText("7 天内 3")).toBeInTheDocument();
    expect(screen.getByText("7-30 天 0")).toBeInTheDocument();
    expect(screen.getByText("30 天以上 0")).toBeInTheDocument();
  });

  it("formats cycle medians with adaptive units and 问题 noun (W3)", () => {
    render(
      <MemoryRouter>
        <FlowCard
          flow={{
            ...completeFlow,
            cycle_time_by_project: [
              {
                project_id: "p1",
                project_name: "A",
                median_days: 0.02, // 0.48 小时
                completed_count: 1,
              },
              {
                project_id: "p2",
                project_name: "B",
                median_days: 0.125, // 3 小时
                completed_count: 2,
              },
              {
                project_id: "p3",
                project_name: "C",
                median_days: 1 / 24, // 恰好 1 小时（小时档下界）
                completed_count: 3,
              },
              {
                project_id: "p4",
                project_name: "D",
                median_days: 1, // 天档下界
                completed_count: 4,
              },
            ],
          }}
          points={flowPoints}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("中位 <1 小时 · 1 个问题")).toBeInTheDocument();
    expect(screen.getByText("中位 3 小时 · 2 个问题")).toBeInTheDocument();
    expect(screen.getByText("中位 1 小时 · 3 个问题")).toBeInTheDocument();
    expect(screen.getByText("中位 1.0 天 · 4 个问题")).toBeInTheDocument();
  });

  it("hides the series legend when the chart degrades to an empty state (W1)", () => {
    // 图转空状态时图例不能留在空态上方：先全零序列
    const zeroPoints = flowPoints.map((point) => ({
      ...point,
      value: 0,
      series: [],
    }));
    const { unmount } = render(
      <MemoryRouter>
        <FlowCard
          flow={{ ...completeFlow, todo_count: 0, in_progress_count: 0 }}
          points={zeroPoints}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("近 30 天暂无新建或已解决")).toBeInTheDocument();
    expect(screen.queryByText("新建")).not.toBeInTheDocument();
    unmount();

    // 降级块连图表都不渲染，图例同样不出现
    render(
      <MemoryRouter>
        <FlowCard
          flow={{ ...completeFlow, complete: false }}
          points={[]}
        />
      </MemoryRouter>,
    );

    expect(screen.getByTestId("flow-card")).toHaveTextContent("暂不可用");
    expect(screen.queryByText("新建")).not.toBeInTheDocument();
    // 降级时零值徽章不渲染，避免被读成真实读数
    expect(screen.queryByText("待办 0 · 进行中 0")).not.toBeInTheDocument();
    expect(screen.queryByTestId("backlog-age-bar")).not.toBeInTheDocument();
  });
});
