import { Archive, Clock, LoaderCircle, type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import type { ThreadStatus } from "@/lib/api/threads";
import { formatRunMinutes, runMinutes } from "@/lib/threads";
import { useNow } from "@/lib/hooks/use-now";

const STATUS_TEXT: Record<ThreadStatus, string> = {
  running: "运行中",
  waiting: "等待",
  idle: "空闲",
  archived: "已归档",
};

const STATUS_STYLE: Record<
  ThreadStatus,
  { icon?: LucideIcon; className: string }
> = {
  running: {
    icon: LoaderCircle,
    className: "text-sky-600 dark:text-sky-400",
  },
  waiting: { icon: Clock, className: "text-muted-foreground" },
  idle: { className: "text-muted-foreground" },
  archived: { icon: Archive, className: "text-muted-foreground/70" },
};

/**
 * T3 风格的纯文字 + 小图标状态。运行中带计时（起点缺失或无法解析时只显示「运行中」）。
 */
export function ThreadStatusBadge({
  status,
  runStartedAt,
  className,
}: {
  status: ThreadStatus;
  runStartedAt?: string;
  className?: string;
}) {
  const now = useNow(30_000, status === "running" && Boolean(runStartedAt));
  const style = STATUS_STYLE[status] ?? STATUS_STYLE.idle;
  const Icon = style.icon;
  const minutes = status === "running" ? runMinutes(runStartedAt, now) : undefined;
  const text = STATUS_TEXT[status] ?? status;

  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-1 text-xs font-medium whitespace-nowrap",
        style.className,
        className,
      )}
    >
      {Icon && (
        <Icon
          aria-hidden
          className={cn(
            "size-3.5",
            status === "running" && "animate-spin motion-reduce:animate-none",
          )}
        />
      )}
      {minutes === undefined ? text : `${text} · ${formatRunMinutes(minutes)}`}
    </span>
  );
}
