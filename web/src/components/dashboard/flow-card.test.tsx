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

describe("FlowCard", () => {
  it("renders bucket badges, cycle list and backlog bar", () => {
    render(
      <MemoryRouter>
        <FlowCard
          flow={{
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
          }}
          points={flowPoints}
        />
      </MemoryRouter>,
    );

    expect(screen.getByTestId("flow-card")).toBeInTheDocument();
    // 恒等式徽章：3+2 == Σopen_issue_count（5）
    expect(screen.getByText("待办 3")).toBeInTheDocument();
    expect(screen.getByText("进行中 2")).toBeInTheDocument();
    expect(screen.getByText("中位 2.5 天 · 3 个")).toBeInTheDocument();
    expect(screen.getByTestId("backlog-age-bar")).toBeInTheDocument();
    expect(screen.getByText("7 天内 2")).toBeInTheDocument();
    expect(screen.getByText("30 天以上 1")).toBeInTheDocument();
  });

  it("shows 暂不可用 for a degraded block instead of zeroed numbers", () => {
    render(
      <MemoryRouter>
        <FlowCard
          flow={{
            complete: false,
            todo_count: 0,
            in_progress_count: 0,
            daily_created: dailyCreated,
            cycle_time_by_project: [],
            backlog_age: { under_7d: 0, d7_to_30d: 0, over_30d: 0 },
          }}
          points={[]}
        />
      </MemoryRouter>,
    );

    const card = screen.getByTestId("flow-card");
    expect(card).toHaveTextContent("暂不可用");
    // 降级时零值徽章不渲染，避免被读成真实读数
    expect(screen.queryByText("待办 0")).not.toBeInTheDocument();
    expect(screen.queryByTestId("backlog-age-bar")).not.toBeInTheDocument();
  });
});
