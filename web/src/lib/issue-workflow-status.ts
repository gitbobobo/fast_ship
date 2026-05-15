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
