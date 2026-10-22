import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { BotChat } from "./bot-chat";
import { renderWithRoute } from "@/test/render";
import type { BotRun } from "@/lib/api/bots";
import type { UseBotChatResult } from "@/lib/hooks/use-bot-chat";

// 组件测试只关心视图行为：useBotChat 整钩替换，hook 侧逻辑由
// use-bot-chat.test.tsx 覆盖。
const chat = vi.hoisted(() => ({
  stopRun: vi.fn(),
  sendMessage: vi.fn(),
  decide: vi.fn(),
  cancelCall: vi.fn(),
  loadOlder: vi.fn(),
  retry: vi.fn(),
  resumeStream: vi.fn(),
  value: null as unknown as UseBotChatResult,
}));

vi.mock("@/lib/hooks/use-bot-chat", () => ({
  useBotChat: () => chat.value,
}));

const runningRun: BotRun = {
  id: "r1",
  bot_id: "b1",
  trigger_source: "user_message",
  status: "running",
  stop_requested: false,
  created_at: "2026-10-15T00:00:30Z",
};

function chatState(overrides: Partial<UseBotChatResult> = {}): UseBotChatResult {
  return {
    loading: false,
    loadError: null,
    entries: [],
    metrics: undefined,
    maintenanceProgress: null,
    calls: [],
    activeRun: null,
    runActive: false,
    stopRequested: false,
    runError: null,
    backgroundCalls: [],
    streamStatus: "open",
    streamError: null,
    sending: false,
    sendError: null,
    stopping: false,
    stopError: null,
    busyCallId: null,
    callActionError: null,
    loadingOlder: false,
    olderError: null,
    hasMoreHistory: false,
    resumeStream: chat.resumeStream,
    retry: chat.retry,
    sendMessage: chat.sendMessage,
    stopRun: chat.stopRun,
    decide: chat.decide,
    cancelCall: chat.cancelCall,
    loadOlder: chat.loadOlder,
    ...overrides,
  } as UseBotChatResult;
}

beforeEach(() => {
  vi.clearAllMocks();
  chat.value = chatState();
});

describe("BotChat 错误条（INT-137）", () => {
  it("auth 失败展示中文说明与「去设置」链接", () => {
    chat.value = chatState({
      runError: {
        code: "provider_auth_failed",
        message:
          "AI 提供商认证失败：密钥无效或已失效，请到设置页更新密钥",
        action: { kind: "ai_providers_settings", label: "去设置" },
      },
    });
    renderWithRoute(<BotChat botId="b1" />);

    const bar = screen.getByTestId("bot-run-error");
    expect(bar).toHaveTextContent(
      "AI 提供商认证失败：密钥无效或已失效，请到设置页更新密钥",
    );
    expect(
      within(bar).getByRole("link", { name: "去设置" }),
    ).toHaveAttribute("href", "/settings/ai-providers");
  });

  it("runError 为 null 时不渲染错误条", () => {
    renderWithRoute(<BotChat botId="b1" />);
    expect(screen.queryByTestId("bot-run-error")).not.toBeInTheDocument();
  });
});

describe("BotChat 停止按钮（INT-137 强制停止）", () => {
  it("首次停止文案为「请求停止」，说明是追加停止消息", () => {
    chat.value = chatState({
      runActive: true,
      activeRun: runningRun,
    });
    renderWithRoute(<BotChat botId="b1" />);

    const button = screen.getByRole("button", { name: "请求停止" });
    expect(button).toBeEnabled();
    expect(button).toHaveAttribute(
      "title",
      "向机器人发送一条「停止」消息，由它结合上下文决定是否结束运行",
    );
  });

  it("已请求停止后按钮仍可点击，变为「强制停止」", async () => {
    chat.value = chatState({
      runActive: true,
      stopRequested: true,
      activeRun: { ...runningRun, stop_requested: true },
    });
    const user = userEvent.setup();
    renderWithRoute(<BotChat botId="b1" />);

    const button = screen.getByRole("button", { name: "强制停止" });
    expect(button).toBeEnabled();
    expect(button).toHaveAttribute("title", "立即中断当前模型请求");

    await user.click(button);
    expect(chat.stopRun).toHaveBeenCalledTimes(1);
  });

  it("停止请求在飞时按钮禁用，避免重复提交", () => {
    chat.value = chatState({
      runActive: true,
      stopping: true,
      activeRun: runningRun,
    });
    renderWithRoute(<BotChat botId="b1" />);

    expect(screen.getByRole("button", { name: "请求停止" })).toBeDisabled();
  });
});
