import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { BotFormDialog } from "@/components/bots/bot-form-dialog";
import type { Bot } from "@/lib/api/bots";
import {
  useCreateBot,
  useTestBotConnection,
  useUpdateBot,
} from "@/lib/hooks/use-bots";
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

const botFixture: Bot = {
  id: "bot-1",
  name: "发布助手",
  persona_prompt: "",
  base_url: "https://api.example.com/v1/",
  model: "deepseek-flash",
  api_key_last4: "ab12",
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

// F8：弹窗的能力展示跟随当前表单目标——目标与已存值一致显示已存能力，
// 测试连接成功后显示本次测试返回的能力，目标变了即隐藏旧展示。
describe("BotFormDialog 能力展示", () => {
  const testMutateAsync = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useCreateBot).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useCreateBot>);
    vi.mocked(useUpdateBot).mockReturnValue({
      mutateAsync: vi.fn(),
      isPending: false,
    } as unknown as ReturnType<typeof useUpdateBot>);
    vi.mocked(useTestBotConnection).mockReturnValue({
      mutateAsync: testMutateAsync,
      isPending: false,
      isError: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useTestBotConnection>);
  });

  it("编辑打开且目标未变：展示已存能力（含来源与口径）", () => {
    renderDialog();

    const shown = screen.getByTestId("bot-context-window");
    expect(shown).toHaveTextContent("上下文窗口 1,048,576 tokens");
    expect(shown).toHaveTextContent("自动检测");
    expect(shown).toHaveTextContent("GET /models");
  });

  it("目标变了：不再展示与当前目标不符的旧能力", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.clear(screen.getByLabelText("模型"));
    await user.type(screen.getByLabelText("模型"), "another-model");

    expect(screen.queryByTestId("bot-context-window")).not.toBeInTheDocument();
  });

  it("地址变了：同样隐藏旧能力", async () => {
    const user = userEvent.setup();
    renderDialog();

    await user.clear(screen.getByLabelText("API 地址"));
    await user.type(
      screen.getByLabelText("API 地址"),
      "https://other.example.com/v1",
    );

    expect(screen.queryByTestId("bot-context-window")).not.toBeInTheDocument();
  });

  it("目标变化后测试连接成功：展示本次测试返回的能力", async () => {
    testMutateAsync.mockResolvedValue({
      ok: true,
      checks: [{ name: "streaming", passed: true }],
      context_window: 131072,
      max_output_tokens: 8192,
      context_source: "detected",
      context_detail: "GET /models",
    });
    const user = userEvent.setup();
    renderDialog();

    await user.clear(screen.getByLabelText("模型"));
    await user.type(screen.getByLabelText("模型"), "another-model");
    await user.click(screen.getByRole("button", { name: "测试连接" }));

    const shown = await screen.findByTestId("bot-context-window");
    expect(shown).toHaveTextContent("上下文窗口 131,072 tokens");
    // 旧目标的能力不得再出现。
    expect(shown).not.toHaveTextContent("1,048,576");
  });

  it("测试后目标又变：测试结果也隐藏", async () => {
    testMutateAsync.mockResolvedValue({
      ok: true,
      checks: [],
      context_window: 131072,
      context_source: "detected",
    });
    const user = userEvent.setup();
    renderDialog();

    await user.clear(screen.getByLabelText("模型"));
    await user.type(screen.getByLabelText("模型"), "another-model");
    await user.click(screen.getByRole("button", { name: "测试连接" }));
    await screen.findByTestId("bot-context-window");

    await user.type(screen.getByLabelText("模型"), "-v2");

    expect(screen.queryByTestId("bot-context-window")).not.toBeInTheDocument();
  });

  it("地址等价写法（尾部斜杠）不触发隐藏", async () => {
    const user = userEvent.setup();
    renderDialog();

    // 已存值本身带尾部斜杠；去掉它仍是同一目标。
    await user.clear(screen.getByLabelText("API 地址"));
    await user.type(
      screen.getByLabelText("API 地址"),
      "https://api.example.com/v1",
    );

    expect(screen.getByTestId("bot-context-window")).toHaveTextContent(
      "1,048,576",
    );
  });
});
