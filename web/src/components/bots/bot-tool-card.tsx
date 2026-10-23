import { useState } from "react";
import { Link } from "react-router";
import { ChevronDown, ChevronUp, Wrench } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ApiRequestError } from "@/lib/api/client";
import { botApi, type BotCall } from "@/lib/api/bots";
import { cn } from "@/lib/utils";
import {
  bidiSafeText,
  toolActionLabel,
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

/** 状态徽标配色；后台结果消息复用同一套（bot-message.tsx）。 */
export const STATUS_BADGE: Record<string, string> = {
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
      {/* 所有 chip 统一最大宽度并 CSS 截断，完整值在 title（视觉审查） */}
      <span className="max-w-72 truncate font-mono [unicode-bidi:isolate]">
        {bidiSafeText(chip.value)}
      </span>
    </>
  );
  if (chip.href) {
    return (
      <Link
        to={chip.href}
        title={chip.title === undefined ? undefined : bidiSafeText(chip.title)}
        className="inline-flex items-center gap-1 rounded-md border bg-background/60 px-1.5 py-0.5 text-xs text-primary hover:border-primary/50 hover:underline"
      >
        {inner}
      </Link>
    );
  }
  return (
    <span
      title={chip.title === undefined ? undefined : bidiSafeText(chip.title)}
      className={cn(
        "inline-flex items-center gap-1 rounded-md border bg-background/60 px-1.5 py-0.5 text-xs",
        chip.tone === "warn" && "border-amber-500/50 text-amber-700 dark:text-amber-400",
      )}
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

/**
 * 工具调用卡：状态从调用账本派生。标题是中文标签，状态徽标贴在
 * 卡片右侧；参数摘要与完整 JSON 属于
 * 确认依据，只在写操作确认卡上呈现，这里不再重复。「查看完整结果」
 * 经 result 端点按需读取，展开状态留在卡片本地。
 */
export function BotToolCard({
  botId,
  call,
  onCancel,
  cancelling = false,
}: {
  botId: string;
  call?: BotCall;
  onCancel?: (callId: string) => void;
  cancelling?: boolean;
}) {
  const [showResult, setShowResult] = useState(false);
  const [fullResult, setFullResult] = useState<string | null>(null);
  const [resultError, setResultError] = useState<string | null>(null);

  const name = call?.tool_name ?? "";
  const action = call?.action ?? "";
  const title = name ? toolActionLabel(name, action) : "工具调用";
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
        {call && (
          <span
            data-testid="bot-call-status"
            className={`ml-auto rounded-full px-2 py-0.5 text-xs font-medium ${STATUS_BADGE[call.status] ?? "bg-muted text-muted-foreground"}`}
          >
            {callStatusLabel(call.status)}
          </span>
        )}
        {cancellable && (
          <Button
            type="button"
            size="xs"
            variant="outline"
            className="hover:border-destructive/50 hover:text-destructive"
            disabled={cancelling}
            onClick={() => onCancel!(call.id)}
          >
            取消
          </Button>
        )}
      </div>
      {call?.error_message && (
        // 错误码是排查用的内部标识，收进 title 而不是直接露出（视觉审查）；
        // message 内嵌的 [code] 前缀（AppError.Error 格式）同样剥掉。
        <p
          title={call.error_code ?? undefined}
          className="text-xs text-destructive [unicode-bidi:isolate]"
        >
          {bidiSafeText(
            call.error_code
              ? call.error_message.replace(/^\[\d+\]\s*/, "")
              : call.error_message,
          )}
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
              <pre className="max-h-60 overflow-auto rounded-lg bg-background p-2 font-mono text-xs [unicode-bidi:isolate]">
                {bidiSafeText(fullResult)}
              </pre>
            ) : (
              <p className="text-xs text-muted-foreground">结果读取中…</p>
            ))}
        </div>
      )}
    </section>
  );
}
