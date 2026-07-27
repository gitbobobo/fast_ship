import { useEffect, useMemo, useRef, useState } from "react";
import { useDroppable } from "@dnd-kit/core";
import { Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  useInfiniteBoardIssues,
  useIssueFilterOptions,
} from "@/lib/hooks/use-issues";
import { COLUMNS, type ColumnId } from "@/routes/board/lib/utils";
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
}: {
  columnId: ColumnId;
  projectId: string;
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

      <div className="flex-1 space-y-2 overflow-y-auto p-2.5">
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
