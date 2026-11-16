import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import {
  MetricChart,
  type MetricChartSeriesPoint,
} from "@/components/dashboard/metric-chart";

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

  it("renders rows with href as links", () => {
    render(
      <MemoryRouter>
        <MetricChart
          points={[
            {
              id: "p1",
              label: "Alpha",
              value: 3,
              href: "/issues?project=p1&state=open",
            },
            { id: "p2", label: "Beta", value: 0 },
          ]}
          variant="bar"
          emptyMessage="暂无已配置项目"
        />
      </MemoryRouter>,
    );

    const link = screen.getByRole("link", { name: /Alpha/ });
    expect(link).toHaveAttribute("href", "/issues?project=p1&state=open");
    // 无 href 的行保持普通行，不出链接。
    expect(screen.queryByRole("link", { name: /Beta/ })).not.toBeInTheDocument();
  });

  it("collapses rows beyond maxRows into a non-clickable summary line", () => {
    const points = Array.from({ length: 12 }, (_, index) => ({
      id: `p${index}`,
      label: `Project ${index}`,
      value: index === 11 ? 4 : 1,
      href: `/issues?project=p${index}&state=open`,
    }));

    render(
      <MemoryRouter>
        <MetricChart
          points={points}
          variant="bar"
          emptyMessage="暂无已配置项目"
          maxRows={10}
          collapsedLabel={(hidden) =>
            `其他 ${hidden.length} 个项目共 ${hidden.reduce(
              (sum, p) => sum + p.value,
              0,
            )} 条`
          }
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("其他 2 个项目共 5 条")).toBeInTheDocument();
    // 前 10 行仍是链接，被折掉的行不出现。
    expect(screen.getAllByRole("link")).toHaveLength(10);
    expect(screen.queryByText("Project 10")).not.toBeInTheDocument();
    expect(screen.queryByText("Project 11")).not.toBeInTheDocument();
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

  it("spaces date labels with a constant stride from the last point", () => {
    const points = Array.from({ length: 27 }, (_, index) => ({
      label: `2026-05-${String(index + 1).padStart(2, "0")}`,
      shortLabel: `D${index}`,
      value: index === 26 ? 2 : 0,
      series:
        index === 26 ? [{ id: "p1", name: "Alpha", value: 2 }] : [],
    }));

    const { container } = render(
      <MetricChart
        points={points}
        variant="area"
        emptyMessage="暂无已解决问题数据"
      />,
    );

    const labels = Array.from(container.querySelectorAll("svg text"))
      .map((node) => node.textContent ?? "")
      .filter((text) => text.startsWith("D"));

    // 从 26 往前每 5 个点取一个，间隔恒为 5 天；首点因此没有标签。
    expect(labels).toEqual(["D1", "D6", "D11", "D16", "D21", "D26"]);
  });

  it("drops an open tooltip when the series becomes empty", () => {
    const points = (values: number[]) =>
      values.map((value, index) => ({
        label: `2026-05-0${index + 1}`,
        shortLabel: `D${index}`,
        value,
        series: value ? [{ id: "p1", name: "Alpha", value }] : [],
      }));

    const { container, rerender } = render(
      <MetricChart
        points={points([0, 0, 1])}
        variant="area"
        emptyMessage="暂无已解决问题数据"
      />,
    );
    const svg = container.querySelector('svg[aria-label="stacked area chart"]');
    expect(svg).not.toBeNull();
    fireEvent.mouseMove(svg as Element, { clientX: 120, clientY: 40 });
    // 测试环境没有布局信息，最近点落回第一个点；这里只关心浮层出现了。
    expect(screen.getByText("2026-05-01")).toBeInTheDocument();

    // 悬浮中数据被刷新成全零：图表转为空状态，浮层不能留在页面上。
    rerender(
      <MetricChart
        points={points([0, 0, 0])}
        variant="area"
        emptyMessage="暂无已解决问题数据"
      />,
    );

    expect(screen.queryByText("2026-05-01")).not.toBeInTheDocument();
    expect(screen.getByText("暂无已解决问题数据")).toBeInTheDocument();
  });

  it("insets the last area point from the plot's right edge (W10)", () => {
    const points = Array.from({ length: 5 }, (_, index) => ({
      label: `2026-05-0${index + 1}`,
      shortLabel: `D${index}`,
      value: index + 1,
      series: [{ id: "s1", name: "Alpha", value: index + 1 }],
    }));

    const { container } = render(
      <MetricChart
        points={points}
        variant="area"
        emptyMessage="暂无已解决问题数据"
      />,
    );

    const path = container.querySelector("svg path");
    expect(path).not.toBeNull();
    // 面积路径坐标成对出现（x,y）：取最大 x 即末点。
    const numbers = (path!.getAttribute("d") ?? "").match(/[\d.]+/g) ?? [];
    const xs = numbers
      .map(Number)
      .filter((_, i) => i % 2 === 0);
    // 测试环境无布局，chartWidth 用默认 720：绘图区右缘 = 720-40 = 680，
    // 末点必须内收 12px（= 668，round4 X7 由 6px 提到 12px），与右边框脱开，
    // 也给线末数值标签留出落脚空间。
    expect(Math.max(...xs)).toBe(668);
    expect(Math.max(...xs)).toBeLessThan(680);
  });

  it("keeps columns-variant coordinates unchanged by the area end inset (W10)", () => {
    const points = Array.from({ length: 5 }, (_, index) => ({
      label: `2026-05-0${index + 1}`,
      shortLabel: `D${index}`,
      value: 1,
      series: [{ id: "s1", name: "Alpha", value: 1 }],
    }));

    const { container } = render(
      <MetricChart
        points={points}
        variant="columns"
        emptyMessage="窗口内没有已计价花费"
      />,
    );

    // columns 按槽位居中（bot 用量页零变化）：末柱中心 = 40 + (640/5)×4.5 = 616。
    const rects = container.querySelectorAll("svg rect");
    expect(rects.length).toBeGreaterThan(0);
    const last = rects[rects.length - 1];
    const center =
      Number(last.getAttribute("x")) + Number(last.getAttribute("width")) / 2;
    expect(center).toBeCloseTo(616, 5);
  });

  /* ─── round4 X 系列 ─── */

  // 两天数据：created 4/4、resolved 0/4。叠加模式下 y 轴按各系列值的最大者
  // （4）缩放：chartMax=4，getY(4)=24、getY(0)=210；若仍按堆叠画，maxTotal=8，
  // 首点顶边 y 应为 117——下面的断言能区分两种模式。
  const overlayPoints: MetricChartSeriesPoint[] = [
    {
      label: "2026-05-01",
      shortLabel: "D1",
      value: 4,
      series: [
        { id: "created", name: "新建", value: 4 },
        { id: "resolved", name: "已解决", value: 0 },
      ],
    },
    {
      label: "2026-05-02",
      shortLabel: "D2",
      value: 8,
      series: [
        { id: "created", name: "新建", value: 4 },
        { id: "resolved", name: "已解决", value: 4 },
      ],
    },
  ];

  it("draws overlay polylines with per-series y instead of stacked areas (X1)", () => {
    const { container } = render(
      <MetricChart
        points={overlayPoints}
        variant="area"
        stacked={false}
        emptyMessage="暂无"
      />,
    );

    // 两条独立折线：各自 y=自身值，而不是堆叠累计顶边（顶边会让人把
    // 「已解决」读成 KPI 的数倍）。
    const created = container.querySelector('path[data-series="created"]');
    const resolved = container.querySelector('path[data-series="resolved"]');
    expect(created).not.toBeNull();
    expect(resolved).not.toBeNull();
    expect(created).toHaveAttribute("d", "M 40 24 L 668 24");
    expect(resolved).toHaveAttribute("d", "M 40 210 L 668 24");
    // 线型区分（X1）：首序列（新建）细虚线，已解决实线——末段重合仍能分辨。
    expect(created).toHaveAttribute("stroke-dasharray");
    expect(resolved?.getAttribute("stroke-dasharray")).toBeNull();
    // 叠加模式不再渲染堆叠面积层。
    expect(container.querySelector('path[fill^="url(#grad-"]')).toBeNull();
    // aria 语义与模式一致。
    expect(
      container.querySelector('svg[aria-label="overlay line chart"]'),
    ).not.toBeNull();
  });

  it("uses the requested color slot for a single series (X2)", () => {
    const single = [
      {
        label: "2026-05-01",
        shortLabel: "D1",
        value: 2,
        series: [{ id: "total", name: "已解决", value: 2 }],
      },
      { label: "2026-05-02", shortLabel: "D2", value: 0, series: [] },
    ];

    // 「每日已解决」传 colorIndex=1：绿色，与流转效率卡的已解决同色。
    const first = render(
      <MetricChart
        points={single}
        variant="area"
        colorIndex={1}
        emptyMessage="暂无"
      />,
    );
    expect(first.container.querySelector("path[stroke]")).toHaveAttribute(
      "stroke",
      "oklch(0.65 0.18 150)",
    );
    first.unmount();

    // 默认仍第 0 槽（蓝），不传 colorIndex 的调用点零变化。
    const second = render(
      <MetricChart points={single} variant="area" emptyMessage="暂无" />,
    );
    expect(second.container.querySelector("path[stroke]")).toHaveAttribute(
      "stroke",
      "oklch(0.65 0.18 250)",
    );
    second.unmount();
  });

  it("keeps multi-series colors in first-seen order regardless of colorIndex (X2)", () => {
    const { container } = render(
      <MetricChart
        points={overlayPoints}
        variant="area"
        stacked={false}
        colorIndex={4}
        emptyMessage="暂无"
      />,
    );

    // colorIndex 只对单系列生效：新建仍蓝、已解决仍绿（bot 用量页多系列不受影响）。
    expect(container.querySelector('path[data-series="created"]')).toHaveAttribute(
      "stroke",
      "oklch(0.65 0.18 250)",
    );
    expect(container.querySelector('path[data-series="resolved"]')).toHaveAttribute(
      "stroke",
      "oklch(0.65 0.18 150)",
    );
  });

  it("labels each overlay line's last value staggered without overlap or overflow (X3)", () => {
    const { container } = render(
      <MetricChart
        points={overlayPoints}
        variant="area"
        stacked={false}
        emptyMessage="暂无"
      />,
    );

    // 线末直接标数值：两条标签都存在，色随线。
    const labels = Array.from(
      container.querySelectorAll("svg text[data-series]"),
    );
    expect(labels.map((n) => n.textContent)).toEqual(["4", "4"]);
    expect(labels[0]).toHaveAttribute("fill", "oklch(0.65 0.18 250)");
    expect(labels[1]).toHaveAttribute("fill", "oklch(0.65 0.18 150)");
    expect(labels[0].getAttribute("class")).toContain("tabular-nums");

    // 末点右上/右下错开：两标签 y 至少差一个字号（11px），不重叠。
    const ys = labels.map((node) => Number(node.getAttribute("y")));
    expect(Math.abs(ys[0] - ys[1])).toBeGreaterThanOrEqual(11);

    // 不越界：标签画在末点（内收后 x=668）右侧，仍在 viewBox（宽 720）内。
    const xs = labels.map((node) => Number(node.getAttribute("x")));
    for (const x of xs) {
      expect(x + 14).toBeLessThan(720);
    }
  });

  it("hides the overlay end labels and tooltip total in stacked mode (X1/X3)", () => {
    // 堆叠面积图（含每日已解决）不画线末数值标签；tooltip 保留「总计」。
    const { container } = render(
      <MetricChart points={overlayPoints} variant="area" emptyMessage="暂无" />,
    );

    expect(container.querySelectorAll("svg text[data-series]")).toHaveLength(0);
    const svg = container.querySelector('svg[aria-label="stacked area chart"]');
    expect(svg).not.toBeNull();
    fireEvent.mouseMove(svg as Element, { clientX: 120, clientY: 40 });
    expect(screen.getByText("总计")).toBeInTheDocument();
  });

  it("prints the last-point axis label under the final area point (X7)", () => {
    const points = [
      {
        label: "2026-05-01",
        shortLabel: "D1",
        value: 2,
        series: [{ id: "s1", name: "Alpha", value: 2 }],
      },
      {
        label: "2026-05-02",
        shortLabel: "D2",
        value: 3,
        series: [{ id: "s1", name: "Alpha", value: 3 }],
      },
    ];

    const { container } = render(
      <MetricChart
        points={points}
        variant="area"
        lastPointLabel="今日"
        emptyMessage="暂无"
      />,
    );

    // 与末点（内收后 x=668）同 x，落在轴区：基线 210 之下、viewBox 高 240 之内。
    const label = screen.getByText("今日");
    expect(label).toHaveAttribute("x", "668");
    const y = Number(label.getAttribute("y"));
    expect(y).toBeGreaterThan(210);
    expect(y).toBeLessThan(240);
    expect(container.querySelector("svg")).toContainElement(label);
  });

  it("ignores lastPointLabel on the columns variant and empty state (X7)", () => {
    const points = [
      {
        label: "2026-05-01",
        shortLabel: "D1",
        value: 2,
        series: [{ id: "s1", name: "Alpha", value: 2 }],
      },
    ];

    // columns 变体（bot 用量页）不渲染末点轴区小字。
    const first = render(
      <MetricChart
        points={points}
        variant="columns"
        lastPointLabel="今日"
        emptyMessage="暂无"
      />,
    );
    expect(screen.queryByText("今日")).not.toBeInTheDocument();
    first.unmount();

    // 空态连 svg 都不渲染，「今日」自然不出现。
    render(
      <MetricChart
        points={[]}
        variant="area"
        lastPointLabel="今日"
        emptyMessage="暂无"
      />,
    );
    expect(screen.getByText("暂无")).toBeInTheDocument();
    expect(screen.queryByText("今日")).not.toBeInTheDocument();
  });
});
