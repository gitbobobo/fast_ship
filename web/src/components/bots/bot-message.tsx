import { useState } from "react";
import {
  CalendarClock,
  CheckCircle2,
  ChevronRight,
  Info,
  Wrench,
  XCircle,
} from "lucide-react";
import type {
  BotCall,
  BotCallDecision,
  BotMessage,
  BotScheduleRunStatus,
} from "@/lib/api/bots";
import { botScheduleRunStatusLabel } from "@/lib/api/bots";
import { scheduleResultLine } from "@/lib/bots/schedule-result-summary";
import { bidiSafeText } from "@/lib/bots/tool-labels";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";
import { BotConfirmationCard } from "./bot-confirmation-card";
import { BotMarkdown } from "./bot-markdown";
import {
  BotToolCard,
  callStatusLabel,
  STATUS_BADGE,
} from "./bot-tool-card";
import { toolActionLabel } from "@/lib/bots/tool-labels";

const DECISION_LABELS: Record<string, string> = {
  approve: "已批准",
  reject: "已拒绝",
  always_allow: "已设为始终允许",
};

/**
 * 定时任务运行事件（INT-157）：一行居中「定时任务「name」已完成」，
 * 不再把整段汇报正文糊在时间线上——有汇报/错误时行可点，弹窗里
 * 用 markdown 渲染详情；无详情（静默/跳过）就是普通事件行。
 * task_name/summary/error 是模型可写数据，渲染前剥双向/控制字符并隔离。
 */
function ScheduleEventView({ message }: { message: BotMessage }) {
  const [open, setOpen] = useState(false);
  const failed = message.content.status === "failed";
  const statusLabel =
    botScheduleRunStatusLabel(message.content.status as BotScheduleRunStatus) ??
    message.content.status ??
    "";
  const taskName =
    message.content.task_name || message.content.task_id || "未命名任务";
  const summary = message.content.summary ?? "";
  const errorCode = message.content.error_code;
  const errorMessage = message.content.error_message;
  // 详情口径与弹窗错误块保持一致：仅 error_code 的失败事件也要可点，
  // 否则错误码无处可看；全空白字段不算详情（点开空弹窗没有意义）。
  const hasDetail = Boolean(
    summary.trim() || errorMessage?.trim() || errorCode?.trim(),
  );
  const line = (
    <>
      定时任务「
      <span className="[unicode-bidi:isolate]">{bidiSafeText(taskName)}</span>
      」{statusLabel}
    </>
  );
  return (
    <p
      className={cn(
        "text-center text-xs text-muted-foreground",
        failed && "text-destructive",
      )}
    >
      {hasDetail ? (
        // 详情唯一入口：胶囊形可点区（高度/悬停底色/后缀箭头做足暗示），
        // 不可点的事件行保持朴素灰字，两种形态一眼可分。
        <button
          type="button"
          data-testid="bot-schedule-event-detail"
          className={cn(
            "inline-flex min-h-7 items-center gap-1.5 rounded-full px-3 py-1.5",
            "text-foreground/75 transition-colors hover:bg-accent hover:text-foreground",
            failed && "text-destructive hover:text-destructive",
          )}
          onClick={() => setOpen(true)}
        >
          <CalendarClock className="h-3.5 w-3.5 shrink-0" />
          {/* line 必须裹成单个 flex 子项：fragment 的多个文本节点在 flex
              容器里会各自吃 gap，任务名两侧被挤出空格（视觉复审）。 */}
          <span>{line}</span>
          <ChevronRight className="h-3.5 w-3.5 shrink-0" />
        </button>
      ) : (
        <span className="inline-flex items-center gap-1.5 px-3 py-1.5">
          <CalendarClock className="h-3.5 w-3.5 shrink-0" />
          <span>{line}</span>
        </span>
      )}
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{line}</DialogTitle>
          </DialogHeader>
          {(errorCode || errorMessage) && (
            // 主信息是面向用户的错误文案；机器可读错误码降级为次级小字，
            // 不抢主信息（排查时仍可见）。
            <div className="rounded-lg bg-destructive/10 px-3 py-2">
              {errorMessage && (
                <p className="text-sm text-destructive [unicode-bidi:isolate]">
                  {bidiSafeText(errorMessage)}
                </p>
              )}
              {errorCode && (
                <p className="mt-1 text-xs text-muted-foreground [unicode-bidi:isolate]">
                  错误码：{bidiSafeText(errorCode)}
                </p>
              )}
            </div>
          )}
          {summary && <BotMarkdown text={bidiSafeText(summary)} />}
        </DialogContent>
      </Dialog>
    </p>
  );
}

