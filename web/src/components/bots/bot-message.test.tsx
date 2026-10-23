import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { BotMessageView } from "./bot-message";
import type { BotCall, BotMessage } from "@/lib/api/bots";

// schedule_event（INT-157）：一行居中的事件行，汇报/错误不再内联展开，
// 收进点击打开的弹窗（markdown 渲染）；不能落进 assistant 分支。

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
  it("渲染为一行居中的事件行：任务名 + 状态，摘要不在行内", () => {
    const { container } = renderMessage(completedEvent);

    const line = screen.getByText(/定时任务「/);
    expect(line).toHaveTextContent("定时任务「每日汇总」已完成");
    expect(line).not.toHaveTextContent("已汇总 3 条变化");
    // 不落 assistant 分支：没有气泡容器，也没有空占位块。
    expect(container.querySelector(".bg-muted.rounded-2xl")).toBeNull();
    expect(container.querySelector(".space-y-2")).toBeNull();
  });

  it("有摘要的事件行可点，弹窗里用 markdown 展示详情", async () => {
    const user = userEvent.setup();
    renderMessage(completedEvent);

    await user.click(screen.getByTestId("bot-schedule-event-detail"));
    expect(screen.getByText("已汇总 3 条变化")).toBeInTheDocument();
  });

  it("失败事件用 destructive 着色，错误信息在弹窗里不在行内", async () => {
    const user = userEvent.setup();
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

    const line = screen.getByText(/定时任务「/);
    expect(line).toHaveTextContent("定时任务「每日汇总」已失败");
    expect(line.closest("p")).toHaveClass("text-destructive");
    expect(line).not.toHaveTextContent("上游 502");

    await user.click(screen.getByTestId("bot-schedule-event-detail"));
    expect(
      screen.getByText(/model_request_failed: 上游 502/),
    ).toBeInTheDocument();
  });

  it("无摘要也无错误的事件行不可点", () => {
    renderMessage({
      ...completedEvent,
      content: { ...completedEvent.content, summary: "" },
    });

    expect(screen.getByText(/定时任务「/)).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  // 任务名是模型可写数据（建/改任务只 trim+限长）：
  // 双向覆盖符（U+202E）能把事件行重排成相反意思，渲染前必须剥掉。
  it("任务名里的双向覆盖与控制字符被剥离", () => {
    renderMessage({
      ...completedEvent,
      content: {
        ...completedEvent.content,
        task_name: "每日\u202E置倒",
        summary: "",
      },
    });

    const line = screen.getByText(/定时任务「/);
    expect(line).toHaveTextContent("每日置倒");
    expect(line.textContent ?? "").not.toMatch(/[\u202B-\u202E]/);
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

  it("lifecycle 优先渲染 user_note，旧消息剥掉内部信息前缀", () => {
    renderMessage({
      id: "m-un",
      bot_id: "b1",
      seq: 42,
      kind: "lifecycle",
      content: {
        event: "toolset_updated",
        detail: {
          note: "（内部信息：不需要向用户转述，也不需要逐项验证）工具集已升级（下一个压缩点生效）",
          user_note: "工具集已升级（下次上下文整理时生效）",
        },
      },
      created_at: "2026-10-18T02:01:00Z",
    });
    expect(screen.getByText("工具集已升级（下次上下文整理时生效）")).toBeInTheDocument();
    expect(screen.queryByText(/内部信息/)).toBeNull();

    renderMessage({
      id: "m-old",
      bot_id: "b1",
      seq: 43,
      kind: "lifecycle",
      content: {
        event: "toolset_updated",
        detail: { note: "（内部信息：不需要向用户转述）工具集已升级" },
      },
      created_at: "2026-10-18T02:02:00Z",
    });
    expect(screen.getByText("工具集已升级")).toBeInTheDocument();
    expect(screen.queryByText(/内部信息/)).toBeNull();
  });
});

// INT-157：带 tool_calls 的 assistant 中间回合整体隐藏——正文只是
// 调用前的过程旁白，工具卡是执行噪音；唯一保留的是待确认调用的
// 确认卡（用户唯一的批准入口）。流式草稿照旧直出气泡。

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

describe("BotMessageView 中间回合隐藏", () => {
  it("带 tool_calls 的中间回合：旁白与工具卡都不渲染", () => {
    const { container } = renderMessage(interimMessage);

    expect(
      screen.queryByText(/我先看一下有哪些项目/),
    ).not.toBeInTheDocument();
    expect(container.querySelector(".bg-muted.rounded-2xl")).toBeNull();
    expect(container.innerHTML).toBe("");
  });

  it("待确认调用仍渲染确认卡：用户唯一的批准入口", () => {
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

  it("无 tool_calls 的最终回复：正文气泡直出", () => {
    const { container } = renderMessage({
      id: "m-final",
      bot_id: "b1",
      seq: 11,
      kind: "assistant",
      content: { text: "这是最终答复。" },
      created_at: "2026-10-18T00:00:02Z",
    });

    expect(screen.getByText(/这是最终答复/)).toBeInTheDocument();
    expect(container.querySelector(".bg-muted.rounded-2xl")).not.toBeNull();
  });

  it("流式草稿带 tool_calls 不隐藏：正文气泡直出", () => {
    const { container } = renderMessage(interimMessage, { draft: true });

    expect(screen.getByText(/我先看一下有哪些项目/)).toBeInTheDocument();
    expect(container.querySelector(".bg-muted.rounded-2xl")).not.toBeNull();
  });
});

// background_result（INT-136 审查）：服务端把工具结果 JSON 截到 200 字符
// 当 summary，直接当正文会断在字符串中间、看不出成败。fs_schedule 用
// execution_context.schedule 拼人话主行，原始内容收进默认折叠区；
// 找不到调用时保持兜底，但 JSON 形状的 summary 不当正文。

const scheduleCreateCall: BotCall = {
  id: "c-sched",
  bot_id: "b1",
  tool_name: "fs_schedule",
  action: "create",
  arguments: { name: "每日站会提醒", cron: "0 9 * * *", timezone: "Asia/Shanghai" },
  execution_mode: "async",
  requires_confirmation: true,
  status: "succeeded",
  execution_context: {
    schedule: {
      task_id: "task-0001",
      name: "每日站会提醒",
      cron: "0 9 * * *",
      timezone: "Asia/Shanghai",
      instruction: "提醒大家开会。",
      needs_report: true,
      next_run_at: "2026-10-21 09:00:00 CST",
    },
  },
  created_at: "2026-10-18T00:00:03Z",
};

// 服务端 botResultSummary 截断后的样子：JSON 断在字符串中间。
const truncatedJsonSummary =
  '{"cron_expression":"0 9 * * *","timezone":"Asia/Shanghai","needs_report":true,"name":"每日';

const backgroundResult: BotMessage = {
  id: "m-bg",
  bot_id: "b1",
  seq: 50,
  kind: "background_result",
  content: {
    call_id: "c-sched",
    tool_name: "fs_schedule",
    action: "create",
    status: "succeeded",
    summary: truncatedJsonSummary,
  },
  created_at: "2026-10-18T00:05:00Z",
};

describe("BotMessageView background_result", () => {
  it("fs_schedule 成功：主行是中文人话，不暴露原始字段名；summary 折叠在原始结果里", () => {
    const { container } = renderMessage(backgroundResult, {
      calls: [scheduleCreateCall],
    });

    const line = screen.getByText(/已创建定时任务/);
    expect(line).toHaveTextContent("已创建定时任务「每日站会提醒」");
    expect(line).toHaveTextContent("0 9 * * *");
    expect(line).toHaveTextContent("Asia/Shanghai");
    expect(line.textContent ?? "").not.toContain("cron_expression");
    // 成功状态可辨识：绿色对勾。
    expect(container.querySelector("svg.text-emerald-600")).not.toBeNull();

    const raw = screen.getByTestId("bot-background-raw-result");
    expect(raw).not.toHaveAttribute("open");
    expect(within(raw).getByText("查看原始结果")).toBeInTheDocument();
    expect(raw).toHaveTextContent('{"cron_expression"');
    expect(raw).toHaveTextContent("每日");
  });

  it("fs_schedule 失败：主行写「未成功」并用 destructive 着色，错误文案直接外露", () => {
    renderMessage(
      {
        ...backgroundResult,
        content: {
          ...backgroundResult.content,
          status: "failed",
          error_code: "schedule_run_failed",
          error_message: "上游模型请求失败",
        },
      },
      { calls: [scheduleCreateCall] },
    );

    const line = screen.getByText(/定时任务/);
    expect(line).toHaveTextContent("创建定时任务「每日站会提醒」未成功");
    expect(line).toHaveClass("text-destructive");
    expect(screen.queryByText(/已创建定时任务/)).not.toBeInTheDocument();
    // 错误文案短、可行动（坏 cron/时区等），不藏进折叠区。
    expect(
      screen.getByText("schedule_run_failed: 上游模型请求失败"),
    ).toBeVisible();
    // 原始 summary（截断的 JSON 片段）仍在折叠区，且默认收起。
    const raw = screen.getByTestId("bot-background-raw-result");
    expect(raw).not.toHaveAttribute("open");
    expect(raw).toHaveTextContent('{"cron_expression"');
  });

  it("summary 是 JSON 但找不到调用：主行只给简短说明，不把 JSON 当正文", () => {
    renderMessage(backgroundResult);

    const note = screen.getByText("后台调用已完成（原始结果见下）");
    expect(note).not.toHaveTextContent("cron_expression");
    const raw = screen.getByTestId("bot-background-raw-result");
    expect(raw).not.toHaveAttribute("open");
    expect(raw).toHaveTextContent('{"cron_expression"');
  });

  it("找不到调用且 summary 是自然语言：保持原兜底直接当正文，不出折叠区", () => {
    renderMessage({
      ...backgroundResult,
      content: { ...backgroundResult.content, summary: "任务已创建并启用。" },
    });

    expect(screen.getByText("任务已创建并启用。")).toBeInTheDocument();
    expect(
      screen.queryByTestId("bot-background-raw-result"),
    ).not.toBeInTheDocument();
  });
});
