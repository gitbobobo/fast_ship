import { useState } from "react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { CalendarClock, ChevronRight, Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { ApiRequestError } from "@/lib/api/client";
import {
  botApi,
  botScheduleRunStatusLabel,
  type BotScheduleRun,
  type BotScheduleTask,
} from "@/lib/api/bots";
import {
  BOT_SCHEDULE_RUNS_PAGE,
  botKeys,
  useBotScheduleTaskRuns,
  useBotScheduleTasks,
  useClearBotScheduleTaskState,
  useSetBotScheduleTaskEnabled,
} from "@/lib/hooks/use-bots";
import { useAuthStore } from "@/lib/store/auth-store";
import { formatCostEstimate } from "./bot-metrics";
import { describeCronExpression, uiLocale } from "@/lib/cron-frequency";
import { cn } from "@/lib/utils";
import { formatDate } from "@/lib/utils/format";

// 定时任务（INT-103）：任务由机器人经工具创建/修改/删除，用户在这里只能
// 查看计划/指令/运行记录并启停——刻意没有新建、编辑、删除入口。

/** 描述不出来（表达式损坏等）时落回原表达式，不丢信息。 */
function scheduleText(task: BotScheduleTask) {
  return (
    describeCronExpression(task.cron_expression, task.timezone, uiLocale()) ??
    task.cron_expression
  );
}

function statusVariant(status: BotScheduleRun["status"]) {
  if (status === "failed") return "destructive" as const;
  if (status === "running") return "default" as const;
  return "secondary" as const;
}

/** 触发来源：cron 到点触发，manual 为机器人经 fs_schedule.run 手动触发。 */
function triggerText(run: BotScheduleRun) {
  return run.trigger_source === "manual" ? "手动触发" : "定时触发";
}

/** 最近一次结果的状态标签：从未运行如实标注。 */
function lastRunLabel(run: BotScheduleRun | null) {
  if (!run) return "尚未运行";
  return botScheduleRunStatusLabel(run.status);
}

/** 最近一次结果的摘要/错误明细：空白视为无，不产出悬空空段。 */
function lastRunDetail(run: BotScheduleRun | null): string | null {
  if (!run) return null;
  return (
    (run.status === "failed" ? run.error_message || run.summary : run.summary)
      ?.trim() || null
  );
}

function formatDuration(ms: number | null) {
  if (ms == null) return "—";
  const seconds = ms / 1000;
  if (seconds < 60) {
    return `${seconds < 10 ? seconds.toFixed(1) : Math.round(seconds)} 秒`;
  }
  const minutes = Math.floor(seconds / 60);
  const rest = Math.round(seconds % 60);
  return rest ? `${minutes} 分 ${rest} 秒` : `${minutes} 分钟`;
}

function runTime(run: BotScheduleRun) {
  const at = run.started_at ?? run.scheduled_for;
  return at ? formatDate(at) : "—";
}

/**
 * 停止一次定时任务运行（INT-137）。停止接口返回的是**主线 run 投影**（受理瞬间
 * 状态仍是 running，且没有 summary / task_id / scheduled_for / duration_ms），
 * 不能整行替换运行记录缓存（会把摘要抹掉、手动触发显示成定时触发）。
 * 只在返回已是终态时按 id 合并状态与时间，其余交给失效后的重取收敛；任务与
 * 运行记录两份缓存都失效，「最近一次结果」随之更新。
 */
function useStopScheduleRun(botId: string) {
  const queryClient = useQueryClient();
  const userId = useAuthStore((state) => state.user?.id ?? null);
  return useMutation({
    mutationFn: ({ taskId, runId }: { taskId: string; runId: string }) =>
      botApi.stopScheduleRun(botId, taskId, runId),
    onSuccess: (run, { taskId }) => {
      if (run.status !== "running") {
        queryClient.setQueryData<BotScheduleRun[]>(
          botKeys.scheduleTaskRuns(userId, botId, taskId),
          (previous) =>
            previous?.map((item) =>
              item.id === run.id
                ? {
                    ...item,
                    status: run.status,
                    finished_at: run.finished_at,
                  }
                : item,
            ),
        );
      }
      void queryClient.invalidateQueries({
        queryKey: botKeys.scheduleTasks(userId, botId),
      });
      void queryClient.invalidateQueries({
        queryKey: botKeys.scheduleTaskRuns(userId, botId, taskId),
      });
    },
  });
}

