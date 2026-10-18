import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { vi } from "vitest";
import AutomationEditorPage from "@/routes/automations/editor";
import { renderWithRoute, withTestQueryClient } from "@/test/render";
import { environmentApi, threadApi } from "@/lib/api/threads";
import { toast } from "sonner";
import {
  useAutomation,
  useAutomationRuns,
  useCreateAutomation,
  useDeleteAutomation,
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
  useAutomation: vi.fn(),
  useAutomationRuns: vi.fn(),
  useCreateAutomation: vi.fn(),
  useUpdateAutomation: vi.fn(),
  useToggleAutomation: vi.fn(),
  useDeleteAutomation: vi.fn(),
}));

vi.mock("@/lib/hooks/use-projects", () => ({
  useProjects: vi.fn(() => ({
    data: { items: [{ id: "p-1", name: "示例项目" }] },
    isLoading: false,
    isError: false,
  })),
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

const issueTrigger = {
  id: "t-2",
  trigger_type: "issue_created",
  cron_expression: null,
  timezone: null,
  next_run_at: null,
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
  triggers: [cronTrigger, issueTrigger],
  prompt_template: "回报令牌",
  model_selection: null,
  runtime_mode: "full-access",
  timeout_seconds: 3600,
  post_action: "settle",
  post_on_timeout: false,
  created_at: "2026-10-14T00:00:00Z",
  updated_at: "2026-10-14T00:00:00Z",
};

const renderNew = () =>
  renderWithRoute(<AutomationEditorPage />, {
    path: "/automations/new",
    initialEntry: "/automations/new",
  });

const renderEdit = () =>
  renderWithRoute(<AutomationEditorPage />, {
    path: "/automations/:id",
    initialEntry: "/automations/a-1",
  });

// 带列表落点的渲染：验证创建/删除成功后跳回 /automations。
const renderNewWithList = () =>
  render(
    withTestQueryClient(
      <MemoryRouter initialEntries={["/automations/new"]}>
        <Routes>
          <Route path="/automations/new" element={<AutomationEditorPage />} />
          <Route path="/automations" element={<div>自动化列表页</div>} />
        </Routes>
      </MemoryRouter>,
    ),
  );

const renderEditWithList = () =>
  render(
    withTestQueryClient(
      <MemoryRouter initialEntries={["/automations/a-1"]}>
        <Routes>
          <Route path="/automations/:id" element={<AutomationEditorPage />} />
          <Route path="/automations" element={<div>自动化列表页</div>} />
        </Routes>
      </MemoryRouter>,
    ),
  );

const triggerCards = () =>
  document.querySelectorAll('[data-slot="trigger-card"]');

const cardByLabel = (label: string) => {
  const text = screen.getAllByText(label).find((el) =>
    el.closest('[data-slot="trigger-card"]'),
  );
  const card = text?.closest('[data-slot="trigger-card"]');
  if (!card) throw new Error(`trigger card not found: ${label}`);
  return card as HTMLElement;
};

describe("AutomationEditorPage", () => {
  const createMutateAsync = vi.fn();
  const updateMutateAsync = vi.fn();
  const deleteMutateAsync = vi.fn();

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
    vi.mocked(useAutomation).mockReturnValue({
      data: undefined,
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomation>);
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
    vi.mocked(useDeleteAutomation).mockReturnValue({
      mutateAsync: deleteMutateAsync,
      isPending: false,
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

  it("新建页：面包屑、名称输入、默认停用、无运行记录分页", () => {
    renderNew();

    expect(
      screen.getByRole("link", { name: "自动化" }),
    ).toHaveAttribute("href", "/automations");
    expect(
      screen.getByRole("heading", { name: /未命名/ }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("名称")).toBeInTheDocument();
    expect(screen.getByText("已停用")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /添加触发器/ }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("提示词模板")).toBeInTheDocument();
    // 新建页不显示分页（没有运行记录可看）。
    expect(screen.queryByRole("tab", { name: "运行记录" })).toBeNull();
    expect(triggerCards()).toHaveLength(0);
  });

  it("添加触发器菜单：搜索过滤 + 六类可选 + 卡片可删", async () => {
    const user = userEvent.setup();
    renderNew();

    await user.click(screen.getByRole("button", { name: /添加触发器/ }));
    const search = await screen.findByLabelText("搜索触发器");
    // 六种类型全列出。
    for (const label of [
      "定时（cron）",
      "Issue 新建",
      "Issue 状态变化",
      "Issue 加标签",
      "GitHub PR 打开",
      "GitHub PR 新提交",
    ]) {
      expect(screen.getByRole("button", { name: label })).toBeInTheDocument();
    }

    await user.type(search, "issue");
    expect(screen.queryByRole("button", { name: "定时（cron）" })).toBeNull();
    await user.click(
      screen.getByRole("button", { name: "Issue 新建" }),
    );

    expect(triggerCards()).toHaveLength(1);
    // 新添加的卡片默认展开。
    expect(screen.getByText(/Issue 新建事件不做事件字段过滤/)).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "删除触发器 Issue 新建" }),
    );
    expect(triggerCards()).toHaveLength(0);
  });

  it("新建 cron 触发器：默认可视化「每天 09:00」+ 浏览器时区", async () => {
    const user = userEvent.setup();
    renderNew();
    const envTz = Intl.DateTimeFormat().resolvedOptions().timeZone;

    await user.click(screen.getByRole("button", { name: /添加触发器/ }));
    await user.click(
      await screen.findByRole("button", { name: "定时（cron）" }),
    );

    const card = cardByLabel("定时（cron）");
    expect(within(card).getByText("频率")).toBeInTheDocument();
    const time = card.querySelector(
      "input[type='time']",
    ) as HTMLInputElement;
    expect(time.value).toBe("09:00");
    expect(
      await within(card).findByText(new RegExp(`每天 09:00（${envTz}）`)),
    ).toBeInTheDocument();
  });

  it("新建保存：请求体携带 triggers 数组并跳回列表", { timeout: 20000 }, async () => {
    createMutateAsync.mockResolvedValue({ data: automation });
    const user = userEvent.setup();
    renderNewWithList();
    const envTz = Intl.DateTimeFormat().resolvedOptions().timeZone;

    await user.type(screen.getByLabelText("名称"), "每日回报");
    await user.type(screen.getByLabelText("提示词模板"), "回报 {{issue_id}}");
    await user.click(screen.getByRole("button", { name: /添加触发器/ }));
    await user.click(
      await screen.findByRole("button", { name: "定时（cron）" }),
    );

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(createMutateAsync).toHaveBeenCalledTimes(1));
    const payload = createMutateAsync.mock.calls[0][0];
    expect(payload.name).toBe("每日回报");
    expect(payload.triggers).toHaveLength(1);
    expect(payload.triggers[0]).toMatchObject({
      trigger_type: "cron",
      cron_expression: "0 9 * * *",
      timezone: envTz,
    });
    expect(payload.triggers[0].id).toBeUndefined();
    // 保存成功跳回列表页。
    expect(await screen.findByText("自动化列表页")).toBeInTheDocument();
  });

  it("启用状态下没有触发器时保存被拦下", async () => {
    const user = userEvent.setup();
    renderNew();

    await user.type(screen.getByLabelText("名称"), "x");
    await user.type(screen.getByLabelText("提示词模板"), "y");
    await user.click(screen.getByRole("switch", { name: "启用状态" }));
    expect(screen.getByText("已启用")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(toast.error).toHaveBeenCalledWith("启用状态下至少需要一个触发器");
    expect(createMutateAsync).not.toHaveBeenCalled();
  });

  it("编辑页：预填触发器（收起态摘要）+ 保存回传触发器 id", async () => {
    updateMutateAsync.mockResolvedValue({ data: automation });
    vi.mocked(useAutomation).mockReturnValue({
      data: automation,
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomation>);
    const user = userEvent.setup();
    renderEdit();

    expect(
      screen.getByRole("heading", { name: /每日令牌回报/ }),
    ).toBeInTheDocument();
    expect(triggerCards()).toHaveLength(2);
    // 已有触发器默认收起，显示摘要。
    expect(
      screen.getByText("定时（cron） · 每天 09:00（Asia/Shanghai）"),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalledTimes(1));
    const payload = updateMutateAsync.mock.calls[0][0];
    expect(payload.id).toBe("a-1");
    expect(payload.data.triggers).toHaveLength(2);
    expect(payload.data.triggers[0]).toMatchObject({
      id: "t-1",
      trigger_type: "cron",
      cron_expression: "0 9 * * *",
      timezone: "Asia/Shanghai",
    });
    expect(payload.data.triggers[1]).toMatchObject({
      id: "t-2",
      trigger_type: "issue_created",
    });
    // 编辑页留在原页并 toast。
    expect(toast.success).toHaveBeenCalledWith("自动化已更新（只影响后续触发）");
  });

  it("编辑页：删除一个触发器后保存只提交剩余的", async () => {
    updateMutateAsync.mockResolvedValue({ data: automation });
    vi.mocked(useAutomation).mockReturnValue({
      data: automation,
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomation>);
    const user = userEvent.setup();
    renderEdit();

    await user.click(
      screen.getByRole("button", { name: "删除触发器 Issue 新建" }),
    );
    expect(triggerCards()).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalledTimes(1));
    const payload = updateMutateAsync.mock.calls[0][0];
    expect(payload.data.triggers).toHaveLength(1);
    expect(payload.data.triggers[0].id).toBe("t-1");
  });

  it("保存进行中删掉触发器：响应 id 不会写到剩下的卡片上", { timeout: 20000 }, async () => {
    // 提交 [cron, issue] 后、响应回来前删掉 cron：响应仍是 [cron, issue]，
    // 回写必须按提交时的 key 对齐，issue 卡片不能被写成 cron 的 id
    // （否则下次保存会把 issue 的配置写进 cron，并删掉真正的 issue 触发器）。
    const pending: ((value: unknown) => void)[] = [];
    updateMutateAsync.mockImplementation(
      () => new Promise((resolve) => pending.push(resolve)),
    );
    vi.mocked(useAutomation).mockReturnValue({
      data: automation,
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomation>);
    const user = userEvent.setup();
    renderEdit();

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalledTimes(1));
    expect(updateMutateAsync.mock.calls[0][0].data.triggers).toHaveLength(2);

    await user.click(
      screen.getByRole("button", { name: "删除触发器 定时（cron）" }),
    );
    expect(triggerCards()).toHaveLength(1);
    expect(
      screen.getByRole("button", { name: "删除触发器 Issue 新建" }),
    ).toBeInTheDocument();

    await act(async () => {
      pending[0]({ data: automation });
    });
    await waitFor(() => expect(toast.success).toHaveBeenCalledTimes(1));

    // 再保存：剩下的卡片必须带自己的 id（t-2），而不是被删掉的 t-1。
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalledTimes(2));
    const payload = updateMutateAsync.mock.calls[1][0];
    expect(payload.data.triggers).toHaveLength(1);
    expect(payload.data.triggers[0]).toMatchObject({
      id: "t-2",
      trigger_type: "issue_created",
    });
    await act(async () => {
      pending[1]({ data: automation });
    });
    updateMutateAsync.mockReset();
  });

  it("编辑 cron 触发器字段：展开卡片改时间进入保存体", { timeout: 20000 }, async () => {
    updateMutateAsync.mockResolvedValue({ data: automation });
    vi.mocked(useAutomation).mockReturnValue({
      data: automation,
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomation>);
    const user = userEvent.setup();
    renderEdit();

    await user.click(
      screen.getByRole("button", { name: "展开触发器 定时（cron）" }),
    );
    const card = cardByLabel("定时（cron）");
    const time = card.querySelector(
      "input[type='time']",
    ) as HTMLInputElement;
    fireEvent.change(time, { target: { value: "10:30" } });

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalledTimes(1));
    expect(updateMutateAsync.mock.calls[0][0].data.triggers[0]).toMatchObject({
      id: "t-1",
      cron_expression: "30 10 * * *",
      timezone: "Asia/Shanghai",
    });
  });

  it("PR 触发器：多模型 + 防抖秒数写入保存体", { timeout: 20000 }, async () => {
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
    createMutateAsync.mockResolvedValue({ data: automation });
    const user = userEvent.setup();
    renderNewWithList();

    await user.type(screen.getByLabelText("名称"), "PR 审查");
    await user.type(screen.getByLabelText("提示词模板"), "评审 {{repo}}");

    // 先选环境/项目，PR 触发器的模型选择器才有 providers。
    await user.click(
      screen.getByRole("combobox", { name: "T3 环境" }),
    );
    await user.click(await screen.findByText("本机环境"));
    await user.click(screen.getByRole("button", { name: "T3 项目" }));
    await user.click(await screen.findByRole("option", { name: /示例项目/ }));

    await user.click(screen.getByRole("button", { name: /添加触发器/ }));
    await user.click(
      await screen.findByRole("button", { name: "GitHub PR 打开" }),
    );

    const card = cardByLabel("GitHub PR 打开");
    // 模型多选：Claude 5 + GLM 5（跨 provider rail）。
    await user.click(
      within(card).getByRole("button", { name: "模型" }),
    );
    await user.click(await screen.findByRole("option", { name: /Claude 5/ }));
    await user.click(screen.getByRole("button", { name: "GLM" }));
    await user.click(await screen.findByRole("option", { name: /GLM 5/ }));

    const debounce = within(card).getByLabelText(
      "防抖窗口（秒）",
    ) as HTMLInputElement;
    expect(debounce.value).toBe("60");
    fireEvent.change(debounce, { target: { value: "120" } });

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(createMutateAsync).toHaveBeenCalledTimes(1));
    const payload = createMutateAsync.mock.calls[0][0];
    expect(payload.triggers).toHaveLength(1);
    expect(payload.triggers[0]).toMatchObject({
      trigger_type: "github_pr_opened",
      debounce_seconds: 120,
      issue_project_id: null,
      model_selections: [
        { instance_id: "inst-a", model: "claude-5" },
        { instance_id: "inst-b", model: "glm-5" },
      ],
    });
  });

  it("PR 触发器至少要选一个模型（保存被拦下）", { timeout: 20000 }, async () => {
    const user = userEvent.setup();
    renderNew();

    await user.type(screen.getByLabelText("名称"), "PR 审查");
    await user.type(screen.getByLabelText("提示词模板"), "评审 {{repo}}");
    await user.click(screen.getByRole("button", { name: /添加触发器/ }));
    await user.click(
      await screen.findByRole("button", { name: "GitHub PR 打开" }),
    );

    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(toast.error).toHaveBeenCalledWith(
      "GitHub PR 触发必须至少选择一个模型",
    );
    expect(createMutateAsync).not.toHaveBeenCalled();
  });

  it("「…」菜单：启停翻转表单状态；删除走确认框并回列表", async () => {
    deleteMutateAsync.mockResolvedValue({});
    vi.mocked(useAutomation).mockReturnValue({
      data: automation,
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomation>);
    const user = userEvent.setup();
    renderEditWithList();

    await user.click(screen.getByRole("button", { name: "更多操作" }));
    await user.click(await screen.findByText("启用"));
    expect(screen.getByText("已启用")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "更多操作" }));
    await user.click(await screen.findByText("删除"));
    await user.click(
      await screen.findByRole("button", { name: "确认删除" }),
    );

    await waitFor(() => expect(deleteMutateAsync).toHaveBeenCalledWith("a-1"));
    expect(await screen.findByText("自动化列表页")).toBeInTheDocument();
  });

  it("编辑页「运行记录」分页展示运行表", async () => {
    vi.mocked(useAutomation).mockReturnValue({
      data: automation,
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomation>);
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
    renderEdit();

    await user.click(screen.getByRole("tab", { name: "运行记录" }));
    expect(await screen.findByText("回报完成")).toBeInTheDocument();
    expect(screen.getByText("已完成")).toBeInTheDocument();
  });

  it("select 选项显示 label 而不是内部值（INT-92）", () => {
    vi.mocked(useAutomation).mockReturnValue({
      data: automation,
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomation>);
    renderEdit();

    const postOnTimeout = screen
      .getByText("超时后仍执行后置")
      .closest("div")!
      .querySelector("[role='combobox']");
    expect(postOnTimeout).toHaveTextContent("否（默认）");

    const postAction = screen
      .getByText("结束后置操作")
      .closest("div")!
      .querySelector("[role='combobox']");
    expect(postAction).toHaveTextContent("settle（收进历史区）");

    expect(screen.getByText("已停用")).toBeInTheDocument();
  });

  it("已选模型但模型列表加载不出来时，仍能改回项目默认", { timeout: 20000 }, async () => {
    updateMutateAsync.mockResolvedValue({ data: automation });
    vi.mocked(useAutomation).mockReturnValue({
      data: {
        ...automation,
        model_selection: { instance_id: "inst-a", model: "claude-5" },
      },
      isLoading: false,
    } as unknown as ReturnType<typeof useAutomation>);
    const user = userEvent.setup();
    renderEdit();

    // composer-options 的 providers 为空：触发器仍可用，且能清除失效选择。
    const trigger = screen.getByRole("button", { name: "模型" });
    expect(trigger).toBeEnabled();
    await waitFor(() => expect(trigger).toHaveTextContent("claude-5（已失效）"));

    await user.click(trigger);
    await user.click(await screen.findByRole("option", { name: "项目默认" }));
    await user.click(screen.getByRole("button", { name: "保存" }));
    await waitFor(() => expect(updateMutateAsync).toHaveBeenCalledTimes(1));
    expect(updateMutateAsync.mock.calls[0][0].data.model_selection).toBeNull();
  });
});
