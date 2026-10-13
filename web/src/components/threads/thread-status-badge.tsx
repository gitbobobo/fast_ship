import { cn } from "@/lib/utils";
import type { ThreadStatus } from "@/lib/api/threads";
import { THREAD_STATUS_LABELS } from "@/lib/threads";

const STATUS_STYLES: Record<ThreadStatus, { badge: string; dot: string }> = {
  running: {
    badge: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
    dot: "bg-emerald-500 animate-pulse motion-reduce:animate-none",
  },
  waiting: {
    badge: "bg-amber-500/15 text-amber-700 dark:text-amber-400",
    dot: "bg-amber-500",
  },
  idle: { badge: "bg-muted text-muted-foreground", dot: "bg-muted-foreground/50" },
  archived: {
    badge: "bg-muted text-muted-foreground/70",
    dot: "bg-muted-foreground/30",
  },
};

export function ThreadStatusBadge({
  status,
  className,
}: {
  status: ThreadStatus;
  className?: string;
}) {
  const style = STATUS_STYLES[status] ?? STATUS_STYLES.idle;
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap",
        style.badge,
        className,
      )}
    >
      <span aria-hidden className={cn("h-1.5 w-1.5 rounded-full", style.dot)} />
      {THREAD_STATUS_LABELS[status] ?? status}
    </span>
  );
}
