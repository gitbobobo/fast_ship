import { useNow } from "@/lib/hooks/use-now";
import {
  formatQuotaAmount,
  formatResetCountdown,
  quotaUsageTone,
  type QuotaUsageTone,
} from "@/lib/quota";
import { cn } from "@/lib/utils";

// 配色按「还剩多少」而不是「是不是报错」：
// - 用尽（100%）是「等重置」，用中性色，别把它读成故障；
// - 95~99% 还没用尽但马上就要用尽，仍用琥珀色提示（否则中性色会被读成「空了」）；
// - 红色只留给「需处理」横幅，进度条永远不红。
const BAR_CLASS: Record<QuotaUsageTone, string> = {
  normal: "bg-primary",
  warning: "bg-amber-500 dark:bg-amber-400",
  danger: "bg-amber-500 dark:bg-amber-400",
};

const TEXT_CLASS: Record<QuotaUsageTone, string> = {
  normal: "text-foreground",
  warning: "text-amber-700 dark:text-amber-400",
  danger: "text-amber-700 dark:text-amber-400",
};

// 用尽窗口专用的中性配色。
const EXHAUSTED_BAR_CLASS = "bg-muted-foreground/60";
const EXHAUSTED_TEXT_CLASS = "text-muted-foreground";

/**
 * 单个额度窗口压成一行：名称（自适应，长名截断） | 进度条（固定宽） |
 * 已用% | 重置倒计时（每 30s 自走）。used/limit 明细不在行内展示，
 * 由卡片级悬浮面板统一承担；受限窗口（限制着账号有效剩余）整行加粗。
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
  const exhausted = percent != null && percent >= 100;
  const width = percent == null ? 0 : Math.min(100, Math.max(0, percent));

  return (
    <div data-testid="quota-window" data-tone={tone}>
      <div
        className={cn(
          "flex items-center gap-2 text-xs",
          limited && "font-medium",
        )}
      >
        <span
          className={cn(
            "min-w-0 max-w-[45%] shrink-0 truncate",
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
          className="h-1.5 min-w-8 flex-1 overflow-hidden rounded-full bg-muted"
        >
          <div
            data-testid="quota-window-bar"
            className={cn(
              "h-full rounded-full transition-[width]",
              exhausted ? EXHAUSTED_BAR_CLASS : BAR_CLASS[tone],
            )}
            style={{ width: `${width}%` }}
          />
        </div>
        <span
          data-testid="quota-window-percent"
          className={cn(
            "w-10 shrink-0 text-right tabular-nums",
            exhausted ? EXHAUSTED_TEXT_CLASS : TEXT_CLASS[tone],
          )}
        >
          {percent == null ? "—" : formatQuotaAmount(percent, "percent")}
        </span>
        <span
          data-testid="quota-window-reset"
          className="shrink-0 text-right text-muted-foreground"
        >
          {w.reset_at ? formatResetCountdown(w.reset_at, now) : ""}
        </span>
      </div>
    </div>
  );
}
