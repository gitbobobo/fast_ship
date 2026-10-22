import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  BotFormDialog,
} from "@/components/bots/bot-form-dialog";
import type { Bot } from "@/lib/api/bots";
import type { AIProvider, CatalogProvider } from "@/lib/api/ai-providers";
import {
  useCreateBot,
  useTestBotConnection,
  useUpdateBot,
} from "@/lib/hooks/use-bots";
import {
  useAIProviders,
  useAIProviderCatalog,
} from "@/lib/hooks/use-ai-providers";
import { renderWithRoute } from "@/test/render";

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("@/lib/hooks/use-bots", () => ({
  useCreateBot: vi.fn(),
  useUpdateBot: vi.fn(),
  useTestBotConnection: vi.fn(),
}));

vi.mock("@/lib/hooks/use-ai-providers", () => ({
  useAIProviders: vi.fn(),
  useAIProviderCatalog: vi.fn(),
}));

const catalogFixture: CatalogProvider = {
  id: "deepseek",
  name: "DeepSeek",
  api: "https://api.deepseek.com/v1",
  models: [
    {
      id: "deepseek-flash",
      name: "DeepSeek Flash",
      context_window: 131072,
      max_output_tokens: 8192,
      reasoning: false,
    },
    {
      id: "deepseek-pro",
      name: "DeepSeek Pro",
      context_window: 262144,
      max_output_tokens: 16384,
      reasoning: false,
    },
  ],
};

const providerFixture: AIProvider = {
  id: "ap-1",
  catalog_id: "deepseek",
  name: "DeepSeek 主号",
  base_url: "https://api.deepseek.com/v1",
  api_key_last4: "ab12",
  bot_count: 1,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

const customProviderFixture: AIProvider = {
  id: "ap-2",
  catalog_id: "custom",
  name: "自建网关",
  base_url: "https://gw.internal/v1",
  api_key_last4: "zz99",
  bot_count: 0,
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

const botFixture: Bot = {
  id: "bot-1",
  name: "发布助手",
  persona_prompt: "",
  provider_id: "ap-1",
  provider_name: "DeepSeek 主号",
  catalog_id: "deepseek",
  base_url: "https://api.deepseek.com/v1",
  model: "deepseek-flash",
  api_key_last4: "ab12",
  prompt_revision: 1,
  platform_prompt_version: 1,
  toolset_version: 1,
  next_event_seq: 1,
  context_window: 1048576,
  max_output_tokens: 262144,
  context_source: "detected",
  context_detail: "GET /models",
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

function renderDialog(editing: Bot | null = botFixture) {
  return renderWithRoute(
    <BotFormDialog open editing={editing} onClose={vi.fn()} />,
  );
}

const createMutateAsync = vi.fn();
const updateMutateAsync = vi.fn();
const testMutateAsync = vi.fn();

function mockProviders(
  providers: AIProvider[] = [providerFixture],
  catalog: CatalogProvider[] = [catalogFixture],
) {
  vi.mocked(useAIProviders).mockReturnValue({
    data: providers,
    isLoading: false,
  } as unknown as ReturnType<typeof useAIProviders>);
  vi.mocked(useAIProviderCatalog).mockReturnValue({
    data: catalog,
    isLoading: false,
  } as unknown as ReturnType<typeof useAIProviderCatalog>);
}

beforeEach(() => {
  vi.clearAllMocks();
  createMutateAsync.mockResolvedValue(botFixture);
  updateMutateAsync.mockResolvedValue(botFixture);
  vi.mocked(useCreateBot).mockReturnValue({
    mutateAsync: createMutateAsync,
    isPending: false,
  } as unknown as ReturnType<typeof useCreateBot>);
  vi.mocked(useUpdateBot).mockReturnValue({
    mutateAsync: updateMutateAsync,
    isPending: false,
  } as unknown as ReturnType<typeof useUpdateBot>);
  vi.mocked(useTestBotConnection).mockReturnValue({
    mutateAsync: testMutateAsync,
    isPending: false,
    isError: false,
    error: null,
    reset: vi.fn(),
  } as unknown as ReturnType<typeof useTestBotConnection>);
  mockProviders();
});

/** 打开 ModelPicker 面板：触发器 aria-label 为「模型」，面板 portal 到 body。 */
async function openPicker(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: "模型" }));
  return await screen.findByRole("listbox");
}

