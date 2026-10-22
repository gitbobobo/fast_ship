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
    const { container } = render(
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
    // 值为 0 时不留填充段，避免被误读成「有少量进度」。
    expect(container.querySelectorAll('[class*="bg-primary/"]')).toHaveLength(0);
  });

  it("draws a fill only for rows with a positive value", () => {
    const { container } = render(
      <MetricChart
        points={[
          { id: "p1", label: "Alpha", value: 0 },
          { id: "p2", label: "Beta", value: 3 },
        ]}
        variant="bar"
        emptyMessage="暂无已配置项目"
      />,
    );

    expect(container.querySelectorAll('[class*="bg-primary/"]')).toHaveLength(1);
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

  it("scales the axis to two integer ticks when the maximum is 1 or 2", () => {
    const renderWithMax = (max: number) => {
      const { unmount } = render(
        <MetricChart
          points={[
            {
              label: "2026-05-11",
              shortLabel: "05-11",
              value: max,
              series: [{ id: "p1", name: "Alpha", value: max }],
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
      return unmount;
    };

    for (const max of [1, 2]) {
      const unmount = renderWithMax(max);
      expect(
        screen.getByRole("img", { name: "stacked area chart" }),
      ).toBeInTheDocument();
      for (const tick of ["0", "1", "2"]) {
        expect(screen.getAllByText(tick)).toHaveLength(1);
      }
      expect(screen.queryByText("4")).not.toBeInTheDocument();
      unmount();
    }
  });

  it("uses a four-interval integer ladder for larger maximums", () => {
    const { unmount } = render(
      <MetricChart
        points={[
          {
            label: "2026-05-11",
            shortLabel: "05-11",
            value: 5,
            series: [{ id: "p1", name: "Alpha", value: 5 }],
          },
        ]}
        variant="area"
        emptyMessage="暂无已解决问题数据"
      />,
    );

    for (const tick of ["0", "2", "4", "6", "8"]) {
      expect(screen.getAllByText(tick)).toHaveLength(1);
    }
    expect(screen.queryByText("10")).not.toBeInTheDocument();
    unmount();
  });

  it("keeps the width observer attached across the empty → data transition", () => {
    const observed: Element[] = [];
    class FakeResizeObserver {
      observe(target: Element) {
        observed.push(target);
      }
      unobserve() {}
      disconnect() {}
    }
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);

    const emptyPoints = Array.from({ length: 30 }, (_, index) => ({
      label: `2026-05-${String(index + 1).padStart(2, "0")}`,
      shortLabel: `05-${String(index + 1).padStart(2, "0")}`,
      value: 0,
      series: [],
    }));
    const { rerender } = render(
      <MetricChart
        points={emptyPoints}
        variant="area"
        emptyMessage="暂无已解决问题数据"
      />,
    );

    // 空状态时测量容器也必须在树上；否则挂载后再来数据时 ResizeObserver 不会重绑，
    // chartWidth 会停在默认值，viewBox 缩放与横轴对齐问题就会回归。
    expect(screen.getByText("暂无已解决问题数据")).toBeInTheDocument();
    expect(observed).toHaveLength(1);

    rerender(
      <MetricChart
        points={[
          {
            label: "2026-05-30",
            shortLabel: "05-30",
            value: 2,
            series: [{ id: "p1", name: "Alpha", value: 2 }],
          },
        ]}
        variant="area"
        emptyMessage="暂无已解决问题数据"
      />,
    );

    expect(
      screen.getByRole("img", { name: "stacked area chart" }),
    ).toBeInTheDocument();
    expect(observed).toHaveLength(1);

    vi.unstubAllGlobals();
  });
});
