import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import BotsPage from "@/routes/bots";
import BotDetailPage from "@/routes/bots/$botId";
import App from "@/App";
import { renderWithRoute } from "@/test/render";
import type { Bot } from "@/lib/api/bots";
import { useBot, useBots } from "@/lib/hooks/use-bots";
import { useBotDetailPanelStore } from "@/lib/store/bot-detail-panel-store";

const {
  createMutateAsync,
  updateMutateAsync,
  deleteMutateAsync,
  testConnectionMutateAsync,
  revokeMutateAsync,
} = vi.hoisted(() => ({
  createMutateAsync: vi.fn(),
  updateMutateAsync: vi.fn(),
  deleteMutateAsync: vi.fn(),
  testConnectionMutateAsync: vi.fn(),
  revokeMutateAsync: vi.fn(),
}));

vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

const fakeBot: Bot = {
  id: "b1",
  name: "发布助手",
  persona_prompt: "帮我盯发布",
  provider_id: "ap-1",
  provider_name: "DeepSeek 主号",
  catalog_id: "deepseek",
  base_url: "https://api.deepseek.com/v1",
  model: "gpt-test",
  api_key_last4: "1234",
  prompt_revision: 1,
  platform_prompt_version: 1,
  toolset_version: 1,
  context_window: 131072,
  max_output_tokens: 32768,
  context_source: "catalog",
  next_event_seq: 10,
  last_message: {
    kind: "assistant",
    text: "已帮你盯好这次发布",
  },
  created_at: "2026-10-15T00:00:00Z",
  updated_at: "2026-10-15T00:00:00Z",
};

// 机器人表单经统一 AI 提供商选模型：提供商与目录数据在这里钉死。
const fakeAIProvider = {
  id: "ap-1",
  catalog_id: "deepseek",
  name: "DeepSeek 主号",
  base_url: "https://api.deepseek.com/v1",
  api_key_last4: "1234",
  bot_count: 1,
  created_at: "2026-10-15T00:00:00Z",
  updated_at: "2026-10-15T00:00:00Z",
};

const fakeCatalogProvider = {
  id: "deepseek",
  name: "DeepSeek",
  api: "https://api.deepseek.com/v1",
  models: [
    {
      id: "gpt-test",
      name: "GPT Test",
      context_window: 131072,
      max_output_tokens: 8192,
    },
    {
      id: "gpt-pro",
      name: "GPT Pro",
      context_window: 262144,
      max_output_tokens: 16384,
    },
  ],
};

vi.mock("@/lib/hooks/use-ai-providers", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/hooks/use-ai-providers")>()),
  useAIProviders: vi.fn(() => ({
    data: [fakeAIProvider],
    isLoading: false,
  })),
  useAIProviderCatalog: vi.fn(() => ({
    data: [fakeCatalogProvider],
    isLoading: false,
  })),
}));

vi.mock("@/lib/hooks/use-bots", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/hooks/use-bots")>()),
  useBots: vi.fn(() => ({ data: [fakeBot], isLoading: false })),
  useBot: vi.fn(() => ({ data: fakeBot, isLoading: false })),
  useCreateBot: vi.fn(() => ({ mutateAsync: createMutateAsync, isPending: false })),
  useUpdateBot: vi.fn(() => ({ mutateAsync: updateMutateAsync, isPending: false })),
  useDeleteBot: vi.fn(() => ({ mutateAsync: deleteMutateAsync, isPending: false })),
  useTestBotConnection: vi.fn(() => ({
    mutateAsync: testConnectionMutateAsync,
    isPending: false,
    isError: false,
    error: null,
    reset: vi.fn(),
  })),
  useBotAuthorizations: vi.fn(() => ({
    data: [
      {
        id: "auth-1",
        tool_name: "fs_issue",
        action: "update",
        scope_kind: "fastship_project",
        scope_id: "proj-1",
        environment_id: null,
        constraints: { fields: ["body"] },
        created_at: "2026-10-15T00:00:00Z",
        revoked_at: null,
      },
    ],
    isLoading: false,
  })),
  useRevokeAuthorization: vi.fn(() => ({
    mutateAsync: revokeMutateAsync,
    isPending: false,
  })),
}));

