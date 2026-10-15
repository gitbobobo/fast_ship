import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import AutomationsPage from "@/routes/automations";
import { renderWithRoute } from "@/test/render";
import { environmentApi, threadApi } from "@/lib/api/threads";
import { toast } from "sonner";
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
    // mockReturnValue(挂起 Promise) 的实现不会被 clearAllMocks 清掉，
    // 每个用例开始前重放 resolved 实现，防 pending 泄漏到后续用例。
    vi.mocked(environmentApi.list).mockResolvedValue({
      data: { items: [{ id: "env-1", name: "本机环境" }] },
    } as unknown as Awaited<ReturnType<typeof environmentApi.list>>);
    vi.mocked(threadApi.composerOptions).mockResolvedValue({
      data: {
        projects: [{ id: "proj-1", title: "示例项目" }],
        config: {},
        settings: {},
      },
    } as unknown as Awaited<ReturnType<typeof threadApi.composerOptions>>);
    vi.mocked(threadApi.workspaceOptions).mockResolvedValue({
      data: {
        project_id: "proj-1",
        cwd: "/tmp",
        is_repo: true,
        ref_name: "main",
        refs: [],
      },
    } as unknown as Awaited<ReturnType<typeof threadApi.workspaceOptions>>);
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
      screen.getByRole("button", { name: "新建自动化" }),
    ).toBeInTheDocument();
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

  // ---- INT-100：可视化频率 + cron 高级模式 ----

  const freqSelect = () =>
    screen.getByText("频率").closest("div")!.querySelector("[role='combobox']")!;
  const envTz = () => Intl.DateTimeFormat().resolvedOptions().timeZone;

  it("新建默认「每天 09:00」，带描述与下次运行预览", async () => {
    const user = userEvent.setup();
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });
    await user.click(screen.getByRole("button", { name: "新建自动化" }));
    const dialog = await screen.findByRole("dialog");

    expect(freqSelect()).toHaveTextContent("每天");
    const time = document.querySelector(
      "input[type='time']",
    ) as HTMLInputElement;
    expect(time.value).toBe("09:00");
    // 时区默认浏览器时区且可搜索，描述与预览同步展示。
    expect(
      within(dialog).getByPlaceholderText("搜索时区…"),
    ).toBeInTheDocument();
    expect(
      await within(dialog).findByText(new RegExp(`每天 09:00（${envTz()}）`)),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/接下来 3 次运行时间/)).toBeInTheDocument();
  });

  it("编辑简单表达式还原为「每天」", async () => {
    const user = userEvent.setup();
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });
    await user.click(screen.getByRole("button", { name: "编辑 每日令牌回报" }));
    await screen.findByRole("dialog");
    expect(freqSelect()).toHaveTextContent("每天");
    const time = document.querySelector(
      "input[type='time']",
    ) as HTMLInputElement;
    expect(time.value).toBe("09:00");
    expect(
      screen.getByPlaceholderText("搜索时区…"),
    ).toHaveValue("Asia/Shanghai");
  });

  it("编辑复杂表达式落入自定义且原文保留", async () => {
    vi.mocked(useAutomations).mockReturnValue({
      data: [
        { ...automation, cron_expression: "*/7 9-17 * * 1-5", timezone: "" },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomations>);
    const user = userEvent.setup();
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });
    await user.click(
      screen.getByRole("button", { name: "编辑 每日令牌回报" }),
    );
    await screen.findByRole("dialog");
    expect(freqSelect()).toHaveTextContent("自定义（cron 表达式）");
    const input = screen.getByLabelText("cron 表达式") as HTMLInputElement;
    expect(input.value).toBe("*/7 9-17 * * 1-5");
    // 空时区按 UTC 生效并显示出来。
    expect(screen.getByPlaceholderText("搜索时区…")).toHaveValue("UTC");
    expect(screen.getByText(/五字段|five|分 时 日 月 周/)).toBeInTheDocument();
  });

  it("自定义模式：实时校验、示例可点击回填", { timeout: 20000 }, async () => {
    const user = userEvent.setup();
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });
    await user.click(screen.getByRole("button", { name: "新建自动化" }));
    const dialog = await screen.findByRole("dialog");

    await user.click(freqSelect());
    await user.click(await screen.findByText("自定义（cron 表达式）"));

    const input = (await within(dialog).findByLabelText(
      "cron 表达式",
    )) as HTMLInputElement;
    // 切到自定义时带入当前可视化表达式。
    expect(input.value).toBe("0 9 * * *");

    await user.clear(input);
    await user.type(input, "61 * * * *");
    expect(
      await within(dialog).findByText(/分钟字段.*超出范围/),
    ).toBeInTheDocument();

    await user.click(within(dialog).getByText("工作日 18:30"));
    expect(input.value).toBe("30 18 * * 1-5");
  });

  it("每周多选星期生成描述", { timeout: 20000 }, async () => {
    const user = userEvent.setup();
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });
    await user.click(screen.getByRole("button", { name: "新建自动化" }));
    const dialog = await screen.findByRole("dialog");

    await user.click(freqSelect());
    await user.click(await screen.findByText("每周"));
    // 默认勾了周一；再勾周三。
    await user.click(within(dialog).getByRole("button", { name: "三" }));
    expect(
      await within(dialog).findByText(
        new RegExp(`每周一、周三 09:00（${envTz()}）`),
      ),
    ).toBeInTheDocument();
  });

  it("英文界面文案（locale=en）", async () => {
    setLang("en-US");
    const user = userEvent.setup();
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });
    await user.click(screen.getByRole("button", { name: "新建自动化" }));
    const dialog = await screen.findByRole("dialog");

    expect(within(dialog).getByText("Frequency")).toBeInTheDocument();
    expect(freqSelectEn()).toHaveTextContent("Daily");
    expect(
      await within(dialog).findByText(
        new RegExp(`Daily at 09:00 \\(${envTz()}\\)`),
      ),
    ).toBeInTheDocument();
    expect(within(dialog).getByText(/Next 3 runs/)).toBeInTheDocument();
  });

  const freqSelectEn = () =>
    screen.getByText("Frequency").closest("div")!.querySelector("[role='combobox']")!;

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

  it("PR 触发至少要选一个模型（保存被拦下）", { timeout: 20000 }, async () => {
    const user = userEvent.setup();
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    await user.click(screen.getByRole("button", { name: "新建自动化" }));
    await screen.findByRole("dialog");
    await user.type(screen.getByLabelText(/名称/), "PR 审查");
    await user.type(screen.getByLabelText("提示词模板"), "评审 {{repo}}");

    await user.click(screen.getAllByRole("combobox")[0]);
    await user.click(await screen.findByText("GitHub PR 打开"));

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(createMutateAsync).not.toHaveBeenCalled();
  });

  it("PR 触发创建携带 model_selections 与防抖默认 60", { timeout: 20000 }, async () => {
    vi.mocked(threadApi.composerOptions).mockResolvedValue({
      data: {
        projects: [{ id: "proj-1", title: "示例项目" }],
        config: {
          providers: [
            {
              instanceId: "inst-a",
              displayName: "Claude",
              models: [{ slug: "claude-5", name: "Claude 5" }],
            },
            {
              instanceId: "inst-b",
              displayName: "GLM",
              models: [{ slug: "glm-5", name: "GLM 5" }],
            },
          ],
        },
        settings: {},
      },
    } as never);
    createMutateAsync.mockResolvedValue({});
    const user = userEvent.setup();
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    const selectByLabel = (label: string) =>
      screen.getByText(label).closest("div")!.querySelector("[role='combobox']")!;

    await user.click(screen.getByRole("button", { name: "新建自动化" }));
    await screen.findByRole("dialog");
    await user.type(screen.getByLabelText(/名称/), "PR 审查");
    await user.type(screen.getByLabelText("提示词模板"), "评审 {{repo}}");

    await user.click(selectByLabel("T3 环境"));
    await user.click(await screen.findByText("本机环境"));
    await user.click(selectByLabel("T3 项目"));
    await user.click(await screen.findByText("示例项目"));

    await user.click(selectByLabel("触发方式"));
    await user.click(await screen.findByText("GitHub PR 打开"));

    await user.click(await screen.findByText("Claude / Claude 5"));
    await user.click(screen.getByText("GLM / GLM 5"));

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(createMutateAsync).toHaveBeenCalledTimes(1));
    const payload = createMutateAsync.mock.calls[0][0];
    expect(payload.trigger_type).toBe("github_pr_opened");
    expect(payload.debounce_seconds).toBe(60);
    expect(payload.model_selections).toEqual([
      { instance_id: "inst-a", model: "claude-5" },
      { instance_id: "inst-b", model: "glm-5" },
    ]);
  });

  it("PR 触发勾选模型时带上 descriptor 默认选项", { timeout: 20000 }, async () => {
    vi.mocked(threadApi.composerOptions).mockResolvedValue({
      data: {
        projects: [{ id: "proj-1", title: "示例项目" }],
        config: {
          providers: [
            {
              instanceId: "inst-a",
              displayName: "Claude",
              models: [
                {
                  slug: "claude-5",
                  name: "Claude 5",
                  capabilities: {
                    optionDescriptors: [
                      {
                        type: "select",
                        id: "effort",
                        currentValue: "high",
                        options: [
                          { id: "low", label: "Low" },
                          { id: "high", label: "High", isDefault: true },
                        ],
                      },
                    ],
                  },
                },
              ],
            },
          ],
        },
        settings: {},
      },
    } as never);
    createMutateAsync.mockResolvedValue({});
    const user = userEvent.setup();
    renderWithRoute(<AutomationsPage />, {
      path: "/automations",
      initialEntry: "/automations",
    });

    const selectByLabel = (label: string) =>
      screen.getByText(label).closest("div")!.querySelector("[role='combobox']")!;

    await user.click(screen.getByRole("button", { name: "新建自动化" }));
    await screen.findByRole("dialog");
    await user.type(screen.getByLabelText(/名称/), "PR 审查");
    await user.type(screen.getByLabelText("提示词模板"), "评审 {{repo}}");

    await user.click(selectByLabel("T3 环境"));
    await user.click(await screen.findByText("本机环境"));
    await user.click(selectByLabel("T3 项目"));
    await user.click(await screen.findByText("示例项目"));

    await user.click(selectByLabel("触发方式"));
    await user.click(await screen.findByText("GitHub PR 打开"));
    await user.click(await screen.findByText("Claude / Claude 5"));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(createMutateAsync).toHaveBeenCalledTimes(1));
    expect(createMutateAsync.mock.calls[0][0].model_selections).toEqual([
      {
        instance_id: "inst-a",
        model: "claude-5",
        options: [{ id: "effort", value: "high" }],
      },
    ]);
  });

  it("运行列表显示排队/已取消、轮次与 SHA", async () => {
    vi.mocked(useAutomationRuns).mockReturnValue({
      data: [
        {
          id: "r-q",
          automation_id: "a-1",
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

  it("PR 触发的规则在列表里显示防抖摘要", () => {
    vi.mocked(useAutomations).mockReturnValue({
      data: [
        {
          ...automation,
          name: "PR 评审",
          trigger_type: "github_pr_synchronize",
          model_selections: [
            { instance_id: "inst-a", model: "claude-5" },
          ],
          debounce_seconds: 120,
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
