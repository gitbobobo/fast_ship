import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BotMessageView } from "./bot-message";
import type { BotMessage } from "@/lib/api/bots";

// schedule_event 是定时任务运行事件的平台投影：必须渲染成安静的事件行，
// 不能落进 assistant 分支变成空块或机器人气泡。

const completedEvent: BotMessage = {
  id: "m-se",
  bot_id: "b1",
  seq: 30,
  kind: "schedule_event",
  content: {
    task_id: "t1",
    task_name: "每日汇总",
    run_id: "r-s1",
    status: "completed",
    summary: "已汇总 3 条变化",
  },
  created_at: "2026-10-17T01:02:10Z",
};

function renderMessage(message: BotMessage) {
  return render(
    <BotMessageView
      botId="b1"
      message={message}
      calls={[]}
      busyCallId={null}
      onDecide={() => {}}
      onCancelCall={() => {}}
    />,
  );
}

describe("BotMessageView schedule_event", () => {
  it("渲染为安静的事件行：任务名 + 状态 + 摘要，不是 assistant 气泡", () => {
    const { container } = renderMessage(completedEvent);

    const line = screen.getByText(/定时任务「每日汇总」已完成/);
    expect(line).toHaveTextContent("已汇总 3 条变化");
    // 不落 assistant 分支：没有气泡容器，也没有空占位块。
    expect(container.querySelector(".bg-muted.rounded-2xl")).toBeNull();
    expect(container.querySelector(".space-y-2")).toBeNull();
  });

  it("失败事件带错误信息并用 destructive 着色", () => {
    renderMessage({
      ...completedEvent,
      content: {
        ...completedEvent.content,
        status: "failed",
        summary: "",
        error_code: "model_request_failed",
        error_message: "上游 502",
      },
    });

    const line = screen.getByText(/定时任务「每日汇总」已失败/);
    expect(line).toHaveClass("text-destructive");
    expect(line).toHaveTextContent("model_request_failed: 上游 502");
  });
});
