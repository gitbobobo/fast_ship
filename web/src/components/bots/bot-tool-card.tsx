import { useState } from "react";
import { Link } from "react-router";
import { ChevronDown, ChevronUp, Wrench } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ApiRequestError } from "@/lib/api/client";
import { botApi, type BotCall } from "@/lib/api/bots";
import {
  TOOL_EFFECT_BADGE,
  TOOL_EFFECT_LABELS,
  toolActionEffect,
  toolActionLabel,
  toolParamChips,
  type ToolParamChip,
} from "@/lib/bots/tool-labels";

/** 调用状态中文标签；未知值原样显示（账本可能先于界面升级）。 */
export const BOT_CALL_STATUS_LABELS: Record<string, string> = {
  pending_confirmation: "等待你确认",
  queued: "已排队",
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

/** 参数 chip：短标识能拼出站内外路由时渲染为链接，完整值进 title。 */
export function ParamChipView({ chip }: { chip: ToolParamChip }) {
  const inner = (
    <>
      <span className="text-muted-foreground">{chip.label}</span>
      <span className="font-mono">{chip.value}</span>
    </>
  );
  if (chip.href) {
    return (
      <Link
        to={chip.href}
        title={chip.title}
        className="inline-flex items-center gap-1 rounded-md border bg-background/60 px-1.5 py-0.5 text-xs text-primary hover:border-primary/50 hover:underline"
      >
        {inner}
      </Link>
    );
  }
  return (
    <span
      title={chip.title}
      className="inline-flex items-center gap-1 rounded-md border bg-background/60 px-1.5 py-0.5 text-xs"
    >
      {inner}
    </span>
  );
}

/** 参数 chips 行：无条目时不渲染，工具卡与确认卡共用同一层布局。 */
export function ParamChips({ chips }: { chips: ToolParamChip[] }) {
  if (chips.length === 0) return null;
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {chips.map((chip) => (
        <ParamChipView key={chip.key} chip={chip} />
      ))}
    </div>
  );
}

/** 「查看原始调用」展开时展示的原始标识与完整参数 JSON。 */
function formatRawCall(name: string, action: string, call?: BotCall): string {
  const head = `${name || "?"}.${action || "?"}`;
  if (!call) return head;
  try {
    return `${head}\n${JSON.stringify(call.arguments, null, 2)}`;
  } catch {
    return head;
  }
}

/**
 * 工具调用卡：状态从调用账本派生。历史消息引用的调用若不在账本里
 * （如只展示最近调用），卡片退化为仅展示工具与 action 的静态形态。
 * 标题是中文标签；原始 tool.action 与完整参数收进「查看原始调用」。
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
  const [showRaw, setShowRaw] = useState(false);
  const [showResult, setShowResult] = useState(false);
  const [fullResult, setFullResult] = useState<string | null>(null);
  const [resultError, setResultError] = useState<string | null>(null);

  const name = call?.tool_name ?? fallbackName ?? "";
  const action = call?.action ?? fallbackAction ?? "";
  const title = name ? toolActionLabel(name, action) : "工具调用";
  const effect = toolActionEffect(name, action);
  const chips = call ? toolParamChips(name, call.arguments) : [];
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
          {title}
        </span>
        {name && (
          <span
            className={`rounded-full px-2 py-0.5 text-xs font-medium ${TOOL_EFFECT_BADGE[effect]}`}
          >
            {TOOL_EFFECT_LABELS[effect]}
          </span>
        )}
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
      <ParamChips chips={chips} />
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
      <div className="space-y-1">
        <Button
          type="button"
          size="xs"
          variant="ghost"
          className="text-muted-foreground"
          onClick={() => setShowRaw((v) => !v)}
        >
          {showRaw ? (
            <>
              <ChevronUp className="h-3 w-3" /> 收起原始调用
            </>
          ) : (
            <>
              <ChevronDown className="h-3 w-3" /> 查看原始调用
            </>
          )}
        </Button>
        {showRaw && (
          <pre className="max-h-60 overflow-auto rounded-lg bg-background p-2 font-mono text-xs whitespace-pre-wrap break-all">
            {formatRawCall(name, action, call)}
          </pre>
        )}
      </div>
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
