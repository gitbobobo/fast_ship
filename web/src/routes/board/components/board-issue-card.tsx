import { Link } from "react-router";
import { useDraggable } from "@dnd-kit/core";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { IssueShipHookBadge } from "@/components/issues/issue-ship-hook-badge";
import { ISSUE_WORKFLOW_STATUS_LABELS } from "@/lib/issue-workflow-status";
import { ISSUE_SOURCE_LABELS } from "@/lib/issue-source";
import { cn } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";

const boardIssueCardClassName =
  "group rounded-md border bg-card p-3 shadow-xs";

function BoardIssueCardContent({ issue }: { issue: Issue }) {
  const status = issue.internal_meta?.workflow_status;

  return (
    <>
      <div className="mb-2 flex items-start gap-2">
        <GripVertical className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground/50" />
        <Link
          to={`/projects/${issue.project_id}/issues/${issue.id}`}
          className="min-w-0 flex-1 text-sm font-medium leading-snug hover:text-primary"
          onPointerDown={(e) => e.stopPropagation()}
          onClick={(e) => e.stopPropagation()}
        >
          <span className="line-clamp-2">{issue.title}</span>
        </Link>
      </div>

      <div className="flex flex-wrap items-center gap-1.5 pl-5">
        <Badge variant="outline" className="text-[10px]">
          {ISSUE_SOURCE_LABELS[issue.source]}
        </Badge>
        <Badge
          variant={issue.state === "open" ? "default" : "secondary"}
          className="text-[10px]"
        >
          {issue.state === "open" ? "Open" : "Closed"}
        </Badge>
        {!!status && (
          <span
            className={cn(
              "inline-flex items-center rounded-full border px-1.5 py-0.5 text-[10px] font-medium",
              status === "todo" &&
                "border-slate-500/20 bg-slate-500/10 text-slate-600 dark:text-slate-400",
              status === "in_progress" &&
                "border-amber-500/20 bg-amber-500/10 text-amber-600 dark:text-amber-400",
              status === "done" &&
                "border-emerald-500/20 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
            )}
          >
            {ISSUE_WORKFLOW_STATUS_LABELS[status]}
          </span>
        )}
        <IssueShipHookBadge hook={issue.ship_hook} className="text-[10px]" />
        {(issue.source === "github"
          ? issue.github?.labels ?? []
          : issue.internal_meta?.labels ?? []
        )
          .slice(0, 2)
          .map((label) => (
            <span
              key={label.name}
              className="rounded-full px-1.5 py-0.5 text-[10px]"
              style={{
                // 将 16 进制颜色与 20（约 12.5% 不透明度）拼接为背景色
                backgroundColor: `#${label.color}20`,
                color: `#${label.color}`,
              }}
            >
              {label.name}
            </span>
          ))}
      </div>

      <div className="mt-2 flex items-center gap-2 pl-5 text-[11px] text-muted-foreground">
        <span className="truncate">@{issue.author.login}</span>
        <span className="shrink-0">·</span>
        <span className="shrink-0">{formatRelativeTime(issue.created_at)}</span>
      </div>
    </>
  );
}

export function BoardIssueCard({ issue }: { issue: Issue }) {
  const { attributes, listeners, setNodeRef, transform, isDragging } =
    useDraggable({
      id: issue.id,
      data: { issue },
    });

  const style = !isDragging && transform
    ? {
        transform: CSS.Translate.toString(transform),
      }
    : undefined;

  return (
    <div
      ref={setNodeRef}
      style={style}
      {...attributes}
      {...listeners}
      className={cn(
        boardIssueCardClassName,
        "cursor-grab touch-none transition-shadow hover:shadow-sm active:cursor-grabbing",
        isDragging && "invisible",
      )}
    >
      <BoardIssueCardContent issue={issue} />
    </div>
  );
}

export function BoardIssueCardOverlay({ issue }: { issue: Issue }) {
  return (
    <div
      className={cn(
        boardIssueCardClassName,
        "pointer-events-none cursor-grabbing",
      )}
    >
      <BoardIssueCardContent issue={issue} />
    </div>
  );
}
