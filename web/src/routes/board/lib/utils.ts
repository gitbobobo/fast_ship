import {
  boardWorkflowStatusParamForStatusValue,
  type BoardWorkflowStatusParam,
} from "@/lib/issue-workflow-status";

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

/** 列状态 → URL 参数值：转发 lib 唯一实现，保持看板调用方路径不变 */
export function getBoardWorkflowStatusParam(statusValue: string): BoardWorkflowStatusParam | null {
  return boardWorkflowStatusParamForStatusValue(statusValue);
}