/**
 * 任务状态以原始 JSON 文本下发（服务端刻意保留数字字面量）。展示优先美化；
 * JSON.parse 会把 >2^53 的整数折成 double，无法无损往返时退回原文，避免界面
 * 显示与库里不一致的数值。
 */
function prettyStateText(raw: string): { text: string; rawFallback: boolean } {
  try {
    const parsed: unknown = JSON.parse(raw);
    if (JSON.stringify(parsed) === raw) {
      return { text: JSON.stringify(parsed, null, 2), rawFallback: false };
    }
  } catch {
    // 非合法 JSON 直接原文展示
  }
  return { text: raw, rawFallback: true };
}

/**
 * 详情右栏的「定时任务」区：默认展开列出任务（标题 + 数量，可再折叠）；
 * 点击行看指令与运行记录，右侧开关是唯一操作。
 * 展开态与详情选择由宿主持有：桌面 aside 与移动 Sheet 会各挂一份本组件，
 * 共享同一份状态、共用同一个 BotScheduleTaskDialog（弹窗只渲染一份）。
 */
export function BotScheduleSection({
  botId,
  expanded,
  onExpandedChange,
  onOpenDetail,
}: {
  botId: string;
  /** 区块展开态；宿主统一持有，多个挂载点共享同一份。 */
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
  /** 点击任务行请求打开详情弹窗；弹窗由宿主统一渲染。 */
  onOpenDetail: (taskId: string) => void;
}) {
  const { data: tasks, isLoading } = useBotScheduleTasks(botId);
  const toggle = useSetBotScheduleTaskEnabled(botId);

  const handleToggle = async (task: BotScheduleTask) => {
    try {
      await toggle.mutateAsync({ taskId: task.id, enabled: !task.enabled });
      toast.success(
        task.enabled ? `已停用「${task.name}」` : `已启用「${task.name}」`,
      );
    } catch (err) {
      toast.error(
        err instanceof ApiRequestError && err.message
          ? err.message
          : "操作失败",
      );
    }
  };

  return (
    <section data-testid="bot-schedule-section" className="w-full">
      <div className="rounded-xl border bg-card">
        <button
          type="button"
          aria-expanded={expanded}
          onClick={() => onExpandedChange(!expanded)}
          className="flex w-full items-center gap-1.5 px-3.5 py-2.5 text-left text-xs font-medium text-muted-foreground"
        >
          <ChevronRight
            className={cn(
              "h-3.5 w-3.5 transition-transform",
              expanded && "rotate-90",
            )}
          />
          <CalendarClock className="h-3.5 w-3.5" />
          定时任务{tasks ? ` · ${tasks.length} 个` : ""}
        </button>

        {expanded &&
          (isLoading ? (
            <div className="space-y-2 border-t px-3.5 py-3">
              <Skeleton className="h-10 rounded-lg" />
              <Skeleton className="h-10 rounded-lg" />
            </div>
          ) : !tasks?.length ? (
            <p className="border-t px-3.5 py-6 text-center text-sm text-muted-foreground">
              让机器人帮你创建
            </p>
          ) : (
            <ul className="divide-y border-t">
              {tasks.map((task) => (
                <li
                  key={task.id}
                  data-testid="bot-schedule-task"
                  className="flex items-center gap-3 px-3.5 py-2.5"
                >
                  <button
                    type="button"
                    className="min-w-0 flex-1 space-y-1 text-left"
                    onClick={() => onOpenDetail(task.id)}
                  >
                    <span className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium">
                        {task.name}
                      </span>
                      {!task.enabled && (
                        <Badge variant="secondary">已停用</Badge>
                      )}
                    </span>
                    <span className="block truncate text-xs text-muted-foreground">
                      {scheduleText(task)}
                    </span>
                    <span className="block truncate text-xs text-muted-foreground">
                      下次运行{" "}
                      {task.next_run_at ? formatDate(task.next_run_at) : "—"}
                      {" · "}
                      {lastRunLabel(task.last_run)}
                    </span>
                    {/* 最近结果摘要单独成行：窄卡上与「下次运行」同行会被省略号
                        截成「已完成 ·」悬空空分隔符的观感。 */}
                    {lastRunDetail(task.last_run) && (
                      <span
                        className="block truncate text-xs text-muted-foreground"
                        title={lastRunDetail(task.last_run) ?? undefined}
                      >
                        {lastRunDetail(task.last_run)}
                      </span>
                    )}
                  </button>
                  {/* 近 7 天花费入口（INT-162）：可空是「未计价」显示「—」；
                      跳总览页并定位到该任务行。不能放进上面的 button（交互嵌套）。 */}
                  <Link
                    to={`/bots/usage?task=${task.id}`}
                    className="shrink-0 text-xs whitespace-nowrap text-muted-foreground hover:text-foreground hover:underline"
                    title="该任务近 7 天花费；点击查看花费总览"
                  >
                    近 7 天{" "}
                    {task.cost_last_7d != null
                      ? formatCostEstimate(task.cost_last_7d)
                      : "—"}
                  </Link>
                  <EnabledSwitch
                    enabled={task.enabled}
                    pending={toggle.isPending}
                    label={`${task.enabled ? "停用" : "启用"} ${task.name}`}
                    onToggle={() => void handleToggle(task)}
                  />
                </li>
              ))}
            </ul>
          ))}
      </div>
    </section>
  );
}