const { chatDecide, chatCancel, chatStop, chatSend } = vi.hoisted(() => ({
  chatDecide: vi.fn(),
  chatCancel: vi.fn(),
  chatStop: vi.fn(),
  chatSend: vi.fn(),
}));

const chatOverrides = vi.hoisted(() => ({ current: {} as Record<string, unknown> }));

vi.mock("@/lib/hooks/use-bot-chat", () => ({
  BOT_HISTORY_PAGE: 50,
  useBotChat: () => ({
    loading: false,
    loadError: null,
    entries: [],
    calls: [],
    activeRun: null,
    runActive: false,
    stopRequested: false,
    backgroundCalls: [],
    streamStatus: "open",
    streamError: null,
    resumeStream: vi.fn(),
    retry: vi.fn(),
    sending: false,
    sendError: null,
    sendMessage: chatSend,
    stopping: false,
    stopError: null,
    stopRun: chatStop,
    busyCallId: null,
    callActionError: null,
    decide: chatDecide,
    cancelCall: chatCancel,
    loadingOlder: false,
    olderError: null,
    hasMoreHistory: false,
    loadOlder: vi.fn(),
    ...chatOverrides.current,
  }),
}));

beforeEach(() => {
  vi.clearAllMocks();
  chatOverrides.current = {};
  // mockReturnValue 的实现不会被 clearAllMocks 清掉，每例前重放默认值。
  vi.mocked(useBots).mockReturnValue({
    data: [fakeBot],
    isLoading: false,
  } as never);
  vi.mocked(useBot).mockReturnValue({
    data: fakeBot,
    isLoading: false,
  } as never);
});

describe("BotsPage", () => {
  it("左栏渲染机器人名称、最近消息预览与新建入口", () => {
    renderWithRoute(<BotsPage />, { path: "/bots", initialEntry: "/bots" });

    const link = screen.getByRole("link", { name: "发布助手" });
    expect(link).toHaveAttribute("href", "/bots/b1");
    expect(within(link).getByText("已帮你盯好这次发布")).toBeInTheDocument();
    expect(
      screen.getAllByRole("button", { name: "新建机器人" }).length,
    ).toBeGreaterThan(0);
    // 未选机器人：中栏是空状态。
    expect(screen.getByText("从左侧选择一个机器人")).toBeInTheDocument();
  });

  it("无机器人时左栏显示空状态", async () => {
    const { useBots } = await import("@/lib/hooks/use-bots");
    vi.mocked(useBots).mockReturnValue({ data: [], isLoading: false } as never);
    renderWithRoute(<BotsPage />, { path: "/bots", initialEntry: "/bots" });

    expect(screen.getByText("还没有机器人")).toBeInTheDocument();
    expect(screen.getByText(/点击右上角/)).toBeInTheDocument();
  });

  it("用户消息的预览带「你：」前缀，assistant 不加", async () => {
    vi.mocked(useBots).mockReturnValue({
      data: [
        {
          ...fakeBot,
          last_message: { kind: "user", text: "帮我汇总一下" },
        },
        {
          ...fakeBot,
          id: "b2",
          name: "巡检",
          last_message: { kind: "assistant", text: "全部正常" },
        },
      ],
      isLoading: false,
    } as never);
    renderWithRoute(<BotsPage />, { path: "/bots", initialEntry: "/bots" });

    // 前缀只影响视觉文本，可访问名仍是机器人名称。
    const mine = screen.getByRole("link", { name: "发布助手" });
    expect(mine).toHaveTextContent("你：帮我汇总一下");
    const other = screen.getByRole("link", { name: "巡检" });
    expect(other).toHaveTextContent("全部正常");
    expect(other.textContent).not.toContain("你：");
  });

  it("creates a bot with name and a picked provider model", async () => {
    createMutateAsync.mockResolvedValue(fakeBot);
    const user = userEvent.setup();
    renderWithRoute(<BotsPage />, { path: "/bots", initialEntry: "/bots" });

    // 左栏图标钮与中栏空状态按钮同名，点其一即可。
    await user.click(screen.getAllByRole("button", { name: "新建机器人" })[0]);
    const dialog = await screen.findByRole("dialog");

    await user.type(within(dialog).getByLabelText("名称"), "发布助手");
    // 新建不预填人设：留空即提交空 persona_prompt（默认人设由服务端注入）。
    expect(within(dialog).getByLabelText("人设提示词")).toHaveValue("");
    // 模型走 ModelPicker：面板 portal 到 body，行 role=option。
    await user.click(within(dialog).getByRole("button", { name: "模型" }));
    const listbox = await screen.findByRole("listbox");
    await user.click(
      within(listbox).getByRole("option", { name: /GPT Pro/ }),
    );
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(createMutateAsync).toHaveBeenCalledTimes(1));
    expect(createMutateAsync.mock.calls[0][0]).toEqual({
      name: "发布助手",
      persona_prompt: "",
      provider_id: "ap-1",
      model: "gpt-pro",
    });
  });
});

