import type { MouseEvent, PointerEvent } from "react";
import { Link } from "react-router";
import { useDraggable } from "@dnd-kit/core";
import { CSS } from "@dnd-kit/utilities";
import { Check, GripVertical } from "lucide-react";
import { GithubIcon } from "@/components/ui/github-icon";
import { IssueShipHookBadge } from "@/components/issues/issue-ship-hook-badge";
import { cn } from "@/lib/utils";
import { formatRelativeTime } from "@/lib/utils/format";
import { useBoardSelection } from "@/routes/board/lib/board-selection-context";

const boardIssueCardClassName =
  "group rounded-md border bg-card p-3 shadow-xs";

// 未读评论用 GitHub 强调蓝 outline，不与选中态 border-primary 抢样式
const unreadOutlineClassName =
  "outline outline-2 outline-github-accent -outline-offset-1";

function hasUnreadComments(issue: Issue) {
  return issue.source === "github" && issue.unread_comments_count > 0;
}

function unreadAriaLabel(issue: Issue) {
  return `${issue.reference} ${issue.title}，有未读评论`;
}

function hasMultiSelectModifier(event: {
  metaKey: boolean;
  ctrlKey: boolean;
}) {
  return event.metaKey || event.ctrlKey;
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

function BoardIssueCardContent({
  issue,
  multiSelectMode,
  showCheckbox,
  selected,
  onSelectIssue,
}: {
  issue: Issue;
  multiSelectMode: boolean;
  showCheckbox: boolean;
  selected: boolean;
  onSelectIssue?: (
    issue: Issue,
    modifiers: { shiftKey: boolean },
  ) => void;
}) {
  const titleClassName =
    "min-w-0 flex-1 line-clamp-2 text-sm font-medium leading-snug";

  return (
    <>
      <div className="mb-2 flex items-start gap-2">
        {showCheckbox ? (
          <span
            role="checkbox"
            aria-checked={selected}
            aria-label={`选择 ${issue.reference}`}
            data-testid="board-issue-checkbox"
            className={cn(
              "mt-0.5 flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-[3px] border transition-colors",
              selected
                ? "border-primary bg-primary text-primary-foreground"
                : "border-muted-foreground/50 bg-background",
            )}
          >
            {selected && <Check className="h-3 w-3" strokeWidth={3} />}
          </span>
        ) : (
          <GripVertical className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground/50" />
        )}
        {multiSelectMode ? (
          // 模式内标题渲染成非链接，避免中键/Cmd 点开新标签，点击由整卡切换勾选
          <span className={titleClassName}>
            <IssueReferenceTitle issue={issue} />
          </span>
        ) : (
          <Link
            to={`/projects/${issue.project_id}/issues/${issue.id}`}
            className={cn(titleClassName, "hover:text-primary")}
            onPointerDown={(e) => e.stopPropagation()}
            onClick={(e) => {
              e.stopPropagation();
              if (hasMultiSelectModifier(e)) {
                // Cmd/Ctrl+点链接默认会新开标签，必须挡住并进入多选
                e.preventDefault();
                onSelectIssue?.(issue, { shiftKey: false });
              }
            }}
          >
            <IssueReferenceTitle issue={issue} />
          </Link>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-1.5 pl-5">
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
  const selection = useBoardSelection();
  const multiSelectMode = selection?.multiSelectMode ?? false;
  const selectPreview = selection?.selectPreview ?? false;
  const showSelectAffordance = multiSelectMode || selectPreview;
  const selected = selection?.selectedIssueIds.has(issue.id) ?? false;
  const selectIssue = selection?.selectIssue;

  const { attributes, listeners, setNodeRef, transform, isDragging } =
    useDraggable({
      id: issue.id,
      data: { issue },
      disabled: showSelectAffordance,
    });

  const style = !isDragging && transform
    ? {
        transform: CSS.Translate.toString(transform),
      }
    : undefined;

  const { onPointerDown: dragPointerDown, ...otherDragListeners } =
    listeners ?? {};

  const handlePointerDown = (event: PointerEvent<HTMLDivElement>) => {
    if (!multiSelectMode && hasMultiSelectModifier(event)) {
      event.preventDefault();
      return;
    }
    dragPointerDown?.(event);
  };

  const handleClick = (event: MouseEvent<HTMLDivElement>) => {
    if (multiSelectMode) {
      // 模式内整卡（含 checkbox、标题）只切换勾选，Cmd/Ctrl 与普通点击一致
      selectIssue?.(issue, { shiftKey: event.shiftKey });
      return;
    }
    if (hasMultiSelectModifier(event)) {
      event.preventDefault();
      selectIssue?.(issue, { shiftKey: false });
    }
  };

  return (
    <div
      ref={setNodeRef}
      style={style}
      {...attributes}
      {...otherDragListeners}
      onPointerDown={handlePointerDown}
      onClick={handleClick}
      className={cn(
        boardIssueCardClassName,
        showSelectAffordance
          ? "cursor-pointer transition-shadow hover:shadow-sm"
          : "cursor-grab touch-none transition-shadow hover:shadow-sm active:cursor-grabbing",
        hasUnreadComments(issue) && unreadOutlineClassName,
        multiSelectMode && selected && "border-primary ring-1 ring-primary/30",
        selectPreview && "ring-1 ring-primary/20",
        isDragging && "invisible",
      )}
      aria-label={hasUnreadComments(issue) ? unreadAriaLabel(issue) : undefined}
    >
      <BoardIssueCardContent
        issue={issue}
        multiSelectMode={multiSelectMode}
        showCheckbox={showSelectAffordance}
        selected={selected}
        onSelectIssue={selectIssue}
      />
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
      <BoardIssueCardContent
        issue={issue}
        multiSelectMode={false}
        showCheckbox={false}
        selected={false}
      />
    </div>
  );
}
