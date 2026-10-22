import { describe, expect, it } from "vitest";
import type { BotCall } from "@/lib/api/bots";
import { scheduleResultLine } from "./schedule-result-summary";

function makeScheduleCall(overrides: Partial<BotCall> = {}): BotCall {
  return {
    id: "call-1",
    bot_id: "b1",
    tool_name: "fs_schedule",
    action: "create",
    arguments: {},
    execution_mode: "async",
    requires_confirmation: true,
    status: "succeeded",
    execution_context: {
      schedule: {
        task_id: "task-0001",
        name: "每日站会提醒",
        cron: "0 9 * * *",
        timezone: "Asia/Shanghai",
      },
    },
    created_at: "2026-10-18T00:00:00Z",
    ...overrides,
  };
}

describe("scheduleResultLine", () => {
  it("成功：动作 + 任务名 + cron（时区）拼一句人话", () => {
    expect(scheduleResultLine(makeScheduleCall(), false)).toBe(
      "已创建定时任务「每日站会提醒」 · 0 9 * * *（Asia/Shanghai）",
    );
  });

  it("失败：改写为「未成功」，不用「已创建」假装完成", () => {
    expect(scheduleResultLine(makeScheduleCall(), true)).toBe(
      "创建定时任务「每日站会提醒」未成功 · 0 9 * * *（Asia/Shanghai）",
    );
  });

  it("缺字段就跳过那一段，不留「—」占位", () => {
    expect(
      scheduleResultLine(
        makeScheduleCall({ execution_context: { schedule: { cron: "0 9 * * *" } } }),
        false,
      ),
    ).toBe("已创建定时任务 · 0 9 * * *");
    // 时区是对 cron 的注解，没有 cron 时连同跳过。
    expect(
      scheduleResultLine(
        makeScheduleCall({
          execution_context: { schedule: { name: "只改汇报", timezone: "UTC" } },
        }),
        false,
      ),
    ).toBe("已创建定时任务「只改汇报」");
    expect(
      scheduleResultLine(makeScheduleCall({ execution_context: null }), false),
    ).toBe("已创建定时任务");
  });

  it("空白字段按未提供处理（与服务端 TrimSpace 同口径）", () => {
    expect(
      scheduleResultLine(
        makeScheduleCall({
          execution_context: { schedule: { name: "  ", cron: "0 9 * * *" } },
        }),
        false,
      ),
    ).toBe("已创建定时任务 · 0 9 * * *");
  });

  it("update 的服务端快照用 cron_expression 键，同样取得到", () => {
    expect(
      scheduleResultLine(
        makeScheduleCall({
          action: "update",
          execution_context: {
            schedule: {
              name: "真实任务",
              cron_expression: "*/5 * * * *",
              timezone: "Asia/Shanghai",
            },
          },
        }),
        false,
      ),
    ).toBe("已修改定时任务「真实任务」 · */5 * * * *（Asia/Shanghai）");
  });

  it("run/delete 不带计划：cron 描述的是「以后按什么跑」", () => {
    expect(
      scheduleResultLine(
        makeScheduleCall({
          action: "run",
          execution_context: {
            schedule: { name: "真实任务", cron: "*/5 * * * *", timezone: "UTC" },
          },
        }),
        false,
      ),
    ).toBe("已运行定时任务「真实任务」");
    expect(
      scheduleResultLine(
        makeScheduleCall({
          action: "delete",
          execution_context: { schedule: { name: "真实任务", cron: "*/5 * * * *" } },
        }),
        true,
      ),
    ).toBe("删除定时任务「真实任务」未成功");
  });

  it("表外动作返回空串，由调用方退回原始 summary 兜底", () => {
    expect(scheduleResultLine(makeScheduleCall({ action: "disable" }), false)).toBe(
      "",
    );
  });
});
