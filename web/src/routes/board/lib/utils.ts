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

export function getColumnIdByStatus(status: string): ColumnId {
  const col = COLUMNS.find((c) => c.statusValue === status);
  return col ? col.id : "unset";
}

export function getActiveProjectId(
  projects: Project[],
  selectedId: string,
  urlId: string | null,
): string {
  if (projects.some((p) => p.id === selectedId)) {
    return selectedId;
  }
  if (urlId && projects.some((p) => p.id === urlId)) {
    return urlId;
  }
  return projects[0]?.id ?? "";
}
