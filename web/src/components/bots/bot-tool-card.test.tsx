import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { BotCall } from "@/lib/api/bots";
import { BotToolCard } from "./bot-tool-card";

function makeCall(overrides: Partial<BotCall> = {}): BotCall {
  return {
    id: "call-1",
    bot_id: "b1",
    tool_name: "fs_issue",
    action: "get",
    arguments: {},
    execution_mode: "sync",
    requires_confirmation: false,
    status: "succeeded",
    created_at: "2026-10-17T01:00:00Z",
    ...overrides,
  };
}

function renderCard(call?: BotCall, fallback?: { name: string; action: string }) {
  return render(
    <MemoryRouter>
      <BotToolCard
        botId="b1"
        call={call}
        fallbackName={fallback?.name}
        fallbackAction={fallback?.action}
      />
    </MemoryRouter>,
  );
}

describe("BotToolCard", () => {
  it("主标题是中文标签，带只读徽标与中文状态；原始标识默认折叠", () => {
    renderCard(makeCall());
    expect(screen.getByText("查看问题")).toBeInTheDocument();
    expect(screen.getByText("只读")).toBeInTheDocument();
    expect(screen.getByTestId("bot-call-status")).toHaveTextContent("已完成");
    expect(screen.queryByText(/fs_issue\.get/)).toBeNull();
  });

  it("写操作显示写入徽标", () => {
    renderCard(
      makeCall({ action: "create", status: "running", arguments: { project_id: "p1", title: "t" } }),
    );
    expect(screen.getByText("创建问题")).toBeInTheDocument();
    expect(screen.getByText("写入")).toBeInTheDocument();
    expect(screen.getByTestId("bot-call-status")).toHaveTextContent("执行中");
  });

  it("pending_confirmation 状态文案为「等待你确认」", () => {
    renderCard(makeCall({ status: "pending_confirmation", requires_confirmation: true }));
    expect(screen.getByTestId("bot-call-status")).toHaveTextContent("等待你确认");
  });

  it("「查看原始调用」展开后显示原始标识与完整参数", () => {
    renderCard(
      makeCall({
        arguments: { issue_id: "i1", page: 2 },
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /查看原始调用/ }));
    expect(screen.getByText(/fs_issue\.get/)).toBeInTheDocument();
    expect(screen.getByText(/"issue_id": "i1"/)).toBeInTheDocument();
    expect(screen.getByText(/"page": 2/)).toBeInTheDocument();
  });

  it("issue_id 参数渲染为可点击的短标识并链接到详情页", () => {
    renderCard(
      makeCall({
        arguments: { issue_id: "abcdef1234567890", project_id: "p1" },
      }),
    );
    const link = screen.getByRole("link", { name: /abcdef12…/ });
    expect(link).toHaveAttribute("href", "/projects/p1/issues/abcdef1234567890");
    expect(link).toHaveAttribute("title", "abcdef1234567890");
  });

  it("未知工具回退显示原始 tool.action，不抛错", () => {
    renderCard(
      makeCall({ tool_name: "acme_thing", action: "do", status: "queued" }),
    );
    expect(screen.getByText("acme_thing.do")).toBeInTheDocument();
    expect(screen.getByTestId("bot-call-status")).toHaveTextContent("已排队");
    // 未知 effect 保守按写入处理
    expect(screen.getByText("写入")).toBeInTheDocument();
  });

  it("账本缺卡时用 fallback 标识渲染静态形态", () => {
    renderCard(undefined, { name: "fs_thread", action: "send" });
    expect(screen.getByText("向线程发送消息")).toBeInTheDocument();
    expect(screen.queryByTestId("bot-call-status")).toBeNull();
  });
});
