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
// 授权提示里的动作名要与服务端口径一致（创建/修改/启用/删除），否则 enable/
// delete 的卡片会写成「修改任务不支持始终允许」。
const SCHEDULE_ACTION_LABELS: Record<string, string> = {
  create: "创建",
  update: "修改",
  enable: "启用",
  delete: "删除",
};

// 每种动作批准后的后果不同：run 只执行一次、enable/创建/修改会授权以后的每次
// 运行、delete 不可逆。卡片按动作说清后果，不要一句「以后每次运行都放行」套到
// 所有动作上。
const SCHEDULE_APPROVAL_HINTS: Record<string, string> = {
  create:
    "批准即授权该任务以后每次运行默认放行写操作；任务运行中的写操作不会再向你确认。",
  update:
    "批准即按上面的配置更新任务，之后每次运行默认放行写操作；任务运行中的写操作不会再向你确认。",
  enable:
    "批准即重新启用该任务：之后的每次运行都默认放行写操作，不会再向你确认。",
  delete: "批准即删除该任务，不可逆（运行记录保留）。",
  run: "批准即立即执行一次该任务；这次运行里的写操作默认放行，不会再向你确认。",
};

/**
 * fs_schedule 确认卡的生效配置摘要：任务一经批准，之后每次运行默认放行
 * 写操作，确认前必须看清指令全文与运行配置。
 *
 * 取值一律只看服务端受理时算好的 `execution_context.schedule`（生效配置）：
 * 前端不重新解释参数、不自己实现合并规则——模型参数对部分动作不生效
 * （run/enable/delete 只按 task_id 读库），对 update 又是「未提供则保留原值」，
 * 两套真值放在一起就会互相遮住（例如空白 instruction、model:null）。
 * 参数只在下面的原始调用 JSON 里原样展示，用于核对改了什么。
 */
function ScheduleConfirmSummary({ call }: { call: BotCall }) {
  const sched = (() => {
    const ctx = call.execution_context?.schedule;
    return ctx !== null && typeof ctx === "object" && !Array.isArray(ctx)
      ? (ctx as Record<string, unknown>)
      : {};
  })();
  const isRun = call.action === "run";

  // 空白串按「未提供」处理（服务端 TrimSpace 语义），避免把空白当成有效配置。
  const str = (v: unknown) =>
    typeof v === "string" && v.trim() !== "" ? v : undefined;
  const schedStr = (...keys: string[]) => {
    for (const key of keys) {
      const v = str(sched[key]);
      if (v !== undefined) return v;
    }
    return undefined;
  };

  // 生效配置一律只看服务端受理时算好的快照：前端不重新解释参数、不自己实现
  // 合并规则。模型参数对部分动作不生效（run/enable/delete 只按 task_id 读库），
  // 对 update 又是「未提供则保留原值」——两套真值放在一起就会互相遮住（空白
  // instruction、model:null 都是这么漏出去的）。参数只在原始调用 JSON 里看。
  const name = schedStr("name", "task_name") ?? "—";
  const cron = schedStr("cron", "cron_expression") ?? "—";
  const timezone = schedStr("timezone") ?? (isRun ? "—" : "UTC");
  const nextRun = schedStr("next_run_at") ?? "—";
  // 模型来源要如实说清：任务固定覆盖 vs 跟随机器人（换模型后下次运行会变）。
  const modelName = schedStr("effective_model") ?? "—";
  const model =
    sched.model_inherited === true
      ? `跟随机器人模型（目前 ${modelName}）`
      : modelName === "—"
        ? "—"
        : `${modelName}（任务指定）`;
  const reportFlag =
    typeof sched.needs_report === "boolean" ? sched.needs_report : undefined;
  const instruction = schedStr("instruction");

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
        {SCHEDULE_APPROVAL_HINTS[call.action] ??
          "批准即按上面的配置生效，请确认无误。"}
        {!SCHEDULE_ALWAYS_ALLOW_ACTIONS.has(call.action) &&
          `${SCHEDULE_ACTION_LABELS[call.action] ?? "修改"}任务不支持「始终允许」，每次都要确认。`}
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
