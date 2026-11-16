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
          { date: "2026-05-11", resolved_count: 1 },
          { date: "2026-05-12", resolved_count: 0 },
          { date: "2026-05-13", resolved_count: 2 },
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

  it("renders action item groups with links and counts", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        action_item_groups: [
          {
            kind: "recommendations",
            count: 2,
            items: [
              {
                title: "优化登录流程",
                detail: "Alpha App",
                href: "/projects/p1/issues/i1",
              },
              { title: "修复崩溃", href: "/projects/p1/issues/i2" },
            ],
          },
          {
            kind: "unread_notifications",
            count: 3,
            items: [{ title: "3 条未读通知", href: "/notifications" }],
          },
        ],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByText("待我处理")).toBeInTheDocument();
    expect(screen.getByText("推荐任务")).toBeInTheDocument();
    expect(screen.getByText("未读通知")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /优化登录流程/ }),
    ).toHaveAttribute("href", "/projects/p1/issues/i1");
    expect(screen.getByText("Alpha App")).toBeInTheDocument();
    expect(screen.queryByText("一切正常")).not.toBeInTheDocument();
  });

  it("shows 一切正常 when every action group is zero", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        action_item_groups: [
          { kind: "recommendations", count: 0, items: [] },
          { kind: "unread_notifications", count: 0, items: [] },
        ],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByText("一切正常")).toBeInTheDocument();
    expect(screen.queryByText("推荐任务")).not.toBeInTheDocument();
  });

  it("marks a partially unavailable group instead of trusting its count", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        action_item_groups: [
          {
            kind: "t3_waiting",
            count: 2,
            complete: false,
            items: [
              {
                title: "等待确认",
                detail: "Mac",
                href: "/threads/env1/t1",
              },
            ],
          },
        ],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByText("T3 线程待处理")).toBeInTheDocument();
    expect(
      screen.getByText("2+ · 部分环境暂不可用"),
    ).toBeInTheDocument();
  });

  it("shows 暂不可用 badge for a fully unavailable group with zero count", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        action_item_groups: [
          { kind: "t3_waiting", count: 0, complete: false, items: [] },
        ],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByText("T3 线程待处理")).toBeInTheDocument();
    expect(screen.getByText("暂不可用")).toBeInTheDocument();
    expect(screen.queryByText("一切正常")).not.toBeInTheDocument();
  });

  it("keeps stale action items hidden while the overview failed", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        action_item_groups: [
          {
            kind: "recommendations",
            count: 5,
            items: [{ title: "旧任务", href: "/projects/p1/issues/i1" }],
          },
        ],
      },
      isLoading: false,
      isError: true,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.queryByText("待我处理")).not.toBeInTheDocument();
    expect(screen.queryByText("旧任务")).not.toBeInTheDocument();
    expect(screen.queryByText("一切正常")).not.toBeInTheDocument();
  });

  it("links the configured-projects KPI card to /projects", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [
          { project_id: "p1", project_name: "Alpha App", open_issue_count: 0 },
        ],
        daily_resolved: dailyResolvedWith({}),
        action_item_groups: [],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(
      screen.getByRole("link", { name: /已配置项目/ }),
    ).toHaveAttribute("href", "/projects");
  });

  it("collapses the open-issues chart beyond top 10 and links rows to filtered issues", async () => {
    const projects = Array.from({ length: 13 }, (_, index) => ({
      project_id: `p${index}`,
      project_name: `Project ${index}`,
      open_issue_count: 13 - index,
    }));
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: projects,
        daily_resolved: dailyResolvedWith({}),
        action_item_groups: [],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(
      screen.getByRole("link", { name: /Project 0/ }),
    ).toHaveAttribute("href", "/issues?project=p0&state=open");
    expect(screen.getByText("其他 3 个项目共 6 条")).toBeInTheDocument();
    expect(screen.queryByText("Project 10")).not.toBeInTheDocument();
    expect(screen.getByText("13 个项目有开启问题")).toBeInTheDocument();
  });
});

