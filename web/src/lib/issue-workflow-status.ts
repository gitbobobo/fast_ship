export const ISSUE_WORKFLOW_STATUS_LABELS = {
  "": "未设置",
  todo: "待处理",
  in_progress: "开发中",
  done: "已完成",
} as const;

export type IssueWorkflowStatus = keyof typeof ISSUE_WORKFLOW_STATUS_LABELS;

export const ISSUE_WORKFLOW_STATUS_OPTIONS = Object.entries(
  ISSUE_WORKFLOW_STATUS_LABELS,
).map(([value, label]) => ({
  value: value as IssueWorkflowStatus,
  label,
}));

export const ISSUE_WORKFLOW_STATUS_SELECT_OPTIONS = ISSUE_WORKFLOW_STATUS_OPTIONS.filter(
  (option) => option.value !== "",
);

/** 列表接口 workflow_status 参数的合法值：看板「未设置」列（statusValue ""）编码为 unset */
export type BoardWorkflowStatusParam = "todo" | "in_progress" | "done" | "unset";

/**
 * 列状态 → 列表接口 workflow_status 参数：唯一出处（INT-239）。
 * 未设置列编码为 unset，其余列同名透传，未知取值返回 null 由调用方决定兜底。
 */
export function boardWorkflowStatusParamForStatusValue(
  statusValue: string,
): BoardWorkflowStatusParam | null {
  if (!Object.hasOwn(ISSUE_WORKFLOW_STATUS_LABELS, statusValue)) {
    return null;
  }
  return statusValue === "" ? "unset" : (statusValue as BoardWorkflowStatusParam);
}
