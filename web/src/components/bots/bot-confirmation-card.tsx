import { ShieldAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { BotCall, BotCallDecision } from "@/lib/api/bots";
import { toolActionLabel, toolParamChips } from "@/lib/bots/tool-labels";
import { botActionErrorText, ParamChips } from "./bot-tool-card";

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
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          title="建立与本次调用相同范围与字段约束的授权，并批准当前调用"
          onClick={() => onDecide(call.id, "always_allow")}
        >
          始终允许
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        「始终允许」按本次调用的 action、范围与写入字段精确授权，可在授权管理中撤销。
      </p>
      {error !== undefined && error !== null && (
        <p role="alert" className="text-xs text-destructive">
          {botActionErrorText(error)}
        </p>
      )}
    </section>
  );
}
