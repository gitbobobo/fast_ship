import { useNow } from "@/lib/hooks/use-now";
import {
  formatQuotaAmount,
  formatResetCountdown,
  quotaUsageTone,
  type QuotaUsageTone,
} from "@/lib/quota";
import { cn } from "@/lib/utils";

const BAR_CLASS: Record<QuotaUsageTone, string> = {
  normal: "bg-primary",
  warning: "bg-amber-500 dark:bg-amber-400",
  danger: "bg-destructive",
};

const TEXT_CLASS: Record<QuotaUsageTone, string> = {
  normal: "text-foreground",
  warning: "text-amber-700 dark:text-amber-400",
  danger: "text-destructive",
};

/**
 * 单个额度窗口：窗口名 + 已用百分比 + 进度条（>=80 警告、>=95 危险）+
 * used/limit 明细 + reset_at 倒计时（每 30s 自走）。
 */
export function QuotaWindowProgress({ window: w }: { window: QuotaWindow }) {
  const now = useNow(30_000, Boolean(w.reset_at));
  const tone = quotaUsageTone(w.used_percent);
  const percent = w.used_percent;
  const width = percent == null ? 0 : Math.min(100, Math.max(0, percent));
  const usage =
    w.used != null && w.limit != null
      ? `${formatQuotaAmount(w.used, w.unit)} / ${formatQuotaAmount(w.limit, w.unit)}`
      : w.limit != null
        ? `限额 ${formatQuotaAmount(w.limit, w.unit)}`
        : null;

  return (
    <div className="space-y-1" data-testid="quota-window" data-tone={tone}>
      <div className="flex items-baseline justify-between gap-2 text-xs">
        <span className="text-muted-foreground">{w.label}</span>
        <span
          data-testid="quota-window-percent"
          className={cn("font-medium tabular-nums", TEXT_CLASS[tone])}
        >
          {percent == null ? "—" : formatQuotaAmount(percent, "percent")}
        </span>
      </div>
      <div
        role="progressbar"
        aria-label={w.label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent ?? 0}
        className="h-1.5 w-full overflow-hidden rounded-full bg-muted"
      >
        <div
          data-testid="quota-window-bar"
          className={cn("h-full rounded-full transition-[width]", BAR_CLASS[tone])}
          style={{ width: `${width}%` }}
        />
      </div>
      {(usage || w.reset_at) && (
        <div className="flex items-baseline justify-between gap-2 text-xs text-muted-foreground">
          <span>{usage ?? ""}</span>
          {w.reset_at && (
            <span data-testid="quota-window-reset">
              {formatResetCountdown(w.reset_at, now)}
            </span>
          )}
        </div>
      )}
    </div>
  );
}
