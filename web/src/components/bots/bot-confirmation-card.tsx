import { ShieldAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { BotCall, BotCallDecision } from "@/lib/api/bots";
import { toolActionLabel, toolParamChips } from "@/lib/bots/tool-labels";
import { botActionErrorText, ParamChips } from "./bot-tool-card";

// fs_schedule 里需要确认的动作（与 server 的 botScheduleConfirmation 对齐）：
// create、敏感字段 update、非 dry_run run、enable、delete。disable 与
// name/cron/timezone-only update 仍同步免确认，不会出现在确认卡上。
const SCHEDULE_CONFIRM_ACTIONS = new Set(["create", "update", "run", "enable", "delete"]);
// 只有 run 可以「始终允许」（按任务绑定）；create/update/enable/delete 服务端
// 也会拒绝，见 bot_authorization.go。
const SCHEDULE_ALWAYS_ALLOW_ACTIONS = new Set(["run"]);

/**
 * fs_schedule 确认卡的生效配置摘要：任务一经批准，之后每次运行默认放行
 * 写操作，确认前必须看清指令全文与运行配置。
 *
 * 取值以服务端受理时算好的 `execution_context.schedule` 为准：
 * - run 的执行器只按 task_id 读库里的任务，模型传来的配置字段一概不生效，
 *   所以这里**只看快照**（否则模型可以拿无害参数遮住任务里真正的指令）；
 * - update 是「提供即改」，展示本次提供的值，未提供的行回退到快照里的现值
 *   （仅改 needs_report/model 时也能看到任务原来的完整指令）；
 * - create 快照与参数同源。
 */
function ScheduleConfirmSummary({ call }: { call: BotCall }) {
  const args = call.arguments ?? {};
  const sched = (() => {
    const ctx = call.execution_context?.schedule;
    return ctx !== null && typeof ctx === "object" && !Array.isArray(ctx)
      ? (ctx as Record<string, unknown>)
      : {};
  })();
  const isUpdate = call.action === "update";
  const isRun = call.action === "run";

  // 空白串按「未提供」处理：服务端 applyScheduleTaskUpdate 对 TrimSpace 后为
  // 空的字段一律视为保留原值，卡片必须同一口径，否则模型传一个空格就能让
  // 卡片显示空指令、实际仍按原指令运行。
  const str = (v: unknown) =>
    typeof v === "string" && v.trim() !== "" ? v : undefined;
  const schedStr = (...keys: string[]) => {
    for (const key of keys) {
      const v = str(sched[key]);
      if (v !== undefined) return v;
    }
    return undefined;
  };
  // 本次调用真正生效的值：只有 create/update 会应用参数；run/enable/delete
  // 的执行器只按 task_id 读库里配置，卡片必须只看快照（否则模型附带的参数能
  // 遮住真正的执行内容）。
  const usesArgs = call.action === "create" || isUpdate;
  const effective = (argKey: string, ...snapKeys: string[]) => {
    if (usesArgs) {
      const argValue = str(args[argKey]);
      if (argValue !== undefined) return argValue;
    }
    return schedStr(...snapKeys);
  };

  const name = effective("name", "name", "task_name") ?? (isUpdate ? "保持不变" : "—");
  const cron =
    effective("cron", "cron", "cron_expression") ?? (isUpdate ? "保持不变" : "—");
  // 时区空串在服务端语义是 UTC；update 未提供且快照缺失才是「保持不变」。
  const timezone =
    effective("timezone", "timezone") ?? (isUpdate ? "保持不变" : isRun ? "—" : "UTC");
  const nextRun = schedStr("next_run_at") ?? "—";
  const model = usesArgs
    ? args.model !== undefined
      ? (str(args.model) ?? "跟随机器人当前模型")
      : (schedStr("effective_model") ?? "—")
    : (schedStr("effective_model") ?? "—");
  const reportFlag = (() => {
    if (usesArgs && typeof args.needs_report === "boolean") return args.needs_report;
    if (typeof sched.needs_report === "boolean") return sched.needs_report;
    return undefined;
  })();
  const instruction = effective("instruction", "instruction");

  const rows: [string, string][] = [
    ["任务名", name],
    ["cron", cron],
    ["时区", timezone],
    ["下次运行", nextRun],
    ["模型", model],
    ["需要汇报", reportFlag === undefined ? "—" : reportFlag ? "是" : "否"],
  ];

  return (
    <div
      data-testid="bot-schedule-summary"
      className="space-y-2 rounded-lg border border-amber-500/30 bg-background/60 px-3 py-2"
    >
      <dl className="grid grid-cols-[4.5rem_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs">
        {rows.map(([label, value]) => (
          <div key={label} className="contents">
            <dt className="text-muted-foreground">{label}</dt>
            <dd className="font-mono break-all">{value}</dd>
          </div>
        ))}
      </dl>
      {instruction !== undefined && (
        <div className="space-y-1">
          <p className="text-xs text-muted-foreground">指令</p>
          <pre className="max-h-72 overflow-auto rounded-lg bg-background/80 p-2 font-mono text-xs whitespace-pre-wrap break-all">
            {instruction}
          </pre>
        </div>
      )}
      <p className="text-xs font-medium text-amber-700 dark:text-amber-400">
        批准即授权该任务以后每次运行默认放行写操作；任务运行中的写操作不会再向你确认。
        {!isRun &&
          `${call.action === "create" ? "创建" : "修改"}任务不支持「始终允许」，每次都要确认。`}
      </p>
    </div>
  );
}

/**
 * 写操作确认卡：只对已保存的调用参数做决定，不能同时修改参数。
 * always_allow 会建立与本次调用相同 action、范围与字段约束的授权，
 * 可随时在「授权管理」中撤销。确认依赖完整可见的参数——整组替换
 * （replace_checklist 等）必须能看清内容，完整 JSON 默认展开不折叠。
 */
export function BotConfirmationCard({
  call,
  busy,
  error,
  onDecide,
}: {
  call: BotCall;
  busy: boolean;
  error?: unknown;
  onDecide: (callId: string, decision: BotCallDecision) => void;
}) {
  const chips = toolParamChips(call.tool_name, call.arguments);
  const isScheduleCall =
    call.tool_name === "fs_schedule" &&
    SCHEDULE_CONFIRM_ACTIONS.has(call.action);
  // fs_schedule 的 create/update 被服务端拒绝「始终允许」（指令要逐次过目），
  // 不再渲染该按钮与其说明；run 仍可按任务维度建立授权。
  const canAlwaysAllow = !(
    call.tool_name === "fs_schedule" && !SCHEDULE_ALWAYS_ALLOW_ACTIONS.has(call.action)
  );
  const argPretty = (() => {
    try {
      return JSON.stringify(call.arguments, null, 2);
    } catch {
      return "";
    }
  })();

  return (
    <section
      data-testid="bot-confirmation-card"
      className="space-y-2.5 rounded-xl border border-amber-500/40 bg-amber-500/5 px-4 py-3 text-sm"
    >
      <div>
        <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <ShieldAlert className="h-3.5 w-3.5" />
          需要你的确认 · {toolActionLabel(call.tool_name, call.action)}
        </h3>
        <p className="mt-0.5 font-mono text-xs text-muted-foreground/70">
          {call.tool_name}.{call.action}
        </p>
      </div>
      {isScheduleCall && <ScheduleConfirmSummary call={call} />}
      <ParamChips chips={chips} />
      {argPretty && (
        <pre className="max-h-72 overflow-auto rounded-lg bg-background/80 p-2 font-mono text-xs whitespace-pre-wrap break-all">
          {argPretty}
        </pre>
      )}
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          disabled={busy}
          onClick={() => onDecide(call.id, "approve")}
        >
          批准
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={() => onDecide(call.id, "reject")}
        >
          拒绝
        </Button>
        {canAlwaysAllow && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy}
            title="建立与本次调用相同范围与字段约束的授权，并批准当前调用"
            onClick={() => onDecide(call.id, "always_allow")}
          >
            始终允许
          </Button>
        )}
      </div>
      {canAlwaysAllow && (
        <p className="text-xs text-muted-foreground">
          「始终允许」按本次调用的 action、范围与写入字段精确授权，可在授权管理中撤销。
        </p>
      )}
      {error !== undefined && error !== null && (
        <p role="alert" className="text-xs text-destructive">
          {botActionErrorText(error)}
        </p>
      )}
    </section>
  );
}
