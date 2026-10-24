import { useLayoutEffect, useRef, useState } from "react";
import { Link } from "react-router";
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
import { BotMetrics, formatCostEstimate } from "./bot-metrics";
import { BotTypingIndicator } from "./bot-typing-indicator";
import { botActionErrorText } from "./bot-tool-card";

function loadErrorText(error: unknown) {
  if (error instanceof ApiRequestError) {
    if (error.status === 404) return "机器人不存在或已被删除。";
    return error.message || "主线加载失败，请稍后重试。";
  }
  return "主线加载失败，请稍后重试。";
}

/** 运行栏徽标的终态文案；表外状态兜底由调用方处理。 */
const TERMINAL_RUN_BADGE: Record<string, string> = {
  completed: "已完成",
  failed: "已失败",
  stopped: "已停止",
  skipped: "已跳过",
};

/**
 * 机器人唯一主线：没有会话列表、没有新建会话。运行状态、流式回答、
 * 工具卡、确认卡与后台调用都在这一条时间线上；「停止主线」在运行栏，
 * 「取消后台调用」在各自卡片上，两个动作互不影响。
 */
export function BotChat({ botId }: { botId: string }) {
  const chat = useBotChat(botId);
  const [text, setText] = useState("");
  const anchorRef = useRef<HTMLDivElement>(null);
  const composerRef = useRef<HTMLDivElement>(null);
  // 浮层实高，写入等高 spacer：滚到底时把最后一条消息顶出浮层遮挡区。
  const [composerHeight, setComposerHeight] = useState(0);
  const last = chat.entries[chat.entries.length - 1];
  // version 里带上浮层实高：等高 spacer 在首次量到之前是不存在的，若只按消息内容
  // 判定，scroll-follow 会在 spacer 长出来之前完成贴底，之后不再跟——首屏就停在
  // 「最后一条消息被浮层压住」的位置（距离超过阈值还会被当成用户上翻）。高度变化
  // 时重跑一次 follow，次序上早于滚动事件，atBottom 仍成立。
  const scrollToBottom = useScrollFollow(
    anchorRef,
    `${chat.entries.length}|${last?.message.id ?? ""}|${last?.message.content.text?.length ?? 0}|${composerHeight}`,
    !chat.loading,
  );
  // 浮层容器本身 h-0，真正有高度的是内部绝对定位层，所以观察它。
  // 首帧常处于 loading/error（浮层尚未挂载），结束后重新挂载时要补测一次。
  useLayoutEffect(() => {
    const el = composerRef.current;
    if (!el) return;
    const measure = () => setComposerHeight(el.offsetHeight);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [chat.loading, chat.loadError]);

  const submit = async () => {
    const value = text.trim();
    if (!value || chat.sending) return;
    setText("");
    // 发送即贴底：用户可能正在上翻读历史，新消息与随之开始的回复都要落在
    // 视线内；随后条目增高仍由 scroll-follow 继续跟随。
    scrollToBottom();
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

  // 已请求停止后再点是「强制停止」：第一次只是追加停止消息等安全点，
  // 服务端宽限后才硬取消；卡住时用户要能立即中断，按钮必须保持可点。
  const stopLabel = chat.stopRequested ? "强制停止" : "请求停止";
  const stopTitle = chat.stopRequested
    ? "立即中断当前模型请求"
    : "向机器人发送一条「停止」消息，由它结合上下文决定是否结束运行";
  // 运行栏在 run 终态后继续展示最近一条（INT-162）：徽标落终态文案，
  // 运行中状态、进度与停止按钮只对未终态 run 渲染。
  const runBadge = chat.runActive
    ? chat.stopRequested
      ? "停止中"
      : chat.activeRun?.status === "queued"
        ? "排队中"
        : "运行中"
    : TERMINAL_RUN_BADGE[chat.activeRun?.status ?? ""] ?? "已结束";
  // 进度只属于它自己的 run：旧 run 的事件/快照不得显示在当前 run 的运行栏。
  const activeProgress =
    chat.runActive &&
    chat.maintenanceProgress &&
    chat.activeRun &&
    chat.maintenanceProgress.runId === chat.activeRun.id
      ? chat.maintenanceProgress
      : null;

  return (
    // 滚动容器由工作台中栏提供（useScrollFollow 找最近的可滚动祖先）；
    // min-h-full + shrink-0 保证消息少时栏内仍有富余高度（浮层才能贴栏底），
    // 且盒子高度始终覆盖全部内容（useScrollFollow 监听的正是这个盒子）。
    <div
      ref={anchorRef}
      className="mx-auto flex min-h-full w-full max-w-3xl min-w-0 shrink-0 flex-col px-4 pt-4 md:px-6"
    >
      {/* 输入框之外的全部内容：flex-1 保证消息少时它撑满栏高、浮层贴底。 */}
      <div className="flex flex-1 flex-col gap-4">
        {/* 可操作的 run 失败提示（INT-137）：仅 auth/quota，其它失败不新增噪音。 */}
        {chat.runError && (
          <div
            role="alert"
            data-testid="bot-run-error"
            className="flex items-center gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            <span className="min-w-0 flex-1">{chat.runError.message}</span>
            {chat.runError.action.kind === "ai_providers_settings" && (
              <Button
                size="xs"
                variant="outline"
                render={<Link to="/settings/ai-providers" />}
              >
                {chat.runError.action.label || "去设置"}
              </Button>
            )}
          </div>
        )}

        {/* 运行栏（INT-162）：run 到终态后仍展示最近一条的「本轮」成本与
            明细链接，直到新 run 开始或离开页面；未终态时才可停止。 */}
        {chat.activeRun != null && (
          <div
            data-testid="bot-run-bar"
            className="flex items-center gap-2 rounded-lg bg-muted/50 px-3 py-2 text-sm"
          >
            <Badge variant="secondary" data-testid="bot-run-status">
              {runBadge}
            </Badge>
            {chat.runActive &&
              (chat.stopRequested ? (
                // 说明「再点会发生什么」：强制停止会丢弃未完成的回复，不能只靠
                // 文字变化表达升级（视觉审查：两个状态样式一致，用户看不出）。
                <span className="min-w-0 truncate text-xs text-muted-foreground">
                  已请求停止；再点「强制停止」将立即中断并丢弃未完成的回复
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
                <span className="min-w-0 truncate text-xs text-muted-foreground">
                  新消息将在安全点交给机器人
                </span>
              ))}
            {(() => {
              const r = chat.activeRun;
              if (r == null) return null;
              // 成本用 display_cost_estimate（自身 + 关联维护已计价，INT-162）：
              // 全部请求为已计价失败的 run 没有 usage_complete 也照常显示；
              // 可空字段是「未计价」显示「—」，不当 0。点击进运行花费明细。
              const tokens =
                r.usage_complete && r.total_tokens != null
                  ? `${r.total_tokens} tokens`
                  : null;
              const cost =
                r.display_cost_estimate != null
                  ? `本轮 ≈${formatCostEstimate(r.display_cost_estimate)}`
                  : "本轮 —";
              return (
                // 成本链接与停止按钮同属右端：ml-auto 只在容器上，两个元素
                // 各占一半剩余空间是错的（视觉审查）。
                <div className="ml-auto flex items-center gap-2">
                  <Link
                    to={`/bots/usage/runs/${r.id}`}
                    className="text-xs text-muted-foreground hover:text-foreground hover:underline"
                    title={
                      r.display_cost_estimate != null
                        ? "成本按内置目录单价估算，仅供参考；无价模型不显示。点击查看本运行花费明细"
                        : "本运行未计价，点击查看明细"
                    }
                  >
                    {tokens == null ? cost : `${tokens} · ${cost}`}
                  </Link>
                  {chat.runActive && (
                    <Button
                      type="button"
                      size="sm"
                      variant="destructive"
                      // 强制停止比普通停止更具破坏性（丢弃草稿、立即终止）：升级后
                      // 用实心红底白字拉开层级，点击区域也放大到可点范围。
                      className={cn(
                        "h-8 px-3",
                        chat.stopRequested &&
                          "bg-destructive text-white hover:bg-destructive/90 focus-visible:ring-destructive/30",
                      )}
                      disabled={chat.stopping}
                      aria-label={stopLabel}
                      title={stopTitle}
                      onClick={() => void chat.stopRun()}
                    >
                      {chat.stopping && (
                        <Loader2 className="mr-1 h-3.5 w-3.5 animate-spin" />
                      )}
                      <Square className="mr-1 h-3.5 w-3.5 fill-current" />
                      {stopLabel}
                    </Button>
                  )}
                </div>
              );
            })()}
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

        {/* 回复中的等待指示贴在内容最底部（原来是输入框里的一行提示文字）。
            放在后台调用面板之后、等高 spacer 之前：贴底后它一定落在可见区，
            不会被上方的后台调用面板顶出视口。 */}
        {/* 已请求停止时不再显示正在打字的动画：状态区已经在说停止中，
            动画会让消息区看起来仍在正常生成（视觉审查）。 */}
        {chat.runActive && !chat.stopRequested && <BotTypingIndicator />}
      </div>

      {/* 等高滚动余量：高度等于浮层实高（上方 ResizeObserver 实测）。滚到底
          时它把最后一条消息顶出浮层遮挡区；滚动途中则留出空间，让消息从浮层
          （半透明 + 模糊背景）下方穿过，而不是被硬截断。 */}
      <div aria-hidden className="shrink-0" style={{ height: composerHeight }} />

      {/* 输入框浮层：外层 h-0 只提供 sticky 贴底定位，不占内容高度；真正有
          高度的是内部绝对定位层。空白区 pointer-events-none，不挡下方消息的
          选择等交互，BotComposer 外再包 pointer-events-auto 恢复可点。 */}
      <div className="sticky bottom-0 z-10 h-0">
        <div
          ref={composerRef}
          className="pointer-events-none absolute inset-x-0 bottom-0 -mx-1 bg-background/95 px-1 pt-2 pb-3 backdrop-blur"
        >
          <div className="pointer-events-auto">
            <BotComposer
              text={text}
              onTextChange={setText}
              onSubmit={() => void submit()}
              submitting={chat.sending}
              actions={<BotMetrics metrics={chat.metrics} />}
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
      </div>
    </div>
  );
}
