import { ListChecks } from "lucide-react";
import type { BotCall } from "@/lib/api/bots";
import { toolParamChips } from "@/lib/bots/tool-labels";
import { BotToolCard } from "./bot-tool-card";

/** 后台列表多行同名卡片并排时的区分副文本：消息正文截前 24 字优先，
 *  无正文回退参数 chip 摘要（工作区/分支等生效取值）。 */
function callSubtitle(call: BotCall): string | undefined {
  const args = call.arguments;
  if (args === null || typeof args !== "object") return undefined;
  const text = args["text"];
  if (typeof text === "string" && text.trim() !== "") {
    const t = text.trim();
    return t.length > 24 ? `${t.slice(0, 24)}…` : t;
  }
  const joined = toolParamChips(call.tool_name, args, call.action)
    .map((c) => c.value)
    .slice(0, 3)
    .join(" · ");
  return joined === "" ? undefined : joined;
}

/**
 * 后台调用列表：异步且未终结的调用（含待确认）集中展示，逐项可取消。
 * 主线 run 完成、甚至页面停留时后台结果晚到，此面板都不因此隐藏——
 * 「主线停止」在对话区，与这里的「逐项取消」是两个独立动作。
 */
export function BotBackgroundCalls({
  botId,
  calls,
  busyCallId,
  actionError,
  onCancel,
}: {
  botId: string;
  calls: BotCall[];
  busyCallId: string | null;
  actionError?: unknown;
  onCancel: (callId: string) => void;
}) {
  if (calls.length === 0) return null;
  return (
    <section
      data-testid="bot-background-calls"
      className="space-y-2 rounded-xl border bg-card px-3.5 py-3"
    >
      <h3 className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <ListChecks className="h-3.5 w-3.5" />
        后台调用（{calls.length}）
        <span className="font-normal">·主线结束后仍会继续，可逐项取消</span>
      </h3>
      {calls.map((call) => (
        <BotToolCard
          key={call.id}
          botId={botId}
          call={call}
          cancelling={busyCallId === call.id}
          onCancel={onCancel}
          subtitle={callSubtitle(call)}
        />
      ))}
      {actionError !== undefined && actionError !== null && (
        <p role="alert" className="text-xs text-destructive">
          {String(actionError)}
        </p>
      )}
      {calls.every((c) => c.status === "pending_confirmation") && (
        <p className="text-xs text-muted-foreground">
          待确认的调用请在上方对话的确认卡中处理；这里也可直接取消。
        </p>
      )}
    </section>
  );
}
