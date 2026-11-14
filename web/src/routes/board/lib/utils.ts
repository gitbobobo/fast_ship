export const COLUMNS = [
  { id: "unset", label: "未设置", statusValue: "" as const },
  { id: "todo", label: "待处理", statusValue: "todo" as const },
  { id: "in_progress", label: "开发中", statusValue: "in_progress" as const },
  { id: "done", label: "已完成", statusValue: "done" as const },
] as const;

export type ColumnId = (typeof COLUMNS)[number]["id"];

export function getColumnStatusValue(columnId: ColumnId): (typeof COLUMNS)[number]["statusValue"] {
  const col = COLUMNS.find((c) => c.id === columnId);
  return col ? col.statusValue : "";
}

/** 列状态 → URL 参数值：其余列同名，「未设置」列编码为 unset */
export function getBoardWorkflowStatusParam(statusValue: string): "todo" | "in_progress" | "done" | "unset" | null {
  const column = COLUMNS.find((c) => c.statusValue === statusValue);
  if (!column) {
    return null;
  }
  return column.id === "unset" ? "unset" : column.statusValue;
}
