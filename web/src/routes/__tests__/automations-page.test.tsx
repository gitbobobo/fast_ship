import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { toast } from "sonner";
import AutomationsPage from "@/routes/automations";
import { renderWithRoute } from "@/test/render";
import { formatDate } from "@/lib/utils/format";
import {
  useAutomationRuns,
  useAutomations,
  useDeleteAutomation,
  useToggleAutomation,
} from "@/lib/hooks/use-automations";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

vi.mock("@/lib/hooks/use-automations", () => ({
  useAutomations: vi.fn(),
  useAutomation: vi.fn(),
  useAutomationRuns: vi.fn(),
  useCreateAutomation: vi.fn(),
  useUpdateAutomation: vi.fn(),
  useToggleAutomation: vi.fn(),
  useDeleteAutomation: vi.fn(),
}));

const cronTrigger = {
  id: "t-1",
  trigger_type: "cron",
  cron_expression: "0 9 * * *",
  timezone: "Asia/Shanghai",
  next_run_at: "2026-10-15T01:00:00Z",
  issue_project_id: null,
  event_filter: null,
  model_selections: [],
  debounce_seconds: 0,
  created_at: "2026-10-14T00:00:00Z",
};

const automation = {
  id: "a-1",
  name: "每日令牌回报",
  enabled: false,
  environment_id: "env-1",
  t3_project_id: "proj-1",
  triggers: [cronTrigger],
  prompt_template: "回报令牌",
  runtime_mode: "full-access",
  timeout_seconds: 3600,
  post_action: "settle",
  post_on_timeout: false,
  created_at: "2026-10-14T00:00:00Z",
  updated_at: "2026-10-14T00:00:00Z",
};

