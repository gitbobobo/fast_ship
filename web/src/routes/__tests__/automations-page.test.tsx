import { fireEvent, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import AutomationsPage from "@/routes/automations";
import { renderWithRoute } from "@/test/render";
import {
  useAutomationRuns,
  useAutomations,
  useCreateAutomation,
  useDeleteAutomation,
  useToggleAutomation,
  useUpdateAutomation,
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
  useAutomationRuns: vi.fn(),
  useCreateAutomation: vi.fn(),
  useUpdateAutomation: vi.fn(),
  useToggleAutomation: vi.fn(),
  useDeleteAutomation: vi.fn(),
}));

vi.mock("@/lib/api/threads", () => ({
  environmentApi: {
    list: vi.fn().mockResolvedValue({
      data: {
        items: [{ id: "env-1", name: "本机环境" }],
      },
    }),
  },
  threadApi: {
    composerOptions: vi.fn().mockResolvedValue({
      data: {
        projects: [{ id: "proj-1", title: "示例项目" }],
        config: {},
        settings: {},
      },
    }),
    workspaceOptions: vi.fn().mockResolvedValue({
      data: {
        project_id: "proj-1",
        cwd: "/tmp",
        is_repo: true,
        ref_name: "main",
        refs: [],
      },
    }),
  },
}));

const automation = {
  id: "a-1",
  name: "每日令牌回报",
  enabled: false,
  environment_id: "env-1",
  t3_project_id: "proj-1",
  trigger_type: "cron",
  cron_expression: "0 9 * * *",
  timezone: "Asia/Shanghai",
  next_run_at: "2026-10-15T01:00:00Z",
  issue_project_id: null,
  prompt_template: "回报令牌",
  runtime_mode: "full-access",
  timeout_seconds: 3600,
  post_action: "settle",
  post_on_timeout: false,
  created_at: "2026-10-14T00:00:00Z",
  updated_at: "2026-10-14T00:00:00Z",
};

describe("AutomationsPage", () => {
  const createMutateAsync = vi.fn();
  const updateMutateAsync = vi.fn();
  const toggleMutateAsync = vi.fn();
  const deleteMutateAsync = vi.fn();

  beforeEach(() => {
    vi.mocked(useAutomations).mockReturnValue({
      data: [automation],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomations>);
    vi.mocked(useAutomationRuns).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomationRuns>);
    vi.mocked(useCreateAutomation).mockReturnValue({
      mutateAsync: createMutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useCreateAutomation>);
    vi.mocked(useUpdateAutomation).mockReturnValue({
      mutateAsync: updateMutateAsync,
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateAutomation>);
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
      screen.getByRole("button", { name: "新建自动化" }),
    ).toBeInTheDocument();
  });

  it("lists automations with trigger summary and post action", () => {
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    expect(screen.getByText("每日令牌回报")).toBeInTheDocument();
    expect(
      screen.getByText("定时（cron） · 0 9 * * *（Asia/Shanghai）"),
    ).toBeInTheDocument();
    expect(screen.getByText("settle")).toBeInTheDocument();
    expect(screen.getByText("已停用")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "查看运行记录 每日令牌回报" }),
    ).toBeInTheDocument();
  });

  it("toggles an automation with full-config PUT", async () => {
    toggleMutateAsync.mockResolvedValue({});
    const user = userEvent.setup();

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    await user.click(screen.getByRole("button", { name: "启用" }));

    await waitFor(() => expect(toggleMutateAsync).toHaveBeenCalledTimes(1));
    const payload = toggleMutateAsync.mock.calls[0][0];
    expect(payload.id).toBe("a-1");
    expect(payload.data.enabled).toBe(true);
    // 全量更新语义：必须携带完整配置。
    expect(payload.data.prompt_template).toBe("回报令牌");
    expect(payload.data.cron_expression).toBe("0 9 * * *");
  });

  it("opens the create dialog with browser timezone and defaults to disabled", async () => {
    const user = userEvent.setup();

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    await user.click(screen.getByRole("button", { name: "新建自动化" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toBeInTheDocument();
    expect(screen.getByText("新建默认停用；启用前需在服务端配置 public_url。")).toBeInTheDocument();
    expect(screen.getByText("模型")).toBeInTheDocument();
    expect(screen.getByText("工作区")).toBeInTheDocument();
  });

  it("edit dialog shows option labels instead of raw values (INT-92)", async () => {
    const user = userEvent.setup();

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    await user.click(
      screen.getByRole("button", { name: "编辑 每日令牌回报" }),
    );
    await screen.findByRole("dialog");

    // value "no"/"settle"/"off" 与显示文案不同，收起态必须渲染 label。
    const postOnTimeout = screen
      .getByText("超时后仍执行后置")
      .closest("div")!
      .querySelector("[role='combobox']");
    expect(postOnTimeout).toHaveTextContent("否（默认）");
    expect(postOnTimeout).not.toHaveTextContent(/\bno\b/);

    const postAction = screen
      .getByText("结束后置操作")
      .closest("div")!
      .querySelector("[role='combobox']");
    expect(postAction).toHaveTextContent("settle（收进历史区）");

    const enabled = screen
      .getByText("启用状态")
      .closest("div")!
      .querySelector("[role='combobox']");
    expect(enabled).toHaveTextContent("停用（默认）");
    expect(enabled).not.toHaveTextContent(/\boff\b/);
  });

  it("edit dialog shows 加载中… while option queries are pending (INT-92)", async () => {
    // 环境/项目选项异步加载期间，收起态不得闪出内部 id。
    const { environmentApi, threadApi } = await import("@/lib/api/threads");
    vi.mocked(environmentApi.list).mockReturnValue(new Promise(() => {}));
    vi.mocked(threadApi.composerOptions).mockReturnValue(new Promise(() => {}));
    const user = userEvent.setup();

    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    await user.click(
      screen.getByRole("button", { name: "编辑 每日令牌回报" }),
    );
    await screen.findByRole("dialog");

    const envSelect = screen
      .getByText("T3 环境")
      .closest("div")!
      .querySelector("[role='combobox']");
    expect(envSelect).toHaveTextContent("加载中…");
    expect(envSelect).not.toHaveTextContent("env-1");

    const projectSelect = screen
      .getByText("T3 项目")
      .closest("div")!
      .querySelector("[role='combobox']");
    expect(projectSelect).toHaveTextContent("加载中…");
    expect(projectSelect).not.toHaveTextContent("proj-1");
  });

  it("shows runs dialog on history click", async () => {
    vi.mocked(useAutomationRuns).mockReturnValue({
      data: [
        {
          id: "r-1",
          automation_id: "a-1",
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
});
