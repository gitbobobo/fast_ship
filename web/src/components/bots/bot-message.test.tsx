import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { BotMessageView } from "./bot-message";
import type { BotCall, BotMessage } from "@/lib/api/bots";

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

function renderMessage(
  message: BotMessage,
  { calls = [], draft = false }: { calls?: BotCall[]; draft?: boolean } = {},
) {
  return render(
    <BotMessageView
      botId="b1"
      message={message}
      draft={draft}
      calls={calls}
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

// INT-122：带 tool_calls 的 assistant 是中间回合，正文只是调用前的
// 过程旁白——折叠成一行「过程」入口默认收起，把视觉重心留给最终回复；
// 工具卡/确认卡是操作入口，必须在折叠之外始终可见。

const interimMessage: BotMessage = {
  id: "m-a1",
  bot_id: "b1",
  seq: 10,
  kind: "assistant",
  content: {
    text: "我先看一下有哪些项目。",
    tool_calls: [
      {
        call_id: "c-read",
        provider_tool_call_id: "ptc-1",
        tool_name: "fs_project",
        action: "list",
        arguments: "{}",
      },
    ],
  },
  created_at: "2026-10-18T00:00:00Z",
};

// 待确认的写操作：arguments 选了不产生站内链接的字段，测试无需包路由。
const pendingCall: BotCall = {
  id: "c-write",
  bot_id: "b1",
  tool_name: "fs_project",
  action: "create",
  arguments: { name: "演示项目" },
  execution_mode: "sync",
  requires_confirmation: true,
  status: "pending_confirmation",
  created_at: "2026-10-18T00:00:01Z",
};

describe("BotMessageView 中间回合折叠", () => {
  it("带 tool_calls 的旁白默认收起为「过程」入口，工具卡在折叠外可见", () => {
    const { container } = renderMessage(interimMessage);

    const toggle = screen.getByRole("button", { name: "过程" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(
      screen.queryByText(/我先看一下有哪些项目/),
    ).not.toBeInTheDocument();
    // 旁白不再以回复气泡呈现。
    expect(container.querySelector(".bg-muted.rounded-2xl")).toBeNull();
    // 工具卡不属于折叠内容，收起状态下依然可见。
    expect(screen.getByTestId("bot-tool-card")).toBeInTheDocument();
  });

  it("点击「过程」展开正文，再点收起", async () => {
    const user = userEvent.setup();
    renderMessage(interimMessage);

    await user.click(screen.getByRole("button", { name: "过程" }));
    expect(screen.getByText(/我先看一下有哪些项目/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "过程" }));
    expect(
      screen.queryByText(/我先看一下有哪些项目/),
    ).not.toBeInTheDocument();
  });

  it("待确认调用渲染确认卡：用户唯一的批准入口不受折叠影响", () => {
    renderMessage(
      {
        ...interimMessage,
        content: {
          ...interimMessage.content,
          tool_calls: [
            {
              call_id: pendingCall.id,
              provider_tool_call_id: "ptc-2",
              tool_name: pendingCall.tool_name,
              action: pendingCall.action,
              arguments: "{}",
            },
          ],
        },
      },
      { calls: [pendingCall] },
    );

    expect(
      screen.queryByText(/我先看一下有哪些项目/),
    ).not.toBeInTheDocument();
    const card = screen.getByTestId("bot-confirmation-card");
    expect(
      within(card).getByRole("button", { name: "批准" }),
    ).toBeInTheDocument();
    expect(screen.queryByTestId("bot-tool-card")).not.toBeInTheDocument();
  });

  it("无 tool_calls 的最终回复：正文气泡直出，没有「过程」入口", () => {
    const { container } = renderMessage({
      id: "m-final",
      bot_id: "b1",
      seq: 11,
      kind: "assistant",
      content: { text: "这是最终答复。" },
      created_at: "2026-10-18T00:00:02Z",
    });

    expect(screen.getByText(/这是最终答复/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "过程" }),
    ).not.toBeInTheDocument();
    expect(container.querySelector(".bg-muted.rounded-2xl")).not.toBeNull();
  });

  it("正文为空的中间消息没有「过程」入口，工具卡仍在", () => {
    renderMessage({
      ...interimMessage,
      content: { ...interimMessage.content, text: "" },
    });

    expect(
      screen.queryByRole("button", { name: "过程" }),
    ).not.toBeInTheDocument();
    expect(screen.getByTestId("bot-tool-card")).toBeInTheDocument();
  });

  it("流式草稿带 tool_calls 不折叠：正文气泡直出", () => {
    const { container } = renderMessage(interimMessage, { draft: true });

    expect(screen.getByText(/我先看一下有哪些项目/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "过程" }),
    ).not.toBeInTheDocument();
    expect(container.querySelector(".bg-muted.rounded-2xl")).not.toBeNull();
  });
});
