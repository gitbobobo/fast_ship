import { Link } from "react-router";
import { useDraggable } from "@dnd-kit/core";
import { CSS } from "@dnd-kit/utilities";
import { GripVertical } from "lucide-react";
import { GithubIcon } from "@/components/ui/github-icon";
import { IssuePullRequestBadge } from "@/components/issues/issue-pull-request-badge";
import { IssueShipHookBadge } from "@/components/issues/issue-ship-hook-badge";
import { cn } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";

const boardIssueCardClassName =
  "group rounded-md border bg-card p-3 shadow-xs";

// 未读评论用 GitHub 强调蓝 outline，不与拖拽态样式冲突
const unreadOutlineClassName =
  "outline outline-2 outline-github-accent -outline-offset-1";

function hasUnreadComments(issue: Issue) {
  return issue.source === "github" && issue.unread_comments_count > 0;
}

function unreadAriaLabel(issue: Issue) {
  return `${issue.reference} ${issue.title}，有未读评论`;
}

function IssueReferenceTitle({ issue }: { issue: Issue }) {
  return (
    <>
      <span className="font-mono text-xs text-muted-foreground">
        {issue.reference}
      </span>{" "}
      {issue.title}
    </>
  );
}

function BoardIssueCardContent({ issue }: { issue: Issue }) {
  const titleClassName =
    "min-w-0 flex-1 line-clamp-2 text-sm font-medium leading-snug";

  return (
    <>
      <div className="mb-2 flex items-start gap-2">
        <GripVertical className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground/50" />
        <Link
          to={`/projects/${issue.project_id}/issues/${issue.id}`}
          className={cn(titleClassName, "hover:text-primary")}
          onPointerDown={(e) => e.stopPropagation()}
          onClick={(e) => e.stopPropagation()}
        >
          <IssueReferenceTitle issue={issue} />
        </Link>
      </div>

      <div className="flex flex-wrap items-center gap-1.5 pl-5">
        <IssueShipHookBadge hook={issue.ship_hook} className="text-[10px]" />
        <IssuePullRequestBadge
          summary={issue.pull_request_summary}
          className="text-[10px]"
        />
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
        {issue.source === "github" && (
          <GithubIcon className="h-3 w-3 shrink-0" />
        )}
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
        hasUnreadComments(issue) && unreadOutlineClassName,
        isDragging && "invisible",
      )}
      aria-label={hasUnreadComments(issue) ? unreadAriaLabel(issue) : undefined}
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
        hasUnreadComments(issue) && unreadOutlineClassName,
        "pointer-events-none cursor-grabbing",
      )}
      aria-label={hasUnreadComments(issue) ? unreadAriaLabel(issue) : undefined}
    >
      <BoardIssueCardContent issue={issue} />
    </div>
  );
}
