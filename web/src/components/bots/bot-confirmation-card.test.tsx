import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { BotCall } from "@/lib/api/bots";
import { BotConfirmationCard } from "./bot-confirmation-card";

// 普通工具卡已隐藏参数摘要：确认卡是写操作确认的唯一依据，
// 参数 chips、完整 JSON 与决定按钮必须始终可见。

function makeCall(overrides: Partial<BotCall> = {}): BotCall {
  return {
    id: "call-1",
    bot_id: "b1",
    tool_name: "fs_issue",
    action: "update",
    arguments: {
      project_id: "p1",
      issue_id: "abcdef1234567890",
      body: "新正文",
    },
    execution_mode: "async",
    requires_confirmation: true,
    status: "pending_confirmation",
    created_at: "2026-10-17T01:00:00Z",
    ...overrides,
  };
}

function renderCard(call: BotCall) {
  return render(
    <MemoryRouter>
      <BotConfirmationCard call={call} busy={false} onDecide={vi.fn()} />
    </MemoryRouter>,
  );
}

describe("BotConfirmationCard", () => {
  it("操作范围与完整参数始终可见：chips + 原始标识 + 完整 JSON", () => {
    renderCard(makeCall());
    const card = screen.getByTestId("bot-confirmation-card");

    // 中文动作标题与原始 tool.action。
    expect(within(card).getByText(/需要你的确认 · 更新问题/)).toBeInTheDocument();
    expect(within(card).getByText("fs_issue.update")).toBeInTheDocument();

    // 参数摘要 chips：项目/问题标签与短标识链接都在。
    expect(within(card).getByText("项目")).toBeInTheDocument();
    expect(within(card).getByText("问题")).toBeInTheDocument();
    const link = within(card).getByRole("link", { name: /abcdef12…/ });
    expect(link).toHaveAttribute(
      "href",
      "/projects/p1/issues/abcdef1234567890",
    );

    // 完整参数 JSON 默认展开，不折叠进任何「查看」入口。
    expect(card).toHaveTextContent('"issue_id": "abcdef1234567890"');
    expect(card).toHaveTextContent('"body": "新正文"');
    expect(card).toHaveTextContent('"project_id": "p1"');
  });

  it("保留三个决定入口与授权说明", () => {
    renderCard(makeCall());
    const card = screen.getByTestId("bot-confirmation-card");
    expect(within(card).getByRole("button", { name: "批准" })).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "拒绝" })).toBeInTheDocument();
    expect(
      within(card).getByRole("button", { name: "始终允许" }),
    ).toBeInTheDocument();
    expect(within(card).getByText(/可在授权管理中撤销/)).toBeInTheDocument();
  });
});
