import { useRef, useState } from "react";
import { Loader2, RefreshCw, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { useScrollFollow } from "@/lib/hooks/use-scroll-follow";
import { useBotChat } from "@/lib/hooks/use-bot-chat";
import { ApiRequestError } from "@/lib/api/client";
import { cn } from "@/lib/utils";
import { BotBackgroundCalls } from "./bot-background-calls";
import { BotComposer } from "./bot-composer";
import { BotMessageView } from "./bot-message";
import { BotMetrics } from "./bot-metrics";
import { botActionErrorText } from "./bot-tool-card";

function loadErrorText(error: unknown) {
  if (error instanceof ApiRequestError) {
    if (error.status === 404) return "机器人不存在或已被删除。";
    return error.message || "主线加载失败，请稍后重试。";
  }
  return "主线加载失败，请稍后重试。";
}

/**
 * 机器人唯一主线：没有会话列表、没有新建会话。运行状态、流式回答、
 * 工具卡、确认卡与后台调用都在这一条时间线上；「停止主线」在运行栏，
 * 「取消后台调用」在各自卡片上，两个动作互不影响。
 */
export function BotChat({ botId }: { botId: string }) {
  const chat = useBotChat(botId);
  const [text, setText] = useState("");
  const anchorRef = useRef<HTMLDivElement>(null);
  const last = chat.entries[chat.entries.length - 1];
  useScrollFollow(
    anchorRef,
    `${chat.entries.length}|${last?.message.id ?? ""}|${last?.message.content.text?.length ?? 0}`,
    !chat.loading,
  );

  const submit = async () => {
    const value = text.trim();
    if (!value || chat.sending) return;
    setText("");
    const ok = await chat.sendMessage(value);
    // 发送失败把草稿还回来，避免用户输入丢失（sendError 是渲染期快照，
    // 不能作为恢复依据）。
    if (!ok) setText(value);
  };

  if (chat.loading) {
    return (
      <div className="mx-auto w-full max-w-3xl space-y-3 px-4 py-6 md:px-6">
        <Skeleton className="h-6 w-1/3" />
        <Skeleton className="h-24 rounded-xl" />
        <Skeleton className="h-16 rounded-xl" />
      </div>
    );
  }

  if (chat.loadError) {
    return (
      <div className="mx-auto w-full max-w-3xl space-y-3 px-4 py-16 text-center text-sm text-muted-foreground md:px-6">
        <p>{loadErrorText(chat.loadError)}</p>
        <Button variant="outline" size="sm" onClick={chat.retry}>
          重试
        </Button>
      </div>
    );
  }

  const stopLabel = chat.stopRequested ? "停止中…" : "请求停止";
  const runBadge = chat.stopRequested
    ? "停止中"
    : chat.activeRun?.status === "queued"
      ? "排队中"
      : "运行中";
  // 进度只属于它自己的 run：旧 run 的事件/快照不得显示在当前 run 的运行栏。
  const activeProgress =
    chat.maintenanceProgress &&
    chat.activeRun &&
    chat.maintenanceProgress.runId === chat.activeRun.id
      ? chat.maintenanceProgress
      : null;

  return (
    <div
      ref={anchorRef}
      className="mx-auto flex w-full max-w-3xl min-w-0 flex-1 flex-col gap-4 px-4 py-4 md:px-6"
    >
      {chat.runActive && (
        <div
          data-testid="bot-run-bar"
          className="flex items-center gap-2 rounded-lg bg-muted/50 px-3 py-2 text-sm"
        >
          <Badge variant="secondary" data-testid="bot-run-status">
            {runBadge}
          </Badge>
          {chat.stopRequested ? (
            <span className="text-xs text-muted-foreground">
              已请求停止，等待当前步骤结束
            </span>
          ) : activeProgress ? (
            // 压缩阶段/失败原因（INT-115）：phase=failed 用醒目但非报错的
            // 样式——压缩会继续在安全点重试或最终给出明确失败，不是致命错误。
            <span
              data-testid="bot-maintenance-progress"
              className={cn(
                "text-xs",
                activeProgress.phase === "failed"
                  ? "font-medium text-amber-600 dark:text-amber-400"
                  : "text-muted-foreground",
              )}
            >
              {activeProgress.message ||
                (activeProgress.phase === "failed"
                  ? "上下文压缩遇到问题"
                  : "正在整理上下文…")}
            </span>
          ) : (
            <span className="text-xs text-muted-foreground">
              新消息将在安全点交给机器人
            </span>
          )}
          {chat.activeRun?.usage_complete && chat.activeRun.total_tokens != null && (
            <span className="ml-auto text-xs text-muted-foreground">
              {chat.activeRun.total_tokens} tokens
            </span>
          )}
          <Button
            type="button"
            size="xs"
            variant="destructive"
            className="ml-auto"
            disabled={chat.stopping || chat.stopRequested}
            aria-label={stopLabel}
            title="向机器人发送一条「停止」消息，由它结合上下文决定是否结束运行"
            onClick={() => void chat.stopRun()}
          >
            {chat.stopping && <Loader2 className="mr-1 h-3 w-3 animate-spin" />}
            <Square className="mr-1 h-3 w-3 fill-current" />
            请求停止
          </Button>
        </div>
      )}

      {chat.hasMoreHistory && chat.entries.length > 0 && (
        <div className="text-center">
          <Button
            type="button"
            size="xs"
            variant="ghost"
            className="text-muted-foreground"
            disabled={chat.loadingOlder}
            onClick={() => void chat.loadOlder()}
          >
            {chat.loadingOlder ? "加载中…" : "加载更早消息"}
          </Button>
          {chat.olderError != null && (
            <p role="alert" className="text-xs text-destructive">
              {botActionErrorText(chat.olderError, "更早消息加载失败")}
            </p>
          )}
        </div>
      )}

      {chat.entries.length === 0 ? (
        <p className="py-12 text-center text-sm text-muted-foreground">
          和机器人打个招呼吧——它能查项目、改 Issue、开 T3 线程。
        </p>
      ) : (
        <div className="space-y-3">
          {chat.entries.map((entry) => (
            <BotMessageView
              key={`${entry.message.id}:${entry.draft ? "draft" : "msg"}`}
              botId={botId}
              message={entry.message}
              draft={entry.draft}
              calls={chat.calls}
              busyCallId={chat.busyCallId}
              callActionError={chat.callActionError}
              onDecide={(callId, decision) =>
                void chat.decide(callId, decision)
              }
              onCancelCall={(callId) => void chat.cancelCall(callId)}
            />
          ))}
        </div>
      )}

      {chat.streamStatus === "reconnecting" && (
        <p className="flex items-center justify-center gap-1.5 text-xs text-muted-foreground">
          <RefreshCw className="h-3 w-3 animate-spin" />
          实时连接中断，正在按序号补读…
        </p>
      )}
      {chat.streamStatus === "error" && (
        <p className="flex items-center justify-center gap-2 text-xs text-muted-foreground">
          实时连接已断开{chat.streamError ? `（${chat.streamError}）` : ""}
          <Button
            type="button"
            size="xs"
            variant="outline"
            onClick={chat.resumeStream}
          >
            重连
          </Button>
        </p>
      )}

      <BotBackgroundCalls
        botId={botId}
        calls={chat.backgroundCalls}
        busyCallId={chat.busyCallId}
        actionError={chat.callActionError}
        onCancel={(callId) => void chat.cancelCall(callId)}
      />

      <div className="sticky bottom-0 -mx-1 bg-background/95 px-1 pt-2 pb-3 backdrop-blur">
        <BotComposer
          text={text}
          onTextChange={setText}
          onSubmit={() => void submit()}
          submitting={chat.sending}
          actions={<BotMetrics metrics={chat.metrics} />}
          banner={
            chat.runActive && !chat.stopRequested ? (
              <p className="rounded-lg bg-primary/5 px-3 py-1.5 text-xs text-muted-foreground">
                机器人正在处理中——现在发送的消息将在安全点交给机器人
              </p>
            ) : undefined
          }
          error={
            chat.sendError != null
              ? botActionErrorText(chat.sendError, "发送失败")
              : chat.stopError != null
                ? botActionErrorText(chat.stopError, "停止失败")
                : undefined
          }
        />
      </div>
    </div>
  );
}