describe("BotFormDialog 模型选择", () => {
  it("新建：触发器回落到第一个提供商的首个模型，保存提交 provider_id+model", async () => {
    const user = userEvent.setup();
    renderDialog(null);

    await user.type(screen.getByLabelText("名称"), "发布助手");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(createMutateAsync).toHaveBeenCalledWith({
      name: "发布助手",
      persona_prompt: "",
      provider_id: "ap-1",
      model: "deepseek-flash",
    });
  });

  it("新建：列表首项是 custom 时默认落到第一个目录提供商并渲染 ModelPicker", async () => {
    // 与实测一致：ai_providers 按 created_at 倒序，最新建的 custom 排最前；
    // 默认落点应是目录里有模型的提供商而不是 aiProviders[0]。
    const newestCustom: AIProvider = {
      ...customProviderFixture,
      id: "ap-9",
      name: "本地假模型",
      created_at: "2026-10-19T00:00:00Z",
    };
    mockProviders([newestCustom, providerFixture]);
    const user = userEvent.setup();
    renderDialog(null);

    // 默认落在目录提供商：模型区是 ModelPicker 而不是手填输入框。
    expect(screen.getByRole("combobox", { name: "提供商" })).toHaveTextContent(
      "DeepSeek 主号",
    );
    expect(screen.getByRole("button", { name: "模型" })).toHaveTextContent(
      "DeepSeek Flash",
    );

    await user.type(screen.getByLabelText("名称"), "巡检");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(createMutateAsync).toHaveBeenCalledWith({
      name: "巡检",
      persona_prompt: "",
      provider_id: "ap-1",
      model: "deepseek-flash",
    });
  });

  it("新建：ModelPicker 选中回调同时给出 providerId 与 model", async () => {
    const user = userEvent.setup();
    renderDialog(null);

    const listbox = await openPicker(user);
    await user.click(
      within(listbox).getByRole("option", { name: /DeepSeek Pro/ }),
    );

    await user.type(screen.getByLabelText("名称"), "巡检");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(createMutateAsync).toHaveBeenCalledWith({
      name: "巡检",
      persona_prompt: "",
      provider_id: "ap-1",
      model: "deepseek-pro",
    });
  });

  it("编辑：回填已存 provider+model，不改选则原样提交", async () => {
    const user = userEvent.setup();
    renderDialog();

    // ModelPicker 触发器显示已存模型名而不是退回第一项。
    expect(screen.getByRole("button", { name: "模型" })).toHaveTextContent(
      "DeepSeek Flash",
    );

    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(updateMutateAsync).toHaveBeenCalledWith({
      name: "发布助手",
      persona_prompt: "",
      provider_id: "ap-1",
      model: "deepseek-flash",
    });
  });

  it("编辑：已存模型从目录下架时标注已失效，不改选仍保留原值", async () => {
    const user = userEvent.setup();
    renderDialog({ ...botFixture, model: "gone-model" });

    expect(screen.getByRole("button", { name: "模型" })).toHaveTextContent(
      "gone-model（已失效）",
    );

    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(updateMutateAsync).toHaveBeenCalledWith({
      name: "发布助手",
      persona_prompt: "",
      provider_id: "ap-1",
      model: "gone-model",
    });
  });

  it("零提供商：空态提示并链到设置页，保存不可用", () => {
    mockProviders([]);
    renderDialog(null);

    const empty = screen.getByTestId("bot-no-providers");
    expect(empty).toHaveTextContent("还没有配置 AI 提供商");
    expect(
      within(empty).getByRole("link", { name: "设置 → AI 提供商" }),
    ).toHaveAttribute("href", "/settings/ai-providers");
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
  });

  it("有自定义提供商时渲染提供商下拉，选中后回落手填模型名", async () => {
    mockProviders([providerFixture, customProviderFixture]);
    const user = userEvent.setup();
    renderDialog(null);

    await user.click(screen.getByRole("combobox", { name: "提供商" }));
    await user.click(
      await screen.findByRole("option", { name: /自建网关/ }),
    );

    const modelInput = screen.getByLabelText("模型");
    await user.type(modelInput, "my-internal-model");
    await user.type(screen.getByLabelText("名称"), "网关助手");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(createMutateAsync).toHaveBeenCalledWith({
      name: "网关助手",
      persona_prompt: "",
      provider_id: "ap-2",
      model: "my-internal-model",
    });
  });

  it("新建：所有提供商都是 custom 时默认仍是手填模型输入框", async () => {
    mockProviders([customProviderFixture]);
    const user = userEvent.setup();
    renderDialog(null);

    // 没有目录提供商可落：回落列表第一项（custom），手填模型名。
    const modelInput = screen.getByLabelText("模型");
    expect(modelInput).toHaveProperty("tagName", "INPUT");
    expect(screen.queryByRole("button", { name: "模型" })).toBeNull();

    await user.type(modelInput, "my-model");
    await user.type(screen.getByLabelText("名称"), "网关");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(createMutateAsync).toHaveBeenCalledWith({
      name: "网关",
      persona_prompt: "",
      provider_id: "ap-2",
      model: "my-model",
    });
  });

  it("提供商下拉：name 已是「自定义」的行不再重复渲染 badge", async () => {
    // 服务端对缺省 name 的 custom 提供商回填「自定义」，badge 不再追加一次。
    const namedCustom: AIProvider = {
      ...customProviderFixture,
      id: "ap-9",
      name: "自定义",
    };
    mockProviders([namedCustom, providerFixture]);
    const user = userEvent.setup();
    renderDialog(null);

    await user.click(screen.getByRole("combobox", { name: "提供商" }));
    const option = await screen.findByRole("option", { name: /^自定义/ });
    expect(within(option).getAllByText("自定义")).toHaveLength(1);
  });

  it("自定义提供商选中后仍可经下拉换回目录提供商", async () => {
    mockProviders([providerFixture, customProviderFixture]);
    const user = userEvent.setup();
    renderDialog(null);

    const select = screen.getByRole("combobox", { name: "提供商" });
    await user.click(select);
    await user.click(
      await screen.findByRole("option", { name: /自建网关/ }),
    );
    expect(screen.getByLabelText("模型")).toHaveProperty("tagName", "INPUT");

    await user.click(select);
    await user.click(
      await screen.findByRole("option", { name: /DeepSeek 主号/ }),
    );
    // 换回目录提供商后又是 ModelPicker。
    expect(screen.getByRole("button", { name: "模型" })).toBeInTheDocument();

    await user.type(screen.getByLabelText("名称"), "巡检");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(createMutateAsync).toHaveBeenCalledWith({
      name: "巡检",
      persona_prompt: "",
      provider_id: "ap-1",
      model: "deepseek-flash",
    });
  });

  it("编辑：切到 custom 清掉不再适用的 pick，切回时恢复已存选择", async () => {
    mockProviders([providerFixture, customProviderFixture]);
    const user = userEvent.setup();
    renderDialog();

    // 先在 picker 里换成 Pro。
    const listbox = await openPicker(user);
    await user.click(
      within(listbox).getByRole("option", { name: /DeepSeek Pro/ }),
    );
    expect(screen.getByRole("button", { name: "模型" })).toHaveTextContent(
      "DeepSeek Pro",
    );

    const select = screen.getByRole("combobox", { name: "提供商" });
    await user.click(select);
    await user.click(
      await screen.findByRole("option", { name: /自建网关/ }),
    );
    // 手填输入为空：不带入目录模型名当陈旧值（编辑对象不是这个提供商）。
    expect(screen.getByLabelText("模型")).toHaveValue("");

    await user.click(select);
    await user.click(
      await screen.findByRole("option", { name: /DeepSeek 主号/ }),
    );
    // 切回编辑对象自己的提供商：恢复已存 pick（Flash），不是中途选的 Pro。
    expect(screen.getByRole("button", { name: "模型" })).toHaveTextContent(
      "DeepSeek Flash",
    );
  });

  it("提供商列表加载失败：错误条 + 重试，不渲染成「还没有配置」", async () => {
    const refetch = vi.fn();
    vi.mocked(useAIProviders).mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error("boom"),
      refetch,
    } as unknown as ReturnType<typeof useAIProviders>);
    const user = userEvent.setup();
    renderDialog(null);

    const panel = screen.getByTestId("bot-providers-error");
    expect(panel).toHaveTextContent("AI 提供商加载失败");
    expect(screen.queryByTestId("bot-no-providers")).toBeNull();
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();

    await user.click(within(panel).getByRole("button", { name: "重试" }));
    expect(refetch).toHaveBeenCalled();
  });

  it("目录加载失败且有目录提供商：错误条 + 禁用保存，不静默当 custom", async () => {
    const refetch = vi.fn();
    vi.mocked(useAIProviderCatalog).mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error("boom"),
      refetch,
    } as unknown as ReturnType<typeof useAIProviderCatalog>);
    const user = userEvent.setup();
    renderDialog(null);

    const panel = screen.getByTestId("bot-catalog-error");
    expect(panel).toHaveTextContent("模型目录加载失败");
    // 不把目录提供商静默当 custom：模型输入框与 picker 都不出现。
    expect(screen.queryByLabelText("模型")).toBeNull();
    expect(screen.getByRole("button", { name: "保存" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "测试连接" })).toBeDisabled();

    await user.click(within(panel).getByRole("button", { name: "重试" }));
    expect(refetch).toHaveBeenCalled();
  });

  it("目录失败但全是自定义提供商：手填模型不受影响，仍可保存", async () => {
    mockProviders([customProviderFixture]);
    vi.mocked(useAIProviderCatalog).mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      error: new Error("boom"),
      refetch: vi.fn(),
    } as unknown as ReturnType<typeof useAIProviderCatalog>);
    const user = userEvent.setup();
    renderDialog(null);

    expect(screen.queryByTestId("bot-catalog-error")).toBeNull();
    await user.type(screen.getByLabelText("模型"), "m1");
    await user.type(screen.getByLabelText("名称"), "网关");
    await user.click(screen.getByRole("button", { name: "保存" }));
    expect(createMutateAsync).toHaveBeenCalledWith({
      name: "网关",
      persona_prompt: "",
      provider_id: "ap-2",
      model: "m1",
    });
  });
});