/**
 * 主线单条消息：用户右对齐、机器人左对齐；assistant 消息不再渲染
 * 过程旁白与已终态工具卡（INT-157）——待确认调用给确认卡（批准入口）、
 * 在途前台调用保留工具卡（逐项取消入口）。后台结果、批准决定与生命
 * 周期事件是平台投影出的结构性消息，用安静的样式与对话正文区分。
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
      user_note?: string;
    };
    if (message.content.event === "compaction_failed") {
      // 压缩失败（INT-115）：note 是服务端给的中文原因。不能只显示英文
      // 事件名——要告诉用户发生了什么、以及可以直接继续发新消息。
      // 远端错误文本可能落到 detail.message，渲染前剥双向/控制字符。
      const reason = detail.note || detail.message || "上下文压缩未成功";
      return (
        <p
          data-testid="bot-compaction-failed-note"
          className="mx-auto max-w-[85%] rounded-lg bg-muted/40 px-3 py-1.5 text-center text-xs text-muted-foreground"
        >
          上下文压缩失败：{bidiSafeText(reason)}。这条消息已停止自动重试，直接继续发新消息即可。
        </p>
      );
    }
    // note 写给模型（可带「（内部信息：…）」前缀与内部术语）；user_note 是
    // 面向用户的短句（INT-139 视觉复审）。旧消息没有 user_note 时把内部前缀
    // 剥掉兜底，其余照常渲染 note。服务侧系统提示用带图标与浅底色的
    // 通知条呈现，不与上下卡片粘连、也不被误读成某张卡片的脚注。
    const text =
      detail.user_note ??
      (detail.note || message.content.event || "").replace(/^（内部信息：[^）]*）/, "");
    return (
      <div className="my-1 flex justify-center">
        <p className="inline-flex items-center gap-1.5 rounded-lg bg-muted/40 px-3 py-1.5 text-xs text-muted-foreground [unicode-bidi:isolate]">
          <Info className="h-3.5 w-3.5 shrink-0" />
          {bidiSafeText(text)}
        </p>
      </div>
    );
  }

  if (message.kind === "approval_decision") {
    const decision = message.content.decision ?? "";
    // 批准之后，用户回看对话时最先要找的是「刚才那步同意了没有」：给它
    // 状态图标与颜色，别再是比「等你确认」更淡的一行灰字。
    const approved = decision === "approve" || decision === "always_allow";
    const rejected = decision === "reject";
    return (
      <p
        className={cn(
          "flex items-center justify-center gap-1.5 text-center text-xs",
          approved && "text-emerald-700 dark:text-emerald-400",
          rejected && "text-destructive",
          !approved && !rejected && "text-muted-foreground",
        )}
      >
        {approved && <CheckCircle2 className="h-3.5 w-3.5" />}
        {rejected && <XCircle className="h-3.5 w-3.5" />}
        {DECISION_LABELS[decision] ?? "已处理决定"}
        {message.content.call_id ? " · 调用已按决定继续" : ""}
      </p>
    );
  }

  if (message.kind === "background_result") {
    // 服务端把工具结果 JSON 截到 200 字符当 summary：直接当正文会断在
    // 字符串中间，也看不出成败。fs_schedule 优先用服务端受理快照拼人话
    // 主行；其余场景保持原兜底，但 JSON 形状的片段不直接当正文，一律收
    // 进折叠区完整展示。summary/error_message 是模型可写串，渲染前剥
    // 双向/控制字符并隔离。
    const call = calls.find((c) => c.id === message.content.call_id);
    const isScheduleCall = call?.tool_name === "fs_schedule";
    const status = message.content.status ?? call?.status ?? "";
    const succeeded = status === "succeeded";
    const failed = status === "failed";
    const humanLine =
      isScheduleCall && call ? scheduleResultLine(call, failed) : "";
    const summaryText = message.content.summary ?? "";
    const summaryLooksJson =
      summaryText.trimStart().startsWith("{") ||
      summaryText.trimStart().startsWith("[");
    // 能关联到的非 schedule 调用，summary 是模型写的自然语言汇报，保持
    // 原样当正文；找不到调用或 schedule 拼不出摘要时才触发 JSON 兜底。
    const hideRawSummary =
      Boolean(humanLine) || ((!call || isScheduleCall) && summaryLooksJson);
    const toolTitle =
      message.content.tool_name
        ? toolActionLabel(
            message.content.tool_name,
            message.content.action ?? "",
          )
        : "";
    return (
      <section className="space-y-1.5 rounded-xl border border-dashed bg-muted/20 px-3.5 py-2.5 text-sm">
        {/* 卡头与工具卡同构：中文工具名 + 状态徽标。内部 call_id 不外露——
            排查走服务端账本，截断的 ID 对用户只有噪音没有信息。 */}
        <div className="flex items-center gap-2">
          <p className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
            <Wrench className="h-3.5 w-3.5" />
            {toolTitle ? `${toolTitle} · 后台结果` : "平台后台结果"}
          </p>
          {status && (
            <span
              className={`ml-auto rounded-full px-2 py-0.5 text-xs font-medium ${STATUS_BADGE[status] ?? "bg-muted text-muted-foreground"}`}
            >
              {callStatusLabel(status)}
            </span>
          )}
        </div>
        {humanLine && (
          <div className="flex items-start gap-1.5">
            {succeeded && (
              <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
            )}
            {failed && (
              <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
            )}
            <p
              className={cn(
                "break-words [unicode-bidi:isolate]",
                failed && "text-destructive",
              )}
            >
              {bidiSafeText(humanLine)}
            </p>
          </div>
        )}
        {hideRawSummary && !humanLine && (
          <p className="text-xs text-muted-foreground">
            {failed
              ? "后台调用未成功（原始结果见下）"
              : "后台调用已完成（原始结果见下）"}
          </p>
        )}
        {!hideRawSummary && message.content.summary && (
          <p className="break-words whitespace-pre-wrap [unicode-bidi:isolate]">
            {bidiSafeText(message.content.summary)}
          </p>
        )}
        {hideRawSummary && message.content.summary && (
          <details
            data-testid="bot-background-raw-result"
            className="text-xs"
          >
            <summary className="cursor-pointer select-none text-muted-foreground hover:text-foreground">
              查看原始结果
            </summary>
            <pre className="mt-1.5 rounded-lg bg-background/80 p-2 font-mono whitespace-pre-wrap break-all [unicode-bidi:isolate]">
              {bidiSafeText(message.content.summary)}
            </pre>
          </details>
        )}
        {/* 错误文案短、可行动（坏 cron/时区等），直接外露而不是藏进折叠区。
            只有 failed 用强调红——cancelled 的「用户取消了调用」是中性事实，
            页面上琥珀色警示句才是唯一强调色（视觉复审）。 */}
        {message.content.error_message && (
          <p
            className={cn(
              "text-xs [unicode-bidi:isolate]",
              failed ? "text-destructive" : "text-muted-foreground",
            )}
          >
            {message.content.error_code
              ? `${bidiSafeText(message.content.error_code)}: `
              : ""}
            {bidiSafeText(message.content.error_message)}
          </p>
        )}
        {/* async 调用的「副作用可能已发生」警示只随结果消息下发——这是
            它唯一的渲染出口（工具卡对 async 终态不渲染，后台面板只收
            在途调用）。与工具卡同一句文案。 */}
        {message.content.effect_may_have_occurred && (
          <p className="text-xs text-amber-700 dark:text-amber-400">
            副作用可能已经发生；请先核实再决定后续操作。
          </p>
        )}
      </section>
    );
  }

  if (message.kind === "schedule_event") {
    return <ScheduleEventView message={message} />;
  }

  if (message.kind === "tool_reply") {
    // 能关联到工具卡的回执已由卡片吸收；关联不到的退化成协议回复提示行。
    return (
      <p className="text-xs text-muted-foreground [unicode-bidi:isolate]">
        {bidiSafeText(message.content.content || "工具已受理")}
      </p>
    );
  }

  // assistant：带 tool_calls 的中间回合整体隐藏（INT-157）——正文只是
  // 调用前的过程旁白，已终态的调用卡是执行噪音；保留三种可见性：
  // 待确认调用的确认卡（唯一批准入口）、在途前台（非 async）调用的
  // 工具卡（唯一逐项取消入口——async 在途调用由后台调用面板承担）、
  // 以及终态但「需要用户留意」的调用卡——取消写调用后副作用可能已
  // 发生、调用失败的原因，都只有工具卡这一个出口，不能一并降噪。
  // 流式草稿照旧直出气泡。
  const toolCalls = message.content.tool_calls ?? [];
  const findCall = (id: string) => calls.find((c) => c.id === id);
  const pendingConfirmations = toolCalls.flatMap((tc) => {
    const call = findCall(tc.call_id);
    return call?.status === "pending_confirmation" && call.requires_confirmation
      ? [call]
      : [];
  });
  const inflightCalls = toolCalls.flatMap((tc) => {
    const call = findCall(tc.call_id);
    return call &&
      call.execution_mode !== "async" &&
      (call.status === "queued" || call.status === "running")
      ? [call]
      : [];
  });
  const attentionCalls = toolCalls.flatMap((tc) => {
    const call = findCall(tc.call_id);
    return call &&
      call.execution_mode !== "async" &&
      call.status !== "pending_confirmation" &&
      call.status !== "queued" &&
      call.status !== "running" &&
      (call.effect_may_have_occurred || call.error_message || call.error_code)
      ? [call]
      : [];
  });
  const showText = draft || toolCalls.length === 0;
  if (
    !showText &&
    !pendingConfirmations.length &&
    !inflightCalls.length &&
    !attentionCalls.length
  ) {
    return null;
  }
  return (
    <div className="space-y-2">
      {showText && (text || draft) && (
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
      )}
      {pendingConfirmations.map((call) => (
        <BotConfirmationCard
          key={call.id}
          call={call}
          busy={busyCallId === call.id}
          error={callActionError}
          onDecide={onDecide}
        />
      ))}
      {[...inflightCalls, ...attentionCalls].map((call) => (
        <BotToolCard
          key={call.id}
          botId={botId}
          call={call}
          onCancel={onCancelCall}
          cancelling={busyCallId === call.id}
        />
      ))}
    </div>
  );
}
