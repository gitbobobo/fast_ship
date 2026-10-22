import { render, screen } from "@testing-library/react";
import { MetricChart } from "@/components/dashboard/metric-chart";

describe("MetricChart", () => {
  it("shows a single empty state instead of a chart when there are no projects", () => {
    render(
      <MetricChart points={[]} variant="bar" emptyMessage="暂无已配置项目" />,
    );

    expect(screen.getByText("暂无已配置项目")).toBeInTheDocument();
    expect(screen.queryByText("暂无项目")).not.toBeInTheDocument();
  });

  it("keeps zero-valued project rows visible instead of an empty state", () => {
    render(
      <MetricChart
        points={[
          { id: "p1", label: "Alpha", value: 0 },
          { id: "p2", label: "Beta", value: 0 },
        ]}
        variant="bar"
        emptyMessage="暂无已配置项目"
      />,
    );

    // 有项目但都清零时不应该退化成空状态：项目名仍要能看到。
    expect(screen.queryByText("暂无已配置项目")).not.toBeInTheDocument();
    expect(screen.getByText("Alpha")).toBeInTheDocument();
    expect(screen.getByText("Beta")).toBeInTheDocument();
  });

  it("shows only the empty state when the 30-day series is all zeros", () => {
    render(
      <MetricChart
        points={Array.from({ length: 30 }, (_, index) => ({
          label: `2026-05-${String(index + 1).padStart(2, "0")}`,
          shortLabel: `05-${String(index + 1).padStart(2, "0")}`,
          value: 0,
          series: [],
        }))}
        variant="area"
        emptyMessage="暂无已解决问题数据"
      />,
    );

    expect(screen.getByText("暂无已解决问题数据")).toBeInTheDocument();
    expect(
      screen.queryByRole("img", { name: "stacked area chart" }),
    ).not.toBeInTheDocument();
  });

  it("uses distinct integer axis ticks when the daily maximum is 1", () => {
    render(
      <MetricChart
        points={[
          {
            label: "2026-05-11",
            shortLabel: "05-11",
            value: 1,
            series: [{ id: "p1", name: "Alpha", value: 1 }],
          },
          {
            label: "2026-05-12",
            shortLabel: "05-12",
            value: 0,
            series: [],
          },
        ]}
        variant="area"
        emptyMessage="暂无已解决问题数据"
      />,
    );

    expect(
      screen.getByRole("img", { name: "stacked area chart" }),
    ).toBeInTheDocument();
    for (const tick of ["0", "1", "2", "3", "4"]) {
      expect(screen.getAllByText(tick)).toHaveLength(1);
    }
  });
});
