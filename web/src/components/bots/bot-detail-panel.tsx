import { KeyRound, Pencil, Trash2, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import type { Bot } from "@/lib/api/bots";
import { formatDate } from "@/lib/utils/format";
import { BotAvatar } from "./bot-avatar";
import { CONTEXT_SOURCE_LABELS } from "./bot-metrics";
import { BotScheduleSection } from "./bot-schedule-section";

/**
 * 定时任务区的共享状态：桌面 aside 与移动 Sheet 各挂一份面板，
 * 展开态与详情选择由工作台统一持有，运行记录弹窗也只渲染一份。
 */
export interface BotDetailPanelSchedule {
  expanded: boolean;
  onExpandedChange: (expanded: boolean) => void;
  onOpenDetail: (taskId: string) => void;
}

/**
 * 工作台右栏：机器人详情（纯展示）。头部是身份与「编辑」「授权管理」
 * 入口，内容区为定时任务（只读 + 启停）与配置摘要，底部是删除入口。
 * 编辑/授权/删除的弹窗与 mutation 都在工作台持有一份；onHide 在两个
 * 场景分别对应「隐藏右栏」与「关闭 Sheet」，由宿主传入。
 */
export function BotDetailPanel({
  bot,
  schedule,
  onEdit,
  onAuthorize,
  onDelete,
  onHide,
}: {
  bot: Bot;
  schedule: BotDetailPanelSchedule;
  onEdit: () => void;
  onAuthorize: () => void;
  onDelete: () => void;
  onHide: () => void;
}) {
  // 窗口缺失或为 0 一律「未知」；回退值只标来源，不声称已检测。
  const contextWindow =
    bot.context_window != null && bot.context_window > 0
      ? `${bot.context_window.toLocaleString("en-US")}${
          bot.context_source
            ? `（${CONTEXT_SOURCE_LABELS[bot.context_source] ?? bot.context_source}）`
            : ""
        }`
      : "未知";

  const configRows: { label: string; value: string; title?: string }[] = [
    { label: "模型", value: bot.model || "—", title: bot.model },
    { label: "API 地址", value: bot.base_url || "—", title: bot.base_url },
    {
      label: "API Key",
      value: bot.api_key_last4 ? `···${bot.api_key_last4}` : "未配置",
    },
    { label: "上下文窗口", value: contextWindow },
    { label: "创建时间", value: formatDate(bot.created_at) },
  ];

  return (
    <div data-testid="bot-detail-panel" className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="flex flex-col gap-5 px-4 py-4">
          <div className="flex items-start gap-3">
            <BotAvatar name={bot.name} size="lg" />
            <div className="min-w-0 flex-1 pt-1">
              <p className="truncate text-base font-semibold">{bot.name}</p>
              <p className="truncate text-xs text-muted-foreground">
                {bot.model}
              </p>
            </div>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="隐藏详情"
              onClick={onHide}
            >
              <X className="h-4 w-4" />
            </Button>
          </div>

          <div className="flex gap-2">
            <Button
              variant="outline"
              size="sm"
              className="flex-1"
              onClick={onEdit}
            >
              <Pencil className="mr-1.5 h-3.5 w-3.5" />
              编辑
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="flex-1"
              onClick={onAuthorize}
            >
              <KeyRound className="mr-1.5 h-3.5 w-3.5" />
              授权管理
            </Button>
          </div>

          <BotScheduleSection botId={bot.id} {...schedule} />

          <section className="space-y-2">
            <h3 className="text-xs font-medium text-muted-foreground">配置</h3>
            <dl className="space-y-1.5">
              {configRows.map((row) => (
                <div
                  key={row.label}
                  className="flex items-baseline justify-between gap-3 text-sm"
                >
                  <dt className="shrink-0 text-muted-foreground">{row.label}</dt>
                  <dd className="truncate" title={row.title ?? row.value}>
                    {row.value}
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        </div>
      </div>

      <div className="shrink-0 border-t px-4 py-3">
        <Button
          variant="destructive"
          size="sm"
          className="w-full"
          onClick={onDelete}
        >
          <Trash2 className="mr-1.5 h-3.5 w-3.5" />
          删除机器人
        </Button>
      </div>
    </div>
  );
}

/**
 * 详情加载中的占位：与真实面板同宽同骨架。切换机器人时右栏保持挂载，
 * 中栏宽度不因详情数据未到位而跳动。
 */
export function BotDetailPanelSkeleton() {
  return (
    <div
      data-testid="bot-detail-skeleton"
      className="flex h-full min-h-0 flex-col"
    >
      <div className="flex flex-col gap-5 px-4 py-4">
        <div className="flex items-center gap-3">
          <Skeleton className="size-16 rounded-full" />
          <div className="min-w-0 flex-1 space-y-2 pt-1">
            <Skeleton className="h-4 w-2/3" />
            <Skeleton className="h-3 w-1/2" />
          </div>
        </div>
        <div className="flex gap-2">
          <Skeleton className="h-7 flex-1" />
          <Skeleton className="h-7 flex-1" />
        </div>
        <Skeleton className="h-28 rounded-xl" />
        <Skeleton className="h-24 rounded-xl" />
      </div>
    </div>
  );
}
