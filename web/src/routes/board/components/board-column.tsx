import { useEffect, useMemo, useRef, useState } from "react";
import { useDroppable } from "@dnd-kit/core";
import { Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  useInfiniteBoardIssues,
  useIssueFilterOptions,
} from "@/lib/hooks/use-issues";
import { COLUMNS, type ColumnId } from "@/routes/board/lib/utils";
import { getColumnScrollKey } from "@/routes/board/lib/board-scroll";
import { usePersistedScroll } from "@/lib/hooks/use-persisted-scroll";
import { BoardIssueCard } from "./board-issue-card";
import {
  BoardColumnFilter,
  type BoardColumnFilterValue,
  DEFAULT_BOARD_COLUMN_FILTER,
} from "./board-column-filter";
import { CloseAllDoneButton } from "./close-all-done-button";

export function BoardColumn({
  columnId,
  projectId,
  onColumnIssuesChange,
}: {
  columnId: ColumnId;
  projectId: string;
  onColumnIssuesChange?: (columnId: ColumnId, issues: Issue[]) => void;
}) {
  const column = COLUMNS.find((c) => c.id === columnId)!;

  const [filter, setFilter] = useState<BoardColumnFilterValue>(
    DEFAULT_BOARD_COLUMN_FILTER,
  );

  useEffect(() => {
    setFilter(DEFAULT_BOARD_COLUMN_FILTER);
  }, [projectId]);

  const { data: filterOptionsData } = useIssueFilterOptions(projectId);
  const labels = filterOptionsData?.labels ?? [];

  const { setNodeRef, isOver } = useDroppable({
    id: column.id,
    data: { type: "column", column },
  });

  const {
    data,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
    isLoading,
  } = useInfiniteBoardIssues(projectId, column.statusValue, {
    label: filter.label || undefined,
    source: filter.source === "all" ? undefined : filter.source,
  });

  const issues = useMemo(
    () => data?.pages.flatMap((page) => page.items) ?? [],
    [data],
  );

  const total = data?.pages[0]?.total ?? 0;

  const sentinelRef = useRef<HTMLDivElement>(null);
  const columnScrollKey = getColumnScrollKey(projectId, columnId, filter);
  const listScrollRef = usePersistedScroll<HTMLDivElement>(columnScrollKey, {
    ready: !isLoading,
  });

  useEffect(() => {
    if (!hasNextPage) return;
    const el = sentinelRef.current;
    if (!el) return;

    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry.isIntersecting && hasNextPage && !isFetchingNextPage) {
          fetchNextPage();
        }
      },
      { rootMargin: "100px" },
    );
    observer.observe(el);
    return () => observer.disconnect();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  // issues 数组变化时（含筛选、翻页）上报给多选 hook，用于剪枝消失的勾选 id。
  // isLoading 期间不上报：列筛选变化时 queryKey 变了，TanStack Query 会先把
  // data 置为 undefined（issues 为空数组），此时上报会把该列勾选全部清掉，
  // 等真实数据到达后再上报，剪枝就只作用于真正消失的 id。
  // 翻页的 isFetchingNextPage 不影响 isLoading，无需在此处理。
  useEffect(() => {
    if (isLoading) return;
    onColumnIssuesChange?.(columnId, issues);
  }, [columnId, issues, onColumnIssuesChange, isLoading]);

  return (
    <div
      ref={setNodeRef}
      className={cn(
        "flex min-w-[280px] max-w-[320px] flex-1 flex-col rounded-lg border bg-muted/20 transition-colors",
        isOver && "bg-muted/50 ring-2 ring-primary/20",
      )}
    >
      <div className="flex items-center justify-between border-b px-3 py-2.5">
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-semibold">{column.label}</h3>
          <span className="inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-muted px-1.5 text-xs font-medium text-muted-foreground">
            {total}
          </span>
        </div>
        <div className="flex items-center gap-1">
          <BoardColumnFilter
            labels={labels}
            value={filter}
            onChange={setFilter}
          />
          {column.id === "done" && !isLoading && total > 0 && (
            <CloseAllDoneButton projectId={projectId} />
          )}
        </div>
      </div>

      <div
        ref={listScrollRef}
        className="flex-1 space-y-2 overflow-y-auto p-2.5"
      >
        {isLoading ? (
          Array.from({ length: 3 }).map((_, i) => (
            <div
              key={i}
              className="h-24 animate-pulse rounded-md border bg-muted/50"
            />
          ))
        ) : issues.length === 0 ? (
          <div className="flex h-24 items-center justify-center rounded-md border border-dashed text-xs text-muted-foreground">
            暂无问题
          </div>
        ) : (
          <>
            {issues.map((issue) => (
              <BoardIssueCard key={issue.id} issue={issue} />
            ))}
            {hasNextPage && (
              <div ref={sentinelRef} className="flex justify-center py-2">
                {isFetchingNextPage && (
                  <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                )}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}
