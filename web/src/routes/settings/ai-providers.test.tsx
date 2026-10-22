import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import AIProvidersPage from "@/routes/settings/ai-providers";
import type { AIProvider, CatalogProvider } from "@/lib/api/ai-providers";
import {
  useAIProviders,
  useAIProviderCatalog,
  useCreateAIProvider,
  useUpdateAIProvider,
  useDeleteAIProvider,
  useTestAIProvider,
} from "@/lib/hooks/use-ai-providers";
import { renderWithRoute } from "@/test/render";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("@/lib/hooks/use-ai-providers", () => ({
  useAIProviders: vi.fn(),
  useAIProviderCatalog: vi.fn(),
  useCreateAIProvider: vi.fn(),
  useUpdateAIProvider: vi.fn(),
  useDeleteAIProvider: vi.fn(),
  useTestAIProvider: vi.fn(),
}));

vi.mock("@/lib/hooks/use-notifications", () => ({
  NOTIFICATION_UNREAD_POLL_MS: 30000,
  useNotifications: vi.fn(() => ({ data: [] })),
  useUnreadNotificationCount: vi.fn(() => ({ data: 0 })),
  useMarkNotificationRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
  useMarkAllNotificationsRead: vi.fn(() => ({ mutate: vi.fn(), mutateAsync: vi.fn() })),
}));

