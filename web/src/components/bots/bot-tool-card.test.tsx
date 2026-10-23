import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
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

function renderCard(
  call?: BotCall,
  onCancel?: (callId: string) => void,
) {
  return render(
    <MemoryRouter>
      <BotToolCard botId="b1" call={call} onCancel={onCancel} />
    </MemoryRouter>,
  );
}

describe("BotToolCard", () => {
  it("标题是中文标签，状态徽标在右侧；不展示参数 chips、原始调用与副作用徽标", () => {
    renderCard(
      makeCall({
        arguments: {
          issue_id: "abcdef1234567890",
          project_id: "p1",
          settled: false,
        },
      }),
    );
    expect(screen.getByText("查看问题")).toBeInTheDocument();
    const status = screen.getByTestId("bot-call-status");
    expect(status).toHaveTextContent("已完成");
    // 状态徽标贴卡片右侧。
    expect(status).toHaveClass("ml-auto");
    // 参数摘要 chips（短标识、「已收合」等）、副作用徽标与原始调用入口已隐藏。
    expect(screen.queryByText(/abcdef12/)).toBeNull();
    expect(screen.queryByText("已收合")).toBeNull();
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.queryByText("只读")).toBeNull();
    expect(
      screen.queryByRole("button", { name: /原始调用/ }),
    ).toBeNull();
    expect(screen.queryByText(/fs_issue\.get/)).toBeNull();
  });

  it("写操作同样不显示「写入」徽标与参数，状态仍在右", () => {
    renderCard(
      makeCall({
        action: "create",
        status: "running",
        arguments: { project_id: "p1", title: "t" },
      }),
    );
    expect(screen.getByText("创建问题")).toBeInTheDocument();
    expect(screen.queryByText("写入")).toBeNull();
    expect(screen.queryByText("项目")).toBeNull();
    const status = screen.getByTestId("bot-call-status");
    expect(status).toHaveTextContent("执行中");
    expect(status).toHaveClass("ml-auto");
  });

  it("pending_confirmation 状态文案为「等待你确认」，取消按钮在状态徽标之后", () => {
    const onCancel = vi.fn();
    renderCard(
      makeCall({ status: "pending_confirmation", requires_confirmation: true }),
      onCancel,
    );
    const status = screen.getByTestId("bot-call-status");
    expect(status).toHaveTextContent("等待你确认");
    const cancel = screen.getByRole("button", { name: "取消" });
    // 取消在状态徽标之后（同在卡片右侧）。
    expect(
      status.compareDocumentPosition(cancel) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    fireEvent.click(cancel);
    expect(onCancel).toHaveBeenCalledWith("call-1");
  });

  it("错误信息与副作用警示保留", () => {
    renderCard(
      makeCall({
        status: "failed",
        error_code: "model_error",
        error_message: "上游 502",
        effect_may_have_occurred: true,
      }),
    );
    // 错误码收进 title 提示，正文只显示消息本身（视觉审查：双码与原始
    // 码不外露）。
    const errText = screen.getByText("上游 502");
    expect(errText).toBeInTheDocument();
    expect(errText).toHaveAttribute("title", "model_error");
    expect(screen.getByText(/副作用可能已经发生/)).toBeInTheDocument();
    expect(screen.getByTestId("bot-call-status")).toHaveTextContent("失败");
  });

  it("错误文案里的参数字段名用等宽样式与普通文案区分（视觉审查）", () => {
    renderCard(
      makeCall({
        status: "failed",
        error_message:
          "workspace.type=root 不接受 base_ref/branch/worktree_path：项目主检出不需要工作区参数；要新建工作树请改用 worktree",
      }),
    );
    const errP = document.querySelector("p.text-destructive")!;
    // 可辨识的字段名（snake_case/点路径/赋值）各成 <code>，中文说明与
    // 单词枚举值保持普通文案。
    const codes = [...errP.querySelectorAll("code")].map((c) => c.textContent);
    expect(codes).toEqual([
      "workspace.type=root",
      "base_ref",
      "worktree_path",
    ]);
    expect(errP).toHaveTextContent("不接受 base_ref/branch/worktree_path");
    expect(errP).toHaveTextContent("要新建工作树请改用 worktree");
  });

  it("截断结果保留「查看完整结果」按需展开", () => {
    renderCard(makeCall({ truncated: true }));
    expect(
      screen.getByRole("button", { name: /查看完整结果/ }),
    ).toBeInTheDocument();
  });

  it("不再渲染原始标识与完整参数", () => {
    renderCard(makeCall({ arguments: { issue_id: "i1", page: 2 } }));
    expect(screen.queryByText(/"issue_id"/)).toBeNull();
    expect(screen.queryByText(/"page"/)).toBeNull();
  });

  it("未知工具回退显示原始 tool.action，不抛错", () => {
    renderCard(
      makeCall({ tool_name: "acme_thing", action: "do", status: "queued" }),
    );
    expect(screen.getByText("acme_thing.do")).toBeInTheDocument();
    expect(screen.getByTestId("bot-call-status")).toHaveTextContent("已排队");
    // 未知 effect 的保守「写入」徽标也不再展示。
    expect(screen.queryByText("写入")).toBeNull();
  });

});
