export const ISSUE_SOURCE_LABELS = {
  internal: "内部",
  github: "GitHub",
} as const;

export type IssueSource = keyof typeof ISSUE_SOURCE_LABELS;

export const ISSUE_SOURCE_FILTER_OPTIONS = [
  { value: "all", label: "全部" },
  { value: "internal", label: "内部问题" },
  { value: "github", label: "GitHub 问题" },
] as const;

export type IssueSourceFilter =
  (typeof ISSUE_SOURCE_FILTER_OPTIONS)[number]["value"];