const providerFixture: AIProvider = {
  id: "ap-1",
  catalog_id: "deepseek",
  name: "DeepSeek 主号",
  base_url: "https://api.deepseek.com/v1",
  api_key_last4: "ab12",
  bot_count: 2,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

const catalogFixture: CatalogProvider[] = [
  {
    id: "deepseek",
    name: "DeepSeek",
    api: "https://api.deepseek.com/v1",
    models: [
      { id: "deepseek-flash", name: "DeepSeek Flash", context_window: 131072, max_output_tokens: 0, reasoning: false },
    ],
  },
  {
    id: "openai",
    name: "OpenAI",
    api: "https://api.openai.com/v1",
    models: [{ id: "gpt-5", name: "GPT-5", context_window: 272000, max_output_tokens: 0, reasoning: false }],
  },
];

const createMutateAsync = vi.fn();
const updateMutateAsync = vi.fn();
const deleteMutateAsync = vi.fn();
const testMutateAsync = vi.fn();

function renderPage() {
  return renderWithRoute(<AIProvidersPage />, {
    path: "/settings/ai-providers",
    initialEntry: "/settings/ai-providers",
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(useAIProviders).mockReturnValue({
    data: [providerFixture],
    isLoading: false,
  } as unknown as ReturnType<typeof useAIProviders>);
  vi.mocked(useAIProviderCatalog).mockReturnValue({
    data: catalogFixture,
    isLoading: false,
  } as unknown as ReturnType<typeof useAIProviderCatalog>);
  vi.mocked(useCreateAIProvider).mockReturnValue({
    mutateAsync: createMutateAsync,
    isPending: false,
  } as unknown as ReturnType<typeof useCreateAIProvider>);
  vi.mocked(useUpdateAIProvider).mockReturnValue({
    mutateAsync: updateMutateAsync,
    isPending: false,
  } as unknown as ReturnType<typeof useUpdateAIProvider>);
  vi.mocked(useDeleteAIProvider).mockReturnValue({
    mutateAsync: deleteMutateAsync,
    isPending: false,
  } as unknown as ReturnType<typeof useDeleteAIProvider>);
  vi.mocked(useTestAIProvider).mockReturnValue({
    mutateAsync: testMutateAsync,
    isPending: false,
    isError: false,
    error: null,
    reset: vi.fn(),
  } as unknown as ReturnType<typeof useTestAIProvider>);
});

describe("AIProvidersPage", () => {
  it("列表渲染名称、地址、Key 末四位与引用数", () => {
    renderPage();

    const row = screen.getByRole("row", { name: /DeepSeek 主号/ });
    expect(row).toHaveTextContent("https://api.deepseek.com/v1");
    expect(row).toHaveTextContent("···ab12");
    expect(row).toHaveTextContent("2 个机器人");
  });

  it("空列表显示空态", () => {
    vi.mocked(useAIProviders).mockReturnValue({
      data: [],
      isLoading: false,
    } as unknown as ReturnType<typeof useAIProviders>);
    renderPage();

    expect(screen.getByText("还没有配置 AI 提供商")).toBeInTheDocument();
  });

  it("列表查询失败：错误条 + 重试，不渲染成空态", async () => {
    const refetch = vi.fn();
    vi.mocked(useAIProviders).mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      refetch,
    } as unknown as ReturnType<typeof useAIProviders>);
    const user = userEvent.setup();
    renderPage();

    expect(screen.queryByText("还没有配置 AI 提供商")).toBeNull();
    await user.click(screen.getByRole("button", { name: "重试" }));
    expect(refetch).toHaveBeenCalled();
  });

  it("新建流程：搜索选目录提供商 → 填 key → 测试连接 → 保存", async () => {
    testMutateAsync.mockResolvedValue({
      ok: true,
      checks: [{ name: "models", passed: true }],
    });
    createMutateAsync.mockResolvedValue(providerFixture);
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: "添加提供商" }));
    const dialog = await screen.findByRole("dialog");

    // 搜索过滤目录，选中 DeepSeek。
    await user.type(within(dialog).getByLabelText("提供商"), "deep");
    const list = within(dialog).getByRole("listbox", { name: "提供商目录" });
    await user.click(within(list).getByRole("option", { name: /DeepSeek/ }));

    // 目录提供商不显示地址输入。
    expect(within(dialog).queryByLabelText("API 地址")).toBeNull();

    await user.type(within(dialog).getByLabelText("API Key"), "sk-live-1");
    await user.click(
      within(dialog).getByRole("button", { name: "测试连接" }),
    );
    await waitFor(() =>
      expect(testMutateAsync).toHaveBeenCalledWith({
        catalog_id: "deepseek",
        api_key: "sk-live-1",
      }),
    );
    expect(
      await within(dialog).findByTestId("provider-connection-checks"),
    ).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(createMutateAsync).toHaveBeenCalledWith({
        catalog_id: "deepseek",
        name: undefined,
        base_url: undefined,
        api_key: "sk-live-1",
      }),
    );
  });

  it("目录选项键盘可达：Tab 进入列表、Enter 选中", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: "添加提供商" }));
    const dialog = await screen.findByRole("dialog");

    // 搜索框之后第一个可聚焦元素就是「自定义」选项行。
    within(dialog).getByLabelText("提供商").focus();
    await user.tab();
    const custom = within(dialog).getByRole("option", { name: /^自定义/ });
    expect(custom).toHaveFocus();

    await user.tab();
    const deepseek = within(dialog).getByRole("option", { name: /^DeepSeek/ });
    expect(deepseek).toHaveFocus();

    await user.keyboard("{Enter}");
    // Enter 激活选中目录提供商：出现 API Key 字段，不出现 custom 的地址输入。
    expect(within(dialog).getByLabelText("API Key")).toBeInTheDocument();
    expect(within(dialog).queryByLabelText("API 地址")).toBeNull();
  });

  it("目录加载失败：不显示「没有匹配的提供商」，给错误条与重试", async () => {
    const refetch = vi.fn();
    vi.mocked(useAIProviderCatalog).mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      refetch,
    } as unknown as ReturnType<typeof useAIProviderCatalog>);
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: "添加提供商" }));
    const dialog = await screen.findByRole("dialog");

    expect(within(dialog).queryByText("没有匹配的提供商")).toBeNull();
    const panel = within(dialog).getByTestId("provider-catalog-error");
    expect(panel).toHaveTextContent("目录加载失败");
    await user.click(within(panel).getByRole("button", { name: "重试" }));
    expect(refetch).toHaveBeenCalled();
  });

  it("测试连接结果随被测目标失效：改 API Key 后检查结果隐藏", async () => {
    testMutateAsync.mockResolvedValue({
      ok: true,
      checks: [{ name: "models", passed: true }],
    });
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: "添加提供商" }));
    const dialog = await screen.findByRole("dialog");
    const list = within(dialog).getByRole("listbox", { name: "提供商目录" });
    await user.click(within(list).getByRole("option", { name: /DeepSeek/ }));

    const keyInput = within(dialog).getByLabelText("API Key");
    await user.type(keyInput, "sk-live-1");
    await user.click(
      within(dialog).getByRole("button", { name: "测试连接" }),
    );
    expect(
      await within(dialog).findByTestId("provider-connection-checks"),
    ).toBeInTheDocument();

    // 目标字段再变，旧结果即隐藏。
    await user.type(keyInput, "-rotated");
    expect(
      within(dialog).queryByTestId("provider-connection-checks"),
    ).toBeNull();
  });

  it("选「自定义」才显示地址与模型输入，保存带 base_url", async () => {
    createMutateAsync.mockResolvedValue(providerFixture);
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: "添加提供商" }));
    const dialog = await screen.findByRole("dialog");
    const list = within(dialog).getByRole("listbox", { name: "提供商目录" });
    await user.click(within(list).getByRole("option", { name: /^自定义/ }));

    await user.type(
      within(dialog).getByLabelText("API 地址"),
      "https://gw.internal/v1",
    );
    await user.type(
      within(dialog).getByLabelText("模型（用于测试连接）"),
      "my-model",
    );
    await user.type(within(dialog).getByLabelText("API Key"), "sk-c");
    await user.click(
      within(dialog).getByRole("button", { name: "测试连接" }),
    );
    await waitFor(() =>
      expect(testMutateAsync).toHaveBeenCalledWith({
        catalog_id: "custom",
        base_url: "https://gw.internal/v1",
        model: "my-model",
        api_key: "sk-c",
      }),
    );

    await user.click(within(dialog).getByRole("button", { name: "保存" }));
    await waitFor(() =>
      expect(createMutateAsync).toHaveBeenCalledWith({
        catalog_id: "custom",
        name: undefined,
        base_url: "https://gw.internal/v1",
        api_key: "sk-c",
      }),
    );
  });

  it("编辑：改名与轮换密钥，未填密钥时测试连接禁用", async () => {
    updateMutateAsync.mockResolvedValue(providerFixture);
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: "编辑 DeepSeek 主号" }));
    const dialog = await screen.findByRole("dialog");

    expect(within(dialog).getByLabelText("名称")).toHaveValue("DeepSeek 主号");
    // 目录提供商地址由目录决定：只读展示，不出现输入框。
    expect(within(dialog).queryByLabelText("API 地址")).toBeNull();
    expect(
      within(dialog).getByRole("button", { name: "测试连接" }),
    ).toBeDisabled();

    const nameInput = within(dialog).getByLabelText("名称");
    await user.clear(nameInput);
    await user.type(nameInput, "DeepSeek 备用");
    await user.type(within(dialog).getByLabelText("API Key"), "sk-new-9");
    await user.click(within(dialog).getByRole("button", { name: "保存" }));

    await waitFor(() =>
      expect(updateMutateAsync).toHaveBeenCalledWith({
        name: "DeepSeek 备用",
        api_key: "sk-new-9",
      }),
    );
  });

  it("编辑自定义提供商：改模型后旧检查结果隐藏", async () => {
    vi.mocked(useAIProviders).mockReturnValue({
      data: [
        {
          ...providerFixture,
          id: "ap-9",
          catalog_id: "custom",
          name: "自建网关",
          base_url: "https://gw.internal/v1",
          bot_count: 0,
        },
      ],
      isLoading: false,
    } as unknown as ReturnType<typeof useAIProviders>);
    testMutateAsync.mockResolvedValue({
      ok: true,
      checks: [{ name: "chat", passed: true }],
    });
    const user = userEvent.setup();
    renderPage();

    await user.click(screen.getByRole("button", { name: "编辑 自建网关" }));
    const dialog = await screen.findByRole("dialog");
    const modelInput = within(dialog).getByLabelText("模型（用于测试连接）");
    await user.type(modelInput, "m1");
    await user.type(within(dialog).getByLabelText("API Key"), "sk-1");
    await user.click(
      within(dialog).getByRole("button", { name: "测试连接" }),
    );
    expect(
      await within(dialog).findByTestId("provider-connection-checks"),
    ).toBeInTheDocument();

    await user.type(modelInput, "2");
    expect(
      within(dialog).queryByTestId("provider-connection-checks"),
    ).toBeNull();
  });

  it("被机器人引用的提供商：确认框只给「知道了」，不给破坏性按钮", async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(
      screen.getByRole("button", { name: "删除 DeepSeek 主号" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("仍被 2 个机器人引用");
    expect(
      within(dialog).queryByRole("button", { name: "确认删除" }),
    ).toBeNull();

    await user.click(
      within(dialog).getByRole("button", { name: "知道了" }),
    );
    expect(deleteMutateAsync).not.toHaveBeenCalled();
  });

  it("未被引用的提供商：确认框给确认删除，调用删除端点", async () => {
    vi.mocked(useAIProviders).mockReturnValue({
      data: [{ ...providerFixture, bot_count: 0 }],
      isLoading: false,
    } as unknown as ReturnType<typeof useAIProviders>);
    deleteMutateAsync.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage();

    await user.click(
      screen.getByRole("button", { name: "删除 DeepSeek 主号" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("删除后无法恢复");
    await user.click(
      within(dialog).getByRole("button", { name: "确认删除" }),
    );
    await waitFor(() => expect(deleteMutateAsync).toHaveBeenCalledWith("ap-1"));
  });
});
