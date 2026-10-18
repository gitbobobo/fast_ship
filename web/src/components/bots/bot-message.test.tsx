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

// compaction_failed lifecycle（INT-115）：服务端 detail 带 code/note 等字段，
// 渲染必须给出明确中文提示（含继续对话的指引），不能只显示英文事件名。

const compactionFailed: BotMessage = {
  id: "m-cf",
  bot_id: "b1",
  seq: 40,
  kind: "lifecycle",
  content: {
    event: "compaction_failed",
    detail: {
      code: "target_unreachable",
      message: "压缩目标不可达：固定前缀+最小摘要预算 ≥ 触发阈值",
      note: "压缩目标不可达：固定前缀+最小摘要预算 ≥ 触发阈值",
      generation: 0,
      through_seq: 12,
    },
  },
  created_at: "2026-10-18T02:00:00Z",
};

describe("BotMessageView compaction_failed", () => {
  it("显示中文失败原因与继续对话指引，不显示英文事件名", () => {
    renderMessage(compactionFailed);

    const note = screen.getByTestId("bot-compaction-failed-note");
    expect(note).toHaveTextContent("上下文压缩失败");
    expect(note).toHaveTextContent("压缩目标不可达：固定前缀+最小摘要预算 ≥ 触发阈值");
    expect(note).toHaveTextContent("继续发新消息");
    expect(note).not.toHaveTextContent("compaction_failed");
  });

  it("缺 note 时回退到 message，再缺则用兜底文案", () => {
    const { unmount } = renderMessage({
      ...compactionFailed,
      content: {
        event: "compaction_failed",
        detail: { code: "context_uncompressible", message: "上下文压缩后仍超出模型窗口" },
      },
    });
    expect(screen.getByTestId("bot-compaction-failed-note")).toHaveTextContent(
      "上下文压缩后仍超出模型窗口",
    );
    unmount();

    renderMessage({
      ...compactionFailed,
      content: { event: "compaction_failed", detail: {} },
    });
    expect(screen.getByTestId("bot-compaction-failed-note")).toHaveTextContent(
      "上下文压缩未成功",
    );
  });

  it("其它 lifecycle 事件仍按 note/event 渲染，不受影响", () => {
    renderMessage({
      id: "m-lc",
      bot_id: "b1",
      seq: 41,
      kind: "lifecycle",
      content: {
        event: "toolset_updated",
        detail: { note: "工具集已升级" },
      },
      created_at: "2026-10-18T02:00:00Z",
    });
    expect(screen.getByText("工具集已升级")).toBeInTheDocument();
  });
});
