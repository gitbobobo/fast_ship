import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { vi } from "vitest";
import { useDashboardOverview } from "@/lib/hooks/use-dashboard";

vi.mock("@/lib/hooks/use-dashboard", () => ({
  useDashboardOverview: vi.fn(),
}));

async function renderDashboardPage() {
  const { default: DashboardPage } = await import("@/routes/dashboard/index");
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
    },
  });

  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={["/dashboard"]}>
        <DashboardPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("DashboardPage", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  function dailyResolvedWith(resolvedByIndex: Record<number, number>) {
    return Array.from({ length: 30 }, (_, index) => ({
      date: `2026-05-${String(index + 1).padStart(2, "0")}`,
      resolved_count: resolvedByIndex[index] ?? 0,
      projects: resolvedByIndex[index]
        ? [
            {
              project_id: "p1",
              project_name: "Alpha App",
              count: resolvedByIndex[index],
            },
          ]
        : [],
    }));
  }

  it("shows the daily average with a <0.1 floor and no trend arrow", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [
          { project_id: "p1", project_name: "Alpha App", open_issue_count: 0 },
        ],
        // 最近 7 天解决 1 条。
        daily_resolved: dailyResolvedWith({ 29: 1 }),
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByText("日均 <0.1")).toBeInTheDocument();
    // 方向图标已移出本需求：不再对两周合计做涨跌判断。
    expect(screen.queryByRole("img", { name: /最近 7 天/ })).toBeNull();
  });

  it("hides stale header numbers when cached data is followed by a failed refetch", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [
          { project_id: "p1", project_name: "Alpha App", open_issue_count: 3 },
        ],
        daily_resolved: dailyResolvedWith({ 29: 2 }),
      },
      isLoading: false,
      isError: true,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByRole("alert")).toHaveTextContent("仪表盘数据加载失败");
    // 缓存里的旧数字不能被当成当前状态展示。
    expect(screen.queryByText(/总计/)).not.toBeInTheDocument();
    expect(screen.queryByText(/^日均 /)).not.toBeInTheDocument();
    expect(screen.queryByText("Alpha App")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "去创建项目" })).not.toBeInTheDocument();
    expect(
      screen.getAllByText("数据加载失败，请在上方重试。"),
    ).toHaveLength(2);
  });


  it("renders open-issue and 30-day resolved chart sections", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [
          {
            project_id: "project-1",
            project_name: "Alpha App",
            open_issue_count: 2,
          },
          {
            project_id: "project-2",
            project_name: "Beta Console",
            open_issue_count: 1,
          },
        ],
        daily_resolved: [
          { date: "2026-05-11", resolved_count: 1, projects: [{ project_id: "p1", project_name: "Alpha App", count: 1 }] },
          { date: "2026-05-12", resolved_count: 0, projects: [] },
          { date: "2026-05-13", resolved_count: 2, projects: [{ project_id: "p1", project_name: "Alpha App", count: 1 }, { project_id: "p2", project_name: "Beta Console", count: 1 }] },
        ],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(
      screen.getAllByText(/未解决问题|open issues|开放问题/i).length,
    ).toBeGreaterThan(0);
    expect(
      screen.getAllByText(/近 ?30 ?天.*已解决|30-?day resolved|最近 30 天已解决/i)
        .length,
    ).toBeGreaterThan(0);
    expect(screen.getByText("Alpha App")).toBeInTheDocument();
    expect(screen.getByText("Beta Console")).toBeInTheDocument();
    expect(screen.getAllByText("2").length).toBeGreaterThan(0);
    expect(screen.getAllByText("1").length).toBeGreaterThan(0);
    expect(screen.getAllByText("0").length).toBeGreaterThan(0);
  });

  it("renders empty-state placeholders with zero values", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: Array.from({ length: 30 }, (_, index) => ({
          date: `2026-05-${String(index + 1).padStart(2, "0")}`,
          resolved_count: 0,
          projects: [],
        })),
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(
      screen.getAllByText(/未解决问题|open issues|开放问题/i).length,
    ).toBeGreaterThan(0);
    expect(
      screen.getAllByText(/近 ?30 ?天.*已解决|30-?day resolved|最近 30 天已解决/i)
        .length,
    ).toBeGreaterThan(0);
    expect(screen.getAllByText("0").length).toBeGreaterThanOrEqual(2);
    // 每个图表只保留一个空状态，且带一个真实的配置入口。
    expect(screen.getAllByText("暂无已配置项目")).toHaveLength(1);
    expect(screen.getAllByText("暂无已解决问题数据")).toHaveLength(1);
    expect(screen.queryByText("暂无项目")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "去创建项目" })).toHaveAttribute(
      "href",
      "/projects",
    );
    // 空数据时不画趋势图（也就不存在重复刻度与下降趋势图标）。
    expect(
      screen.queryByRole("img", { name: "stacked area chart" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/日均 0\.0/)).not.toBeInTheDocument();
  });

  it("shows a retryable error state instead of an empty account when the overview fails", async () => {
    const refetch = vi.fn();
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      refetch,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByRole("alert")).toHaveTextContent(
      "仪表盘数据加载失败",
    );
    const retry = screen.getByRole("button", { name: "重试" });
    retry.click();
    expect(refetch).toHaveBeenCalledTimes(1);
    // 请求失败不等于「没有项目」：不要给创建入口，也不要显示空状态文案。
    expect(
      screen.queryByRole("link", { name: "去创建项目" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText("暂无已配置项目")).not.toBeInTheDocument();
    expect(screen.queryByText("暂无已解决问题数据")).not.toBeInTheDocument();
    expect(
      screen.getAllByText("数据加载失败，请在上方重试。"),
    ).toHaveLength(2);
  });
});
