import { GitPullRequest } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export function IssuePullRequestBadge({
  summary,
  className,
}: {
  summary?: IssuePullRequestSummary | null;
  className?: string;
}) {
  if (!summary || summary.total <= 0) {
    return null;
  }

  return (
    <Badge
      variant="outline"
      className={cn(
        "border-violet-500/30 bg-violet-500/10 text-violet-700 dark:text-violet-300",
        className,
      )}
    >
      <GitPullRequest data-icon="inline-start" />
      {summary.merged}/{summary.total} 已合并
    </Badge>
  );
}
