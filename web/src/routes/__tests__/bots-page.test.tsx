import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import BotsPage from "@/routes/bots";
import BotDetailPage from "@/routes/bots/$botId";
import App from "@/App";
import { renderWithRoute } from "@/test/render";
import type { Bot } from "@/lib/api/bots";
import { useBots } from "@/lib/hooks/use-bots";

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
  base_url: "https://api.example.com/v1",
  model: "gpt-test",
  api_key_last4: "1234",
  next_message_seq: 2,
  next_event_seq: 10,
  created_at: "2026-10-15T00:00:00Z",
  updated_at: "2026-10-15T00:00:00Z",
};

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
});

describe("BotsPage", () => {
  it("renders bot rows with masked key and entry points", () => {
    renderWithRoute(<BotsPage />, { path: "/bots", initialEntry: "/bots" });

    expect(screen.getByRole("heading", { name: "机器人" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "发布助手" })).toHaveAttribute(
      "href",
      "/bots/b1",
    );
    expect(screen.getByText("gpt-test")).toBeInTheDocument();
    expect(screen.getByText("···1234")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "新建机器人" }),
    ).toBeInTheDocument();
    // 对话入口用 render={<Link/>}，渲染为链接而非按钮。
    expect(
      screen.getByRole("link", { name: "对话 发布助手" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "编辑 发布助手" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "删除 发布助手" }),
    ).toBeInTheDocument();
  });

  it("shows the empty state when no bots exist", async () => {
    const { useBots } = await import("@/lib/hooks/use-bots");
    vi.mocked(useBots).mockReturnValue({ data: [], isLoading: false } as never);
    renderWithRoute(<BotsPage />, { path: "/bots", initialEntry: "/bots" });

    expect(screen.getByText("还没有机器人")).toBeInTheDocument();
    expect(
      screen.getByText(/填入 OpenAI 兼容接口的地址与 Key/),
    ).toBeInTheDocument();
  });

  it("creates a bot with name, persona, endpoint, model and key", async () => {
    createMutateAsync.mockResolvedValue(fakeBot);
    const user = userEvent.setup();
    renderWithRoute(<BotsPage />, { path: "/bots", initialEntry: "/bots" });

    await user.click(screen.getByRole("button", { name: "新建机器人" }));
    const dialog = await screen.findByRole("dialog");

    await user.type(within(dialog).getByLabelText("名称"), "发布助手");
    await user.type(within(dialog).getByLabelText("人设提示词"), "盯发布");
    await user.type(
      within(dialog).getByLabelText("API 地址"),
      "https://api.example.com/v1",
    );
    await user.type(within(dialog).getByLabelText("模型"), "gpt-test");
    await user.type(within(dialog).getByLabelText("API Key"), "sk-live-1");
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() => expect(createMutateAsync).toHaveBeenCalledTimes(1));
    expect(createMutateAsync.mock.calls[0][0]).toEqual({
      name: "发布助手",
      persona_prompt: "盯发布",
      base_url: "https://api.example.com/v1",
      model: "gpt-test",
      api_key: "sk-live-1",
    });
  });

  it("edit dialog keeps the key empty and only shows its last four digits", async () => {
    const user = userEvent.setup();
    renderWithRoute(<BotsPage />, { path: "/bots", initialEntry: "/bots" });

    await user.click(screen.getByRole("button", { name: "编辑 发布助手" }));
    const dialog = await screen.findByRole("dialog");

    const keyInput = within(dialog).getByLabelText("API Key") as HTMLInputElement;
    expect(keyInput).toHaveValue("");
    expect(within(dialog).getByText(/末四位 1234/)).toBeInTheDocument();
    // 已存配置不回填到表单。
    expect(within(dialog).getByLabelText("名称")).toHaveValue("发布助手");
  });

  it("tests connection against the stored key with form overrides", async () => {
    testConnectionMutateAsync.mockResolvedValue({
      ok: true,
      checks: [{ name: "streaming", passed: true }],
    });
    const user = userEvent.setup();
    renderWithRoute(<BotsPage />, { path: "/bots", initialEntry: "/bots" });

    await user.click(screen.getByRole("button", { name: "编辑 发布助手" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: "测试连接" }),
    );

    await waitFor(() =>
      expect(testConnectionMutateAsync).toHaveBeenCalledWith({
        bot_id: "b1",
        base_url: "https://api.example.com/v1",
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
    renderWithRoute(<BotsPage />, { path: "/bots", initialEntry: "/bots" });

    await user.click(screen.getByRole("button", { name: "删除 发布助手" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(
      within(dialog).getByRole("button", { name: "确认删除" }),
    );
    await waitFor(() => expect(deleteMutateAsync).toHaveBeenCalledWith("b1"));
  });
});

describe("BotDetailPage", () => {
  const renderDetail = () =>
    renderWithRoute(<BotDetailPage />, {
      path: "/bots/:botId",
      initialEntry: "/bots/b1",
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
      within(runBar).getByRole("button", { name: "停止" }),
    );
    await waitFor(() => expect(chatStop).toHaveBeenCalled());
    expect(chatCancel).not.toHaveBeenCalled();

    await user.click(
      within(background).getByRole("button", { name: "取消" }),
    );
    await waitFor(() => expect(chatCancel).toHaveBeenCalledWith("c1"));
  });

  it("lists authorizations with scope and field constraints, and revokes", async () => {
    revokeMutateAsync.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderDetail();

    await user.click(
      await screen.findByRole("button", { name: "授权管理" }),
    );
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

  it("opens the edit dialog from the detail header", async () => {
    const user = userEvent.setup();
    renderDetail();

    await user.click(
      await screen.findByRole("button", { name: "编辑" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("名称")).toHaveValue("发布助手");
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