// 能力展示跟随当前表单目标：测试结果属于它测试时的提供商+模型，已存能力
// 属于已保存目标，目录规格按选中模型实时查；目标变了即隐藏旧展示。
describe("BotFormDialog 能力展示", () => {
  it("编辑打开且目标未变：展示已存能力（含来源与口径）", () => {
    renderDialog();

    const shown = screen.getByTestId("bot-context-window");
    expect(shown).toHaveTextContent("上下文窗口 1,048,576 tokens");
    expect(shown).toHaveTextContent("自动检测");
    expect(shown).toHaveTextContent("GET /models");
  });

  it("换到目录里的另一个模型：展示目录规格窗口", async () => {
    const user = userEvent.setup();
    renderDialog();

    const listbox = await openPicker(user);
    await user.click(
      within(listbox).getByRole("option", { name: /DeepSeek Pro/ }),
    );

    const shown = screen.getByTestId("bot-context-window");
    expect(shown).toHaveTextContent("上下文窗口 262,144 tokens");
    expect(shown).toHaveTextContent("内置目录");
  });

  it("新建模式：回落模型的目录窗口直接展示", () => {
    renderDialog(null);

    const shown = screen.getByTestId("bot-context-window");
    expect(shown).toHaveTextContent("131,072");
    expect(shown).toHaveTextContent("内置目录");
  });

  it("测试连接成功：展示本次测试返回的能力", async () => {
    testMutateAsync.mockResolvedValue({
      ok: true,
      checks: [{ name: "streaming", passed: true }],
      context_window: 9999,
      max_output_tokens: 4096,
      context_source: "detected",
      context_detail: "GET /models",
    });
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "测试连接" }));

    const shown = await screen.findByTestId("bot-context-window");
    expect(shown).toHaveTextContent("上下文窗口 9,999 tokens");
    expect(testMutateAsync).toHaveBeenCalledWith({
      bot_id: "bot-1",
      provider_id: "ap-1",
      model: "deepseek-flash",
    });
  });

  it("测试后目标又变：测试结果隐藏，按新目标回落", async () => {
    testMutateAsync.mockResolvedValue({
      ok: true,
      checks: [],
      context_window: 9999,
      context_source: "detected",
    });
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "测试连接" }));
    await screen.findByText(/9,999/);

    const listbox = await openPicker(user);
    await user.click(
      within(listbox).getByRole("option", { name: /DeepSeek Pro/ }),
    );

    const shown = screen.getByTestId("bot-context-window");
    expect(shown).not.toHaveTextContent("9,999");
    expect(shown).toHaveTextContent("262,144");
  });

  it("测试后改模型：逐项检查结果随旧目标一起隐藏", async () => {
    testMutateAsync.mockResolvedValue({
      ok: true,
      checks: [{ name: "streaming", passed: true }],
    });
    const user = userEvent.setup();
    renderDialog();

    await user.click(screen.getByRole("button", { name: "测试连接" }));
    expect(
      await screen.findByTestId("bot-connection-checks"),
    ).toBeInTheDocument();

    const listbox = await openPicker(user);
    await user.click(
      within(listbox).getByRole("option", { name: /DeepSeek Pro/ }),
    );
    expect(screen.queryByTestId("bot-connection-checks")).toBeNull();
  });
});

