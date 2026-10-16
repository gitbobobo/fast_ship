import { useState } from "react";
import { ChevronDown, ChevronUp, Wrench } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ApiRequestError } from "@/lib/api/client";
import { botApi, type BotCall } from "@/lib/api/bots";

/** 调用状态中文标签；未知值原样显示（账本可能先于界面升级）。 */
export const BOT_CALL_STATUS_LABELS: Record<string, string> = {
  pending_confirmation: "待确认",
  queued: "已受理",
  running: "执行中",
  succeeded: "已完成",
  failed: "失败",
  rejected: "已拒绝",
  cancelled: "已取消",
  not_executed: "未执行",
};

export function callStatusLabel(status: string) {
  return BOT_CALL_STATUS_LABELS[status] ?? status;
}

/** 机器人写操作的错误文案：服务端 message 优先，否则兜底。 */
export function botActionErrorText(error: unknown, fallback = "操作失败，请重试") {
  return error instanceof ApiRequestError && error.message
    ? error.message
    : fallback;
}

const STATUS_BADGE: Record<string, string> = {
  pending_confirmation: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
  queued: "bg-primary/10 text-primary",
  running: "bg-primary/10 text-primary",
  succeeded: "bg-emerald-500/15 text-emerald-700 dark:text-emerald-400",
  failed: "bg-destructive/10 text-destructive",
  rejected: "bg-muted text-muted-foreground",
  cancelled: "bg-muted text-muted-foreground",
  not_executed: "bg-muted text-muted-foreground",
};

/** 参数摘要：一行可读的 JSON；过长截断，完整内容进「查看详情」。 */
function summarizeArguments(call: Pick<BotCall, "arguments">): string {
  try {
    const text = JSON.stringify(call.arguments);
    return text.length > 200 ? `${text.slice(0, 200)}…` : text;
  } catch {
    return "";
  }
}

/**
 * 工具调用卡：状态从调用账本派生。历史消息引用的调用若不在账本里
 * （如只展示最近调用），卡片退化为仅展示工具与 action 的静态形态。
 * 「查看完整结果」经 result 端点按需读取，展开状态留在卡片本地。
 */
export function BotToolCard({
  botId,
  call,
  fallbackName,
  fallbackAction,
  onCancel,
  cancelling = false,
}: {
  botId: string;
  call?: BotCall;
  /** 账本缺卡时的展示兜底（来自消息里的 tool_calls 引用）。 */
  fallbackName?: string;
  fallbackAction?: string;
  onCancel?: (callId: string) => void;
  cancelling?: boolean;
}) {
  const [showResult, setShowResult] = useState(false);
  const [fullResult, setFullResult] = useState<string | null>(null);
  const [resultError, setResultError] = useState<string | null>(null);

  const name = call?.tool_name ?? fallbackName ?? "";
  const action = call?.action ?? fallbackAction ?? "";
  const cancellable =
    call &&
    onCancel &&
    (call.status === "pending_confirmation" ||
      call.status === "queued" ||
      call.status === "running");

  const loadFullResult = async (callId: string) => {
    setResultError(null);
    try {
      const result = await botApi.callResult(botId, callId);
      setFullResult(JSON.stringify(result, null, 2));
    } catch (err) {
      setResultError(botActionErrorText(err, "结果读取失败"));
    }
  };

  return (
    <section
      data-testid="bot-tool-card"
      className="space-y-2 rounded-xl border bg-muted/30 px-3.5 py-2.5 text-sm"
    >
      <div className="flex flex-wrap items-center gap-2">
        <span className="flex items-center gap-1.5 font-medium">
          <Wrench className="h-3.5 w-3.5 text-muted-foreground" />
          {name}.{action}
        </span>
        {call && (
          <span
            data-testid="bot-call-status"
            className={`rounded-full px-2 py-0.5 text-xs font-medium ${STATUS_BADGE[call.status] ?? "bg-muted text-muted-foreground"}`}
          >
            {callStatusLabel(call.status)}
          </span>
        )}
        {cancellable && (
          <Button
            type="button"
            size="xs"
            variant="ghost"
            className="ml-auto text-muted-foreground"
            disabled={cancelling}
            onClick={() => onCancel!(call.id)}
          >
            取消
          </Button>
        )}
      </div>
      {call && Object.keys(call.arguments ?? {}).length > 0 && (
        <p className="break-all font-mono text-xs text-muted-foreground">
          {summarizeArguments(call)}
        </p>
      )}
      {call?.error_message && (
        <p className="text-xs text-destructive">
          {call.error_code ? `${call.error_code}: ` : ""}
          {call.error_message}
        </p>
      )}
      {call?.effect_may_have_occurred && (
        <p className="text-xs text-amber-700 dark:text-amber-400">
          副作用可能已经发生；请先核实再决定后续操作。
        </p>
      )}
      {call?.truncated && (
        <div className="space-y-1">
          <Button
            type="button"
            size="xs"
            variant="ghost"
            className="text-muted-foreground"
            onClick={() => {
              if (showResult) {
                setShowResult(false);
                return;
              }
              setShowResult(true);
              if (fullResult === null) void loadFullResult(call.id);
            }}
          >
            {showResult ? (
              <>
                <ChevronUp className="h-3 w-3" /> 收起完整结果
              </>
            ) : (
              <>
                <ChevronDown className="h-3 w-3" /> 查看完整结果
              </>
            )}
          </Button>
          {showResult &&
            (resultError ? (
              <p role="alert" className="text-xs text-destructive">
                {resultError}
              </p>
            ) : fullResult !== null ? (
              <pre className="max-h-60 overflow-auto rounded-lg bg-background p-2 font-mono text-xs">
                {fullResult}
              </pre>
            ) : (
              <p className="text-xs text-muted-foreground">结果读取中…</p>
            ))}
        </div>
      )}
    </section>
  );
}
