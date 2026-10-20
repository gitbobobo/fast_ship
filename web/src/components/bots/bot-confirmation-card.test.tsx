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

// fs_schedule 的 create/update/run 需逐次确认：摘要块给出服务端受理时算好的
// execution_context.schedule（下次运行与有效模型），前端不解析 cron。
function makeScheduleCall(overrides: Partial<BotCall> = {}): BotCall {
  return makeCall({
    tool_name: "fs_schedule",
    action: "create",
    arguments: {
      name: "每日巡检",
      cron: "0 9 * * *",
      timezone: "Asia/Shanghai",
      instruction: "检查各项目未关闭的问题，\n逐条用中文汇报。",
      model: "",
      needs_report: true,
    },
    execution_context: {
      schedule: {
        task_id: "task-0001",
        name: "每日巡检",
        cron: "0 9 * * *",
        timezone: "Asia/Shanghai",
        instruction: "检查各项目未关闭的问题，\n逐条用中文汇报。",
        needs_report: true,
        next_run_at: "2026-10-21 09:00:00 CST",
        next_run_at_utc: "2026-10-21T01:00:00Z",
        effective_model: "gpt-5.1",
      },
    },
    ...overrides,
  });
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

  it("非 fs_schedule 的确认卡不渲染定时任务摘要", () => {
    renderCard(makeCall());
    const card = screen.getByTestId("bot-confirmation-card");
    expect(
      within(card).queryByTestId("bot-schedule-summary"),
    ).not.toBeInTheDocument();
  });

  it("fs_schedule.create：摘要展示七项信息与授权提示，不渲染「始终允许」", () => {
    renderCard(makeScheduleCall());
    const card = screen.getByTestId("bot-confirmation-card");
    const summary = within(card).getByTestId("bot-schedule-summary");

    expect(within(summary).getByText("任务名")).toBeInTheDocument();
    expect(within(summary).getByText("每日巡检")).toBeInTheDocument();
    expect(within(summary).getByText("0 9 * * *")).toBeInTheDocument();
    expect(within(summary).getByText("Asia/Shanghai")).toBeInTheDocument();
    expect(
      within(summary).getByText("2026-10-21 09:00:00 CST"),
    ).toBeInTheDocument();
    // 生效模型取服务端快照（参数里的空串在卡片上不再二次解释）。
    expect(within(summary).getByText("gpt-5.1")).toBeInTheDocument();
    expect(within(summary).getByText("需要汇报")).toBeInTheDocument();
    expect(within(summary).getByText("是")).toBeInTheDocument();
    // 指令全文原样展示，不截断。
    expect(summary).toHaveTextContent("逐条用中文汇报。");

    expect(
      within(summary).getByText(
        /批准即授权该任务以后每次运行默认放行写操作；任务运行中的写操作不会再向你确认。/,
      ),
    ).toBeInTheDocument();
    expect(
      within(summary).getByText(/创建任务不支持「始终允许」，每次都要确认。/),
    ).toBeInTheDocument();

    expect(within(card).getByRole("button", { name: "批准" })).toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "拒绝" })).toBeInTheDocument();
    expect(
      within(card).queryByRole("button", { name: "始终允许" }),
    ).not.toBeInTheDocument();
    expect(
      within(card).queryByText(/可在授权管理中撤销/),
    ).not.toBeInTheDocument();
  });

  it("fs_schedule.update：展示服务端合并后的生效配置，不渲染「始终允许」", () => {
    renderCard(
      makeScheduleCall({
        action: "update",
        arguments: {
          task_id: "task-0001",
          instruction: "改成只看高优先级问题。",
        },
        execution_context: {
          schedule: {
            task_id: "task-0001",
            name: "每日巡检",
            cron: "0 9 * * *",
            timezone: "Asia/Shanghai",
            instruction: "改成只看高优先级问题。",
            needs_report: false,
            next_run_at: "2026-10-21 09:00:00 CST",
            effective_model: "gpt-5.1",
          },
        },
      }),
    );
    const card = screen.getByTestId("bot-confirmation-card");
    const summary = within(card).getByTestId("bot-schedule-summary");

    // 未提供的字段由服务端合并后给出实际生效值，卡片只渲染快照。
    expect(within(summary).getByText("每日巡检")).toBeInTheDocument();
    expect(within(summary).getByText("0 9 * * *")).toBeInTheDocument();
    expect(within(summary).getByText("Asia/Shanghai")).toBeInTheDocument();
    expect(within(summary).getByText("gpt-5.1")).toBeInTheDocument();
    expect(within(summary).getByText("否")).toBeInTheDocument();
    expect(
      within(summary).getByText("2026-10-21 09:00:00 CST"),
    ).toBeInTheDocument();
    expect(summary).toHaveTextContent("改成只看高优先级问题。");
    expect(
      within(summary).getByText(/修改任务不支持「始终允许」，每次都要确认。/),
    ).toBeInTheDocument();
    expect(
      within(card).queryByRole("button", { name: "始终允许" }),
    ).not.toBeInTheDocument();
  });

  // INT-136 审查回归：run 的执行器只按 task_id 读库里的任务，模型多传的配置
  // 字段不生效——卡片若优先显示参数，就能拿一份无害指令遮住真正的执行内容。
  it("fs_schedule.run：忽略模型传入的配置字段，只显示服务端任务快照", () => {
    renderCard(
      makeScheduleCall({
        action: "run",
        arguments: {
          task_id: "task-0001",
          name: "只读巡检",
          instruction: "只统计未关闭问题数量，不要修改任何内容。",
          needs_report: false,
        },
        execution_context: {
          schedule: {
            name: "真实任务",
            cron_expression: "*/5 * * * *",
            timezone: "Asia/Shanghai",
            instruction: "每天把高优先级问题全部关闭并删除过期任务。",
            needs_report: true,
            next_run_at: "2026-10-21 09:00:00 CST",
            effective_model: "gpt-5.1",
          },
        },
      }),
    );
    const summary = within(screen.getByTestId("bot-schedule-summary"));
    expect(summary.getByText("真实任务")).toBeInTheDocument();
    expect(summary.getByText("*/5 * * * *")).toBeInTheDocument();
    expect(
      summary.getByText("每天把高优先级问题全部关闭并删除过期任务。"),
    ).toBeInTheDocument();
    expect(summary.getByText("是")).toBeInTheDocument();
    expect(summary.queryByText("只读巡检")).not.toBeInTheDocument();
    expect(
      summary.queryByText("只统计未关闭问题数量，不要修改任何内容。"),
    ).not.toBeInTheDocument();
  });

  // 只改 needs_report/model 的敏感 update 也要看到任务原来的指令全文：
  // 批准等于授权它以后每次运行默认放行写操作，用户必须看清它在做什么。
  it("fs_schedule.update：未提供指令时仍展示服务端快照里的指令全文", () => {
    renderCard(
      makeScheduleCall({
        action: "update",
        arguments: { task_id: "task-0001", needs_report: false },
        execution_context: {
          schedule: {
            name: "每日巡检",
            cron: "0 9 * * *",
            timezone: "Asia/Shanghai",
            instruction: "检查各项目未关闭的问题，逐条用中文汇报。",
            needs_report: false,
            next_run_at: "2026-10-21 09:00:00 CST",
            effective_model: "gpt-5.1",
          },
        },
      }),
    );
    const summary = within(screen.getByTestId("bot-schedule-summary"));
    expect(
      summary.getByText("检查各项目未关闭的问题，逐条用中文汇报。"),
    ).toBeInTheDocument();
    expect(summary.getByText("否")).toBeInTheDocument();
    expect(summary.queryByText("保持不变")).not.toBeInTheDocument();
  });

  // 服务端把空白 instruction 当「未提供」（保留原值），卡片必须同一口径：
  // 否则模型传一个空格就能让卡片显示空指令、实际按原指令运行。
  it("fs_schedule.update：空白 instruction 按未提供处理，仍展示快照里的指令", () => {
    renderCard(
      makeScheduleCall({
        action: "update",
        arguments: { task_id: "task-0001", instruction: "   ", needs_report: false },
        execution_context: {
          schedule: {
            name: "真实任务",
            cron: "0 9 * * *",
            instruction: "每天把高优先级问题全部关闭。",
            needs_report: false,
            next_run_at: "2026-10-21 09:00:00 CST",
            effective_model: "gpt-5.1",
          },
        },
      }),
    );
    const summary = within(screen.getByTestId("bot-schedule-summary"));
    expect(
      summary.getByText("每天把高优先级问题全部关闭。"),
    ).toBeInTheDocument();
  });

  // enable/delete 的执行器只按 task_id 读库里配置：模型附带的参数不能上卡。
  it("fs_schedule.enable：忽略模型传入的配置字段，只显示服务端任务快照", () => {
    renderCard(
      makeScheduleCall({
        action: "enable",
        arguments: {
          task_id: "task-0001",
          instruction: "只统计问题数量，不要修改任何内容。",
        },
        execution_context: {
          schedule: {
            name: "真实任务",
            cron: "*/5 * * * *",
            instruction: "每天把高优先级问题全部关闭。",
            next_run_at: "2026-10-21 09:00:00 CST",
            effective_model: "gpt-5.1",
          },
        },
      }),
    );
    const summary = within(screen.getByTestId("bot-schedule-summary"));
    expect(
      summary.getByText("每天把高优先级问题全部关闭。"),
    ).toBeInTheDocument();
    expect(
      summary.queryByText("只统计问题数量，不要修改任何内容。"),
    ).not.toBeInTheDocument();
  });

  // enable 是「重新武装一个运行内默认放行写操作的任务」、delete 不可逆：
  // 两者都要确认，且都不提供「始终允许」。
  it.each(["enable", "delete"] as const)(
    "fs_schedule.%s：展示任务快照、不渲染「始终允许」",
    (action) => {
      renderCard(
        makeScheduleCall({
          action,
          arguments: { task_id: "task-0001" },
          execution_context: {
            schedule: {
              name: "真实任务",
              cron: "0 9 * * *",
              timezone: "Asia/Shanghai",
              instruction: "每天把高优先级问题全部关闭。",
              next_run_at: "2026-10-21 09:00:00 CST",
              effective_model: "gpt-5.1",
            },
          },
        }),
      );
      const summary = within(screen.getByTestId("bot-schedule-summary"));
      expect(summary.getByText("真实任务")).toBeInTheDocument();
      expect(
        summary.getByText("每天把高优先级问题全部关闭。"),
      ).toBeInTheDocument();
      const card = screen.getByTestId("bot-confirmation-card");
      expect(
        within(card).queryByRole("button", { name: "始终允许" }),
      ).not.toBeInTheDocument();
      // 提示文案里的动作名要跟着动作走（enable/delete 不能写成「修改」）。
      const label = action === "enable" ? "启用" : "删除";
      expect(
        summary.getByText(new RegExp(`${label}任务不支持「始终允许」`)),
      ).toBeInTheDocument();
    },
  );

  it("fs_schedule.run：参数只有 task_id 时读服务端快照，保留「始终允许」", () => {
    renderCard(
      makeScheduleCall({
        action: "run",
        arguments: { task_id: "task-0001" },
        execution_context: {
          schedule: {
            name: "每日巡检",
            cron_expression: "0 9 * * *",
            timezone: "Asia/Shanghai",
            next_run_at: "2026-10-21 09:00:00 CST",
            next_run_at_utc: "2026-10-21T01:00:00Z",
            effective_model: "gpt-5.1",
          },
        },
      }),
    );
    const card = screen.getByTestId("bot-confirmation-card");
    const summary = within(card).getByTestId("bot-schedule-summary");

    expect(within(summary).getByText("每日巡检")).toBeInTheDocument();
    expect(within(summary).getByText("0 9 * * *")).toBeInTheDocument();
    expect(within(summary).getByText("Asia/Shanghai")).toBeInTheDocument();
    expect(
      within(summary).getByText("2026-10-21 09:00:00 CST"),
    ).toBeInTheDocument();
    expect(within(summary).getByText("gpt-5.1")).toBeInTheDocument();
    // run 没有 instruction 字段行；needs_report 快照没给时占位。
    expect(within(summary).queryByText("指令")).not.toBeInTheDocument();
    expect(within(summary).getByText("需要汇报")).toBeInTheDocument();
    expect(
      within(summary).getByText(
        /批准即授权该任务以后每次运行默认放行写操作/,
      ),
    ).toBeInTheDocument();
    expect(
      within(summary).queryByText(/不支持「始终允许」/),
    ).not.toBeInTheDocument();

    expect(
      within(card).getByRole("button", { name: "始终允许" }),
    ).toBeInTheDocument();
    expect(within(card).getByText(/可在授权管理中撤销/)).toBeInTheDocument();
  });
});