// INT-133：默认人设由服务端系统自动注入，新建机器人不再预填人设模板；
// 人设是「追加、可覆盖默认语气」的补充，编辑回填已存值，清空即保存为空。
describe("BotFormDialog 人设", () => {
  it("新建模式：人设留空，只以占位文案说明默认说话方式", () => {
    renderDialog(null);

    const textarea = screen.getByLabelText("人设提示词");
    expect(textarea).toHaveValue("");
    expect(textarea.getAttribute("placeholder")).toContain("默认说话方式");
  });

  it("编辑模式：回填已存人设", () => {
    renderDialog({ ...botFixture, persona_prompt: "已存人设，语气冷静。" });

    expect(screen.getByLabelText("人设提示词")).toHaveValue("已存人设，语气冷静。");
  });

  it("编辑模式已存人设为空：保持空", () => {
    renderDialog({ ...botFixture, persona_prompt: "" });

    expect(screen.getByLabelText("人设提示词")).toHaveValue("");
  });

  it("新建模式人设留空保存：提交空 persona_prompt", async () => {
    const user = userEvent.setup();
    renderDialog(null);

    expect(screen.getByLabelText("人设提示词")).toHaveValue("");
    await user.type(screen.getByLabelText("名称"), "发布助手");
    await user.click(screen.getByRole("button", { name: "保存" }));

    expect(createMutateAsync).toHaveBeenCalledWith({
      name: "发布助手",
      persona_prompt: "",
      provider_id: "ap-1",
      model: "deepseek-flash",
    });
  });
});
