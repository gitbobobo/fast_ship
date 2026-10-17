import { Link } from "react-router";
import { Clock, Sparkles } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useRestoreRecommendation } from "@/lib/hooks/use-recommendations";
import { ISSUE_WORKFLOW_STATUS_LABELS } from "@/lib/issue-workflow-status";
import { cn } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";
import { toast } from "sonner";

const RECOMMENDATION_PRIORITY_META: Record<
  IssueRecommendation["priority"],
  { label: string; className: string }
> = {
  high: {
    label: "高优先级",
    className: "border-destructive/30 bg-destructive/10 text-destructive",
  },
  medium: {
    label: "中优先级",
    className:
      "border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400",
  },
  low: {
    label: "低优先级",
    className:
      "border-slate-500/20 bg-slate-500/10 text-slate-600 dark:text-slate-400",
  },
};

function RecommendationDependencyLink({
  dep,
}: {
  dep: RecommendationDependency;
}) {
  const closed = dep.state === "closed";
  const done = closed || dep.workflow_status === "done";
  const label = closed
    ? "已关闭"
    : ISSUE_WORKFLOW_STATUS_LABELS[dep.workflow_status];

  const statusClass = done
    ? "text-muted-foreground"
    : dep.workflow_status === "in_progress"
      ? "text-amber-600 dark:text-amber-400"
      : "text-slate-600 dark:text-slate-400";

  return (
    <Link
      to={`/projects/${dep.project_id}/issues/${dep.issue_id}`}
      className="inline-flex max-w-full items-center gap-1.5 rounded-md border bg-background px-1.5 py-0.5 text-xs hover:bg-muted"
    >
      <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
        {dep.reference}
      </span>
      <span
        className={cn(
          "min-w-0 truncate",
          done && "text-muted-foreground line-through",
        )}
      >
        {dep.title}
      </span>
      <span className={cn("shrink-0", statusClass)}>{label}</span>
    </Link>
  );
}

/** 推荐横幅：被推荐的问题进入详情页时显示在正文卡片上方，与正文同级呈现「为什么现在做这条」。 */
function RecommendationBanner({
  recommendation,
}: {
  recommendation: IssueRecommendation;
}) {
  const priority = RECOMMENDATION_PRIORITY_META[recommendation.priority];
  return (
    <section
      aria-label="推荐"
      className="rounded-2xl border border-violet-500/25 bg-violet-500/5 px-4 py-3.5"
    >
      <div className="flex items-center gap-2">
        <Sparkles className="h-4 w-4 text-violet-500" />
        <span className="text-sm font-semibold">推荐任务</span>
        <span
          className={cn(
            "inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium",
            priority.className,
          )}
        >
          {priority.label}
        </span>
      </div>
      <p className="mt-2 text-sm leading-relaxed whitespace-pre-wrap text-foreground/90">
        {recommendation.reason}
      </p>
      {recommendation.dependencies.length > 0 && (
        <div className="mt-2.5 flex items-start gap-2">
          <span className="shrink-0 py-0.5 text-xs text-muted-foreground">
            前置
          </span>
          <div className="flex min-w-0 flex-wrap gap-1.5">
            {recommendation.dependencies.map((dep) => (
              <RecommendationDependencyLink key={dep.issue_id} dep={dep} />
            ))}
          </div>
        </div>
      )}
    </section>
  );
}

/** 已延后推荐横幅：低调灰底，展示延后时间与备注，可一键移回推荐。 */
function DeferredRecommendationBanner({
  recommendation,
}: {
  recommendation: IssueRecommendation;
}) {
  const restoreRecommendation = useRestoreRecommendation();

  const handleRestore = async () => {
    try {
      await restoreRecommendation.mutateAsync(recommendation.issue.id);
      toast.success("已移回推荐");
    } catch {
      toast.error("移回推荐失败");
    }
  };

  return (
    <section
      aria-label="已延后推荐"
      className="rounded-2xl border bg-muted/40 px-4 py-3.5"
    >
      <div className="flex items-center gap-2">
        <Clock className="h-4 w-4 text-muted-foreground" />
        <span className="text-sm font-medium text-muted-foreground">
          已延后
          {recommendation.deferred_at
            ? ` · ${formatRelativeTime(recommendation.deferred_at)}`
            : ""}
        </span>
        <Button
          variant="outline"
          size="sm"
          className="ml-auto h-7 text-xs"
          onClick={() => void handleRestore()}
          disabled={restoreRecommendation.isPending}
        >
          {restoreRecommendation.isPending ? "移回中..." : "移回推荐"}
        </Button>
      </div>
      {recommendation.defer_note ? (
        <p className="mt-1.5 text-sm leading-relaxed whitespace-pre-wrap text-foreground/80">
          {recommendation.defer_note}
        </p>
      ) : null}
    </section>
  );
}

/** 按 recommendation.status 自动选渲染：active → 推荐横幅；deferred → 已延后横幅。 */
export function IssueRecommendationBanner({
  recommendation,
}: {
  recommendation: IssueRecommendation;
}) {
  return recommendation.status === "deferred" ? (
    <DeferredRecommendationBanner recommendation={recommendation} />
  ) : (
    <RecommendationBanner recommendation={recommendation} />
  );
}