describe("AutomationsPage", () => {
  const toggleMutateAsync = vi.fn();
  const deleteMutateAsync = vi.fn();

  // INT-100：界面语言跟随浏览器，测试固定中文。
  const setLang = (lang: string) => {
    Object.defineProperty(window.navigator, "language", {
      value: lang,
      configurable: true,
    });
    Object.defineProperty(window.navigator, "languages", {
      value: [lang],
      configurable: true,
    });
  };

  beforeEach(() => {
    setLang("zh-CN");
    vi.mocked(useAutomations).mockReturnValue({
      data: [automation],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomations>);
    vi.mocked(useAutomationRuns).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomationRuns>);
    vi.mocked(useToggleAutomation).mockReturnValue({
      mutateAsync: toggleMutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useToggleAutomation>);
    vi.mocked(useDeleteAutomation).mockReturnValue({
      mutateAsync: deleteMutateAsync,
    } as unknown as ReturnType<typeof useDeleteAutomation>);
  });

  afterEach(() => {
    vi.clearAllMocks();
    setLang("en-US");
  });

  it("renders the empty state", () => {
    vi.mocked(useAutomations).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomations>);

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    expect(screen.getByRole("heading", { name: "自动化" })).toBeInTheDocument();
    expect(screen.getByText("暂无自动化")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "新建自动化" }),
    ).toHaveAttribute("href", "/automations/new");
  });

  it("lists automations with trigger summary and post action", () => {
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    expect(screen.getByText("每日令牌回报")).toBeInTheDocument();
    // INT-100：触发列展示中文描述，原表达式放到 hover。
    const triggerCell = screen.getByText(
      "定时（cron） · 每天 09:00（Asia/Shanghai）",
    );
    expect(triggerCell).toBeInTheDocument();
    expect(triggerCell).toHaveAttribute("title", "0 9 * * *（Asia/Shanghai）");
    expect(screen.getByText("settle")).toBeInTheDocument();
    expect(screen.getByText("已停用")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "查看运行记录 每日令牌回报" }),
    ).toBeInTheDocument();
  });

  it("INT-118：多个触发器显示首个 + 等 N 个", () => {
    vi.mocked(useAutomations).mockReturnValue({
      data: [
        {
          ...automation,
          triggers: [
            cronTrigger,
            {
              id: "t-2",
              trigger_type: "issue_created",
              issue_project_id: null,
              created_at: "2026-10-14T00:00:00Z",
            },
          ],
        },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomations>);

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    expect(
      screen.getByText("定时（cron） · 每天 09:00（Asia/Shanghai） 等 2 个"),
    ).toBeInTheDocument();
  });

  it("INT-118：无触发器显示「未配置」，下次运行为 —", () => {
    vi.mocked(useAutomations).mockReturnValue({
      data: [{ ...automation, triggers: [] }],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomations>);

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    expect(screen.getByText("未配置")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();
  });

  it("INT-118：下次运行取所有 cron 触发器里最早的非空 next_run_at", () => {
    vi.mocked(useAutomations).mockReturnValue({
      data: [
        {
          ...automation,
          triggers: [
            { ...cronTrigger, id: "t-late", next_run_at: "2026-10-16T01:00:00Z" },
            {
              ...cronTrigger,
              id: "t-none",
              cron_expression: "0 8 * * *",
              next_run_at: null,
            },
            cronTrigger,
          ],
        },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomations>);

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    expect(
      screen.getByText(formatDate("2026-10-15T01:00:00Z")),
    ).toBeInTheDocument();
  });

  it("INT-118：启用/停用只把 id 交给 hook（全量请求体由 hook 取最新详情构造）", async () => {
    toggleMutateAsync.mockResolvedValue({});
    const user = userEvent.setup();

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    await user.click(screen.getByRole("button", { name: "启用" }));

    await waitFor(() => expect(toggleMutateAsync).toHaveBeenCalledTimes(1));
    // 页面不得再用列表行里的旧触发器快照组请求体（会把别处新增的触发器删掉）。
    expect(toggleMutateAsync).toHaveBeenCalledWith("a-1");
    expect(toggleMutateAsync.mock.calls[0]).toHaveLength(1);
  });

  it("启停成功后按原 enabled 给 toast", async () => {
    toggleMutateAsync.mockResolvedValue({});
    const user = userEvent.setup();

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    await user.click(screen.getByRole("button", { name: "启用" }));

    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("已启用"));
  });

  it("行内编辑链接指向 /automations/:id", () => {
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    expect(
      screen.getByRole("link", { name: "编辑 每日令牌回报" }),
    ).toHaveAttribute("href", "/automations/a-1");
  });

  it("shows runs dialog on history click", async () => {
    vi.mocked(useAutomationRuns).mockReturnValue({
      data: [
        {
          id: "r-1",
          automation_id: "a-1",
          trigger_id: "t-1",
          trigger_type: "cron",
          trigger_key: "cron:2026-10-14T00:00:00Z",
          triggered_at: "2026-10-14T00:00:00Z",
          status: "completed",
          environment_id: "env-1",
          t3_project_id: "proj-1",
          thread_id: "th-1",
          deadline_at: "2026-10-14T01:00:00Z",
          post_state: "succeeded",
          post_attempt: 0,
          summary: "回报完成",
        },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomationRuns>);
    const user = userEvent.setup();

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    await user.click(
      screen.getByRole("button", { name: "查看运行记录 每日令牌回报" }),
    );

    expect(await screen.findByText("回报完成")).toBeInTheDocument();
    expect(screen.getByText("已完成")).toBeInTheDocument();
  });

  it("运行列表显示排队/已取消、轮次与 SHA", async () => {
    vi.mocked(useAutomationRuns).mockReturnValue({
      data: [
        {
          id: "r-q",
          automation_id: "a-1",
          trigger_id: "t-2",
          trigger_type: "github_pr_synchronize",
          trigger_key: "pr:o/h:1:s2:i:m",
          triggered_at: "2026-10-14T00:00:00Z",
          status: "queued",
          environment_id: "env-1",
          t3_project_id: "proj-1",
          thread_id: "th-q",
          deadline_at: "2026-10-14T01:00:00Z",
          post_state: "none",
          post_attempt: 0,
          repo_full_name: "octocat/hw",
          pr_number: 1,
          head_sha: "abcdef1234567890",
          round_number: 3,
          model_name: "claude-5",
        },
        {
          id: "r-c",
          automation_id: "a-1",
          trigger_id: "t-2",
          trigger_type: "github_pr_synchronize",
          trigger_key: "pr:o/h:1:s1:i:m",
          triggered_at: "2026-10-13T00:00:00Z",
          status: "cancelled",
          error_code: "superseded",
          environment_id: "env-1",
          t3_project_id: "proj-1",
          thread_id: "th-c",
          deadline_at: "2026-10-13T01:00:00Z",
          post_state: "none",
          post_attempt: 0,
          head_sha: "1234567890abcdef",
          round_number: 2,
          model_name: "glm-5",
        },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomationRuns>);
    const user = userEvent.setup();

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });
    await user.click(
      screen.getByRole("button", { name: "查看运行记录 每日令牌回报" }),
    );

    expect(await screen.findByText("排队中")).toBeInTheDocument();
    expect(screen.getByText("已取消")).toBeInTheDocument();
    expect(screen.getByText("superseded")).toBeInTheDocument();
    expect(screen.getByText(/R3/)).toBeInTheDocument();
    expect(screen.getByText(/abcdef1/)).toBeInTheDocument();
  });

  it("delete button is reachable per row", () => {
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    expect(
      screen.getByRole("button", { name: "删除 每日令牌回报" }),
    ).toBeInTheDocument();
    // fireEvent 兜底：aria-label 查询在 jsdom 下也必须可定位。
    fireEvent.click(
      screen.getByRole("button", { name: "删除 每日令牌回报" }),
    );
  });

  it("PR 触发的规则在列表里显示防抖摘要", () => {
    vi.mocked(useAutomations).mockReturnValue({
      data: [
        {
          ...automation,
          name: "PR 评审",
          triggers: [
            {
              id: "t-pr",
              trigger_type: "github_pr_synchronize",
              model_selections: [
                { instance_id: "inst-a", model: "claude-5" },
              ],
              debounce_seconds: 120,
              issue_project_id: null,
              created_at: "2026-10-14T00:00:00Z",
            },
          ],
        },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomations>);

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });
    expect(screen.getByText("PR 评审")).toBeInTheDocument();
    expect(
      screen.getByText("GitHub PR 新提交 · claude-5 · 防抖 120s"),
    ).toBeInTheDocument();
  });
});
