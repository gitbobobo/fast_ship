import { useState } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";
import type {
  BotCall,
  BotCallDecision,
  BotMessage,
  BotScheduleRunStatus,
} from "@/lib/api/bots";
import { botScheduleRunStatusLabel } from "@/lib/api/bots";
import { cn } from "@/lib/utils";
import { BotConfirmationCard } from "./bot-confirmation-card";
import { BotMarkdown } from "./bot-markdown";
import { BotToolCard } from "./bot-tool-card";

const DECISION_LABELS: Record<string, string> = {
  approve: "已批准",
  reject: "已拒绝",
  always_allow: "已设为始终允许",
};

/**
 * 中间回合的过程旁白：带 tool_calls 的 assistant 正文只是调用前的
 * 碎碎念，不是最终答复——折叠成一行「过程」入口默认收起，展开后用
 * 安静的 muted 小字呈现，与最终回复的气泡拉开层级。折叠状态只留在
 * 组件本地，不持久化。
 */
function ProcessNarration({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
      >
        {open ? (
          <ChevronUp className="h-3 w-3" />
        ) : (
          <ChevronDown className="h-3 w-3" />
        )}
        过程
      </button>
      {open && (
        <div className="mt-1 border-l-2 pl-2.5 text-xs text-muted-foreground">
          <BotMarkdown text={text} />
        </div>
      )}
    </div>
  );
}

/**
 * 主线单条消息：用户右对齐、机器人左对齐；assistant 消息内联渲染
 * 工具卡（待确认时替换为确认卡）。后台结果、批准决定与生命周期
 * 事件是平台投影出的结构性消息，用安静的样式与对话正文区分。
 */
export function BotMessageView({
  botId,
  message,
  draft = false,
  calls,
  busyCallId,
  callActionError,
  onDecide,
  onCancelCall,
}: {
  botId: string;
  message: BotMessage;
  draft?: boolean;
  calls: BotCall[];
  busyCallId: string | null;
  callActionError?: unknown;
  onDecide: (callId: string, decision: BotCallDecision) => void;
  onCancelCall: (callId: string) => void;
}) {
  const text = message.content.text;

  if (message.kind === "user") {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-2xl rounded-br-sm bg-primary px-3.5 py-2 text-sm text-primary-foreground break-words whitespace-pre-wrap">
          {text}
        </div>
      </div>
    );
  }

  if (message.kind === "lifecycle") {
    // detail 是服务端附加的任意 JSON；已知恢复通知带 note 字段。
    const detail = (message.content.detail ?? {}) as {
      note?: string;
      message?: string;
    };
    if (message.content.event === "compaction_failed") {
      // 压缩失败（INT-115）：note 是服务端给的中文原因。不能只显示英文
      // 事件名——要告诉用户发生了什么、以及可以直接继续发新消息。
      const reason = detail.note || detail.message || "上下文压缩未成功";
      return (
        <p
          data-testid="bot-compaction-failed-note"
          className="mx-auto max-w-[85%] rounded-lg bg-muted/40 px-3 py-1.5 text-center text-xs text-muted-foreground"
        >
          上下文压缩失败：{reason}。这条消息已停止自动重试，直接继续发新消息即可。
        </p>
      );
    }
    return (
      <p className="text-center text-xs text-muted-foreground">
        {detail.note || message.content.event || ""}
      </p>
    );
  }

  if (message.kind === "approval_decision") {
    const decision = message.content.decision ?? "";
    return (
      <p className="text-center text-xs text-muted-foreground">
        {DECISION_LABELS[decision] ?? "已处理决定"}
        {message.content.call_id ? " · 调用已按决定继续" : ""}
      </p>
    );
  }

  if (message.kind === "background_result") {
    return (
      <section className="space-y-1.5 rounded-xl border border-dashed bg-muted/20 px-3.5 py-2.5 text-sm">
        <p className="text-xs font-medium text-muted-foreground">
          平台后台结果
          {message.content.call_id ? ` · ${message.content.call_id.slice(0, 8)}` : ""}
        </p>
        {message.content.summary && (
          <p className="break-words whitespace-pre-wrap">{message.content.summary}</p>
        )}
        {message.content.error_message && (
          <p className="text-xs text-destructive">
            {message.content.error_code ? `${message.content.error_code}: ` : ""}
            {message.content.error_message}
          </p>
        )}
      </section>
    );
  }

  if (message.kind === "schedule_event") {
    // 定时任务运行事件是平台投影出的结构性消息：安静的居中事件行，
    // 不是机器人的回复气泡。状态文案与「定时任务」区同一套标签。
    const failed = message.content.status === "failed";
    const statusLabel =
      botScheduleRunStatusLabel(message.content.status as BotScheduleRunStatus) ??
      message.content.status ??
      "";
    return (
      <p
        className={cn(
          "text-center text-xs text-muted-foreground",
          failed && "text-destructive",
        )}
      >
        定时任务「
        {message.content.task_name || message.content.task_id || "未命名任务"}」
        {statusLabel}
        {message.content.summary ? `：${message.content.summary}` : ""}
        {message.content.error_message && (
          <span className="block">
            {message.content.error_code ? `${message.content.error_code}: ` : ""}
            {message.content.error_message}
          </span>
        )}
      </p>
    );
  }

  if (message.kind === "tool_reply") {
    // 能关联到工具卡的回执已由卡片吸收；关联不到的退化成协议回复提示行。
    return (
      <p className="text-xs text-muted-foreground">
        {message.content.content || "工具已受理"}
      </p>
    );
  }

  // assistant：带 tool_calls 的是中间回合，正文只是过程旁白，折叠成
  // 「过程」入口，把视觉重心留给最终回复；工具卡与确认卡是操作入口，
  // 必须在折叠之外始终可见。草稿（draft）保持流式气泡不折叠。
  const toolCalls = message.content.tool_calls ?? [];
  return (
    <div className="space-y-2">
      {!draft && toolCalls.length > 0 && text ? (
        <ProcessNarration text={text} />
      ) : (
        (text || draft) && (
          <div
            className={cn(
              "inline-block max-w-[85%] min-w-0 rounded-2xl rounded-bl-sm bg-muted px-3.5 py-2 text-sm",
              draft && "opacity-80",
            )}
          >
            <BotMarkdown text={text ?? ""} streaming={draft} />
            {draft && (
              <span className="ml-0.5 inline-block h-3.5 w-1.5 animate-pulse rounded-sm bg-foreground/60 align-middle" />
            )}
          </div>
        )
      )}
      {toolCalls.map((tc) => {
        const call = calls.find((c) => c.id === tc.call_id);
        if (call?.status === "pending_confirmation" && call.requires_confirmation) {
          return (
            <BotConfirmationCard
              key={tc.call_id}
              call={call}
              busy={busyCallId === call.id}
              error={callActionError}
              onDecide={onDecide}
            />
          );
        }
        return (
          <BotToolCard
            key={tc.call_id}
            botId={botId}
            call={call}
            fallbackName={tc.tool_name}
            fallbackAction={tc.action}
            onCancel={onCancelCall}
            cancelling={busyCallId === tc.call_id}
          />
        );
      })}
    </div>
  );
}