describe("BotDetailPage", () => {
  const renderDetail = () =>
    renderWithRoute(<BotDetailPage />, {
      path: "/bots/:botId",
      initialEntry: "/bots/b1",
    });

  // 右栏默认收起（INT-134），展开状态持久化在 store 里。需要看右栏内容的
  // 用例显式展开；默认收起的用例自己把它设回去。
  beforeEach(() => {
    useBotDetailPanelStore.setState({ expanded: true });
  });

  it("右栏默认收起，展开后记住选择", async () => {
    const user = userEvent.setup();
    useBotDetailPanelStore.setState({ expanded: false });
    renderDetail();

    await screen.findByRole("heading", { name: "发布助手" });
    expect(screen.queryByTestId("bot-detail-panel")).toBeNull();

    await user.click(screen.getByRole("button", { name: "显示详情" }));
    expect(await screen.findByTestId("bot-detail-panel")).toBeInTheDocument();
    // 展开状态落在持久化的 store 里，刷新后沿用。
    expect(useBotDetailPanelStore.getState().expanded).toBe(true);
  });

  it("shows the bot name in the header and the empty main line", async () => {
    renderDetail();
    expect(
      await screen.findByRole("heading", { name: "发布助手" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/和机器人打个招呼吧/)).toBeInTheDocument();
    expect(
      screen.getByRole("textbox", { name: "机器人消息内容" }),
    ).toBeInTheDocument();
  });

  it("左栏选中项带 aria-current", async () => {
    renderDetail();
    expect(
      await screen.findByRole("link", { name: "发布助手" }),
    ).toHaveAttribute("aria-current", "page");
  });

  it("右栏展示配置摘要与定时任务区", async () => {
    renderDetail();
    const panel = await screen.findByTestId("bot-detail-panel");

    expect(panel).toHaveTextContent("发布助手");
    expect(within(panel).getAllByText("gpt-test").length).toBeGreaterThan(0);
    expect(panel).toHaveTextContent("DeepSeek 主号");
    expect(panel).toHaveTextContent("···1234");
    expect(panel).toHaveTextContent("创建时间");
    expect(
      within(panel).getByTestId("bot-schedule-section"),
    ).toBeInTheDocument();
    expect(
      within(panel).getByRole("button", { name: "删除机器人" }),
    ).toBeInTheDocument();
  });

  it("详情未加载时右栏不卸载：骨架占位，页头用列表名不回跳", async () => {
    // 切换机器人时详情查询重新加载：右栏保持挂载、中栏宽度不变。
    vi.mocked(useBot).mockReturnValue({
      data: undefined,
      isLoading: true,
    } as never);
    renderDetail();

    expect(
      await screen.findByTestId("bot-detail-skeleton"),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("bot-detail-panel")).toBeNull();
    // 标题取列表缓存里的名字，不掉回「机器人」。
    expect(
      screen.getByRole("heading", { name: "发布助手" }),
    ).toBeInTheDocument();
  });

  it("「详情」打开 Sheet：有可访问名，与右栏是同一份面板", async () => {
    const user = userEvent.setup();
    renderDetail();

    await user.click(screen.getByRole("button", { name: "详情" }));
    const sheet = await screen.findByRole("dialog", { name: "发布助手详情" });
    expect(
      within(sheet).getByTestId("bot-detail-panel"),
    ).toBeInTheDocument();
    // 桌面 aside 与 Sheet 各挂一份面板，但共享工作台的弹窗与状态。
    expect(screen.getAllByTestId("bot-detail-panel")).toHaveLength(2);
  });

  it("移动端有明确的回列表入口", async () => {
    renderDetail();
    expect(
      await screen.findByRole("link", { name: "机器人列表" }),
    ).toHaveAttribute("href", "/bots");
  });

  it("「隐藏详情」移除右栏，「显示详情」恢复", async () => {
    const user = userEvent.setup();
    renderDetail();
    const panel = await screen.findByTestId("bot-detail-panel");

    // 页头切换钮与面板右上角 X 同名，点面板上的那个。
    await user.click(within(panel).getByRole("button", { name: "隐藏详情" }));
    expect(screen.queryByTestId("bot-detail-panel")).toBeNull();
    expect(useBotDetailPanelStore.getState().expanded).toBe(false);

    await user.click(screen.getByRole("button", { name: "显示详情" }));
    expect(await screen.findByTestId("bot-detail-panel")).toBeInTheDocument();
  });

  it("sends a message from the composer", async () => {
    chatSend.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderDetail();

    await user.type(
      screen.getByRole("textbox", { name: "机器人消息内容" }),
      "帮我看看问题",
    );
    await user.click(screen.getByRole("button", { name: "发送" }));

    await waitFor(() => expect(chatSend).toHaveBeenCalledWith("帮我看看问题"));
  });

  it("renders confirmation cards for pending writes and forwards decisions", async () => {
    chatDecide.mockResolvedValue(undefined);
    chatOverrides.current = {
      entries: [
        {
          message: {
            id: "m-u",
            bot_id: "b1",
            seq: 1,
            kind: "user",
            content: { text: "把项目描述改成新文案" },
            created_at: "2026-10-15T00:00:00Z",
          },
        },
        {
          message: {
            id: "m-a",
            bot_id: "b1",
            seq: 2,
            kind: "assistant",
            content: {
              text: "我来修改，需要确认。",
              tool_calls: [
                { call_id: "c1", tool_name: "fs_project", action: "update" },
              ],
            },
            created_at: "2026-10-15T00:00:10Z",
          },
        },
      ],
      calls: [
        {
          id: "c1",
          bot_id: "b1",
          tool_name: "fs_project",
          action: "update",
          arguments: { project_id: "p1", description: "新文案" },
          execution_mode: "async",
          requires_confirmation: true,
          status: "pending_confirmation",
          created_at: "2026-10-15T00:00:10Z",
        },
      ],
    };
    const user = userEvent.setup();
    renderDetail();

    expect(screen.getByText("把项目描述改成新文案")).toBeInTheDocument();
    const card = await screen.findByTestId("bot-confirmation-card");
    expect(within(card).getByText(/fs_project\.update/)).toBeInTheDocument();
    expect(within(card).getByText(/"project_id"/)).toBeInTheDocument();

    await user.click(within(card).getByRole("button", { name: "始终允许" }));
    await waitFor(() =>
      expect(chatDecide).toHaveBeenCalledWith("c1", "always_allow"),
    );

    await user.click(within(card).getByRole("button", { name: "拒绝" }));
    await waitFor(() => expect(chatDecide).toHaveBeenCalledWith("c1", "reject"));
  });

  it("shows the run bar with stop separate from background cancels", async () => {
    chatStop.mockResolvedValue(undefined);
    chatCancel.mockResolvedValue(undefined);
    chatOverrides.current = {
      runActive: true,
      activeRun: {
        id: "r1",
        bot_id: "b1",
        trigger_source: "user_message",
        status: "running",
        usage_complete: true,
        total_tokens: 456,
        created_at: "2026-10-15T00:00:20Z",
      },
      backgroundCalls: [
        {
          id: "c1",
          bot_id: "b1",
          tool_name: "fs_issue",
          action: "comment",
          arguments: {},
          execution_mode: "async",
          requires_confirmation: false,
          status: "queued",
          created_at: "2026-10-15T00:00:30Z",
        },
      ],
    };
    const user = userEvent.setup();
    renderDetail();

    // 主线停止与后台取消是两个入口。
    const runBar = await screen.findByTestId("bot-run-bar");
    expect(within(runBar).getByTestId("bot-run-status")).toHaveTextContent("运行中");
    expect(within(runBar).getByText("456 tokens")).toBeInTheDocument();

    const background = await screen.findByTestId("bot-background-calls");
    expect(within(background).getByText("后台调用（1）")).toBeInTheDocument();

    await user.click(
      within(runBar).getByRole("button", { name: "请求停止" }),
    );
    await waitFor(() => expect(chatStop).toHaveBeenCalled());
    expect(chatCancel).not.toHaveBeenCalled();

    await user.click(
      within(background).getByRole("button", { name: "取消" }),
    );
    await waitFor(() => expect(chatCancel).toHaveBeenCalledWith("c1"));
  });

  it("回复中把等待动画放在内容最底部，输入框里不再有文字提示", async () => {
    chatOverrides.current = {
      runActive: true,
      entries: [
        {
          message: {
            id: "m1",
            bot_id: "b1",
            session_kind: "main",
            seq: 1,
            role: "user",
            kind: "user",
            content: { text: "把定时任务重启一下" },
            created_at: "2026-10-15T00:00:20Z",
          },
        },
      ],
      activeRun: {
        id: "r1",
        bot_id: "b1",
        trigger_source: "user_message",
        status: "running",
        created_at: "2026-10-15T00:00:20Z",
      },
      // 后台调用面板和活动 run 可以同时在场：等待动画必须在它之后，
      // 贴底时才不会被面板顶出可见区。
      backgroundCalls: [
        {
          id: "c1",
          bot_id: "b1",
          tool_name: "fs_issue",
          action: "comment",
          arguments: {},
          execution_mode: "async",
          requires_confirmation: false,
          status: "queued",
          created_at: "2026-10-15T00:00:30Z",
        },
      ],
    };
    renderDetail();

    const indicator = await screen.findByTestId("bot-typing-indicator");
    expect(indicator).toHaveAccessibleName("机器人正在回复");
    const background = await screen.findByTestId("bot-background-calls");
    expect(
      background.compareDocumentPosition(indicator) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    // 提示从输入框里挑走了：表单内不再有那句长文案。
    const composer = screen.getByRole("form", { name: "机器人消息输入" });
    expect(composer).not.toHaveTextContent("机器人正在处理中");
  });

  it("shows maintenance progress in the run bar with distinct styling on failure", async () => {
    chatOverrides.current = {
      runActive: true,
      activeRun: {
        id: "r1",
        bot_id: "b1",
        trigger_source: "user_message",
        status: "running",
        created_at: "2026-10-15T00:00:20Z",
      },
      maintenanceProgress: {
        runId: "r1",
        phase: "summary",
        message: "正在生成上下文摘要（第 2 批）…",
      },
    };
    renderDetail();

    const bar = await screen.findByTestId("bot-run-bar");
    const progress = await screen.findByTestId("bot-maintenance-progress");
    expect(progress).toHaveTextContent("正在生成上下文摘要（第 2 批）…");
    // 非失败阶段是安静的说明文字。
    expect(progress).not.toHaveClass("text-amber-600");
    expect(within(bar).getByText("请求停止")).toBeInTheDocument();

    // phase=failed 用醒目但非报错的样式：卸载当前渲染后换 override 重挂。
    cleanup();
    chatOverrides.current = {
      runActive: true,
      activeRun: {
        id: "r1",
        bot_id: "b1",
        trigger_source: "user_message",
        status: "running",
        created_at: "2026-10-15T00:00:20Z",
      },
      maintenanceProgress: {
        runId: "r1",
        phase: "failed",
        message: "压缩目标不可达：固定前缀+最小摘要预算 ≥ 触发阈值",
      },
    };
    renderDetail();
    const failed = await screen.findByTestId("bot-maintenance-progress");
    expect(failed).toHaveTextContent("压缩目标不可达");
    expect(failed).toHaveClass("text-amber-600");
  });

  it("does not show maintenance progress owned by another run", async () => {
    chatOverrides.current = {
      runActive: true,
      activeRun: {
        id: "r2",
        bot_id: "b1",
        trigger_source: "user_message",
        status: "running",
        created_at: "2026-10-15T00:00:30Z",
      },
      // 旧 run 的进度：不得出现在当前 run 的运行栏。
      maintenanceProgress: {
        runId: "r1",
        phase: "summary",
        message: "旧 run 的进度",
      },
    };
    renderDetail();
    await screen.findByTestId("bot-run-bar");
    expect(screen.queryByTestId("bot-maintenance-progress")).toBeNull();
  });

  it("lists authorizations with scope and field constraints, and revokes", async () => {
    revokeMutateAsync.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderDetail();

    const panel = await screen.findByTestId("bot-detail-panel");
    await user.click(within(panel).getByRole("button", { name: "授权管理" }));
    const dialog = await screen.findByRole("dialog");
    const item = await within(dialog).findByTestId("bot-authorization-item");
    expect(item).toHaveTextContent("fs_issue.update");
    expect(item).toHaveTextContent("fastship_project：proj-1");
    expect(item).toHaveTextContent("允许写入字段：fields");

    await user.click(
      within(item).getByRole("button", { name: "撤销 fs_issue.update 授权" }),
    );
    await waitFor(() => expect(revokeMutateAsync).toHaveBeenCalledWith("auth-1"));
  });

  it("opens the edit dialog from the detail panel", async () => {
    const user = userEvent.setup();
    renderDetail();

    const panel = await screen.findByTestId("bot-detail-panel");
    await user.click(within(panel).getByRole("button", { name: "编辑" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("名称")).toHaveValue("发布助手");
  });

  it("edit dialog prefills provider+model via the model picker", async () => {
    const user = userEvent.setup();
    renderDetail();

    const panel = await screen.findByTestId("bot-detail-panel");
    await user.click(within(panel).getByRole("button", { name: "编辑" }));
    const dialog = await screen.findByRole("dialog");

    // 密钥/地址在提供商侧管理，机器人表单不再出现这些字段。
    expect(within(dialog).queryByLabelText("API Key")).toBeNull();
    expect(within(dialog).queryByLabelText("API 地址")).toBeNull();
    expect(within(dialog).getByLabelText("名称")).toHaveValue("发布助手");
    expect(
      within(dialog).getByRole("button", { name: "模型" }),
    ).toHaveTextContent("GPT Test");
  });

  it("tests connection against the picked provider and model", async () => {
    testConnectionMutateAsync.mockResolvedValue({
      ok: true,
      checks: [{ name: "streaming", passed: true }],
    });
    const user = userEvent.setup();
    renderDetail();

    const panel = await screen.findByTestId("bot-detail-panel");
    await user.click(within(panel).getByRole("button", { name: "编辑" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: "测试连接" }),
    );

    await waitFor(() =>
      expect(testConnectionMutateAsync).toHaveBeenCalledWith({
        bot_id: "b1",
        provider_id: "ap-1",
        model: "gpt-test",
      }),
    );
    // 逐项检查结果渲染出来。
    expect(
      await screen.findByTestId("bot-connection-checks"),
    ).toBeInTheDocument();
  });

  it("delete asks for confirmation and hits the endpoint", async () => {
    deleteMutateAsync.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderDetail();

    const panel = await screen.findByTestId("bot-detail-panel");
    await user.click(
      within(panel).getByRole("button", { name: "删除机器人" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    await user.click(
      within(dialog).getByRole("button", { name: "确认删除" }),
    );
    await waitFor(() => expect(deleteMutateAsync).toHaveBeenCalledWith("b1"));
  });
});

describe("App 路由注册", () => {
  function renderApp(entry: string) {
    // App 用 BrowserRouter；测试里以真实组件渲染并依赖未登录重定向验证注册。
    window.history.replaceState(null, "", entry);
    return render(<App />);
  }

  it("registers /bots", async () => {
    const view = renderApp("/bots");
    // 未登录 → AppLayout 重定向登录页（登录页标题）。
    expect((await screen.findAllByText("登录")).length).toBeGreaterThan(0);
    view.unmount();
  });

  it("registers /bots/:botId", async () => {
    const view = renderApp("/bots/b1");
    expect((await screen.findAllByText("登录")).length).toBeGreaterThan(0);
    view.unmount();
  });
});
