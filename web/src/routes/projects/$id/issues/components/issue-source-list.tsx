import { useEffect, useRef } from "react";
import { ListX, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import type { IssueSourceEntry } from "./use-issue-source";

function UnreadDot() {
  return (
    <span
      role="img"
      aria-label="有未读评论"
      className="h-1.5 w-1.5 shrink-0 rounded-full bg-github-accent"
    />
  );
}

export interface IssueSourceListProps {
  entries: IssueSourceEntry[];
  currentIssueId: string;
  onSelect: (issue: Issue) => void;
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  onLoadMore: () => void;
  /** 内部滚动容器额外类名（如最大高度） */
  className?: string;
}

/**
 * 详情页来源列表：编号、标题、状态与未读标记；高亮当前问题并自动滚动到可见。
 * 「已不符合来源条件」的保留项用弱化样式加小标记。桌面左栏与移动抽屉共用本组件，
 * 数据与翻页控制器由页面级的 useIssueSourceList 提供，本组件不持有副作用。
 */
export function IssueSourceList({
  entries,
  currentIssueId,
  onSelect,
  isLoading,
  isError,
  onRetry,
  hasNextPage,
  isFetchingNextPage,
  onLoadMore,
  className,
}: IssueSourceListProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);

  // 自动滚动：当前项或列表变化后把当前项滚进可视区
  useEffect(() => {
    if (!currentIssueId) {
      return;
    }
    containerRef.current
      ?.querySelector(`[data-issue-id="${CSS.escape(currentIssueId)}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [currentIssueId, entries.length]);

  useEffect(() => {
    if (!hasNextPage || isFetchingNextPage || isError) {
      return;
    }
    const el = sentinelRef.current;
    if (!el) {
      return;
    }
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting) {
          onLoadMore();
        }
      },
      { rootMargin: "100px" },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [hasNextPage, isFetchingNextPage, isError, onLoadMore]);

  if (isLoading) {
    return (
      <div className={cn("flex items-center justify-center gap-2 p-6 text-sm text-muted-foreground", className)}>
        <Loader2 className="h-4 w-4 animate-spin" />
        加载中…
      </div>
    );
  }

  // 有已加载条目时分页/后台刷新失败不整替列表：只在内联提供重试，保住用户已看到的内容
  if (isError && entries.length === 0) {
    return (
      <div className={cn("flex flex-col items-center gap-2 p-6 text-sm text-muted-foreground", className)}>
        <span>来源列表加载失败</span>
        <button
          type="button"
          onClick={onRetry}
          className="rounded-md border px-2.5 py-1 text-xs font-medium hover:bg-muted"
        >
          重试
        </button>
      </div>
    );
  }

  if (entries.length === 0) {
    return (
      <div className={cn("flex flex-col items-center gap-2 p-6 text-sm text-muted-foreground", className)}>
        <ListX className="h-5 w-5" />
        <span>来源列表暂无问题</span>
      </div>
    );
  }

  return (
    <div ref={containerRef} data-testid="issue-source-list" className={className}>
      <ul className="space-y-0.5 p-1.5">
        {entries.map((entry) => {
          const isCurrent = entry.issue.id === currentIssueId;
          const hasUnread =
            entry.issue.source === "github" &&
            entry.issue.unread_comments_count > 0;
          return (
            <li key={entry.issue.id}>
              <button
                type="button"
                data-issue-id={entry.issue.id}
                aria-current={isCurrent ? "true" : undefined}
                onClick={() => onSelect(entry.issue)}
                title={entry.stale ? "已不符合来源条件，仅保留在此处" : undefined}
                className={cn(
                  "flex w-full items-start gap-2 rounded-lg border-l-2 px-2 py-1.5 text-left text-sm transition-colors",
                  entry.stale
                    ? "border-transparent text-muted-foreground/70 hover:bg-muted/50"
                    : "border-transparent text-foreground/90 hover:bg-muted",
                  isCurrent &&
                    "border-primary bg-accent font-medium text-foreground hover:bg-accent",
                )}
              >
                <span className="mt-0.5 flex w-3 shrink-0 justify-center">
                  {hasUnread ? <UnreadDot /> : null}
                </span>
                <span className="min-w-0 flex-1">
                  <span
                    className={cn(
                      "block font-mono text-xs text-muted-foreground",
                      entry.stale && "opacity-60",
                      isCurrent && "font-semibold text-primary",
                    )}
                  >
                    {entry.issue.reference}
                  </span>
                  <span className="mt-0.5 line-clamp-2 block leading-snug">
                    {entry.issue.title}
                  </span>
                  {entry.stale && (
                    <span className="mt-1 inline-flex items-center rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
                      已不符合来源条件
                    </span>
                  )}
                </span>
                {/* stale 行的快照状态可能已过期（如同屏详情已 CLOSED），不再展示胶囊 */}
                {!entry.stale && (
                  <span
                    className={cn(
                      "mt-0.5 shrink-0 rounded-full border px-1.5 py-0.5 text-[10px] font-semibold uppercase",
                      entry.issue.state === "open"
                        ? "border-emerald-500/20 bg-emerald-500/10 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/15 dark:text-emerald-300"
                        : "border-rose-500/20 bg-rose-500/10 text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/15 dark:text-rose-300",
                    )}
                  >
                    {entry.issue.state === "open" ? "Open" : "Closed"}
                  </span>
                )}
              </button>
            </li>
          );
        })}
      </ul>
      {(hasNextPage || isError) && (
        <div ref={sentinelRef} className="flex justify-center py-2">
          {isError ? (
            <button
              type="button"
              onClick={onRetry}
              className="rounded-md border px-2.5 py-1 text-xs font-medium hover:bg-muted"
            >
              {hasNextPage ? "加载更多失败，点击重试" : "刷新失败，点击重试"}
            </button>
          ) : (
            isFetchingNextPage && (
              <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            )
          )}
        </div>
      )}
    </div>
  );
}
