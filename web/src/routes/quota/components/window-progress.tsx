import { useNow } from "@/lib/hooks/use-now";
import {
  formatQuotaAmount,
  formatResetCountdown,
  quotaUsageTone,
  type QuotaUsageTone,
} from "@/lib/quota";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

// >=95 的窗口多半在等重置而不是出了错：进度条与百分比用中性色，
// 红色只留给「需处理」横幅；80~95 仍用琥珀色提示快见底。
const BAR_CLASS: Record<QuotaUsageTone, string> = {
  normal: "bg-primary",
  warning: "bg-amber-500 dark:bg-amber-400",
  danger: "bg-muted-foreground/40",
};

const TEXT_CLASS: Record<QuotaUsageTone, string> = {
  normal: "text-foreground",
  warning: "text-amber-700 dark:text-amber-400",
  danger: "text-muted-foreground",
};

/**
 * 单个额度窗口压成一行：名称（自适应，长名截断） | 进度条（固定宽） |
 * 已用% | 重置倒计时（每 30s 自走）；used/limit 明细收进 tooltip，
 * trigger 可聚焦（tabIndex=0 + 窗口名与明细做可访问名称），键盘可达。
 * 受限窗口（限制着账号有效剩余）整行加粗。
 */
export function QuotaWindowProgress({
  window: w,
  limited = false,
}: {
  window: QuotaWindow;
  /** 该窗口是当前池的受限窗口（used_percent 最大者） */
  limited?: boolean;
}) {
  const now = useNow(30_000, Boolean(w.reset_at));
  const tone = quotaUsageTone(w.used_percent);
  const percent = w.used_percent;
  const width = percent == null ? 0 : Math.min(100, Math.max(0, percent));
  const detail =
    w.used != null && w.limit != null
      ? `${formatQuotaAmount(w.used, w.unit)} / ${formatQuotaAmount(w.limit, w.unit)}`
      : w.limit != null
        ? `限额 ${formatQuotaAmount(w.limit, w.unit)}`
        : null;

  const row = (
    <>
      <span
        className={cn(
          "min-w-0 flex-1 truncate",
          limited ? "text-foreground" : "text-muted-foreground",
        )}
      >
        {w.label}
      </span>
      <div
        role="progressbar"
        aria-label={w.label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent ?? undefined}
        className="h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-muted"
      >
        <div
          data-testid="quota-window-bar"
          className={cn("h-full rounded-full transition-[width]", BAR_CLASS[tone])}
          style={{ width: `${width}%` }}
        />
      </div>
      <span
        data-testid="quota-window-percent"
        className={cn("w-10 shrink-0 text-right tabular-nums", TEXT_CLASS[tone])}
      >
        {percent == null ? "—" : formatQuotaAmount(percent, "percent")}
      </span>
      <span
        data-testid="quota-window-reset"
        className="shrink-0 text-right text-muted-foreground"
      >
        {w.reset_at ? formatResetCountdown(w.reset_at, now) : ""}
      </span>
    </>
  );

  const rowClass = cn(
    "flex items-center gap-2 text-xs",
    limited && "font-medium",
  );

  const body = detail ? (
    <Tooltip>
      <TooltipTrigger
        render={
          <div
            className={cn(
              rowClass,
              "rounded-sm focus-visible:outline-1 focus-visible:outline-ring",
            )}
            tabIndex={0}
            aria-label={`${w.label}：${detail}`}
          />
        }
      >
        {row}
      </TooltipTrigger>
      <TooltipContent>{detail}</TooltipContent>
    </Tooltip>
  ) : (
    <div className={rowClass}>{row}</div>
  );

  return (
    <div data-testid="quota-window" data-tone={tone}>
      {body}
    </div>
  );
}