/**
 * 仓库没有 ui/switch 组件：按钮模拟拨杆开关，role="switch" +
 * aria-checked 暴露给辅助技术与测试。
 */
function EnabledSwitch({
  enabled,
  pending,
  label,
  onToggle,
}: {
  enabled: boolean;
  pending: boolean;
  label: string;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={enabled}
      aria-label={label}
      disabled={pending}
      onClick={onToggle}
      className={cn(
        "relative inline-flex h-5 w-9 shrink-0 items-center rounded-full outline-none transition-colors",
        "focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50",
        enabled ? "bg-primary" : "bg-input dark:bg-input/60",
      )}
    >
      <span
        className={cn(
          "block h-4 w-4 rounded-full bg-background shadow-sm transition-transform",
          enabled ? "translate-x-[18px]" : "translate-x-0.5",
        )}
      />
    </button>
  );
}

/**
 * 详情弹窗：指令全文 + 最近一页运行记录（只读，无编辑入口）。
 * 宿主只挂一份（工作台）：弹窗自身按 taskId 从任务列表解析任务，
 * 启停后刷新即同步；任务被机器人删掉时自动关上。
 */
export function BotScheduleTaskDialog({
  botId,
  taskId,
  onClose,
}: {
  botId: string;
  taskId: string | null;
  onClose: () => void;
}) {
  const { data: tasks } = useBotScheduleTasks(botId);
  const task = tasks?.find((t) => t.id === taskId) ?? null;
  const { data: runs, isLoading } = useBotScheduleTaskRuns(
    botId,
    task?.id ?? null,
  );
  const clearState = useClearBotScheduleTaskState(botId);
  const stopRun = useStopScheduleRun(botId);
  const [stoppingRunId, setStoppingRunId] = useState<string | null>(null);
  const stateView = task?.state != null ? prettyStateText(task.state) : null;

  const handleClearState = async (taskId: string) => {
    try {
      await clearState.mutateAsync(taskId);
      toast.success("状态已清空");
    } catch (err) {
      toast.error(
        err instanceof ApiRequestError && err.message
          ? err.message
          : "清空失败",
      );
    }
  };

  const handleStopRun = async (run: BotScheduleRun) => {
    if (!task || stoppingRunId) return;
    setStoppingRunId(run.id);
    try {
      await stopRun.mutateAsync({ taskId: task.id, runId: run.id });
      // 受理只代表停止请求已落库：真正 stopped 由运行槽在安全点收尾，等重取
      // 收敛，不在这里谎报「已停止」。
      toast.success("已发出停止请求");
    } catch (err) {
      toast.error(
        err instanceof ApiRequestError && err.message
          ? err.message
          : "停止失败",
      );
    } finally {
      setStoppingRunId(null);
    }
  };

  return (
    <Dialog
      open={Boolean(task)}
      onOpenChange={(next) => !next && onClose()}
    >
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{task?.name ?? "定时任务"}</DialogTitle>
          {task && (
            <DialogDescription>
              {scheduleText(task)}
              {" · "}
              {task.model ? `模型 ${task.model}` : "模型跟随机器人"}
              {" · "}
              {task.needs_report ? "运行后回报" : "静默运行"}
            </DialogDescription>
          )}
        </DialogHeader>
        {task && (
          <div className="space-y-4">
            <div className="space-y-1.5">
              <h4 className="text-xs font-medium text-muted-foreground">
                任务指令
              </h4>
              <p className="whitespace-pre-wrap rounded-lg bg-muted/50 px-3 py-2 text-sm">
                {task.instruction || "—"}
              </p>
            </div>
            <div className="space-y-1.5">
              <div className="flex items-center gap-2">
                <h4 className="text-xs font-medium text-muted-foreground">
                  任务状态
                </h4>
                <Button
                  type="button"
                  size="xs"
                  variant="ghost"
                  className="ml-auto text-destructive"
                  disabled={task.state == null || clearState.isPending}
                  onClick={() => void handleClearState(task.id)}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                  清空状态
                </Button>
              </div>
              {task.state == null ? (
                <p className="py-4 text-center text-sm text-muted-foreground">
                  暂无状态
                </p>
              ) : (
                <div className="space-y-1">
                  <pre
                    data-testid="bot-schedule-task-state"
                    className="max-h-64 overflow-auto whitespace-pre-wrap rounded-lg bg-muted/50 px-3 py-2 font-mono text-xs"
                  >
                    {stateView?.text}
                  </pre>
                  <p className="text-xs text-muted-foreground">
                    {stateView?.rawFallback &&
                      "含大整数等，按原始 JSON 显示；"}
                    {task.state_updated_at &&
                      `更新于 ${formatDate(task.state_updated_at)}`}
                  </p>
                </div>
              )}
            </div>
            <div className="space-y-1.5">
              <h4 className="text-xs font-medium text-muted-foreground">
                最近运行
              </h4>
              {isLoading ? (
                <div className="space-y-2">
                  <Skeleton className="h-10 rounded-lg" />
                  <Skeleton className="h-10 rounded-lg" />
                </div>
              ) : !runs?.length ? (
                <p className="py-4 text-center text-sm text-muted-foreground">
                  还没有运行记录
                </p>
              ) : (
                <>
                  <ul className="divide-y rounded-lg border">
                    {runs.map((run) => (
                      <li
                        key={run.id}
                        data-testid="bot-schedule-run"
                        className="space-y-1 px-3 py-2"
                      >
                        <div className="flex items-center gap-2">
                          <Badge variant={statusVariant(run.status)}>
                            {botScheduleRunStatusLabel(run.status)}
                          </Badge>
                          <span className="text-xs text-muted-foreground">
                            {triggerText(run)}
                          </span>
                          <span className="text-xs text-muted-foreground">
                            {runTime(run)}
                          </span>
                          {/* INT-162：运行行始终可点进花费明细——未计价的运行
                              同样有状态/错误/请求可看，金额位显示「—」
                              （缺失 ≠ 0）；金额口径 display_cost_estimate
                              含关联维护已计价。 */}
                          <Link
                            to={`/bots/usage/runs/${run.id}`}
                            className="ml-auto text-xs text-muted-foreground hover:text-foreground hover:underline"
                            title={
                              run.display_cost_estimate != null
                                ? "成本实收优先、缺失按目录估算（含关联维护），仅供参考；无价模型不显示"
                                : "本运行未计价，点击查看花费明细"
                            }
                          >
                            {run.display_cost_estimate != null
                              ? `≈${formatCostEstimate(run.display_cost_estimate)}`
                              : "—"}
                          </Link>
                          <span className="text-xs text-muted-foreground">
                            {formatDuration(run.duration_ms)}
                          </span>
                          {run.status === "running" && (
                            // 行内停止按钮：放大到 h-8/px-3 的可点区域，与左侧
                            // 时长之间留出间距，避免误触（视觉审查）。
                            <Button
                              type="button"
                              size="sm"
                              variant="destructive"
                              className="ml-2 h-8 px-3"
                              aria-label="停止该次运行"
                              disabled={stoppingRunId === run.id}
                              onClick={() => void handleStopRun(run)}
                            >
                              {stoppingRunId === run.id && (
                                <Loader2 className="h-3.5 w-3.5 animate-spin" />
                              )}
                              停止
                            </Button>
                          )}
                        </div>
                        {(run.summary || run.error_message) && (
                          <p className="text-xs text-muted-foreground">
                            {run.error_message ?? run.summary}
                          </p>
                        )}
                      </li>
                    ))}
                  </ul>
                  {runs.length >= BOT_SCHEDULE_RUNS_PAGE && (
                    <p className="text-xs text-muted-foreground">
                      仅显示最近 {BOT_SCHEDULE_RUNS_PAGE} 条
                    </p>
                  )}
                </>
              )}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
