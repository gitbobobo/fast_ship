// background_result 的 fs_schedule 人话摘要：服务端会把工具结果 JSON 截到
// 200 字符当 summary，直接当正文既断在字符串中间也看不出成败；这里用
// 服务端受理时算好的 execution_context.schedule 快照拼一句可读的结果行。
// 缺字段就跳过那一段，不输出「—」之类的占位；未知动作返回空串，由
// 调用方退回原始 summary 兜底。
import type { BotCall } from "@/lib/api/bots";

// 动作词与确认卡 SCHEDULE_ACTION_LABELS 同一套口径。
const SCHEDULE_RESULT_VERBS: Record<string, string> = {
  create: "创建",
  update: "修改",
  enable: "启用",
  delete: "删除",
  run: "运行",
};

// 空白串按「未提供」处理，与确认卡的 TrimSpace 口径一致。
const str = (v: unknown) =>
  typeof v === "string" && v.trim() !== "" ? v : undefined;

// cron/时区描述的是「以后按什么计划跑」：create/update/enable 有意义，
// delete 之后任务已不存在、run 是这次手动触发，都不该再拼计划。
const CARRIES_SCHEDULE = new Set(["create", "update", "enable"]);

export function scheduleResultLine(call: BotCall, failed: boolean): string {
  const verb = SCHEDULE_RESULT_VERBS[call.action];
  if (!verb) return "";
  const ctx = call.execution_context?.schedule;
  const sched =
    ctx !== null && typeof ctx === "object" && !Array.isArray(ctx)
      ? (ctx as Record<string, unknown>)
      : {};
  const name = str(sched.name) ?? str(sched.task_name);
  const cron = str(sched.cron) ?? str(sched.cron_expression);
  const timezone = str(sched.timezone);
  const core = `定时任务${name ? `「${name}」` : ""}`;
  let line = failed ? `${verb}${core}未成功` : `已${verb}${core}`;
  if (cron && CARRIES_SCHEDULE.has(call.action)) {
    line += ` · ${cron}`;
    // 时区是对运行时刻的注解，没有 cron 时单独一个（UTC）反而费解，一并跳过。
    if (timezone) line += `（${timezone}）`;
  }
  return line;
}