describe("DashboardPage INT-235 阶段二/三（流转效率 / AI 面板 / 发布节奏）", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  function dailyResolvedWith(resolvedByIndex: Record<number, number>) {
    return Array.from({ length: 30 }, (_, index) => ({
      date: `2026-05-${String(index + 1).padStart(2, "0")}`,
      resolved_count: resolvedByIndex[index] ?? 0,
    }));
  }

  const dailyCreated = Array.from({ length: 30 }, (_, index) => ({
    date: `2026-05-${String(index + 1).padStart(2, "0")}`,
    created_count: index === 29 ? 4 : 0,
  }));

  it("renders hero summary with resolved count and pending total", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({ 28: 2, 29: 3 }),
        action_item_groups: [
          { kind: "recommendations", count: 2, items: [] },
          { kind: "unread_notifications", count: 3, items: [] },
        ],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    // N=daily_resolved 尾 7 天合计；M=待处理组 count 合计
    expect(screen.getByText("近 7 天解决 5 个，5 件事等你处理")).toBeInTheDocument();
  });

  it("marks the hero pending total with + when any action group is incomplete", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        action_item_groups: [
          { kind: "t3_waiting", count: 2, complete: false, items: [] },
          { kind: "recommendations", count: 1, items: [] },
        ],
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByText("近 7 天解决 0 个，3+ 件事等你处理")).toBeInTheDocument();
  });

  it("falls back to the greeting while unavailable", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        action_item_groups: [{ kind: "recommendations", count: 4, items: [] }],
      },
      isLoading: false,
      isError: true,
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    // 摘要句依赖数据可用性：失败时回退问候语，绝不展示旧数字
    expect(screen.queryByText(/近 7 天解决/)).not.toBeInTheDocument();
    expect(screen.getByText(/查看今日项目概况/)).toBeInTheDocument();
  });

  it("renders the flow card with bucket badges, cycle list and backlog bar", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [
          { project_id: "p1", project_name: "Alpha App", open_issue_count: 5 },
        ],
        daily_resolved: dailyResolvedWith({ 29: 1 }),
        daily_created: dailyCreated,
        flow: {
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
        },
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByTestId("flow-card")).toBeInTheDocument();
    // 恒等式徽章：3+2 == Σopen_issue_count（5）
    expect(screen.getByText("待办 3")).toBeInTheDocument();
    expect(screen.getByText("进行中 2")).toBeInTheDocument();
    expect(screen.getByText("中位 2.5 天 · 3 个")).toBeInTheDocument();
    expect(screen.getByTestId("backlog-age-bar")).toBeInTheDocument();
    expect(screen.getByText("7 天内 2")).toBeInTheDocument();
    expect(screen.getByText("30 天以上 1")).toBeInTheDocument();
  });

  it("shows 暂不可用 for a degraded flow block instead of zeroed numbers", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        flow: {
          complete: false,
          todo_count: 0,
          in_progress_count: 0,
          daily_created: dailyCreated,
          cycle_time_by_project: [],
          backlog_age: { under_7d: 0, d7_to_30d: 0, over_30d: 0 },
        },
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    const card = screen.getByTestId("flow-card");
    expect(card).toHaveTextContent("暂不可用");
    // 降级时零值徽章不渲染，避免被读成真实读数
    expect(screen.queryByText("待办 0")).not.toBeInTheDocument();
    expect(screen.queryByTestId("backlog-age-bar")).not.toBeInTheDocument();
  });

  it("renders the AI panel with per-env rows, review buckets and bots stats", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        ai: {
          t3: {
            environments: [
              {
                environment_id: "env-1",
                environment_name: "工作站",
                running: 2,
                waiting: 1,
                queued: 1,
                readable: true,
              },
              {
                environment_id: "env-2",
                environment_name: "离线机",
                running: 0,
                waiting: 0,
                queued: 0,
                readable: false,
              },
            ],
          },
          reviews: {
            passed: 3,
            blocked: 1,
            errored: 1,
            in_flight: 2,
            pass_rate: 0.75,
          },
          bots: {
            bot_count: 1,
            timezone: "Asia/Shanghai",
            cost: null,
            total_requests: 12,
            total_tokens: 3456,
            next_schedule: {
              bot_id: "b1",
              bot_name: "值夜机器人",
              task_name: "晚报",
              next_run_at: "2026-05-30T12:00:00Z",
            },
          },
        },
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByTestId("ai-panel-card")).toBeInTheDocument();
    expect(screen.getByText("运行 2 · 等待 1 · 排队 1")).toBeInTheDocument();
    expect(screen.getByText("本轮不可读")).toBeInTheDocument();
    // 通过率 = 3/(3+1) = 75%
    expect(screen.getByText("通过率 75%")).toBeInTheDocument();
    expect(screen.getByText("Tokens · 请求（1 个机器人）")).toBeInTheDocument();
    expect(screen.getByText(/3,456 · 12/)).toBeInTheDocument();
    // cost=null 是缺失不是 0：显示「未计价」而不是 $0.00
    expect(screen.getByText("未计价")).toBeInTheDocument();
    expect(screen.getByText(/下次任务/)).toBeInTheDocument();
    expect(screen.getByText(/晚报 · 值夜机器人/)).toBeInTheDocument();
  });

  it("hides the pass rate when its denominator is zero and shows the empty bots state", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        ai: {
          reviews: {
            passed: 0,
            blocked: 0,
            errored: 0,
            in_flight: 1,
            pass_rate: null,
          },
          bots: {
            bot_count: 0,
            timezone: "UTC",
            cost: null,
            total_requests: 0,
            total_tokens: 0,
          },
        },
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.queryByText(/通过率/)).not.toBeInTheDocument();
    expect(screen.getByText("未配置机器人")).toBeInTheDocument();
    expect(screen.getByText("近 7 天（按你的时区）")).toBeInTheDocument();
  });

  it("renders quota mini bars with remaining percent and status badges", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        ai: {
          quota: {
            accounts: [
              {
                id: "acc-1",
                label: "主账号",
                provider: "claude",
                provider_name: "Claude",
                credential_hint: "…ab12",
                status: "ok",
                note: "",
                plan_name: "",
                last_error: "",
                created_at: "2026-05-01T00:00:00Z",
                updated_at: "2026-05-01T00:00:00Z",
                fetched_at: null,
                last_success_at: null,
                balance: null,
                windows: [
                  {
                    kind: "session",
                    label: "五小时",
                    pool: "",
                    used: 4,
                    limit: 10,
                    unit: "percent",
                    used_percent: 40,
                    resets_at: null,
                    reset_at: null,
                  },
                ],
              },
              {
                id: "acc-2",
                label: "备用号",
                provider: "codex",
                provider_name: "Codex",
                credential_hint: "…cd34",
                status: "credential_invalid",
                note: "",
                plan_name: "",
                last_error: "",
                created_at: "2026-05-01T00:00:00Z",
                updated_at: "2026-05-01T00:00:00Z",
                fetched_at: null,
                last_success_at: null,
                balance: null,
                windows: [],
              },
            ],
          },
        },
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByTestId("ai-quota-section")).toBeInTheDocument();
    // 剩余百分比由前端 accountRemaining 推导：100-40=60%
    expect(screen.getByText("60%")).toBeInTheDocument();
    // 快照旧/失效状态与徽章一起展示
    expect(screen.getByText("凭据失效")).toBeInTheDocument();
    expect(screen.getAllByTestId("dashboard-quota-bar")).toHaveLength(2);
  });

  it("renders the release card with pending links, timeline and unmerged count", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        release: {
          pending_count: 2,
          pending_versions: [
            {
              version_id: "v1",
              version_number: "v2.0.0",
              project_id: "p1",
              project_name: "Alpha App",
              created_at: "2026-05-28T00:00:00Z",
              href: "/projects/p1/versions/v1",
            },
          ],
          recent_releases: [
            {
              version_id: "v0",
              version_number: "v1.9.0",
              project_id: "p1",
              project_name: "Alpha App",
              created_at: "2026-05-20T00:00:00Z",
              shipped_at: "2026-05-20T08:00:00Z",
              href: "/projects/p1/versions/v0",
            },
          ],
          unmerged_pr_issue_count: 3,
        },
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByTestId("release-card")).toBeInTheDocument();
    expect(screen.getByText("待发版 2")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /v2\.0\.0/ }),
    ).toHaveAttribute("href", "/projects/p1/versions/v1");
    expect(screen.getByText(/距上次发版/)).toBeInTheDocument();
    expect(
      screen.getByText("3 个开启问题还有未合并的 PR"),
    ).toBeInTheDocument();
  });

  it("shows 暂不可用 for degraded ai/release blocks and hides new cards on overview failure", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      data: {
        open_issues_by_project: [],
        daily_resolved: dailyResolvedWith({}),
        flow: { complete: false, todo_count: 0, in_progress_count: 0, daily_created: [], cycle_time_by_project: [], backlog_age: { under_7d: 0, d7_to_30d: 0, over_30d: 0 } },
        ai: { reviews: { complete: false, passed: 0, blocked: 0, errored: 0, in_flight: 0, pass_rate: null } },
        release: {
          complete: false,
          pending_count: 0,
          pending_versions: [],
          recent_releases: [],
          unmerged_pr_issue_count: 0,
        },
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.getByTestId("flow-card")).toHaveTextContent("暂不可用");
    expect(screen.getByTestId("ai-reviews-section")).toHaveTextContent("暂不可用");
    expect(screen.getByTestId("release-card")).toHaveTextContent("暂不可用");
  });

  it("hides new blocks entirely while the overview is loading", async () => {
    vi.mocked(useDashboardOverview).mockReturnValue({
      isLoading: true,
    } as unknown as ReturnType<typeof useDashboardOverview>);

    await renderDashboardPage();

    expect(screen.queryByTestId("flow-card")).not.toBeInTheDocument();
    expect(screen.queryByTestId("ai-panel-card")).not.toBeInTheDocument();
    expect(screen.queryByTestId("release-card")).not.toBeInTheDocument();
  });
});
