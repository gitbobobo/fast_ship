import { useDroppable } from "@dnd-kit/core";
import { cn } from "@/lib/utils";
import { COLUMNS, type ColumnId } from "@/routes/board/lib/utils";
import { BoardIssueCard } from "./board-issue-card";
import { CloseAllDoneButton } from "./close-all-done-button";

export function BoardColumn({
  columnId,
  issues,
}: {
  columnId: ColumnId;
  issues: Issue[];
}) {
  const column = COLUMNS.find((c) => c.id === columnId)!;

  const { setNodeRef, isOver } = useDroppable({
    id: column.id,
    data: { type: "column", column },
  });

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
            {issues.length}
          </span>
        </div>
        {column.id === "done" && issues.length > 0 && (
          <CloseAllDoneButton issues={issues} />
        )}
      </div>

      <div className="flex-1 space-y-2 overflow-y-auto p-2.5">
        {issues.length === 0 ? (
          <div className="flex h-24 items-center justify-center rounded-md border border-dashed text-xs text-muted-foreground">
            暂无问题
          </div>
        ) : (
          issues.map((issue) => <BoardIssueCard key={issue.id} issue={issue} />)
        )}
      </div>
    </div>
  );
}
