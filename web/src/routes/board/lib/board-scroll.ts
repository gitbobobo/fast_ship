import type { ColumnId } from "@/routes/board/lib/utils";
import type { BoardColumnFilterValue } from "@/routes/board/components/board-column-filter";

export function getColumnScrollKey(
  projectId: string,
  columnId: ColumnId,
  filter: BoardColumnFilterValue,
) {
  return `board-col:${projectId}:${columnId}:${filter.label}:${filter.source}`;
}
